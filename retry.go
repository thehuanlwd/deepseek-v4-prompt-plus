package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AntiLoopAnalysis is the result returned by the sub-agent analyzer.
type AntiLoopAnalysis struct {
	Judgment       string `json:"judgment"`        // "loop" | "excessive" | "normal"
	Guidance       string `json:"guidance"`        // guidance text injected into retry
	EnableThinking bool   `json:"enable_thinking"` // whether retry should enable thinking
}

// ── Tuning constants ─────────────────────────────────────────────────────────

const (
	analyzerTimeout     = 15 * time.Second
	analyzerMaxTokens   = 1024
	reasoningSummaryLen = 3000 // max chars of reasoning injected into retry guidance
)

const antiLoopAnalyzerPrompt = `You are a thinking-process analyzer for an AI assistant. Your task: analyze a truncated thinking process to determine whether it got stuck in a repetitive loop or excessive reasoning.

## Judgment Criteria
- "loop": Clear repetitive pattern detected — the same conclusion was verified ≥3 times, or the same reasoning step was repeated ≥3 times without progress.
- "excessive": Thinking is very long but NOT looping — reasoning makes progress but is unnecessarily verbose or over-cautious.
- "normal": The reasoning appears normal and productive; it was merely cut off by the token limit.

## Response Format
You MUST respond with ONLY a valid JSON object (no markdown fences, no extra text):
{
  "judgment": "loop|excessive|normal",
  "guidance": "Instructions to the model for the retry, in Chinese. For 'loop': point out where the loop occurred and tell it to skip repeated verification. For 'excessive': tell it to be concise and avoid over-analysis. For 'normal': tell it to continue from where it left off.",
  "enable_thinking": true or false
}

## Important
- guidance must be in Chinese, speaking directly to the model.
- For "loop" or "excessive", recommend enable_thinking=false (disable thinking on retry to speed up output).
- For "normal", recommend enable_thinking=true (keep thinking on retry).
- Keep guidance under 200 characters.`

// antiLoopAnalyzerPartialPrompt is used when analyzing an IN-PROGRESS thinking
// process (not yet truncated). It focuses on detecting early signs of looping
// or excessive reasoning that warrant preemptive intervention.
const antiLoopAnalyzerPartialPrompt = `You are a real-time thinking-process monitor. Your task: analyze an IN-PROGRESS (not yet finished) thinking process to detect early signs of looping or excessive reasoning. IMPORTANT: the model has been thinking for a long time with NO content output yet. This is a warning sign.

## Judgment Criteria
- "loop": ≥2 repetitions of the same verification, analysis, or reasoning pattern detected. The model is going in circles.
- "excessive": The thinking is very long with NO content output. The model should have reached conclusions by now but is still over-analyzing.
- "normal": ONLY if the reasoning is clearly making distinct forward progress and is close to producing output. If in doubt, lean toward "excessive".

## CRITICAL RULE
If the thinking process shows NO visible content output (the assistant has said nothing yet) AND the thinking has been going on for what appears to be thousands of tokens, this is almost certainly "excessive". A healthy reasoning process should produce some output by now.

## Response Format
You MUST respond with ONLY a valid JSON object (no markdown fences, no extra text):
{
  "judgment": "loop|excessive|normal",
  "guidance": "Instructions in Chinese. For 'excessive': tell it to STOP thinking and immediately output conclusions. For 'loop': point out the repeating pattern and tell it to break out. For 'normal': say '继续推理'.",
  "enable_thinking": true or false
}

## Important
- For "loop" or "excessive", ALWAYS recommend enable_thinking=false (DISABLE thinking in retry to force output).
- For "normal", recommend enable_thinking=true.
- guidance must be in Chinese, under 200 characters.
- ERR ON THE SIDE OF INTERVENTION. A false positive (unnecessary restart) is better than letting the model spiral for thousands more tokens.`

// analyzeResult wraps the parallel analyzer outcome.
type analyzeResult struct {
	analysis *AntiLoopAnalysis
	err      error
}

// needsIntervention returns true if the analysis recommends stopping.
func (ar analyzeResult) needsIntervention() bool {
	return ar.err == nil && ar.analysis != nil &&
		(ar.analysis.Judgment == "loop" || ar.analysis.Judgment == "excessive")
}

