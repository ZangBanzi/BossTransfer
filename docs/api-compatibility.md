# 外部 API 兼容范围

## 115 Open API

BossTransfer 只使用 115 Open 平台接口，不使用浏览器 Cookie 或私有网页接口。

| 功能 | 接口 |
|---|---|
| PKCE 设备授权 | `/open/authDeviceCode`、二维码状态、`/open/deviceCodeToToken` |
| 刷新令牌 | `/open/refreshToken` |
| 用户信息 | `/open/user/info` |
| 搜索 | `/open/ufile/search` |
| 目录列表 | `/open/ufile/files` |
| 目录信息 | `/open/folder/get_info` |
| 下载地址 | `/open/ufile/downurl` |
| 秒传初始化 | `/open/upload/init` |

令牌失效时会先使用 refresh token 刷新一次，再重试原请求。

## CloudDrive2

客户端使用 CD2 官方 gRPC 服务完成：

- 账号密码登录；
- API Token 校验；
- 读取挂载点；
- 浏览云端目录；
- `GetDownloadLink` 获取客户 115 中已存在文件的下载地址。

2.1.0 不调用 `AddOfflineFiles`，因此不会由 BossTransfer 创建 `[Search]` 虚拟目录。

## qBittorrent Web API

客户端使用 `/api/v2`：

- 账号密码登录；
- qBittorrent 5.2+ Bearer API Key；
- 读取版本和默认保存路径；
- `/api/v2/torrents/add` 添加磁力或 `.torrent`。

普通媒体文件不提交给 qBittorrent。
