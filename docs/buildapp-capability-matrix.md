# Build App 能力矩阵（权威）

> 本文是 Build App（`internal/buildapp` C 路径）相对 Playground 与 iBUHub 的**最终能力边界**。
> 每行“Build 支持”都指向一个可执行测试或一份活体证据；未列即不支持。
> 生成于 phase4 能力补齐（plan `buildapp-capability-parity`），账号 2267 自有 app `7f4818a8`。

## 1. Build App 支持的能力

| 能力 | 入口 | 证据（测试 / 活体） |
|------|------|---------------------|
| 文本生成（非流式） | `POST /v1beta/models/{m}:generateContent` + `accountID` | `TestBuildAppBodyFromGenerateRequest_BasicText` + `TestParseBuildAppJSON_TextResponse`；历史活体 `PROBE_OK` 200 |
| 文本生成（流式 SSE） | `...:streamGenerateContent` | `TestBuildAppResponseEvents_Stream` |
| Token 计数 | `...:countTokens` | `TestHandleGeminiAction_buildCountTokens_usesBuildRelay` |
| 单条向量化 | `...:embedContent`（转 batch 再拆回） | `TestHandleGeminiAction_buildEmbedContent_convertsToBatchAndSplitsResponse` |
| 批量向量化 | `...:batchEmbedContents` | `TestHandleGeminiAction_buildBatchEmbedContents_passthrough` |
| OpenAI 向量 | `POST /v1/embeddings` + `account_id` | `TestHandleOpenAIEmbeddings_buildAccount_mapsBatchToOpenAIList` |
| 文件上传 | `POST /v1/files`（multipart/related→`body_b64`） | `TestHandleOpenAIFileUpload_buildAccount_relaysExactBytes` |
| **音频 / TTS** | `...:generateContent` + `responseModalities:["AUDIO"]` | **活体 200**：`.omo/evidence/.../task-5-media.md`（`todo5-tts.json`，24kHz PCM base64，5.3s） |
| 函数调用完整回合 | native `tools`/`functionCall`/`functionResponse` | `TestParseBuildAppJSON_FunctionCall`；历史活体 200 最终回答 |
| OpenAI Chat 兼容 | `POST /v1/chat/completions` + `account_id` | `TestChatCompletions_buildAccount_routesThroughBuildEvents` |
| OpenAI Responses 兼容 | `POST /v1/responses` + `account_id` | `TestResponses_buildAccount_routesThroughBuildEvents` |
| Anthropic Messages 兼容 | `POST /v1/messages` + `account_id` | `TestAnthropicMessages_buildAccount_routesThroughBuildEvents` |
| 独立 Build 模型目录 | `GET /v1beta/models?account_id=` / `GET /v1/models?account_id=` | `TestBuildCatalog_*` / `TestGeminiModels_buildAccountContext_*`；**活体 200**：`task-5-catalog.json`（4 模型） |

## 2. Build App 不支持（明确排除）

| 能力 | 原因 | 归属 |
|------|------|------|
| 图片生成 | 2267 applet 的 ListModels 目录不含 image 模型；用户排除该目标 | 仅 Playground |
| 视频 / `predictLongRunning` | API 层未接 Build 分支；目录不含 Veo；用户排除 | 仅 Playground |
| Gemini Live | 持续 Bidi/WebSocket 协议，非 HTTP relay 范畴 | 仅 Playground（`live.go`） |
| Robotics Streaming | 同上，独立 Bidi 协议 | 仅 Playground |
| OpenAI `/v1/audio/speech` 适配 | 未做；native Gemini TTS 端点已能出音频 | 需要时另提 |
| 任意路径 / 外部 URL 代理 | relay 白名单仅 `/v1beta/`、`/v1/`、`/upload/`，拒绝 `://`、`//`、`..` | 设计约束 |

## 3. 与 iBUHub 的关系

- **已移植**：`proxy_request` WS relay、authIndex 握手、每账号自有 applet、`streaming_mode` fake/real、二进制 `body_b64`、`countTokens`/`embedContent`/`batchEmbedContents`/上传/文件引用、OpenAI Chat/Responses/Anthropic 兼容、函数调用。
- **未移植（有意）**：iBUHub 的 Express 前端、`config.apiKeys` 访问密码、Web UI/VNC/网页登录、usage stats、通用任意路径 catch-all、Node/Playwright 浏览器管理。详见 `ref-ibuhub-baseline.md` §4。

## 4. 与 Playground 的关系

- Playground 仍是**完整能力端**：文本/流式/工具 + 图片 + 音频 + 视频 + 转录 + Live + Robotics + Embeddings。
- Build 是**已打通的生成/工具/向量/上传/音频端**，模型面由**独立目录**（经 applet 中继的 `ListModels`）决定，不冒用 Playground 目录。
- 双端互斥：账号 `mode` 固定单线；Build 接管时释放 Playground 浏览器树（`trackedService.ServeBuildApp` → `ForceEvictAccount`），避免 1C2G 双树常驻。

## 5. 运行前提（Build）

- 账号 `mode=buildapp` + `build_app_url` 指向**该账号自己 Remix 的 app**（默认 `cab9ab6c` 已被 Google 403）。
- Camoufox **有头**（`BUILDAPP_HEADLESS` 默认 false）+ OS 级真实鼠标点击放行 `bootstrapChannel`（Launch!）。
- 出口走 HY2（`BUILDAPP_PROXY=socks5://...`，`ProxyBypass=127.0.0.1,localhost`）。
- 首调冷启动约 150s（含引导点击 + WS 握手 + Google 首响应）。
