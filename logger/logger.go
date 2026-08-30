// Package logger 封装了基于 zap 的日志组件，支持多种输出模式。
//
// 三种模式：
//   - ModeSplit（默认）：按级别分文件，每个文件只含该级别（fatal 归入 error.log）；
//   - ModeRange：按级别分文件，每个文件含该级别及以上；
//   - ModeSingle：所有日志写入同一个 app.log，只按级别过滤。
//
// 无论哪种模式，都可以用 Options.Levels 指定白名单，只保留真正关心的那几个级别。
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// 支持的日志输出模式。
const (
	// ModeSplit 按级别分文件，每个文件只含该级别（fatal 归入 error.log）。
	ModeSplit = "split"
	// ModeRange 按级别分文件，每个文件含该级别及以上。
	ModeRange = "range"
	// ModeSingle 全部日志写入同一个文件，只按级别过滤。
	ModeSingle = "single"
)

// singleFileName 是 single 模式下唯一的日志文件名。
const singleFileName = "app.log"

// fileLevels 允许单独成文件的级别；dpanic/panic/fatal 都归到 error 一档，
// 避免日志目录里冒出一堆几乎不会有内容的文件。
var fileLevels = []zapcore.Level{
	zapcore.DebugLevel,
	zapcore.InfoLevel,
	zapcore.WarnLevel,
	zapcore.ErrorLevel,
}

var log *zap.Logger // 全局日志实例

// files 记录本次初始化打开的文件句柄，供 Close 释放（重复 Init 时也会先关旧的）。
var files []*os.File

// Options 日志初始化参数。
type Options struct {
	// Dir 日志文件存放目录。
	Dir string
	// Level 最低输出级别（debug/info/warn/error）。Levels 非空时该字段失效。
	Level string
	// Mode 输出模式，取值见 ModeSplit / ModeRange / ModeSingle，缺省为 split。
	Mode string
	// Levels 级别白名单，非空时只输出列出的级别（例如只要 warn 和 error）。
	// 填了 error 就同时包含 fatal。
	Levels []string
	// DisableConsole 关闭控制台输出；容器里日志已经交给 stdout 收集时可关掉，避免重复。
	DisableConsole bool
}

// Init 按给定模式初始化日志组件。
func Init(opts Options) error {
	mode, err := normalizeMode(opts.Mode)
	if err != nil {
		return err
	}
	// 重复初始化（配置热加载、测试）时先放掉旧句柄，避免句柄泄漏、文件删不掉。
	closeFiles()

	levels, err := resolveLevels(opts)
	if err != nil {
		return err
	}

	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory %s: %w", dir, err)
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     east8TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	encoder := zapcore.NewConsoleEncoder(encoderConfig)

	// filter 是全局闸门：决定一条日志到底要不要输出。
	filter := levelSetFilter(levels)

	cores := make([]zapcore.Core, 0, len(levels)+1)
	switch mode {
	case ModeSingle:
		// 单一文件：闸门就是级别白名单 / 阈值。
		file, err := openLogFile(dir, singleFileName)
		if err != nil {
			return err
		}
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(file), filter))
	case ModeRange:
		// 每个级别一个文件，内容向上叠加：error.log 里的 error 也会出现在 warn.log。
		for _, lv := range levels {
			file, err := openLogFile(dir, lv.String()+".log")
			if err != nil {
				return err
			}
			cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(file), atLeastLevel(lv)))
		}
	default: // ModeSplit
		// 每个级别一个文件，文件之间互不重叠。
		for _, lv := range levels {
			file, err := openLogFile(dir, lv.String()+".log")
			if err != nil {
				return err
			}
			cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(file), exactLevel(lv)))
		}
	}

	if !opts.DisableConsole {
		cores = append(cores, zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), filter))
	}

	log = zap.New(zapcore.NewTee(cores...), zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	zap.ReplaceGlobals(log)
	return nil
}

// openLogFile 以追加模式打开指定日志文件，不存在则创建。
// 句柄登记在 files 里，由 Close 统一释放。
func openLogFile(path, name string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(path, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// 半途失败时不能留着已经打开的句柄。
		closeFiles()
		return nil, fmt.Errorf("failed to open log file %s: %w", name, err)
	}
	files = append(files, file)
	return file, nil
}

// closeFiles 释放本次初始化打开的全部日志文件句柄。
func closeFiles() {
	for _, f := range files {
		_ = f.Close()
	}
	files = nil
}

// normalizeMode 校验并补默认模式。
func normalizeMode(mode string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "":
		return ModeSplit, nil
	case ModeSplit, ModeRange, ModeSingle:
		return m, nil
	default:
		return "", fmt.Errorf("invalid log mode %q, want one of %s/%s/%s", mode, ModeSplit, ModeRange, ModeSingle)
	}
}

