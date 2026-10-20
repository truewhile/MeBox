package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// 远程条目自身没有媒体源时（例如把「整部剧」的 ID 当播放目标），远程 Emby 会回
// 500 + 一句 .NET 异常。异常原文不能直接丢给用户，但要能从日志里查得到。
func TestProxyVideoStreamOnLineHidesUpstreamException(t *testing.T) {
	const upstreamBody = "Unable to cast object of type 'MediaBrowser.Controller.Entities.TV.Series' to type 'MediaBrowser.Controller.Entities.IHasMediaSources'."

	var gotPath, gotStatic, gotRange, gotToken string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotStatic = req.URL.Query().Get("Static")
		gotRange = req.Header.Get("Range")
		gotToken = req.Header.Get("X-Emby-Token")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	svc := &EmbyRemoteService{stream: upstream.Client(), log: zap.NewNop()}
	cfg := &EmbyRemoteConfig{BaseURL: upstream.URL, Token: "tok-1"}

	req := httptest.NewRequest(http.MethodGet, "/api/stream/embyremote~acct~222679", nil)
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()

	err := svc.proxyVideoStreamOnLine(context.Background(), rec, req, cfg, "222679")
	if err == nil {
		t.Fatal("expected an error for upstream 500")
	}
	if gotPath != "/emby/Videos/222679/stream" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotStatic != "true" {
		t.Fatalf("Static = %q, want true", gotStatic)
	}
	if gotRange != "bytes=0-1023" {
		t.Fatalf("Range = %q, want forwarded range", gotRange)
	}
	if gotToken != "tok-1" {
		t.Fatalf("X-Emby-Token = %q", gotToken)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("nothing should be written to the client on upstream failure, got %q", rec.Body.String())
	}

	msg := err.Error()
	if strings.Contains(msg, "Unable to cast") || strings.Contains(msg, "MediaBrowser") {
		t.Fatalf("raw upstream exception must not leak to clients: %q", msg)
	}
	if !strings.Contains(msg, "无法播放该条目") {
		t.Fatalf("error should tell the user what to do, got %q", msg)
	}
}

// 4xx（例如远程条目被删除/无权限）保持原有措辞，带着上游说明便于排查。
func TestProxyVideoStreamOnLineKeepsClientErrorDetail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Item not found"))
	}))
	defer upstream.Close()

	svc := &EmbyRemoteService{stream: upstream.Client(), log: zap.NewNop()}
	cfg := &EmbyRemoteConfig{BaseURL: upstream.URL, Token: "tok-1"}

	req := httptest.NewRequest(http.MethodGet, "/api/stream/embyremote~acct~404", nil)
	err := svc.proxyVideoStreamOnLine(context.Background(), httptest.NewRecorder(), req, cfg, "404")
	if err == nil {
		t.Fatal("expected an error for upstream 404")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "Item not found") {
		t.Fatalf("client error detail should be kept, got %q", err.Error())
	}
}
