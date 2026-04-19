# 发版手册

本文档记录 `hcscq/sub2api` 当前实际使用的发版流程，包括 Git tag、GitHub Actions 监控、GHCR 镜像校验，以及本机拉镜像后的验收与上线步骤。

## 发版模型

- 发版工作流位于 `.github/workflows/release.yml`
- 触发方式有两种：
  - 推送 `v*` 格式的 tag
  - 手动 `workflow_dispatch`
- 工作流会依次完成：
  - 根据 tag 生成本次发版使用的 `backend/cmd/server/VERSION`
  - 构建前端产物
  - 调用 GoReleaser 生成 GitHub Release 和 GHCR 镜像
  - 发布成功后把 `backend/cmd/server/VERSION` 回写到默认分支

推荐路径是“带注释的 tag push”，因为 tag 标题和正文会直接成为本次 GitHub Release 的说明。

## 发版前检查

1. 确认要发布的改动已经提交。
2. 如果工作区有其他未提交改动，只提交这次发布相关的文件，不要把无关改动混进去。
3. 先推分支提交，再推 tag。
4. 明确本次是：
   - 标准发版：使用 `.goreleaser.yaml`
   - 简化发版：只发 x86_64 GHCR 镜像，使用 `.goreleaser.simple.yaml`

## 标准发版步骤

### 1. 同步并确认当前状态

```bash
git fetch fork --tags
git status --short --branch
git log --oneline --decorate -n 5
git tag --sort=-creatordate | head -10
```

### 2. 先推送分支提交

```bash
git push fork main
```

### 3. 创建带注释的 tag

tag 第一段建议写一个短标题，正文写本次发布说明。正文会被 GoReleaser 用作 Release 内容。

```bash
git tag -a v0.1.132 -m "补充发版流程文档并新增 release skill" -m "- 新增 docs/RELEASE.md 与 docs/RELEASE_CN.md
- 新增仓库内的 Codex release skill
- 新增 GitHub Release workflow 监控脚本"
```

### 4. 推送 tag

```bash
git push fork v0.1.132
```

## 持续监控 GitHub 镜像构建

仓库内已经附带了一个不依赖 `gh` CLI 的监控脚本：

```bash
python3 .codex/skills/sub2api-release/scripts/watch_release.py \
  --repo hcscq/sub2api \
  --tag v0.1.132
```

常用参数：

- `--interval 15`：每 15 秒轮询一次
- `--timeout 3600`：最多等待 1 小时
- `--run-id <id>`：监控指定的手动触发 run

脚本会：

- 等待 `release.yml` 对应 run 出现
- 持续输出状态变化
- 在结束后输出 JSON 摘要，包含：
  - workflow run URL
  - 最终状态
  - 每个 job 的结果
  - GitHub Release URL
  - 发布时间

## 校验 GitHub Release 和 GHCR 镜像

工作流成功后，先拉取目标镜像并拿到 digest：

```bash
docker pull ghcr.io/hcscq/sub2api:v0.1.132
docker image inspect ghcr.io/hcscq/sub2api:v0.1.132 \
  --format '{{index .RepoDigests 0}}'
```

输出会是这种带 digest 的形式：

```text
ghcr.io/hcscq/sub2api:v0.1.132@sha256:...
```

正式上线时尽量使用这个带 digest 的完整引用，避免同名 tag 漂移。

## 本机拉镜像后的 Smoke Test

最小校验可以先确认镜像里的程序版本是对的：

```bash
docker run --rm ghcr.io/hcscq/sub2api:v0.1.132 /app/sub2api -version
```

如果要在当前维护机上做真实发布，本机现有部署目录是 `/root/sub2api-deploy`。推荐步骤：

1. 把 `docker-compose.yml` 里的 `sub2api` 镜像更新为新的 digest 引用
2. 只拉取并重建应用容器

```bash
cd /root/sub2api-deploy
docker compose pull sub2api
docker compose up -d sub2api
docker compose ps sub2api
curl -fsS http://127.0.0.1:8080/health
docker exec sub2api /app/sub2api -version
```

如果不是这台维护机，请把 `/root/sub2api-deploy` 换成实际部署目录。

## 回滚

回滚方式是把部署文件中的镜像引用改回上一个 digest，然后重新拉起容器：

```bash
cd /root/sub2api-deploy
docker compose up -d sub2api
docker compose ps sub2api
curl -fsS http://127.0.0.1:8080/health
```

建议把“上一版 digest”保存在提交记录或运维记录中，不要临时猜。

## 简化发版

`release.yml` 支持 simple release，只构建 x86_64 的 GHCR 镜像并跳过更大的产物集合。

- tag push 路径：读取仓库变量 `SIMPLE_RELEASE`
- workflow_dispatch 路径：读取输入参数 `simple_release`

只有在你明确只想发单架构 GHCR 镜像时才使用。

## 注意事项

- `backend/cmd/server/VERSION` 会在发布成功后由 workflow 自动回写到默认分支；如果你希望本地文件与线上版本一致，等 workflow 完成后再同步一次 `main`。
- tag 正文就是 Release 正文，推送前先写好。
- 如果必须手动触发而不是推 tag，可以使用 GitHub Actions UI，或者用具备 workflow 权限的 token 调 `workflow_dispatch` API。
