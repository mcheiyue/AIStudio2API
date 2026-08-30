# Phase 3 实施规划：Q2 服务端硬化 + Q3 Build App 进 WebUI

> 本文档是 Q2（服务端硬化）+ Q3（Build App 配置进 WebUI）的**详细实施规划**，按 commit 拆分，每个 commit 原子、独立可编译、可 `git revert` 回滚。
> 配套代码基线：Q1 合并后 `main` @ `9628c59`（已含上游 `459f2da` + Build App C 路径移植）。

## 0. 分支与前置

- 从 `main` 切 `feature/q2q3-hardening` 分支实施；每个 commit 独立构建通过后再继续；全部完成后由你确认再合并回 `main`。
- 每个 commit 遵循 Conventional Commits 前缀（`feat:` / `fix:` / `docs:`）+ 中文正文，与现有 fork 提交风格一致。
- 全量验证门槛：每个 commit 后 `go build ./...` 必须 exit=0；前端 commit 后 `npm run build`（web 目录）通过。

## 1. 总体 commit 序列（可回溯）

| #   | Commit 主题                                            | 改动范围                                              | 独立回滚     |
| --- | ------------------------------------------------------ | ----------------------------------------------------- | ------------ |
| C1  | `feat(buildapp): Camoufox 无头模式可由 BUILDAPP_HEADLESS 配置` | `internal/aistudio/buildapp_worker.go`                | ✅ git revert |
| C2  | `feat(admin): 账户状态暴露 Build App worker 就绪态`        | `internal/api/admin.go` + `internal/aistudio/accounts.go` + `internal/app/admin.go` | ✅ git revert |
| C3  | `feat(admin): 新增服务重载端点，免重启即重读账号`            | `internal/api/router.go` + `internal/app/admin.go`     | ✅ git revert |
| C4  | `docs(ops): 补 Docker restart 策略，确认无进程自杀`         | `compose*.yml` + `docs/phase2-buildapp-implementation.md` | ✅ git revert |
| C5  | `feat(api): AccountInput 支持 mode 与 build_app_url`       | `internal/api/admin.go`（`CreateAccount`/`UpdateAccount`） | ✅ git revert |
| C6  | `feat(webui): 账户表单支持传输模式与 Build App URL`         | `web/src/api.ts` + `web/src/components/AccountsPanel.vue` + `web/src/i18n.ts` | ✅ git revert |

> 顺序：C1→C4 为 Q2（服务端硬化），C5→C6 为 Q3（WebUI）。C2/C3 互不依赖；C6 依赖 C5 的接口字段。

---

## 2. 详细实施

### Commit C1 — Camoufox 无头模式可配置

**目标**：Build App worker 在无头 VPS 上能拉起 Camoufox（当前 `Headless:false` 硬编码会失败）。

**改动文件**：`internal/aistudio/buildapp_worker.go`（约 line 44）

**具体改动**：
- 当前：`Headless: false,`
- 改为读取环境变量 `BUILDAPP_HEADLESS`，默认 `"true"`（服务端默认无头）；本地 Windows 开发显式设为 `"false"`。
- 建议新增包内 helper（或内联）：
  ```go
  func buildAppHeadless() bool {
      v := strings.ToLower(os.Getenv("BUILDAPP_HEADLESS"))
      if v == "false" || v == "0" {
          return false
      }
      return true // 默认无头，适配服务器部署
  }
  ```
  并将 `Headless: buildAppHeadless(),` 写入 `camoufoxnative.Options`。

**验证**：
- `go build ./...` exit=0。
- 本地设 `BUILDAPP_HEADLESS=false ./aistudio2api ...` 仍能 GUI 调试；VPS 不设该变量默认无头启动 worker。

**回滚**：`git revert <C1>`。

---

### Commit C2 — 账户状态暴露 Build App worker 就绪态

**目标**：CPA / 运维可通过 `GET /api/accounts` 区分 buildapp worker 是 `idle`/`warming`/`ready`/`error`，而非盲目重试。

**改动文件**：
1. `internal/api/admin.go`：`AdminAccount` 结构（line 85-96）新增字段
   ```go
   BuildAppWorker string `json:"build_app_worker,omitempty"` // idle/warming/ready/error，仅 mode=buildapp 有值
   ```
2. `internal/aistudio/accounts.go`：新增方法
   ```go
   // BuildAppWorkerState 返回账号 Build App worker 当前就绪态
   func (p *AccountPool) BuildAppWorkerState(accountID string) string {
       p.mu.RLock(); defer p.mu.RUnlock()
       w, ok := p.buildappWorkers[accountID]
       if !ok { return "idle" }
       return w.State() // 需在 BuildAppWorker 上实现 State()
   }
   ```
   （`BuildAppWorker.State()` 返回 `warming`/`ready`/`error`，依据 `internal/buildapp.Server` 的就绪标志。）
3. `internal/app/admin.go`：在 `Accounts`/`Account` handler 组装 `AdminAccount` 时，对 `account.Config.Mode == AccountModeBuildApp` 的账号填入 `BuildAppWorker: pool.BuildAppWorkerState(account.ID)`。

**验证**：
- `go build ./...` exit=0。
- 起服务后对 mode=buildapp 账号首次发请求前 `GET /api/accounts` 看到 `build_app_worker: "idle"`，发请求后变为 `warming`→`ready`。

**回滚**：`git revert <C2>`。

---

### Commit C3 — 服务重载端点（免全进程重启即重读账号）

**目标**：手改 `auth/` 下 `account.json` 后，不必 kill 进程即可让新账号/模式生效。

