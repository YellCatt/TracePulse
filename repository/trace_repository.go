// Package repository 数据访问层。
package repository

import (
	"errors"
	"fmt"
	"time"

	"github.com/example/tracepulse/config"
	"github.com/example/tracepulse/model"
	"gorm.io/gorm"
)

// TraceRepository 链路读写接口。
type TraceRepository interface {
	CreateTrace(trace *model.Trace) error
	UpdateTrace(trace *model.Trace) error
	GetTraceByID(traceID string) (*model.Trace, error)
	ListTraces(filter model.TraceFilter) ([]model.Trace, int64, error)
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
	return r.db.Create(trace).Error
}

func (r *traceRepository) UpdateTrace(trace *model.Trace) error {
	return r.db.Save(trace).Error
}

func (r *traceRepository) GetTraceByID(traceID string) (*model.Trace, error) {
	var trace model.Trace
	err := r.db.Where("trace_id = ?", traceID).First(&trace).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &trace, nil
}

func (r *traceRepository) DeleteTracesBefore(cutoff time.Time) (int64, error) {
	result := r.db.Where("start_time < ?", cutoff).Delete(&model.Trace{})
	return result.RowsAffected, result.Error
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

	const chunk = 500
	return r.db.Transaction(func(tx *gorm.DB) error {
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
}

func (r *traceRepository) GetEventsByTraceID(traceID string) ([]model.TraceEvent, error) {
	var events []model.TraceEvent
	err := r.db.Where("trace_id = ?", traceID).
		Order("timestamp ASC, id ASC").
		Find(&events).Error
	return events, err
}

// DeleteEventsBefore 按写入时间清理事件（服务端保留策略，与客户端时钟是否准确无关）。
func (r *traceRepository) DeleteEventsBefore(cutoff time.Time) (int64, error) {
	result := r.db.Where("created_at < ?", cutoff).Delete(&model.TraceEvent{})
	return result.RowsAffected, result.Error
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
	return result.RowsAffected, result.Error
}

// ----------------------------------------------------------------- misc ----

func (r *traceRepository) Vacuum(deletedRows int64) error {
	return config.VacuumDatabase(r.db, deletedRows)
}
