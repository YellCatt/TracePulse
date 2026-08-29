package config

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/example/gapi/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NewDatabase 打开 SQLite 并建表建索引。
//
// 使用 github.com/glebarez/sqlite（底层 modernc.org/sqlite，纯 Go 无 CGO），
// 因此可以 CGO_ENABLED=0 交叉编译出单文件静态二进制，直接丢到极路由上跑。
func NewDatabase() *gorm.DB {
	db, err := gorm.Open(sqlite.Open(cfg.Database.Path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	applyPragmas(db)

	if err := db.AutoMigrate(&model.User{}, &model.Trace{}, &model.TraceEvent{}); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	if err := ensureIndexes(db); err != nil {
		log.Printf("warning: failed to ensure indexes: %v", err)
	}

	ensureStats(db)

	return db
}

// applyPragmas 设置 SQLite 运行参数。
func applyPragmas(db *gorm.DB) {
	// 默认 WAL：读写并发不互相阻塞，适合"边上报边查询"的场景。
	if mode := strings.ToUpper(strings.TrimSpace(cfg.Database.JournalMode)); mode != "" {
		execPragma(db, "journal_mode = "+mode)
	}
	// 写锁等待，避免并发写入直接抛 "database is locked"。
	execPragma(db, fmt.Sprintf("busy_timeout = %d", cfg.Database.BusyTimeoutMs))
	// U 盘 / SD 卡上 NORMAL 已足够安全，且大幅减少 fsync 次数。
	execPragma(db, fmt.Sprintf("synchronous = %s", cfg.Database.SyncMode))
	// 增量自动回收：删除数据后空闲页可被复用，配合 incremental_vacuum 归还磁盘。
	// 注意：该参数只在建表前对新建库生效，已有库需要一次 VACUUM 才能转换。
	execPragma(db, "auto_vacuum = INCREMENTAL")
	execPragma(db, "foreign_keys = OFF")
	execPragma(db, "temp_store = MEMORY")
}

func execPragma(db *gorm.DB, pragma string) {
	if err := db.Exec("PRAGMA " + pragma).Error; err != nil {
		log.Printf("warning: pragma %q failed: %v", pragma, err)
	}
}

// ensureIndexes 建立检索所需的索引。
//
// 索引设计完全围绕真实查询形态：
//   - 列表页默认按 start_time 倒序翻页 -> (start_time DESC)
//   - 列表页叠加 status / service / has_error 过滤 -> 复合索引把过滤列放在前导列
//   - 详情页按 trace_id 取全部事件并按时间排序 -> (trace_id, timestamp)
//   - module / level 过滤以 EXISTS 子查询命中 -> (trace_id, level) / (trace_id, module)
//   - 过期清理按时间范围批量删除 -> (timestamp) / (created_at)
func ensureIndexes(db *gorm.DB) error {
	indexes := []string{
		// ---- traces ----
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_traces_trace_id ON traces(trace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_traces_start_time ON traces(start_time DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_traces_status_start_time ON traces(status, start_time DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_traces_service_start_time ON traces(service_name, start_time DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_traces_error_start_time ON traces(has_error, start_time DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_traces_created_at ON traces(created_at)`,

		// ---- trace_events ----
		`CREATE INDEX IF NOT EXISTS idx_events_trace_id_timestamp ON trace_events(trace_id, timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_events_trace_id_level ON trace_events(trace_id, level)`,
		`CREATE INDEX IF NOT EXISTS idx_events_trace_id_module ON trace_events(trace_id, module)`,
		`CREATE INDEX IF NOT EXISTS idx_events_level_timestamp ON trace_events(level, timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_events_module_timestamp ON trace_events(module, timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_events_timestamp ON trace_events(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_events_created_at ON trace_events(created_at)`,
	}

	for _, stmt := range indexes {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// ensureStats 首次启动时跑一次 ANALYZE，让查询优化器有统计信息可选对索引。
// sqlite_stat1 非空说明之前已经分析过，跳过以免大库上耗时。
func ensureStats(db *gorm.DB) {
	var n int64
	err := db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`).Scan(&n).Error
	if err != nil || n == 0 {
		if err := db.Exec("ANALYZE").Error; err != nil {
			log.Printf("warning: analyze failed: %v", err)
		}
		return
	}

	var cnt int64
	if err := db.Raw("SELECT count(*) FROM sqlite_stat1").Scan(&cnt).Error; err == nil && cnt == 0 {
		if err := db.Exec("ANALYZE").Error; err != nil {
			log.Printf("warning: analyze failed: %v", err)
		}
	}
}

// VacuumDatabase 回收空闲页。deletedRows 为本次清理删除的行数。
func VacuumDatabase(db *gorm.DB, deletedRows int64) error {
	if db == nil {
		return errors.New("nil db")
	}

	// 增量回收代价极低，每次清理都可以跑。
	if err := db.Exec("PRAGMA incremental_vacuum").Error; err != nil {
		return err
	}

	if cfg.Trace.DisableVacuum || deletedRows <= 0 {
		return nil
	}

	// 库不是增量自动回收模式时，只有完整 VACUUM 才能把空间还给文件系统。
	var mode int
	row := db.Raw("PRAGMA auto_vacuum").Row()
	if err := row.Scan(&mode); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if mode != 0 {
		return nil
	}

	// 完整 VACUUM 会重建整库，仅在确实删掉了数据时执行。
	if err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL").Error; err != nil {
		return err
	}
	return db.Exec("VACUUM").Error
}
