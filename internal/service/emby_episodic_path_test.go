package service

import (
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// TestLikelyEpisodicPathSQLNormalisesSeparator 守住剧集路径判定的转义差异。
//
// 此前 embyLikelyEpisodicPathSQL 会额外生成一份反斜杠模式
// （strings.ReplaceAll(pattern, "/", `\`)）。PostgreSQL 的 LIKE 默认就把反斜杠
// 当转义符，模式结尾的 `\%` 被解析成「字面百分号」而不是「反斜杠 + 通配符」，
// 那些模式在 PostgreSQL 下永不命中；SQLite 没有默认转义符所以一直正常。
// 现在统一在 SQL 里把分隔符归一化成 '/'，模式本身不再含反斜杠。
func TestLikelyEpisodicPathSQLNormalisesSeparator(t *testing.T) {
	clause, args := embyLikelyEpisodicPathSQL()
	if !strings.Contains(clause, "REPLACE(media.path") {
		t.Fatalf("clause no longer normalises the path separator:\n%s", clause)
	}
	if got := strings.Count(clause, "LIKE ?"); got != len(args) {
		t.Fatalf("clause has %d placeholders but %d args:\n%s", got, len(args), clause)
	}
	for _, arg := range args {
		pattern, ok := arg.(string)
		if !ok {
			t.Fatalf("unexpected pattern type %T", arg)
		}
		if strings.Contains(pattern, `\`) {
			t.Fatalf("pattern %q still contains a backslash; PostgreSQL's LIKE would treat it as an escape character", pattern)
		}
		if !strings.Contains(pattern, "/") {
			t.Fatalf("pattern %q lost its separator", pattern)
		}
	}
}

// TestFilterLikelyEpisodicPathsHandlesWindowsStylePaths 验证反斜杠路径（Windows
// 宿主机入库的媒体）仍然被判成剧集并从电影库查询里排除。
func TestFilterLikelyEpisodicPathsHandlesWindowsStylePaths(t *testing.T) {
	db := newServiceTestDB(t)
	episodic := model.Media{
		Base:       model.Base{ID: "ep-1"},
		Path:       `D:\Media\国产剧\我的剧\Season 1\我的剧 - S01E01.mkv`,
		SeasonNum:  1,
		EpisodeNum: 1,
	}
	movie := model.Media{
		Base: model.Base{ID: "mv-1"},
		Path: `D:\Media\电影\某电影\某电影.mkv`,
	}
	if err := db.Create(&episodic).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&movie).Error; err != nil {
		t.Fatal(err)
	}

	q := filterLikelyEpisodicPathsFromMovieQuery(
		db.Model(&model.Media{}).Where("season_num > 0 OR episode_num > 0"),
	)
	var rows []model.Media
	if err := q.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("windows-style episodic path was not excluded, got %d rows", len(rows))
	}

	// 反证：电影类型的行必须保留。
	var kept []model.Media
	keepQ := filterLikelyEpisodicPathsFromMovieQuery(
		db.Model(&model.Media{}).Where("path LIKE ?", `D:\Media\电影\%`),
	)
	if err := keepQ.Find(&kept).Error; err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].ID != "mv-1" {
		t.Fatalf("plain movie row was dropped from the movie query: %+v", kept)
	}
}
