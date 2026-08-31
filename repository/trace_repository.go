// Package repository 数据访问层。
package repository

import (
	"errors"
	"fmt"
	"time"

	"github.com/example/tracepulse/config"
	"github.com/example/tracepulse/logger"
	"github.com/example/tracepulse/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// TraceRepository 链路读写接口。
type TraceRepository interface {
	CreateTrace(trace *model.Trace) error
	UpdateTrace(trace *model.Trace) error
	GetTraceByID(traceID string) (*model.Trace, error)
	ListTraces(filter model.TraceFilter) ([]model.Trace, int64, error)
	ListURLStats(filter model.URLStatsFilter) ([]model.URLStatRow, error)
	DeleteTracesBefore(cutoff time.Time) (int64, error)

	CreateEvent(event *model.TraceEvent) error
	CreateEvents(events []model.TraceEvent) error
	GetEventsByTraceID(traceID string) ([]model.TraceEvent, error)
	DeleteEventsBefore(cutoff time.Time) (int64, error)
	DeleteOrphanEvents(gracePeriod time.Duration) (int64, error)

	Vacuum(deletedRows int64) error
}

type traceRepository struct {
	db *gorm.DB
}

func NewTraceRepository(db *gorm.DB) TraceRepository {
	return &traceRepository{db: db}
}

// ---------------------------------------------------------------- traces ----

func (r *traceRepository) CreateTrace(trace *model.Trace) error {
	err := r.db.Create(trace).Error
	if err != nil {
		logger.Error("create trace failed",
			zap.String("trace_id", trace.TraceID), zap.Error(err))
		return err
	}
	logger.Debug("trace persisted",
		zap.String("trace_id", trace.TraceID),
		zap.String("service", trace.ServiceName),
		zap.String("status", trace.Status),
	)
	return nil
}

func (r *traceRepository) UpdateTrace(trace *model.Trace) error {
	err := r.db.Save(trace).Error
	if err != nil {
		logger.Error("update trace failed",
			zap.String("trace_id", trace.TraceID), zap.Error(err))
		return err
	}
	logger.Debug("trace updated",
		zap.String("trace_id", trace.TraceID),
		zap.Int("event_count", trace.EventCount),
		zap.String("status", trace.Status),
	)
	return nil
}

func (r *traceRepository) GetTraceByID(traceID string) (*model.Trace, error) {
	var trace model.Trace
	err := r.db.Where("trace_id = ?", traceID).First(&trace).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		logger.Debug("trace not found in db", zap.String("trace_id", traceID))
		return nil, nil
	}
	if err != nil {
		logger.Error("get trace by id failed",
			zap.String("trace_id", traceID), zap.Error(err))
		return nil, err
	}
	return &trace, nil
}

func (r *traceRepository) DeleteTracesBefore(cutoff time.Time) (int64, error) {
	result := r.db.Where("start_time < ?", cutoff).Delete(&model.Trace{})
	if result.Error != nil {
		logger.Error("delete traces before failed",
			zap.Time("cutoff", cutoff), zap.Error(result.Error))
		return 0, result.Error
	}
	logger.Debug("traces deleted by cleanup",
		zap.Int64("rows", result.RowsAffected),
		zap.Time("cutoff", cutoff),
	)
	return result.RowsAffected, nil
}

// ListTraces 多条件过滤 + 分页。
//
// 实现要点：
//  1. 过滤与分页使用两条独立构造的查询，避免 GORM Statement 复用造成的条件串味；
//  2. level / module 这类"链路中是否出现过"的条件用 EXISTS 子查询表达，
//     既避免了 JOIN 产生的重复行，也不再需要 DISTINCT（DISTINCT 会让 GORM 只
//     选出 traces.id，导致其余字段扫不出来）；
//  3. 默认按 start_time 倒序，命中 idx_traces_start_time，翻页走索引而不是全表。
func (r *traceRepository) ListTraces(filter model.TraceFilter) ([]model.Trace, int64, error) {
	filter.Normalize()

	var total int64
	if err := applyTraceFilters(r.db.Model(&model.Trace{}), filter).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var traces []model.Trace
	err := applyTraceFilters(r.db.Model(&model.Trace{}), filter).
		Order("traces.start_time DESC").
		Offset((filter.Page - 1) * filter.PageSize).
		Limit(filter.PageSize).
		Find(&traces).Error
	if err != nil {
		return nil, 0, err
	}

	return traces, total, nil
}

