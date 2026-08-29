# Fork 同步上游（Mag1cFall）操作手册

> 配合 `docs/fork-baseline.md` 使用。目标：拉取 Mag1cFall 新提交并合并到本 fork，
> 同时保留第 4 节列出的 fork 特有补丁，不引入回归。

## 0. 前置确认
- 工作树干净（`git status` 无未提交改动，临时探针/日志已清）。
- 已通读 `fork-baseline.md` 第 3 节，知道哪些文件会与上游冲突。

## 1. 拉全量上游历史（本仓库 upstream/main 是浅拷贝，必须先 fetch）
```bash
git fetch upstream
git fetch upstream --tags
```

## 2. 查看上游相对基点的增量，预判冲突
```bash
# 本 fork 相对上游改了哪些文件（我们的 7 个 commit 的合集）
git diff --stat 4c0f205 HEAD
# 上游自 base 以来的提交
git log --oneline 4c0f205..upstream/main
# 上游碰了哪些文件（与我们的清单求交集即潜在冲突点）
git diff --stat 4c0f205 upstream/main
```

## 3. 合并策略（推荐 merge，保留 fork 历史；如需线性可 rebase）
```bash
git checkout main
git merge upstream/main        # 冲突集中在第 3.2 节的「改上游文件」
```
冲突高风险文件（必须人工看）：
- `internal/aistudio/auth.go` — 比对 SameSite 修复，取「接受任意 SameSite」版本（见 baseline §4.2）。
- `internal/api/gemini.go` — 保留 `mode=buildapp` 分支与 accountID 传递；上游若改了同一函数，手动合入我们的分支。
- `internal/aistudio/accounts.go` — 保留 BuildAppWorker 工厂与 `p.Account()` 统一查找。
- `internal/aistudio/{client,service,types}.go`、`cmd/aistudio2api/runtime.go` — 保留 AccountMode 枚举与 ServeBuildApp 接线。

零冲突文件（直接采纳）：`internal/buildapp/*`、`internal/aistudio/buildapp_worker.go`、`internal/camoufoxnative/session.go`、`Dockerfile` 等（上游无同名文件）。

## 4. 合并后验证（不通过不部署）
```bash
# 本地构建（仅验证编译，发布仍走 GHCR Actions，见规则 #929）
$env:GOPROXY="https://goproxy.cn,direct"; go build ./cmd/aistudio2api
# 路由 + Build App 冒烟：起服务发一次真实 chat，确认 200 + 候选内容
# （参考 fork-baseline.md §5 的启动方式与账号配置）
```

## 5. 收尾
- 更新 `fork-baseline.md` 第 1 节「当前 main tip / base」为新的 merge-base；若上游调整了 base 提交，重算 `git merge-base main upstream/main`。
- 提交合并结果（conventional commit + 中文正文），推送 `origin main`。
- 触发 GHCR Actions 重建镜像（规则 #929：发布只走 Actions，禁止本地/手动 docker build）。

## 6. 回滚预案
若合并后 Build App 链路回归且短时修不好：
```bash
git revert <merge-commit>          # 或 git reset --hard <上一个已知好 main tip>
```
已知的「好基线」：`cae9595`（本文件撰写时的 main tip，Build App 已端到端验证通过）。
