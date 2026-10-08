package reader

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 本文件：拷贝系列轻小说源的端到端回归。
//
// 合成一个「照搬拷贝轻小说写法」的书源，一次覆盖四个修复点：
//  1. 搜索列表规则 `results.list.[*]`（legado 的 `.[*]` 形式）；
//  2. 目录规则用 `new Packages.java.util.LinkedHashMap()/ArrayList()` 拼装；
//  3. 正文规则用 `java.cacheFile(url)` 取整卷文本；
//  4. 正文是 GBK 文本——必须走 Raw 原始字节通道落盘，否则会被 UTF-8 解码成乱码。

func TestCopyLightNovelSourceEndToEnd(t *testing.T) {
	var srv *httptest.Server
	gbkBody, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("第一章 开始\n第二章 继续"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	// 搜索：Django REST 风格的 JSON。q_type/_update 参数照抄真实源。
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"results":{"list":[
			{"name":"转生成为史莱姆","path_word":"slime","author":[{"name":"作者甲"}],"cover":"https://img.example.com/1.jpg"}
		]}}`))
	})
	mux.HandleFunc("/api/book/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"results":{"book":{"name":"转生成为史莱姆","path_word":"slime"}}}`))
	})
	mux.HandleFunc("/api/volumes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"results":{"list":[{"name":"第一卷","id":7}]}}`))
	})
	mux.HandleFunc("/api/volume/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":200,"results":{"volume":{"txt_addr":"%s/api/txt/7","txt_encoding":"GBK"}}}`, srv.URL)
	})
	mux.HandleFunc("/api/txt/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(gbkBody)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	source := map[string]any{
		"bookSourceName": "合成拷贝轻小说源",
		"bookSourceUrl":  srv.URL,
		"enabled":        true,
		"enabledExplore": true,
		"searchUrl":      srv.URL + "/api/search?limit=20&q_type=&q={{key}}&_update=true",
		"ruleSearch": map[string]any{
			"bookList": "results.list.[*]",
			"name":     "$.name",
			"author":   "$.author[*].name",
			"coverUrl": "$.cover",
			"bookUrl":  srv.URL + "/api/book/{{$.path_word}}?_update=true",
		},
		"ruleBookInfo": map[string]any{
			"init":  "$.results.book",
			"name":  "$.name",
			"intro": "$.brief",
			"tocUrl": srv.URL + "/api/volumes?book={{$.path_word}}&_update=true",
		},
		"ruleToc": map[string]any{
			"chapterList": `<js>
function mk(n, u) { var m = new Packages.java.util.LinkedHashMap(); m.put('name', n); m.put('url', u); return m; }
var base = '` + srv.URL + `/api/volume/';
var out = new Packages.java.util.ArrayList();
var vols = JSON.parse(result).results.list;
for (var i = 0; i < vols.length; i++) { out.add(mk(vols[i].name, base + vols[i].id + '?_update=true')); }
out;</js>`,
			"chapterName": "$.name",
			"chapterUrl":  "$.url",
		},
		"ruleContent": map[string]any{
			"content": `@js:java.cacheFile(java.getString('$..txt_addr'))`,
		},
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}

	svc, _ := newLoginTestService(t)
	svc.cfg.Cache.CacheDir = t.TempDir()

	res := svc.SmokeSource(t.Context(), string(raw), "史莱姆")
	if !res.OK {
		t.Fatalf("链路失败于 %s: %s\n%s", res.FailedAt, res.Error, joinLogs(res))
	}
	if res.SearchHits != 1 {
		t.Fatalf("搜索命中=%d, want 1（`.[*]` 路径修复）", res.SearchHits)
	}
	if res.Chapters != 1 {
		t.Fatalf("章节数=%d, want 1（Packages 修复）", res.Chapters)
	}
	if res.ContentLen == 0 {
		t.Fatalf("正文长度=0（cacheFile/Raw 修复未生效）\n%s", joinLogs(res))
	}
	if !strings.Contains(joinLogs(res), "第一章") {
		t.Fatalf("正文未正确解码（GBK 落盘被 utf-8 破坏）\n%s", joinLogs(res))
	}
}

func joinLogs(res *SmokeChainResult) string {
	var b strings.Builder
	for _, l := range res.Logs {
		fmt.Fprintf(&b, "[%s/%s] %s\n", l.Stage, l.Level, l.Message)
	}
	return b.String()
}
