package rule

import "strings"

// RuleAnalyzer 移植自 legado RuleAnalyzer.kt，逐方法对齐。
// 用于按 "@"/"&&"/"||"/"%%"/"@" 切分规则字符串，切分时跳过引号内与
// 平衡组（[...]、(...)）内的分隔符，避免与选择器或正则内容冲突。

// RuleAnalyzer 是无状态的切分器实例（一次使用）。
type RuleAnalyzer struct {
	queue        string
	pos          int
	start        int
	startX       int
	rule         []string
	step         int
	elementsType string
	code         bool
}

// NewRuleAnalyzer 对应 RuleAnalyzer(data, code)；code=true 时平衡组按
// 代码语义（处理转义、区分 [] 与 ()）处理，用于 jsonPath 规则。
func NewRuleAnalyzer(data string, code bool) *RuleAnalyzer {
	return &RuleAnalyzer{queue: data, code: code}
}

// ElementsType 返回上次 splitRule 使用的组合符（"&&"/"||"/"%%"）。
func (a *RuleAnalyzer) ElementsType() string { return a.elementsType }

// Rules 返回切分结果。
func (a *RuleAnalyzer) Rules() []string { return a.rule }

// Trim 对应 trim()：修剪当前规则之前的 "@" 或不可见字符。
func (a *RuleAnalyzer) Trim() {
	if a.pos >= len(a.queue) {
		return
	}
	if a.queue[a.pos] == '@' || a.queue[a.pos] < '!' {
		a.pos++
		for a.pos < len(a.queue) && (a.queue[a.pos] == '@' || a.queue[a.pos] < '!') {
			a.pos++
		}
		a.start = a.pos
		a.startX = a.pos
	}
}

// ReSetPos 对应 reSetPos()。
func (a *RuleAnalyzer) ReSetPos() {
	a.pos = 0
	a.startX = 0
}

// consumeTo 对应 consumeTo(seq)。
func (a *RuleAnalyzer) consumeTo(seq string) bool {
	a.start = a.pos
	offset := strings.Index(a.queue[a.pos:], seq)
	if offset == -1 {
		return false
	}
	a.pos += offset
	return true
}

// consumeToAny 对应 consumeToAny(seq)。
func (a *RuleAnalyzer) consumeToAny(seqs ...string) bool {
	pos := a.pos
	for pos != len(a.queue) {
		for _, s := range seqs {
			if strings.HasPrefix(a.queue[pos:], s) {
				a.step = len(s)
				a.pos = pos
				return true
			}
		}
		pos++
	}
	return false
}

// findToAny 对应 findToAny(seq: Char)。
func (a *RuleAnalyzer) findToAny(chars ...byte) int {
	pos := a.pos
	for pos != len(a.queue) {
		for _, c := range chars {
			if a.queue[pos] == c {
				return pos
			}
		}
		pos++
	}
	return -1
}

// chompCodeBalanced 对应 chompCodeBalanced(open, close)：拉出一个平衡组，
// 存在转义文本，'[' 与参数对 (open/close) 分别计数。
func (a *RuleAnalyzer) chompCodeBalanced(open, close byte) bool {
	pos := a.pos
	depth := 0
	otherDepth := 0
	inSingle := false
	inDouble := false
	for {
		if pos == len(a.queue) {
			break
		}
		c := a.queue[pos]
		pos++
		if c != '\\' {
			if c == '\'' && !inDouble {
				inSingle = !inSingle
			} else if c == '"' && !inSingle {
				inDouble = !inDouble
			}
			if inSingle || inDouble {
				continue
			}
			if c == '[' {
				depth++
			} else if c == ']' {
				depth--
			} else if depth == 0 {
				if c == open {
					otherDepth++
				} else if c == close {
					otherDepth--
				}
			}
		} else {
			pos++
		}
		if !(depth > 0 || otherDepth > 0) {
			break
		}
	}
	if depth > 0 || otherDepth > 0 {
		return false
	}
	a.pos = pos
	return true
}

