# 部署到云端服务器

把整个 `deploy/` 目录拷到服务器，三个命令搞定。

## 1. 准备

```bash
mkdir -p /opt/v2ex-notifier && cd /opt/v2ex-notifier
# 把 deploy/compose.yaml 和 deploy/env.example 拷过来

cp env.example .env
chmod 600 .env
$EDITOR .env        # 填 V2EX_TOKEN 和 FEISHU_WEBHOOK
```

`.env` 里**不要**写 `HTTPS_PROXY` —— 云端直连 v2ex.com。
（本地那台才需要代理，容器里也要写成 `host.docker.internal` 而不是 `127.0.0.1`。）

## 2. 启动

```bash
docker compose pull
docker compose up -d
docker compose logs -f
```

首次启动应该看到：

```
INFO authenticated with V2EX              username=...
INFO v2ex-notifier starting               interval=1m0s first_run=skip state=/data/state.db
INFO startup message sent
INFO first run: recording existing notifications without pushing  count=N
```

群里会先收到一张 🟢 启动卡片。**不会刷屏** —— `FIRST_RUN=skip` 会把当前未读提醒
全部标记为已见但不推送，之后只推真正的新提醒。

## 3. 升级

```bash
# 改 compose.yaml 里的 image tag（例如 :0.1.0 -> :0.2.0）
docker compose pull && docker compose up -d
```

状态卷 `notifier-data` 不受影响，不会重推历史提醒。

## 排查

| 现象 | 原因 |
|---|---|
| `dial tcp [2a03:2880:...]:443: i/o timeout` | 没有代理且 DNS 被污染。云端正常情况不应出现；若服务器在受限网络，需要一个真能到 v2ex.com 的代理，配上 `HTTPS_PROXY`。 |
| `dial tcp 127.0.0.1:7897: connect: connection refused` | 误把本地代理地址配到了服务器，或容器内用了 `127.0.0.1`（那是容器自己）。删掉 `HTTPS_PROXY`。 |
| `V2EX rejected the token` | 令牌失效，去 <https://www.v2ex.com/settings/tokens> 换新，更新 `.env` 后 `docker compose up -d`。 |
| 长时间无日志 | 正常。无新提醒时 info 级别不打日志；推送成功才有 `pushed notifications`。想确认在轮询，把 `LOG_LEVEL=debug`。 |
| 飞书返回 `code=19021` | 机器人开了签名校验但没填 `FEISHU_SECRET`。 |

## registry 选择

`compose.yaml` 默认用 GHCR。国内服务器拉 GHCR 可能很慢，两个替代方案：

1. 配 Docker Hub 镜像（仓库 Settings → Secrets 加 `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN`，
   workflow 会自动多推一份），然后改 `image:` 为 `<你的用户名>/v2ex-notifier:0.1.0`
2. 自建 registry，或用 `docker save` / `docker load` 搬运
