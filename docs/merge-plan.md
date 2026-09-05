# 合并规划：两个上游仓库同步

> 本文档是合并操作的唯一入口。替代散落在 `fork-baseline.md` / `fork-sync-playbook.md` / `ref-ibuhub-baseline.md` 的旧内容。
> 所有基线 commit、diff 矩阵、冲突风险和回滚方案均在此维护。

---

## 1. 仓库与基线

| 仓库 | 角色 | 本地路径 | 基线 commit | 当前 tip | 变更规模 |
|------|------|----------|-------------|----------|----------|
| **Mag1cFall/AIStudio2API** | 主上游（fork 来源） | `D:\OpenCode\2API\AIStudio2api\repo\AIStudio2API` | `4c0f205`（fork 起点） | `422c753` | +16,431/-3,631 行，81 文件 |
| **iBUHub/AIStudioToAPI** | Build App 协议来源 | `D:\OpenCode\2API\AIStudio2api\repo\AIStudioToAPI` | `3ff4b60` | `db624c2` | +22/-2 行，3 文件 |
| **mcheiyue/AIStudio2API** | 本 fork | 同上 | `bf03a6c`（合并前稳定点） | `4c8fcf1` | 已合并 Mag1cFall `422c753` |

> ⚠️ 两个上游仓库在本地均为**浅克隆**。合并前必须 `git fetch` 拉全量历史。

---

## 2. Mag1cFall 上游自基线的变更

### 2.1 提交列表（`4c0f205` → `422c753`）

| # | Commit | 主题 | 关键文件 |
|---|--------|------|----------|
| 1 | `d18b95d` | normalize SameSite cookie attributes | `camoufoxnative/` |
| 2 | `9a3dd8e` | merge PR #16（camoufoxnative 同上） | 合并 commit |
| 3 | `459f2da` | **完善多协议代理与账户池运行时** | `runtime.go`（+3254）、`service.go`（+411） |
| 4 | `1242478` | **通过 Camoufox 发送受保护请求** | `service.go` 重构 Protected 路径 |
| 5 | `1f73736` | 完善账户导入与浏览器登录 | `login_native.go`（新增）、`admin.go` |
| 6 | `94bef3f` | 正确处理输入拒绝并优化多图请求 | `generate.go`、`upload.go` |
| 7 | `eee87c7` | 正确归因并持久化额度冷却 | `quota.go`（新增） |
| 8 | `422c753` | 按模型支持范围映射思考档位 | `service.go`、`types.go` |

### 2.2 文件分类（自基线 `4c0f205`）

#### 仅上游新增（4 文件，我们直接采纳）
| 文件 | 说明 |
|------|------|
| `internal/aistudio/decoder.go` | 流式解析器 |
| `internal/aistudio/login_native.go` | 浏览器登录流程 |
| `internal/aistudio/quota.go` | 额度冷却持久化 |
| `internal/camoufoxnative/protected.go` | Protected 请求能力 |

#### 仅我们新增（34 文件，上游无同名，merge 零冲突）
| 分类 | 文件 |
|------|------|
| Build App 核心 | `internal/buildapp/{session,transport,ws,applet_state,launchhit,trusted_click,osclick_*}.go` |
| Build App 接线 | `internal/aistudio/{buildapp_worker,buildapp_events}.go`、`internal/api/buildapp_body.go` |
| Camoufox 扩展 | `internal/camoufoxnative/{session,types,window_actions,pointer_actions}.go` |
| 探针/CI | `cmd/buildapptest/`、`Dockerfile`、`.github/workflows/`、`compose.deploy.yml` |
| 文档 | `docs/phase3-*.md`、`docs/phase4-*.md`、`docs/fork-*.md`、`docs/ref-*.md` |

#### 两边都改（73 文件）= 潜在冲突区
见 §3。

---

## 3. 冲突矩阵（73 文件 × 风险分级）

### 3.1 高风险（我们显式改了核心路由/接口 + 上游也改了同函数）

| 文件 | 我们的改动 | 上游的改动 | 合并策略 |
|------|-----------|-----------|----------|
| `internal/api/openai.go` | Phase 4 D4：`chatRequest` + `AccountID`，`handleChatCompletions` 加 buildapp 分支 | 新增 `N`/`ParallelToolCalls`/`Logprobs`/`LogitBias` 字段；`shouldWriteRequestError()` 替换 `errors.Is` | **手动合并**：我们的 buildapp 分支插入点在 `toGenerateRequest` 之后、`Generate` 之前；上游的新字段在校验层。函数内不同区域，无逻辑互斥，但需确认插入位置。 |
| `internal/api/responses.go` | Phase 4 D5：`responsesRequest` + `AccountID`，`handleResponses` 加 buildapp 分支 | +`Store` 字段；`ParallelToolCalls`/`Truncation` 校验；`shouldWriteRequestError()` | 同上 |
| `internal/api/anthropic.go` | Phase 4 D6：`anthropicRequest` + `AccountID`，`handleAnthropicMessages` 加 buildapp 分支 | `anthropicTool.UnmarshalJSON`；流式重构用 `anthropicStreamWriter`；移除 `CountTokens` 预调用 | 同上。上游流式重构幅度大，需仔细对齐我们的 buildapp 分支位置。 |
| `internal/aistudio/service.go` | Phase 4 D3b：加 `BuildAppService` 接口 | `ProtectedPreparer` 加 `SendProtected()`；`Worker` 签名加 `modelID`；新增 `RefreshAccountModels()` | 我们加的是独立接口（不改现有 Service），上游改的是现有接口方法。**低冲突**但需确认编译兼容。 |
| `internal/app/runtime.go` | Phase 4：`trackedService` 加 `ServeBuildAppEvents` 委托 | +3254 行全量重写（运行时拆分） | **最大风险**。上游几乎重写了整个 runtime.go。我们的改动集中在 `trackedService` 方法，可能被上游重写覆盖。需手动合入。 |

