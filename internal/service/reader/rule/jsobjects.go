package rule

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// 本文件：桥接层的小工具（编码字节转换、cookie/cache 对象、时间格式化）。

var b64EncodingVariants = []*base64.Encoding{
	base64.StdEncoding,
	base64.RawStdEncoding,
	base64.URLEncoding,
	base64.RawURLEncoding,
}

// base64DecodeBytes 宽松解码：自动补 padding、支持 URL-safe 变体
// （对应 legado Base64 解码的宽容行为）。
func base64DecodeBytes(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	var lastErr error
	for _, enc := range b64EncodingVariants {
		b, err := enc.DecodeString(s)
		if err == nil {
			return b, nil
		}
		lastErr = err
	}
	// 缺 padding 的 std 变体
	if padded := s + strings.Repeat("=", (4-len(s)%4)%4); padded != s {
		if b, err := base64.StdEncoding.DecodeString(padded); err == nil {
			return b, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无效 Base64")
	}
	return nil, lastErr
}

func base64DecodeString(s string) (string, error) {
	b, err := base64DecodeBytes(s)
	return string(b), err
}

func hexDecodeBytes(s string) ([]byte, error) { return hex.DecodeString(s) }
func base64StdEncodeBytes(b []byte) string    { return base64.StdEncoding.EncodeToString(b) }
func hexEncodeBytes(b []byte) string          { return hex.EncodeToString(b) }

// ─── cookie 对象（对应 legado CookieStore 注入的 `cookie`） ─────────────────

// 会话状态由服务层注入（落库）；未注入时用进程内 MemoryState。
// 见 state.go —— 旧的全局 cookieJar 因无法按书源隔离且不落库已废弃。

// newCookieObject 构造 JS 的 `cookie` 对象：
// getCookie(url[,key]) / setCookie(url,cookie) / replaceCookie / removeCookie / getKey。
func newCookieObject(vm *goja.Runtime, state SourceState) *goja.Object {
	o := vm.NewObject()
	set := func(k string, v any) {
		if err := o.Set(k, v); err != nil {
			panic(vm.ToValue(err.Error()))
		}
	}
	set("getCookie", func(call goja.FunctionCall) goja.Value {
		tag := stringArg(call, 0)
		if len(call.Arguments) > 1 {
			return vm.ToValue(state.GetCookieKey(tag, stringArg(call, 1)))
		}
		return vm.ToValue(state.GetCookie(tag))
	})
	set("setCookie", func(call goja.FunctionCall) goja.Value {
		rawURL := stringArg(call, 0)
		cookie := stringArgOr(call, 1, "")
		if cookie != "" {
			state.SetCookie(rawURL, cookie)
		}
		return vm.ToValue(cookie)
	})
	// replaceCookie 与 setCookie 在服务端实现中同为覆盖式合并（对应 legado 的语义）
	set("replaceCookie", func(call goja.FunctionCall) goja.Value {
		rawURL := stringArg(call, 0)
		cookie := stringArgOr(call, 1, "")
		if cookie != "" {
			state.SetCookie(rawURL, cookie)
		}
		return vm.ToValue(cookie)
	})
	set("removeCookie", func(call goja.FunctionCall) goja.Value {
		state.RemoveCookie(stringArg(call, 0))
		return goja.Null()
	})
	set("getKey", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(state.GetCookieKey(stringArg(call, 0), stringArg(call, 1)))
	})
	return o
}

// ─── 书籍 / 章节对象 ───────────────────────────────────────────────────────

