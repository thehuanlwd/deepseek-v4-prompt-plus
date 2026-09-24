package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ── Tuning constants ─────────────────────────────────────────────────────────

const (
	// HTTP transport.
	dialTimeout           = 30 * time.Second
	keepAlive             = 30 * time.Second
	maxIdleConns          = 100
	idleConnTimeout       = 90 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	expectContinueTimeout = 1 * time.Second

	// Streaming / buffering.
	scannerInitialBuf = 65536    // 64 KB
	scannerMaxBuf     = 10485760 // 10 MB
	captureBufCap     = 10485760 // 10 MB

	// Logging.
	truncateBodyMaxLen = 5242880 // 5 MB
)

// ProxyContext 封装代理请求转发的上下文参数，用于扁平化跨函数参数传递
type ProxyContext struct {
	ResponseWriter  http.ResponseWriter
	Response        *http.Response
	LogID           int64
	StartTime       time.Time
	Format          string
	Route           string
	OriginalBody    string
	TransformedBody []byte
	Transformed     bool
	Data            map[string]interface{}
	SessionID       string
	TurnID          int
	SemanticType    string
	HasTools        bool // 是否携带工具（防幻觉保护伞使用）
}

type ProxyServer struct {
	config      *SafeConfig
	logger      *Logger
	analysisSvc *AnalysisService
	client      *http.Client
}

func NewProxyServer(cfg *SafeConfig, l *Logger, svc *AnalysisService) *ProxyServer {
	return &ProxyServer{
		config:      cfg,
		logger:      l,
		analysisSvc: svc,
		client: &http.Client{
			Timeout: 0, // no timeout (streaming)
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: keepAlive,
				}).DialContext,
				MaxIdleConns:          maxIdleConns,
				IdleConnTimeout:       idleConnTimeout,
				TLSHandshakeTimeout:   tlsHandshakeTimeout,
				ExpectContinueTimeout: expectContinueTimeout,
			},
		},
	}
}

func (s *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/favicon.ico" {
		exe, err := os.Executable()
		if err == nil {
			iconPath := filepath.Join(filepath.Dir(exe), "favicon.ico")
			if _, err := os.Stat(iconPath); err == nil {
				http.ServeFile(w, r, iconPath)
				return
			}
		}
		http.NotFound(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/index_v2.html" ||
		strings.HasSuffix(r.URL.Path, ".css") || strings.HasSuffix(r.URL.Path, ".js") ||
		strings.HasSuffix(r.URL.Path, ".png") || strings.HasSuffix(r.URL.Path, ".svg") ||
		strings.HasSuffix(r.URL.Path, ".json") {
		handleGUI(w, r, s.logger, s.config, s.analysisSvc)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/ws") {
		handleWebSocket(w, r, s.logger)
		return
	}

	s.handleProxy(w, r)
}

func (s *ProxyServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	// 拦截客户端获取模型列表请求：若当前供应商配置了自定义/指定模型，直接返回本地模型清单；为空时继续穿透至上游
	cleanPath := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == "GET" && (cleanPath == "/v1/models" || cleanPath == "/models") {
		if p, ok := s.activeProvider(); ok && len(p.Models) > 0 {
			type modelItem struct {
				ID      string `json:"id"`
				Object  string `json:"object"`
				Created int64  `json:"created"`
				OwnedBy string `json:"owned_by"`
			}
			var list []modelItem
			now := time.Now().Unix()
			for _, m := range p.Models {
				ownedBy := m.Group
				if ownedBy == "" {
					ownedBy = p.Name
				}
				list = append(list, modelItem{
					ID:      m.ID,
					Object:  "model",
					Created: now,
					OwnedBy: ownedBy,
				})
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data":   list,
			})
			return
		}
	}

	startTime := time.Now()
	cfg := s.config.Get()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	originalBody := string(body)

	format := detectFormat(r.URL.Path, originalBody)
	upstream := s.selectUpstream(format)

	var transformed bool
	var hasSystemPrompt bool
	var transformedBody []byte

	// Parse the request body once; all subsequent modifications work
	// on the parsed map in-place to avoid redundant marshal/unmarshal.
	var data map[string]interface{}
	if json.Unmarshal(body, &data) != nil {
		// Not valid JSON — pass through as-is
		data = nil
	}

	// 在转换之前检测语义类型（转换会修改 data 中的 messages）
	semanticType := detectSemanticType(data, format)

	if format == "openai" {
		hasSystemPrompt = strings.Contains(originalBody, `"role":"system"`) ||
			strings.Contains(originalBody, `"role": "system"`)
		// 工具结果回传阶段（tool_result）跳过转换注入，保持 tool_result 纯净，
		// 与 Anthropic 分支行为一致，避免向工具结果的 user 消息注入系统/额外提示词。
		if data != nil && semanticType != "tool_result" && (hasSystemPrompt || (cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none")) {
			transformed = transformOpenAIInPlace(data, cfg.SystemPromptPlacement, cfg.ExtraPrompt, cfg.ExtraPromptPlacement)
		}
	} else if format == "anthropic" {
		hasSystemPrompt = strings.Contains(originalBody, `"system":`)
		// 如果是工具结果回传阶段（tool_result），跳过 Anthropic 转换注入，防止多余注入破坏 tool_result 纯净格式导致 400
		if data != nil && semanticType != "tool_result" && (hasSystemPrompt || (cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none")) {
			transformed = transformAnthropicInPlace(data, cfg.SystemPromptPlacement, cfg.ExtraPrompt, cfg.ExtraPromptPlacement)
		}
	}

	// Apply thinking / max_tokens injections on the already-parsed map.
	if data != nil && (format == "openai" || format == "anthropic") {
		s.injectThinkingParams(data, format)
		s.injectMaxTokens(data)
	}

	if data != nil && cfg.AutoReasoningContent {
		injectReasoningContent(data)
	}

	if data != nil && cfg.AutoFixEmptyContent {
		fixEmptyAssistantContent(data)
	}

	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			transformedBody = b
		} else {
			transformedBody = body
			transformed = false
		}
	} else {
		transformedBody = body
	}

	// 构建请求审计：对照「设置页面配置」与「重组后请求体实际生效」，供自检脚本核对。
	audit := buildRequestAudit(data, format, cfg)

	upstreamURL := joinUpstream(upstream, r.URL.Path)
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(transformedBody))
	if err != nil {
		http.Error(w, "Failed to create upstream request", http.StatusInternalServerError)
		return
	}

	copyHeaders(proxyReq.Header, r.Header)
	key := s.activeAPIKey()
	proxyReq.Header.Set("Authorization", "Bearer "+key)
	if format == "anthropic" {
		// Anthropic 协议（含 DeepSeek / opencode.ai 的 Anthropic 兼容端点）用 x-api-key 认证。
		proxyReq.Header.Set("x-api-key", key)
	} else {
		proxyReq.Header.Del("x-api-key")
	}
	proxyReq.Header.Set("Content-Length", strconv.Itoa(len(transformedBody)))
	proxyReq.Host = ""

	// ── 前置创建连接中的日志 ──
	isStreamReq := false
	if data != nil {
		if s, ok := data["stream"].(bool); ok {
			isStreamReq = s
		}
	}
	logID := s.logger.Add(LogEntry{
		Time:            startTime,
		Format:          format,
		Method:          r.Method,
		Path:            r.URL.Path,
		StatusCode:      0,
		LatencyMs:       0,
		Stream:          isStreamReq,
		Transformed:     transformed,
		HasSystemPrompt: hasSystemPrompt,
		SemanticType:    semanticType,
		OriginalBody:    condStr(cfg.VerboseLogging, truncateBody(originalBody), ""),
		TransformedBody: condStr(cfg.VerboseLogging, truncateBody(string(transformedBody)), ""),
		Status:          "connecting",
		RequestAudit:    audit,
	})

	// 持久化请求审计，供自检脚本读取「最近一次」请求核对一致性。
	s.logger.WriteAudit(audit)

	resp, err := s.client.Do(proxyReq)
	if err != nil {
		log.Printf("[proxy] upstream error: %v", err)
		http.Error(w, "Upstream connection failed", http.StatusBadGateway)
		s.logger.UpdateOnResponse(logID, 502, time.Since(startTime).Milliseconds(), "completed", nil, "", "")
		return
	}
	defer resp.Body.Close()

	respHeaders := make(map[string]string)
	for k, vv := range resp.Header {
		if len(vv) > 0 {
			respHeaders[k] = strings.Join(vv, ", ")
		}
	}

	isStream := resp.Header.Get("Content-Type") == "text/event-stream" ||
		strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")

	// ── Assign Session and Turn upfront ──
	var sessID string
	var turnID int
	if cfg.AnalysisEnabled {
		sessID, turnID = s.analysisSvc.AssignSessionAndTurn(startTime, originalBody, format, r.URL.Path)
	}

	hasTools := false
	if data != nil {
		if t, ok := data["tools"]; ok && t != nil {
			if arr, isArr := t.([]interface{}); isArr && len(arr) > 0 {
				hasTools = true
			}
		}
		if f, ok := data["functions"]; ok && f != nil {
			if arr, isArr := f.([]interface{}); isArr && len(arr) > 0 {
				hasTools = true
			}
		}
	}

	ctx := &ProxyContext{
		ResponseWriter:  w,
		Response:        resp,
		LogID:           logID,
		StartTime:       startTime,

		Format:          format,
		Route:           r.URL.Path,
		OriginalBody:    originalBody,
		TransformedBody: transformedBody,
		Transformed:     transformed,
		Data:            data,
		SessionID:       sessID,
		TurnID:          turnID,
		SemanticType:    semanticType,
		HasTools:        hasTools,
	}

	// ── Anti-Loop detection ──
	if cfg.AntiLoopEnabled {
		if isStream {
			// Streaming: forward chunks immediately, retry if truncated
			latency := time.Since(startTime)
			s.logger.UpdateOnResponse(logID, resp.StatusCode, latency.Milliseconds(), "connecting", respHeaders, "", "")
			copyHeaders(w.Header(), resp.Header)
			w.WriteHeader(resp.StatusCode)
			s.forwardStreamWithAntiLoop(ctx)
			return
		}

		// Non-streaming: buffer and retry (no streaming UX issue)
		bodyBytes, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			log.Printf("[proxy] read upstream body: %v", readErr)
			http.Error(w, "Failed to read upstream response", http.StatusBadGateway)
			return
		}

		fr := detectBufferFinishReason(bodyBytes)
		if fr == "length" || fr == "max_tokens" {
			log.Printf("[antiloop] non-stream finish_reason=%s, triggering retry", fr)
			bodyBytes = s.handleAntiLoop(transformedBody, format, "")
			fr = detectBufferFinishReason(bodyBytes)
		}

		hasToolsInResponse := len(parseAssistantToolCalls(string(bodyBytes), format)) > 0
		finalAction := s.updateFinalSemanticAction(logID, semanticType, hasTools, hasToolsInResponse, fr)
		semanticType = finalAction

		latency := time.Since(startTime)
		s.logger.UpdateOnResponse(logID, resp.StatusCode, latency.Milliseconds(), "connecting", respHeaders, "", "")

		var pm TokenUsage
		if u := extractUsageFromBody(string(bodyBytes)); u != nil {
			s.logger.UpdateTokenUsageAndStatus(logID, u, "completed")
			pm = *u
		} else {
			s.logger.UpdateTokenUsageAndStatus(logID, nil, "completed")
		}
		if cfg.VerboseLogging {
			s.logger.UpdateLastResponse(logID, truncateBody(string(bodyBytes)))
		}

		// 发射非流式 TraceEvent
		traceEv := NewTraceEvent(
			startTime,
			logID,
			sessID,
			turnID,
			"primary",
			format,
			r.URL.Path,
			resp.StatusCode,
			latency.Milliseconds(),
			detectModel(originalBody),
			upstream,
			buildRequestMeta(data, format, transformed, cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none", semanticType),
			ResponseMeta{
				FinishReason:     detectBufferFinishReason(bodyBytes),
				PromptTokens:     int(pm.Prompt),
				CompletionTokens: int(pm.Completion),
				TotalTokens:      int(pm.Total),
				CacheHitTokens:   int(pm.CacheHit),
				CacheMissTokens:  int(pm.CacheMiss),
				ReasoningContent: parseAssistantReasoning(string(bodyBytes), format),
				Content:          parseAssistantContent(string(bodyBytes), format),
			},
			originalBody,
			string(bodyBytes),
		)
	traceEv.UpstreamRequest = string(transformedBody)
	// 非流式整轮完成：回填总耗时（首响 latency_ms 已在上方 connecting 阶段记录）
	s.logger.UpdateTotalMs(logID, time.Since(startTime).Milliseconds())
	s.analysisSvc.SubmitEvent(traceEv)

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	w.Write(bodyBytes)
	return
	}

	// ── Normal path (anti-loop disabled): stream/buffer to client immediately ──
	latency := time.Since(startTime)
	s.logger.UpdateOnResponse(logID, resp.StatusCode, latency.Milliseconds(), "connecting", respHeaders, "", "")

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if isStream {
		s.forwardStream(ctx)
	} else {
		s.forwardBuffered(ctx)
	}
}

