package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/model"
)

func newMediaFilterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Media{}, &model.PlaybackHistory{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedFilterMedia(t *testing.T, db *gorm.DB, rows ...*model.Media) {
	t.Helper()
	for _, row := range rows {
		if err := db.WithContext(context.Background()).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func listFiltered(t *testing.T, db *gorm.DB, filter MediaQueryFilter) []string {
	t.Helper()
	var rows []model.Media
	q := db.WithContext(context.Background()).Model(&model.Media{})
	q = applyMediaQueryFilter(q, filter)
	if err := q.Order("title asc").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Title)
	}
	return out
}

func hasTitle(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// 多个类型之间是「或」：勾选 Action 与 Comedy 应同时命中两类。
func TestFilterByGenreOR(t *testing.T) {
	db := newMediaFilterTestDB(t)
	seedFilterMedia(t, db,
		&model.Media{Title: "动作", Genres: "Action", Path: "/a.mkv", LibraryID: "lib-1"},
		&model.Media{Title: "喜剧", Genres: "Comedy", Path: "/b.mkv", LibraryID: "lib-1"},
		&model.Media{Title: "剧情", Genres: "Drama", Path: "/c.mkv", LibraryID: "lib-1"},
	)

	got := listFiltered(t, db, MediaQueryFilter{IncludeNSFW: true, Genres: []string{"Action", "Comedy"}})
	if !hasTitle(got, "动作") || !hasTitle(got, "喜剧") {
		t.Fatalf("result = %v, want both 动作 and 喜剧", got)
	}
	if hasTitle(got, "剧情") {
		t.Fatalf("result = %v, must not contain 剧情", got)
	}
}

// 类型匹配必须是整词匹配：搜 "Action" 不能命中 "ActionComedy" 这类拼接值。
func TestFilterByGenreDoesNotMatchSubstring(t *testing.T) {
	db := newMediaFilterTestDB(t)
	seedFilterMedia(t, db,
		&model.Media{Title: "精确", Genres: "Action,Drama", Path: "/a.mkv", LibraryID: "lib-1"},
		&model.Media{Title: "拼接", Genres: "ActionComedy", Path: "/b.mkv", LibraryID: "lib-1"},
	)

	got := listFiltered(t, db, MediaQueryFilter{IncludeNSFW: true, Genres: []string{"Action"}})
	if !hasTitle(got, "精确") {
		t.Fatalf("result = %v, want 精确", got)
	}
	if hasTitle(got, "拼接") {
		t.Fatalf("result = %v, must not match ActionComedy for Action", got)
	}
}

func TestFilterYearAndRating(t *testing.T) {
	db := newMediaFilterTestDB(t)
	seedFilterMedia(t, db,
		&model.Media{Title: "老片", Year: 1995, Rating: 9, Path: "/a.mkv", LibraryID: "lib-1"},
		&model.Media{Title: "中年", Year: 2010, Rating: 5, Path: "/b.mkv", LibraryID: "lib-1"},
		&model.Media{Title: "新片", Year: 2023, Rating: 8, Path: "/c.mkv", LibraryID: "lib-1"},
	)

	got := listFiltered(t, db, MediaQueryFilter{IncludeNSFW: true, YearMin: 2000, YearMax: 2020})
	if len(got) != 1 || got[0] != "中年" {
		t.Fatalf("year filter result = %v, want [中年]", got)
	}

	got = listFiltered(t, db, MediaQueryFilter{IncludeNSFW: true, RatingMin: 8})
	if len(got) != 2 {
		t.Fatalf("rating filter result = %v, want 2 entries", got)
	}
}

// 「未观看」的语义是「没有标记看完的记录」：看了一半的仍应出现。
func TestFilterUnwatchedExcludesCompleted(t *testing.T) {
	db := newMediaFilterTestDB(t)
	seedFilterMedia(t, db,
		&model.Media{Base: model.Base{ID: "m-done"}, Title: "看完", Path: "/a.mkv", LibraryID: "lib-1"},
		&model.Media{Base: model.Base{ID: "m-half"}, Title: "看一半", Path: "/b.mkv", LibraryID: "lib-1"},
		&model.Media{Base: model.Base{ID: "m-new"}, Title: "没看过", Path: "/c.mkv", LibraryID: "lib-1"},
	)

	ctx := context.Background()
	for _, h := range []*model.PlaybackHistory{
		{UserID: "u1", MediaID: "m-done", Completed: true},
		{UserID: "u1", MediaID: "m-half", Completed: false},
		// 别人的完播记录不应影响本人筛选。
		{UserID: "u2", MediaID: "m-new", Completed: true},
	} {
		if err := db.WithContext(ctx).Create(h).Error; err != nil {
			t.Fatal(err)
		}
	}

	got := listFiltered(t, db, MediaQueryFilter{
		IncludeNSFW: true, UnwatchedOnly: true, UnwatchedUserID: "u1",
	})
	if hasTitle(got, "看完") {
		t.Fatalf("result = %v, must exclude completed media", got)
	}
	if !hasTitle(got, "看一半") || !hasTitle(got, "没看过") {
		t.Fatalf("result = %v, want both 看一半 and 没看过", got)
	}
}

// 多词类型（如 "Science Fiction"）：列侧 SQL 会 REPLACE 掉空格，参数侧也必须同步
// 去掉空格，两侧对称才能命中。
func TestFilterByGenreMultiWordStripsSpaces(t *testing.T) {
	db := newMediaFilterTestDB(t)
	seedFilterMedia(t, db,
		&model.Media{Title: "科幻", Genres: "Science Fiction,Drama", Path: "/a.mkv", LibraryID: "lib-1"},
		&model.Media{Title: "动作", Genres: "Action", Path: "/b.mkv", LibraryID: "lib-1"},
	)

	got := listFiltered(t, db, MediaQueryFilter{IncludeNSFW: true, Genres: []string{"Science Fiction"}})
	if !hasTitle(got, "科幻") {
		t.Fatalf("result = %v, want 科幻 (multi-word genre must match after space stripping)", got)
	}
	if hasTitle(got, "动作") {
		t.Fatalf("result = %v, must not contain 动作", got)
	}
}

// UnwatchedOnly 缺省 userID 时必须忽略该条件，而不是返回空结果。
func TestFilterUnwatchedWithoutUserIsIgnored(t *testing.T) {
	db := newMediaFilterTestDB(t)
	seedFilterMedia(t, db, &model.Media{Title: "片", Path: "/a.mkv", LibraryID: "lib-1"})

	got := listFiltered(t, db, MediaQueryFilter{IncludeNSFW: true, UnwatchedOnly: true})
	if len(got) != 1 {
		t.Fatalf("result = %v, want the row to be returned", got)
	}
}
