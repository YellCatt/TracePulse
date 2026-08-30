package service

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/example/tracepulse/config"
	"github.com/example/tracepulse/model"
	"github.com/example/tracepulse/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// newTestDB 在临时目录里建一个真实 SQLite 库，走与生产完全相同的表结构。
// 返回的 close 必须在测试结束时调用，否则 Windows 上文件句柄不释放，TempDir 清不掉。
func newTestDB(t *testing.T) (db *gorm.DB, closeDB func()) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.Trace{}, &model.TraceEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("underlying sql.DB: %v", err)
	}
	return db, func() { _ = sqlDB.Close() }
}

// newTestService 组装一个不依赖全局配置的 trace service，供测试直接使用。
// ndjson 兜底指向临时文件，避免污染测试输出。
func newTestService(t *testing.T, cfg config.TraceConfig) (TraceService, *fakeAlert, func()) {
	t.Helper()

	db, closeDB := newTestDB(t)
	alert := newFakeAlert()
	cfg.NDJSONPath = filepath.Join(t.TempDir(), "fallback.ndjson")
	cfg.NDJSONMaxMB = 1
	if cfg.MaxEventsPerTrace <= 0 {
		cfg.MaxEventsPerTrace = 100
	}

	svc := NewTraceService(repository.NewTraceRepository(db), alert, cfg)
	return svc, alert, closeDB
}

// fakeAlert 记录告警调用，替代真实 SMTP。
type fakeAlert struct {
	mu     chan struct{}
	traces []string
	drops  int
}

func newFakeAlert() *fakeAlert {
	return &fakeAlert{mu: make(chan struct{}, 1)}
}

func (f *fakeAlert) lock()   { f.mu <- struct{}{} }
func (f *fakeAlert) unlock() { <-f.mu }

func (f *fakeAlert) AlertOnTrace(trace *model.Trace, events []model.TraceEvent) {
	f.lock()
	defer f.unlock()
	f.traces = append(f.traces, trace.TraceID)
}

func (f *fakeAlert) AlertOnQueueDrop(droppedCount int) {
	f.lock()
	defer f.unlock()
	f.drops += droppedCount
}

func (f *fakeAlert) Shutdown() {}

func (f *fakeAlert) droppedCount() int {
	f.lock()
	defer f.unlock()
	return f.drops
}

// blockingRepo 包装真实仓库，让落盘卡在 gate 上。
//
// 用途：把"队列被打满"这件事变成确定性事件。否则消费协程可能跑得比 200 次
// ReportEvents 还快，队列始终不满，测试就变成了时好时坏的 flaky test。
type blockingRepo struct {
	repository.TraceRepository

	gate    chan struct{}
	entered chan struct{} // 首次进入落盘时关闭，测试据此确认消费协程已被卡住

	mu       sync.Mutex
	blockAll bool
	once     sync.Once
}

func (b *blockingRepo) setBlockAll(v bool) {
	b.mu.Lock()
	b.blockAll = v
	b.mu.Unlock()
}

func (b *blockingRepo) isBlocked() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.blockAll
}

func (b *blockingRepo) GetTraceByID(id string) (*model.Trace, error) {
	if b.isBlocked() {
		b.once.Do(func() { close(b.entered) })
		<-b.gate
	}
	return b.TraceRepository.GetTraceByID(id)
}

// blockingTraceService 构造一个落盘会被阻塞住的 trace service。
func blockingTraceService(t *testing.T, cfg config.TraceConfig) (TraceService, *fakeAlert, *blockingRepo, func()) {
	t.Helper()

	db, closeDB := newTestDB(t)
	alert := newFakeAlert()
	cfg.NDJSONPath = filepath.Join(t.TempDir(), "fallback.ndjson")
	cfg.NDJSONMaxMB = 1
	if cfg.MaxEventsPerTrace <= 0 {
		cfg.MaxEventsPerTrace = 100
	}
	repo := &blockingRepo{
		TraceRepository: repository.NewTraceRepository(db),
		gate:            make(chan struct{}),
		entered:         make(chan struct{}),
		blockAll:        true,
	}
	return NewTraceService(repo, alert, cfg), alert, repo, closeDB
}

