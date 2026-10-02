package rule

import (
	"encoding/json"
	"regexp"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
)

// 本文件对应 AnalyzeUrl.kt 的 URL 解析部分（不含网络执行与 JS 执行）。

// URLOption 对应 AnalyzeUrl.UrlOption。
type URLOption struct {
	Method           string          `json:"method"`
	Charset          string          `json:"charset"`
	Headers          map[string]any  `json:"headers"`
	Body             json.RawMessage `json:"body"`
	Origin           string          `json:"origin"`
	Retry            *int            `json:"retry"`
	Type             string          `json:"type"`
	WebView          json.RawMessage `json:"webView"`
	WebJs            string          `json:"webJs"`
	DnsIp            string          `json:"dnsIp"`
	Js               string          `json:"js"`
	BodyJs           string          `json:"bodyJs"`
	ServerID         json.RawMessage `json:"serverID"`
	WebViewDelayTime json.RawMessage `json:"webViewDelayTime"`
}

// Request 是解析后的可执行请求（供服务层执行）。
type Request struct {
	URL        string // 最终 URL（GET 时已含重编码后的 query）
	URLNoQuery string
	Method     string
	Headers    map[string]string
	Body       string
	IsForm     bool // Body 为已编码的 form 数据
	IsJSON     bool // 以 application/json 发送
	Charset    string
	// HexBody 对应 URL 选项里的 type：声明了 type 时，响应按「原始字节的 hex」
	// 返回而不是解码成文本（对应 legado AnalyzeUrl.type）。
	// 书源用它配合 data: 地址当参数信封，见 datauri.go。
	HexBody bool
	// BodyJsFn 对应 UrlOption.bodyJs：响应体二次处理（JS 执行闭包）。
	BodyJsFn func(body string) string
	// Unsupported 非 nil 表示该请求依赖当前阶段不支持的能力，
	// 值为对应错误（webView；JS 在接入 runner 后已支持）。
	Unsupported error
	// Retry 对应 URL 选项的 retry：可恢复失败时的额外重试次数。
	Retry *int
}

// ParseAnalyzeUrl 对应 AnalyzeUrl.init：URL 规则 → 可执行请求。
// key/page 对应搜索关键词与页码（{{key}}/{{page}}/<1,2,3>）。
func ParseAnalyzeUrl(mUrl, key string, page int, baseUrl string) (*Request, error) {
	return ParseAnalyzeUrlWithJS(mUrl, key, page, baseUrl, nil)
}