// sanitizeReasoningEffort 归一化推理强度档位。
// Vercel / OpenRouter / OpenAI 兼容网关仅接受 "none" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max"。
// 若配置或客户端携带 "ultra"，自动安全映射至最高合法枚举 "max"，避免触发 400 invalid_request_error。
func sanitizeReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "ultra":
		return "max"
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return effort
	}
}

func (s *ProxyServer) injectThinkingParams(data map[string]interface{}, format string) {
	cfg := s.config.Get()
	mode := cfg.ThinkingMode
	if mode == "" {
		// 客户端未被服务端强制覆盖时，若客户端自身携带了 "ultra" 等非法枚举，也进行安全归一化
		if eff, ok := data["reasoning_effort"].(string); ok {
			data["reasoning_effort"] = sanitizeReasoningEffort(eff)
		}
		if reasoning, ok := data["reasoning"].(map[string]interface{}); ok {
			if eff, ok := reasoning["effort"].(string); ok {
				reasoning["effort"] = sanitizeReasoningEffort(eff)
			}
		}
		if outputCfg, ok := data["output_config"].(map[string]interface{}); ok {
			if eff, ok := outputCfg["effort"].(string); ok {
				outputCfg["effort"] = sanitizeReasoningEffort(eff)
			}
		}
		return
	}

	if format == "openai" {
		if mode == "disabled" {
			data["thinking"] = map[string]interface{}{"type": "disabled"}
			data["reasoning_effort"] = "none"
			data["reasoning"] = map[string]interface{}{"effort": "none"}
		} else if mode == "enabled" {
			data["thinking"] = map[string]interface{}{"type": "enabled"}
			if cfg.ReasoningEffort != "" {
				eff := sanitizeReasoningEffort(cfg.ReasoningEffort)
				data["reasoning_effort"] = eff
				data["reasoning"] = map[string]interface{}{"effort": eff}
			}
		}
	} else if format == "anthropic" {
		if mode == "disabled" {
			// 强制关闭思考：清理可能残留的 thinking / output_config / reasoning_effort / reasoning，
			// 否则原始请求自带的思考字段会导致思考仍被启用（与 OpenAI 行为一致）。
			data["thinking"] = map[string]interface{}{"type": "disabled"}
			delete(data, "output_config")
			delete(data, "reasoning_effort")
			delete(data, "reasoning")
		} else if mode == "enabled" {
			delete(data, "reasoning_effort")
			delete(data, "reasoning")
			if cfg.ReasoningEffort != "" {
				data["output_config"] = map[string]interface{}{"effort": sanitizeReasoningEffort(cfg.ReasoningEffort)}
			}
		}
	}
}

func (s *ProxyServer) injectMaxTokens(data map[string]interface{}) {
	cfg := s.config.Get()
	mode := cfg.MaxTokensMode
	switch mode {
	case "":
		return // 不修改：保持原始请求不变
	case "off":
		// 不发送：强制删除 max_tokens 参数（即使原始请求携带也移除）。
		delete(data, "max_tokens")
		return
	}

	var maxTokens int
	switch mode {
	case "5000":
		maxTokens = 5000
	case "32000":
		maxTokens = 32000
	case "384000":
		maxTokens = 384000
	case "custom":
		maxTokens = cfg.MaxTokensCustom
	default:
		return
	}

	// 所有档位（含自定义）统一上限 384000，不得超过。
	const maxTokensHardCap = 384000
	if maxTokens > maxTokensHardCap {
		maxTokens = maxTokensHardCap
	}

	if maxTokens <= 0 {
		return
	}

	data["max_tokens"] = maxTokens
}

// buildRequestAudit 解析重组后的请求体，对照设置页面配置生成审计记录。
// 返回的 RequestAudit 同时包含「意图配置」(Cfg*) 与「实际生效」(Actual*)，
// 供自检脚本核对两者是否一致。
func buildRequestAudit(data map[string]interface{}, format string, cfg Config) *RequestAudit {
	a := &RequestAudit{
		Time:                     time.Now(),
		Format:                   format,
		CfgSystemPromptPlacement: cfg.SystemPromptPlacement,
		CfgExtraPromptPlacement:  cfg.ExtraPromptPlacement,
		CfgExtraPromptEmpty:      cfg.ExtraPrompt == "",
		CfgThinkingMode:          cfg.ThinkingMode,
		CfgReasoningEffort:       cfg.ReasoningEffort,
		CfgMaxTokensMode:         cfg.MaxTokensMode,
	}

	if data == nil {
		return a
	}

	// 实际：是否存在独立 system 消息/字段
	if format == "anthropic" {
		_, a.ActualHasStandaloneSystem = data["system"]
	} else {
		if msgs, ok := data["messages"].([]interface{}); ok {
			for _, m := range msgs {
				if mm, ok := m.(map[string]interface{}); ok {
					if role, _ := mm["role"].(string); role == "system" {
						a.ActualHasStandaloneSystem = true
						break
					}
				}
			}
		}
	}

	// 实际：消息体内是否含 <system_prompt> / <supreme_instruction> 标签
	a.ActualHasSystemPromptTag = messagesContainTag(data, "<system_prompt>")
	a.ActualHasSupremeInstruction = messagesContainTag(data, "<supreme_instruction>")

	// 实际：思考参数
	if thinking, ok := data["thinking"].(map[string]interface{}); ok {
		a.ActualThinkingType, _ = thinking["type"].(string)
	}
	if eff, ok := data["reasoning_effort"].(string); ok {
		a.ActualReasoningEffort = eff
	}
	if outputCfg, ok := data["output_config"].(map[string]interface{}); ok {
		if eff, ok := outputCfg["effort"].(string); ok {
			a.ActualReasoningEffort = eff
		}
	}
	if mt, ok := data["max_tokens"].(float64); ok {
		a.ActualMaxTokens = int(mt)
	}

	return a
}

// messagesContainTag 遍历 messages 中所有 user/assistant/system 消息的 content
// （兼容 string 与 Anthropic text 块数组），判断是否存在目标标签子串。
func messagesContainTag(data map[string]interface{}, tag string) bool {
	msgs, ok := data["messages"].([]interface{})
	if !ok {
		return false
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if extractTextContent(mm["content"]) != "" && strings.Contains(extractTextContent(mm["content"]), tag) {
			return true
		}
	}
	return false
}

func injectReasoningContent(data map[string]interface{}) {
	messages, ok := data["messages"].([]interface{})
	if !ok {
		return
	}
	for _, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "assistant" {
			continue
		}
		if _, hasToolCalls := m["tool_calls"]; !hasToolCalls {
			continue
		}
		if _, hasReasoning := m["reasoning_content"]; hasReasoning {
			continue
		}
		m["reasoning_content"] = ""
	}
}

func fixEmptyAssistantContent(data map[string]interface{}) {
	messages, ok := data["messages"].([]interface{})
	if !ok {
		return
	}

	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "assistant" {
			continue
		}

		reasoning, hasReasoning := m["reasoning_content"].(string)
		if !hasReasoning || reasoning == "" {
			continue
		}

		content, hasContent := m["content"].(string)
		if hasContent && content != "" {
			continue
		}

		m["content"] = reasoning
		return
	}
}

