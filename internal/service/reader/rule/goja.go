package rule

import (
	"context"
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
	// SourceProps 注入为 JS 的 `source` 对象（书源 JSON 原样）；
	// 同时供 source.getLoginInfoMap 从 loginUi 的 default 初始化登录信息。
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
	// State 书源会话状态（变量/登录信息/Cookie）。nil 时用进程内 MemoryState。
	State SourceState
	// JSLib 书源 jsLib：在运行时创建后立即执行一次，
	// 其顶层函数与 lexical 绑定对该源后续所有 JS 可见（对应 legado SharedJsScope）。
	JSLib string
	// StateOnly 只构建会话状态与 jsLib 环境（登录交互用），
	// 不注入 book/result 等规则上下文。
	StateOnly bool
	// Browser 宿主浏览器实现（java.startBrowser / startBrowserAwait）。
	// nil 时这两个函数抛出不支持错误。
	Browser BrowserHost
	// CacheDir 书源文件缓存根目录（java.downloadFile / cacheFile 落盘用）。
	// 为空时这些函数抛出明确错误。
	CacheDir string
	// FetchBytes 拉原始字节（queryTTF 的 URL 形态、图片解密前的取图）。
	// 由服务层注入，复用书源 header / Cookie / 限速 / 重试。
	FetchBytes func(absURL string) ([]byte, error)
	// Ctx 本次执行的可取消上下文，透传给 BrowserHost 的等待。
	Ctx context.Context
}

// interruptGuard 是 JS 执行超时的看门狗：到期后中断虚拟机。
//
// 单独抽出来的原因是 java.startBrowserAwait 会阻塞等待用户在网页上操作
// （可达数分钟），这段时间必须暂停计时，否则默认 10s 的超时会在用户还没
// 点完 √ 时就把脚本打断。Pause/Resume 之间不计时，Resume 后重新起算完整
// 的一段预算——语义即「每一段自动执行各有一次预算，等人不算」。
type interruptGuard struct {
	vm      *goja.Runtime
	timeout time.Duration
	reason  string
	mu      sync.Mutex
	paused  int
	stopped bool
	timer   *time.Timer
}

func newInterruptGuard(vm *goja.Runtime, timeout time.Duration, reason string) *interruptGuard {
	g := &interruptGuard{vm: vm, timeout: timeout, reason: reason}
	g.start()
	return g
}

// start 起一个新的超时计时（调用方需持锁或处于初始化阶段）。
func (g *interruptGuard) start() {
	g.timer = time.AfterFunc(g.timeout, func() {
		g.mu.Lock()
		skip := g.stopped || g.paused > 0
		g.mu.Unlock()
		if !skip {
			g.vm.Interrupt(g.reason)
		}
	})
}

// Pause 暂停计时（等待人工操作），返回恢复函数。
func (g *interruptGuard) Pause() func() {
	g.mu.Lock()
	g.paused++
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	g.mu.Unlock()
	return g.Resume
}

// Resume 恢复计时。
func (g *interruptGuard) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused > 0 {
		g.paused--
	}
	if g.paused == 0 && !g.stopped && g.timer == nil {
		g.start()
	}
}

// Stop 永久停止计时（本次 JS 执行结束）。
func (g *interruptGuard) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopped = true
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// JSRunner 是一个单协程使用的 JS 运行时（每个 AnalyzeRule 一个实例）。
type JSRunner struct {
	vm    *goja.Runtime
	cfg   JSConfig
	vars  map[string]string // runner 级变量（URL 上下文 java.put/get）
	state SourceState
	// jsLibErr 记录 jsLib 执行失败原因（登录接口需要如实回报）。
	jsLibErr error
	// guard 当前执行的超时看门狗；java.startBrowserAwait 阻塞期间置为 nil。
	guardMu sync.Mutex
	guard   *interruptGuard
}

// setGuard 记录/清除当前执行的看门狗。
func (r *JSRunner) setGuard(g *interruptGuard) {
	r.guardMu.Lock()
	r.guard = g
	r.guardMu.Unlock()
}