// ParseAnalyzeUrlWithJS 在 ParseAnalyzeUrl 基础上支持 JS：
// URL 中的 <js>/@js: 块、{{js}} 内嵌、选项里的 js/bodyJs。
// runner 为 nil 时遇到 JS 标记 Unsupported（P0 兼容路径）。
func ParseAnalyzeUrlWithJS(mUrl, key string, page int, baseUrl string, runner *JSRunner) (*Request, error) {
	req := &Request{Method: "GET", Headers: map[string]string{}}
	// baseUrl 自身可能带 ",{...}" 选项段，先截断（对应 init 中的 paramPattern）
	if st, _, ok := findParamSplit(baseUrl); ok {
		baseUrl = baseUrl[:st]
	}
	ruleUrl := mUrl

	// ── analyzeJs：URL 中的 <js>/@js:，@result 引用前序结果 ──
	if jsPatternRe.MatchString(ruleUrl) {
		if runner == nil {
			req.Unsupported = ErrJsUnsupported
			// 移除 JS 块继续解析，便于调试接口展示其余部分
			ruleUrl = jsPatternRe.ReplaceAllString(ruleUrl, "")
		} else {
			// 对应 AnalyzeUrl.analyzeJs
			start := 0
			result := ruleUrl
			for _, g := range jsPatternRe.FindAllStringSubmatchIndex(ruleUrl, -1) {
				if g[0] > start {
					if seg := strings.TrimSpace(ruleUrl[start:g[0]]); seg != "" {
						result = strings.ReplaceAll(seg, "@result", anyToString(result))
					}
				}
				jsBody := ""
				if g[2] >= 0 {
					jsBody = ruleUrl[g[2]:g[3]]
				} else if g[4] >= 0 {
					jsBody = ruleUrl[g[4]:g[5]]
				}
				v, err := runner.Run(nil, jsBody, result, baseUrl)
				if err != nil {
					return req, err
				}
				result = anyToString(v)
				start = g[1]
			}
			if len(ruleUrl) > start {
				if seg := strings.TrimSpace(ruleUrl[start:]); seg != "" {
					result = strings.ReplaceAll(seg, "@result", result)
				}
			}
			ruleUrl = result
		}
	}

	// ── replaceKeyPageJs：{{...}} 与 <页码列表> ──
	if strings.Contains(ruleUrl, "{{") && strings.Contains(ruleUrl, "}}") {
		var subErr error
		if runner == nil {
			ra := NewRuleAnalyzer(ruleUrl, false)
			out := ra.InnerRule2("{{", "}}", func(inner string) string {
				trimmed := strings.TrimSpace(inner)
				switch trimmed {
				case "key":
					return key
				case "page":
					p := page
					if p < 1 {
						p = 1
					}
					return anyToString(float64(p))
				default:
					subErr = ErrJsUnsupported
					return ""
				}
			})
			if subErr != nil {
				req.Unsupported = subErr
			} else if out != "" {
				ruleUrl = out
			}
		} else {
			// 对应 legado：{{...}} 一律按 JS 执行（key/page 为绑定变量）
			ra := NewRuleAnalyzer(ruleUrl, false)
			out := ra.InnerRule2("{{", "}}", func(inner string) string {
				v, err := runner.Run(nil, strings.TrimSpace(inner), nil, baseUrl)
				if err != nil {
					subErr = err
					return ""
				}
				return anyToString(v)
			})
			if subErr != nil {
				return req, subErr
			}
			if out != "" {
				ruleUrl = out
			}
		}
	}
	if page >= 1 {
		for _, m := range pagePatternRe.FindAllStringSubmatch(ruleUrl, -1) {
			pages := strings.Split(m[1], ",")
			idx := page
			if idx > len(pages) {
				idx = len(pages)
			}
			ruleUrl = strings.ReplaceAll(ruleUrl, m[0], strings.TrimSpace(pages[idx-1]))
		}
	}

	// ── analyzeUrl：分离 URL 与选项 JSON ──
	ruleUrl = strings.TrimSpace(ruleUrl)
	st, end, hasOpt := findParamSplit(ruleUrl)
	urlNoOption := ruleUrl
	if hasOpt {
		urlNoOption = ruleUrl[:st]
	}
	urlNoOption = strings.TrimSpace(urlNoOption)
	finalURL := GetAbsoluteURL(baseUrl, urlNoOption)
	if b := GetBaseUrl(finalURL); b != "" {
		baseUrl = b
	}
	req.URL = finalURL

	if hasOpt {
		optionStr := strings.TrimSpace(ruleUrl[end:])
		var option URLOption
		if err := json.Unmarshal([]byte(optionStr), &option); err != nil {
			// 宽松重试：去掉可能的前后杂字符
			if err2 := json.Unmarshal([]byte(strings.TrimPrefix(optionStr, ",")), &option); err2 != nil {
				return req, nil
			}
		}
		switch strings.ToUpper(option.Method) {
		case "POST":
			req.Method = "POST"
		case "HEAD":
			req.Method = "HEAD"
		}
		for k, v := range option.Headers {
			req.Headers[k] = anyToString(v)
		}
		if len(option.Body) > 0 {
			var bodyStr string
			if err := json.Unmarshal(option.Body, &bodyStr); err == nil {
				req.Body = bodyStr
			} else {
				req.Body = string(option.Body)
			}
		}
		req.Charset = option.Charset
		req.Retry = option.Retry
		// 对应 legado AnalyzeUrl.type：值本身不参与判断，只要非空就把响应按
		// 「原始字节的 hex」返回。书源借此把 data: 地址当参数信封用。
		if option.Type != "" {
			req.HexBody = true
		}
		if option.WebJs != "" && req.Unsupported == nil {
			req.Unsupported = ErrWebJSUnsupported
		}
		if useWebView(option.WebView) && req.Unsupported == nil {
			req.Unsupported = ErrWebJSUnsupported
		}
		// 对应 AnalyzeUrl：option.js 在解析完成后执行，结果覆盖 url
		if option.Js != "" && req.Unsupported == nil {
			if runner == nil {
				req.Unsupported = ErrJsUnsupported
			} else if v, err := runner.Run(nil, option.Js, req.URL, baseUrl); err != nil {
				return req, err
			} else if s := anyToString(v); s != "" {
				req.URL = s
			}
		}
		// 对应 AnalyzeUrl：bodyJs 在响应后执行，结果作为 body
		if option.BodyJs != "" && req.Unsupported == nil && runner != nil {
			req.BodyJsFn = func(body string) string {
				v, err := runner.Run(nil, option.BodyJs, body, baseUrl)
				if err != nil {
					return body
				}
				return anyToString(v)
			}
		} else if option.BodyJs != "" && req.Unsupported == nil {
			req.Unsupported = ErrJsUnsupported
		}
	}

	// ── query / body 编码（对应 analyzeUrl 尾部） ──
	//
	// query 一律先做一次百分号编码规范化：legado 底层的 OkHttp 会把非 ASCII 与
	// 非法字符编码掉，而 Go 的 http 客户端会把 RawQuery 原样写进请求行 —— 原生
	// 中文、花括号、引号会直接上线，服务端多半回 400/空响应（书源侧表现为
	// request() 判定「线路报错」，把全部线路试一遍后返回空串）。
	// POST 同样要编码：query 并不会挪进 body。
	req.URL = normalizeQuery(req.URL, req.Charset)
	if req.Method == "POST" {
		req.URLNoQuery = req.URL
		body := req.Body
		if body != "" && !isJSONStr(body) && !isXMLStr(body) && req.Headers["Content-Type"] == "" {
			req.Body = encodeParams(body, req.Charset, false)
			req.IsForm = true
		} else if isJSONStr(body) && req.Headers["Content-Type"] == "" {
			req.IsJSON = true
		}
	} else if pos := strings.Index(req.URL, "?"); pos != -1 {
		req.URLNoQuery = req.URL[:pos]
	} else {
		req.URLNoQuery = req.URL
	}
	return req, nil
}

