# Changelog

All notable changes to DSPlus are documented here. This project follows the [Keep a Changelog](https://keepachangelog.com/) format.

---

## [0.2.4] - 2026-10-07

### Added
- **Multi-provider model routing (one endpoint, many upstreams)** — All enabled providers now serve simultaneously, dispatched by the `model` field of each request instead of a single global "active provider". When the same model ID is offered by more than one provider, every duplicate is exposed as `ProviderID/ModelID`; the prefix is stripped before forwarding, so the upstream still receives its real model name. Requests matching no configured model fall through to the default provider (first entry in the list) with the model name passed through unchanged.
- **Provider ID and per-provider enable switch** — `providers[]` gained `id` (unique identifier, also used as the conflict prefix; letters, digits, underscores and hyphens only) and `enabled` (whether the provider takes part in routing). The GUI places the ID next to the provider name, fills it automatically from English names, warns on duplicates, and replaces the old single-choice switch with an independent enable toggle per provider. The first entry is the default provider and cannot be disabled.
- **Drag-and-drop provider ordering** — The provider list can be reordered by dragging; the first entry is the default provider. Auto-save is paused while dragging and flushed once on drop.
- **Model route preview endpoint and conflict badges** — `GET /api/route/preview` returns the resolved routing table; the model list shows the public model ID (conflict prefix highlighted), the owning provider and a status badge (prefixed / unroutable / disabled), plus a summary bar counting conflicts and shadowed entries.
- **Route table unit tests** — `route_test.go` covers conflict prefixing, three-way conflicts, shadowed entries, pass-through providers, disabled providers, model IDs containing `/`, prefix matching case-insensitivity, fallback behaviour and legacy ID migration.

### Changed
- **Default provider now follows list order** — `active_provider` is kept only for backward compatibility and stays in sync with the first entry; it is no longer used for routing, which also removes the silent mis-selection that occurred when two providers shared a name.
- **`/v1/models` merges all enabled providers** — conflicting entries appear with their prefix; entries shadowed by a second-level conflict are not exposed.
- **Anti-loop retries and the analyzer sub-agent stay on the provider that served the request** — previously they always targeted the globally active provider.
- **Legacy configuration migration** — providers without an `id` get one generated on load (English names reuse the name, others become `provider1`, `provider2`, … with numeric suffixes on collision); a legacy `active_provider` pointing at a non-first provider is moved to the front once, so the previously selected provider stays the default. Providers without `enabled` are treated as enabled.
- **Settings page** — the "current provider" dropdown and the add/remove buttons are gone; the page now shows the default provider read-only with a link to the model services page, and its editor targets the first provider.

### Fixed
- **Missing `anthropic_base_url` on the analyzer sub-agent** — the anti-loop analyzer now resolves its endpoint through the same format-aware helper as the main request instead of hard-coding `base_url + /chat/completions`.
- **API keys could be encrypted twice** — saving a configuration whose key was already stored in `ENC:` form (for example after a decryption failure) no longer re-encrypts the ciphertext, which previously left the GUI displaying an unusable value.
- **Test suite aligned with the implementation** — Two long-failing unit tests were corrected: format detection now verifies both behaviours separately (no known protocol path → decide from the request body; known path → the path wins), and body truncation now checks the keep-head-and-tail strategy (the old assertions still assumed tail truncation with a `[truncated]` marker).

## [0.2.4] 中文 - 2026-10-07

### Added（新增）
- **多供应商模型路由（一个接口对接多个上游）** — 所有已启用的供应商同时生效，按每次请求的 `model` 字段分发，而不再由全局「当前供应商」独占。同一模型 ID 出现在多个供应商时，所有重名项对外统一显示为 `供应商ID/模型ID`，转发前会剥掉前缀还原上游真实模型名；未命中任何已配置模型的请求回退到默认供应商（列表第一位）并原样透传模型名。
- **供应商 ID 与独立启用开关** — `providers[]` 新增 `id`（唯一标识，同时作为冲突前缀，仅允许字母、数字、下划线、短横线）与 `enabled`（是否参与路由）。界面在供应商名称旁增加 ID 输入框，英文名称自动填充、重复时提醒，并把原来的单选开关换成每个供应商独立的启用开关；第一位为默认供应商且不可关闭。
- **供应商拖拽排序** — 供应商列表支持拖拽调整顺序，第一位即默认供应商；拖拽期间暂停自动保存，落位后统一提交一次。
- **模型路由预览接口与冲突徽标** — 新增 `GET /api/route/preview` 返回解析后的路由表；模型列表展示对外模型 ID（冲突前缀高亮）、归属供应商与状态徽标（已加前缀 / 无法路由 / 未启用），顶部显示冲突与遮蔽数量汇总。
- **路由表单元测试** — `route_test.go` 覆盖冲突加前缀、三方冲突、遮蔽项、穿透模式供应商、未启用供应商、含 `/` 的模型名、前缀大小写不敏感、未命中回退与旧配置 ID 迁移。

### Changed（变更）
- **默认供应商改由列表顺序决定** — `active_provider` 仅作兼容保留并与第一位保持同步，不再参与路由；由此消除了两个供应商同名时静默命中错误项的隐患。
- **模型清单接口合并输出** — `/v1/models` 合并所有已启用供应商的清单，重名项带前缀；因二次冲突被遮蔽的条目不再对外暴露。
- **防循环重试与分析子Agent跟随本次命中的供应商** — 此前无论请求发往哪里，它们都固定打到全局活动供应商。
- **旧配置自动迁移** — 没有 `id` 的供应商在加载时自动生成（英文名称取同名，其余按 `provider1`、`provider2` 顺序并在冲突时加序号）；旧的 `active_provider` 若指向非首位供应商，会一次性上移到首位，使用户原先选定的供应商继续作为默认供应商；没有 `enabled` 的供应商视为启用。
- **设置页调整** — 移除「当前供应商」下拉框与供应商增删按钮，改为只读展示默认供应商并提供跳转入口；设置页的供应商编辑区固定编辑第一位（默认供应商）。

### Fixed（修复）
- **分析子Agent遗漏 `anthropic_base_url`** — 防循环分析器的端点改为与主请求共用同一套格式感知解析，不再硬编码 `base_url + /chat/completions`。
- **API Key 可能被二次加密** — 保存时若密钥已是 `ENC:` 密文（例如解密失败的保留值），不再重复加密，避免界面只能看到无法使用的一串密文。
- **测试与实现对齐** — 修正两条长期失败的单元测试：请求格式识别拆成两条分别验证「未命中已知协议路径时按请求体判定」与「命中协议路径时路径优先」；日志截断改为校验「保头尾、省略中段」策略（原断言停留在旧的尾部截断 + 英文标记实现）。

---

## [0.2.3] - 2026-10-02

### Added
- **DeepSeek-specific Reasoning Effort Mapping** — Reasoning intensity is now collapsed to DeepSeek's official tiers (`low` / `high` / `max`) when the request targets a DeepSeek-series model (model id containing `deepseek`, or an upstream on `*.deepseek.com`): `minimal → low`, `low → low`, `medium → high`, `high → high`, `xhigh → high`, `max → max`, `ultra → max`. The rule covers the configured value, the anti-loop retry value and any client-supplied effort, and is keyed on the model belonging to the DeepSeek series rather than on the vendor. Models outside that series keep the previous behavior (`ultra → max`, every other value passed through unchanged).

### Fixed
- **Test suite compilation** — `dsplus_test.go` referenced the `configPath` helper without calling it, which prevented the entire test binary from building.

## [0.2.3] 中文 - 2026-10-02

### Added（新增）
- **DeepSeek 专属推理强度映射** — 当请求指向 DeepSeek 系列模型（模型名含 `deepseek`，或上游属于 `*.deepseek.com`）时，推理强度收敛到 DeepSeek 官方档位（`low` / `high` / `max`）：`minimal → low`、`low → low`、`medium → high`、`high → high`、`xhigh → high`、`max → max`、`ultra → max`。该规则覆盖服务端配置值、防循环重试值与客户端自带值，判据是「模型属于 DeepSeek 系列」而非厂商；非 DeepSeek 系列的模型维持原有行为（仅 `ultra → max`，其余档位原样透传）。

### Fixed（修复）
- **测试套件编译** — `dsplus_test.go` 中 `configPath` 未加括号调用，导致整个测试二进制无法编译。

---

## [0.2.2] - 2026-09-24

### Added
- **Unified 7-tier Reasoning Effort Standard** — Extended thinking intensity options across UI, API, and configs to `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, and `ultra`, unifying multi-vendor / multi-gateway reasoning configurations.
- **Thinking Levels Comparison Test Suite** — Added `test/test_thinking_levels.go` to test all 8 thinking modes/levels against live upstreams.

### Changed
- **Reasoning Effort Sanitization & Safe Mapping** — Added `sanitizeReasoningEffort` to map non-standard `ultra` tier to `max` at proxy boundary, preventing 400 Bad Request errors (`invalid_request_error: param reasoning.effort`) when routing through Vercel AI SDK, OpenRouter, and strict API Gateways.

### Fixed
- **Gateway Thinking Disable Parity** — Fixed issue where forcing thinking off failed on OpenRouter / Cline Gateway by injecting `reasoning_effort: "none"` and `reasoning: {effort: "none"}` alongside DeepSeek's `thinking: {type: "disabled"}`.
- **Anthropic Format-Aware Retry Thinking** — Fixed Anti-Loop retry requests to properly format thinking parameters (`output_config.effort` vs `reasoning_effort`) based on protocol format.

## [0.2.2] 中文 - 2026-09-24

### Added（新增）
- **统一 7 档思考强度标准** — UI 设置页、配置管理与代理层全面扩展思考强度为 `minimal`、`low`、`medium`、`high`、`xhigh`、`max`、`ultra` 7 档，实现跨厂商（OpenAI / Anthropic / DeepSeek / AI Gateway）统一标尺。
- **全档位自动化测试套件** — 新增 `test/test_thinking_levels.go`，支持对所有思考档位进行真实调用验证与 Token 开销对比。

### Changed（变更）
- **推理强度安全归一化映射** — 引入 `sanitizeReasoningEffort` 保护层，将 `ultra` 等拓展档位安全映射为网关白名单中的 `max`，彻底规避 Vercel AI SDK / OpenRouter 等网关的 400 `invalid_request_error` 校验报错。

### Fixed（修复）
- **网关强制关闭思考生效** — 修复了通过 Cline / OpenRouter 网关代理时「强制关闭思考」失效的问题，同时注入 `reasoning_effort: "none"` 与 `reasoning: {effort: "none"}` 确保思维链 100% 关闭。
- **Anthropic 格式感知重试适配** — 修复 Anti-Loop 截断重试链路中的协议格式感知，针对 `/v1/messages` 请求规范注入 `output_config.effort` 并清理冗余字段。

---

## [0.2.1] - 2026-07-12

> 🎉 **DSPlus v0.2.1 — 重要修复版。** 修复了一个导致系统提示词失效的严重 bug：当「系统提示词不修改 placement」搭配「额外提示词注入」时，系统提示词会被静默丢弃，使原始请求与重组后请求对应不上。同时新增多供应商支持、API Key 加密存储与请求审计自检脚本。

### Added

- **Multi-provider support** — `config.json` now accepts a `providers` list; each provider configures its own `base_url`, optional `anthropic_base_url`, and `api_key`, switched at runtime via `active_provider`. The GUI settings page can add / remove / switch providers. Legacy `api_key` / `openai_upstream` / `anthropic_upstream` fields remain backward compatible and are auto-upgraded into a default provider on load.
- **Encrypted API Key storage** — API Keys and per-provider keys are now AES-GCM encrypted (with an `ENC:` prefix) before being written to `config.json` instead of stored in plaintext; legacy plaintext keys are upgraded to ciphertext on next save.
- **Request audit & self-check** — every proxied request now appends a `RequestAudit` record to `test/request_audit.jsonl`, capturing both the configured intent (`Cfg*`) and the actual effective values in the transformed request (`Actual*`: presence of standalone system prompt, `<system_prompt>` / `<supreme_instruction>` tags, thinking type, reasoning_effort, max_tokens). New `audit/main.go` script: run `go run ./audit` to read the latest record and verify config-vs-actual consistency, exiting non-zero on mismatch.
- **System prompt shown separately in GUI** — the settings page now has a dedicated area displaying the system prompt.

### Changed

- **max_tokens options expanded** — added a `384000` tier (hard cap: no value may exceed it, including the `custom` tier) and an `off` ("do not send") tier that forcibly removes the `max_tokens` parameter.

### Fixed

- **System prompt lost under "no change" placement + extra injection** — when `system_prompt_placement=none` while extra-prompt injection was enabled, the system prompt was destroyed *before* the "should we restructure?" check, causing silent loss and a mismatch between the original and transformed requests. System messages / fields are now preserved unless actually being restructured.
- **Silent injection drops** — `appendToUserContent` now falls back to string coercion for unexpected `content` types (null / number) instead of silently discarding the injected block; OpenAI system collection now also accepts Anthropic-style text-block arrays.
- **OpenAI / Anthropic transform parity** — the OpenAI path now also skips prompt restructuring during the `tool_result` stage (previously only Anthropic did), avoiding injecting prompts into tool-result messages.
- **Anthropic thinking toggle symmetry** — forcing thinking *off* now explicitly writes `thinking:{type:disabled}` and clears leftover `output_config` / `reasoning_effort` (previously only the OpenAI path handled this), so a thinking field carried by the original request is truly disabled.

## [0.2.1] 中文 - 2026-07-12

> 🎉 **DSPlus v0.2.1 —— 重要修复版。** 修复了一个导致系统提示词失效的严重 bug：当「系统提示词不修改 placement」搭配「额外提示词注入」时，系统提示词会被静默丢弃，使原始请求与重组后请求对应不上。同时新增多供应商支持、API Key 加密存储与请求审计自检脚本。

### Added（新增）

- **多供应商支持** — `config.json` 现支持 `providers` 列表，每个供应商可单独配置 `base_url`、可选的 `anthropic_base_url` 与 `api_key`，并通过 `active_provider` 切换当前供应商；GUI 设置页可增删与切换供应商。旧版 `api_key` / `openai_upstream` / `anthropic_upstream` 字段保持向后兼容，加载时自动升级为默认供应商。
- **API Key 加密存储** — 写入 `config.json` 前，API Key 与各供应商密钥统一使用 AES-GCM 加密（带 `ENC:` 前缀），不再明文落盘；旧版明文密钥在下次保存时自动升级为密文。
- **请求审计与自检脚本** — 每次代理请求都会向 `test/request_audit.jsonl` 追加一条 `RequestAudit` 记录，同时保存「设置页面配置意图」(`Cfg*`) 与「重组后请求体实际生效值」(`Actual*`，含独立 system 提示词、`<system_prompt>` / `<supreme_instruction>` 标签、thinking 类型、reasoning_effort、max_tokens)。新增 `audit/main.go` 自检脚本：运行 `go run ./audit` 读取最近一条记录，逐项核对配置与实际生效是否一致，不一致时以非零退出码退出。
- **系统提示词单独展示** — GUI 设置页新增独立的系统提示词展示区，便于查看与编辑。

### Changed（变更）

- **max_tokens 档位扩展** — 新增 `384000` 档（硬上限，所有档位含自定义均不得超过 384000）与「不发送」(`off`) 档（强制删除 `max_tokens` 参数）；自定义档同样受 384000 上限钳制。

### Fixed（修复）

- **系统提示词在「不修改」+ 额外注入组合下丢失** — 当 `system_prompt_placement=none` 但启用了额外提示词注入时，转换函数在「是否需重组」判断之前就销毁了 system 消息/字段，导致系统提示词被静默丢弃、原始请求与重组后请求对应不上。现仅在真正重组系统提示词时才丢弃，否则原样保留。
- **注入块静默丢失** — `appendToUserContent` 对 `null` / 数字等非预期 `content` 类型改为字符串兜底拼接，不再静默丢弃注入块；OpenAI 系统提示词收集现兼容 Anthropic 风格 text 块数组（content 为数组时也能正确提取）。
- **OpenAI / Anthropic 转换一致性** — OpenAI 路径在 `tool_result` 阶段也跳过提示词重组（此前仅 Anthropic 跳过），避免向工具结果消息注入系统 / 额外提示词。
- **Anthropic 思考开关对称** — 强制关闭思考时显式写入 `thinking:{type:disabled}` 并清理残留的 `output_config` / `reasoning_effort`（此前仅 OpenAI 路径处理），确保原始请求自带的思考字段被真正关闭。

## [0.2.0] - 2026-07-07

> 🎉 **DSPlus v0.2.0 is here — this is the product's true debut.**
>
> DSPlus evolves from a minimal system-prompt proxy into a complete local **analysis + enhancement system** for DeepSeek V4: fewer hallucinations, stronger instruction following, no reasoning loops, no tool-call freezes, with built-in session analysis and a local GUI.

### Added

- **Prompt Guard** — reorganize the system prompt (place after the *first* or *last* user message, or leave unchanged) and inject high-priority global instructions so constraints stay in DeepSeek V4's effective context. Greatly reduces format dropouts, failed prohibitions, and persona drift.
- **Anti-Loop engine** — detects runaway reasoning, analyzes in parallel, and retries with an independent retry model + guided prompt when stuck in a loop or truncated by `max_tokens`.
- **Empty content repair** — auto-backfills empty assistant `content` after tool calls, breaking the silent-reply feedback loop that IDEs can amplify.
- **reasoning_content auto-completion** — improves tool-call format compatibility for reasoning models.
- **Intent confirmation (experimental)** — re-injects the latest user intent right before the final answer to reduce logic drift / logic hallucinations. Note: token usage can roughly double, so it is off by default.
- **Long-conversation stability** — maintains persona and settings across long context and reduces empty / short-output drift.
- **CoT stabilization** — clearer boundaries between `reasoning_content`, body text, and tool calls.
- **Session analysis** — aggregates proxy traffic into Sessions / Turns, keeps lossless hash-deduplicated archives, and exports Markdown diagnostic reports.
- **Local GUI** — live dashboard, settings, token / cache-hit stats, retry view, and basic diagnostics; serves both the OpenAI-compatible and Anthropic-compatible proxy on a single port.
- **Real-time logs** — ring-buffer logging with WebSocket broadcast.
- **i18n** — Chinese / English GUI and bilingual documentation.
- **Themes** — YoRHa and Classic (GitHub Dark) GUI themes.
- **Cross-platform build** — Windows (WebView2 window + tray on CGO build, or browser fallback on non-CGO) and headless builds for Linux / macOS.
- **Client guides** — SillyTavern, Claude Code / coding agents, Open WebUI, and OpenAI / Anthropic SDK integration.

### Changed

- Restructured request pipeline: format detection → semantic stage recognition → system prompt reorganization → extra prompt injection → `thinking` / `max_tokens` handling → reasoning / empty-content repair → forward to DeepSeek API → streaming capture & logging → optional Anti-Loop / intent confirmation / auto retry.
- GUI auto-opens (WebView2 on CGO build, default browser on non-CGO); the proxy itself runs independently of the GUI.
- Repo renamed to `deepseek-v4-prompt-plus` for clearer discoverability.

### Fixed

- Cross-platform compilation: moved the Windows-only `kernel32` language detection behind `//go:build windows` (into `lang_windows.go`, with `lang_other.go` for other platforms) so Linux / macOS builds succeed.

### Previous release

- **0.1.0-alpha** — initial proof-of-concept: a local DeepSeek V4 system-prompt restructuring proxy.

---

## [0.2.0] 中文 - 2026-07-07

> 🎉 **DSPlus v0.2.0 正式发布——这是本产品的真正亮相。**
>
> DSPlus 从一个最小化的 system prompt 重组代理，演进为面向 DeepSeek V4 的完整本地「分析 + 强化系统」：更少幻觉、更强指令遵循、不再思维循环、不再工具调用假死，并自带会话分析与本地 GUI。

### Added（新增）

- **Prompt Guard（提示词守护）** — 重组 system prompt（拼接在首条或最后一条用户消息后，或不修改），并注入高优先级全局指令，让约束稳定停留在 DeepSeek V4 的有效上下文中。显著减少格式掉落、禁令失效与人设漂移。
- **Anti-Loop 防循环引擎** — 检测推理死循环，并行分析，并在卡住或被 `max_tokens` 截断时用独立重试模型 + 指导语重试。
- **空 content 修复** — 工具调用后自动回填空的 assistant `content`，打断 IDE 会放大的「沉默回复」循环。
- **reasoning_content 自动补全** — 提升推理模型在工具调用场景下的格式兼容性。
- **意图确认（实验性）** — 在最终回答前重新注入最新用户意图，缓解逻辑漂移 / 逻辑幻觉。注意：Token 消耗可能接近翻倍，默认关闭。
- **长对话稳定** — 在长上下文中维持人设与设定，减少输出变空变短的退化。
- **CoT 稳定处理** — 厘清 `reasoning_content`、正文与工具调用之间的边界。
- **会话分析** — 将代理流量聚合为会话（Session）/ 轮次（Turn），无损哈希去重归档，并导出 Markdown 诊断报告。
- **本地 GUI** — 实时仪表盘、设置、Token / 缓存命中统计、重试视图与基础诊断；同一端口同时提供 OpenAI 兼容与 Anthropic 兼容代理。
- **实时日志** — 环形缓冲日志 + WebSocket 实时推送。
- **国际化** — 中文 / 英文 GUI 与双语文档。
- **主题** — YoRHa 与 Classic（GitHub Dark）GUI 主题。
- **跨平台构建** — Windows（CGO 构建为 WebView2 窗口 + 托盘，非 CGO 为浏览器回退）以及 Linux / macOS 无界面构建。
- **客户端接入指南** — SillyTavern、Claude Code / Coding Agent、Open WebUI，以及 OpenAI / Anthropic SDK 接入。

### Changed（变更）

- 重构请求管线：格式检测 → 语义阶段识别 → system prompt 重组 → 额外 Prompt 注入 → `thinking` / `max_tokens` 处理 → reasoning / 空 content 修复 → 转发 DeepSeek API → 流式捕获与日志 → 可选 Anti-Loop / 意图确认 / 自动重试。
- GUI 随启动自动打开（CGO 构建为 WebView2 窗口，非 CGO 为默认浏览器）；代理本身不依赖 GUI 即可运行。
- 仓库更名为 `deepseek-v4-prompt-plus`，提升可发现性。

### Fixed（修复）

- 跨平台编译：将 Windows 专属的 `kernel32` 语言检测移至 `//go:build windows` 约束的 `lang_windows.go`（其它平台由 `lang_other.go` 占位），使 Linux / macOS 构建成功。

### Previous release（历史版本）

- **0.1.0-alpha** — 初始验证版：本地 DeepSeek V4 system prompt 重组代理。
