package rule

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// 本文件对应 legado 的 Rhino JS 执行层（RhinoScriptEngine / evalJS），
// 用纯 Go 的 goja 实现。`java` 对象的函数在 bridge.go 中以 Go 原生实现，
// 函数名与 legado JsExtensions 保持一致（存量书源 JS 硬编码了这些名字）。

const (
	defaultJSTimeout = 10 * time.Second
	// programCacheSize 编译产物缓存上限（超出后整体清空，防无限增长）。
	programCacheSize = 256
)

var (
	programCacheMu sync.Mutex
	programCache   = map[string]*goja.Program{}
)

func compileCached(js string) (*goja.Program, error) {
	programCacheMu.Lock()
	defer programCacheMu.Unlock()
	if p, ok := programCache[js]; ok {
		return p, nil
	}
	p, err := goja.Compile(fmt.Sprintf("<js:%d>", len(js)), js, false)
	if err != nil {
		return nil, err
	}
	if len(programCache) >= programCacheSize {
		programCache = map[string]*goja.Program{}
	}
	programCache[js] = p
	return p, nil
}

// JSFetcher 由服务层注入：桥接函数发起网络请求用（走 MeBox 的 HTTP 客户端，
// 自动携带书源级请求头、UA、代理与重定向）。
type JSFetcher func(req *Request) (body string, finalURL string, code int, err error)

// JSConfig 构造 JS 运行时的配置。
type JSConfig struct {
	Fetch JSFetcher
	// SourceProps 注入为 JS 的 `source` 对象（书源 JSON 原样）。
	SourceProps map[string]any
	// Log 对应 java.log。
	Log func(msg string)
	// Timeout 单次 JS 执行超时，默认 10s。
	Timeout time.Duration
	// BaseURL 对应 evalJS 的 baseUrl 绑定。
	BaseURL string
	// Key / Page 搜索上下文绑定（{{key}}/{{page}} 在 JS 里的取值）。
	Key  string
	Page int
}

// JSRunner 是一个单协程使用的 JS 运行时（每个 AnalyzeRule 一个实例）。
type JSRunner struct {
	vm   *goja.Runtime
	cfg  JSConfig
	vars map[string]string // runner 级变量（URL 上下文 java.put/get）
}

// NewJSRunner 创建运行时并注入全局对象：cookie / cache / source。
func NewJSRunner(cfg JSConfig) *JSRunner {
	vm := goja.New()
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultJSTimeout
	}
	r := &JSRunner{vm: vm, cfg: cfg, vars: map[string]string{}}
	vm.Set("cookie", newCookieObject(vm))
	vm.Set("cache", newCacheObject(vm))
	if cfg.SourceProps != nil {
		vm.Set("source", cfg.SourceProps)
	} else {
		vm.Set("source", vm.NewObject())
	}
	return r
}

// ForAnalyzer 返回绑定到指定解析器的执行函数（java.getString 等规则回调
// 会桥回该解析器，对应 legado 中 AnalyzeRule 自身实现 JsExtensions）。
func (r *JSRunner) ForAnalyzer(a *AnalyzeRule) func(js string, result any) (any, error) {
	return func(js string, result any) (any, error) {
		return r.Run(a, js, result, "")
	}
}

// RunWithBaseURL 与 Run 相同，但覆盖本次执行的 baseUrl 绑定（AnalyzeUrl 用）。
func (r *JSRunner) RunWithBaseURL(a *AnalyzeRule, js string, result any, baseURL string) (any, error) {
	return r.Run(a, js, result, baseURL)
}

