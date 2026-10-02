package rule

import (
	"strings"
	"testing"
)

// TestGetStringListURLFromJSArray URL 类规则（nextTocUrl / chapterUrl / coverUrl…）
// 由 JS 生成时，goja 导出的是 []any。早期实现只认 []string，JS 给的地址数组会被
// 静默丢掉，表现为「书源写了 nextTocUrl 却永远不翻页」。
func TestGetStringListURLFromJSArray(t *testing.T) {
	ar := NewAnalyzeRule()
	ar.SetContent(`{"total":399,"limit":100}`, "https://api.example.com/toc?limit=100&offset=0")
	ar.SetJSRunner(func(js string, result any) (any, error) {
		return NewJSRunner(JSConfig{BaseURL: "https://api.example.com/toc?limit=100&offset=0"}).Run(nil, js, result, "https://api.example.com/toc?limit=100&offset=0")
	})

	// JS 返回数组（真实书源就是这种写法：一次给出全部后续页）
	rule := `<js>['https://api.example.com/toc?offset=100','https://api.example.com/toc?offset=200']</js>`
	urls, err := ar.GetStringList(rule, nil, true)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(urls) != 2 {
		t.Fatalf("拿到 %d 个地址，期望 2（JS 数组被丢掉了）: %v", len(urls), urls)
	}
	if urls[0] != "https://api.example.com/toc?offset=100" || urls[1] != "https://api.example.com/toc?offset=200" {
		t.Fatalf("地址不对: %v", urls)
	}

	// 相对地址要按当前页绝对化
	ruleRel := `<js>['/toc?offset=300']</js>`
	urls, err = ar.GetStringList(ruleRel, nil, true)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "https://api.example.com/toc?offset=300") {
		t.Fatalf("相对地址没有绝对化: %v", urls)
	}
}
