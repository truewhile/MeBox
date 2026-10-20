package rule

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/dop251/goja"
)

// newJSURLObject 构造 legado JsURL 的等效对象（java.toURL）。
//
// 对应 io.legado.app.utils.JsURL：它只暴露四个属性 ——
//
//	host         主机名
//	origin       "scheme://host[:port]"（默认端口不写）
//	pathname     路径（原样，未解码）
//	searchParams 查询参数（值已 URL 解码；同名只留最后一个，与 legado 一致）
//
// legado 返回的是 Java Map，Rhino 下书源会写 searchParams.get("x")，
// 而 goja 里更自然的是 searchParams.x，所以两者都支持。
func newJSURLObject(vm *goja.Runtime, raw, base string) (*goja.Object, error) {
	u, err := parseJSURL(raw, base)
	if err != nil {
		return nil, err
	}
	obj := vm.NewObject()
	origin := u.Scheme + "://" + u.Hostname()
	if port := u.Port(); port != "" {
		origin += ":" + port
	}
	_ = obj.Set("host", u.Hostname())
	_ = obj.Set("origin", origin)
	_ = obj.Set("pathname", u.EscapedPath())

	params := vm.NewObject()
	for key, values := range u.Query() {
		if len(values) == 0 {
			continue
		}
		_ = params.Set(key, values[len(values)-1])
	}
	// searchParams.get(name)：兼容 Java Map 的取法；不存在时给 null（对应 Java 的 null）。
	get := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Null()
		}
		v := params.Get(call.Arguments[0].String())
		if v == nil || goja.IsUndefined(v) {
			return goja.Null()
		}
		return v
	}
	_ = params.Set("get", get)
	_ = obj.Set("searchParams", params)
	return obj, nil
}

// parseJSURL 解析 java.toURL 的入参。base 非空时按基础地址解析相对路径
// （对应 Java 的 URL(base, url)）；解析不出绝对地址时报错（Java 会抛 MalformedURLException）。
func parseJSURL(raw, base string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("toURL: 地址为空")
	}
	if base != "" {
		b, err := url.Parse(strings.TrimSpace(base))
		if err != nil || !b.IsAbs() {
			return nil, fmt.Errorf("toURL: baseUrl 不是绝对地址: %s", base)
		}
		rel, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("toURL: %v", err)
		}
		u := b.ResolveReference(rel)
		if !u.IsAbs() || u.Host == "" {
			return nil, fmt.Errorf("toURL: 解析结果不是绝对地址: %s", raw)
		}
		return u, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("toURL: %v", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("toURL: 不是绝对地址: %s", raw)
	}
	return u, nil
}
