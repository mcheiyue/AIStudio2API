# AIStudio2API Fork 代码基线与引入提交梳理

> 用途：记录本 fork（mcheiyue/AIStudio2API）相对上游（Mag1cFall/AIStudio2API）引入的提交，
> 作为当前 fork 的代码基线。后续同步上游更新时，可据此快速定位「我们改了哪些文件、哪些会与上游冲突」。

## 1. 仓库与基线

| 项 | 值 |
|----|----|
| 上游 remote | `upstream` = https://github.com/Mag1cFall/AIStudio2API.git |
| 本 fork remote | `origin` = https://github.com/mcheiyue/AIStudio2API.git |
| Fork 起点（base） | `4c0f205` — Mag1cFall 全量源码快照（提交信息仅写 `LICENSE`，但树含完整 Mag1cFall 源码：cmd/ internal/ go.mod 等 76 个 .go 文件） |
| 当前 main tip | `cae9595` |
| main 提交总数 | 8（1 个 base + 7 个我们引入） |

> ⚠️ 本仓库内 `upstream/main` 为浅拷贝（本地只取到 tip `4c0f205`）。真正同步前必须先
> `git fetch upstream` 拉取 Mag1cFall 全量历史，否则 merge-base 信息不足。

## 2. 我们引入的 7 个提交（diff 基线）

| # | Hash | 类型 | 主题 | 改动文件 | 性质 |
|---|------|------|------|----------|------|
| 1 | `935399c` | ci | add GHCR docker build pipeline, linux binary artifact and deploy compose | `.dockerignore`, `.github/workflows/build.yml`, `Dockerfile`, `compose.deploy.yml` | **fork 新增**（上游无） |
| 2 | `c014143` | fix | accept non-standard SameSite values in storage state validation | `internal/aistudio/auth.go` | 改上游文件 |
| 3 | `664f67e` | feat | 路由接入 buildapp 传输层并与 Playground 并存 | `cmd/aistudio2api/runtime.go`, `cmd/buildapptest/main.go`, `internal/aistudio/accounts.go`, `internal/aistudio/buildapp_worker.go`, `internal/aistudio/client.go`, `internal/aistudio/service.go`, `internal/aistudio/types.go`, `internal/api/gemini.go`, `internal/buildapp/session.go`, `internal/buildapp/transport.go`, `internal/buildapp/ws.go`, `internal/camoufoxnative/session.go` | 混合：buildapp/* 与 buildapp_worker.go / camoufoxnative/session.go / cmd/buildapptest 为**新增**；其余改上游文件 |
| 4 | `828b994` | fix | 修正账号查找与路由分支的 accountID 传递及请求体 | `internal/api/gemini.go`, `internal/aistudio/accounts.go` | 改上游文件 |
| 5 | `7ce3956` | fix | 转发请求剥离 key/Content-Length 等破坏代理的头部 | `internal/buildapp/transport.go` | 改 fork 新增文件 |
| 6 | `e077cc1` | fix | Camoufox 经 BUILDAPP_PROXY 出口并对 localhost 豁免 | `internal/aistudio/buildapp_worker.go` | 改 fork 新增文件 |
| 7 | `cae9595` | fix | 导航与 Preview 激活加重试直至 WS 连上 | `internal/buildapp/session.go` | 改 fork 新增文件 |

## 3. 文件分类（同步上游时的冲突风险定位）

### 3.1 纯新增文件（上游无同名文件，merge 几乎零冲突，风险低）
- `internal/buildapp/session.go` — applet 托管 + 导航 + Preview 激活 + 重试
- `internal/buildapp/transport.go` — proxy_request 构造 + 响应泵送
- `internal/buildapp/ws.go` — 9998 WS 中继服务端
- `internal/aistudio/buildapp_worker.go` — 账号级 Build App worker
- `internal/camoufoxnative/session.go` — 通用 Session（StartSession/Navigate/Evaluate/FindFrame/AddInitScript）
- `cmd/buildapptest/main.go` — 调试探针（非生产，可删）
- `Dockerfile` / `.github/workflows/build.yml` / `compose.deploy.yml` / `.dockerignore` — CI/部署（fork 特有）

### 3.2 修改上游文件（上游若动同一文件会冲突，需人工 reconcile，风险中）
- `internal/aistudio/auth.go` — `c014143` 放宽 SameSite 白名单（上游可能已有自己的修复，需比对）
- `internal/api/gemini.go` — `664f67e`+`828b994` 加 `mode=buildapp` 分支 + accountID 传递 + 极简请求体转发
- `internal/aistudio/accounts.go` — `664f67e`+`828b994` 加 BuildAppWorker 工厂 + 统一走 `p.Account()`
- `internal/aistudio/client.go` / `service.go` / `types.go` — `664f67e` 加 `AccountMode` 枚举与 `ServeBuildApp` 接线
- `cmd/aistudio2api/runtime.go` — `664f67e` 初始化 camoufox 路径与 WS 基端口

## 4. 必须保留的 fork 特有补丁（同步后不得被上游覆盖）

1. **Build App C 路径全部**（`internal/buildapp/*` + `buildapp_worker.go` + `camoufoxnative/session.go`）：上游 Mag1cFall 无此功能，属纯增量，合并安全但删除即丢能力。
2. **SameSite 放宽**（`c014143`）：上游若未修同 bug，必须保留；若上游已修，以「接受任意 SameSite」为准（更稳）。
3. **CI/GHCR 管线**（`935399c`）：fork 发布只走 GitHub Actions（见项目规则 #929），上游无对应物。
4. **路由分支**（gemini.go / accounts.go / service.go / types.go / runtime.go）：本 fork 的核心差异点，合并后需回归验证 `mode=buildapp` 与 Playground 并存。

## 5. 当前验证状态（基线锁定时的实测）

- 2267 账号（Pro）走 Build App（`mode=buildapp` + 自有 fork app `7f4818a8`）端到端返回 `200 OK` + 真实 gemini-2.5-flash 回复。
- 冷启动首次调用 Google 约 155s（accountID 首次 Build App 调用慢），热 worker 约 8s。
- 运行时需 `BUILDAPP_PROXY=socks5://127.0.0.1:7897`（本地 Clash mixed 端口）+ `ProxyBypass=127.0.0.1,localhost`。

## 6. 参考项目（外部，非 fork 代码依赖）

grilling 期间对比过的其他 3 个 AI Studio 2API 代理。**仅 iBUHub 有代码级协议依赖**（我们的 Build App C 路径是其 `ProxyServerSystem` 的 Go 重写）；其余两者仅概念参考、无代码复用，无需各自标基线。

| 项目 | 上游 | 基线 commit | 参考价值 | 基线文档 |
|------|------|-------------|----------|----------|
| iBUHub / AIStudioToAPI | iBUHub/AIStudioToAPI | `3ff4b60` | **高**：Build App 协议来源，C 路径直接移植 | [docs/ref-ibuhub-baseline.md](./ref-ibuhub-baseline.md) |
| chrysoljq / aistudio-api | chrysoljq/aistudio-api | `f2c51b8` | 低：capture/replay 模板架构，仅对比未复用 | 无（仅此处一行出处） |
| CJackHwang / AIstudioProxyAPI | CJackHwang/AIStudioProxyAPI | `044c3db` | 低：proxy_server，仅登录/会话模式对比，fork 已原生支持 | 无（仅此处一行出处） |

> 三者本地均为浅克隆（仅 1 个 tip commit），同步前需 `git fetch` 拉全量。iBUHub 的协议细节见其专属基线文档；另两者若日后要复用某特性，再补基线。
