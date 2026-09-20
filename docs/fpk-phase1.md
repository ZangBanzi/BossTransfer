# 阶段 1 FPK 构建记录

> 历史记录：该 FPK 未通过飞牛应用中心安装验证，也未适配 ARM，不属于 BossTransfer 1.1.0 正式发布物。1.1.0 请使用 Docker 包。

日期：2026-09-18  
版本：FPK manifest `0.1.0`，BossTransfer 工程阶段 `0.1.0-phase1`

## 结论

已在飞牛服务器上使用官方 `fnpack` 1.2.4 构建 x86 FPK，并下载到本地交付目录。尚未执行飞牛应用中心安装测试。

## 产物

本地文件：

```text
dist/fpk/boss-transfer-client-fnos-x86_0.1.0.fpk
```

大小：

```text
2,890,440 bytes
```

SHA256：

```text
ecae55df178c8307082d3f48f3ccfe0098aeefc2b098031d189d6d89072661f1
```

服务器端原始输出：

```text
/tmp/bosstransferclient.fpk
```

服务器端 SHA256 与本地一致。

## 实际验证

- 服务器存在官方 `fnpack`：`/usr/local/bin/fnpack`
- `fnpack` 版本：`1.2.4`
- 服务器架构：`x86_64`
- 使用 linux/amd64 `client` 二进制。
- `fnpack build -d /tmp/boss-transfer-fpk-build` 成功。
- 本地 JSON 配置校验通过：
  - `config/privilege`
  - `config/resource`
  - `app/ui/config`

## 打包调整记录

`fnpack create` 模板显示当前 1.2.4 需要以下结构：

- `cmd/install_init`
- `cmd/install_callback`
- `cmd/upgrade_init`
- `cmd/upgrade_callback`
- `cmd/uninstall_init`
- `cmd/uninstall_callback`
- `cmd/config_init`
- `cmd/config_callback`
- `cmd/main`

因此阶段 1 FPK 使用 `cmd/*` 生命周期脚本，不再使用旧的 `wizard/install`、`wizard/upgrade`、`wizard/uninstall` shell 脚本。`wizard/` 保持空目录。

`app/ui/config` 已按官方模板改为 `.url` map 格式。

## 未验证

- 未在飞牛应用中心安装。
- 未验证启动、停止、卸载、升级生命周期。
- 未验证 UI 网关映射。
- 未验证 ARM FPK。
