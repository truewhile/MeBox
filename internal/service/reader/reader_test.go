package reader

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

const e2eBookHTML = `<!DOCTYPE html>
<html><body>
<div class="box" id="main">
  <div class="item"><h3><a href="/book/1">斗破苍穹</a></h3><span class="author">天蚕土豆</span></div>
  <div class="item"><h3><a href="/book/2">凡人修仙传</a></h3><span class="author">忘语</span></div>
</div>
</body></html>`

const e2eBookInfoHTML = `<html><body>
<div class="info"><h1>斗破苍穹</h1><span class="author">天蚕土豆</span>
<p class="intro">三十年河东三十年河西</p>
<a class="toc" href="/book/1/toc.html">查看目录</a></div>
</body></html>`

const e2eTocHTML = `<html><body>
<ul class="chapters">
<li class="vol">第一卷</li>
<li><a href="/book/1/c1.html">第一章 陨落的天才</a></li>
<li><a href="/book/1/c2.html">第二章 斗气大陆</a></li>
</ul>
</body></html>`

const e2eContentHTML = `<html><body><div id="content">  魂殿来犯，<br>萧炎浴火重生。  </div>
<a class="next" href="/book/1/c1_2.html">下一页</a></body></html>`

const e2eContentPage2HTML = `<html><body><div id="content">少女微凉的手掌传来。</div></body></html>`

// e2eServer 模拟一个完整的书源站点：搜索/详情/目录/正文（含 nextContentUrl 翻页）。
func e2eServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/"):
			_, _ = w.Write([]byte(e2eBookHTML))
		case strings.HasPrefix(r.URL.Path, "/book/1/toc"):
			_, _ = w.Write([]byte(e2eTocHTML))
		case strings.HasPrefix(r.URL.Path, "/book/1/c1_2"):
			_, _ = w.Write([]byte(e2eContentPage2HTML))
		case strings.HasPrefix(r.URL.Path, "/book/1/c"):
			_, _ = w.Write([]byte(e2eContentHTML))
		case strings.HasPrefix(r.URL.Path, "/book/"):
			_, _ = w.Write([]byte(e2eBookInfoHTML))
		default:
			http.NotFound(w, r)
		}
	}))
}

// e2eSource 对齐 legado 书源 JSON 结构，覆盖 jsoup 全部四段规则。
func e2eSourceJSON(server string) string {
	return fmt.Sprintf(`{
  "bookSourceUrl": %q,
  "bookSourceName": "测试源",
  "bookSourceType": 0,
  "searchUrl": "%s/search/{{key}}/1.html",
  "ruleSearch": {
    "bookList": "class.item",
    "name": "tag.h3@tag.a@text",
    "bookUrl": "tag.h3@tag.a@href",
    "author": "class.author@text"
  },
  "ruleBookInfo": {
    "name": "class.info@tag.h1@text",
    "author": "class.info@class.author@text",
    "intro": "class.info@class.intro@text",
    "tocUrl": "class.info@class.toc@href"
  },
  "ruleToc": {
    "chapterList": "class.chapters@tag.li",
    "chapterName": "tag.a@text",
    "chapterUrl": "tag.a@href",
    "isVolume": "tag.a@text"
  },
  "ruleContent": {
    "content": "id.content@textNodes",
    "nextContentUrl": "class.next@href"
  }
}`, server, server)
}

// newE2EEngine 执行与 ReaderService 相同的链路（不含 DB）：
// ParseAnalyzeUrl → execute → AnalyzeRule。
type e2eEngine struct {
	server string
	client *http.Client
}

func (e *e2eEngine) fetch(t *testing.T, urlRule, key string, page int) (*rule.AnalyzeRule, string) {
	t.Helper()
	req, err := rule.ParseAnalyzeUrl(urlRule, key, page, e.server)
	if err != nil {
		t.Fatal(err)
	}
	if req.Unsupported != nil {
		t.Fatalf("unsupported: %v", req.Unsupported)
	}
	httpReq, _ := http.NewRequest("GET", req.URL, nil)
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := e.client.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	ar := rule.NewAnalyzeRule()
	ar.SetContent(string(body), resp.Request.URL.String())
	return ar, resp.Request.URL.String()
}

