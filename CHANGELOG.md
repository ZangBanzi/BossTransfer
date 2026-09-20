# Changelog

## Unreleased

- 暂无。

## 2.1.0 - 2026-09-20

- 管理端改为直接绑定管理员 115 Open 资源账号，只负责资源搜索与客户授权。
- 客户下载前必须绑定自己的 115；资源先秒传到客户 115，再从客户账号取得下载地址。
- 下载方式由客户本地配置决定，管理端不再控制系统下载、CloudDrive2 或 qBittorrent。
- CloudDrive2 改为读取客户 115 中已秒传的真实文件，不再创建 `[Search]` 虚拟目录。
- `.torrent` 由客户端使用 115 所需请求头读取后上传给本机 qB，避免 qB 直接获取签名 URL 失败。
- 移除 NextEmby、管理端 CloudDrive2 资源库和客户端 115 挂载依赖。
- 重做管理端与客户端页面，优化影片文件夹归组与手机端后端接入页面。
- 新增 115 API、令牌加密、资源凭据、秒传二次摘要和三种下载路径测试。

## 2.0.3 - 2026-09-20

- 客户端 CloudDrive2 设置新增远程目录浏览器，读取官方 `canOfflineDownload` 能力，只允许保存明确支持离线下载的具体云盘目录。
- 修复把 CloudDrive2 聚合根 `/` 误判为有效下载位置的问题；直接挂载 115 云根的 NAS 也可验证并选用该根目录。
- 修复后端接入页在手机和平板宽度仍保留桌面双栏、导致表单被挤出屏幕的问题，并优化挂载点、云目录和保存操作的触控布局。
- Docker Compose 增加 PID 上限和容器日志轮转；正式运行镜像增加 OCI 版本信息，构建上下文增加 `.dockerignore`。
- 正式部署改为显式无缓存构建，避免部分 NAS 的 BuildKit 在删除旧镜像后仍复用旧二进制层；旧聚合根配置不再误报 CloudDrive2 已就绪。
- 完成真实管理端资源搜索与系统下载链路验证，确认远程文件按声明大小完整落盘；补充 CloudDrive2 目录能力、直挂云根和不支持目录的回归测试。

## 2.0.2 - 2026-09-20

- 引入 `awesome-design-md` 技能并以 Linear `DESIGN.md` 作为客户端与管理端的统一视觉规范。
- 重做资源搜索、后端接入、登录、首次改密和授权激活页面，使用暗色原生画布、精确层级和统一交互状态。
- 客户端搜索页进一步压缩选择路径，保留影片目录聚合、视频文件展开和下载器状态提示。
- 客户端设置改用编号步骤与聚焦工作区，授权、下载位置、CloudDrive2、qBittorrent 和登录安全的状态一眼可见。
- 管理端增加资源库、用户源、客户授权和管理账号四段快捷导航，页面内容按实际操作顺序排列。
- 完成桌面与移动端浏览器验收，验证无横向溢出、无脚本错误且 NFO 辅助文件不会进入客户结果。

## 2.0.1 - 2026-09-20

- 客户端搜索结果改为按影片文件夹聚合，主视图不再逐条展示 NFO、字幕等辅助文件。
- 兼容 CloudDrive2 的 `[Search]关键词` 虚拟目录返回，按真实影片目录名和文件名前缀重新归组，并清理 TMDB 标记与重复副本后缀。
- 每个影片文件夹直接显示片名、清晰度、影片大小和可下载影片数，并自动选择最大影片文件作为快捷下载目标。
- 文件详情只保留可下载的视频、ISO 和种子文件，仍可为单个文件选择管理员允许且本机已就绪的下载器。
- 重做客户端资源搜索页和后端接入页，新增下载能力状态、设置分区导航、可视化挂载位置列表与移动端布局。
- 管理端搜索响应新增不泄露资源路径的影片分组标识与文件夹显示名，同时保持原文件级字段兼容。

## 2.0.0 - 2026-09-20

