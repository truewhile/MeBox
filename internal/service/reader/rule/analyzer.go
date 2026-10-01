package rule

import (
	"net/url"
	"strings"

	"github.com/PaesslerAG/jsonpath"
	"golang.org/x/net/html"
)

// 本文件对应 AnalyzeRule.kt 主类。

// AnalyzeRule 解析规则获取结果（对应 AnalyzeRule）。
type AnalyzeRule struct {
	content     any
	baseUrl     string
	redirectURL *url.URL
	isJSON      bool
	isRegex     bool

	// jsRunner 由 P2 阶段的 goja 引擎注入；nil 时 JS 规则报 ErrJsUnsupported。
	jsRunner func(js string, result any) (any, error)

	// 变量层级（对应 chapter → book → ruleData → source）
	chapterVars map[string]string
	bookVars    map[string]string
	vars        map[string]string
	chapterTitle string
	chapterIndex int
	bookName     string
	// bookMeta 书籍元数据（对应 legado 规则 JS 里的 Book 实体字段）。
	bookMeta map[string]any
	// bookCustom 书籍自定义变量（对应 legado Book.variableMap），
	// 由规则 JS 的 book.getVariable / book.putVariable 读写。
	bookCustom map[string]string
	// bookTypeOverride 书源在规则 JS 里给 book.type 赋的值
	// （听书/漫画/短剧源靠它声明书籍类型），由服务层读回。
	bookTypeOverride *int
	sourceGetter     func(key string) string
	sourcePutter     func(key, value string)

	ruleCache map[string][]*SourceRule
}

// NewAnalyzeRule 创建解析器。
func NewAnalyzeRule() *AnalyzeRule {
	return &AnalyzeRule{ruleCache: map[string][]*SourceRule{}}
}

// SetJSRunner 注入 JS 执行器（P2）。
func (a *AnalyzeRule) SetJSRunner(runner func(js string, result any) (any, error)) {
	a.jsRunner = runner
}

// SetContent 对应 setContent(content, baseUrl)。
func (a *AnalyzeRule) SetContent(content any, baseUrl string) *AnalyzeRule {
	a.content = content
	switch content.(type) {
	case *html.Node:
		a.isJSON = false
	default:
		a.isJSON = LooksLikeJSON(anyToString(content))
	}
	if baseUrl != "" {
		a.baseUrl = baseUrl
	}
	return a
}

// SetBaseUrl 对应 setBaseUrl。
func (a *AnalyzeRule) SetBaseUrl(baseUrl string) *AnalyzeRule {
	if baseUrl != "" {
		a.baseUrl = baseUrl
	}
	return a
}

// SetRedirectUrl 对应 setRedirectUrl。
func (a *AnalyzeRule) SetRedirectUrl(u string) *AnalyzeRule {
	if strings.HasPrefix(u, "data:") {
		return a
	}
	if parsed, err := url.Parse(u); err == nil {
		a.redirectURL = parsed
	}
	return a
}

// SetChapterContext 设置章节上下文（title 与章节级变量存储）。
func (a *AnalyzeRule) SetChapterContext(title string, vars map[string]string) {
	a.chapterTitle = title
	if vars != nil {
		a.chapterVars = vars
	}
}

// SetChapterIndex 设置当前章节下标（规则 JS 的 chapter.index）。
func (a *AnalyzeRule) SetChapterIndex(i int) { a.chapterIndex = i }

// SetBookMeta 注入书籍元数据（legado 的 Book 实体字段），
// 供规则 JS 里的 `book` 对象读取（name/author/coverUrl/durChapterIndex…）。
func (a *AnalyzeRule) SetBookMeta(meta map[string]any) {
	if len(meta) == 0 {
		return
	}
	if a.bookMeta == nil {
		a.bookMeta = make(map[string]any, len(meta))
	}
	for k, v := range meta {
		a.bookMeta[k] = v
	}
}

// SetBookCustomVars 注入书籍自定义变量（对应 legado Book.variableMap），
// 规则 JS 通过 book.getVariable / book.putVariable 读写。
func (a *AnalyzeRule) SetBookCustomVars(vars map[string]string) {
	if vars != nil {
		a.bookCustom = vars
	}
}

// SetBookType 记录书源声明的书籍类型（legado Book.type）。
func (a *AnalyzeRule) SetBookType(t int) {
	v := t
	a.bookTypeOverride = &v
}