// Run 执行一段书源 JS。绑定集合对应 legado evalJS：
// java / cookie / cache / source / book / result / baseUrl / chapter / title /
// src / page / key / nextChapterUrl。
func (r *JSRunner) Run(a *AnalyzeRule, js string, result any, baseURL string) (any, error) {
	vm := r.vm
	// java 对象：每次执行重建（桥回当前解析器）
	vm.Set("java", newJavaObject(vm, r, a))
	// 上下文绑定
	if a != nil {
		vm.Set("book", map[string]any{"name": a.bookName})
		vm.Set("chapter", map[string]any{"title": a.chapterTitle})
		vm.Set("title", a.chapterTitle)
		if a.content != nil {
			vm.Set("src", resultString(a.content))
		} else {
			vm.Set("src", nil)
		}
	} else {
		vm.Set("book", nil)
		vm.Set("chapter", nil)
		vm.Set("title", nil)
		vm.Set("src", nil)
	}
	base := baseURL
	if base == "" {
		base = r.cfg.BaseURL
		if a != nil && base == "" {
			base = a.baseUrl
		}
	}
	vm.Set("baseUrl", base)
	vm.Set("result", toJSValue(vm, result))
	if r.cfg.Key != "" {
		vm.Set("key", r.cfg.Key)
	} else {
		vm.Set("key", nil)
	}
	if r.cfg.Page > 0 {
		vm.Set("page", r.cfg.Page)
	} else {
		vm.Set("page", nil)
	}
	vm.Set("nextChapterUrl", nil)

	prog, err := compileCached(js)
	if err != nil {
		return nil, fmt.Errorf("JS 编译失败: %w", err)
	}
	timer := time.AfterFunc(r.cfg.Timeout, func() { vm.Interrupt("JS 执行超时") })
	defer timer.Stop()
	v, err := vm.RunProgram(prog)
	if err != nil {
		return nil, fmt.Errorf("JS 执行失败: %v", err)
	}
	return exportValue(v), nil
}

// exportValue 把 JS 返回值转为 Go 值（字符串/数值/映射/切片）。
func exportValue(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	switch v.ExportType() {
	case nil:
		return nil
	default:
		return v.Export()
	}
}

// toJSValue 把引擎内部结果转为可注入 JS 的值。
// Element 列表等 DOM 结果以序列化字符串传入（对应 Rhino 的 Java 对象字符串化）。
func toJSValue(vm *goja.Runtime, v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string, bool, int, int32, int64, float64, map[string]any, []any:
		return t
	case []string:
		arr := make([]any, len(t))
		for i, s := range t {
			arr[i] = s
		}
		return arr
	case *goja.Object:
		return t
	case *goja.Runtime:
		return nil
	default:
		return resultString(v)
	}
}

// newResponseObject 构造 Connection.Response / StrResponse 的 JS 等价物：
// body()/code()/url()/header(name)。
func newResponseObject(vm *goja.Runtime, body string, code int, finalURL string, headers map[string][]string) *goja.Object {
	o := vm.NewObject()
	mustSet := func(k string, v any) {
		if err := o.Set(k, v); err != nil {
			panic(vm.ToValue(err.Error()))
		}
	}
	mustSet("body", func(call goja.FunctionCall) goja.Value { return vm.ToValue(body) })
	mustSet("bodyStr", body)
	mustSet("code", func(call goja.FunctionCall) goja.Value { return vm.ToValue(code) })
	mustSet("url", func(call goja.FunctionCall) goja.Value { return vm.ToValue(finalURL) })
	mustSet("header", func(call goja.FunctionCall) goja.Value {
		name := strings.ToLower(strings.TrimSpace(toStringArg(call, 0)))
		for k, vs := range headers {
			if strings.ToLower(k) == name && len(vs) > 0 {
				return vm.ToValue(vs[0])
			}
		}
		return goja.Null()
	})
	return o
}

func toStringArg(call goja.FunctionCall, i int) string {
	if i >= len(call.Arguments) {
		return ""
	}
	return call.Arguments[i].String()
}

func stringArg(call goja.FunctionCall, i int) string {
	return strings.TrimSpace(toStringArg(call, i))
}

func stringArgOr(call goja.FunctionCall, i int, def string) string {
	if i >= len(call.Arguments) || goja.IsUndefined(call.Arguments[i]) || goja.IsNull(call.Arguments[i]) {
		return def
	}
	return call.Arguments[i].String()
}