// hardLimitMessages provides the fixed error content when the retry also hits token limit.
func hardLimitMessages(format string) []byte {
	content := "抱歉，当前任务超出了处理上限。请尝试将问题拆分为更小的步骤后重新提问。"
	now := time.Now().Unix()

	if format == "openai" {
		resp := map[string]interface{}{
			"id":      "dsplus-antiloop",
			"object":  "chat.completion",
			"created": now,
			"model":   "deepseek-chat",
			"choices": []map[string]interface{}{
				{
					"index":         0,
					"finish_reason": "stop",
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": content,
					},
				},
			},
			"usage": map[string]interface{}{
				"total_tokens":      0,
				"prompt_tokens":     0,
				"completion_tokens": 0,
			},
		}
		b, _ := json.Marshal(resp)
		return b
	}

	// Anthropic format
	resp := map[string]interface{}{
		"id":          "dsplus-antiloop",
		"type":        "message",
		"role":        "assistant",
		"model":       "deepseek-chat",
		"stop_reason": "end_turn",
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": content,
			},
		},
		"usage": map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// detectStreamFinishReason parses the buffered SSE stream to extract
// the finish_reason and accumulated reasoning_content.
func detectStreamFinishReason(buf []byte) (finishReason string, reasoningContent string) {
	lines := strings.Split(string(buf), "\n")
	var reasoningBuilder strings.Builder

	for _, line := range lines {
		if delta, err := ParseSSELine(line); err == nil && delta != nil {
			if delta.FinishReason != "" {
				finishReason = delta.FinishReason
			}
			if delta.ReasoningContent != "" {
				reasoningBuilder.WriteString(delta.ReasoningContent)
			}
		}
	}

	return finishReason, reasoningBuilder.String()
}

// detectBufferFinishReason extracts finish_reason from a non-streaming JSON response.
func detectBufferFinishReason(body []byte) string {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	if choices, ok := resp["choices"].([]interface{}); ok {
		for _, c := range choices {
			if choice, ok := c.(map[string]interface{}); ok {
				if fr, ok := choice["finish_reason"].(string); ok {
					return fr
				}
			}
		}
	}
	// Also check Anthropic stop_reason
	if sr, ok := resp["stop_reason"].(string); ok {
		return sr
	}
	return ""
}

// handleAntiLoop orchestrates the retry flow (non-streaming path).
// It analyzes, builds, executes, and returns the final response body.
func (s *ProxyServer) handleAntiLoop(transformedBody []byte, format string, reasoningContent string, prov Provider) []byte {
	log.Printf("[antiloop] detected finish_reason=length for %s request, starting analysis", format)

	retryBody, err := s.runAntiLoopAnalysis(transformedBody, format, "", reasoningContent, prov)
	if err != nil {
		log.Printf("[antiloop] analyzer failed: %v, falling back to simple retry", err)
		retryBody = s.buildSimpleRetryRequest(transformedBody, format, "", reasoningContent, prov)
	}

	respBody, fr := s.executeRetry(retryBody, format, prov)
	if fr == "length" || fr == "max_tokens" {
		log.Printf("[antiloop] retry also hit limit, giving up")
		return hardLimitMessages(format)
	}

	log.Printf("[antiloop] retry succeeded, finish_reason=%s", fr)
	return replaceDSMLMarkersBytes(respBody)
}

// runAntiLoopAnalysis calls the sub-agent analyzer and builds the retry request.
// phase1Content: the assistant's truncated output from Phase 1.
// reasoningContent: the raw reasoning_content from Phase 1 (sent to analyzer AND injected in retry).
// Returns the retry request body ready for execution, or an error if analysis failed.
func (s *ProxyServer) runAntiLoopAnalysis(transformedBody []byte, format string, phase1Content string, reasoningContent string, prov Provider) ([]byte, error) {
	analysis, err := s.callAntiLoopAnalyzer(transformedBody, format, reasoningContent, prov)
	if err != nil {
		return nil, err
	}

	log.Printf("[antiloop] analysis: judgment=%s enable_thinking=%v", analysis.Judgment, analysis.EnableThinking)
	retryBody := s.buildGuidedRetryRequest(transformedBody, format, analysis, phase1Content, reasoningContent, false, prov)
	return retryBody, nil
}

