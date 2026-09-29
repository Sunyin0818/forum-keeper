# v2ex-notifier

轮询 V2EX 提醒，发现新消息时通过飞书自定义机器人推送**交互卡片**。

- 数据源：V2EX API 2.0 `GET /api/v2/notifications`
- 认证：Personal Access Token（`Authorization: Bearer ...`）
- 去重：本地 bbolt 保存已推送过的 `notification.id`
- 推送：飞书 webhook，支持签名校验、失败重试、多条聚合
- 部署：单容器（Docker / Podman 均可），distroless 风格，非 root 运行

## 快速开始

### 1. 获取 V2EX Personal Access Token

打开 <https://www.v2ex.com/settings/tokens> 创建令牌，复制形如
`00000000-1111-2222-3333-444444444444` 的占位值，替换掉 `.env` 里的 `V2EX_TOKEN`。

> 令牌有有效期（30/60/90/180/360 天）。建议单独建一个只用于本服务的令牌，到期轮换时只改 `V2EX_TOKEN`。

### 2. 创建飞书自定义机器人

在目标群 → 设置 → 群机器人 → 添加机器人 → **自定义机器人**，拿到 webhook：

```
https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

安全设置三选一：

| 方式 | 说明 | 本服务支持 |
|---|---|---|
| 签名校验 | 生成 `secret`，请求带 `timestamp` + `sign` | ✅ 设置 `FEISHU_SECRET` |
| 自定义关键词 | 消息里必须包含关键词 | ✅ 卡片标题含 “V2EX” |
| IP 白名单 | 限制来源 IP | 视部署环境 |

> 如果开启了**签名校验**，必须填 `FEISHU_SECRET`，否则飞书会返回
> `code=19021 sign match fail`。本服务会在启动日志中记录此类失败并重试。

### 3. 配置并启动

```bash
./scripts/init-env.sh   # 从 .env.example 生成 .env（已存在则拒绝覆盖）
$EDITOR .env            # 至少填 V2EX_TOKEN 和 FEISHU_WEBHOOK
```

```bash
# Docker
docker compose up -d --build

# Podman
podman compose up -d --build
# 或分步：
podman build -t v2ex-notifier:latest .
podman-compose up -d
```

查看日志：

```bash
docker compose logs -f      # 或 podman compose logs -f
```

## 配置项

| 变量 | 默认值 | 说明 |
|---|---|---|
| `V2EX_TOKEN` | — | **必填**，Personal Access Token |
| `FEISHU_WEBHOOK` | — | **必填**，飞书机器人 webhook |
| `FEISHU_SECRET` | 空 | 开启了签名校验时必填 |
| `POLL_INTERVAL` | `60s` | 轮询间隔，最小 `5s` |
| `V2EX_PROXY` | 空 | 仅 v2ex.com 走此代理；飞书始终直连 |
| `FIRST_RUN` | `skip` | `skip` 首轮只记录不推送；`push` 全推 |
| `MARK_READ` | `false` | 推送成功后调用 `DELETE /notifications/:id` |
| `FILTER_TYPES` | 空 | 只推指定类型：`reply,mention,thanks,other` |
| `ALERT_ON_ERROR` | `true` | 连续失败达到阈值时发飞书告警 |
| `STARTUP_MESSAGE` | `true` | 启动时发送「服务已启动」卡片 |
| `STARTUP_MESSAGE_COOLDOWN` | `10m` | 两条启动消息的最小间隔；重启循环不会刷屏，设 `0` 则每次都发 |
| `MAX_PAGES` | `3` | 每轮最多翻几页（每页 20 条） |
| `HTTP_TIMEOUT` | `20s` | 单次请求超时 |
| `STATE_PATH` | `state.db` | 状态文件路径；容器内由 compose 覆盖为 `/data/state.db` |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## 验证（不打扰群里任何人）

```bash
# 只读一次，打印「将会推送什么」：不发送、不写盘、不创建任何文件
./scripts/dry-run.sh

# 或者不用脚本，直接来：
set -a; . ./.env; set +a
./bin/notifier --once --dry-run
```

> `make` 不是必须的，上面的命令等价于 `make dry-run`。如果装了 make，也可以直接
> `make dry-run` / `make test` / `make build`。

`--dry-run` 以**只读**方式打开状态库（文件不存在时用空状态），绝不会创建或修改任何
文件。也可以对已有容器执行：
podman run --rm --network=host \
  --env-file .env -e STATE_PATH=/data/state.db \
  -v v2ex-notifier_notifier-data:/data \
  v2ex-notifier:latest --once --dry-run
```

输出示例：

```
level=INFO msg="dry-run: would push" id=1234 member=alice text="回复了你的主题" topic=555 snippet="@alice 我也遇到过同样的问题"
```

## 启动消息

服务启动时会先发一张绿色卡片，方便确认部署成功和参数是否符合预期：

```
┌────────────────────────────────────┐
│ 🟢 V2EX 通知服务已启动              │
├────────────────────────────────────┤
│ 版本: 1338e0f                      │
│ 账号: @yourname (id 12345)         │
│ 轮询间隔: 1m0s                     │
│ 首轮策略: skip                     │
│ 标记已读: 否                       │
│ 类型过滤: 全部                     │
│ V2EX 代理: 已设置                  │
│ 状态文件: /data/state.db           │
├────────────────────────────────────┤
│ 启动时间 2026-09-29 15:30:00       │
└────────────────────────────────────┘
```

