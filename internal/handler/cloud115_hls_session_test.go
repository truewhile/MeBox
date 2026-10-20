package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/service"
)

// 云 HLS 分片代理的错误映射：
//   - 会话不存在/过期要给前端可判断的 code，前端据此重拉 master.m3u8 建会话续播，
//     而不是当成播放失败直接退回本地转码；
//   - 客户端主动断开（seek、切清晰度、关页面取消在途请求）不是网关故障，记 499，
//     与 nginx 的口径一致。以前统一记 502，既污染监控又会让前端拿到误导性错误。
func newCloud115HLSSessionTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := &service.Container{
		Cloud115: service.NewCloud115PlaybackService(&config.Config{}, zap.NewNop(), nil, nil),
	}
	router := gin.New()
	router.GET("/api/cloud115/hls/:session/:key", cloud115HLSSessionHandler(svc))
	return router
}

func TestCloud115HLSSessionUnknownSessionReturnsCodedNotFound(t *testing.T) {
	router := newCloud115HLSSessionTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/cloud115/hls/unknown-session/abc?media_id=media-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hls_session_not_found") {
		t.Fatalf("expected machine-readable code in body, got %s", rec.Body.String())
	}
}

func TestCloud115HLSSessionClientAbortReturns499(t *testing.T) {
	router := newCloud115HLSSessionTestRouter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/cloud115/hls/unknown-session/abc?media_id=media-1", nil).
		WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 499 {
		t.Fatalf("status = %d, want 499 (client closed request); body=%s", rec.Code, rec.Body.String())
	}
}