// normalizeQuery 把 URL 的 query 规范化成百分号编码形式。
// 已经编码好的 query 原样保留（对应 NetworkUtils.encodedQuery 的短路），data: 地址不动。
func normalizeQuery(rawURL, charset string) string {
	if IsDataURI(rawURL) {
		return rawURL
	}
	pos := strings.Index(rawURL, "?")
	if pos < 0 {
		return rawURL
	}
	base := rawURL[:pos]
	query := encodeParams(rawURL[pos+1:], charset, true)
	if query == "" {
		return base
	}
	return base + "?" + query
}

var pagePatternRe = regexp.MustCompile(`<([^>]*)>`)

func useWebView(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s != "" && s != "false"
	}
	return true
}

func isJSONStr(s string) bool {
	t := strings.TrimSpace(s)
	if !(strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) {
		return false
	}
	return json.Valid([]byte(t))
}

func isXMLStr(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">")
}

// findParamSplit 对应 paramPattern \s*,\s*(?=\{) 的手动实现。
// 返回匹配起点（含逗号前空白）与选项 JSON 起点。
func findParamSplit(s string) (start, end int, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] != ',' {
			continue
		}
		st := i
		for st > 0 && isSpaceByte(s[st-1]) {
			st--
		}
		j := i + 1
		for j < len(s) && isSpaceByte(s[j]) {
			j++
		}
		if j < len(s) && s[j] == '{' {
			return st, j, true
		}
	}
	return 0, 0, false
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// encodeParams 对应 AnalyzeUrl.encodeParams。
func encodeParams(params, charset string, isQuery bool) string {
	checkEncoded := charset == ""
	cs := charset
	if cs == "" {
		cs = "utf-8"
	}
	if isQuery && cs != "escape" {
		if encodedQuery(params) {
			return params
		}
		return queryEncode(params, cs)
	}
	var sb strings.Builder
	lenP := len(params)
	pos := 0
	for pos <= lenP {
		if sb.Len() > 0 {
			sb.WriteString("&")
		}
		ampOffset := strings.IndexByte(params[pos:], '&')
		if ampOffset == -1 {
			ampOffset = lenP
		} else {
			ampOffset += pos
		}
		eqOffset := strings.IndexByte(params[pos:ampOffset], '=')
		if eqOffset == -1 {
			sb.WriteString(appendEncodedStr(params[pos:ampOffset], checkEncoded, cs))
		} else {
			eqAbs := eqOffset + pos
			sb.WriteString(appendEncodedStr(params[pos:eqAbs], checkEncoded, cs))
			sb.WriteString("=")
			sb.WriteString(appendEncodedStr(params[eqAbs+1:ampOffset], checkEncoded, cs))
		}
		pos = ampOffset + 1
	}
	return sb.String()
}

