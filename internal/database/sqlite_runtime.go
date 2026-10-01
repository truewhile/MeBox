package database

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
)

// sqliteGateHoldLimit 是写闸持有者的最长合法持有时长。语句级写闸在 SQL
// 执行 panic 时 After 回调不会运行，令牌会泄漏并让后续所有写入永久等锁；
// 超过该时长的持有者按泄漏强制回收（60s 内单条写语句远未到，正常写路径
// 不受影响）。
const sqliteGateHoldLimit = 60 * time.Second

func installSQLiteWriteGate(db *gorm.DB) {
	if db == nil {
		return
	}
	const lockedKey = "mebox:sqlite_write_locked"
	gate := newSQLiteWriteGate()
	lock := func(tx *gorm.DB) {
		ctx := context.Background()
		if tx.Statement != nil && tx.Statement.Context != nil {
			ctx = tx.Statement.Context
		}
		holder, err := gate.Lock(ctx)
		if err != nil {
			_ = tx.AddError(err)
			return
		}
		tx.InstanceSet(lockedKey, holder)
	}
	unlock := func(tx *gorm.DB) {
		if holder, ok := tx.InstanceGet(lockedKey); ok {
			if h, ok := holder.(*sqliteGateHolder); ok {
				gate.Unlock(h)
			}
		}
	}
	rawLock := func(tx *gorm.DB) {
		if tx.Statement != nil && isReadOnlySQL(tx.Statement.SQL.String()) {
			return
		}
		lock(tx)
	}
	_ = db.Callback().Create().Before("gorm:create").Register("mebox:sqlite_write_lock", lock)
	_ = db.Callback().Create().After("gorm:create").Register("mebox:sqlite_write_unlock", unlock)
	_ = db.Callback().Update().Before("gorm:update").Register("mebox:sqlite_write_lock", lock)
	_ = db.Callback().Update().After("gorm:update").Register("mebox:sqlite_write_unlock", unlock)
	_ = db.Callback().Delete().Before("gorm:delete").Register("mebox:sqlite_write_lock", lock)
	_ = db.Callback().Delete().After("gorm:delete").Register("mebox:sqlite_write_unlock", unlock)
	_ = db.Callback().Raw().Before("gorm:raw").Register("mebox:sqlite_write_lock", rawLock)
	_ = db.Callback().Raw().After("gorm:raw").Register("mebox:sqlite_write_unlock", unlock)
}

func isReadOnlySQL(sql string) bool {
	trimmed := strings.TrimSpace(sql)
	if len(trimmed) == 0 {
		return false
	}
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "EXPLAIN") {
		return true
	}
	if strings.HasPrefix(upper, "WITH") && !strings.Contains(upper, "INSERT") && !strings.Contains(upper, "UPDATE") && !strings.Contains(upper, "DELETE") {
		return true
	}
	return false
}

// sqliteWriteGate serializes in-process SQLite writes. 所有权令牌（而非裸
// 信号量）保证只有持有者本人能释放；持有超时按泄漏自动回收，避免一次
// panic 让进程的 SQLite 写入半永久性瘫痪。
type sqliteWriteGate struct {
	mu    sync.Mutex
	cond  *sync.Cond
	owner *sqliteGateHolder
}

type sqliteGateHolder struct {
	id       uint64
	acquired time.Time
}

var sqliteGateHolderSeq atomic.Uint64

func newSQLiteWriteGate() *sqliteWriteGate {
	g := &sqliteWriteGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *sqliteWriteGate) Lock(ctx context.Context) (*sqliteGateHolder, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	// ctx 取消时唤醒等待者（cond 无法感知 ctx，用旁路 goroutine 广播）。
	if done := ctx.Done(); done != nil {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-done:
				g.cond.Broadcast()
			case <-stop:
			}
		}()
	}
	for {
		if g.owner == nil {
			holder := &sqliteGateHolder{
				id:       sqliteGateHolderSeq.Add(1),
				acquired: time.Now(),
			}
			g.owner = holder
			return holder, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Since(g.owner.acquired) > sqliteGateHoldLimit {
			// 持有者疑似 panic 泄漏（After 回调未执行）：强制回收。
			g.owner = nil
			g.cond.Broadcast()
			continue
		}
		g.cond.Wait()
	}
}

func (g *sqliteWriteGate) Unlock(h *sqliteGateHolder) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if h == nil || g.owner != h {
		return
	}
	g.owner = nil
	g.cond.Broadcast()
}

func buildSQLiteDSN(cfg *config.Config) string {
	dbPath := cfg.Database.DBPath
	if !filepath.IsAbs(dbPath) {
		// keep as-is to respect user-provided relative paths.
		dbPath = filepath.Clean(dbPath)
	}
	// _txlock=immediate：事务以写锁开始。此前 deferred BEGIN 在并发事务
	// 升级写锁时会绕过 busy_timeout 直接报 SQLITE_BUSY。
	dsn := dbPath + "?_txlock=immediate&_pragma=foreign_keys(1)"
	if cfg.Database.WALMode {
		dsn += "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}
	if cfg.Database.BusyTimeout > 0 {
		dsn += fmt.Sprintf("&_pragma=busy_timeout(%d)", cfg.Database.BusyTimeout)
	}
	if cfg.Database.CacheSize != 0 {
		dsn += fmt.Sprintf("&_pragma=cache_size(%d)", cfg.Database.CacheSize)
	}
	dsn += "&_pragma=temp_store(MEMORY)&_pragma=mmap_size(536870912)"
	if cfg.Database.WALMode {
		dsn += "&_pragma=wal_autocheckpoint(1000)"
	}
	return dsn
}

func isSQLite(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "sqlite"
}

func isPostgres(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres"
}
