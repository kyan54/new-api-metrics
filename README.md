# New API Metrics

独立、只读、轻量的 New API 月度用量统计服务。Go 单进程，静态页面内嵌，不需要 Node.js、Redis 或额外数据库服务。保持 New API 官方镜像独立升级。

## 功能

- 渠道、API Key、用户三个统计维度，按 ID 汇总，支持同名 Key。
- 自然月、固定 IANA 时区，渠道／用户／Key／模型交叉筛选。
- 输入 Token、输出 Token、总 Token、请求数、每日趋势。
- 消费额度与参考折算费用、CSV 导出（与已查询的筛选条件一致）。
- 独立管理员登录，HttpOnly 会话 Cookie，8 小时过期，重启服务使会话失效。
- 只读 SQLite 连接，不创建源数据库，不建索引，不更改 New API 配置。

## 统计口径

1. 只统计 logs.type = 2 消费记录。请求数是消费记录条数，不保证等于客户端请求次数；重试是否生成多条记录取决于 New API 的记账方式。
2. 输入、输出直接取日志的 prompt_tokens、completion_tokens，总 Token 为两者相加。不同渠道对缓存、推理 Token 的记录语义可能不同；本服务不再次加计这些子项，也不保证与供应商账单一致。
3. 月报采用 [月初零点, 下月月初零点)，默认 Asia/Taipei，不是最近 30 天。可用 REPORT_TIMEZONE 修改，支持夏令时。
4. 渠道／用户按 ID 汇总。Key 按 Key ID 和日志中的用户 ID 汇总，避免历史归属变化混算。存在的对象显示当前名称；删除对象使用日志名称（如果有）或 ID 标记。
5. 三个视图代表同一批消费，不能相加。零 Token 记录单独计数，但无法仅据零值判断缺失 usage 还是实际零用量。
6. 参考费用 = 日志 quota ÷ QUOTA_PER_UNIT。默认 500000 和 USD 是可配置参数，不会自动读取价格设置或还原历史汇率。与源实例计费设置不一致时请调整。
7. Agent Plan／Coding Plan 月费、超额费及供应商侧额度不在此数据库中，因此不作为实际账单或剩余额度展示。
8. 历史统计依赖源日志。日志清理、关闭消费日志、旁路直连导致的缺失无法恢复。需要年度查询时，请在 New API 保留相应月份的消费日志。
9. 当前只支持一个 SQLite 数据源。独立日志数据库、MySQL、PostgreSQL 尚未支持。

## 兼容性

已对照 New API v1.0.0-rc.23 实际数据库结构开发；自动识别 logs.channel_id 或 logs.channel。启动时验证必需字段，结构不兼容时直接报错，不静默返回零。

必须存在 logs、channels、tokens、users 表。仅查询用量、名称和 ID，不查询密码、API Key 原文、对话内容或 IP。升级 New API 前先备份；升级后核对一笔消费。

## Docker 部署

在与 New API 数据库同一台主机部署，避免网络文件系统上的 SQLite 锁问题。

    git clone git@github.com:kyan54/new-api-metrics.git
    cd new-api-metrics
    cp .env.example .env
    # 编辑 .env，设置独立 ADMIN_PASSWORD 和真实 NEW_API_DATA_DIR
    docker compose up -d --build

默认仅监听宿主机 127.0.0.1:8090。需要局域网访问时，在 .env 设置 BIND_IP 为服务器内网 IP，再重建容器。公网访问使用 HTTPS 反向代理，并设置 COOKIE_SECURE=true。

NEW_API_DATA_DIR 是整个目录，不是 .db 文件，需同时提供可能存在的 -wal、-shm。Compose 使用只读挂载且禁止自动创建不存在的宿主机目录。不要使用 immutable=1 读取运行中的数据库，它可能忽略 WAL 并导致陈旧数据。

容器默认 UID/GID 为 65532:65532。宿主机目录必须可遍历，数据库及现有 WAL/SHM 文件必须可读。不要为方便而放开所有权限；可给统计容器配置已获授权的专用组。若 WAL 需要恢复或创建 SHM 而只读挂载无法完成，应先由 New API 正常恢复数据库，或使用 SQLite 在线备份生成一致快照后读取。不要直接复制运行中的单个 .db 文件作为一致备份。

Compose 的 128m 内存、0.5 CPU 是试运行上限，不是实测常驻占用保证。数据多时根据查询调整。应用同时只运行一个数据查询，15 秒超时；无持续轮询任务。

### 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| LISTEN_ADDR | :8090 | 容器／二进制监听地址 |
| NEW_API_DB | /data/one-api.db | SQLite 文件；Compose 内为 /source/one-api.db |
| ADMIN_USER | admin | 独立管理员账号 |
| ADMIN_PASSWORD | 无 | 必填，至少 12 字节；不要提交到仓库 |
| ADMIN_PASSWORD_FILE | 无 | 从秘密文件读取密码，优先于环境变量 |
| REPORT_TIMEZONE | Asia/Taipei | 月报和每日趋势时区 |
| QUOTA_PER_UNIT | 500000 | 每单位金额对应额度，必须为正数 |
| QUOTA_CURRENCY | USD | 参考费用单位，仅标签 |
| COOKIE_SECURE | false | HTTPS 部署设为 true |

不要把 New API 管理员密码复用为统计服务密码。

### 访问与验证

- 网页：http://服务器内网IP:8090（已绑定内网地址时）
- GET /healthz 仅表示进程已启动，不能代替真实报表查询。
- 登录后选择月份，分别打开三个标签，比较合计是否一致。
- 导出 CSV，确认总 Token 与页面一致。
- 选择已知调用，核对源日志渠道、Key、用户归属。

### 升级与回退

    git pull --ff-only
    docker compose up -d --build

升级前记录当前 Git 提交和镜像 ID。回退时使用上一版本镜像重新创建统计容器；不会更改 New API 数据库。New API 自身继续使用官方镜像。

## 本地开发

需要 Go 1.26.4 或更高版本。

    go mod download
    go test -race ./...
    go vet ./...
    export NEW_API_DB=/absolute/path/to/test-one-api.db
    export ADMIN_PASSWORD='replace-with-your-test-password'
    export LISTEN_ADDR=127.0.0.1:8090
    go run .

纯 Go SQLite 驱动，无 CGO。Linux amd64 编译：

    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o new-api-metrics .

不要使用生产数据库作为测试写入目标。自动测试会在临时目录创建合成数据，覆盖月份边界、夏令时、三维合计、同名 Key、删除渠道、只读连接、WAL 读取、登录／会话、CSV 公式注入与筛选参数校验。

## 管理员权限边界

只有一个独立管理员，不复用 New API Cookie，不提供六用户分权。获得该管理员账号即可查看全部用量。

登录失败按连接来源 IP 限制为五分钟十次，不信任客户端传入的代理 IP 头；在统一反向代理后限制可能共用。只读 API 使用严格同站 Cookie，写操作要求自定义请求头，不开放 CORS。建议内网访问或 HTTPS 入口限制访问范围。
