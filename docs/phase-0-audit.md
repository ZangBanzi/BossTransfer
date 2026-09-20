# 阶段 0 审计报告

日期：2026-09-18  
范围：`<WORKSPACE_ROOT>` 与附件 `<MASTER_PROMPT_PATH>`

## 结论

当前工作区不是 BossTransfer 成品仓库，而是一个无 Git 元数据的父目录，里面包含既有 `MoviePilot-Plugins-Boss` 插件项目。已创建独立 `BossTransfer/` 新项目目录，旧插件保持不变。

## 已执行检查

| 检查项 | 结果 |
|---|---|
| `pwd` | `<WORKSPACE_ROOT>` |
| `git status --short` | 父目录和 `MoviePilot-Plugins-Boss` 均返回非 Git 仓库 |
| `rg --files` | 父目录包含 `.work/`、`MoviePilot-Plugins-Boss/`；旧项目含 `plugins.v2/mediaarchiver`、`frontend`、`tests`、`docs`、发布包 |
| `AGENTS.md` | 已读取，要求先读 `HANDOFF.md`、`README.md`、`TEST_REPORT.md`，不得新增监听端口，不得改旧播放链路 |
| 旧附件 | 已读取 `archive/original_uploads/01-__init__.py`、`02-README.md`、`03-package.v2.fragment.json` |
| 当前基线 | `MoviePilot-Plugins-Boss/plugins.v2/mediaarchiver/__init__.py` 为 `MediaArchiver` 4.6.0，仍是 MoviePilot 插件 |
| Docker | 当前 PATH 无 `docker` 命令，无法现场读取容器、网络、挂载和版本 |
| 端口 | 对 8096、8091、8097、8098、8092、3333、3334、19798 的监听查询没有返回监听记录 |
| 本地工具链 | `go`、`python`、`npm`、`fnpack` 不在 PATH；`node` 为 v24.19.0 |

## 旧插件观察

原始附件 `archive/original_uploads/01-__init__.py` 是 MoviePilot v2 实体归档插件：

- 使用 `_PluginBase`、`MediaServerHelper`、`VForm/VBtn` 和同步 API 入口。
- 使用 `threading.Lock` 防重复点击。
- 未分页请求 Emby `/Items`，字段包含 `Path,MediaSources`。
- 对 `/mnt/115` 进行文件复制和移动。
- 成人、Remux、平台剧集规则以短关键词硬编码。
- 对配置根目录不存在采用硬失败，这是可保留思想。
- 用 `Path.resolve(strict=True).relative_to(root)` 做根目录约束，这是可保留思想。

当前 4.6.0 插件已转为虚拟库和封面工坊，保留 8098 -> MoviePilot API 3334 -> Emby 8096 旧链路，不适合作为 BossTransfer 新系统脚手架。

## 外部资料核对

| 系统 | 核对结果 |
|---|---|
| Emby | 官方 API 文档可访问。`/System/Info`、`/Users/{UserId}/Items`、`/Items/{Id}/Images/{Type}` 等接口已确认存在，后续只在管理端用管理员 Key 读取和代理必要字段。 |
| CloudDrive2 | 官方站点声明核心服务提供 gRPC API、API Token 权限控制和 proto 下载；API 指南页可访问，版本显示 v1.0.17。下载页显示当前 core release 1.0.18。 |
| fnOS | 用户提供的官方 `developer.fnnas.com` 链接在当前浏览工具中不可访问；可访问的镜像资料显示 FPK 结构、`manifest`、`config/privilege`、`config/resource`、`wizard`、`cmd`、`fnpack build` 等约束。最终必须用官方页面和真实 fnOS 设备复核。 |
| NextEmby | Docker Hub 显示 `nextemby/nextemby`，默认 8091，近期标签观察到 v4.5.8；Wiki 可访问但未找到稳定公开 API 规范。 |
| NextFind | Docker Hub 显示 `nextemby/nextfind`，默认 8092，近期标签观察到 v3.6.4；第一版只预留适配器，不修改服务。 |
| 115 | `open.115.com` 开放平台和官方帮助页可访问。官方协议说明开放接口需开发者入驻、应用审核和接口权限；具体分享 API 需登录开放文档后再核实。自动分享未核实时必须提供人工回退。 |

## 阻塞项

- 缺少 Go、Python、npm、Docker、fnpack，当前机器不能完成构建、测试、镜像、FPK。
- 未提供 Emby、NextEmby、115、CloudDrive2 凭证和测试端点，只能准备探针，不能声称真实联通。
- 没有 x86/ARM fnOS 真机，不能声称 FPK 可安装或可升级。
- NextEmby、NextFind、115 分享 API 未确认公开稳定接口。

## 阶段 0 判断

阶段 0 文档和探针可以完成。阶段 1 的工程骨架可以继续，但正式外部适配器只能先做接口边界、配置模板、Mock 和契约测试，不能写“看起来能用”的生产调用。

## 资料链接

- Emby System API: https://dev.emby.media/reference/RestAPI/SystemService/getSystemInfo.html
- Emby Items API: https://dev.emby.media/reference/RestAPI/ItemsService.html
- Emby library browsing: https://dev.emby.media/doc/restapi/Browsing-the-Library.html
- CloudDrive2 API guide: https://www.clouddrive2.com/api/CloudDrive2_gRPC_API_Guide.html
- CloudDrive2 API overview: https://www.clouddrive2.com/
- CloudDrive2 download: https://www.clouddrive2.com/en/download.html
- 115 open platform: https://open.115.com/
- NextEmby Docker Hub: https://hub.docker.com/r/nextemby/nextemby
- NextFind Docker Hub: https://hub.docker.com/r/nextemby/nextfind
- fnOS official docs, pending access: https://developer.fnnas.com/
