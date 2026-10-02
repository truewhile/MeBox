// Package reader — legado 书源兼容的阅读子系统服务层。
// 本文件对应 legado data/entities/BookSource.kt 的 JSON 结构。
package reader

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// BookSource 书源 JSON 结构（字段与 legado 实体一致，未知字段忽略）。
type BookSource struct {
	RawJSON           string         `json:"-"`
	BookSourceURL     string         `json:"bookSourceUrl"`
	BookSourceName    string         `json:"bookSourceName"`
	BookSourceGroup   *string        `json:"bookSourceGroup"`
	BookSourceType    *int           `json:"bookSourceType"` // 0文本 1音频 2图片 3文件 4视频
	BookURLPattern    *string        `json:"bookUrlPattern"`
	CustomOrder       *int           `json:"customOrder"`
	Enabled           *bool          `json:"enabled"`
	EnabledExplore    *bool          `json:"enabledExplore"`
	EnabledCookieJar  *bool          `json:"enabledCookieJar"`
	ConcurrentRate    *string        `json:"concurrentRate"`
	Header            *string        `json:"header"`
	LoginURL          *string        `json:"loginUrl"`
	LoginUI           *string        `json:"loginUi"`
	LoginCheckJS      *string        `json:"loginCheckJs"`
	JSLib             *string        `json:"jsLib"`
	BookSourceComment *string        `json:"bookSourceComment"`
	LastUpdateTime    *int64         `json:"lastUpdateTime"`
	RespondTime       *int64         `json:"respondTime"`
	Weight            *int           `json:"weight"`
	ExploreURL        *string        `json:"exploreUrl"`
	SearchURL         *string        `json:"searchUrl"`
	RuleExplore       *ExploreRule   `json:"ruleExplore"`
	RuleSearch        *SearchRule    `json:"ruleSearch"`
	RuleBookInfo      *BookInfoRule  `json:"ruleBookInfo"`
	RuleToc           *TocRule       `json:"ruleToc"`
	RuleContent       *ContentRule   `json:"ruleContent"`
	VariableComment   *string        `json:"variableComment"`
	Variables         map[string]any `json:"-"`
	RawVariables      *string        `json:"variables"`
}

// EnabledCookieJarOrDefault 是否启用 Cookie 自动携带（legado 默认 true）。
func (b *BookSource) EnabledCookieJarOrDefault() bool {
	return b.EnabledCookieJar == nil || *b.EnabledCookieJar
}

// HasLogin 是否声明了登录能力（登录需要 loginUrl 的 JS 或 loginUi 表单）。
func (b *BookSource) HasLogin() bool {
	return strings.TrimSpace(SPtr(b.LoginURL)) != "" || strings.TrimSpace(SPtr(b.LoginUI)) != ""
}

// SearchCheckKeyWord 书源在搜索规则里声明的校验关键字（legado SearchRule.checkKeyWord）。
//
// 源的作者用它证明「这个地址返回的确实是正常搜索结果」：校验时搜这个关键字，
// 响应里应该出现它；被风控/换域名后响应里就没有了。legado 的 getCheckKeyword
// 对含 http / :: / ++ / -- 的值不认（那是地址或扩展标记，不是关键字），这里保持一致。
func (b *BookSource) SearchCheckKeyWord() string {
	if b == nil || b.RuleSearch == nil {
		return ""
	}
	kw := strings.TrimSpace(SPtr(b.RuleSearch.CheckKeyWord))
	if kw == "" || strings.Contains(kw, "http") || strings.Contains(kw, "::") ||
		strings.Contains(kw, "++") || strings.Contains(kw, "--") {
		return ""
	}
	return kw
}

// LoginJS 返回 loginUrl 的纯 JS 体（剥掉 @js: / <js>…< 包裹）。
// 对应 legado BaseSource.getLoginJs()：loginUi 的按钮 action 会拼在其后执行，
// 因此 loginUrl 同时充当登录交互的函数库。
func (b *BookSource) LoginJS() string {
	return stripJSWrapper(SPtr(b.LoginURL))
}

// stripJSWrapper 去掉 JS 规则的 @js: / <js>…</js> 包裹。
func stripJSWrapper(s string) string {
	out, _ := stripJSWrapperOK(s)
	return out
}

// stripJSWrapperOK 与 stripJSWrapper 相同，但额外告知是否命中 JS 包装。
// 「可能是 JSON 也可能是 JS 规则」的字段（如书源 header）需要区分这两者。
func stripJSWrapperOK(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "@js:"):
		return trimmed[len("@js:"):], true
	case strings.HasPrefix(lower, "<js>"):
		body := trimmed[len("<js>"):]
		body = strings.TrimSuffix(strings.TrimSpace(body), "</js>")
		body = strings.TrimSuffix(strings.TrimSpace(body), "<")
		return body, true
	default:
		return trimmed, false
	}
}

// SearchRule 搜索规则。
type SearchRule struct {
	CheckKeyWord *string `json:"checkKeyWord"`
	BookList     *string `json:"bookList"`
	Name         *string `json:"name"`
	Author       *string `json:"author"`
	Kind         *string `json:"kind"`
	WordCount    *string `json:"wordCount"`
	LastChapter  *string `json:"lastChapter"`
	Intro        *string `json:"intro"`
	CoverURL     *string `json:"coverUrl"`
	BookURL      *string `json:"bookUrl"`
}

