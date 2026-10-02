package rule

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// 本文件对应 AnalyzeByJSonPath.kt（Jayway JsonPath 语义，Go 侧用
// PaesslerAG/jsonpath 实现）。

type jsonAnalyzer struct {
	root any
}

func newJSONAnalyzer(doc any) *jsonAnalyzer {
	switch t := doc.(type) {
	case string:
		var v any
		if err := json.Unmarshal([]byte(t), &v); err != nil {
			return &jsonAnalyzer{root: nil}
		}
		return &jsonAnalyzer{root: v}
	default:
		return &jsonAnalyzer{root: doc}
	}
}

// jsonRead 对应 ctx.read(rule)：路径求值，失败返回 nil。
func jsonRead(root any, path string) any {
	if root == nil || path == "" {
		return nil
	}
	v, err := jsonpathGet(path, root)
	if err != nil {
		return nil
	}
	return v
}

// jsonValueToString 对应 Kotlin 的 ob.toString() / joinToString("\n")。
// 列表按 "\n" 拼接（legado 的 getString 就是这么做的）；对象走 Java 的 Map.toString
// 形态（见 javaValueString），书源的正则大量依赖这个形态。
func jsonValueToString(ob any) string {
	switch t := ob.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = jsonValueToString(e)
		}
		return strings.Join(parts, "\n")
	default:
		return javaValueString(t)
	}
}

// javaValueString 按 Java 的 Map/List toString 形态输出 JSON 值。
//
// 书源规则里的正则就是照着这个形态写的，最典型的是拷贝漫画的作者规则
// `$.author##.*name=(.*?)\,.*##$1`：它期望拿到 `{name=岸本斉史, alias=…}`。
// 之前这里用 Go 的 fmt 输出 `map[alias:… name:…]`，`name=` 根本匹配不到，
// 作者字段就退化成一整串 map 文本。
//
// 已知差异：Java 的 LinkedTreeMap 保留 JSON 里的键顺序，而 Go 的
// map[string]interface{} 不保序（JSONPath 库也只认这个类型），这里按 **键排序**
// 输出以保证确定性。绝大多数「取某个键」的正则不受影响，但依赖原始键序的正则
// （如 `.*name=(.*?)\,` 且 name 是最后一个键）仍可能与阅读 App 的结果不同。
func javaValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = javaValueString(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(javaValueString(t[k]))
		}
		sb.WriteByte('}')
		return sb.String()
	default:
		return anyToString(v)
	}
}

// getString 对应 AnalyzeByJSonPath.getString。
func (a *jsonAnalyzer) getString(rule string) string {
	if rule == "" {
		return ""
	}
	ra := NewRuleAnalyzer(rule, true)
	rules := ra.SplitRule("&&", "||")
	if len(rules) == 1 {
		ra.ReSetPos()
		result := ra.InnerRule("{$.", 1, 1, func(inner string) string {
			return a.getString(inner)
		})
		if result == "" {
			result = jsonValueToString(jsonRead(a.root, rule))
		}
		return result
	}
	var textList []string
	for _, rl := range rules {
		temp := a.getString(rl)
		if temp != "" {
			textList = append(textList, temp)
			if ra.ElementsType() == "||" {
				break
			}
		}
	}
	return strings.Join(textList, "\n")
}

// getStringList 对应 AnalyzeByJSonPath.getStringList。
func (a *jsonAnalyzer) getStringList(rule string) []string {
	var result []string
	if rule == "" {
		return result
	}
	ra := NewRuleAnalyzer(rule, true)
	rules := ra.SplitRule("&&", "||", "%%")
	if len(rules) == 1 {
		ra.ReSetPos()
		st := ra.InnerRule("{$.", 1, 1, func(inner string) string {
			return a.getString(inner)
		})
		if st == "" {
			ob := jsonRead(a.root, rule)
			if ob == nil {
				return result
			}
			if lst, ok := ob.([]any); ok {
				for _, o := range lst {
					result = append(result, jsonValueToString(o))
				}
			} else {
				result = append(result, jsonValueToString(ob))
			}
		} else {
			result = append(result, st)
		}
		return result
	}
	var results [][]string
	for _, rl := range rules {
		temp := a.getStringList(rl)
		if len(temp) > 0 {
			results = append(results, temp)
			if ra.ElementsType() == "||" {
				break
			}
		}
	}
	if len(results) > 0 {
		if ra.ElementsType() == "%%" {
			for i := 0; i < len(results[0]); i++ {
				for _, temp := range results {
					if i < len(temp) {
						result = append(result, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				result = append(result, temp...)
			}
		}
	}
	return result
}

// getObject 对应 getObject：直接返回求值结果。
func (a *jsonAnalyzer) getObject(rule string) any {
	return jsonRead(a.root, rule)
}

// getList 对应 getList：要求路径结果为数组（对应 jayway read<ArrayList>，
// 非数组时 legado 侧捕获异常返回空列表）。
func (a *jsonAnalyzer) getList(rule string) []any {
	var result []any
	if rule == "" {
		return result
	}
	ra := NewRuleAnalyzer(rule, true)
	rules := ra.SplitRule("&&", "||", "%%")
	if len(rules) == 1 {
		ob := jsonRead(a.root, rules[0])
		if lst, ok := ob.([]any); ok {
			return lst
		}
		return result
	}
	var results [][]any
	for _, rl := range rules {
		temp := a.getList(rl)
		if len(temp) > 0 {
			results = append(results, temp)
			if ra.ElementsType() == "||" {
				break
			}
		}
	}
	if len(results) > 0 {
		if ra.ElementsType() == "%%" {
			for i := 0; i < len(results[0]); i++ {
				for _, temp := range results {
					if i < len(temp) {
						result = append(result, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				result = append(result, temp...)
			}
		}
	}
	return result
}
