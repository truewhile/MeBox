// Package service — database hot backup and restore.
//
// Backup files are stored under {data_dir}/backups/ with a timestamp suffix.
// The admin UI can list / download / delete / restore them.
//
// 两种方言的备份形态不同：
//   - SQLite：用 "VACUUM INTO"（3.27.0 起可用）产出一个一致的库文件，
//     扩展名 .db；恢复即「替换库文件 + 重启」。
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
	dir := b.backupDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	ts := time.Now().Format("20060102_150405")

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
	}, nil
}

func (b *BackupService) createSQLiteBackup(ctx context.Context, stamp string) (string, error) {
	dst := filepath.Join(b.backupDir(), fmt.Sprintf("mebox_%s.db", stamp))
	// VACUUM INTO creates a clean, non-WAL copy atomically.
	if err := b.db.WithContext(ctx).Exec("VACUUM INTO ?", dst).Error; err != nil {
		return "", fmt.Errorf("VACUUM INTO failed: %w", err)
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
		if e.IsDir() || !isBackupFilename(e.Name()) {
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
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// Delete removes a single backup file.
func (b *BackupService) Delete(filename string) error {
	path, err := b.backupFilePath(filename)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Restore replaces the live database with the given backup.
// WARNING: this is destructive. SQLite restores by swapping the database file
// (the caller should shut down the server afterwards); PostgreSQL restores by
// rebuilding every table inside a single transaction.
func (b *BackupService) Restore(ctx context.Context, filename string) error {
	src, err := b.backupFilePath(filename)
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
	return b.restoreSQLiteBackup(filename, src)
}

func (b *BackupService) restoreSQLiteBackup(filename, src string) error {
	dbPath, err := filepath.Abs(b.cfg.Database.DBPath)
	if err != nil || dbPath == "" {
		return errors.New("invalid database path")
	}
	// Strategy: rename live → live.old, copy backup → live, delete old.
	old := dbPath + ".before_restore"
	if err := os.Rename(dbPath, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(src) // #nosec G304 -- src is constrained by backupFilePath to the configured backups directory.
	if err != nil {
		// Revert
		_ = os.Rename(old, dbPath)
		return err
	}
	if err := os.WriteFile(dbPath, data, 0o600); err != nil { // #nosec G703 -- database path is trusted process config and normalized before restore.
		_ = os.Rename(old, dbPath)
		return err
	}
	_ = os.Remove(old)
	// Also remove WAL/SHM so SQLite opens the fresh file cleanly.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	b.log.Warn("database restored from backup — restart the server",
		zap.String("backup", filename))
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
