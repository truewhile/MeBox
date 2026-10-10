package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

// legacyReaderSource 旧版书源结构（全局唯一，没有 user_id）。
type legacyReaderSource struct {
	ID             string `gorm:"primaryKey"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
	Name           string
	SourceURL      string `gorm:"type:varchar(512);uniqueIndex"`
	RawJSON        string
	Enabled        bool
	EnabledExplore bool
	Variables      string
	Header         string
}

func (legacyReaderSource) TableName() string { return "reader_book_sources" }

// 迁移路径：老库（书源全局唯一、无 user_id）升级后
//  1. 旧唯一索引被删除，两个用户可以各持一份同 URL 的书源副本；
//  2. 书源按「谁书架上有这本书」回填归属，多个引用者各得一份副本；
//  3. 软删的历史书源行被清理，删除后重新导入不再撞唯一键。
func TestReaderSourceMigrationBackfill(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上 sqlite 文件句柄会阻塞 TempDir 清理，测试结束前先关连接。
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// 造老结构：只有书源表（带旧唯一索引），书籍表用当前模型建。
	if err := db.AutoMigrate(&legacyReaderSource{}, &model.ReaderBook{}); err != nil {
		t.Fatal(err)
	}
	shared := &legacyReaderSource{ID: "legacy-1", Name: "共享源", SourceURL: "https://shared.example.com", Enabled: true}
	if err := db.Create(shared).Error; err != nil {
		t.Fatal(err)
	}
	softDeleted := &legacyReaderSource{ID: "legacy-2", Name: "已删源", SourceURL: "https://gone.example.com"}
	if err := db.Create(softDeleted).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(softDeleted).Error; err != nil { // 旧版 DeleteSource 的软删
		t.Fatal(err)
	}
	// u1 两本、u2 一本都引用共享源；孤儿源无人引用。
	books := []model.ReaderBook{
		{UserID: "u1", Origin: "https://shared.example.com", BookURL: "https://shared.example.com/b/1", Name: "书1"},
		{UserID: "u1", Origin: "https://shared.example.com", BookURL: "https://shared.example.com/b/2", Name: "书2"},
		{UserID: "u2", Origin: "https://shared.example.com", BookURL: "https://shared.example.com/b/1", Name: "书1"},
		{UserID: "u2", Origin: "https://orphan.example.com", BookURL: "https://orphan.example.com/b/1", Name: "孤儿"},
	}
	for i := range books {
		if err := db.Create(&books[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 迁移。
	if err := dropLegacyReaderSourceUniqueIndex(db); err != nil {
		t.Fatalf("删旧索引失败: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
	if err := backfillReaderSourceOwnership(db); err != nil {
		t.Fatalf("回填失败: %v", err)
	}

	// 1. 旧唯一索引已删除：两个用户同 URL 各自一份。
	var rows []model.ReaderBookSource
	if err := db.Order("user_id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	byUser := map[string]model.ReaderBookSource{}
	for _, r := range rows {
		if r.SourceURL == "https://shared.example.com" {
			byUser[r.UserID] = r
		}
	}
	if len(byUser) != 2 {
		t.Fatalf("共享源应有两份副本，实际 %d 份: %+v", len(byUser), byUser)
	}
	if byUser["u1"].ID == "" || byUser["u2"].ID == "" || byUser["u1"].ID == byUser["u2"].ID {
		t.Fatalf("两份副本应有不同行 ID: u1=%q u2=%q", byUser["u1"].ID, byUser["u2"].ID)
	}
	if byUser["u1"].RawJSON != shared.RawJSON || byUser["u1"].Name != "共享源" {
		t.Fatal("副本应保留原书源内容")
	}
	// 引用最多的用户保留原行（u1 有两本书）。
	if byUser["u1"].ID != "legacy-1" {
		t.Fatalf("原行应归引用最多的用户，实际 %q", byUser["u1"].ID)
	}

	// 2. 软删的历史行被清理。
	var softLeft int64
	if err := db.Unscoped().Model(&model.ReaderBookSource{}).
		Where("source_url = ?", "https://gone.example.com").Count(&softLeft).Error; err != nil {
		t.Fatal(err)
	}
	if softLeft != 0 {
		t.Fatalf("软删的历史书源应被清理，剩余 %d 行", softLeft)
	}

	// 3. 无人引用的书源保持无归属（不会凭空分配给谁）。
	var orphans int64
	if err := db.Model(&model.ReaderBookSource{}).
		Where("source_url = ? AND user_id <> ''", "https://orphan.example.com").Count(&orphans).Error; err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatal("无人引用的书源不应被分配归属")
	}

	// 4. 回填是幂等的（再次执行不产生重复副本）。
	if err := backfillReaderSourceOwnership(db); err != nil {
		t.Fatal(err)
	}
	var total int64
	if err := db.Model(&model.ReaderBookSource{}).Where("source_url = ?", "https://shared.example.com").Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("重复回填不应新增副本，实际 %d 份", total)
	}
}
