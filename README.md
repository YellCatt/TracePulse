# TracePulse

轻量级链路追踪与日志采集服务端。单文件二进制 + SQLite，内置检索页面与邮件告警，既能在服务器上跑，也能塞进 OpenWrt 路由器。

业务侧按步上报事件，TracePulse 按 `trace_id` 聚合成完整链路落库；出问题时邮件直接把整条链路送到眼前，点开链接就是详情页——不用再去服务器上翻日志猜。

## 核心特性

- **上报永不阻塞业务**：事件进非阻塞队列，队列满直接丢弃并告警，绝不把采集方拖死
- **崩溃不丢事件**：事件先写 ndjson 兜底日志再入队，进程挂掉数据仍在
- **内存不泄漏**：活跃链路有 TTL 兜底，客户端漏发 `end` 事件也会强制落盘
- **零依赖前端**：检索页与详情页服务端渲染，手机浏览器无需 JS 也能排查
- **告警不炸群**：同链路同原因去重 + 全局最小间隔限流 + 队列丢弃聚合汇总
- **纯 Go 实现**：`modernc.org/sqlite` 无 CGO，可交叉编译成静态单文件
- **开箱即用**：配置文件缺失自动生成，字段缺失自动补全并回写

## 快速开始

### 环境要求

Go 1.22+

### 构建与运行

```bash
go build -o tracepulse
./tracepulse
```

首次运行会在 `config/config.yaml` 生成完整配置文件（含全部默认值），按需修改后重启即可。

### 访问

| 入口 | 地址 |
|------|------|
| 链路检索页 | http://localhost:8084/traces |
| 链路详情页 | http://localhost:8084/trace/{trace_id} |
| Swagger 文档 | http://localhost:8084/swagger/index.html |
| 健康检查 | http://localhost:8084/health |

根路径 `/` 会重定向到 `/traces`。

## 上报协议

### POST /api/traces/report

请求体支持两种写法，任选其一：

```bash
# 写法一：批量上报（推荐）
curl -X POST http://localhost:8084/api/traces/report \
  -H "Content-Type: application/json" \
  -d '{
    "events": [
      {
        "trace_id": "req-7f3a91",
        "span_id": "span-1",
        "timestamp": "2026-08-30T10:00:00+08:00",
        "level": "info",
        "module": "order-service",
        "event": "start",
        "message": "开始处理下单请求",
        "params": {"user_id": 1024, "sku": "A-01"}
      },
      {
        "trace_id": "req-7f3a91",
        "span_id": "span-2",
        "parent_span_id": "span-1",
        "timestamp": "2026-08-30T10:00:01.250+08:00",
        "level": "error",
        "module": "order-service",
        "event": "db.query",
        "message": "库存扣减失败",
        "error_message": "deadlock detected",
        "params": {"sql": "UPDATE stock SET qty=qty-1 WHERE sku=?"}
      },
      {
        "trace_id": "req-7f3a91",
        "span_id": "span-1",
        "timestamp": "2026-08-30T10:00:01.300+08:00",
        "level": "info",
        "module": "order-service",
        "event": "end",
        "message": "下单失败"
      }
    ]
  }'
```

```bash
# 写法二：裸数组（单条上报更省事）
curl -X POST http://localhost:8084/api/traces/report \
  -H "Content-Type: application/json" \
  -d '[{"trace_id":"req-7f3a91","level":"info","module":"pay","event":"start","message":"开始支付"}]'
```

成功响应：

```json
{"status":"ok","count":3}
```

### 事件字段

| 字段 | 必填 | 说明 |
|------|:----:|------|
| `trace_id` | 是 | 链路唯一标识。为空则记为 `unknown`，无法聚合 |
| `span_id` / `parent_span_id` | 否 | 跨度标识，用于在详情页还原调用层级 |
| `timestamp` | 否 | RFC3339 时间，缺省为服务端当前时间 |
| `level` | 否 | `trace`/`debug`/`info`/`warn`/`error`/`fatal`，缺省 `info` |
| `module` | 否 | 模块名。**首条事件的 module 会作为整条链路的 service_name** |
| `event` | 否 | 事件名。`start` / `end` 为特殊值，收到 `end` 立即落盘 |
| `message` | 否 | 事件描述 |
| `params` | 否 | 附加参数，详见下方容错说明 |
| `error_message` | 否 | 错误信息 |

