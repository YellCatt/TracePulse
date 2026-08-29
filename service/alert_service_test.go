package service

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/tracepulse/config"
	"github.com/example/tracepulse/model"
)

// recordingSender 替换真实 SMTP，记录被投递的告警。
type recordingSender struct {
	mu       sync.Mutex
	subjects []string
	bodies   []string
	gate     chan struct{} // 非 nil 时阻塞发送，用于构造积压场景
}

func (r *recordingSender) send(_ config.AlertConfig, subject, html, text string) error {
	if r.gate != nil {
		<-r.gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subjects = append(r.subjects, subject)
	r.bodies = append(r.bodies, html)
	return nil
}

func (r *recordingSender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subjects)
}

func (r *recordingSender) lastBody() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return ""
	}
	return r.bodies[len(r.bodies)-1]
}

func (r *recordingSender) allSubjects() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.subjects))
	copy(out, r.subjects)
	return out
}

// newTestAlertService 构造一个用 recordingSender 替代 SMTP 的告警服务。
func newTestAlertService(t *testing.T, cfg config.AlertConfig) (*alertService, *recordingSender) {
	t.Helper()

	cfg.Enabled = true
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	if cfg.MaxEventsInMail <= 0 {
		cfg.MaxEventsInMail = 100
	}
	if cfg.MinIntervalSeconds <= 0 {
		cfg.MinIntervalSeconds = 0
	}
	if cfg.DedupSeconds == nil {
		cfg.DedupSeconds = intPtrForTest(300)
	}
	if len(cfg.Recipients) == 0 {
		cfg.Recipients = []string{"ops@example.com"}
	}

	sender := &recordingSender{}
	s := NewAlertService(cfg).(*alertService)
	s.sender = sender.send
	return s, sender
}

func intPtrForTest(v int) *int { return &v }

func testTrace(id, status string) *model.Trace {
	return &model.Trace{
		TraceID:      id,
		Status:       status,
		ServiceName:  "svc",
		StartTime:    time.Now().Add(-time.Second),
		EndTime:      time.Now(),
		DurationMs:   1000,
		HasError:     status == model.TraceStatusError,
		ErrorMessage: "boom",
		EventCount:   1,
	}
}

// waitFor 轮询等待条件成立，避免测试依赖 goroutine 调度时序。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestAlertFiresOnConfiguredTrigger 配置的触发条件命中时必须发信。
func TestAlertFiresOnConfiguredTrigger(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers: []string{model.TriggerError},
	})
	defer svc.Shutdown()

	svc.AlertOnTrace(testTrace("t-1", model.TraceStatusError), nil)

	waitFor(t, 3*time.Second, func() bool { return sender.count() == 1 },
		"expected 1 alert for an error trace")

	subjects := sender.allSubjects()
	if !strings.Contains(subjects[0], "t-1") {
		t.Errorf("subject should contain trace_id, got %q", subjects[0])
	}
}

// TestAlertIgnoresNonMatchingStatus 未配置的触发条件不该发信，
// 否则正常链路也会灌满收件箱。
func TestAlertIgnoresNonMatchingStatus(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers: []string{model.TriggerError},
	})
	defer svc.Shutdown()

	svc.AlertOnTrace(testTrace("t-ok", model.TraceStatusOK), nil)
	svc.AlertOnTrace(testTrace("t-warn", model.TraceStatusWarn), nil)
	svc.Shutdown()

	if sender.count() != 0 {
		t.Fatalf("sent %d alerts, want 0 for non-matching statuses", sender.count())
	}
}

// TestAlertDedupSuppressesRepeat 同一条链路反复落盘（长链路分批写）时，
// 去重必须生效，否则一封告警会被复制成十几封。
func TestAlertDedupSuppressesRepeat(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers:     []string{model.TriggerError},
		DedupSeconds: intPtrForTest(300),
	})
	defer svc.Shutdown()

	for i := 0; i < 10; i++ {
		svc.AlertOnTrace(testTrace("t-dup", model.TraceStatusError), nil)
	}
	svc.Shutdown()

	if got := sender.count(); got != 1 {
		t.Fatalf("sent %d alerts for the same trace, want 1 (dedup broken)", got)
	}
}

// TestAlertDedupDistinguishesStatuses 同一条链路状态变化后应当允许再次告警，
// 先 warn 再 error 是有意义的状态升级。
func TestAlertDedupDistinguishesStatuses(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers:     []string{model.TriggerWarn, model.TriggerError},
		DedupSeconds: intPtrForTest(300),
	})
	defer svc.Shutdown()

	svc.AlertOnTrace(testTrace("t-esc", model.TraceStatusWarn), nil)
	svc.AlertOnTrace(testTrace("t-esc", model.TraceStatusError), nil)
	svc.Shutdown()

	if got := sender.count(); got != 2 {
		t.Fatalf("sent %d alerts, want 2 (warn then error should both alert)", got)
	}
}

