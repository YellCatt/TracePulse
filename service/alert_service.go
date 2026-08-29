// Package service 告警服务。
//
// 目标：链路出问题时，一封邮件把完整链路内容送到眼前，邮件里的 trace_id 点开就是
// 网页详情，不用再去服务器上翻日志猜。
//
// 工程上的约束（血泪教训）：
//  1. 告警绝不能拖垮采集 —— 全程异步，队列满了直接丢告警；
//  2. 告警绝不能炸群 —— 同链路同状态去重 + 全局最小发送间隔，故障风暴时也要守住收件箱；
//  3. 告警内容必须转义 —— 链路里的 message / params 来自业务，直接拼 HTML 会导致
//     邮件渲染错乱甚至注入，这里统一走 html/template 自动转义。
package service

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/example/tracepulse/config"
	"github.com/example/tracepulse/logger"
	"github.com/example/tracepulse/model"
	"github.com/example/tracepulse/view"
	"go.uber.org/zap"
)

// AlertService 告警接口。所有方法都必须是非阻塞的。
type AlertService interface {
	// AlertOnTrace 链路落盘后判定是否需要告警。由 trace service 调用。
	AlertOnTrace(trace *model.Trace, events []model.TraceEvent)
	// AlertOnQueueDrop 事件队列溢出时调用，内部会做窗口聚合，不会一条事件一封邮件。
	AlertOnQueueDrop(droppedCount int)
	// Shutdown 停止告警协程，等待已入队告警发完。
	Shutdown()
}

const (
	jobKindTrace     = "trace"
	jobKindQueueDrop = "queue_drop"

	// queueDropAggWindow 队列丢弃告警的聚合窗口，窗口内只发一封汇总邮件。
	queueDropAggWindow = 30 * time.Second

	// dedupMaxEntries 去重表上限，超过后清理过期项，防止常驻内存无限增长。
	dedupMaxEntries = 20000
)

type alertJob struct {
	kind  string
	trace *model.Trace
	// events 是本次要渲染的事件；omitted 是被截断掉、没放进来的条数。
	// 两者分开保存，邮件里才能如实告诉阅读者"还省略了多少条"。
	events  []model.TraceEvent
	omitted int

	dropCount int
}

type alertService struct {
	cfg config.AlertConfig

	// sender 发信实现。默认走 SMTP，测试可替换为录制桩，避免依赖真实邮件服务器。
	sender func(cfg config.AlertConfig, subject, htmlBody, textBody string) error

	jobs     chan alertJob
	quit     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once

	mu        sync.Mutex
	dedup     map[string]time.Time
	dropCount int
	dropTimer *time.Timer
}

func NewAlertService(cfg config.AlertConfig) AlertService {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	s := &alertService{
		cfg:    cfg,
		sender: sendMailViaSMTP,
		jobs:   make(chan alertJob, cfg.QueueSize),
		quit:   make(chan struct{}),
		dedup:  make(map[string]time.Time),
	}

	s.wg.Add(1)
	go s.worker()

	return s
}

// ---------------------------------------------------------------- 触发判定 ----

// AlertOnTrace 判定并投递链路告警。
func (s *alertService) AlertOnTrace(trace *model.Trace, events []model.TraceEvent) {
	if !s.cfg.Enabled || trace == nil {
		return
	}

	reason := s.matchTrigger(trace)
	if reason == "" {
		return
	}

	// 同一条链路、同一个原因，在去重窗口内只发一次。
	if !s.claim(trace.TraceID + "|" + reason) {
		logger.Debug("alert suppressed by dedup",
			zap.String("trace_id", trace.TraceID), zap.String("reason", reason))
		return
	}

	evs := events
	omitted := 0
	if len(evs) > s.cfg.MaxEventsInMail {
		// 邮件里放不下就保留头尾，中间省略，详情仍然去网页看。
		head := s.cfg.MaxEventsInMail / 2
		tail := s.cfg.MaxEventsInMail - head
		merged := make([]model.TraceEvent, 0, s.cfg.MaxEventsInMail)
		merged = append(merged, evs[:head]...)
		merged = append(merged, evs[len(evs)-tail:]...)
		omitted = len(evs) - len(merged)
		evs = merged
	}

	s.enqueue(alertJob{kind: jobKindTrace, trace: trace, events: evs, omitted: omitted})
}