**背景证据**：`buildRuntimeGeneration`(lifecycle.go:95) → `newRuntime`(lifecycle.go:102) → `store.Load()`(runtime.go:35-36) 每次构建生成服务都会重读磁盘账号。因此「重启服务」=「重载账号」。

**改动文件**：
1. `internal/api/router.go`：注册管理端点（与现有 `POST /api/accounts` 同保护级）
   ```go
   mux.HandleFunc("POST /api/service/reload", s.handleServiceReload)
   ```
2. `internal/app/admin.go`：实现 `handleServiceReload` —— 调用 `runtimeManager` 的 `StopService` + `StartService`（或现有 management 重启路径），触发 `buildRuntimeGeneration` 重新 `store.Load()`。

**验证**：
- `go build ./...` exit=0。
- 手动新增一个 `account.json` 后 `curl -X POST /api/service/reload`，再 `GET /api/accounts` 出现新账号（无需重启进程）。

**回滚**：`git revert <C3>`。

---

### Commit C4 — Docker restart 策略文档 + 确认无进程自杀

**目标**：用进程管理器兜底，替代本地调试用的 600s 自杀 hack。

**背景证据**：600s 自杀定时器原本写在旧的 `cmd/aistudio2api/runtime.go`；Q1 合并时该文件被上游删除（`git rm`），`main.go:11` 仅 `os.Exit(app.Run(os.Args[1:]))`，**当前已无自杀逻辑**。本 commit 仅做收尾文档化。

**改动文件**：
1. `compose.yml` / `compose.deploy.yml`（如存在）：为 aistudio2api 服务补
   ```yaml
   restart: unless-stopped
   ```
2. `docs/phase2-buildapp-implementation.md`：在「本地启动」段补充「生产用 Docker/systemd 托管，不在代码内自杀」。

**验证**：`docker compose config` 校验 YAML 合法（若本地有 docker）。

**回滚**：`git revert <C4>`。

---

### Commit C5 — AccountInput 支持 mode 与 build_app_url（后端）

**目标**：管理 API 可创建/更新 buildapp 账号，不再只能手改 `account.json`。

**背景证据**：`AccountInput`（admin.go:99-105）当前仅 `Label/Enabled/Proxy/Locale/Timezone`；后端 `AccountConfig` 已有 `Mode` 与 `BuildAppURL`(accounts.go:81) 字段。

**改动文件**：`internal/api/admin.go`
1. `AccountInput` 结构新增：
   ```go
   Mode        string `json:"mode"`                     // playground | buildapp
   BuildAppURL string `json:"build_app_url,omitempty"`  // mode=buildapp 时的 applet 地址
   ```
2. `CreateAccount`(line 136) 与 `UpdateAccount`(line 197)：在映射 `accountConfig` 处补
   ```go
   accountConfig.Mode = input.Mode
   accountConfig.BuildAppURL = input.BuildAppURL
   ```
   （注意：`Mode` 空时回退默认 `playground`，与 `EffectiveMode()` 行为一致。）

**验证**：
- `go build ./...` exit=0。
- `POST /api/accounts` 带 `{"mode":"buildapp","build_app_url":"https://aistudio.google.com/apps/xxxx"}` 创建账号，`GET /api/accounts` 该账号 `mode` 正确。

**回滚**：`git revert <C5>`。

---

### Commit C6 — 账户表单支持传输模式与 Build App URL（前端）

**目标**：WebUI 自助建 buildapp 账号。

**改动文件**：
1. `web/src/api.ts`：`AccountInput` 类型同步加 `mode?: string; build_app_url?: string;`
2. `web/src/components/AccountsPanel.vue`：新建账号表单增加
   - 「传输模式」下拉：`playground` / `buildapp`
   - 当选 `buildapp` 时显示「Build App URL」输入框（placeholder 提示 fork 自己的 app 实例地址）
3. `web/src/i18n.ts`：补 `mode` / `build_app_url` / `buildAppHint` 等中英文本

**验证**：
- web 目录 `npm run build` 通过（或 `vue-tsc` 类型检查无错）。
- 手动：UI 建一个 mode=buildapp 账号，提交后后端 `GET /api/accounts` 可见。

**回滚**：`git revert <C6>`。

---

## 3. 验收总览

| 能力                         | 验证方式                                              |
| ---------------------------- | ----------------------------------------------------- |
| Build App worker 无头启动    | VPS 不设 `BUILDAPP_HEADLESS`，worker 起得来（C1）      |
| buildapp 就绪态可见          | `GET /api/accounts` 返回 `build_app_worker` 字段（C2） |
| 账号热加载                   | `POST /api/service/reload` 后新账号生效（C3）         |
| 无进程自杀 + 容器自愈        | compose `restart: unless-stopped`（C4）               |
| API 建 buildapp 账号         | `POST /api/accounts` 带 mode/build_app_url（C5）      |
| UI 建 buildapp 账号          | 前端表单提交成功（C6）                                |

## 4. 风险与备注

- **C2 的 `BuildAppWorker.State()`**：需在 `internal/buildapp` 的 Worker/Server 上补一个就绪态读取方法（如基于 `server.ready` 原子标志），实现量极小；落地时若发现 `BuildAppWorker` 未暴露状态，则在该 commit 内一并补。
- **C3 重启会短暂中断在飞请求**：重载是「停服→重读→起服」，与上游 `StopService/StartService` 语义一致；若需零中断，可改为仅重建 pool 的 buildapp worker 子集（超出本规划，留作后续优化）。
- **C6 前端构建**：若仓库 web 未配 `npm` 构建链路，至少保证 `vue-tsc` 类型检查通过；纯 UI 文案不影响后端。
- 所有 commit 均不触碰 `internal/buildapp` 核心传输逻辑（C 路径已端到端验证），仅在其外围加配置/状态/接线。
