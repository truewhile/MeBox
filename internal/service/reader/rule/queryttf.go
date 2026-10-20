package rule

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/dop251/goja"
)

// 本文件对应 legado JsExtensions 的 queryTTF / replaceFont：
// 解析被字体混淆的正文。书源规则通常写成
//
//	java.replaceFont(result, java.queryTTF(errorFontUrl), java.queryTTF(correctFontUrl))
//
// 错误字体把码点映射到错字形、正确字体把字形映射回真实码点，按字形轮廓配对即可还原。

// queryTTFCacheMax 已解析字体缓存条数（对齐 legado 的 LruCache 小容量策略）。
const queryTTFCacheMax = 32

var queryTTFCache = struct {
	mu sync.Mutex
	m  map[string]*queryTTFFont
	// order 记录插入顺序，超容量时淘汰最早的一条。
	order []string
}{m: map[string]*queryTTFFont{}}

// queryTTFFromBytes 解析字体并缓存（key = SHA-256）。
func queryTTFFromBytes(data []byte, useCache bool) (*queryTTFFont, error) {
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	if useCache {
		queryTTFCache.mu.Lock()
		if f, ok := queryTTFCache.m[key]; ok {
			queryTTFCache.mu.Unlock()
			return f, nil
		}
		queryTTFCache.mu.Unlock()
	}
	f, err := parseQueryTTFFont(data)
	if err != nil {
		return nil, err
	}
	if !useCache {
		return f, nil
	}
	queryTTFCache.mu.Lock()
	if len(queryTTFCache.order) >= queryTTFCacheMax {
		oldest := queryTTFCache.order[0]
		queryTTFCache.order = queryTTFCache.order[1:]
		delete(queryTTFCache.m, oldest)
	}
	queryTTFCache.m[key] = f
	queryTTFCache.order = append(queryTTFCache.order, key)
	queryTTFCache.mu.Unlock()
	return f, nil
}

// installFontBridge 注册 java.queryTTF / queryBase64TTF / replaceFont。
// set 是 java 对象的属性写入闭包（其中已包含 bridgeErr 的异常抛出语义）。
func (r *JSRunner) installFontBridge(vm *goja.Runtime, set func(string, any)) {
	// fontErr 把错误翻译成 JS 异常：书源常把 queryTTF 放在 try/catch 里降级。
	fontErr := func(name string, msg string) goja.Value {
		return vm.ToValue("java." + name + ": " + msg)
	}
	set("queryTTF", func(call goja.FunctionCall) goja.Value {
		useCache := true
		if len(call.Arguments) > 1 {
			useCache = call.Arguments[1].ToBoolean()
		}
		data, err := r.fontDataFromArg(call)
		if err != nil {
			panic(fontErr("queryTTF", err.Error()))
		}
		font, err := queryTTFFromBytes(data, useCache)
		if err != nil {
			panic(fontErr("queryTTF", err.Error()))
		}
		return vm.ToValue(font)
	})
	// queryBase64TTF 是 legado 的旧别名，语义完全相同。
	set("queryBase64TTF", func(call goja.FunctionCall) goja.Value {
		data, err := r.fontDataFromArg(call)
		if err != nil {
			panic(fontErr("queryBase64TTF", err.Error()))
		}
		font, err := queryTTFFromBytes(data, true)
		if err != nil {
			panic(fontErr("queryBase64TTF", err.Error()))
		}
		return vm.ToValue(font)
	})
	set("replaceFont", func(call goja.FunctionCall) goja.Value {
		text := stringArg(call, 0)
		errorFont := queryTTFFontArg(call, 1)
		correctFont := queryTTFFontArg(call, 2)
		filter := false
		if len(call.Arguments) > 3 {
			filter = call.Arguments[3].ToBoolean()
		}
		if errorFont == nil || correctFont == nil {
			return vm.ToValue(text)
		}
		return vm.ToValue(replaceFontText(text, errorFont, correctFont, filter))
	})
}

// fontDataFromArg 从 JS 参数取字体字节：URL / base64 / ArrayBuffer / typed array。
func (r *JSRunner) fontDataFromArg(call goja.FunctionCall) ([]byte, error) {
	if len(call.Arguments) == 0 || goja.IsUndefined(call.Arguments[0]) || goja.IsNull(call.Arguments[0]) {
		return nil, errQueryTTF("缺少字体参数")
	}
	// 字节形态（ArrayBuffer / Uint8Array）优先。
	if data, ok := exportBytes(r.vm, call.Arguments[0]); ok && len(data) > 0 {
		return data, nil
	}
	raw := strings.TrimSpace(call.Arguments[0].String())
	if raw == "" {
		return nil, errQueryTTF("缺少字体参数")
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		data, err := r.FetchBytes(raw)
		if err != nil {
			return nil, errQueryTTF("下载字体失败: " + err.Error())
		}
		return data, nil
	}
	// base64（支持 data:font/...;base64, 前缀与 URL-safe 变体）。
	if i := strings.Index(raw, "base64,"); i >= 0 {
		raw = raw[i+len("base64,"):]
	}
	data, err := base64DecodeBytes(raw)
	if err != nil {
		return nil, errQueryTTF("字体数据不是 base64")
	}
	return data, nil
}

// errQueryTTF 构造 queryTTF 相关错误（拼上函数名前缀由调用方负责）。
func errQueryTTF(msg string) error { return fmt.Errorf("%s", msg) }

// queryTTFFontArg 把 JS 参数还原成 *queryTTFFont。
// 书源常把 queryTTF 的结果存进变量再传给 replaceFont，goja 会原样保留 Go 指针。
func queryTTFFontArg(call goja.FunctionCall, idx int) *queryTTFFont {
	if len(call.Arguments) <= idx {
		return nil
	}
	if f, ok := call.Arguments[idx].Export().(*queryTTFFont); ok {
		return f
	}
	return nil
}

// replaceFontText 按字形轮廓把错误字体渲染的文本还原成正确字体对应的真实文字。
// 顺序对齐 legado JsExtensions.replaceFont：逐码点取错字形，再到正确字体查回码点。
func replaceFontText(text string, errorFont, correctFont *queryTTFFont, filter bool) string {
	var sb strings.Builder
	sb.Grow(len(text))
	for _, cp := range text {
		if isBlankUnicode(cp) {
			sb.WriteRune(cp)
			continue
		}
		glyph := errorFont.unicodeToGlyph[cp]
		if _, ok := errorFont.unicodeToGlyphID[cp]; !ok {
			// 错误字体里没有这个码点（对应 legado 的 glyfId == 0 → 视为无字形）。
			glyph = ""
		}
		if glyph == "" {
			if filter {
				continue
			}
			sb.WriteRune(cp)
			continue
		}
		if real, ok := correctFont.glyphToUnicode[glyph]; ok && real != 0 {
			sb.WriteRune(real)
			continue
		}
		if filter {
			continue
		}
		sb.WriteRune(cp)
	}
	return sb.String()
}

// isBlankUnicode 判断码点是否是不可见的空白（对齐 legado 的 isBlankUnicode 列表）。
func isBlankUnicode(cp rune) bool {
	switch cp {
	case ' ', '\t', '\n', '\r', '\v', '\f', 0x00A0, 0x2000, 0x2001, 0x2002, 0x2003,
		0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200A, 0x2028, 0x2029,
		0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return false
}
