package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestBackupRetentionPreservesManualAndOtherDialectBackups(t *testing.T) {
	svc, _ := newBackupTestService(t)
	names := []string{"mebox_auto_new.db", "mebox_auto_old.db", "mebox_manual.db", "mebox_auto_postgres.json.gz"}
	for i, name := range names {
		path := filepath.Join(svc.backupDir(), name)
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Prune(1); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		_, err := os.Stat(filepath.Join(svc.backupDir(), name))
		if name == "mebox_auto_old.db" {
			if !os.IsNotExist(err) {
				t.Fatalf("old automatic backup was retained: %v", err)
			}
		} else if err != nil {
			t.Fatalf("backup %s should have been preserved: %v", name, err)
		}
	}
}

func TestBackupCreateUsesUniquePrivateCompletedFiles(t *testing.T) {
	svc, _ := newBackupTestService(t)
	manual, err := svc.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	automatic, err := svc.CreateAutomatic(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if manual.Filename == automatic.Filename || manual.Automatic || !automatic.Automatic {
		t.Fatalf("invalid backup identities: manual=%#v automatic=%#v", manual, automatic)
	}
	items, err := svc.List()
	if err != nil || len(items) != 2 {
		t.Fatalf("completed backup list: items=%#v err=%v", items, err)
	}
	for _, item := range items {
		info, err := os.Stat(item.Path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("backup permissions are not private: %s err=%v", item.Path, err)
		}
	}
}
