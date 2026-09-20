# 阶段 0 验收步骤

## 可复现命令

```powershell
Get-ChildItem -LiteralPath .\BossTransfer -Recurse -File | Select-Object FullName
Get-Content -LiteralPath .\BossTransfer\README.md -Raw
Get-Content -LiteralPath .\BossTransfer\api\openapi.yaml -Raw
```

## 审计验收

| 项 | 预期 |
|---|---|
| 新项目目录 | `BossTransfer/` 存在 |
| 旧插件 | `MoviePilot-Plugins-Boss/` 未被修改 |
| 必需文档 | `docs/requirements.md`、`architecture.md`、`security-model.md`、`task-state-machine.md`、`port-plan.md`、`legacy-migration.md`、`api-compatibility.md` 存在 |
| ADR | `docs/adr/` 存在且包含初始 ADR |
| OpenAPI | `api/openapi.yaml` 存在 |
| 探针 | `scripts/probes/` 存在 PowerShell 探针 |

## 阶段状态

| 验收项 | 状态 | 证据 |
|---|---|---|
| 仓库审计 | 通过 | `docs/phase-0-audit.md` |
| 旧插件迁移边界 | 通过 | `docs/legacy-migration.md` |
| 官方接口初核 | 部分通过 | `docs/api-compatibility.md` |
| Docker 现场核验 | 未测 | 当前机器无 `docker` |
| FPK 工具链 | 未测 | 当前机器无 `fnpack` |
| Go 构建 | 未测 | 当前机器无 `go` |
| 真实外部闭环 | 未测 | 缺测试端点、凭证和设备 |