- 将管理端与客户端改为完整的中央授权架构；本地 Web 登录和软件授权分别管理。
- 新安装的管理端和客户端默认使用 `admin/password`，支持在 Web 页面修改；环境变量仅用于首次初始化，不覆盖持久化凭据。
- 新增一次性授权码、设备身份、心跳租约、到期时间、设备上限、策略更新、授权撤销和设备释放。
- 新增 NextEmby/Emby 官方 API 接入，可同步用户并为指定客户创建授权。
- 管理端通过 CloudDrive2 官方 gRPC 配置管理员资源库，支持账号密码、TOTP 或 API Token；可展开聚合挂载并可视化选择真正支持搜索的云盘根。
- CloudDrive2 搜索优先使用原生搜索；聚合根会并发查询可搜索云根，服务不支持搜索时使用有目录数、条目数、深度和总时限的兼容遍历。
- 客户端改为搜索管理员资源库；搜索结果使用短期加密资源凭据，解析下载前再次校验设备和授权。
- 新增系统下载、客户 CloudDrive2 和 qBittorrent 三种下载方式，下载器同时受中央策略与客户端本地验证控制。
- CloudDrive2 客户端支持账号密码或 API Token，直接从挂载列表选择下载目标目录。
- qBittorrent 支持账号密码与 5.2+ API Key；仅接收磁力链接和 `.torrent` 文件。
- 密码使用 PBKDF2 摘要；设备凭据、CloudDrive2、NextEmby 和 qBittorrent 秘密在对应数据卷中加密保存。
- 提供合并部署、仅管理端和仅客户端三套 Compose，并保持 1.1.0 默认命名卷兼容。
- 双架构 Docker 正式包内置 CA 证书包和 `scratch` 运行镜像定义，部署时无需拉取基础镜像。
- 更新 OpenAPI、正式部署、客户操作、升级迁移和 GitHub Release 文档。

## 1.1.0 - 2026-09-19

- 接入 CloudDrive2 官方 gRPC API，通过最小权限 API Token 读取真实挂载点。
- 新增三步存储向导与可视化来源/保存目录浏览器，不再要求填写容器路径。
- API Token 仅在客户端本机加密保存，管理端和浏览器读取接口均不返回明文。
- 修复 NAS 宿主机路径被误当作容器路径、无效配置仍提示成功、无效来源搜索返回空结果等问题。
- Docker 增加宿主网关和 `rslave` 挂载传播，支持 CloudDrive2 重连后的 FUSE 挂载。
- 设置页与存储设置 API 增加独立 HTTP Basic 认证、JSON Content-Type 和同源校验。
- 更换 CloudDrive2 地址时不再复用旧 Token，并限制连接到本机或 RFC1918 局域网 IP。
- 拒绝来源或目标路径中的符号链接，串行化配置密钥创建与加密落盘。
- 目录浏览器实时验证目标写权限，不允许确认不可写目录。
- 提供 Linux amd64 与 arm64 Docker 发布包和对应校验清单。
- 正式包强制从干净的 `v1.1.0` 标签构建，固定安全文件权限，仅分发 `.env.example`。
- 移除未通过应用中心验证的旧 FPK 骨架；1.1.0 正式支持 Docker 部署。

## 1.0.0 - 2026-09-18

- 将客户端改为正式客户使用页：搜索框、搜索结果、保存到本地、最近保存任务。
- 新增客户端后端接入页，可配置 115 挂载目录和本地保存目录，并检查读写权限。
- 新增真实本地保存能力：从已挂载的 115 目录安全复制文件或文件夹到本地目录。
- 新增路径穿越防护，阻止通过 API 访问源目录或目标目录之外的路径。
- 将管理端改为正式运维面板：服务状态、客户使用流程、部署信息、常用 Docker 命令。
- 更新 Docker Compose：源目录只读挂载，目标目录可写挂载，新增客户端数据卷。
- 更新本地烟测：搜索模拟 115 文件，执行保存任务，并校验复制结果。
- 新增正式部署教程、客户操作教程和 GitHub 上传发布教程。

## 0.1.0-phase1 - 2026-09-18

- Added minimal manager and client Go service entrypoints with liveness and readiness routes.
- Added external adapter probe boundaries without production business calls.
- Added initial SQLite schema draft for devices, licenses, catalog, tasks, outbox, and audit events.
- Added Docker Compose and manager Dockerfile draft using non-root execution and reserved-port avoidance.
- Added fnOS client package skeleton with manifest, privilege/resource files, lifecycle scripts, UI entry, and app directories.
- Added Vue/Vite package skeleton for later admin and client UI work.
- Added `scripts/verify-phase1.ps1` for offline structural checks in the current workstation.
- Added Go unit tests for reserved port handling and HTTP health handlers.
- Added `scripts/smoke-phase1.ps1` for manager/client process smoke testing.
- Added linux amd64/arm64 release package builder and SSH deployment smoke script.
- Added fnOS x86 FPK source structure compatible with `fnpack` 1.2.4 and produced a phase 1 x86 FPK.

## 0.1.0-phase0 - 2026-09-18

- 创建 BossTransfer 新项目工作区。
- 完成阶段 0 审计、需求、架构、安全、状态机、端口、迁移和 API 兼容性文档。
- 创建 OpenAPI v0 草案和外部连通性探针脚本。
- 标记 Go、Python、npm、Docker、fnpack 在当前机器不可用；真实构建、FPK 打包和外部闭环仍未验证。