// matchTrigger 返回命中的告警原因，未命中返回空串。
func (s *alertService) matchTrigger(trace *model.Trace) string {
	for _, trigger := range s.cfg.Triggers {
		switch trigger {
		case model.TriggerError, model.TriggerWarn, model.TriggerTimeout:
			if trace.Status == trigger {
				return trigger
			}
		case model.TriggerSlow:
			if s.cfg.SlowThresholdMs > 0 && trace.DurationMs >= s.cfg.SlowThresholdMs {
				return model.TriggerSlow
			}
		}
	}
	return ""
}

// dedupSeconds 取去重窗口秒数，未配置时回落到 300 秒。
func dedupSeconds(v *int) int {
	if v == nil {
		return 300
	}
	return *v
}

// claim 抢占一次告警名额，窗口内重复调用返回 false。
func (s *alertService) claim(key string) bool {
	now := time.Now()
	window := time.Duration(dedupSeconds(s.cfg.DedupSeconds)) * time.Second

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.dedup) > dedupMaxEntries {
		for k, t := range s.dedup {
			if now.Sub(t) > window {
				delete(s.dedup, k)
			}
		}
	}
	if t, ok := s.dedup[key]; ok && now.Sub(t) < window {
		return false
	}
	s.dedup[key] = now
	return true
}

// AlertOnQueueDrop 记录一次队列丢弃，并在聚合窗口结束时汇总成一封邮件。
func (s *alertService) AlertOnQueueDrop(droppedCount int) {
	if !s.cfg.Enabled || droppedCount <= 0 {
		return
	}
	if !s.hasTrigger(model.TriggerQueueDrop) {
		return
	}

	s.mu.Lock()
	s.dropCount += droppedCount
	if s.dropTimer == nil {
		s.dropTimer = time.AfterFunc(queueDropAggWindow, s.flushQueueDrop)
	}
	s.mu.Unlock()
}

// flushQueueDrop 汇总窗口内的丢弃事件，发一封邮件。
func (s *alertService) flushQueueDrop() {
	s.mu.Lock()
	n := s.dropCount
	s.dropCount = 0
	s.dropTimer = nil
	s.mu.Unlock()

	if n <= 0 {
		return
	}
	s.enqueue(alertJob{kind: jobKindQueueDrop, dropCount: n})
}

func (s *alertService) hasTrigger(trigger string) bool {
	for _, t := range s.cfg.Triggers {
		if t == trigger {
			return true
		}
	}
	return false
}

// enqueue 非阻塞投递，队列满则丢弃告警。
func (s *alertService) enqueue(job alertJob) {
	select {
	case s.jobs <- job:
	case <-s.quit:
	default:
		logger.Warn("alert queue full, dropping alert",
			zap.String("kind", job.kind))
	}
}

// ------------------------------------------------------------------ worker ----

func (s *alertService) worker() {
	defer s.wg.Done()

	minInterval := time.Duration(s.cfg.MinIntervalSeconds) * time.Second
	var lastSent time.Time

	for {
		select {
		case <-s.quit:
			// 关闭前把已入队的告警尽量发完。
			for {
				select {
				case job := <-s.jobs:
					s.handle(job)
				default:
					return
				}
			}
		case job := <-s.jobs:
			// 全局限流：故障风暴时也要守住收件箱和 SMTP 账号。
			if !lastSent.IsZero() && minInterval > 0 {
				if wait := minInterval - time.Since(lastSent); wait > 0 {
					select {
					case <-time.After(wait):
					case <-s.quit:
						// 关闭时不再等待限流窗口，直接把剩下的告警发出去。
						// 否则停机瞬间的故障信号会恰好丢在等待窗口里。
					}
				}
			}
			s.handle(job)
			lastSent = time.Now()
		}
	}
}

func (s *alertService) handle(job alertJob) {
	var subject, htmlBody, textBody string

	switch job.kind {
	case jobKindTrace:
		subject, htmlBody, textBody = renderTraceMail(job.trace, job.events, job.omitted, s.cfg)
	default:
		subject, htmlBody, textBody = renderQueueDropMail(job.dropCount)
	}

	if err := s.sender(s.cfg, subject, htmlBody, textBody); err != nil {
		logger.Error("failed to send alert email",
			zap.String("kind", job.kind),
			zap.String("subject", subject),
			zap.Error(err),
		)
		return
	}
	logger.Info("alert email sent",
		zap.String("kind", job.kind),
		zap.String("subject", subject),
	)
}

