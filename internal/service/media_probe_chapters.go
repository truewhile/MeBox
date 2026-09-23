package service

import (
	"regexp"
	"strings"

	"github.com/truewhile/MeBox/internal/model"
)

// chapterMinMs 是章节被判为「可跳过区间」的最短长度。太短的章节没有可跳过的
// 内容，多半是章节切分噪声。
const chapterMinMs = 2000

// chapterKindPatterns 是章节标题 → 片段类型的映射表。
//
// 只认「有语义的」标题。大量发布组的章节叫 Chapter 01/02…，那种一律不猜：
// auto 档会优先采用章节数据，猜错会让跳过按钮指到错误的位置，比拿不到数据更糟。
// 匹配不到就返回空串，让 auto 回落到 TheIntroDB。
//
// 西文关键词要求词边界（前后不能是字母），否则 "Options" 会被当成片头（op）、
// "Weekend" 会被当成片尾（end）。中日文关键词按子串匹配（CJK 不是 [a-z]，所以
// 同一条边界规则对它们天然成立）。
var chapterKindPatterns = []struct {
	kind    string
	pattern *regexp.Regexp
}{
	{model.SegmentKindIntro, regexp.MustCompile(`(?i)(^|[^a-z])(intro|opening|op|ncop)([^a-z]|$)|片头|オープニング`)},

	{model.SegmentKindRecap, regexp.MustCompile(`(?i)(^|[^a-z])(recap|previously)([^a-z]|$)|前情|回顾|あらすじ`)},

	{model.SegmentKindCredits, regexp.MustCompile(`(?i)(^|[^a-z])(credits|ending|end|outro|nced|ed)([^a-z]|$)|片尾|エンディング`)},

	{model.SegmentKindPreview, regexp.MustCompile(`(?i)(^|[^a-z])(preview|next\s+episode|next\s+time)([^a-z]|$)|预告|次回|予告`)},
}

// chapterTitleKind 返回章节标题对应的片段类型；识别不出来时返回空串。
func chapterTitleKind(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	for _, entry := range chapterKindPatterns {
		if entry.pattern.MatchString(title) {
			return entry.kind
		}
	}
	return ""
}

// chaptersToSegments 把内嵌章节映射成可跳过的区间。
//
// 只有标题能识别出类型时才产出区间；EndMs 为 0 的章节落成 0（= 延续到片尾），
// 与提供方契约一致，由客户端按媒体总时长补齐。
func chaptersToSegments(chapters []ProbeChapter) []model.MediaSegment {
	rows := make([]model.MediaSegment, 0, 4)
	for _, chapter := range chapters {
		kind := chapterTitleKind(chapter.Title)
		if kind == "" || chapter.StartMs < 0 {
			continue
		}
		if chapter.EndMs > 0 && chapter.EndMs-chapter.StartMs < chapterMinMs {
			continue
		}
		rows = append(rows, model.MediaSegment{
			Kind:    kind,
			StartMs: chapter.StartMs,
			EndMs:   chapter.EndMs,
			Source:  SegmentSourceFFprobe,
		})
	}
	return rows
}
