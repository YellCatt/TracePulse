// Package config 负责配置的加载、默认值补全与持久化。
//
// 设计要点：配置文件缺失时自动生成；已存在的配置文件若缺少某些字段（例如老版本
// 只有 server/database/log 三段），会在加载后用默认值补齐并回写，保证配置项
// 始终与运行时的实际行为一致，避免出现 "配置了但没生效" 的错觉。
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config 顶层配置结构。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Log      LogConfig      `yaml:"log"`
	Trace    TraceConfig    `yaml:"trace"`
	Alert    AlertConfig    `yaml:"alert"`
}

// ServerConfig HTTP 服务配置。
type ServerConfig struct {
	Port int `yaml:"port"`
	// ReadTimeoutSeconds / WriteTimeoutSeconds 控制慢客户端与上传体的资源占用。
	ReadTimeoutSeconds  int `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds int `yaml:"write_timeout_seconds"`
	// ShutdownTimeoutSeconds 优雅关闭时等待存量请求与内存 trace 落盘的上限。
	ShutdownTimeoutSeconds int `yaml:"shutdown_timeout_seconds"`
}

// DatabaseConfig SQLite 配置。
type DatabaseConfig struct {
	Path string `yaml:"path"`
	// BusyTimeoutMs 遇到写锁时的重试等待时间，写多读多场景必须设置，否则直接报
	// "database is locked"。
	BusyTimeoutMs int `yaml:"busy_timeout_ms"`
	// JournalMode SQLite 日志模式：WAL（默认）/ DELETE / TRUNCATE / PERSIST / MEMORY。
	// WAL 下读写互不阻塞，适合边上报边查询；写 U 盘想少一个 -wal 文件可改 DELETE。
	// 用字符串而非 bool，是为了让"字段缺失"也能落到正确的默认值。
	JournalMode string `yaml:"journal_mode"`
	// SyncMode SQLite 同步策略：FULL / NORMAL / OFF。
	// 运行在 U 盘 / SD 卡上建议 NORMAL，兼顾性能与掉电安全。
	SyncMode string `yaml:"sync_mode"`
}

// LogConfig 日志配置。
type LogConfig struct {
	Path  string `yaml:"path"`
	Level string `yaml:"level"`
}

// TraceConfig 链路采集与存储配置。
type TraceConfig struct {
	// QueueSize 事件队列容量，队列满时新事件被丢弃并触发 queue_drop 告警。
	QueueSize int `yaml:"queue_size"`
	// TTLSeconds 内存中活跃 trace 的最大存活时间，超时仍未收到 end 事件则强制落盘。
	TTLSeconds int `yaml:"ttl_seconds"`
	// FlushBatch 单个 trace 累计多少条事件后提前落盘（防止长链路一直驻留内存）。
	FlushBatch int `yaml:"flush_batch"`
	// FlushMs 批量落盘的时间窗口。
	FlushMs int `yaml:"flush_ms"`
	// CleanupDays 过期链路保留天数，后台定时清理。
	CleanupDays int `yaml:"cleanup_days"`
	// CleanupIntervalMinutes 清理任务执行间隔。
	CleanupIntervalMinutes int `yaml:"cleanup_interval_minutes"`
	// DisableVacuum 关闭磁盘空间回收（默认开启）。
	// 用反向语义，保证老配置文件缺少该字段时也走"开启回收"这个正确默认值。
	DisableVacuum bool `yaml:"disable_vacuum"`
	// NDJSONPath 兜底日志路径，trace-server 不可用时事件仍能落盘/落 stdout。
	// 为空则输出到 stdout（容器友好，交给 Docker log driver 收集）。
	NDJSONPath string `yaml:"ndjson_path"`
	// NDJSONMaxMB 兜底日志单文件上限，超过后轮转，保留一个历史文件。
	NDJSONMaxMB int `yaml:"ndjson_max_mb"`
	// MaxEventsPerTrace 单条链路最多保留多少事件，超出部分丢弃，防止单条异常链路撑爆磁盘。
	MaxEventsPerTrace int `yaml:"max_events_per_trace"`
	// ReportMaxBodyBytes 上报接口请求体上限，防止超大 payload 打爆内存。
	ReportMaxBodyBytes int `yaml:"report_max_body_bytes"`
}

// AlertConfig 邮件告警配置。
type AlertConfig struct {
	Enabled bool `yaml:"enabled"`

	SMTPHost string `yaml:"smtp_host"`
	SMTPPort int    `yaml:"smtp_port"`
	SMTPUser string `yaml:"smtp_user"`
	SMTPPass string `yaml:"smtp_password"`
	SMTPFrom string `yaml:"smtp_from"`

	// UseTLS 隐式 TLS（通常配合 465 端口）。端口为 465 或未设置 StartTLS 时自动开启。
	UseTLS bool `yaml:"use_tls"`
	// StartTLS 显式 STARTTLS（通常配合 587 端口）。
	StartTLS bool `yaml:"starttls"`
	// InsecureSkipVerify 跳过证书校验，仅建议内网自签证书场景使用。
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`

	Recipients []string `yaml:"recipients"`
	// Triggers 触发告警的条件，可选值：error / warn / timeout / queue_drop。
	Triggers []string `yaml:"triggers"`
	// PublicURL 告警邮件中「查看详情」链接的 base URL，例如 http://1.2.3.4:8086。
	PublicURL string `yaml:"public_url"`

	// SlowThresholdMs 慢调用阈值（毫秒）。triggers 含 slow 时，链路耗时超过该值即告警。
	// 0 表示关闭慢调用告警。
	SlowThresholdMs int64 `yaml:"slow_threshold_ms"`

	// TimeoutSeconds 单次发信超时。
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// DedupSeconds 同一 trace_id + 状态 在该窗口内只告警一次，避免重复邮件轰炸。
	// 用指针区分"未配置"（取默认 300）与"显式设为 0"（关闭去重）。
	DedupSeconds *int `yaml:"dedup_seconds"`
	// MinIntervalSeconds 全局告警最小发送间隔（令牌桶限流），防止故障风暴时邮件炸群。
	MinIntervalSeconds int `yaml:"min_interval_seconds"`
	// MaxEventsInMail 邮件正文中最多附带的事件条数。
	MaxEventsInMail int `yaml:"max_events_in_mail"`
	// QueueSize 告警队列容量，队列满时直接丢弃告警（宁可丢告警也不能拖垮采集）。
	QueueSize int `yaml:"queue_size"`
}