// BookTypeOverride 返回书源在规则 JS 里声明的书籍类型；
// ok 为 false 表示书源没有声明（应沿用书架记录里的类型）。
func (a *AnalyzeRule) BookTypeOverride() (int, bool) {
	if a.bookTypeOverride == nil {
		return 0, false
	}
	return *a.bookTypeOverride, true
}

// bookTypeValue 供 book.type 读取：优先书源本次声明的值，否则用元数据里的。
func (a *AnalyzeRule) bookTypeValue() any {
	if a.bookTypeOverride != nil {
		return *a.bookTypeOverride
	}
	if v, ok := a.bookMeta["type"]; ok {
		return v
	}
	return 0
}

// SetBookContext 设置书籍上下文（name 与书籍级变量存储）。
func (a *AnalyzeRule) SetBookContext(name string, vars map[string]string) {
	a.bookName = name
	if vars != nil {
		a.bookVars = vars
	}
}

// SetSourceVariables 注入书源级变量读写（source.variableMap）。
func (a *AnalyzeRule) SetSourceVariables(getter func(key string) string, putter func(key, value string)) {
	a.sourceGetter = getter
	a.sourcePutter = putter
}

// jsonpathGet 包装 PaesslerAG/jsonpath.Get。
func jsonpathGet(path string, root any) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, err = nil, ErrJsUnsupported
		}
	}()
	return jsonpath.Get(path, root)
}

// ─── 变量存取（对应 put/get） ───────────────────────────────────────────────

// Put 对应 put(key, value)：chapter → book → 局部 → source。
func (a *AnalyzeRule) Put(key, value string) string {
	switch {
	case a.chapterVars != nil:
		a.chapterVars[key] = value
	case a.bookVars != nil:
		a.bookVars[key] = value
	default:
		if a.vars == nil {
			a.vars = map[string]string{}
		}
		a.vars[key] = value
	}
	return value
}

// Get 对应 get(key)：特殊键 bookName/title 优先取上下文。
func (a *AnalyzeRule) Get(key string) string {
	switch key {
	case "bookName":
		if a.bookName != "" {
			return a.bookName
		}
	case "title":
		if a.chapterTitle != "" {
			return a.chapterTitle
		}
	}
	for _, store := range []map[string]string{a.chapterVars, a.bookVars, a.vars} {
		if store != nil {
			if v, ok := store[key]; ok && v != "" {
				return v
			}
		}
	}
	if a.sourceGetter != nil {
		if v := a.sourceGetter(key); v != "" {
			return v
		}
	}
	return ""
}

// ─── JS ────────────────────────────────────────────────────────────────────

func (a *AnalyzeRule) evalJS(js string, result any) (any, error) {
	if a.jsRunner == nil {
		return nil, ErrJsUnsupported
	}
	return a.jsRunner(js, result)
}

// ─── 规则拆分缓存 ──────────────────────────────────────────────────────────

func (a *AnalyzeRule) splitSourceRuleCached(ruleStr string) []*SourceRule {
	if ruleStr == "" {
		return nil
	}
	if cached, ok := a.ruleCache[ruleStr]; ok {
		return cached
	}
	rules := SplitSourceRule(ruleStr, false, a.isJSON, &a.isRegex)
	a.ruleCache[ruleStr] = rules
	return rules
}

func (a *AnalyzeRule) putRule(putMap map[string]string) error {
	for k, v := range putMap {
		s, err := a.GetString(v, nil, false)
		if err != nil {
			return err
		}
		a.Put(k, s)
	}
	return nil
}

func (a *AnalyzeRule) makeDeps() *RuleDeps {
	return &RuleDeps{
		JS:   a.evalJS,
		Rule: func(rule string) (string, error) { return a.GetString(rule, nil, false) },
		Get:  a.Get,
	}
}

// ─── 主流程 ────────────────────────────────────────────────────────────────

// GetStringList 对应 getStringList(rule, mContent, isUrl)。
func (a *AnalyzeRule) GetStringList(ruleStr string, mContent any, isUrl bool) ([]string, error) {
	if ruleStr == "" {
		return nil, nil
	}
	ruleList := a.splitSourceRuleCached(ruleStr)
	return a.getStringListRules(ruleList, mContent, isUrl)
}

