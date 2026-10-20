package service

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

func newBackupTestService(t *testing.T) (*BackupService, *config.Config) {
	t.Helper()
	cfg := &config.Config{}
	cfg.App.DataDir = t.TempDir()
	db := newServiceTestDB(t)
	svc := &BackupService{cfg: cfg, log: zap.NewNop(), db: db}
	// Create 会先建目录；直接调各方言的 create* 助手时需要它已存在。
	if err := os.MkdirAll(svc.backupDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	return svc, cfg
}

// TestPostgresLogicalBackupRoundTrip 验证「备份 → 改库 → 恢复」后整库回到备份时的
// 样子，包括软删除行与 json:"-" 的列。
//
// 逻辑备份/恢复本身是方言无关的 GORM 代码（PostgreSQL 专有的只有文件后缀与
// isPostgres() 分派），所以可以直接在 SQLite 上跑。
func TestPostgresLogicalBackupRoundTrip(t *testing.T) {
	svc, _ := newBackupTestService(t)
	ctx := context.Background()
	db := svc.db

	lib := model.Library{Base: model.Base{ID: "lib-1"}, Name: "Movies", Path: "/media/movies", Type: "movie", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.LibraryRoot{Base: model.Base{ID: "root-1"}, LibraryID: lib.ID, Path: "/media/movies"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Media{Base: model.Base{ID: "m-keep"}, LibraryID: lib.ID, Title: "Keep", Path: "/media/movies/keep.mkv"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Media{Base: model.Base{ID: "m-gone"}, LibraryID: lib.ID, Title: "Gone", Path: "/media/movies/gone.mkv"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&model.Media{}, "id = ?", "m-gone").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Setting{Key: "k", Value: "v"}).Error; err != nil {
		t.Fatal(err)
	}
	// 下面这些列都带 json:"-"：走结构体自带的 JSON 序列化会被整列丢掉，
	// 恢复之后用户权限、置顶库、加密后的密钥会一起消失。
	user := model.User{
		Base:                 model.Base{ID: "u-1"},
		Username:             "alice",
		PasswordHash:         "hash",
		Role:                 "user",
		AllowedLibraryIDs:    `["lib-1"]`,
		PinnedLibraryIDs:     `["lib-1"]`,
		HideAdult:            true,
		IsActive:             true,
		DanmakuEnabled:       true,
		PlayerVr360GuideSeen: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.APIConfig{Base: model.Base{ID: "cfg-1"}, Provider: "tmdb", APIKey: "ciphertext", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	dump, err := svc.createPostgresBackup(ctx, "20260101_000000")
	if err != nil {
		t.Fatalf("createPostgresBackup: %v", err)
	}
	if dump == "" {
		t.Fatal("createPostgresBackup returned an empty path")
	}

	// 破坏当前数据：加一条备份里没有的、硬删一条备份里有的。
	if err := db.Create(&model.Media{Base: model.Base{ID: "m-extra"}, LibraryID: lib.ID, Title: "Extra", Path: "/media/movies/extra.mkv"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Delete(&model.Media{}, "id = ?", "m-keep").Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.restorePostgresBackup(ctx, dump); err != nil {
		t.Fatalf("restorePostgresBackup: %v", err)
	}

	var ids []string
	if err := db.Unscoped().Model(&model.Media{}).Order("id asc").Pluck("id", &ids).Error; err != nil {
		t.Fatal(err)
	}
	if want := []string{"m-gone", "m-keep"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("media ids after restore = %v, want %v", ids, want)
	}

	// 软删除状态必须原样还原，而不是被「复活」。
	var gone model.Media
	if err := db.Unscoped().First(&gone, "id = ?", "m-gone").Error; err != nil {
		t.Fatal(err)
	}
	if !gone.DeletedAt.Valid {
		t.Fatal("soft-deleted media was revived by restore")
	}
	var live int64
	if err := db.Model(&model.Media{}).Where("id = ?", "m-keep").Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live media count = %d, want 1", live)
	}

	var value string
	if err := db.Model(&model.Setting{}).Where("key = ?", "k").Select("value").Scan(&value).Error; err != nil {
		t.Fatal(err)
	}
	if value != "v" {
		t.Fatalf("setting value = %q, want v", value)
	}
	var libCount int64
	if err := db.Model(&model.Library{}).Count(&libCount).Error; err != nil {
		t.Fatal(err)
	}
	if libCount != 1 {
		t.Fatalf("library count = %d, want 1", libCount)
	}
	var rootCount int64
	if err := db.Model(&model.LibraryRoot{}).Count(&rootCount).Error; err != nil {
		t.Fatal(err)
	}
	if rootCount != 1 {
		t.Fatalf("library root count = %d, want 1", rootCount)
	}

	// json:"-" 的列必须同样被备份：它们是权限与凭据，丢了比丢一条媒体严重得多。
	var restoredUser model.User
	if err := db.First(&restoredUser, "id = ?", "u-1").Error; err != nil {
		t.Fatal(err)
	}
	if restoredUser.PasswordHash != "hash" {
		t.Fatalf("password hash = %q, want hash", restoredUser.PasswordHash)
	}
	if restoredUser.AllowedLibraryIDs != `["lib-1"]` {
		t.Fatalf(`allowed library ids = %q, want ["lib-1"]`, restoredUser.AllowedLibraryIDs)
	}
	if restoredUser.PinnedLibraryIDs != `["lib-1"]` {
		t.Fatalf(`pinned library ids = %q, want ["lib-1"]`, restoredUser.PinnedLibraryIDs)
	}
	if !restoredUser.HideAdult || !restoredUser.IsActive || !restoredUser.DanmakuEnabled || !restoredUser.PlayerVr360GuideSeen {
		t.Fatalf("boolean columns lost: %+v", restoredUser)
	}

	var restoredConfig model.APIConfig
	if err := db.First(&restoredConfig, "id = ?", "cfg-1").Error; err != nil {
		t.Fatal(err)
	}
	if restoredConfig.APIKey != "ciphertext" {
		t.Fatalf("api key = %q, want ciphertext", restoredConfig.APIKey)
	}
}

// TestRestorePostgresBackupKeepsDataOnCorruptFile 验证损坏的备份不会在回滚后留下
// 半清空的库：恢复是先清空再插入，必须整体在一个事务里，否则中途失败就把库清掉了。
func TestRestorePostgresBackupKeepsDataOnCorruptFile(t *testing.T) {
	svc, _ := newBackupTestService(t)
	ctx := context.Background()
	if err := svc.db.Create(&model.Setting{Key: "keep", Value: "me"}).Error; err != nil {
		t.Fatal(err)
	}

	corrupt := filepath.Join(t.TempDir(), "broken.json.gz")
	file, err := os.Create(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(file)
	// 头部完整、正文半截：解码在清空表之后才会失败。
	if _, err := zw.Write([]byte(
		`{"format":"mebox-postgres-logical","version":1,"created_at":"2026-01-01T00:00:00Z"}` + "\n" +
			`{"table":"media","rows":[`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if err := svc.restorePostgresBackup(ctx, corrupt); err == nil {
		t.Fatal("corrupt backup should fail to restore")
	}

	var value string
	if err := svc.db.Model(&model.Setting{}).Where("key = ?", "keep").Select("value").Scan(&value).Error; err != nil {
		t.Fatal(err)
	}
	if value != "me" {
		t.Fatalf("a failed restore wiped the database: settings keep = %q", value)
	}
}

// TestRestoreRejectsBackupFromOtherDialect 验证备份文件与当前方言不匹配时给出明确
// 错误，而不是把 SQLite 文件写到 PostgreSQL 部署的库路径上（那是原实现的隐患）。
func TestRestoreRejectsBackupFromOtherDialect(t *testing.T) {
	svc, _ := newBackupTestService(t)
	pgStyle := filepath.Join(svc.backupDir(), "mebox_20260101_000000.json.gz")
	if err := os.WriteFile(pgStyle, []byte("not-gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	// SQLite 路径下不允许恢复 PostgreSQL 逻辑备份。
	if err := svc.Restore(context.Background(), filepath.Base(pgStyle)); err == nil {
		t.Fatal("SQLite restore accepted a PostgreSQL logical backup")
	}
}
