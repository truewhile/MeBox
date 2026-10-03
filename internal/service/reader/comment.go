package reader

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

// 本文件：正文里的「段评」锚点解析（对齐 legado 的段评机制）。
//
// legado 侧段评有两种落在正文里的形态：
//
//  1. 段落里的 <comment ident="评论地址" count="评论数" />：宿主（苹果/源阅分支）
//     直接把它解析成段落旁的评论气泡；
//  2. 「改版」宿主不认 <comment>，改由书源在正文规则里把它换成一张内嵌 SVG 图片，
//     图片地址后面再跟一段 JSON 配置：
//     <img src="data:image/svg+xml;base64,…{"click":"showCmt('评论地址','平台','段评')","style":"text"}">
//     legado 识别的是地址后面的这份配置（ReadBookActivity.oldClickImg），点击时把
//     配置里的 click/js 当 JS 执行（showCmt 内部再调 java.showBrowser 打开评论页）。
//
// MeBox 没有 Android 的 Canvas 排版，正文按行渲染，因此这里把两种形态都归一成
// 「某一行的段评气泡」：只摘掉标记本身，正文文字一个字都不动，并把评论地址 / 评论数
// 结构化下发给前端；点击气泡时前端复用宿主浏览器（带书源 Cookie）打开评论地址。
//
// 约定：但凡无法确定评论地址的图片，一律原样保留交给既有链路，绝不猜一个地址出来。

// ContentComment 正文里的一条段评锚点（随正文一起下发给前端）。
type ContentComment struct {
	// Line 段落行号：Content 按 "\n" 拆分后的 0-based 下标。
	Line int `json:"line"`
	// Count 评论数（0 表示未知：前端只画一个不带数字的气泡）。
	Count int `json:"count"`
	// URL 评论地址（书源 showCmt 的第一个参数），前端点击时交给宿主浏览器打开。
	URL string `json:"url"`
	// Label 评论类型：段评 / 本章说，用作气泡提示。
	Label string `json:"label,omitempty"`
	// Block 独占一行的整块评论（章末「本章说」），前端按居中整块渲染。
	Block bool `json:"block,omitempty"`
}

// commentTagRe 匹配正文里的 <comment …> 标签。属性顺序不定、可能自闭合，
// 也可能带一个配对结束标签（`<comment …>…</comment>`），一并吃掉。
var commentTagRe = regexp.MustCompile(`(?is)<comment\b[^>]*?/?>(?:</comment>)?`)

