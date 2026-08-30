package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/tracepulse/config"
	"github.com/example/tracepulse/model"
	"github.com/example/tracepulse/repository"
	"github.com/example/tracepulse/service"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// stubAlert 不需要 SMTP 的告警实现。
type stubAlert struct{}

func (stubAlert) AlertOnTrace(*model.Trace, []model.TraceEvent) {}
func (stubAlert) AlertOnQueueDrop(int)                          {}
func (stubAlert) Shutdown()                                     {}

func newTestController(t *testing.T) (*TraceController, func()) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/c.db"), &gorm.Config{
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

	cfg := traceTestConfig()
	svc := service.NewTraceService(repository.NewTraceRepository(db), stubAlert{}, cfg)
	return NewTraceController(svc, 1<<20), func() { svc.Shutdown(); _ = sqlDB.Close() }
}

// waitForTrace 轮询等待异步落盘完成，避免测试依赖 flush 时序。
func waitForTrace(t *testing.T, c *TraceController, traceID string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/traces/"+traceID, nil)
		req.SetPathValue("trace_id", traceID)
		rec := httptest.NewRecorder()
		c.GetTraceJSON(rec, req)
		if rec.Code == http.StatusOK {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("trace %s was never persisted within 5s", traceID)
}

func traceTestConfig() config.TraceConfig {
	cfg := config.TraceConfig{}
	cfg.QueueSize = 100
	cfg.TTLSeconds = 3600
	cfg.FlushBatch = 1
	cfg.FlushMs = 10
	cfg.CleanupDays = 7
	cfg.MaxEventsPerTrace = 1000
	cfg.NDJSONMaxMB = 1
	return cfg
}

// TestReportAcceptsBothPayloadShapes 上报接口必须同时接受 {"events":[...]} 与裸数组，
// 采集端单行上报时更省事。
func TestReportAcceptsBothPayloadShapes(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	wrapped := `{"events":[{"trace_id":"t-1","level":"info","module":"m","event":"e"}]}`
	bare := `[{"trace_id":"t-2","level":"info","module":"m","event":"e"}]`

	for _, body := range []string{wrapped, bare} {
		req := httptest.NewRequest(http.MethodPost, "/api/traces/report", strings.NewReader(body))
		rec := httptest.NewRecorder()
		c.ReportEvents(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("body %s: status = %d, want 200 (resp: %s)", body, rec.Code, rec.Body.String())
		}
	}
}

// TestReportRejectsGarbage 非法输入要给出明确 400，而不是把脏数据写进库。
func TestReportRejectsGarbage(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	for _, body := range []string{``, `not json`, `[]`, `{}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/traces/report", strings.NewReader(body))
		rec := httptest.NewRecorder()
		c.ReportEvents(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

// TestReportEnforcesBodyLimit 超大 payload 必须被拦下，否则一个请求就能打爆内存。
func TestReportEnforcesBodyLimit(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	big := `{"events":[` + strings.Repeat(`{"trace_id":"t","event":"e"},`, 5000) + `{"trace_id":"t","event":"e"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/traces/report", strings.NewReader(big))
	rec := httptest.NewRecorder()
	c.ReportEvents(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 413 or 400 for oversized payload", rec.Code)
	}
}

// TestGetTraceJSONNotFound 精确查询不存在的 trace_id 必须返回 404，
// 前端据此提示"已过期或尚未落库"。
func TestGetTraceJSONNotFound(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	req := httptest.NewRequest(http.MethodGet, "/api/traces/nope", nil)
	req.SetPathValue("trace_id", "nope")
	rec := httptest.NewRecorder()
	c.GetTraceJSON(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestDetailPageEscapesHostileContent 链路内容来自业务方，可能带尖括号。
// 详情页面必须转义，否则一条恶意日志就能在排查时执行脚本。
func TestDetailPageEscapesHostileContent(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	payload := `[{"trace_id":"t-xss","level":"error","module":"m","event":"boom",
		"message":"<script>alert(1)</script>","error_message":"<img src=x onerror=alert(2)>"}]`
	req := httptest.NewRequest(http.MethodPost, "/api/traces/report", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	c.ReportEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	// 上报是异步落盘的，轮询等待事件进入详情页可查状态。
	waitForTrace(t, c, "t-xss")

	pageReq := httptest.NewRequest(http.MethodGet, "/trace/t-xss", nil)
	pageReq.SetPathValue("trace_id", "t-xss")
	pageRec := httptest.NewRecorder()
	c.TraceDetailPage(pageRec, pageReq)

	if pageRec.Code != http.StatusOK {
		t.Fatalf("detail page status = %d, want 200", pageRec.Code)
	}

	body := pageRec.Body.String()
	for _, dangerous := range []string{"<script>alert(1)</script>", "<img src=x onerror=alert(2)>"} {
		if strings.Contains(body, dangerous) {
			t.Fatalf("unescaped content rendered in page: %s", dangerous)
		}
	}
	// 转义后的实体应当出现
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("expected escaped &lt;script&gt; in output")
	}
}

// TestDetailPageNotFoundShowsGuidance trace_id 不存在时页面要给出可操作的提示，
// 而不是一个空白表格让人以为是 bug。
func TestDetailPageNotFoundShowsGuidance(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	req := httptest.NewRequest(http.MethodGet, "/trace/missing", nil)
	req.SetPathValue("trace_id", "missing")
	rec := httptest.NewRecorder()
	c.TraceDetailPage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing") {
		t.Error("404 page should echo the trace_id being searched")
	}
}

// TestDetailPageRequiresTraceID 缺 trace_id 应返回 400 而不是 500。
func TestDetailPageRequiresTraceID(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	req := httptest.NewRequest(http.MethodGet, "/trace/", nil)
	req.SetPathValue("trace_id", "   ")
	rec := httptest.NewRecorder()
	c.TraceDetailPage(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestSearchPageRejectsBadFilterParams 非法参数要报 400 并给出可读原因，
// 不能静默忽略（否则用户以为"没有数据"）。
func TestSearchPageRejectsBadFilterParams(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	cases := map[string]string{
		"page=0":             "page",
		"page=abc":           "page",
		"page_size=0":        "page_size",
		"min_duration_ms=x":  "min_duration_ms",
		"min_duration_ms=-1": "min_duration_ms",
		"has_error=maybe":    "has_error",
		"start_time=昨天":      "start_time",
	}
	for query, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/traces?"+query, nil)
		rec := httptest.NewRecorder()
		c.SearchPage(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", query, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("query %q: error message should mention %q", query, want)
		}
	}
}

// TestSearchPageAcceptsRelativeTime 相对时间是排查时的高频用法，
// "最近一小时" 不该要求用户去手算时间戳。
func TestSearchPageAcceptsRelativeTime(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	for _, q := range []string{"start_time=1h", "start_time=30m", "start_time=7d", "start_time=24h"} {
		req := httptest.NewRequest(http.MethodGet, "/traces?"+q, nil)
		rec := httptest.NewRecorder()
		c.SearchPage(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("query %q: status = %d, want 200 (%s)", q, rec.Code, rec.Body.String())
		}
	}
}

// TestSearchPageListsAllOnEmptyParams 打开页面 / 空条件点查询也必须列出全部数据，
// 不能只显示"未查询"的空状态（否则用户会误以为没有数据）。
func TestSearchPageListsAllOnEmptyParams(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	reportReq := httptest.NewRequest(http.MethodPost, "/api/traces/report",
		strings.NewReader(`{"events":[{"trace_id":"t-empty-params","level":"info","module":"m","event":"start"}]}`))
	reportRec := httptest.NewRecorder()
	c.ReportEvents(reportRec, reportReq)
	if reportRec.Code != http.StatusOK {
		t.Fatalf("report: status = %d, want 200 (%s)", reportRec.Code, reportRec.Body.String())
	}
	waitForTrace(t, c, "t-empty-params")

	pageReq := httptest.NewRequest(http.MethodGet, "/traces", nil)
	pageRec := httptest.NewRecorder()
	c.SearchPage(pageRec, pageReq)

	if pageRec.Code != http.StatusOK {
		t.Fatalf("empty params: status = %d, want 200 (%s)", pageRec.Code, pageRec.Body.String())
	}
	if !strings.Contains(pageRec.Body.String(), "t-empty-params") {
		t.Errorf("empty params page should list persisted traces, got: %s", pageRec.Body.String())
	}
}

// TestParseTimeFlexible 时间解析的各种写法。
func TestParseTimeFlexible(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"1h", true}, {"30m", true}, {"7d", true}, {"90s", true},
		{"2026-01-02 15:04:05", true},
		{"2026-01-02", true},
		{"2026-01-02T15:04:05Z", true},
		{"昨天", false}, {"", false}, {"abc", false}, {"1x", false},
	}
	for _, c := range cases {
		_, ok := parseTimeFlexible(c.in)
		if ok != c.want {
			t.Errorf("parseTimeFlexible(%q) ok = %v, want %v", c.in, ok, c.want)
		}
	}
}

// TestParseTimeRelativeIsInThePast 相对时间必须解析成"过去"，否则查的是未来时间窗。
func TestParseTimeRelativeIsInThePast(t *testing.T) {
	got, ok := parseTimeFlexible("1h")
	if !ok {
		t.Fatal("failed to parse 1h")
	}
	if !got.Before(time.Now()) {
		t.Errorf("start_time=1h resolved to %v, want a time in the past", got)
	}
	if d := time.Since(got); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("start_time=1h resolved to %v (ago %v), want ~1h ago", got, d)
	}
}

// TestListTracesJSONReturnsPaging 列表接口要带齐分页元信息，前端才能渲染分页器。
func TestListTracesJSONReturnsPaging(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	req := httptest.NewRequest(http.MethodGet, "/api/traces?page_size=10", nil)
	rec := httptest.NewRecorder()
	c.ListTracesJSON(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out model.TraceListResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Page != 1 || out.PageSize != 10 {
		t.Fatalf("paging meta wrong: page=%d size=%d", out.Page, out.PageSize)
	}
	if out.Traces == nil {
		t.Error("traces should serialize as an empty array, not null")
	}
}

// TestStatsJSONReportsQueueWatermark 队列水位是判断"该不该扩容"的唯一依据。
func TestStatsJSONReportsQueueWatermark(t *testing.T) {
	c, done := newTestController(t)
	defer done()

	req := httptest.NewRequest(http.MethodGet, "/api/traces/stats", nil)
	rec := httptest.NewRecorder()
	c.StatsJSON(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"queue_len", "queue_cap", "active_traces", "dropped_total"} {
		if _, ok := out[key]; !ok {
			t.Errorf("stats missing %q", key)
		}
	}
}
