// Package controller HTTP 处理层。
package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/example/tracepulse/model"
	"github.com/example/tracepulse/service"
	"github.com/example/tracepulse/view"
)

// TraceController 链路相关的 HTTP handler，同时提供 JSON API 与内置 HTML 页面。
type TraceController struct {
	traceService service.TraceService
	templates    *template.Template
	maxBodyBytes int64
}

func NewTraceController(traceService service.TraceService, maxBodyBytes int64) *TraceController {
	if maxBodyBytes <= 0 {
		maxBodyBytes = 8 << 20
	}
	return &TraceController{
		traceService: traceService,
		templates:    pageTemplates,
		maxBodyBytes: maxBodyBytes,
	}
}

// ---------------------------------------------------------------- JSON API ----

// ReportEvents 上报一批链路事件。
//
// 请求体支持两种写法：
//
//	{"events":[{...},{...}]}   批量（推荐）
//	[{...},{...}]              裸数组（单行上报时更省事）
//
// 队列满时事件会被丢弃，但接口仍然返回 200 —— 采集端不应该因为服务端压力大而失败重试，
// 否则容易把雪崩放大。
func (c *TraceController) ReportEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, c.maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResp("request body too large"))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResp("empty request body"))
		return
	}

	var events []model.TraceEvent
	var req model.ReportRequest
	if err := json.Unmarshal(body, &req); err == nil && len(req.Events) > 0 {
		events = req.Events
	} else if err := json.Unmarshal(body, &events); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp("invalid json body, expect {\"events\":[...]} or [...]"))
		return
	}

	if len(events) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResp("events is empty"))
		return
	}
	if len(events) > 5000 {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResp("too many events in one request, max 5000"))
		return
	}

	if err := c.traceService.ReportEvents(events); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResp(err.Error()))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"count":  len(events),
	})
}

// GetTraceJSON 按 trace_id 精确查询一条链路的完整内容。
func (c *TraceController) GetTraceJSON(w http.ResponseWriter, r *http.Request) {
	traceID := strings.TrimSpace(r.PathValue("trace_id"))
	if traceID == "" {
		writeJSON(w, http.StatusBadRequest, errorResp("trace_id is required"))
		return
	}

	trace, events, err := c.traceService.GetTrace(traceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResp(err.Error()))
		return
	}
	if trace == nil {
		writeJSON(w, http.StatusNotFound, errorResp("trace not found"))
		return
	}

	writeJSON(w, http.StatusOK, &model.TraceDetail{Trace: *trace, Events: events})
}

// ListTracesJSON 多条件过滤 + 分页查询。
func (c *TraceController) ListTracesJSON(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp(err.Error()))
		return
	}

	result, err := c.traceService.ListTraces(filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResp(err.Error()))
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// StatsJSON 返回队列水位等运行时指标，便于判断是否需要扩容或调大队列。
func (c *TraceController) StatsJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, c.traceService.Stats())
}

// -------------------------------------------------------------- HTML 页面 ----

// TraceDetailPage 链路详情页，时序展示整条链路的每一步。
func (c *TraceController) TraceDetailPage(w http.ResponseWriter, r *http.Request) {
	traceID := strings.TrimSpace(r.PathValue("trace_id"))

	data := &detailPageData{Title: "链路详情 · TracePulse", TraceID: traceID}

	if traceID == "" {
		c.renderDetail(w, http.StatusBadRequest, data.withError("trace_id 不能为空"))
		return
	}

	trace, events, err := c.traceService.GetTrace(traceID)
	if err != nil {
		c.renderDetail(w, http.StatusInternalServerError, data.withError(err.Error()))
		return
	}
	if trace == nil {
		c.renderDetail(w, http.StatusNotFound, data.withError(
			fmt.Sprintf("未找到 trace_id 为 %q 的链路，可能已过期被清理，或尚未落库", traceID)))
		return
	}

	data.Found = true
	data.Trace = trace
	data.build(trace, events)
	c.renderDetail(w, http.StatusOK, data)
}

// SearchPage 检索页：过滤表单 + 结果列表 + 分页，服务端渲染，手机浏览器无需 JS。
func (c *TraceController) SearchPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// 直接输入 trace_id 精确查询时，一步到位跳到详情页，这是告警排查最高频的路径。
	if gotoTID := strings.TrimSpace(q.Get("goto_trace_id")); gotoTID != "" {
		http.Redirect(w, r, "/trace/"+url.PathEscape(gotoTID), http.StatusFound)
		return
	}

	data := &listPageData{Title: "链路检索 · TracePulse", PageSizeOptions: pageSizeOptions}

	filter, err := parseFilter(q)
	if err != nil {
		c.renderList(w, http.StatusBadRequest, data.withError(err.Error()))
		return
	}

	data.Filter = filter
	data.Form = filterToForm(filter)
	data.Form.StartRaw = strings.TrimSpace(q.Get("start_time"))
	data.Form.EndRaw = strings.TrimSpace(q.Get("end_time"))

	// 只有用户真正提交了任一条件，或显式翻页时才查库，避免打开首页就全表聚合。
	if q.Has("page") || filterHasCondition(filter) {
		result, err := c.traceService.ListTraces(filter)
		if err != nil {
			c.renderList(w, http.StatusInternalServerError, data.withError(err.Error()))
			return
		}
		data.Result = result
		data.build(result)
	}

	data.Queried = data.Result != nil
	c.renderList(w, http.StatusOK, data)
}

