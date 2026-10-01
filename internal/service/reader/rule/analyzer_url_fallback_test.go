package rule

import "testing"

// 聚合类书源（如「光遇聚合」）的搜索请求地址本身就是 data:;base64,... 参数信封。
// 规则取值为空时 legado 语义会回退 baseUrl；若此时的 baseUrl 不是 http(s) 地址，
// 回退结果会把封面/书址污染成非图片文本（前端 <img> 直接破图）。
func TestGetStringURLFallbackSkipsNonHTTPBaseURL(t *testing.T) {
	const payload = `{"data":[{"book_name":"全球高武","author":"老鹰吃小鸡"}]}`
	const envelope = "data:;base64,eyJrZXkiOiLlhajnkIPpq5jmraYiLCJ0YWIiOiLlsI/or7QiLCJzb3VyY2VzS2V5Ijoi5YWo6YOoIiwicGFnZSI6MSwiZGlzYWJsZWRfc291cmNlcyI6IjAifQ=="

	elementOf := func(t *testing.T, content, baseURL string) (*AnalyzeRule, any) {
		t.Helper()
		a := NewAnalyzeRule()
		a.SetContent(content, baseURL)
		els, err := a.GetElements("$.data")
		if err != nil {
			t.Fatalf("GetElements: %v", err)
		}
		if len(els) != 1 {
			t.Fatalf("GetElements: want 1 element, got %d", len(els))
		}
		return a, els[0]
	}

	t.Run("空取值不会把 data: 信封当地址返回", func(t *testing.T) {
		a, el := elementOf(t, payload, envelope)
		got, err := a.GetString("$.thumb_url", el, true)
		if err != nil {
			t.Fatalf("GetString: %v", err)
		}
		if got != "" {
			t.Fatalf("封面应为空，实际 = %q", got)
		}
	})

	t.Run("baseUrl 是 http(s) 时仍按 legado 语义回退", func(t *testing.T) {
		const base = "https://www.example.com/search?q=x"
		a, el := elementOf(t, payload, base)
		got, err := a.GetString("$.thumb_url", el, true)
		if err != nil {
			t.Fatalf("GetString: %v", err)
		}
		if got != base {
			t.Fatalf("want base fallback %q, got %q", base, got)
		}
	})

	t.Run("规则有取值时原样返回", func(t *testing.T) {
		const withCover = `{"data":[{"book_name":"书","thumb_url":"https://img.example.com/c.jpg"}]}`
		a, el := elementOf(t, withCover, envelope)
		got, err := a.GetString("$.thumb_url", el, true)
		if err != nil {
			t.Fatalf("GetString: %v", err)
		}
		if got != "https://img.example.com/c.jpg" {
			t.Fatalf("封面 = %q", got)
		}
	})
}