// writeAntiloopIndicator sends a proper SSE chunk (with full metadata fields)
// to the client so they know the system is re-analyzing before the retry.
func (s *ProxyServer) writeAntiloopIndicator(w http.ResponseWriter, flusher http.Flusher, canFlush bool, capture *captureBuffer, id, model string, created float64) {
	chunk := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"role":    "assistant",
					"content": "\n\n[检测到输出被截断，正在重新整理思路...]\n\n",
				},
				"finish_reason": nil,
			},
		},
	}
	b, _ := json.Marshal(chunk)
	line := "data: " + string(b) + "\n\n"
	w.Write([]byte(line))
	capture.Write([]byte(line))
	if canFlush {
		flusher.Flush()
	}
}

func (s *ProxyServer) executeAndStreamRetry(
	w http.ResponseWriter,
	flusher http.Flusher,
	canFlush bool,
	body []byte,
	format string,
	capture *captureBuffer,
	streamID string,
	prov Provider,
) (string, *TokenUsage) {
	startTime := time.Now()
	reqID := nextTraceReqID()
	cfg := s.config.Get()

	// Always log that retry was attempted (before the call)
	log.Printf("[antiloop] retry attempt: reqID=%d format=%s streamID=%s body_bytes=%d", reqID, format, streamID, len(body))
	traceKeyvals("event", "retry_attempt", "req_id", reqID, "format", format, "body_bytes", len(body))

	retryResp, err := s.executeRetryCall(body, format, prov)
	if err != nil {
		log.Printf("[antiloop] reqID=%d retry call failed: %v", reqID, err)
		s.logger.Add(LogEntry{
			Time:         startTime,
			Format:       format,
			RequestType:  "antiloop_retry",
			Method:       "POST",
			Path:         condStr(format == "anthropic", "/v1/messages", "/v1/chat/completions"),
			StatusCode:   502,
			LatencyMs:    time.Since(startTime).Milliseconds(),
			TotalMs:      time.Since(startTime).Milliseconds(),
			Status:       "completed",
			OriginalBody: condStr(cfg.VerboseLogging, truncateBody(string(body)), ""),
		})
		return "", nil
	}
	defer retryResp.Body.Close()

	// Deferred fallback: ensure a log entry exists no matter what
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[antiloop] reqID=%d RETRY PANIC: %v", reqID, r)
			s.logger.Add(LogEntry{
				Time:        startTime,
				Format:      format,
				RequestType: "antiloop_retry",
				Method:      "POST",
				Path:        condStr(format == "anthropic", "/v1/messages", "/v1/chat/completions"),
				StatusCode:  500,
				LatencyMs:   time.Since(startTime).Milliseconds(),
				TotalMs:     time.Since(startTime).Milliseconds(),
				Status:      "completed",
			})
		}
	}()

	scanner := bufio.NewScanner(retryResp.Body)
	scanner.Buffer(make([]byte, 0, 65536), 1048576)

	var finishReason string
	var lastUsage *TokenUsage
	var respCapture bytes.Buffer
	var retryChunkCount int
	watchdogStop := startProgressWatchdog(fmt.Sprintf("RetryPhase(reqID=%d)", reqID), 60*time.Second)

	for scanner.Scan() {
		watchdogStop = resetProgressWatchdog(watchdogStop)
		retryChunkCount++

		line := scanner.Text()
		line = replaceDSMLMarkers(line)

		// 结束标记统一由 finalizeStream 补发：这里跳过上游的 [DONE]，
		// 否则客户端会在同一轮响应里收到两个结束事件。
		if strings.TrimSpace(line) == "data: [DONE]" {
			continue
		}

		if delta, err := ParseSSELine(line); err == nil && delta != nil {
			delta.RawChunk["id"] = streamID
			if delta.FinishReason != "" {
				finishReason = delta.FinishReason
			}
			if delta.Usage != nil {
				lastUsage = delta.Usage
			}

			b, _ := json.Marshal(delta.RawChunk)
			line = "data: " + string(b)
		}

		if _, err := w.Write([]byte(line + "\n")); err != nil {
			log.Printf("[antiloop] reqID=%d retry write error: %v", reqID, err)
			traceKeyvals("event", "retry_write_error", "req_id", reqID, "error", err.Error())
			break
		}
		capture.Write([]byte(line + "\n"))
		respCapture.Write([]byte(line + "\n"))
		if canFlush {
			flusher.Flush()
		}
	}

	close(watchdogStop)
	if err := scanner.Err(); err != nil {
		log.Printf("[antiloop] reqID=%d retry scanner error: %v", reqID, err)
		traceKeyvals("event", "retry_scanner_error", "req_id", reqID, "error", err.Error())
	} else {
		log.Printf("[antiloop] reqID=%d retry scanner finished (chunks=%d)", reqID, retryChunkCount)
		traceKeyvals("event", "retry_scanner_eof", "req_id", reqID, "chunks", retryChunkCount)
	}

	// Log the retry call
	log.Printf("[antiloop] retry logging: status=%d latency=%dms body_bytes=%d resp_bytes=%d",
		retryResp.StatusCode, time.Since(startTime).Milliseconds(), len(body), respCapture.Len())
	traceKeyvals("event", "retry_logging", "status", retryResp.StatusCode,
		"latency_ms", time.Since(startTime).Milliseconds(), "resp_bytes", respCapture.Len())
	retryLogID := s.logger.Add(LogEntry{
		Time:         startTime,
		Format:       format,
		RequestType:  "antiloop_retry",
		Method:       "POST",
		Path:         "/chat/completions (防循环重试-stream)",
		StatusCode:   retryResp.StatusCode,
		LatencyMs:    time.Since(startTime).Milliseconds(),
		OriginalBody: condStr(cfg.VerboseLogging, truncateBody(string(body)), ""),
		ResponseBody: condStr(cfg.VerboseLogging, truncateBody(respCapture.String()), ""),
	})
	log.Printf("[antiloop] retry logged: id=%d", retryLogID)
	// 重试请求同样要落「已完成」状态与整轮耗时，否则仪表盘会留下一行未完成的记录。
	s.logger.UpdateTokenUsageAndStatus(retryLogID, lastUsage, "completed")
	s.logger.UpdateTotalMs(retryLogID, time.Since(startTime).Milliseconds())

	return finishReason, lastUsage
}

