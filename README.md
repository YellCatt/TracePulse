# TracePulse

轻量级链路追踪与日志采集服务端。单文件二进制 + SQLite，内置检索页面与邮件告警，既能在服务器上跑，也能塞进 OpenWrt 路由器。

业务侧按步上报事件，TracePulse 按 `trace_id` 聚合成完整链路落库；出问题时邮件直接把整条链路送到眼前，点开链接就是详情页——不用再去服务器上翻日志猜。

## 核心特性

- **上报永不阻塞业务**：事件进非阻塞队列，队列满直接丢弃并告警，绝不把采集方拖死
- **崩溃不丢事件**：事件先写 ndjson 兜底日志再入队，进程挂掉数据仍在
- **内存不泄漏**：活跃链路有 TTL 兜底，客户端漏发 `end` 事件也会强制落盘
- **零依赖前端**：检索页与详情页服务端渲染，手机浏览器无需 JS 也能排查
- **告警不炸群**：同链路同原因去重 + 全局最小间隔限流 + 队列丢弃聚合汇总
- **跨平台**：默认 `modernc.org/sqlite`（纯 Go 无 CGO）出静态单文件；MIPS 路由器自动切到 CGO 版 `mattn/go-sqlite3`，极路由可直接跑
- **日志按需分流**：三种输出模式（按级别分文件 / 分文件且向上叠加 / 单一文件）配合级别白名单，只要关心的那部分日志
- **开箱即用**：配置文件缺失自动生成，字段缺失才自动补全并回写（字段齐全时不动你的文件，注释得以保留）；首次启动自动灌入演示数据，打开页面就有东西可看
- **路由器免运维**：`startup.sh` 守护脚本自带崩溃重启、定时自更新与热替换

## 快速开始

### 环境要求

Go 1.22+

### 构建与运行

```bash
go build -o tracepulse
./tracepulse
```

首次运行会在 `config/config.yaml` 生成完整配置文件（含全部默认值），按需修改后重启即可。

> **注意**：配置与数据库都按**相对工作目录**解析，所以要在项目根目录下启动，否则会重新生成一份空配置并新建一个空库（表现是"改了配置没生效 / 数据不见了"）。用 systemd 或守护脚本部署时记得设 `WorkingDirectory`。

### 访问

| 入口 | 地址 |
|------|------|
| 链路检索页 | http://localhost:8086/traces |
| 链路详情页 | http://localhost:8086/trace/{trace_id} |
| Swagger 文档 | http://localhost:8086/swagger/index.html |
| 健康检查 | http://localhost:8086/health |
| 系统监控 | http://localhost:8086/status |

根路径 `/` 会重定向到 `/traces`。

首次启动若库里还没有任何链路，会自动灌入一批演示数据（覆盖 ok / warn / error / timeout 四种状态），打开页面即可看到效果，无需先接上报。正式部署在配置里把 `demo.disable` 设为 `true` 关闭。

## 上报协议

### POST /api/traces/report

`url` 可选，表示这条链路对应的业务入口（页面 URL 或接口地址），会记到链路的 `url` 字段并在列表页 / 详情页展示。两种传法：

| 传法 | 适用 |
|------|------|
| 请求体字段 `"url"` | 批量写法。与 `events` 同级 |
| 查询参数 `?url=` | 任意写法。**裸数组没有包裹层，只能用它传** |

请求体优先于查询参数；都没传就留空，不影响事件入库。超过 2048 个字符会截断（不会因为一个过长的 url 拒掉整批事件）。

请求体支持两种写法，任选其一：

```bash
# 写法一：批量上报（推荐）
curl -X POST http://localhost:8086/api/traces/report \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://shop.example.com/order/confirm?sku=A-01",
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
# 写法二：裸数组（单条上报更省事，url 走查询参数）
curl -X POST 'http://localhost:8086/api/traces/report?url=https://shop.example.com/pay' \
  -H "Content-Type: application/json" \
  -d '[{"trace_id":"req-7f3a91","level":"info","module":"pay","event":"start","message":"开始支付"}]'
```

单条上报也可以继续用批量写法，把 `url` 放进请求体：