// TestShutdownFlushesInMemoryTraces 验证优雅关闭不丢数据：
// 上报后链路还停留在内存（未到 flush 时机），Shutdown 必须把它落库。
func TestShutdownFlushesInMemoryTraces(t *testing.T) {
	// TTL 足够长 + FlushMs 足够久，确保数据此刻只存在于内存中。
	svc, _, closeDB := newTestService(t, config.TraceConfig{
		QueueSize:   100,
		TTLSeconds:  3600,
		FlushBatch:  1000,
		FlushMs:     100000,
		CleanupDays: 7,
	})
	defer closeDB()

	if err := svc.ReportEvents([]model.TraceEvent{
		{TraceID: "t-1", Timestamp: time.Now(), Level: model.LevelInfo, Module: "m", Event: "start"},
		{TraceID: "t-1", Timestamp: time.Now(), Level: model.LevelError, Module: "m", Event: "boom", ErrorMessage: "kaput"},
	}, ""); err != nil {
		t.Fatalf("report: %v", err)
	}

	// 关闭前查库：应当还没有任何数据。
	if tr, _, err := svc.GetTrace("t-1"); err != nil || tr != nil {
		t.Fatalf("expected trace only in memory before shutdown, got trace=%v err=%v", tr, err)
	}

	svc.Shutdown()

	trace, events, err := svc.GetTrace("t-1")
	if err != nil {
		t.Fatalf("get trace after shutdown: %v", err)
	}
	if trace == nil {
		t.Fatal("trace was lost on shutdown: graceful shutdown must flush in-memory traces")
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events persisted, got %d", len(events))
	}
	if trace.Status != model.TraceStatusError || !trace.HasError {
		t.Fatalf("expected error status, got status=%q hasError=%v", trace.Status, trace.HasError)
	}
}

// TestShutdownIsIdempotent 重复调用 Shutdown 不应 panic，且关闭后拒绝新上报。
func TestShutdownIsIdempotent(t *testing.T) {
	svc, _, closeDB := newTestService(t, config.TraceConfig{
		QueueSize: 10, TTLSeconds: 3600, FlushBatch: 1000, FlushMs: 100000,
		CleanupDays: 7,
	})
	defer closeDB()

	svc.Shutdown()
	svc.Shutdown()

	if err := svc.ReportEvents([]model.TraceEvent{{TraceID: "t-x", Event: "e"}}, ""); err == nil {
		t.Fatal("expected error when reporting after shutdown")
	}
}

// TestReportURLIsPersistedOnTrace url 是排查时定位业务入口的线索，必须跟着事件
// 穿过异步队列落到 traces.url 上，而不是丢在半路。
func TestReportURLIsPersistedOnTrace(t *testing.T) {
	svc, _, closeDB := newTestService(t, config.TraceConfig{
		QueueSize: 100, TTLSeconds: 3600, FlushBatch: 1000, FlushMs: 100000, CleanupDays: 7,
	})
	defer closeDB()

	const want = "https://shop.example.com/order/confirm?sku=A-01"
	if err := svc.ReportEvents([]model.TraceEvent{
		{TraceID: "t-url", Timestamp: time.Now(), Level: model.LevelInfo, Module: "order", Event: "start"},
	}, want); err != nil {
		t.Fatalf("report: %v", err)
	}

	svc.Shutdown() // 立刻落库，避免测试依赖 flush 时序

	trace, _, err := svc.GetTrace("t-url")
	if err != nil {
		t.Fatalf("get trace: %v", err)
	}
	if trace == nil {
		t.Fatal("trace was not persisted")
	}
	if trace.URL != want {
		t.Fatalf("url = %q, want %q", trace.URL, want)
	}
}

