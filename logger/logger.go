// Package logger 封装了基于 zap 的日志组件，支持按级别输出到不同文件和控制台。
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var log *zap.Logger // 全局日志实例

// Init 初始化日志组件。
// path  日志文件存放目录；level 控制台最低输出级别（debug/info/warn/error）。
func Init(path string, level string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("failed to create log directory %s: %w", path, err)
	}

	lvl := zapcore.InfoLevel
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return fmt.Errorf("invalid log level %q: %w", level, err)
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

	infoFile, err := openLogFile(path, "info.log")
	if err != nil {
		return err
	}
	warnFile, err := openLogFile(path, "warn.log")
	if err != nil {
		return err
	}
	errorFile, err := openLogFile(path, "error.log")
	if err != nil {
		return err
	}

	infoCore := zapcore.NewCore(encoder, zapcore.AddSync(infoFile), exactLevel(lvl, zapcore.InfoLevel))
	warnCore := zapcore.NewCore(encoder, zapcore.AddSync(warnFile), exactLevel(lvl, zapcore.WarnLevel))
	errorCore := zapcore.NewCore(encoder, zapcore.AddSync(errorFile), atLeastLevel(zapcore.ErrorLevel))
	consoleCore := zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), atLeastLevel(lvl))

	core := zapcore.NewTee(infoCore, warnCore, errorCore, consoleCore)

	log = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	zap.ReplaceGlobals(log)
	return nil
}

// openLogFile 以追加模式打开指定日志文件，不存在则创建。
func openLogFile(path, name string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(path, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file %s: %w", name, err)
	}
	return file, nil
}

// exactLevel 只输出指定级别，且不低于用户配置的最低级别。
func exactLevel(min, target zapcore.Level) zapcore.LevelEnabler {
	return zap.LevelEnablerFunc(func(l zapcore.Level) bool {
		return l >= min && l == target
	})
}

// atLeastLevel 输出该级别及以上日志。
func atLeastLevel(min zapcore.Level) zapcore.LevelEnabler {
	return zap.LevelEnablerFunc(func(l zapcore.Level) bool {
		return l >= min
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
