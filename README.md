# forum-keeper

论坛小助手：一个常驻容器做两件事。

1. **提醒** —— 轮询 V2EX 提醒，有新消息就推一张飞书交互卡片。
2. **签到** —— 每天定时给 **V2EX** 和 **2libra** 签到，结果汇总成一张飞书卡片（带余额）。

不用浏览器、不用 GitHub Actions、不监听任何端口。

## 快速开始

### 1. 建飞书机器人

目标群 → 设置 → 群机器人 → 添加机器人 → **自定义机器人**，拿到 webhook：

```
https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

如果机器人开了**签名校验**，还要把 `secret` 填到 `FEISHU_SECRET`。

### 2. 准备站点凭据

| 变量 | 用途 | 怎么拿 |
|---|---|---|
| `V2EX_TOKEN` | 只在 **V2EX 提醒**用（API 2.0） | <https://www.v2ex.com/settings/tokens>。**可留空**——留空就只签到 |
| `V2EX_COOKIE` | **V2EX 签到**用（网页 Cookie） | 见下方说明 |
| `LIBRA_COOKIE` | **2libra 签到**用 | 见下方说明 |

**V2EX_COOKIE**：登录 <https://www.v2ex.com/> 后按 `F12` → **Network** → 刷新 → 点任一
`www.v2ex.com` 请求 → **Request Headers** 里的 `Cookie:` 整行，原样复制。

也可以 `F12` → **Application** → Cookies → 复制 `A2` 的值；开了 2FA 还要连 `A2O` 一起。

**LIBRA_COOKIE**：登录 <https://2libra.com/> 后 `F12` → **Network** → 任一请求 →
请求头 `cookie`，取 `access_token=` 后面的值：

```
LIBRA_COOKIE=access_token=eyJhbGci...
# 或者直接填那串 token 本身（不带 = 时自动按 Bearer 发送）
```

> **为什么 V2EX 要两个凭据？** V2EX 自己有两套互不相通的认证：API 2.0
> （`/api/v2/*`）只认 `V2EX_TOKEN`，网页（`/mission/daily`）只认会话 Cookie。
> Token 访问签到页会被 302 到 `/signin`，所以签到必须用 Cookie。
> 2libra 只有一套凭据（`access_token`），Cookie 或 Bearer 是同一种值的两种传法。

### 3. 配置并运行

```bash
./scripts/init-env.sh   # 从 .env.example 生成 .env（已存在则拒绝覆盖，权限 0600）
$EDITOR .env            # 填 FEISHU_WEBHOOK + 站点凭据

docker compose up -d --build   # Podman 用 podman compose
docker compose logs -f
```

最小配置（只签到、不提醒）：

```ini
FEISHU_WEBHOOK=https://open.feishu.cn/open-apis/bot/v2/hook/...
V2EX_COOKIE=A2=...
LIBRA_COOKIE=access_token=...
```

不做容器也行：[不使用容器](#不使用容器)。

## 配置

命名规则：同一模块共用前缀，按「必填 → 凭据 → 默认即可 → 高级」排列。

### 必填

| 变量 | 默认 | 说明 |
|---|---|---|
| `FEISHU_WEBHOOK` | — | **必填**，飞书机器人 webhook |
| `FEISHU_SECRET` | 空 | 机器人开了签名校验时必填 |

### 凭据

填了才启用对应功能，不填就跳过该功能。

| 变量 | 默认 | 说明 |
|---|---|---|
| `V2EX_COOKIE` | 空 | V2EX 签到用（`A2=...`，2FA 加 `A2O`） |
| `LIBRA_COOKIE` | 空 | 2libra 签到用（`access_token=...` 或裸 token） |
| `V2EX_TOKEN` | 空 | V2EX 提醒用；留空则只签到 |

### 默认即可（按需修改）

| 变量 | 默认 | 说明 |
|---|---|---|
| `CHECKIN_TIME` | `06:00` | 每日签到时间（`HH:MM`，按 `TZ`） |
| `CHECKIN_ON_START` | `true` | 启动补签：今天还没签就补；同一天重启跳过 |
| `TZ` | `Asia/Shanghai` | 调度与所有时间戳的时区 |
| `V2EX_POLL_INTERVAL` | `60s` | 提醒轮询间隔，最小 `5s` |
| `V2EX_FIRST_RUN` | `skip` | 首次只记录不推送（防刷屏）；`push` 全推 |
| `V2EX_MARK_READ` | `false` | 推送成功后删除该条 V2EX 提醒 |
| `V2EX_FILTER_TYPES` | 空 | 只推 `reply,mention,thanks,other` |
| `V2EX_MAX_PAGES` | `3` | 每轮最多翻几页（每页 20 条） |
| `NOTIFY_STARTUP` | `true` | 启动时发「已启动」卡片 |
| `NOTIFY_STARTUP_COOLDOWN` | `10m` | 两条启动卡片最小间隔，防重启刷屏 |
| `NOTIFY_ALERT_ON_ERROR` | `true` | 连续失败达阈值时告警 |
| `HTTP_TIMEOUT` | `20s` | 单次请求超时 |
| `STATE_PATH` | `state.db` | 状态文件；容器内为 `/data/state.db` |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `HTTPS_PROXY` | 空 | 仅 V2EX 与签到请求走它，飞书始终直连 |

### 高级（默认指向公网站点）

| 变量 | 默认 |
|---|---|
| `V2EX_API_BASE_URL` | `https://www.v2ex.com/api/v2` |
| `V2EX_WEB_BASE_URL` | `https://www.v2ex.com` |
| `LIBRA_BASE_URL` | `https://2libra.com` |

### 代理说明

`v2ex.com` 在部分网络不可达（DNS 污染），需要代理；`open.feishu.cn` 必须直连。
代理只读标准变量 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`，只作用于 V2EX 与签到请求。

容器内的 `127.0.0.1` 是容器自己，所以本地 Clash 要写宿主机地址：

```ini
HTTPS_PROXY=http://host.docker.internal:7897   # Podman 用 host.containers.internal
NO_PROXY=localhost,127.0.0.1
```

云端服务器直连 v2ex.com，**不要**设这一项。宿主机直接跑二进制时
`scripts/local-env.sh` 会自动把 `host.docker.internal` 改写成 `127.0.0.1`。

## 命令

```
--checkin          立即签到一次并推送
--checkin --dry-run  只打印签到结果，不推送、不记录
--once             单次提醒轮询
--once --dry-run   只打印「将会推送什么」，不发不写
--notify-startup   只发一张启动卡片（验证 webhook）
--version          打印版本
```

不带参数则常驻：提醒轮询 + 定时签到。

```bash
# 本地构建并直接跑
go build -o bin/forum-keeper ./cmd/forum-keeper
. ./scripts/local-env.sh
./bin/forum-keeper --checkin --dry-run
```

装了 make 可以用 `make build` / `test` / `dry-run` / `checkin` / `checkin-dry-run` / `run`。

## 卡片长这样

签到（两个站点合并成一张，带余额）：

```
⚠️ 每日签到 · 1/2 成功
✅ V2EX
    └ 今日已签过，余额 29 银币 69 铜币
❌ 2libra
    └ Cookie 已失效或无权限（HTTP 401）
签到时间 2026-10-02 06:00:05
```

提醒（多条自动合并，超过 10 条只显示前 10 条）：

```
💬 V2EX · 回复了你
@someone 回复了你
📄 Fedora 上最好用的截图工具
> 按的时候屏幕是不是会闪一下？
                          [ 查看 ]
来自 V2EX · 1 条
```

## 签到行为

- **幂等**：站点返回「今天已经签到过了」按成功处理，重复执行不会出错。
- **不漏签**：`CHECKIN_ON_START=true`（默认）时，启动发现今天还没签就立即补签
  （例如 06:00 容器正好在重启）；已签过则跳过。
- **不重复**：每天只签一次、只发一张卡片，调度到点时也会先查状态库。
- **互不影响**：一个站点失败不挡另一个，卡片逐条列明。
- **自动重试**：网络错误 / 429 / 5xx 重试 3 次（2s、4s）。
- **带余额**：V2EX 签到后额外读 `/balance`；2libra 由签到接口直接返回。

## 常见问题

| 现象 | 原因 / 处理 |
|---|---|
| `V2EX rejected the token` | `V2EX_TOKEN` 失效，去设置页换新 |
| 飞书 `code=19021 sign match fail` | 开了签名校验但 `FEISHU_SECRET` 没填或填错 |
| `dial tcp ... timeout` | V2EX 不可达，检查 `HTTPS_PROXY`（容器里别写 `127.0.0.1`） |
| 签到卡片 `Cookie 已失效` | `V2EX_COOKIE` / `LIBRA_COOKIE` 过期，重新获取后 `docker compose up -d` |
| 签到卡片 `未找到签到 token` | V2EX 未登录（Cookie 不全，2FA 缺 `A2O`），或页面结构变了 |
| 一直没有提醒推送 | 正常：首次启动 `V2EX_FIRST_RUN=skip` 只记录不推送；无新消息时 info 不打印日志。用 `LOG_LEVEL=debug` 确认 |
| 签到时间不对 | 容器 `TZ` 不对，`CHECKIN_TIME` 按它解释 |
| 不想用签到 | 清空 `V2EX_COOKIE` 和 `LIBRA_COOKIE`（凭据存在即启用） |

## 不使用容器

```bash
go build -o bin/forum-keeper ./cmd/forum-keeper
. ./scripts/local-env.sh        # 载入 .env 并适配宿主机代理地址
./bin/forum-keeper              # 常驻
```

## 部署到云端

`deploy/` 是自包含的云端包（钉版本拉镜像、无代理、持久卷、`TZ`）：

```bash
scp -r deploy/ server:/opt/forum-keeper/
ssh server 'cd /opt/forum-keeper && cp env.example .env && chmod 600 .env && $EDITOR .env'
ssh server 'cd /opt/forum-keeper && docker compose pull && docker compose up -d'
```

细节与排查见 `deploy/README.md`。云端**不要**用仓库根目录的 `compose.yaml`（那是给本地开发用的）。

镜像发布在 GHCR，push `v*` tag 触发 CI：

```bash
git tag -a v0.2.0 -m "v0.2.0" && git push origin v0.2.0
# -> ghcr.io/sunyin0818/forum-keeper:0.2.0 / :0.2 / :latest（amd64 + arm64）
```

包默认 private，要跨机器拉取就在 GitHub → Packages 里改为 public，或 `docker login ghcr.io`。

## 开发

```bash
./scripts/init-env.sh   # 生成 .env（0600，已存在不覆盖）
./scripts/dry-run.sh    # 只读：列出待记录/待推送的提醒
./scripts/smoke.sh      # 离线端到端：mock V2EX + mock 飞书

gofmt -l . && go vet ./... && go test ./...   # 与 CI 的 test job 一致
```

目录结构：

```
cmd/forum-keeper/   入口与命令行参数
internal/config/    环境变量解析与校验
internal/v2ex/      V2EX API 2.0 客户端
internal/checkin/   签到（V2EX / 2libra）+ 调度
internal/feishu/    飞书 webhook 与卡片渲染
internal/store/     bbolt：已推送提醒 id、上次签到时间
internal/app/       组装与主循环
deploy/             云端部署包
```