// TestReportEventURLWinsOverRequestURL 事件自带 url 时不该被请求级 url 覆盖 ——
// 否则同一批里不同入口的链路会串成同一个地址；没带的则用请求级 url 兜底。
func TestReportEventURLWinsOverRequestURL(t *testing.T) {
	svc, _, closeDB := newTestService(t, config.TraceConfig{
		QueueSize: 100, TTLSeconds: 3600, FlushBatch: 1000, FlushMs: 100000, CleanupDays: 7,
	})
	defer closeDB()

	if err := svc.ReportEvents([]model.TraceEvent{
		{
			TraceID: "t-own", Timestamp: time.Now(), Level: model.LevelInfo,
			Module: "m", Event: "start", URL: "https://a.example.com/own",
		},
		{
			TraceID: "t-fallback", Timestamp: time.Now(), Level: model.LevelInfo,
			Module: "m", Event: "start",
		},
	}, "https://fallback.example.com/req"); err != nil {
		t.Fatalf("report: %v", err)
	}

	svc.Shutdown()

	for _, tc := range []struct{ id, want string }{
		{"t-own", "https://a.example.com/own"},
		{"t-fallback", "https://fallback.example.com/req"},
	} {
		trace, _, err := svc.GetTrace(tc.id)
		if err != nil {
			t.Fatalf("get trace %s: %v", tc.id, err)
		}
		if trace == nil {
			t.Fatalf("trace %s was not persisted", tc.id)
		}
		if trace.URL != tc.want {
			t.Fatalf("trace %s url = %q, want %q", tc.id, trace.URL, tc.want)
		}
	}
}

// TestQueueFullDropsWithoutBlocking 队列满时必须直接丢弃并保持非阻塞，
// 绝不能把采集端堵住 —— 这是"不拖垮业务"这条约束的核心。
func TestQueueFullDropsWithoutBlocking(t *testing.T) {
	// 落盘被 gate 卡住，确保队列一定会被打满，避免测试变成 flaky。
	svc, alert, repo, closeDB := blockingTraceService(t, config.TraceConfig{
		QueueSize:   5,
		TTLSeconds:  3600, // TTL 很长，保证事件不会被 TTL 消费掉
		FlushBatch:  1,    // 每来一条就落盘，确保很快触发一次 flush
		FlushMs:     5,    // flush 定时器要够快，测试不能干等
		CleanupDays: 7,
	})
	defer closeDB()
	// 先放行再关闭，否则 Shutdown 会一直等在这个 gate 上。
	defer func() {
		repo.setBlockAll(false)
		close(repo.gate)
		svc.Shutdown()
	}()

	// 先投一条把消费协程引入落盘流程，等它卡在 gate 上再灌量。
	// 这样"队列被打满"是确定性的，而不是赌消费者够不够慢。
	if err := svc.ReportEvents([]model.TraceEvent{
		{TraceID: "t-trigger", Timestamp: time.Now(), Level: model.LevelInfo, Module: "m", Event: "tick"},
	}, ""); err != nil {
		t.Fatalf("report trigger: %v", err)
	}
	select {
	case <-repo.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer never reached the flush path, cannot construct a full queue")
	}

	batch := make([]model.TraceEvent, 0, 200)
	for i := 0; i < 200; i++ {
		batch = append(batch, model.TraceEvent{
			TraceID:   "t-load",
			Timestamp: time.Now(),
			Level:     model.LevelInfo,
			Module:    "m",
			Event:     "tick",
		})
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- svc.ReportEvents(batch, "") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("report should not fail when queue is full: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReportEvents blocked when queue was full")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ReportEvents took %v, expected non-blocking", elapsed)
	}

	if svc.Stats().DroppedTotal == 0 {
		t.Fatal("expected dropped events when queue overflows")
	}
	if alert.droppedCount() == 0 {
		t.Fatal("expected queue drop alert to be triggered")
	}
	// 队列容量之外的事件必须全部被丢弃，不能凭空多出容量。
	if got := svc.Stats().DroppedTotal; got < 195 {
		t.Fatalf("dropped %d events, want at least 195 (queue cap 5 of 200)", got)
	}
}

