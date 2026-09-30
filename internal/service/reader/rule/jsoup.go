package rule

import (
	"strings"

	"golang.org/x/net/html"
)

// 本文件对应 AnalyzeByJSoup.kt（含 ElementsSingle 索引语法）。

type jsoupAnalyzer struct {
	root *html.Node
}

func newJsoupAnalyzer(doc any) *jsoupAnalyzer {
	return &jsoupAnalyzer{root: toHTMLNode(doc)}
}

// toHTMLNode 对应 AnalyzeByJSoup.parse(doc)：节点直通，字符串解析为 HTML。
func toHTMLNode(doc any) *html.Node {
	switch t := doc.(type) {
	case nil:
		return nil
	case *html.Node:
		return t
	default:
		return parseHTML(anyToString(t))
	}
}

// getStringList 对应 AnalyzeByJSoup.getStringList。
func (a *jsoupAnalyzer) getStringList(ruleStr string) []string {
	var textS []string
	if ruleStr == "" {
		return textS
	}
	isCss := false
	elementsRule := ruleStr
	if hasPrefixFold(ruleStr, "@CSS:") {
		isCss = true
		elementsRule = strings.TrimSpace(ruleStr[5:])
	}

	if elementsRule == "" {
		if d := nodeData(a.root); d != "" {
			textS = append(textS, d)
		} else {
			textS = append(textS, "")
		}
		return textS
	}

	ra := NewRuleAnalyzer(elementsRule, false)
	ruleStrS := ra.SplitRule("&&", "||", "%%")

	var results [][]string
	for _, ruleStrX := range ruleStrS {
		var temp []string
		if isCss {
			// 对应 isCss 分支：lastIndexOf('@') 分离选择器与提取规则
			lastIndex := strings.LastIndex(ruleStrX, "@")
			var elements []*html.Node
			var lastRule string
			if lastIndex == -1 {
				elements = selectCSS(a.root, ruleStrX)
				lastRule = "text"
			} else {
				selector := ruleStrX[:lastIndex]
				if strings.TrimSpace(selector) == "" {
					elements = []*html.Node{a.root}
				} else {
					elements = selectCSS(a.root, selector)
				}
				lastRule = ruleStrX[lastIndex+1:]
			}
			temp = getResultLast(elements, lastRule)
		} else {
			temp = a.getResultList(ruleStrX)
		}
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
						textS = append(textS, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				textS = append(textS, temp...)
			}
		}
	}
	return textS
}

// getString 对应 getString：多结果以 \n 连接。
func (a *jsoupAnalyzer) getString(ruleStr string) string {
	list := a.getStringList(ruleStr)
	if len(list) == 0 {
		return ""
	}
	if len(list) == 1 {
		return list[0]
	}
	return strings.Join(list, "\n")
}

