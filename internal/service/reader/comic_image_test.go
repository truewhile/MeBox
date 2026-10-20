package reader

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 漫画源（聚合类漫画源就是这么写的）正文规则直接给 <img src="…"> 的 HTML。
// 整行当地址会被代理成一堆取不回的「图」，页面上全是破图；必须按标签抽 src。
func TestImageRefsInLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{"纯地址原样返回", "https://cdn.example.com/1.webp", []string{"https://cdn.example.com/1.webp"}},
		{"双引号 img", `<img src="https://cdn.example.com/2.webp">`, []string{"https://cdn.example.com/2.webp"}},
		{"单引号 img", `<img src='https://cdn.example.com/3.webp'/>`, []string{"https://cdn.example.com/3.webp"}},
		{"属性顺序无关", `<img data-x="1" src="https://cdn.example.com/4.webp" class="a">`, []string{"https://cdn.example.com/4.webp"}},
		{"一行多个标签", `<p><img src="/a/1.webp"><img src='/a/2.webp'></p>`, []string{"/a/1.webp", "/a/2.webp"}},
		{"img 没有 src 时跳过", `<img alt="空">`, nil},
		{"非 img 的 HTML 片段跳过", `</div>`, nil},
	}
	for _, c := range cases {
		got := imageRefsInLine(c.line)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// 端到端：图片型书籍走 GetContentForBook 时，Images 必须是真正的图片地址，
// 不能把 <img> 标签本身当成图片地址（那会让代理取回一堆 HTML，前端全破图）。
func TestGetContentForBookExtractsImageSrc(t *testing.T) {
	page := `<html><body><div class="pages">
<img src="/img/1.webp"><img src="/img/2.webp">
</div></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	svc, _ := newLoginTestService(t)
	ctx := t.Context()
	srcJSON := fmt.Sprintf(`{
  "bookSourceUrl": %q,
  "bookSourceName": "漫画测试源",
  "bookSourceType": 64,
  "ruleContent": { "content": "class.pages@html" }
}`, srv.URL)
	sourceID := importTestSource(t, svc, srcJSON, srv.URL)

	book := &model.ReaderBook{
		UserID:     "u1",
		Origin:     srv.URL,
		OriginName: "漫画测试源",
		BookURL:    srv.URL + "/book/1",
		Name:       "航海王",
		Type:       2, // 图片
	}
	if err := svc.repo.CreateBook(ctx, book); err != nil {
		t.Fatalf("创建书籍失败: %v", err)
	}
	if err := svc.SaveChapters(ctx, book.ID, []ChapterInput{
		{Index: 0, Title: "第1话", URL: srv.URL + "/book/1/c1.html"},
	}); err != nil {
		t.Fatalf("写入章节失败: %v", err)
	}
	_ = sourceID

	out, err := svc.GetContentForBook(ctx, "u1", book.ID, 0)
	if err != nil {
		t.Fatalf("取正文失败: %v", err)
	}
	if out.Type != "image" {
		t.Fatalf("类型 = %q，期望 image", out.Type)
	}
	if len(out.Images) != 2 {
		t.Fatalf("图片数 = %d，期望 2（%v）", len(out.Images), out.Images)
	}
	for i, img := range out.Images {
		// 书架维度返回的是签名代理地址（浏览器取图带不上防盗链头），
		// 把里面的原始地址解出来核对，顺便确认没有被塞进 HTML 标签。
		if !strings.HasPrefix(img, "/api/reader/media?") {
			t.Fatalf("第 %d 张不是代理地址：%q", i, img)
		}
		encoded := img[strings.Index(img, "u=")+2:]
		if end := strings.IndexByte(encoded, '&'); end >= 0 {
			encoded = encoded[:end]
		}
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("第 %d 张地址解不开：%q", i, img)
		}
		want := fmt.Sprintf("%s/img/%d.webp", srv.URL, i+1)
		if string(raw) != want {
			t.Fatalf("第 %d 张解析出 %q，期望 %q", i, raw, want)
		}
	}
}
