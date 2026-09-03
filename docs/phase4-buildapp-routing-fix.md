# Phase 4：Build App 路由修复与兼容端点接入

## 背景

2026-09-03 核实两个缺口：

1. **UpdateAccount 回归**：`internal/app/admin.go` 的 `UpdateAccount` 用 `DefaultAccountConfig` 重建配置，不映射 `input.Mode`/`input.BuildAppURL`（C5 曾加守卫，upstream merge 后丢失）。WebUI 编辑 buildapp 账号会重置 mode 回 playground + 清空 build_app_url。

2. **OpenAI/Anthropic 端点不路由 buildapp**：`openai.go`/`responses.go`/`anthropic.go` 三个 handler 无 accountID 字段、无 buildapp 分支。OpenAI/Anthropic 格式客户端调 buildapp 账号报「没有符合条件的 AI Studio 账户」。原版 iBUHub 三格式统一走 Build App 中继。

---

## 代码现状（2026-09-03 逐文件核实）

### 数据结构

```go
// internal/aistudio/types.go L220-228
type GenerateRequest struct {
    ID        string           `json:"id"`
    Model     string           `json:"model"`
    System    string           `json:"system,omitempty"`        // native Gemini: systemInstruction
    Contents  []Content        `json:"contents"`                // native Gemini: contents ✓
    Config    GenerationConfig `json:"config,omitempty"`        // native Gemini: generationConfig
    Tools     Tools            `json:"tools,omitempty"`         // native Gemini: tools ✓
    AccountID string           `json:"account_id,omitempty"`    // 本地路由字段，需剥除
}
// 注意：JSON tag 与 native Gemini API 不匹配（System→systemInstruction, Config→generationConfig）

// internal/aistudio/types.go L337-353
type Event struct {
    Kind                EventKind  `json:"kind"`
    Text                string     // 正文增量
    ToolCall            *FunctionCall
    Usage               *Usage
    FinishReason        string
    ThoughtSignature    string
    ProviderModel       string
    Err                 error      `json:"-"`
    // + ExecutableCode/CodeExecutionResult/Grounding/Citation/Media/Transcript
}

// EventKind 枚举 (L309-334)
//   text / reasoning / tool_call / executable_code / code_execution_result
//   grounding / citation / media / thought_signature / usage / finish / error
```

### Service 接口

```go
// internal/aistudio/types.go L355-364
type Service interface {
    Models(context.Context) ([]Model, error)
    CountTokens(context.Context, TokenCountRequest) (TokenCount, error)
    Generate(context.Context, GenerateRequest) (<-chan Event, error)
    AccountMode(accountID string) string
    ServeBuildApp(ctx context.Context, rw http.ResponseWriter, r *http.Request, accountID string) error
}
```

### Build App 当前输出路径（HTTP 反代，非 Event 流）

```go
// internal/buildapp/transport.go L29
func (t *Transport) SubmitRequest(r *http.Request, body []byte) (string, <-chan AppletMessage, error)
//   返回 reqID + AppletMessage chan

// internal/buildapp/transport.go L76
func (t *Transport) PumpTo(w http.ResponseWriter, ch <-chan AppletMessage, reqID string)
//   遍历 AppletMessage，按 EventType 写 HTTP ResponseWriter：
//     "response_headers" → WriteHeader(msg.Status) + headers
//     "chunk"             → w.Write([]byte(msg.Data))   ← native Gemini JSON 片段直写
//     "error"             → w.Write(msg.Message) + return
//     "stream_close"      → return

// internal/buildapp/ws.go L33
type AppletMessage struct {
    EventType string  `json:"event_type"`
    Status    int     `json:"status"`
    Headers   map[string]string `json:"headers"`
    Data      string  `json:"data"`        // native Gemini JSON（非流式完整 / 流式 SSE 增量）
    Message   string  `json:"message"`
}

// internal/aistudio/service.go L77-84
func (s *PooledService) ServeBuildApp(ctx, rw, r, accountID) error {
    worker, _ := s.pool.BuildAppWorker(ctx, accountID)
    worker.ServeHTTP(rw, r)   // ← 直接 HTTP 反代，native Gemini JSON 直写 ResponseWriter
    return nil
}
```

### 兼容端点当前流程（Playground only）

