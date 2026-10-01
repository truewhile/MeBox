package rule

import (
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
)

// 本文件对应 legado 的 NetworkUtils.kt / EncoderUtils.kt 中与规则引擎相关的函数。

// GetAbsoluteURL 对应 NetworkUtils.getAbsoluteURL(baseURL: String?, relativePath)。
// baseURL 会先截掉 ",{...}" 选项段（substringBefore(",")）。
func GetAbsoluteURL(baseURL, relativePath string) string {
	rel := strings.TrimSpace(relativePath)
	if isAbsURL(rel) || isDataURL(rel) || strings.HasPrefix(rel, "javascript") {
		if strings.HasPrefix(rel, "javascript") {
			return ""
		}
		return rel
	}
	if baseURL == "" || isDataURL(baseURL) {
		return rel
	}
	base := baseURL
	if i := strings.Index(base, ","); i >= 0 {
		base = base[:i]
	}
	baseURLParsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return rel
	}
	return GetAbsoluteURLParsed(baseURLParsed, rel)
}

// GetAbsoluteURLParsed 对应 NetworkUtils.getAbsoluteURL(baseURL: URL?, relativePath)。
func GetAbsoluteURLParsed(base *url.URL, relativePath string) string {
	rel := strings.TrimSpace(relativePath)
	if base == nil {
		return rel
	}
	if isAbsURL(rel) || isDataURL(rel) {
		return rel
	}
	if strings.HasPrefix(rel, "javascript") {
		return ""
	}
	ref, err := url.Parse(rel)
	if err != nil {
		return rel
	}
	return base.ResolveReference(ref).String()
}

// GetBaseUrl 对应 NetworkUtils.getBaseUrl：scheme://host[:port]。
func GetBaseUrl(u string) string {
	if len(u) >= 8 && (strings.EqualFold(u[:7], "http://") || (len(u) >= 9 && strings.EqualFold(u[:8], "https://"))) {
		if idx := strings.Index(u[8:], "/"); idx >= 0 {
			return u[:8+idx]
		}
		return u
	}
	return ""
}

// isAbsURL 对应 String.isAbsUrl()。
func isAbsURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func isDataURL(s string) bool {
	return strings.HasPrefix(s, "data:")
}

// notNeedEncodingQuery / notNeedEncodingForm 对应 NetworkUtils 的两个 BitSet。
var (
	notNeedEncodingQuery = buildEncodingSet("!$&()*+,-./:;=?@[\\]^_`{|}~")
	notNeedEncodingForm  = buildEncodingSet("*-._")
)

func buildEncodingSet(extra string) map[rune]bool {
	set := make(map[rune]bool, 128)
	for r := 'a'; r <= 'z'; r++ {
		set[r] = true
	}
	for r := 'A'; r <= 'Z'; r++ {
		set[r] = true
	}
	for r := '0'; r <= '9'; r++ {
		set[r] = true
	}
	for _, r := range extra {
		set[r] = true
	}
	return set
}

func isDigit16(r byte) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// encodedQuery 对应 NetworkUtils.encodedQuery(str)：判断字符串是否已按
// urlEncode 规范编码（无需再编码返回 true）。
func encodedQuery(s string) bool {
	return encodedWith(s, notNeedEncodingQuery)
}

// encodedForm 对应 NetworkUtils.encodedForm(str)。
func encodedForm(s string) bool {
	return encodedWith(s, notNeedEncodingForm)
}

func encodedWith(s string, allow map[rune]bool) bool {
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r < 128 && allow[r] {
			continue
		}
		if r == '%' && i+2 < len(rs) && isDigit16(byte(rs[i+1])) && isDigit16(byte(rs[i+2])) {
			i += 2
			continue
		}
		return false
	}
	return len(rs) > 0
}

// JSEscape 对应 EncoderUtils.escape（JS escape() 语义）。
func JSEscape(src string) string {
	var sb strings.Builder
	for _, r := range src {
		code := int(r)
		if (code >= 48 && code <= 57) || (code >= 65 && code <= 90) || (code >= 97 && code <= 122) {
			sb.WriteRune(r)
			continue
		}
		switch {
		case code < 16:
			fmt.Fprintf(&sb, "%%0%X", code)
		case code < 256:
			fmt.Fprintf(&sb, "%%%X", code)
		default:
			fmt.Fprintf(&sb, "%%u%04X", code)
		}
	}
	return sb.String()
}

// DecodeBytes 按字符集名解码字节流，name 为空时按 UTF-8（带 BOM 处理）。
func DecodeBytes(b []byte, name string) (string, error) {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return string(b[3:]), nil
	}
	if name == "" || strings.EqualFold(name, "utf-8") || strings.EqualFold(name, "utf8") {
		dec := unicode.UTF8.NewDecoder()
		out, err := dec.Bytes(b)
		if err != nil {
			return string(b), nil
		}
		return string(out), nil
	}
	enc, err := htmlindex.Get(name)
	if err != nil {
		return string(b), nil
	}
	out, derr := enc.NewDecoder().Bytes(b)
	if derr != nil {
		return string(b), nil
	}
	return string(out), nil
}

// LooksLikeJSON 对应 String.isJson()（宽松：trim 后以 { 或 [ 开头）。
func LooksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}