// streamHardLimitSSE sends the "triggered output limit" message as a proper SSE chunk.
func (s *ProxyServer) streamHardLimitSSE(w http.ResponseWriter, flusher http.Flusher, canFlush bool, format string, capture *captureBuffer, id, model string, created float64) {
	chunk := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"role":    "assistant",
					"content": "抱歉，当前任务超出了处理上限。请尝试将问题拆分为更小的步骤后重新提问。",
				},
				"finish_reason": "stop",
			},
		},
	}
	b, _ := json.Marshal(chunk)
	line := "data: " + string(b) + "\n\n"
	w.Write([]byte(line))
	capture.Write([]byte(line))
	if canFlush {
		flusher.Flush()
	}
}

// executeRetryCall makes the retry HTTP request and returns the response for streaming.
// Caller is responsible for closing resp.Body.
func (s *ProxyServer) executeRetryCall(body []byte, format string, prov Provider) (*http.Response, error) {
	upstream := s.resolveBaseURL(prov, format)
	path := "/chat/completions"
	if format == "anthropic" {
		path = "/v1/messages"
	}

	req, err := http.NewRequest("POST", joinUpstream(upstream, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	key := s.providerAPIKey(prov)
	req.Header.Set("Authorization", "Bearer "+key)
	if format == "anthropic" {
		req.Header.Set("x-api-key", key)
	}

	return s.client.Do(req)
}
func (s *ProxyServer) callAntiLoopAnalyzer(transformedBody []byte, format string, reasoningContent string, prov Provider) (*AntiLoopAnalysis, error) {
	analysisBody, err := buildAnalyzerRequest(transformedBody, format, reasoningContent)
	if err != nil {
		return nil, fmt.Errorf("build analyzer request: %w", err)
	}
	return s.callAntiLoopAnalyzerWith(analysisBody, "思维分析", prov)
}

// parallelAnalyze is called from a goroutine to analyze the in-progress thinking.
// It sends the result (or error) to the provided channel.
func (s *ProxyServer) parallelAnalyze(
	done chan<- analyzeResult,
	transformedBody []byte,
	format string,
	reasoningSnapshot string,
	phase1Content string,
	prov Provider,
) {
	analysis, err := s.callAntiLoopAnalyzerPartial(transformedBody, format, reasoningSnapshot, phase1Content, prov)
	done <- analyzeResult{analysis, err}
}

// callAntiLoopAnalyzerPartial is like callAntiLoopAnalyzer but uses a different
// prompt optimized for IN-PROGRESS thinking detection (not truncated).
func (s *ProxyServer) callAntiLoopAnalyzerPartial(
	transformedBody []byte,
	format string,
	reasoningContent string,
	phase1Content string,
	prov Provider,
) (*AntiLoopAnalysis, error) {
	analysisBody, err := buildAnalyzerRequestPartial(transformedBody, format, reasoningContent, phase1Content)
	if err != nil {
		return nil, fmt.Errorf("build partial analyzer request: %w", err)
	}
	return s.callAntiLoopAnalyzerWith(analysisBody, "并行思维分析", prov)
}

// callAntiLoopAnalyzerWith is the unified implementation for calling the analyzer
// sub-agent.  pathLabel distinguishes the two call sites in log entries.
// prov 为本次请求命中的供应商：分析子Agent与主请求共用同一上游，避免多供应商下打到别家。
func (s *ProxyServer) callAntiLoopAnalyzerWith(analysisBody []byte, pathLabel string, prov Provider) (*AntiLoopAnalysis, error) {
	startTime := time.Now()
	cfg := s.config.Get()

	// 分析子Agent固定走 OpenAI 格式端点；按格式解析可避免忽略 AnthropicBaseURL 的旧硬编码问题。
	upstreamURL := joinUpstream(s.resolveBaseURL(prov, "openai"), "/chat/completions")

	req, err := http.NewRequest("POST", upstreamURL, bytes.NewReader(analysisBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.providerAPIKey(prov))

	client := &http.Client{Timeout: analyzerTimeout}
	resp, err := client.Do(req)
	if err != nil {
		s.logger.Add(LogEntry{
			Time:        startTime,
			Format:      "openai",
			RequestType: "antiloop_analyzer",
			Method:      "POST",
			Path:        "/v1/chat/completions",
			StatusCode:  502,
			LatencyMs:   time.Since(startTime).Milliseconds(),
			TotalMs:     time.Since(startTime).Milliseconds(),
			Status:      "completed",
		})
		return nil, fmt.Errorf("analyzer API call: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		s.logger.Add(LogEntry{
			Time:        startTime,
			Format:      "openai",
			RequestType: "antiloop_analyzer",
			Method:      "POST",
			Path:        "/v1/chat/completions",
			StatusCode:  resp.StatusCode,
			LatencyMs:   time.Since(startTime).Milliseconds(),
			TotalMs:     time.Since(startTime).Milliseconds(),
			Status:      "completed",
		})
		return nil, fmt.Errorf("read analyzer response: %w", err)
	}

	// Log the analyzer call
	analyzerLogID := s.logger.Add(LogEntry{
		Time:         startTime,
		Format:       "openai",
		RequestType:  "antiloop_analyzer",
		Method:       "POST",
		Path:         "/v1/chat/completions",
		StatusCode:   resp.StatusCode,
		LatencyMs:    time.Since(startTime).Milliseconds(),
		OriginalBody: condStr(cfg.VerboseLogging, truncateBody(string(analysisBody)), ""),
		ResponseBody: condStr(cfg.VerboseLogging, truncateBody(string(respBytes)), ""),
	})

	// Parse OpenAI response
	var oaiResp map[string]interface{}
	if err := json.Unmarshal(respBytes, &oaiResp); err != nil {
		return nil, fmt.Errorf("parse analyzer response: %w", err)
	}

	// 分析子 Agent 也是一次完整调用，同样要回填用量、状态与耗时。
	s.logger.UpdateTokenUsageAndStatus(analyzerLogID, parseUsageFromMap(oaiResp), "completed")
	s.logger.UpdateTotalMs(analyzerLogID, time.Since(startTime).Milliseconds())

	choices, ok := oaiResp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return nil, fmt.Errorf("no choices in analyzer response")
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid choice format")
	}

	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("no message in analyzer choice")
	}

	content, _ := message["content"].(string)
	if content == "" {
		return nil, fmt.Errorf("empty analyzer response")
	}

	// Strip markdown fences if present
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var analysis AntiLoopAnalysis
	if err := json.Unmarshal([]byte(content), &analysis); err != nil {
		return nil, fmt.Errorf("parse analysis JSON: %w (content: %s)", err, truncateBody(content))
	}

	// 提交分析器 TraceEvent
	analyzerEventID := "ana_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var analyzerBody map[string]interface{}
	json.Unmarshal(analysisBody, &analyzerBody)

	anaEv := NewTraceEvent(
		startTime,
		analyzerLogID,
		"",
		0,
		"analyzer",
		"openai",
		"/chat/completions ("+pathLabel+")",
		resp.StatusCode,
		time.Since(startTime).Milliseconds(),
		"deepseek-chat",
		s.resolveBaseURL(prov, "openai"),
		buildRequestMeta(analyzerBody, "openai", false, false),
		ResponseMeta{
			AnalyzerJudgment: analysis.Judgment,
			FinishReason:     "stop",
		},
		string(analysisBody),
		string(respBytes),
	)
	anaEv.UpstreamRequest = anaEv.RawRequest
	anaEv.ID = analyzerEventID
	s.analysisSvc.SubmitEvent(anaEv)

	return &analysis, nil
}

