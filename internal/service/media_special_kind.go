package service

import (
	"regexp"
	"strings"
)

const (
	mediaSpecialTheatrical = "theatrical"
	mediaSpecialOVA        = "ova"
	mediaSpecialOAD        = "oad"
	mediaSpecialOVD        = "ovd"
	mediaSpecialONA        = "ona"
	mediaSpecialExtra      = "extra"
	mediaSpecialBonus      = "bonus"
	mediaSpecialOmake      = "omake"
	mediaSpecialPicture    = "picture_drama"
	mediaSpecialNCOP       = "ncop"
	mediaSpecialNCED       = "nced"
	mediaSpecialGeneric    = "special"
)

var mediaSpecialKindPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{mediaSpecialOVA, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ova(?:s)?(?:[\s._-]*\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialOAD, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])oad(?:s)?(?:[\s._-]*\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialOVD, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ovd(?:s)?(?:[\s._-]*\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialONA, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ona(?:s)?(?:[\s._-]*\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialPicture, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:picture[\s._-]*drama|画像特典)(?:[^a-z0-9]|$)`)},
	{mediaSpecialNCOP, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ncop(?:\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialNCED, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])nced(?:\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialExtra, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])extras?(?:[\s._-]*\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialBonus, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])bonus(?:es)?(?:[\s._-]*\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialOmake, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])omake(?:[\s._-]*\d+)?(?:[^a-z0-9]|$)`)},
	{mediaSpecialGeneric, regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:special(?:[\s._-]*episodes?)?|specials|sps?)(?:[\s._-]*\d+)?(?:[^a-z0-9]|$)|特别篇|特別篇|番外篇?|特典|外传|外傳|总集篇|總集篇`)},
}

func mediaSpecialKind(path string) string {
	if pathHasTheatricalFolder(path) || patTheatricalTitle.MatchString(path) {
		return mediaSpecialTheatrical
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		for _, pattern := range mediaSpecialKindPatterns {
			if pattern.re.MatchString(part) {
				return pattern.kind
			}
		}
	}
	return ""
}
