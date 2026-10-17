package service

import (
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestPostgresLogicalBackupPreservesZeroValues(t *testing.T) {
	svc, _ := newBackupTestService(t)
	user := model.User{Username: "disabled", PasswordHash: "hash"}
	if err := svc.db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Model(&user).Updates(map[string]any{
		"is_active": false, "hide_adult": false, "player_volume": 0, "danmaku_enabled": false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	dump, err := svc.createPostgresBackup(t.Context(), "zero-values")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.restorePostgresBackup(t.Context(), dump); err != nil {
		t.Fatal(err)
	}
	var got model.User
	if err := svc.db.First(&got, "id = ?", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.IsActive || got.HideAdult || got.PlayerVolume != 0 || got.DanmakuEnabled {
		t.Fatalf("restore replaced zero values: active=%v hide_adult=%v volume=%v danmaku=%v", got.IsActive, got.HideAdult, got.PlayerVolume, got.DanmakuEnabled)
	}
}
