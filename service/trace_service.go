// Package service 链路核心服务。
//
// 数据流向：
//
//	HTTP 上报 ──► 非阻塞队列 ──► 内存聚合（按 trace_id）──► 批量落库 ──► 告警判定
//	                  │                                          │
//	                  └─► ndjson 兜底落盘                    TTL 超时强制落盘
//
// 可靠性约束：
//  1. 上报接口永不阻塞业务线程 —— 队列满直接丢弃并告警，绝不让采集方被拖死；
//  2. 事件先写 ndjson 再入队 —— trace-server 自身挂掉也不会丢事件；
//  3. 内存活跃 trace 有 TTL —— 客户端漏发 end 事件时也要强制落盘，杜绝内存泄漏。
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/example/tracepulse/config"
	"github.com/example/tracepulse/logger"
	"github.com/example/tracepulse/model"
	"github.com/example/tracepulse/repository"
	"go.uber.org/zap"
)

// ReportMeta 一次上报请求携带的链路级元信息。
//
// 单独成结构体而不是摊成 ReportEvents 的参数列表：这类请求级字段还会继续加
// （url / service_name 只是开头），摊平会让调用方每次都跟着改签名，且多个
// 同类型 string 参数相邻时极易传错位置。
type ReportMeta struct {
	// URL 业务入口地址（接口名），记到 traces.url。
	URL string
	// ServiceName 服务名，记到 traces.service_name。为空时退化用事件的 module。
	ServiceName string
}

// TraceService 链路服务接口。
type TraceService interface {
	// ReportEvents 上报一批事件，meta 是这次上报的链路级元信息。
	// 事件自带 url / service_name 时以事件为准，meta 只作兜底。
	ReportEvents(events []model.TraceEvent, meta ReportMeta) error
	GetTrace(traceID string) (*model.Trace, []model.TraceEvent, error)
	ListTraces(filter model.TraceFilter) (*model.TraceListResult, error)
	Stats() TraceStats
	Shutdown()
}

// TraceStats 运行时指标，用于 /health 观测队列水位。
type TraceStats struct {
	QueueLen     int   `json:"queue_len"`
	QueueCap     int   `json:"queue_cap"`
	ActiveTraces int   `json:"active_traces"`
	DroppedTotal int64 `json:"dropped_total"`
	FlushedTotal int64 `json:"flushed_total"`
	ShuttingDown bool  `json:"shutting_down"`
}

// orphanGracePeriod 孤儿事件清理的并发安全窗口。
//
// 落盘顺序是"先建 trace 再插事件"，清理任务若正好卡在这两步之间，会把刚要写入的
// 事件误判成孤儿。留出足够宽限期（远大于单次 flush 耗时）即可规避。
const orphanGracePeriod = 10 * time.Minute

// activeTrace 内存中尚未落盘的一条链路。
type activeTrace struct {
	trace  *model.Trace
	events []model.TraceEvent

	// serviceFromModule 当前 service_name 是否由事件的 module 推导而来。
	//
	// 用来让"显式上报的服务名"盖掉"module 推导值"：长链路分批上报时，可能第一批
	// 还没拿到服务名（只有 module），后续批次才带上。反向则不覆盖 —— 服务名一旦
	// 确定就不该被后来的值反复改写。
	serviceFromModule bool
}

type traceService struct {
	repo     repository.TraceRepository
	alertSvc AlertService
	cfg      config.TraceConfig

	queue  chan model.TraceEvent
	active map[string]*activeTrace
	mu     sync.Mutex

	ndjsonMu   sync.Mutex
	ndjsonFile *os.File
	ndjsonPath string

	shutdown chan struct{}
	stopOnce sync.Once
	closed   atomic.Bool
	wg       sync.WaitGroup

	droppedTotal atomic.Int64
	flushedTotal atomic.Int64
}

