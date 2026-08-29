//go:build !mips && !mipsle && !mips64 && !mips64le

package config

import (
	"log"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NewDatabase 打开 SQLite 并建表建索引。
//
// 使用 github.com/glebarez/sqlite（底层 modernc.org/sqlite，纯 Go 无 CGO），
// 因此可以 CGO_ENABLED=0 交叉编译出单文件静态二进制。
//
// 注意：modernc.org/libc 未提供 MIPS 实现，无法构建 linux/mipsle。
// MIPS（极路由等 MT7620/MT7621 设备）走同目录下的 database_cgo.go，
// 使用 CGO 版驱动 mattn/go-sqlite3 + gorm.io/driver/sqlite。
func NewDatabase() *gorm.DB {
	db, err := gorm.Open(sqlite.Open(cfg.Database.Path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	initDatabase(db)

	return db
}
