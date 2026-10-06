# v2ex-notifier

一个服务做两件事：

1. **提醒**：轮询 V2EX 提醒，发现新消息时通过飞书自定义机器人推送**交互卡片**。
2. **签到**：每天定时给 **V2EX** 和 **2libra** 签到，结果汇总成一张飞书卡片。

- 数据源：V2EX API 2.0 `GET /api/v2/notifications`
- 认证：Personal Access Token（`Authorization: Bearer ...`）；签到另需站点 Cookie
- 去重：本地 bbolt 保存已推送过的 `notification.id`
- 推送：飞书 webhook，支持签名校验、失败重试、多条聚合
- 部署：单容器（Docker / Podman 均可），distroless 风格，非 root 运行

## 快速开始

### 1. 获取 V2EX Personal Access Token（只想签到可跳过）

打开 <https://www.v2ex.com/settings/tokens> 创建令牌，复制形如
`00000000-1111-2222-3333-444444444444` 的占位值，替换掉 `.env` 里的 `V2EX_TOKEN`。

> 令牌有有效期（30/60/90/180/360 天）。建议单独建一个只用于本服务的令牌，到期轮换时只改 `V2EX_TOKEN`。
>
> `V2EX_TOKEN` **可以留空**：留空就只跑每日签到、不轮询 V2EX 提醒。

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
$EDITOR .env            # 至少填 FEISHU_WEBHOOK；V2EX_TOKEN 留空则只签到
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

命名规则：同一子系统的变量共用前缀，分层排列，最需要改的在最前面。

```
V2EX_*    V2EX API 与每日签到
LIBRA_*   2libra
FEISHU_*  飞书机器人
CHECKIN_* 签到调度
NOTIFY_*  通知行为
HTTP_ / STATE_ / LOG_ / TZ  通用运维
```

### 1. 必填（不填服务跑不起来）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `FEISHU_WEBHOOK` | — | **必填**，飞书机器人 webhook；提醒与签到卡片都走它 |
| `FEISHU_SECRET` | 空 | 机器人开启签名校验时必填 |

### 2. 凭据（需要你填写，填了才启用对应功能）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `V2EX_COOKIE` | 空 | V2EX 网页会话 Cookie（`A2=...`，2FA 还需 `A2O`），**签到**用。填了才启用 V2EX 签到 |
| `LIBRA_COOKIE` | 空 | 2libra 凭据：`access_token=...`，或裸 token（无 `=`/`;` 时按 Bearer 发送）。填了才启用 2libra 签到 |
| `V2EX_TOKEN` | 空 | **只用于 V2EX 提醒**（API 2.0）。**可留空**——留空就只跑签到，不轮询提醒 |

> **为什么 V2EX 有 Token 和 Cookie 两个？** 因为 V2EX 自己有两套互不相通的认证：
> API 2.0（`/api/v2/*`）认 `V2EX_TOKEN`，网页（`/mission/daily`）认会话 Cookie。
> Token 拿到签到页会被 302 到 `/signin`，所以签到必须用 Cookie；两件事都做就两个都填。
> 而 2libra 只有一套凭据（`access_token`），Cookie 或 Bearer 只是同一个值的两种传法。