// newBookObject 构造规则 JS 里的 `book`（对应 legado 的 Book 实体）。
//
// 书源会读它的元数据（name / author / coverUrl / durChapterIndex / order / type…）、
// 给它赋值（book.type = …、book.imageStyle = …）、调用 setUseReplaceRule()，
// 以及用 getVariable / putVariable 读写书籍自定义变量。
//
// 早期这里只绑了 {"name": ...}，书源一碰 `book.setUseReplaceRule(false)`
// 就 TypeError，整段详情/目录规则 JS 直接失败（表现为「详情空白、目录 0 章」）。
func newBookObject(vm *goja.Runtime, a *AnalyzeRule) *goja.Object {
	o := vm.NewObject()
	set := func(k string, v any) {
		_ = o.Set(k, v)
	}
	for k, v := range a.bookMeta {
		if k == "type" {
			continue // type 用访问器，见下
		}
		set(k, v)
	}
	// name 以 SetBookContext 的值为准（legado 里 book.name 就是这个）
	set("name", a.bookName)
	set("bookName", a.bookName)

	// book.type：书源会赋值来声明书籍类型（听书=1 / 漫画=2 …），
	// legado 会把它写回 Book.type，服务层据此决定正文按文本/音频/图片返回。
	// 用访问器把写入记下来，否则赋值只活在本次 JS 里，读完仍是文本。
	_ = o.DefineAccessorProperty("type",
		vm.ToValue(func(call goja.FunctionCall) goja.Value { return vm.ToValue(a.bookTypeValue()) }),
		vm.ToValue(func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) > 0 && !goja.IsUndefined(call.Arguments[0]) && !goja.IsNull(call.Arguments[0]) {
				a.SetBookType(int(call.Arguments[0].ToInteger()))
			}
			return goja.Undefined()
		}),
		goja.FLAG_FALSE, goja.FLAG_TRUE)

	// readConfig：书源读 book.readConfig.useReplaceRule，并可能回写
	rc := vm.NewObject()
	_ = rc.Set("useReplaceRule", false)
	set("readConfig", rc)
	set("setUseReplaceRule", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 {
			_ = rc.Set("useReplaceRule", call.Arguments[0].ToBoolean())
		}
		return goja.Null()
	})

	// 书籍自定义变量（legado Book.variableMap）。
	//
	// 注意「缺省返回空串」：legado 的 RuleDataInterface.getVariable 是
	//   variableMap[key] ?: getBigVariable(key) ?: ""
	// 返回 "" 而不是 null。书源会直接写 `String(book.getVariable('custom')) || ''`，
	// 若这里返回 null，String(null) 得到字符串 "null"（真值），会被当成
	// tone_id 发给站点，站点直接返回空正文（表现为「正文 0 字」）。
	set("getVariable", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(a.bookCustom[stringArg(call, 0)])
	})
	set("putVariable", func(call goja.FunctionCall) goja.Value {
		if a.bookCustom == nil {
			a.bookCustom = map[string]string{}
		}
		a.bookCustom[stringArg(call, 0)] = stringArgOr(call, 1, "")
		if a.bookVarPutter != nil {
			a.bookVarPutter()
		}
		return goja.Null()
	})
	return o
}

// newChapterObject 构造规则 JS 里的 `chapter`（对应 legado 的 BookChapter）。
func newChapterObject(vm *goja.Runtime, a *AnalyzeRule) *goja.Object {
	o := vm.NewObject()
	_ = o.Set("title", a.chapterTitle)
	_ = o.Set("index", a.chapterIndex)
	_ = o.Set("isVip", false)
	_ = o.Set("isPay", false)
	return o
}

// newSourceObject 构造 JS 的 `source` 对象。
// 对应 legado BaseSource 的变量与登录信息读写。
func newSourceObject(vm *goja.Runtime, state SourceState, props map[string]any) *goja.Object {
	o := vm.NewObject()
	set := func(k string, v any) {
		if err := o.Set(k, v); err != nil {
			panic(vm.ToValue(err.Error()))
		}
	}
	bindSourceState(vm, set, state, props)
	// source.get/put：书源级键值缓存（legado 中即 source 的方法）。
	// 注意 java.get/put 另有语义（网络 / 解析器变量），在 bridge.go 中定义，
	// 不在这里覆盖。
	set("get", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(sourceKeyGet(state, stringArg(call, 0)))
	})
	set("put", func(call goja.FunctionCall) goja.Value {
		val := stringArgOr(call, 1, "")
		sourceKeyPut(state, stringArg(call, 0), val)
		return vm.ToValue(val)
	})
	// 书源自身属性（bookSourceUrl 等）原样可读，存量 JS 会读 source.loginUi 等做能力探测。
	for k, v := range props {
		if o.Get(k) != nil {
			continue
		}
		_ = o.Set(k, v)
	}
	return o
}

