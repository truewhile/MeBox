package service

import "testing"

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		in           string
		wantS, wantE int
	}{
		{"Breaking.Bad.S01E02.1080p.mkv", 1, 2},
		{"breaking.bad.s5e14.mkv", 5, 14},
		{"Friends 1x02.mp4", 1, 2},
		{"Friends 10x24 - The One Where.mkv", 10, 24},
		{"Some Anime - EP05 [1080p].mkv", 1, 5},
		{"Some Anime - E12.mkv", 1, 12},
		{"[MagicStar] 凡人修仙传 年番 - 146 [1080p].mkv", 1, 146},
		{`Some Show/Season 02/Some Show - EP03.mkv`, 2, 3},
		{`Some Show/S02/Some Show - E04.mkv`, 2, 4},
		{`剧集/第2季/剧集 第05集.mkv`, 2, 5},
		{"日剧 第03集.mkv", 1, 3},
		{"日剧 第十集.mkv", 1, 10},
		{"日剧 第二十五话.mkv", 1, 25},
		{"日剧 第12话.mkv", 1, 12},
		{"综艺 第4期下.mkv", 1, 4},
		{`综艺/Season 06/综艺 第17期.mkv`, 6, 17},
		{`动漫/第二季/04.mkv`, 2, 4},
		{`动漫/第十季/第十一集.mkv`, 10, 11},
		{`剧集/S00/剧集 - E01.mkv`, 0, 1},
		{`剧集/Specials/剧集 - 02.mkv`, 0, 2},
		{`剧集/特别篇/03.mkv`, 0, 3},
		{`剧集/剧集 - S00E04.mkv`, 0, 4},
		{`动漫/摇曳露营/OVA/Season 3 [OVA01 [1080p].mkv`, 0, 1},
		{`动漫/示例/OAD/示例.OAD02.mkv`, 0, 2},
		{`动漫/示例/OVD/示例-OVD03.mkv`, 0, 3},
		{`动漫/示例/ONA/示例_ONA04.mkv`, 0, 4},
		{"Movie.2020.1080p.mkv", 0, 0},
		// 分辨率不能被当成季集号：1920x1080 曾匹配出 20x108。
		{"Movie.2020.1920x1080.mkv", 0, 0},
		{"1920x1080.mkv", 0, 0},
		{"[Group][Show][02][3840x2160].mkv", 1, 2},
		// 字幕组方括号集号。
		{"[UHA-WINGS][Peter Grill to Kenja no Jikan][01][BDRIP 1920x1080 HEVC-YUV420P10 FLAC].strm", 1, 1},
		{"[UHA-WINGS][Peter Grill to Kenja no Jikan][12][BDRIP 1920x1080 HEVC-YUV420P10 FLAC].strm", 1, 12},
		// 方括号里的年份/分辨率不是集号。
		{"[Group][Show][2024][1080p].mkv", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			s, e := ParseEpisode(tc.in)
			if s != tc.wantS || e != tc.wantE {
				t.Errorf("ParseEpisode(%q) = (%d, %d), want (%d, %d)",
					tc.in, s, e, tc.wantS, tc.wantE)
			}
		})
	}
}

func TestEpisodeRefsFromTitleParsesRanges(t *testing.T) {
	tests := []struct {
		name string
		want []episodeRef
	}{
		{
			name: "Archives The Nanyang Mystery 2026 S01E07-S01E08 2160p",
			want: []episodeRef{{Season: 1, Episode: 7}, {Season: 1, Episode: 8}},
		},
		{
			name: "The Heir 2026 S01E39-E42 WEB-DL",
			want: []episodeRef{{Season: 1, Episode: 39}, {Season: 1, Episode: 40}, {Season: 1, Episode: 41}, {Season: 1, Episode: 42}},
		},
	}
	for _, tt := range tests {
		got := episodeRefsFromTitle(tt.name)
		if len(got) != len(tt.want) {
			t.Fatalf("episodeRefsFromTitle(%q) len = %d, want %d: %#v", tt.name, len(got), len(tt.want), got)
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Fatalf("episodeRefsFromTitle(%q)[%d] = %#v, want %#v", tt.name, i, got[i], tt.want[i])
			}
		}
	}
}