func TestEndToEndSourceChain(t *testing.T) {
	srv := e2eServer()
	defer srv.Close()

	bs, err := ParseBookSource(e2eSourceJSON(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	engine := &e2eEngine{server: srv.URL, client: srv.Client()}

	// ── 搜索 ──
	ar, _ := engine.fetch(t, SPtr(bs.SearchURL), "斗罗", 1)
	sr := bs.RuleSearch
	els, err := ar.GetElements(SPtr(sr.BookList))
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 2 {
		t.Fatalf("search elements = %d", len(els))
	}
	name, _ := ar.GetString(SPtr(sr.Name), els[0], false)
	if name != "斗破苍穹" {
		t.Fatalf("search name = %q", name)
	}
	bookURL, _ := ar.GetString(SPtr(sr.BookURL), els[0], true)
	if !strings.Contains(bookURL, "/book/1") {
		t.Fatalf("bookURL = %q", bookURL)
	}

	// ── 详情 ──
	ar, _ = engine.fetch(t, bookURL, "", 0)
	bir := bs.RuleBookInfo
	if got, _ := ar.GetString(SPtr(bir.Name), nil, false); got != "斗破苍穹" {
		t.Fatalf("info name = %q", got)
	}
	if got, _ := ar.GetString(SPtr(bir.Intro), nil, false); got != "三十年河东三十年河西" {
		t.Fatalf("info intro = %q", got)
	}
	tocURL, _ := ar.GetString(SPtr(bir.TocURL), nil, true)
	if !strings.Contains(tocURL, "/book/1/toc") {
		t.Fatalf("tocURL = %q", tocURL)
	}

	// ── 目录 ──
	ar, _ = engine.fetch(t, tocURL, "", 0)
	tr := bs.RuleToc
	chEls, err := ar.GetElements(SPtr(tr.ChapterList))
	if err != nil {
		t.Fatal(err)
	}
	if len(chEls) != 3 {
		t.Fatalf("chapters = %d", len(chEls))
	}
	var chapters []TocChapter
	for i, el := range chEls {
		title, _ := ar.GetString(SPtr(tr.ChapterName), el, false)
		if title == "" {
			continue // 卷名行没有 <a>，取不到标题（服务层同样跳过）
		}
		url, _ := ar.GetString(SPtr(tr.ChapterURL), el, true)
		chapters = append(chapters, TocChapter{Index: i, Title: title, URL: url})
	}
	if len(chapters) != 2 {
		t.Fatalf("chapters = %d", len(chapters))
	}
	if chapters[0].Title != "第一章 陨落的天才" {
		t.Fatalf("chapter0 = %+v", chapters[0])
	}

	// ── 正文（含 nextContentUrl 翻页合并） ──
	var parts []string
	url := chapters[0].URL
	for i := 0; i < 5; i++ {
		ar, finalURL := engine.fetch(t, url, "", 0)
		list, err := ar.GetStringList(SPtr(bs.RuleContent.Content), nil, false)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, strings.Join(list, "\n"))
		next, _ := ar.GetString(SPtr(bs.RuleContent.NextContentURL), nil, true)
		if next == "" || next == url || next == finalURL {
			break
		}
		url = next
	}
	content := strings.Join(parts, "\n")
	if !strings.Contains(content, "萧炎浴火重生") || !strings.Contains(content, "少女微凉的手掌") {
		t.Fatalf("content = %q", content)
	}
}

// TestGetContentForBookEmptyContentFails 书源一条内容都没返回时不能当成功下发。
//
// 回归：聚合类书源把七条线路全试完会返回空串（光遇聚合的 request() 就是这么写的），
// 以前这种空正文会当成功下发，前端渲染成一张白页并缓存下来，读者只能干等。
func TestGetContentForBookEmptyContentFails(t *testing.T) {
	svc, bookID := setupTextChapterBook(t, "")

	_, err := svc.GetContentForBook(t.Context(), "u1", bookID, 0)
	if err == nil {
		t.Fatal("空正文应当报错，实际当成功下发了")
	}
	if !strings.Contains(err.Error(), "正文为空") {
		t.Fatalf("错误信息 = %q，期望提示正文为空", err.Error())
	}
}

// ─── 纯函数测试：导入识别 / 搜索合并 ────────────────────────────────────────

func TestParseSourcePayload(t *testing.T) {
	// 数组
	arr := `[{"bookSourceUrl":"http://a.com","bookSourceName":"A"},{"bookSourceUrl":"http://b.com","bookSourceName":"B"}]`
	if got := ParseSourcePayload(arr); len(got) != 2 {
		t.Fatalf("array payload = %d", len(got))
	}
	// 单对象
	single := `{"bookSourceUrl":"http://a.com","bookSourceName":"A"}`
	if got := ParseSourcePayload(single); len(got) != 1 {
		t.Fatalf("single payload = %d", len(got))
	}
	// Base64
	b64 := base64StdEncode(single)
	if got := ParseSourcePayload(b64); len(got) != 1 {
		t.Fatalf("base64 payload = %d", len(got))
	}
	// UTF-8 BOM（Windows 导出文件常见）
	if got := ParseSourcePayload("\uFEFF" + arr); len(got) != 2 {
		t.Fatalf("bom array payload = %d", len(got))
	}
}

func TestMergeSearchResults(t *testing.T) {
	hit := func(name, author, origin string) searchHit {
		return searchHit{book: SearchBook{
			Name: name, Author: author,
			Origins: []SearchOrigin{{OriginName: origin, BookURL: "u"}},
		}}
	}
	hits := []searchHit{
		hit("遮天", "辰东", "源C"),
		hit("斗破苍穹", "天蚕土豆", "源B"),
		hit("斗破苍穹", "天蚕土豆", "源A"),
		hit("斗罗大陆", "唐家三少", "源D"),
	}
	merged := mergeSearchResults(hits, "斗")
	if len(merged) != 3 {
		t.Fatalf("merged = %d", len(merged))
	}
	// 精确命中「斗破苍穹」应排第一（tier 0），且合并两源
	if merged[0].Name != "斗破苍穹" || len(merged[0].Origins) != 2 {
		t.Fatalf("first = %+v", merged[0])
	}
}

func base64StdEncode(s string) string {
	return base64EncodeStr(s)
}
