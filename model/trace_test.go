package model

import (
	"encoding/json"
	"testing"
)

func decodeEvent(t *testing.T, body string) TraceEvent {
	t.Helper()
	var e TraceEvent
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return e
}

// TestParamsAcceptsObject params 传对象是采集端最自然的写法。
// 库里存的是字符串，但不能因此把整批事件拒掉。
func TestParamsAcceptsObject(t *testing.T) {
	e := decodeEvent(t, `{"trace_id":"t","params":{"gateway":"alipay","rtt_ms":2400}}`)

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(e.Params), &m); err != nil {
		t.Fatalf("params should be a JSON string, got %q: %v", e.Params, err)
	}
	if m["gateway"] != "alipay" {
		t.Errorf("gateway = %v, want alipay", m["gateway"])
	}
	if m["rtt_ms"] != float64(2400) {
		t.Errorf("rtt_ms = %v, want 2400", m["rtt_ms"])
	}
}

// TestParamsAcceptsString 已经是 JSON 文本时原样保留，不做二次编码。
func TestParamsAcceptsString(t *testing.T) {
	e := decodeEvent(t, `{"trace_id":"t","params":"{\"a\":1}"}`)
	if e.Params != `{"a":1}` {
		t.Fatalf("params = %q, want the original JSON text", e.Params)
	}
}

// TestParamsAcceptsScalars 标量类型也要能存下来，不能静默丢失。
func TestParamsAcceptsScalars(t *testing.T) {
	cases := map[string]string{
		`{"params":42}`:    "42",
		`{"params":true}`:  "true",
		`{"params":[1,2]}`: "[1,2]",
		`{"params":"raw"}`: "raw",
	}
	for body, want := range cases {
		e := decodeEvent(t, `{"trace_id":"t",`+body[1:])
		if e.Params != want {
			t.Errorf("body %s: params = %q, want %q", body, e.Params, want)
		}
	}
}

// TestParamsMissingOrNull 缺失或 null 应当得到空串，而不是 "null" 这种噪音。
func TestParamsMissingOrNull(t *testing.T) {
	if got := decodeEvent(t, `{"trace_id":"t"}`).Params; got != "" {
		t.Errorf("missing params = %q, want empty", got)
	}
	if got := decodeEvent(t, `{"trace_id":"t","params":null}`).Params; got != "" {
		t.Errorf("null params = %q, want empty", got)
	}
}

// TestUnmarshalStillValidatesTypes 兼容不等于放水：
// 类型明显错误（给字符串字段传对象）仍然必须报错。
func TestUnmarshalStillValidatesTypes(t *testing.T) {
	var e TraceEvent
	if err := json.Unmarshal([]byte(`{"trace_id":{"nested":1}}`), &e); err == nil {
		t.Error("expected error when trace_id is an object")
	}
}

// TestParamsListOrdersKeys KV 展示要稳定排序，否则同样的链路每次渲染顺序都在变。
func TestParamsListOrdersKeys(t *testing.T) {
	var e TraceEvent
	e.Params = `{"zeta":1,"alpha":2,"mid":3}`

	list := e.ParamsList()
	if len(list) != 3 {
		t.Fatalf("got %d entries, want 3", len(list))
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, kv := range list {
		if kv.Key != want[i] {
			t.Errorf("key[%d] = %q, want %q", i, kv.Key, want[i])
		}
	}
}

// TestParamsListFallsBackOnGarbage 非对象格式不能让页面崩掉，要退化成整串展示。
func TestParamsListFallsBackOnGarbage(t *testing.T) {
	var e TraceEvent
	e.Params = `not json at all`

	list := e.ParamsList()
	if len(list) != 1 || list[0].Value != "not json at all" {
		t.Fatalf("expected raw fallback, got %+v", list)
	}
}

// TestParamsListEmpty 空 params 返回 nil，模板里按"-"渲染。
func TestParamsListEmpty(t *testing.T) {
	var e TraceEvent
	if list := e.ParamsList(); list != nil {
		t.Errorf("expected nil for empty params, got %+v", list)
	}
}

// TestParamsListIntegerRendering 整数不该显示成 1.0，否则排查时会以为是浮点精度问题。
func TestParamsListIntegerRendering(t *testing.T) {
	var e TraceEvent
	e.Params = `{"count":42,"ratio":0.5,"ok":true,"name":"x"}`

	got := map[string]string{}
	for _, kv := range e.ParamsList() {
		got[kv.Key] = kv.Value
	}
	if got["count"] != "42" {
		t.Errorf("count = %q, want 42 (not 42.0)", got["count"])
	}
	if got["ratio"] != "0.5" {
		t.Errorf("ratio = %q, want 0.5", got["ratio"])
	}
	if got["ok"] != "true" {
		t.Errorf("ok = %q, want true", got["ok"])
	}
}

// TestReportRequestShape 上报用的包装结构也要能正常解析。
func TestReportRequestShape(t *testing.T) {
	var req ReportRequest
	if err := json.Unmarshal([]byte(`{"events":[{"trace_id":"t1"},{"trace_id":"t2"}]}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(req.Events))
	}
}
