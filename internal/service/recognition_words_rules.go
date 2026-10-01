package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func parseRecognitionWordRules(raw string) []recognitionWordRule {
	var out []recognitionWordRule
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		out = append(out, parseRecognitionWordRule(line))
	}
	return out
}

func parseRecognitionWordRule(line string) recognitionWordRule {
	rule := recognitionWordRule{raw: line}
	for _, part := range strings.Split(line, "&&") {
		part = strings.TrimSpace(part)
		switch {
		case strings.Contains(part, "=>"):
			pieces := strings.SplitN(part, "=>", 2)
			rule.replaceFrom = strings.TrimSpace(pieces[0])
			rule.replaceTo = normalizeRecognitionReplacement(strings.TrimSpace(pieces[1]))
		case strings.Contains(part, "<>") && strings.Contains(part, ">>"):
			beforeAfter := strings.SplitN(part, ">>", 2)
			bounds := strings.SplitN(beforeAfter[0], "<>", 2)
			// A malformed rule without "<>" would otherwise index past the
			// slice; word lists are fetched from the network, so stay defensive.
			if len(bounds) < 2 {
				continue
			}
			rule.offsetLeft = strings.TrimSpace(bounds[0])
			rule.offsetRight = strings.TrimSpace(bounds[1])
			rule.offsetExpr = strings.TrimSpace(beforeAfter[1])
		default:
			rule.block = part
		}
	}
	compileRecognitionWordRule(&rule)
	return rule
}

var recognitionReplacementRE = regexp.MustCompile(`\\([0-9]+)`)

func normalizeRecognitionReplacement(value string) string {
	return recognitionReplacementRE.ReplaceAllString(value, "$$$1")
}

// compileRecognitionWordRule pre-compiles every pattern in a rule so the hot
// clean-query path never recompiles regexes per candidate.
func compileRecognitionWordRule(rule *recognitionWordRule) {
	if rule == nil {
		return
	}
	if rule.block != "" {
		if re, err := regexp.Compile(rule.block); err == nil {
			rule.blockRE = re
		}
	}
	if rule.replaceFrom != "" {
		if re, err := regexp.Compile(rule.replaceFrom); err == nil {
			rule.replaceRE = re
		}
	}
	if rule.offsetExpr != "" && (rule.offsetLeft != "" || rule.offsetRight != "") {
		if re, err := compileRecognitionOffsetRE(rule.offsetLeft, rule.offsetRight); err == nil {
			rule.offsetRE = re
		}
	}
}

func compileRecognitionOffsetRE(left, right string) (*regexp.Regexp, error) {
	leftPattern := firstNonEmpty(left, `^`)
	rightPattern := firstNonEmpty(right, `$`)
	return regexp.Compile(`(?i)(` + leftPattern + `)(\d{1,5})(` + rightPattern + `)`)
}

func applyRecognitionWordRules(raw string, rules []recognitionWordRule) string {
	out := strings.TrimSpace(raw)
	for _, rule := range rules {
		if rule.block != "" {
			out = applyRecognitionBlock(out, rule)
		}
		if rule.replaceFrom != "" {
			out = applyRecognitionReplace(out, rule)
		}
		if rule.offsetLeft != "" || rule.offsetRight != "" {
			out = applyRecognitionOffset(out, rule)
		}
	}
	return strings.Join(strings.Fields(out), " ")
}

func applyRecognitionBlock(raw string, rule recognitionWordRule) string {
	if rule.blockRE != nil {
		return rule.blockRE.ReplaceAllString(raw, " ")
	}
	return strings.ReplaceAll(raw, rule.block, " ")
}

func applyRecognitionReplace(raw string, rule recognitionWordRule) string {
	if rule.replaceRE != nil {
		return rule.replaceRE.ReplaceAllString(raw, rule.replaceTo)
	}
	return strings.ReplaceAll(raw, rule.replaceFrom, rule.replaceTo)
}

func applyRecognitionOffset(raw string, rule recognitionWordRule) string {
	if strings.TrimSpace(rule.offsetExpr) == "" {
		return raw
	}
	re := rule.offsetRE
	if re == nil {
		compiled, err := compileRecognitionOffsetRE(rule.offsetLeft, rule.offsetRight)
		if err != nil {
			return raw
		}
		re = compiled
	}
	return re.ReplaceAllStringFunc(raw, func(match string) string {
		return applyRecognitionOffsetMatch(re, match, rule.offsetExpr)
	})
}

func applyRecognitionOffsetMatch(re *regexp.Regexp, match, expr string) string {
	parts := re.FindStringSubmatch(match)
	if len(parts) < 4 {
		return match
	}
	ep, err := strconv.Atoi(parts[2])
	if err != nil {
		return match
	}
	next, ok := evalRecognitionEpisodeExpr(expr, ep)
	if !ok || next < 0 {
		return match
	}
	format := "%d"
	if width := len(parts[2]); width > 1 && width <= 2 {
		format = "%0" + strconv.Itoa(width) + "d"
	}
	return parts[1] + fmt.Sprintf(format, next) + parts[3]
}

func evalRecognitionEpisodeExpr(expr string, ep int) (int, bool) {
	value := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(expr), " ", ""))
	if value == "" || value == "EP" {
		return ep, true
	}
	if n, err := strconv.Atoi(value); err == nil {
		return n, true
	}
	if out, ok := evalRecognitionEpisodeAddSub(value, ep); ok {
		return out, true
	}
	return evalRecognitionEpisodeMul(value, ep)
}

func evalRecognitionEpisodeAddSub(value string, ep int) (int, bool) {
	for _, op := range []string{"+", "-"} {
		if !strings.HasPrefix(value, "EP"+op) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(value, "EP"+op))
		if err != nil {
			return 0, false
		}
		if op == "+" {
			return ep + n, true
		}
		return ep - n, true
	}
	return 0, false
}

func evalRecognitionEpisodeMul(value string, ep int) (int, bool) {
	if strings.HasPrefix(value, "EP*") {
		n, err := strconv.Atoi(strings.TrimPrefix(value, "EP*"))
		return ep * n, err == nil
	}
	if strings.HasSuffix(value, "*EP") {
		n, err := strconv.Atoi(strings.TrimSuffix(value, "*EP"))
		return ep * n, err == nil
	}
	return 0, false
}
