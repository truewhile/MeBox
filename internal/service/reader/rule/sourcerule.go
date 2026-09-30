package rule

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// 本文件对应 AnalyzeRule.kt 中的 SourceRule 内部类与 splitSourceRule。

// Mode 规则模式（对应 AnalyzeRule.Mode）。
type Mode int

const (
	ModeXPath Mode = iota
	ModeJson
	ModeDefault
	ModeJs
	ModeRegex
	ModeWebJs
)

var (
	jsPatternRe    = regexp.MustCompile(`(?i)<js>([\w\W]*?)</js>|@js:([\w\W]*)`)
	webJsPatternRe = regexp.MustCompile(`(?i)@webjs:([\w\W]{5,})`)
	putPatternRe   = regexp.MustCompile(`(?i)@put:(\{[^}]+?\})`)
	evalPatternRe  = regexp.MustCompile(`(?i)@get:\{[^}]+?\}|\{\{[\w\W]*?\}\}`)
	regexGroupRe   = regexp.MustCompile(`\$\d{1,2}`)
)

// makeUpRule 参数类型（对应 SourceRule 的 ruleType 常量）。
const (
	paramGet    = -2 // @get:{name}
	paramJs     = -1 // {{js}}
	paramText   = 0  // 字面文本
	paramGroupN = 1  // >0 为 $N 正则分组引用，typ 即分组序号
)

type ruleParam struct {
	typ int
	val string
}

// SourceRule 是拆分后的单条规则（对应 AnalyzeRule.SourceRule）。
type SourceRule struct {
	Rule string
	Mode Mode

	putMap     map[string]string
	ruleParams []ruleParam
}

// SplitSourceRule 对应 AnalyzeRule.splitSourceRule(ruleStr, allInOne)。
// contentIsJSON 对应 Kotlin 成员 isJSON（当前内容是否为 JSON）。
// isRegex 对应 AnalyzeRule.isRegex 持久标记，可为 nil。
func SplitSourceRule(ruleStr string, allInOne, contentIsJSON bool, isRegex *bool) []*SourceRule {
	if ruleStr == "" {
		return nil
	}
	mode := ModeDefault
	start := 0
	if allInOne && strings.HasPrefix(ruleStr, ":") {
		mode = ModeRegex
		if isRegex != nil {
			*isRegex = true
		}
		start = 1
	} else if isRegex != nil && *isRegex {
		mode = ModeRegex
	}

	type rulePart struct {
		start int
		end   int
		rule  string
		mode  Mode
	}
	var parts []rulePart
	for _, g := range jsPatternRe.FindAllStringSubmatchIndex(ruleStr, -1) {
		jsBody := ""
		if g[2] >= 0 { // group(1)： <js>...</js>
			jsBody = ruleStr[g[2]:g[3]]
		} else if g[4] >= 0 { // group(2)： @js:...
			jsBody = ruleStr[g[4]:g[5]]
		}
		parts = append(parts, rulePart{g[0], g[1], jsBody, ModeJs})
	}
	for _, g := range webJsPatternRe.FindAllStringSubmatchIndex(ruleStr, -1) {
		parts = append(parts, rulePart{g[0], g[1], ruleStr[g[2]:g[3]], ModeWebJs})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].start < parts[j].start })

	var out []*SourceRule
	for _, p := range parts {
		if p.start < start {
			continue
		}
		if p.start > start {
			if tmp := strings.TrimSpace(ruleStr[start:p.start]); tmp != "" {
				out = append(out, newSourceRule(tmp, mode, contentIsJSON))
			}
		}
		out = append(out, newSourceRule(p.rule, p.mode, contentIsJSON))
		start = p.end
	}
	if len(ruleStr) > start {
		if tmp := strings.TrimSpace(ruleStr[start:]); tmp != "" {
			out = append(out, newSourceRule(tmp, mode, contentIsJSON))
		}
	}
	return out
}

