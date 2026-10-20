package database

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/model"
)

// TestAutoMigrateCreatesMediaPathUniqueIndex 守住 media.path 的唯一约束。
//
// 模型上不再声明 gorm:"uniqueIndex"（PostgreSQL 的 btree 单条索引项上限是
// 2704 字节，而 varchar(1024) 在中文路径下可达 3072 字节，对整列建唯一索引会让
// 这类行插入直接失败），改由 AutoMigrate 里的 ensureMediaPathIndex 按方言显式
// 建立。一旦这个调用被漏掉，扫描会静默产生重复的媒体行（同一文件被入库多次）。
func TestAutoMigrateCreatesMediaPathUniqueIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "mebox.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 必须在 TempDir 清理之前关掉连接，否则 Windows 上删除库文件会失败。
	// t.Cleanup 是后进先出：这里注册的关闭早于 t.TempDir 注册的删除。
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	var definition string
	if err := db.Raw(`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' AND name = 'idx_media_path'`).Scan(&definition).Error; err != nil {
		t.Fatal(err)
	}
	if definition == "" {
		t.Fatal("AutoMigrate did not create idx_media_path")
	}
	if !strings.Contains(strings.ToUpper(definition), "UNIQUE") {
		t.Fatalf("idx_media_path is not unique: %q", definition)
	}

	if err := db.Create(&model.Media{Base: model.Base{ID: "m-1"}, Path: "/media/a.mkv"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Media{Base: model.Base{ID: "m-2"}, Path: "/media/a.mkv"}).Error; err == nil {
		t.Fatal("a duplicate media path was accepted; the unique constraint is not enforced")
	}
}
