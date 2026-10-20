package repository

import (
	"errors"
	"testing"
	"time"

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

// TestSaveSourceStateAfterSourceDeleted 回归：书源删除后，同一 source_url 的状态必须还能存。
//
// source_url 上的唯一索引覆盖软删行，而 DeleteSource 曾经只做软删：
// 之后 SaveSourceState 的 First（默认排除软删行）查不到 → Create 撞唯一约束，
// 表现为「保存书源会话状态失败: UNIQUE constraint failed」，
// cookie / 登录态从此永远存不进去（删过或重导入过的书源必现）。
func TestSaveSourceStateAfterSourceDeleted(t *testing.T) {
	repo := newReaderTestRepo(t)
	ctx := t.Context()
	const url = "https://d.example.com"

	src := &model.ReaderBookSource{Name: "源", SourceURL: url, Enabled: true}
	if err := repo.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveSourceState(ctx, &model.ReaderSourceState{
		SourceURL: url, Cookies: `{"example.com":"a=1"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteSource(ctx, src.ID); err != nil {
		t.Fatal(err)
	}

	// 重新导入同 URL 的书源，再存一次状态
	again := &model.ReaderBookSource{Name: "源", SourceURL: url, Enabled: true}
	if err := repo.CreateSource(ctx, again); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveSourceState(ctx, &model.ReaderSourceState{
		SourceURL: url, Cookies: `{"example.com":"a=2"}`,
	}); err != nil {
		t.Fatalf("书源删除/重导入后状态必须还能保存: %v", err)
	}
	st, err := repo.GetSourceState(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || st.Cookies != `{"example.com":"a=2"}` {
		t.Fatalf("状态未写入: %+v", st)
	}
}

// TestSaveSourceStateRevivesSoftDeletedRow 回归：老数据里已被软删的状态行要能复活。
//
// 修复前 DeleteSource 是软删，历史库里可能已经躺着软删行；
// 保存时必须把它救活，不能去 INSERT 撞唯一约束。
func TestSaveSourceStateRevivesSoftDeletedRow(t *testing.T) {
	repo := newReaderTestRepo(t)
	ctx := t.Context()
	const url = "https://e.example.com"

	if err := repo.SaveSourceState(ctx, &model.ReaderSourceState{
		SourceURL: url, Cookies: `{"example.com":"old=1"}`,
	}); err != nil {
		t.Fatal(err)
	}
	// 造出老版本 DeleteSource 留下的样子：行还在但被软删
	if err := repo.db.Exec(
		"update reader_source_states set deleted_at = ? where source_url = ?",
		time.Now(), url).Error; err != nil {
		t.Fatal(err)
	}

	if err := repo.SaveSourceState(ctx, &model.ReaderSourceState{
		SourceURL: url, Cookies: `{"example.com":"new=2"}`,
	}); err != nil {
		t.Fatalf("软删过的历史行应被复活，而不是撞唯一约束: %v", err)
	}
	st, err := repo.GetSourceState(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || st.Cookies != `{"example.com":"new=2"}` {
		t.Fatalf("复活后状态未写入: %+v", st)
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