### 3. 默认即可（有合理默认值，按需修改）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `CHECKIN_TIME` | `06:00` | 每日签到时间（`HH:MM`，按 `TZ`） |
| `CHECKIN_ON_START` | `true` | 启动补签：今天还没签就直接签；同一天重启跳过。设为 `false` 则只等 `CHECKIN_TIME` |
| `TZ` | `Asia/Shanghai` | 时区；调度时间与卡片/日志时间戳都用它 |
| `V2EX_POLL_INTERVAL` | `60s` | 轮询间隔，最小 `5s` |
| `V2EX_FIRST_RUN` | `skip` | `skip` 首轮只记录不推送；`push` 全推 |
| `V2EX_MARK_READ` | `false` | 推送成功后调用 `DELETE /notifications/:id` |
| `V2EX_FILTER_TYPES` | 空 | 只推指定类型：`reply,mention,thanks,other` |
| `V2EX_MAX_PAGES` | `3` | 每轮最多翻几页（每页 20 条） |
| `NOTIFY_STARTUP` | `true` | 启动时发送「服务已启动」卡片 |
| `NOTIFY_STARTUP_COOLDOWN` | `10m` | 两条启动消息的最小间隔；重启循环不会刷屏，设 `0` 则每次都发 |
| `NOTIFY_ALERT_ON_ERROR` | `true` | 连续失败达到阈值时发飞书告警 |
| `HTTP_TIMEOUT` | `20s` | 单次请求超时 |
| `STATE_PATH` | `state.db` | 状态文件路径；容器内由 compose 覆盖为 `/data/state.db` |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `HTTPS_PROXY` | 空 | 仅 V2EX 与签到请求走此代理；飞书始终直连（标准变量） |

### 4. 高级（默认指向公网站点，一般不用改）

| 变量 | 默认值 |
|---|---|
| `V2EX_API_BASE_URL` | `https://www.v2ex.com/api/v2` |
| `V2EX_WEB_BASE_URL` | `https://www.v2ex.com` |
| `LIBRA_BASE_URL` | `https://2libra.com` |

### 每日签到

签到对两个站点分别执行，最后合并成**一张**飞书卡片：

```
┌────────────────────────────────────┐
│ ⚠️ 每日签到 · 1/2 成功              │
├────────────────────────────────────┤
│ ✅ V2EX                            │
│     └ 获得 18 铜币                  │
│ ❌ 2libra                          │
│     └ Cookie 已失效（HTTP 401）     │
├────────────────────────────────────┤
│ 签到时间 2026-10-02 06:00:05       │
└────────────────────────────────────┘
```

**站点凭据怎么拿：**

| 站点 | 拿法 |
|---|---|
| V2EX | 登录 v2ex.com → F12 → Application → Cookies → `https://www.v2ex.com`，复制 `A2` 的值（开了 2FA 要连 `A2O` 一起），拼成 `A2=...; A2O=...`。最稳是直接复制任一请求头里的整行 `Cookie:` |
| 2libra | 登录 2libra.com → F12 → Network → 任一请求 → 请求头 `cookie`，取 `access_token=` 后面的值，拼成 `access_token=...`；也可直接填这个值本身（自动按 Bearer 发送） |

> V2EX 签到走的是网页 `/mission/daily`，**只认 Cookie，不认上面的 API Token**，两者都要配。
> 2libra 站点地址和 V2EX 网页源可用 `LIBRA_BASE_URL` / `V2EX_WEB_BASE_URL` 覆盖，只用于自建或测试，日常不用改。

**设计要点：**

- **签到幂等**：站点返回「今天已经签到过了」按成功处理，重复执行不会报错。
- **一张卡片**：两个站点的结果合并推送，不会一次发两条。
- **失败不中断**：一个站点 401 不影响另一个站点签到，卡片里逐条列明。
- **自动重试**：网络错误 / 429 / 5xx 会重试 3 次（2s、4s）；签到幂等，重试不会重复签。
- **Cloudflare 识别**：2libra 若返回人机验证页，会明确提示而不是报“签到失败”。
- **手动触发**：`./bin/notifier --checkin`（`--dry-run` 只打印不发送）。
- **重启不重复、停机不漏签**：`CHECKIN_ON_START=true`（默认）下，服务启动时若发现今天还没签（比如 06:00 时容器正好在重启）会立即补签；已签过则跳过。调度到点时也会再查一次，避免一天两张卡片。
- **不再依赖 GitHub Actions**：签到调度在容器内完成，因此没有「仓库 60 天无活动 → 定时任务被自动停用」这个问题。

手动跑一次：