func NewTraceService(repo repository.TraceRepository, alertSvc AlertService, cfg config.TraceConfig) TraceService {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1000
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 300
	}
	if cfg.FlushBatch <= 0 {
		cfg.FlushBatch = 200
	}
	if cfg.FlushMs <= 0 {
		cfg.FlushMs = 200
	}
	if cfg.CleanupDays <= 0 {
		cfg.CleanupDays = 7
	}
	if cfg.CleanupIntervalMinutes <= 0 {
		cfg.CleanupIntervalMinutes = 60
	}
	if cfg.MaxEventsPerTrace <= 0 {
		cfg.MaxEventsPerTrace = 5000
	}
	if cfg.NDJSONMaxMB <= 0 {
		cfg.NDJSONMaxMB = 64
	}

	s := &traceService{
		repo:     repo,
		alertSvc: alertSvc,
		cfg:      cfg,
		queue:    make(chan model.TraceEvent, cfg.QueueSize),
		active:   make(map[string]*activeTrace),
		shutdown: make(chan struct{}),
	}

	logger.Debug("trace service initialized",
		zap.Int("queue_size", cfg.QueueSize),
		zap.Int("flush_batch", cfg.FlushBatch),
		zap.Int("flush_ms", cfg.FlushMs),
		zap.Int("ttl_seconds", cfg.TTLSeconds),
		zap.Int("max_events_per_trace", cfg.MaxEventsPerTrace),
		zap.String("ndjson_path", cfg.NDJSONPath),
	)

	s.openNDJSON()

	s.wg.Add(3)
	go s.processLoop()
	go s.ttlLoop()
	go s.cleanupLoop()

	return s
}

// ------------------------------------------------------------------ 上报 ----

// ReportEvents 接收一批事件，meta 里的链路级信息记到链路上。全程非阻塞，队列满则丢弃。
//
// 传递路径：HTTP 层 → 事件携带字段 → 队列 → 内存聚合 → traces。
// 事件自己带了值时以事件为准（这样同一条链路分批上报、或一条链路跨多个入口时
// 都能各自留下线索），meta 只给没带值的事件兜底。
func (s *traceService) ReportEvents(events []model.TraceEvent, meta ReportMeta) error {
	if s.closed.Load() {
		return errors.New("trace service is shutting down")
	}

	dropped := 0
	for i := range events {
		e := events[i]

		if e.TraceID == "" {
			// 没有 trace_id 的事件无法聚合，直接落兜底日志后跳过。
			e.TraceID = "unknown"
		}
		if e.URL == "" {
			e.URL = meta.URL
		}
		if e.ServiceName == "" {
			e.ServiceName = meta.ServiceName
		}
		now := time.Now()
		if e.Timestamp.IsZero() {
			e.Timestamp = now
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		if e.Level == "" {
			e.Level = model.LevelInfo
		}

		// 先落兜底日志，再入队：进程此刻崩溃也不丢。
		s.writeNDJSON(e)

		select {
		case s.queue <- e:
		default:
			dropped++
		}
	}

	if dropped > 0 {
		s.droppedTotal.Add(int64(dropped))
		logger.Warn("trace queue full, dropping events",
			zap.Int("count", dropped),
			zap.Int("queue_len", len(s.queue)),
			zap.Int("queue_cap", cap(s.queue)),
		)
		s.alertSvc.AlertOnQueueDrop(dropped)
	}

	logger.Debug("trace events enqueued",
		zap.Int("accepted", len(events)-dropped),
		zap.Int("dropped", dropped),
		zap.Int("queue_len", len(s.queue)),
	)

	return nil
}

// ------------------------------------------------------------------ 查询 ----

func (s *traceService) GetTrace(traceID string) (*model.Trace, []model.TraceEvent, error) {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return nil, nil, errors.New("trace_id is required")
	}

	trace, err := s.repo.GetTraceByID(traceID)
	if err != nil {
		return nil, nil, err
	}
	if trace == nil {
		return nil, nil, nil
	}

	events, err := s.repo.GetEventsByTraceID(traceID)
	if err != nil {
		return nil, nil, err
	}

	return trace, events, nil
}