var cfg Config

// LoadConfig 读取配置；文件不存在则生成默认配置。
func LoadConfig() {
	configPath := "config/config.yaml"

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		log.Println("config file not found, creating default config...")
		cfg = defaultConfig()
		if err := SaveConfig(configPath); err != nil {
			log.Fatalf("failed to create default config: %v", err)
		}
	} else {
		file, err := os.ReadFile(configPath)
		if err != nil {
			log.Fatalf("failed to read config file: %v", err)
		}
		if err := yaml.Unmarshal(file, &cfg); err != nil {
			log.Fatalf("failed to parse config file: %v", err)
		}
	}

	// 补齐缺失字段，并把最终生效的配置回写，保证配置与行为一致。
	applyDefaults()
	if err := SaveConfig(configPath); err != nil {
		log.Printf("warning: failed to persist merged config: %v", err)
	}
}

// defaultConfig 返回一份完整的默认配置。
func defaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Port:                   8086,
			ReadTimeoutSeconds:     30,
			WriteTimeoutSeconds:    60,
			ShutdownTimeoutSeconds: 20,
		},
		Database: DatabaseConfig{
			Path:          "./data.db",
			BusyTimeoutMs: 5000,
			JournalMode:   "WAL",
			SyncMode:      "NORMAL",
		},
		Log: LogConfig{
			Path:  "./logs",
			Level: "info",
		},
		Trace: TraceConfig{
			QueueSize:              1000,
			TTLSeconds:             300,
			FlushBatch:             200,
			FlushMs:                200,
			CleanupDays:            7,
			CleanupIntervalMinutes: 60,
			DisableVacuum:          false,
			NDJSONPath:             "",
			NDJSONMaxMB:            64,
			MaxEventsPerTrace:      5000,
			ReportMaxBodyBytes:     8 << 20, // 8MB
		},
		Alert: AlertConfig{
			Enabled:            false,
			SMTPHost:           "smtp.example.com",
			SMTPPort:           587,
			SMTPUser:           "alerts@example.com",
			SMTPPass:           "",
			SMTPFrom:           "alerts@example.com",
			StartTLS:           true,
			Recipients:         []string{"admin@example.com"},
			Triggers:           []string{"error", "warn", "timeout", "queue_drop"},
			PublicURL:          "http://localhost:8086",
			TimeoutSeconds:     15,
			DedupSeconds:       intPtr(300),
			MinIntervalSeconds: 60,
			MaxEventsInMail:    500,
			QueueSize:          256,
		},
	}
}

