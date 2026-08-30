package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"
)

// emit 依次打出四个级别各一条日志，并返回日志目录。
func emit(t *testing.T, opts Options) string {
	t.Helper()
	dir := t.TempDir()
	opts.Dir = dir
	opts.DisableConsole = true // 测试里不要污染 go test 输出
	if err := Init(opts); err != nil {
		t.Fatalf("init logger: %v", err)
	}
	// 释放文件句柄，否则 Windows 上临时目录删不掉。
	t.Cleanup(Close)

	Debug("debug line")
	Info("info line")
	Warn("warn line")
	Error("error line")
	Sync()

	return dir
}

func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "" // 文件没建出来等同于空内容，交给断言去判断
	}
	return string(data)
}

// TestSplitModeWritesOneLevelPerFile split 模式下文件之间不能重叠。
func TestSplitModeWritesOneLevelPerFile(t *testing.T) {
	dir := emit(t, Options{Level: "debug", Mode: ModeSplit})

	for _, name := range []string{"debug.log", "info.log", "warn.log", "error.log"} {
		if readLog(t, dir, name) == "" {
			t.Fatalf("%s should exist in split mode", name)
		}
	}

	want := map[string]string{
		"debug.log": "debug",
		"info.log":  "info",
		"warn.log":  "warn",
		"error.log": "error",
	}
	for name, lvl := range want {
		got := readLog(t, dir, name)
		assertOnlyLevel(t, name, got, lvl)
	}
}

// TestRangeModeStacksLevels range 模式下高级别日志会同时出现在低级别文件里。
func TestRangeModeStacksLevels(t *testing.T) {
	dir := emit(t, Options{Level: "debug", Mode: ModeRange})

	// warn.log 里是 warn 及以上，error.log 里只剩 error。
	assertContains(t, "warn.log", readLog(t, dir, "warn.log"), "warn line", "error line")
	assertContains(t, "error.log", readLog(t, dir, "error.log"), "error line")
	assertNotContains(t, "error.log", readLog(t, dir, "error.log"), "warn line")
	assertNotContains(t, "warn.log", readLog(t, dir, "warn.log"), "info line")
}

// TestSingleModeUsesOneFile single 模式下只有一个 app.log。
func TestSingleModeUsesOneFile(t *testing.T) {
	dir := emit(t, Options{Level: "info", Mode: ModeSingle})

	got := readLog(t, dir, singleFileName)
	assertContains(t, singleFileName, got, "info line", "warn line", "error line")
	// 阈值 info 之下不落盘。
	assertNotContains(t, singleFileName, got, "debug line")

	for _, name := range []string{"info.log", "warn.log", "error.log"} {
		if readLog(t, dir, name) != "" {
			t.Errorf("%s must not be created in single mode", name)
		}
	}
}

// TestLevelsWhitelist 白名单优先级高于 level 阈值，只要列出的级别。
func TestLevelsWhitelist(t *testing.T) {
	dir := emit(t, Options{Level: "debug", Levels: []string{"warn", "ERROR"}, Mode: ModeSplit})

	// 白名单之外的级别既不落盘也不建文件。
	for _, name := range []string{"debug.log", "info.log"} {
		if readLog(t, dir, name) != "" {
			t.Errorf("%s must not be created when filtered out by levels", name)
		}
	}
	assertContains(t, "warn.log", readLog(t, dir, "warn.log"), "warn line")
	assertContains(t, "error.log", readLog(t, dir, "error.log"), "error line")
}

// TestLevelsWhitelistSingleMode 白名单 + single：一个文件里只有关心的级别。
func TestLevelsWhitelistSingleMode(t *testing.T) {
	dir := emit(t, Options{Level: "debug", Levels: []string{"error"}, Mode: ModeSingle})

	got := readLog(t, dir, singleFileName)
	assertContains(t, singleFileName, got, "error line")
	assertNotContains(t, singleFileName, got, "debug line", "info line", "warn line")
}

// TestInvalidModeRejected 模式写错必须在启动阶段报错，而不是悄悄退化成默认模式。
func TestInvalidModeRejected(t *testing.T) {
	if err := Init(Options{Dir: t.TempDir(), Mode: "verbose", DisableConsole: true}); err == nil {
		t.Fatal("invalid mode should be rejected")
	}
	if err := Init(Options{Dir: t.TempDir(), Level: "verbose"}); err == nil {
		t.Fatal("invalid level should be rejected")
	}
	if err := Init(Options{Dir: t.TempDir(), Levels: []string{"verbose"}}); err == nil {
		t.Fatal("invalid whitelist level should be rejected")
	}
}

// TestLevelThresholdSkipsLowerFiles 阈值提高时不该留下空文件。
func TestLevelThresholdSkipsLowerFiles(t *testing.T) {
	dir := emit(t, Options{Level: "warn", Mode: ModeSplit})

	for _, name := range []string{"debug.log", "info.log"} {
		if readLog(t, dir, name) != "" {
			t.Errorf("%s must not be created when level is warn", name)
		}
	}
	assertContains(t, "warn.log", readLog(t, dir, "warn.log"), "warn line")
}

// TestFatalFallsIntoErrorFile fatal 归入 error，不额外产生 fatal.log。
func TestFatalFallsIntoErrorFile(t *testing.T) {
	levels, err := resolveLevels(Options{Level: "debug"})
	if err != nil {
		t.Fatalf("resolveLevels: %v", err)
	}
	if len(levels) != len(fileLevels) {
		t.Fatalf("levels = %v, want %v", levels, fileLevels)
	}
	if normalize(zapcore.FatalLevel) != zapcore.ErrorLevel {
		t.Error("fatal must be normalized to error")
	}
	if !exactLevel(zapcore.ErrorLevel).Enabled(zapcore.FatalLevel) {
		t.Error("error.log should also receive fatal entries")
	}
}

func assertOnlyLevel(t *testing.T, name, got, lvl string) {
	t.Helper()
	if !contains(got, lvl+" line") {
		t.Errorf("%s missing %q entry: %q", name, lvl, got)
	}
	for _, other := range []string{"debug line", "info line", "warn line", "error line"} {
		if other == lvl+" line" {
			continue
		}
		if contains(got, other) {
			t.Errorf("%s should not contain %q: %q", name, other, got)
		}
	}
}

func assertContains(t *testing.T, name, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !contains(got, p) {
			t.Errorf("%s missing %q: %q", name, p, got)
		}
	}
}

func assertNotContains(t *testing.T, name, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if contains(got, p) {
			t.Errorf("%s should not contain %q: %q", name, p, got)
		}
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