func (s *ProxyServer) forwardStream(ctx *ProxyContext) {
	cfg := s.config.Get()
	flusher, canFlush := ctx.ResponseWriter.(http.Flusher)
	capture := newCaptureBuffer(captureBufCap)
	scanner := bufio.NewScanner(ctx.Response.Body)
	scanner.Buffer(make([]byte, 0, scannerInitialBuf), scannerMaxBuf)

	var lastUsage *TokenUsage
	var finishReason string
	var reasoningBuilder strings.Builder
	var contentBuilder strings.Builder

	var lastPushTime time.Time
	var estPrompt int64
	if ctx.Data != nil {
		estPrompt = int64(float64(len(ctx.OriginalBody)) / 2.5)
	}

	var antiHallucinationIntercepted bool
	var hasToolsInStream bool
	var streamID string
	var streamModel string
	var streamCreated float64

	for scanner.Scan() {
		line := scanner.Text()
		line = replaceDSMLMarkers(line)

		delta, err := ParseSSELine(line)
		if err == nil && delta != nil {
			if delta.ReasoningContent != "" {
				reasoningBuilder.WriteString(delta.ReasoningContent)
			}
			if delta.Content != "" {
				contentBuilder.WriteString(delta.Content)
			}

			if hasToolCallsInDelta(delta) {
				hasToolsInStream = true
			}

			if streamID == "" {
				if id, _ := delta.RawChunk["id"].(string); id != "" {
					streamID = id
				}
				if m, _ := delta.RawChunk["model"].(string); m != "" {
					streamModel = m
				}
				if c, ok := delta.RawChunk["created"].(float64); ok {
					streamCreated = c
				}
			}

			// 防幻觉拦截检测：开启防幻觉 + 请求不带 tools + 流中不含 tools 信号 + 发生了思维链推理 + 大模型输出了第一个正式 content
			if cfg.AntiHallucinationEnabled && !ctx.HasTools && !hasToolsInStream && reasoningBuilder.Len() > 0 && delta.Content != "" && !antiHallucinationIntercepted {
				antiHallucinationIntercepted = true
				break // 立即截断跳出！不输出此 content chunk 给客户端。
			}

			if delta.Usage != nil {
				lastUsage = delta.Usage
			}
			if delta.FinishReason != "" {
				finishReason = delta.FinishReason
			}
		}

		if _, err := ctx.ResponseWriter.Write([]byte(line + "\n")); err != nil {
			log.Printf("[proxy] forwardStream write error: %v", err)
			break
		}
		capture.Write([]byte(line + "\n"))
		if canFlush {
			flusher.Flush()
		}

		if delta != nil && (delta.ReasoningContent != "" || delta.Content != "") {
			now := time.Now()
			if lastPushTime.IsZero() || now.Sub(lastPushTime) >= 200*time.Millisecond {
				lastPushTime = now
				totalChars := len(reasoningBuilder.String()) + len(contentBuilder.String())
				estCompletion := int64(float64(totalChars) / 2.3)
				s.logger.UpdateTokenUsageAndStatus(ctx.LogID, &TokenUsage{
					Prompt:     estPrompt,
					Completion: estCompletion,
					Total:      estPrompt + estCompletion,
				}, "streaming")
			}
		}
	}

	if err := scanner.Err(); err != nil && !antiHallucinationIntercepted {
		log.Printf("[proxy] forwardStream scanner error: %v", err)
	}

	// ── 情况一：被防幻觉逻辑截断 ──
	if antiHallucinationIntercepted {
		// 1. 关闭第一阶段的响应体连接
		ctx.Response.Body.Close()

		// 2. 提取最新消息并渲染校准词
		latestUserMsg := getLatestUserMsg(ctx.Data)
		calibrationPrompt := cfg.AntiHallucinationPrompt
		if calibrationPrompt == "" {
			calibrationPrompt = "\n\n[意图校准] 用户现在关心的是：{{latest_user_message}}\n\n"
		}
		calibrationText := strings.ReplaceAll(calibrationPrompt, "{{latest_user_message}}", latestUserMsg)

		// 3. 向客户端流中补全校准词提示
		s.writeCalibrationSSE(ctx.ResponseWriter, flusher, canFlush, capture, ctx.Format, calibrationText, streamID, streamModel, streamCreated)

		// 4. 完结第一阶段日志记录
		s.logger.UpdateSystemEvent(ctx.LogID, "thinking_finished")
		if lastUsage != nil {
			s.logger.UpdateTokenUsageAndStatus(ctx.LogID, lastUsage, "completed")
		} else {
			estCompletion := int64(float64(reasoningBuilder.Len()) / 2.3)
			s.logger.UpdateTokenUsageAndStatus(ctx.LogID, &TokenUsage{
				Prompt:     estPrompt,
				Completion: estCompletion,
				Total:      estPrompt + estCompletion,
			}, "completed")
		}
		if cfg.VerboseLogging {
			s.logger.UpdateLastResponse(ctx.LogID, truncateBody(capture.String()))
		}

		// 5. 新建“思维修正”第二阶段日志
		secondLogID := s.logger.Add(LogEntry{
			Time:            time.Now(),
			Format:          ctx.Format,
			RequestType:     "antihallucination_retry", // 思维修正
			Method:          "POST",
			Path:            ctx.Route,
			StatusCode:      200,
			Stream:          true,
			Transformed:     true,
			HasSystemPrompt: false,
			SemanticType:    ctx.SemanticType,
			Status:          "connecting",
		})

		// 6. 拼装第二次请求的 body
		var secondReq map[string]interface{}
		json.Unmarshal(ctx.TransformedBody, &secondReq)

		// 强制禁用二次推理
		if ctx.Format == "openai" {
			secondReq["thinking"] = map[string]interface{}{"type": "disabled"}
		} else if ctx.Format == "anthropic" {
			delete(secondReq, "output_config")
		}
		delete(secondReq, "reasoning_effort")

		// 插入已有的推理链和校准前缀（包裹在think内）
		messagesRaw, ok := secondReq["messages"].([]interface{})
		if ok {
			if ctx.Format == "openai" {
				messagesRaw = append(messagesRaw, map[string]interface{}{
					"role":    "assistant",
					"content": "<think>\n" + reasoningBuilder.String() + "\n" + calibrationText + "\n</think>",
				})
			} else {
				messagesRaw = append(messagesRaw, map[string]interface{}{
					"role": "assistant",
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": "<think>\n" + reasoningBuilder.String() + "\n" + calibrationText + "\n</think>",
						},
					},
				})
			}
			secondReq["messages"] = messagesRaw
		}
		secondReqBody, _ := json.Marshal(secondReq)
		s.logger.UpdateOnResponse(secondLogID, 0, 0, "connecting", nil, string(secondReqBody), "")

		// 7. 发起第二次请求并实时转发
		retryFR, retryUsage, secondRespBody := s.executeAndStreamAntiHallucination(ctx, secondReqBody, secondLogID, secondReq)

		hasToolsInSecondResponse := len(parseAssistantToolCalls(secondRespBody, ctx.Format)) > 0
		finalAction := s.updateFinalSemanticAction(secondLogID, ctx.SemanticType, ctx.HasTools, hasToolsInSecondResponse, retryFR)

		// 8. 写入 DONE 包结束客户端对话流
		ctx.ResponseWriter.Write([]byte("data: [DONE]\n"))
		if canFlush {
			flusher.Flush()
		}

		// 9. 提交第一阶段的主 TraceEvent 存档
		var pm TokenUsage
		if lastUsage != nil {
			pm = *lastUsage
		}
		primaryEv := NewTraceEvent(
			ctx.StartTime,
			ctx.LogID,
			ctx.SessionID,
			ctx.TurnID,
			"primary",
			ctx.Format,
			ctx.Route,
			ctx.Response.StatusCode,
			time.Since(ctx.StartTime).Milliseconds(),
			detectModel(ctx.OriginalBody),
			s.selectUpstream(ctx.Format),
			buildRequestMeta(ctx.Data, ctx.Format, ctx.Transformed, cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none", ctx.SemanticType),
			ResponseMeta{
				FinishReason:      "stop",
				PromptTokens:      int(pm.Prompt),
				CompletionTokens:  int(pm.Completion),
				TotalTokens:       int(pm.Total),
				CacheHitTokens:    int(pm.CacheHit),
				CacheMissTokens:   int(pm.CacheMiss),
				ReasoningContent:  reasoningBuilder.String(),
				Content:           "", // 已经被截断
				AntiLoopTriggered: false,
			},
			ctx.OriginalBody,
			capture.String(),
		)
		primaryEv.UpstreamRequest = string(ctx.TransformedBody)
		s.analysisSvc.SubmitEvent(primaryEv)

		// 10. 提交第二阶段的“思维修正” TraceEvent 存档，通过 parent_id 关联
		var rpm TokenUsage
		if retryUsage != nil {
			rpm = *retryUsage
		}
		retryEv := NewTraceEvent(
			time.Now(),
			secondLogID,
			ctx.SessionID,
			ctx.TurnID,
			finalAction,
			ctx.Format,
			ctx.Route,
			200,
			0,
			detectModel(ctx.OriginalBody),
			s.selectUpstream(ctx.Format),
			buildRequestMeta(secondReq, ctx.Format, true, false, finalAction),
			ResponseMeta{
				FinishReason:     retryFR,
				PromptTokens:     int(rpm.Prompt),
				CompletionTokens: int(rpm.Completion),
				TotalTokens:      int(rpm.Total),
				CacheHitTokens:   int(rpm.CacheHit),
				CacheMissTokens:  int(rpm.CacheMiss),
			},
			string(secondReqBody),
			secondRespBody,
		)
		retryEv.UpstreamRequest = string(secondReqBody)
	retryEv.ParentID = "ev_" + strconv.FormatInt(ctx.StartTime.UnixNano(), 36)
	s.analysisSvc.SubmitEvent(retryEv)

	// 防幻觉截断：主请求到此结束，回填整轮耗时（含首阶段生成与截断判定）
	s.logger.UpdateTotalMs(ctx.LogID, time.Since(ctx.StartTime).Milliseconds())

	return
}

	// ── 情况二：常规模式走完 ──
	finalAction := s.updateFinalSemanticAction(ctx.LogID, ctx.SemanticType, ctx.HasTools, hasToolsInStream, finishReason)
	ctx.SemanticType = finalAction

	if lastUsage != nil {
		s.logger.UpdateTokenUsageAndStatus(ctx.LogID, lastUsage, "completed")
	} else {
		totalChars := len(reasoningBuilder.String()) + len(contentBuilder.String())
		estCompletion := int64(float64(totalChars) / 2.3)
		var estPrompt int64
		if ctx.Data != nil {
			estPrompt = int64(float64(len(ctx.OriginalBody)) / 2.5)
		}
		s.logger.UpdateTokenUsageAndStatus(ctx.LogID, &TokenUsage{
			Prompt:     estPrompt,
			Completion: estCompletion,
			Total:      estPrompt + estCompletion,
		}, "completed")
	}
	if cfg.VerboseLogging {
		s.logger.UpdateLastResponse(ctx.LogID, truncateBody(capture.String()))
	}

	// 常规流式走完：回填整轮完成耗时（latency_ms 仍为首字节延迟）
	s.logger.UpdateTotalMs(ctx.LogID, time.Since(ctx.StartTime).Milliseconds())

	var pm TokenUsage
	if lastUsage != nil {
		pm = *lastUsage
	}
	traceEv := NewTraceEvent(
		ctx.StartTime,
		ctx.LogID,
		ctx.SessionID,
		ctx.TurnID,
		"primary",
		ctx.Format,
		ctx.Route,
		ctx.Response.StatusCode,
		time.Since(ctx.StartTime).Milliseconds(),
		detectModel(ctx.OriginalBody),
		s.selectUpstream(ctx.Format),
		buildRequestMeta(ctx.Data, ctx.Format, ctx.Transformed, cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none", ctx.SemanticType),
		ResponseMeta{
			FinishReason:     finishReason,
			PromptTokens:     int(pm.Prompt),
			CompletionTokens: int(pm.Completion),
			TotalTokens:      int(pm.Total),
			CacheHitTokens:   int(pm.CacheHit),
			CacheMissTokens:  int(pm.CacheMiss),
			ReasoningContent: reasoningBuilder.String(),
			Content:          contentBuilder.String(),
		},
		ctx.OriginalBody,
		capture.String(),
	)
	traceEv.UpstreamRequest = string(ctx.TransformedBody)
	s.analysisSvc.SubmitEvent(traceEv)
}

