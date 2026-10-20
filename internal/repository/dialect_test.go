package repository

import (
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestSegmentPrewarmQueryBindsBooleanFound 守住 PostgreSQL 下片段预取查询的合法性。
//
// media_segment_fetches.found 是布尔列，而 PostgreSQL 没有 boolean = integer
// 运算符：写成 f.found = 0 / = 1 会让计划任务里的片段预取固定报
//
//	operator does not exist: boolean = integer (SQLSTATE 42883)
//
// SQLite 一直能跑，所以这个错只在 PostgreSQL 上暴露（表现为日志里周期性出现
// 该错误、片头片尾数据永远补不上）。这里断言语句里只能出现占位符绑定。
func TestSegmentPrewarmQueryBindsBooleanFound(t *testing.T) {
	query := segmentPrewarmCandidatesQuery
	lower := strings.ToLower(query)
	for _, forbidden := range []string{"found = 0", "found = 1", "found=0", "found=1", "found = true", "found = false"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("预取查询把布尔列与字面量比较（%q），PostgreSQL 会直接报错：\n%s", forbidden, query)
		}
	}
	if got := strings.Count(query, "f.found = ?"); got != 2 {
		t.Fatalf("expected 2 placeholder-bound found comparisons, got %d:\n%s", got, query)
	}
}

// TestLikeOperatorMatchesSQLiteCaseInsensitivity 守住搜索大小写行为的方言差异。
//
// SQLite 的 LIKE 对 ASCII 不区分大小写，PostgreSQL 的 LIKE 区分大小写：同一个
// 搜索词在两种方言下必须给出同样的结果集，PostgreSQL 必须走 ILIKE。
func TestLikeOperatorMatchesSQLiteCaseInsensitivity(t *testing.T) {
	pg, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "postgres://mebox:mebox@127.0.0.1:5432/mebox?sslmode=disable",
	}), &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatalf("open dry-run postgres: %v", err)
	}
	if got := LikeOperator(pg); got != "ILIKE" {
		t.Fatalf("postgres LikeOperator = %q, want ILIKE", got)
	}
	if got := LikeOperator(nil); got != "LIKE" {
		t.Fatalf("nil LikeOperator = %q, want LIKE", got)
	}
}
