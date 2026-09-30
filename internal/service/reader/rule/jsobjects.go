package rule

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

var jsCache = struct {
	mu sync.Mutex
	m  map[string]string
}{m: map[string]string{}}

func newCacheObject(vm *goja.Runtime) *goja.Object {
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
		jsCache.mu.Lock()
		if len(jsCache.m) >= 4096 {
			jsCache.m = map[string]string{}
		}
		jsCache.m[key] = val
		jsCache.mu.Unlock()
		return vm.ToValue(val)
	})
	set("get", func(call goja.FunctionCall) goja.Value {
		key := stringArg(call, 0)
		jsCache.mu.Lock()
		v, ok := jsCache.m[key]
		jsCache.mu.Unlock()
		if !ok {
			return goja.Null()
		}
		return vm.ToValue(v)
	})
	set("delete", func(call goja.FunctionCall) goja.Value {
		key := stringArg(call, 0)
		jsCache.mu.Lock()
		delete(jsCache.m, key)
		jsCache.mu.Unlock()
		return goja.Null()
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