// bindSourceState 把书源会话状态方法绑定到目标 JS 对象。
// legado 中 book source 的 evalJS 把 `java` 绑成书源对象自身，因此 `java` 与
// `source` 都能读到 getVariable/getLoginInfo 等方法——这里保持一致。
func bindSourceState(vm *goja.Runtime, set func(k string, v any), state SourceState, props map[string]any) {
	set("getVariable", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(state.GetVariable())
	})
	setVariable := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 || goja.IsUndefined(call.Arguments[0]) || goja.IsNull(call.Arguments[0]) {
			state.SetVariable("")
			return goja.Null()
		}
		state.SetVariable(call.Arguments[0].String())
		return goja.Null()
	}
	set("setVariable", setVariable)
	set("putVariable", setVariable)

	set("getLoginInfo", func(call goja.FunctionCall) goja.Value {
		if v := state.GetLoginInfo(); v != "" {
			return vm.ToValue(v)
		}
		return goja.Null()
	})
	set("putLoginInfo", func(call goja.FunctionCall) goja.Value {
		v := ""
		if len(call.Arguments) > 0 && !goja.IsUndefined(call.Arguments[0]) && !goja.IsNull(call.Arguments[0]) {
			v = call.Arguments[0].String()
		}
		state.SetLoginInfo(v)
		return vm.ToValue(true)
	})
	set("removeLoginInfo", func(call goja.FunctionCall) goja.Value {
		state.SetLoginInfo("")
		return goja.Null()
	})
	// getLoginInfoMap 对应 Kotlin：解析登录信息 JSON；
	// 未设置且 loginUi 非空时，用 loginUi 里各字段的 default 初始化。
	set("getLoginInfoMap", func(call goja.FunctionCall) goja.Value {
		if m := parseLoginInfoMap(state.GetLoginInfo()); m != nil {
			return vm.ToValue(m)
		}
		return vm.ToValue(initLoginInfoFromUI(props))
	})

	set("getLoginHeader", func(call goja.FunctionCall) goja.Value {
		if v := state.GetLoginHeader(); v != "" {
			return vm.ToValue(v)
		}
		return goja.Null()
	})
	set("putLoginHeader", func(call goja.FunctionCall) goja.Value {
		header := stringArgOr(call, 0, "")
		state.SetLoginHeader(header)
		// 请求头里的 Cookie 同步进 cookie 存储（对应 legado putLoginHeader）
		if header != "" {
			var m map[string]any
			if json.Unmarshal([]byte(header), &m) == nil {
				for k, v := range m {
					if strings.EqualFold(k, "cookie") {
						state.SetCookie(headerCookieURL(props), fmt.Sprintf("%v", v))
					}
				}
			}
		}
		return goja.Null()
	})
	set("removeLoginHeader", func(call goja.FunctionCall) goja.Value {
		state.SetLoginHeader("")
		state.RemoveCookie(headerCookieURL(props))
		return goja.Null()
	})
	set("getKey", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(headerCookieURL(props))
	})
}

// sourceKeyGet / sourceKeyPut：source.get/put 的独立键值槽，
// 以 "__kv__" 前缀编码进同一份变量 JSON，从而与 getVariable 一起持久化。
const sourceKVVar = "__kv__"

func sourceKeyGet(state SourceState, key string) string {
	if key == "" {
		return ""
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(state.GetVariable()), &m)
	return m[sourceKVVar+key]
}

func sourceKeyPut(state SourceState, key, val string) {
	if key == "" {
		return
	}
	m := map[string]string{}
	_ = json.Unmarshal([]byte(state.GetVariable()), &m)
	if m == nil {
		m = map[string]string{}
	}
	m[sourceKVVar+key] = val
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	state.SetVariable(string(b))
}

// headerCookieURL 取 loginHeader 里 Cookie 归属的 URL。
func headerCookieURL(props map[string]any) string {
	if props != nil {
		if u, ok := props["bookSourceUrl"].(string); ok && u != "" {
			return u
		}
	}
	return ""
}

// parseLoginInfoMap 解析登录信息 JSON；空串或非法 JSON 返回 nil。
func parseLoginInfoMap(s string) map[string]string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var raw map[string]any
	if json.Unmarshal([]byte(s), &raw) != nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

// initLoginInfoFromUI 用 loginUi 各字段 default 生成初始登录信息
// （对应 legado getLoginInfoMap：跳过 button 类型）。
func initLoginInfoFromUI(props map[string]any) map[string]string {
	out := map[string]string{}
	raw, _ := props["loginUi"].(string)
	if strings.TrimSpace(raw) != "" {
		var rows []map[string]any
		if json.Unmarshal([]byte(raw), &rows) == nil {
			for _, row := range rows {
				if t, _ := row["type"].(string); t == "button" {
					continue
				}
				name, _ := row["name"].(string)
				if name == "" {
					continue
				}
				def, _ := row["default"].(string)
				out[name] = def
			}
		}
	}
	return out
}