// appendEncodedStr 对应 StringBuilder.appendEncoded。
func appendEncodedStr(value string, checkEncoded bool, charset string) string {
	if checkEncoded && encodedForm(value) {
		return value
	}
	if charset == "escape" {
		return JSEscape(value)
	}
	return javaURLEncode(value, charset)
}

// javaURLEncode 对应 java.net.URLEncoder.encode(value, charset)：
// 字母数字与 .-*_ 保留，空格转 +，其余按字符集字节 %XX。
func javaURLEncode(value, charset string) string {
	enc := encoderFor(charset)
	var sb strings.Builder
	for _, r := range value {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'),
			r == '.', r == '-', r == '*', r == '_':
			sb.WriteRune(r)
		case r == ' ':
			sb.WriteByte('+')
		default:
			for _, b := range enc(string(r)) {
				const hex = "0123456789ABCDEF"
				sb.WriteByte('%')
				sb.WriteByte(hex[b>>4])
				sb.WriteByte(hex[b&0xF])
			}
		}
	}
	return sb.String()
}

// queryEncode 对应 hutool RFC3986.UNRESERVED.orNew(...) 的 query 编码：
// 保留 notNeedEncodingQuery 集合内的字符，其余按字符集字节 %XX。
func queryEncode(params, charset string) string {
	enc := encoderFor(charset)
	var sb strings.Builder
	for _, r := range params {
		if r < 128 && notNeedEncodingQuery[r] {
			sb.WriteRune(r)
			continue
		}
		for _, b := range enc(string(r)) {
			const hex = "0123456789ABCDEF"
			sb.WriteByte('%')
			sb.WriteByte(hex[b>>4])
			sb.WriteByte(hex[b&0xF])
		}
	}
	return sb.String()
}

func encoderFor(charset string) func(string) []byte {
	if strings.EqualFold(charset, "utf-8") || strings.EqualFold(charset, "utf8") {
		utf8Enc := unicode.UTF8
		return func(s string) []byte { b, _ := utf8Enc.NewEncoder().Bytes([]byte(s)); return b }
	}
	enc, err := htmlindex.Get(charset)
	if err != nil {
		utf8Enc := unicode.UTF8
		return func(s string) []byte { b, _ := utf8Enc.NewEncoder().Bytes([]byte(s)); return b }
	}
	return func(s string) []byte { b, _ := enc.NewEncoder().Bytes([]byte(s)); return b }
}
