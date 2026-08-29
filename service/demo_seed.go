package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/example/tracepulse/model"
	"github.com/example/tracepulse/repository"
)

// demoEvent 演示链路中的一个事件。offsetMs 是相对链路起点的毫秒偏移。
type demoEvent struct {
	offsetMs int64
	level    string
	module   string
	event    string
	message  string
	// params 必须是紧凑 JSON 对象字符串，与上报接口落库的格式保持一致。
	params       string
	errorMessage string
	spanID       string
	parentSpanID string
}

// demoTrace 一条演示链路的构造描述。
type demoTrace struct {
	service      string
	status       string
	errorMessage string
	// ago 是链路起点距现在的时长，用来在列表页铺开时间轴。
	ago    time.Duration
	events []demoEvent
}

// demoTraces 内置演示数据，覆盖 ok / warn / error / timeout 四种状态，
// 一打开页面就能看到列表、详情、慢步骤标记、错误高亮与 span 层级的实际效果。
func demoTraces() []demoTrace {
	return []demoTrace{
		{ // 正常下单：走完 start → ... → end，页面上是绿色 ok。
			service: "demo-order-api",
			status:  model.TraceStatusOK,
			ago:     4 * time.Minute,
			events: []demoEvent{
				{0, model.LevelInfo, "order-api", model.EventStart, "收到下单请求",
					`{"user_id":10086,"sku_count":2}`, "", "6f1a2b3c", ""},
				{12, model.LevelDebug, "auth", "verify_token", "校验用户令牌",
					`{"user_id":10086}`, "", "9c2d1e4f", "6f1a2b3c"},
				{46, model.LevelInfo, "db", "query_stock", "查询商品库存",
					`{"sku":"SKU-88231","stock":17}`, "", "9c2d1e4f", "6f1a2b3c"},
				{118, model.LevelInfo, "inventory", "deduct_stock", "扣减库存成功",
					`{"sku":"SKU-88231","remain":15}`, "", "9c2d1e4f", "6f1a2b3c"},
				{182, model.LevelInfo, "order-api", model.EventEnd, "下单成功",
					`{"order_id":"NO202608300001","amount":199.9}`, "", "6f1a2b3c", ""},
			},
		},
		{ // 支付回调验签失败：error 级事件，详情里整条链路标红。
			service:      "demo-payment-api",
			status:       model.TraceStatusError,
			errorMessage: "signature mismatch: expected 9f2c1a..., got 3ab17d...",
			ago:          21 * time.Minute,
			events: []demoEvent{
				{0, model.LevelInfo, "payment-api", model.EventStart, "收到微信支付回调",
					`{"channel":"wxpay"}`, "", "a1b2c3d4", ""},
				{9, model.LevelDebug, "payment-api", "parse_callback", "解析回调报文",
					`{"bytes":512}`, "", "e5f6a7b8", "a1b2c3d4"},
				{28, model.LevelError, "payment-api", "verify_sign", "回调验签失败",
					`{"algorithm":"HMAC-SHA256"}`, "signature mismatch: expected 9f2c1a..., got 3ab17d...",
					"e5f6a7b8", "a1b2c3d4"},
				{31, model.LevelError, "payment-api", model.EventEnd, "支付回调处理失败",
					"", "订单支付状态未更新，需人工核对", "a1b2c3d4", ""},
			},
		},
		{ // 上游慢调用：warn 级，中间一步的间隔远超整条链路的 30%，会被标成慢步骤。
			service: "demo-api-gateway",
			status:  model.TraceStatusWarn,
			ago:     47 * time.Minute,
			events: []demoEvent{
				{0, model.LevelInfo, "api-gateway", model.EventStart, "转发请求 GET /api/user/profile",
					`{"method":"GET","path":"/api/user/profile"}`, "", "1a2b3c4d", ""},
				{6, model.LevelDebug, "api-gateway", "match_route", "路由匹配命中",
					`{"route":"/api/user/profile"}`, "", "5e6f7a8b", "1a2b3c4d"},
				{1480, model.LevelWarn, "upstream", "call_user_service", "上游响应偏慢",
					`{"upstream":"user-service","elapsed_ms":1474}`, "", "5e6f7a8b", "1a2b3c4d"},
				{1552, model.LevelInfo, "api-gateway", model.EventEnd, "请求完成（含慢调用）",
					`{"status":200,"elapsed_ms":1552}`, "", "1a2b3c4d", ""},
			},
		},
		{ // 客户端漏发 end 被 TTL 强制落盘：timeout 状态，详情页能看到"没收到 end"。
			service:      "demo-report-job",
			status:       model.TraceStatusTimeout,
			errorMessage: `trace timeout: no "end" event received within 300s`,
			ago:          85 * time.Minute,
			events: []demoEvent{
				{0, model.LevelInfo, "report-job", model.EventStart, "开始生成运营日报",
					`{"date":"2026-08-30"}`, "", "7c8d9e0f", ""},
				{230, model.LevelInfo, "report-job", "fetch_orders", "拉取订单明细",
					`{"rows":128000}`, "", "2a3b4c5d", "7c8d9e0f"},
				{2400, model.LevelWarn, "report-job", "export_queued", "导出任务排队中",
					`{"queue_len":3}`, "", "2a3b4c5d", "7c8d9e0f"},
			},
		},
		{ // 慢查询但成功：耗时 2.5s，可用 min_duration_ms 阈值筛出来。
			service: "demo-user-api",
			status:  model.TraceStatusOK,
			ago:     190 * time.Minute,
			events: []demoEvent{
				{0, model.LevelInfo, "user-api", model.EventStart, "查询用户详情",
					`{"user_id":10086}`, "", "b1c2d3e4", ""},
				{11, model.LevelDebug, "cache", "cache_get", "缓存未命中",
					`{"key":"user:10086"}`, "", "f5a6b7c8", "b1c2d3e4"},
				{34, model.LevelInfo, "db", "query_user", "查询用户主表",
					`{"table":"users","cost_ms":18}`, "", "f5a6b7c8", "b1c2d3e4"},
				{1210, model.LevelInfo, "db", "query_orders", "查询用户订单列表（未命中索引）",
					`{"rows":320,"cost_ms":1156}`, "", "0d9e8f7a", "b1c2d3e4"},
				{2530, model.LevelInfo, "user-api", model.EventEnd, "查询完成",
					`{"user_id":10086,"orders":320}`, "", "b1c2d3e4", ""},
			},
		},
	}
}

