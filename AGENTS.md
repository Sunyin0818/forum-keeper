# AGENTS.md

面向在本仓库里改代码的人（和 AI agent）。用户文档、部署、配置见 [README](README.md)。

## 项目结构

```
cmd/forum-keeper/   入口与命令行参数
internal/config/    config.yaml 解析、默认值与环境变量覆盖
internal/v2ex/      V2EX API 2.0 客户端（提醒）
internal/checkin/   签到（V2EX / 2libra）+ 调度
internal/ptsite/    PT 通用签到 / NexusPHP 站内信 / 去重与重试 + 内置预设
internal/feishu/    飞书 webhook 与卡片渲染
internal/store/     bbolt：已推送提醒 id、PT 日状态、PT 消息去重/待发送
internal/app/       组装与主循环
compose.yaml        云端运行定义（钉镜像版本）
Dockerfile          CI 构建镜像的唯一入口
config.example.yaml 配置模板（可提交）
scripts/            smoke / mock 服务器
.github/workflows/  CI：test → image → release
```

## 本地开发

```bash
cp -n config.example.yaml config.yaml && chmod 600 config.yaml   # 仅首次
$EDITOR config.yaml

go build -o bin/forum-keeper ./cmd/forum-keeper
./bin/forum-keeper              # 常驻；开发时也可 go run ./cmd/forum-keeper
```

应用按 `CONFIG_FILE` 找配置；没设置时依次尝试 `config.yaml`、`config/config.yaml`、
`/config/config.yaml`。一次性调试直接用命令行参数：

```bash
CONFIG_FILE=config.yaml go run ./cmd/forum-keeper --once --dry-run
./scripts/smoke.sh      # 离线端到端：mock V2EX + mock 飞书 + mock PT
```

命令行参数（`--once`、`--checkin`、`--pt-messages`、`--check-config`、`--dry-run`、
`--notify-startup`、`--version`）见 README「命令」。

## 提交前验证

与 CI 的 `test` job 保持一致：

```bash
gofmt -l . && go vet ./... && go test ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
./scripts/smoke.sh
```

CI 还会跑 `go test -race`（本地需要 `CGO_ENABLED=1`）。测试不得访问真实站点：
HTTP 一律用 `httptest`，端到端用 `scripts/smoke.sh` + `scripts/mockserver.py`。

## 工程约束

### 运行时形态

- **不内置浏览器**。Cloudflare / Turnstile / 图片验证码直接报失败，绝不假装成功。
- **不监听端口**，没有 HTTP server；常驻进程只做轮询、定时任务和出站请求。
- **不依赖 GitHub Actions 运行时**。Actions 只负责测试、构建镜像和发布。
- PT 功能默认关闭：`pt.sites` 为空即关闭。
- 凭据存在即启用对应功能；不要新增「空值也算启用」的隐式开关。

### 签到与状态

- 签到必须**幂等**：站点返回「今天已签到」按成功处理，重复执行不出错。
- **成功必须有证据**。V2EX 的 `redeem` 可能 302 却不入账，只有签到页变成「已领取」或
  `/balance` 出现**当天日期**的奖励流水才算成功；没证据就如实报失败。
- 卡片投递失败不能让状态前移：签到只做一次，卡片重发不重复签到；最终没发出就不记为
  「今天已签」，重启补发。
- 每天只签一次、只发一张卡；PT 站点各自记录状态，一个站失败不挡其他站。
- 状态库是 bbolt（`server.state_path`，默认 `state.db`，0600）。`seen` / `pt_checkin` /
  `pt_seen` / `pt_pending` / `pt_card` 各桶的语义由 `internal/store` 集中管理。
  丢状态只会导致重推/重试，不应影响凭据。
- 配置目录只读、状态目录可写：容器里 `/config` 是 `:ro`，持久状态只写 `/data`
  （根文件系统只读，`/tmp` 是 tmpfs）。

### 配置

- 配置只有一份 `config.yaml`，结构按模块分组：`feishu` / `v2ex` / `libra` / `checkin` /
  `reminders` / `notify` / `pt` / `server`。字段名就是 YAML 键。
- YAML 解析用 `yaml.Decoder` + `KnownFields(true)`：**拼错的键直接报错**，不静默忽略。
- 只写要改的项，其余走 `config.Default()` 的默认值。新增配置项要同时更新
  `config.example.yaml` 和 README 表格。
- 环境变量是覆盖层（`FEISHU_WEBHOOK`、`V2EX_COOKIE`、`CHECKIN_TIME`、`TZ` 等），
  用于一次性运行和 CI；文件是主来源。
- secret 只放 `config.yaml`：600、gitignored、不进镜像（`.dockerignore` 已排除
  `config.yaml`）。PT 站点可直接写 `cookie`；需要提交站点列表时改用 `cookie_env`
  引用环境变量。
- 新增凭据要保证：日志、错误、卡片、状态库都不出现原值。

### 飞书卡片与隐私

- V2EX 通知、PT 站内信正文都来自他人，**必须用 `plain_text` 渲染**，禁止放进
  `lark_md`（否则可注入链接或 `<at id=all>`）。
- 错误信息进卡片或日志前要脱敏：webhook URL 是凭据，统一走
  `feishu.RedactWebhook`；不要把 cookie/token 拼进 `Detail`。
- 卡片内容与条数上限（10 条）是用户可见契约，改文案或布局要同步更新
  `internal/feishu/*_test.go` 和 `scripts/smoke.sh` 的断言。
- 飞书限流（`code=11232`）按限流退避重试，不能当成永久失败。

### PT 站点

- 请求必须绑定配置的站点 host：`resolve()` 拒绝跨主机路径/表单提交，避免把 cookie
  带到第三方。
- 站点按 NexusPHP 通用流程处理；内置预设（`internal/ptsite/presets.yaml`）只覆盖
  路径/方法/文案/token，用户显式字段优先。
- 维护页、验证码、登录失效要区分成不同失败原因，不要统一报「签到失败」。
- 消息正文提取要认准消息表：某些主题在标题与消息表之间还有一张导航表，取错表会把
  删除/回复/转发按钮当成正文。

### 时间

- V2EX 任务日从 08:00（UTC+8）开始；`checkin.time` 默认 `08:10`，08:00 前跑要报
  `pending` 并等待窗口，不能误记「已签过」。
- 调度和时间戳统一用 `checkin.timezone`（默认 `Asia/Shanghai`）。

## 发布

镜像从 `v*` tag 发布：测试 → 多架构构建并推送 → 创建 GitHub Release。

```bash
git tag -a v0.6.0 -m "v0.6.0" && git push origin v0.6.0
# -> ghcr.io/sunyin0818/forum-keeper:0.6.0 / :0.6 / :latest（amd64 + arm64）
```

- 发布后把 `compose.yaml` 的 `image:` 钉到新版本。
- Docker Hub 是可选目标：仓库 Secrets 里同时配 `DOCKERHUB_USERNAME` /
  `DOCKERHUB_TOKEN` 才会多推一份。
- 改名不会连带改 GHCR 的包名；旧的 `ghcr.io/sunyin0818/v2ex-notifier` 需要在
  GitHub Packages 页面手动删除。
- 镜像只含静态二进制，`config.yaml`、`state.db`、`*.md` 都被 `.dockerignore` 排除。

## 文档分工

- `README.md`：项目简介、效果、Docker 部署、配置、命令。
- `AGENTS.md`：目录结构、开发命令、工程约束、发布流程。
- `config.example.yaml`：配置模板与逐项注释。