// ListURLStats 按 URL 分组统计调用次数、错误数、耗时等。
//
// 实现要点：
//  1. 一个 trace_id 算一次调用（GROUP BY service_name, url 即可）；
//  2. 只统计 url 非空的链路 —— 纯内部链路没上报业务入口，统计出来没有意义；
//  3. COALESCE 包裹所有聚合列，避免空结果集扫出来 NULL 被 GORM 映射成零值；
//  4. 结果按 call_count DESC 排序，热点 URL 自然排在最前，排查时一眼看到高频接口。
func (r *traceRepository) ListURLStats(filter model.URLStatsFilter) ([]model.URLStatRow, error) {
	var rows []model.URLStatRow

	q := r.db.Model(&model.Trace{}).
		Select(`
			service_name AS service,
			url,
			COUNT(*) AS call_count,
			COALESCE(SUM(CASE WHEN has_error = 1 THEN 1 ELSE 0 END), 0) AS error_count,
			COALESCE(CAST(AVG(duration_ms) AS INTEGER), 0) AS avg_duration_ms,
			COALESCE(MAX(duration_ms), 0) AS max_duration_ms,
			MAX(start_time) AS last_time
		`).
		Where("url != ''").
		Group("service_name, url").
		Order("call_count DESC")

	// 三种过滤条件按需拼接。GORM 的条件是延迟求值的，Where 链的顺序不影响 SQL 语义。
	if filter.Service != "" {
		q = q.Where("service_name = ?", filter.Service)
	}
	if !filter.StartTime.IsZero() {
		q = q.Where("start_time >= ?", filter.StartTime)
	}
	if !filter.EndTime.IsZero() {
		q = q.Where("start_time <= ?", filter.EndTime)
	}

	// Debug 级别把完整 SQL 和绑定参数打出来，方便排查"为什么没数据"或"为什么多了数据"。
	logger.Debug("list url stats building query",
		zap.String("service", filter.Service),
		zap.Time("start_time", filter.StartTime),
		zap.Time("end_time", filter.EndTime),
		zap.String("sql", q.Statement.SQL.String()),
		zap.Any("vars", q.Statement.Vars),
	)

	err := q.Scan(&rows).Error
	if err != nil {
		logger.Error("list url stats failed",
			zap.String("service", filter.Service),
			zap.Time("start_time", filter.StartTime),
			zap.Time("end_time", filter.EndTime),
			zap.Error(err))
		return nil, err
	}

	logger.Debug("url stats queried",
		zap.Int("count", len(rows)),
		zap.String("service", filter.Service),
		zap.Time("start_time", filter.StartTime),
		zap.Time("end_time", filter.EndTime),
	)
	return rows, nil
}

// applyTraceFilters 把非零过滤条件拼到查询上。
func applyTraceFilters(q *gorm.DB, f model.TraceFilter) *gorm.DB {
	if f.TraceID != "" {
		q = q.Where("traces.trace_id = ?", f.TraceID)
	}
	if f.Service != "" {
		q = q.Where("traces.service_name = ?", f.Service)
	}
	if f.Status != "" {
		q = q.Where("traces.status = ?", f.Status)
	}
	if f.HasError != nil {
		q = q.Where("traces.has_error = ?", *f.HasError)
	}
	if f.MinDurationMs > 0 {
		q = q.Where("traces.duration_ms >= ?", f.MinDurationMs)
	}
	if !f.StartTime.IsZero() {
		q = q.Where("traces.start_time >= ?", f.StartTime)
	}
	if !f.EndTime.IsZero() {
		q = q.Where("traces.start_time <= ?", f.EndTime)
	}

	// 事件维度过滤：链路中只要存在一条匹配的事件即算命中。
	if f.Level != "" {
		q = q.Where(`EXISTS (SELECT 1 FROM trace_events e WHERE e.trace_id = traces.trace_id AND e.level = ?)`, f.Level)
	}
	if f.Module != "" {
		q = q.Where(`EXISTS (SELECT 1 FROM trace_events e WHERE e.trace_id = traces.trace_id AND e.module = ?)`, f.Module)
	}

	if f.Keyword != "" {
		like := "%" + f.Keyword + "%"
		q = q.Where(`
			traces.trace_id LIKE ?
			OR traces.service_name LIKE ?
			OR traces.error_message LIKE ?
			OR EXISTS (
				SELECT 1 FROM trace_events e
				WHERE e.trace_id = traces.trace_id
				  AND (e.event LIKE ? OR e.message LIKE ? OR e.error_message LIKE ? OR e.params LIKE ?)
			)`, like, like, like, like, like, like, like)
	}

	return q
}

