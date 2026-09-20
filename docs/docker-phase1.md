# 阶段 1 Docker 部署记录

日期：2026-09-18  
版本：0.1.0-phase1

## 结论

已在用户 NAS 上完成 Docker 方式部署测试。NAS 架构为 `x86_64`，Docker 与 Docker Compose 可用。由于 NAS 上 `8085` 已被占用，本次 Docker 对外端口使用：

| 服务 | 对外端口 | 容器端口 | 页面 |
|---|---:|---:|---|
| 管理端 | 18085 | 8085 | `/` |
| 客户端 | 18086 | 18085 | `/` |

公网健康检查和页面访问均通过。后续页面优化部署时，已先删除旧的 BossTransfer 容器，再启动新版容器。

## UI 优化记录

已从 GitHub 安装并使用 `frontend-app-builder` skill：

```text
<CODEX_HOME>\skills\frontend-app-builder
```

视觉概念图：

```text
<LOCAL_GENERATED_ASSET_PATH>\boss-transfer-concept.png
```

本地实现截图：

```text
dist/qa/manager-dashboard.png
dist/qa/client-dashboard.png
```

按概念图继续优化：

- 页面顶部不再使用额外 eyebrow 标签，标题直接进入角色说明。
- “三步操作路径”置于标题下方，先告诉管理员当前应做什么。
- “主要操作”集中放置跨端跳转、刷新状态、复制地址和 CloudDrive2 设置。
- “服务状态”把 ready 检查和测试版警告并排显示。
- “诊断与调试”和“后续功能（开发中）”分开，灰色按钮只表示路线预留。

## 本地改动

- 管理端内置页面升级为管理控制台：顶部操作按钮、三步操作路径、业务入口预留、部署排查和服务检查。
- 客户端内置页面升级为客户端控制台：返回管理端、CloudDrive2 设置、本机复制流程预留、部署排查和服务检查。
- 客户端新增 `-healthcheck`，Docker healthcheck 可真实验证进程。
- Docker Compose 改为双服务：`manager` 与 `client`。
- Docker runtime 镜像使用 `FROM scratch`，通过 `COPY --chmod=755` 修正 Windows 打包后的 Linux 执行位。
- 新增 Docker 专用打包脚本：`scripts/build-docker-package.ps1`。

## 本地产物

Docker 包：

```text
dist/docker/bosstransfer-docker-0.1.0-phase1-linux-amd64.tar.gz
```

SHA256：

```text
5752bee194976c9d5d4fa79eb49207aca3449dcfaf3d33821a87e0abc2005990
```

包内关键文件：

- `.env`
- `.env.example`
- `docker-compose.yml`
- `manager-runtime.Dockerfile`
- `client-runtime.Dockerfile`
- `bin/manager`
- `bin/client`
- `README-docker.md`

## 已执行验证

本地：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-phase1.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\smoke-phase1.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-docker-package.ps1 -Architecture amd64
```

结果：

- `go test ./...` 通过。
- 管理端与客户端 Windows 本地构建通过。
- 本地进程烟测通过：健康检查、首页、摘要 JSON、CloudDrive2 设置 JSON。
- Docker amd64 包构建通过。
- tar 包层级检查通过。

NAS：

```text
Docker version 28.5.2
Docker Compose version v2.40.3
```

部署方式：

```bash
docker compose -p bosstransfer down --remove-orphans
docker compose -p bosstransfer build --no-cache
docker compose -p bosstransfer up -d
```

部署验证：

- 旧容器 `bosstransfer-client-1` 与 `bosstransfer-manager-1` 已先被删除。
- `http://127.0.0.1:18085/api/v1/health/live` 返回 200。
- `http://127.0.0.1:18086/api/v1/health/live` 返回 200。
- `http://127.0.0.1:18085/` 首页下载成功。
- `http://127.0.0.1:18086/` 首页下载成功。
- 从外部访问 `http://<NAS-IP>:18085/api/v1/health/live` 返回 200。
- 从外部访问 `http://<NAS-IP>:18086/api/v1/health/live` 返回 200。
- 从外部访问两个首页均返回 200，管理端与客户端页面均包含 `三步操作路径` 和 `主要操作`。
- 从外部访问 ready 接口返回 `degraded`，这是阶段 1 测试版预期：真实数据库、设备 Key、CloudDrive2 和外部适配器尚未开启。

## 运行说明

如果需要在 NAS 上重新部署：

```bash
cd /tmp/bosstransfer-docker-0.1.0-phase1-linux-amd64
sudo docker compose -p bosstransfer build --no-cache
sudo docker compose -p bosstransfer up -d
```

停止：

```bash
cd /tmp/bosstransfer-docker-0.1.0-phase1-linux-amd64
sudo docker compose -p bosstransfer down
```

查看状态：

```bash
sudo docker compose -p bosstransfer ps
```

## 当前限制

- 当前仍是阶段 1 测试包，真实任务队列、Emby 目录同步、设备注册和转存流程尚未开启。
- `BOSSTRANSFER_CD2_ADDR=127.0.0.1:19798` 在容器内指向客户端容器自身；后续接 CloudDrive2 时需要改成宿主机可达地址或改用 host network/网关地址。
- 测试账号不能直接访问 Docker daemon，本次 Docker 操作使用 `sudo` 执行。

