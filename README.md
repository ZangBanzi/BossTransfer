# BossTransfer 2.1.0

BossTransfer 是面向 NAS 客户的 115 影视资源搜索与下载系统。它由中央管理端和每位客户独立部署的客户端组成。

## 这套系统如何工作

1. 管理员在管理端绑定自己的 115 资源账号，并选择客户可以搜索的目录。
2. 管理员创建客户授权，生成一次性在线激活码。
3. 客户部署客户端，使用默认账号 `admin`、密码 `password` 首次登录并修改密码。
4. 客户使用授权码激活客户端，再扫码绑定自己的 115 账号。
5. 客户搜索管理员的影视资源。选择文件下载时，管理端只提供秒传所需的文件摘要。
6. 客户端调用 115 Open API，把文件秒传到客户自己的 115，再从客户自己的 115 获取下载地址。
7. 客户选择系统下载、CloudDrive2 或 qBittorrent。页面只启用客户已配置且适用于当前文件的方式。

管理端不会取得客户的 115、CloudDrive2 或 qBittorrent 凭据。客户端也不会取得管理员 115 的下载地址和提取码。

## 管理端职责

- 绑定管理员的 115 资源账号。
- 可视化选择允许搜索的 115 目录。
- 创建客户授权、设置有效期和设备数。
- 生成一次性授权码、撤销授权、释放旧设备。
- 校验已授权设备的搜索、秒传准备和二次摘要请求。

## 客户端职责

- 管理独立的 Web 登录账号。
- 使用授权码在线激活。
- 绑定客户自己的 115，并选择秒传保存目录。
- 按影视文件夹展示搜索结果，隐藏 NFO、字幕和图片等辅助文件。
- 可视化选择本地保存目录。
- 按需连接客户自己的 CloudDrive2 或 qBittorrent。
- 下载并显示任务状态。

## 下载方式

| 方式 | 启用条件 | 实际数据来源 |
|---|---|---|
| 系统下载 | 客户已绑定 115，本地目录可写 | 客户自己的 115 下载地址 |
| CloudDrive2 | 客户已绑定 115，已连接 CD2，并选择与 115 秒传目录对应的云端目录 | CD2 为客户 115 中已秒传文件生成的下载地址 |
| qBittorrent | 客户已绑定 115，qB 已验证，资源是磁力或 `.torrent` | 客户端读取客户 115 中的种子并上传给本机 qB，或提交磁力链接 |

CloudDrive2 流程不会调用离线下载来创建 `[Search]` 虚拟目录。它只查找客户 115 中刚完成秒传的真实文件。

## 快速部署

### 1. 准备配置

```bash
cd deploy/docker
cp .env.example .env
mkdir -p data/downloads
```

编辑 `.env`。中央管理端可预填 `BOSSTRANSFER_115_CLIENT_ID`，也可以在 Web 页面填写 115 Open AppID。

### 2. 同机启动管理端与客户端

```bash
docker compose -p bosstransfer build --no-cache
docker compose -p bosstransfer up -d
```

- 管理端：`http://NAS-IP:18085`
- 客户端：`http://NAS-IP:18086`
- 两端首次账号：`admin`
- 两端首次密码：`password`

### 3. 分开部署

中央服务器：

```bash
docker compose -f docker-compose.manager.yml -p bosstransfer-manager build --no-cache
docker compose -f docker-compose.manager.yml -p bosstransfer-manager up -d
```

客户 NAS：

```bash
docker compose -f docker-compose.client.yml -p bosstransfer-client build --no-cache
docker compose -f docker-compose.client.yml -p bosstransfer-client up -d
```

客户部署时在 `.env` 设置：

```env
BOSSTRANSFER_MANAGER_URL=https://你的管理端域名
BOSSTRANSFER_LOCAL_DOWNLOAD_DIR=/volume1/downloads/BossTransfer
```

## 首次使用顺序

### 管理员

1. 登录管理端并修改默认密码。
2. 填写 115 Open AppID，扫码绑定资源账号。
3. 在页面中选择客户允许搜索的 115 目录。
4. 创建客户，设置有效期和设备数。
5. 复制只显示一次的授权码并交给客户。

### 客户

1. 登录自己的客户端并修改默认密码。
2. 填写管理端地址和授权码完成在线激活。
3. 在“后端接入”扫码绑定自己的 115，选择秒传目录。
4. 在页面中选择 NAS 本地保存目录。
5. 需要时连接 CloudDrive2 或 qBittorrent。
6. 返回搜索页，搜索影视名称并选择文件和下载方式。

## 数据卷

| 卷 | 内容 |
|---|---|
| `bosstransfer_manager-data` | 管理端账号、115 令牌、授权、设备和签名密钥 |
| `bosstransfer_client-data` | 客户端账号、设备身份、客户 115 令牌、CD2 与 qB 配置 |

115、CloudDrive2 和 qBittorrent 的令牌或密码使用 AES-GCM 加密后保存。升级时保留两个数据卷。

## 升级

先停止并删除旧 BossTransfer 容器，再删除明确命名的旧镜像，保留数据卷：

```bash
docker compose -p bosstransfer down --remove-orphans
docker image rm bosstransfer/manager:旧版本 bosstransfer/client:旧版本 2>/dev/null || true
docker compose -p bosstransfer build --no-cache
docker compose -p bosstransfer up -d
```

不要添加 `-v`，否则会删除账号、授权和客户配置。

## 验证

```bash
docker compose -p bosstransfer ps
curl -fsS http://127.0.0.1:18085/api/v1/health/live
curl -fsS http://127.0.0.1:18086/api/v1/health/live
```

源码测试：

```bash
go test ./...
```

## 文档

- [正式部署教程](docs/deployment-production.md)
- [管理员与客户操作教程](docs/customer-operation-guide.md)
- [系统架构](docs/architecture.md)
- [GitHub 上传与发布流程](docs/github-upload-guide.md)
- [安全模型](docs/security-model.md)
