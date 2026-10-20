package reader

import "testing"

// TestRawSourceNeedsBrowser webView/webjs 类书源要在列表里先标出来
// （服务端没有无头浏览器，这类源注定跑不通，不该等用户点进去才报错）。
func TestRawSourceNeedsBrowser(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"webView 选项", `{"searchUrl":"https://a.com/s,{webView:true}"}`, true},
		{"webjs 规则", `{"ruleContent":{"content":"@webjs:getDom()"}}`, true},
		{"规则里提到 webJs", `{"ruleToc":{"chapterList":"<js>if(supportWebJs){}</js>"}}`, true},
		{"普通源", `{"searchUrl":"https://a.com/s?q={{key}}","ruleSearch":{"bookList":"$.list[*]"}}`, false},
		{"空", ``, false},
	}
	for _, c := range cases {
		if got := rawSourceNeedsBrowser(c.raw); got != c.want {
			t.Errorf("%s: rawSourceNeedsBrowser = %v，期望 %v", c.name, got, c.want)
		}
	}
}
