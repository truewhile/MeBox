package database

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
)

// DatabaseStatus describes the currently active database engine and runtime metrics.
type DatabaseStatus struct {
	Type         string           `json:"type"`
	DSN          string           `json:"dsn,omitempty"`
	DBPath       string           `json:"db_path,omitempty"`
	OpenConns    int              `json:"open_conns"`
	InUse        int              `json:"in_use"`
	Idle         int              `json:"idle"`
	MaxOpenConns int              `json:"max_open_conns"`
	TableCounts  map[string]int64 `json:"table_counts"`
}

// PostgresTestResult returns latency and version info after testing connection.
type PostgresTestResult struct {
	Success   bool   `json:"success"`
	LatencyMS int64  `json:"latency_ms"`
	Version   string `json:"version,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
}

// DatabaseMigrationResult returns row counts and execution duration of migration.
type DatabaseMigrationResult struct {
	Success    bool             `json:"success"`
	TotalRows  int64            `json:"total_rows"`
	TableRows  map[string]int64 `json:"table_rows"`
	DurationMS int64            `json:"duration_ms"`
	Message    string           `json:"message,omitempty"`
	Error      string           `json:"error,omitempty"`
}

// InspectDatabaseStatus queries the currently active database for metrics and table rows.
func InspectDatabaseStatus(db *gorm.DB, cfg *config.Config) *DatabaseStatus {
	st := &DatabaseStatus{
		Type:        "sqlite",
		TableCounts: make(map[string]int64),
	}
	if cfg != nil {
		st.DBPath = cfg.Database.DBPath
		if cfg.Database.Type == "postgres" || (cfg.Database.Type == "auto" && strings.TrimSpace(cfg.Database.DSN) != "") {
			st.Type = "postgres"
			st.DSN = MaskDSN(cfg.Database.DSN)
		}
	}
	if isPostgres(db) {
		st.Type = "postgres"
	}

	if db != nil {
		if sqlDB, err := db.DB(); err == nil {
			stats := sqlDB.Stats()
			st.OpenConns = stats.OpenConnections
			st.InUse = stats.InUse
			st.Idle = stats.Idle
			st.MaxOpenConns = stats.MaxOpenConnections
		}

		// Count rows for major model tables
		for _, m := range model.AllModels() {
			if tbl, err := modelTableName(db, m); err == nil {
				if db.Migrator().HasTable(tbl) {
					var count int64
					if err := db.Raw("SELECT COUNT(1) FROM " + quoteIdent(tbl)).Scan(&count).Error; err == nil {
						st.TableCounts[tbl] = count
					}
				}
			}
		}
	}
	return st
}

// TestPostgres establishes a temporary connection to verify reachability and permissions.
func TestPostgres(dsn string) (*PostgresTestResult, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return &PostgresTestResult{
			Success: false,
			Error:   "PostgreSQL DSN 不能为空",
		}, nil
	}

	start := time.Now()
	testDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return &PostgresTestResult{
			Success: false,
			Error:   fmt.Sprintf("连接失败: %v", err),
		}, nil
	}

	sqlDB, err := testDB.DB()
	if err != nil {
		return &PostgresTestResult{
			Success: false,
			Error:   fmt.Sprintf("获取底层连接失败: %v", err),
		}, nil
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		return &PostgresTestResult{
			Success: false,
			Error:   fmt.Sprintf("Ping 超时或失败: %v", err),
		}, nil
	}

	var version string
	if err := testDB.WithContext(ctx).Raw("SELECT version()").Scan(&version).Error; err != nil {
		version = "PostgreSQL (unknown version)"
	}

	latency := time.Since(start).Milliseconds()
	return &PostgresTestResult{
		Success:   true,
		LatencyMS: latency,
		Version:   version,
		Message:   "连接成功",
	}, nil
}

// MigrateCurrentToPostgres performs schema initialization and full table data copy into target PostgreSQL.
func MigrateCurrentToPostgres(src *gorm.DB, targetDSN string, batchSize int, log *zap.Logger) (*DatabaseMigrationResult, error) {
	targetDSN = strings.TrimSpace(targetDSN)
	if targetDSN == "" {
		return nil, fmt.Errorf("target PostgreSQL DSN cannot be empty")
	}
	if src == nil {
		return nil, fmt.Errorf("current database is not available")
	}

	started := time.Now()
	targetDB, err := gorm.Open(postgres.Open(targetDSN), &gorm.Config{
		Logger: newGormLogger(log),
	})
	if err != nil {
		return nil, fmt.Errorf("open target PostgreSQL: %w", err)
	}
	targetSQLDB, err := targetDB.DB()
	if err == nil {
		defer targetSQLDB.Close()
	}

	// 1. 初始化目标库 Schema、类型与索引
	if err := AutoMigrateWithConfig(targetDB, nil, log); err != nil {
		return nil, fmt.Errorf("auto migrate target PostgreSQL: %w", err)
	}

	// 2. 安全重置目标数据库的初始默认数据
	if err := resetBootstrapTargetBeforeSQLiteMigrationIfSafe(src, targetDB, log); err != nil {
		return nil, fmt.Errorf("reset target bootstrap data: %w", err)
	}

	// 3. 执行数据批量复制
	tableRows, totalRows, err := copyModelTables(src, targetDB, batchSize)
	if err != nil {
		return nil, fmt.Errorf("copy tables: %w", err)
	}

	// 4. 标记迁移完成
	if err := markSQLiteMigrationComplete(targetDB); err != nil {
		return nil, fmt.Errorf("mark migration complete: %w", err)
	}

	duration := time.Since(started).Milliseconds()
	return &DatabaseMigrationResult{
		Success:    true,
		TotalRows:  totalRows,
		TableRows:  tableRows,
		DurationMS: duration,
		Message:    fmt.Sprintf("成功迁移 %d 条记录至 PostgreSQL", totalRows),
	}, nil
}

// MaskDSN masks the password in a connection string for safe API responses.
func MaskDSN(rawDSN string) string {
	rawDSN = strings.TrimSpace(rawDSN)
	if rawDSN == "" {
		return ""
	}
	if u, err := url.Parse(rawDSN); err == nil && u.User != nil {
		if pass, hasPassword := u.User.Password(); hasPassword && pass != "" {
			rawUserPass := u.User.String()
			user := u.User.Username()
			maskedUserPass := user + ":******"
			return strings.Replace(rawDSN, rawUserPass+"@", maskedUserPass+"@", 1)
		}
	}
	// Fallback for keyword-style DSN (e.g. host=... password=...)
	if strings.Contains(rawDSN, "password=") {
		parts := strings.Fields(rawDSN)
		for i, p := range parts {
			if strings.HasPrefix(p, "password=") {
				parts[i] = "password=******"
			}
		}
		return strings.Join(parts, " ")
	}
	return rawDSN
}