// TestAlertDisabledSendsNothing 告警关闭时必须彻底静默。
func TestAlertDisabledSendsNothing(t *testing.T) {
	cfg := config.AlertConfig{Enabled: false, Triggers: []string{model.TriggerError}}
	svc := NewAlertService(cfg).(*alertService)
	sender := &recordingSender{}
	svc.sender = sender.send
	_ = sender
	defer svc.Shutdown()

	svc.AlertOnTrace(testTrace("t-1", model.TraceStatusError), nil)
	svc.AlertOnQueueDrop(5)
	svc.Shutdown()

	if sender.count() != 0 {
		t.Fatalf("sent %d alerts while disabled, want 0", sender.count())
	}
}

// TestQueueDropAggregatesIntoSingleMail 队列持续溢出时，
// 不能一条事件一封邮件，必须聚合成一封汇总。
func TestQueueDropAggregatesIntoSingleMail(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers: []string{model.TriggerQueueDrop},
	})
	defer svc.Shutdown()

	for i := 0; i < 50; i++ {
		svc.AlertOnQueueDrop(1)
	}

	// 聚合窗口是 30s，这里直接触发 flush 而不是等，测试不该睡 30 秒。
	svc.flushQueueDrop()
	svc.Shutdown()

	if got := sender.count(); got != 1 {
		t.Fatalf("sent %d alerts for 50 drops, want 1 aggregated mail", got)
	}
	body := sender.lastBody()
	if !strings.Contains(body, "50") {
		t.Errorf("aggregated mail should report total dropped count (50), body: %s", body)
	}
}

// TestQueueDropSkippedWhenNotTriggered 未配置 queue_drop 时丢弃事件不该发信。
func TestQueueDropSkippedWhenNotTriggered(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers: []string{model.TriggerError},
	})
	defer svc.Shutdown()

	svc.AlertOnQueueDrop(10)
	svc.flushQueueDrop()
	svc.Shutdown()

	if sender.count() != 0 {
		t.Fatalf("sent %d queue-drop alerts without the trigger configured", sender.count())
	}
}

// TestAlertQueueFullDropsAlertsRatherThanBlocking 告警队列满时必须丢弃告警，
// 绝不能反过来把落盘流程卡住 —— 宁可少收一封邮件，也不能丢采集数据。
func TestAlertQueueFullDropsAlertsRatherThanBlocking(t *testing.T) {
	svc, _ := newTestAlertService(t, config.AlertConfig{
		Triggers:  []string{model.TriggerError},
		QueueSize: 2,
	})

	done := make(chan struct{})
	go func() {
		// 远多于队列容量的告警，必须立刻返回而不是阻塞。
		for i := 0; i < 200; i++ {
			svc.AlertOnTrace(testTrace("t-flood", model.TraceStatusError), nil)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("AlertOnTrace blocked when alert queue was full")
	}
	svc.Shutdown()
}

// TestMailContainsFullTimeline 邮件正文要带完整时间线，
// 让人不用点开链接也能大致判断问题。
func TestMailContainsFullTimeline(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers: []string{model.TriggerError},
	})
	defer svc.Shutdown()

	base := time.Now().Add(-time.Second)
	events := []model.TraceEvent{
		{TraceID: "t-1", Timestamp: base, Level: model.LevelInfo, Module: "order", Event: "start", Message: "begin"},
		{TraceID: "t-1", Timestamp: base.Add(500 * time.Millisecond), Level: model.LevelWarn, Module: "payment", Event: "retry", Message: "timeout"},
		{TraceID: "t-1", Timestamp: base.Add(time.Second), Level: model.LevelError, Module: "payment", Event: "pay_failed", Message: "failed", ErrorMessage: "kaput"},
	}

	svc.AlertOnTrace(testTrace("t-1", model.TraceStatusError), events)
	svc.Shutdown()

	body := sender.lastBody()
	for _, want := range []string{"t-1", "order", "payment", "pay_failed", "kaput", "timeout"} {
		if !strings.Contains(body, want) {
			t.Errorf("mail body missing %q", want)
		}
	}
}

