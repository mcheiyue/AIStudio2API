# Phase 3：Build App OS 点击生产化 + 双端互斥 + 有头化

日期：2026-09-02 ｜ 分支：main（在 feature/q2q3-hardening 合并后的主线上）

## 背景与已验证事实

1. Build App applet 的 `window.fetch` 被 Google 运行时替换，先 `await bootstrapChannel`；
   bootstrapChannel 只在**真实用户手势（user activation）**后放行。
2. 三种输入判定（2026-09-02 实测，2267 自有 app 7f4818a8）：
   - 合成 dispatchEvent / BiDi `input.performActions` / SendKeys Enter → 全部失败（403 或 0 回包）
   - **OS 级真实鼠标点击（user32 SetCursorPos + mouse_event）→ response_headers 200 + PROBE_OK**（探针 osinput-9）
3. 内存实测（Camoufox 152.0.4-beta.29，2267 applet 会话）：
   - 无头：8 进程 814.8MB；有头：9 进程 854.6MB（+39.8MB，+5%）
   - 大头是 Camoufox 多进程树本身；有头化内存代价可接受
4. Playground（Mag1cFall WAA/NativeWorker）恒为 `Headless: true`（runtime.go:463），
   只做 BotGuard proof + HTTP，**不依赖有头**，本次不动。
5. BuildAppWorker 当前 `buildAppHeadless()` 默认 true（buildapp_worker.go:19-23），
   但已证无头无法建立 user activation → **生产必须改为有头**（Linux 用 Xvfb 虚拟屏）。

## 目标（3 项改造 + 模型测试）

| # | 改造 | 说明 |
|---|------|------|
| 1 | 双端 worker 互斥（内存保护） | build 激活关 play worker；play 激活关 build worker。避免双树 ~1.67GB 在 1C2G 上 OOM |
| 2 | OS 层真实点击生产化 | `internal/buildapp/osclick_windows.go` + `osclick_linux.go`（build tags），请求期自动点击 Launch! |
| 3 | BUILDAPP_HEADLESS 默认 false + Xvfb | worker 默认有头；compose/文档补 Xvfb + xdotool + DISPLAY |

测试：探针用内置 ClickAt 跑 `gemini-3.7-flash` 与 `gemini-3.5-flash`，均需 200 + PROBE_OK。

## 实施（分 commit，每步 go build + go vet + go test）

### C1 worker 保存 Session + Close 完善 + 默认有头
- `buildapp_worker.go`：
  - `NewBuildAppWorker` 保存 `LaunchApplet` 返回的 `*buildapp.Session`（当前丢弃 → Close 无法关浏览器，内存无法回收）
  - `Close()` 增加 `sess.Close()`（关闭 Camoufox 会话）+ `server.Stop()`
  - `buildAppHeadless()` 默认改 **false**（`BUILDAPP_HEADLESS=1/true` 才无头；注释更新）

### C2 OS 点击跨平台实现
- 新增 `internal/buildapp/osclick_windows.go`（`//go:build windows`）：
  - `ClickAt(ctx, viewportX, viewportY)`：user32 `GetClientRect`+`ClientToScreen`（客户区原点）→ `SetCursorPos`+`mouse_event`（LEFTDOWN/UP）
  - 通过 `s.cam` 拿 hwnd：Camoufox 窗口句柄（`FindWindow`/`EnumWindows` 按进程名 "camoufox"）+ 窗口激活（`SetForegroundWindow` 可选）
- 新增 `internal/buildapp/osclick_linux.go`（`//go:build linux`）：
  - `ClickAt`：`xdotool search --name Camoufox getwindowgeometry` 取窗口 id/位置 →
    `xdotool mousemove --window <id> <x> <y> click 1`（相对窗口坐标，浏览器自动处理装饰）
  - 依赖安装：`apt-get install -y xdotool xvfb`；容器入口 `Xvfb :99 & export DISPLAY=:99`
