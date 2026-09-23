package service

import (
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 章节标题只有「有语义」时才认。西文关键词要词边界，否则 Options / Weekend
// 这类标题会被误判成片头/片尾；中文日文按子串匹配。
func TestChapterTitleKind(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Intro", model.SegmentKindIntro},
		{"Opening", model.SegmentKindIntro},
		{"Opening Title", model.SegmentKindIntro},
		{"OP", model.SegmentKindIntro},
		{"NCOP", model.SegmentKindIntro},
		{"片头", model.SegmentKindIntro},
		{"オープニング", model.SegmentKindIntro},

		{"Recap", model.SegmentKindRecap},
		{"Previously on", model.SegmentKindRecap},
		{"前情提要", model.SegmentKindRecap},
		{"回顾", model.SegmentKindRecap},
		{"あらすじ", model.SegmentKindRecap},

		{"End Credits", model.SegmentKindCredits},
		{"Credits", model.SegmentKindCredits},
		{"Ending", model.SegmentKindCredits},
		{"ED", model.SegmentKindCredits},
		{"Outro", model.SegmentKindCredits},
		{"片尾", model.SegmentKindCredits},
		{"エンディング", model.SegmentKindCredits},

		{"Next Episode", model.SegmentKindPreview},
		{"Next time", model.SegmentKindPreview},
		{"Preview", model.SegmentKindPreview},
		{"预告", model.SegmentKindPreview},
		{"次回予告", model.SegmentKindPreview},

		// 无语义或容易误判的标题一律不认：猜错比没有数据更糟。
		{"", ""},
		{"   ", ""},
		{"Chapter 01", ""},
		{"Chapter 12", ""},
		{"Options", ""},
		{"Weekend", ""},
		{"Main Menu", ""},
		{"Menu", ""},
	}
	for _, tc := range cases {
		if got := chapterTitleKind(tc.title); got != tc.want {
			t.Errorf("chapterTitleKind(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestChaptersToSegmentsOnlyKeepsSemanticChapters(t *testing.T) {
	rows := chaptersToSegments([]ProbeChapter{
		{Index: 0, StartMs: 0, EndMs: 95_000, Title: "Chapter 01"},
		{Index: 1, StartMs: 228_664, EndMs: 246_143, Title: "Opening"},
		{Index: 2, StartMs: 246_500, EndMs: 247_400, Title: "Recap"}, // 不足 chapterMinMs
		{Index: 3, StartMs: 3_431_000, EndMs: 0, Title: "End Credits"},
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want 2 (Opening + End Credits)", rows)
	}
	if rows[0].Kind != model.SegmentKindIntro || rows[0].StartMs != 228_664 || rows[0].EndMs != 246_143 {
		t.Fatalf("first row = %#v", rows[0])
	}
	if rows[1].Kind != model.SegmentKindCredits || rows[1].StartMs != 3_431_000 || rows[1].EndMs != 0 {
		t.Fatalf("second row = %#v", rows[1])
	}
	for _, row := range rows {
		if row.Source != SegmentSourceFFprobe {
			t.Fatalf("source = %q, want %q", row.Source, SegmentSourceFFprobe)
		}
	}
}

func TestChaptersToSegmentsIgnoresUntitledAndNegativeChapters(t *testing.T) {
	rows := chaptersToSegments([]ProbeChapter{
		{Index: 0, StartMs: 0, EndMs: 60_000, Title: ""},
		{Index: 1, StartMs: -1, EndMs: 60_000, Title: "Intro"},
	})
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want none", rows)
	}
}

func TestNormalizeSegmentSourceFallsBackToAuto(t *testing.T) {
	cases := map[string]string{
		"":           SegmentSourceAuto,
		"  ":         SegmentSourceAuto,
		"auto":       SegmentSourceAuto,
		"AUTO":       SegmentSourceAuto,
		"ffprobe":    SegmentSourceFFprobe,
		" FFprobe  ": SegmentSourceFFprobe,
		"theintrodb": SegmentSourceTheIntroDB,
		"TheIntroDB": SegmentSourceTheIntroDB,
		"aniskip":    SegmentSourceAuto, // 未知值按 auto 处理
	}
	for input, want := range cases {
		if got := NormalizeSegmentSource(input); got != want {
			t.Errorf("NormalizeSegmentSource(%q) = %q, want %q", input, got, want)
		}
	}
}