// pauseTimeout 暂停当前 JS 执行的超时计时，返回恢复函数。
// 供 java.startBrowserAwait 在等待人工操作期间调用。
func (r *JSRunner) pauseTimeout() func() {
	r.guardMu.Lock()
	g := r.guard
	r.guardMu.Unlock()
	if g == nil {
		return func() {}
	}
	return g.Pause()
}

// fetch 执行一次桥接网络请求，等待期间暂停 JS 超时看门狗。
//
// 书源会把「线路重试」写进规则 JS：光遇聚合的 request() 会串行试 7 条线路，单条
// 线路最长可能等到客户端的 30s 超时。不暂停的话整条规则会被 10s 的 JS 超时打断，
// 而这个中断是 goja 的 Go panic，书源自己写的 try/catch 接不住——表现就是「线路
// 还在重试，接口已经 400」。java.startBrowserAwait 等待人工操作时用的是同一套暂停
// 机制。暂停只覆盖网络等待，纯 CPU 死循环仍然受 Timeout 约束。
func (r *JSRunner) fetch(req *Request) (string, string, int, error) {
	if r.cfg.Fetch == nil {
		return "", "", 0, ErrJsUnsupported
	}
	resume := r.pauseTimeout()
	defer resume()
	return r.cfg.Fetch(req)
}

// cacheNamespace cache 对象的命名空间：优先取书源 URL，退回 BaseURL。
func (c JSConfig) cacheNamespace() string {
	if c.SourceProps != nil {
		if v, ok := c.SourceProps["bookSourceUrl"]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return c.BaseURL
}

// NewJSRunner 创建运行时：注入全局对象 cookie / cache / source，并执行 jsLib。
func NewJSRunner(cfg JSConfig) *JSRunner {
	vm := goja.New()
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultJSTimeout
	}
	state := cfg.State
	if state == nil {
		state = NewMemoryState()
	}
	r := &JSRunner{vm: vm, cfg: cfg, vars: map[string]string{}, state: state}
	vm.Set("cookie", newCookieObject(vm, state))
	// cache 按书源命名空间隔离：namespace 取书源 URL，持久层落 CacheDir/reader-js-cache。
	vm.Set("cache", newCacheObject(vm, cfg.cacheNamespace(), cfg.CacheDir))
	// source 必须在 jsLib 之前注入：jsLib 的 getVariable/BaseUrl 依赖它。
	srcObj := newSourceObject(vm, state, cfg.SourceProps)
	vm.Set("source", srcObj)
	// java 也要在 jsLib 之前就位（jsLib 顶层可能引用 java.*）。
	r.installJava(nil)
	// Packages（Java 类命名空间）同样要在 jsLib 之前注入：不少书源在 jsLib 或
	// 目录/正文规则里用 `new Packages.java.util.LinkedHashMap()` 这类写法。
	r.installPackages()
	// 部分源把 source 的方法也当 java 成员用（同一 Kotlin 对象暴露两份）。
	r.loadJSLib()
	return r
}

// installPackages 注入 Java 风格类命名空间（Packages.java.util.*）。
//
// goja 是纯 JS 运行时，没有 Java。书源（尤其照搬 Java 教程写的）常用
// `new Packages.java.util.ArrayList()` / `LinkedHashMap` / `HashMap` 来拼
// 目录列表和请求头，缺了它整条规则直接 ReferenceError（拷贝漫画轻小说源的
// 目录规则就是这么写的）。
//
// 实现要点：容器用「真正的 JS 数组 / 对象」，方法用 Object.defineProperty 定义为
// 不可枚举属性。这样容器交给引擎侧（GetElements/GetString）时导出的是干净的
// 数组（[]any）或映射（map[string]any），而不会把 add/put 这些方法也当成元素或
// 请求头带出去。
func (r *JSRunner) installPackages() {
	if _, err := r.vm.RunString(packagesPreamble); err != nil {
		// 注入失败不影响其它规则，只是 Packages.* 不可用。
		r.jsLibErr = fmt.Errorf("Packages 环境初始化失败: %w", err)
	}
}