```go
// internal/api/openai.go
type chatRequest struct { ... 无 accountID ... }
handleChatCompletions: decodeJSON → request.toGenerateRequest(id) → s.service.Generate(ctx, req) → events
  → 非流式: consumeEvents(ctx, events, nil) → buildChatCompletion(...)
  → 流式:   streamChatCompletion(w, r, request, requestID, created, events)

// internal/api/responses.go
type responsesRequest struct { ... 无 accountID ... }
handleResponses: decodeJSON → request.toGenerateRequest(id) → s.service.Generate(ctx, req) → events
  → 非流式: consumeEvents → buildResponses(...)
  → 流式:   streamResponses(...)

// internal/api/anthropic.go
type anthropicRequest struct { ... 无 accountID ... }
handleAnthropicMessages: decodeJSON → request.toGenerateRequest(id) → s.service.Generate(ctx, req) → events
  → 非流式: consumeEvents → buildAnthropicResponse(...)
  → 流式:   streamAnthropic(...)

// internal/api/generate.go L103
func consumeEvents(ctx, events <-chan Event, emit func(Event) error) (generationResult, error)
//   统一消费 chan Event，按 Kind 分类累积 result

// native Gemini 路径（参照，已接 buildapp）
// internal/api/gemini.go
type geminiRequest struct { ... 有 accountID ... }
handleGeminiAction: rawBody → accountID 路由 → handleGeminiBuildApp → ServeBuildApp（直写 native Gemini）
```

### 两端数据流对比

```
Playground:
  客户端 → toGenerateRequest → GenerateRequest
  → service.Generate → chan Event（MakerSuite RPC 解析为结构化事件）
  → consumeEvents / streaming writer → OpenAI/Anthropic 格式

Build App（当前，native Gemini only）:
  客户端 native Gemini → prepareBuildAppGeminiBody → raw body
  → ServeBuildApp → worker.ServeHTTP → PumpTo → AppletMessage.Data 直写 ResponseWriter
  （native Gemini JSON，不经 Event 层）
```

### 鸿沟

OpenAI/Anthropic 端点要接 buildapp，需要：
1. 把 OpenAI/Anthropic 请求转成 native Gemini body（GenerateRequest → native Gemini JSON）
2. 把 buildapp 的 native Gemini 响应解析成 `chan Event`（AppletMessage → Event）
3. 复用现有 `consumeEvents` / streaming writer 做 Event → OpenAI/Anthropic 格式转换

---

## 方案

### D1：修复 UpdateAccount Mode/BuildAppURL 映射

**文件**：`internal/app/admin.go`

**改动**：`UpdateAccount`（L~563）在 `accountConfig.Timezone` 之后、`Validate` 之前加两行守卫，与 `CreateAccount`（L~517-524）完全对齐：

```go
if mode := strings.TrimSpace(input.Mode); mode != "" {
    accountConfig.Mode = mode
}
if url := strings.TrimSpace(input.BuildAppURL); url != "" {
    accountConfig.BuildAppURL = url
}
```

**验证**：go build + vet + test；POST /api/accounts/{id} 更新 enabled=true → GET 确认 mode=buildapp 未丢。

---

### D2：GenerateRequest → native Gemini body 转换函数

**文件**：`internal/api/buildapp_body.go`

**新增函数**：

```go
// buildAppBodyFromGenerateRequest 把内部 GenerateRequest 转成 native Gemini API JSON body。
// 字段映射：
//   System   → systemInstruction.parts[0].text
//   Contents → contents（role + parts，保留 functionCall/functionResponse/text）
//   Tools    → tools[0].functionDeclarations[]（schema 类型大写 OBJECT/STRING）
//   Config   → generationConfig（temperature/topP/maxOutputTokens 等）
// 剥除 account_id（本地路由字段）。
func buildAppBodyFromGenerateRequest(req aistudio.GenerateRequest) ([]byte, error)
```

**实现要点**：
- 复用 `normalizeBuildAppSchemaTypes`（已有，L~140）做 schema 类型大写
- 复用 `ensureBuildAppThoughtSignatures`（已有）做 functionResponse 回合补 thoughtSignature
- `generationConfig` 字段名映射：`Config.Temperature` → `temperature`、`Config.TopP` → `topP`、`Config.MaxOutputTokens` → `maxOutputTokens` 等
- `systemInstruction` 格式：`{"parts":[{"text":"..."}]}`

**单测**：`buildapp_body_test.go` 加 `TestBuildAppBodyFromGenerateRequest`——构造含 tools + systemInstruction + contents 的 GenerateRequest，断言输出的 native Gemini body 字段名和 schema 大写正确。

---

### D3：native Gemini JSON → Event 解析器 + GenerateBuildApp 接口

这是核心工作量。分两步：

