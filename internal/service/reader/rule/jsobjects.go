package rule

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
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

var cookieJar = struct {
	mu sync.Mutex
	m  map[string]map[string]string // host → name → value
}{m: map[string]map[string]string{}}

// CookieJarRecord 供服务层在执行 HTTP 请求时记录 Set-Cookie。
func CookieJarRecord(rawURL string, cookies []string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	host := u.Host
	cookieJar.mu.Lock()
	defer cookieJar.mu.Unlock()
	jar, ok := cookieJar.m[host]
	if !ok {
		jar = map[string]string{}
		cookieJar.m[host] = jar
	}
	for _, c := range cookies {
		pair := strings.SplitN(c, ";", 2)[0]
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		name := strings.TrimSpace(kv[0])
		if name == "" {
			continue
		}
		jar[name] = strings.TrimSpace(kv[1])
	}
}

// CookieJarHeader 供书源 JS 查询（对应 CookieStore.getCookie(tag[, key])）。
func CookieJarHeader(rawURL, key string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	cookieJar.mu.Lock()
	defer cookieJar.mu.Unlock()
	jar, ok := cookieJar.m[u.Host]
	if !ok {
		return ""
	}
	if key != "" {
		return jar[key]
	}
	var parts []string
	for k, v := range jar {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func newCookieObject(vm *goja.Runtime) *goja.Object {
	o := vm.NewObject()
	set := func(k string, v any) {
		if err := o.Set(k, v); err != nil {
			panic(vm.ToValue(err.Error()))
		}
	}
	// getCookie(key) 或 getCookie(url, key)
	set("getCookie", func(call goja.FunctionCall) goja.Value {
		tag := stringArg(call, 0)
		key := ""
		if len(call.Arguments) > 1 {
			key = stringArg(call, 1)
		}
		return vm.ToValue(CookieJarHeader(tag, key))
	})
	set("setCookie", func(call goja.FunctionCall) goja.Value {
		rawURL := stringArg(call, 0)
		cookie := stringArgOr(call, 1, "")
		if cookie != "" {
			CookieJarRecord(rawURL, []string{cookie})
		}
		return vm.ToValue(cookie)
	})
	return o
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