// packagesPreamble 定义全局 Packages（以及与之等价的 java.util / java.lang 常用类）。
const packagesPreamble = `
var Packages = (function () {
  function def(o, k, v) {
    Object.defineProperty(o, k, { value: v, enumerable: false, writable: true, configurable: true });
  }
  function makeMap() {
    var m = {};
    def(m, 'put', function (k, v) { m[k] = v; return v; });
    def(m, 'putAll', function (o) { if (o) { for (var k in o) { m[k] = o[k]; } } });
    def(m, 'get', function (k) { return m[k] === undefined ? null : m[k]; });
    def(m, 'getOrDefault', function (k, d) { return m[k] === undefined ? d : m[k]; });
    def(m, 'containsKey', function (k) { return Object.prototype.hasOwnProperty.call(m, k); });
    def(m, 'containsValue', function (v) { for (var k in m) { if (m[k] === v) { return true; } } return false; });
    def(m, 'remove', function (k) { var v = m[k]; delete m[k]; return v; });
    def(m, 'size', function () { return Object.keys(m).length; });
    def(m, 'isEmpty', function () { return Object.keys(m).length === 0; });
    def(m, 'clear', function () { for (var k in m) { delete m[k]; } });
    def(m, 'keySet', function () { return Object.keys(m); });
    def(m, 'values', function () { var a = []; for (var k in m) { a.push(m[k]); } return a; });
    def(m, 'entrySet', function () { var a = []; for (var k in m) { a.push({ key: k, value: m[k] }); } return a; });
    def(m, 'toString', function () { return JSON.stringify(m); });
    return m;
  }
  function makeList() {
    var a = [];
    def(a, 'add', function (x) { a.push(x); return true; });
    def(a, 'addAll', function (xs) { if (xs) { for (var i = 0; i < xs.length; i++) { a.push(xs[i]); } } return true; });
    def(a, 'get', function (i) { return a[i]; });
    def(a, 'size', function () { return a.length; });
    def(a, 'isEmpty', function () { return a.length === 0; });
    def(a, 'contains', function (x) { return a.indexOf(x) >= 0; });
    def(a, 'remove', function (i) { return a.splice(i, 1)[0]; });
    def(a, 'clear', function () { a.length = 0; });
    def(a, 'toArray', function () { return a.slice(); });
    def(a, 'toString', function () { return a.join(','); });
    return a;
  }
  var util = {};
  var mapNames = ['LinkedHashMap', 'HashMap', 'TreeMap', 'Hashtable', 'ConcurrentHashMap', 'LinkedTreeMap'];
  for (var i = 0; i < mapNames.length; i++) { util[mapNames[i]] = makeMap; }
  var listNames = ['ArrayList', 'LinkedList', 'Vector'];
  for (var j = 0; j < listNames.length; j++) { util[listNames[j]] = makeList; }
  util.HashSet = makeList;
  util.Arrays = { asList: function () { return Array.prototype.slice.call(arguments); } };
  util.Collections = {
    emptyList: function () { return []; },
    emptyMap: function () { return makeMap(); },
    singletonList: function (x) { return [x]; }
  };
  var lang = {
    String: function (v) { return v == null ? '' : String(v); },
    Integer: function (v) { return parseInt(v, 10) || 0; },
    Long: function (v) { return parseInt(v, 10) || 0; },
    Double: function (v) { return parseFloat(v) || 0; },
    Boolean: function (v) { return !!v; },
    StringBuilder: function () {
      var s = '';
      var o = {};
      def(o, 'append', function (x) { s += (x == null ? '' : x); return o; });
      def(o, 'toString', function () { return s; });
      return o;
    }
  };
  return { java: { util: util, lang: lang } };
})();
`

// installJava 安装/刷新本次执行可见的 java 对象（桥回指定解析器）。
func (r *JSRunner) installJava(a *AnalyzeRule) {
	r.vm.Set("java", newJavaObject(r.vm, r, a))
}