`params` 容错：库里存的是字符串，但上报时写对象 / 数组 / 数字 / 布尔都能正常接收，会自动序列化成紧凑 JSON 并在页面与邮件里按 KV 展开——不会因为一个字段写法不对就让整批事件被拒。

### 链路状态判定

| 状态 | 触发条件 |
|------|----------|
| `ok` | 默认状态，链路正常结束 |
| `warn` | 链路中出现 `warn` 级事件，且无 error |
| `error` | 链路中出现 `error` / `fatal` 级事件，同时置 `has_error=true` |
| `timeout` | TTL 内未收到 `end` 事件，强制落盘并置 `has_error=true` |

### 限制与错误码

| 限制 | 默认值 | 配置项 |
|------|--------|--------|
| 单次请求事件数 | 5000 | 硬编码上限 |
| 请求体大小 | 8 MB | `trace.report_max_body_bytes` |
| 单条链路事件数 | 5000 | `trace.max_events_per_trace` |
| 队列容量 | 1000 | `trace.queue_size` |

| 状态码 | 含义 |
|--------|------|
| 200 | 成功。**注意：队列满导致事件被丢弃时也返回 200**——采集端不应因服务端压力大而失败重试，否则会把雪崩放大 |
| 400 | JSON 非法、body 为空、events 为空 |
| 413 | 请求体超限或单次事件数超限 |
| 503 | 服务正在关闭 |

## 查询接口

### GET /api/traces

多条件过滤 + 分页。

| 参数 | 说明 |
|------|------|
| `trace_id` | 精确匹配 |
| `service` | 服务名（即首条事件的 module） |
| `status` | `ok` / `warn` / `error` / `timeout` |
| `level` | 链路中出现过该级别的事件 |
| `module` | 链路中出现过该模块的事件 |
| `keyword` | 模糊搜索，覆盖 trace_id、service、error_message 及事件的 event/message/params |
| `has_error` | `true` / `false` |
| `min_duration_ms` | 慢调用阈值，只返回耗时 ≥ 该值的链路 |
| `start_time` / `end_time` | 时间范围，支持绝对与相对两种写法 |
| `page` | 页码，默认 1 |
| `page_size` | 每页条数，默认 20，上限 200 |

时间参数支持 `2026-08-30 10:00:00`、`2026-08-30`、`RFC3339`，以及相对时间 `30s` / `15m` / `1h` / `7d`。

```bash
# 最近一小时内的失败链路
curl "http://localhost:8084/api/traces?status=error&start_time=1h&page_size=50"

# 耗时超过 3 秒的慢调用
curl "http://localhost:8084/api/traces?min_duration_ms=3000"

# 按关键词模糊搜索
curl "http://localhost:8084/api/traces?keyword=deadlock"
```

响应示例：

```json
{
  "total": 1,
  "traces": [
    {
      "id": 1,
      "trace_id": "req-7f3a91",
      "service_name": "order-service",
      "status": "error",
      "start_time": "2026-08-30T10:00:00+08:00",
      "end_time": "2026-08-30T10:00:01.3+08:00",
      "duration_ms": 1300,
      "has_error": true,
      "error_message": "deadlock detected",
      "event_count": 3
    }
  ],
  "page": 1,
  "page_size": 20,
  "total_pages": 1
}
```

### GET /api/traces/{trace_id}

```bash
curl http://localhost:8084/api/traces/req-7f3a91
```

返回 `{"trace":{...},"events":[...]}`，事件按时间正序。未找到返回 404。

### GET /api/traces/stats

运行时指标，用于观测队列水位、判断是否需要调大队列或扩容：

```bash
curl http://localhost:8084/api/traces/stats
```

