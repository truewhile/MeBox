package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// PostgreSQL tests use a disposable schema, never the caller's existing tables.
// Set MEBOX_TEST_POSTGRES_DSN to include a real PostgreSQL server in local runs.
func compatibilityDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	var db *gorm.DB
	if dialect == "sqlite" {
		cfg := &config.Config{}
		cfg.Database.Type = dialect
		cfg.Database.DBPath = filepath.Join(t.TempDir(), "test.db")
		cfg.Database.WALMode = true
		cfg.Database.BusyTimeout = 5000
		cfg.Database.MaxOpenConns = 8
		var err error
		db, err = Open(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		dsn := os.Getenv("MEBOX_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("set MEBOX_TEST_POSTGRES_DSN to run PostgreSQL integration tests")
		}
		pgcfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid PostgreSQL test configuration")
		}
		admin := stdlib.OpenDB(*pgcfg)
		t.Cleanup(func() { _ = admin.Close() })
		schemaName := "mebox_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if _, err := admin.Exec("CREATE SCHEMA " + quoteIdent(schemaName)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := admin.Exec("DROP SCHEMA " + quoteIdent(schemaName) + " CASCADE"); err != nil {
				t.Error(err)
			}
		})
		pgcfg.RuntimeParams["search_path"] = schemaName + ",public"
		conn := stdlib.OpenDB(*pgcfg)
		t.Cleanup(func() { _ = conn.Close() })
		db, err = gorm.Open(postgres.New(postgres.Config{Conn: conn}), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
	}
	db.Logger = logger.Default.LogMode(logger.Silent)
	if conn, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDatabaseCompatibility(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			t.Run("long_paths_and_restart", func(t *testing.T) {
				db := compatibilityDB(t, dialect)
				// Distinct CJK characters prevent TOAST compression from hiding
				// the PostgreSQL btree entry size limit (> 2704 bytes).
				var path strings.Builder
				path.WriteString("/media/")
				for i := range 980 {
					path.WriteRune(rune(0x4e00 + i))
				}
				path.WriteString(".mkv")
				media := model.Media{Path: path.String(), Title: "Long path"}
				if err := db.Create(&media).Error; err != nil {
					t.Fatalf("insert long media path: %v", err)
				}
				if err := db.Create(&model.Media{Path: media.Path}).Error; err == nil {
					t.Fatal("duplicate path accepted")
				}
				if err := AutoMigrate(db); err != nil {
					t.Fatalf("migrate existing database: %v", err)
				}
				var found model.Media
				if err := db.Where("path = ?", media.Path).First(&found).Error; err != nil {
					t.Fatalf("lookup long path: %v", err)
				}
			})
			if dialect == "postgres" {
				t.Run("upgrade_legacy_path_indexes", func(t *testing.T) {
					db := compatibilityDB(t, dialect)
					for _, unique := range []string{"UNIQUE ", ""} {
						if err := db.Exec("DROP INDEX idx_media_path").Error; err != nil {
							t.Fatal(err)
						}
						if err := db.Exec("CREATE " + unique + "INDEX idx_media_path ON media(path)").Error; err != nil {
							t.Fatal(err)
						}
						for range 2 {
							if err := ensureMediaPathIndex(db); err != nil {
								t.Fatal(err)
							}
						}
						isUnique, method, found, err := postgresIndexProperties(db, "idx_media_path")
						if err != nil || !found || isUnique || method != "hash" {
							t.Fatalf("legacy index not upgraded: unique=%v method=%s found=%v err=%v", isUnique, method, found, err)
						}
					}
				})
			}
			t.Run("search_ranking", func(t *testing.T) {
				db := compatibilityDB(t, dialect)
				// Exercise LIKE fallback in both engines.
				if dialect == "sqlite" {
					resetMediaSearchIndex(db)
				}
				for i, title := range []string{"The Alpha story", "Alpha sequel", "Alpha"} {
					row := model.Media{Base: model.Base{ID: fmt.Sprintf("m-%d", i), CreatedAt: time.Now().Add(time.Duration(i) * time.Minute)}, Title: title, Path: fmt.Sprintf("/media/%d.mkv", i)}
					if err := db.Create(&row).Error; err != nil {
						t.Fatal(err)
					}
				}
				items, total, err := repository.New(db).Media.SearchFilteredPage(t.Context(), "Alpha", 0, 10, repository.MediaQueryFilter{IncludeNSFW: true})
				if err != nil {
					t.Fatal(err)
				}
				if total != 3 || len(items) != 3 || items[0].Title != "Alpha" || items[1].Title != "Alpha sequel" {
					t.Fatalf("unexpected search ranking: total=%d, items=%v", total, items)
				}
			})
			t.Run("concurrent_media_upsert", func(t *testing.T) {
				db := compatibilityDB(t, dialect)
				repo := repository.New(db).Media
				const workers = 8
				// Synchronize the initial lookups to force the insert race.
				var lookups sync.WaitGroup
				lookups.Add(workers)
				if dialect == "postgres" {
					if err := db.Callback().Query().After("gorm:after_query").Register("test:insert_race", func(tx *gorm.DB) {
						if tx.Statement.Table == "media" && tx.Error == gorm.ErrRecordNotFound {
							lookups.Done()
							lookups.Wait()
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				errs := make(chan error, workers)
				for range workers {
					go func() { errs <- repo.Upsert(ctx, &model.Media{Path: "/media/shared.mkv", Title: "Shared"}) }()
				}
				for range workers {
					if err := <-errs; err != nil {
						t.Errorf("concurrent upsert: %v", err)
					}
				}
				var count int64
				if err := db.Model(&model.Media{}).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("media count=%d, err=%v", count, err)
				}
			})
			t.Run("search_filters", func(t *testing.T) {
				db := compatibilityDB(t, dialect)
				rows := []model.Media{
					{Base: model.Base{ID: "keep"}, LibraryID: "lib-1", Genres: "Action", Year: 2020, Rating: 8},
					{Base: model.Base{ID: "other-library"}, LibraryID: "lib-2", Genres: "Action", Year: 2020, Rating: 8},
					{Base: model.Base{ID: "other-genre"}, LibraryID: "lib-1", Genres: "Comedy", Year: 2020, Rating: 8},
					{Base: model.Base{ID: "too-old"}, LibraryID: "lib-1", Genres: "Action", Year: 1990, Rating: 8},
					{Base: model.Base{ID: "too-new"}, LibraryID: "lib-1", Genres: "Action", Year: 2026, Rating: 8},
					{Base: model.Base{ID: "low-rating"}, LibraryID: "lib-1", Genres: "Action", Year: 2020, Rating: 2},
					{Base: model.Base{ID: "watched"}, LibraryID: "lib-1", Genres: "Action", Year: 2020, Rating: 8},
				}
				for i := range rows {
					rows[i].Title = "Alpha " + rows[i].ID
					rows[i].Path = "/media/" + rows[i].ID + ".mkv"
					if err := db.Create(&rows[i]).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Create(&model.PlaybackHistory{UserID: "viewer", MediaID: "watched", Completed: true}).Error; err != nil {
					t.Fatal(err)
				}
				items, total, err := repository.New(db).Media.SearchFilteredPage(t.Context(), "alpha", 0, 10, repository.MediaQueryFilter{
					IncludeNSFW: true, LibraryID: "lib-1", Genres: []string{"Action"}, YearMin: 2000, YearMax: 2025, RatingMin: 5,
					UnwatchedOnly: true, UnwatchedUserID: "viewer",
				})
				if err != nil {
					t.Fatal(err)
				}
				if total != 1 || len(items) != 1 || items[0].ID != "keep" {
					t.Fatalf("search ignored filters: total=%d, items=%v", total, items)
				}
			})
			t.Run("migration_preserves_zero_values", func(t *testing.T) {
				src := compatibilityDB(t, "sqlite")
				dst := compatibilityDB(t, dialect)
				user := model.User{Username: "disabled", PasswordHash: "hash"}
				if err := src.Create(&user).Error; err != nil {
					t.Fatal(err)
				}
				if err := src.Model(&user).Updates(map[string]any{"is_active": false, "hide_adult": false, "player_volume": 0, "danmaku_enabled": false}).Error; err != nil {
					t.Fatal(err)
				}
				if _, _, err := copyModelTables(src, dst, 500); err != nil {
					t.Fatal(err)
				}
				var got model.User
				if err := dst.First(&got, "id = ?", user.ID).Error; err != nil {
					t.Fatal(err)
				}
				if got.IsActive || got.HideAdult || got.PlayerVolume != 0 || got.DanmakuEnabled {
					t.Fatalf("migration replaced stored zero values: active=%v hide_adult=%v volume=%v danmaku=%v", got.IsActive, got.HideAdult, got.PlayerVolume, got.DanmakuEnabled)
				}
			})
		})
	}
}
