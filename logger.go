package main

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type TokenUsage struct {
	Prompt     int64 `json:"prompt"`
	Completion int64 `json:"completion"`
	Total      int64 `json:"total"`
	CacheHit   int64 `json:"cache_hit"`
	CacheMiss  int64 `json:"cache_miss"`
}

// RequestAudit 记录单次代理请求「设置页面意图配置」与「重组后请求体实际生效」
// 的对照，供自检脚本核对两者是否一致。Cfg* 为请求时刻的配置快照，Actual* 为
// 解析重组后请求体得到的真实生效值。
type RequestAudit struct {
	Time time.Time `json:"time"`
	Format string  `json:"format"`
	// 意图配置（请求时刻的设置页面快照）
	CfgSystemPromptPlacement string `json:"cfg_system_prompt_placement"`
	CfgExtraPromptPlacement  string `json:"cfg_extra_prompt_placement"`
	CfgExtraPromptEmpty      bool   `json:"cfg_extra_prompt_empty"`
	CfgThinkingMode          string `json:"cfg_thinking_mode"`
	CfgReasoningEffort       string `json:"cfg_reasoning_effort"`
	CfgMaxTokensMode         string `json:"cfg_max_tokens_mode"`
	// 实际生效（解析重组后的请求体得到）
	ActualHasStandaloneSystem    bool   `json:"actual_has_standalone_system"`
	ActualHasSystemPromptTag     bool   `json:"actual_has_system_prompt_tag"`
	ActualHasSupremeInstruction  bool   `json:"actual_has_supreme_instruction"`
	ActualThinkingType           string `json:"actual_thinking_type"`
	ActualReasoningEffort        string `json:"actual_reasoning_effort"`
	ActualMaxTokens              int    `json:"actual_max_tokens"`
}

type LogEntry struct {
	ID               int64             `json:"id"`
	Time             time.Time         `json:"time"`
	Format           string            `json:"format"`
	RequestType      string            `json:"request_type,omitempty"`  // "" = proxy, "antiloop_analyzer", "antiloop_retry"
	Method           string            `json:"method"`
	Path             string            `json:"path"`
	StatusCode       int               `json:"status_code"`
	LatencyMs        int64             `json:"latency_ms"`
	TotalMs          int64             `json:"total_ms"` // 整轮完成耗时（含流式生成），用于首页“总耗时”列
	TokensPerSec     float64           `json:"tokens_per_sec"` // 单请求输出 token 速率 = 输出tokens(含思考) / (总耗时-首响)，单位 token/秒
	Stream           bool              `json:"stream"`
	Transformed      bool              `json:"transformed"`
	HasSystemPrompt  bool              `json:"has_system_prompt"`
	SemanticType     string            `json:"semantic_type,omitempty"`
	SystemEvent      string            `json:"system_event,omitempty"`
	ResponseHeaders  map[string]string `json:"response_headers,omitempty"`
	OriginalBody     string            `json:"original_body,omitempty"`
	TransformedBody  string            `json:"transformed_body,omitempty"`
	ResponseBody     string            `json:"response_body,omitempty"`
	TokenUsage       *TokenUsage       `json:"token_usage,omitempty"`
	Status           string            `json:"status,omitempty"`
	RequestAudit     *RequestAudit     `json:"request_audit,omitempty"`
}

type Logger struct {
	mu          sync.RWMutex
	entries     []LogEntry
	maxSize     int
	nextID      int64
	subscriber  chan LogEntry
}

func NewLogger(maxSize int) *Logger {
	return &Logger{
		entries:    make([]LogEntry, 0, maxSize),
		maxSize:    maxSize,
		subscriber: make(chan LogEntry, 100),
	}
}

func (l *Logger) Add(e LogEntry) int64 {
	id := atomic.AddInt64(&l.nextID, 1)
	e.ID = id
	if e.Time.IsZero() {
		e.Time = time.Now()
	}

	l.mu.Lock()
	if len(l.entries) >= l.maxSize {
		l.entries = l.entries[1:]
	}
	l.entries = append(l.entries, e)
	l.mu.Unlock()

	select {
	case l.subscriber <- e:
	default:
	}
	return id
}

