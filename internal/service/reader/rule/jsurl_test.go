package rule

import (
	"strings"
	"testing"
)

// TestJavaValueStringMatchesJavaMapToString 书源正则依赖 Java 的 Map.toString 形态。
// 真实案例：拷贝漫画的 author 规则 `$.author##.*name=(.*?)\,.*##$1`，
// 期望拿到 `{name=岸本斉史, alias=…}`；Go 的 `map[...]` 形态匹配不到，作者会退化成一串 map 文本。
func TestJavaValueStringMatchesJavaMapToString(t *testing.T) {
	ar := NewAnalyzeRule()
	ar.SetContent(`{"author":[{"name":"岸本斉史","alias":"岸本齐史,キシモトマサ","path_word":"anbenqishi"}]}`, "https://api.example.com")

	got, err := ar.GetString(`$.author##.*name=(.*?)\,.*##$1`, nil, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != "岸本斉史" {
		t.Fatalf("author = %q，期望 岸本斉史（Java Map 形态没对上）", got)
	}

	// 对象/数组/标量的字符串形态
	str, err := ar.GetString(`$.author[0]`, nil, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	for _, want := range []string{"{", "name=岸本斉史", "alias=岸本齐史,キシモトマサ", "path_word=anbenqishi"} {
		if !strings.Contains(str, want) {
			t.Errorf("对象字符串 %q 缺少 %q", str, want)
		}
	}
	if strings.Contains(str, "map[") {
		t.Errorf("仍是 Go 的 map 形态: %q", str)
	}
}

// TestJavaValueStringScalars 标量与嵌套结构按 Java 习惯输出。
func TestJavaValueStringScalars(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "null"},
		{"x", "x"},
		{true, "true"},
		{float64(399), "399"},
		{float64(1.5), "1.5"},
		{[]any{float64(1), "a"}, "[1, a]"},
		{map[string]any{"b": float64(1), "a": "x"}, "{a=x, b=1}"},
		{map[string]any{"n": []any{"x", "y"}}, "{n=[x, y]}"},
	}
	for _, c := range cases {
		if got := javaValueString(c.in); got != c.want {
			t.Errorf("javaValueString(%#v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestJavaToURL legado JsURL 的四个属性（并兼容 Java Map 的 searchParams.get）。
func TestJavaToURL(t *testing.T) {
	r := NewJSRunner(JSConfig{})
	ar := NewAnalyzeRule()

	check := func(expr, want string) {
		t.Helper()
		v, err := r.Run(ar, expr, nil, "")
		if err != nil {
			t.Fatalf("%s 失败: %v", expr, err)
		}
		if got := anyToString(v); got != want {
			t.Errorf("%s = %q，期望 %q", expr, got, want)
		}
	}

	check(`java.toURL('https://api.example.com:8443/a/b?x=1&y=%E4%B8%AD%E6%96%87').host`, "api.example.com")
	check(`java.toURL('https://api.example.com:8443/a/b?x=1&y=%E4%B8%AD%E6%96%87').origin`, "https://api.example.com:8443")
	check(`java.toURL('https://api.example.com/a/b').origin`, "https://api.example.com")
	check(`java.toURL('https://api.example.com/a/b?x=1').pathname`, "/a/b")
	check(`java.toURL('https://api.example.com/a/b?x=1&y=%E4%B8%AD%E6%96%87').searchParams.x`, "1")
	check(`java.toURL('https://api.example.com/a/b?x=1&y=%E4%B8%AD%E6%96%87').searchParams.y`, "中文")
	check(`java.toURL('https://api.example.com/a/b?x=1').searchParams.get('x')`, "1")
	// 相对地址按 baseUrl 解析（对应 Java 的 URL(base, url)）
	check(`java.toURL('/next?p=2', 'https://api.example.com/a/b').origin`, "https://api.example.com")
	check(`java.toURL('/next?p=2', 'https://api.example.com/a/b').pathname`, "/next")
	// 不存在的参数给 null（对应 Java Map.get 返回 null）
	check(`String(java.toURL('https://api.example.com/a').searchParams.get('nope'))`, "null")
	// 非绝对地址必须报错（Java 抛 MalformedURLException），不能静默给一个空对象
	if _, err := r.Run(ar, `java.toURL('not-a-url').host`, nil, ""); err == nil {
		t.Error("非绝对地址应当报错")
	}
}
