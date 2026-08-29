//go:build mips || mipsle || mips64 || mips64le

package config

import (
	"log"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NewDatabase 打开 SQLite 并建表建索引（MIPS 专用实现）。
//
// 极路由（MT7620/MT7621 等）是 MIPS 架构，纯 Go 的 modernc.org/sqlite 依赖的
// modernc.org/libc 没有 MIPS 实现，无法构建 linux/mipsle。
// 因此这里改用 CGO 版驱动：gorm.io/driver/sqlite + github.com/mattn/go-sqlite3，
// 由 OpenWrt SDK 的交叉工具链编译 C 源码。
//
// 构建时必须开启 CGO 并指定交叉编译器，例如（极路由常见为 mipsel-openwrt-linux）：
//
//	CGO_ENABLED=1 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
//	  CC=mipsel-openwrt-linux-musl-gcc \
//	  go build -ldflags="-s -w" -o tracepulse-mipsle .
//
// 若追求体积，可再加 -tags "sqlite_omit_load_extension" 关掉扩展加载，
// 并用 -ldflags "-linkmode external -extldflags '-static'" 做静态链接。
//
// 初始化逻辑与默认实现共用 database_common.go，此处只负责打开数据库。
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