// resolveLevels 计算最终需要落盘的级别：白名单优先，否则按阈值展开。
// 返回值按级别从低到高排列，且只包含会产生文件的四档。
func resolveLevels(opts Options) ([]zapcore.Level, error) {
	if len(opts.Levels) > 0 {
		want := make(map[zapcore.Level]struct{}, len(opts.Levels))
		for _, raw := range opts.Levels {
			lv, err := parseLevel(raw)
			if err != nil {
				return nil, err
			}
			want[normalize(lv)] = struct{}{}
		}
		levels := make([]zapcore.Level, 0, len(want))
		for _, lv := range fileLevels {
			if _, ok := want[lv]; ok {
				levels = append(levels, lv)
			}
		}
		if len(levels) == 0 {
			return nil, fmt.Errorf("log.levels %v contains no usable level", opts.Levels)
		}
		return levels, nil
	}

	min := zapcore.InfoLevel
	if s := strings.TrimSpace(opts.Level); s != "" {
		lv, err := parseLevel(s)
		if err != nil {
			return nil, err
		}
		min = lv
	}
	min = normalize(min)

	levels := make([]zapcore.Level, 0, len(fileLevels))
	for _, lv := range fileLevels {
		if lv >= min {
			levels = append(levels, lv)
		}
	}
	return levels, nil
}

// parseLevel 解析级别文本，大小写与前后空格均容错。
func parseLevel(s string) (zapcore.Level, error) {
	var lv zapcore.Level
	if err := lv.UnmarshalText([]byte(strings.ToLower(strings.TrimSpace(s)))); err != nil {
		return lv, fmt.Errorf("invalid log level %q: %w", s, err)
	}
	return lv, nil
}

// normalize 把 dpanic/panic/fatal 归拢到 error，保证日志目录里最多四个文件。
func normalize(l zapcore.Level) zapcore.Level {
	if l >= zapcore.ErrorLevel {
		return zapcore.ErrorLevel
	}
	if l < zapcore.DebugLevel {
		return zapcore.DebugLevel
	}
	return l
}

// levelSetFilter 只放行集合内的级别。
func levelSetFilter(levels []zapcore.Level) zapcore.LevelEnabler {
	set := make(map[zapcore.Level]struct{}, len(levels))
	for _, lv := range levels {
		set[lv] = struct{}{}
	}
	return zap.LevelEnablerFunc(func(l zapcore.Level) bool {
		_, ok := set[normalize(l)]
		return ok
	})
}

// exactLevel 只输出指定级别（fatal 计作 error）。
func exactLevel(target zapcore.Level) zapcore.LevelEnabler {
	return zap.LevelEnablerFunc(func(l zapcore.Level) bool {
		return normalize(l) == target
	})
}

// atLeastLevel 输出该级别及以上日志。
func atLeastLevel(min zapcore.Level) zapcore.LevelEnabler {
	return zap.LevelEnablerFunc(func(l zapcore.Level) bool {
		return normalize(l) >= min
	})
}

var east8Location = time.FixedZone("CST", 8*60*60) // 东八区时区

// east8TimeEncoder 将时间格式化为东八区（CST）本地时间格式。
func east8TimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.In(east8Location).Format("2006-01-02 15:04:05"))
}

// Info 记录 Info 级别日志。
func Info(msg string, fields ...zap.Field) {
	if log == nil {
		return
	}
	log.Info(msg, fields...)
}

// Warn 记录 Warn 级别日志。
func Warn(msg string, fields ...zap.Field) {
	if log == nil {
		return
	}
	log.Warn(msg, fields...)
}

// Error 记录 Error 级别日志。
func Error(msg string, fields ...zap.Field) {
	if log == nil {
		return
	}
	log.Error(msg, fields...)
}

// Debug 记录 Debug 级别日志。
func Debug(msg string, fields ...zap.Field) {
	if log == nil {
		return
	}
	log.Debug(msg, fields...)
}

// Fatal 记录 Fatal 级别日志，随后程序退出。
func Fatal(msg string, fields ...zap.Field) {
	if log == nil {
		return
	}
	log.Fatal(msg, fields...)
}

// Sync 刷新所有日志缓冲到磁盘，应在程序退出前调用。
func Sync() {
	if log == nil {
		return
	}
	_ = log.Sync()
}

// Close 关闭日志文件句柄，用于重复初始化或测试场景释放资源。
// 调用后不应继续写日志。
func Close() {
	Sync()
	closeFiles()
	log = nil
}