func (c *TraceController) renderDetail(w http.ResponseWriter, status int, data *detailPageData) {
	renderHTML(w, c.templates, "detail.html", status, data)
}

func (c *TraceController) renderList(w http.ResponseWriter, status int, data *listPageData) {
	renderHTML(w, c.templates, "list.html", status, data)
}

// renderHTML 渲染页面。出错时降级为纯文本提示，保证排障时永远能看到原因。
func renderHTML(w http.ResponseWriter, t *template.Template, name string, status int, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		_, _ = io.WriteString(w, "<pre>render page failed: "+template.HTMLEscapeString(err.Error())+"</pre>")
	}
}

// ------------------------------------------------------------ 页面数据结构 ----

type detailPageData struct {
	Title   string
	TraceID string
	Found   bool
	Error   string

	Trace   *model.Trace
	Summary traceSummary
	Events  []eventRow
}

type traceSummary struct {
	Status       string
	Service      string
	Duration     string
	Start        string
	End          string
	EventCount   int
	ErrorMessage string
	LevelStats   []levelStatRow
}

type levelStatRow struct {
	Level string
	Count int
}

type eventRow struct {
	Seq          int
	Clock        string
	FullTime     string
	Offset       string
	Gap          string
	GapSlow      bool
	Level        string
	Module       string
	Event        string
	Message      string
	MessageShort string
	MessageLong  bool
	Params       []model.KV
	ErrorMsg     string
	IsError      bool
	SpanID       string
	ParentSpanID string
}

func (d *detailPageData) withError(msg string) *detailPageData {
	d.Error = msg
	return d
}

// build 把领域模型转换成展示模型：时间本地化、计算步进间隔、标记长文本。
func (d *detailPageData) build(trace *model.Trace, events []model.TraceEvent) {
	d.Summary = traceSummary{
		Status:       trace.Status,
		Service:      trace.ServiceName,
		Duration:     view.FormatDuration(trace.DurationMs),
		Start:        view.FormatTime(trace.StartTime),
		End:          view.FormatTime(trace.EndTime),
		EventCount:   trace.EventCount,
		ErrorMessage: trace.ErrorMessage,
	}

	counts := make(map[string]int)
	base := trace.StartTime
	var prev time.Time
	prevSet := false

	d.Events = make([]eventRow, 0, len(events))
	for i, e := range events {
		if i == 0 {
			base = e.Timestamp
		}

		row := eventRow{
			Seq:          i + 1,
			Clock:        view.FormatClock(e.Timestamp),
			FullTime:     view.FormatTime(e.Timestamp),
			Offset:       view.FormatOffset(e.Timestamp.Sub(base).Milliseconds()),
			Level:        e.Level,
			Module:       e.Module,
			Event:        e.Event,
			Message:      e.Message,
			MessageShort: view.Truncate(e.Message, 160),
			MessageLong:  view.IsLong(e.Message, 160),
			Params:       e.ParamsList(),
			ErrorMsg:     e.ErrorMessage,
			IsError:      e.Level == model.LevelError || e.Level == model.LevelFatal,
			SpanID:       e.SpanID,
			ParentSpanID: e.ParentSpanID,
		}

		if prevSet {
			gap := e.Timestamp.Sub(prev).Milliseconds()
			row.Gap = view.FormatOffset(gap)
			// 单步间隔超过整条链路耗时的 30% 就标记为慢步骤，方便一眼定位卡点。
			row.GapSlow = trace.DurationMs > 0 && gap > trace.DurationMs*30/100 && gap > 50
		}
		prevSet = true
		prev = e.Timestamp

		counts[e.Level]++
		d.Events = append(d.Events, row)
	}

	// 级别统计按数量降序，错误级优先置顶。
	levels := make([]levelStatRow, 0, len(counts))
	for lv, n := range counts {
		levels = append(levels, levelStatRow{Level: lv, Count: n})
	}
	sortLevelStats(levels)
	d.Summary.LevelStats = levels
}

