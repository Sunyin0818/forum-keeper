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
┌────────────────────────────────────────────┐
│ 💬 V2EX · 回复了你                          │
├────────────────────────────────────────────┤
│ @zhaixingzhaiyue 回复了你                   │
│ 📄 Fedora 上最好用的截图工具 - 微信          │
│ > @Sunyin 按的时候屏幕是不是会闪一下？       │
│                                [ 查看 ]     │
├────────────────────────────────────────────┤
│ 来自 V2EX · 1 条                            │
└────────────────────────────────────────────┘
```

字段来源（均为真实 API 字段）：

| 卡片中的位置 | 来源 |
|---|---|
| 标题动作（`回复了你` / `在回复中提到了你` / `感谢了你的主题`） | 由 `text` 的内容归类得出 |
| `@用户名` | `member.username` |
| `📄 主题标题` | 从 `text` 里 `class="topic-link"` 的锚文本提取 |
| `> 回复内容` | `payload`（纯文本），为空时回退到 `payload_rendered` 去标签 |
| `查看` 按钮 | 由 `text` / `payload` 里的 `/t/<id>` 拼出 |

> ⚠️ V2EX 的 `text` 字段是 **HTML 片段**，例如：
> `<a href="/member/x"><strong>x</strong></a> 在 <a href="/t/1#reply2" class="topic-link">标题</a> 里回复了你`
> 所有输出路径都会先去标签，**不会**把 `<a>` / `<strong>` 渲染到卡片里。
> 由于标题就在 `text` 里，正常情况**不需要**额外调 `GET /topics/:id`，只在
> 提取不到时才回退请求（省下限流配额）。

同一轮多条提醒会合并成一张卡片（标题形如 `💬 V2EX · 3 条回复`），
超过 10 条只展示前 10 条并在底部提示剩余数量。

## 工作原理

```
每 POLL_INTERVAL 触发一次
   └─ GET /api/v2/notifications?p=1..MAX_PAGES     （按时间倒序）
        └─ 与本地已见 id 求差集
             ├─ 无新消息 → 结束（info 级别不打日志，见 FAQ）
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

因此 `V2EX_PROXY` **只在 `.env` 里配置**，`compose.yaml` 不给默认值 —— 它必须区分环境：

| 环境 | `V2EX_PROXY` |
|---|---|
| 本地笔记本（走 Clash） | `http://host.docker.internal:7897` |
| 云端服务器（直连 v2ex.com） | 留空（或不写这一行） |

> 为什么不在 compose 里填个默认值？因为默认值必然只对一半环境正确，
> 另一半会**静默走一个不存在的代理**，表现为一直拉不到提醒、日志里
> `dial tcp ... timeout`。配置放 `.env` 里，两种环境各写各的。

本地容器访问宿主机代理的地址：

| 运行时 | 地址 |
|---|---|
| Docker Desktop / Linux | `http://host.docker.internal:7897` |
| Podman | `http://host.containers.internal:7897` |
| 两者通用、最省事 | `--network=host` + `http://127.0.0.1:7897` |

`compose.yaml` 已配置 `extra_hosts: host.docker.internal:host-gateway`，
Docker 与 Podman 均可解析。云端用不到这行，但也无害。

## 不使用容器

```bash
go build -o bin/notifier ./cmd/notifier
set -a; . ./.env; set +a
./bin/notifier            # 常驻
./bin/notifier --once     # 单次
```

## 自动构建并发布镜像

`.github/workflows/ci.yml` 做了两件事：

```
test （gofmt / go vet / go test -race / 离线端到端 smoke / 编译）
  └─ image （多架构构建 + 推送，PR 跳过）
```

### 触发与产物

| 触发 | 产生什么 |
|---|---|
| push 到 `main` | 只跑 `test`，**不出镜像** |
| push tag `v1.2.3` | 镜像 `1.2.3`、`1.2`、`1`、`latest` |
| push tag `v0.2.0` | 镜像 `0.2.0`、`0.2`、`latest`（没 `0`，见下） |
| push tag `v0.2.0-rc1` | 镜像 `0.2.0-rc1`（预发布**不加** `latest`） |
| Pull Request | 只跑 `test` |
| 手动 `workflow_dispatch` | 只跑 `test` |

两个约定：

- **0.x 不生成 `0` 别名。** semver 规定 `0.y.z` 明确不稳定，浮动 `0` 会误导；
  上 1.0 后把这行的 `enable` 去掉即可。
- **预发布不加 `latest`。** 靠 `flavor: latest=auto` 实现；`v0.2.0-rc1`
  只会得到 `0.2.0-rc1`，不会抢走 `latest`。

镜像平台：`linux/amd64` + `linux/arm64`，附带 provenance 和 SBOM。

> 构建器不跑 QEMU：`Dockerfile` 里 builder 阶段用 `--platform=$BUILDPLATFORM`
> 加 Go 交叉编译（`TARGETOS`/`TARGETARCH`），所以 arm64 镜像不需要在模拟器里
> 重编译整个 Go 工具链，构建快很多。

### 发版

```bash
git tag -a v1.0.0 -m "v1.0.0"
git push origin main --tags
```

推送后 GitHub Actions 会自动出镜像，`-X main.version=v1.0.0` 会被烤进二进制
（`--version` 可验证）。

> **为什么一次发版看起来跑了两个 CI？**
> `git push origin main --tags` 是**两个** push 事件：分支一个、tag 一个，
> 各自触发一次 workflow。这是故意的：分支那次是快速门禁（只跑 test，
> 约 1 分钟），tag 那次才构建并推镜像（约 1.5 分钟）。
>
> 同一个 commit 只被构建**一次**，不会重复。如果只想让 tag 那次跑，
> 就分两次推：先 `git push origin main`，确认绿了再 `git push origin v1.0.0`。
>
> run 标题带 ref（`ci · push · main` / `ci · push · v1.0.0`）以便区分。

