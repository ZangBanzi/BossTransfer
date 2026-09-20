# BossTransfer 2.1.0 正式部署教程

## 1. 确认 NAS 架构

```bash
uname -m
```

| 输出 | 架构 |
|---|---|
| `x86_64`、`amd64` | `linux/amd64` |
| `aarch64`、`arm64` | `linux/arm64` |

使用与架构一致的正式发布包。下载后校验：

```bash
sha256sum bosstransfer-docker-2.1.0-linux-*.tar.gz
```

## 2. 解压并准备配置

```bash
tar xzf bosstransfer-docker-2.1.0-linux-amd64.tar.gz
cd bosstransfer-docker-2.1.0-linux-amd64
cp .env.example .env
chmod 600 .env
```

不要把 `.env`、账号密码、授权码、API Key 或 Token 上传到 GitHub。

## 3. 清理旧 BossTransfer 容器和镜像

升级时保留命名卷，只清理 BossTransfer 自己的容器和镜像。

同机部署：

```bash
docker compose -p bosstransfer down --remove-orphans
docker image rm bosstransfer/manager:2.0.3 bosstransfer/client:2.0.3 2>/dev/null || true
```

分离部署：

```bash
docker compose -f docker-compose.manager.yml -p bosstransfer-manager down --remove-orphans
docker compose -f docker-compose.client.yml -p bosstransfer-client down --remove-orphans
docker image rm bosstransfer/manager:2.0.3 bosstransfer/client:2.0.3 2>/dev/null || true
```

不要使用 `docker compose down -v` 或 `docker system prune --volumes`。这些命令会删除账号、授权和客户配置。

## 4. 配置中央管理端

管理员服务器的 `.env`：

```env
BOSSTRANSFER_MANAGER_HTTP_PORT=18085
BOSSTRANSFER_MANAGER_DATA_VOLUME=bosstransfer_manager-data
BOSSTRANSFER_MANAGER_RUN_AS=0:0
BOSSTRANSFER_MANAGER_USER=admin
BOSSTRANSFER_MANAGER_PASSWORD=password
BOSSTRANSFER_LICENSE_PEPPER=
BOSSTRANSFER_115_CLIENT_ID=
```

- `BOSSTRANSFER_LICENSE_PEPPER` 留空时会在管理端数据卷中自动生成。显式设置时至少 16 个字符，并在升级时保持不变。
- `BOSSTRANSFER_115_CLIENT_ID` 可以留空，管理员首次登录后在页面填写 AppID。
- 环境变量账号密码只用于空数据卷的首次初始化。

启动管理端：

```bash
docker compose -f docker-compose.manager.yml -p bosstransfer-manager build --no-cache
docker compose -f docker-compose.manager.yml -p bosstransfer-manager up -d
```

建议把 `https://license.example.com` 反向代理到 `127.0.0.1:18085`，并配置有效 TLS 证书。客户激活、心跳和资源请求都使用该地址。

## 5. 配置客户客户端

客户 NAS 的 `.env`：

```env
BOSSTRANSFER_CLIENT_HTTP_PORT=18086
BOSSTRANSFER_CLIENT_DATA_VOLUME=bosstransfer_client-data
BOSSTRANSFER_MANAGER_URL=https://license.example.com
BOSSTRANSFER_SETUP_USER=admin
BOSSTRANSFER_SETUP_PASSWORD=password
BOSSTRANSFER_CD2_ADDR=host.docker.internal:19798
BOSSTRANSFER_LOCAL_DOWNLOAD_DIR=/vol1/downloads/BossTransfer
BOSSTRANSFER_CLIENT_USER=0:0
```

创建系统下载目录：

```bash
mkdir -p /vol1/downloads/BossTransfer
```

启动客户端：

```bash
docker compose -f docker-compose.client.yml -p bosstransfer-client build --no-cache
docker compose -f docker-compose.client.yml -p bosstransfer-client up -d
```

客户端不需要挂载 115 或 CD2 文件系统。系统下载只需要一个可写的本地目标目录。

## 6. 管理端与客户端同机

适合管理员自用或单机验证：

```bash
mkdir -p ./data/downloads
docker compose -p bosstransfer build --no-cache
docker compose -p bosstransfer up -d
```

- 管理端：`http://NAS-IP:18085`
- 客户端：`http://NAS-IP:18086`
- 合并部署中的客户端默认连接 `http://manager:8085`

## 7. 首次初始化

### 管理端

1. 使用 `admin` / `password` 登录。
2. 立即修改管理账号密码。
3. 在“115 资源库”填写 115 Open AppID，扫码绑定管理员资源账号。
4. 在页面内选择客户允许搜索的目录。
5. 创建客户授权，设置有效期和设备数，复制一次性授权码。

### 客户端

1. 使用 `admin` / `password` 登录。
2. 立即修改客户端自己的账号密码。
3. 使用管理端地址和授权码在线激活。
4. 扫码绑定客户自己的 115，选择秒传目录。
5. 在页面内选择 NAS 本地下载目录。
6. 需要时配置 CD2 或 qB。

## 8. CloudDrive2 配置

CD2 在客户 NAS 宿主机运行时，容器通常填写：

```text
host.docker.internal:19798
```

支持：

- CD2 账号、密码和可选 TOTP；
- CD2 API Token。

连接后必须在页面中选择与客户 115 秒传目录对应的 CD2 云端目录。下载时客户端调用 CD2 `GetDownloadLink` 读取客户网盘中的真实文件，不调用离线下载，不会创建搜索虚拟目录。

## 9. qBittorrent 配置

WebUI 地址示例：

```text
http://host.docker.internal:8080
```

支持账号密码和 qBittorrent 5.2+ API Key。保存前会真实读取 qB 版本和默认保存目录。qB 只用于磁力和 `.torrent`；115 中的种子由客户端带 115 所需请求头读取，再上传到本机 qB Web API。

## 10. 验收

### 容器与健康接口

```bash
docker compose -p bosstransfer ps
curl -fsS http://127.0.0.1:18085/api/v1/health/live
curl -fsS http://127.0.0.1:18085/api/v1/health/ready
curl -fsS http://127.0.0.1:18086/api/v1/health/live
curl -fsS http://127.0.0.1:18086/api/v1/health/ready
```

分离部署时给命令加对应的 `-f` 和项目名。

### 业务验收

1. 管理员绑定 115，选择资源目录。
2. 创建临时客户授权并激活一个客户端。
3. 客户绑定自己的 115，选择秒传目录。
4. 搜索一个小型影视或测试文件。
5. 使用系统下载，确认文件先出现在客户 115，再写入 NAS 本地目录。
6. 如客户使用 CD2，确认未出现新的 `[Search]` 目录，并验证真实文件可下载。
7. 如客户使用 qB，用 `.torrent` 测试任务提交。
8. 撤销临时授权，确认客户端不能继续发起新搜索和下载。

## 11. 查看日志

```bash
docker compose -p bosstransfer logs --tail=200 manager client
```

分离部署：

```bash
docker compose -f docker-compose.manager.yml -p bosstransfer-manager logs --tail=200 manager
docker compose -f docker-compose.client.yml -p bosstransfer-client logs --tail=200 client
```

## 12. 数据备份

```bash
docker run --rm -v bosstransfer_manager-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/bosstransfer-manager-data.tar.gz -C /data .
docker run --rm -v bosstransfer_client-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/bosstransfer-client-data.tar.gz -C /data .
```

恢复时先停止对应容器，把归档解压回原命名卷，再重新启动。