func (l *Logger) UpdateLastResponse(id int64, respBody string) {
	l.mu.Lock()
	var matchedEntry *LogEntry
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].ID == id {
			l.entries[i].ResponseBody = respBody
			matchedEntry = &l.entries[i]
			break
		}
	}
	l.mu.Unlock()

	if matchedEntry != nil {
		l.writeDebugLog(*matchedEntry)
	}
}

func (l *Logger) UpdateTokenUsage(id int64, usage *TokenUsage) {
	l.UpdateTokenUsageAndStatus(id, usage, "")
}

func (l *Logger) UpdateTokenUsageAndStatus(id int64, usage *TokenUsage, status string) {
	l.mu.Lock()
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].ID == id {
			l.entries[i].TokenUsage = usage
			if status != "" {
				l.entries[i].Status = status
			}
			l.recomputeRate(i)
			// broadcast updated entry so frontend gets token stats without refresh
			select {
			case l.subscriber <- l.entries[i]:
			default:
			}
			l.mu.Unlock()
			return
		}
	}
	l.mu.Unlock()
}

func (l *Logger) UpdateOnResponse(id int64, statusCode int, 		latencyMs int64, status string, headers map[string]string, originalBody, transformedBody string) {
	l.mu.Lock()
	var matchedEntry *LogEntry
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].ID == id {
			l.entries[i].StatusCode = statusCode
			l.entries[i].LatencyMs = latencyMs
			if status != "" {
				l.entries[i].Status = status
			}
			if headers != nil {
				l.entries[i].ResponseHeaders = headers
			}
			if originalBody != "" {
				l.entries[i].OriginalBody = originalBody
			}
			if transformedBody != "" {
				l.entries[i].TransformedBody = transformedBody
			}
			l.recomputeRate(i)
			select {
			case l.subscriber <- l.entries[i]:
			default:
			}
			matchedEntry = &l.entries[i]
			break
		}
	}
	l.mu.Unlock()

	if matchedEntry != nil && matchedEntry.Status == "completed" && (matchedEntry.StatusCode >= 400 || matchedEntry.ResponseBody == "") {
		l.writeDebugLog(*matchedEntry)
	}
}

// UpdateTotalMs 在请求/流完全结束后回填整轮完成耗时（total_ms），
// 与首字节延迟（latency_ms）区分：latency_ms 测到首字节，total_ms 测到整轮完成。
func (l *Logger) UpdateTotalMs(id int64, totalMs int64) {
	l.mu.Lock()
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].ID == id {
			l.entries[i].TotalMs = totalMs
			l.recomputeRate(i)
			// 广播更新，使前端实时拿到整轮耗时
			select {
			case l.subscriber <- l.entries[i]:
			default:
			}
			break
		}
	}
	l.mu.Unlock()
}

// recomputeRate 依据「输出 tokens（含思考）/（总耗时 - 首响）」回填单请求 token 速率（token/秒）。
// 严格限制：仅对流式输出（Stream == true）且生成阶段耗时大于等于 200ms 的请求进行计算；
// 非流式请求的生成耗时包含在首响中，其 TotalMs-LatencyMs 仅为网络读包耗时，严禁计算误导。
func (l *Logger) recomputeRate(i int) {
	e := &l.entries[i]
	if !e.Stream || e.TokenUsage == nil || e.TokenUsage.Completion <= 0 {
		e.TokensPerSec = 0
		return
	}
	denomMs := e.TotalMs - e.LatencyMs // 总耗时 - 首响 = 解码生成阶段耗时
	if denomMs < 200 { // 过滤小于 200ms 的极短请求，避免超小分母极端离群值
		e.TokensPerSec = 0
		return
	}
	e.TokensPerSec = float64(e.TokenUsage.Completion) / (float64(denomMs) / 1000.0)
}

// AvgOutputTokensPerSec 按照业界标准（总量加权解码吞吐率），
// 即：所有有效流式请求的总输出 Tokens / 总有效解码生成秒数。
// 彻底杜绝算术平均被短分母异常点带飞的问题。
func (l *Logger) AvgOutputTokensPerSec() float64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var totalCompletion int64
	var totalDenomMs int64
	for _, e := range l.entries {
		if e.Stream && e.TokenUsage != nil && e.TokenUsage.Completion > 0 {
			denomMs := e.TotalMs - e.LatencyMs
			if denomMs >= 200 {
				totalCompletion += e.TokenUsage.Completion
				totalDenomMs += denomMs
			}
		}
	}
	if totalDenomMs <= 0 || totalCompletion <= 0 {
		return 0
	}
	return float64(totalCompletion) / (float64(totalDenomMs) / 1000.0)
}