// TestMailEscapesHostileContent 链路内容由业务上报，可能包含尖括号。
// 邮件里必须转义，否则一条日志就能破坏邮件结构甚至注入。
func TestMailEscapesHostileContent(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers: []string{model.TriggerError},
	})
	defer svc.Shutdown()

	events := []model.TraceEvent{
		{TraceID: "t-xss", Timestamp: time.Now(), Level: model.LevelError, Module: "m",
			Event: "boom", Message: "<script>alert(1)</script>"},
	}
	tr := testTrace("t-xss", model.TraceStatusError)
	tr.ErrorMessage = "<img src=x onerror=alert(2)>"

	svc.AlertOnTrace(tr, events)
	svc.Shutdown()

	body := sender.lastBody()
	for _, dangerous := range []string{"<script>alert(1)</script>", "<img src=x onerror=alert(2)>"} {
		if strings.Contains(body, dangerous) {
			t.Fatalf("unescaped content in alert mail: %s", dangerous)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("expected escaped content in mail body")
	}
}

// TestMailTruncatesHugeTimeline 超长链路要截断，避免生成几 MB 的邮件被 SMTP 拒收。
func TestMailTruncatesHugeTimeline(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers:        []string{model.TriggerError},
		MaxEventsInMail: 20,
	})
	defer svc.Shutdown()

	base := time.Now().Add(-time.Minute)
	events := make([]model.TraceEvent, 0, 500)
	for i := 0; i < 500; i++ {
		events = append(events, model.TraceEvent{
			TraceID: "t-long", Timestamp: base.Add(time.Duration(i) * time.Millisecond),
			Level: model.LevelInfo, Module: "m", Event: "tick",
		})
	}

	svc.AlertOnTrace(testTrace("t-long", model.TraceStatusError), events)
	svc.Shutdown()

	body := sender.lastBody()
	// 渲染出的事件行数应受 MaxEventsInMail 约束，而不是全部 500 条。
	if rows := strings.Count(body, "seq-cell"); false {
		_ = rows
	}
	if n := strings.Count(body, "<tr"); n > 40 {
		t.Fatalf("mail contains %d table rows, expected truncation around 20 events", n)
	}
	if !strings.Contains(body, "省略") && !strings.Contains(body, "omitted") {
		t.Error("truncated mail should tell the reader that some events were omitted")
	}
}

// TestAlertMinIntervalThrottles 全局最小发送间隔必须挡住告警风暴，
// 否则一次故障能把整个邮件组打挂。
func TestAlertMinIntervalThrottles(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers:           []string{model.TriggerError},
		MinIntervalSeconds: 1,
		DedupSeconds:       intPtrForTest(0), // 关掉去重，单独验证限流
	})
	defer svc.Shutdown()

	// 5 条不同 trace，若无限流会瞬间发 5 封
	for i := 0; i < 5; i++ {
		svc.AlertOnTrace(testTrace("t-throttle-"+string(rune('a'+i)), model.TraceStatusError), nil)
	}

	// 1 秒内最多 1 封
	time.Sleep(700 * time.Millisecond)
	if got := sender.count(); got > 1 {
		t.Fatalf("sent %d alerts within throttle window, want at most 1", got)
	}

	svc.Shutdown()
	if sender.count() != 5 {
		t.Fatalf("after shutdown sent %d alerts, want 5 (throttled but not lost)", sender.count())
	}
}

// TestSlowTriggerUsesThreshold 慢调用告警要按阈值触发。
func TestSlowTriggerUsesThreshold(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers:        []string{model.TriggerSlow},
		SlowThresholdMs: 1000,
	})
	defer svc.Shutdown()

	slow := testTrace("t-slow", model.TraceStatusOK)
	slow.DurationMs = 5000
	fast := testTrace("t-fast", model.TraceStatusOK)
	fast.DurationMs = 50

	svc.AlertOnTrace(slow, nil)
	svc.AlertOnTrace(fast, nil)
	svc.Shutdown()

	if got := sender.count(); got != 1 {
		t.Fatalf("sent %d alerts, want 1 (only the slow trace)", got)
	}
}

// TestShutdownFlushesPendingDrops 关闭前要把已累计的丢弃事件发出去，
// 否则"队列溢出"这个信号会在停机时丢失。
func TestShutdownFlushesPendingDrops(t *testing.T) {
	svc, sender := newTestAlertService(t, config.AlertConfig{
		Triggers: []string{model.TriggerQueueDrop},
	})
	svc.AlertOnQueueDrop(7)
	svc.Shutdown()

	if sender.count() != 1 {
		t.Fatalf("sent %d alerts on shutdown, want 1 pending drop alert", sender.count())
	}
}
