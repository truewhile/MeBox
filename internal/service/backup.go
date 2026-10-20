// Package service — database hot backup and restore.
//
// Backup files are stored under {data_dir}/backups/ with a timestamp suffix.
// The admin UI can list / download / delete / restore them.
//
// 两种方言的备份形态不同：
//   - SQLite：用 "VACUUM INTO"（3.27.0 起可用）产出一个一致的库文件，
//     扩展名 .db；恢复先暂存，在下次启动打开数据库前替换文件。
//   - PostgreSQL：没有单文件语义，用逻辑备份（见 backup_postgres.go），
//     扩展名 .json.gz；恢复是在单个事务内重建全部表。
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/database"
)

// BackupService manages database backups.
type BackupService struct {
	cfg *config.Config
	log *zap.Logger
	db  *gorm.DB
}

// NewBackupService is the constructor.
func NewBackupService(cfg *config.Config, log *zap.Logger, db *gorm.DB) *BackupService {
	return &BackupService{cfg: cfg, log: log, db: db}
}

// BackupInfo describes one stored backup file.
type BackupInfo struct {
	Filename  string    `json:"filename"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	Automatic bool      `json:"automatic"`
}

func (b *BackupService) backupDir() string {
	return filepath.Join(b.cfg.App.DataDir, "backups")
}

func (b *BackupService) backupFilePath(filename string) (string, error) {
	if !isValidBackupFilename(filename) {
		return "", errors.New("invalid filename")
	}
	dir, err := filepath.Abs(b.backupDir())
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(dir, filename))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", errors.New("invalid filename")
	}
	return path, nil
}

// ResolvePath returns the absolute path of a listed backup after applying the
// same filename and directory checks used by restore and delete operations.
func (b *BackupService) ResolvePath(filename string) (string, error) {
	path, err := b.backupFilePath(filename)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("backup is not a regular file")
	}
	return path, nil
}

// isPostgres reports whether the service is backed by PostgreSQL.
//
// PostgreSQL 不支持 SQLite 的 "VACUUM INTO"，按文件替换恢复也会写错目标，
// 因此备份/恢复必须按方言分支，不能共用一条路径。
func (b *BackupService) isPostgres() bool {
	return b.db != nil && b.db.Dialector != nil && b.db.Dialector.Name() == "postgres"
}

// Create produces a new hot backup. SQLite uses "VACUUM INTO"; PostgreSQL uses
// a logical dump.
func (b *BackupService) Create(ctx context.Context) (*BackupInfo, error) {
	return b.create(ctx, false)
}

func (b *BackupService) CreateAutomatic(ctx context.Context) (*BackupInfo, error) {
	return b.create(ctx, true)
}

func (b *BackupService) create(ctx context.Context, automatic bool) (*BackupInfo, error) {
	dir := b.backupDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	now := time.Now()
	ts := fmt.Sprintf("%s_%09d", now.Format("20060102_150405"), now.Nanosecond())
	if automatic {
		ts = "auto_" + ts
	}

	var (
		dst string
		err error
	)
	if b.isPostgres() {
		dst, err = b.createPostgresBackup(ctx, ts)
	} else {
		dst, err = b.createSQLiteBackup(ctx, ts)
	}
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(dst)
	if err != nil {
		return nil, err
	}
	b.log.Info("backup created", zap.String("path", dst), zap.Int64("size", stat.Size()))
	return &BackupInfo{
		Filename:  filepath.Base(dst),
		Path:      dst,
		Size:      stat.Size(),
		CreatedAt: stat.ModTime(),
		Automatic: automatic,
	}, nil
}

func (b *BackupService) createSQLiteBackup(ctx context.Context, stamp string) (string, error) {
	dst := filepath.Join(b.backupDir(), fmt.Sprintf("mebox_%s.db", stamp))
	tmp := dst + ".tmp"
	defer os.Remove(tmp)
	// Publish only completed backups, with credentials protected by file mode.
	if err := b.db.WithContext(ctx).Exec("VACUUM INTO ?", tmp).Error; err != nil {
		return "", fmt.Errorf("VACUUM INTO failed: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// List returns every backup file in the backup directory, newest first.
func (b *BackupService) List() ([]BackupInfo, error) {
	dir := b.backupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]BackupInfo, 0, len(entries))
	for _, e := range entries {
		if !e.Type().IsRegular() || !isBackupFilename(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{
			Filename:  e.Name(),
			Path:      filepath.Join(dir, e.Name()),
			Size:      info.Size(),
			CreatedAt: info.ModTime(),
			Automatic: strings.HasPrefix(e.Name(), "mebox_auto_"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// Prune keeps maxCount automatic backups for the current database dialect.
// Manual backups and backups from another dialect are never pruned.
// A value below one disables pruning so an invalid setting cannot erase all
// recoverable copies.
func (b *BackupService) Prune(maxCount int) error {
	if maxCount < 1 {
		return nil
	}
	items, err := b.List()
	if err != nil {
		return err
	}
	eligible := items[:0]
	for _, item := range items {
		if item.Automatic && strings.HasSuffix(item.Filename, postgresBackupSuffix) == b.isPostgres() {
			eligible = append(eligible, item)
		}
	}
	items = eligible
	if maxCount >= len(items) {
		return nil
	}
	for _, item := range items[maxCount:] {
		if err := b.Delete(item.Filename); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Delete removes a single backup file.
func (b *BackupService) Delete(filename string) error {
	path, err := b.backupFilePath(filename)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Restore stages SQLite backups for the next startup. PostgreSQL restores
// rebuild every table inside a single transaction.
func (b *BackupService) Restore(ctx context.Context, filename string) error {
	src, err := b.ResolvePath(filename)
	if err != nil {
		return err
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("backup not found: %s", filename)
	}
	if b.isPostgres() {
		if !strings.HasSuffix(filename, postgresBackupSuffix) {
			return errors.New("该备份是 SQLite 文件备份，当前运行在 PostgreSQL 上，无法恢复")
		}
		return b.restorePostgresBackup(ctx, src)
	}
	if !strings.HasSuffix(filename, ".db") {
		return errors.New("该备份是 PostgreSQL 逻辑备份，当前运行在 SQLite 上，无法恢复")
	}
	if err := database.StageSQLiteRestore(ctx, b.cfg, src); err != nil {
		return err
	}
	b.log.Warn("SQLite backup staged; restart to apply", zap.String("backup", filename))
	return nil
}

// isBackupFilename reports whether name looks like a backup produced by either
// dialect (SQLite ".db" or PostgreSQL ".json.gz").
func isBackupFilename(name string) bool {
	return strings.HasSuffix(name, ".db") || strings.HasSuffix(name, postgresBackupSuffix)
}

// isValidBackupFilename rejects path traversal attempts and non-backup files.
func isValidBackupFilename(name string) bool {
	if name == "" {
		return false
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return false
	}
	return isBackupFilename(name)
}
