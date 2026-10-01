package service

import (
	"testing"

	"go.uber.org/zap"
)

// The rule set is cached process-wide, so a config write must invalidate it
// immediately; otherwise the admin would have to wait out the TTL before a
// saved word list takes effect.
func TestRecognitionWordsCacheInvalidatedBySaveConfig(t *testing.T) {
	repos := newOrganizerTestRepo(t)
	svc := NewRecognitionWordsService(zap.NewNop(), repos)

	if err := svc.SaveConfig(t.Context(), RecognitionWordsConfig{
		Enabled:   true,
		LocalText: "BADWORD => 好标题",
	}); err != nil {
		t.Fatal(err)
	}
	if got := ApplyRecognitionWords(t.Context(), repos, "BADWORD"); got != "好标题" {
		t.Fatalf("first apply = %q, want 好标题", got)
	}

	if err := svc.SaveConfig(t.Context(), RecognitionWordsConfig{
		Enabled:   true,
		LocalText: "BADWORD => 新标题",
	}); err != nil {
		t.Fatal(err)
	}
	if got := ApplyRecognitionWords(t.Context(), repos, "BADWORD"); got != "新标题" {
		t.Fatalf("cached rules were not invalidated after SaveConfig: got %q, want 新标题", got)
	}
}

func TestRecognitionWordsDisabledReturnsRawInput(t *testing.T) {
	repos := newOrganizerTestRepo(t)
	svc := NewRecognitionWordsService(zap.NewNop(), repos)
	if err := svc.SaveConfig(t.Context(), RecognitionWordsConfig{
		Enabled:   false,
		LocalText: "BADWORD => 好标题",
	}); err != nil {
		t.Fatal(err)
	}
	if got := ApplyRecognitionWords(t.Context(), repos, "BADWORD"); got != "BADWORD" {
		t.Fatalf("disabled recognition words changed input: got %q", got)
	}
}
