# Todo 4: Build 独立模型与能力目录

执行：Sisyphus 主会话接手（子代理留下连贯目录层半成品 + 一处编译错误；API 层接线由主会话完成）。

## 实现内容

### 目录层（internal/aistudio，子代理半成品 + 修复）
- `buildapp_catalog.go`：
  - `buildAppCatalogCache`：按账号 TTL 缓存（默认 1h）+ 单飞刷新（inflight channel）+ 失败缓存（fail-closed）。
  - `relayBuildAppListModels`：经 `AccountPool.BuildAppWorker` 中继 `GET /v1beta/models`（路径前缀与方法均已被 relay 边界允许）。
  - `parseGoogleListModels`：宽容解析标准 Google ListModels JSON（非 Playground sparse-array 协议）；过滤空名/去重；方法白名单 = 5 个已接入方法（generate/stream/count/embed），不声明任何媒体/Bidi 能力。
  - `CheckBuildAppMethod`：模型存在 + 方法允许校验；`buildAppMethodAllowed` 对 embed/generate 双别名互通。
  - sentinels：`ErrBuildAppCatalogUnavailable`、`ErrBuildAppModelNotAvailable`。
- `types.go`：`BuildAppModel`、`BuildAppCatalogInfo`、`BuildAppCatalog` 可选接口。
- `accounts.go`：AccountPool 挂 `buildappCatalog` 缓存，fetch 回调注入。
- 修复：子代理遗留 `t.fatal` → `t.Fatal`（测试编译失败）。

### API 层（主会话）
- `router.go`：server 增加可选 `buildCatalog aistudio.BuildAppCatalog`（带 ok 断言装配，不破坏测试 stub）。
- `gemini_build_adapters.go`：
  - `checkBuildAppCatalog`：catalog nil → 放行（单元 stub seam）；拉取失败 → 502；模型/方法不允许 → 400。
  - `buildAppCatalogForRequest` + `openAIBuildModelObject`/`geminiBuildModelObject`：models 端点 buildapp 上下文输出。
- `gemini.go`：`handleGeminiAction` 对 buildapp 的 5 个方法统一目录校验；`handleGeminiModels`/`handleGeminiModel` 按 `?account_id=`（buildapp 账号）输出 Build 目录，其余保持 Playground 原路径字节不变。
- `openai.go`：`handleOpenAIModels` 同上分支。
- `handleOpenAIEmbeddings`：接入目录校验（fail-closed）。
- `admin.go`（api + app）：DTO 增加 `build_app_models` / `build_app_catalog_age_seconds`（不可用时 -1）。

## 测试（真实执行）

- aistudio：`TestParseGoogleListModels_standardJSON`（含 bidi/媒体方法泄漏断言）、`_malformed`、`TestCheckBuildAppMethod`（未知模型/空目录/方法拒绝）、`TestBuildAppCatalogCache_ttlAndFailClosed`（TTL 过期重取 + 失败缓存）、`_singleFlight`（8 并发 1 fetch）。
- api：未知模型/错误方法 3 例 400 且 0 worker、目录不可用 502 且 0 worker、无目录接口时旧行为保持（放行 + relay 调用 1 次）、`/v1beta/models?account_id=` 输出 Build 目录、无 account_id 时不触碰 Build 目录（fetches==0）、embeddings 未知模型 400。

## 验证命令与结果

- `go build ./...` → exit 0
- `go vet ./...` → exit 0
- `go test ./... -count=1` → 全部 ok（aistudio 0.356s / api 0.410s / buildapp / camoufoxnative）
- `gofmt -l internal` → 空（顺带修复了 accounts.go/admin.go 的既有格式漂移）

## 设计决策记录

- fail-closed 语义：生产 PooledService 实现 catalog 接口 → 目录拉取失败必然拒绝请求；测试 stub 未实现接口 → 校验跳过（seam 注释说明）。
- 不为 Build 目录开放任何媒体/Bidi 方法声明——白名单仅限已接入的 5 个方法，媒体归 Todo 5 探测后再议。
- models 端点的 buildapp 上下文是显式 opt-in（?account_id= 且该账号 mode=buildapp），Playground 请求零改动。

## 残留风险

- relayed ListModels 的真实响应字段以 applet 实际返回为准（fixture 按标准 Google ListModels JSON 构造；Todo 5/E2E 阶段验证）。
- Build 目录项缺少 Playground 的 capabilities/capability_options/access_modes/paid 字段（Build 侧无此数据源，诚实省略）。

## 提交

`feat(buildapp): track independent model capabilities`