// TestTTLForcesFlushOfStalledTrace 客户端漏发 end 事件时，TTL 到期必须强制落盘，
// 否则内存里的活跃 trace 会永久泄漏。
func TestTTLForcesFlushOfStalledTrace(t *testing.T) {
	svc, _, closeDB := newTestService(t, config.TraceConfig{
		QueueSize:         100,
		TTLSeconds:        1,
		FlushBatch:        100000,
		FlushMs:           100000,
		CleanupDays:       7,
		MaxEventsPerTrace: 100,
	})
	defer closeDB()
	defer svc.Shutdown()

	// 时间戳故意设为 10 秒前，让 TTL 立刻判定为过期。
	stale := time.Now().Add(-10 * time.Second)
	if err := svc.ReportEvents([]model.TraceEvent{
		{TraceID: "t-stalled", Timestamp: stale, Level: model.LevelInfo, Module: "m", Event: "start"},
	}, ""); err != nil {
		t.Fatalf("report: %v", err)
	}

	var (
		trace *model.Trace
		err   error
	)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		trace, _, err = svc.GetTrace("t-stalled")
		if err == nil && trace != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if err != nil {
		t.Fatalf("get trace: %v", err)
	}
	if trace == nil {
		t.Fatal("stalled trace was never flushed: TTL sweep is broken, this leaks memory")
	}
	if trace.Status != model.TraceStatusTimeout {
		t.Fatalf("expected timeout status, got %q", trace.Status)
	}
}

// TestListTracesFilters 覆盖检索的主要路径：状态过滤、模块过滤（EXISTS 子查询）、
// 关键词模糊搜索、慢调用阈值、时间范围、分页。
func TestListTracesFilters(t *testing.T) {
	svc, _, closeDB := newTestService(t, config.TraceConfig{
		QueueSize: 100, TTLSeconds: 3600, FlushBatch: 1, FlushMs: 10,
		CleanupDays: 7, MaxEventsPerTrace: 100,
	})
	defer closeDB()

	base := time.Now().Add(-time.Hour)
	mk := func(id, level, module, event string, dur time.Duration) []model.TraceEvent {
		return []model.TraceEvent{
			{TraceID: id, Timestamp: base, Level: model.LevelInfo, Module: module, Event: "start"},
			{TraceID: id, Timestamp: base.Add(dur), Level: level, Module: module, Event: event,
				Message: "payment declined for order " + id},
			{TraceID: id, Timestamp: base.Add(dur), Level: model.LevelInfo, Module: module, Event: model.EventEnd},
		}
	}

	for _, evs := range [][]model.TraceEvent{
		mk("t-ok", model.LevelInfo, "order", "done", 50*time.Millisecond),
		mk("t-err", model.LevelError, "payment", "pay_failed", 1500*time.Millisecond),
		mk("t-warn", model.LevelWarn, "inventory", "low_stock", 80*time.Millisecond),
	} {
		if err := svc.ReportEvents(evs, ""); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	svc.Shutdown() // 立刻落库，避免测试依赖 flush 时序

	// 状态过滤
	res, err := svc.ListTraces(model.TraceFilter{Status: model.TraceStatusError})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 1 || res.Traces[0].TraceID != "t-err" {
		t.Fatalf("status filter wrong: total=%d", res.Total)
	}

	// 模块过滤走 EXISTS 子查询，且不能产生重复行
	res, err = svc.ListTraces(model.TraceFilter{Module: "payment"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 1 || res.Traces[0].TraceID != "t-err" {
		t.Fatalf("module filter wrong: total=%d", res.Total)
	}

	// 关键词命中事件消息（不在 traces 表里的字段）
	res, err = svc.ListTraces(model.TraceFilter{Keyword: "declined"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 3 {
		t.Fatalf("keyword search across events wrong: total=%d, want 3", res.Total)
	}

	// 慢调用阈值：只有 1500ms 的那条满足
	res, err = svc.ListTraces(model.TraceFilter{MinDurationMs: 1000})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 1 || res.Traces[0].TraceID != "t-err" {
		t.Fatalf("min duration filter wrong: total=%d", res.Total)
	}

	// 分页
	res, err = svc.ListTraces(model.TraceFilter{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 3 || len(res.Traces) != 2 || res.TotalPages != 2 {
		t.Fatalf("pagination wrong: total=%d rows=%d pages=%d", res.Total, len(res.Traces), res.TotalPages)
	}

	// 时间范围：结束时间早于所有数据，应无结果
	res, err = svc.ListTraces(model.TraceFilter{EndTime: base.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.Total != 0 {
		t.Fatalf("time range filter wrong: total=%d, want 0", res.Total)
	}
}
