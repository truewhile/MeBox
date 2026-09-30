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
	"strings"

	"github.com/dop251/goja"
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
		body, _, _, err := r.cfg.Fetch(req)
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
		respBody, finalURL, code, err := r.cfg.Fetch(req)
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
	set("t2s", func(call goja.FunctionCall) goja.Value { return vm.ToValue(stringArg(call, 0)) })
	set("s2t", func(call goja.FunctionCall) goja.Value { return vm.ToValue(stringArg(call, 0)) })
	set("htmlFormat", func(call goja.FunctionCall) goja.Value { return vm.ToValue(stringArg(call, 0)) })

	// ── 书源会话状态（legado 中 `java` 与 `source` 是同一对象） ──
	if r.state != nil {
		bindSourceState(vm, set, r.state, r.cfg.SourceProps)
	}

	// ── 宿主交互：服务端无 UI，转为可回传前端的提示 / 待打开链接 ──
	if r.state != nil {
		toast := func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) > 0 {
				r.state.Toast(call.Arguments[0].String())
			}
			return goja.Null()
		}
		set("toast", toast)
		set("longToast", toast)
		// startBrowser(url, title)：记录待打开地址，前端可代开新标签。
		set("startBrowser", func(call goja.FunctionCall) goja.Value {
			r.state.OpenBrowser(stringArg(call, 0), stringArgOr(call, 1, ""))
			return goja.Null()
		})
		// startBrowserAwait：服务端无 WebView，无法等待人工校验。
		// 记录地址后抛出明确错误，避免书源逻辑误把空 body 当成功。
		set("startBrowserAwait", func(call goja.FunctionCall) goja.Value {
			url := stringArg(call, 0)
			r.state.OpenBrowser(url, stringArgOr(call, 1, ""))
			panic(vm.ToValue("java.startBrowserAwait: 服务端无浏览器，需要人工操作的页面请手动打开：" + url))
		})
	}
	// 设备标识：部分源用 deviceID/androidId 做"是否支持该环境"探测，
	// 成功返回会让源走安卓分支，这里统一以异常告知不支持并回退到通用分支。
	for _, name := range []string{"deviceID", "androidId"} {
		set(name, func(call goja.FunctionCall) goja.Value {
			panic(vm.ToValue("java." + name + ": 服务端无设备标识"))
		})
	}
	// 刷新发现页 / 打开界面：纯 UI 动作，服务端空实现。
	for _, name := range []string{"refreshExplore", "open", "showBrowser", "reLoginView", "qread"} {
		set(name, func(call goja.FunctionCall) goja.Value { return goja.Null() })
	}

	// 需要真正无头浏览器/本地文件系统的能力：明确抛出不支持
	unsupported := func(name string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			panic(vm.ToValue("java." + name + " 需要浏览器或本地文件能力，服务端不支持"))
		}
	}
	for _, name := range []string{
		"webView", "webViewGetSource", "webViewGetOverrideUrl",
		"openVideoPlayer", "getVerificationCode",
		"importScript", "cacheFile", "downloadFile", "readFile", "readTxtFile", "deleteFile",
		"unzipFile", "un7zFile", "unrarFile", "unArchiveFile", "getTxtInFolder",
		"getZipStringContent", "getZipByteArrayContent",
		"getRarStringContent", "get7zStringContent",
	} {
		set(name, unsupported(name))
	}

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