```bash
set -a; . ./.env; set +a
./bin/notifier --checkin            # 真签到 + 推送
./bin/notifier --checkin --dry-run  # 只打印结果，不推送、不记录
```

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
│ 每日签到: V2EX + 2libra（每天 06:00 Asia/Shanghai） │
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

这个命令会忽略 `NOTIFY_STARTUP=false` 和冷却时间，适合用来验证 webhook 是否可用。

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
每 V2EX_POLL_INTERVAL 触发一次
   └─ GET /api/v2/notifications?p=1..V2EX_MAX_PAGES     （按时间倒序）
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

`v2ex.com` 从很多网络直接不可达（实测：DNS 把 `www.v2ex.com` 解析到
`2a03:2880:...`，那是 Facebook 的 IPv6 段，典型污染），而 `open.feishu.cn` 需要直连。做法：

- **V2EX 请求**读标准环境变量 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`
- **飞书请求**显式禁用代理（`Proxy: nil`，有单测守着）

没有自定义代理变量 —— 少一个容易配错的旋钮。`compose.yaml` 也不给默认值，
完全由 `.env` 决定：

| 环境 | `.env` 里 |
|---|---|
| 本地笔记本（走 Clash） | `HTTPS_PROXY=http://host.docker.internal:7897` |
| 云端服务器（直连 v2ex.com） | 不写这一行 |

> `.env` 里的地址是按**容器**写的。在宿主机上直接跑二进制时（`./scripts/dry-run.sh`、
> `make run`），`scripts/local-env.sh` 会自动把 `host.docker.internal` /
> `host.containers.internal` 改写成 `127.0.0.1` —— 否则宿主机解析不了这个域名，
> 本地验证会报 `lookup host.docker.internal: no such host` 而直接失败。

### 两个坑

**1. 容器里不能用 `127.0.0.1`。** 容器内的 `127.0.0.1` 是容器自己，不是宿主机。
必须用 `host.docker.internal`（Docker）/ `host.containers.internal`（Podman），
或者用 `--network=host` 配 `127.0.0.1`。上面说了，宿主机上直接跑时这条会自动改写。

**2. podman 会把宿主机的 `*_proxy` 变量复制进容器。** 宿主机上的
`https_proxy=http://127.0.0.1:7897` 会被原样带进去，而那个地址在容器里指向容器自己 ——
表现是“代理明明配了却连不上”。`compose.yaml` 里显式把继承来的小写
`http_proxy` / `https_proxy` 清空，只让 `.env` 生效。Docker 没有这个行为，
两边因此表现一致。

> Go 的 `httpproxy` 取 `HTTPS_PROXY` 优先于 `https_proxy`，所以即使 podman 注入了错的小写值，
> 显式设置的大写值仍然生效 —— 这一点已在容器内实测，不是推测。

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
./bin/notifier --once     # 单次轮询
./bin/notifier --checkin  # 立即签到一次
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
  -e HTTPS_PROXY=http://host.docker.internal:7897 \
  --add-host host.docker.internal:host-gateway \
  -v v2ex-notifier-data:/data \
  ghcr.io/sunyin0818/v2ex-notifier:latest
```

或者仍用 compose，通过环境变量换成远端镜像：

```bash
echo 'V2EX_NOTIFIER_IMAGE=ghcr.io/sunyin0818/v2ex-notifier:latest' >> .env
podman compose pull && podman compose up -d --no-build
```

### 清理旧 tag

GHCR 是按 **manifest** 删版本的，不是按 tag。多个 tag 经常指向同一个 manifest
（实测 `0` / `0.1` / `0.1.0` / `latest` / `sha-81f53dc` 五者共用一个 digest），
所以“删掉那个 `sha-` tag”会连发布版一起删掉。

`scripts/ghcr-prune.sh` 只删**不含任何发布 tag** 的 manifest，并在预演时把
“和发布 tag 共用 manifest 的旧 tag”列出来警告：

```bash
./scripts/ghcr-prune.sh                                     # 预演（匿名只读，无需令牌）

