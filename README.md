# forum-keeper

论坛小助手：一个常驻容器做三件事。

1. **提醒** —— 轮询 V2EX 提醒，有新消息就推一张飞书交互卡片。
2. **签到** —— 每天定时给 **V2EX**、**2libra** 和配置的 **PT 站点**签到，结果汇总成飞书卡片（带余额/奖励）。
3. **PT 站内信** —— 轮询 PT 站未读消息，新消息推飞书；本地去重，首次运行默认不刷屏。

不用浏览器、不用 GitHub Actions、不监听任何端口。PT 功能默认关闭，填 `PT_SITES_FILE` 才启用。

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
| `PT_SITES_FILE` | **PT 签到 + 站内信** | 指向一个 `pt-sites.json`，格式见 [PT 站点](#pt-站点可选)；不填则关闭 PT 功能 |
| `V2EX_COOKIE` | **V2EX 签到**用（网页 Cookie） | 见下方说明 |
| `LIBRA_COOKIE` | **2libra 签到**用 | 见下方说明 |

**V2EX_COOKIE**：登录 <https://www.v2ex.com/> 后按 `F12` → **Network** → 刷新 → 点任一
`www.v2ex.com` 请求 → **Request Headers** 里的 `Cookie:` 整行，原样复制（`_ga`/`_gid`
这类跟踪项可删，值里含 `$` 会让 compose 刷警告）。

最小集合是 `A2`、`A2O`（2FA 账号）和 **`PB3_SESSION`**。`A2` 单独就能浏览，但
「领取每日登录奖励」还需要 `PB3_SESSION`（2026-10 观察：只带 `A2`/`A2O` 时
`redeem` 返回 302 却不入账）：

```
V2EX_COOKIE=A2=...; A2O=...; PB3_SESSION=...
```

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

### PT 站点（可选）

PT 站点用一份 JSON 描述，不把 cookie 写进代码，也不依赖 MoviePilot / qd 运行时：

```bash
cp config/pt-sites.example.json config/pt-sites.json
chmod 600 config/pt-sites.json
export BTSCHOOL_COOKIE='your-cookie-header'
```

**最小配置**只有两个字段（其余全部有默认值）：

```json
{
  "sites": [
    {"url": "https://pt.btschool.club", "cookie_env": "BTSCHOOL_COOKIE"}
  ]
}
```

默认行为：

- `name` 取域名；
- `signin.mode=checkin`，先访问 `/attendance.php`，找不到入口会从首页发现；
- `messages.engine=nexusphp`，`messages.mode=full`；
- `timeout=20s`，UA 用内置浏览器标识。

只有站点行为不同才需要加字段：

| 字段 | 什么时候要写 |
|---|---|
| `name` | 想在卡片/日志里显示自定义名称 |
| `preset` | 强制指定内置预设，或写 `none` 关闭按域名自动匹配 |
| `cookie` | 本地临时测试，直接写 Cookie（不建议长期使用） |
| `ua` | Cookie 与 UA 绑定，或站点要求特定浏览器 |
| `timeout` | 站点慢，默认 20s 不够 |
| `signin.mode` | `login`（只保活，不签到）或 `off`（只读消息） |
| `signin.path` / `method` / `submit_text` | 不是 `/attendance.php`，或按钮文案特殊 |
| `signin.success` / `already` / `absent_keywords` | 站点成功/已签到文案特殊 |
| `signin.two_step` | 需要先抓 token 再提交 |
| `messages.engine` | 设为 `off` 可关闭该站站内信 |
| `messages.mode` | `list` = 只读标题/时间，保留未读；`full` = 读正文（会标已读） |
| `messages.selectors` | 站点模板特殊，需要覆盖默认选择器 |

说明：

- `cookie_env` 引用环境变量，避免 cookie 落盘；JSON 本身也要 `chmod 600`。
- `signin.mode`：`checkin` 正常签到；`login` 只访问首页保活（适合没有独立签到页的站，是否计入活跃取决于站点）；`off` 不签到。
- `messages.engine`：`nexusphp` 或 `off`；`mode=full` 读正文（NexusPHP 会在站点侧标已读），`mode=list` 只读标题/时间，保留未读状态。
- 站点按 NexusPHP 通用流程处理：`/attendance.php`、首页「每日签到」表单、常见签到路径自动发现、二段式 token 表单。
- **不内置浏览器**：Cloudflare / Turnstile / 图片验证码会直接报失败，不会假装签到成功。把浏览器里的 `cf_clearance` 和对应 UA 一起填进 cookie/ua 通常能过；过不了就等它放行或手工签到。
- 读正文会打开 `messages.php?action=viewmessage&id=...`，NexusPHP 会因此把消息标为已读；不想改变未读状态就用 `mode=list`。

### 内置预设

按域名自动套用，不用手写路径/文案：

| 预设 | 域名 | 关键行为 |
|---|---|---|
| `btschool` | `btschool.club`、`pt.btschool.club` | 首页「每日签到」，GET `/index.php?action=addbonus` |
| `carpt` | `carpt.net` | NexusPHP 通用 `GET/POST /attendance.php` |
| `ptzone` | `ptzone.xyz` | NexusPHP 通用 `GET/POST /attendance.php` |
| `cspt` | `cspt.top` | NexusPHP 通用 `GET/POST /attendance.php` |

- 显式写 `"preset": "btschool"` 可强制使用；写 `"preset": "none"` 关闭自动匹配。
- 预设只覆盖“路径/方法/文案/token”等规则，用户显式写的字段优先。
- `carpt` / `ptzone` / `cspt` 是标准 NexusPHP 的通用规则；如果站点后续改了签到路径，用 `signin.path` 覆盖即可。
- 需要浏览器/验证码的站点（Cloudflare、Turnstile、图片验证码、随机答题）没有收录；这类站点仍会明确报失败。
- 站点维护页会明确报「站点维护中，稍后重试」，不会当成普通签到失败。CSPT 当前处于维护状态，恢复后再跑即可。

配置写错或想确认默认值是否生效，先跑：

```bash
./bin/forum-keeper --check-config
```

它只读配置、不签到、不发消息，会打印每个站点的解析结果（不会打印 cookie）。

然后在 `config/.env` 里写：

```ini
PT_SITES_FILE=./config/pt-sites.json
```

### 3. 配置并运行

本地直接跑 Go（容器只用于云端，见 [部署到云端](#部署到云端)）。

```bash
cp -n config/.env.example config/.env && chmod 600 config/.env   # -n：已有的配置不会被覆盖
$EDITOR config/.env     # 填 FEISHU_WEBHOOK + 站点凭据

go build -o bin/forum-keeper ./cmd/forum-keeper
. ./scripts/local-env.sh        # 读 config/.env（cookie 里有分号，不能直接 source）
./bin/forum-keeper              # 常驻；开发时 go run ./cmd/forum-keeper
```

最小配置（只签到、不提醒）：

```ini
FEISHU_WEBHOOK=https://open.feishu.cn/open-apis/bot/v2/hook/...
V2EX_COOKIE=A2=...; A2O=...; PB3_SESSION=...
LIBRA_COOKIE=access_token=...
```

## 配置

命名规则：同一模块共用前缀，按「必填 → 凭据 → 默认即可 → 高级」排列。

下面的表格是速查。日常 `config/.env` 里只需要 `config/.env.example` 列的基本键；用 PT 时再加 `PT_SITES_FILE`。其余不写即默认；
想直接拿一份写全了默认值的配置，或拷某一行出来改，用 [`config/.env.reference`](config/.env.reference)。

### 必填

| 变量 | 默认 | 说明 |
|---|---|---|
| `FEISHU_WEBHOOK` | — | **必填**，飞书机器人 webhook |
| `FEISHU_SECRET` | 空 | 机器人开了签名校验时必填 |

### 凭据

填了才启用对应功能，不填就跳过该功能。

| 变量 | 默认 | 说明 |
|---|---|---|
| `V2EX_COOKIE` | 空 | V2EX 签到用（`A2=...`，2FA 加 `A2O`，再加 `PB3_SESSION`） |
| `LIBRA_COOKIE` | 空 | 2libra 签到用（`access_token=...` 或裸 token） |
| `V2EX_TOKEN` | 空 | V2EX 提醒用；留空则只签到 |
| `PT_SITES_FILE` | 空 | PT 站点 JSON；填了才启用 PT 签到和站内信 |

### 默认即可（按需修改）

| 变量 | 默认 | 说明 |
|---|---|---|
| `CHECKIN_TIME` | `08:10` | 每日签到时间（`HH:MM`，按 `TZ`）。**别早于 `08:00`**——V2EX 的"任务日"从 08:00（UTC+8）才开始，更早去签只会看到前一天已领取 |
| `CHECKIN_ON_START` | `true` | 启动补签：今天还没签就补；同一天重启跳过 |
| `TZ` | `Asia/Shanghai` | 调度与所有时间戳的时区 |
| `V2EX_POLL_INTERVAL` | `60s` | 提醒轮询间隔，最小 `5s` |
| `V2EX_FIRST_RUN` | `skip` | 首次只记录不推送（防刷屏）；`push` 全推 |
| `V2EX_MARK_READ` | `false` | 推送成功后删除该条 V2EX 提醒 |
| `V2EX_FILTER_TYPES` | 空 | 只推 `reply,mention,thanks,other` |
| `V2EX_MAX_PAGES` | `3` | 每轮最多翻几页（每页 20 条） |
| `PT_POLL_INTERVAL` | `2h` | PT 站内信轮询间隔，最小 `1m` |
| `PT_FIRST_RUN` | `skip` | PT 首次只记录已有未读不推送；`push` 全推 |
| `PT_CHECKIN_RETRIES` | `2` | PT 单站失败重试次数（10m / 30m / 2h 退避） |
| `PT_CONCURRENCY` | `4` | 同时签到的 PT 站点数（1-16） |
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

### 配置目录与状态目录

配置和运行状态分开：

- `config/`：`.env`（webhook/cookie）、`pt-sites.json`（站点列表）；容器里只读挂载到 `/config`；
- `/data`：bbolt 状态库，运行时会写；丢失只会导致重推/重试，不影响凭据。

本仓的 `config/` 里同时放了可提交的模板和参考：

```
config/.env.example
config/.env.reference
config/pt-sites.example.json
config/.env              # 实际 secret，600，gitignored
config/pt-sites.json     # 实际站点列表，gitignored
```

更完整的权限、轮换和备份建议见 [`config/README.md`](config/README.md)。

## 命令

```
--check-config     校验配置并打印解析后的站点（不签到、不发消息）
--checkin          立即签到一次并推送（含 PT）
--checkin --dry-run  只打印签到结果，不推送、不记录
--pt-messages      立即轮询一次 PT 站内信并推送
--once             单次提醒轮询
--once --dry-run   只打印「将会推送什么」，不发不写
--notify-startup   只发一张启动卡片（验证 webhook）
--version          打印版本
```

不带参数则常驻：提醒轮询 + 定时签到 + PT 站内信轮询。

```bash
# 本地构建并直接跑
go build -o bin/forum-keeper ./cmd/forum-keeper
. ./scripts/local-env.sh
./bin/forum-keeper --checkin --dry-run
```

## 卡片长这样

签到分成两张卡：V2EX / 2libra 一张，PT 站点单独一张，互不混排。

论坛签到：

```
⚠️ 论坛签到 · 1/2 成功
1 成功 · 1 失败 · 共 2 个站点

站点      结果
✅ V2EX    今日已签过（获得 8 铜币）
❌ 2libra  Cookie 已失效或无权限（HTTP 401）
签到时间 2026-10-02 06:00:05
```

PT 签到：

```
✅ PT 签到 · 4 个站点全部成功
4 成功 · 0 失败 · 共 4 个站点

站点       结果
✅ 学校     今日已签到
✅ CarPT    签到成功（获得 10 魔力值）
✅ PTZone   签到成功（获得 10 魔力值）
✅ 财神     今日已签到
签到时间 2026-10-02 06:00:05
```

PT 站内信（多站点合并，正文为纯文本，最多 10 条）：

```
📬 站点消息 · 1 条
HDSky · 账号安全提醒
2026-10-23 09:00:00
请在 7 天内修改密码
                          [ 查看消息 ]
共 1 条 · 2026-10-23 09:00:05
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
  （例如容器正好在重启）；已签过则跳过。
- **V2EX 日界是 08:00（UTC+8）**：V2EX 的"任务日"不是自然日，08:00 之前的签到页
  还挂着前一天，已领取的样子和"今天签过"一字不差。所以默认签到时间放在 `08:10`；
  万一在 08:00 前跑（重启补签），V2EX 会报 `pending` 而不是"已签过"——不落盘、
  不发卡片，等到 08:01 自动重试，不会把当天漏掉。
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
- **PT 分站重试**：PT 站点各自记录当天状态，失败按 10m / 30m / 2h 重试，最多
  `PT_CHECKIN_RETRIES` 次；一个站点失败不会挡住其他站点，也不会把当天误记为完成。
- **PT 站内信去重**：按 `站点 + 消息 id` 记录已推送；没有稳定 id 时用内容摘要兜底。
  首次运行 `PT_FIRST_RUN=skip` 只记录不推送。飞书发送失败会保留 pending，下轮只重发
  卡片，不重复打开消息详情。
- **不误报**：V2EX 的 `redeem` 接口有时返回 302 却一分不入账（2026-10 观察）。
  所以只有拿到证据——签到页变成「已领取」或 `/balance` 出现**当天日期**的奖励流水
  ——才算成功，否则卡片如实报失败并说明原因；流水也只认当天日期，不会拿昨天的
  奖励冒充今天。

## 常见问题

| 现象 | 原因 / 处理 |
|---|---|
| `V2EX rejected the token` | `V2EX_TOKEN` 失效，去设置页换新 |
| 飞书 `code=19021 sign match fail` | 开了签名校验但 `FEISHU_SECRET` 没填或填错 |
| `dial tcp ... timeout` | V2EX 不可达。云端直连不该出现；本地看 shell 里的 `HTTPS_PROXY` 是否指向可用的代理 |
| 签到卡片 `Cookie 已失效` | `V2EX_COOKIE` / `LIBRA_COOKIE` 过期。本地改完 `.env` 重启进程；云端 `docker compose up -d` |
| 云端每次 `up` 刷一串 `WARN The "o16" variable is not set` | cookie 值里含 `$`（`_ga_*`、`FCNEC` 这类分析 cookie），被 Docker Compose 当成变量插值吃掉了。**功能不受影响** —— 认证字段 `A2`/`A2O`/`PB3_SESSION`/`access_token` 不含 `$`。把 cookie 精简成只留 `A2=...; A2O=...; PB3_SESSION=...` 即可消除 |
| 云端 `pull access denied` | 服务器在拉一个不存在的镜像；确认 `compose.yaml` 的 `image:` 是 `ghcr.io/sunyin0818/forum-keeper:<版本>` |
| 签到卡片 `未找到签到 token` | V2EX 未登录（Cookie 不全，2FA 缺 `A2O`），或页面结构变了 |
| 签到卡片 `签到未生效` | V2EX 的 `redeem` 返回 302 但没入账。最常见原因是 `V2EX_COOKIE` 缺 `PB3_SESSION`（只带 `A2`/`A2O`）；补齐后重试。 |
| 签到成功但群里没卡片，日志 `code=11232 frequency limited` | 飞书对 webhook 限流。重启会补发；长期出现可把 `CHECKIN_TIME` 错开群内其他机器人的推送时间 |
| 一直没有提醒推送 | 正常：首次启动 `V2EX_FIRST_RUN=skip` 只记录不推送；无新消息时 info 不打印日志。用 `LOG_LEVEL=debug` 确认 |
| 签到时间不对 | 容器 `TZ` 不对，`CHECKIN_TIME` 按它解释 |
| 不想用签到 | 清空 `V2EX_COOKIE` 和 `LIBRA_COOKIE`（凭据存在即启用） |
| PT 签到卡片 `Cookie 已失效` | PT 站 cookie 过期；重新复制 Cookie，UA 要与浏览器一致 |
| PT 卡片 `Cloudflare/WAF` | 站点要求浏览器验证。带 `cf_clearance` 的 cookie 可尝试；本仓不内置浏览器 |
| PT 卡片 `未匹配到签到结果` | 该站签到入口/文案不同，在 `signin` 里指定 `path`、`success`、`already` 或 `submit_text` |
| PT 消息第一次没有推送 | 正常：`PT_FIRST_RUN=skip` 只记录已有未读；新消息才会推 |
| 不想让 PT 消息变成已读 | 把该站 `messages.mode` 改成 `list`（只读列表，不打开详情） |

## 部署到云端

`compose.yaml` 是唯一的部署产物定义，`image:` 钉着发布版本。

```bash
# 在仓库根目录执行。云端只要 compose.yaml + config/ 目录
ssh server 'mkdir -p /opt/forum-keeper/config'
scp compose.yaml server:/opt/forum-keeper/
scp -r config/.env.example config/.env.reference config/pt-sites.example.json \
  server:/opt/forum-keeper/config/

ssh server 'cd /opt/forum-keeper && cp -n config/.env.example config/.env && chmod 600 config/.env && $EDITOR config/.env'
ssh server 'cd /opt/forum-keeper && docker compose pull && docker compose up -d'
```

`config/.env` 里填 `FEISHU_WEBHOOK` 和要启用的凭据（`V2EX_TOKEN` 留空就只签到）。

用 PT 站点时额外：

```bash
scp config/pt-sites.example.json server:/opt/forum-keeper/config/pt-sites.json
# 容器以 UID 10001 运行，配置目录要让它能读
ssh server 'chown 10001:10001 /opt/forum-keeper/config/pt-sites.json && chmod 600 /opt/forum-keeper/config/pt-sites.json'
# config/.env 由运行 docker compose 的宿主机用户读取，不要 chown 给 10001
ssh server 'chmod 600 /opt/forum-keeper/config/.env'
```

`compose.yaml` 已经把 `./config` 只读挂载到 `/config`，并设置 `PT_SITES_FILE=/config/pt-sites.json`；状态库仍在 `/data`。

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
cp -n config/.env.example config/.env && chmod 600 config/.env   # 仅首次
./scripts/dry-run.sh    # 只读：列出待记录/待推送的提醒
./scripts/smoke.sh      # 离线端到端：mock V2EX + mock 飞书

gofmt -l . && go vet ./... && go test ./...   # 与 CI 的 test job 一致
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...   # 依赖/标准库漏洞扫描
```

目录结构：

```
cmd/forum-keeper/   入口与命令行参数
internal/config/    环境变量解析与校验
internal/v2ex/      V2EX API 2.0 客户端
internal/checkin/   签到（V2EX / 2libra）+ 调度
internal/ptsite/    PT 通用签到 / NexusPHP 站内信 / 去重与重试
internal/feishu/    飞书 webhook 与卡片渲染
internal/store/     bbolt：已推送提醒 id、PT 日状态、PT 消息去重/待发送
internal/app/       组装与主循环
compose.yaml     云端运行定义（本地不跑容器）：跑哪个镜像、怎么跑
Dockerfile       CI 构建镜像的唯一入口
config/               环境变量、PT 站点配置（示例与参考；实际 secret 文件已 gitignore）
  .env.example        最小配置模板
  .env.reference      全量配置参考
  pt-sites.example.json  PT 站点配置示例
config/.env、config/pt-sites.json  本地实际配置（不提交）
scripts/         dry-run / smoke / local-env 等宿主机辅助脚本
```
