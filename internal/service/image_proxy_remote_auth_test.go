package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// TestImageProxyInjectsRemoteEmbyToken 覆盖封面空白问题：图片 URL 里签发时的
// api_key 已经失效，但账号当前 token 有效。代理回源必须用当前 token 覆盖旧值，
// 否则挂载 Emby 的封面会一直 401、下发占位图。
func TestImageProxyInjectsRemoteEmbyToken(t *testing.T) {
	const current = "current-token"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != current {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEG)
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newUpstreamImageProxy(t, u.Host)
	proxy.SetRemoteEmbyAuthProvider(func(context.Context, string) (string, bool) {
		return current, true
	})

	raw := upstream.URL + "/emby/Items/abc/Images/Primary?api_key=stale-token"
	rec := httptest.NewRecorder()
	if err := proxy.Serve(t.Context(), rec, httptest.NewRequest(http.MethodGet, "/api/img", nil), raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Body.Bytes(), testJPEG) {
		t.Fatalf("body = %x, want the upstream image served with the current token", rec.Body.Bytes())
	}
}

// TestImageProxyRefreshesRemoteEmbyTokenOn401 覆盖 token 在运行中被撤销的场景：
// 账号当前 token 也被上游拒绝时，代理应重新登录一次并重试，且只刷新一次。
func TestImageProxyRefreshesRemoteEmbyTokenOn401(t *testing.T) {
	const (
		stale = "stale-token"
		fresh = "fresh-token"
	)
	var mu sync.Mutex
	refreshes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != fresh {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEG)
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newUpstreamImageProxy(t, u.Host)
	proxy.SetRemoteEmbyAuthProvider(func(context.Context, string) (string, bool) {
		return stale, true
	})
	proxy.SetRemoteEmbyAuthRefresher(func(context.Context, string) (string, error) {
		mu.Lock()
		refreshes++
		mu.Unlock()
		return fresh, nil
	})

	raw := upstream.URL + "/emby/Items/abc/Images/Primary?api_key=" + stale
	rec := httptest.NewRecorder()
	if err := proxy.Serve(t.Context(), rec, httptest.NewRequest(http.MethodGet, "/api/img", nil), raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Body.Bytes(), testJPEG) {
		t.Fatalf("body = %x, want the upstream image served after re-auth", rec.Body.Bytes())
	}

	mu.Lock()
	got := refreshes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("refreshes = %d, want exactly 1 re-auth", got)
	}
}

// TestImageProxyLeavesNonEmbyHostsUntouched 确认新的凭据注入只作用于已配置的
// 远程 Emby 主机：普通图床（这里用 provider 返回 ok=false 模拟）不受影响。
func TestImageProxyDoesNotInjectForUnknownHost(t *testing.T) {
	var gotHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Emby-Token")
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEG)
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newUpstreamImageProxy(t, u.Host)
	proxy.SetRemoteEmbyAuthProvider(func(context.Context, string) (string, bool) {
		return "", false
	})

	raw := upstream.URL + "/img/cover.jpg"
	rec := httptest.NewRecorder()
	if err := proxy.Serve(t.Context(), rec, httptest.NewRequest(http.MethodGet, "/api/img", nil), raw); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "" {
		t.Fatalf("X-Emby-Token = %q, want no credential header for a non-Emby host", gotHeader)
	}
}
