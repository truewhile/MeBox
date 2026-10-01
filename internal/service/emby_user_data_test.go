package service

import (
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestMergedRemoteUserData(t *testing.T) {
	tests := []struct {
		name     string
		raw      any
		history  model.PlaybackHistory
		position int64
		played   bool
		percent  float64
		count    int
		preserve any
	}{
		{
			name: "in-progress preserves non-user remote fields only",
			raw: map[string]any{
				"PlayCount":             2,
				"IsFavorite":            true,
				"PlaybackPositionTicks": int64(999),
				"Custom":                "remote-value",
			},
			history:  model.PlaybackHistory{PositionMs: 25_000, DurationMs: 100_000},
			position: 250_000_000,
			played:   false,
			percent:  25,
			count:    0,
			preserve: "remote-value",
		},
		{
			name:     "completed ensures a play count",
			raw:      map[string]any{"PlayCount": 0},
			history:  model.PlaybackHistory{PositionMs: 100_000, DurationMs: 100_000, Completed: true},
			position: 1_000_000_000,
			played:   true,
			percent:  100,
			count:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := mergedRemoteUserData(tt.raw, &tt.history)
			if got := out["PlaybackPositionTicks"]; got != tt.position {
				t.Fatalf("PlaybackPositionTicks = %#v, want %d", got, tt.position)
			}
			if got := out["Played"]; got != tt.played {
				t.Fatalf("Played = %#v, want %t", got, tt.played)
			}
			if got := out["PlayedPercentage"]; got != tt.percent {
				t.Fatalf("PlayedPercentage = %#v, want %v", got, tt.percent)
			}
			if got := out["PlayCount"]; got != tt.count {
				t.Fatalf("PlayCount = %#v, want %d", got, tt.count)
			}
			if tt.preserve != nil && out["Custom"] != tt.preserve {
				t.Fatalf("Custom = %#v, want %#v", out["Custom"], tt.preserve)
			}
			if out["IsFavorite"] != false && out["IsFavorite"] != true {
				t.Fatalf("IsFavorite missing: %#v", out)
			}
		})
	}
}

func TestApplyMeBoxUserDataClearsSharedRemoteState(t *testing.T) {
	out := applyMeBoxUserData(map[string]any{
		"IsFavorite":            true,
		"PlaybackPositionTicks": int64(42_000_000),
		"Played":                true,
		"PlayedPercentage":      80.0,
		"PlayCount":             3,
		"Key":                   "keep",
	}, nil, false)
	if out["IsFavorite"] != false {
		t.Fatalf("IsFavorite = %#v, want false", out["IsFavorite"])
	}
	if out["PlaybackPositionTicks"] != int64(0) {
		t.Fatalf("PlaybackPositionTicks = %#v, want 0", out["PlaybackPositionTicks"])
	}
	if out["Played"] != false {
		t.Fatalf("Played = %#v, want false", out["Played"])
	}
	if out["PlayedPercentage"] != float64(0) {
		t.Fatalf("PlayedPercentage = %#v, want 0", out["PlayedPercentage"])
	}
	if out["PlayCount"] != 0 {
		t.Fatalf("PlayCount = %#v, want 0", out["PlayCount"])
	}
	if out["Key"] != "keep" {
		t.Fatalf("Key = %#v, want keep", out["Key"])
	}

	fav := applyMeBoxUserData(map[string]any{"IsFavorite": false}, nil, true)
	if fav["IsFavorite"] != true {
		t.Fatalf("favorite overlay IsFavorite = %#v, want true", fav["IsFavorite"])
	}
}

func TestRemoteItemMapsFindsEnvelopeItems(t *testing.T) {
	remoteID := EncodeEmbyRemoteID("mount-1", "item-1")
	payload := map[string]any{
		"Items": []any{
			map[string]any{"Id": remoteID},
			map[string]any{"Id": "local-item"},
		},
	}
	items := remoteItemMaps(payload)
	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2", len(items))
	}
	if items[0]["Id"] != remoteID {
		t.Fatalf("first item ID = %#v, want %q", items[0]["Id"], remoteID)
	}
}