// ─── cache 对象（对应 legado CacheManager 注入的 `cache`） ──────────────────
//
// legado 的 cache 有两套存储：put/get/delete 落持久缓存（ACache），
// putMemory/getFromMemory 落进程内内存缓存。书源靠后者记录「这条段评点过几次」
// 这类临时状态——光遇聚合的 paraForAndroid 每一段带段评的文字都会调
// cache.putMemory(url, 0)，缺了它整条正文规则会抛 TypeError 直接失败。
// 两套存储分开，否则 getFromMemory 会读到 put 写进去的持久值。
//
// 持久层按书源（bookSourceUrl）命名空间隔离，并落到 CacheDir/reader-js-cache：
// 之前是包级全局 map，任何书源的 put/get 全局可见，多个源用同一个 key 会互相串值，
// 一个源写满 4096 条还会把别的源的缓存一起清掉；重启后也全部丢失。

// jsCacheMaxEntries 单个书源命名空间的条目上限：超了按写入时间淘汰最旧的一半，
// 只影响本命名空间，不再波及其它书源。
const jsCacheMaxEntries = 4096

// jsCacheNamespace 一个书源的持久/内存缓存命名空间。
type jsCacheNamespace struct {
	mu   sync.Mutex
	data map[string]string
	seq  map[string]int64
	next int64
	path string
}

// jsCacheRegistry 按命名空间持有 cache，命名空间取书源 URL。
var jsCacheRegistry = struct {
	mu sync.Mutex
	m  map[string]*jsCacheNamespace
}{m: map[string]*jsCacheNamespace{}}

// jsCacheFor 取（或创建）命名空间；cacheDir 非空时尝试从磁盘恢复。
func jsCacheFor(namespace, cacheDir string) *jsCacheNamespace {
	if namespace == "" {
		namespace = "__global__"
	}
	jsCacheRegistry.mu.Lock()
	ns, ok := jsCacheRegistry.m[namespace]
	if !ok {
		ns = &jsCacheNamespace{data: map[string]string{}, seq: map[string]int64{}}
		if cacheDir != "" {
			ns.path = filepath.Join(cacheDir, "reader-js-cache", md5Hex(namespace, true)+".json")
		}
		ns.load()
		jsCacheRegistry.m[namespace] = ns
	}
	jsCacheRegistry.mu.Unlock()
	return ns
}

// load 从磁盘恢复命名空间（仅供 jsCacheFor 在注册表锁内首次调用）。
func (n *jsCacheNamespace) load() {
	if n.path == "" {
		return
	}
	raw, err := os.ReadFile(n.path) // #nosec G304 -- 路径由服务端生成
	if err != nil || len(raw) == 0 {
		return
	}
	var stored struct {
		Data map[string]string `json:"data"`
		Seq  map[string]int64  `json:"seq"`
	}
	if json.Unmarshal(raw, &stored) != nil {
		return
	}
	if stored.Data != nil {
		n.data = stored.Data
	}
	if stored.Seq != nil {
		n.seq = stored.Seq
		for _, v := range stored.Seq {
			if v > n.next {
				n.next = v
			}
		}
	}
}

// persist 把命名空间写回磁盘（临时文件 + 原子 rename）。
func (n *jsCacheNamespace) persist() {
	if n.path == "" {
		return
	}
	payload, err := json.Marshal(struct {
		Data map[string]string `json:"data"`
		Seq  map[string]int64  `json:"seq"`
	}{n.data, n.seq})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(n.path), 0o750); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(n.path), ".js-cache-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	_ = os.Rename(name, n.path)
}

// put 写一个持久键值（返回最终值）。
func (n *jsCacheNamespace) put(key, value string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.data[key]; !exists && len(n.data) >= jsCacheMaxEntries {
		n.evictOldestLocked()
	}
	n.next++
	n.data[key] = value
	n.seq[key] = n.next
	n.persist()
	return value
}