func (s *ProxyServer) forwardBuffered(ctx *ProxyContext) {
	cfg := s.config.Get()
	bodyBytes, err := io.ReadAll(ctx.Response.Body)
	if err != nil {
		log.Printf("[proxy] forwardBuffered read error: %v", err)
		s.logger.UpdateTokenUsageAndStatus(ctx.LogID, nil, "completed")
		return
	}
	bodyBytes = replaceDSMLMarkersBytes(bodyBytes)
	ctx.ResponseWriter.Write(bodyBytes)

	var lastUsage *TokenUsage
	if u := extractUsageFromBody(string(bodyBytes)); u != nil {
		s.logger.UpdateTokenUsageAndStatus(ctx.LogID, u, "completed")
		lastUsage = u
	} else {
		s.logger.UpdateTokenUsageAndStatus(ctx.LogID, nil, "completed")
	}
	if cfg.VerboseLogging {
		s.logger.UpdateLastResponse(ctx.LogID, truncateBody(string(bodyBytes)))
	}

	fr := detectBufferFinishReason(bodyBytes)

	hasToolsInResponse := len(parseAssistantToolCalls(string(bodyBytes), ctx.Format)) > 0
	finalAction := s.updateFinalSemanticAction(ctx.LogID, ctx.SemanticType, ctx.HasTools, hasToolsInResponse, fr)
	ctx.SemanticType = finalAction

	// 发射 TraceEvent
	var pm TokenUsage
	if lastUsage != nil {
		pm = *lastUsage
	}
	traceEv := NewTraceEvent(
		ctx.StartTime,
		ctx.LogID,
		ctx.SessionID,
		ctx.TurnID,
		"primary",
		ctx.Format,
		ctx.Route,
		ctx.Response.StatusCode,
		time.Since(ctx.StartTime).Milliseconds(),
		detectModel(ctx.OriginalBody),
		s.selectUpstream(ctx.Format),
		buildRequestMeta(ctx.Data, ctx.Format, ctx.Transformed, cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none", ctx.SemanticType),
		ResponseMeta{
			FinishReason:     fr,
			PromptTokens:     int(pm.Prompt),
			CompletionTokens: int(pm.Completion),
			TotalTokens:      int(pm.Total),
			CacheHitTokens:   int(pm.CacheHit),
			CacheMissTokens:  int(pm.CacheMiss),
			ReasoningContent: parseAssistantReasoning(string(bodyBytes), ctx.Format),
			Content:          parseAssistantContent(string(bodyBytes), ctx.Format),
		},
		ctx.OriginalBody,
		string(bodyBytes),
	)
	traceEv.UpstreamRequest = string(ctx.TransformedBody)
	// 非流式整轮完成：回填总耗时（首响 latency_ms 已在 connecting 阶段记录）
	s.logger.UpdateTotalMs(ctx.LogID, time.Since(ctx.StartTime).Milliseconds())
	s.analysisSvc.SubmitEvent(traceEv)
}

type antiLoopState struct {
	reqID                 int64
	parentEventID         string
	finishReason          string
	reasoningBuilder      strings.Builder
	contentBuilder        strings.Builder
	lastUsage             *TokenUsage
	streamID              string
	streamModel           string
	streamCreated         float64
	capture               *captureBuffer
	earlyStop             bool
	earlyStopAnalysis     *AntiLoopAnalysis
	debugLogID            int64
	watchdogStop          chan<- struct{}
	scannerExitedNormally bool
	flusher               http.Flusher
	canFlush              bool
	scanner               *bufio.Scanner
	needsRetry            bool
	retryBody             []byte
	hasToolsInStream      bool
}

func (s *ProxyServer) initAntiLoopState(ctx *ProxyContext) *antiLoopState {
	cfg := s.config.Get()
	flusher, canFlush := ctx.ResponseWriter.(http.Flusher)
	scanner := bufio.NewScanner(ctx.Response.Body)
	scanner.Buffer(make([]byte, 0, scannerInitialBuf), scannerMaxBuf)

	reqID := nextTraceReqID()
	parentEventID := "ev_" + strconv.FormatInt(ctx.StartTime.UnixNano(), 36)

	traceKeyvals("event", "phase1_start", "req_id", reqID, "threshold", cfg.AntiLoopCheckTokens,
		"retry_model", cfg.AntiLoopRetryModel, "format", ctx.Format)

	var debugLogID int64
	if cfg.DebugMode {
		debugLogID = s.logger.Add(LogEntry{
			Time:        time.Now(),
			Format:      "debug",
			RequestType: "debug",
			Method:      "TRACE",
			Path:        "/antiloop/tokens",
			StatusCode:  cfg.AntiLoopCheckTokens,
		})
		log.Printf("[antiloop] debug entry created id=%d for reqID=%d", debugLogID, reqID)
	}

	watchdogStop := startProgressWatchdog(fmt.Sprintf("Phase1(reqID=%d)", reqID), 60*time.Second)

	return &antiLoopState{
		reqID:         reqID,
		parentEventID: parentEventID,
		capture:       newCaptureBuffer(captureBufCap),
		debugLogID:    debugLogID,
		watchdogStop:  watchdogStop,
		flusher:       flusher,
		canFlush:      canFlush,
		scanner:       scanner,
	}
}

func (s *ProxyServer) streamAndMonitorPhase1(ctx *ProxyContext, state *antiLoopState) {
	log.Printf("[antiloop] Phase 1 start: streaming first response in real-time, reqID=%d", state.reqID)
	cfg := s.config.Get()

	var completionTokens int
	var hasToolsInStream bool
	var analyzeDone chan analyzeResult
	var analyzeTriggered bool
	chunkCount := 0
	lastTracedTokens := 0
	var lastPushTime time.Time
	var estPrompt int64
	if ctx.Data != nil {
		estPrompt = int64(float64(len(ctx.OriginalBody)) / 2.5)
	}

	for state.scanner.Scan() {
		state.watchdogStop = resetProgressWatchdog(state.watchdogStop)

		line := state.scanner.Text()
		line = replaceDSMLMarkers(line)

		deltaForTools, _ := ParseSSELine(line)
		if hasToolCallsInDelta(deltaForTools) {
			hasToolsInStream = true
			state.hasToolsInStream = true
		}

		if !strings.HasPrefix(line, "data: ") {
			if _, err := ctx.ResponseWriter.Write([]byte(line + "\n")); err != nil {
				log.Printf("[antiloop] reqID=%d write error (non-data): %v", state.reqID, err)
				traceKeyvals("event", "write_error", "req_id", state.reqID, "error", err.Error())
				state.scannerExitedNormally = false
				break
			}
			state.capture.Write([]byte(line + "\n"))
			if state.canFlush {
				state.flusher.Flush()
			}
			continue
		}

		dataChunk := strings.TrimPrefix(line, "data: ")
		if dataChunk == "[DONE]" {
			continue
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(dataChunk), &chunk); err != nil {
			if _, werr := ctx.ResponseWriter.Write([]byte(line + "\n")); werr != nil {
				log.Printf("[antiloop] reqID=%d write error (unmarshal fallback): %v", state.reqID, werr)
				traceKeyvals("event", "write_error", "req_id", state.reqID, "context", "unmarshal_fallback", "error", werr.Error())
				break
			}
			state.capture.Write([]byte(line + "\n"))
			if state.canFlush {
				state.flusher.Flush()
			}
			continue
		}

		if state.streamID == "" {
			if id, _ := chunk["id"].(string); id != "" {
				state.streamID = id
			}
			if m, _ := chunk["model"].(string); m != "" {
				state.streamModel = m
			}
			if c, ok := chunk["created"].(float64); ok {
				state.streamCreated = c
			}
		}

		if usage, ok := chunk["usage"].(map[string]interface{}); ok {
			if ct, ok := usage["completion_tokens"].(float64); ok {
				completionTokens = int(ct)
			}
		}

		estimatedTokens := (state.reasoningBuilder.Len() + state.contentBuilder.Len()) / 4
		effectiveTokens := completionTokens
		if estimatedTokens > effectiveTokens {
			effectiveTokens = estimatedTokens
		}
		chunkCount++

		if effectiveTokens > 0 && (effectiveTokens-lastTracedTokens >= 500 || (completionTokens > 0 && lastTracedTokens == 0)) {
			lastTracedTokens = effectiveTokens
			traceKeyvals("event", "chunk", "n", chunkCount, "usage", completionTokens,
				"est", estimatedTokens, "eff", effectiveTokens,
				"reasoning_chars", state.reasoningBuilder.Len(), "content_chars", state.contentBuilder.Len())
		}

		if cfg.AntiLoopCheckTokens > 0 && !analyzeTriggered && effectiveTokens >= cfg.AntiLoopCheckTokens {
			if state.contentBuilder.Len() == 0 && state.reasoningBuilder.Len() > cfg.AntiLoopCheckTokens*2 {
				tracelog("[antiloop] HEURISTIC: content=0 reasoning=%d chars → forcing intervention", state.reasoningBuilder.Len())
				traceKeyvals("event", "heuristic_force", "reasoning_chars", state.reasoningBuilder.Len(), "content_chars", 0)
				state.earlyStop = true
				state.earlyStopAnalysis = &AntiLoopAnalysis{
					Judgment:       "excessive",
					Guidance:       "你已经思考了极长时间但没有输出任何内容。立即停止思考，直接给出最终结论。",
					EnableThinking: false,
				}
				s.logger.Add(LogEntry{
					Time:        time.Now(),
					Format:      "openai",
					RequestType: "antiloop_analyzer",
					Method:      "HEURISTIC",
					Path:        "/antiloop/heuristic (启发式判定)",
					StatusCode:  200,
					LatencyMs:   0,
					OriginalBody: condStr(cfg.VerboseLogging,
						fmt.Sprintf("reasoning_chars=%d content_chars=%d threshold=%d",
							state.reasoningBuilder.Len(), state.contentBuilder.Len(), cfg.AntiLoopCheckTokens), ""),
					ResponseBody: condStr(cfg.VerboseLogging, "judgment=excessive guidance=立即停止思考", ""),
				})
				ctx.Response.Body.Close()
				return
			}

			analyzeTriggered = true
			analyzeDone = make(chan analyzeResult, 1)
			go s.parallelAnalyze(analyzeDone, ctx.TransformedBody, ctx.Format, state.reasoningBuilder.String(), state.contentBuilder.String())
			log.Printf("[antiloop] parallel analysis triggered at %d tokens", effectiveTokens)
			traceKeyvals("event", "analyzer_launch", "eff", effectiveTokens, "usage", completionTokens, "est", estimatedTokens)
		}

		if cfg.DebugMode && state.debugLogID != 0 {
			s.logger.UpdateTokenUsage(state.debugLogID, &TokenUsage{
				Total:      int64(effectiveTokens),
				Prompt:     int64(completionTokens),
				Completion: int64(estimatedTokens),
				CacheHit:   int64(state.reasoningBuilder.Len()),
				CacheMiss:  int64(state.contentBuilder.Len()),
			})
		}

		if analyzeDone != nil {
			select {
			case result := <-analyzeDone:
				if result.needsIntervention() {
					log.Printf("[antiloop] parallel analyzer says STOP (judgment=%s), intervening", result.analysis.Judgment)
					traceKeyvals("event", "analyzer_stop", "judgment", result.analysis.Judgment, "tokens", effectiveTokens)
					state.earlyStop = true
					state.earlyStopAnalysis = result.analysis

					analyzerEventID := "ana_" + strconv.FormatInt(time.Now().UnixNano(), 36)
					anaEv := NewTraceEvent(
						time.Now(),
						ctx.LogID,
						ctx.SessionID,
						ctx.TurnID,
						"analyzer",
						"openai",
						"/antiloop/analyze (parallel)",
						200,
						0,
						"deepseek-chat",
						s.activeBaseURL(),
						buildRequestMeta(ctx.Data, ctx.Format, ctx.Transformed, cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none", ctx.SemanticType),
						ResponseMeta{
							AnalyzerJudgment: result.analysis.Judgment,
							FinishReason:     "stop",
						},
						"Parallel Analysis Target: "+state.reasoningBuilder.String(),
						fmt.Sprintf("Judgment: %s\nGuidance: %s", result.analysis.Judgment, result.analysis.Guidance),
					)
					anaEv.UpstreamRequest = anaEv.RawRequest
					anaEv.ID = analyzerEventID
					anaEv.ParentID = state.parentEventID
					s.analysisSvc.SubmitEvent(anaEv)

					ctx.Response.Body.Close()
					return
				} else {
					log.Printf("[antiloop] parallel analyzer says CONTINUE (judgment=%s)", result.analysis.Judgment)
					traceKeyvals("event", "analyzer_continue", "judgment", result.analysis.Judgment)
				}
				analyzeDone = nil
			default:
			}
		}

		modified := false
		if choices, ok := chunk["choices"].([]interface{}); ok {
			for _, c := range choices {
				if choice, ok := c.(map[string]interface{}); ok {
					if fr, _ := choice["finish_reason"].(string); fr != "" {
						state.finishReason = fr
						delete(choice, "finish_reason")
						modified = true
					}
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if rc, _ := delta["reasoning_content"].(string); rc != "" {
							state.reasoningBuilder.WriteString(rc)
						}
						if ct, _ := delta["content"].(string); ct != "" {
							state.contentBuilder.WriteString(ct)
						}
					}
				}
			}
		}
		if chunk["type"] == "content_block_delta" {
			if delta, ok := chunk["delta"].(map[string]interface{}); ok {
				if rc, _ := delta["thinking"].(string); rc != "" {
					state.reasoningBuilder.WriteString(rc)
				}
				if ct, _ := delta["text"].(string); ct != "" {
					state.contentBuilder.WriteString(ct)
				}
			}
		}

		// 防幻觉拦截：开启防幻觉 + 请求不带 tools + 流中不含 tools 信号 + 发生了思维链推理 + 已经开始输出正式正文
		if cfg.AntiHallucinationEnabled && !ctx.HasTools && !hasToolsInStream && state.reasoningBuilder.Len() > 0 && state.contentBuilder.Len() > 0 && !state.earlyStop {
			state.earlyStop = true
			state.earlyStopAnalysis = &AntiLoopAnalysis{
				Judgment:       "antihallucination",
				Guidance:       "",
				EnableThinking: false,
			}
			ctx.Response.Body.Close()
			return
		}

		if u := parseUsageFromMap(chunk); u != nil && state.lastUsage == nil {
			state.lastUsage = u
		}

		var outLine string
		if modified {
			b, _ := json.Marshal(chunk)
			outLine = "data: " + string(b) + "\n"
		} else {
			outLine = line + "\n"
		}
		if _, err := ctx.ResponseWriter.Write([]byte(outLine)); err != nil {
			log.Printf("[antiloop] reqID=%d write error (data): %v", state.reqID, err)
			traceKeyvals("event", "write_error", "req_id", state.reqID, "context", "data_write", "error", err.Error())
			break
		}
		state.capture.Write([]byte(outLine))
		if state.canFlush {
			state.flusher.Flush()
		}

		// 节流推送临时估计 Token
		now := time.Now()
		if lastPushTime.IsZero() || now.Sub(lastPushTime) >= 200*time.Millisecond {
			lastPushTime = now
			totalChars := state.reasoningBuilder.Len() + state.contentBuilder.Len()
			estCompletion := int64(float64(totalChars) / 2.3)
			s.logger.UpdateTokenUsageAndStatus(ctx.LogID, &TokenUsage{
				Prompt:     estPrompt,
				Completion: estCompletion,
				Total:      estPrompt + estCompletion,
			}, "streaming")
		}
	}

	state.scannerExitedNormally = true
}

func (s *ProxyServer) executeRetryPhase2(ctx *ProxyContext, state *antiLoopState) {
	cfg := s.config.Get()
	if state.watchdogStop != nil {
		close(state.watchdogStop)
	}

	if state.scannerExitedNormally {
		if err := state.scanner.Err(); err != nil {
			log.Printf("[antiloop] reqID=%d scanner error: %v", state.reqID, err)
			traceKeyvals("event", "scanner_error", "req_id", state.reqID, "error", err.Error())
		} else {
			log.Printf("[antiloop] reqID=%d scanner finished normally", state.reqID)
			traceKeyvals("event", "scanner_eof", "req_id", state.reqID)
		}
	} else {
		log.Printf("[antiloop] reqID=%d scanner exited abnormally (write error or break)", state.reqID)
		traceKeyvals("event", "scanner_abort", "req_id", state.reqID)
	}

	// ── 拦截防幻觉并在 Anti-Loop 下处理 ──
	if state.earlyStop && state.earlyStopAnalysis != nil && state.earlyStopAnalysis.Judgment == "antihallucination" {
		log.Printf("[antiloop] reqID=%d hijacked by Anti-Hallucination continuation", state.reqID)
		
		latestUserMsg := getLatestUserMsg(ctx.Data)
		calibrationPrompt := cfg.AntiHallucinationPrompt
		if calibrationPrompt == "" {
			calibrationPrompt = "\n\n[意图校准] 用户现在关心的是：{{latest_user_message}}\n\n"
		}
		calibrationText := strings.ReplaceAll(calibrationPrompt, "{{latest_user_message}}", latestUserMsg)

		s.writeCalibrationSSE(ctx.ResponseWriter, state.flusher, state.canFlush, state.capture, ctx.Format, calibrationText, state.streamID, state.streamModel, state.streamCreated)

		s.logger.UpdateSystemEvent(ctx.LogID, "thinking_finished")
		if state.lastUsage != nil {
			s.logger.UpdateTokenUsageAndStatus(ctx.LogID, state.lastUsage, "completed")
		} else {
			var estPrompt int64
			if ctx.Data != nil {
				estPrompt = int64(float64(len(ctx.OriginalBody)) / 2.5)
			}
			estCompletion := int64(float64(state.reasoningBuilder.Len()) / 2.3)
			s.logger.UpdateTokenUsageAndStatus(ctx.LogID, &TokenUsage{
				Prompt:     estPrompt,
				Completion: estCompletion,
				Total:      estPrompt + estCompletion,
			}, "completed")
		}
		if cfg.VerboseLogging {
			s.logger.UpdateLastResponse(ctx.LogID, truncateBody(state.capture.String()))
		}

		secondLogID := s.logger.Add(LogEntry{
			Time:            time.Now(),
			Format:          ctx.Format,
			RequestType:     "antihallucination_retry", // 思维修正
			Method:          "POST",
			Path:            ctx.Route,
			StatusCode:      200,
			Stream:          true,
			Transformed:     true,
			HasSystemPrompt: false,
			SemanticType:    ctx.SemanticType,
			Status:          "connecting",
		})

		var secondReq map[string]interface{}
		json.Unmarshal(ctx.TransformedBody, &secondReq)

		if ctx.Format == "openai" {
			secondReq["thinking"] = map[string]interface{}{"type": "disabled"}
		} else if ctx.Format == "anthropic" {
			delete(secondReq, "output_config")
		}
		delete(secondReq, "reasoning_effort")

		messagesRaw, ok := secondReq["messages"].([]interface{})
		if ok {
			reasoningStr := state.reasoningBuilder.String()
			if ctx.Format == "openai" {
				messagesRaw = append(messagesRaw, map[string]interface{}{
					"role":    "assistant",
					"content": "<think>\n" + reasoningStr + "\n" + calibrationText + "\n</think>",
				})
			} else {
				messagesRaw = append(messagesRaw, map[string]interface{}{
					"role": "assistant",
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": "<think>\n" + reasoningStr + "\n" + calibrationText + "\n</think>",
						},
					},
				})
			}
			secondReq["messages"] = messagesRaw
		}
		secondReqBody, _ := json.Marshal(secondReq)
		s.logger.UpdateOnResponse(secondLogID, 0, 0, "connecting", nil, string(secondReqBody), "")

		retryFR, retryUsage, secondRespBody := s.executeAndStreamAntiHallucination(ctx, secondReqBody, secondLogID, secondReq)

		hasToolsInSecondResponse := len(parseAssistantToolCalls(secondRespBody, ctx.Format)) > 0
		finalAction := s.updateFinalSemanticAction(secondLogID, ctx.SemanticType, ctx.HasTools, hasToolsInSecondResponse, retryFR)

		var pm TokenUsage
		if state.lastUsage != nil {
			pm = *state.lastUsage
		}
		primaryEv := NewTraceEvent(
			ctx.StartTime,
			ctx.LogID,
			ctx.SessionID,
			ctx.TurnID,
			"primary",
			ctx.Format,
			ctx.Route,
			ctx.Response.StatusCode,
			time.Since(ctx.StartTime).Milliseconds(),
			detectModel(ctx.OriginalBody),
			s.selectUpstream(ctx.Format),
			buildRequestMeta(ctx.Data, ctx.Format, ctx.Transformed, cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none", ctx.SemanticType),
			ResponseMeta{
				FinishReason:      "stop",
				PromptTokens:      int(pm.Prompt),
				CompletionTokens:  int(pm.Completion),
				TotalTokens:       int(pm.Total),
				CacheHitTokens:    int(pm.CacheHit),
				CacheMissTokens:   int(pm.CacheMiss),
				ReasoningContent:  state.reasoningBuilder.String(),
				Content:           "",
				AntiLoopTriggered: false,
			},
			ctx.OriginalBody,
			state.capture.String(),
		)
		primaryEv.UpstreamRequest = string(ctx.TransformedBody)
		s.analysisSvc.SubmitEvent(primaryEv)

		var rpm TokenUsage
		if retryUsage != nil {
			rpm = *retryUsage
		}
		retryEv := NewTraceEvent(
			time.Now(),
			secondLogID,
			ctx.SessionID,
			ctx.TurnID,
			finalAction,
			ctx.Format,
			ctx.Route,
			200,
			0,
			detectModel(ctx.OriginalBody),
			s.selectUpstream(ctx.Format),
			buildRequestMeta(secondReq, ctx.Format, true, false, finalAction),
			ResponseMeta{
				FinishReason:     retryFR,
				PromptTokens:     int(rpm.Prompt),
				CompletionTokens: int(rpm.Completion),
				TotalTokens:      int(rpm.Total),
				CacheHitTokens:   int(rpm.CacheHit),
				CacheMissTokens:  int(rpm.CacheMiss),
			},
			string(secondReqBody),
			secondRespBody,
		)
		retryEv.UpstreamRequest = string(secondReqBody)
		retryEv.ParentID = "ev_" + strconv.FormatInt(ctx.StartTime.UnixNano(), 36)
		s.analysisSvc.SubmitEvent(retryEv)

		state.needsRetry = false
		return
	}

	state.needsRetry = state.earlyStop || state.finishReason == "length"
	if !state.needsRetry {
		log.Printf("[antiloop] reqID=%d no retry needed (finish_reason=%s)", state.reqID, state.finishReason)
		traceKeyvals("event", "no_retry", "req_id", state.reqID, "finish_reason", state.finishReason)
		if state.finishReason != "" {
			if _, err := ctx.ResponseWriter.Write([]byte("\n")); err != nil {
				log.Printf("[antiloop] reqID=%d write error (no-retry separator): %v", state.reqID, err)
				return
			}
			state.capture.Write([]byte("\n"))
			finishChunk := map[string]interface{}{
				"id":      state.streamID,
				"object":  "chat.completion.chunk",
				"created": state.streamCreated,
				"model":   state.streamModel,
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"delta":         map[string]interface{}{},
						"finish_reason": state.finishReason,
					},
				},
			}
			b, _ := json.Marshal(finishChunk)
			line := "data: " + string(b) + "\n"
			if _, err := ctx.ResponseWriter.Write([]byte(line)); err != nil {
				log.Printf("[antiloop] reqID=%d write error (no-retry finish chunk): %v", state.reqID, err)
				return
			}
			state.capture.Write([]byte(line))
			if state.canFlush {
				state.flusher.Flush()
			}
			if _, err := ctx.ResponseWriter.Write([]byte("\n")); err != nil {
				log.Printf("[antiloop] reqID=%d write error (no-retry final newline): %v", state.reqID, err)
				return
			}
			state.capture.Write([]byte("\n"))
		}
		return
	}

	// 触发 retry 前，推送 paused 状态
	var estPrompt int64
	if ctx.Data != nil {
		estPrompt = int64(float64(len(ctx.OriginalBody)) / 2.5)
	}
	totalChars := state.reasoningBuilder.Len() + state.contentBuilder.Len()
	estCompletion := int64(float64(totalChars) / 2.3)
	s.logger.UpdateTokenUsageAndStatus(ctx.LogID, &TokenUsage{
		Prompt:     estPrompt,
		Completion: estCompletion,
		Total:      estPrompt + estCompletion,
	}, "paused")

	reasoningContent := state.reasoningBuilder.String()
	phase1Content := state.contentBuilder.String()

	if state.streamID == "" {
		state.streamID = "dsplus-antiloop"
	}
	if state.streamModel == "" {
		state.streamModel = "deepseek-chat"
	}
	if state.streamCreated == 0 {
		state.streamCreated = float64(time.Now().Unix())
	}

	if _, err := ctx.ResponseWriter.Write([]byte("\n")); err != nil {
		log.Printf("[antiloop] reqID=%d write error (retry separator): %v", state.reqID, err)
		traceKeyvals("event", "write_error", "req_id", state.reqID, "context", "retry_separator", "error", err.Error())
		return
	}
	state.capture.Write([]byte("\n"))

	if state.earlyStop {
		log.Printf("[antiloop] reqID=%d early-stop indicator, analysis judgment=%s", state.reqID, state.earlyStopAnalysis.Judgment)
	} else {
		log.Printf("[antiloop] reqID=%d length-fallback indicator", state.reqID)
	}
	s.writeAntiloopIndicator(ctx.ResponseWriter, state.flusher, state.canFlush, state.capture, state.streamID, state.streamModel, state.streamCreated)

	if _, err := ctx.ResponseWriter.Write([]byte("\n")); err != nil {
		log.Printf("[antiloop] reqID=%d write error (retry indicator separator): %v", state.reqID, err)
		traceKeyvals("event", "write_error", "req_id", state.reqID, "context", "retry_indicator_sep", "error", err.Error())
		return
	}
	state.capture.Write([]byte("\n"))
	if state.canFlush {
		state.flusher.Flush()
	}

	if state.earlyStop && state.earlyStopAnalysis != nil {
		log.Printf("[antiloop] reqID=%d building early-stop retry (judgment=%s)", state.reqID, state.earlyStopAnalysis.Judgment)
		state.retryBody = s.buildGuidedRetryRequest(ctx.TransformedBody, ctx.Format, state.earlyStopAnalysis, phase1Content, reasoningContent, true)
	} else if state.finishReason == "length" {
		log.Printf("[antiloop] reqID=%d building length-fallback retry (reasoning=%d bytes, content=%d bytes)", state.reqID, len(reasoningContent), len(phase1Content))
		var analyzeErr error
		state.retryBody, analyzeErr = s.runAntiLoopAnalysis(ctx.TransformedBody, ctx.Format, phase1Content, reasoningContent)
		if analyzeErr != nil {
			log.Printf("[antiloop] reqID=%d analyzer failed: %v, using simple retry", state.reqID, analyzeErr)
			state.retryBody = s.buildSimpleRetryRequest(ctx.TransformedBody, ctx.Format, phase1Content, reasoningContent)
		}
	}

	log.Printf("[antiloop] reqID=%d Phase 2: executing retry request (body_bytes=%d)", state.reqID, len(state.retryBody))
	traceKeyvals("event", "retry_start", "req_id", state.reqID, "body_bytes", len(state.retryBody), "early_stop", state.earlyStop)
	retryFR, retryUsage := s.executeAndStreamRetry(ctx.ResponseWriter, state.flusher, state.canFlush, state.retryBody, ctx.Format, state.capture, state.streamID)
	log.Printf("[antiloop] reqID=%d Phase 2 end: retry finish_reason=%s retry_usage=%v", state.reqID, retryFR, retryUsage != nil)
	traceKeyvals("event", "retry_end", "req_id", state.reqID, "finish_reason", retryFR, "has_usage", retryUsage != nil)

	if retryFR == "length" || retryFR == "max_tokens" {
		log.Printf("[antiloop] reqID=%d retry also hit limit, sending hard-limit message", state.reqID)
		if _, err := ctx.ResponseWriter.Write([]byte("\n")); err != nil {
			log.Printf("[antiloop] reqID=%d write error (hard-limit separator): %v", state.reqID, err)
		}
		state.capture.Write([]byte("\n"))
		s.streamHardLimitSSE(ctx.ResponseWriter, state.flusher, state.canFlush, ctx.Format, state.capture, state.streamID, state.streamModel, state.streamCreated)
	}

	var rpm TokenUsage
	if retryUsage != nil {
		rpm = *retryUsage
	}
	retryEventID := "ret_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	retryEv := NewTraceEvent(
		time.Now(),
		ctx.LogID,
		ctx.SessionID,
		ctx.TurnID,
		"retry",
		ctx.Format,
		ctx.Route+" (retry)",
		200,
		0,
		cfg.AntiLoopRetryModel,
		s.selectUpstream(ctx.Format),
		buildRequestMeta(ctx.Data, ctx.Format, ctx.Transformed, cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none", ctx.SemanticType),
		ResponseMeta{
			FinishReason:     retryFR,
			PromptTokens:     int(rpm.Prompt),
			CompletionTokens: int(rpm.Completion),
			TotalTokens:      int(rpm.Total),
			CacheHitTokens:   int(rpm.CacheHit),
			CacheMissTokens:  int(rpm.CacheMiss),
			RetryModel:       cfg.AntiLoopRetryModel,
		},
		string(state.retryBody),
		state.capture.String(),
	)
	retryEv.UpstreamRequest = string(state.retryBody)
	retryEv.ID = retryEventID
	retryEv.ParentID = state.parentEventID
	s.analysisSvc.SubmitEvent(retryEv)

	if retryUsage != nil {
		state.lastUsage = retryUsage
	}
}