// loadJSLib 执行书源 jsLib（一次），使其中定义的函数与 lexical 绑定
// 对后续 Run 可见（goja 的顶层 let/const 与函数声明在同一 Runtime 内保持）。
func (r *JSRunner) loadJSLib() {
	if strings.TrimSpace(r.cfg.JSLib) == "" {
		return
	}
	prog, err := compileCached(r.cfg.JSLib)
	if err != nil {
		r.jsLibErr = fmt.Errorf("jsLib 编译失败: %w", err)
		return
	}
	g := newInterruptGuard(r.vm, r.cfg.Timeout, "jsLib 执行超时")
	r.setGuard(g)
	defer func() {
		g.Stop()
		r.setGuard(nil)
	}()
	if _, err := r.vm.RunProgram(prog); err != nil {
		r.jsLibErr = fmt.Errorf("jsLib 执行失败: %v", err)
	}
}

// JSLibErr 返回 jsLib 的执行错误（nil 表示正常）。
func (r *JSRunner) JSLibErr() error { return r.jsLibErr }

// State 返回运行时使用的会话状态。
func (r *JSRunner) State() SourceState { return r.state }

// EvalAction 在已装载 jsLib 的环境中执行一段交互 JS（登录按钮 action 等）。
// bindings 为附加的 JS 全局绑定（如 result）。返回脚本的返回值。
//
// 绑定集合与 Run 对齐（baseUrl/key/page），因为登录交互 JS 同样会用到它们。
func (r *JSRunner) EvalAction(js string, bindings map[string]any) (any, error) {
	if r.jsLibErr != nil {
		return nil, r.jsLibErr
	}
	vm := r.vm
	r.installJava(nil)
	vm.Set("baseUrl", r.cfg.BaseURL)
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
	for k, v := range bindings {
		vm.Set(k, toJSValue(vm, v))
	}
	prog, err := compileCached(js)
	if err != nil {
		return nil, fmt.Errorf("JS 编译失败: %w", err)
	}
	g := newInterruptGuard(vm, r.cfg.Timeout, "JS 执行超时")
	r.setGuard(g)
	defer func() {
		g.Stop()
		r.setGuard(nil)
	}()
	v, err := vm.RunProgram(prog)
	if err != nil {
		return nil, fmt.Errorf("JS 执行失败: %v", err)
	}
	return exportValue(v), nil
}