// sortLevelStats 错误级优先，其余按数量降序。
func sortLevelStats(rows []levelStatRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j-1], rows[j]
			if levelRank(b.Level) < levelRank(a.Level) ||
				(levelRank(b.Level) == levelRank(a.Level) && b.Count > a.Count) {
				rows[j-1], rows[j] = b, a
				continue
			}
			break
		}
	}
}

func levelRank(level string) int {
	switch level {
	case model.LevelFatal:
		return 0
	case model.LevelError:
		return 1
	case model.LevelWarn:
		return 2
	default:
		return 3
	}
}

// ---------------------------------------------------------------- 列表页 ----

type listPageData struct {
	Title   string
	Error   string
	Queried bool

	Filter model.TraceFilter
	Form   filterForm
	Result *model.TraceListResult
	Rows   []traceRow

	Pages    []int
	PrevPage int
	NextPage int

	PageSizeOptions []int
}

// pageSizeOptions 每页条数候选值。
var pageSizeOptions = []int{20, 50, 100, 200}

// QuickURL 生成「快捷时间范围」链接，保留当前已填写的过滤条件。
//
// 用法：{{.QuickURL "24h"}}；也可以追加需要覆盖的条件：{{.QuickURL "24h" "status" "error"}}
func (d *listPageData) QuickURL(args ...string) template.URL {
	if len(args) == 0 {
		return template.URL("/traces")
	}

	v := d.filterValues()
	v.Set("start_time", args[0])
	// 切换时间范围时回到第一页，否则很容易停在一个不存在的页码上。
	v.Del("page")

	for i := 1; i+1 < len(args); i += 2 {
		if args[i+1] == "" {
			v.Del(args[i])
			continue
		}
		v.Set(args[i], args[i+1])
	}

	return template.URL("/traces?" + v.Encode())
}

// filterValues 把过滤条件序列化为查询串，供分页与快捷链接复用。
func (d *listPageData) filterValues() url.Values {
	v := url.Values{}
	if d.Form.TraceID != "" {
		v.Set("trace_id", d.Form.TraceID)
	}
	if d.Form.Service != "" {
		v.Set("service", d.Form.Service)
	}
	if d.Form.Status != "" {
		v.Set("status", d.Form.Status)
	}
	if d.Form.Level != "" {
		v.Set("level", d.Form.Level)
	}
	if d.Form.Module != "" {
		v.Set("module", d.Form.Module)
	}
	if d.Form.Keyword != "" {
		v.Set("keyword", d.Form.Keyword)
	}
	if d.Form.HasError != "" {
		v.Set("has_error", d.Form.HasError)
	}
	if d.Form.MinDuration != "" {
		v.Set("min_duration_ms", d.Form.MinDuration)
	}
	if d.Form.Start != "" {
		v.Set("start_time", d.Form.Start)
	}
	if d.Form.End != "" {
		v.Set("end_time", d.Form.End)
	}
	return v
}

type filterForm struct {
	TraceID     string
	Service     string
	Status      string
	Level       string
	Module      string
	Keyword     string
	HasError    string
	MinDuration string
	Start       string
	End         string
	PageSize    int

	// StartRaw / EndRaw 保留用户原始输入（例如 "24h"）。
	// 输入框回填用它，链接生成用规范化后的绝对时间，这样翻页时时间窗不会漂移。
	StartRaw string
	EndRaw   string
}

type traceRow struct {
	Trace    *model.Trace
	Start    string
	End      string
	Duration string
	IsError  bool
}

func (d *listPageData) withError(msg string) *listPageData {
	d.Error = msg
	return d
}

func (d *listPageData) build(result *model.TraceListResult) {
	d.Rows = make([]traceRow, 0, len(result.Traces))
	for i := range result.Traces {
		t := result.Traces[i]
		d.Rows = append(d.Rows, traceRow{
			Trace:    &t,
			Start:    view.FormatTime(t.StartTime),
			End:      view.FormatTime(t.EndTime),
			Duration: view.FormatDuration(t.DurationMs),
			IsError:  t.Status == model.TraceStatusError || t.Status == model.TraceStatusTimeout || t.HasError,
		})
	}

	// 分页器：最多展示 7 个页码，围绕当前页居中。
	const window = 7
	total := result.TotalPages
	cur := result.Page
	if total <= window {
		d.Pages = make([]int, 0, total)
		for i := 1; i <= total; i++ {
			d.Pages = append(d.Pages, i)
		}
	} else {
		start := cur - window/2
		if start < 1 {
			start = 1
		}
		end := start + window - 1
		if end > total {
			end = total
			start = end - window + 1
		}
		d.Pages = make([]int, 0, window)
		for i := start; i <= end; i++ {
			d.Pages = append(d.Pages, i)
		}
	}

	if cur > 1 {
		d.PrevPage = cur - 1
	}
	if cur < total {
		d.NextPage = cur + 1
	}
}

