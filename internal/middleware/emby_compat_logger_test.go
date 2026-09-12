package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestEmbyCompatLoggerRecordsClientAndUnimplementedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zap.InfoLevel)
	router := gin.New()
	router.Use(EmbyCompatLogger(zap.New(core), func(path string) bool {
		return strings.HasPrefix(path, "/emby/") || strings.HasPrefix(path, "/System/")
	}))
	router.GET("/emby/System/Info/Public", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ServerName": "MeBox"})
	})
	router.NoRoute(func(c *gin.Context) {
		c.Status(http.StatusNotFound)
	})

	successReq := httptest.NewRequest(http.MethodGet, "/emby/System/Info/Public?api_key=do-not-log", nil)
	successReq.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Infuse", Device="Apple TV", DeviceId="device-1", Version="8.2", UserId="user-1", Token="secret-token"`)
	successResp := httptest.NewRecorder()
	router.ServeHTTP(successResp, successReq)
	if successResp.Code != http.StatusOK {
		t.Fatalf("success status = %d, want 200", successResp.Code)
	}

	missingReq := httptest.NewRequest(http.MethodPost, "/Search/Hints", nil)
	missingReq.Header.Set("X-Emby-Token", "secret-token")
	missingResp := httptest.NewRecorder()
	router.ServeHTTP(missingResp, missingReq)
	if missingResp.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", missingResp.Code)
	}

	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("logged entries = %d, want 2: %#v", len(entries), entries)
	}

	success := entries[0].ContextMap()
	if success["path"] != "/emby/System/Info/Public" {
		t.Fatalf("success path = %#v", success["path"])
	}
	if success["client"] != "Infuse" || success["device"] != "Apple TV" || success["device_id"] != "device-1" {
		t.Fatalf("client fields not parsed: %#v", success)
	}
	if success["unimplemented"] != false {
		t.Fatalf("success unimplemented = %#v, want false", success["unimplemented"])
	}
	if _, ok := success["token"]; ok {
		t.Fatalf("token must not be logged: %#v", success)
	}
	if success["query_keys"] == nil {
		t.Fatalf("query_keys missing: %#v", success)
	}
	if strings.Contains(entries[0].Message, "secret-token") {
		t.Fatalf("token leaked in message: %q", entries[0].Message)
	}

	missing := entries[1].ContextMap()
	if entries[1].Message != "emby API not implemented" {
		t.Fatalf("missing message = %q", entries[1].Message)
	}
	if missing["path"] != "/Search/Hints" || missing["unimplemented"] != true {
		t.Fatalf("missing fields = %#v", missing)
	}
	if missing["method"] != http.MethodPost {
		t.Fatalf("missing method = %#v", missing["method"])
	}
}

func TestEmbyCompatLoggerSkipsUnrelatedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zap.InfoLevel)
	router := gin.New()
	router.Use(EmbyCompatLogger(zap.New(core), func(string) bool { return false }))
	router.GET("/api/health", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer regular-token")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if logs.Len() != 0 {
		t.Fatalf("unrelated request should not be logged: %#v", logs.All())
	}
}

func TestEmbyCompatLoggerMarksSPAFallbackAsUnimplemented(t *testing.T) {
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zap.InfoLevel)
	router := gin.New()
	router.Use(EmbyCompatLogger(zap.New(core), func(string) bool { return false }))
	router.NoRoute(func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<html></html>"))
	})

	req := httptest.NewRequest(http.MethodGet, "/NewEmbyFeature/Test", nil)
	req.Header.Set("X-Emby-Token", "secret-token")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("logged entries = %d, want 1", len(entries))
	}
	if entries[0].Message != "emby API not implemented" {
		t.Fatalf("message = %q, want unimplemented warning", entries[0].Message)
	}
	if entries[0].ContextMap()["unimplemented"] != true {
		t.Fatalf("unimplemented field missing: %#v", entries[0].ContextMap())
	}
}
