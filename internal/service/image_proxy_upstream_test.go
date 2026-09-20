package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
)

func newUpstreamImageProxy(t *testing.T, allowedHosts ...string) *ImageProxy {
	t.Helper()
	proxy := NewImageProxy(&config.Config{Cache: config.CacheConfig{CacheDir: filepath.Join(t.TempDir(), "cache")}}, zap.NewNop())
	if len(allowedHosts) > 0 {
		proxy.SetAllowedRemoteHostsProvider(func() []string { return allowedHosts })
	}
	return proxy
}

func TestUpstreamImageFetchURLForwardsResizeToConfiguredMount(t *testing.T) {
	proxy := newUpstreamImageProxy(t, "192.168.1.50:8096")
	raw := "http://192.168.1.50:8096/emby/Items/abc/Images/Primary?api_key=secret"

	got := proxy.upstreamImageFetchURL(raw, imageResizeOptions{MaxWidth: 480, MaxHeight: 600, Quality: 82})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("maxWidth") != "480" || q.Get("maxHeight") != "600" || q.Get("quality") != "82" {
		t.Fatalf("forwarded query = %q, want maxWidth=480 maxHeight=600 quality=82", u.RawQuery)
	}
	if q.Get("api_key") != "secret" {
		t.Fatalf("api_key = %q, want the credential preserved", q.Get("api_key"))
	}
	if u.Path != "/emby/Items/abc/Images/Primary" {
		t.Fatalf("path = %q, want the item image path unchanged", u.Path)
	}

	// The default quality is applied when the client does not send one.
	got = proxy.upstreamImageFetchURL(raw, imageResizeOptions{MaxWidth: 160})
	if !strings.Contains(got, "quality="+strconv.Itoa(imageResizeDefaultQuality)) {
		t.Fatalf("url = %q, want the default encoding quality forwarded", got)
	}
}

func TestUpstreamImageFetchURLLeavesOtherUpstreamsAlone(t *testing.T) {
	proxy := newUpstreamImageProxy(t, "192.168.1.50:8096")
	opts := imageResizeOptions{MaxWidth: 480, MaxHeight: 600, Quality: 82}

	// Not a configured mount: TMDb/Douban do not honour these parameters, so the
	// proxy must keep fetching their originals and resize locally.
	tmdb := "https://image.tmdb.org/t/p/original/poster.jpg"
	if got := proxy.upstreamImageFetchURL(tmdb, opts); got != tmdb {
		t.Fatalf("url = %q, want %q unchanged", got, tmdb)
	}
	// Configured mount but no size requested: keep the full original.
	mount := "http://192.168.1.50:8096/emby/Items/abc/Images/Primary?api_key=secret"
	if got := proxy.upstreamImageFetchURL(mount, imageResizeOptions{}); got != mount {
		t.Fatalf("url = %q, want %q unchanged", got, mount)
	}
}

func TestRemoteImageCacheKeyIgnoresCredentialRotation(t *testing.T) {
	proxy := newUpstreamImageProxy(t)
	first := "http://192.168.1.50:8096/emby/Items/abc/Images/Primary?api_key=old-token"
	second := "http://192.168.1.50:8096/emby/Items/abc/Images/Primary?api_key=new-token"

	_, firstPath, _ := proxy.remoteImageCachePathsForValidated(first)
	_, secondPath, _ := proxy.remoteImageCachePathsForValidated(second)
	if firstPath != secondPath {
		t.Fatalf("token rotation changed the cache key: %q vs %q", firstPath, secondPath)
	}

	other := "http://192.168.1.50:8096/emby/Items/zzz/Images/Primary?api_key=new-token"
	_, otherPath, _ := proxy.remoteImageCachePathsForValidated(other)
	if otherPath == secondPath {
		t.Fatal("different items must not share a cache key")
	}

	// A generic `token` parameter can be part of a signed URL's identity, so it
	// must stay in the key. Only credential-ish names are dropped.
	signed := "http://cdn.example.com/img/1.jpg?token=aaa"
	signedOther := "http://cdn.example.com/img/1.jpg?token=bbb"
	_, signedPath, _ := proxy.remoteImageCachePathsForValidated(signed)
	_, signedOtherPath, _ := proxy.remoteImageCachePathsForValidated(signedOther)
	if signedPath == signedOtherPath {
		t.Fatal("generic token query parameters must remain part of the cache key")
	}
}

func TestRemoteImageFetchClientsPreferDirectForConfiguredMount(t *testing.T) {
	proxy := newUpstreamImageProxy(t, "192.168.1.50:8096")

	mountClients := proxy.remoteImageFetchClients("192.168.1.50:8096")
	if len(mountClients) != 2 || mountClients[0].name != "direct" || mountClients[1].name != "default" {
		t.Fatalf("mount client order = %+v, want direct first", clientNames(mountClients))
	}
	if mountClients[0].client != proxy.directClient {
		t.Fatal("direct fetches must reuse the shared no-proxy client")
	}

	cdnClients := proxy.remoteImageFetchClients("image.tmdb.org")
	if len(cdnClients) != 2 || cdnClients[0].name != "default" || cdnClients[1].name != "direct" {
		t.Fatalf("cdn client order = %+v, want default first", clientNames(cdnClients))
	}
}

func clientNames(clients []remoteImageFetchClient) []string {
	out := make([]string, 0, len(clients))
	for _, c := range clients {
		out = append(out, c.name)
	}
	return out
}

// TestServeRemoteImageForwardsResizeAndCachesPerSize is the regression test for
// sluggish artwork on mounted Emby libraries: MeBox used to download the remote
// original and scale it locally for every requested size. The size must now be
// forwarded to the mount, and each size must get its own cache entry.
func TestServeRemoteImageForwardsResizeAndCachesPerSize(t *testing.T) {
	var mu sync.Mutex
	queries := []url.Values{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEG)
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newUpstreamImageProxy(t, u.Host)
	raw := upstream.URL + "/emby/Items/abc/Images/Primary?api_key=secret"

	serve := func(t *testing.T, query string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/img?"+query, nil)
		if err := proxy.Serve(t.Context(), rec, req, raw); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		return rec
	}

	rec := serve(t, "maxWidth=480&maxHeight=600&quality=82")
	if got := rec.Body.Bytes(); !bytes.Equal(got, testJPEG) {
		t.Fatalf("body = %x, want the upstream image", got)
	}

	mu.Lock()
	first := append([]url.Values(nil), queries...)
	mu.Unlock()
	if len(first) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(first))
	}
	if first[0].Get("maxWidth") != "480" || first[0].Get("maxHeight") != "600" || first[0].Get("quality") != "82" {
		t.Fatalf("upstream query = %q, want the client's thumbnail request forwarded", first[0].Encode())
	}

	// Same size again: served from the disk cache, no second upstream call.
	serve(t, "maxWidth=480&maxHeight=600&quality=82")
	mu.Lock()
	if len(queries) != 1 {
		mu.Unlock()
		t.Fatalf("upstream calls = %d, want 1 after a cache hit", len(queries))
	}
	mu.Unlock()

	// A different size must be its own cache entry, not a re-use of the 480px file.
	serve(t, "maxWidth=160&quality=60")
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("upstream calls = %d, want 2 for two distinct sizes", len(queries))
	}
	if got := queries[1].Get("maxWidth"); got != "160" {
		t.Fatalf("second upstream maxWidth = %q, want 160", got)
	}
	if got := queries[1].Get("quality"); got != "60" {
		t.Fatalf("second upstream quality = %q, want 60", got)
	}
}