// applyDefaults 对零值字段逐一兜底。
func applyDefaults() {
	d := defaultConfig()

	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		cfg.Server.Port = d.Server.Port
	}
	if cfg.Server.ReadTimeoutSeconds <= 0 {
		cfg.Server.ReadTimeoutSeconds = d.Server.ReadTimeoutSeconds
	}
	if cfg.Server.WriteTimeoutSeconds <= 0 {
		cfg.Server.WriteTimeoutSeconds = d.Server.WriteTimeoutSeconds
	}
	if cfg.Server.ShutdownTimeoutSeconds <= 0 {
		cfg.Server.ShutdownTimeoutSeconds = d.Server.ShutdownTimeoutSeconds
	}

	if cfg.Database.Path == "" {
		cfg.Database.Path = d.Database.Path
	}
	if cfg.Database.BusyTimeoutMs <= 0 {
		cfg.Database.BusyTimeoutMs = d.Database.BusyTimeoutMs
	}
	if cfg.Database.JournalMode == "" {
		cfg.Database.JournalMode = d.Database.JournalMode
	}
	if cfg.Database.SyncMode == "" {
		cfg.Database.SyncMode = d.Database.SyncMode
	}

	if cfg.Log.Path == "" {
		cfg.Log.Path = d.Log.Path
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = d.Log.Level
	}

	if cfg.Trace.QueueSize <= 0 {
		cfg.Trace.QueueSize = d.Trace.QueueSize
	}
	if cfg.Trace.TTLSeconds <= 0 {
		cfg.Trace.TTLSeconds = d.Trace.TTLSeconds
	}
	if cfg.Trace.FlushBatch <= 0 {
		cfg.Trace.FlushBatch = d.Trace.FlushBatch
	}
	if cfg.Trace.FlushMs <= 0 {
		cfg.Trace.FlushMs = d.Trace.FlushMs
	}
	if cfg.Trace.CleanupDays <= 0 {
		cfg.Trace.CleanupDays = d.Trace.CleanupDays
	}
	if cfg.Trace.CleanupIntervalMinutes <= 0 {
		cfg.Trace.CleanupIntervalMinutes = d.Trace.CleanupIntervalMinutes
	}
	if cfg.Trace.MaxEventsPerTrace <= 0 {
		cfg.Trace.MaxEventsPerTrace = d.Trace.MaxEventsPerTrace
	}
	if cfg.Trace.NDJSONMaxMB <= 0 {
		cfg.Trace.NDJSONMaxMB = d.Trace.NDJSONMaxMB
	}
	if cfg.Trace.ReportMaxBodyBytes <= 0 {
		cfg.Trace.ReportMaxBodyBytes = d.Trace.ReportMaxBodyBytes
	}

	if cfg.Alert.SMTPPort <= 0 {
		cfg.Alert.SMTPPort = d.Alert.SMTPPort
	}
	if cfg.Alert.SMTPFrom == "" {
		cfg.Alert.SMTPFrom = cfg.Alert.SMTPUser
	}
	if len(cfg.Alert.Recipients) == 0 {
		cfg.Alert.Recipients = d.Alert.Recipients
	}
	if len(cfg.Alert.Triggers) == 0 {
		cfg.Alert.Triggers = d.Alert.Triggers
	}
	if cfg.Alert.PublicURL == "" {
		cfg.Alert.PublicURL = fmt.Sprintf("http://localhost:%d", cfg.Server.Port)
	}
	// 465 端口默认为隐式 TLS；587 端口默认 STARTTLS。
	if cfg.Alert.SMTPPort == 465 && !cfg.Alert.StartTLS {
		cfg.Alert.UseTLS = true
	}
	if cfg.Alert.TimeoutSeconds <= 0 {
		cfg.Alert.TimeoutSeconds = d.Alert.TimeoutSeconds
	}
	if cfg.Alert.DedupSeconds == nil {
		cfg.Alert.DedupSeconds = intPtr(*d.Alert.DedupSeconds)
	}
	if cfg.Alert.MinIntervalSeconds <= 0 {
		cfg.Alert.MinIntervalSeconds = d.Alert.MinIntervalSeconds
	}
	if cfg.Alert.MaxEventsInMail <= 0 {
		cfg.Alert.MaxEventsInMail = d.Alert.MaxEventsInMail
	}
	if cfg.Alert.QueueSize <= 0 {
		cfg.Alert.QueueSize = d.Alert.QueueSize
	}
}

// SaveConfig 将当前配置序列化到指定路径。
func SaveConfig(path string) error {
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func GetServerConfig() ServerConfig     { return cfg.Server }
func GetDatabaseConfig() DatabaseConfig { return cfg.Database }
func GetLogConfig() LogConfig           { return cfg.Log }
func GetTraceConfig() TraceConfig       { return cfg.Trace }
func GetAlertConfig() AlertConfig       { return cfg.Alert }

// 兼容旧调用方：保留细粒度 getter。
func GetServerPort() int      { return cfg.Server.Port }
func GetDatabasePath() string { return cfg.Database.Path }
func GetLogPath() string      { return cfg.Log.Path }
func GetLogLevel() string     { return cfg.Log.Level }

// intPtr 返回一个指向常量的指针，用于需要区分「未配置」与「显式零值」的配置项。
func intPtr(v int) *int {
	return &v
}

// InitDirectories 创建日志目录与数据库所在目录。
func InitDirectories() error {
	if err := os.MkdirAll(cfg.Log.Path, 0755); err != nil {
		return err
	}

	dbDir := filepath.Dir(cfg.Database.Path)
	if dbDir != "." && dbDir != "" {
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			return err
		}
	}

	return nil
}
