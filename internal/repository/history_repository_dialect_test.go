package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// TestHistoryProgressUpsertQualifiesConflictWhereColumns 守住 PostgreSQL 下进度上报
// 的 upsert 语句合法性。
//
// ON CONFLICT DO UPDATE 的 WHERE 里如果不给列名加表限定，PostgreSQL 会因为这些列
// 同时出现在本次插入的行里而拒绝该语句：
//
//	column reference "session_started_at_ms" is ambiguous (SQLSTATE 42702)
//
// 后果是 POST /api/history 全部 500，观看进度与续播完全存不下来。SQLite 不做这项
// 检查（旧写法在 SQLite 下一直正常），所以只能在 PostgreSQL 方言下断言。
func TestHistoryProgressUpsertQualifiesConflictWhereColumns(t *testing.T) {
	repos, rec := newPostgresDryRunRepos(t)
	ctx := context.Background()

	if err := repos.History.UpsertProgress(ctx, &model.PlaybackHistory{
		UserID:             "u-1",
		MediaID:            "m-1",
		PositionMs:         1_000,
		DurationMs:         2_000,
		SessionID:          "session-1",
		SessionStartedAtMs: 111,
		Sequence:           2,
	}); err != nil {
		t.Fatalf("build upsert: %v", err)
	}

	sql := rec.all()
	if strings.TrimSpace(sql) == "" {
		t.Fatal("no SQL captured from UpsertProgress")
	}
	if !strings.Contains(sql, "ON CONFLICT") {
		t.Fatalf("expected an ON CONFLICT upsert, got:\n%s", sql)
	}

	// 只看 ON CONFLICT 之后的部分：SET 的赋值目标和 CASE 里的 duration_ms 引用
	// 属于另一套语法规则，不在这里断言。
	conflict := sql[strings.Index(sql, "ON CONFLICT"):]
	whereIdx := strings.Index(conflict, " WHERE ")
	if whereIdx < 0 {
		t.Fatalf("ON CONFLICT clause has no DO UPDATE ... WHERE guard:\n%s", sql)
	}
	where := conflict[whereIdx:]

	for _, want := range []string{
		`"playback_histories"."session_started_at_ms"`,
		`"playback_histories"."sequence"`,
	} {
		if !strings.Contains(where, want) {
			t.Fatalf("DO UPDATE WHERE is missing the table-qualified %s; it is ambiguous on PostgreSQL:\n%s", want, sql)
		}
	}
	if strings.Contains(where, `("session_started_at_ms"`) || strings.Contains(where, ` AND "sequence"`) {
		t.Fatalf("DO UPDATE WHERE still references unqualified columns:\n%s", sql)
	}
}
