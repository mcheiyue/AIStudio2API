# Todo 3: OpenAI Embeddings 与文件上传 Build 路由

执行：Sisyphus 主会话直接实现（三次委派均虚报交付，见 ledger）。

## 实现内容

- `internal/api/gemini_build_adapters.go`：新增 `handleOpenAIEmbeddings`。
  - OpenAI input（string 或 string[]）→ native `batchEmbedContents`（每条 input 一个 request，`models/<model>`）。
  - 经 `ServeBuildApp` capture 模式中继；响应 `embeddings[].values` → OpenAI `{object:"list",data:[{object:"embedding",embedding,index}],model}`。
  - 非 buildapp / 缺失账号 → 400 `embeddings require a buildapp account_id`，不启动 worker；input 形状错误、空数组 → 400。
- `internal/api/files.go`：新增 `handleOpenAIBuildFileUpload`。
  - buildapp 分支（`handleOpenAIFileUpload` L80-83 调用）：文件字节限 `buildapp.MaxRelayBodyBytes`（32MiB），超限 413 且不启动 worker。
  - 组 multipart/related（JSON 元数据 + 媒体体）→ `POST /upload/v1beta/files?uploadType=multipart`，Content-Type `multipart/related; boundary=aistudio2api-build-upload`；relay 对 `/upload/` 路径强制走 `body_b64`（二进制安全）。
  - 响应 `file.{name,uri,...}` → OpenAI file object（id/purpose/status/bytes）。
- `internal/buildapp/relay.go`：导出 `EncodeProxyRequest`/`DecodeProxyPayload` 供 API 层与测试复用（前一执行者遗留的有用半成品，保留）。
- `internal/api/router.go`：注册 `POST /v1/embeddings`（前一执行者遗留，保留）。

## 测试（全部真实执行）

- `gemini_build_adapters_test.go`：embeddings buildapp 成功映射（2 条 input → 2 个 request、index/值断言）、非 buildapp/缺账号 400 且 0 worker、坏 input 3 例 400 且 0 worker。
- `buildapp_files_test.go`（新）：上传字节经 body_b64 roundtrip 精确保留、`/upload/v1beta/files` 路径与 multipart/related、Content-Length 一致、OpenAI file object 形状；超限 413 且 0 worker；playground 账号保持既有 `file upload is unavailable` 错误。
- 既有未提交测试保留：`buildapp_body_test.go` fileData 保留、`recordingStudio` 记录 ContentLength/ContentType。

## 验证命令与结果

- `go build ./...` → exit 0
- `go vet ./...` → exit 0
- `go test ./... -count=1` → 全部 ok（api 1.262s / aistudio / buildapp / camoufoxnative）
- `gofmt -l`：仅 `internal/aistudio/accounts.go`、`internal/api/admin.go`（上游合并遗留既有漂移，不属本任务改动集，未处理）

## 残留风险

- 无真实 applet E2E（fixture 级验证；真实上传响应字段以 Google 实际返回为准，Todo 5 探测阶段覆盖）。
- OpenAI `file_id` 引用未映射到 FileRef（现有 fileData 直传已覆盖主要路径）。

## 提交

`feat(buildapp): route embeddings and file operations`（见 git log）
