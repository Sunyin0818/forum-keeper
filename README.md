# forum-keeper

常驻容器，提供三项功能：

- **每日签到**：V2EX、2libra、PT 站点。
- **V2EX 提醒**：轮询新提醒并推送。
- **2libra 提醒**：用官方 OAuth API 轮询新通知并推送。
- **PT 站内信**：轮询未读消息并推送。

结果通过飞书群自定义机器人发送。

## 推送示例

签到卡片：

```
✅ 论坛签到 · 2 个站点全部成功
2 成功 · 0 失败 · 共 2 个站点
站点      结果
✅ V2EX    今日已签过（获得 8 铜币）
✅ 2libra  签到成功（获得 110 金币）
签到时间 2026-10-02 06:00:05
```

站点消息卡片：

```
📬 站点消息 · 1 条
HDSky · 账号安全提醒
请在 7 天内修改密码
                          [ 查看消息 ]
```

## 部署

环境要求：Docker、Compose v2。镜像：`ghcr.io/sunyin0818/forum-keeper`。

### 1. 创建飞书机器人

群设置 → 群机器人 → 添加机器人 → 自定义机器人。记录 webhook；机器人启用签名校验时，同时记录 secret。

### 2. 准备部署目录

```bash
mkdir -p forum-keeper
cp compose.yaml config.example.yaml forum-keeper/
cd forum-keeper
cp config.example.yaml config.yaml
# 容器以 UID/GID 10001 只读挂载配置，属组 10001 必须可读；其他用户仍不可读
sudo chown root:10001 config.yaml
sudo chmod 640 config.yaml
```

`config.yaml` 里有凭据，按 secret 管理：不要提交进 Git，也不要打进镜像。

### 3. 编辑配置

```bash
$EDITOR config.yaml
```

最小配置（只签到、不提醒）：

```yaml
feishu:
  webhook: 'https://open.feishu.cn/open-apis/bot/v2/hook/...'
v2ex:
  cookie: 'A2=...; A2O=...; V2EX_TAB=...'
libra:
  cookie: 'access_token=...'
```

