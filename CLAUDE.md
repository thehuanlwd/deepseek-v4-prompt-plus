# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 文档地图

| 位置 | 用途 | 是否入库 |
|------|------|---------|
| `CLAUDE.md`（本文件） | AI 入口：架构、模块、命令 | 是 |
| `README.md` | 用户功能说明与接入指南 | 是 |
| `docs/开发必读/` | AI 开发记录：标准、坑点、设计规格（见 `docs/开发必读/README.md`） | 是 |
| `docs/` | 用户文档与 SEO 推广文档（README、截图、推广文档） | 是 |
| `其他文档/` | 个人灵感与笔记 | **否** |

修改代码前应先阅读 `docs/开发必读/` 中与本次改动相关的文档，尤其是坑点记录与设计规格。

## 项目概述

DSPlus 是一个本地运行的 **分析+强化系统**。它作为一个轻量级的 API 中转代理，专为 DeepSeek 等模型设计。它能够完整分析原始请求和返回数据，其核心系统能力包含：

1. **分析系统**：自动将代理流量归入会话（Session）与轮次（Turn），无损记录并完整分析原始请求和响应数据，支持自包含哈希去重归档和 Markdown 诊断报告导出。
2. **强化系统**：
   - **提示词强化**：将 System Prompt 重组到合适的位置以适应特定模型缓存，支持注入额外的高优先级全局指令。
   - **防循环强化**：内置防思维循环引擎，支持并行检测判定与模型退避重试，以解决推理模型（如 DeepSeek-R1）在工具调用场景下的死循环问题。

## 常用命令

### 编译构建

```bash
# 无 CGO 编译（浏览器 GUI）- 推荐
CGO_ENABLED=0 go build -ldflags="-s -w" -o DSPlus.exe .

# CGO 编译（内嵌 WebView 窗口）
CGO_ENABLED=1 go build -ldflags="-H windowsgui -s -w" -o DSPlus.exe .

# Windows 批处理
build.bat
```

### 运行

```bash
DSPlus.exe                  # 默认端口 8188
DSPlus.exe --port=9999      # 自定义端口
# 注意：GUI 始终自动打开。窗口关闭按钮（X）会隐藏到托盘（最小化到托盘），托盘菜单“退出”才真正停止服务。
```

### 测试

```bash
go test ./...
go test -v -run TestTransformOpenAIInPlace ./...  # 单个测试
```

## 架构设计

### 请求处理流程

```
客户端请求 → 格式检测 → 按模型路由（选定供应商 + 还原上游真实模型名）→ System Prompt 重组 → 参数注入 → 转发上游 API
                 ↓
           防循环引擎（可选）→ 检测到死循环 → 子Agent分析 → 重试
```

### 响应回传流程

| 模式 | 触发条件 | 处理方式 |
|------|----------|----------|
| 改写模式 | 防循环开启，或防幻觉开启且请求不带 tools | 上游压缩则先解压，再逐行解析/改写，以明文回传（不回压，去掉 `Content-Encoding`） |
| 纯透传 | 其余请求 | 上游字节原样转发，不解压、不解析、不改写；另用旁路副本解析，供仪表盘与诊断分析使用 |

请求侧的开关（reasoning_content 补全、空 content 修复、System Prompt 与额外 Prompt 注入、思考档位、Max Tokens）只修改发出去的请求体，不影响响应回传模式。

### 核心模块职责

| 文件 | 职责 | 关键函数 |
|------|------|---------|
| `config.go` | 配置管理（JSON 持久化）、供应商字段与迁移规范化 | `LoadConfig()`, `SaveConfig()`, `SafeConfig`, `normalizeProviders()` |
| `route.go` | 全局模型路由表：模型名 → 供应商 + 上游真实模型名 | `buildRouteTable()`, `RouteTable.Resolve()`, `ListModels()` |
| `transform.go` | System Prompt 重组（OpenAI + Anthropic） | `transformOpenAIInPlace()`, `transformAnthropicInPlace()` |
| `proxy.go` | HTTP 代理核心 + 防循环流引擎 | `forwardStreamWithAntiLoop()`, `injectThinkingParams()` |
| `retry.go` | 防循环：子Agent分析、并行检测、重试 | `callAntiLoopAnalyzerWith()`, `parallelAnalyze()` |
| `analysis.go` | 会话聚合分析与自包含哈希去重日志 | `InitAnalysisService()`, `SubmitEvent()`, `loadHistoryFromDisk()` |
| `logger.go` | 环形缓冲日志 + Token 统计 + WebSocket 广播 | `NewLogger()`, `UpdateTokenUsage()` |
| `gui.go` | 内嵌 Web 前端 + REST/WebSocket API | `handleGUI()` |
| `gui_webview.go` | WebView2 桌面窗口（CGO 模式） | `openGUI()`, `hasGUI()` |
| `gui_fallback.go` | 浏览器回退（非 CGO 模式） | `openGUI()` |
| `ws.go` | WebSocket 实时推送 | `wsHub`, `handleWebSocket()` |