func TestOnlineEpisodeIdentityFromPathMapsAnimeEpisodeZeroToSpecials(t *testing.T) {
	cases := []struct {
		path        string
		wantSeason  int
		wantEpisode int
	}{
		{`动漫/路人女主/Season 1/S01E00 - 爱与青春的杀必死回.mkv`, 0, 1},
		{`动漫/路人女主/Season 2/S02E00 - 恋爱与纯情的杀必死回.mkv`, 0, 2},
		{`动漫/路人女主/Season 2/S02E03 - 初稿与二稿.mkv`, 2, 3},
		{`动漫/路人女主/Specials/S00E04.mkv`, 0, 4},
	}
	for _, tc := range cases {
		season, episode := onlineEpisodeIdentityFromPath(tc.path)
		if season != tc.wantSeason || episode != tc.wantEpisode {
			t.Errorf("onlineEpisodeIdentityFromPath(%q) = (%d, %d), want (%d, %d)",
				tc.path, season, episode, tc.wantSeason, tc.wantEpisode)
		}
	}
}

func TestResolutionEpisodeArtifact(t *testing.T) {
	cases := []struct {
		path        string
		wantSeason  int
		wantEpisode int
		wantOK      bool
	}{
		{"[UHA-WINGS][Peter Grill to Kenja no Jikan][01][BDRIP 1920x1080 HEVC-YUV420P10 FLAC].strm", 20, 108, true},
		{"今永纱奈/20170601000150_2,00x_3840x2160_amq-13.strm", 40, 216, true},
		{"Movie.2020.1080p.mkv", 0, 0, false},
		{"Show.S01E02.mkv", 0, 0, false},
	}
	for _, tc := range cases {
		season, episode, ok := resolutionEpisodeArtifact(tc.path)
		if season != tc.wantSeason || episode != tc.wantEpisode || ok != tc.wantOK {
			t.Errorf("resolutionEpisodeArtifact(%q) = (%d, %d, %v), want (%d, %d, %v)",
				tc.path, season, episode, ok, tc.wantSeason, tc.wantEpisode, tc.wantOK)
		}
	}
}

func TestDropResolutionArtifactEpisodeIdentity(t *testing.T) {
	// 分辨率伪集号被剔除，并从生成的「第 N 集」标题里清掉。
	polluted := &LocalMetadata{SeasonNum: 20, EpisodeNum: 108, EpisodeTitle: "第 108 集"}
	dropResolutionArtifactEpisodeIdentity(polluted, "[G][Show][01][BDRIP 1920x1080 x].strm")
	if polluted.SeasonNum != 0 || polluted.EpisodeNum != 0 || polluted.EpisodeTitle != "" {
		t.Fatalf("resolution artifact not dropped: %+v", polluted)
	}

	// 真实单集号不受影响。
	real := &LocalMetadata{SeasonNum: 1, EpisodeNum: 3, EpisodeTitle: "本地第三集"}
	dropResolutionArtifactEpisodeIdentity(real, "Show/S01E03 1920x1080.mkv")
	if real.SeasonNum != 1 || real.EpisodeNum != 3 || real.EpisodeTitle != "本地第三集" {
		t.Fatalf("real episode identity was modified: %+v", real)
	}

	// 名称里没有分辨率时不改动任何值。
	plain := &LocalMetadata{SeasonNum: 20, EpisodeNum: 108}
	dropResolutionArtifactEpisodeIdentity(plain, "Show/S20E108.mkv")
	if plain.SeasonNum != 20 || plain.EpisodeNum != 108 {
		t.Fatalf("unrelated identity was cleared: %+v", plain)
	}

	// 文件名里 S20E108 是真实标记时，即使同时含分辨率也必须保留。
	legit := &LocalMetadata{SeasonNum: 20, EpisodeNum: 108}
	dropResolutionArtifactEpisodeIdentity(legit, "Show/Show.S20E108.1920x1080.mkv")
	if legit.SeasonNum != 20 || legit.EpisodeNum != 108 {
		t.Fatalf("legitimate S20E108 identity was cleared: %+v", legit)
	}
}
