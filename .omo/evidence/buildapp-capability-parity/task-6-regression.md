# Todo 6: 回归现有 Build 兼容端点、工具调用和 Playground 隔离

执行：Sisyphus 主会话。

## 覆盖矩阵（全部 fixture 级、真实执行）

| 回归项 | 测试 | 结论 |
| --- | --- | --- |
| OpenAI Chat buildapp 分支 | `TestChatCompletions_buildAccount_routesThroughBuildEvents` | ServeBuildAppEvents 被调（account/model/stream/body 断言，`"text":"hi"` 保留），Playground Generate 0 次；响应 chat.completion 形状正确 |
| OpenAI Chat Playground 隔离 | `TestChatCompletions_withoutAccount_keepsPlaygroundPath` | 无 account_id → 走 Generate（1 次），SABE 0 次 |
| Responses buildapp 分支 | `TestResponses_buildAccount_routesThroughBuildEvents` | body 保留 input；resp_ ID 输出 |
| Anthropic buildapp 分支 | `TestAnthropicMessages_buildAccount_routesThroughBuildEvents` | msg_ ID + type=message + assistant + content 块 |
| Live/Robotics 隔离 | `TestLiveAndRobotics_neverTouchBuild` | `/v1/live`、`/v1/robotics/stream` 零 SABE、零 relay 调用 |
| 函数调用解析 | 既有 `TestParseBuildAppJSON_FunctionCall` 等 | 保持通过 |
| 事件流（流式/非流式） | 既有 `TestBuildAppResponseEvents_Stream/_NonStream` | 保持通过 |
| 原生 Gemini / 目录 / embeddings / 上传 | 既有 27 个 api 测试 | 保持通过 |

stub 修复：`recordingStudio.ServeBuildAppEvents`/`Generate` 空流（无终止事件）触发 consumeEvents `upstream stream closed before finish` —— 改为 text + finish(STOP) 最小有效序列；`compatHandler` 补 `responseStates`（Responses 存状态空指针）。

## 验证命令与结果

- `go test ./... -count=1` → 全部 ok
- `go vet ./...` → exit 0
- `go build ./...` → exit 0
- `gofmt -l internal` → 空

## 残留风险

- 兼容端点的真实端到端（真实 applet 回包）未在本轮执行，属 Todo 5/Todo 8 的活体验证范畴。
- Anthropic 流式 writer 路径未单独测（非 buildapp 分支既有覆盖由上游维护）。

## 提交

`test(buildapp): verify capability parity boundaries`
