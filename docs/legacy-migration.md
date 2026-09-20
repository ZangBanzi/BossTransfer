# BossTransfer 升级到 2.1.0

2.1.0 将产品收敛为“管理员授权资源，客户自行绑定账号并下载”的正式流程。

## 架构变化

- 管理端只绑定管理员的 115 资源账号、选择可搜索目录、创建客户和签发一次性激活码。
- 客户端必须绑定客户自己的 115 账号，搜索管理员允许的资源并先秒传到客户 115。
- 下载器全部由客户在自己的 NAS 上配置。系统下载始终可配置；CloudDrive2 与 qBittorrent 验证成功后才显示为可用。
- 已移除 NextEmby、管理端 CloudDrive2 资源库、管理端下载器策略和客户端 115 本地挂载依赖。
- 默认 Web 账号仍为 `admin/password`，只用于新数据卷首次登录。客户随后可以在 Web 页面修改本地账号密码。

## 升级前备份

停止旧容器前备份两个命名卷：

~~~bash
docker run --rm -v bosstransfer_manager-data:/data -v "$PWD":/backup alpine tar czf /backup/manager-before-2.1.0.tar.gz -C /data .
docker run --rm -v bosstransfer_client-data:/data -v "$PWD":/backup alpine tar czf /backup/client-before-2.1.0.tar.gz -C /data .
~~~

不要执行 `docker compose down -v`，否则授权、账号和加密凭据会随命名卷一起删除。

## 替换旧容器与镜像

先停止旧服务并删除旧镜像，保留数据卷：

~~~bash
docker compose -p bosstransfer down --remove-orphans
docker image ls --format '{{.Repository}}:{{.Tag}}' | grep '^bosstransfer/' | xargs -r docker image rm
~~~

解压对应 NAS 架构的正式包：

~~~bash
tar xzf bosstransfer-docker-2.1.0-linux-<arch>.tar.gz
cd bosstransfer-docker-2.1.0-linux-<arch>
cp .env.example .env
chmod 600 .env
~~~

保留原命名卷名称：

~~~env
BOSSTRANSFER_MANAGER_DATA_VOLUME=bosstransfer_manager-data
BOSSTRANSFER_CLIENT_DATA_VOLUME=bosstransfer_client-data
~~~

重新构建并启动：

~~~bash
docker compose -p bosstransfer build --no-cache
docker compose -p bosstransfer up -d
~~~

## 升级后操作

1. 管理员登录管理端，绑定管理员 115 并选择客户可搜索的目录。
2. 管理员创建客户，设置有效期和设备数，生成一次性激活码。
3. 客户登录客户端并在线激活，然后绑定客户自己的 115 账号。
4. 客户在“后端接入”中选择本地保存目录；需要时再接入 CloudDrive2 或 qBittorrent。
5. 搜索一个小型影视文件，确认秒传、客户 115 下载链接和所选下载器均成功后再正式使用。

旧版 NextEmby、管理员 CloudDrive2 和客户端资源挂载配置不会参与 2.1.0 的新流程，可以在确认升级成功后自行清理。
