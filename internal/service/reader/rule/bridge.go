package rule

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"strings"

	"github.com/dop251/goja"
	"github.com/google/uuid"
)

// 本文件对应 legado JsExtensions / JsEncodeUtils / RegexJsExtensions 中
// 注入为 `java` 对象的函数。函数名与 Kotlin 版一一对应（存量书源硬编码），
// 实现全部为 Go 原生；浏览器/文件/压缩包类能力明确抛出不支持错误。

func newJavaObject(vm *goja.Runtime, r *JSRunner, a *AnalyzeRule) *goja.Object {
	o := vm.NewObject()
	set := func(k string, v any) {
		if err := o.Set(k, v); err != nil {
			panic(vm.ToValue(err.Error()))
		}
	}

	bridgeErr := func(name string, err error) goja.Value {
		panic(vm.ToValue(fmt.Sprintf("java.%s: %v", name, err)))
	}

	// ── 网络（对应 JsExtensions.ajax/ajaxAll/connect/get/post/head） ──
	fetchStr := func(name string, urlVal goja.Value, extraHeaders map[string]any) string {
		if r.cfg.Fetch == nil {
			bridgeErr(name, ErrJsUnsupported)
		}
		urlStr := urlVal.String()
		if arr, ok := urlVal.Export().([]any); ok && len(arr) > 0 {
			urlStr = fmt.Sprintf("%v", arr[0]) // 对应 legado：List 取第一个
		}
		req, err := ParseAnalyzeUrlWithJS(urlStr, "", 0, r.cfg.BaseURL, r)
		if err != nil {
			bridgeErr(name, err)
		}
		if req.Unsupported != nil {
			bridgeErr(name, req.Unsupported)
		}
		for k, v := range extraHeaders {
			if _, exists := req.Headers[k]; !exists {
				req.Headers[k] = fmt.Sprintf("%v", v)
			}
		}
		body, _, _, err := r.fetch(req)
		if err != nil {
			bridgeErr(name, err)
		}
		return body
	}

	fetchResp := func(name string, args []goja.Value, method, body string) *goja.Object {
		if r.cfg.Fetch == nil {
			bridgeErr(name, ErrJsUnsupported)
		}
		urlStr := ""
		if len(args) > 0 {
			urlStr = args[0].String()
		}
		req, err := ParseAnalyzeUrlWithJS(urlStr, "", 0, r.cfg.BaseURL, r)
		if err != nil {
			bridgeErr(name, err)
		}
		if method == "POST" {
			req.Method = "POST"
			req.Body = body
			trimmed := strings.TrimSpace(body)
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				req.IsJSON = true
			} else if body != "" {
				req.IsForm = true
				req.Body = encodeParams(body, req.Charset, false)
			}
		}
		if len(args) > 1 && !goja.IsUndefined(args[1]) && !goja.IsNull(args[1]) {
			if extra, ok := args[1].Export().(map[string]any); ok {
				for k, v := range extra {
					req.Headers[k] = fmt.Sprintf("%v", v)
				}
			}
		}
		if req.Unsupported != nil {
			bridgeErr(name, req.Unsupported)
		}
		respBody, finalURL, code, err := r.fetch(req)
		if err != nil {
			bridgeErr(name, err)
		}
		return newResponseObject(vm, respBody, code, finalURL, nil)
	}

	set("ajax", func(call goja.FunctionCall) goja.Value {
		extra := map[string]any(nil)
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
			if m, ok := call.Arguments[1].Export().(map[string]any); ok {
				extra = m
			}
		}
		return vm.ToValue(fetchStr("ajax", call.Arguments[0], extra))
	})
	set("ajaxAll", func(call goja.FunctionCall) goja.Value {
		var urls []any
		if len(call.Arguments) > 0 {
			if arr, ok := call.Arguments[0].Export().([]any); ok {
				urls = arr
			}
		}
		out := make([]any, 0, len(urls))
		for _, u := range urls {
			out = append(out, fetchStr("ajaxAll", vm.ToValue(u), nil))
		}
		return vm.ToValue(out)
	})
	set("connect", func(call goja.FunctionCall) goja.Value {
		return fetchResp("connect", call.Arguments, "GET", "")
	})
	// get 双语义（对应 Kotlin 重载）：get(url, headers) 走网络，get(key) 读变量
	set("get", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) >= 2 && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
			return fetchResp("get", call.Arguments, "GET", "")
		}
		key := stringArg(call, 0)
		if a != nil {
			return vm.ToValue(a.Get(key))
		}
		return vm.ToValue(r.vars[key])
	})
	set("post", func(call goja.FunctionCall) goja.Value {
		body := stringArgOr(call, 1, "")
		return fetchResp("post", call.Arguments, "POST", body)
	})
	set("head", func(call goja.FunctionCall) goja.Value {
		return fetchResp("head", call.Arguments, "HEAD", "")
	})

	// ── 规则回调（桥回当前 AnalyzeRule，对应 AnalyzeRule.getString 等公有方法） ──
	ruleStr := func(name string, args []goja.Value) (string, error) {
		if a == nil {
			return "", fmt.Errorf("无规则上下文")
		}
		mContent := any(nil)
		if len(args) > 1 && !goja.IsUndefined(args[1]) && !goja.IsNull(args[1]) {
			mContent = args[1].Export()
		}
		isURL := len(args) > 2 && args[2].ToBoolean()
		return a.GetString(args[0].String(), mContent, isURL)
	}
	set("getString", func(call goja.FunctionCall) goja.Value {
		s, err := ruleStr("getString", call.Arguments)
		if err != nil {
			bridgeErr("getString", err)
		}
		return vm.ToValue(s)
	})
	set("getStringList", func(call goja.FunctionCall) goja.Value {
		if a == nil {
			return goja.Null()
		}
		list, err := a.GetStringList(stringArgOr(call, 0, ""), nil, false)
		if err != nil {
			bridgeErr("getStringList", err)
		}
		return vm.ToValue(list)
	})
	set("getElements", func(call goja.FunctionCall) goja.Value {
		if a == nil {
			return goja.Null()
		}
		els, err := a.GetElements(stringArgOr(call, 0, ""))
		if err != nil {
			bridgeErr("getElements", err)
		}
		out := make([]any, 0, len(els))
		for _, el := range els {
			out = append(out, resultString(el))
		}
		return vm.ToValue(out)
	})
	set("put", func(call goja.FunctionCall) goja.Value {
		key := stringArg(call, 0)
		val := stringArgOr(call, 1, "")
		if a != nil {
			return vm.ToValue(a.Put(key, val))
		}
		r.vars[key] = val
		return vm.ToValue(val)
	})

	// ── 编码（对应 JsEncodeUtils / JsExtensions） ──
	set("md5Encode", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(md5Hex(stringArg(call, 0), false))
	})
	set("md5Encode16", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(md5Hex(stringArg(call, 0), true))
	})
	set("base64Decode", func(call goja.FunctionCall) goja.Value {
		s, err := base64DecodeString(stringArg(call, 0))
		if err != nil {
			bridgeErr("base64Decode", err)
		}
		return vm.ToValue(s)
	})
	set("base64DecodeToByteArray", func(call goja.FunctionCall) goja.Value {
		b, err := base64DecodeBytes(stringArg(call, 0))
		if err != nil {
			bridgeErr("base64DecodeToByteArray", err)
		}
		return vm.ToValue(vm.NewArrayBuffer(b))
	})
	set("base64Encode", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(base64.StdEncoding.EncodeToString([]byte(stringArg(call, 0))))
	})
	set("hexDecodeToString", func(call goja.FunctionCall) goja.Value {
		b, err := hex.DecodeString(strings.TrimSpace(stringArg(call, 0)))
		if err != nil {
			bridgeErr("hexDecodeToString", err)
		}
		return vm.ToValue(string(b))
	})
	set("hexDecodeToByteArray", func(call goja.FunctionCall) goja.Value {
		b, err := hex.DecodeString(strings.TrimSpace(stringArg(call, 0)))
		if err != nil {
			bridgeErr("hexDecodeToByteArray", err)
		}
		return vm.ToValue(vm.NewArrayBuffer(b))
	})
	set("hexEncodeToString", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(hex.EncodeToString([]byte(stringArg(call, 0))))
	})
	set("strToBytes", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(vm.NewArrayBuffer([]byte(stringArg(call, 0))))
	})
	set("bytesToStr", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 || goja.IsUndefined(call.Arguments[0]) || goja.IsNull(call.Arguments[0]) {
			return vm.ToValue("")
		}
		// 字节参数可能是 ArrayBuffer / typed array：这类值的 Export() 返回属性 map，
		// 只有 ExportTo 才能取回字节，旧写法会退化成 String(value) 得到 "[object ArrayBuffer]"。
		var buf []byte
		if err := vm.ExportTo(call.Arguments[0], &buf); err == nil {
			return vm.ToValue(string(buf))
		}
		if buf, ok := call.Arguments[0].Export().([]byte); ok {
			return vm.ToValue(string(buf))
		}
		return vm.ToValue(call.Arguments[0].String())
	})
	set("encodeURI", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(javaURLEncode(stringArg(call, 0), stringArgOr(call, 1, "utf-8")))
	})
	set("timeFormat", func(call goja.FunctionCall) goja.Value {
		ts := int64(call.Arguments[0].ToFloat())
		format := stringArgOr(call, 1, "yyyy-MM-dd HH:mm")
		return vm.ToValue(javaTimeFormat(ts, format, 0))
	})
	set("timeFormatUTC", func(call goja.FunctionCall) goja.Value {
		ts := int64(call.Arguments[0].ToFloat())
		format := stringArgOr(call, 1, "yyyy-MM-dd HH:mm")
		sh := int64(0)
		if len(call.Arguments) > 2 {
			sh = int64(call.Arguments[2].ToFloat())
		}
		return vm.ToValue(javaTimeFormat(ts, format, sh))
	})

	// ── 加解密（对应 JsEncodeUtils） ──
	set("createSymmetricCrypto", func(call goja.FunctionCall) goja.Value {
		transformation := stringArgOr(call, 0, "AES/CBC/PKCS5Padding")
		key := stringArgOr(call, 1, "")
		iv := stringArgOr(call, 2, "")
		cipher, err := newSymmetricCipher(transformation, key, iv)
		if err != nil {
			bridgeErr("createSymmetricCrypto", err)
		}
		return newCipherObject(vm, cipher)
	})
	aesVariant := func(mode string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			data := stringArg(call, 0)
			key := stringArgOr(call, 1, "")
			defaultTrans := mode
			trans := stringArgOr(call, 2, defaultTrans)
			iv := stringArgOr(call, 3, "")
			cipher, err := newSymmetricCipher(trans, key, iv)
			if err != nil {
				bridgeErr(mode, err)
			}
			out, err := cipher.decryptAuto(data)
			if err != nil {
				bridgeErr(mode, err)
			}
			return vm.ToValue(string(out))
		}
	}
	set("aesDecodeToString", aesVariant("AES/CBC/PKCS5Padding"))
	set("aesBase64DecodeToString", aesVariant("AES/CBC/PKCS5Padding"))
	set("aesBase64DecodeToByteArray", aesVariant("AES/CBC/PKCS5Padding"))
	set("desDecodeToString", aesVariant("DES/CBC/PKCS5Padding"))
	set("desBase64DecodeToString", aesVariant("DES/CBC/PKCS5Padding"))
	encryptStr := func(data, key, trans, iv string) string {
		cipher, err := newSymmetricCipher(trans, key, iv)
		if err != nil {
			bridgeErr("encrypt", err)
		}
		out, err := cipher.encrypt([]byte(data))
		if err != nil {
			bridgeErr("encrypt", err)
		}
		return string(out)
	}
	set("aesEncodeToString", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(encryptStr(stringArg(call, 0), stringArgOr(call, 1, ""), stringArgOr(call, 2, "AES/CBC/PKCS5Padding"), stringArgOr(call, 3, "")))
	})
	set("desEncodeToString", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(encryptStr(stringArg(call, 0), stringArgOr(call, 1, ""), stringArgOr(call, 2, "DES/CBC/PKCS5Padding"), stringArgOr(call, 3, "")))
	})
	encryptB64 := func(data, key, trans, iv string) string {
		cipher, err := newSymmetricCipher(trans, key, iv)
		if err != nil {
			bridgeErr("encrypt", err)
		}
		out, err := cipher.encrypt([]byte(data))
		if err != nil {
			bridgeErr("encrypt", err)
		}
		return base64.StdEncoding.EncodeToString(out)
	}
	set("aesEncodeToBase64String", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(encryptB64(stringArg(call, 0), stringArgOr(call, 1, ""), stringArgOr(call, 2, "AES/CBC/PKCS5Padding"), stringArgOr(call, 3, "")))
	})
	set("desEncodeToBase64String", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(encryptB64(stringArg(call, 0), stringArgOr(call, 1, ""), stringArgOr(call, 2, "DES/CBC/PKCS5Padding"), stringArgOr(call, 3, "")))
	})
	set("tripleDESDecodeStr", func(call goja.FunctionCall) goja.Value {
		data := stringArg(call, 0)
		key := stringArgOr(call, 1, "")
		trans := "DESede/CBC/PKCS5Padding"
		var ivv string
		switch {
		case len(call.Arguments) > 4: // (data, key, mode, padding, iv)
			trans = "DESede/" + stringArgOr(call, 2, "CBC") + "/" + stringArgOr(call, 3, "PKCS5Padding")
			ivv = stringArgOr(call, 4, "")
		case len(call.Arguments) > 2: // (data, key, transformation, iv?)
			trans = stringArgOr(call, 2, trans)
			ivv = stringArgOr(call, 3, "")
		}
		cipher, err := newSymmetricCipher(trans, key, ivv)
		if err != nil {
			bridgeErr("tripleDESDecodeStr", err)
		}
		out, err := cipher.decryptAuto(data)
		if err != nil {
			bridgeErr("tripleDESDecodeStr", err)
		}
		return vm.ToValue(string(out))
	})
	set("digestHex", func(call goja.FunctionCall) goja.Value {
		h, err := digestHash(stringArgOr(call, 1, "MD5"))
		if err != nil {
			bridgeErr("digestHex", err)
		}
		h.Write([]byte(stringArg(call, 0)))
		return vm.ToValue(hex.EncodeToString(h.Sum(nil)))
	})
	set("digestBase64Str", func(call goja.FunctionCall) goja.Value {
		h, err := digestHash(stringArgOr(call, 1, "MD5"))
		if err != nil {
			bridgeErr("digestBase64Str", err)
		}
		h.Write([]byte(stringArg(call, 0)))
		return vm.ToValue(base64.StdEncoding.EncodeToString(h.Sum(nil)))
	})
	set("HMacHex", func(call goja.FunctionCall) goja.Value {
		h, err := hmacHash(stringArgOr(call, 1, "HmacSHA256"), []byte(stringArgOr(call, 2, "")))
		if err != nil {
			bridgeErr("HMacHex", err)
		}
		h.Write([]byte(stringArg(call, 0)))
		return vm.ToValue(hex.EncodeToString(h.Sum(nil)))
	})
	set("HMacBase64", func(call goja.FunctionCall) goja.Value {
		h, err := hmacHash(stringArgOr(call, 1, "HmacSHA256"), []byte(stringArgOr(call, 2, "")))
		if err != nil {
			bridgeErr("HMacBase64", err)
		}
		h.Write([]byte(stringArg(call, 0)))
		return vm.ToValue(base64.StdEncoding.EncodeToString(h.Sum(nil)))
	})

	// ── 其他 ──
	set("log", func(call goja.FunctionCall) goja.Value {
		if r.cfg.Log != nil {
			r.cfg.Log(call.Arguments[0].String())
		}
		return goja.Null()
	})
	set("getWebViewUA", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(defaultWebViewUA)
	})
	// 简繁转换（对应 legado ChineseUtils → quick-transfer）。这里按单字映射实现，
	// 见 zh_convert.go 的说明。
	set("t2s", func(call goja.FunctionCall) goja.Value { return vm.ToValue(zhToSimplified(stringArg(call, 0))) })
	set("s2t", func(call goja.FunctionCall) goja.Value { return vm.ToValue(zhToTraditional(stringArg(call, 0))) })
	set("htmlFormat", func(call goja.FunctionCall) goja.Value { return vm.ToValue(stringArg(call, 0)) })
	// randomUUID：部分书源拿它生成设备标识/请求 ID（缺失会让整条规则抛异常）。
	set("randomUUID", func(call goja.FunctionCall) goja.Value { return vm.ToValue(uuid.NewString()) })
	// toNumChapter：章节标题里的数字规整成阿拉伯数字（见 zh_number.go）。
	set("toNumChapter", func(call goja.FunctionCall) goja.Value { return vm.ToValue(numChapter(stringArg(call, 0))) })
	// toURL(url[, baseUrl])：对应 legado JsURL（host/origin/pathname/searchParams）。
	set("toURL", func(call goja.FunctionCall) goja.Value {
		base := ""
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
			base = call.Arguments[1].String()
		}
		obj, err := newJSURLObject(vm, stringArg(call, 0), base)
		if err != nil {
			bridgeErr("toURL", err)
		}
		return obj
	})
	// logType：调试用，打印值的类型（对应 legado 的日志实现）。
	set("logType", func(call goja.FunctionCall) goja.Value {
		if r.cfg.Log != nil {
			if len(call.Arguments) == 0 || goja.IsNull(call.Arguments[0]) || goja.IsUndefined(call.Arguments[0]) {
				r.cfg.Log("null")
			} else {
				r.cfg.Log(fmt.Sprintf("%T", call.Arguments[0].Export()))
			}
		}
		return goja.Null()
	})

	// ── 本地文件（对应 legado JsExtensions 的缓存/文件系列） ──
	//
	// 服务端把文件限制在自己的缓存目录（<cache>/reader/files）内，
	// 书源只能用相对路径（允许以 / 开头，downloadFile 的返回值即此形态）。
	set("downloadFile", func(call goja.FunctionCall) goja.Value {
		// 旧签名 downloadFile(contentHex, url)：把 hex 字符串落成文件。
		if len(call.Arguments) >= 2 && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
			url := stringArg(call, 1)
			raw, err := hex.DecodeString(strings.TrimSpace(stringArg(call, 0)))
			if err != nil {
				bridgeErr("downloadFile", err)
			}
			rel, err := writeCacheFile(r.cfg.CacheDir, cacheFileName(url), raw)
			if err != nil {
				bridgeErr("downloadFile", err)
			}
			return vm.ToValue(rel)
		}
		rel, err := downloadToCache(r, stringArg(call, 0))
		if err != nil {
			bridgeErr("downloadFile", err)
		}
		return vm.ToValue(rel)
	})
	// cacheFile(url): 下载并缓存文本文件，返回文件**内容**（对应 legado 语义）。
	set("cacheFile", func(call goja.FunctionCall) goja.Value {
		url := stringArg(call, 0)
		if dir := readerFilesDir(r.cfg.CacheDir); dir != "" {
			if b, err := os.ReadFile(filepath.Join(dir, cacheFileName(url))); err == nil && len(b) > 0 {
				return vm.ToValue(decodeTextAuto(b))
			}
		}
		rel, err := downloadToCache(r, url)
		if err != nil {
			bridgeErr("cacheFile", err)
		}
		full, err := resolveCacheFilePath(r.cfg.CacheDir, rel)
		if err != nil {
			bridgeErr("cacheFile", err)
		}
		b, err := os.ReadFile(full)
		if err != nil {
			bridgeErr("cacheFile", err)
		}
		return vm.ToValue(decodeTextAuto(b))
	})
	set("readFile", func(call goja.FunctionCall) goja.Value {
		full, err := resolveCacheFilePath(r.cfg.CacheDir, stringArg(call, 0))
		if err != nil {
			return goja.Null()
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return goja.Null()
		}
		return vm.ToValue(vm.NewArrayBuffer(b))
	})
	set("readTxtFile", func(call goja.FunctionCall) goja.Value {
		full, err := resolveCacheFilePath(r.cfg.CacheDir, stringArg(call, 0))
		if err != nil {
			return vm.ToValue("")
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return vm.ToValue("")
		}
		if cs := stringArgOr(call, 1, ""); cs != "" {
			if s, derr := DecodeBytes(b, cs); derr == nil {
				return vm.ToValue(s)
			}
		}
		return vm.ToValue(decodeTextAuto(b))
	})
	set("deleteFile", func(call goja.FunctionCall) goja.Value {
		full, err := resolveCacheFilePath(r.cfg.CacheDir, stringArg(call, 0))
		if err != nil {
			return vm.ToValue(false)
		}
		return vm.ToValue(os.Remove(full) == nil)
	})
	// getFile(path): 返回 File 对象（提供书源常用的 exists/length/name/delete/readText）。
	set("getFile", func(call goja.FunctionCall) goja.Value {
		full, err := resolveCacheFilePath(r.cfg.CacheDir, stringArg(call, 0))
		if err != nil {
			full = ""
		}
		return newFileObject(vm, full, stringArg(call, 0))
	})

	// ── 书源会话状态（legado 中 `java` 与 `source` 是同一对象） ──
	if r.state != nil {
		bindSourceState(vm, set, r.state, r.cfg.SourceProps)
	}

	// ── 宿主交互：提示、浏览器页面、登录界面刷新 ──
	//
	// legado 用内置 WebView 承载页面：startBrowser / startBrowserAwait 打开一个
	// 页面让用户完成防爬校验、登录或参数选择，Await 版本还会把用户操作后的
	// 页面源码作为 StrResponse 返回。真实的聚合类书源（如光遇聚合）把「线路
	// 切换」「用户后台」「书源设置」全部建在这两个函数上，因此这里必须真正
	// 把页面交给前端，而不是记录个地址就算完。
	if r.state != nil {
		toast := func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) > 0 {
				r.state.Toast(call.Arguments[0].String())
			}
			return goja.Null()
		}
		set("toast", toast)
		set("longToast", toast)

		// startBrowser(url, title[, html])：展示页面，不等待用户完成。
		set("startBrowser", func(call goja.FunctionCall) goja.Value {
			if err := r.openBrowser(parseBrowserArgs(call)); err != nil {
				bridgeErr("startBrowser", err)
			}
			return goja.Null()
		})
		// showBrowser(url, html, preloadJs, config)：legado 中同样是「打开即返回」
		// 的对话框，只是参数含义不同，这里统一按展示处理。
		set("showBrowser", func(call goja.FunctionCall) goja.Value {
			req := parseBrowserArgs(call)
			if req.Title == "" {
				req.Title = stringArgOr(call, 2, "")
			}
			if err := r.openBrowser(req); err != nil {
				bridgeErr("showBrowser", err)
			}
			return goja.Null()
		})
		// startBrowserAwait(url, title[, refetchAfterSuccess][, html])：
		// 展示页面并阻塞等待用户完成后回传页面内容（对应 legado StrResponse）。
		set("startBrowserAwait", func(call goja.FunctionCall) goja.Value {
			req := parseBrowserArgs(call)
			if len(call.Arguments) > 2 && !goja.IsUndefined(call.Arguments[2]) && !goja.IsNull(call.Arguments[2]) {
				if _, isStr := call.Arguments[2].Export().(string); !isStr {
					req.Refetch = call.Arguments[2].ToBoolean()
				}
			}
			res, err := r.awaitBrowser(req)
			if err != nil {
				bridgeErr("startBrowserAwait", err)
			}
			finalURL := res.URL
			if finalURL == "" {
				finalURL = req.URL
			}
			return newResponseObject(vm, res.Body, 200, finalURL, nil)
		})

		// reLoginView / refreshExplore：请求宿主重新渲染登录表单。
		// MeBox 每次登录动作后都会重新拉取 loginUi 并重建表单，因此这是真实
		// 生效的信号，而不是静默空实现。
		for _, name := range []string{"reLoginView", "refreshExplore"} {
			set(name, func(call goja.FunctionCall) goja.Value {
				r.requestUIRefresh()
				return goja.Null()
			})
		}
		// upLoginData(data)：书源把服务端返回的值回填进登录表单。
		set("upLoginData", func(call goja.FunctionCall) goja.Value {
			r.applyLoginData(call.Arguments)
			return goja.Null()
		})
	}
	// 设备标识：部分源用 deviceID/androidId 做"是否支持该环境"探测，
	// 成功返回会让源走安卓分支，这里统一以异常告知不支持并回退到通用分支。
	for _, name := range []string{"deviceID", "androidId"} {
		set(name, func(call goja.FunctionCall) goja.Value {
			panic(vm.ToValue("java." + name + ": 服务端无设备标识"))
		})
	}
	// 说明：下面这些名字在服务端刻意不定义，调用即抛异常，以对齐 legado 的
	// 真实可用面，避免「静默空实现」让书源误判运行环境：
	//   qread / showReadingBrowser / startBrowserDp —— legado 中并不存在
	//   （例如 checkEnv 用 java.qread() 探测「轻阅」，旧实现返回成功会让书源
	//    误认为运行在轻阅里，从而跳过它自己的降级分支）
	//   open / searchBook —— legado 中仅做原生界面跳转（打开搜索页 / 登录页），
	//                        服务端没有对应页面，谎报成功会让书源走错分支

	// 需要真正无头浏览器/本地文件系统的能力：明确抛出不支持，并把原因说清楚
	// （书源调试面板会把这句原样显示出来，用户能立刻判断该不该继续折腾这个源）。
	unsupported := func(name, reason string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			panic(vm.ToValue(fmt.Sprintf("java.%s 服务端不支持：%s", name, reason)))
		}
	}
	for _, name := range []string{
		"webView", "webViewGetSource", "webViewGetOverrideUrl",
		"openVideoPlayer", "getVerificationCode",
	} {
		set(name, unsupported(name, "需要无头浏览器（WebView）"))
	}
	for _, name := range []string{
		"unzipFile", "un7zFile", "unrarFile", "unArchiveFile", "getTxtInFolder",
		"getZipStringContent", "getZipByteArrayContent",
		"getRarStringContent", "get7zStringContent",
	} {
		set(name, unsupported(name, "需要压缩包解压能力"))
	}
	// importScript 在 legado 里是「下载 JS 文件并 eval」，服务端可做但会引入
	// 任意脚本执行面，暂不支持，保持明确报错。
	set("importScript", unsupported("importScript", "服务端不支持动态加载外部脚本"))

	// 字体混淆还原（queryTTF / queryBase64TTF / replaceFont）。
	r.installFontBridge(vm, set)

	return o
}

const defaultWebViewUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

func md5Hex(s string, short bool) string {
	sum := md5.Sum([]byte(s))
	h := hex.EncodeToString(sum[:])
	if short {
		return h[8:24]
	}
	return h
}

func digestHash(algorithm string) (hash.Hash, error) {
	switch strings.ToUpper(strings.NewReplacer("-", "", "_", "").Replace(algorithm)) {
	case "MD5":
		return md5.New(), nil
	case "SHA1":
		return sha1.New(), nil
	case "SHA256":
		return sha256.New(), nil
	case "SHA384":
		return sha512.New384(), nil
	case "SHA512":
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("不支持的摘要算法: %s", algorithm)
	}
}

func hmacHash(algorithm string, key []byte) (hash.Hash, error) {
	switch strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(algorithm)) {
	case "hmacmd5":
		return hmac.New(md5.New, key), nil
	case "hmacsha1":
		return hmac.New(sha1.New, key), nil
	case "hmacsha256":
		return hmac.New(sha256.New, key), nil
	case "hmacsha512":
		return hmac.New(sha512.New, key), nil
	default:
		return nil, fmt.Errorf("不支持的 HMAC 算法: %s", algorithm)
	}
}
