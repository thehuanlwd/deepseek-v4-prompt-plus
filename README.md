<div align="center">

# DSPlus

### Unleash the true power of DeepSeek V4

**Enhance instruction following, stabilize long conversations, and reduce hallucinations, format dropouts, reasoning-chain confusion, and tool-call freezes.**

<br>

<p>
  <a href="README.md"><strong>English</strong></a> · <a href="docs/README_zh.md">中文</a>
</p>

<div align="center" markdown="1">

![Go](https://img.shields.io/badge/Go-1.20+-00ADD8?style=for-the-badge&logo=go&logoColor=white)&#160;
![DeepSeek](https://img.shields.io/badge/DeepSeek-V4-4B7BFF?style=for-the-badge)&#160;
![OpenAI Compatible](https://img.shields.io/badge/OpenAI-Compatible-111111?style=for-the-badge&logo=openai&logoColor=white)&#160;
![Anthropic Style](https://img.shields.io/badge/Anthropic-Style-191919?style=for-the-badge)&#160;
![Local Proxy](https://img.shields.io/badge/Local-Proxy-2E7D32?style=for-the-badge)&#160;
![Windows](https://img.shields.io/badge/Windows-GUI-0078D4?style=for-the-badge&logo=windows&logoColor=white)

</div>

<br>

[Quick Start](#quick-start) · [Problems Solved](#part-2-typical-problems-of-deepseek-v4-and-how-dsplus-solves-them) · [Features](#core-features) · [Technical Docs](#part-3-technical-documentation) · [Security](#security--privacy)

<br>
</div>

---

## Part 1: DSPlus at a Glance

I really like the DeepSeek V4 series. It is cheap enough and capable enough to have become my daily driver.

But in real-world use — especially in long conversations, roleplay, complex prompts, coding agents, tool calls, and chain-of-thought (CoT) reasoning — it can also expose some stability issues.

If you have run into these situations too, DSPlus can give you a noticeably better experience.

### Common Symptoms

| Instruction Following Decay | Long-Conversation Degradation | Hallucinations & Logic Drift |
|---|---|---|
| Format dropouts | Empty output | Fabricated settings |
| Failed constraints | Lost focus | Confused timeline |
| Persona drift | Misremembered context | Reversed causality |
| Uncontrolled length | Safety-mode loop | Wrong numeric judgments |

| CoT Confusion | Coding Agent Anomalies | Tool-Call Issues |
|---|---|---|
| Body text leaks into reasoning | Guesses requirements | Tool calls fail |
| Double reasoning chains | Ignores constraints | Freezes up |
| English CoT | Empty content | Repeated calls |
| Confused perspective | Doesn't execute tasks | Stuck in reasoning |



### A Solution Validated by the Community

> A lot of community practice has shown that there is a simple and effective method: **putting the system prompt after the first message** can significantly reduce hallucinations and improve instruction following.
>
> A further approach is to append the system prompt after every message, but that leads to higher token costs and a messy conversation structure.
>
> DSPlus found the best approach: through automatic concatenation, it can append the system prompt to **only the last message** — saving tokens while keeping the conversation structure intact.

### What DSPlus Does

DSPlus is a fully local middle layer:

```text
Your client / IDE / frontend tool
          ↓
       DSPlus
   Local Prompt Guard proxy
          ↓
      DeepSeek V4 API
```

Its job is to make DeepSeek V4 more stable in complex scenarios:

- Follow the system prompt more reliably
- Less likely to drop formatting, persona, or constraints
- Less likely to degrade into empty short replies in long conversations
- Clearer boundaries between CoT / reasoning_content / body text
- Fewer empty replies, freezes, and tool-call anomalies in coding agents
- Automatically intervene, analyze, and retry when reasoning runs too long or gets stuck in a loop
- Optional intent-confirmation mechanism for some logic errors

### Core Features

| Capability | Summary |
|---|---|
| Prompt Guard | Enhances the persistent influence of system prompts, formatting requirements, persona settings, and prohibitions |
| Instruction-Following Boost | Mitigates rule decay, format dropouts, failed constraints, and persona drift after multiple turns |
| Long-Conversation Stability | Mitigates empty output, scattered attention, and recent-memory loss after long context |
| CoT Stabilization | Reduces boundary confusion between reasoning_content, body text, and tool calls |
| Empty Content Repair | Fixes empty assistant content after tool calls so the IDE can see the reply |
| Anti-Loop | Detects excessive reasoning, looping reasoning, and truncated output, then analyzes and retries automatically |
| Intent Confirmation | Experimental feature to mitigate some logic hallucinations, but at a higher token cost |
| Local GUI | Visual configuration, request logs, tokens, cache hits, retries, and basic diagnostics |

### UI Preview

> Screenshots are in `docs/images/`. Some `.webp` files may be animated demos.

<div align="center">

<table style="border-collapse:separate;border-spacing:5px;margin:0 auto">
  <tr>
    <td width="50%" align="center" style="padding:0"><img src="docs/images/DASHBOARD.jpg" alt="DSPlus Dashboard"></td>
    <td width="50%" align="center" style="padding:0"><img src="docs/images/SETTINGS.jpg" alt="DSPlus Settings"></td>
  </tr>
  <tr>
    <td align="center" style="padding:0"><b>Live Dashboard</b></td>
    <td align="center" style="padding:0"><b>System Settings</b></td>
  </tr>
  <tr>
    <td width="50%" align="center" style="padding:0"><img src="docs/images/ANALYSIS.jpg" alt="DSPlus Analysis"></td>
    <td width="50%" align="center" style="padding:0"><img src="docs/images/Classic%20(GitHub%20Dark).jpg" alt="DSPlus Classic Theme"></td>
  </tr>
  <tr>
    <td align="center" style="padding:0"><b>Basic Diagnostic Analysis</b></td>
    <td align="center" style="padding:0"><b>Classic GitHub Dark Theme</b></td>
  </tr>
</table>

<br>

<table style="border-collapse:separate;border-spacing:5px;margin:0 auto">
  <tr>
    <td width="50%" align="center" style="padding:0"><img src="docs/images/DASHBOARD.webp" alt="DSPlus Dashboard Demo"></td>
    <td width="50%" align="center" style="padding:0"><img src="docs/images/PixPin_2026-07-07_19-43-25.webp" alt="DSPlus Demo"></td>
  </tr>
  <tr>
    <td align="center" style="padding:0"><b>Animated Demo 1</b></td>
    <td align="center" style="padding:0"><b>Animated Demo 2</b></td>
  </tr>
</table>

</div>

### Before / After

A qualitative comparison of what DSPlus changes. Quantitative benchmarks are being built (see Roadmap).

| Scenario | Without DSPlus | With DSPlus |
|---|---|---|
| Unsupported citation / fact request | Invents sources, papers, links | Prompt Guard strengthens constraints, fewer unsupported claims |
| Long conversation | Persona / setting drift, empty or short output | Reorganized system prompt keeps behavioral boundaries |
| After tool call | `content: ""`, IDE shows nothing | Empty content auto-repaired, breaks the silent-reply loop |
| Coding agent | Ignores constraints, repeats calls, freezes | Stage-aware injection; Anti-Loop retry on stuck reasoning |
| Reasoning loop | Re-verifies same conclusion, truncated by max_tokens | Anti-Loop captures → analyzes → retries with guidance |
| Logic drift | Treats unspoken things as facts | Intent confirmation (experimental) re-aligns before answering |

### Quick Start

> DSPlus is fundamentally a local HTTP proxy. It **runs without the GUI** and needs **no prebuilt EXE**: it is just a Go program you can run from source or compile into an executable for any platform. The GUI is only an optional web dashboard on the same port (see [Startup Methods](#startup-methods)).

Run (either one):

```batch
go run .                 # run from source, no build needed
go build -o dsplus .     # build an executable for your platform (dsplus.exe on Windows)
./dsplus                 # run the built binary
```

Default address:

```text
http://127.0.0.1:8188
```

Then change the API Base URL in your client to:

```text
http://127.0.0.1:8188
```

Fill in your DeepSeek API Key on the DSPlus settings page.

---

## Part 2: Typical Problems of DeepSeek V4 and How DSPlus Solves Them

DeepSeek V4 is a very capable and very cheap model. DSPlus is not designed to deny that, but to enhance its high-frequency stability problems in complex real-world scenarios through a local proxy layer.

### 1. Weak Instruction Following

This is the most frequent problem.

#### Typical Symptoms

- Format dropouts
- Failed constraints
- Forgotten persona
- Uncontrolled length
- Rule decay after multiple turns
- First-turn style polluting later output

#### How DSPlus Solves It

Before a request reaches DeepSeek V4, DSPlus reorganizes the system prompt and extra rules so that key constraints more reliably enter the model's effective context.

Three strategies are supported:

| Strategy | Suitable For |
|---|---|
| After the first user message | Default mode, balances stability with cache hits |
| After the last user message | Emphasizes the latest constraints; good for short context or strong-control scenarios |
| No modification | Keeps the original request structure and only uses other enhancement capabilities |

It also supports injecting an extra high-priority prompt, which can be used for:

- Fixed output format
- Persona settings
- Prohibited items
- Global behavior guidelines
- Project-level conventions
- Coding Agent work rules

### 2. Context and Long-Conversation Degradation

Long conversations are among the most common problems for DeepSeek V4 users.

#### Typical Symptoms

- Lower information density
- Shorter, emptier output
- Scattered attention
- Recent-memory loss
- Persona and setting drift
- Falling into rigid safety mode
- Overly strong first-turn output inertia

#### How DSPlus Solves It

DSPlus does not break the model's own context limit, but it tries to reduce the behavioral drift caused by long conversations.

It does so through:

- Reorganizing the system prompt
- Strengthening global constraints
- Controlling the thinking parameter
- Controlling max_tokens
- Fixing CoT / content boundaries
- Triggering Anti-Loop on abnormal reasoning

to help DeepSeek V4 maintain more stable behavioral boundaries in long context.

### 3. CoT / Reasoning-Chain Confusion

Reasoning models easily develop confused boundaries between reasoning_content and body text in tool calls, multi-turn history, and complex clients.

#### Typical Symptoms

- Body text written into the reasoning chain
- Double reasoning chains
- English CoT
- Reasoning-chain hallucinations
- Confused reasoning-chain perspective
- Client cannot see the formal reply

#### How DSPlus Solves It

DSPlus provides several CoT-related fixes:

| Feature | Effect |
|---|---|
| reasoning_content auto-completion | Reduces tool-call format compatibility issues |
| Empty content auto-repair | Backfills the most recent empty assistant content with reasoning |
| thinking mode control | Optionally leave unset, force off, or force on thinking |
| Independent retry thinking config | Set thinking and effort separately on Anti-Loop retries |
| Streaming capture | Captures reasoning / content to judge whether something is abnormal |

Empty content repair is important for coding agents.

Some IDEs or agent clients do not display reasoning_content. If `content: ""` keeps appearing in history, the model may imitate this pattern, causing persistent empty replies afterward. DSPlus can break this feedback loop from the request side.

### 4. Coding Agent and Tool-Call Anomalies

In tools like Claude Code, OpenCode, the OpenAI SDK, and Anthropic-style clients, DeepSeek V4's problems become more pronounced, because these scenarios demand more of formatting, tool calls, state management, and instruction following.

#### Typical Symptoms

- Tool calls fail
- Freezes after tool return
- Repeated calls to the same tool
- Keeps analyzing when it should execute
- Empty content causes no IDE output
- Guesses files, guesses requirements, ignores constraints
- CoT and body text misaligned after tool calls

#### How DSPlus Solves It

DSPlus identifies different stages such as conversation, tool calls, and tool returns, and tries to avoid injecting inappropriate content during the tool-return stage, reducing the risk of breaking the client's protocol structure.

At the same time, it improves stability in coding agents through:

- Prompt Guard
- Empty content repair
- reasoning_content compatibility
- Anti-Loop
- Request logs and token tracking

### 5. Hallucinations and Logic Drift

Logic hallucinations are a harder problem. DSPlus currently optimizes the first three categories — instruction following, long-conversation degradation, and CoT confusion — more noticeably.

For logic hallucinations, DSPlus provides an experimental intent-confirmation mechanism.

#### Typical Symptoms

- Fabricated settings
- Confused timeline
- Reversed causality
- Wrong numeric judgments
- Wrong character position
- Treating things the user never said as facts

#### Intent Confirmation Mechanism

Intent confirmation re-injects the latest user intent after the model finishes thinking and before it outputs the formal answer, so the model re-aligns with the current question one last time before the final output.

It may mitigate:

- Going off topic
- Context drift
- Some logic drift
- Some hallucination-type errors

But it also has a clear cost:

```text
Token consumption may nearly double.
```

So it is not a default recommended feature, and is better suited for scenarios where accuracy is the priority and higher cost is acceptable.

### 6. Anti-Loop (Infinite Thinking Prevention)

DeepSeek V4 sometimes stays in the reasoning stage for a long time on complex tasks.

#### Typical Symptoms

- Keeps reasoning, never outputs body text
- Repeatedly verifies the same conclusion
- Overly cautious, afraid to proceed
- Output truncated by length / max_tokens
- Keeps analyzing in coding agents, never executes tasks

#### How DSPlus Solves It

Anti-Loop will:

1. Capture streaming or non-streaming responses.
2. Record reasoning, content, and finish_reason.
3. Start analysis when a threshold is reached or output is truncated.
4. Judge whether it is a loop, excessive, or normal.
5. Construct a retry request with guidance.
6. Use an independent retry model and thinking config.
7. Return a clear fallback message if it exceeds the limit again.

---

## Part 3: Technical Documentation

This part is for power users, developers, and anyone who needs to audit / deploy / build it themselves.

### How It Works

DSPlus is a local HTTP proxy. It receives client requests, enhances them per configuration, and forwards them to the DeepSeek API.

```text
Client request
  ↓
Format detection
  ↓
Semantic stage recognition
  ↓
System Prompt reorganization
  ↓
Extra Prompt injection
  ↓
thinking / max_tokens parameter handling
  ↓
reasoning_content / empty content repair
  ↓
Forward to DeepSeek API
  ↓
Streaming capture and logging
  ↓
Optional Anti-Loop / intent confirmation / auto retry
  ↓
Return to client
```

### Supported API Styles

| Style | Recognition | Default Upstream |
|---|---|---|
| OpenAI style | `messages` array, `role` field, `/chat/completions` path | `https://api.deepseek.com` |
| Anthropic style | Top-level `system`, `/v1/messages`, compatible message structure | `https://api.deepseek.com/anthropic` |

DSPlus tries to transparently pass through unrecognized or unmodified requests.

### Project Structure

```text
DSPlus/
├── main.go              # Program entry, service start, restart, GUI open
├── config.go            # Config structure, defaults, language detection, safe read/write
├── transform.go         # System Prompt reorganization, supports OpenAI / Anthropic styles
├── proxy.go             # HTTP proxy core, request handling, parameter injection, streaming forward
├── retry.go             # Anti-Loop analysis, guided retry, retry request construction
├── analysis.go          # Session analysis, Session / Turn aggregation, Markdown export
├── logger.go            # Real-time logs, token stats, WebSocket broadcast
├── trace.go             # Anti-loop trace log
├── gui.go               # Web GUI and internal REST API
├── gui_webview.go       # WebView2 desktop window and tray in CGO mode
├── gui_fallback.go      # Browser fallback in non-CGO mode
├── ws.go                # WebSocket real-time push
├── web/
│   ├── index_v2.html    # Current main UI
│   ├── app_v2.js        # Frontend business logic
│   ├── index_v2.css     # Main styles
│   ├── theme_yorha.css  # YoRHa theme
│   ├── theme_classic.css# Classic theme
│   ├── i18n.js          # Internationalization logic
│   └── locales/         # zh / en language files
├── docs/                # Design docs, specs, and issue records
├── docs/images/         # README screenshots and demo images
├── go.mod / go.sum      # Go module dependencies
├── build.bat            # Windows build script
└── README.md            # Current document
```

### Startup Methods

DSPlus is a Go-based local proxy. It **needs no prebuilt EXE and can run as a proxy without the GUI**. The GUI is only an optional web dashboard on the same port (`http://127.0.0.1:8188/`) — proxying does not depend on it.

#### Run directly from source (no EXE)

```batch
go run .
```

#### Build it yourself (any platform)

```batch
go build -o dsplus .     # generates dsplus.exe on Windows
./dsplus                 # Linux / macOS
dsplus.exe               # Windows
```

> The `DSPlus.exe` in the repo is just a prebuilt Windows example; you can `go build` an executable for your own platform with any name.

#### Three run modes

| Mode | Build | GUI behavior | When to use |
|---|---|---|---|
| Desktop GUI (embedded window) | `CGO_ENABLED=1 go build ...` | Auto-opens a WebView2 window; closes to tray | Windows desktop, want visual config |
| Desktop GUI (browser) | `CGO_ENABLED=0 go build ...` | Auto-opens your default browser to the dashboard | No CGO toolchain, still want GUI |
| Pure proxy / headless | Same, just run the binary | The GUI dashboard is an optional page on the same port — **not opening it does not affect proxying**; on headless environments (server / container) the GUI simply does not open | Server, CI, transparent proxy only |

In every mode the proxy listens and forwards on `http://127.0.0.1:8188`; the GUI is just an extra observability / configuration entry point.

### Quick Start

#### 1. Start DSPlus

```batch
dsplus                  # or dsplus.exe; or just go run .
```

Default listen address:

```text
http://127.0.0.1:8188
```

Specify a port:

```batch
DSPlus.exe --port=9999
```

The local GUI opens automatically after startup.

#### 2. Configure the DeepSeek API Key

Open the GUI, go to "System Settings", and fill in the DeepSeek API Key.

The API Key is only saved locally in `config.json`.

#### 3. Change the Client Base URL

Change the client API address to:

```text
http://127.0.0.1:8188
```

The actual request is forwarded to the DeepSeek API by DSPlus.

### Client Integration Examples

#### OpenAI SDK / OpenAI-style Client

```python
from openai import OpenAI

client = OpenAI(
    api_key="any-value",
    base_url="http://127.0.0.1:8188"
)
```

#### Anthropic-style Client

```batch
set ANTHROPIC_BASE_URL=http://127.0.0.1:8188
set ANTHROPIC_AUTH_TOKEN=any-value
```

#### Cherry Studio / ChatBox / Open WebUI

Fill in the API settings:

```text
API Base URL: http://127.0.0.1:8188
API Key: any value or the placeholder required by the client
```

The DeepSeek API Key is configured on the DSPlus settings page.

### Main Configuration Items

The config file is in the same directory as `DSPlus.exe`:

```text
config.json
```

| Field | Description |
|---|---|
| `api_key` | DeepSeek API Key, saved locally |
| `port` | Local listen port, default 8188 |
| `lan_access` | Whether to allow LAN / WSL access |
| `openai_upstream` | OpenAI-style upstream address |
| `anthropic_upstream` | Anthropic-style upstream address |
| `language` | GUI language, supports zh / en |
| `thinking_mode` | Unset, force thinking off, or force thinking on |
| `reasoning_effort` | Reasoning intensity: `minimal` / `low` / `medium` / `high` / `xhigh` / `max` / `ultra` |
| `system_prompt_placement` | System Prompt concatenation position, first / last / none |
| `extra_prompt` | Extra high-priority instruction |
| `extra_prompt_placement` | Extra Prompt injection position, first / last / none |
| `max_tokens_mode` | No change, 5000, 32000, or custom |
| `max_tokens_custom` | Custom max_tokens value |
| `auto_reasoning_content` | Auto-complete reasoning_content |
| `auto_fix_empty_content` | Auto-repair empty assistant content |
| `anti_loop_enabled` | Whether to enable Anti-Loop |
| `antiloop_retry_model` | Anti-Loop retry model |
| `antiloop_retry_thinking` | Whether to enable thinking on retry |
| `antiloop_retry_effort` | Reasoning intensity on retry: `minimal` / `low` / `medium` / `high` / `xhigh` / `max` / `ultra` |
| `antiloop_check_tokens` | Active loop-detection threshold, 0 means only rely on truncation fallback |
| `anti_hallucination_enabled` | Whether to enable experimental intent confirmation |
| `anti_hallucination_prompt` | Intent-confirmation prompt template |
| `analysis_enabled` | Whether to enable local analysis records |
| `analysis_retention_days` | Analysis log retention days |
| `verbose_logging` | Whether to keep detailed request / response content in real-time logs |
| `debug_mode` | Whether to enable debug information |

### Local API Endpoints

DSPlus serves both the proxy and GUI on a single port.

| Path | Purpose |
|---|---|
| `/chat/completions` | OpenAI-style proxy entry |
| `/v1/chat/completions` | OpenAI-style proxy entry |
| `/v1/messages` | Anthropic-style proxy entry |
| Other non-GUI paths | Pass through to the corresponding upstream by format |
| `/` | Local GUI home |
| `/api/status` | Service status |
| `/api/logs` | Real-time request logs, supports `limit` / `offset` |
| `/api/logs/{id}` | Single request detail |
| `/api/config` | Get / save config |
| `/api/restart` | Restart local service |
| `/api/analysis/status` | Analysis service status |
| `/api/analysis/sessions` | Analysis session list |
| `/api/analysis/sessions/{id}` | Session detail |
| `/api/analysis/sessions/{id}/timeline` | Session timeline pagination |
| `/api/analysis/sessions/{id}/export.md` | Export Markdown diagnostic report |
| `/ws` | WebSocket real-time log push |

### Build

This project is developed in Go.

#### No CGO Build

No C compiler needed; the GUI opens in a browser.

```batch
set CGO_ENABLED=0
go build -ldflags="-s -w" -o DSPlus.exe .
```

#### CGO Build

Requires an available C compilation environment, e.g. MinGW. The build then supports the embedded WebView2 window and tray behavior.

```batch
set CGO_ENABLED=1
go build -ldflags="-H windowsgui -s -w" -o DSPlus.exe .
```

#### Test

```batch
go test ./...
```

Run a specific test:

```batch
go test -v -run TestTransformOpenAIInPlace ./...
```

### Security & Privacy

DSPlus is a local proxy, but it processes your complete request and response content. Configure the logging options according to your own privacy needs.

| Item | Description |
|---|---|
| API Key | Saved locally in `config.json`; do not commit to public repos |
| Request content | May contain private data, business data, code, or prompts |
| verbose_logging | When on, GUI request details keep more complete request / response content |
| analysis_enabled | When on, generates local analysis history for session viewing and export |
| LAN Access | When on, listens on `0.0.0.0`; only recommended on trusted networks |
| Log files | Anti-loop traces and analysis history may be written to local files; clean them periodically |

Suggestions:

- Do not commit `config.json`
- Do not expose log files and analysis reports
- Do not enable LAN access on untrusted networks
- Before sharing screenshots, check for API Keys, private code, customer info, or private prompts

### Transparency and Auditability

DSPlus's core logic is concentrated in a small number of Go files, making it easy to audit.

| Capability | Main Files |
|---|---|
| System Prompt reorganization | `transform.go` |
| Request proxy and parameter injection | `proxy.go` |
| Anti-Loop and retry | `retry.go` |
| Config and defaults | `config.go` |
| Logging and real-time updates | `logger.go`, `ws.go` |
| Session analysis and export | `analysis.go` |
| GUI and internal API | `gui.go`, `web/` |

You can read these files directly to confirm what DSPlus does and does not do.

### Current Limitations

- DSPlus does not replace DeepSeek V4.
- DSPlus does not break the model's own context window.
- DSPlus does not connect to the internet for fact-checking.
- DSPlus does not guarantee complete elimination of hallucinations.
- Intent confirmation is still in testing and may significantly increase token consumption.
- Anti-Loop generates extra analysis and retry requests, which may increase latency and cost.
- Basic diagnostic analysis is currently mainly for local troubleshooting and is not advertised as an intelligent diagnostic system.

### Roadmap

Planned directions:

- More mature intent-confirmation strategy
- Lower token cost for intent confirmation
- Stronger logic-drift mitigation
- Agent / Workflow-oriented intelligent diagnostic analysis
- Automatic analysis of time spent in each stage
- Token distribution analysis and cost localization
- More client integration docs
- More Before / After cases

### FAQ

**Can DSPlus completely eliminate DeepSeek V4 hallucinations?**

No. DSPlus cannot guarantee DeepSeek V4 will never hallucinate. It is designed to reduce hallucinations through better prompt structure, stronger system prompt following, and reasoning-loop detection.

**How does DSPlus reduce DeepSeek V4 hallucinations?**

DSPlus is a local API proxy. It restructures requests, strengthens system prompt following, adds anti-hallucination guardrails, and retries unstable responses when reasoning loops are detected.

**Does DSPlus work with SillyTavern / roleplay?**

Yes. DSPlus helps reduce persona drift, setting fabrication, and repetitive output in long roleplay contexts.

**Is DSPlus good for coding agents?**

Yes. DSPlus helps coding agents follow the system prompt more reliably and reduces unsupported assumptions and instruction drift on long tasks.

**Does DSPlus connect to the internet for fact-checking?**

No. DSPlus is a local proxy and does not perform online fact-checking.

**What about token cost?**

Most features add little overhead. Intent confirmation (experimental) may nearly double token usage, so it is off by default.

**Where is my API Key stored?**

Locally in `config.json` next to `DSPlus.exe`. Do not commit it to public repositories.

### License

Please refer to the actual License file in the repository. If you plan to release it publicly, it is recommended to add a clear open-source license.
