package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

func TestStopHLSChecksMediaVisibility(t *testing.T) {
	router, svc, secret := newPlaybackScopeTestRouter(t)
	svc.Transcoder = service.NewTranscoderService(svc.Cfg, zap.NewNop(), svc.Repo, nil)
	router.DELETE("/api/hls/:id", middleware.AuthRequired(secret), stopTranscodeHandler(svc))
	req := httptest.NewRequest(http.MethodDelete, "/api/hls/nonexistent?session_id=player-a", nil)
	req.Header.Set("Authorization", "Bearer "+signedTestToken(t, secret))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stop of a nonexistent/invisible media: status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	profile := &model.PlayProfile{UserID: "user-1", Name: "restricted", IsDefault: true, AllowedLibraryIDs: `["other-library"]`}
	if err := svc.Repo.DB.Create(profile).Error; err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/hls/media-1?session_id=player-a", nil)
	req.Header.Set("Authorization", "Bearer "+signedTestToken(t, secret))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invisible existing media can be stopped: %d %s", rec.Code, rec.Body.String())
	}
}
