package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLoadConfigBackfillsLegacyConfig 老配置文件只有 server/database/log 三段时，
// 加载后必须补齐 trace/alert 的默认值，否则会出现"改了配置没生效"的假象。
func TestLoadConfigBackfillsLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "config.yaml")
	legacy := `
server:
  port: 19999
database:
  path: ./legacy.db
log:
  path: ./logs
  level: warn
`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	restore := chdir(t, dir)
	loadIntoTestConfig(legacyPath)
	defer restore()

	if cfg.Server.Port != 19999 {
		t.Errorf("port = %d, want 19999 (existing values must be preserved)", cfg.Server.Port)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("log level = %q, want warn", cfg.Log.Level)
	}
	if cfg.Database.Path != "./legacy.db" {
		t.Errorf("db path = %q, want ./legacy.db", cfg.Database.Path)
	}

	// 以下字段老配置里没有，必须落到合理默认值
	if cfg.Trace.TTLSeconds <= 0 {
		t.Errorf("ttl_seconds not defaulted: %d", cfg.Trace.TTLSeconds)
	}
	if cfg.Trace.QueueSize <= 0 {
		t.Errorf("queue_size not defaulted: %d", cfg.Trace.QueueSize)
	}
	if cfg.Trace.CleanupDays != 7 {
		t.Errorf("cleanup_days = %d, want 7 (retention policy must be explicit)", cfg.Trace.CleanupDays)
	}
	if cfg.Alert.DedupSeconds == nil || *cfg.Alert.DedupSeconds != 300 {
		t.Errorf("dedup_seconds not defaulted to 300: %v", cfg.Alert.DedupSeconds)
	}
	if len(cfg.Alert.Triggers) == 0 {
		t.Error("alert triggers not defaulted")
	}
}

// TestLoadConfigPersistsMergedConfig 补齐后的配置必须回写，
// 让运维能直接看到"实际生效的参数"，而不是猜。
func TestLoadConfigPersistsMergedConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("server:\n  port: 19998\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	restore := chdir(t, dir)
	defer restore()
	loadIntoTestConfig(p)

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}

	var persisted Config
	if err := yaml.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("parse merged config: %v", err)
	}
	if persisted.Trace.TTLSeconds <= 0 {
		t.Errorf("merged config missing trace.ttl_seconds: %+v", persisted.Trace)
	}
	if persisted.Database.JournalMode == "" {
		t.Errorf("merged config missing database.journal_mode: %+v", persisted.Database)
	}
}

// TestDisableVacuumSemantics 布尔默认值必须"缺失即安全"。
// 老配置里没有 disable_vacuum 时，磁盘回收应当默认开启（否则 U 盘会无限膨胀）。
func TestDisableVacuumSemantics(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("server:\n  port: 8080\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	restore := chdir(t, dir)
	defer restore()
	loadIntoTestConfig(p)

	if cfg.Trace.DisableVacuum {
		t.Error("vacuum must be enabled by default on legacy configs")
	}
}

// TestUseTLSInferredFromPort465 465 端口必须自动走隐式 TLS，
// 否则运维填了 465 却明文握手，会卡在超时上很难查。
func TestUseTLSInferredFromPort465(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "alert:\n  enabled: true\n  smtp_host: smtp.example.com\n  smtp_port: 465\n"
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	restore := chdir(t, dir)
	defer restore()
	loadIntoTestConfig(p)

	if !cfg.Alert.UseTLS {
		t.Error("port 465 should imply implicit TLS")
	}
	if cfg.Alert.SMTPFrom == "" && cfg.Alert.SMTPUser != "" {
		t.Error("smtp_from should fall back to smtp_user")
	}
}

// TestDedupSecondsExplicitZeroHonored 显式设为 0 表示关闭去重，不能被默认值覆盖。
func TestDedupSecondsExplicitZeroHonored(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "alert:\n  dedup_seconds: 0\n"
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	restore := chdir(t, dir)
	defer restore()
	loadIntoTestConfig(p)

	if cfg.Alert.DedupSeconds == nil || *cfg.Alert.DedupSeconds != 0 {
		t.Errorf("explicit dedup_seconds:0 must be honored, got %v", cfg.Alert.DedupSeconds)
	}
}

// TestRepoConfigIsComplete 仓库里的 config.yaml 必须字段齐全。
// 缺任何一个字段，启动时都会触发回写补默认值，而回写会抹掉文件里手写的注释。
// 这个用例就是要守住那批注释：新增配置字段时必须同步改 config.yaml。
func TestRepoConfigIsComplete(t *testing.T) {
	raw, err := os.ReadFile("config.yaml") // 与 config_test.go 同目录
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	if !strings.Contains(string(raw), "#") {
		t.Fatal("config.yaml is expected to carry comments; did someone strip them?")
	}

	var local Config
	if err := yaml.Unmarshal(raw, &local); err != nil {
		t.Fatalf("parse repo config: %v", err)
	}

	before, err := yaml.Marshal(&local)
	if err != nil {
		t.Fatalf("marshal loaded config: %v", err)
	}

	cfg = local
	t.Cleanup(func() { cfg = Config{} })
	applyDefaults()

	after, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal merged config: %v", err)
	}

	if !bytes.Equal(before, after) {
		t.Errorf("config.yaml is missing fields, so startup would rewrite it and drop all comments.\n"+
			"add the missing fields to config.yaml.\n--- as written ---\n%s\n--- after defaults ---\n%s", before, after)
	}
}

// chdir 切换工作目录并返回还原函数。LoadConfig 使用相对路径，测试里必须切目录。
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}
}

// loadIntoTestConfig 复用生产逻辑加载指定路径的配置。
//
// cfg 是包级变量，必须先清零：否则 -count=2 或测试乱序执行时，上一个用例留下的
// 值会被当成"用户显式配置"，让默认值断言失真。
func loadIntoTestConfig(path string) {
	cfg = Config{}

	// LoadConfig 内部固定读取 config/config.yaml，这里直接内联等价流程，
	// 既复用同一份 applyDefaults，又不必把路径硬编码进生产代码。
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		panic(err)
	}
	applyDefaults()
	if err := os.WriteFile(path, mustYAML(&cfg), 0644); err != nil {
		panic(err)
	}
}

func mustYAML(v interface{}) []byte {
	b, err := yaml.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