参数说明见 [配置](#配置)。

### 4. 启动

```bash
docker compose up -d
```

启动后飞书群收到一张「已启动」卡片。

### 5. 授权 2libra 提醒（可选）

填好 `libra.client_id` / `client_secret` / `redirect_uri` 后，用**包含 `--libra-auth` 的镜像**运行一次：

```bash
docker compose run --rm -it forum-keeper --libra-auth
```

浏览器打开提示的链接并同意，把地址栏里的整条回调 URL 粘回终端。令牌写入数据卷的 `/data/libra_token.json`（0600），之后 `docker compose up -d` 启动即可。

如果授权是在另一台机器完成的，把生成的 `libra_token.json` 复制进数据卷，属主必须是容器用户：

```bash
docker run --rm -v forum-keeper_data:/data -v "$PWD/libra_token.json:/src:ro" alpine:3.20 \
  sh -c 'cp /src /data/libra_token.json && chown 10001:10001 /data/libra_token.json && chmod 600 /data/libra_token.json'
```

令牌包含刷新令牌，按凭据管理；不要提交、不要打进镜像。授权失效（`401`）或 `read:me` 被收回（`403 insufficient_scope`）时，程序只推一张「已停止」卡片，重新执行 `--libra-auth` 后重启即可。

## 配置

配置文件为 `config.yaml`，容器内只读挂载到 `/config/config.yaml`。逐项注释见 [`config.example.yaml`](config.example.yaml)。

### 凭据

留空即跳过对应功能。

| 字段 | 说明 |
|---|---|
| `feishu.webhook` | 群自定义机器人 webhook，必填 |
| `feishu.secret` | 机器人开启签名校验时填写 |
| `v2ex.cookie` | V2EX 签到。浏览器 Cookie **整串**复制：`A2`、`A2O`（2FA）、`V2EX_TAB` 缺一不可（少了 `V2EX_TAB` 会被判「浏览器设置有奇奇怪怪的地方」）；`PB3_SESSION` 由站点下发，填了也会忽略。程序每天访问一次首页续期 `A2O`/`V2EX_TAB`，续期结果写 `v2ex.cookie_file`，config 只作为首次种子 |
| `v2ex.cookie_file` | 续期后的会话 Cookie 落盘位置，默认与 `state.db` 同目录的 `v2ex_cookie`（容器里是 `/data/v2ex_cookie`），0600 |
| `v2ex.token` | V2EX 提醒（API 2.0）。留空则关闭提醒 |
| `libra.cookie` | 2libra 签到（旧接口）。填 `access_token=...` 或裸 token；被 Cloudflare 拦截时加上 `cf_clearance` |
| `libra.client_id` / `libra.client_secret` / `libra.redirect_uri` | 2libra 提醒的 OAuth 应用凭据，三项必须同时配置。回调地址要与申请时填的完全一致 |
| `libra.token_file` | OAuth 令牌落盘位置，默认与 `state.db` 同目录的 `libra_token.json`（0600） |
| `libra.scopes` | 授权 scope，默认 `profile read:me`；提醒不需要金币权限 |
| `libra.filter_types` | 2libra 推送类型，留空 = 全部；可选 `reply,reply_mention,mention,reaction,system,pandora,level_up,get_reward,favorite,content_check,badge_unlocked,follow,comment_pin` |

V2EX 提醒使用 API Token，签到使用网页 Cookie，两者独立配置。2libra 提醒使用 OAuth 令牌：在 2libra「设置 → OAuth 应用」申请机密客户端，填好三项凭据后运行一次 `--libra-auth`，服务启动时读取令牌并自动刷新（刷新令牌轮换、绝对有效期 90 天，到期需重新授权）。

### 签到

| 字段 | 默认 | 说明 |
|---|---|---|
| `checkin.time` | `08:10` | 每日签到时间（`HH:MM`）。V2EX 任务日从 08:00（UTC+8）开始 |
| `checkin.on_start` | `true` | 启动时补签当天未签到的站点 |
| `checkin.timezone` | `Asia/Shanghai` | 调度与所有时间戳使用的时区 |

### 提醒

| 字段 | 默认 | 说明 |
|---|---|---|
| `reminders.interval` | `60s` | 轮询间隔，最小 `5s` |
| `reminders.max_pages` | `3` | 每轮最多翻几页（V2EX 每页 20 条，2libra 每页 50 条） |
| `reminders.first_run` | `skip` | 首次运行：`skip` 只记录，`push` 全部推送 |
| `reminders.mark_read` | `false` | 推送后删除 V2EX 对应提醒 |
| `reminders.filter_types` | 空 | 推送类型：`reply,mention,thanks,other` |

### 通知

| 字段 | 默认 | 说明 |
|---|---|---|
| `notify.startup` | `true` | 启动时发送启动卡片 |
| `notify.startup_cooldown` | `10m` | 两条启动卡片的最小间隔 |
| `notify.alert_on_error` | `true` | 连续失败达阈值时发送飞书告警 |

### PT 站点

| 字段 | 默认 | 说明 |
|---|---|---|
| `pt.poll_interval` | `2h` | 站内信轮询间隔，最小 `1m` |
| `pt.first_run` | `skip` | 首次运行：`skip` 只记录已有未读，`push` 全部推送 |
| `pt.retries` | `2` | 单站签到失败重试次数（10m / 30m / 2h 退避） |
| `pt.concurrency` | `4` | 同时签到的站点数（1-16） |
| `pt.sites` | 空 | 站点列表，留空即关闭 PT 功能 |

站点按 NexusPHP 通用流程处理：`/attendance.php`、首页「每日签到」表单、常见签到路径自动发现、二段式 token 表单。内置预设按域名匹配：`btschool`、`carpt`、`ptzone`、`cspt`。

Cloudflare/WAF、验证码、随机答题站点返回验证失败；站点维护页返回「站点维护中」。

### 服务

| 字段 | 默认 | 说明 |
|---|---|---|
| `server.http_timeout` | `20s` | 单次 HTTP 请求超时 |
| `server.state_path` | `state.db` | 状态库路径。容器内由 compose 覆盖为 `/data/state.db` |
| `server.log_level` | `info` | `debug`、`info`、`warn`、`error` |

### 目录

| 路径 | 用途 |
|---|---|
| `config.yaml` | 配置与凭据，容器只读挂载到 `/config` |
| `/data` | 状态库（Docker 卷 `forum-keeper_data`） |

## 命令

| 参数 | 作用 |
|---|---|
| `--check-config` | 校验配置并打印解析后的站点 |
| `--checkin [--dry-run]` | 立即签到；`--dry-run` 只打印结果 |
| `--pt-messages` | 立即轮询一次 PT 站内信 |
| `--once [--dry-run]` | 单次提醒轮询（V2EX + 2libra）；`--dry-run` 只打印内容 |
| `--libra-auth` | 一次性完成 2libra OAuth 授权并落盘令牌 |
| `--notify-startup` | 发送一张启动卡片，验证 webhook |
| `--version` | 打印版本 |

```bash
docker compose run --rm forum-keeper --check-config
docker compose run --rm forum-keeper --checkin --dry-run
docker compose run --rm -it forum-keeper --libra-auth   # 需要 -it 才能粘贴回调 URL
```

不带参数时常驻运行。一次性命令和常驻进程共用状态库，同时执行前先 `docker compose stop`。

## 升级

```bash
$EDITOR compose.yaml          # 修改 image 标签
docker compose pull && docker compose up -d
```

数据保存在 `data` 卷。

## 常见问题

| 现象 | 处理 |
|---|---|
| 签到卡片提示 `Cookie 已失效` | 更新 `config.yaml` 中的凭据，执行 `docker compose restart forum-keeper`（`up -d` 不会因挂载的配置文件变化而重启） |
| 签到卡片提示 `两步验证未通过` | 正常运行时程序每天续期 `A2O`，出现这条说明续期已失败（容器停了超过一个月，或 V2EX 主动要求重新验证）。浏览器重新登录并完成两步验证，整串复制 `A2`、`A2O`、`V2EX_TAB` 更新 `v2ex.cookie` 后重启；当天想补签用 `docker compose run --rm forum-keeper --checkin`。删掉 `/data/v2ex_cookie` 可强制回到 config 里的 cookie |
| 日志出现 `人机验证` / `blocked by Cloudflare` | 请求被 Cloudflare 拦在 V2EX 之外，token/cookie 未必失效：程序会静默重试，提醒最多 6 小时一次；换出口 IP 或等风控解除，不要为此轮换 token |
| 日志提示 `2libra reminders disabled` 或没有 2libra 卡片 | 先运行 `docker compose run --rm -it forum-keeper --libra-auth`，按提示在浏览器授权并把地址栏的回调 URL 粘回终端。令牌默认写到 `/data/libra_token.json`。授权失效或 scope 被收回时只会推一张「已停止」卡片，重新授权后重启即可 |
| 2libra 提示 `insufficient_scope` | 用户在「设置 → 授权应用」里收回了 `read:me`。重新运行 `--libra-auth`（重新授权是整体替换，不是累加） |
| 提醒轮询报 `401` / `Token expired` | V2EX API 令牌有有效期（最长 180 天）。令牌被拒绝后程序只推一张「通知已停止」并结束轮询，不会反复重试；去 <https://www.v2ex.com/settings/tokens> 新建一个，更新 `v2ex.token` 后 `docker compose restart forum-keeper` |
| 首次启动没有推送历史提醒 | `reminders.first_run`、`pt.first_run` 默认为 `skip`；改为 `push` 后重启 |
| 签到成功但群里没有卡片 | 检查飞书 webhook、群机器人状态和容器日志 |
| V2EX 卡片提示 `签到未生效` | 看卡片里的 `领取页`：提示「浏览器设置有奇奇怪怪的地方」= cookie 缺 `V2EX_TAB`，把浏览器 Cookie 整串重新复制；标题 `Sign In` = cookie 已过期 |
| PT 卡片提示 `未匹配到签到结果` | 在站点的 `signin` 中配置 `path`、`submit_text`、`success` 或 `already` |

## 开发

目录结构、本地构建、测试和工程约束见 [AGENTS.md](AGENTS.md)。