// buildAnalyzerRequest creates the request body for the sub-agent analyzer.
func buildAnalyzerRequest(transformedBody []byte, format string, reasoningContent string) ([]byte, error) {
	return buildAnalyzerRequestWith(transformedBody, reasoningContent, "", antiLoopAnalyzerPrompt, 8000)
}

// buildAnalyzerRequestPartial creates the request body for the parallel (in-progress) analyzer.
func buildAnalyzerRequestPartial(transformedBody []byte, format string, reasoningContent string, phase1Content string) ([]byte, error) {
	return buildAnalyzerRequestWith(transformedBody, reasoningContent, phase1Content, antiLoopAnalyzerPartialPrompt, 6000)
}

// buildAnalyzerRequestWith is the unified implementation for both analyzer variants.
func buildAnalyzerRequestWith(transformedBody []byte, reasoningContent, phase1Content, prompt string, maxReasoningLen int) ([]byte, error) {
	var original map[string]interface{}
	if err := json.Unmarshal(transformedBody, &original); err != nil {
		return nil, err
	}

	var messages []interface{}
	if msgs, ok := original["messages"].([]interface{}); ok {
		messages = msgs
	} else {
		messages = []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": fmt.Sprintf("Original request (Anthropic format):\n%s", string(transformedBody)),
			},
		}
	}

	messagesJSON, _ := json.MarshalIndent(messages, "", "  ")

	truncatedReasoning := reasoningContent
	if len(truncatedReasoning) > maxReasoningLen {
		half := maxReasoningLen / 2
		truncatedReasoning = truncatedReasoning[:half] + "\n\n... [中间省略] ...\n\n" + truncatedReasoning[len(truncatedReasoning)-half:]
	}

	var userContent string
	if phase1Content != "" {
		userContent = fmt.Sprintf(`## 完整对话上下文
%s

## 当前已输出的内容（部分）
%s

## 正在进行的思考过程（部分）
%s

请分析上述思考过程是否已出现循环迹象或过度推理，并返回 JSON 结果。`, string(messagesJSON), truncateBody(phase1Content), truncatedReasoning)
	} else {
		userContent = fmt.Sprintf(`## 完整对话上下文
%s

## 被截断的思考过程 (reasoning_content)
%s

请分析上述思考过程是否陷入死循环或过度推理，并返回 JSON 结果。`, string(messagesJSON), truncatedReasoning)
	}

	analyzerReq := map[string]interface{}{
		"model": "deepseek-chat",
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": prompt,
			},
			{
				"role":    "user",
				"content": userContent,
			},
		},
		"max_tokens":      analyzerMaxTokens,
		"response_format": map[string]interface{}{"type": "json_object"},
		"thinking":        map[string]interface{}{"type": "disabled"},
	}

	return json.Marshal(analyzerReq)
}

