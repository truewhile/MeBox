package rule

import (
	"regexp"
	"strconv"
)

// 中文数字 → 阿拉伯数字，用于 java.toNumChapter。
//
// 对应 legado 的 `AppPattern.titleNumPattern` + `StringUtils.stringToInt`：把
// 「第一百零八章」规整成「第108章」。书源用它统一章节标题（排序/去重）。
// 解析不出来时原样返回，不做猜测。
var chapterTokenRe = regexp.MustCompile(`[0-9]+|[〇零一二三四五六七八九十百千万两兩]+`)

var (
	zhDigits   = map[rune]int{'〇': 0, '零': 0, '一': 1, '二': 2, '两': 2, '兩': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	zhUnits    = map[rune]int{'十': 10, '百': 100, '千': 1000}
	zhBigUnits = map[rune]int{'万': 10000, '億': 100000000, '亿': 100000000}
)

// numChapter 把标题里第一处数字规整成阿拉伯数字。
func numChapter(s string) string {
	loc := chapterTokenRe.FindStringIndex(s)
	if loc == nil {
		return s
	}
	n, ok := parseCNSNum(s[loc[0]:loc[1]])
	if !ok {
		return s
	}
	return s[:loc[0]] + strconv.Itoa(n) + s[loc[1]:]
}

// parseCNSNum 解析纯阿拉伯数字或中文数字；无法解析返回 false。
func parseCNSNum(tok string) (int, bool) {
	if tok == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(tok); err == nil {
		return n, true
	}
	// 全是数字字符（如「一〇二」这种按位念法）→ 逐位拼起来
	if digitsOnly(tok) {
		n := 0
		for _, r := range tok {
			n = n*10 + zhDigits[r]
		}
		return n, true
	}
	total, section, num := 0, 0, 0
	seen := false
	for _, r := range tok {
		if d, ok := zhDigits[r]; ok {
			num = d
			seen = true
			continue
		}
		if u, ok := zhUnits[r]; ok {
			if num == 0 {
				num = 1 // 「十二」这类省略了前导一的写法
			}
			section += num * u
			num = 0
			seen = true
			continue
		}
		if big, ok := zhBigUnits[r]; ok {
			section += num
			if section == 0 {
				section = 1
			}
			total += section * big
			section, num = 0, 0
			seen = true
			continue
		}
		return 0, false
	}
	if !seen {
		return 0, false
	}
	return total + section + num, true
}

// digitsOnly 判断字符串是否只由中文数字字符组成。
func digitsOnly(s string) bool {
	for _, r := range s {
		if _, ok := zhDigits[r]; !ok {
			return false
		}
	}
	return true
}