// PageURL 生成指定页码的链接，保留当前所有过滤条件。
func (d *listPageData) PageURL(page int) template.URL {
	v := d.filterValues()
	if d.Form.PageSize > 0 && d.Form.PageSize != model.DefaultPageSize {
		v.Set("page_size", strconv.Itoa(d.Form.PageSize))
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return template.URL("/traces?" + v.Encode())
}

// ------------------------------------------------------------ 参数解析 ----

// parseFilter 从查询串解析过滤条件。
func parseFilter(q url.Values) (model.TraceFilter, error) {
	f := model.TraceFilter{
		TraceID: strings.TrimSpace(q.Get("trace_id")),
		Service: strings.TrimSpace(q.Get("service")),
		Status:  strings.TrimSpace(q.Get("status")),
		Level:   strings.TrimSpace(q.Get("level")),
		Module:  strings.TrimSpace(q.Get("module")),
		Keyword: strings.TrimSpace(q.Get("keyword")),
	}

	if v := strings.TrimSpace(q.Get("has_error")); v != "" {
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			b := true
			f.HasError = &b
		case "false", "0", "no":
			b := false
			f.HasError = &b
		default:
			return f, errors.New("has_error 只支持 true / false")
		}
	}

	if v := strings.TrimSpace(q.Get("min_duration_ms")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return f, errors.New("min_duration_ms 必须是非负整数")
		}
		f.MinDurationMs = n
	}

	if v := strings.TrimSpace(q.Get("start_time")); v != "" {
		t, ok := parseTimeFlexible(v)
		if !ok {
			return f, fmt.Errorf("start_time 格式无法识别: %q（支持 2026-01-02 15:04:05 / RFC3339 / 1h / 30m / 7d）", v)
		}
		f.StartTime = t
	}
	if v := strings.TrimSpace(q.Get("end_time")); v != "" {
		t, ok := parseTimeFlexible(v)
		if !ok {
			return f, fmt.Errorf("end_time 格式无法识别: %q（支持 2026-01-02 15:04:05 / RFC3339 / 1h / 30m / 7d）", v)
		}
		f.EndTime = t
	}

	f.Page = 1
	if v := strings.TrimSpace(q.Get("page")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return f, errors.New("page 必须是正整数")
		}
		f.Page = n
	}
	f.PageSize = model.DefaultPageSize
	if v := strings.TrimSpace(q.Get("page_size")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return f, errors.New("page_size 必须是正整数")
		}
		f.PageSize = n
	}
	f.Normalize()

	return f, nil
}

// filterToForm 把过滤条件还原成表单字符串，用于回填输入框。
// （model.TraceFilter 不在本包内，无法直接定义方法，故用普通函数。）
func filterToForm(f model.TraceFilter) filterForm {
	form := filterForm{
		TraceID:  f.TraceID,
		Service:  f.Service,
		Status:   f.Status,
		Level:    f.Level,
		Module:   f.Module,
		Keyword:  f.Keyword,
		PageSize: f.PageSize,
	}
	if f.HasError != nil {
		if *f.HasError {
			form.HasError = "true"
		} else {
			form.HasError = "false"
		}
	}
	if f.MinDurationMs > 0 {
		form.MinDuration = strconv.FormatInt(f.MinDurationMs, 10)
	}
	if !f.StartTime.IsZero() {
		form.Start = f.StartTime.In(view.Loc).Format("2006-01-02 15:04:05")
	}
	if !f.EndTime.IsZero() {
		form.End = f.EndTime.In(view.Loc).Format("2006-01-02 15:04:05")
	}
	return form
}

// filterHasCondition 判断用户是否输入了任一过滤条件。
func filterHasCondition(f model.TraceFilter) bool {
	return f.TraceID != "" || f.Service != "" || f.Status != "" || f.Level != "" ||
		f.Module != "" || f.Keyword != "" || f.HasError != nil || f.MinDurationMs > 0 ||
		!f.StartTime.IsZero() || !f.EndTime.IsZero()
}

// parseTimeFlexible 解析时间，支持绝对时间与相对时间两种写法。
//
// 相对时间（1h / 30m / 7d）是排查时的高频用法：出故障后只想看最近一小时。
func parseTimeFlexible(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}

	if d, ok := parseRelative(s); ok {
		return time.Now().Add(-d), true
	}

	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, view.Loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseRelative 解析 30s / 30m / 12h / 7d 这类相对时间。
func parseRelative(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return 0, false
	}

	switch unit {
	case 's':
		return time.Duration(n) * time.Second, true
	case 'm':
		return time.Duration(n) * time.Minute, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// ------------------------------------------------------------------ 工具 ----

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func errorResp(msg string) map[string]interface{} {
	return map[string]interface{}{"error": msg}
}