// buildGuidedRetryRequest creates the retry request body with full context from Phase 1,
// the analyzer's guidance, and the configured retry model/thinking settings.
func (s *ProxyServer) buildGuidedRetryRequest(transformedBody []byte, format string, analysis *AntiLoopAnalysis, phase1Content string, reasoningContent string, earlyStop bool, prov Provider) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(transformedBody, &data); err != nil {
		return transformedBody
	}

	cfg := s.config.Get()

	// 1. Replace model with configured retry model
	data["model"] = cfg.AntiLoopRetryModel

	// 2. Remove max_tokens to give full room for retry
	delete(data, "max_tokens")

	// 3. Apply retry thinking configuration
	s.applyRetryThinking(data, format, analysis, prov)

	// 4. Append Phase 1 context as new messages
	s.injectRetryContext(data, format, phase1Content, reasoningContent, analysis, earlyStop)

	b, err := json.Marshal(data)
	if err != nil {
		return transformedBody
	}
	return b
}

// applyRetryThinking sets thinking/effort on the retry request based on config + analysis and request format.
func (s *ProxyServer) applyRetryThinking(data map[string]interface{}, format string, analysis *AntiLoopAnalysis, prov Provider) {
	cfg := s.config.Get()
	mode := cfg.AntiLoopRetryThinking
	// DeepSeek 系列按官方档位收敛；其他厂商保持既有归一化策略不变。
	// 注意：调用方已把 data["model"] 换成重试模型，因此这里按重试模型与命中供应商共同判定。
	deepSeek := s.isDeepSeekModelFor(data, format, prov)
	if mode == "" {
		// Not set: use analyzer's recommendation (or disable thinking if analysis is nil / recommends disabled)
		if analysis == nil || !analysis.EnableThinking {
			data["thinking"] = map[string]interface{}{"type": "disabled"}
			delete(data, "reasoning_effort")
			delete(data, "output_config")
		}
		return
	}
	if mode == "disabled" {
		data["thinking"] = map[string]interface{}{"type": "disabled"}
		data["reasoning_effort"] = "none"
		data["reasoning"] = map[string]interface{}{"effort": "none"}
		delete(data, "output_config")
	} else if mode == "enabled" {
		if format == "anthropic" {
			delete(data, "reasoning_effort")
			delete(data, "reasoning")
			if cfg.AntiLoopRetryEffort != "" {
				data["output_config"] = map[string]interface{}{"effort": sanitizeReasoningEffortFor(cfg.AntiLoopRetryEffort, deepSeek)}
			}
		} else {
			data["thinking"] = map[string]interface{}{"type": "enabled"}
			if cfg.AntiLoopRetryEffort != "" {
				eff := sanitizeReasoningEffortFor(cfg.AntiLoopRetryEffort, deepSeek)
				data["reasoning_effort"] = eff
				data["reasoning"] = map[string]interface{}{"effort": eff}
			}
		}
	}
}