func (s *ProxyServer) finalizeStream(ctx *ProxyContext, state *antiLoopState) {
	cfg := s.config.Get()
	if _, err := ctx.ResponseWriter.Write([]byte("data: [DONE]\n")); err != nil {
		log.Printf("[antiloop] write error ([DONE]): %v", err)
	}
	state.capture.Write([]byte("data: [DONE]\n"))
	if state.canFlush {
		state.flusher.Flush()
	}
	log.Printf("[antiloop] reqID=%d stream complete, [DONE] sent", state.reqID)
	finalAction := s.updateFinalSemanticAction(ctx.LogID, ctx.SemanticType, ctx.HasTools, state.hasToolsInStream, state.finishReason)
	ctx.SemanticType = finalAction

	if state.lastUsage != nil {
		s.logger.UpdateTokenUsageAndStatus(ctx.LogID, state.lastUsage, "completed")
	} else {
		totalChars := state.reasoningBuilder.Len() + state.contentBuilder.Len()
		estCompletion := int64(float64(totalChars) / 2.3)
		var estPrompt int64
		if ctx.Data != nil {
			estPrompt = int64(float64(len(ctx.OriginalBody)) / 2.5)
		}
		s.logger.UpdateTokenUsageAndStatus(ctx.LogID, &TokenUsage{
			Prompt:     estPrompt,
			Completion: estCompletion,
			Total:      estPrompt + estCompletion,
		}, "completed")
	}
	if cfg.VerboseLogging {
		s.logger.UpdateLastResponse(ctx.LogID, truncateBody(state.capture.String()))
	}

	var pm TokenUsage
	if state.lastUsage != nil {
		pm = *state.lastUsage
	}
	primaryEv := NewTraceEvent(
		ctx.StartTime,
		ctx.LogID,
		ctx.SessionID,
		ctx.TurnID,
		"primary",
		ctx.Format,
		ctx.Route,
		ctx.Response.StatusCode,
		time.Since(ctx.StartTime).Milliseconds(),
		detectModel(ctx.OriginalBody),
		s.selectUpstream(ctx.Format),
		buildRequestMeta(ctx.Data, ctx.Format, ctx.Transformed, cfg.ExtraPrompt != "" && cfg.ExtraPromptPlacement != "none", ctx.SemanticType),
		ResponseMeta{
			FinishReason:      state.finishReason,
			PromptTokens:      int(pm.Prompt),
			CompletionTokens:  int(pm.Completion),
			TotalTokens:       int(pm.Total),
			CacheHitTokens:    int(pm.CacheHit),
			CacheMissTokens:   int(pm.CacheMiss),
			ReasoningContent:  state.reasoningBuilder.String(),
			Content:           state.contentBuilder.String(),
			AntiLoopTriggered: state.needsRetry,
		},
		ctx.OriginalBody,
		state.capture.String(),
	)
	primaryEv.UpstreamRequest = string(ctx.TransformedBody)
	primaryEv.ID = state.parentEventID
	s.analysisSvc.SubmitEvent(primaryEv)

	// anti-loop 流式完成：回填整轮完成耗时
	s.logger.UpdateTotalMs(ctx.LogID, time.Since(ctx.StartTime).Milliseconds())
}

