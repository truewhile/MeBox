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
	// Unsupported 非 nil 表示该请求依赖当前阶段不支持的能力，
	// 值为对应错误（webView/js/type）。
	Unsupported error
}

// ParseAnalyzeUrl 对应 AnalyzeUrl.init：URL 规则 → 可执行请求。
// key/page 对应搜索关键词与页码（{{key}}/{{page}}/<1,2,3>）。
func ParseAnalyzeUrl(mUrl, key string, page int, baseUrl string) (*Request, error) {
	req := &Request{Method: "GET", Headers: map[string]string{}}
	// baseUrl 自身可能带 ",{...}" 选项段，先截断（对应 init 中的 paramPattern）
	if st, _, ok := findParamSplit(baseUrl); ok {
		baseUrl = baseUrl[:st]
	}
	ruleUrl := mUrl

	// ── analyzeJs：URL 中的 <js>/@js:（P0 不支持） ──
	if jsPatternRe.MatchString(ruleUrl) {
		req.Unsupported = ErrJsUnsupported
		// 移除 JS 块继续解析，便于调试接口展示其余部分
		ruleUrl = jsPatternRe.ReplaceAllString(ruleUrl, "")
	}

	// ── replaceKeyPageJs：{{...}} 与 <页码列表> ──
	if strings.Contains(ruleUrl, "{{") && strings.Contains(ruleUrl, "}}") {
		var subErr error
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
		if option.Type != "" && req.Unsupported == nil {
			req.Unsupported = ErrTypeUnsupported
		}
		if option.WebJs != "" && req.Unsupported == nil {
			req.Unsupported = ErrWebJSUnsupported
		}
		if useWebView(option.WebView) && req.Unsupported == nil {
			req.Unsupported = ErrWebJSUnsupported
		}
		if option.Js != "" && req.Unsupported == nil {
			req.Unsupported = ErrJsUnsupported
		}
		if option.BodyJs != "" && req.Unsupported == nil {
			req.Unsupported = ErrJsUnsupported
		}
	}

	// ── query / body 编码（对应 analyzeUrl 尾部） ──
	if req.Method == "POST" {
		req.URLNoQuery = req.URL
		body := req.Body
		if body != "" && !isJSONStr(body) && !isXMLStr(body) && req.Headers["Content-Type"] == "" {
			req.Body = encodeParams(body, req.Charset, false)
			req.IsForm = true
		} else if isJSONStr(body) && req.Headers["Content-Type"] == "" {
			req.IsJSON = true
		}
	} else {
		pos := strings.Index(req.URL, "?")
		if pos != -1 {
			query := encodeParams(req.URL[pos+1:], req.Charset, true)
			req.URLNoQuery = req.URL[:pos]
			if query != "" {
				req.URL = req.URLNoQuery + "?" + query
			} else {
				req.URL = req.URLNoQuery
			}
		} else {
			req.URLNoQuery = req.URL
		}
	}
	return req, nil
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
