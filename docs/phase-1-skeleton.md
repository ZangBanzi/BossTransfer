# 阶段 1 工程骨架记录

日期：2026-09-18  
版本：0.1.0-phase1

## 结论

阶段 1 已完成最小工程骨架、Go 单元测试、Go 构建和本地进程烟测。当前机器仍缺少 npm、Docker 和 fnpack，因此前端构建、Compose 校验、镜像构建和 FPK 打包仍标记为未测。

## 已完成

- Go module 与管理端、客户端入口。
- `/api/v1/health/live` 与 `/api/v1/health/ready` 健康检查。
- 外部适配器探针边界，未加入生产业务调用。
- SQLite 初始迁移草案，包含设备、授权、目录、任务、任务事件、Outbox 和审计。
- 活动任务部分唯一索引，覆盖同用户、同设备、同内容、同目标目录的重复点击。
- Docker Compose 与 manager Dockerfile 草案，默认端口 8085，非 root 用户 65532。
- fnOS FPK 空壳结构、生命周期脚本、权限/资源文件和占位图标。
- Vue/Vite 前端依赖骨架。
- 离线验证脚本 `scripts/verify-phase1.ps1`。
- 进程烟测脚本 `scripts/smoke-phase1.ps1`。

## 实际验证

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File 'BossTransfer\scripts\verify-phase1.ps1'
powershell -NoProfile -ExecutionPolicy Bypass -File 'BossTransfer\scripts\smoke-phase1.ps1'
```

验证结果：

```json
{
  "ok": true,
  "checked_files": 23,
  "go_files": 13,
  "go_test": "passed",
  "go_build": "passed"
}
```

烟测结果：

```json
{
  "ok": true,
  "manager_ready": "degraded",
  "client_ready": "degraded",
  "cd2_status": "unknown"
}
```

另外复跑：

- PowerShell 探针语法检查：通过。
- OpenAPI smoke checks：通过。
- manager `/api/v1/health/live`：通过。
- manager `/api/v1/health/ready`：通过，骨架阶段预期为 `degraded`。
- client `/api/v1/health/live`：通过。
- client `/api/v1/health/ready`：通过，骨架阶段预期为 `degraded`。
- client `/api/v1/client/settings/clouddrive2`：通过，骨架阶段预期 `token_present=false`。

## 未测

- `npm install` / `npm run build`
- `docker compose config`
- Docker 镜像构建和运行健康检查。
- `fnpack build`
- x86/ARM fnOS 真机安装、启停、升级和卸载。
- Emby、NextEmby、NextFind、115、CloudDrive2 真实联通。

## 后续服务器验证建议

服务器安装工具链后按顺序执行：

```powershell
cd BossTransfer
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-phase1.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\smoke-phase1.ps1
cd web
npm install
npm run build
cd ..
docker compose --env-file .\deploy\docker\.env.example -f .\deploy\docker\docker-compose.yml config
```

若要验证 FPK，先构建客户端二进制并复制到 `packaging/fnos/app/bin/client`，再按 fnOS 官方工具执行打包。