func (s *traceService) ListTraces(filter model.TraceFilter) (*model.TraceListResult, error) {
	filter.Normalize()

	traces, total, err := s.repo.ListTraces(filter)
	if err != nil {
		return nil, err
	}

	totalPages := int((total + int64(filter.PageSize) - 1) / int64(filter.PageSize))

	return &model.TraceListResult{
		Total:      total,
		Traces:     traces,
		Page:       filter.Page,
		PageSize:   filter.PageSize,
		TotalPages: totalPages,
	}, nil
}

func (s *traceService) Stats() TraceStats {
	s.mu.Lock()
	active := len(s.active)
	s.mu.Unlock()

	return TraceStats{
		QueueLen:     len(s.queue),
		QueueCap:     cap(s.queue),
		ActiveTraces: active,
		DroppedTotal: s.droppedTotal.Load(),
		FlushedTotal: s.flushedTotal.Load(),
		ShuttingDown: s.closed.Load(),
	}
}

// ------------------------------------------------------------------ 关闭 ----

// Shutdown 优雅关闭：停掉后台协程前先把内存中的数据全部落盘。
func (s *traceService) Shutdown() {
	s.stopOnce.Do(func() {
		s.closed.Store(true)
		logger.Info("trace service shutting down, flushing remaining events...")

		close(s.shutdown)
		// processLoop 会在退出前排空队列并落盘全部活跃 trace。
		s.wg.Wait()

		s.ndjsonMu.Lock()
		if s.ndjsonFile != nil {
			_ = s.ndjsonFile.Sync()
			_ = s.ndjsonFile.Close()
			s.ndjsonFile = nil
		}
		s.ndjsonMu.Unlock()

		logger.Info("trace service shutdown complete")
	})
}

// -------------------------------------------------------------- 处理循环 ----

func (s *traceService) processLoop() {
	defer s.wg.Done()

	flushTimer := time.NewTimer(time.Duration(s.cfg.FlushMs) * time.Millisecond)
	defer flushTimer.Stop()

	for {
		select {
		case <-s.shutdown:
			// 退出前把队列抽干，保证优雅关闭不丢数据。
			for {
				select {
				case e := <-s.queue:
					s.addToActive(e)
				default:
					s.flushAllActive()
					return
				}
			}
		case e := <-s.queue:
			s.addToActive(e)
		case <-flushTimer.C:
			s.flushBatch()
			flushTimer.Reset(time.Duration(s.cfg.FlushMs) * time.Millisecond)
		}
	}
}

// addToActive 把事件并入内存中的链路，并推进聚合状态。
func (s *traceService) addToActive(e model.TraceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	at, exists := s.active[e.TraceID]
	if !exists {
		// 显式服务名优先，缺省才退化用 module —— 老采集端只传 module 时行为不变。
		serviceName := e.ServiceName
		fromModule := false
		if serviceName == "" {
			serviceName = e.Module
			fromModule = true
		}

		at = &activeTrace{
			trace: &model.Trace{
				TraceID:     e.TraceID,
				ServiceName: serviceName,
				Status:      model.TraceStatusOK,
				StartTime:   e.Timestamp,
				HasError:    false,
			},
			events:            make([]model.TraceEvent, 0, 8),
			serviceFromModule: fromModule,
		}
		s.active[e.TraceID] = at
		logger.Debug("active trace created",
			zap.String("trace_id", e.TraceID),
			zap.String("service", serviceName),
			zap.String("module", e.Module),
			zap.String("event", e.Event),
			zap.Int("active_total", len(s.active)),
		)
	}

	// 先到先得：链路的入口地址以首批事件为准，后续批次不覆盖已记录的值。
	if at.trace.URL == "" && e.URL != "" {
		at.trace.URL = e.URL
	}
	// 唯一例外：module 推导出的服务名会被显式上报值替换。
	if at.serviceFromModule && e.ServiceName != "" {
		at.trace.ServiceName = e.ServiceName
		at.serviceFromModule = false
	}

	// 单条链路的事件数封顶，防止异常链路无限写撑爆内存和磁盘。
	if len(at.events) >= s.cfg.MaxEventsPerTrace {
		logger.Warn("trace event cap reached, ignoring event",
			zap.String("trace_id", e.TraceID),
			zap.Int("max_events_per_trace", s.cfg.MaxEventsPerTrace),
		)
		return
	}

	at.events = append(at.events, e)

	if e.Timestamp.Before(at.trace.StartTime) {
		at.trace.StartTime = e.Timestamp
	}
	if e.Timestamp.After(at.trace.EndTime) {
		at.trace.EndTime = e.Timestamp
	}
	at.trace.EventCount = len(at.events)
	at.trace.DurationMs = at.trace.EndTime.Sub(at.trace.StartTime).Milliseconds()

	switch e.Level {
	case model.LevelError, model.LevelFatal:
		at.trace.HasError = true
		at.trace.Status = model.TraceStatusError
		if at.trace.ErrorMessage == "" {
			at.trace.ErrorMessage = firstNonEmpty(e.ErrorMessage, e.Message)
		}
	case model.LevelWarn:
		if !at.trace.HasError {
			at.trace.Status = model.TraceStatusWarn
		}
	}
}