// 下面三条属性正则都允许单/双引号两种写法；捕获组 1 是双引号内容，2 是单引号内容。
var (
	commentIdentRe   = regexp.MustCompile(`(?is)\bident\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	commentCountRe   = regexp.MustCompile(`(?is)\bcount\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	commentOnPressRe = regexp.MustCompile(`(?is)\bonPress\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// commentJSURLRe 从 showCmt / showReadingBrowser / startBrowserDp 的调用里取第一个字符串参数。
// 苹果分支的 <comment onPress="java.showReadingBrowser('地址',…)" /> 就靠它取地址。
// RE2 不支持反向引用，双/单引号两种写法拆成两个捕获组。
var commentJSURLRe = regexp.MustCompile(`(?is)(?:showCmt|showReadingBrowser|startBrowserDp)\s*\(\s*(?:"([^"]*)"|'([^']*)')`)

// imgParamRe 宽容地提取图片地址后面 JSON 配置里的键值对。
//
// 不用 encoding/json 解析：光遇聚合的「本章说」把配置拼成 {'type':'qtbzs',"click":"…",'style':'FULL'}
// （单双引号混用），严格 JSON 解析会失败，而它又是合法 JS 对象字面量。
var imgParamRe = regexp.MustCompile(`(?is)["']?(click|js|style|type|width)["']?\s*:\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)')`)

// svgCountRe 取内嵌段评 SVG 里画在气泡上的数字（书源把评论数直接画进了 SVG 文本）。
var svgCountRe = regexp.MustCompile(`(?is)<text[^>]*>\s*([0-9]+)\s*\+?\s*</text>`)

// wrapMarkupRe 匹配不带内容的块级标签，用来判断一行是不是「只有评论标记」。
var wrapMarkupRe = regexp.MustCompile(`(?is)</?(?:p|div|br|hr|h[1-6]|article|dd|dl)\b[^>]*>`)

// extractContentComments 摘出正文里的段评标记，返回清理后的正文与结构化锚点。
//
// 只改行内内容、不增删行，因此评论的 Line 与调用方按 "\n" 拆出的下标始终一致。
func extractContentComments(content string) (string, []ContentComment) {
	if content == "" || (!strings.Contains(content, "<comment") && !strings.Contains(content, "<img")) {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	out := make([]string, len(lines))
	comments := make([]ContentComment, 0, 4)
	changed := false
	for i, line := range lines {
		text, cs := extractLineComments(line, i)
		out[i] = text
		if len(cs) > 0 {
			changed = true
			comments = append(comments, cs...)
		}
	}
	if !changed {
		return content, nil
	}
	return strings.Join(out, "\n"), comments
}

// extractLineComments 处理一行：返回去掉段评标记后的文字与该行上的段评。
func extractLineComments(line string, lineNo int) (string, []ContentComment) {
	type marker struct {
		start, end int
		comment    ContentComment
	}
	var markers []marker

	for _, m := range commentTagRe.FindAllStringIndex(line, -1) {
		if c, ok := parseCommentTag(line[m[0]:m[1]], lineNo); ok {
			markers = append(markers, marker{m[0], m[1], c})
		}
	}
	for _, tag := range scanImgTags(line) {
		if c, ok := parseCommentImage(tag.src, lineNo); ok {
			markers = append(markers, marker{tag.start, tag.end, c})
		}
	}
	if len(markers) == 0 {
		return line, nil
	}

	sort.Slice(markers, func(i, j int) bool { return markers[i].start < markers[j].start })
	comments := make([]ContentComment, 0, len(markers))
	var b strings.Builder
	prev := 0
	for _, mk := range markers {
		if mk.start < prev { // 与前一个标记重叠（理论上不会出现），跳过避免重复删除
			continue
		}
		b.WriteString(line[prev:mk.start])
		prev = mk.end
		c := mk.comment
		// 摘掉标记后整行只剩标签/空白 → 说明评论独占一行（章末「本章说」）
		c.Block = isBlankMarkup(line[:mk.start] + line[mk.end:])
		comments = append(comments, c)
	}
	b.WriteString(line[prev:])
	return b.String(), comments
}

// imgTagMatch 一行里一个 <img> 标签的位置与 src 值。
type imgTagMatch struct {
	start, end int
	src        string
}

// scanImgTags 扫描一行里的所有 <img> 标签，返回标签位置与 src 值。
//
// 不能用 <img[^>]*src="([^"]*)"> 这类正则：书源把段评 SVG 拼成
// <img src="data:…,{"click":"showCmt('…','…')"}"> —— 属性值里的 JSON 又用了双引号，
// 正则会在第一个内层引号处截断。这里改为手工扫描：进入花括号后忽略引号，
// 只把「花括号之外遇到的引号」当作属性值的结束。
func scanImgTags(line string) []imgTagMatch {
	var out []imgTagMatch
	lower := strings.ToLower(line)
	for i := 0; i < len(line); {
		idx := strings.Index(lower[i:], "<img")
		if idx < 0 {
			break
		}
		start := i + idx
		src, end, ok := scanImgTagSrc(line, start)
		if !ok {
			i = start + 4
			continue
		}
		out = append(out, imgTagMatch{start: start, end: end, src: src})
		i = end
	}
	return out
}

// scanImgTagSrc 从 <img 起始位置扫描出 src 属性值，并返回标签结束下标（含 '>'）。
func scanImgTagSrc(line string, start int) (string, int, bool) {
	lower := strings.ToLower(line)
	i := start + 4 // 跳过 "<img"
	for {
		si := strings.Index(lower[i:], "src")
		if si < 0 {
			return "", 0, false
		}
		si += i
		q := si + 3
		for q < len(line) && isHTMLSpace(line[q]) {
			q++
		}
		// 必须是 src= 而不是 srcset= 之类的近似字段
		if q >= len(line) || line[q] != '=' {
			i = si + 3
			continue
		}
		q++
		for q < len(line) && isHTMLSpace(line[q]) {
			q++
		}
		if q >= len(line) || (line[q] != '"' && line[q] != '\'') {
			i = si + 3
			continue
		}
		quote := line[q]
		valStart := q + 1
		depth := 0
		j := valStart
		for ; j < len(line); j++ {
			c := line[j]
			switch {
			case c == '{':
				depth++
			case c == '}' && depth > 0:
				depth--
			case c == quote && depth == 0:
				// 花括号之外的引号才是属性值的结束
				gt := strings.IndexByte(line[j:], '>')
				if gt < 0 {
					return "", 0, false
				}
				return line[valStart:j], j + gt + 1, true
			}
		}
		return "", 0, false
	}
}

// isHTMLSpace 判断 HTML 属性之间的空白（含换行/制表）。
func isHTMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// parseCommentTag 解析宿主侧的 <comment ident count /> 形式。
func parseCommentTag(tag string, lineNo int) (ContentComment, bool) {
	rawURL := attrOf(commentIdentRe, tag)
	if rawURL == "" {
		rawURL = attrOf(commentOnPressRe, tag)
	}
	rawURL = extractCommentURL(rawURL)
	if rawURL == "" {
		return ContentComment{}, false
	}
	count, _ := strconv.Atoi(strings.TrimSpace(attrOf(commentCountRe, tag)))
	return ContentComment{Line: lineNo, Count: count, URL: rawURL, Label: "段评"}, true
}

// parseCommentImage 解析「图片地址 + JSON 配置」形式的段评。
//
// 只把真正指向评论的气泡当段评：地址要么来自 showCmt/showReadingBrowser，
// 要么图片本身就是内嵌的段评 SVG。其它带 click 配置的图片保持原样，避免误伤。
func parseCommentImage(src string, lineNo int) (ContentComment, bool) {
	src = strings.TrimSpace(src)
	if src == "" {
		return ContentComment{}, false
	}
	base, params := splitImgSrcParams(src)
	if len(params) == 0 {
		return ContentComment{}, false
	}
	action := params["click"]
	if action == "" {
		action = params["js"]
	}
	if action == "" {
		return ContentComment{}, false
	}
	isCommentCall := strings.Contains(action, "showCmt") || strings.Contains(action, "showReadingBrowser")
	if !isCommentCall && !strings.HasPrefix(base, "data:image/svg+xml") {
		return ContentComment{}, false
	}
	rawURL := extractCommentURL(action)
	if rawURL == "" {
		return ContentComment{}, false
	}
	label := "段评"
	if strings.Contains(action, "本章说") {
		label = "本章说"
	}
	return ContentComment{
		Line:  lineNo,
		Count: commentCountFromImage(base),
		URL:   rawURL,
		Label: label,
	}, true
}

// splitImgSrcParams 把图片地址拆成（地址, JSON 配置），没有配置时返回 (src, nil)。
//
// 对应 legado 的 AnalyzeUrl.paramPattern `\s*,\s*(?=\{)`：地址与配置之间用「逗号 + 花括号」分隔。
func splitImgSrcParams(src string) (string, map[string]string) {
	for i := 0; i < len(src); i++ {
		if src[i] != ',' {
			continue
		}
		j := i + 1
		for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
			j++
		}
		if j >= len(src) || src[j] != '{' {
			continue
		}
		end := strings.LastIndexByte(src, '}')
		if end <= j {
			continue
		}
		if params := parseImgParams(src[j : end+1]); len(params) > 0 {
			return strings.TrimSpace(src[:i]), params
		}
	}
	return src, nil
}

// parseImgParams 宽容解析图片配置里的键值对（见 imgParamRe 的说明）。
func parseImgParams(raw string) map[string]string {
	ms := imgParamRe.FindAllStringSubmatch(raw, -1)
	if len(ms) == 0 {
		return nil
	}
	out := make(map[string]string, len(ms))
	for _, m := range ms {
		val := m[2]
		if val == "" {
			val = m[3]
		}
		val = strings.ReplaceAll(val, `\"`, `"`)
		val = strings.ReplaceAll(val, `\'`, `'`)
		out[strings.ToLower(m[1])] = val
	}
	return out
}

// commentCountFromImage 从内嵌段评 SVG 里取评论数（书源把数字画进了 SVG 文本）。
// 取不到时返回 0，前端只画一个不带数字的气泡。
func commentCountFromImage(src string) int {
	decoded, ok := decodeCommentDataURI(src)
	if !ok {
		return 0
	}
	m := svgCountRe.FindStringSubmatch(decoded)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// decodeCommentDataURI 解出 data: URI 的文本内容（支持 base64 与明文两种）。
func decodeCommentDataURI(s string) (string, bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", false
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return "", false
	}
	meta, payload := s[:comma], s[comma+1:]
	if strings.HasSuffix(strings.ToLower(meta), ";base64") {
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return payload, true
}

// extractCommentURL 从 showCmt/showReadingBrowser 调用或裸地址里取评论地址。
func extractCommentURL(js string) string {
	js = strings.TrimSpace(js)
	if js == "" {
		return ""
	}
	if m := commentJSURLRe.FindStringSubmatch(js); m != nil {
		return trimCommentURL(firstSubmatch(m))
	}
	if strings.HasPrefix(js, "http://") || strings.HasPrefix(js, "https://") {
		return trimCommentURL(js)
	}
	return ""
}

// attrOf 取属性值，双引号优先、单引号兜底。
func attrOf(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return firstSubmatch(m)
}

// firstSubmatch 返回「双引号 / 单引号」两个捕获组里非空的那个。
func firstSubmatch(m []string) string {
	if len(m) > 1 && m[1] != "" {
		return m[1]
	}
	if len(m) > 2 {
		return m[2]
	}
	return ""
}

// trimCommentURL 去空白并还原 HTML 实体。
// 苹果/源阅分支会把地址里的 & 转成 &amp;（paraForiOS），直接拿去请求会 404。
func trimCommentURL(raw string) string {
	r := strings.NewReplacer(
		"&quot;", `"`, "&#34;", `"`,
		"&#39;", "'", "&apos;", "'",
		"&lt;", "<", "&gt;", ">",
		"&amp;", "&",
	)
	return strings.TrimSpace(r.Replace(raw))
}

// isBlankMarkup 判断一段文字去掉空块级标签/空白后是否为空。
func isBlankMarkup(s string) bool {
	s = wrapMarkupRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "\u3000", " ")
	return strings.TrimSpace(s) == ""
}

// ─── 打开段评（宿主浏览器承载） ─────────────────────────────────────────────

// OpenContentComment 在宿主浏览器里打开一条段评。
//
// 书源点击段评气泡时执行的是它自己的 showCmt(...)：先 java.ajax 取评论页，
// 再 java.showBrowser 展示。MeBox 不跑书源 JS，而是把 showCmt 的第一个参数
// （评论地址）解析出来（见本文件），复用承载登录页的那套宿主浏览器打开：
// 服务端带书源 Cookie 抓取评论页，前端 iframe 展示。这样评论接口需要的登录态
// 仍然生效——前端直接 window.open 会以未登录身份访问。
//
// 返回的 BrowserPage 直接给前端渲染，不必走轮询（段评只需展示、无需回传 DOM）。
func (s *ReaderService) OpenContentComment(ctx context.Context, userID, bookID, rawURL, title string) (*BrowserPage, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("阅读服务未就绪")
	}
	target, err := validateCommentURL(rawURL)
	if err != nil {
		return nil, err
	}
	book, err := s.repo.GetBook(ctx, bookID)
	if err != nil || book == nil {
		return nil, fmt.Errorf("书籍不存在或已移出书架")
	}
	sourceID := ""
	if src, findErr := s.repo.GetSourceByURL(ctx, book.Origin); findErr == nil && src != nil {
		sourceID = src.ID
	}
	if strings.TrimSpace(title) == "" {
		title = "段评"
	}
	entry, err := s.registerBrowser(ctx, book.Origin, sourceID, userID,
		// 段评是「只展示」的页面，没有登录会话可回写 Cookie。
		browserCookieTarget{},
		rule.BrowserTask{URL: target, Title: title}, browserModeOpen)
	if err != nil {
		return nil, err
	}
	page := s.browserPageOf(entry)
	return &page, nil
}

// validateCommentURL 校验评论地址：只允许 http(s)，并挡住内网地址。
//
// 段评地址来自书源正文，会带着书源登录态由服务端去抓；不做限制就等于给了一个
// 「以书源身份请求任意地址」的口子（SSRF）。
func validateCommentURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", fmt.Errorf("评论地址为空")
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("评论地址无效：%s", truncateForLog(u, 80))
	}
	if isPrivateHost(parsed.Hostname()) {
		return "", fmt.Errorf("评论地址指向内网，已拒绝")
	}
	return u, nil
}

// isPrivateHost 判断主机名是否是回环/内网/链路本地地址。
func isPrivateHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()
}