// chompRuleBalanced 对应 chompRuleBalanced(open, close)：引号外才处理转义。
func (a *RuleAnalyzer) chompRuleBalanced(open, close byte) bool {
	pos := a.pos
	depth := 0
	inSingle := false
	inDouble := false
	for {
		if pos == len(a.queue) {
			break
		}
		c := a.queue[pos]
		pos++
		if c == '\'' && !inDouble {
			inSingle = !inSingle
		} else if c == '"' && !inSingle {
			inDouble = !inDouble
		}
		if inSingle || inDouble {
			continue
		}
		if c == '\\' {
			pos++
			continue
		}
		if c == open {
			depth++
		} else if c == close {
			depth--
		}
		if !(depth > 0) {
			break
		}
	}
	if depth > 0 {
		return false
	}
	a.pos = pos
	return true
}

func (a *RuleAnalyzer) chompBalanced(open, close byte) bool {
	if a.code {
		return a.chompCodeBalanced(open, close)
	}
	return a.chompRuleBalanced(open, close)
}

// SplitRule 对应 splitRule(vararg split)：把 queue 切分成规则列表。
// 首段按 splits 中最先出现的分隔符确定组合类型，其余按该类型循环切分。
func (a *RuleAnalyzer) SplitRule(splits ...string) []string {
	if len(splits) == 1 {
		a.elementsType = splits[0]
		if !a.consumeTo(a.elementsType) {
			a.rule = append(a.rule, a.queue[a.startX:])
			return a.rule
		}
		a.step = len(a.elementsType)
		return a.splitRuleNext()
	}
	if !a.consumeToAny(splits...) {
		a.rule = append(a.rule, a.queue[a.startX:])
		return a.rule
	}

	end := a.pos
	a.pos = a.start
	for {
		st := a.findToAny('[', '(')
		if st == -1 {
			a.rule = []string{a.queue[a.startX:end]}
			a.elementsType = a.queue[end : end+a.step]
			a.pos = end + a.step
			for a.consumeTo(a.elementsType) {
				a.rule = append(a.rule, a.queue[a.start:a.pos])
				a.pos += a.step
			}
			a.rule = append(a.rule, a.queue[a.pos:])
			return a.rule
		}
		if st > end {
			a.rule = []string{a.queue[a.startX:end]}
			a.elementsType = a.queue[end : end+a.step]
			a.pos = end + a.step
			for a.consumeTo(a.elementsType) && a.pos < st {
				a.rule = append(a.rule, a.queue[a.start:a.pos])
				a.pos += a.step
			}
			if a.pos > st {
				a.startX = a.start
				return a.splitRuleNext()
			}
			a.rule = append(a.rule, a.queue[a.pos:])
			return a.rule
		}
		a.pos = st
		next := byte(')')
		if a.queue[a.pos] == '[' {
			next = ']'
		}
		if !a.chompBalanced(a.queue[a.pos], next) {
			return []string{a.queue}
		}
		if end <= a.pos {
			break
		}
	}
	a.start = a.pos
	return a.splitRuleFirst(splits...)
}

// splitRuleFirst 对应首段匹配的 tailrec 递归（elementsType 尚未确定）。
func (a *RuleAnalyzer) splitRuleFirst(splits ...string) []string {
	if len(splits) == 1 {
		a.elementsType = splits[0]
		if !a.consumeTo(a.elementsType) {
			a.rule = append(a.rule, a.queue[a.startX:])
			return a.rule
		}
		a.step = len(a.elementsType)
		return a.splitRuleNext()
	}
	if !a.consumeToAny(splits...) {
		a.rule = append(a.rule, a.queue[a.startX:])
		return a.rule
	}
	end := a.pos
	a.pos = a.start
	for {
		st := a.findToAny('[', '(')
		if st == -1 {
			a.rule = []string{a.queue[a.startX:end]}
			a.elementsType = a.queue[end : end+a.step]
			a.pos = end + a.step
			for a.consumeTo(a.elementsType) {
				a.rule = append(a.rule, a.queue[a.start:a.pos])
				a.pos += a.step
			}
			a.rule = append(a.rule, a.queue[a.pos:])
			return a.rule
		}
		if st > end {
			a.rule = []string{a.queue[a.startX:end]}
			a.elementsType = a.queue[end : end+a.step]
			a.pos = end + a.step
			for a.consumeTo(a.elementsType) && a.pos < st {
				a.rule = append(a.rule, a.queue[a.start:a.pos])
				a.pos += a.step
			}
			if a.pos > st {
				a.startX = a.start
				return a.splitRuleNext()
			}
			a.rule = append(a.rule, a.queue[a.pos:])
			return a.rule
		}
		a.pos = st
		next := byte(')')
		if a.queue[a.pos] == '[' {
			next = ']'
		}
		if !a.chompBalanced(a.queue[a.pos], next) {
			return []string{a.queue}
		}
		if end <= a.pos {
			break
		}
	}
	a.start = a.pos
	return a.splitRuleFirst(splits...)
}