// shouldFlush 收到 end 事件或事件数达到阈值即落盘。
func (s *traceService) shouldFlush(at *activeTrace) bool {
	if len(at.events) == 0 {
		return false
	}
	return at.events[len(at.events)-1].Event == model.EventEnd
}

func (s *traceService) flushBatch() {
	s.mu.Lock()
	var toFlush []*activeTrace
	for id, at := range s.active {
		if len(at.events) >= s.cfg.FlushBatch || s.shouldFlush(at) {
			toFlush = append(toFlush, at)
			delete(s.active, id)
		}
	}
	activeRemaining := len(s.active)
	s.mu.Unlock()

	logger.Debug("trace batch flush",
		zap.Int("count", len(toFlush)),
		zap.Int("active_remaining", activeRemaining),
	)

	for _, at := range toFlush {
		s.flushTrace(at)
	}
}

func (s *traceService) flushAllActive() {
	s.mu.Lock()
	all := make([]*activeTrace, 0, len(s.active))
	for _, at := range s.active {
		all = append(all, at)
	}
	s.active = make(map[string]*activeTrace)
	s.mu.Unlock()

	logger.Debug("flushing all active traces on shutdown", zap.Int("count", len(all)))

	for _, at := range all {
		s.flushTrace(at)
	}
}

// flushTrace 把一条链路写入数据库，并触发告警判定。
func (s *traceService) flushTrace(at *activeTrace) {
	if at == nil || len(at.events) == 0 {
		return
	}

	trace := at.trace
	trace.EventCount = len(at.events)
	if trace.Status == "" {
		trace.Status = model.TraceStatusOK
	}

	existing, err := s.repo.GetTraceByID(trace.TraceID)
	if err != nil {
		logger.Error("failed to check trace before flush",
			zap.String("trace_id", trace.TraceID), zap.Error(err))
		return
	}

	partial := false
	if existing != nil {
		// 长链路被分批落盘过，这里做增量合并。
		partial = true
		existing.EventCount += trace.EventCount
		if trace.StartTime.Before(existing.StartTime) {
			existing.StartTime = trace.StartTime
		}
		if trace.EndTime.After(existing.EndTime) {
			existing.EndTime = trace.EndTime
		}
		existing.DurationMs = existing.EndTime.Sub(existing.StartTime).Milliseconds()

		if trace.HasError {
			existing.HasError = true
			existing.Status = model.TraceStatusError
			if existing.ErrorMessage == "" {
				existing.ErrorMessage = trace.ErrorMessage
			}
		} else if trace.Status == model.TraceStatusWarn && !existing.HasError {
			existing.Status = model.TraceStatusWarn
		}
		if existing.ServiceName == "" {
			existing.ServiceName = trace.ServiceName
		}
		// 分批落盘时只有其中一批带 url，已有值就不动，没有则补齐。
		if existing.URL == "" {
			existing.URL = trace.URL
		}

		if err := s.repo.UpdateTrace(existing); err != nil {
			logger.Error("failed to update trace",
				zap.String("trace_id", trace.TraceID), zap.Error(err))
			return
		}
		trace = existing
	} else {
		if err := s.repo.CreateTrace(trace); err != nil {
			logger.Error("failed to create trace",
				zap.String("trace_id", trace.TraceID), zap.Error(err))
			return
		}
	}

	if err := s.repo.CreateEvents(at.events); err != nil {
		logger.Error("failed to create trace events",
			zap.String("trace_id", trace.TraceID),
			zap.Int("count", len(at.events)),
			zap.Error(err),
		)
		return
	}

	s.flushedTotal.Add(int64(len(at.events)))

	logger.Debug("trace flushed to db",
		zap.String("trace_id", trace.TraceID),
		zap.Int("events", len(at.events)),
		zap.Bool("partial", partial),
		zap.String("status", trace.Status),
	)

	// 告警要带完整链路内容；分批落盘时内存里只有这一段，回库补齐。
	s.maybeAlert(trace, at.events, partial)
}