```bash
curl -X POST http://localhost:8086/api/traces/report \
  -H "Content-Type: application/json" \
  -d '{"url":"/api/v1/pay/create","events":[{"trace_id":"req-7f3a91","level":"info","module":"pay","event":"start"}]}'
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
| `url` | 否 | 业务入口地址。**事件级字段，仅用于把值带进链路**，事件表不落这一列；同一条链路以首批上报的值为准 |

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
| url 长度 | 2048 字符，超出截断 | 硬编码上限 |
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
curl "http://localhost:8086/api/traces?status=error&start_time=1h&page_size=50"

# 耗时超过 3 秒的慢调用
curl "http://localhost:8086/api/traces?min_duration_ms=3000"

# 按关键词模糊搜索
curl "http://localhost:8086/api/traces?keyword=deadlock"
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
curl http://localhost:8086/api/traces/req-7f3a91
```

返回 `{"trace":{...},"events":[...]}`，事件按时间正序。未找到返回 404。

### GET /api/traces/stats

运行时指标，用于观测队列水位、判断是否需要调大队列或扩容：

```bash
curl http://localhost:8086/api/traces/stats
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

邮件中的「查看详情」链接由 `alert.public_url` 生成，部署到服务器后记得改成能被收件人访问的地址，例如 `http://10.0.0.5:8086`。

## 配置说明

配置文件路径固定为 `config/config.yaml`（相对工作目录）。首次运行自动生成完整配置。

**配置文件的写入时机**：只有当配置里**缺字段**时，程序才会回写补全默认值——此时会重排格式并丢掉你手写的注释（YAML 序列化的固有限制）。字段齐全时不动文件，注释可以放心保留。因此新增配置字段时，记得同步补进 `config.yaml`，否则注释会被抹掉一次。

完整示例（即仓库中 `config/config.yaml` 的内容，省略注释）：

```yaml
server:
  port: 8086
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
  mode: split
  levels: []
  disable_console: false
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
  public_url: http://localhost:8086
  slow_threshold_ms: 0
  timeout_seconds: 15
  dedup_seconds: 300
  min_interval_seconds: 60
  max_events_in_mail: 500
  queue_size: 256
demo:
  disable: false
  force: false
```

### server

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `port` | 监听端口 | 8086 |
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
| `level` | 最低日志级别（`debug`/`info`/`warn`/`error`），配了 `levels` 时失效 | info |
| `mode` | 输出模式，见下表 | split |
| `levels` | 级别白名单，**只输出列出的级别**（如 `[warn, error]`）；留空则按 `level` 阈值输出 | 空 |
| `disable_console` | 关闭控制台输出（容器里日志已交给 stdout 收集时可关） | false |

`mode` 三种取值：

| 模式 | 产物 | 说明 |
|------|------|------|
| `split`（默认） | `debug.log` `info.log` `warn.log` `error.log` | 按级别分文件，**文件之间不重叠**，每个文件只有该级别；`fatal` 归入 `error.log` |
| `range` | `debug.log` `info.log` `warn.log` `error.log` | 按级别分文件，**内容向上叠加**：`warn.log` 里含 warn 及以上，方便"只看异常及更严重" |
| `single` | `app.log` | 所有日志进一个文件，只按级别过滤 |

只会为真正生效的级别创建文件：`level: info` 时不会出现空的 `debug.log`。`levels` 与 `mode` 可组合，例如 `mode: single` + `levels: [warn, error]` 就是"一个文件里只有告警和错误"。

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
| `public_url` | 邮件「查看详情」链接的 base URL | http://localhost:8086 |
| `slow_threshold_ms` | 慢调用阈值（毫秒），0 表示关闭 | 0 |
| `timeout_seconds` | 单次发信超时 | 15 |
| `dedup_seconds` | 去重窗口（秒），显式设 0 关闭去重 | 300 |
| `min_interval_seconds` | 全局最小发信间隔 | 60 |
| `max_events_in_mail` | 邮件中最多附带的事件条数 | 500 |
| `queue_size` | 告警队列容量 | 256 |

