package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/truewhile/MeBox/internal/config"
)

func TestSQLiteRestoreStagesWithoutChangingLiveDatabase(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Database.Type = "sqlite"
	cfg.Database.DBPath = filepath.Join(root, "live.db")
	live, err := sql.Open("sqlite", sqliteRestoreDSN(cfg.Database.DBPath, "rwc"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL", "CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)",
		"INSERT INTO settings VALUES ('state', 'backup')",
	} {
		if _, err := live.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "backup.db")
	if _, err := live.Exec("VACUUM INTO ?", source); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec("UPDATE settings SET value = 'live'"); err != nil {
		t.Fatal(err)
	}
	if err := StageSQLiteRestore(t.Context(), cfg, source); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := live.QueryRow("SELECT value FROM settings").Scan(&value); err != nil || value != "live" {
		t.Fatalf("staging changed live database: value=%q err=%v", value, err)
	}
	if _, err := live.Exec("UPDATE settings SET value = 'after-stage'"); err != nil {
		t.Fatalf("database could not write after staging: %v", err)
	}
	if err := StageSQLiteRestore(t.Context(), cfg, source); err == nil {
		t.Fatal("second staging should not replace the pending restore")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingSQLiteRestore(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", sqliteRestoreDSN(cfg.Database.DBPath, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.QueryRow("SELECT value FROM settings").Scan(&value); err != nil || value != "backup" {
		t.Fatalf("restore failed: value=%q err=%v", value, err)
	}
	previous, err := filepath.Glob(cfg.Database.DBPath + ".before-restore-*")
	if err != nil || len(previous) != 1 {
		t.Fatalf("previous database not preserved: files=%v err=%v", previous, err)
	}
	old, err := sql.Open("sqlite", sqliteRestoreDSN(previous[0], "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := old.QueryRow("SELECT value FROM settings").Scan(&value); err != nil || value != "after-stage" {
		t.Fatalf("old database lost latest writes: value=%q err=%v", value, err)
	}
	if err := ApplyPendingSQLiteRestore(t.Context(), cfg, nil); err != nil {
		t.Fatalf("repeat startup: %v", err)
	}
}

func TestSQLiteRestoreRejectsCorruptAndSymlinkBackups(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Database.DBPath = filepath.Join(root, "live.db")
	source := filepath.Join(root, "corrupt.db")
	if err := os.WriteFile(source, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StageSQLiteRestore(t.Context(), cfg, source); err == nil {
		t.Fatal("corrupt backup accepted")
	}
	link := filepath.Join(root, "link.db")
	if err := os.Symlink(source, link); err == nil {
		if err := StageSQLiteRestore(t.Context(), cfg, link); err == nil {
			t.Fatal("symlink backup accepted")
		}
	}
	if _, err := os.Stat(cfg.Database.DBPath + ".restore-pending"); !os.IsNotExist(err) {
		t.Fatalf("failed validation left pending data: %v", err)
	}
}
