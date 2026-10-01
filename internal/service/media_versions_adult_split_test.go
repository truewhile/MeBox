package service

import (
	"strconv"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// 真实场景：同一部片的分片里只有第 4 个被刮削成功（nsfw=true），其余仍是 pending。
// 修复前：已刮削的分片走「番号」分组、其余落到「标题」分组，7 个分片会裂成两张卡
// （一张只有第 4 个，另一张是其余 6 个）。
func TestGroupMediaVersionsKeepsUnscrapedPartsWithScrapedSibling(t *testing.T) {
	const libID = "lib-1"
	const dir = "/media/云下载/sivr-270/"

	parts := func(nums ...int) []model.Media {
		out := make([]model.Media, 0, len(nums))
		for _, n := range nums {
			out = append(out, model.Media{
				LibraryID: libID,
				Title:     "sivr 270",
				Path:      dir + "sivr-270-" + strconv.Itoa(n) + ".strm",
				Container: "strm",
			})
		}
		return out
	}

	items := parts(1, 2, 3)
	items = append(items, model.Media{
		LibraryID:    libID,
		Title:        "SIVR-270-【VR】河北彩花×ご奉仕ナース",
		OriginalName: "SIVR-270",
		Path:         dir + "sivr-270-4.strm",
		NSFW:         true,
		ScrapeStatus: "matched",
		Container:    "strm",
	})
	items = append(items, parts(5, 6, 7)...)

	grouped := GroupMediaVersions(items)
	if len(grouped) != 1 {
		paths := make([]string, 0, len(grouped))
		for _, g := range grouped {
			paths = append(paths, g.Path)
		}
		t.Fatalf("分成 %d 张卡 %v，期望折叠成 1 张", len(grouped), paths)
	}
	if len(grouped[0].Versions) != 7 {
		t.Fatalf("版本数 = %d，期望 7", len(grouped[0].Versions))
	}
}

// 反向保护：没有任何分片被确认为成人内容时，不能仅凭文件名里的番号形态折叠，
// 否则普通媒体也可能被误并进同一张卡。
func TestGroupMediaVersionsIgnoresUnvouchedAdultCode(t *testing.T) {
	items := []model.Media{
		{LibraryID: "lib-2", Title: "Alpha Movie", Path: "/media/movies/Alpha Movie/ABC-123-1080p.mkv"},
		{LibraryID: "lib-2", Title: "Beta Movie", Path: "/media/movies/Beta Movie/ABC-123-2160p.mkv"},
	}
	grouped := GroupMediaVersions(items)
	if len(grouped) != 2 {
		t.Fatalf("分成 %d 张卡，期望 2 张（未被确认成人内容的番号不参与折叠）", len(grouped))
	}
}