- `Session` 加导出方法 `ClickLaunch(ctx)`：`LocateLaunch` → `ClickAt(CX, CY)`
- 新增 `internal/buildapp/osclick_test.go`（逻辑自检：坐标换算仅依赖平台实现，windows 测试标记短跳过）

### C3 请求期自动点击集成（worker）
- `buildapp_worker.go` `ServeHTTP`：`SubmitRequest` 后起 goroutine：
  - 循环（间隔 1s，至多 30s）：`LocateLaunch` 命中 → `ClickLaunch()` → 退出
  - 与 `PumpTo` 并行，成功回包即停（transports 已有 request_id 关联）
- 探针 `cmd/buildapptest` 加 `-os-click` flag（Go 内置点击，替代 os-click.ps1），
  用于模型测试（3.7/3.5 flash）

### C4 双端互斥
- `AccountPool`（internal/aistudio/accounts.go）：
  - 加 `CloseBuildAppWorker(accountID) error`：关闭并删除 buildapp worker
- `accountWorkerManager`（internal/app/runtime.go）：
  - 加 `ForceEvictAccount(accountID) error`：强制 close 该账号 play worker（参考 `evictIdleWorker`/`closeAccountWorker`，不管 idle）
- 双向接线：
  - build 激活关 play：`runtimeManager.ServeBuildApp`（lifecycle.go:225）入口先 `ForceEvictAccount(accountID)` 再委托
  - play 激活关 build：`ensureWorker` 成功拉起账号 worker 后关对应账号 build worker
    （runtimeManager 持有 pool 引用：给 Service 接口加 `CloseBuildAppWorker`，或 runtimeManager 持有 `*aistudio.AccountPool` —— 实施时以最小改动为准）

### C5 部署与文档
- `compose.deploy.yml`：buildapp 服务/环境加 `BUILDAPP_HEADLESS=false`（或默认）
- 部署说明补：镜像/主机安装 xvfb + xdotool、`Xvfb :99` + `DISPLAY=:99`（Docker 侧 entrypoint 或 systemd）
- 更新 `docs/phase2-buildapp-implementation.md` 的部署段 + `docs/fork-sync-playbook.md`? （不涉及）

### 测试
- 本地（Windows）：`buildapptest -os-click -model gemini-3.5-flash` → 200 + PROBE_OK
- 本地（Windows）：`buildapptest -os-click -model gemini-3.7-flash` → 200 + PROBE_OK
  （若 3.7-flash 报 model not found，向 Google 确认该模型 ID；记录实际错误）
- 回归：`go test ./...`、`go vet ./...`、`git diff --check`

## 部署变更（C5 已实施）

- `Dockerfile`：运行镜像安装 `xvfb` + `xdotool`；ENTRYPOINT 改 `docker/entrypoint.sh`
- `docker/entrypoint.sh`：容器内启动 `Xvfb :99 -screen 0 1680x1050x24`，export `DISPLAY=:99`，
  默认 `BUILDAPP_HEADLESS=false`（有头），再 exec 二进制
- `compose.deploy.yml`：`BUILDAPP_HEADLESS: "false"`；`mem_limit` 900m → 1200m
  （有头树 ~855MB + Xvfb + Go 本体；双端互斥保证同账号只留一棵浏览器树）
- 服务器侧无需额外安装（Xvfb/xdotool 已进镜像；host 网络下 DISPLAY 保持在容器内）

## 风险与备注

- Linux 生产有头需要 Xvfb（无物理屏）；xdotool 依赖 `xdotool` 包与 Xvfb 同机
- `ForceEvictAccount` 可能打断正在进行的 play 请求（降级切换本身是独占流，可接受；切换前 play 已报 429/403）
- worker 启动 160s 超时内含 Camoufox 有头启动；Xvfb 未起时 LaunchApplet 会失败 → C1 起对 `BUILDAPP_HEADLESS=false` 没有 DISPLAY 的场景报错说明