#### D3a：native Gemini JSON → Event 解析器

**文件**：`internal/buildapp/gemini_event.go`（新建）

**新增**：

```go
// parseGeminiResponse 把非流式 native Gemini 响应 JSON 解析成 []Event。
// 输入示例：
//   {"candidates":[{"content":{"parts":[{"text":"..."}],"role":"model"},
//                    "finishReason":"STOP"}],
//    "usageMetadata":{"promptTokenCount":67,"candidatesTokenCount":16,"totalTokenCount":124}}
func parseGeminiResponse(data []byte) ([]aistudio.Event, error)

// parseGeminiStreamChunk 把流式 SSE 增量解析成 []Event。
// 流式时 AppletMessage.Data 是 SSE "data: {...}\n\n" 或裸 JSON 片段，
// 需按 Google generativelanguage streaming 格式解析 candidates[0].content.parts 增量。
func parseGeminiStreamChunk(data []byte) ([]aistudio.Event, error)
```

**解析映射**：

| native Gemini JSON                          | Event                          |
| ------------------------------------------- | ------------------------------ |
| `candidates[0].content.parts[].text`       | `Event{Kind: "text", Text:...}` |
| `candidates[0].content.parts[].functionCall`| `Event{Kind: "tool_call", ToolCall:...}` |
| `candidates[0].finishReason`               | `Event{Kind: "finish", FinishReason:...}` |
| `usageMetadata`                             | `Event{Kind: "usage", Usage:...}` |
| `candidates[0].content.parts[].thoughtSignature` | `Event{Kind: "thought_signature", ThoughtSignature:...}` |
| error/status 字段                           | `Event{Kind: "error", Err:...}` |

**单测**：`gemini_event_test.go`——用真实 200 响应（今天 e2e 拿到的 functionCall + 最终回答 JSON）做 fixture，断言解析出的 Event 序列正确。

#### D3b：Transport.PumpToEvents + Service.GenerateBuildApp

**文件**：`internal/buildapp/transport.go` + `internal/aistudio/service.go` + `internal/app/runtime.go`

**transport.go 新增**：

```go
// PumpToEvents 消费 AppletMessage 流，解析为 chan Event 输出。
// 替代 PumpTo 的 ResponseWriter 直写路径，供兼容端点复用 Event → OpenAI/Anthropic 转换。
func (t *Transport) PumpToEvents(ch <-chan AppletMessage, reqID string) <-chan aistudio.Event
```

逻辑：
- `response_headers` → 检查 Status，非 200 时发 `Event{Kind:"error"}`
- `chunk` → 调 `parseGeminiResponse`（非流式）或 `parseGeminiStreamChunk`（流式）→ 解析出的 Event 发入 chan
- `error` → `Event{Kind:"error", Err:...}`
- `stream_close` → 发 `Event{Kind:"finish"}` 后 close chan

**service.go 新增**：

```go
// Service 接口加方法
GenerateBuildApp(ctx context.Context, req GenerateRequest) (<-chan Event, error)
```

```go
// PooledService 实现
func (s *PooledService) GenerateBuildApp(ctx, req) (<-chan Event, error) {
    body, err := buildAppBodyFromGenerateRequest(req)  // D2
    if err != nil { return nil, err }
    worker, err := s.pool.BuildAppWorker(ctx, req.AccountID)
    if err != nil { return nil, err }
    reqID, ch, err := worker.transport.SubmitRequest(proxyReq, body)
    if err != nil { return nil, err }
    return worker.transport.PumpToEvents(ch, reqID), nil  // D3a
}
```

**runtime.go**：`trackedService` 委托 `GenerateBuildApp` 到 `service.GenerateBuildApp`。

**注意**：`ServeBuildApp`（native Gemini 直写路径）保持不变——native Gemini 调用方仍走它。`GenerateBuildApp` 是新方法，仅供兼容端点用。

---

### D4：OpenAI Chat Completions → buildapp 路由

**文件**：`internal/api/openai.go`

**改动**：

1. `chatRequest` 加字段：
   ```go
   AccountID string `json:"account_id,omitempty"`
   ```

2. `handleChatCompletions` 在 `toGenerateRequest` 后、`s.service.Generate` 前加分支：
   ```go
   if generateRequest.AccountID != "" && s.service.AccountMode(generateRequest.AccountID) == aistudio.AccountModeBuildApp {
       events, err := s.service.GenerateBuildApp(r.Context(), generateRequest)
       if err != nil { writeOpenAIError(w, ...); return }
       // 复用现有流程
       if request.Stream {
           s.streamChatCompletion(w, r, request, requestID, created, events)
           return
       }
       result, err := consumeEvents(r.Context(), events, nil)
       if err != nil { writeOpenAIError(w, ...); return }
       writeJSON(w, http.StatusOK, buildChatCompletion(requestID, created, request.Model, result))
       return
   }
   ```

