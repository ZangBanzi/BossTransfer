# 阶段检查表

## 阶段 0：审计与技术验证

- [x] 审计当前目录和旧插件结构。
- [x] 读取项目规则、README、交接和测试报告。
- [x] 读取旧实体归档附件。
- [x] 创建需求、架构、安全、状态机、端口、迁移和 API 兼容性文档。
- [x] 创建 OpenAPI 草案。
- [x] 创建最小连通性探针脚本。
- [ ] 安装工具链并运行探针。
- [ ] 获取真实测试端点、凭证和 fnOS 设备。

## 阶段 1：工程骨架

- [x] Go workspace 和模块边界。
- [x] 管理端健康检查入口。
- [x] 客户端健康检查入口。
- [x] SQLite 初始迁移草案。
- [x] Vue/Vite 前端基础壳。
- [x] Docker Compose 骨架。
- [x] fnOS FPK 空壳和生命周期脚本。
- [x] Go 编译和单元测试。
- [x] manager/client 本地进程烟测。
- [x] linux/amd64 与 linux/arm64 发布包构建。
- [x] SSH 部署脚本语法检查和包内容检查。
- [x] x86 FPK 使用官方 fnpack 构建。
- [ ] npm 前端安装和构建。
- [ ] Docker Compose 配置验证和镜像构建。
- [ ] x86 FPK 飞牛应用中心安装测试。
- [ ] ARM FPK 构建。
- [x] 服务器 SSH 部署和远程 smoke test。
- [ ] 真实 fnOS 安装、启停和卸载。