// splitRuleNext 对应二段匹配 splitRule()（elementsType 已确定）。
func (a *RuleAnalyzer) splitRuleNext() []string {
	for {
		end := a.pos
		a.pos = a.start
		for {
			st := a.findToAny('[', '(')
			if st == -1 {
				a.rule = append(a.rule, a.queue[a.startX:end])
				a.pos = end + a.step
				for a.consumeTo(a.elementsType) {
					a.rule = append(a.rule, a.queue[a.start:a.pos])
					a.pos += a.step
				}
				a.rule = append(a.rule, a.queue[a.pos:])
				return a.rule
			}
			if st > end {
				a.rule = append(a.rule, a.queue[a.startX:end])
				a.pos = end + a.step
				for a.consumeTo(a.elementsType) && a.pos < st {
					a.rule = append(a.rule, a.queue[a.start:a.pos])
					a.pos += a.step
				}
				if a.pos > st {
					a.startX = a.start
					return a.splitRuleNext()
				}
				a.rule = append(a.rule, a.queue[a.pos:])
				return a.rule
			}
			a.pos = st
			next := byte(')')
			if a.queue[a.pos] == '[' {
				next = ']'
			}
			if !a.chompBalanced(a.queue[a.pos], next) {
				return []string{a.queue}
			}
			if end <= a.pos {
				break
			}
		}
		a.start = a.pos
		if !a.consumeTo(a.elementsType) {
			a.rule = append(a.rule, a.queue[a.startX:])
			return a.rule
		}
	}
}

// InnerRule 对应 innerRule(inner, startStep, endStep, fr) 第一变体：
// 替换所有内嵌规则（如 {$.xxx}），fr 返回空表示该处不是有效内嵌规则。
// 返回替换后的完整字符串；无一替换成功时返回 ""。
func (a *RuleAnalyzer) InnerRule(inner string, startStep, endStep int, fr func(string) string) string {
	var sb []byte
	for {
		if !a.consumeTo(inner) {
			break
		}
		posPre := a.pos
		if a.chompCodeBalanced('{', '}') {
			frv := fr(a.queue[posPre+startStep : a.pos-endStep])
			if frv != "" {
				sb = append(sb, a.queue[a.startX:posPre]...)
				sb = append(sb, frv...)
				a.startX = a.pos
				continue
			}
		}
		a.pos += len(inner)
	}
	if a.startX == 0 {
		return ""
	}
	sb = append(sb, a.queue[a.startX:]...)
	return string(sb)
}

// InnerRule2 对应 innerRule(startStr, endStr, fr) 第二变体：无平衡组检查。
// 无一替换成功时返回原串。
func (a *RuleAnalyzer) InnerRule2(startStr, endStr string, fr func(string) string) string {
	var sb []byte
	for {
		if !a.consumeTo(startStr) {
			break
		}
		a.pos += len(startStr)
		posPre := a.pos
		if a.consumeTo(endStr) {
			frv := fr(a.queue[posPre:a.pos])
			sb = append(sb, a.queue[a.startX:posPre-len(startStr)]...)
			sb = append(sb, frv...)
			a.pos += len(endStr)
			a.startX = a.pos
		}
	}
	if a.startX == 0 {
		return a.queue
	}
	sb = append(sb, a.queue[a.startX:]...)
	return string(sb)
}
