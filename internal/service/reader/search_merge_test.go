package reader

import "testing"

// 同书多源合并：先到的源没有封面/简介时，必须用后面源的有效值补齐。
// 否则「有源带封面」的书在合并成一行后会是空白（legado 一源一行则不会出现）。
func TestMergeSearchResultsFillsMissingFields(t *testing.T) {
	hits := []searchHit{
		{book: SearchBook{
			Name:     "全球高武",
			Author:   "蛮荒帝霸",
			CoverURL: "",
			Origins:  []SearchOrigin{{OriginName: "搜书神器", BookURL: "/a"}},
		}},
		{book: SearchBook{
			Name:          "全球高武",
			Author:        "蛮荒帝霸",
			CoverURL:      "https://res.chuangke.tv/qk/246/a3/76/2271.jpg",
			Intro:         "一句话简介",
			Kind:          "连载",
			WordCount:     "177.2万字",
			LatestChapter: "第694章",
			Origins:       []SearchOrigin{{OriginName: "svip_AU文学", BookURL: "/b"}},
		}},
	}

	got := mergeSearchResults(hits, "全球高武")
	if len(got) != 1 {
		t.Fatalf("合并后应只有 1 条，实际 %d", len(got))
	}
	book := got[0]
	if book.CoverURL != "https://res.chuangke.tv/qk/246/a3/76/2271.jpg" {
		t.Fatalf("封面未从其它源补齐: %q", book.CoverURL)
	}
	if book.Intro != "一句话简介" {
		t.Fatalf("简介未补齐: %q", book.Intro)
	}
	if book.Kind != "连载" || book.WordCount != "177.2万字" || book.LatestChapter != "第694章" {
		t.Fatalf("字段未补齐: %+v", book)
	}
	if len(book.Origins) != 2 {
		t.Fatalf("源数 = %d, want 2", len(book.Origins))
	}
}

// 已经有值的字段不能被后来的源覆盖（先到的更权威，且保证结果稳定）。
func TestMergeSearchResultsKeepsExistingFields(t *testing.T) {
	hits := []searchHit{
		{book: SearchBook{Name: "书", Author: "作者", CoverURL: "https://a.example.com/1.jpg", Intro: "甲的简介"}},
		{book: SearchBook{Name: "书", Author: "作者", CoverURL: "https://b.example.com/2.jpg", Intro: "乙的简介"}},
	}

	got := mergeSearchResults(hits, "书")
	if len(got) != 1 {
		t.Fatalf("合并后应只有 1 条，实际 %d", len(got))
	}
	if got[0].CoverURL != "https://a.example.com/1.jpg" || got[0].Intro != "甲的简介" {
		t.Fatalf("不该被后来的源覆盖: %+v", got[0])
	}
}

// 无书名的结果直接丢弃，且不会参与合并。
func TestMergeSearchResultsSkipsEmptyName(t *testing.T) {
	hits := []searchHit{
		{book: SearchBook{Name: "", Author: "作者", CoverURL: "https://a.example.com/1.jpg"}},
		{book: SearchBook{Name: "书", Author: "作者"}},
	}

	got := mergeSearchResults(hits, "书")
	if len(got) != 1 || got[0].Name != "书" {
		t.Fatalf("结果 = %+v", got)
	}
}
