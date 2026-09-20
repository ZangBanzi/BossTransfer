# BossTransfer GitHub 上传与发布流程

## 1. 上传前检查

在项目根目录执行：

```bash
git status --short
go test ./...
git diff --check
```

确认以下内容没有进入仓库：

- `.env`；
- 115、CD2、qB 的令牌或账号密码；
- 客户授权码；
- `manager-data`、`client-data` 数据卷内容；
- 本地构建缓存 `.tools`、`.cache`；
- 下载文件和日志。

## 2. 创建 GitHub 仓库

1. 登录 GitHub。
2. 点击 **New repository**。
3. 填写仓库名，例如 `BossTransfer`。
4. 选择公开或私有。
5. 不要勾选自动创建 README、`.gitignore` 或 License，避免第一次推送冲突。
6. 创建仓库并复制仓库地址。

## 3. 首次上传

如果本地尚未初始化 Git：

```bash
git init
git branch -M main
```

添加并检查文件：

```bash
git add .
git status
```

提交：

```bash
git commit -m "release: BossTransfer 2.1.0"
```

连接远程仓库并推送：

```bash
git remote add origin https://github.com/你的账号/BossTransfer.git
git push -u origin main
```

已经存在 `origin` 时改用：

```bash
git remote set-url origin https://github.com/你的账号/BossTransfer.git
git push -u origin main
```

## 4. 后续更新

```bash
git status --short
go test ./...
git diff --check
git add .
git commit -m "fix: describe the change"
git push
```

## 5. 创建正式版本标签

确认 `VERSION` 与 `internal/buildinfo/buildinfo.go` 都是 `2.1.0`：

```bash
git tag -a v2.1.0 -m "BossTransfer 2.1.0"
git push origin v2.1.0
```

## 6. 创建 GitHub Release

1. 打开 GitHub 仓库的 **Releases**。
2. 点击 **Draft a new release**。
3. 选择标签 `v2.1.0`。
4. 标题填写 `BossTransfer 2.1.0`。
5. 上传 amd64、arm64 正式包及对应 SHA256 文件。
6. 在说明中写明升级时保留数据卷、先清理旧容器和旧镜像、再使用 `--no-cache` 构建。
7. 发布 Release。

## 7. 推荐发布说明

```markdown
## BossTransfer 2.1.0

- 管理端改为直接绑定管理员 115 Open 资源账号
- 客户下载前必须绑定自己的 115
- 资源先秒传到客户 115，再由客户选择下载方式
- 移除管理端下载器策略和 NextEmby 操作
- CD2 不再创建 [Search] 虚拟目录
- 重做管理端与客户端页面，适配手机端
- 支持 linux/amd64 与 linux/arm64

升级时保留 manager-data 和 client-data 数据卷，删除旧 BossTransfer 容器和镜像后使用 --no-cache 重建。
```
