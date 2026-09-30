package rule

import (
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

// 本文件对应 AnalyzeByRegex.kt。Java 正则语义用 regexp2 对齐
// （支持前向后向断言与反向引用），匹配循环对齐 Matcher.find()。

// splitNotBlankAndTrim 对应 String.splitNotBlank("&&")：切分并去空白项。
func splitNotBlankAndTrim(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// regexGetElement 对应 AnalyzeByRegex.getElement：多段正则串联，
// 最终返回第一个匹配的全部分组（含 group 0）。
func regexGetElement(res string, regs []string, index int) []string {
	if index >= len(regs) {
		return nil
	}
	re, err := regexp2.Compile(regs[index], regexp2.None)
	if err != nil {
		return nil
	}
	m, err := re.FindStringMatchStartingAt(res, 0)
	if err != nil || m == nil {
		return nil
	}
	if index+1 == len(regs) {
		info := make([]string, 0, len(m.Groups()))
		for _, g := range m.Groups() {
			if len(g.Captures) > 0 {
				info = append(info, g.Captures[0].String())
			} else {
				info = append(info, "")
			}
		}
		return info
	}
	var sb strings.Builder
	for m != nil {
		sb.WriteString(m.String())
		m, _ = re.FindNextMatch(m)
	}
	return regexGetElement(sb.String(), regs, index+1)
}

// regexGetElements 对应 AnalyzeByRegex.getElements：多段正则串联，
// 最终按每个匹配返回一组分组列表。
func regexGetElements(res string, regs []string, index int) [][]string {
	if index >= len(regs) {
		return nil
	}
	re, err := regexp2.Compile(regs[index], regexp2.None)
	if err != nil {
		return nil
	}
	m, err := re.FindStringMatchStartingAt(res, 0)
	if err != nil || m == nil {
		return nil
	}
	if index+1 == len(regs) {
		var books [][]string
		for m != nil {
			info := make([]string, 0, len(m.Groups()))
			for _, g := range m.Groups() {
				if len(g.Captures) > 0 {
					info = append(info, g.Captures[0].String())
				} else {
					info = append(info, "")
				}
			}
			books = append(books, info)
			m, _ = re.FindNextMatch(m)
		}
		return books
	}
	var sb strings.Builder
	for m != nil {
		sb.WriteString(m.String())
		m, _ = re.FindNextMatch(m)
	}
	return regexGetElements(sb.String(), regs, index+1)
}

// regexReplaceAll 对应 Kotlin Regex.replace(result, replacement)
// （Java $N 分组替换语义，regexp2 的 Replace 原生支持）。
func regexReplaceAll(pattern, result, replacement string) string {
	re, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return strings.ReplaceAll(result, pattern, replacement)
	}
	out, err := re.Replace(result, replacement, 0, -1)
	if err != nil {
		return result
	}
	return out
}

// ApplyUserReplace 应用一条用户替换净化规则（对应 legado ReplaceRule）。
// isRegex=false 按字面替换；isRegex=true 用 Java 正则语义并带匹配超时
// （防灾难性回溯挂死服务），编译失败回退字面替换。
func ApplyUserReplace(content, pattern, replacement string, isRegex bool, timeoutMS int64) string {
	if pattern == "" {
		return content
	}
	if !isRegex {
		return strings.ReplaceAll(content, pattern, replacement)
	}
	re, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return strings.ReplaceAll(content, pattern, replacement)
	}
	if timeoutMS > 0 {
		re.MatchTimeout = time.Duration(timeoutMS) * time.Millisecond
	} else {
		re.MatchTimeout = 3 * time.Second
	}
	out, err := re.Replace(content, replacement, 0, -1)
	if err != nil {
		return content
	}
	return out
}

// ApplyReplaceRegexString 应用书源 replaceRegex 字符串（"##pattern##replace[##x]" 格式），
// 对应 ContentRule.replaceRegex 的处理。
func ApplyReplaceRegexString(content, replaceRegex string) string {
	if replaceRegex == "" {
		return content
	}
	segs := strings.Split(replaceRegex, "##")
	if len(segs) < 2 {
		return content
	}
	pattern := segs[1]
	replacement := ""
	replaceFirst := false
	if len(segs) > 2 {
		replacement = segs[2]
	}
	if len(segs) > 3 {
		replaceFirst = true
	}
	if replaceFirst {
		return regexReplaceFirstOnFirstMatch(pattern, content, replacement)
	}
	return regexReplaceAll(pattern, content, replacement)
}

// regexReplaceFirstOnFirstMatch 对应 replaceRegex 的 replaceFirst 分支：
// 找到第一个匹配（无匹配返回 ""），在匹配文本上做首次替换。
func regexReplaceFirstOnFirstMatch(pattern, result, replacement string) string {
	re, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return replacement
	}
	m, err := re.FindStringMatch(result)
	if err != nil || m == nil {
		return ""
	}
	out, err := re.Replace(m.String(), replacement, 0, 1)
	if err != nil {
		return m.String()
	}
	return out
}