// getString0 对应 getString0：只取第一个。
func (a *jsoupAnalyzer) getString0(ruleStr string) string {
	list := a.getStringList(ruleStr)
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// getElements 对应 getElements。
func (a *jsoupAnalyzer) getElements(rule string) []*html.Node {
	if rule == "" || a.root == nil {
		return nil
	}
	isCss := false
	elementsRule := rule
	if hasPrefixFold(rule, "@CSS:") {
		isCss = true
		elementsRule = strings.TrimSpace(rule[5:])
	}
	ra := NewRuleAnalyzer(elementsRule, false)
	ruleStrS := ra.SplitRule("&&", "||", "%%")

	var elementsList [][]*html.Node
	if isCss {
		for _, ruleStr := range ruleStrS {
			tempS := selectCSS(a.root, ruleStr)
			elementsList = append(elementsList, tempS)
			if len(tempS) > 0 && ra.ElementsType() == "||" {
				break
			}
		}
	} else {
		for _, ruleStr := range ruleStrS {
			rsRule := NewRuleAnalyzer(ruleStr, false)
			rsRule.Trim()
			rs := rsRule.SplitRule("@")
			var el []*html.Node
			if len(rs) > 1 {
				el = []*html.Node{a.root}
				for _, rl := range rs {
					var es []*html.Node
					for _, et := range el {
						es = append(es, a.getElementsOf(et, rl)...)
					}
					el = es
				}
			} else {
				es := newElementsSingle().getElementsSingle(a.root, ruleStr)
				el = es
			}
			elementsList = append(elementsList, el)
			if len(el) > 0 && ra.ElementsType() == "||" {
				break
			}
		}
	}
	var elements []*html.Node
	if len(elementsList) > 0 {
		if ra.ElementsType() == "%%" {
			for i := 0; i < len(elementsList[0]); i++ {
				for _, es := range elementsList {
					if i < len(es) {
						elements = append(elements, es[i])
					}
				}
			}
		} else {
			for _, es := range elementsList {
				elements = append(elements, es...)
			}
		}
	}
	return elements
}

// getElementsOf 对应私有 getElements(temp, rule) 递归。
func (a *jsoupAnalyzer) getElementsOf(temp *html.Node, rule string) []*html.Node {
	if temp == nil || rule == "" {
		return nil
	}
	isCss := false
	elementsRule := rule
	if hasPrefixFold(rule, "@CSS:") {
		isCss = true
		elementsRule = strings.TrimSpace(rule[5:])
	}
	ra := NewRuleAnalyzer(elementsRule, false)
	ruleStrS := ra.SplitRule("&&", "||", "%%")

	var elementsList [][]*html.Node
	if isCss {
		for _, ruleStr := range ruleStrS {
			tempS := selectCSS(temp, ruleStr)
			elementsList = append(elementsList, tempS)
			if len(tempS) > 0 && ra.ElementsType() == "||" {
				break
			}
		}
	} else {
		for _, ruleStr := range ruleStrS {
			rsRule := NewRuleAnalyzer(ruleStr, false)
			rsRule.Trim()
			rs := rsRule.SplitRule("@")
			var el []*html.Node
			if len(rs) > 1 {
				el = []*html.Node{temp}
				for _, rl := range rs {
					var es []*html.Node
					for _, et := range el {
						es = append(es, a.getElementsOf(et, rl)...)
					}
					el = es
				}
			} else {
				el = newElementsSingle().getElementsSingle(temp, ruleStr)
			}
			elementsList = append(elementsList, el)
			if len(el) > 0 && ra.ElementsType() == "||" {
				break
			}
		}
	}
	var elements []*html.Node
	if len(elementsList) > 0 {
		if ra.ElementsType() == "%%" {
			for i := 0; i < len(elementsList[0]); i++ {
				for _, es := range elementsList {
					if i < len(es) {
						elements = append(elements, es[i])
					}
				}
			}
		} else {
			for _, es := range elementsList {
				elements = append(elements, es...)
			}
		}
	}
	return elements
}

// getResultList 对应 getResultList：按 "@" 步进，最后一段作为提取规则。
func (a *jsoupAnalyzer) getResultList(ruleStr string) []string {
	if ruleStr == "" {
		return nil
	}
	elements := []*html.Node{a.root}
	rule := NewRuleAnalyzer(ruleStr, false)
	rule.Trim()
	rules := rule.SplitRule("@")
	last := len(rules) - 1
	for i := 0; i < last; i++ {
		var es []*html.Node
		for _, elt := range elements {
			es = append(es, newElementsSingle().getElementsSingle(elt, rules[i])...)
		}
		elements = es
	}
	if len(elements) == 0 {
		return nil
	}
	return getResultLast(elements, rules[last])
}

// getResultLast 对应 getResultLast：按最后一个规则提取内容。
func getResultLast(elements []*html.Node, lastRule string) []string {
	var textS []string
	switch lastRule {
	case "text":
		for _, element := range elements {
			if text := nodeText(element); text != "" {
				textS = append(textS, text)
			}
		}
	case "textNodes":
		for _, element := range elements {
			tn := nodeTextNodes(element)
			if len(tn) > 0 {
				textS = append(textS, strings.Join(tn, "\n"))
			}
		}
	case "ownText":
		for _, element := range elements {
			if text := nodeOwnText(element); text != "" {
				textS = append(textS, text)
			}
		}
	case "html":
		for _, element := range elements {
			if h := outerHTMLNoScript(element); h != "" {
				textS = append(textS, h)
			}
		}
	case "all":
		var sb strings.Builder
		for _, element := range elements {
			sb.WriteString(outerHTML(element))
		}
		textS = append(textS, sb.String())
	default:
		for _, element := range elements {
			url := attrValue(element, lastRule)
			if url == "" || containsStr(textS, url) {
				continue
			}
			textS = append(textS, url)
		}
	}
	return textS
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ─── ElementsSingle：索引语法（对应 data class ElementsSingle） ─────────────

type indexRange struct {
	start *int
	end   *int
	step  int
}

type elementsSingle struct {
	split        byte // '.' 选择 / '!' 排除 / ' ' 无索引
	beforeRule   string
	indexDefault []int
	indexes      []any // int 或 indexRange
}

func newElementsSingle() *elementsSingle {
	return &elementsSingle{split: '.'}
}

func (e *elementsSingle) getElementsSingle(temp *html.Node, rule string) []*html.Node {
	e.findIndexSet(rule)

	var elements []*html.Node
	if e.beforeRule == "" {
		elements = childrenElements(temp)
	} else {
		rules := strings.Split(e.beforeRule, ".")
		arg := ""
		if len(rules) > 1 {
			arg = rules[1]
		}
		switch rules[0] {
		case "children":
			elements = childrenElements(temp)
		case "class":
			if arg != "" {
				elements = getElementsByClass(temp, arg)
			}
		case "tag":
			if arg != "" {
				elements = getElementsByTag(temp, arg)
			}
		case "id":
			if arg != "" {
				elements = getElementsById(temp, arg)
			}
		case "text":
			if arg != "" {
				elements = getElementsContainingOwnText(temp, arg)
			}
		default:
			elements = selectCSS(temp, e.beforeRule)
		}
	}

	// 索引集合：slice+set 模拟 Kotlin LinkedHashSet 的插入顺序去重
	indexSet := make([]int, 0, len(elements))
	seen := make(map[int]bool)
	addIndex := func(ix int) {
		if !seen[ix] {
			seen[ix] = true
			indexSet = append(indexSet, ix)
		}
	}

	lenn := len(elements)
	lastIndexes := -1
	if len(e.indexDefault) > 0 {
		lastIndexes = len(e.indexDefault) - 1
	} else if len(e.indexes) > 0 {
		lastIndexes = len(e.indexes) - 1
	}

	if len(e.indexes) == 0 {
		// 旧式索引：逆向遍历还原顺序
		for ix := lastIndexes; ix >= 0; ix-- {
			it := e.indexDefault[ix]
			if it >= 0 && it < lenn {
				addIndex(it)
			} else if it < 0 && lenn >= -it {
				addIndex(it + lenn)
			}
		}
	} else {
		for ix := lastIndexes; ix >= 0; ix-- {
			if rg, ok := e.indexes[ix].(indexRange); ok {
				start := 0
				if rg.start != nil {
					start = *rg.start
				}
				if start < 0 {
					start += lenn
				}
				end := lenn - 1
				if rg.end != nil {
					end = *rg.end
				}
				if end < 0 {
					end += lenn
				}
				if (start < 0 && end < 0) || (start >= lenn && end >= lenn) {
					continue
				}
				if start >= lenn {
					start = lenn - 1
				} else if start < 0 {
					start = 0
				}
				if end >= lenn {
					end = lenn - 1
				} else if end < 0 {
					end = 0
				}
				if start == end || rg.step >= lenn {
					addIndex(start)
					continue
				}
				step := rg.step
				if step > 0 {
					// 正向步长原样使用
				} else if -step < lenn {
					step = step + lenn
				} else {
					step = 1
				}
				if end > start {
					for i := start; i <= end; i += step {
						addIndex(i)
					}
				} else {
					for i := start; i >= end; i -= step {
						addIndex(i)
					}
				}
			} else {
				it := e.indexes[ix].(int)
				if it >= 0 && it < lenn {
					addIndex(it)
				} else if it < 0 && lenn >= -it {
					addIndex(it + lenn)
				}
			}
		}
	}

	if e.split == '!' {
		exclude := make(map[int]bool)
		for _, ix := range indexSet {
			exclude[ix] = true
		}
		var es []*html.Node
		for i, el := range elements {
			if !exclude[i] {
				es = append(es, el)
			}
		}
		elements = es
	} else if e.split == '.' {
		var es []*html.Node
		for _, ix := range indexSet {
			if ix >= 0 && ix < lenn {
				es = append(es, elements[ix])
			}
		}
		elements = es
	}
	return elements
}

// findIndexSet 对应 ElementsSingle.findIndexSet：从右向左解析索引。
// 支持旧式 tag.div.-1:10:2 / tag.div!0:3 与新式 tag.div[!-1, 3:-2:-10, 2]。
func (e *elementsSingle) findIndexSet(rule string) {
	rus := []rune(strings.TrimSpace(rule))
	n := len(rus)
	if n == 0 {
		e.split = ' '
		e.beforeRule = ""
		return
	}
	var curList []*int // 区间临时列表（逆向压入：右端、左端、间隔）
	l := ""            // 暂存数字字符串
	curMinus := false

	head := rus[n-1] == ']'
	length := n
	if head {
		length-- // 跳过尾部 ']'
	}
findLoop:
	for length >= 0 {
		length--
		if length < 0 {
			break
		}
		rl := rus[length]
		if rl == ' ' {
			continue
		}
		if rl >= '0' && rl <= '9' {
			l = string(rl) + l
			continue
		}
		if rl == '-' {
			curMinus = true
			continue
		}
		var curInt *int
		if l != "" {
			v := parseIntSafe(l)
			if curMinus {
				v = -v
			}
			curInt = &v
		}
		if head {
			switch rl {
			case ':':
				curList = append(curList, curInt)
			default:
				if len(curList) == 0 {
					if curInt == nil {
						break findLoop // 是 jsoup 选择器而非索引列表
					}
					e.indexes = append(e.indexes, *curInt)
				} else {
					rg := indexRange{start: curInt, end: curList[len(curList)-1], step: 1}
					if len(curList) == 2 && curList[0] != nil {
						rg.step = *curList[0]
					}
					e.indexes = append(e.indexes, rg)
					curList = curList[:0]
				}
				if rl == '!' {
					e.split = '!'
					for {
						length--
						if length < 0 {
							break
						}
						rl = rus[length]
						if rl != ' ' {
							break
						}
					}
				}
				if rl == '[' {
					if length < 0 {
						length = 0
					}
					e.beforeRule = string(rus[:length])
					return
				}
				if rl != ',' {
					break findLoop
				}
			}
		} else {
			if rl == '!' || rl == '.' || rl == ':' {
				v := 0
				if curInt != nil {
					v = *curInt
				}
				e.indexDefault = append(e.indexDefault, v)
				if rl != ':' {
					e.split = byte(rl)
					e.beforeRule = string(rus[:length])
					return
				}
			} else {
				break findLoop
			}
		}
		l = ""
		curMinus = false
	}
	e.split = ' '
	e.beforeRule = string(rus)
}

func parseIntSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