// newSourceRule 对应 SourceRule.init：模式识别 + 分离 put + 拆分 @get/{{}}/$N。
func newSourceRule(ruleStr string, mode Mode, contentIsJSON bool) *SourceRule {
	sr := &SourceRule{Mode: mode, putMap: map[string]string{}}
	rule := ruleStr
	switch {
	case mode == ModeJs || mode == ModeRegex:
		// 保持原样
	case hasPrefixFold(ruleStr, "@CSS:"):
		sr.Mode = ModeDefault
	case strings.HasPrefix(ruleStr, "@@"):
		sr.Mode = ModeDefault
		rule = ruleStr[2:]
	case hasPrefixFold(ruleStr, "@XPath:"):
		sr.Mode = ModeXPath
		rule = ruleStr[7:]
	case hasPrefixFold(ruleStr, "@Json:"):
		sr.Mode = ModeJson
		rule = ruleStr[6:]
	case contentIsJSON || strings.HasPrefix(ruleStr, "$.") || strings.HasPrefix(ruleStr, "$["):
		sr.Mode = ModeJson
	case strings.HasPrefix(ruleStr, "/"):
		sr.Mode = ModeXPath
	}
	// 分离 @put:{...}
	rule = splitPutRule(rule, sr.putMap)
	sr.Rule = rule
	sr.splitEvalParams()
	return sr
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// splitPutRule 对应 splitPutRule：提取并移除 @put:{...} 段。
func splitPutRule(ruleStr string, putMap map[string]string) string {
	out := ruleStr
	for _, m := range putPatternRe.FindAllStringSubmatch(ruleStr, -1) {
		out = strings.Replace(out, m[0], "", 1)
		parsed := map[string]string{}
		if err := json.Unmarshal([]byte(m[1]), &parsed); err == nil {
			for k, v := range parsed {
				putMap[k] = v
			}
			continue
		}
		// 宽松解析（对应 GSON lenient）：key:val 键值对
		pairRe := regexp.MustCompile(`["']?(\w+)["']?\s*:\s*(["']?)([^,{}]*?)\2`)
		for _, pm := range pairRe.FindAllStringSubmatch(m[1], -1) {
			putMap[pm[1]] = pm[3]
		}
	}
	return out
}

// splitEvalParams 对应 init 中 @get/{{ }} 的拆分循环。
func (sr *SourceRule) splitEvalParams() {
	rule := sr.Rule
	start := 0
	locs := evalPatternRe.FindAllStringIndex(rule, -1)
	if len(locs) > 0 {
		firstStart := locs[0][0]
		prefix := rule[:firstStart]
		if sr.Mode != ModeJs && sr.Mode != ModeRegex &&
			(firstStart == 0 || !strings.Contains(prefix, "##")) {
			sr.Mode = ModeRegex
		}
		for _, loc := range locs {
			if loc[0] > start {
				sr.splitRegex(rule[start:loc[0]])
			}
			tmp := rule[loc[0]:loc[1]]
			switch {
			case hasPrefixFold(tmp, "@get:"):
				sr.ruleParams = append(sr.ruleParams, ruleParam{paramGet, tmp[6 : len(tmp)-1]})
			case strings.HasPrefix(tmp, "{{"):
				sr.ruleParams = append(sr.ruleParams, ruleParam{paramJs, tmp[2 : len(tmp)-2]})
			default:
				sr.splitRegex(tmp)
			}
			start = loc[1]
		}
	}
	if len(rule) > start {
		sr.splitRegex(rule[start:])
	}
}

// splitRegex 对应 SourceRule.splitRegex：拆分 $N 分组引用。
// $N 匹配只作用于 "##" 前的第一段，"##..." 尾部作为字面参数保留，
// 由 MakeUpRule 重新按 "##" 切分。
func (sr *SourceRule) splitRegex(ruleStr string) {
	start := 0
	first := strings.Split(ruleStr, "##")[0]
	locs := regexGroupRe.FindAllStringIndex(first, -1)
	if len(locs) > 0 {
		if sr.Mode != ModeJs && sr.Mode != ModeRegex {
			sr.Mode = ModeRegex
		}
		for _, loc := range locs {
			if loc[0] > start {
				sr.ruleParams = append(sr.ruleParams, ruleParam{paramText, ruleStr[start:loc[0]]})
			}
			n := 0
			fmt.Sscanf(ruleStr[loc[0]+1:loc[1]], "%d", &n)
			sr.ruleParams = append(sr.ruleParams, ruleParam{n, ruleStr[loc[0]:loc[1]]})
			start = loc[1]
		}
	}
	if len(ruleStr) > start {
		sr.ruleParams = append(sr.ruleParams, ruleParam{paramText, ruleStr[start:]})
	}
}

// ResolvedSourceRule 对应 AnalyzeRule.ResolvedSourceRule。
type ResolvedSourceRule struct {
	Rule         string
	ReplaceRegex string
	Replacement  string
	ReplaceFirst bool
	ParamSize    int
}

// RuleDeps 是 MakeUpRule 解析内嵌 {{...}} 时需要的执行环境。
type RuleDeps struct {
	// JS 执行 {{js}}（P0 未接入 goja 时返回 ErrJsUnsupported）
	JS func(js string, result any) (any, error)
	// Rule 执行 {{@rule}} / {{$.rule}} 形式的规则引用
	Rule func(rule string) (string, error)
	// Get 对应 AnalyzeRule.get(key)
	Get func(key string) string
}

// MakeUpRule 对应 SourceRule.makeUpRule(result)：替换 @get/{{}}/$N，
// 再按 "##" 切分出正则段。
func (sr *SourceRule) MakeUpRule(result any, deps *RuleDeps) (ResolvedSourceRule, error) {
	resolved := sr.Rule
	if len(sr.ruleParams) > 0 {
		var infoVal []byte
		for i := len(sr.ruleParams) - 1; i >= 0; i-- {
			p := sr.ruleParams[i]
			switch {
			case p.typ >= paramGroupN:
				// 对应 Kotlin：(result as? List<String?>) 成功但越界时不插入任何内容
				switch lst := result.(type) {
				case []string:
					if len(lst) > p.typ {
						infoVal = prepend(infoVal, lst[p.typ])
					}
				case []any:
					if len(lst) > p.typ {
						if s, ok := lst[p.typ].(string); ok {
							infoVal = prepend(infoVal, s)
						}
					}
				default:
					infoVal = prepend(infoVal, p.val)
				}
			case p.typ == paramJs:
				if isRuleString(p.val) {
					s, err := deps.Rule(p.val)
					if err != nil {
						return ResolvedSourceRule{}, err
					}
					infoVal = prepend(infoVal, s)
				} else {
					v, err := deps.JS(p.val, result)
					if err != nil {
						return ResolvedSourceRule{}, err
					}
					infoVal = prepend(infoVal, anyToString(v))
				}
			case p.typ == paramGet:
				infoVal = prepend(infoVal, deps.Get(p.val))
			default:
				infoVal = prepend(infoVal, p.val)
			}
		}
		resolved = string(infoVal)
	}
	segs := strings.Split(resolved, "##")
	r := ResolvedSourceRule{
		Rule:         strings.TrimSpace(segs[0]),
		ReplaceFirst: len(segs) > 3,
		ParamSize:    len(sr.ruleParams),
	}
	if len(segs) > 1 {
		r.ReplaceRegex = segs[1]
	}
	if len(segs) > 2 {
		r.Replacement = segs[2]
	}
	return r, nil
}

func prepend(dst []byte, s string) []byte {
	return append([]byte(s), dst...)
}

func isRuleString(s string) bool {
	return strings.HasPrefix(s, "@") ||
		strings.HasPrefix(s, "$.") ||
		strings.HasPrefix(s, "$[") ||
		strings.HasPrefix(s, "//")
}

// anyToString 对应 Kotlin 值字符串化（整值 Double 去小数，对应 %.0f）。
func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%.0f", t)
		}
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