// maybeAlert 触发告警。partial 为真时先从库里取全量事件。
func (s *traceService) maybeAlert(trace *model.Trace, batch []model.TraceEvent, partial bool) {
	if trace == nil {
		return
	}
	evs := batch
	if partial {
		if all, err := s.repo.GetEventsByTraceID(trace.TraceID); err == nil && len(all) > 0 {
			evs = all
		}
	}
	s.alertSvc.AlertOnTrace(trace, evs)
}

// ------------------------------------------------------------------ TTL ----

// ttlLoop 定期扫描内存中停留过久的链路，强制落盘。
// 这是防止「客户端崩溃没发 end 事件」导致内存泄漏的最后一道防线。
func (s *traceService) ttlLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdown:
			return
		case <-ticker.C:
			s.checkTTL()
		}
	}
}

func (s *traceService) checkTTL() {
	s.mu.Lock()
	now := time.Now()
	ttl := time.Duration(s.cfg.TTLSeconds) * time.Second

	var expired []*activeTrace
	for id, at := range s.active {
		if len(at.events) == 0 {
			continue
		}
		lastEvent := at.events[len(at.events)-1]
		if now.Sub(lastEvent.Timestamp) > ttl {
			at.trace.Status = model.TraceStatusTimeout
			if at.trace.ErrorMessage == "" {
				at.trace.ErrorMessage = fmt.Sprintf(
					"trace timeout: no %q event received within %ds", model.EventEnd, s.cfg.TTLSeconds)
			}
			at.trace.HasError = true
			expired = append(expired, at)
			delete(s.active, id)
		}
	}
	activeTotal := len(s.active)
	s.mu.Unlock()

	logger.Debug("trace TTL scan",
		zap.Int("active_total", activeTotal),
		zap.Int("expired", len(expired)),
	)

	for _, at := range expired {
		logger.Warn("trace TTL expired, forcing flush",
			zap.String("trace_id", at.trace.TraceID),
			zap.Int("ttl_seconds", s.cfg.TTLSeconds),
			zap.Int("events", len(at.events)),
		)
		s.flushTrace(at)
	}
}

// --------------------------------------------------------------- 清理任务 ----

func (s *traceService) cleanupLoop() {
	defer s.wg.Done()

	interval := time.Duration(s.cfg.CleanupIntervalMinutes) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.runCleanup()

	for {
		select {
		case <-s.shutdown:
			return
		case <-ticker.C:
			s.runCleanup()
		}
	}
}