// Shutdown 停止告警协程。
func (s *alertService) Shutdown() {
	s.stopOnce.Do(func() {
		s.flushQueueDrop()
		close(s.quit)
		s.wg.Wait()
	})
}

// ------------------------------------------------------------ 邮件内容渲染 ----

type mailEvent struct {
	Idx       int
	Time      string
	Offset    string // 相对链路起点的偏移，例如 +128ms
	Level     string
	Module    string
	Event     string
	Message   string
	Params    string
	ErrorMsg  string
	Highlight bool
}

type mailData struct {
	TraceID    string
	Status     string
	Service    string
	Duration   string
	EventCount int
	ErrorMsg   string
	StartAt    string
	EndAt      string
	URL        string
	Reason     string
	Events     []mailEvent
	ShownCount int
	Hidden     int
	LevelStats []levelStat
}

type levelStat struct {
	Level string
	Count int
}

// renderTraceMail 生成链路告警邮件的主题、HTML 与纯文本正文。
//
// omitted 是调用方为控制邮件体积而截掉的事件条数，必须如实展示给阅读者，
// 否则他会误以为看到的就是完整链路，从而漏掉中间的异常步骤。
func renderTraceMail(trace *model.Trace, events []model.TraceEvent, omitted int, cfg config.AlertConfig) (string, string, string) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })

	hidden := omitted
	if trace.EventCount > len(events)+omitted {
		hidden += trace.EventCount - len(events) - omitted
	}

	data := mailData{
		TraceID:    trace.TraceID,
		Status:     trace.Status,
		Service:    trace.ServiceName,
		Duration:   view.FormatDuration(trace.DurationMs),
		EventCount: trace.EventCount,
		ErrorMsg:   trace.ErrorMessage,
		StartAt:    view.FormatTime(trace.StartTime),
		EndAt:      view.FormatTime(trace.EndTime),
		URL:        fmt.Sprintf("%s/trace/%s", strings.TrimRight(cfg.PublicURL, "/"), trace.TraceID),
		ShownCount: len(events),
		Hidden:     hidden,
	}

	counts := make(map[string]int)
	for i, e := range events {
		off := e.Timestamp.Sub(trace.StartTime).Milliseconds()
		data.Events = append(data.Events, mailEvent{
			Idx:       i + 1,
			Time:      view.FormatClock(e.Timestamp),
			Offset:    view.FormatOffset(off),
			Level:     e.Level,
			Module:    e.Module,
			Event:     e.Event,
			Message:   e.Message,
			Params:    formatParamsText(e.Params),
			ErrorMsg:  e.ErrorMessage,
			Highlight: e.Level == model.LevelError || e.Level == model.LevelFatal,
		})
		counts[e.Level]++
	}
	for lv, n := range counts {
		data.LevelStats = append(data.LevelStats, levelStat{Level: lv, Count: n})
	}
	sort.Slice(data.LevelStats, func(i, j int) bool { return data.LevelStats[i].Count > data.LevelStats[j].Count })

	subject := fmt.Sprintf("[Trace Alert][%s] %s", strings.ToUpper(trace.Status), trace.TraceID)
	if trace.ServiceName != "" {
		subject = fmt.Sprintf("[Trace Alert][%s] %s - %s", strings.ToUpper(trace.Status), trace.ServiceName, trace.TraceID)
	}

	return subject, execTemplate(traceMailHTML, data), execTemplate(traceMailText, data)
}

// renderer 同时适配 html/template 与 text/template。
type renderer interface {
	Execute(w io.Writer, data interface{}) error
}

func renderQueueDropMail(dropped int) (string, string, string) {
	data := struct {
		Count int
		At    string
	}{dropped, view.FormatTime(time.Now())}

	subject := fmt.Sprintf("[Trace Alert][QUEUE_DROP] %d events dropped", dropped)
	return subject, execTemplate(dropMailHTML, data), execTemplate(dropMailText, data)
}

// execTemplate 渲染邮件模板。HTML 版本走 html/template，自动转义业务内容。
func execTemplate(tmpl renderer, data interface{}) string {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		logger.Error("failed to render alert template", zap.Error(err))
		return ""
	}
	return buf.String()
}