### demo

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `disable` | 关闭启动时的演示数据写入，**正式部署建议设为 true** | false |
| `force` | 即使库里已有链路也再灌一批，用于反复演示 | false |

演示数据的 `trace_id` 带启动时间戳，不会撞上 `traces.trace_id` 的唯一索引；写入走 repository 直连，**不经过队列与 ndjson 兜底**，更重要的是不会触发告警（否则 error / timeout 状态会真的往外发邮件）。

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
│   ├── config.go           # 配置结构、默认值、缺失字段回写
│   ├── config.yaml         # 运行期读取的配置文件
│   ├── database.go         # 默认（纯 Go）驱动
│   ├── database_cgo.go     # MIPS 架构专用 CGO 驱动（build tag 切换）
│   └── database_common.go  # 两种驱动共用的初始化、索引、清理逻辑
├── controller/          # HTTP 处理层：JSON API + 页面渲染
│   ├── trace_controller.go
│   ├── templates.go        # 页面模板（HTML + CSS，零依赖）
│   └── status_controller.go
├── service/             # 业务逻辑层
│   ├── trace_service.go    # 链路聚合、落盘、TTL、清理
│   ├── alert_service.go    # 告警判定、去重、限流、发信
│   ├── alert_template.go   # 告警邮件模板
│   ├── demo_seed.go        # 首次启动的演示数据
│   └── status_service.go
├── repository/          # 数据访问层
│   └── trace_repository.go
├── model/               # 数据模型与请求结构
│   ├── trace.go
│   └── status.go
├── router/              # 路由注册与内嵌 Swagger 定义
├── view/                # 展示层格式化（网页与邮件共用）
├── logger/              # Zap 日志：三种输出模式 + 级别白名单
├── docs/                # Swagger 文档注册
└── startup.sh           # OpenWrt 路由器守护脚本（自启动 / 自更新 / 崩溃重启）
```

## 技术栈

- **语言**: Go 1.22（标准库 `net/http` + `http.ServeMux`）
- **ORM**: GORM
- **数据库**: SQLite
  - 默认：`glebarez/sqlite`（底层 `modernc.org/sqlite`，纯 Go 无 CGO）
  - MIPS：`gorm.io/driver/sqlite` + `mattn/go-sqlite3`（CGO 版，供极路由等 MIPS 设备使用）
- **日志**: Zap
- **系统监控**: gopsutil
- **API 文档**: Swagger（http-swagger）

## 附带接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /health | 健康检查，返回 `{"status":"ok","message":"Service is running"}` |
| GET | /status | 系统监控：CPU、内存、网络速率、各磁盘 IO、运行时长 |

```bash
curl http://localhost:8086/status
```

`/status` 返回示例（容量单位为 KB，速率为 KB/s，具体见 `units` 字段）：

```json
{
  "cpu": {"usage": 12.5, "count": 8, "vendor": "GenuineIntel", "model": "...", "mhz": 2600, "cache_size": 8192},
  "memory": {"total": 16777216, "used": 8388608, "free": 4194304, "available": 12582912, "usage": 50},
  "network": {"bytes_recv": 1048576, "bytes_sent": 524288, "recv_speed": 12.3, "send_speed": 4.5},
  "disk": [{"mountpoint": "C:", "total": 524288000, "used": 262144000, "free": 262144000, "usage": 50, "read_speed": 0, "write_speed": 128.5}],
  "uptime": 86400,
  "units": {"cpu": "核", "cpu_usage": "%", "memory": "KB", "memory_usage": "%", "network": "KB", "speed": "KB/s", "disk": "KB", "disk_usage": "%", "uptime": "秒"}
}
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

仓库自带 GitHub Actions，推送到 `main` 会自动构建 7 个平台的产物（Linux amd64 / arm64 / armv7 / mipsle、Windows amd64、macOS amd64 / arm64），打包后发布到 `dev-latest` 预发布版本。

其中 `linux/mipsle` 走独立的 `build-mips` 任务：GitHub 没有 MIPS 架构的托管 runner，所以不换机器而是在 x86_64 runner 上跑 musl 交叉工具链，开 CGO 静态链接出包，极路由可直接下载使用。

### MIPS 路由器（极路由等）

