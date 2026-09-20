# BossTransfer 2.1.0

- 管理端只绑定管理员 115 并管理客户授权。
- 客户下载前绑定自己的 115，资源先秒传到客户账号。
- 下载方式由客户本地配置：系统下载、CloudDrive2 或 qBittorrent。
- CloudDrive2 不再创建 `[Search]` 虚拟目录。
- `.torrent` 从客户 115 读取后上传到本机 qB。
- 管理端与客户端重做为桌面和手机响应式页面。

升级时保留 manager-data 和 client-data 命名卷，删除旧 BossTransfer 容器和镜像后使用 --no-cache 重建。