// formatParamsText 把 JSON 参数渲染成紧凑的 k=v 文本，用于纯文本邮件与客户端预览。
func formatParamsText(params string) string {
	var e model.TraceEvent
	e.Params = params
	list := e.ParamsList()
	if len(list) == 0 {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, kv := range list {
		if kv.Key == "" {
			parts = append(parts, kv.Value)
			continue
		}
		parts = append(parts, kv.Key+"="+kv.Value)
	}
	return strings.Join(parts, " ")
}

// -------------------------------------------------------------- SMTP 发送 ----

// sendMailViaSMTP 通过 SMTP 发送一封 multipart/alternative 邮件（纯文本 + HTML）。
//
// 同时提供 text/plain 与 text/html 两个版本，既保证手机邮件客户端能看，
// 也显著降低被判垃圾邮件的概率。
func sendMailViaSMTP(cfg config.AlertConfig, subject, htmlBody, textBody string) error {
	if len(cfg.Recipients) == 0 {
		return fmt.Errorf("no recipients configured")
	}
	if cfg.SMTPHost == "" {
		return fmt.Errorf("smtp host not configured")
	}

	msg, err := buildMessage(cfg.SMTPFrom, cfg.Recipients, subject, htmlBody, textBody)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(cfg.SMTPHost, fmt.Sprintf("%d", cfg.SMTPPort))
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)

	tlsCfg := &tls.Config{
		ServerName:         cfg.SMTPHost,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}

	var conn net.Conn
	if cfg.UseTLS {
		dialer := &net.Dialer{Timeout: timeout}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = net.DialTimeout("tcp", addr, timeout)
	}
	if err != nil {
		return fmt.Errorf("dial smtp %s: %w", addr, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	client, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("new smtp client: %w", err)
	}
	defer client.Close()

	if err := client.Hello("localhost"); err != nil {
		return fmt.Errorf("smtp helo: %w", err)
	}

	// 587 端口通常是明文 + STARTTLS 升级。
	if cfg.StartTLS && !cfg.UseTLS {
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	// 内网中继可能不需要认证，用户名留空即跳过。
	if cfg.SMTPUser != "" {
		auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	from := cfg.SMTPFrom
	if from == "" {
		from = cfg.SMTPUser
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range cfg.Recipients {
		if rcpt == "" {
			continue
		}
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt to %s: %w", rcpt, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("write mail body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close mail body: %w", err)
	}

	return client.Quit()
}

// buildMessage 组装 RFC5322 报文。
func buildMessage(from string, to []string, subject, htmlBody, textBody string) ([]byte, error) {
	boundary := fmt.Sprintf("=_b%d_%s", time.Now().UnixNano(), randomBoundary(12))

	var buf bytes.Buffer
	buf.WriteString("From: " + from + "\r\n")
	buf.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	buf.WriteString("Subject: " + encodeRFC2047(subject) + "\r\n")
	buf.WriteString("Date: " + time.Now().Format("Mon, 02 Jan 2006 15:04:05 -0700") + "\r\n")
	buf.WriteString(fmt.Sprintf("Message-ID: <%d.%s@tracepulse>\r\n", time.Now().UnixNano(), randomBoundary(8)))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=%q\r\n", boundary))
	buf.WriteString("\r\n")

	writePart := func(contentType, body string) error {
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: " + contentType + "; charset=\"UTF-8\"\r\n")
		buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
		buf.WriteString("\r\n")
		w := quotedprintable.NewWriter(&buf)
		if _, err := w.Write([]byte(body)); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		buf.WriteString("\r\n")
		return nil
	}

	if err := writePart("text/plain", textBody); err != nil {
		return nil, err
	}
	if err := writePart("text/html", htmlBody); err != nil {
		return nil, err
	}

	buf.WriteString("--" + boundary + "--\r\n")
	return buf.Bytes(), nil
}

// encodeRFC2047 把非 ASCII 主题（中文）编码成 RFC2047 形式。
func encodeRFC2047(s string) string {
	if s == "" || isASCII(s) {
		return s
	}
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func randomBoundary(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 退化路径：随机源不可用时用时间戳兜底，边界串只影响 MIME 分隔，不影响功能。
		seed := time.Now().UnixNano()
		for i := range b {
			seed = seed*6364136223846793005 + 1442695040888963407
			b[i] = alphabet[int(uint64(seed)>>33)%len(alphabet)]
		}
		return string(b)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