// SeedDemoData 写入内置演示链路，返回成功写入的链路条数。
//
// 设计取舍：
//  1. 直接写 repository 而不是走 ReportEvents —— 演示数据不必经过队列与 ndjson 兜底，
//     更重要的是不能触发告警（error / timeout 状态会真的往外发邮件）；
//  2. 库里已有链路时默认跳过，避免每次重启都重复灌一批，也不会污染真实数据；
//  3. force 用于反复演示，每次启动追加一批；trace_id 带启动时间戳，不会撞唯一索引；
//  4. 单条失败不中断，继续写剩下的，最后返回最后一个错误。
func SeedDemoData(repo repository.TraceRepository, force bool) (int, error) {
	if repo == nil {
		return 0, errors.New("nil trace repository")
	}

	if !force {
		// 只探总数，不取行：PageSize 取最小值即可。
		if _, total, err := repo.ListTraces(model.TraceFilter{PageSize: 1}); err != nil {
			return 0, fmt.Errorf("count traces before seeding: %w", err)
		} else if total > 0 {
			return 0, nil
		}
	}

	now := time.Now()
	stamp := now.Format("20060102150405")
	inserted := 0
	var lastErr error

	for i, d := range demoTraces() {
		if len(d.events) == 0 {
			continue
		}

		start := now.Add(-d.ago)
		last := d.events[len(d.events)-1]
		end := start.Add(time.Duration(last.offsetMs) * time.Millisecond)
		traceID := fmt.Sprintf("demo-%s-%02d", stamp, i+1)

		trace := &model.Trace{
			TraceID:      traceID,
			ServiceName:  d.service,
			Status:       d.status,
			StartTime:    start,
			EndTime:      end,
			DurationMs:   end.Sub(start).Milliseconds(),
			HasError:     d.status == model.TraceStatusError || d.status == model.TraceStatusTimeout,
			ErrorMessage: d.errorMessage,
			EventCount:   len(d.events),
			CreatedAt:    end,
			UpdatedAt:    end,
		}
		if err := repo.CreateTrace(trace); err != nil {
			lastErr = fmt.Errorf("create demo trace %s: %w", traceID, err)
			continue
		}

		events := make([]model.TraceEvent, 0, len(d.events))
		for _, de := range d.events {
			ts := start.Add(time.Duration(de.offsetMs) * time.Millisecond)
			events = append(events, model.TraceEvent{
				TraceID:      traceID,
				SpanID:       de.spanID,
				ParentSpanID: de.parentSpanID,
				Timestamp:    ts,
				Level:        de.level,
				Module:       de.module,
				Event:        de.event,
				Message:      de.message,
				Params:       de.params,
				ErrorMessage: de.errorMessage,
				// CreatedAt 跟着事件时间一起回填，避免"3 小时前的事件，写入时间是刚才"。
				CreatedAt: ts,
			})
		}
		if err := repo.CreateEvents(events); err != nil {
			lastErr = fmt.Errorf("create demo events %s: %w", traceID, err)
			continue
		}

		inserted++
	}

	return inserted, lastErr
}
