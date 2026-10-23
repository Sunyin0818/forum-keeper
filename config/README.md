# config/

运行时配置目录。容器里挂载到 `/config`（只读），运行状态仍然只写 `/data`。

```
config/
├── .env.example          最小配置模板（可提交）
├── .env.reference        全量配置参考（可提交）
├── .env                  实际配置，含 webhook/cookie（gitignored，600）
├── pt-sites.example.json PT 站点配置示例（可提交）
└── pt-sites.json         实际 PT 站点列表，引用 cookie_env（gitignored）
```

## 为什么分开 `/config` 和 `/data`

- `/config`：声明式配置和凭据，变更频率低，应该只读挂载，方便备份/版本管理；
- `/data`：bbolt 状态库（已推送提醒、PT 日状态、消息去重），运行时会写，丢失只会导致重推/重试，不应和配置混在一起。

`docker-compose` 里对应：

```yaml
env_file:
  - path: config/.env
    format: raw
environment:
  PT_SITES_FILE: /config/pt-sites.json
volumes:
  - data:/data
  - ./config:/config:ro
```

## 管理建议

1. **配置进 Git，secret 不进 Git**
   - 提交 `.env.example`、`.env.reference`、`pt-sites.example.json`；
   - `.env`、`pt-sites.json` 已在 `.gitignore`。
2. **`pt-sites.json` 不放裸 cookie**
   - 用 `cookie_env` 引用环境变量；这样站点列表可以安全地放进配置/仓库。
3. **文件权限**
   - `config/.env`、`config/pt-sites.json`：`600`；
   - `config/.env` 由运行 `docker compose` 的宿主机用户读取（Compose 客户端解析 env_file），保持该用户所有即可；
   - `config/pt-sites.json` 会挂载进容器，容器 UID 10001 需要能读：`chown 10001:10001 config/pt-sites.json`。
4. **轮换 cookie**
   - 改 `config/.env` 后 `docker compose up -d` 重建容器即可；状态库 `/data` 不受影响。
5. **备份**
   - 备份 `config/`（有 secret，注意加密）和 `/data` 卷；
   - 不要把 `config/.env` 打进镜像：`.dockerignore` 已排除整个 `config/`。
6. **更强隔离（可选）**
   - 如果不想把 secret 放 `.env`，可由部署系统生成 `config/.env` 再挂载，或使用 Docker secrets/外部 secret manager；
   - 本仓目前不直接支持 secret manager，保持“生成文件后挂载”的方式最简单。