func (s *ProxyServer) forwardStreamWithAntiLoop(ctx *ProxyContext) {
	state := s.initAntiLoopState(ctx)

	// Phase 1: 实时流式转发与 token 监控
	s.streamAndMonitorPhase1(ctx, state)

	// Phase 2: 并行干预或超长阶段下的重试处理
	s.executeRetryPhase2(ctx, state)

	// Finalize: 发送 DONE 标签、写日志并提交 TraceEvent
	s.finalizeStream(ctx, state)
}

// activeProvider 返回当前选中的供应商；找不到时回退到第一个。
func (s *ProxyServer) activeProvider() (Provider, bool) {
	cfg := s.config.Get()
	for _, p := range cfg.Providers {
		if p.Name == cfg.ActiveProvider {
			return p, true
		}
	}
	if len(cfg.Providers) > 0 {
		return cfg.Providers[0], true
	}
	return Provider{}, false
}

// selectUpstream 返回当前供应商对应当前请求格式的基础地址。
// 同一供应商可同时服务 OpenAI 与 Anthropic 两种格式：
//   - Anthropic 格式优先使用 AnthropicBaseURL（若已配置）；
//   - 对已知 OpenAI 网关（如 api.deepseek.com）自动在其 base 后追加 /anthropic；
//   - 否则回退到 BaseURL。
// format 参数用于按请求格式选择正确端点，实现单地址自动匹配。
func (s *ProxyServer) selectUpstream(format string) string {
	if p, ok := s.activeProvider(); ok {
		return s.resolveBaseURL(p, format)
	}
	cfg := s.config.Get()
	if format == "anthropic" {
		if cfg.AnthropicUpstream != "" {
			return cfg.AnthropicUpstream
		}
		if cfg.OpenAIUpstream != "" {
			return cfg.OpenAIUpstream
		}
	} else {
		if cfg.OpenAIUpstream != "" {
			return cfg.OpenAIUpstream
		}
		if cfg.AnthropicUpstream != "" {
			return cfg.AnthropicUpstream
		}
	}
	return "https://api.deepseek.com"
}

