package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/service/cloud115"
)

// 115 开放平台（openapi）驱动测试：mock proapi 的列目录/直链接口。

func newOpenAPI115TestProvider(t *testing.T, handler http.HandlerFunc) (*openAPI115Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	old := cloud115.ProAPIBase
	cloud115.ProAPIBase = srv.URL
	t.Cleanup(func() { cloud115.ProAPIBase = old; srv.Close() })
	p := NewOpenAPI115("100195125", "token-abc", "refresh-xyz")
	return p, srv
}

func Test115OpenAPIListAndResolve(t *testing.T) {
	p, _ := newOpenAPI115TestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/ufile/files":
			if r.Header.Get("Authorization") != "Bearer token-abc" {
				t.Errorf("missing authorization header")
			}
			if r.URL.Query().Get("cid") != "0" {
				t.Errorf("bad cid %q", r.URL.Query().Get("cid"))
			}
			w.Write([]byte(`{"state":true,"data":[
				{"fid":"100","cid":"100","fc":"0","fn":"Movies","fs":0,"pc":""},
				{"fid":"200","fc":"1","fn":"Inception.mkv","fs":456,"pc":"pick200"}]}`))
		case "/open/ufile/downurl":
			if r.Header.Get("Authorization") != "Bearer token-abc" {
				t.Errorf("missing authorization header")
			}
			_ = r.ParseForm()
			if r.PostFormValue("pick_code") != "pick200" {
				t.Errorf("bad pick_code %q", r.PostFormValue("pick_code"))
			}
			w.Write([]byte(`{"state":true,"data":{"200":{"file_name":"Inception.mkv","url":{"url":"https://cdn.115/x.mkv?t=1"}}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})

	entries, err := p.List(context.Background(), "0")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries: %#v", entries)
	}
	if !entries[0].IsDir || entries[0].ID != "100" || entries[0].Name != "Movies" {
		t.Fatalf("dir entry wrong: %#v", entries[0])
	}
	if entries[1].IsDir || entries[1].PickCode != "pick200" || entries[1].Size != 456 {
		t.Fatalf("file entry wrong: %#v", entries[1])
	}
	link, err := p.Resolve(context.Background(), "pick200")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if link.URL != "https://cdn.115/x.mkv?t=1" {
		t.Fatalf("bad url: %s", link.URL)
	}
	if link.Proxy {
		t.Fatalf("115 openapi should default to 302 (no proxy)")
	}
}

func Test115OpenAPIListPaginates(t *testing.T) {
	p, _ := newOpenAPI115TestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/ufile/files" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100
		}
		// 首页返回满页，之后返回 1 条：驱动按 offset/limit 翻页直到短页
		count := limit
		if offset > 0 {
			count = 1
		}
		items := make([]string, 0, count)
		for i := 0; i < count; i++ {
			n := offset + i
			items = append(items, `{"fid":"`+strconv.Itoa(n)+`","fc":"1","fn":"Movie.`+padZero(n)+`.mkv","fs":1,"pc":"pick`+strconv.Itoa(n)+`"}`)
		}
		w.Write([]byte(`{"state":true,"data":[` + strings.Join(items, ",") + `]}`))
	})

	entries, err := p.List(context.Background(), "0")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// List 使用文档上限 limit=1150：首页 1150 条 + 短页 1 条
	if len(entries) != 1151 {
		t.Fatalf("entries = %d, want 1151", len(entries))
	}
	if entries[1150].ID != "1150" || entries[1150].PickCode != "pick1150" {
		t.Fatalf("last entry wrong: %#v", entries[1150])
	}
}

func Test115OpenAPIErrorSurfaced(t *testing.T) {
	p, _ := newOpenAPI115TestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/ufile/downurl" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"state":false,"message":"文件不存在"}`))
	})
	_, err := p.Resolve(context.Background(), "pickX")
	if err == nil || !strings.Contains(err.Error(), "文件不存在") {
		t.Fatalf("want upstream error surfaced, got %v", err)
	}
}

func Test115OpenAPIPingRequiresToken(t *testing.T) {
	p := NewOpenAPI115("100195125", "", "")
	if err := p.Ping(context.Background()); err == nil {
		t.Fatalf("ping without token should fail")
	}
}

func padZero(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}
