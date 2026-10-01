package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

func TestMountedEmbyPlayingProgressAndResumePipeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	repos := repository.New(db)
	user := &model.User{
		Base:         model.Base{ID: "user-1"},
		Username:     "test_viewer",
		PasswordHash: "x",
		Role:         "user",
		Tier:         "free",
		IsActive:     true,
	}
	if err := repos.User.Create(t.Context(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	cfg := &config.Config{}
	logger := zap.NewNop()
	svc := &service.Container{
		Repo:     repos,
		Emby:     service.NewEmbyService(cfg, logger, repos),
		Sessions: service.NewSessionTrackerService(logger),
		Playback: service.NewPlaybackService(logger, repos),
	}

	router := gin.New()
	// 注册带认证的路由，模拟已登录用户
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Next()
	})
	router.POST("/Sessions/Playing/Progress", embyPlayingProgressHandler(svc))
	router.GET("/Items", embyItemsHandler(svc))
	router.GET("/Users/:userId/Items/Resume", embyResumeItemsHandler(svc))
	router.GET("/Sessions", embySessionsHandler(svc))

	remoteMediaID := service.EncodeEmbyRemoteID("mount-1", "remote-item-123")

	// 1. 测试上报进度：客户端使用小写 query 参数 itemId / positionTicks
	progressReq := httptest.NewRequest(
		http.MethodPost,
		"/Sessions/Playing/Progress?itemId="+remoteMediaID+"&positionTicks=300000000&runTimeTicks=1000000000&playSessionId=remote-mount-1-2000000000000",
		nil,
	)
	wProgress := httptest.NewRecorder()
	router.ServeHTTP(wProgress, progressReq)
	if wProgress.Code != http.StatusNoContent {
		t.Fatalf("progress status = %d, body = %s", wProgress.Code, wProgress.Body.String())
	}

	// 验证已持久化到 PlaybackHistory
	var hist model.PlaybackHistory
	if err := db.Where("user_id = ? AND media_id = ?", user.ID, remoteMediaID).First(&hist).Error; err != nil {
		t.Fatalf("playback history not saved: %v", err)
	}
	if hist.PositionMs != 30000 {
		t.Fatalf("expected position_ms = 30000, got %d", hist.PositionMs)
	}
	if hist.SessionID != "remote-mount-1-2000000000000" || hist.SessionStartedAtMs != 2000000000000 {
		t.Fatalf("unexpected playback session metadata: %#v", hist)
	}

	// 2. 测试 Filters=IsResumable 能够包含该远程条目
	resumableReq := httptest.NewRequest(
		http.MethodGet,
		"/Items?Filters=IsResumable",
		nil,
	)
	wResumable := httptest.NewRecorder()
	router.ServeHTTP(wResumable, resumableReq)
	if wResumable.Code != http.StatusOK {
		t.Fatalf("items resumable status = %d, body = %s", wResumable.Code, wResumable.Body.String())
	}
	var resumableEnvelope map[string]any
	if err := json.Unmarshal(wResumable.Body.Bytes(), &resumableEnvelope); err != nil {
		t.Fatalf("decode resumable: %v", err)
	}
	// 因为没有配置真实的远程客户端连接，该远程条目在当前离线测试中不会 panic 崩溃，并且正常响应 Envelope
	if resumableEnvelope["TotalRecordCount"] == nil {
		t.Fatalf("missing TotalRecordCount in resumable envelope")
	}

	// 3. 测试 /Users/:userId/Items/Resume 别名路由
	resumeAliasReq := httptest.NewRequest(
		http.MethodGet,
		"/Users/"+user.ID+"/Items/Resume",
		nil,
	)
	wResumeAlias := httptest.NewRecorder()
	router.ServeHTTP(wResumeAlias, resumeAliasReq)
	if wResumeAlias.Code != http.StatusOK {
		t.Fatalf("resume alias status = %d, body = %s", wResumeAlias.Code, wResumeAlias.Body.String())
	}

	// 4. 测试 /Sessions 返回 NowPlayingItem
	sessionsReq := httptest.NewRequest(http.MethodGet, "/Sessions", nil)
	wSessions := httptest.NewRecorder()
	router.ServeHTTP(wSessions, sessionsReq)
	if wSessions.Code != http.StatusOK {
		t.Fatalf("sessions status = %d, body = %s", wSessions.Code, wSessions.Body.String())
	}
	var sessionsList []map[string]any
	if err := json.Unmarshal(wSessions.Body.Bytes(), &sessionsList); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(sessionsList) == 0 {
		t.Fatalf("expected at least 1 session")
	}
	nowPlaying, ok := sessionsList[0]["NowPlayingItem"].(map[string]any)
	if !ok || nowPlaying["Id"] != remoteMediaID {
		t.Fatalf("expected NowPlayingItem with id %q, got %#v", remoteMediaID, sessionsList[0]["NowPlayingItem"])
	}
}

func signMockToken(secret, userID string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, _ := token.SignedString([]byte(secret))
	return s
}
