# 参考基线：iBUHub / AIStudioToAPI（Build App 协议来源）

> 用途：我们的 Build App C 路径（`internal/buildapp/*`）是 iBUHub `ProxyServerSystem` 协议的 **Go 重写**。
> 本文记录该协议的来源 commit、关键文件、字段，以及我们相对上游**省略/改动**的部分，
> 以便未来 iBUHub 升级其代理协议时能快速 diff 对齐。

## 1. 来源与克隆状态

| 项 | 值 |
|----|----|
| 上游 | https://github.com/iBUHub/AIStudioToAPI.git |
| 本地路径 | `D:\OpenCode\2API\AIStudio2api\repo\AIStudioToAPI` |
| 基线 commit | `db624c2`（当前参考 tip，v1.3.7） |
| 对比起点 | `3ff4b60`（此前逆向协议基线） |
| 克隆状态 | 已完成从 `3ff4b60` 到 `db624c2` 的差异核对；下一次同步前需 `git fetch` |
| 栈 | Node + Playwright(Firefox/Camoufox)；我们是 Go 移植，非 fork |

> 本次核对确认 `ProxyServerSystem.js`、`RequestHandler.js`、`BrowserManager.js` 在上述区间内零改动；
> 若 iBUHub 后续改了代理协议，应以新 tip 重新比对。

## 2. 我们逆向并移植的关键文件（协议基线 @ 3ff4b60）

| 文件 | 作用 | 我们移植到的代码 |
|------|------|------------------|
| `AIStudioToAPI/src/core/ProxyServerSystem.js` | WS 9998 服务端 + HTTP 代理；`ConnectionRegistry` 按 authIndex 注册连接；`_handleIncomingMessage` 分发 | `internal/buildapp/ws.go`（Server / Submit / handleWS / PumpTo） |
| `AIStudioToAPI/src/core/RequestHandler.js` | `_buildProxyRequest`（~4138 行）构造请求体；`_forwardRequest`（~4363 行）转发；非流式 `:generateContent` 强制 `streaming_mode:"fake"`，流式 `:streamGenerateContent` 用 `"real"` + `query_params:{alt:"sse"}` | `internal/buildapp/transport.go`（构造 proxy_request、剥无效头、按 method/path 判定 streaming_mode） |
| `AIStudioToAPI/src/core/BrowserManager.js` | `addInitScript`（~614）注入 `requestAuthIndex` 响应器（`if(window===window.top)` 激活）；`_loadAndConfigureBuildScript` 已整体注释掉（编辑器注入未启用） | `internal/camoufoxnative/session.go` 的 `AddInitScript`（等价 authIndex responder）；`internal/buildapp/session.go` 的 LaunchApplet（导航 + 点穿引导 + 激活 Preview） |

## 3. 协议字段（proxy_request / applet→server 回包）

### 3.1 server → applet：`proxy_request`
```
event_type:        "proxy_request"
request_id:        "<uuid>"
request_attempt_id:"<uuid>_attempt_1_<rand>"
method:           "POST"
path:             "/v1beta/models/<model>:generateContent"   # 已剥掉 /proxy 前缀
query_params:     {}                                          # 上游调用方 ?key= 等无效参数须剥除
headers:          { "Content-Type": "application/json", ... } # 透明透传调用方头；不含 key 时仅 Content-Type
body:             "<原始 JSON 请求体，仅保留 contents 等有效字段>"
streaming_mode:   "fake" | "real"                            # 非流式强制 fake，流式 real+alt=sse
is_generative:    true
response_transform: null
```

### 3.2 applet → server：回包
```
{ event_type:"response_headers", headers:{...}, status:<int>, request_id, request_attempt_id }
{ event_type:"chunk", data:"<原始响应字节>", request_id, request_attempt_id }
{ event_type:"stream_close", request_id, request_attempt_id }
{ event_type:"error", message:"Proxy browser error: ...", status:<int>, request_id }
```

### 3.3 authIndex 握手（postMessage）
- applet 在 run 模式激活后向父窗口 `postMessage({type:"requestAuthIndex"})`
- 顶层窗口注入的响应器回 `postMessage({type:"authIndexResponse", authIndex:0})`
- applet 随后连 `ws://127.0.0.1:9998?authIndex=0`；服务端按 authIndex 入册连接

## 4. 与上游的关键 delta（我们省略 / 改动的部分）

| iBUHub 原实现 | 我们的处理 | 原因 |
|---------------|-----------|------|
| 完整 HTTP 前端 + `config.apiKeys` 访问密码（clientKey） | **不实现**；直接用 Mag1cFall 账号模型 | fork 已有账号体系，无需 iBUHub 的访问密码层 |
| applet WS Broker（桥接浏览器 applet 上下文 ↔ 服务端） | **不实现**；applet 直连我们 Go 的 9998 | 纯 relay，去掉中间 Broker 层 |
| `build.js` 编辑器注入 + gapi OAuth（`#232` 修复） | **不实现**；gapi 在 run-mode module scope，跨域顶层取不到（false positive） | 2267 用自有 fork app（`7f4818a8`）+ 会话鉴权，无需注入 OAuth |
| `RequestHandler` 的多种代理路径 / 鉴权分支 | **仅保留** `generativelanguage` 反向代理最小集 | 只需 Build App 链路 |
| Node/Playwright 启动浏览器 | **改为** Go + camoufoxnative（Mag1cFall 既有） | 与 fork 栈统一，复用 gorilla/websocket |
| `#73`/`#142`/`#192` 的 WS 超时、applet ID 错配、连接卡死 | 我们在 `session.go` 加导航重试 + 点击 Preview 直至 WS 真连上（150s）缓解 | 已知脆弱点，仍属 headless UI 偶发 |

## 5. 已知脆弱点（同步上游时需关注）

1. **applet 依赖 Google Build App 运行时**：换默认 applet（如原 `cab9ab6c`）会对所有人 403；必须用自己的 fork app（见 `account.json` 的 `build_app_url`）。
2. **Camoufox 出口必须走 HY2**：直连 Google 被重置（见 fork-baseline §5 的 `BUILDAPP_PROXY`）。
3. **首调冷启动慢**：2267 首次 Build App 调用 Google 约 155s；上游若改了 applet 预热逻辑会影响此数。
4. **协议字段名敏感**：`event_type` / `request_attempt_id` / `streaming_mode` 拼写须与 applet 严格一致，否则 applet 静默不回包。

## 6. 同步上游时的对齐步骤

```bash
cd D:\OpenCode\2API\AIStudio2api\repo\AIStudioToAPI
git fetch origin && git log --oneline 3ff4b60..origin/main   # 看 ProxyServerSystem 相关变更
# 重点 diff：src/core/ProxyServerSystem.js / RequestHandler.js / BrowserManager.js
# 若 proxy_request 字段或握手有变，回到 internal/buildapp/{ws,transport,session}.go 对齐
```

本次结果：协议核心文件无差异，因此没有新的 Go 代码需要吸收；iBUHub 的模型目录新增 `gemini-3.8-flash` 不直接复制到主 fork，主 fork 使用 AI Studio 动态模型目录。