### 3.2 中风险（双方改了同文件但不同函数/区域）

| 文件 | 我们 | 上游 | 预判 |
|------|------|------|------|
| `internal/api/router.go` | +`buildAppService` 字段 + 类型断言 + 路由注册 | 删除 `publicModels`；新增 files/transcriptions/live/robotics 路由 | 不同位置，自动合并大概率成功 |
| `internal/api/admin.go` | 无直接改动（Phase 4 不涉及） | +账户导入、浏览器登录 handler | 自动合并 |
| `internal/aistudio/accounts.go` | C4 双端互斥：`CloseBuildAppWorker`/`ForceEvictAccount` | 账户池重构 | 需确认字段位置 |
| `internal/aistudio/types.go` | 无直接改动（Phase 4 只读现有类型） | +`GoogleSearchOptions`/`TranscriptionConfig`/`Event.Transcript` | 自动合并 |
| `internal/app/admin.go` | D1：`UpdateAccount` 加 Mode/BuildAppURL 守卫 | 账户导入 handler | 不同函数 |
| `web/src/*` | C6：WebUI mode 表单 | WebUI 新功能 | 需检查组件结构是否变化 |

### 3.3 低风险（双方改了同文件但改动区域完全不重叠）

其余 ~60 个文件。大多是：
- 上游加新功能（transcribe/video/live），我们没碰
- 我们加 buildapp 探针/部署文件，上游没碰
- 双方都改了 import 或 helper，不互斥

---

## 4. iBUHub 自基线的变更

### 4.1 提交列表（`3ff4b60` → `db624c2`）

| # | Commit | 主题 | 文件 | 影响 |
|---|--------|------|------|------|
| 1 | `812aca9` | fix: defer Playwright proxy module loading | `scripts/auth/setupAuth.js`（3 行） | **不涉及**协议层 |
| 2 | `cb810c9` | feat: add Gemini 3.8 Flash model | `configs/models.json`（+19 行） | 仅模型列表 |
| 3 | `db624c2` | release: v1.3.7 | `package.json`（版本号） | 无功能变更 |

### 4.2 协议文件状态

| 文件 | 状态 |
|------|------|
| `src/core/ProxyServerSystem.js` | **零改动** |
| `src/core/RequestHandler.js` | **零改动** |
| `src/core/BrowserManager.js` | **零改动** |

**结论**：iBUHub 无协议变更，我们的 Go 移植（`internal/buildapp/*`）仍然对齐，无需同步。

---

## 5. 合并计划

### 阶段 A：Mag1cFall 合并（主上游，已完成）

**前置条件**：
- 工作树干净（当前 ✓，仅 `.codegraph/` 未跟踪）
- `git fetch upstream` 拉全量（已完成 ✓）

**步骤**：

1. **创建合并分支**：
   ```bash
   git checkout -b merge/upstream-422c753 main
   ```

2. **执行 merge**：已完成。隔离分支 `merge/upstream-422c753` 产生合并提交 `f94573f`，随后已合回 `main`，主线合并提交为 `4c8fcf1`。
   ```bash
   git merge upstream/main
   ```
   实际冲突为 2 个文件：`internal/app/admin.go`、`web/src/components/AccountsPanel.vue`。

3. **逐文件解决冲突**：已完成。保留了上游新的浏览器登录/Chrome 导入账户流程，以及 fork 的 `mode`/`build_app_url` 编辑链路。
   合并后发现 fork 新增的通用 Build App Session 引用了不存在的 `browserMajor`，已改为复用同包现有的 `camoufoxFirefoxMajor` 常量；这是唯一额外兼容修正。

4. **构建验证**：已完成：
   ```bash
   go build ./... && go vet ./... && go test ./...
   ```

5. **合并回 main**：已完成，主线提交为 `4c8fcf1`。
   ```bash
   git checkout main && git merge --no-ff merge/upstream-422c753
   ```

6. **更新基线文档**：本次已完成状态更新。旧文档暂不删除，避免丢失历史和操作细节；后续以本文档为唯一同步入口。

