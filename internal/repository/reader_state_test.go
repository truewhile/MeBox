package repository

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/database"
	"github.com/truewhile/MeBox/internal/model"
)

func newReaderTestRepo(t *testing.T) *ReaderRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db).Reader
}

// TestSourceStateRoundTrip 验证会话状态按书源 URL 存取。
func TestSourceStateRoundTrip(t *testing.T) {
	repo := newReaderTestRepo(t)
	ctx := t.Context()

	// 不存在时返回 (nil, nil)，调用方据此走默认值
	got, err := repo.GetSourceState(ctx, "https://a.example.com")
	if err != nil || got != nil {
		t.Fatalf("首次读取应为空: got=%v err=%v", got, err)
	}

	st := &model.ReaderSourceState{
		SourceURL:   "https://a.example.com",
		Variable:    `{"线路":"v1"}`,
		LoginInfo:   `{"邮箱":"u@e.com"}`,
		LoginHeader: `{"X-Token":"t"}`,
		Cookies:     `{"example.com":"qttoken=abc"}`,
	}
	if err := repo.SaveSourceState(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetSourceState(ctx, "https://a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.LoginInfo != st.LoginInfo || got.Variable != st.Variable {
		t.Fatalf("读回不一致: %+v", got)
	}

	// 再次保存应为更新而非插入（同 URL 唯一）
	st.LoginInfo = `{"邮箱":"new@e.com"}`
	if err := repo.SaveSourceState(ctx, st); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := repo.db.Model(&model.ReaderSourceState{}).
		Where("source_url = ?", "https://a.example.com").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("同一书源应只有一行状态，实际 %d", count)
	}
	got, _ = repo.GetSourceState(ctx, "https://a.example.com")
	if got.LoginInfo != `{"邮箱":"new@e.com"}` {
		t.Fatalf("更新未生效: %+v", got)
	}
}

// TestDeleteSourceAlsoClearsState 删除书源应连带清理登录态。
func TestDeleteSourceAlsoClearsState(t *testing.T) {
	repo := newReaderTestRepo(t)
	ctx := t.Context()

	src := &model.ReaderBookSource{Name: "源", SourceURL: "https://b.example.com", Enabled: true}
	if err := repo.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveSourceState(ctx, &model.ReaderSourceState{
		SourceURL: "https://b.example.com", LoginInfo: `{"a":"b"}`,
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteSource(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	st, err := repo.GetSourceState(ctx, "https://b.example.com")
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	if st != nil {
		t.Fatalf("书源删除后登录态应一并清理，实际仍在: %+v", st)
	}
}

// TestImportUpdateKeepsSourceState 重新导入书源（更新 RawJSON）不应丢失登录态。
func TestImportUpdateKeepsSourceState(t *testing.T) {
	repo := newReaderTestRepo(t)
	ctx := t.Context()

	src := &model.ReaderBookSource{Name: "源", SourceURL: "https://c.example.com", RawJSON: `{"v":1}`}
	if err := repo.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveSourceState(ctx, &model.ReaderSourceState{
		SourceURL: "https://c.example.com", LoginInfo: `{"k":"keep"}`,
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟导入更新：只改书源自身字段
	src.RawJSON = `{"v":2}`
	if err := repo.UpdateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	st, err := repo.GetSourceState(ctx, "https://c.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || st.LoginInfo != `{"k":"keep"}` {
		t.Fatalf("更新书源后登录态丢失: %+v", st)
	}
}
