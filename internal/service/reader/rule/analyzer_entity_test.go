package rule

import (
	"strings"
	"testing"
)

// 规则结果里的地址带查询串。html.UnescapeString 会按 HTML5 旧式简写把 &para 吃成
// 「¶」，参数名变成 ¶ 之后上游按缺参数返回空页面——光遇聚合的段评面板一片空白
// 就是这个原因（见 reader/comment.go、reader/browser_panel.go）。
func TestUnescapeRuleEntitiesKeepsQueryString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "段评地址里的 &para 不能被当成实体",
			in:   "https://v1.qingtian618.com/get_para_review?item_id=X&para=1&source=QQ%E9%98%85%E8%AF%BB",
			want: "https://v1.qingtian618.com/get_para_review?item_id=X&para=1&source=QQ%E9%98%85%E8%AF%BB",
		},
		{
			name: "&not 开头的参数名同样不能被吃掉",
			in:   "https://example.com/api?book=1&notify=1",
			want: "https://example.com/api?book=1&notify=1",
		},
		{
			name: "带分号的实体照旧还原",
			in:   "url?a=1&amp;b=2&amp;c=3",
			want: "url?a=1&b=2&c=3",
		},
		{
			name: "数字实体照旧还原",
			in:   "&#39;引号&#39; &#x27;x&#x27; &#169;",
			want: "'引号' 'x' ©",
		},
		{
			name: "文字里的旧式简写（后面不是 = 或字母数字）照旧还原",
			in:   "第一段&nbsp第二段&copy 2026&times",
			want: "第一段\u00a0第二段© 2026×",
		},
		{
			name: "不是实体的 & 原样保留",
			in:   "a & b &foo; c",
			want: "a & b &foo; c",
		},
		{
			name: "不含 & 的字符串直接放行",
			in:   "魔法大陆。",
			want: "魔法大陆。",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := unescapeRuleEntities(c.in); got != c.want {
				t.Fatalf("unescapeRuleEntities(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

// 端到端：走 GetString 的规则结果同样不能把段评地址里的 &para 吃掉
// （光遇聚合的正文规则是 <js>…</js>$.content 形态，最终取 $.content）。
func TestGetStringCommentURLKeepsParaParam(t *testing.T) {
	const url = "https://v1.qingtian618.com/get_para_review?item_id=YmlkPTYxNDc4MiZjaWQ9MQ&para=11&source=QQ%E9%98%85%E8%AF%BB"
	payload := `{"content":"<p>正文<comment ident=\"` + url + `\" count=\"39\" /></p>"}`

	a := NewAnalyzeRule()
	a.SetContent(payload, "https://v1.qingtian618.com")
	got, err := a.GetString("$.content", nil, false)
	if err != nil {
		t.Fatalf("GetString: %v", err)
	}
	if !strings.Contains(got, "&para=11") {
		t.Fatalf("段评地址里的 &para=11 被改写了: %q", got)
	}
	if strings.Contains(got, "\u00b6") {
		t.Fatalf("段评地址里出现了 ¶（HTML 旧式实体还原的产物）: %q", got)
	}
}