// BookInfoRule 详情规则。
type BookInfoRule struct {
	Init         *string `json:"init"`
	Name         *string `json:"name"`
	Author       *string `json:"author"`
	Kind         *string `json:"kind"`
	WordCount    *string `json:"wordCount"`
	LastChapter  *string `json:"lastChapter"`
	Intro        *string `json:"intro"`
	CoverURL     *string `json:"coverUrl"`
	TocURL       *string `json:"tocUrl"`
	CanReName    *string `json:"canReName"`
	DownloadUrls *string `json:"downloadUrls"`
}

// TocRule 目录规则。
type TocRule struct {
	PreUpdateJs *string `json:"preUpdateJs"`
	ChapterList *string `json:"chapterList"`
	ChapterName *string `json:"chapterName"`
	ChapterURL  *string `json:"chapterUrl"`
	IsVolume    *string `json:"isVolume"`
	UpdateTime  *string `json:"updateTime"`
	// NextTocURL 下一页目录规则（可为多值）。长书目录按 offset/limit 分页时靠它
	// 取全，缺了就只能拿到第一页（如 399 章只出现 100 章）。
	NextTocURL *string `json:"nextTocUrl"`
}

// ContentRule 正文规则。
type ContentRule struct {
	Content        *string `json:"content"`
	NextContentURL *string `json:"nextContentUrl"`
	WebJs          *string `json:"webJs"`
	SourceRegex    *string `json:"sourceRegex"`
	ReplaceRegex   *string `json:"replaceRegex"`
	ImageStyle     *string `json:"imageStyle"`
	PayAction      *string `json:"payAction"`
}

// ExploreRule 发现规则。
type ExploreRule struct {
	BookList     *string `json:"bookList"`
	Name         *string `json:"name"`
	Author       *string `json:"author"`
	Kind         *string `json:"kind"`
	WordCount    *string `json:"wordCount"`
	LastChapter  *string `json:"lastChapter"`
	Intro        *string `json:"intro"`
	CoverURL     *string `json:"coverUrl"`
	BookURL      *string `json:"bookUrl"`
	ExploreURL   *string `json:"exploreUrl"`
	ExploreKinds *string `json:"exploreKinds"`
	CheckKeyWord *string `json:"checkKeyWord"`
}

// ParseBookSource 将书源 JSON 解析为结构体。
//
// 先按标准 JSON 解一次；失败时用「宽容版」再解一次（见 normalizeSourceJSON）。
// 阅读生态里的导出工具写法五花八门，同一份书源在别处能用、在这里却整体导入失败，
// 只因为某个字段的类型变了形状 —— 这类差异不值得让用户改 JSON。
func ParseBookSource(raw string) (*BookSource, error) {
	var bs BookSource
	if err := json.Unmarshal([]byte(raw), &bs); err != nil {
		var relaxed BookSource
		if err2 := json.Unmarshal([]byte(normalizeSourceJSON(raw)), &relaxed); err2 != nil {
			return nil, err // 报原始错误：字段位置更贴近用户看到的 JSON
		}
		bs = relaxed
	}
	bs.RawJSON = raw
	if bs.RawVariables != nil && strings.TrimSpace(*bs.RawVariables) != "" {
		_ = json.Unmarshal([]byte(*bs.RawVariables), &bs.Variables)
	}
	return &bs, nil
}

// sourceNumericFields 是书源里可能被写成字符串的数字字段。
// 例：{"lastUpdateTime":"1788449879889"}（阅读本体导出/第三方源站的常见写法）。
var sourceNumericFields = []string{
	"customOrder", "weight", "bookSourceType", "lastUpdateTime", "respondTime",
}

// sourceRuleObjectFields 在 legado 里是对象，但部分导出工具把空规则写成 []。
// 例：{"ruleExplore":[]}（空发现规则）。
var sourceRuleObjectFields = []string{
	"ruleExplore", "ruleSearch", "ruleBookInfo", "ruleToc", "ruleContent",
}

// normalizeSourceJSON 消化书源 JSON 与 legado 实体之间的形状差异：
//   - 数字字段被写成字符串 → 转成数字；
//   - 规则对象被写成空数组 [] → 转成空对象 {}。
//
// 只动顶层已知字段，规则 JS 字符串原样保留。解析不出来就原样返回，交给调用方报错。
func normalizeSourceJSON(raw string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return raw
	}
	changed := false
	for _, key := range sourceNumericFields {
		value, ok := fields[key]
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			continue // 本来就是数字（或其它类型）：不掺和
		}
		text = strings.TrimSpace(text)
		if text == "" {
			// 空字符串当成「没有这个字段」，否则会报类型错误
			delete(fields, key)
			changed = true
			continue
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			continue // 不是整数（如 "1.0"）：不猜，让标准解析去报错
		}
		fields[key] = json.RawMessage(text)
		changed = true
	}
	for _, key := range sourceRuleObjectFields {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("[]")) {
			fields[key] = json.RawMessage("{}")
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return string(out)
}

// SourceProps 书源 JSON 原样转为 map（注入 JS 的 `source` 对象）。
func (b *BookSource) SourceProps() map[string]any {
	if b.RawJSON == "" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(b.RawJSON), &m) != nil {
		return nil
	}
	return m
}

// Type 返回书源类型（默认文本）。
func (b *BookSource) Type() int {
	if b.BookSourceType == nil {
		return 0
	}
	return *b.BookSourceType
}

// SPtr 取字符串指针字段值。
func SPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// IPtr 取整型指针字段值。
func IPtr(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}