```json
{
  "queue_len": 0,
  "queue_cap": 1000,
  "active_traces": 2,
  "dropped_total": 0,
  "flushed_total": 128,
  "shutting_down": false
}
```

`dropped_total` 持续增长说明队列偏小或下游写入变慢；`active_traces` 长期不降说明有大量链路没发 `end` 事件，只能靠 TTL 兜底。

## 内置页面

| 路径 | 说明 |
|------|------|
| `GET /traces` | 检索页：过滤表单 + 结果列表 + 分页 |
| `GET /trace/{trace_id}` | 详情页：按时间线展示每一步 |
| `GET /traces?goto_trace_id=xxx` | 直接跳转到指定链路详情，排查邮件告警时最快 |

详情页会标出**慢步骤**：单步间隔超过整条链路耗时 30% 且大于 50ms 的步骤会高亮，方便一眼定位卡点。长文本自动折叠，点击展开。

页面时间统一按东八区渲染，与告警邮件完全一致（容器内 TZ 常为 UTC，这里显式固定）。

## 邮件告警

在 `config/config.yaml` 中把 `alert.enabled` 设为 `true` 并配好 SMTP 即可。

### 触发条件

`alert.triggers` 可选值：

| 值 | 含义 |
|----|------|
| `error` | 链路状态为 error |
| `warn` | 链路状态为 warn |
| `timeout` | 链路 TTL 超时未正常结束 |
| `queue_drop` | 事件队列溢出（30 秒窗口内汇总成一封） |
| `slow` | 链路耗时 ≥ `alert.slow_threshold_ms`（该值为 0 时不生效） |

### 防轰炸机制

- **去重**：同一 `trace_id` + 触发原因，在 `dedup_seconds`（默认 300 秒）内只发一封。显式设为 `0` 可关闭去重
- **限流**：`min_interval_seconds` 控制全局最小发信间隔，故障风暴时守住收件箱
- **截断**：事件数超过 `max_events_in_mail` 时保留头尾、省略中间，并在邮件里如实写明省略条数，完整内容仍去网页看
- **不拖垮采集**：告警全程异步，告警队列满直接丢弃——宁可丢告警也不能影响采集链路

### SMTP 端口

| 端口 | 默认行为 |
|------|----------|
| 465 | 自动启用隐式 TLS |
| 587 | 默认 STARTTLS |

内网自签证书可设 `insecure_skip_verify: true`。

邮件中的「查看详情」链接由 `alert.public_url` 生成，部署到服务器后记得改成能被收件人访问的地址，例如 `http://10.0.0.5:8084`。

## 配置说明

首次运行会自动生成完整的 `config/config.yaml`：

```yaml
server:
  port: 8084
  read_timeout_seconds: 30
  write_timeout_seconds: 60
  shutdown_timeout_seconds: 20
database:
  path: ./data.db
  busy_timeout_ms: 5000
  journal_mode: WAL
  sync_mode: NORMAL
log:
  path: ./logs
  level: info
trace:
  queue_size: 1000
  ttl_seconds: 300
  flush_batch: 200
  flush_ms: 200
  cleanup_days: 7
  cleanup_interval_minutes: 60
  disable_vacuum: false
  ndjson_path: ""
  ndjson_max_mb: 64
  max_events_per_trace: 5000
  report_max_body_bytes: 8388608
alert:
  enabled: false
  smtp_host: smtp.example.com
  smtp_port: 587
  smtp_user: alerts@example.com
  smtp_password: ""
  smtp_from: alerts@example.com
  use_tls: false
  starttls: true
  insecure_skip_verify: false
  recipients:
    - admin@example.com
  triggers:
    - error
    - warn
    - timeout
    - queue_drop
  public_url: http://localhost:8084
  slow_threshold_ms: 0
  timeout_seconds: 15
  dedup_seconds: 300
  min_interval_seconds: 60
  max_events_in_mail: 500
  queue_size: 256
```

### server

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `port` | 监听端口 | 8084 |
| `read_timeout_seconds` | 读超时 | 30 |
| `write_timeout_seconds` | 写超时 | 60 |
| `shutdown_timeout_seconds` | 优雅关闭等待上限 | 20 |