// resolveBaseURL 根据请求格式解析供应商的实际基础地址。
func (s *ProxyServer) resolveBaseURL(p Provider, format string) string {
	if format == "anthropic" {
		if p.AnthropicBaseURL != "" {
			return p.AnthropicBaseURL
		}
		// 自动匹配：DeepSeek 的 OpenAI 网关与 Anthropic 网关仅差一个 /anthropic 前缀。
		if strings.Contains(p.BaseURL, "api.deepseek.com") && !strings.Contains(p.BaseURL, "/anthropic") {
			return strings.TrimRight(p.BaseURL, "/") + "/anthropic"
		}
	}
	return p.BaseURL
}

// activeBaseURL 返回当前供应商的基础地址（供重试/分析等内部调用使用）。
func (s *ProxyServer) activeBaseURL() string {
	if p, ok := s.activeProvider(); ok {
		return p.BaseURL
	}
	return s.selectUpstream("")
}

// activeAPIKey 返回当前供应商的 API Key；为空时回退到全局 APIKey。
func (s *ProxyServer) activeAPIKey() string {
	if p, ok := s.activeProvider(); ok && p.APIKey != "" {
		return p.APIKey
	}
	return s.config.Get().APIKey
}

// joinUpstream 将基础地址与请求路径安全拼接。
// 若基础地址已以 /v1 结尾且路径也以 /v1/ 开头，则去掉重复前缀，
// 以兼容 OpenAI 风格（base 含 /v1）与 DeepSeek 风格（base 不含 /v1）两种写法。
func joinUpstream(base, path string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return path
	}
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		return base + path[3:]
	}
	return base + path
}

func detectFormat(path string, body string) string {
	path = strings.TrimSuffix(path, "/")
	if strings.HasSuffix(path, "/v1/chat/completions") || strings.HasSuffix(path, "/chat/completions") {
		return "openai"
	}
	if strings.HasSuffix(path, "/v1/messages") || strings.HasSuffix(path, "/messages") {
		return "anthropic"
	}
	if strings.HasSuffix(path, "/v1/models") || strings.HasSuffix(path, "/models") {
		return "openai"
	}
	if strings.HasSuffix(path, "/version") || strings.HasSuffix(path, "/v1/version") {
		return "version"
	}
	if strings.HasSuffix(path, "/props") || strings.HasSuffix(path, "/v1/props") {
		return "props"
	}

	if !isJSON(body) {
		return "unknown"
	}

	// Parse once and inspect structural fields (avoid fragile string matching)
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return "unknown"
	}

	// Must have a messages array
	messages, hasMessages := data["messages"]
	if !hasMessages {
		return "unknown"
	}
	_, messagesIsArray := messages.([]interface{})
	if !messagesIsArray {
		return "unknown"
	}

	// Strong Anthropic signal: top-level "system" field
	if _, hasSystem := data["system"]; hasSystem {
		return "anthropic"
	}

	// Check for system role inside messages
	hasSystemRole := messagesContainRole(messages.([]interface{}), "system")

	// Anthropic format: has max_tokens + no system role in messages
	// (DeepSeek Anthropic endpoint expects max_tokens)
	_, hasMaxTokens := data["max_tokens"]
	if hasMaxTokens && !hasSystemRole {
		return "anthropic"
	}

	// Everything else with messages is OpenAI format
	return "openai"
}

