// Package view 展示层公共格式化工具，被 HTML 页面与告警邮件共用，
// 保证「邮件里看到的时间」和「网页上看到的时间」完全一致。
package view

import (
	"fmt"
	"time"
)

// Loc 展示用固定时区（东八区）。容器内 TZ 常常是 UTC，显式固定避免时间对不上。
var Loc = time.FixedZone("CST", 8*60*60)

const (
	layoutFull   = "2006-01-02 15:04:05.000"
	layoutClock  = "15:04:05.000"
	layoutMinute = "2006-01-02 15:04"
)

// FormatTime 完整时间。
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(Loc).Format(layoutFull)
}

// FormatClock 仅时分秒毫秒，用于链路时间线。
func FormatClock(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(Loc).Format(layoutClock)
}

// FormatMinute 分钟精度，用于列表页。
func FormatMinute(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(Loc).Format(layoutMinute)
}

// FormatDuration 把毫秒渲染成人类可读耗时。
//
// 全程整数运算、向下截断，不用浮点格式化：浮点四舍五入会把 59999ms 显示成
// "60.00s"、把 119999ms 显示成 "2m0.0s"，看起来像跨了一档，排查时极易误判。
func FormatDuration(ms int64) string {
	switch {
	case ms < 0:
		return "-"
	case ms < 1_000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%d.%02ds", ms/1_000, (ms%1_000)/10)
	case ms < 3_600_000:
		rem := ms % 60_000
		return fmt.Sprintf("%dm%d.%ds", ms/60_000, rem/1_000, (rem%1_000)/100)
	default:
		rem := ms % 3_600_000
		return fmt.Sprintf("%dh%d.%dm", ms/3_600_000, rem/60_000, (rem%60_000)/6_000)
	}
}

// FormatOffset 相对链路起点的偏移，例如 +128ms / -1.20s。
func FormatOffset(ms int64) string {
	if ms == 0 {
		return "0ms"
	}
	if ms > 0 {
		return "+" + FormatDuration(ms)
	}
	return "-" + FormatDuration(-ms)
}

// Truncate 长文本截断，尾部加省略号。按 rune 计数，中文不会切出乱码。
func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// IsLong 判断内容是否需要折叠展示。
func IsLong(s string, threshold int) bool {
	if threshold <= 0 {
		threshold = 120
	}
	return len([]rune(s)) > threshold
}
