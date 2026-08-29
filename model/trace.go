package model

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 链路状态取值。与 alert.triggers 配置项一一对应。
const (
	TraceStatusOK      = "ok"
	TraceStatusWarn    = "warn"
	TraceStatusError   = "error"
	TraceStatusTimeout = "timeout"
)

// 告警触发条件取值。
const (
	TriggerError     = "error"
	TriggerWarn      = "warn"
	TriggerTimeout   = "timeout"
	TriggerQueueDrop = "queue_drop"
	TriggerSlow      = "slow"
)

// KV 参数表中的一行。
type KV struct {
	Key   string
	Value string
}

// 事件级别取值。
const (
	LevelTrace = "trace"
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
	LevelFatal = "fatal"
)

// 特殊事件名：收到 end 即认为链路正常结束并立即落盘。
const (
	EventStart = "start"
	EventEnd   = "end"
)

// 分页边界。
const (
	DefaultPageSize = 20
	MaxPageSize     = 200
)

// Trace 一条链路的聚合结果。索引统一由 config.ensureIndexes 用裸 SQL 创建，
// 这里刻意不写 gorm index tag，避免 GORM 自动建索引与手写索引重名打架。
type Trace struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TraceID      string    `gorm:"uniqueIndex;size:64;not null" json:"trace_id"`
	ServiceName  string    `gorm:"size:128" json:"service_name"`
	Status       string    `gorm:"size:16" json:"status"`
	StartTime    time.Time `gorm:"not null" json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	DurationMs   int64     `json:"duration_ms"`
	HasError     bool      `json:"has_error"`
	ErrorMessage string    `gorm:"type:text" json:"error_message"`
	EventCount   int       `json:"event_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (Trace) TableName() string { return "traces" }

// TraceEvent 链路中的单个事件（时序表的一行）。
type TraceEvent struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TraceID      string    `gorm:"size:64;not null" json:"trace_id"`
	SpanID       string    `gorm:"size:64" json:"span_id"`
	ParentSpanID string    `gorm:"size:64" json:"parent_span_id"`
	Timestamp    time.Time `gorm:"not null" json:"timestamp"`
	Level        string    `gorm:"size:16;not null" json:"level"`
	Module       string    `gorm:"size:128" json:"module"`
	Event        string    `gorm:"size:256" json:"event"`
	Message      string    `gorm:"type:text" json:"message"`
	Params       string    `gorm:"type:text" json:"params"`
	ErrorMessage string    `gorm:"type:text" json:"error_message"`
	CreatedAt    time.Time `json:"created_at"`
}

func (TraceEvent) TableName() string { return "trace_events" }

// ParamsList 把 params（JSON 对象字符串）解析为有序 KV 列表，供页面与邮件渲染。
// 解析失败时退化为整串展示，保证异常数据也能看见。
func (e TraceEvent) ParamsList() []KV {
	if strings.TrimSpace(e.Params) == "" {
		return nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(e.Params), &raw); err != nil || raw == nil {
		return []KV{{Key: "", Value: e.Params}}
	}

	list := make([]KV, 0, len(raw))
	for k, v := range raw {
		list = append(list, KV{Key: k, Value: stringify(v)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return list
}

// stringify 把任意 JSON 值转成可读字符串。
func stringify(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		// 整数值不要显示成 1.0
		if t == float64(int64(t)) && t >= -1e15 && t <= 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// ReportRequest 上报请求体。支持 {"events":[...]} 与裸数组 [...] 两种写法。
type ReportRequest struct {
	Events []TraceEvent `json:"events"`
}

// TraceFilter 列表检索条件。
type TraceFilter struct {
	TraceID  string `form:"trace_id" json:"trace_id"`
	Service  string `form:"service" json:"service"`
	Status   string `form:"status" json:"status"`
	Level    string `form:"level" json:"level"`
	Module   string `form:"module" json:"module"`
	Keyword  string `form:"keyword" json:"keyword"`
	HasError *bool  `form:"has_error" json:"has_error"`

	// MinDurationMs 慢调用阈值，只返回耗时 >= 该值的链路。
	MinDurationMs int64 `form:"min_duration_ms" json:"min_duration_ms"`

	StartTime time.Time `form:"start_time" json:"start_time"`
	EndTime   time.Time `form:"end_time" json:"end_time"`

	Page     int `form:"page" json:"page"`
	PageSize int `form:"page_size" json:"page_size"`
}

// Normalize 修正分页入参，防止 PageSize 过大把内存打爆。
func (f *TraceFilter) Normalize() {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = DefaultPageSize
	}
	if f.PageSize > MaxPageSize {
		f.PageSize = MaxPageSize
	}
}

// TraceListResult 分页列表响应。
type TraceListResult struct {
	Total    int64   `json:"total"`
	Traces   []Trace `json:"traces"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	// TotalPages 方便前端直接渲染分页器。
	TotalPages int `json:"total_pages"`
}

// TraceDetail 链路详情响应。
type TraceDetail struct {
	Trace  Trace        `json:"trace"`
	Events []TraceEvent `json:"events"`
}
