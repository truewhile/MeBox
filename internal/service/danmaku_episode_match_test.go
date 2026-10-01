package service

import (
	"testing"
)

func TestDanmakuEpisodeNumber(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"第1话 裏切りの大空", "1"},
		{"第18话 自己相似的两性同体-Fractal Androgynous-", "18"},
		{"第11话", "11"},
		{"【dandan&animeko】 第29集 那我们走吧", "29"},
		{"第 202 集", "202"},
		{"第1.5话 特别篇", "1.5"},
		{"正片", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := danmakuEpisodeNumber(tc.in); got != tc.want {
			t.Errorf("danmakuEpisodeNumber(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDanmakuEpisodeSubtitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"第1话 裏切りの大空", "裏切りの大空"},
		{"第18话 自己相似的两性同体-Fractal Androgynous-", "自己相似的两性同体-Fractal Androgynous-"},
		{"第11话", ""},
		{"【dandan&animeko】 第29集 那我们走吧", "那我们走吧"},
		{"正片", ""},
	}
	for _, tc := range cases {
		if got := danmakuEpisodeSubtitle(tc.in); got != tc.want {
			t.Errorf("danmakuEpisodeSubtitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 刮削信息匹配：优先使用本地已刮削的年份、集数和集标题，不能再被同名续作
// 或缺少集标题的平台源抢走。
func TestMatchScrapedDanmakuEpisodesUsesYearAndEpisodeTitle(t *testing.T) {
	candidates := []DanmakuAnime{
		{AnimeID: 1, AnimeTitle: "命运石之门 0(2018)【TV动画】from dandan&animeko", Episodes: []DanmakuEpisode{
			{EpisodeID: 10299, EpisodeTitle: "【dandan&animeko】 第1话 零化域的缺失之环-Absolute Zero-"},
		}},
		{AnimeID: 2, AnimeTitle: "命运石之门(2011)【TV动画】from dandan&animeko", Episodes: []DanmakuEpisode{
			{EpisodeID: 10322, EpisodeTitle: "【dandan&animeko】 第1话 始与终的序章-Turning Point-"},
		}},
		{AnimeID: 3, AnimeTitle: "命运石之门(2011)【动漫】from 360", Episodes: []DanmakuEpisode{
			{EpisodeID: 10218, EpisodeTitle: "【qq】 第1集"},
		}},
	}

	got := matchScrapedDanmakuEpisodes(candidates, "命运石之门", 2011, "1", "起始与终结的序章")
	if len(got) != 1 {
		t.Fatalf("matched %d anime, want 1: %#v", len(got), got)
	}
	if got[0].AnimeID != 2 || len(got[0].Episodes) != 1 || got[0].Episodes[0].EpisodeID != 10322 {
		t.Fatalf("picked wrong source: %#v", got)
	}
}

// 真实数据形态：官方 match 给出的剧名+集数在配置源里会命中多季/多版本，
// 且顺序不可靠。必须靠副标题选中正确的那一集。
func TestPickDanmakuEpisodeIDDisambiguatesBySubtitle(t *testing.T) {
	// 实测样本：官方 match「命运石之门 第18话 自己相似的两性同体-…」，
	// 配置源首条却是《命运石之门 0》的同一集号。
	candidates := []DanmakuAnime{
		{AnimeID: 1, AnimeTitle: "命运石之门 0(2018)", Episodes: []DanmakuEpisode{
			{EpisodeID: 11072, EpisodeTitle: "【dandan&animeko】 第18话 并进对称的牵牛星-Translational"},
		}},
		{AnimeID: 2, AnimeTitle: "命运石之门(2011)", Episodes: []DanmakuEpisode{
			{EpisodeID: 11095, EpisodeTitle: "【dandan&animeko】 第18话 自己相似的两性同体-Fractal Androgynous-"},
		}},
	}
	got, ok := pickDanmakuEpisodeID(candidates, "18", "第18话 自己相似的两性同体-Fractal Androgynous-")
	if !ok {
		t.Fatal("expected a confident match")
	}
	if got != 11095 {
		t.Fatalf("picked episodeId %d, want 11095 (first candidate is a different season)", got)
	}
}

// 目标带副标题但没有任何候选的副标题对得上时，必须放弃而不是退回第一条，
// 否则会给用户播放另一部番的弹幕。
func TestPickDanmakuEpisodeIDRejectsWhenSubtitleNotFound(t *testing.T) {
	candidates := []DanmakuAnime{
		{AnimeID: 1, AnimeTitle: "某番 第一季", Episodes: []DanmakuEpisode{
			{EpisodeID: 111, EpisodeTitle: "第3话 完全不同的标题"},
		}},
	}
	if got, ok := pickDanmakuEpisodeID(candidates, "3", "第3话 期望的标题"); ok {
		t.Fatalf("expected no match, got episodeId %d", got)
	}
}

// 目标没有副标题（如「第11话」）时，退而要求集数一致；集数对不上同样放弃。
func TestPickDanmakuEpisodeIDNumberOnlyFallback(t *testing.T) {
	candidates := []DanmakuAnime{
		{AnimeID: 1, AnimeTitle: "86 第二季", Episodes: []DanmakuEpisode{
			// 该源对第二季采用绝对集号，第 11 集记作第 22 话。
			{EpisodeID: 11113, EpisodeTitle: "第22话 辛"},
		}},
	}
	if got, ok := pickDanmakuEpisodeID(candidates, "11", "第11话"); ok {
		t.Fatalf("episode number mismatch must be rejected, got episodeId %d", got)
	}

	same := []DanmakuAnime{
		{AnimeID: 1, AnimeTitle: "某番", Episodes: []DanmakuEpisode{
			{EpisodeID: 222, EpisodeTitle: "第11话"},
		}},
	}
	got, ok := pickDanmakuEpisodeID(same, "11", "第11话")
	if !ok || got != 222 {
		t.Fatalf("pickDanmakuEpisodeID = (%d,%v), want (222,true)", got, ok)
	}
}

func TestPickDanmakuEpisodeIDSkipsInvalidIDs(t *testing.T) {
	candidates := []DanmakuAnime{
		{AnimeID: 1, AnimeTitle: "某番", Episodes: []DanmakuEpisode{
			{EpisodeID: 0, EpisodeTitle: "第1话 目标"},
			{EpisodeID: -5, EpisodeTitle: "第1话 目标"},
			{EpisodeID: 333, EpisodeTitle: "第1话 目标"},
		}},
	}
	got, ok := pickDanmakuEpisodeID(candidates, "1", "第1话 目标")
	if !ok || got != 333 {
		t.Fatalf("pickDanmakuEpisodeID = (%d,%v), want (333,true)", got, ok)
	}
}
