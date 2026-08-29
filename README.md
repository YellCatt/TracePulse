# GAPI

一个基于 Go + GORM + SQLite 构建的 RESTful API 服务模板。

## 功能特性

- ✅ 自动配置文件创建：首次运行自动生成 `config.yaml`
- ✅ 纯 Go 实现：使用 `modernc.org/sqlite`，无需 CGO
- ✅ 跨平台：支持 Linux、Windows、macOS
- ✅ 用户管理 API：提供完整的用户 CRUD 操作
- ✅ 系统监控 API：实时监控 CPU、内存、网速和硬盘 IO
- ✅ 结构化日志：使用 Zap 日志库
- ✅ API 文档：集成 Swagger UI

## 快速开始

### 前置要求

- Go 1.22+

### 安装依赖

```bash
go mod tidy
```

### 构建

```bash
# 开发环境构建
go build -o gapi

# 生产环境构建（禁用 CGO）
CGO_ENABLED=0 go build -o gapi
```

### 运行

```bash
./gapi
```

首次运行时，系统会自动创建 `config/config.yaml` 配置文件。

### 访问服务

- API 服务：http://localhost:8084
- Swagger 文档：http://localhost:8084/swagger/index.html

## API 接口

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | /health | 健康检查 |
| GET | /status | 系统状态监控（CPU/内存/网速/硬盘IO） |
| GET | /users | 获取所有用户 |
| GET | /users/:id | 获取单个用户 |
| POST | /users | 创建用户 |
| PUT | /users/:id | 更新用户 |
| DELETE | /users/:id | 删除用户 |

### 示例请求

#### 创建用户

```bash
curl -X POST http://localhost:8084/users \
  -H "Content-Type: application/json" \
  -d '{"name": "John Doe", "email": "john@example.com"}'
```

#### 获取所有用户

```bash
curl http://localhost:8084/users
```

#### 获取单个用户

```bash
curl http://localhost:8084/users/1
```

#### 更新用户

```bash
curl -X PUT http://localhost:8084/users/1 \
  -H "Content-Type: application/json" \
  -d '{"name": "John Updated", "email": "john.updated@example.com"}'
```

#### 删除用户

```bash
curl -X DELETE http://localhost:8084/users/1
```

#### 获取系统状态

```bash
curl http://localhost:8084/status
```

示例响应：

```json
{
  "cpu": {
    "usage": 12.5,
    "count": 8,
    "vendor": "GenuineIntel",
    "model": "Intel(R) Core(TM) i7-10700",
    "mhz": 2900,
    "cache_size": 16384
  },
  "memory": {
    "total": 16384000,
    "used": 8192000,
    "free": 4096000,
    "available": 8192000,
    "usage": 50.0
  },
  "network": {
    "bytes_recv": 120563,
    "bytes_sent": 96441,
    "recv_speed": 1024.0,
    "send_speed": 512.0
  },
  "disk": {
    "total": 500000000,
    "used": 250000000,
    "free": 250000000,
    "usage": 50.0,
    "read_speed": 1024.0,
    "write_speed": 512.0
  },
  "uptime": 3600,
  "units": {
    "cpu": "count",
    "cpu_usage": "percent",
    "memory": "KB",
    "memory_usage": "percent",
    "network": "KB",
    "speed": "KB/s",
    "disk": "KB",
    "disk_usage": "percent",
    "uptime": "seconds"
  }
}
```

## 配置文件

配置文件 `config/config.yaml` 会在首次运行时自动创建：

```yaml
server:
  port: 8084

database:
  path: ./data.db

log:
  path: ./logs
  level: info
```

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| server.port | 服务端口 | 8084 |
| database.path | SQLite 数据库路径 | ./data.db |
| log.path | 日志目录 | ./logs |
| log.level | 日志级别 | info |

## 项目结构

```
gapi/
├── config/          # 配置管理
│   ├── config.go    # 配置加载
│   ├── config.yaml  # 配置文件
│   └── database.go  # 数据库初始化
├── controller/      # 控制器
│   ├── user_controller.go
│   └── status_controller.go
├── service/         # 服务层
│   ├── user_service.go
│   └── status_service.go
├── repository/      # 数据访问层
│   └── user_repository.go
├── model/           # 数据模型
│   ├── user.go
│   └── status.go
├── router/          # 路由配置
│   └── router.go
├── logger/          # 日志管理
│   └── logger.go
├── docs/            # API 文档
│   └── docs.go
├── main.go          # 入口文件
└── go.mod           # Go 模块依赖
```

## 技术栈

- **框架**: Go 1.22
- **ORM**: GORM
- **数据库**: SQLite (modernc.org/sqlite)
- **日志**: Zap
- **系统监控**: gopsutil
- **API 文档**: Swagger

## 编译说明

由于使用纯 Go 的 SQLite 驱动，可以在任何平台上编译：

```bash
# Linux
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o gapi-linux

# Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o gapi.exe

# macOS
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o gapi-macos
```

## 许可证

MIT License