**关键**：`streamChatCompletion` / `consumeEvents` / `buildChatCompletion` 全部复用——它们消费 `chan Event`，不关心 Event 来自 playground 还是 buildapp。

---

### D5：OpenAI Responses → buildapp 路由

**文件**：`internal/api/responses.go`

同 D4 模式：
1. `responsesRequest` 加 `AccountID`
2. `handleResponses` 加分支调 `GenerateBuildApp` → 复用 `streamResponses` / `consumeEvents` / `buildResponses`

---

### D6：Anthropic Messages → buildapp 路由

**文件**：`internal/api/anthropic.go`

同 D4 模式：
1. `anthropicRequest` 加 `AccountID`
2. `handleAnthropicMessages` 加分支调 `GenerateBuildApp` → 复用 `streamAnthropic` / `consumeEvents` / `buildAnthropicResponse`

---

## 提交分割

| Commit | 文件                                              | 内容                                                          |
| ------ | ------------------------------------------------- | ------------------------------------------------------------- |
| D1     | admin.go                                          | UpdateAccount 加 Mode/BuildAppURL 守卫                        |
| D2     | buildapp_body.go + buildapp_body_test.go          | buildAppBodyFromGenerateRequest + 单测                        |
| D3a    | buildapp/gemini_event.go + gemini_event_test.go   | parseGeminiResponse + parseGeminiStreamChunk + 单测           |
| D3b    | buildapp/transport.go + service.go + runtime.go  | PumpToEvents + Service.GenerateBuildApp + trackedService 委托 |
| D4     | openai.go                                          | chatRequest 加 accountID + handleChatCompletions buildapp 分支 |
| D5     | responses.go                                       | responsesRequest 加 accountID + handleResponses buildapp 分支  |
| D6     | anthropic.go                                       | anthropicRequest 加 accountID + handleAnthropicMessages 分支   |

## 验证

1. **D1**：POST /api/accounts/{id} 更新 enabled → GET 确认 mode=buildapp 未丢
2. **D2**：单测 `TestBuildAppBodyFromGenerateRequest` 通过
3. **D3a**：单测用今天 e2e 的真实 200 响应 fixture 通过
4. **D3b**：go build + vet + test 全绿
5. **D4**：OpenAI 格式 `POST /v1/chat/completions` + `account_id` + 非流式 → 200 + OpenAI 格式响应
6. **D4 流式**：stream=true → SSE 格式正确
7. **D4 工具**：OpenAI 格式 tools → buildapp → functionCall 返回为 OpenAI tool_calls 格式
8. **D5**：`POST /v1/responses` + account_id → 200
9. **D6**：`POST /v1/messages` + account_id → 200
10. **回归**：native Gemini 路径（无 account_id）不受影响

## 风险

1. **D3a native Gemini JSON 解析**：非流式响应结构清晰（candidates + usageMetadata），但流式 SSE 增量格式需确认——Google generativelanguage streaming 的 chunk 可能是完整 `{"candidates":[...]}` 增量或裸 parts 片段，需用真实流式响应 fixture 验证。
2. **D2 GenerateRequest → native Gemini body**：`GenerationConfig` 结构体字段与 native Gemini `generationConfig` 的字段名映射需逐个核对。`Tools` 类型是自定义的，序列化后是否与 native Gemini `functionDeclarations` 格式一致需确认。
3. **D3b BuildAppWorker 暴露 Transport**：当前 `PumpTo` 是 Transport 方法，`GenerateBuildApp` 需要访问 worker 内部的 Transport。需确认 BuildAppWorker 是否已暴露 Transport，或需加 getter。
4. **流式 vs 非流式判定**：GenerateBuildApp 需知道请求是流式还是非流式来决定用 `parseGeminiResponse` 还是 `parseGeminiStreamChunk`。判定依据可以是请求 URL path 含 `streamGenerateContent`，或在 GenerateRequest 里加 Stream 字段。
5. **Camoufox 冷启动**：兼容端点第一次调 buildapp 账号会触发 ~150s 冷启动。consumeEvents 的 ctx timeout 需足够长（当前 `service.timeout` 由配置控制）。