func (l *Logger) UpdateSemanticType(id int64, semanticType string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].ID == id {
			l.entries[i].SemanticType = semanticType
			select {
			case l.subscriber <- l.entries[i]:
			default:
			}
			return
		}
	}
}

func (l *Logger) UpdateSystemEvent(id int64, systemEvent string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].ID == id {
			l.entries[i].SystemEvent = systemEvent
			select {
			case l.subscriber <- l.entries[i]:
			default:
			}
			return
		}
	}
}

func (l *Logger) Subscribe() <-chan LogEntry {
	return l.subscriber
}

// ToSummary 返回剔除大体积 Body 和复杂审计的轻量级摘要对象，专供列表与 WebSocket 广播
func (e LogEntry) ToSummary() LogEntry {
	copy := e
	copy.OriginalBody = ""
	copy.TransformedBody = ""
	copy.ResponseBody = ""
	copy.RequestAudit = nil
	return copy
}

func (l *Logger) GetAll(limit, offset int) []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	start := len(l.entries) - offset - limit
	if start < 0 {
		start = 0
	}
	end := len(l.entries) - offset
	if end > len(l.entries) {
		end = len(l.entries)
	}
	if start >= end {
		return nil
	}

	result := make([]LogEntry, end-start)
	for i, entry := range l.entries[start:end] {
		entryCopy := entry.ToSummary()
		if entryCopy.TokenUsage != nil {
			usageCopy := *entryCopy.TokenUsage
			entryCopy.TokenUsage = &usageCopy
		}
		if entryCopy.ResponseHeaders != nil {
			headersCopy := make(map[string]string, len(entryCopy.ResponseHeaders))
			for k, v := range entryCopy.ResponseHeaders {
				headersCopy[k] = v
			}
			entryCopy.ResponseHeaders = headersCopy
		}
		result[i] = entryCopy
	}

	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

func (l *Logger) Get(id int64) *LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].ID == id {
			entryCopy := l.entries[i]
			if entryCopy.TokenUsage != nil {
				usageCopy := *entryCopy.TokenUsage
				entryCopy.TokenUsage = &usageCopy
			}
			if entryCopy.ResponseHeaders != nil {
				headersCopy := make(map[string]string, len(entryCopy.ResponseHeaders))
				for k, v := range entryCopy.ResponseHeaders {
					headersCopy[k] = v
				}
				entryCopy.ResponseHeaders = headersCopy
			}
			return &entryCopy
		}
	}
	return nil
}

func (l *Logger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = l.entries[:0]
}

func (l *Logger) Stats() map[string]int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	total := len(l.entries)
	today := 0
	totalTokens := 0
	promptTokens := 0
	completionTokens := 0
	cacheHitTokens := 0
	cacheMissTokens := 0
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	for _, e := range l.entries {
		if e.Time.After(todayStart) {
			today++
		}
		if e.TokenUsage != nil {
			totalTokens += int(e.TokenUsage.Total)
			promptTokens += int(e.TokenUsage.Prompt)
			completionTokens += int(e.TokenUsage.Completion)
			cacheHitTokens += int(e.TokenUsage.CacheHit)
			cacheMissTokens += int(e.TokenUsage.CacheMiss)
		}
	}

	return map[string]int{
		"total":             total,
		"today":             today,
		"total_tokens":      totalTokens,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"cache_hit_tokens":  cacheHitTokens,
		"cache_miss_tokens": cacheMissTokens,
	}
}

func (l *Logger) writeDebugLog(e LogEntry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_ = os.MkdirAll("test", 0755)
	f, err := os.OpenFile("test/proxy_debug_logs.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// WriteAudit 将单次请求的配置意图/实际生效对照追加到 audit JSONL，
// 供独立的自检脚本读取「最近一次」请求并核对一致性。
func (l *Logger) WriteAudit(a *RequestAudit) {
	if a == nil {
		return
	}
	b, err := json.Marshal(a)
	if err != nil {
		return
	}
	_ = os.MkdirAll("test", 0755)
	f, err := os.OpenFile("test/request_audit.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
