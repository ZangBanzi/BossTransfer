# 阶段 1 部署测试记录

日期：2026-09-18  
版本：0.1.0-phase1

## 结论

已完成本地 Linux amd64/arm64 发布包构建、包内容检查和部署脚本语法检查。已使用测试 SSH 入口把 linux/amd64 发布包上传到服务器临时目录，并完成远程 manager live health smoke test。没有做长期驻留服务部署，也没有写系统目录。

## 已生成产物

目录：`dist/release/`

| 文件 | 架构 | SHA256 | 大小 |
|---|---|---|---:|
| `bosstransfer-0.1.0-phase1-linux-amd64.tar.gz` | linux/amd64 | `5453806cc8987856a7dba63df5cebfdc088a5584e71154a5eab30b2f71da86e0` | 5,887,607 |
| `bosstransfer-0.1.0-phase1-linux-arm64.tar.gz` | linux/arm64 | `30e2e3b12fb64dab78851c146779664b8b579ba3b6f79147b975de431f7f6647` | 5,287,574 |
| `release-manifest.phase1.json` | manifest | 记录上述产物 | - |

## 实际验证

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File 'BossTransfer\scripts\build-release.ps1'
```

结果：

- `go test ./...` 通过。
- linux/amd64 `manager`、`client` 构建通过。
- linux/arm64 `manager`、`client` 构建通过。
- tar.gz 产物生成通过。
- SHA256 manifest 生成通过。

```powershell
tar -tf 'BossTransfer\dist\release\bosstransfer-0.1.0-phase1-linux-amd64.tar.gz'
tar -tf 'BossTransfer\dist\release\bosstransfer-0.1.0-phase1-linux-arm64.tar.gz'
```

包内已确认包含：

- `bin/manager`
- `bin/client`
- `remote-smoke.sh`
- `README.md`
- `VERSION`
- `CHANGELOG.md`
- `api/openapi.yaml`
- `deploy/docker-compose.yml`
- `deploy/.env.example`
- 阶段文档和安全/API 兼容性文档

```powershell
PowerShell script syntax: OK
```

已检查：

- `scripts/build-release.ps1`
- `scripts/deploy-linux-ssh.ps1`
- `scripts/verify-phase1.ps1`
- `scripts/smoke-phase1.ps1`

## 远程部署脚本

脚本：`scripts/deploy-linux-ssh.ps1`

设计边界：

- 使用 `scp` 上传发布包。
- 使用 `ssh` 在用户目录解压。
- 默认远程目录为 `~/bosstransfer-phase1`。
- 不需要 root。
- 不写系统目录。
- 不碰 Docker。
- 只启动发布包内 `bin/manager` 做 smoke test。
- 只清理同目录下 `manager.pid` 指向的测试进程。

用法示例：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\deploy-linux-ssh.ps1 `
  -HostName example.local `
  -UserName user `
  -Architecture amd64 `
  -RemoteDir '~/bosstransfer-phase1' `
  -ManagerPort 8085
```

如果使用 SSH key：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\deploy-linux-ssh.ps1 `
  -HostName example.local `
  -UserName user `
  -IdentityFile C:\path\to\key `
  -Architecture amd64
```

## 远程部署测试

目标：一台 Linux x86_64 测试服务器。

架构探测：`Linux x86_64`。

远程目录：`/tmp/bosstransfer-phase1-test`。

测试端口：`127.0.0.1:18085`。

部署产物：`bosstransfer-0.1.0-phase1-linux-amd64.tar.gz`，SHA256 `5453806cc8987856a7dba63df5cebfdc088a5584e71154a5eab30b2f71da86e0`。

实际命令：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File 'BossTransfer\scripts\deploy-linux-ssh.ps1' `
  -HostName <NAS-IP> `
  -UserName <SSH-USER> `
  -Port <SSH-PORT> `
  -Architecture amd64 `
  -RemoteDir '/tmp/bosstransfer-phase1-test' `
  -ManagerPort 18085
```

结果：

```text
manager live health ok on http://127.0.0.1:18085
```

远程复核：

- `/tmp/bosstransfer-phase1-test/VERSION` 为 `0.1.0-phase1`。
- `bin/manager` 与 `bin/client` 存在并可执行。
- 精确进程检查返回 `NO_MANAGER_PROCESS`，说明 smoke test 后测试进程已停止。
- 测试账号没有可用的 home 目录，所以测试目录使用 `/tmp`，未使用 `~`。

## 未完成

- 未验证服务器 Docker、systemd、端口、防火墙或反向代理。
- 未验证 fnOS FPK 真机安装。
- 未做长期驻留服务部署，只完成用户态临时 smoke test。

