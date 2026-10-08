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

本地直接跑 Go（容器只用于云端，见 [部署到云端](#部署到云端)）。

```bash
cp -n .env.example .env && chmod 600 .env   # -n：已有的 .env 不会被覆盖
$EDITOR .env            # 填 FEISHU_WEBHOOK + 站点凭据

go build -o bin/forum-keeper ./cmd/forum-keeper
. ./scripts/local-env.sh        # 读 .env（cookie 里有分号，不能直接 source）
./bin/forum-keeper              # 常驻；开发时 go run ./cmd/forum-keeper
```

最小配置（只签到、不提醒）：

```ini
FEISHU_WEBHOOK=https://open.feishu.cn/open-apis/bot/v2/hook/...
V2EX_COOKIE=A2=...
LIBRA_COOKIE=access_token=...
```

## 配置

命名规则：同一模块共用前缀，按「必填 → 凭据 → 默认即可 → 高级」排列。

下面的表格是速查。日常 `.env` 里只需要 `.env.example` 列的那 4 个键，其余不写即默认；
想直接拿一份写全了默认值的配置，或拷某一行出来改，用 [`.env.reference`](.env.reference)。

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

### 高级（默认指向公网站点）

| 变量 | 默认 |
|---|---|
| `V2EX_API_BASE_URL` | `https://www.v2ex.com/api/v2` |
| `V2EX_WEB_BASE_URL` | `https://www.v2ex.com` |
| `LIBRA_BASE_URL` | `https://2libra.com` |

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

## 卡片长这样

签到（两个站点合并成一张，带余额）：

```
⚠️ 每日签到 · 1/2 成功
✅ V2EX
    └ 今日已签过（获得 8 铜币），余额 29 银币 77 铜币
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
- **卡片不丢**：飞书对 webhook 限流（`code=11232 frequency limited`）时按限流退避重试
  （客户端 10s、20s、30s），投递失败再隔 1、2 分钟重发卡片；签到只做一次，不会因重发
  卡片而重复签到。卡片最终仍未发出就不记为「今天已签」，重启会补发。
- **带余额**：V2EX 签到后额外读 `/balance`，一次请求同时拿到当前余额和当天
  「每日登录奖励 N 铜币」流水（签到页 flash 抓不到金额时的兜底，已签过也能显示
  今天领了多少）；2libra 由签到接口直接返回，已签过（接口返回 `coins=0`）时再读
  `/api/coins/today-transaction`（即 <https://2libra.com/coins> 的数据源）补上当天金币。

## 常见问题

| 现象 | 原因 / 处理 |
|---|---|
| `V2EX rejected the token` | `V2EX_TOKEN` 失效，去设置页换新 |
| 飞书 `code=19021 sign match fail` | 开了签名校验但 `FEISHU_SECRET` 没填或填错 |
| `dial tcp ... timeout` | V2EX 不可达。云端直连不该出现；本地看 shell 里的 `HTTPS_PROXY` 是否指向可用的代理 |
| 签到卡片 `Cookie 已失效` | `V2EX_COOKIE` / `LIBRA_COOKIE` 过期。本地改完 `.env` 重启进程；云端 `docker compose up -d` |
| 云端每次 `up` 刷一串 `WARN The "o16" variable is not set` | cookie 值里含 `$`（`_ga_*`、`FCNEC` 这类分析 cookie），被 Docker Compose 当成变量插值吃掉了。**功能不受影响** —— 认证字段 `A2`/`A2O`/`access_token` 不含 `$`。把 cookie 精简成只留 `A2=...; A2O=...` 即可消除 |
| 云端 `pull access denied` | 服务器在拉一个不存在的镜像；确认 `compose.yaml` 的 `image:` 是 `ghcr.io/sunyin0818/forum-keeper:<版本>` |
| 签到卡片 `未找到签到 token` | V2EX 未登录（Cookie 不全，2FA 缺 `A2O`），或页面结构变了 |
| 签到成功但群里没卡片，日志 `code=11232 frequency limited` | 飞书对 webhook 限流。重启会补发；长期出现可把 `CHECKIN_TIME` 错开群内其他机器人的推送时间 |
| 一直没有提醒推送 | 正常：首次启动 `V2EX_FIRST_RUN=skip` 只记录不推送；无新消息时 info 不打印日志。用 `LOG_LEVEL=debug` 确认 |
| 签到时间不对 | 容器 `TZ` 不对，`CHECKIN_TIME` 按它解释 |
| 不想用签到 | 清空 `V2EX_COOKIE` 和 `LIBRA_COOKIE`（凭据存在即启用） |

## 部署到云端

`compose.yaml` 是唯一的部署产物定义，`image:` 钉着发布版本。

```bash
# 在仓库根目录执行。云端只要两个文件
ssh server 'mkdir -p /opt/forum-keeper'
scp compose.yaml .env.example server:/opt/forum-keeper/

ssh server 'cd /opt/forum-keeper && cp -n .env.example .env && chmod 600 .env && $EDITOR .env'
ssh server 'cd /opt/forum-keeper && docker compose pull && docker compose up -d'
```

`.env` 里填 `FEISHU_WEBHOOK` 和要启用的凭据（`V2EX_TOKEN` 留空就只签到）。

服务器直连 v2ex.com，不需要代理。受限网络的服务器要过代理，就在 `compose.yaml` 的
`environment:` 里加一行 `HTTPS_PROXY: http://<该服务器的代理>`（只作用于 V2EX 与签到请求，
飞书始终直连）。本地运行需要的代理由本机环境自行提供。

首次启动应该看到（群里会先收到一张 🟢 启动卡片，**不会刷屏** —— `V2EX_FIRST_RUN=skip`
把当前未读全部记为已见但不推送）：

```
INFO authenticated with V2EX              username=...
INFO forum-keeper starting               interval=1m0s first_run=skip state=/data/state.db
INFO startup message sent
INFO first run: recording existing notifications without pushing  count=N
```

升级 = 改 `compose.yaml` 里 `image:` 这一行，重新拷过去：

```bash
scp compose.yaml server:/opt/forum-keeper/
ssh server 'cd /opt/forum-keeper && docker compose pull && docker compose up -d'
```

状态卷 `data`（实际卷名 `forum-keeper_data`）不受影响，不会重推历史提醒。
国内服务器拉 GHCR 慢的话：配 Docker Hub 镜像（仓库 Secrets 加 `DOCKERHUB_USERNAME` /
`DOCKERHUB_TOKEN`，workflow 会自动多推一份），或 `docker save` / `docker load` 搬运。

镜像发布在 GHCR，push `v*` tag 会依次：跑测试 → 构建并推送镜像 → **创建 GitHub Release**（自动生成 release notes）：

```bash
git tag -a v0.2.0 -m "v0.2.0" && git push origin v0.2.0
# -> ghcr.io/sunyin0818/forum-keeper:0.2.0 / :0.2 / :latest（amd64 + arm64）
# -> https://github.com/Sunyin0818/forum-keeper/releases/tag/v0.2.0
```

包默认 private，要跨机器拉取就在 GitHub → Packages 里改为 public，或 `docker login ghcr.io`。

> **改名后的旧包残留**：仓库改名不会连带改 GHCR 的包名。旧的
> `ghcr.io/sunyin0818/v2ex-notifier` 若还在，在网页上删：
> <https://github.com/users/Sunyin0818/packages/container/v2ex-notifier/settings>

## 开发

```bash
cp -n .env.example .env && chmod 600 .env   # 仅首次
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
compose.yaml     云端运行定义（本地不跑容器）：跑哪个镜像、怎么跑
Dockerfile       CI 构建镜像的唯一入口
scripts/         dry-run / smoke / local-env 等宿主机辅助脚本
```