// EvalLoginCheck 执行书源的 loginCheckJs（对应 legado WebBook 的 checkJs 钩子）。
//
// 语义对齐 legado：作用域里的 `result` 是一个响应对象（body()/code()/url()），
// 脚本须返回响应对象或字符串；返回新 body 时调用方以之替换原响应体，
// 从而支持「检测到会话失效 → 重新登录 → 重取页面」。
func (r *JSRunner) EvalLoginCheck(js, body string, code int, finalURL string) (string, bool, error) {
	if r.jsLibErr != nil {
		return "", false, r.jsLibErr
	}
	vm := r.vm
	r.installJava(nil)
	vm.Set("baseUrl", r.cfg.BaseURL)
	vm.Set("result", newResponseObject(vm, body, code, finalURL, nil))

	prog, err := compileCached(js)
	if err != nil {
		return "", false, fmt.Errorf("loginCheckJs 编译失败: %w", err)
	}
	g := newInterruptGuard(vm, r.cfg.Timeout, "loginCheckJs 执行超时")
	r.setGuard(g)
	defer func() {
		g.Stop()
		r.setGuard(nil)
	}()
	v, err := vm.RunProgram(prog)
	if err != nil {
		return "", false, fmt.Errorf("loginCheckJs 执行失败: %v", err)
	}
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "", false, nil
	}
	// 返回响应对象时取 body()（对应 legado 的 StrResponse）
	if obj, ok := v.(*goja.Object); ok {
		if fn, ok := goja.AssertFunction(obj.Get("body")); ok {
			res, err := fn(obj)
			if err != nil {
				return "", false, fmt.Errorf("loginCheckJs 执行失败: %v", err)
			}
			return res.String(), true, nil
		}
	}
	return v.String(), true, nil
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
	r.installJava(a)
	// 上下文绑定
	if a != nil {
		vm.Set("book", newBookObject(vm, a))
		vm.Set("chapter", newChapterObject(vm, a))
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
	// baseUrl 绑定优先用「解析器当前处理的页面地址」再退回书源地址。
	//
	// 对应 legado：evalJS 里 bindings["baseUrl"] = analyzeRule.baseUrl，
	// 而 baseUrl 由各阶段 setBaseUrl(bookUrl / tocUrl / chapterUrl) 设定。
	// 这一点很关键：聚合类书源会用 String(baseUrl).startsWith("data:")
	// 判断「当前这一层是不是书源自搭的参数信封」，若把 baseUrl 固定成书源地址，
	// 书源会走 else 分支直接把 hex 原文当结果返回，详情/目录随之全空。
	if base == "" && a != nil {
		base = a.baseUrl
	}
	if base == "" {
		base = r.cfg.BaseURL
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

	prog, err := compileRuleJS(js)
	if err != nil {
		return nil, fmt.Errorf("JS 编译失败: %w", err)
	}
	g := newInterruptGuard(vm, r.cfg.Timeout, "JS 执行超时")
	r.setGuard(g)
	defer func() {
		g.Stop()
		r.setGuard(nil)
	}()
	v, err := vm.RunProgram(prog)
	if err != nil {
		return nil, fmt.Errorf("JS 执行失败: %v", err)
	}
	return exportValue(v), nil
}

// scopedRuleJS 把一段规则 JS 包进块作用域后编译。
//
// 同一个 goja Runtime 会被一个书源的所有规则 JS 复用，而顶层 let/const 会留在
// 全局词法环境里，于是「前一个脚本声明过的名字，后一个脚本再声明」会直接报
// `SyntaxError: Identifier 'x' has already been declared`。
//
// 典型触发（光遇聚合）：搜索列表规则是
//
//	<js>const { key, tab, sourcesKey, page, ... } = res; ...</js>$.data
//
// 而单本书的 bookUrl 规则是
//
//	<js>let book_id = ...; let tab = result.tab || '小说'; ...</js>
//
// 两者在同一轮解析里先后执行，第二个必然编译失败；因为失败发生在「逐条取字段」
// 阶段且被 continue 跳过，表现出来就是「搜索有结果但一条都读不出来」。
// legado 用的 Rhino 对顶层 let 更宽松，所以同一书源在阅读 App 里是正常的。
//
// 包一层块即可隔离词法声明，同时保留块最后表达式的值（JS 规范中块的完成值就是
// 最后一条语句的值），也不改变 this（仍是全局对象，书源的 this.getVariable /
// this.BaseUrl 照常可用）。
func scopedRuleJS(js string) string {
	return "{\n" + js + "\n}"
}

// functionRuleJS 把规则 JS 包成函数体后求值。
//
// legado 允许书源规则用顶层 `return` 提前返回（拷贝漫画轻小说源的正文规则
// 就在 if 分支里 `return '<img ...>'`），而 JS 脚本语法不允许顶层 return，
// 只有函数体允许。这里作为块形式的兜底。
func functionRuleJS(js string) string {
	return "(function(){\n" + js + "\n})()"
}

// compileRuleJS 编译一段书源规则 JS。
//
// 优先用「块」形式：块的完成值就是最后一条语句的值，大量书源依赖它
// （如 `@js:1+2` 直接产出 3），且块作用域能隔离顶层 let/const（见 scopedRuleJS）。
// 块形式编译失败时退回「函数体」形式，兼容 legado 允许的顶层 return。
func compileRuleJS(js string) (*goja.Program, error) {
	prog, err := compileCached(scopedRuleJS(js))
	if err == nil {
		return prog, nil
	}
	if prog2, err2 := compileCached(functionRuleJS(js)); err2 == nil {
		return prog2, nil
	}
	return nil, err // 两种形式都编译不过，返回块形式的错误（更贴近书源原文）
}

// RunImageDecode 执行图片字节二次解密 JS（coverDecodeJs / ruleContent.imageDecode）。
//
// 对应 legado ImageUtils.getDecodeResult：绑定 result=图片字节、src=图片地址，
// 规则返回解密后的字节（ArrayBuffer / typed array）。执行失败返回 error，
// 调用方决定是回 502 还是原样透传。
func (r *JSRunner) RunImageDecode(js string, data []byte, src string) ([]byte, error) {
	vm := r.vm
	r.installJava(nil)
	vm.Set("book", nil)
	vm.Set("chapter", nil)
	vm.Set("title", nil)
	vm.Set("baseUrl", r.cfg.BaseURL)
	vm.Set("result", vm.ToValue(vm.NewArrayBuffer(data)))
	vm.Set("src", src)
	vm.Set("key", nil)
	vm.Set("page", nil)
	vm.Set("nextChapterUrl", nil)

	prog, err := compileRuleJS(stripRuleJSWrapper(js))
	if err != nil {
		return nil, fmt.Errorf("图片解密 JS 编译失败: %w", err)
	}
	g := newInterruptGuard(vm, r.cfg.Timeout, "图片解密超时")
	r.setGuard(g)
	defer func() {
		g.Stop()
		r.setGuard(nil)
	}()
	v, err := vm.RunProgram(prog)
	if err != nil {
		return nil, fmt.Errorf("图片解密失败: %v", err)
	}
	out, ok := exportBytes(vm, v)
	if !ok {
		return nil, fmt.Errorf("图片解密规则没有返回字节")
	}
	return out, nil
}

// FetchBytes 用书源的网络栈拉原始字节（queryTTF 的 URL 形态、图片解密前的取图）。
func (r *JSRunner) FetchBytes(absURL string) ([]byte, error) {
	if r.cfg.FetchBytes == nil {
		return nil, ErrJsUnsupported
	}
	resume := r.pauseTimeout()
	defer resume()
	return r.cfg.FetchBytes(absURL)
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

// stripRuleJSWrapper 去掉 JS 规则的 @js: / <js>…</js> 包裹。
// 与 reader 包的 stripJSWrapper 同语义；规则包不能反向依赖 reader 包，故此处保留一份。
func stripRuleJSWrapper(s string) string {
	trimmed := strings.TrimSpace(s)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "@js:"):
		return strings.TrimSpace(trimmed[len("@js:"):])
	case strings.HasPrefix(lower, "<js>"):
		body := trimmed[len("<js>"):]
		body = strings.TrimSuffix(strings.TrimSpace(body), "</js>")
		body = strings.TrimSuffix(strings.TrimSpace(body), "<")
		return body
	default:
		return trimmed
	}
}