func (a *AnalyzeRule) getStringListRules(ruleList []*SourceRule, mContent any, isUrl bool) ([]string, error) {
	var result any
	content := mContent
	if content == nil {
		content = a.content
	}
	if content != nil && len(ruleList) > 0 {
		result = content
		for _, sourceRule := range ruleList {
			if err := a.putRule(sourceRule.putMap); err != nil {
				return nil, err
			}
			resolved, err := sourceRule.MakeUpRule(result, a.makeDeps())
			if err != nil {
				return nil, err
			}
			if result == nil {
				continue
			}
			rule := resolved.Rule
			if rule != "" || resolved.ReplaceRegex == "" {
				switch sourceRule.Mode {
				case ModeWebJs:
					return nil, ErrWebJSUnsupported
				case ModeJs:
					result, err = a.evalJS(rule, result)
					if err != nil {
						return nil, err
					}
				case ModeJson:
					result = newJSONAnalyzer(result).getStringList(rule)
				case ModeXPath:
					result = newXPathAnalyzer(result).getStringList(rule)
				case ModeDefault:
					result = newJsoupAnalyzer(result).getStringList(rule)
				default:
					result = rule
				}
			}
			if resolved.ReplaceRegex != "" {
				if lst, ok := result.([]string); ok {
					out := make([]string, 0, len(lst))
					for _, item := range lst {
						out = append(out, applyReplaceRegex(item, resolved))
					}
					result = out
				} else {
					result = applyReplaceRegex(resultString(result), resolved)
				}
			}
		}
	}
	if result == nil {
		return nil, nil
	}
	if s, ok := result.(string); ok {
		result = strings.Split(s, "\n")
	}
	if isUrl {
		var urlList []string
		if lst, ok := result.([]string); ok {
			for _, u := range lst {
				abs := a.absolutize(u)
				if abs != "" && !containsStr(urlList, abs) {
					urlList = append(urlList, abs)
				}
			}
		}
		return urlList, nil
	}
	switch t := result.(type) {
	case []string:
		return t, nil
	case []any:
		out := make([]string, len(t))
		for i, v := range t {
			out[i] = anyToString(v)
		}
		return out, nil
	case []*html.Node:
		out := make([]string, len(t))
		for i, v := range t {
			out[i] = outerHTML(v)
		}
		return out, nil
	default:
		return []string{anyToString(result)}, nil
	}
}

// GetString 对应 getString(rule, mContent, isUrl)。
func (a *AnalyzeRule) GetString(ruleStr string, mContent any, isUrl bool) (string, error) {
	if ruleStr == "" {
		return "", nil
	}
	ruleList := a.splitSourceRuleCached(ruleStr)
	return a.getStringRules(ruleList, mContent, isUrl)
}

func (a *AnalyzeRule) getStringRules(ruleList []*SourceRule, mContent any, isUrl bool) (string, error) {
	var result any
	content := mContent
	if content == nil {
		content = a.content
	}
	if content != nil && len(ruleList) > 0 {
		result = content
		for _, sourceRule := range ruleList {
			if err := a.putRule(sourceRule.putMap); err != nil {
				return "", err
			}
			resolved, err := sourceRule.MakeUpRule(result, a.makeDeps())
			if err != nil {
				return "", err
			}
			if result == nil {
				continue
			}
			rule := resolved.Rule
			if rule != "" || resolved.ReplaceRegex == "" {
				switch sourceRule.Mode {
				case ModeWebJs:
					return "", ErrWebJSUnsupported
				case ModeJs:
					result, err = a.evalJS(rule, result)
					if err != nil {
						return "", err
					}
				case ModeJson:
					result = newJSONAnalyzer(result).getString(rule)
				case ModeXPath:
					result = newXPathAnalyzer(result).getString(rule)
				case ModeDefault:
					if isUrl {
						result = newJsoupAnalyzer(result).getString0(rule)
					} else {
						result = newJsoupAnalyzer(result).getString(rule)
					}
				default:
					result = rule
				}
			}
			if result != nil && resolved.ReplaceRegex != "" {
				result = applyReplaceRegex(resultString(result), resolved)
			}
		}
	}
	if result == nil {
		result = ""
	}
	str := resultString(result)
	if strings.Contains(str, "&") {
		str = html.UnescapeString(str)
	}
	if isUrl {
		if strings.TrimSpace(str) == "" {
			return a.baseUrl, nil
		}
		return a.absolutize(str), nil
	}
	return str, nil
}