> 代理项只显示「已设置 / 未设置」。代理 URL 可能内嵌账号密码，因此**不会**
> 写进消息里。

只发一次测试消息（不启动轮询、不发送、不轮询）：

```bash
set -a; . ./.env; set +a
./bin/notifier --notify-startup
```

这个命令会忽略 `STARTUP_MESSAGE=false` 和冷却时间，适合用来验证 webhook 是否可用。

**防刷屏**：容器常常配 `restart: unless-stopped`，如果启动后很快崩掉重启，
默认 `10m` 的冷却时间会抑制重复消息（日志会记录 `startup message skipped (cooldown)`）。

## 卡片长这样

```
┌────────────────────────────────────┐
│ 🙏 V2EX · 感谢了你的主题            │
├────────────────────────────────────┤
│ @carol 感谢了你的主题               │
│ 📄 关于 xxx 的讨论                  │
│ > @carol 我也遇到过同样的问题        │
│                        [ 查看 ]     │
├────────────────────────────────────┤
│ 来自 V2EX · 1 条                    │
└────────────────────────────────────┘
```

同一轮多条提醒会合并成一张卡片（标题形如 `💬 V2EX · 3 条回复`），
超过 10 条只展示前 10 条并在底部提示剩余数量。

## 工作原理

```
每 POLL_INTERVAL 触发一次
   └─ GET /api/v2/notifications?p=1..MAX_PAGES     （按时间倒序）
        └─ 与本地已见 id 求差集
             ├─ 无新消息 → 结束
             └─ 有新消息 → 组卡片 → POST 飞书 webhook
                  └─ 2xx 之后才把 id 写入本地状态
```

关键设计：

- **先发送成功，再落盘**：飞书失败时 id 不写入，下一轮自动重试，不会丢消息。
- **首轮只记录**：新部署时把当前提醒全部标记为已见，避免一次刷屏。
- **翻页提前终止**：提醒按时间倒序，整页都已见过就不再翻更旧的页。
- **限流友好**：V2EX 限制每 IP 600 次/小时，`60s` 轮询约 60 次/小时；客户端会解析 `X-Rate-Limit-*` 并记录（`LOG_LEVEL=debug` 可见）。
- **失败退避**：网络错误 / 5xx / 429 最多重试 3 次（2s、4s）；401/403 视为令牌失效，直接退出让容器重启并报警。
- **异常告警**：连续失败 ≥5 次时发一条飞书文本告警，30 分钟最多一条。

## 代理说明

`v2ex.com` 在国内访问不稳定，而 `open.feishu.cn` 需要直连。本服务用两个独立的
HTTP client：

- `V2EX_PROXY` 只作用于 V2EX 请求，留空则使用系统环境变量代理
- 飞书请求显式禁用代理

因此 `V2EX_PROXY` 在 `.env.example` 里是**注释掉**的：

- **本地运行**：不设置 → 走系统代理（或直连）
- **容器运行**：`compose.yaml` 自动注入默认值 `http://host.docker.internal:7897`；
  要改就在 `.env` 里显式设置即可

容器访问宿主机代理的地址：

| 运行时 | 地址 |
|---|---|
| Docker Desktop / Linux | `http://host.docker.internal:7897` |
| Podman | `http://host.containers.internal:7897` |
| 两者通用、最省事 | `--network=host` + `http://127.0.0.1:7897` |

`compose.yaml` 已配置 `extra_hosts: host.docker.internal:host-gateway`，
Docker 与 Podman 均可解析。

## 不使用容器

```bash
go build -o bin/notifier ./cmd/notifier
set -a; . ./.env; set +a
./bin/notifier            # 常驻
./bin/notifier --once     # 单次
```

## 开发

**没有 make 也能用**：

```bash
./scripts/init-env.sh   # 生成 .env（0600 权限，已存在则不覆盖）
./scripts/dry-run.sh
./scripts/smoke.sh

# 装了 make 的话
go test ./...     # 等价 make test
go vet ./...      # 等价 make vet
gofmt -w .        # 等价 make fmt
go build -o bin/notifier ./cmd/notifier   # 等价 make build
```

`scripts/smoke.sh` 会验证：首轮只记录、第二轮推送新提醒、主题标题解析、
`MARK_READ` 删除提醒。它需要 `go`、`python3`、`curl`。

## 常见问题

**启动就退出，日志 `V2EX rejected the token`**
令牌无效或已过期，去 <https://www.v2ex.com/settings/tokens> 重新生成，更新 `V2EX_TOKEN` 后重启。

**飞书返回 `sign match fail`**
开启了签名校验但没填 `FEISHU_SECRET`，或 secret 复制错了。

**日志 `poll failed: ... dial tcp ... timeout`**
V2EX 不可达。检查 `V2EX_PROXY` 是否指向可用的代理，以及代理节点本身是否正常。

**一直没有推送**
确认 `FIRST_RUN` 的行为：首次启动默认只记录不推送。看 `LOG_LEVEL=debug` 的日志确认是否真的没有新提醒。

**重复推送同一条**
不会发生：`notification.id` 是稳定的去重键。若手动删除了 `/data/state.db`，会退化成一次「首轮」。