### 镜像仓库

**默认发到 GHCR，不需要任何配置**（用内置的 `GITHUB_TOKEN`）：

```
ghcr.io/sunyin0818/v2ex-notifier:latest
```

> 首次推送后，包默认是 private。要在别的机器上拉取，去 GitHub →
> 你的头像 → Packages → 该 package → Package settings → Change visibility
> 改为 public（或 `docker login ghcr.io`）。

**可选：同时发 Docker Hub。** 在仓库 Settings → Secrets and variables →
Actions 里加两个 secret，workflow 会自动多推一份；没设就跳过：

| Secret | 值 |
|---|---|
| `DOCKERHUB_USERNAME` | 你的 Docker Hub 用户名 |
| `DOCKERHUB_TOKEN` | Docker Hub **Access Token**（不是登录密码） |

> GHCR 在国内部分网络下较慢，如果拉取是瓶颈，用 Docker Hub 或自建 registry 更实际。

### 跑已发布的镜像

```bash
docker run -d --name v2ex-notifier --restart unless-stopped \
  --env-file .env \
  -e STATE_PATH=/data/state.db \
  -e V2EX_PROXY=http://host.docker.internal:7897 \
  --add-host host.docker.internal:host-gateway \
  -v v2ex-notifier-data:/data \
  ghcr.io/sunyin0818/v2ex-notifier:latest
```

或者仍用 compose，通过环境变量换成远端镜像：

```bash
echo 'V2EX_NOTIFIER_IMAGE=ghcr.io/sunyin0818/v2ex-notifier:latest' >> .env
podman compose pull && podman compose up -d --no-build
```

### 发布前检查

workflow 会在 `test` job 卡住以下情况，不通过就不出镜像：

- `gofmt -l` 非空
- `go vet` 报错
- 单测失败（含 `-race`）
- `scripts/smoke.sh` 端到端失败

## 部署到云端服务器

云端不要用本地的 `compose.yaml` 默认值 —— 它带 `host.docker.internal:7897`
这类本地约定。最小可用的一份：

```yaml
# /opt/v2ex-notifier/compose.yaml
services:
  notifier:
    image: ghcr.io/sunyin0818/v2ex-notifier:0.1.0   # 钉版本，别用 latest
    container_name: v2ex-notifier
    restart: unless-stopped
    env_file: .env
    environment:
      STATE_PATH: /data/state.db
      TZ: Asia/Shanghai        # 启动卡片上的时间
    volumes:
      - notifier-data:/data    # 必须是持久卷，否则重启丢去重状态
volumes:
  notifier-data:
```

```bash
# /opt/v2ex-notifier/.env  —— 云端仅这三行即可
V2EX_TOKEN=...
FEISHU_WEBHOOK=...
FEISHU_SECRET=...          # 机器人开了签名校验才需要

# 注意：不要写 V2EX_PROXY，云端直连 v2ex.com
```

```bash
docker compose pull && docker compose up -d
docker compose logs -f
```

### 几个容易踩的点

| 点 | 说明 |
|---|---|
| **钉版本，不用 `latest`** | `latest` 会随时变动，重启可能就换了代码。写死 `:0.1.0`，升级时改成 `:0.2.0` 再 pull。 |
| **`V2EX_PROXY` 留空** | 填了本地那个代理地址会连不上 v2ex.com，表现为日志 `dial tcp ... timeout`。 |
| **`/data` 要持久卷** | 丢了不会刷屏（首轮只记录），但去重基准会重置，期间离线时的提醒可能不会被推。 |
| **`TZ` 建议显式设置** | Podman 会挂载宿主机 `/etc/localtime`，Docker **不会**，不设就是 UTC。 |
| **重启自动拉起** | `restart: unless-stopped` 已包含；docker daemon 开机启动即可。 |
| **升级方式** | 改 `.env`/compose 里的版本号 → `docker compose pull && docker compose up -d`。状态卷不受影响。 |
| **registry 选哪个** | 海外机器 GHCR 直连没问题；国内机器 GHCR 可能很慢，建议配 Docker Hub（见上）或自建 registry。 |

### 首次部署不会刷屏

新卷上第一次启动会走 `FIRST_RUN=skip`：把当前未读提醒全部标记为已见但不推送，
只出一张 🟢 启动卡片。之后只推真正的新提醒。

## 开发

**没有 make 也能用**：

```bash
./scripts/init-env.sh   # 生成 .env（0600 权限，已存在则不覆盖）
./scripts/dry-run.sh    # 只读：列出待记录/待推送的提醒，不发不写
./scripts/smoke.sh      # 离线端到端：mock V2EX + mock 飞书

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

**运行中长时间没有任何日志，服务是不是挂了？**
这是正常的。无新提醒时 info 级别不打日志（只在 debug 下输出
`no new notifications`），推送成功才会输出 `pushed notifications`。
想确认它还在轮询，把 `LOG_LEVEL=debug` 打开，每轮会输出一行
`rate limit remaining=...`。

**飞书卡片里出现 `<a href=...>` 这类标签**
不应该发生。V2EX 的 `text` 字段是 HTML 片段，所有渲染路径都会先去标签；
`scripts/smoke.sh` 有断言守着这个行为。若真遇到了，是上游格式变了，开个 issue 并附上
`--dry-run` 的输出。

**重复推送同一条**
不会发生：`notification.id` 是稳定的去重键。若手动删除了 `/data/state.db`，会退化成一次「首轮」。