// messagesContainRole checks if any message in the array has the given role.
func messagesContainRole(messages []interface{}, role string) bool {
	for _, mRaw := range messages {
		m, ok := mRaw.(map[string]interface{})
		if !ok {
			continue
		}
		if r, _ := m["role"].(string); r == role {
			return true
		}
	}
	return false
}

func isJSON(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) > 0 && s[0] == '{'
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func isHopByHop(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailers", "transfer-encoding":
		return true
	}
	return false
}

func truncateBody(s string) string {
	if len(s) > truncateBodyMaxLen {
		half := truncateBodyMaxLen / 2
		omitted := len(s) - truncateBodyMaxLen
		return s[:half] + fmt.Sprintf("\n\n... [中间已省略 %d 字节] ...\n\n", omitted) + s[len(s)-half:]
	}
	return s
}

func condStr(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func extractUsageFromBody(body string) *TokenUsage {
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil
	}
	return parseUsageFromMap(resp)
}

func parseUsageFromMap(data map[string]interface{}) *TokenUsage {
	usageRaw, ok := data["usage"]
	if !ok {
		return nil
	}
	usage, ok := usageRaw.(map[string]interface{})
	if !ok {
		return nil
	}

	u := &TokenUsage{}

	if v, ok := usage["total_tokens"]; ok {
		u.Total = toInt64(v)
	}
	if v, ok := usage["prompt_tokens"]; ok {
		u.Prompt = toInt64(v)
	}
	if v, ok := usage["completion_tokens"]; ok {
		u.Completion = toInt64(v)
	}
	if v, ok := usage["prompt_cache_hit_tokens"]; ok {
		u.CacheHit = toInt64(v)
	}
	if v, ok := usage["prompt_cache_miss_tokens"]; ok {
		u.CacheMiss = toInt64(v)
	}

	// 兼容 OpenAI 官方标准结构: prompt_tokens_details.cached_tokens
	if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
		if v, ok := details["cached_tokens"]; ok {
			u.CacheHit = toInt64(v)
		}
	}
	// 兼容部分代理网关的顶层 cached_tokens
	if v, ok := usage["cached_tokens"]; ok && u.CacheHit == 0 {
		u.CacheHit = toInt64(v)
	}

	// 兼容 Anthropic 格式
	if v, ok := usage["input_tokens"]; ok {
		u.Prompt = toInt64(v)
		u.CacheMiss = u.Prompt
	}
	if v, ok := usage["output_tokens"]; ok {
		u.Completion = toInt64(v)
	}
	if v, ok := usage["cache_read_input_tokens"]; ok {
		u.CacheHit = toInt64(v)
		u.Prompt += u.CacheHit
	}
	if v, ok := usage["cache_creation_input_tokens"]; ok {
		u.CacheMiss += toInt64(v)
	}

	// 统一维护 CacheMiss：如果已知 Prompt 且有了 CacheHit，但 CacheMiss 未给出
	if u.CacheMiss == 0 && u.Prompt > 0 {
		if u.Prompt >= u.CacheHit {
			u.CacheMiss = u.Prompt - u.CacheHit
		} else {
			u.CacheMiss = u.Prompt
		}
	}

	if u.Total == 0 && (u.Prompt > 0 || u.Completion > 0) {
		u.Total = u.Prompt + u.Completion
	}

	return u
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

type captureBuffer struct {
	buf bytes.Buffer
	cap int
}

func newCaptureBuffer(cap int) *captureBuffer {
	return &captureBuffer{cap: cap}
}

func (c *captureBuffer) Write(p []byte) (int, error) {
	remaining := c.cap - c.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
	} else {
		c.buf.Write(p)
	}
	return len(p), nil
}

func (c *captureBuffer) String() string {
	s := c.buf.String()
	if c.buf.Len() >= c.cap {
		s += "\n\n... [stream truncated]"
	}
	return s
}

func getLatestUserMsg(data map[string]interface{}) string {
	if data == nil {
		return ""
	}
	messagesRaw, ok := data["messages"]
	if !ok {
		return ""
	}
	messages, ok := messagesRaw.([]interface{})
	if !ok || len(messages) == 0 {
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "user" {
			if content, ok := m["content"].(string); ok {
				return content
			}
			if contentArr, ok := m["content"].([]interface{}); ok {
				var sb strings.Builder
				for _, blockRaw := range contentArr {
					if block, ok := blockRaw.(map[string]interface{}); ok {
						if t, _ := block["type"].(string); t == "text" {
							if txt, ok := block["text"].(string); ok {
								sb.WriteString(txt)
							}
						}
					}
				}
				return sb.String()
			}
		}
	}
	return ""
}

func (s *ProxyServer) writeCalibrationSSE(
	w http.ResponseWriter,
	flusher http.Flusher,
	canFlush bool,
	capture *captureBuffer,
	format string,
	text string,
	id string,
	model string,
	created float64,
) {
	if id == "" {
		id = "dsplus-calibration-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if model == "" {
		model = "deepseek-chat"
	}
	if created == 0 {
		created = float64(time.Now().Unix())
	}

	var line string
	if format == "openai" {
		chunk := map[string]interface{}{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"delta": map[string]interface{}{
						"reasoning_content": text,
					},
					"finish_reason": nil,
				},
			},
		}
		b, _ := json.Marshal(chunk)
		line = "data: " + string(b) + "\n\n"
	} else {
		// Anthropic format
		chunk := map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
				"type": "thinking_delta",
				"thinking": text,
			},
		}
		b, _ := json.Marshal(chunk)
		line = "data: " + string(b) + "\n\n"
	}

	w.Write([]byte(line))
	capture.Write([]byte(line))
	if canFlush {
		flusher.Flush()
	}
}

func (s *ProxyServer) executeAndStreamAntiHallucination(
	ctx *ProxyContext,
	body []byte,
	secondLogID int64,
	originalData map[string]interface{},
) (string, *TokenUsage, string) {
	startTime := time.Now()
	cfg := s.config.Get()
	flusher, canFlush := ctx.ResponseWriter.(http.Flusher)

	// 发起连接
	retryResp, err := s.executeRetryCall(body, ctx.Format)
	if err != nil {
		log.Printf("[antihallucination] retry call failed: %v", err)
		s.logger.UpdateOnResponse(secondLogID, 502, time.Since(startTime).Milliseconds(), "completed", nil, string(body), "")
		return "", nil, ""
	}
	defer retryResp.Body.Close()

	s.logger.UpdateOnResponse(secondLogID, retryResp.StatusCode, 0, "streaming", nil, string(body), "")

	scanner := bufio.NewScanner(retryResp.Body)
	scanner.Buffer(make([]byte, 0, scannerInitialBuf), scannerMaxBuf)

	var finishReason string
	var lastUsage *TokenUsage
	var contentBuilder strings.Builder
	var respCapture bytes.Buffer
	var lastPushTime time.Time

	var estPrompt int64
	estPrompt = int64(float64(len(body)) / 2.5)

	for scanner.Scan() {
		line := scanner.Text()
		line = replaceDSMLMarkers(line)

		if delta, err := ParseSSELine(line); err == nil && delta != nil {
			if delta.FinishReason != "" {
				finishReason = delta.FinishReason
			}
			if delta.Usage != nil {
				lastUsage = delta.Usage
			}
			if delta.Content != "" {
				contentBuilder.WriteString(delta.Content)
			}
		}

		if _, err := ctx.ResponseWriter.Write([]byte(line + "\n")); err != nil {
			log.Printf("[antihallucination] write client error: %v", err)
			break
		}
		respCapture.Write([]byte(line + "\n"))
		if canFlush {
			flusher.Flush()
		}

		// 实时更新 Token 计数展示
		if contentBuilder.Len() > 0 {
			now := time.Now()
			if lastPushTime.IsZero() || now.Sub(lastPushTime) >= 200*time.Millisecond {
				lastPushTime = now
				estCompletion := int64(float64(contentBuilder.Len()) / 2.3)
				s.logger.UpdateTokenUsageAndStatus(secondLogID, &TokenUsage{
					Prompt:     estPrompt,
					Completion: estCompletion,
					Total:      estPrompt + estCompletion,
				}, "streaming")
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[antihallucination] scanner error: %v", err)
	}

	latency := time.Since(startTime).Milliseconds()
	s.logger.UpdateOnResponse(secondLogID, retryResp.StatusCode, latency, "completed", nil, "", "")
	s.logger.UpdateTotalMs(secondLogID, latency)

	if lastUsage != nil {
		s.logger.UpdateTokenUsageAndStatus(secondLogID, lastUsage, "completed")
	} else {
		estCompletion := int64(float64(contentBuilder.Len()) / 2.3)
		lastUsage = &TokenUsage{
			Prompt:     estPrompt,
			Completion: estCompletion,
			Total:      estPrompt + estCompletion,
		}
		s.logger.UpdateTokenUsageAndStatus(secondLogID, lastUsage, "completed")
	}

	if cfg.VerboseLogging {
		s.logger.UpdateLastResponse(secondLogID, truncateBody(respCapture.String()))
	}

	return finishReason, lastUsage, respCapture.String()
}

func hasToolCallsInDelta(delta *StreamDelta) bool {
	if delta == nil || delta.RawChunk == nil {
		return false
	}
	// OpenAI format tool_calls detection
	if choices, ok := delta.RawChunk["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if d, ok := choice["delta"].(map[string]interface{}); ok {
				if tc, ok := d["tool_calls"]; ok && tc != nil {
					return true
				}
			}
		}
	}
	// Anthropic format tool_use detection
	if typeVal, _ := delta.RawChunk["type"].(string); typeVal == "content_block_start" {
		if block, ok := delta.RawChunk["content_block"].(map[string]interface{}); ok {
			if bType, _ := block["type"].(string); bType == "tool_use" {
				return true
			}
		}
	}
	return false
}

// updateFinalSemanticAction 根据响应结果，在请求结束时更新日志的最终行为定性
func (s *ProxyServer) updateFinalSemanticAction(logID int64, initialAction string, hasToolsInRequest bool, hasToolsInResponse bool, finishReason string) string {
	finalAction := initialAction

	if hasToolsInResponse {
		if initialAction == "工具回传" {
			finalAction = "调用回传"
		} else {
			finalAction = "工具调用"
		}
	} else if finishReason == "stop" {
		finalAction = "完成对话"
	}

	s.logger.UpdateSemanticType(logID, finalAction)
	return finalAction
}
