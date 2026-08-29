package view

import (
	"strings"
	"testing"
	"time"
)

// TestFormatTimeUsesFixedZone 邮件里看到的时间必须和网页上一致。
// 容器里 TZ 常常是 UTC，如果不固定时区，告警邮件会让人对着时间差排查半天。
func TestFormatTimeUsesFixedZone(t *testing.T) {
	// 2026-08-29 10:00:00 UTC = 2026-08-29 18:00:00 CST
	utc := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)

	got := FormatTime(utc)
	if !strings.HasPrefix(got, "2026-08-29 18:00:00") {
		t.Fatalf("FormatTime = %q, want CST (UTC+8) rendering", got)
	}
	if strings.HasPrefix(got, "2026-08-29 10:00:00") {
		t.Fatalf("FormatTime rendered in caller's local zone instead of fixed CST: %q", got)
	}
}

func TestFormatTimeZeroValue(t *testing.T) {
	if got := FormatTime(time.Time{}); got != "-" {
		t.Fatalf("FormatTime(zero) = %q, want %q", got, "-")
	}
	if got := FormatClock(time.Time{}); got != "-" {
		t.Fatalf("FormatClock(zero) = %q, want %q", got, "-")
	}
	if got := FormatMinute(time.Time{}); got != "-" {
		t.Fatalf("FormatMinute(zero) = %q, want %q", got, "-")
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{-1, "-"},
		{0, "0ms"},
		{999, "999ms"},
		{1000, "1.00s"},
		{1500, "1.50s"},
		{59_999, "59.99s"},
		{60_000, "1m0.0s"},
		{90_500, "1m30.5s"},
		{3_600_000, "1h0.0m"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.ms); got != c.want {
			t.Errorf("FormatDuration(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}

func TestFormatOffset(t *testing.T) {
	if got := FormatOffset(0); got != "0ms" {
		t.Errorf("FormatOffset(0) = %q, want 0ms", got)
	}
	if got := FormatOffset(128); got != "+128ms" {
		t.Errorf("FormatOffset(128) = %q, want +128ms", got)
	}
	if got := FormatOffset(-2000); got != "-2.00s" {
		t.Errorf("FormatOffset(-2000) = %q, want -2.00s", got)
	}
}

// TestTruncateKeepsMultibyteIntact 截断按 rune 计数，中文不能被切成半个字节。
func TestTruncateKeepsMultibyteIntact(t *testing.T) {
	s := "支付网关超时，第 1 次重试"
	got := Truncate(s, 5)
	if !strings.HasPrefix(got, "支付网关超") {
		t.Fatalf("Truncate = %q, want first 5 runes preserved", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("Truncate = %q, want ellipsis suffix", got)
	}
	// 结果必须是合法 UTF-8，不能出现替换字符
	if strings.ContainsRune(got, '�') {
		t.Fatalf("Truncate produced invalid UTF-8: %q", got)
	}
}

func TestTruncateShortInputUnchanged(t *testing.T) {
	if got := Truncate("short", 100); got != "short" {
		t.Fatalf("Truncate should not modify short input, got %q", got)
	}
	if got := Truncate("", 100); got != "" {
		t.Fatalf("Truncate empty = %q, want empty", got)
	}
}

func TestIsLong(t *testing.T) {
	if IsLong("abc", 2) != true {
		t.Error("expected long")
	}
	if IsLong("abc", 3) != false {
		t.Error("expected not long")
	}
	if IsLong("abc", 0) != false {
		t.Error("threshold 0 should fall back to default 120")
	}
}
