package rule

import (
	"strings"
	"testing"
)

// 本文件：规则 JS 作用域隔离的回归测试。
//
// 同一个 goja Runtime 会被一个书源的所有规则 JS 复用，而顶层 let/const 会留在
// 全局词法环境里。若不做隔离，「前一个脚本声明过的名字，后一个脚本再声明」就会
// 报 `SyntaxError: Identifier 'x' has already been declared`。
//
// 光遇聚合正好踩中：搜索列表规则是 `const { key, tab, ... } = res`，
// 单本书的 bookUrl 规则是 `let tab = result.tab || '小说'`。失败发生在「逐条取
// 字段」阶段且被 continue 跳过，于是表现为「搜索有结果但一条都读不出来」。
// legado 用的 Rhino 对顶层 let 更宽松，所以同一书源在阅读 App 里正常。

// TestRuleJSScopeIsIsolated 先后执行的规则 JS 不应因顶层 let/const 重名而失败。
func TestRuleJSScopeIsIsolated(t *testing.T) {
	r := NewJSRunner(JSConfig{})

	// 第一个脚本：解构声明（搜索列表规则的写法）
	v, err := r.Run(nil, `const { tab, key } = {tab:'A', key:'K'}; tab + key`, nil, "")
	if err != nil {
		t.Fatalf("第一个脚本失败: %v", err)
	}
	if got := anyToString(v); got != "AK" {
		t.Fatalf("第一个脚本返回 %q", got)
	}

	// 第二个脚本重名声明（bookUrl 规则的写法）：不得报「已声明」
	for i := 0; i < 3; i++ {
		v, err = r.Run(nil, `let tab = 'B'; let book_id = 'x' + tab; book_id`, nil, "")
		if err != nil {
			t.Fatalf("第 %d 次重名声明冲突: %v", i+2, err)
		}
		if got := anyToString(v); got != "xB" {
			t.Fatalf("第 %d 次返回 %q", i+2, got)
		}
	}
}

// TestRuleJSKeepsCompletionValue 包块不能吃掉「最后一条语句的值」——
// 书源的 URL 规则几乎都靠这个完成值返回结果。
func TestRuleJSKeepsCompletionValue(t *testing.T) {
	r := NewJSRunner(JSConfig{})

	v, err := r.Run(nil, "`data:;base64,AAAA,{\"type\":\"gysearch\"}`", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); !strings.HasPrefix(got, "data:;base64,") {
		t.Fatalf("模板字面量的完成值丢了: %q", got)
	}

	// 末尾带行注释也不能把收尾的块注释掉
	v, err = r.Run(nil, "let a = 'ok'; a // 末尾注释", nil, "")
	if err != nil {
		t.Fatalf("带末尾注释的脚本失败: %v", err)
	}
	if got := anyToString(v); got != "ok" {
		t.Fatalf("返回 %q", got)
	}
}

// TestRuleJSThisStaysGlobal 包块后 this 仍是全局对象，
// 书源的 this.getVariable / this.BaseUrl / this.request 才照常可用。
func TestRuleJSThisStaysGlobal(t *testing.T) {
	r := NewJSRunner(JSConfig{})
	v, err := r.Run(nil, `String(this === globalThis) + '|' + typeof java.ajax`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != "true|function" {
		t.Fatalf("this 语义被破坏: %q", got)
	}
}

// TestRuleJSCanReadGlobalsFromJSLib 规则 JS 仍能读到 jsLib 定义的全局函数
// 与顶层 lexical 绑定（hosts 这类）。
func TestRuleJSCanReadGlobalsFromJSLib(t *testing.T) {
	r := NewJSRunner(JSConfig{
		JSLib: `let hosts = ['https://v1.example.com'];
function BaseUrl(){ return hosts[0] }
function pick(k){ return k + '@' + BaseUrl() }`,
	})
	if err := r.JSLibErr(); err != nil {
		t.Fatalf("jsLib 失败: %v", err)
	}
	v, err := r.Run(nil, `{ let x = 'A'; pick(x) }`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := anyToString(v); got != "A@https://v1.example.com" {
		t.Fatalf("规则读不到 jsLib 的全局: %q", got)
	}
}
