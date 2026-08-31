package model

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
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
	// URL 这条链路对应的业务入口地址（页面 URL 或接口地址），由上报方传入。
	// size 给到 2048：真实业务的 URL 常带一串 query 参数，给小了会被截断到认不出接口。
	URL       string    `gorm:"size:2048" json:"url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	// URL / ServiceName 是链路级属性（traces.url / traces.service_name），这里刻意
	// 标成 gorm:"-"：事件表不必每行重复存一份，只是借事件把值穿过异步队列带到聚合逻辑。
	// ServiceName 优先级高于 Module，缺省时才退化用 Module。
	URL         string    `gorm:"-" json:"url,omitempty"`
	ServiceName string    `gorm:"-" json:"service_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (TraceEvent) TableName() string { return "trace_events" }

// UnmarshalJSON 兼容 params 的多种上报写法。
//
// 采集端最自然的写法是把 params 当对象传（"params":{"k":1}），而库里存的是字符串。
// 如果严格按 string 解析，一个字段写法不对就会让整批事件被 400 拒掉 ——
// 为了上报成功率的健壮性，这里容忍对象 / 数字 / 布尔 / 数组，统一序列化成紧凑
// JSON 字符串存库，前端照样能按 KV 展开。
func (e *TraceEvent) UnmarshalJSON(data []byte) error {
	// 用别名避免递归调用本方法。
	type alias TraceEvent

	aux := struct {
		*alias
		Params json.RawMessage `json:"params"`
		// 采集端（尤其是前端 SDK）习惯写驼峰，这里一并收下，与 snake_case 等价。
		ServiceNameAlt string `json:"serviceName"`
	}{alias: (*alias)(e)}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	e.Params = normalizeParams(aux.Params)
	if e.ServiceName == "" {
		e.ServiceName = aux.ServiceNameAlt
	}
	return nil
}

// normalizeParams 把任意 JSON 值规范化成字符串：
//   - 字符串原样保留（可能本身就是 JSON 文本）
//   - 其他类型重新序列化成紧凑 JSON
//   - 缺失则返回空串
func normalizeParams(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	// 已经是 JSON 字符串字面量，去掉外层引号。
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}
	// null 视为空。
	if string(trimmed) == "null" {
		return ""
	}
	// 其他类型（对象 / 数组 / 数字 / 布尔）压缩成紧凑 JSON。
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return string(trimmed)
	}
	return compact.String()
}

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

// ReportRequest 上报请求体。支持 {"url":"...","service_name":"...","events":[...]}
// 与裸数组 [...] 两种写法。
type ReportRequest struct {
	// URL 链路的业务入口地址（接口名）。裸数组写法没有包裹层，改用查询参数 ?url= 传递。
	URL string `json:"url"`
	// ServiceName 服务名。裸数组写法改用查询参数 ?service_name= 传递。
	// 都不传时退化用首条事件的 module，老采集端无需改动。
	ServiceName string       `json:"service_name"`
	Events      []TraceEvent `json:"events"`
}

// UnmarshalJSON 兼容 service_name 的驼峰写法。
//
// 与 TraceEvent 同理：snake_case 是本项目的主写法，但驼峰在前端 SDK 里太常见了，
// 为了不让一个字段命名差异就丢掉整批事件，这里两种都收下。
func (r *ReportRequest) UnmarshalJSON(data []byte) error {
	// 用别名避免递归调用本方法。
	type alias ReportRequest

	aux := struct {
		*alias
		ServiceNameAlt string `json:"serviceName"`
	}{alias: (*alias)(r)}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if r.ServiceName == "" {
		r.ServiceName = aux.ServiceNameAlt
	}
	return nil
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

// URLStatsFilter URL 统计筛选条件。
type URLStatsFilter struct {
	Service   string    `form:"service" json:"service"`
	StartTime time.Time `form:"start_time" json:"start_time"`
	EndTime   time.Time `form:"end_time" json:"end_time"`
}

// FlexTime 是一个能兼容多种数据库驱动返回类型的时间字段。
//
// SQLite 的聚合函数（如 MAX(start_time)）返回的是 TEXT 原始值而非 time.Time，
// 直接用 time.Time 做 Scan 目标会报 "unsupported Scan, storing driver.Value
// type string into type *time.Time"。FlexTime 通过实现 sql.Scanner 同时兼容
// time.Time / string / []byte / nil 四种输入，内嵌 time.Time 保证 JSON 序列化
// 和方法调用的行为与原生 time.Time 完全一致。
type FlexTime struct {
	time.Time
}

func (f *FlexTime) Scan(value interface{}) error {
	if value == nil {
		f.Time = time.Time{}
		return nil
	}
	switch v := value.(type) {
	case time.Time:
		f.Time = v
		return nil
	case string:
		return f.parseString(v)
	case []byte:
		return f.parseString(string(v))
	default:
		return fmt.Errorf("FlexTime.Scan: unsupported type %T", value)
	}
}

func (f *FlexTime) parseString(s string) error {
	if s == "" {
		f.Time = time.Time{}
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			f.Time = t
			return nil
		}
	}
	return fmt.Errorf("FlexTime.Scan: cannot parse %q as time", s)
}

var _ sql.Scanner = (*FlexTime)(nil)

// URLStatRow 单条 URL 统计结果。
type URLStatRow struct {
	Service     string   `json:"service"`
	URL         string   `json:"url"`
	CallCount   int64    `json:"call_count"`
	ErrorCount  int64    `json:"error_count"`
	AvgDuration int64    `json:"avg_duration_ms"`
	MaxDuration int64    `json:"max_duration_ms"`
	LastTime    FlexTime `json:"last_time"`
}

// URLStatsResult URL 统计列表响应。
type URLStatsResult struct {
	Rows      []URLStatRow `json:"rows"`
	Total     int          `json:"total"`
	Service   string       `json:"service,omitempty"`
	StartTime time.Time    `json:"start_time,omitempty"`
	EndTime   time.Time    `json:"end_time,omitempty"`
}