`modernc.org/libc` 没有 MIPS 实现，因此 MIPS 架构**不能**用 `CGO_ENABLED=0` 的默认驱动。项目已内置好切换：`config/database_cgo.go` 带 `//go:build mips || mipsle || mips64 || mips64le` 标签，构建 MIPS 时会自动改用 CGO 版驱动（`gorm.io/driver/sqlite` + `mattn/go-sqlite3`），初始化逻辑与默认实现共用 `config/database_common.go`，无需改动任何业务代码。

编译需要 OpenWrt SDK 提供的交叉工具链，并把 `CC` 指向它，例如极路由（MT7620/MT7621，mipsel 小端）：

```bash
export STAGING_DIR=/path/to/openwrt-sdk/staging_dir
export PATH=$STAGING_DIR/toolchain-mipsel_24kc_gcc-*/bin:$PATH

CGO_ENABLED=1 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
  CC=mipsel-openwrt-linux-gcc \
  go build -ldflags="-s -w" -o tracepulse-mipsle .

# 静态链接（路由器上通常缺 libc 之外的依赖，推荐）
CGO_ENABLED=1 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
  CC=mipsel-openwrt-linux-musl-gcc \
  go build -ldflags="-s -w -linkmode external -extldflags '-static'" \
  -o tracepulse-mipsle .
```

- `GOARCH` 取值：32 位用 `mipsle`（小端，绝大多数路由器），大端用 `mips`；64 位用 `mips64le` / `mips64`。
- `GOMIPS`：无 FPU 或内核未开启 FP 模拟时设 `softfloat`（默认），有硬浮点可设 `hardfloat`。
- 想在 Windows 上交叉编译，可用 WSL / Docker 跑上述命令，宿主 gcc 编不了 MIPS 目标。
- 体积可再压缩：加 `-tags sqlite_omit_load_extension` 关掉 SQLite 扩展加载，或用 `upx --best`（MIPS 需 upx 支持该架构）。

### 路由器守护脚本

仓库根目录的 `startup.sh` 是给 OpenWrt 用的守护脚本，把二进制丢上去就能长期跑：

- **自更新**：定时从 GitHub Release 下载，和当前二进制 `cmp` 比对，内容不同才热替换重启（相同则不重启，避免无谓抖动）
- **先起后更**：本地已有二进制时先启动、再后台检查更新，下载失败不会阻塞启动
- **崩溃自愈**：进程退出后指数退避重启（5s 起，上限 300s）
- **自愈目录**：插件目录不可写时自动退到 `/tmp`；日志目录被删会重建，否则 `>>` 重定向失败会导致进程根本拉不起来
- **防重复启动**：PID 文件 + `kill -0` 探测，避免起出两个实例抢同一个端口

```sh
sh startup.sh          # 前台运行，Ctrl+C 优雅退出
```

脚本顶部的配置区可直接改：`PLUGIN_DIR`（安装目录）、`DOWNLOAD_URL`（更新源）、`UPDATE_INTERVAL`（检查间隔，默认 3 小时）。

## 优雅关闭

收到 `SIGINT` / `SIGTERM` 后按依赖顺序收敛：停止接收新请求 → 排空队列并把内存中的链路全部落盘 → 停止告警协程。全程受 `server.shutdown_timeout_seconds` 约束，超时则强制退出。

## 测试

```bash
go test ./...
```

测试使用临时目录中的真实 SQLite 库，走与生产完全相同的表结构。覆盖范围：

| 包 | 覆盖内容 |
|----|----------|
| `config` | 老配置字段补全、回写行为、**仓库配置完整性**（缺字段会在启动时抹掉注释，用测试守住） |
| `logger` | 三种输出模式的实际文件产物、级别白名单过滤、阈值不建空文件、非法配置拒绝 |
| `service` | 链路聚合与增量合并、TTL 强制落盘、告警去重与限流、告警关闭时静默 |
| `controller` | 上报协议容错、参数校验、过滤查询、页面 HTML 转义 |
| `repository` | 多条件过滤、分页、时间范围 |
| `view` | 时间/耗时格式化、长文本截断 |

## 许可证

MIT License