// GetElement 对应 getElement(ruleStr)。
func (a *AnalyzeRule) GetElement(ruleStr string) (any, error) {
	if ruleStr == "" {
		return nil, nil
	}
	var result any
	content := a.content
	ruleList := SplitSourceRule(ruleStr, true, a.isJSON, &a.isRegex)
	if content != nil && len(ruleList) > 0 {
		result = content
		for _, sourceRule := range ruleList {
			if err := a.putRule(sourceRule.putMap); err != nil {
				return nil, err
			}
			resolved, err := sourceRule.MakeUpRule(result, a.makeDeps())
			if err != nil {
				return nil, err
			}
			if result == nil {
				continue
			}
			rule := resolved.Rule
			switch sourceRule.Mode {
			case ModeRegex:
				result = regexGetElement(resultString(result), splitNotBlankAndTrim(rule, "&&"), 0)
			case ModeWebJs:
				return nil, ErrWebJSUnsupported
			case ModeJs:
				result, err = a.evalJS(rule, result)
				if err != nil {
					return nil, err
				}
			case ModeJson:
				result = newJSONAnalyzer(result).getObject(rule)
			case ModeXPath:
				result = newXPathAnalyzer(result).getElements(rule)
			default:
				result = newJsoupAnalyzer(result).getElements(rule)
			}
			if resolved.ReplaceRegex != "" {
				result = applyReplaceRegex(resultString(result), resolved)
			}
		}
	}
	return result, nil
}

// GetElements 对应 getElements(ruleStr)：列表获取。
// 注意：与 Kotlin 一致，这里不做 makeUpRule（无 ## 正则段处理）。
func (a *AnalyzeRule) GetElements(ruleStr string) ([]any, error) {
	var result any
	content := a.content
	ruleList := SplitSourceRule(ruleStr, true, a.isJSON, &a.isRegex)
	if content != nil && len(ruleList) > 0 {
		result = content
		for _, sourceRule := range ruleList {
			if err := a.putRule(sourceRule.putMap); err != nil {
				return nil, err
			}
			if result == nil {
				continue
			}
			rule := sourceRule.Rule
			var err error
			switch sourceRule.Mode {
			case ModeRegex:
				result = regexGetElements(resultString(result), splitNotBlankAndTrim(rule, "&&"), 0)
			case ModeWebJs:
				return nil, ErrWebJSUnsupported
			case ModeJs:
				result, err = a.evalJS(rule, result)
				if err != nil {
					return nil, err
				}
			case ModeJson:
				result = newJSONAnalyzer(result).getList(rule)
			case ModeXPath:
				result = newXPathAnalyzer(result).getElements(rule)
			default:
				result = newJsoupAnalyzer(result).getElements(rule)
			}
		}
	}
	if result != nil {
		switch t := result.(type) {
		case []any:
			return t, nil
		case []string:
			out := make([]any, len(t))
			for i, v := range t {
				out[i] = v
			}
			return out, nil
		case []*html.Node:
			out := make([]any, len(t))
			for i, v := range t {
				out[i] = v
			}
			return out, nil
		}
	}
	return []any{}, nil
}

// ─── 工具 ──────────────────────────────────────────────────────────────────

func (a *AnalyzeRule) absolutize(u string) string {
	if strings.TrimSpace(u) == "" {
		return ""
	}
	if a.redirectURL != nil {
		return GetAbsoluteURLParsed(a.redirectURL, u)
	}
	return GetAbsoluteURL(a.baseUrl, u)
}

// applyReplaceRegex 对应 replaceRegex(result, resolved)。
func applyReplaceRegex(result string, r ResolvedSourceRule) string {
	if r.ReplaceRegex == "" {
		return result
	}
	if r.ReplaceFirst {
		return regexReplaceFirstOnFirstMatch(r.ReplaceRegex, result, r.Replacement)
	}
	return regexReplaceAll(r.ReplaceRegex, result, r.Replacement)
}

// resultString 对应 Kotlin 的 result.toString()。
func resultString(result any) string {
	switch t := result.(type) {
	case nil:
		return ""
	case string:
		return t
	case *html.Node:
		return outerHTML(t)
	case []string:
		return strings.Join(t, "\n")
	case []*html.Node:
		var sb strings.Builder
		for _, n := range t {
			sb.WriteString(outerHTML(n))
		}
		return sb.String()
	default:
		return anyToString(result)
	}
}