// ----------------------------------------------------------- trace_events ----

func (r *traceRepository) CreateEvent(event *model.TraceEvent) error {
	return r.db.Create(event).Error
}

// CreateEvents 批量写入事件，分批放进事务，失败整批回滚。
func (r *traceRepository) CreateEvents(events []model.TraceEvent) error {
	if len(events) == 0 {
		return nil
	}

	start := time.Now()
	const chunk = 500
	err := r.db.Transaction(func(tx *gorm.DB) error {
		for start := 0; start < len(events); start += chunk {
			end := start + chunk
			if end > len(events) {
				end = len(events)
			}
			batch := events[start:end]
			if err := tx.Create(&batch).Error; err != nil {
				return fmt.Errorf("insert events [%d:%d]: %w", start, end, err)
			}
		}
		return nil
	})
	if err != nil {
		logger.Error("create events failed",
			zap.Int("count", len(events)), zap.Error(err))
		return err
	}
	logger.Debug("trace events persisted",
		zap.Int("count", len(events)),
		zap.Duration("elapsed", time.Since(start)),
	)
	return nil
}

func (r *traceRepository) GetEventsByTraceID(traceID string) ([]model.TraceEvent, error) {
	var events []model.TraceEvent
	err := r.db.Where("trace_id = ?", traceID).
		Order("timestamp ASC, id ASC").
		Find(&events).Error
	if err != nil {
		logger.Error("get events by trace failed",
			zap.String("trace_id", traceID), zap.Error(err))
		return nil, err
	}
	logger.Debug("trace events fetched",
		zap.String("trace_id", traceID),
		zap.Int("count", len(events)),
	)
	return events, nil
}

// DeleteEventsBefore 按写入时间清理事件（服务端保留策略，与客户端时钟是否准确无关）。
func (r *traceRepository) DeleteEventsBefore(cutoff time.Time) (int64, error) {
	result := r.db.Where("created_at < ?", cutoff).Delete(&model.TraceEvent{})
	if result.Error != nil {
		logger.Error("delete events before failed",
			zap.Time("cutoff", cutoff), zap.Error(result.Error))
		return 0, result.Error
	}
	logger.Debug("events deleted by cleanup",
		zap.Int64("rows", result.RowsAffected),
		zap.Time("cutoff", cutoff),
	)
	return result.RowsAffected, nil
}

// DeleteOrphanEvents 清理父链路已不存在的孤儿事件。
//
// 为什么按"无父链路"而不是按时间判定：链路是按 start_time 判过期删除的，但事件
// 可能因为跨 TTL 分批落盘而写入得更晚，单靠 created_at 清不干净。孤儿事件既查不到
// 又占空间，父链路没了它就是垃圾。
//
// gracePeriod 是并发安全窗口：落盘是先建 trace 再插事件，若清理恰好卡在这两步之间，
// 会把刚要写入的事件误删。只清理宽限期之前创建的事件即可规避。
func (r *traceRepository) DeleteOrphanEvents(gracePeriod time.Duration) (int64, error) {
	if gracePeriod < 0 {
		gracePeriod = 0
	}
	result := r.db.Where("created_at < ?", time.Now().Add(-gracePeriod)).
		Where("trace_id NOT IN (?)", r.db.Model(&model.Trace{}).Select("trace_id")).
		Delete(&model.TraceEvent{})
	if result.Error != nil {
		logger.Error("delete orphan events failed",
			zap.Duration("grace_period", gracePeriod), zap.Error(result.Error))
		return 0, result.Error
	}
	logger.Debug("orphan events deleted",
		zap.Int64("rows", result.RowsAffected),
		zap.Duration("grace_period", gracePeriod),
	)
	return result.RowsAffected, nil
}

// ----------------------------------------------------------------- misc ----

func (r *traceRepository) Vacuum(deletedRows int64) error {
	err := config.VacuumDatabase(r.db, deletedRows)
	if err != nil {
		logger.Error("database vacuum failed",
			zap.Int64("deleted_rows", deletedRows), zap.Error(err))
		return err
	}
	logger.Debug("database vacuum completed",
		zap.Int64("deleted_rows", deletedRows),
	)
	return nil
}