// evictOldestLocked 淘汰最旧的一半条目（调用方需持锁）。
func (n *jsCacheNamespace) evictOldestLocked() {
	type entry struct {
		key string
		seq int64
	}
	entries := make([]entry, 0, len(n.data))
	for k := range n.data {
		entries = append(entries, entry{k, n.seq[k]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
	drop := len(entries)/2 + 1
	for i := 0; i < drop && i < len(entries); i++ {
		delete(n.data, entries[i].key)
		delete(n.seq, entries[i].key)
	}
}

// get 读一个持久键。
func (n *jsCacheNamespace) get(key string) (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	v, ok := n.data[key]
	return v, ok
}

// del 删除一个持久键。
func (n *jsCacheNamespace) del(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.data, key)
	delete(n.seq, key)
	n.persist()
}

// memoryStore 进程内内存缓存（putMemory/getFromMemory），同样按命名空间隔离。
var jsMemoryRegistry = struct {
	mu sync.Mutex
	m  map[string]map[string]string
}{m: map[string]map[string]string{}}

func jsMemoryFor(namespace string) map[string]string {
	if namespace == "" {
		namespace = "__global__"
	}
	jsMemoryRegistry.mu.Lock()
	defer jsMemoryRegistry.mu.Unlock()
	store, ok := jsMemoryRegistry.m[namespace]
	if !ok {
		store = map[string]string{}
		jsMemoryRegistry.m[namespace] = store
	}
	return store
}

// newCacheObject 构造 JS 的 `cache` 对象。
// namespace 取书源 URL；cacheDir 非空时 put/get 持久化到磁盘。
func newCacheObject(vm *goja.Runtime, namespace, cacheDir string) *goja.Object {
	ns := jsCacheFor(namespace, cacheDir)
	o := vm.NewObject()
	set := func(k string, v any) {
		if err := o.Set(k, v); err != nil {
			panic(vm.ToValue(err.Error()))
		}
	}

	set("put", func(call goja.FunctionCall) goja.Value {
		key := stringArg(call, 0)
		val := ""
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
			val = call.Arguments[1].String()
		}
		return vm.ToValue(ns.put(key, val))
	})
	set("get", func(call goja.FunctionCall) goja.Value {
		if v, ok := ns.get(stringArg(call, 0)); ok {
			return vm.ToValue(v)
		}
		return goja.Null()
	})
	set("delete", func(call goja.FunctionCall) goja.Value {
		ns.del(stringArg(call, 0))
		return goja.Null()
	})
	// 内存缓存（legado Cache.getFromMemory / putMemory）：进程内、不落盘。
	mem := jsMemoryFor(namespace)
	set("putMemory", func(call goja.FunctionCall) goja.Value {
		key := stringArg(call, 0)
		val := ""
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
			val = call.Arguments[1].String()
		}
		jsMemoryRegistry.mu.Lock()
		if _, exists := mem[key]; !exists && len(mem) >= jsCacheMaxEntries {
			mem = map[string]string{}
			jsMemoryRegistry.m[namespace] = mem
		}
		mem[key] = val
		jsMemoryRegistry.mu.Unlock()
		return vm.ToValue(val)
	})
	set("getFromMemory", func(call goja.FunctionCall) goja.Value {
		jsMemoryRegistry.mu.Lock()
		v, ok := mem[stringArg(call, 0)]
		jsMemoryRegistry.mu.Unlock()
		if !ok {
			return goja.Null()
		}
		return vm.ToValue(v)
	})
	return o
}

// ─── 时间格式化（对应 JsExtensions.timeFormat/timeFormatUTC） ───────────────

// javaTimeFormat 将 Java SimpleDateFormat 常用 pattern 转 Go 布局。
// sh 为时区偏移小时数（timeFormatUTC 语义，0 表示本地时区）。
func javaTimeFormat(ts int64, format string, sh int64) string {
	loc := time.Local
	if sh != 0 {
		loc = time.FixedZone(fmt.Sprintf("UTC%+d", sh), int(sh)*3600)
	}
	t := time.UnixMilli(ts).In(loc)
	return t.Format(javaPatternToGo(format))
}

func javaPatternToGo(p string) string {
	replacements := []struct{ java, goLayout string }{
		{"yyyy", "2006"}, {"yy", "06"},
		{"MM", "01"}, {"dd", "02"},
		{"HH", "15"}, {"hh", "03"},
		{"mm", "04"}, {"ss", "05"},
		{"SSS", "000"},
		{"a", "PM"},
	}
	// 优先替换长 token，避免 yyyy 被 yy+yy 拆坏
	for _, r := range replacements {
		p = strings.ReplaceAll(p, r.java, "\x00"+r.goLayout+"\x00")
	}
	return strings.NewReplacer("\x00", "").Replace(p)
}
