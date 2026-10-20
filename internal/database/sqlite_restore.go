package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

var sqliteRestoreMu sync.Mutex

func sqliteRestorePath(cfg *config.Config) (string, error) {
	if cfg == nil || strings.TrimSpace(cfg.Database.DBPath) == "" || cfg.Database.DBPath == ":memory:" || strings.HasPrefix(cfg.Database.DBPath, "file:") {
		return "", errors.New("恢复需要配置有效的 SQLite 数据库文件路径")
	}
	return filepath.Abs(cfg.Database.DBPath)
}

func sqliteRestoreDSN(path, mode string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(5000)"}}
	u.RawQuery = q.Encode()
	return u.String()
}

func validateSQLiteRestore(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("备份必须是常规文件")
	}
	db, err := sql.Open("sqlite", sqliteRestoreDSN(path, "ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("SQLite 备份校验失败: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("SQLite 备份校验失败: %s", result)
	}
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('users', 'media', 'settings')").Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		return errors.New("备份不包含 MeBox 数据表")
	}
	return nil
}

// StageSQLiteRestore leaves the live database and its WAL untouched. The
// validated copy is applied on the next startup, before any database is opened.
func StageSQLiteRestore(ctx context.Context, cfg *config.Config, source string) error {
	sqliteRestoreMu.Lock()
	defer sqliteRestoreMu.Unlock()
	dbPath, err := sqliteRestorePath(cfg)
	if err != nil {
		return err
	}
	pending := dbPath + ".restore-pending"
	if _, err := os.Lstat(pending); err == nil {
		return errors.New("已有等待恢复的备份，请重启 MeBox 后再操作")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := validateSQLiteRestore(ctx, source); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dbPath), ".mebox-restore-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := validateSQLiteRestore(ctx, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, pending)
}

// ApplyPendingSQLiteRestore must run before database.Open and before workers or
// HTTP listeners start. The previous database is retained alongside the file.
func ApplyPendingSQLiteRestore(ctx context.Context, cfg *config.Config, log *zap.Logger) error {
	if cfg == nil {
		return nil
	}
	dialect := normalizeDatabaseType(cfg.Database.Type)
	if dialect == "auto" {
		dialect = effectiveAutoDatabaseType(cfg)
	}
	if dialect != "sqlite" {
		return nil
	}
	sqliteRestoreMu.Lock()
	defer sqliteRestoreMu.Unlock()
	dbPath, err := sqliteRestorePath(cfg)
	if err != nil {
		return err
	}
	pending := dbPath + ".restore-pending"
	if _, err := os.Lstat(pending); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateSQLiteRestore(ctx, pending); err != nil {
		return err
	}
	previous := ""
	if _, err := os.Lstat(dbPath); err == nil {
		// A clean checkpoint prevents an old WAL from being replayed into the
		// replacement database. A busy checkpoint leaves all files untouched.
		live, err := sql.Open("sqlite", sqliteRestoreDSN(dbPath, "rw"))
		if err != nil {
			return err
		}
		var busy, pages, copied int
		err = live.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &pages, &copied)
		closeErr := live.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if busy != 0 {
			return errors.New("SQLite 数据库仍被占用，恢复已保留等待下次启动")
		}
		old, err := os.CreateTemp(filepath.Dir(dbPath), filepath.Base(dbPath)+".before-restore-*")
		if err != nil {
			return err
		}
		previous = old.Name()
		if err := old.Close(); err != nil {
			return err
		}
		if err := os.Remove(previous); err != nil {
			return err
		}
		if err := os.Rename(dbPath, previous); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(pending, dbPath); err != nil {
		if previous != "" {
			if rollbackErr := os.Rename(previous, dbPath); rollbackErr != nil {
				return fmt.Errorf("恢复失败: %w；回退失败: %v，原数据库保留于 %s", err, rollbackErr, previous)
			}
		}
		return err
	}
	if log != nil {
		log.Warn("SQLite backup restored before startup", zap.String("previous_database", previous))
	}
	return nil
}