### database

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `path` | SQLite 文件路径 | ./data.db |
| `busy_timeout_ms` | 写锁等待时间，并发写入下必须设置 | 5000 |
| `journal_mode` | `WAL`（读写不互斥）/ `DELETE` / `TRUNCATE` / `PERSIST` / `MEMORY` | WAL |
| `sync_mode` | `FULL` / `NORMAL` / `OFF`。U 盘、SD 卡建议 `NORMAL` | NORMAL |

### log

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `path` | 日志目录 | ./logs |
| `level` | 日志级别 | info |

### trace

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `queue_size` | 事件队列容量，满则丢弃并告警 | 1000 |
| `ttl_seconds` | 活跃链路存活时间，超时未收到 `end` 则强制落盘 | 300 |
| `flush_batch` | 单条链路累计多少事件后提前落盘 | 200 |
| `flush_ms` | 批量落盘时间窗口 | 200 |
| `cleanup_days` | 数据保留天数 | 7 |
| `cleanup_interval_minutes` | 清理任务执行间隔 | 60 |
| `disable_vacuum` | 关闭磁盘空间回收 | false |
| `ndjson_path` | 兜底日志路径，**留空则输出到 stdout**（容器友好） | "" |
| `ndjson_max_mb` | 兜底日志单文件上限，超出轮转并保留一个历史文件 | 64 |
| `max_events_per_trace` | 单条链路事件数上限 | 5000 |
| `report_max_body_bytes` | 上报接口请求体上限 | 8388608 (8MB) |

### alert

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `enabled` | 是否启用告警 | false |
| `smtp_host` / `smtp_port` | SMTP 服务器 | smtp.example.com / 587 |
| `smtp_user` / `smtp_password` | 发信账号密码 | - |
| `smtp_from` | 发件人，留空则取 `smtp_user` | - |
| `use_tls` | 隐式 TLS（465 端口自动开启） | false |
| `starttls` | 显式 STARTTLS（587 端口默认） | true |
| `insecure_skip_verify` | 跳过证书校验，仅建议内网自签场景 | false |
| `recipients` | 收件人列表 | admin@example.com |
| `triggers` | 触发条件列表 | error, warn, timeout, queue_drop |
| `public_url` | 邮件「查看详情」链接的 base URL | http://localhost:8084 |
| `slow_threshold_ms` | 慢调用阈值（毫秒），0 表示关闭 | 0 |
| `timeout_seconds` | 单次发信超时 | 15 |
| `dedup_seconds` | 去重窗口（秒），显式设 0 关闭去重 | 300 |
| `min_interval_seconds` | 全局最小发信间隔 | 60 |
| `max_events_in_mail` | 邮件中最多附带的事件条数 | 500 |
| `queue_size` | 告警队列容量 | 256 |

## 架构

```
HTTP 上报 ──► 非阻塞队列 ──► 内存聚合（按 trace_id）──► 批量落库 ──► 告警判定
                  │                                          │
                  └─► ndjson 兜底落盘                    TTL 超时强制落盘
```

三条可靠性约束：

1. **上报永不阻塞业务线程** —— 队列满直接丢弃并告警，绝不让采集方被拖死
2. **事件先写 ndjson 再入队** —— TracePulse 自身挂掉也不会丢事件
3. **内存活跃链路有 TTL** —— 客户端漏发 `end` 事件时也要强制落盘，杜绝内存泄漏

### 落盘时机

满足以下任一条件即落盘：

- 链路的**最后一条**事件是 `end`
- 单条链路累计事件数达到 `flush_batch`
- TTL 超时（`ttl_seconds`）
- 进程关闭

判定只看当前队尾事件，因此 `end` 要作为链路的收尾事件上报。若在 `end` 之后又补报了其他事件，链路会重新进入等待，直到下一个 `end`、攒够 `flush_batch` 或 TTL 超时才落盘。

长链路被分批落盘时，后续批次会与库中已有记录做增量合并（累加事件数、扩展时间范围、合并错误状态），告警时再回库取全量事件，保证邮件里的链路是完整的。