func TestRecordProgressFallbacksToExistingHistoryDuration(t *testing.T) {
	svc := newTestEmbyService(t)
	remoteID := EncodeEmbyRemoteID("mount-test", "item-999")
	user := &model.User{Username: "resume_test_user", Role: "user", Tier: "free", IsActive: true}
	if err := svc.repo.User.Create(t.Context(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// 先以有 runtimeTicks 写入首次进度
	if err := svc.RecordProgress(t.Context(), user.ID, remoteID, 10_000_000, 100_000_000); err != nil {
		t.Fatalf("first record progress: %v", err)
	}
	// 再次上报，但某些客户端此时发了 0 runtimeTicks
	if err := svc.RecordProgress(t.Context(), user.ID, remoteID, 95_000_000, 0); err != nil {
		t.Fatalf("second record progress: %v", err)
	}

	var hist model.PlaybackHistory
	if err := svc.repo.DB.Where("user_id = ? AND media_id = ?", user.ID, remoteID).First(&hist).Error; err != nil {
		t.Fatalf("find hist: %v", err)
	}
	if hist.DurationMs != 10_000 {
		t.Fatalf("expected duration 10000ms, got %d", hist.DurationMs)
	}
	if !hist.Completed {
		t.Fatalf("expected 95%% progress to be completed")
	}
}

func TestMarkPlayedStoresRemoteItemLocallyPerUser(t *testing.T) {
	svc := newTestEmbyService(t)
	remoteID := EncodeEmbyRemoteID("mount-test", "item-played")
	alice := &model.User{Username: "alice_played", Role: "user", Tier: "free", IsActive: true}
	bob := &model.User{Username: "bob_played", Role: "user", Tier: "free", IsActive: true}
	if err := svc.repo.User.Create(t.Context(), alice); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := svc.repo.User.Create(t.Context(), bob); err != nil {
		t.Fatalf("create bob: %v", err)
	}

	if err := svc.MarkPlayed(t.Context(), alice.ID, remoteID, true); err != nil {
		t.Fatalf("mark played: %v", err)
	}

	var aliceRows, bobRows int64
	_ = svc.repo.DB.Model(&model.PlaybackHistory{}).Where("user_id = ? AND media_id = ?", alice.ID, remoteID).Count(&aliceRows)
	_ = svc.repo.DB.Model(&model.PlaybackHistory{}).Where("user_id = ? AND media_id = ?", bob.ID, remoteID).Count(&bobRows)
	if aliceRows != 1 {
		t.Fatalf("alice history rows = %d, want 1", aliceRows)
	}
	if bobRows != 0 {
		t.Fatalf("bob should not see alice remote played state, rows=%d", bobRows)
	}

	payload := map[string]any{
		"Id": remoteID,
		"UserData": map[string]any{
			"IsFavorite":            true,
			"PlaybackPositionTicks": int64(50_000_000),
			"Played":                true,
		},
	}
	if err := svc.mergeRemoteUserData(t.Context(), bob.ID, payload); err != nil {
		t.Fatalf("merge for bob: %v", err)
	}
	bobData := payload["UserData"].(map[string]any)
	if bobData["IsFavorite"] != false {
		t.Fatalf("bob IsFavorite leaked: %#v", bobData)
	}
	if bobData["Played"] != false || bobData["PlaybackPositionTicks"] != int64(0) {
		t.Fatalf("bob playback leaked: %#v", bobData)
	}

	if err := svc.mergeRemoteUserData(t.Context(), alice.ID, payload); err != nil {
		t.Fatalf("merge for alice: %v", err)
	}
	aliceData := payload["UserData"].(map[string]any)
	if aliceData["Played"] != true {
		t.Fatalf("alice Played = %#v, want true", aliceData["Played"])
	}
}