// injectRetryContext appends Phase 1's output and the analyzer's guidance as new messages.
// earlyStop: true if the retry was triggered preemptively (not by finish_reason=length).
func (s *ProxyServer) injectRetryContext(data map[string]interface{}, format string, phase1Content string, reasoningContent string, analysis *AntiLoopAnalysis, earlyStop bool) {
	messagesRaw, ok := data["messages"]
	if !ok {
		return
	}
	messages, ok := messagesRaw.([]interface{})
	if !ok {
		return
	}

	// Append assistant message: the truncated Phase 1 output
	if phase1Content != "" {
		messages = append(messages, map[string]interface{}{
			"role":    "assistant",
			"content": phase1Content,
		})
	}

	// Build guidance: analysis + reasoning summary
	var guidanceText string
	if earlyStop {
		guidanceText = "你的推理被主动中断——检测到"
		if analysis != nil {
			guidanceText += analysis.Judgment + "（" + analysis.Guidance + "）"
		} else {
			guidanceText += "思维过程过长或存在循环"
		}
		guidanceText += "。\n\n"
	} else {
		guidanceText = "你的上一轮回答因输出超长被截断，但已取得部分进展。\n\n"
	}
	if analysis != nil && !earlyStop {
		guidanceText += "分析判定：" + analysis.Judgment + "\n"
		guidanceText += "改进指导：" + analysis.Guidance + "\n\n"
	} else if analysis == nil {
		guidanceText += "请精简思考过程，直接给出最终结论。\n\n"
	}

	// Include condensed reasoning summary (max 3000 chars)
	if len(reasoningContent) > 0 {
		summary := reasoningContent
		const maxSummary = reasoningSummaryLen
		if len(summary) > maxSummary {
			half := maxSummary / 2
			summary = summary[:half] + "\n\n... [中间省略] ...\n\n" + summary[len(summary)-half:]
		}
		guidanceText += "你的思考过程摘要：\n" + summary + "\n\n"
	}

	guidanceText += "请从断点继续完成任务，不要重复已经说过的内容。对于已经得出的正确结论可以直接引用。"

	// Append user message with guidance
	messages = append(messages, map[string]interface{}{
		"role":    "user",
		"content": guidanceText,
	})

	data["messages"] = messages
}

