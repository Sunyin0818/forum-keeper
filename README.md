# forum-keeper

常驻容器，提供三项功能：

- **每日签到**：V2EX、2libra、PT 站点。
- **V2EX 提醒**：轮询新提醒并推送。
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
chmod 600 config.yaml
```

### 3. 编辑配置

```bash
$EDITOR config.yaml
```

最小配置（只签到、不提醒）：

```yaml
feishu:
  webhook: 'https://open.feishu.cn/open-apis/bot/v2/hook/...'
v2ex:
  cookie: 'A2=...; A2O=...; PB3_SESSION=...'
libra:
  cookie: 'access_token=...'
```

参数说明见 [配置](#配置)。

### 4. 启动

```bash
docker compose up -d
```

启动后飞书群收到一张「已启动」卡片。

## 配置

配置文件为 `config.yaml`，容器内只读挂载到 `/config/config.yaml`。完整字段说明见 [`config.example.yaml`](config.example.yaml)。

### 凭据

留空即跳过对应功能。

| 字段 | 说明 |
|---|---|
| `feishu.webhook` | 群自定义机器人 webhook，必填 |
| `feishu.secret` | 机器人开启签名校验时填写 |
| `v2ex.cookie` | V2EX 签到。浏览器 Cookie 中的 `A2`、`A2O`（2FA）、`PB3_SESSION` |
| `v2ex.token` | V2EX 提醒（API 2.0）。留空则关闭提醒 |
| `libra.cookie` | 2libra 签到。填 `access_token=...` 或裸 token |

V2EX 提醒使用 API Token，签到使用网页 Cookie，两者独立配置。

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
| `reminders.max_pages` | `3` | 每轮最多翻几页（每页 20 条） |
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
| `server.state_path` | `state.db` | 状态库路径，容器内为 `/data/state.db` |
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
| `--once [--dry-run]` | 单次提醒轮询；`--dry-run` 只打印内容 |
| `--notify-startup` | 发送一张启动卡片，验证 webhook |
| `--version` | 打印版本 |

```bash
docker compose run --rm forum-keeper --check-config
docker compose run --rm forum-keeper --checkin --dry-run
```

不带参数时常驻运行。一次性命令和常驻进程共用状态库，同时执行前先 `docker compose stop`。

## 从旧版配置迁移

旧版用 `config/.env` + `config/pt-sites.json` 两个文件。仓库里带了一个转换脚本：

```bash
python3 scripts/migrate-env-to-yaml.py config/.env config/pt-sites.json config.yaml
chmod 600 config.yaml
docker compose up -d
```

## 升级

```bash
$EDITOR compose.yaml          # 修改 image 标签
docker compose pull && docker compose up -d
```

数据保存在 `data` 卷。

## 常见问题

| 现象 | 处理 |
|---|---|
| 签到卡片提示 `Cookie 已失效` | 更新 `config.yaml` 中的凭据，执行 `docker compose up -d` |
| 首次启动没有推送历史提醒 | `reminders.first_run`、`pt.first_run` 默认为 `skip`；改为 `push` 后重启 |
| 签到成功但群里没有卡片 | 检查飞书 webhook、群机器人状态和容器日志 |
| V2EX 卡片提示 `签到未生效` | 更新 `v2ex.cookie`，确认包含 `PB3_SESSION` |
| PT 卡片提示 `未匹配到签到结果` | 在站点的 `signin` 中配置 `path`、`submit_text`、`success` 或 `already` |

## 开发

目录结构、本地构建、测试和工程约束见 [AGENTS.md](AGENTS.md)。
