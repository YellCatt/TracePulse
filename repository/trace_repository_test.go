package repository

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/example/tracepulse/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newTestRepo(t *testing.T) (TraceRepository, func()) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "repo.db")), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Trace{}, &model.TraceEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	return NewTraceRepository(db), func() { _ = sqlDB.Close() }
}

func seedTrace(t *testing.T, repo TraceRepository, id, status string, start time.Time, events []model.TraceEvent) {
	t.Helper()
	tr := &model.Trace{
		TraceID:    id,
		Status:     status,
		StartTime:  start,
		EndTime:    start.Add(time.Second),
		DurationMs: 1000,
		HasError:   status == model.TraceStatusError,
		EventCount: len(events),
	}
	if err := repo.CreateTrace(tr); err != nil {
		t.Fatalf("create trace: %v", err)
	}
	if len(events) > 0 {
		if err := repo.CreateEvents(events); err != nil {
			t.Fatalf("create events: %v", err)
		}
	}
}

func ev(traceID, level, module, event string, ts time.Time) model.TraceEvent {
	return model.TraceEvent{
		TraceID:   traceID,
		Level:     level,
		Module:    module,
		Event:     event,
		Timestamp: ts,
	}
}

// TestModuleFilterDoesNotDuplicateRows 模块过滤走 EXISTS 子查询。
//
// 这是回归测试：早期实现用 JOIN + DISTINCT，一条链路有多条同模块事件时会被
// JOIN 成多行，分页数量和实际返回条数对不上，翻页会出现重复/丢失。
func TestModuleFilterDoesNotDuplicateRows(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()

	base := time.Now().Add(-time.Hour)
	// 同一条链路里塞 3 条 payment 模块事件，专门用来触发 JOIN 重复行问题。
	seedTrace(t, repo, "t-1", model.TraceStatusError, base, []model.TraceEvent{
		ev("t-1", model.LevelInfo, "payment", "start", base),
		ev("t-1", model.LevelWarn, "payment", "retry", base.Add(500*time.Millisecond)),
		ev("t-1", model.LevelError, "payment", "pay_failed", base.Add(time.Second)),
	})
	seedTrace(t, repo, "t-2", model.TraceStatusOK, base, []model.TraceEvent{
		ev("t-2", model.LevelInfo, "order", "start", base),
		ev("t-2", model.LevelInfo, "order", "end", base.Add(time.Second)),
	})

	traces, total, err := repo.ListTraces(model.TraceFilter{Module: "payment", PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1 (JOIN would inflate this to 3)", total)
	}
	if len(traces) != 1 {
		t.Fatalf("returned %d rows, want 1", len(traces))
	}
	if traces[0].TraceID != "t-1" {
		t.Fatalf("trace_id = %q, want t-1", traces[0].TraceID)
	}
	// 扫描出来的字段必须完整，不能只填了 ID。
	if traces[0].Status != model.TraceStatusError {
		t.Fatalf("status = %q, want error (fields must be fully scanned)", traces[0].Status)
	}
}

// TestLevelFilterAcrossEvents 级别过滤命中"链路中出现过该级别"的语义。
func TestLevelFilterAcrossEvents(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()

	base := time.Now().Add(-time.Hour)
	seedTrace(t, repo, "t-warn", model.TraceStatusWarn, base, []model.TraceEvent{
		ev("t-warn", model.LevelInfo, "m", "start", base),
		ev("t-warn", model.LevelWarn, "m", "slow", base.Add(time.Second)),
	})
	seedTrace(t, repo, "t-ok", model.TraceStatusOK, base, []model.TraceEvent{
		ev("t-ok", model.LevelInfo, "m", "start", base),
		ev("t-ok", model.LevelInfo, "m", "end", base.Add(time.Second)),
	})

	traces, total, err := repo.ListTraces(model.TraceFilter{Level: model.LevelWarn})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(traces) != 1 || traces[0].TraceID != "t-warn" {
		t.Fatalf("level filter wrong: total=%d rows=%d", total, len(traces))
	}
}

// TestKeywordSearchesEventFields 关键词要能搜到只存在于事件里的文案，
// 这也是实际排查时最常用的方式（拿报错信息反查链路）。
func TestKeywordSearchesEventFields(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()

	base := time.Now().Add(-time.Hour)
	hit := ev("t-hit", model.LevelError, "payment", "pay_failed", base)
	hit.Message = "connection refused by upstream"
	hit.Params = `{"gateway":"wxpay","code":"SYSTEM_ERROR"}`
	seedTrace(t, repo, "t-hit", model.TraceStatusError, base, []model.TraceEvent{hit})

	miss := ev("t-miss", model.LevelInfo, "order", "done", base)
	miss.Message = "all good"
	seedTrace(t, repo, "t-miss", model.TraceStatusOK, base, []model.TraceEvent{miss})

	for _, kw := range []string{"connection refused", "SYSTEM_ERROR", "t-hit"} {
		_, total, err := repo.ListTraces(model.TraceFilter{Keyword: kw})
		if err != nil {
			t.Fatalf("list %q: %v", kw, err)
		}
		if total != 1 {
			t.Errorf("keyword %q: total = %d, want 1", kw, total)
		}
	}
}

// TestPaginationStable 分页结果应当按 start_time 倒序且不重不漏。
func TestPaginationStable(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()

	base := time.Now().Add(-time.Hour)
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		id := "t-" + string(rune('a'+i))
		ids = append(ids, id)
		seedTrace(t, repo, id, model.TraceStatusOK, base.Add(time.Duration(i)*time.Minute), nil)
	}

	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		traces, total, err := repo.ListTraces(model.TraceFilter{Page: page, PageSize: 2})
		if err != nil {
			t.Fatalf("list page %d: %v", page, err)
		}
		if total != 5 {
			t.Fatalf("total = %d, want 5", total)
		}
		want := 2
		if page == 3 {
			want = 1
		}
		if len(traces) != want {
			t.Fatalf("page %d returned %d rows, want %d", page, len(traces), want)
		}
		for _, tr := range traces {
			if seen[tr.TraceID] {
				t.Fatalf("trace %s duplicated across pages", tr.TraceID)
			}
			seen[tr.TraceID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d distinct traces across pages, want 5", len(seen))
	}
}

// TestGetEventsOrderedByTimestamp 详情页时间线必须严格按时间升序，
// 否则看到的因果顺序会是错的。
func TestGetEventsOrderedByTimestamp(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()

	base := time.Now().Add(-time.Hour)
	// 故意乱序写入
	events := []model.TraceEvent{
		ev("t-1", model.LevelInfo, "m", "third", base.Add(3*time.Second)),
		ev("t-1", model.LevelInfo, "m", "first", base),
		ev("t-1", model.LevelInfo, "m", "second", base.Add(time.Second)),
	}
	seedTrace(t, repo, "t-1", model.TraceStatusOK, base, events)

	got, err := repo.GetEventsByTraceID("t-1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	want := []string{"first", "second", "third"}
	for i, e := range got {
		if e.Event != want[i] {
			t.Fatalf("event[%d] = %q, want %q (timeline must be chronological)", i, e.Event, want[i])
		}
	}
}

// TestCreateEventsBatchTransaction 大批量写入要能落库，且失败时整批回滚。
func TestCreateEventsBatchTransaction(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()

	base := time.Now()
	events := make([]model.TraceEvent, 0, 1200)
	for i := 0; i < 1200; i++ {
		events = append(events, ev("t-big", model.LevelInfo, "m", "tick", base.Add(time.Duration(i)*time.Millisecond)))
	}
	if err := repo.CreateEvents(events); err != nil {
		t.Fatalf("create events: %v", err)
	}

	got, err := repo.GetEventsByTraceID("t-big")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(got) != 1200 {
		t.Fatalf("persisted %d events, want 1200 (batching must not drop rows)", len(got))
	}
}

// TestOrphanGracePeriodProtectsInFlightEvents 宽限期内的事件不能被误删。
//
// 落盘是"先建 trace 再插事件"，清理若卡在两步之间会把在途事件当成孤儿清掉。
func TestOrphanGracePeriodProtectsInFlightEvents(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()

	base := time.Now().Add(-time.Hour)
	seedTrace(t, repo, "t-gone", model.TraceStatusOK, base, []model.TraceEvent{
		ev("t-gone", model.LevelInfo, "m", "start", base),
	})
	if _, err := repo.DeleteTracesBefore(time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("delete traces: %v", err)
	}

	// 宽限期 1 小时：这些事件刚创建，必须被保护。
	n, err := repo.DeleteOrphanEvents(time.Hour)
	if err != nil {
		t.Fatalf("delete orphans: %v", err)
	}
	if n != 0 {
		t.Fatalf("deleted %d events within grace period, want 0", n)
	}
}

// TestDeleteCleansUpOrphans 清理孤儿事件，避免"链路删了事件还在"的残留。
func TestDeleteCleansUpOrphans(t *testing.T) {
	repo, closeDB := newTestRepo(t)
	defer closeDB()

	base := time.Now().Add(-time.Hour)
	seedTrace(t, repo, "t-keep", model.TraceStatusOK, time.Now(), []model.TraceEvent{
		ev("t-keep", model.LevelInfo, "m", "start", time.Now()),
	})
	seedTrace(t, repo, "t-drop", model.TraceStatusOK, base, []model.TraceEvent{
		ev("t-drop", model.LevelInfo, "m", "start", base),
	})

	if _, err := repo.DeleteTracesBefore(time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("delete traces: %v", err)
	}
	// 宽限期为 0：这些事件刚写入，用旧的 created_at 条件会被漏掉，
	// 而"父链路已删"才是判断孤儿的真正依据。
	n, err := repo.DeleteOrphanEvents(0)
	if err != nil {
		t.Fatalf("delete orphans: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted %d orphan events, want 1", n)
	}

	remaining, err := repo.GetEventsByTraceID("t-drop")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("%d events残留 after cleanup, want 0", len(remaining))
	}
}