// buildSimpleRetryRequest creates a retry request with generic guidance (no analyzer).
func (s *ProxyServer) buildSimpleRetryRequest(transformedBody []byte, format string, phase1Content string, reasoningContent string, prov Provider) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(transformedBody, &data); err != nil {
		return transformedBody
	}

	cfg := s.config.Get()

	// Replace model with configured retry model
	data["model"] = cfg.AntiLoopRetryModel

	// Remove max_tokens
	delete(data, "max_tokens")

	// Apply retry thinking config (disabled by default)
	s.applyRetryThinking(data, format, nil, prov)

	// Inject Phase 1 context
	s.injectRetryContext(data, format, phase1Content, reasoningContent, nil, false)

	b, err := json.Marshal(data)
	if err != nil {
		return transformedBody
	}
	return b
}

// executeRetry sends the retry request to the upstream API and returns the
// full response body along with the finish_reason.
func (s *ProxyServer) executeRetry(body []byte, format string, prov Provider) (responseBody []byte, finishReason string) {
	startTime := time.Now()
	cfg := s.config.Get()

	upstream := s.resolveBaseURL(prov, format)
	path := "/chat/completions"
	if format == "anthropic" {
		path = "/v1/messages"
	}

	req, err := http.NewRequest("POST", joinUpstream(upstream, path), bytes.NewReader(body))
	if err != nil {
		log.Printf("[antiloop] retry request error: %v", err)
		return hardLimitMessages(format), "stop"
	}
	req.Header.Set("Content-Type", "application/json")
	key := s.providerAPIKey(prov)
	req.Header.Set("Authorization", "Bearer "+key)
	if format == "anthropic" {
		req.Header.Set("x-api-key", key)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("[antiloop] retry upstream error: %v", err)
		s.logger.Add(LogEntry{
			Time:         startTime,
			Format:       format,
			RequestType:  "antiloop_retry",
			Method:       "POST",
			Path:         path,
			StatusCode:   502,
			LatencyMs:    time.Since(startTime).Milliseconds(),
			OriginalBody: condStr(cfg.VerboseLogging, truncateBody(string(body)), ""),
		})
		return hardLimitMessages(format), "stop"
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[antiloop] retry read error: %v", err)
		return hardLimitMessages(format), "stop"
	}

	// Check Content-Type to determine if streaming
	contentType := resp.Header.Get("Content-Type")
	isStream := strings.Contains(contentType, "text/event-stream")

	var fr string
	if isStream {
		fr, _ = detectStreamFinishReason(respBytes)
	} else {
		fr = detectBufferFinishReason(respBytes)
	}

	// ── Log the retry call ──
	retryLogID := s.logger.Add(LogEntry{
		Time:         startTime,
		Format:       format,
		RequestType:  "antiloop_retry",
		Method:       "POST",
		Path:         path,
		StatusCode:   resp.StatusCode,
		LatencyMs:    time.Since(startTime).Milliseconds(),
		OriginalBody: condStr(cfg.VerboseLogging, truncateBody(string(body)), ""),
		ResponseBody: condStr(cfg.VerboseLogging, truncateBody(string(respBytes)), ""),
	})

	// Extract and update token usage
	if u := extractUsageFromBody(string(respBytes)); u != nil {
		s.logger.UpdateTokenUsage(retryLogID, u)
	}

	return respBytes, fr
}
