package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestSegmentPrewarmDefaultsOff(t *testing.T) {
	repos := repository.New(newServiceTestDB(t, &model.Setting{}))
	scheduler := NewSchedulerService(zap.NewNop(), repos, nil, nil, nil, nil, "")
	if scheduler.segmentPrewarmEnabled(t.Context()) {
		t.Fatal("prewarm must default to off")
	}
}

func TestJobSegmentPrewarmSkippedWhenDisabled(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	repos := repository.New(newServiceTestDB(t))
	if err := repos.DB.Create(&model.Media{
		Base: model.Base{ID: "mv-1"}, Path: "/a.mkv", TMDbID: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	segments := NewMediaSegmentService(zap.NewNop(), repos).
		SetIntroDB(NewIntroDBService(zap.NewNop()).SetBaseURL(server.URL)).
		SetPrewarmGap(0)
	scheduler := NewSchedulerService(zap.NewNop(), repos, nil, nil, nil, nil, "")
	scheduler.SetSegments(segments)

	if err := scheduler.jobSegmentPrewarm(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("provider calls = %d, want 0 when prewarm is disabled", got)
	}

	if err := repos.Setting.Set(t.Context(), SegmentPrewarmEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.jobSegmentPrewarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("provider calls = %d, want 1 after enabling prewarm", got)
	}
}
