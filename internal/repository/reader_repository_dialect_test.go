package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// sqlCaptureLogger 只记录 GORM 生成的 SQL，不打印任何日志。
type sqlCaptureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *sqlCaptureLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface { return l }
func (l *sqlCaptureLogger) Info(context.Context, string, ...interface{})     {}
func (l *sqlCaptureLogger) Warn(context.Context, string, ...interface{})     {}
func (l *sqlCaptureLogger) Error(context.Context, string, ...interface{})    {}

func (l *sqlCaptureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	l.lines = append(l.lines, sql)
	l.mu.Unlock()
}

func (l *sqlCaptureLogger) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// newPostgresDryRunRepos 返回使用 PostgreSQL 方言的 repository 容器：
// DryRun 只构建 SQL 不执行，DisableAutomaticPing 让它无需真实数据库也能离线运行，
// SkipDefaultTransaction 则挡掉 GORM 默认事务包装——那一步会真的去开事务连接，
// 在 DryRun 下同样会拨号（ToSQL 内部也是这么绕开的）。于是可以直接断言生成语句
// 在 PostgreSQL 下是否合法。
func newPostgresDryRunRepos(t *testing.T) (*Container, *sqlCaptureLogger) {
	t.Helper()
	rec := &sqlCaptureLogger{}
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "postgres://mebox:mebox@127.0.0.1:5432/mebox?sslmode=disable",
	}), &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 rec,
	})
	if err != nil {
		t.Fatalf("open dry-run postgres: %v", err)
	}
	return New(db), rec
}

// TestReaderListsUseDialectQuotedReservedColumns 守住阅读子系统列表查询在
// PostgreSQL 下的合法性。reader_books.order / reader_chapters.index /
// reader_replace_rules.order 都是关键字级列名（order 在 PostgreSQL 里是保留字）。
//
// 排序条件曾经写成硬编码反引号的原始字符串，例如 .Order("`order` ASC")：
// SQLite 正常，PostgreSQL 会把反引号词法分析成运算符，直接报
// `syntax error at or near "order"` (SQLSTATE 42601)，书架 / 目录 / 替换规则整页 500。
// 现在改用 clause.Column，由 GORM 按方言加引号（SQLite 反引号 / PostgreSQL 双引号）。
func TestReaderListsUseDialectQuotedReservedColumns(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		run   func(repos *Container) error
		wants []string
	}{
		{
			name:  "books",
			run:   func(repos *Container) error { _, err := repos.Reader.ListBooks(ctx, "user-1"); return err },
			wants: []string{`"order"`, `"updated_at" DESC`},
		},
		{
			name:  "chapters",
			run:   func(repos *Container) error { _, err := repos.Reader.ListChapters(ctx, "book-1"); return err },
			wants: []string{`"index"`},
		},
		{
			name:  "replace rules",
			run:   func(repos *Container) error { _, err := repos.Reader.ListReplaceRules(ctx, "user-1"); return err },
			wants: []string{`"order" DESC`, `"created_at"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos, rec := newPostgresDryRunRepos(t)
			if err := tc.run(repos); err != nil {
				t.Fatalf("build query: %v", err)
			}
			sql := rec.all()
			if strings.TrimSpace(sql) == "" {
				t.Fatal("no SQL captured from the repository call")
			}
			if strings.Contains(sql, "`") {
				t.Fatalf("SQL uses backtick quoting, which PostgreSQL rejects:\n%s", sql)
			}
			if !strings.Contains(sql, "ORDER BY") {
				t.Fatalf("SQL has no ORDER BY clause:\n%s", sql)
			}
			for _, want := range tc.wants {
				if !strings.Contains(sql, want) {
					t.Fatalf("SQL missing %q:\n%s", want, sql)
				}
			}
		})
	}
}