### 关键设计模式

1. **SafeConfig 线程安全**：配置读写使用 `sync.RWMutex` 保护
2. **环形缓冲日志**：`Logger` 固定容量（2000条），自动淘汰旧日志
3. **双格式支持**：通过结构化解析自动判断 OpenAI/Anthropic 格式
4. **防循环三级防线**：
   - 启发式判定（content==0 && reasoning 过高）
   - 并行分析器（goroutine，不中断主流程）
   - finish_reason="length" 兜底
5. **自包含哈希去重日志**：`AnalysisService` 对大段 system prompt 计算 MD5，同天内仅首发写入明文，后续使用哈希引用，并在读盘时自包含还原，解决无损存储与磁盘体积的矛盾。
6. **物理行为与系统干预分离**：日志的 `SemanticType` 只代表真实的 API 物理网络交互行为，而防循环重试、防幻觉、思考完成等辅助控制事件由 `RequestType` 和 `SystemEvent` 承载，并在最后一列表头独立渲染，请求端点如实展现原本路径，行为与标记彻底解耦。
7. **局部增量更新流**：前端 Token 数据高频更新时改用防御性增量更新，绝不重写 `innerHTML`，以避免鼠标 Hover 闪烁并保证整行动作交互（如点击抽屉）的高灵敏传递。
8. **多供应商按模型路由**：`providers` 数组顺序即优先级，第一位为默认供应商（路由未命中的兜底目标）；跨供应商重名的模型 ID 对外统一加前缀 `供应商ID/模型ID`，请求命中后剥离前缀还原上游真实模型名；未命中则原样透传给默认供应商。路由表为纯函数 `buildRouteTable()`，每次请求现算，配置变更即时生效。规则细节见 `docs/开发必读/2026-10-07-multi-provider-model-routing-design.md`。

### API 端点

| 路径 | 用途 |
|------|------|
| `/chat/completions` | OpenAI 格式代理入口 |
| `/v1/messages` | Anthropic 格式代理入口 |
| `/` | GUI 仪表盘 |
| `/api/status` | 服务状态 JSON |
| `/api/logs` | 请求日志列表 |
| `/api/config` | 获取/更新配置 |
| `/api/route/preview` | 模型路由表预览（对外模型名、冲突与遮蔽状态） |
| `/api/analysis/status` | 获取分析服务运行状态 |
| `/api/analysis/sessions` | 获取分析会话列表 |
| `/api/analysis/sessions/{id}` | 获取特定会话详情 |
| `/api/analysis/sessions/{id}/export.md` | 导出特定会话的诊断报告 |
| `/ws` | WebSocket 实时推送 |

## 配置说明

配置文件 `config.json` 自动生成在 `DSPlus.exe` 同目录，关键字段：

- `api_key`: 全局兜底 API Key
- `providers`: 供应商列表，数组顺序即优先级；每项含 `id`（模型重名时的对外前缀，字符白名单 `[A-Za-z0-9_-]`）、`name`、`enabled`（缺省为启用，第一位强制启用）、`base_url`、`anthropic_base_url`、`api_key`（加密）、`models`（为空时不参与路由表，作为默认供应商时承担全能透传）
- `active_provider`: 兼容字段，仅与列表第一位同步，不再作为路由依据
- `port`: 监听端口（默认 8188）
- `system_prompt_placement`: 拼接位置（`first`/`last`/`none`）
- `thinking_mode`: 思考模式（`enabled`/`disabled`/空）
- `anti_loop_enabled`: 防循环总开关
- `antiloop_check_tokens`: 主动检测阈值（0=关闭）
- `language`: 界面语言（"zh" / "en"），首次启动按系统自动检测
- GUI：始终默认打开并不可禁用（设置页已移除“启动时打开 GUI”开关，--no-gui 标志被忽略）。窗口 X 关闭按钮会最小化到托盘，托盘“退出”才真正退出。

## 注意事项

1. **CGO 依赖**：内嵌 WebView 需要 MinGW 编译器（推荐 WinLibs）
2. **API Key 安全**：明文存储在 `config.json`，不要提交到版本控制
3. **日志文件**：`antiloop_trace.log` 自动追加写入，定期清理
4. **防循环成本**：每次重试额外消耗 API 调用，按需开启

## 依赖包

- `github.com/gorilla/websocket`: WebSocket 实时推送
- `github.com/webview/webview_go`: 内嵌桌面窗口（CGO 模式）