# 实际删除需要 delete:packages 权限
# classic PAT: 勾选 delete:packages
# fine-grained PAT: Packages -> Read and write
GHCR_TOKEN=<token> ./scripts/ghcr-prune.sh --apply
# 装了 gh 也可以： GHCR_TOKEN=$(gh auth token) ./scripts/ghcr-prune.sh --apply
```

### 发布前检查

workflow 会在 `test` job 卡住以下情况，不通过就不出镜像：

- `gofmt -l` 非空
- `go vet` 报错
- 单测失败（含 `-race`）
- `scripts/smoke.sh` 端到端失败

## 部署到云端服务器

云端**不要**用仓库根目录的 `compose.yaml` —— 它是为本地开发的（`build:`、
`extra_hosts: host.docker.internal`）。`deploy/` 里有一份自包含的云端包：

```
deploy/
├── compose.yaml    # 钉版本拉镜像，无代理配置，TZ，持久卷
├── env.example     # 只有 V2EX_TOKEN / FEISHU_WEBHOOK / FEISHU_SECRET
└── README.md       # 三步部署 + 排查表
```

```bash
scp -r deploy/ server:/opt/v2ex-notifier/
ssh server 'cd /opt/v2ex-notifier && cp env.example .env && chmod 600 .env && $EDITOR .env'
ssh server 'cd /opt/v2ex-notifier && docker compose pull && docker compose up -d'
```

关键约定（细节见 `deploy/README.md`）：

| 点 | 说明 |
|---|---|
| **钉版本，不用 `latest`** | `restart: unless-stopped` 下重启不该悄悄换代码。升级时改 tag 再 pull。 |
| **不设 `HTTPS_PROXY`** | 云端直连 v2ex.com。填了本地地址会静默连不上。 |
| **`/data` 必须是持久卷** | 丢了会重置去重基准。 |
| **`TZ` 显式设置** | Podman 挂载宿主机 `/etc/localtime`，Docker 不挂，不设就是 UTC。 |
| **首次部署不刷屏** | `V2EX_FIRST_RUN=skip` 只记录不推送，只出一张 🟢 启动卡片。 |

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
`V2EX_MARK_READ` 删除提醒。它需要 `go`、`python3`、`curl`。

## 常见问题

**启动就退出，日志 `V2EX rejected the token`**
令牌无效或已过期，去 <https://www.v2ex.com/settings/tokens> 重新生成，更新 `V2EX_TOKEN` 后重启。

**飞书返回 `sign match fail`**
开启了签名校验但没填 `FEISHU_SECRET`，或 secret 复制错了。

**日志 `poll failed: ... dial tcp ... timeout`**
V2EX 不可达。检查 `HTTPS_PROXY` 是否指向可用的代理、地址是否是 `host.docker.internal`
（不是 `127.0.0.1`），以及代理节点本身是否正常。

**一直没有推送**
确认 `V2EX_FIRST_RUN` 的行为：首次启动默认只记录不推送。看 `LOG_LEVEL=debug` 的日志确认是否真的没有新提醒。

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

**签到卡片显示 `Cookie 已失效` / `HTTP 401`**
凭据过期了。V2EX 换 `V2EX_COOKIE`（重新登录后复制 `A2`，2FA 账号还要 `A2O`），2libra 换 `LIBRA_COOKIE`。改完 `.env` 后 `docker compose up -d` 重建容器。

**签到卡片显示 `未找到签到 token`**
V2EX 的签到页结构变了，或者是页面压根没登录成功。先用浏览器确认 `https://www.v2ex.com/mission/daily` 打开后是签到按钮而不是登录页。

**启动日志出现 `next check-in scheduled` 但时间不对**
容器的 `TZ` 不对（检查 `.env` 里的 `TZ` / `compose.yaml`）；`CHECKIN_TIME` 是按这个时区解释的。

**不想用签到**
清空 `V2EX_COOKIE` 和 `LIBRA_COOKIE` 即可（站点凭据存在就启用）。启动卡片会显示 `每日签到: 未启用`。