### 数据存储

- WAL 模式下读写互不阻塞，边上报边查询
- 预建 13 个索引，覆盖列表翻页、状态/服务/错误过滤、事件时间线、module/level 子查询与过期清理
- `level` / `module` 过滤走 `EXISTS` 子查询，避免 JOIN 产生重复行
- 后台定时清理过期数据，并回收父链路已删除的孤儿事件
- 通过 `incremental_vacuum` 归还磁盘空间，可用 `disable_vacuum` 关闭

## 项目结构

```
TracePulse/
├── main.go              # 入口：组装各层并启动 HTTP 服务
├── config/              # 配置加载、默认值补全、数据库与索引
│   ├── config.go
│   ├── database.go
│   └── config.yaml
├── controller/          # HTTP 处理层：JSON API + 页面渲染
│   ├── trace_controller.go
│   ├── templates.go     # 页面模板
│   ├── user_controller.go
│   └── status_controller.go
├── service/             # 业务逻辑层
│   ├── trace_service.go    # 链路聚合、落盘、TTL、清理
│   ├── alert_service.go    # 告警判定、去重、限流、发信
│   ├── alert_template.go   # 告警邮件模板
│   ├── user_service.go
│   └── status_service.go
├── repository/          # 数据访问层
│   ├── trace_repository.go
│   └── user_repository.go
├── model/               # 数据模型与请求结构
│   ├── trace.go
│   ├── user.go
│   └── status.go
├── router/              # 路由注册与内嵌 Swagger 定义
├── view/                # 展示层格式化（网页与邮件共用）
├── logger/              # Zap 日志
└── docs/                # Swagger 文档注册
```

## 技术栈

- **语言**: Go 1.22（标准库 `net/http` + `http.ServeMux`）
- **ORM**: GORM
- **数据库**: SQLite（glebarez/sqlite，底层 modernc.org/sqlite，纯 Go 无 CGO）
- **日志**: Zap
- **系统监控**: gopsutil
- **API 文档**: Swagger（http-swagger）

## 附带接口

除链路追踪外，还保留了模板自带的两个模块：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /health | 健康检查 |
| GET | /status | 系统监控：CPU、内存、网络速率、各磁盘 IO、运行时长 |
| GET | /api/users | 用户列表 |
| POST | /api/users | 创建用户 |
| GET | /api/users/{id} | 查询用户 |
| PUT | /api/users/{id} | 更新用户 |
| DELETE | /api/users/{id} | 删除用户 |

```bash
curl http://localhost:8084/status
curl -X POST http://localhost:8084/api/users \
  -H "Content-Type: application/json" \
  -d '{"name":"John","age":30}'
```

## 编译

纯 Go SQLite 驱动，可交叉编译出静态单文件：

```bash
# Linux
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tracepulse-linux

# Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o tracepulse.exe

# macOS
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o tracepulse-macos
```

仓库自带 GitHub Actions，推送到 `main` 会自动构建 6 个平台的产物（Linux amd64 / arm64 / armv7、Windows amd64、macOS amd64 / arm64），打包后发布到 `dev-latest` 预发布版本。

> **注意**：`modernc.org/libc` 未提供 MIPS 实现，因此**无法**构建 `linux/mipsle`。若需要在 MIPS 架构的 OpenWrt 路由器上运行，必须改用 CGO 版驱动（`mattn/go-sqlite3` + `gorm.io/driver/sqlite`）并配合 OpenWrt SDK 工具链编译。

## 优雅关闭

收到 `SIGINT` / `SIGTERM` 后按依赖顺序收敛：停止接收新请求 → 排空队列并把内存中的链路全部落盘 → 停止告警协程。全程受 `server.shutdown_timeout_seconds` 约束，超时则强制退出。

## 测试

```bash
go test ./...
```

覆盖配置补全、上报协议容错、过滤查询、页面 HTML 转义、告警去重与模板渲染等。测试使用临时目录中的真实 SQLite 库，走与生产完全相同的表结构。

## 许可证

MIT License