**回滚基线**：`bf03a6c`（合并前的 main tip，Phase 4 全部已提交）

### 阶段 B：iBUHub 协议确认（已完成）

**无需吸收代码**。从 `3ff4b60` 到 `db624c2` 的 3 个提交只修改 `setupAuth.js`、`configs/models.json` 和 `package.json`；`ProxyServerSystem.js`、`RequestHandler.js`、`BrowserManager.js` 零改动，因此我们的 Go 移植协议不需要调整。

已将 `db624c2` 记录为当前参考 tip。`AIStudioToAPI` 工作区中的 `BrowserManager.js` 本地修改和探针/日志文件均未纳入主 fork。

---

## 6. 现有文档整合

合并完成后，以下文档的旧内容由本文档替代：

| 旧文档 | 处理 |
|--------|------|
| `docs/fork-baseline.md` | 保留历史记录；当前 tip 更新为 `4c8fcf1` |
| `docs/fork-sync-playbook.md` | 保留历史操作记录；同步以本文档为准 |
| `docs/ref-ibuhub-baseline.md` | 保留协议字段细节；参考 tip 更新为 `db624c2` |
| `docs/phase3-*.md` | 保留（实施记录，非操作手册） |
| `docs/phase4-*.md` | 保留（实施记录） |

---

## 7. 风险与备注

1. **runtime.go 是最大风险**：上游 +3254 行全量重写，我们的改动集中在 `trackedService` 的一个方法。需先读上游新结构再合入。
2. **不改变我们的 Build App 核心**：`internal/buildapp/*` 是纯新增文件，merge 零冲突。上游不可能覆盖。
3. **不改变 iBUHub 协议**：协议文件零改动，无需同步。
4. **Phase 4 的 D4-D6 路由分支是自包含的**：它们在 handler 函数内插入 if 分支，不改函数签名。即使上游在同一函数加了新校验，只要插入位置不重叠，自动合并即可。
5. **真实冒烟暂未在本次合并中执行**：本次已完成 Go `build`、`vet`、`test` 和 WebUI `npm run build`；Build App/Playground 真实端点回归需作为后续部署前检查，不把静态验证误报为线上通过。

## 8. 本次执行记录

- Mag1cFall 合并分支：`merge/upstream-422c753`
- Mag1cFall 合并提交：`f94573f`
- 主线合并提交：`4c8fcf1`
- 合并前回退点：`bf03a6c`
- Mag1cFall 实际冲突：`internal/app/admin.go`、`web/src/components/AccountsPanel.vue`
- 额外兼容修正：`internal/camoufoxnative/session.go` 复用 `camoufoxFirefoxMajor`
- iBUHub 参考 tip：`db624c2`；协议核心文件无变更，未吸收其代码
- 验证：`go build ./...`、`go vet ./...`、`go test ./...`、`web/npm run build` 均通过

## 9. 合并后能力补齐（plan `buildapp-capability-parity`）

合并 Mag1cFall `422c753` 后，按 iBUHub 能力基线补齐 Build App 的 HTTP 适配缺口，并建立独立 Build 目录。最终能力边界见 [`buildapp-capability-matrix.md`](./buildapp-capability-matrix.md)。

| commit | 内容 |
|--------|------|
| `0b7893a` | relay 二进制安全 `body_b64` + 方法/路径白名单边界 |
| `d369ccf` | native `countTokens`/`embedContent`/`batchEmbedContents` Build 路由 |
| `f9d136c` | OpenAI `/v1/embeddings` + 文件上传/引用 Build 路由 |
| `975646b` | 独立 Build 模型/能力目录（TTL 缓存 + 单飞 + fail-closed）+ 校验接线 + admin DTO |
| `a7ce6a9` | 兼容端点/工具/Live·Robotics 隔离回归测试 |
| `ca6ccd6` | 修：runtimeManager 漏转发 SABE/catalog（启动 panic） |
| `673c418` | 修：trackedService 漏转发 catalog（目录 3ms 秒 502） |
| `a84f30e` | 修：worker ServeHTTP 对 GET nil body 未防护（panic） |
| `26cc6ac` | 修：LocateLaunch 假阳性致 Launch! 点击循环提前退出 |
| `438ecac` | 活体证据：Build 目录 200 + 音频/TTS 200（PCM） |

- 活体验证（2267 自有 app `7f4818a8`，Camoufox headed + SOCKS 7897）：`GET /v1beta/models?account_id=` → 200 真实 4 模型目录；`gemini-2.5-flash-preview-tts:generateContent` → 200 音频 PCM。
- 图片/视频/Live/Robotics 明确不在 Build 范围（前者目录不含且用户排除，后者属独立 Bidi 协议）。
- 这些修复是**上游合并引入的运行时回归**（`ca6ccd6`/`673c418`）与既有 Build 代码缺陷（`a84f30e`/`26cc6ac`），单测未覆盖、仅真实启动/请求暴露；后续同步上游后必须跑一次真实 Build 冒烟。