// exportBytes 把 JS 返回值按字节取出：ArrayBuffer / typed array / 字符串。
// 图片与字体解密规则返回的都是字节，ArrayBuffer 的 Export() 只给出属性 map，
// 必须走 ExportTo 才能拿到真正的字节。
func exportBytes(vm *goja.Runtime, v goja.Value) ([]byte, bool) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, false
	}
	var buf []byte
	if err := vm.ExportTo(v, &buf); err == nil {
		return buf, true
	}
	if s, ok := v.Export().(string); ok {
		return []byte(s), true
	}
	return nil, false
}

// toJSValue 把引擎内部结果转为可注入 JS 的值。
// Element 列表等 DOM 结果以序列化字符串传入（对应 Rhino 的 Java 对象字符串化）。
func toJSValue(vm *goja.Runtime, v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string, bool, int, int32, int64, float64, map[string]any, []any:
		return t
	// 登录表单等以 map[string]string 传入，交给 goja 直接反射转换
	case map[string]string:
		m := make(map[string]any, len(t))
		for k, v := range t {
			m[k] = v
		}
		return m
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
	// bodyAsBytes：部分书源用 `java.bytesToStr(resp.bodyAsBytes(), enc)` 处理
	// 非 UTF-8（GBK）正文（拷贝漫画轻小说源即如此）。
	mustSet("bodyAsBytes", func(call goja.FunctionCall) goja.Value { return vm.ToValue(vm.NewArrayBuffer([]byte(body))) })
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