// runCleanup 删除过期数据并回收磁盘空间。
func (s *traceService) runCleanup() {
	cutoff := time.Now().AddDate(0, 0, -s.cfg.CleanupDays)
	logger.Debug("trace cleanup started",
		zap.String("cutoff", cutoff.Format(time.RFC3339)),
		zap.Int("retention_days", s.cfg.CleanupDays),
	)

	eventDeleted, err := s.repo.DeleteEventsBefore(cutoff)
	if err != nil {
		logger.Error("failed to cleanup trace events", zap.Error(err))
	}

	traceDeleted, err := s.repo.DeleteTracesBefore(cutoff)
	if err != nil {
		logger.Error("failed to cleanup traces", zap.Error(err))
	}

	// 链路按 start_time 判过期，事件可能写入更晚，这一步清理残留孤儿。
	orphanDeleted, err := s.repo.DeleteOrphanEvents(orphanGracePeriod)
	if err != nil {
		logger.Error("failed to cleanup orphan trace events", zap.Error(err))
	}

	deleted := eventDeleted + traceDeleted + orphanDeleted
	if deleted > 0 {
		logger.Info("cleaned up expired traces",
			zap.Int64("events", eventDeleted),
			zap.Int64("traces", traceDeleted),
			zap.Int64("orphan_events", orphanDeleted),
			zap.Int("retention_days", s.cfg.CleanupDays),
		)
	}

	if err := s.repo.Vacuum(deleted); err != nil {
		logger.Error("failed to vacuum database", zap.Error(err))
	}
}

// ------------------------------------------------------------ ndjson 兜底 ----

// openNDJSON 打开兜底日志文件；未配置路径时直接写 stdout（容器场景）。
func (s *traceService) openNDJSON() {
	if s.cfg.NDJSONPath == "" {
		return
	}

	if dir := filepath.Dir(s.cfg.NDJSONPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			logger.Warn("failed to create ndjson dir, falling back to stdout",
				zap.String("path", s.cfg.NDJSONPath), zap.Error(err))
			return
		}
	}

	f, err := os.OpenFile(s.cfg.NDJSONPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Warn("failed to open ndjson fallback file, using stdout",
			zap.String("path", s.cfg.NDJSONPath), zap.Error(err))
		return
	}

	s.ndjsonFile = f
	s.ndjsonPath = s.cfg.NDJSONPath
	logger.Debug("ndjson fallback file opened",
		zap.String("path", s.cfg.NDJSONPath),
	)
}

// writeNDJSON 追加一行事件 JSON。文件写失败时降级到 stdout，绝不回传错误影响上报。
func (s *traceService) writeNDJSON(e model.TraceEvent) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	line := append(data, '\n')

	s.ndjsonMu.Lock()
	defer s.ndjsonMu.Unlock()

	if s.ndjsonFile == nil {
		_, _ = os.Stdout.Write(line)
		return
	}

	if _, err := s.ndjsonFile.Write(line); err != nil {
		logger.Error("failed to write ndjson fallback", zap.Error(err))
		return
	}

	// 超过体积阈值就轮转，避免兜底日志本身把 U 盘写满。
	if info, err := s.ndjsonFile.Stat(); err == nil {
		if info.Size() > int64(s.cfg.NDJSONMaxMB)<<20 {
			logger.Debug("ndjson fallback file exceeds size limit, rotating",
				zap.Int64("size_bytes", info.Size()),
				zap.Int("max_mb", s.cfg.NDJSONMaxMB),
			)
			s.rotateNDJSONLocked()
		}
	}
}

// rotateNDJSONLocked 轮转兜底日志文件，保留一个历史文件。
func (s *traceService) rotateNDJSONLocked() {
	_ = s.ndjsonFile.Close()

	backup := s.ndjsonPath + ".1"
	_ = os.Remove(backup)
	_ = os.Rename(s.ndjsonPath, backup)

	f, err := os.OpenFile(s.ndjsonPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Error("failed to reopen ndjson file after rotation", zap.Error(err))
		s.ndjsonFile = nil
		return
	}
	s.ndjsonFile = f
	logger.Info("ndjson fallback file rotated", zap.String("path", s.ndjsonPath))
}

// ------------------------------------------------------------------ 工具 ----

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
