# Todo 5: Build App 媒体能力活体探测（范围经用户收窄：仅音频，排除图片/视频）

执行：Sisyphus 主会话，真实服务 + 2267 自有 app（7f4818a8）+ Camoufox headed + SOCKS 7897。

## 结论

| 能力 | Build 是否可用 | 证据 | 是否需新代码 |
| --- | --- | --- | --- |
| 音频 / TTS | **可用** | `POST /v1beta/models/gemini-2.5-flash-preview-tts:generateContent` → 200，5.311s，返回 `inlineData{mimeType:"audio/L16;codec=pcm;rate=24000", data:<base64 PCM>}` 70625 字节 | 否，走现有 native raw-body 透传 |
| 图片生成 | 不在目标（用户排除）；且 applet ListModels 目录不含 image 模型 | cat4 目录仅 4 模型，无 image | — |
| 视频 (Veo/predictLongRunning) | 不在目标（用户排除）；目录不含、API 层未接 Build | cat4 目录无 predictLongRunning 模型 | — |

## 关键发现

1. **TTS 无需新路由**：Gemini TTS = `generateContent` + `generationConfig.responseModalities:["AUDIO"]` + `speechConfig`。现有 native Build 分支（`gemini.go` → `handleGeminiBuildNative` → `ServeBuildApp`）已原样透传请求体并回传标准 Gemini JSON，`inlineData` 音频字节完整到达调用方。
2. **独立 Build 目录（Todo 4）经活体验证**：`GET /v1beta/models?account_id=2267` 经 applet 中继返回 200 + 真实 4 模型目录（`todo5-cat4.json`），证明 Build 目录数据源真实、不冒用 Playground。
3. **Launch! 点击修复生效**：本轮 OS 点击落在真实中心 `screen=(1130,521)`（viewport≈1118,517），非修复前的原点 `(12,4)`；点击循环在 Launch! 渲染后 ~1s 内命中（`26cc6ac`）。

## 拉起过程中修复的真实 bug（均已提交）

- `ca6ccd6` runtimeManager 漏转发 SABE/catalog → 启动 panic
- `673c418` trackedService 漏转发 catalog → 目录请求 3ms 秒 502
- `a84f30e` worker ServeHTTP 对 GET nil body 未防护 → panic
- `26cc6ac` LocateLaunch 用宽松解析器 → Launch! 未渲染时零矩形假阳性、点击循环提前退出

## 验证命令与结果

- 服务 detached 启动（PID 65032），`/api/control/start` → RUNNING
- 目录：`curl --max-time 300 GET /v1beta/models?account_id=...` → 200，1172 字节 JSON（`todo5-cat4.json`）
- TTS：`curl --max-time 150 POST .../gemini-2.5-flash-preview-tts:generateContent` → 200，70410 字节含 base64 PCM（`todo5-tts.json`）
- 服务日志：`response_headers status=200` + `data` + `stream_close`（`todo5-err.log`）

## 残留 / 未做（按用户指示）

- 图片、视频：用户明确“不以图片、视频为目标”，且该 applet 目录不含相应模型 → 不接入、不探测。
- OpenAI `/v1/audio/speech` 的 Build 适配（把 OpenAI TTS 请求转 Gemini TTS、PCM→WAV 封装）：未做。native Gemini TTS 端点已能出音频；如需 OpenAI 形状再单独提。

## 提交

无代码提交（音频经既有 native 路径已通）；本文件为探测证据。
