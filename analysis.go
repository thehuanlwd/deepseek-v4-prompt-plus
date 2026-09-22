package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ChatMessage struct {
	Role             string   `json:"role"`
	Content          string   `json:"content,omitempty"`
	ReasoningContent string   `json:"reasoning_content,omitempty"`
	ToolCalls        []string `json:"tool_calls,omitempty"`
}

type ToolMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// TraceEvent 代表代理过程中的一次结构化事实事件
type TraceEvent struct {
	ID              string        `json:"id"`
	ParentID        string        `json:"parent_id,omitempty"` // 用于关联重试或分析子事件
	Time            time.Time     `json:"time"`
	LogID           int64         `json:"log_id"`
	SessionID       string        `json:"session_id"`
	TurnID          int           `json:"turn_id"`
	Phase           string        `json:"phase"` // primary, analyzer, retry, debug
	Format          string        `json:"format"`
	Route           string        `json:"route"`
	Status          int           `json:"status"`
	LatencyMs       int64         `json:"latency_ms"`    // 整轮完成耗时（首字节 → 流结束），历史字段，与新加的 total_ms 同源
	FirstByteMs     int64         `json:"first_byte_ms"` // 首响：请求进入 → 上游首字节（TTFT），与首页 latency_ms 一致
	TotalMs         int64         `json:"total_ms"`      // 总耗时：请求进入 → 整轮完成，与首页 total_ms 列一致
	Model           string        `json:"model"`
	Upstream        string        `json:"upstream"`
	Request         RequestMeta   `json:"request"`
	Response        ResponseMeta  `json:"response"`
	RawRequest      string        `json:"raw_request,omitempty"` // 详细记录时可用，持久化可选择性丢弃
	UpstreamRequest string        `json:"upstream_request,omitempty"`
	RawResponse     string        `json:"raw_response,omitempty"` // 详细记录时可用，持久化可选择性丢弃
	ChatHistory     []ChatMessage `json:"chat_history,omitempty"` // 持久化清洗后的聊天历史
	StorageError    string        `json:"storage_error,omitempty"`
}

// RequestMeta 存储请求关键元数据
type RequestMeta struct {
	Model                   string         `json:"model"`
	Stream                  bool           `json:"stream"`
	ThinkingMode            string         `json:"thinking_mode"`
	ReasoningEffort         string         `json:"reasoning_effort"`
	MaxTokens               int            `json:"max_tokens"`
	MessageCount            int            `json:"message_count"`
	RoleCounts              map[string]int `json:"role_counts"`
	LastUserSummary         string         `json:"last_user_summary"`
	SystemPromptTransformed bool           `json:"system_prompt_transformed"`
	ExtraPromptInjected     bool           `json:"extra_prompt_injected"`
	SemanticType            string         `json:"semantic_type,omitempty"`
	Tools                   []string       `json:"tools,omitempty"`
	ToolsDetail             []ToolMeta     `json:"tools_detail,omitempty"`
}

// ResponseMeta 存储响应元数据
type ResponseMeta struct {
	FinishReason      string   `json:"finish_reason"`
	PromptTokens      int      `json:"prompt_tokens"`
	CompletionTokens  int      `json:"completion_tokens"`
	TotalTokens       int      `json:"total_tokens"`
	CacheHitTokens    int      `json:"cache_hit_tokens"`
	CacheMissTokens   int      `json:"cache_miss_tokens"`
	ReasoningTokens   int      `json:"reasoning_tokens"`
	ReasoningContent  string   `json:"reasoning_content"`
	Content           string   `json:"content,omitempty"`
	ToolCalls         []string `json:"tool_calls,omitempty"`
	AntiLoopTriggered bool     `json:"antiloop_triggered"`
	AnalyzerJudgment  string   `json:"analyzer_judgment"`
	RetryModel        string   `json:"retry_model"`
}

// ConversationTurn 代表一轮完整的对话（包含主请求，潜在的防循环判定，重试等）
type ConversationTurn struct {
	TurnID            int           `json:"turn_id"`
	StartTime         time.Time     `json:"start_time"`
	UserMessage       string        `json:"user_message"`
	SystemModified    bool          `json:"system_modified"`
	ExtraInjected     bool          `json:"extra_injected"`
	Events            []*TraceEvent `json:"events"`
	AssistantResponse string        `json:"assistant_response"`
	ReasoningContent  string        `json:"reasoning_content"`
	ToolCalls         []string      `json:"tool_calls"`
	ChatHistory       []ChatMessage `json:"chat_history,omitempty"` // 新增：保存清洗后的整条请求交互历史
}

// ConversationSession 代表会话维度的会话对象
type ConversationSession struct {
	ID               string                    `json:"id"`
	StartTime        time.Time                 `json:"start_time"`
	EndTime          time.Time                 `json:"end_time"`
	RequestCount     int                       `json:"request_count"`
	Models           []string                  `json:"models"`
	PromptTokens     int                       `json:"prompt_tokens"`
	CompletionTokens int                       `json:"completion_tokens"`
	TotalTokens      int                       `json:"total_tokens"`
	CacheHitTokens   int                       `json:"cache_hit_tokens"`
	CacheMissTokens  int                       `json:"cache_miss_tokens"`
	Turns            map[int]*ConversationTurn `json:"turns"`
	Errors           int                       `json:"errors"`
	Retries          int                       `json:"retries"`
	GroupingReason   string                    `json:"grouping_reason"`
}

// SessionSummary 返回给 UI 的紧凑结构
type SessionSummary struct {
	ID               string    `json:"id"`
	StartTime        time.Time `json:"start_time"`
	EndTime          time.Time `json:"end_time"`
	RequestCount     int       `json:"request_count"`
	Models           string    `json:"models"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CacheHitRatio    float64   `json:"cache_hit_ratio"`
	Errors           int       `json:"errors"`
	Retries          int       `json:"retries"`
	SummaryText      string    `json:"summary_text"`
	Format           string    `json:"format"`
	Status           int       `json:"status"`
}

type TimelineItem struct {
	Type             string      `json:"type"`
	SessionID        string      `json:"session_id"`
	TurnID           int         `json:"turn_id"`
	Role             string      `json:"role,omitempty"`
	Phase            string      `json:"phase,omitempty"`
	LogID            int64       `json:"log_id,omitempty"`
	Time             time.Time   `json:"time"`
	Preview          string      `json:"preview,omitempty"` // 用于存储完整的内容文本
	ReasoningContent string      `json:"reasoning_content,omitempty"`
	MessageIdx       int         `json:"message_idx,omitempty"`
	Event            *TraceEvent `json:"event,omitempty"`
	Tools            []ToolMeta  `json:"tools,omitempty"` // 可用工具列表详情
	ToolCalls        []string    `json:"tool_calls,omitempty"` // 新增：工具调用详情列表
}

type TimelinePage struct {
	Items  []TimelineItem `json:"items"`
	Total  int            `json:"total"`
	Offset int            `json:"offset"`
	Limit  int            `json:"limit"`
}

// AnalysisService 核心分析服务
type AnalysisService struct {
	config         *SafeConfig
	logger         *Logger
	lock           sync.RWMutex
	sessions       map[string]*ConversationSession
	eventChan      chan *TraceEvent
	logDir         string
	shutdownCh     chan struct{}
	writtenHashes  map[string]bool // 今日已写盘的哈希表
	currentLogDate string          // 当前日志文件名对应的日期 2006-01-02
}

var (
	globalAnalysisService *AnalysisService
	analysisOnce          sync.Once
)

// InitAnalysisService 初始化全局分析服务
func InitAnalysisService(cfg *SafeConfig, logger *Logger) *AnalysisService {
	analysisOnce.Do(func() {
		exe, _ := os.Executable()
		logDir := filepath.Join(filepath.Dir(exe), "analysis_logs")

		globalAnalysisService = &AnalysisService{
			config:         cfg,
			logger:         logger,
			sessions:       make(map[string]*ConversationSession),
			eventChan:      make(chan *TraceEvent, 2000), // 异步事件缓冲区
			logDir:         logDir,
			shutdownCh:     make(chan struct{}),
			writtenHashes:  make(map[string]bool),
			currentLogDate: "",
		}

		if cfg.Get().AnalysisEnabled {
			// 创建日志目录
			if err := os.MkdirAll(logDir, 0755); err != nil {
				log.Printf("[analysis] failed to create directory %s: %v", logDir, err)
			}
			// 重启时同步从磁盘加载历史日志还原内存会话
			globalAnalysisService.loadHistoryFromDisk()

			go globalAnalysisService.runWorker()
			go globalAnalysisService.cleanupOldLogs()
		}
	})
	return globalAnalysisService
}

// GetAnalysisService 获取全局分析服务实例
func GetAnalysisService() *AnalysisService {
	return globalAnalysisService
}

// SubmitEvent 提交 TraceEvent 至服务，非阻塞
func (s *AnalysisService) SubmitEvent(ev *TraceEvent) {
	if s == nil || !s.config.Get().AnalysisEnabled {
		return
	}
	// 仅过滤聊天 completions/messages 相关的路由
	route := ev.Route
	if ev.Phase == "primary" &&
		!strings.Contains(route, "/v1/chat/completions") &&
		!strings.Contains(route, "/v1/messages") {
		return
	}
	select {
	case s.eventChan <- ev:
	default:
		log.Printf("[analysis] warning: event queue full, dropping event %s", ev.ID)
	}
}

// Stop 停止分析服务
func (s *AnalysisService) Stop() {
	if s == nil {
		return
	}
	close(s.shutdownCh)
}

func (s *AnalysisService) runWorker() {
	for {
		select {
		case ev := <-s.eventChan:
			s.processEvent(ev)
		case <-s.shutdownCh:
			return
		}
	}
}

func (s *AnalysisService) processEvent(ev *TraceEvent) {
	s.lock.Lock()
	defer s.lock.Unlock()

	// 回填首响/总耗时，与首页 latency_ms / total_ms 对齐
	if s.logger != nil {
		if entry := s.logger.Get(ev.LogID); entry != nil {
			ev.FirstByteMs = entry.LatencyMs
			ev.TotalMs = entry.TotalMs
		}
	}

	// 1. 进行 Session 归拢与推导
	s.inferSession(ev)

	// 2. 内存聚合
	sess, exists := s.sessions[ev.SessionID]
	if !exists {
		sess = &ConversationSession{
			ID:             ev.SessionID,
			StartTime:      ev.Time,
			EndTime:        ev.Time,
			Turns:          make(map[int]*ConversationTurn),
			GroupingReason: ev.Phase,
		}
		s.sessions[ev.SessionID] = sess
	}

	if ev.Time.Before(sess.StartTime) {
		sess.StartTime = ev.Time
	}
	if ev.Time.After(sess.EndTime) {
		sess.EndTime = ev.Time
	}

	// 累计计数
	sess.RequestCount++
	sess.PromptTokens += ev.Response.PromptTokens
	sess.CompletionTokens += ev.Response.CompletionTokens
	sess.TotalTokens += ev.Response.TotalTokens
	sess.CacheHitTokens += ev.Response.CacheHitTokens
	sess.CacheMissTokens += ev.Response.CacheMissTokens

	if ev.Status >= 400 {
		sess.Errors++
	}
	if ev.Phase == "retry" {
		sess.Retries++
	}

	// 更新模型列表
	foundModel := false
	for _, m := range sess.Models {
		if m == ev.Model {
			foundModel = true
			break
		}
	}
	if !foundModel && ev.Model != "" {
		sess.Models = append(sess.Models, ev.Model)
	}

	// 合流至 Turn 结构
	turn, turnExists := sess.Turns[ev.TurnID]
	if !turnExists {
		turn = &ConversationTurn{
			TurnID:    ev.TurnID,
			StartTime: ev.Time,
		}
		sess.Turns[ev.TurnID] = turn
	}

	turn.Events = append(turn.Events, ev)

	// 收集文本段以展示聊天流
	if ev.Phase == "primary" {
		if ev.Request.LastUserSummary == "" && ev.RawRequest != "" {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(ev.RawRequest), &data); err == nil {
				ev.Request = buildRequestMeta(data, ev.Format, ev.Request.SystemPromptTransformed, ev.Request.ExtraPromptInjected)
			}
		}
		turn.UserMessage = ev.Request.LastUserSummary
		turn.SystemModified = ev.Request.SystemPromptTransformed
		turn.ExtraInjected = ev.Request.ExtraPromptInjected
	}

	// 如果 primary 包含 response_body 或者是 retry 阶段的 response_body，我们将其作为 assistant 的回复
	if ev.Phase == "primary" || ev.Phase == "retry" {
		if ev.Response.ReasoningContent != "" {
			turn.ReasoningContent = ev.Response.ReasoningContent
		} else if ev.RawResponse != "" {
			if reasoning := parseAssistantReasoning(ev.RawResponse, ev.Format); reasoning != "" {
				turn.ReasoningContent = reasoning
			}
		}
		ev.Response.ReasoningContent = turn.ReasoningContent

		// 优先使用 Response.Content，否则从 RawResponse 提取
		if ev.Response.Content != "" {
			turn.AssistantResponse = ev.Response.Content
		} else if ev.RawResponse != "" {
			if content := parseAssistantContent(ev.RawResponse, ev.Format); content != "" {
				turn.AssistantResponse = content
			}
		}
		ev.Response.Content = turn.AssistantResponse

		// 收集 ToolCalls
		if ev.RawResponse != "" {
			if tools := parseAssistantToolCalls(ev.RawResponse, ev.Format); len(tools) > 0 {
				turn.ToolCalls = tools
				ev.Response.ToolCalls = tools
			}
		}

		// 提取清洗后的 ChatHistory 并存入 turn 和 ev
		if ev.RawRequest != "" {
			lastHistoryLen := 0
			var prevHistory []ChatMessage
			if sess != nil {
				prevHistory = sess.getFullChatHistory()
				lastHistoryLen = len(prevHistory)
			}
			incremental := extractChatHistory(ev.RawRequest, ev.Format, turn.AssistantResponse, turn.ReasoningContent, turn.ToolCalls, true, lastHistoryLen)
			turn.ChatHistory = incremental
			ev.ChatHistory = incremental
		}
	}

	// 3. Analysis 开启即持久化；写盘失败不能影响代理主链路。
	s.writeToDisk(ev)
	if ev.Phase == "primary" || ev.Phase == "retry" {
		turn.AssistantResponse = ev.Response.Content
		turn.ReasoningContent = ev.Response.ReasoningContent
		turn.ToolCalls = ev.Response.ToolCalls
	}

	// 4. 清空内存中庞大的原始数据以释放内存
	ev.RawRequest = ""
	ev.RawResponse = ""
}

// filterNonSystemMessages 返回去除 role=="system" 后的消息切片，用于会话归拢时忽略可变系统提示词
func filterNonSystemMessages(history []ChatMessage) []ChatMessage {
	if len(history) == 0 {
		return history
	}
	var filtered []ChatMessage
	for _, m := range history {
		if m.Role != "system" {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// inferSession 推导和归拢会话 ID 以及轮次（TurnID）
func (s *AnalysisService) inferSession(ev *TraceEvent) {
	if ev.SessionID != "" {
		return
	}

	var currentHistory []ChatMessage
	if ev.RawRequest != "" {
		currentHistory = extractChatHistory(ev.RawRequest, ev.Format, "", "", nil, false, 0)
	}

	// 过滤 system 消息：IDE 的工作记忆机制会频繁修改 system prompt，
	// 导致前缀匹配断裂。归拢时忽略 system 角色，仅以 user/assistant/tool 锚定会话。
	currentCompact := filterNonSystemMessages(currentHistory)

	var matchedSess *ConversationSession
	cutoff := ev.Time.Add(-30 * time.Minute)

	for _, sess := range s.sessions {
		if sess.EndTime.Before(cutoff) {
			continue
		}
		sessCompact := filterNonSystemMessages(sess.getFullChatHistory())
		n := len(sessCompact)
		if len(currentCompact) >= n && n > 0 {
			match := true
			for i := 0; i < n; i++ {
				if currentCompact[i].Role != sessCompact[i].Role ||
					currentCompact[i].Content != sessCompact[i].Content {
					match = false
					break
				}
			}
			if match {
				matchedSess = sess
				break
			}
		}
	}

	if matchedSess != nil {
		ev.SessionID = matchedSess.ID
		maxTurnID := 0
		for tid := range matchedSess.Turns {
			if tid > maxTurnID {
				maxTurnID = tid
			}
		}
		ev.TurnID = maxTurnID + 1
	} else {
		ev.SessionID = "sess_" + strconv.FormatInt(ev.Time.UnixNano(), 36)
		ev.TurnID = 1
	}
}

// NewTraceEvent 是 TraceEvent 的统一构建工厂方法
func NewTraceEvent(
	startTime time.Time,
	logID int64,
	sessionID string,
	turnID int,
	phase string,
	format string,
	route string,
	status int,
	latencyMs int64,
	model string,
	upstream string,
	reqMeta RequestMeta,
	respMeta ResponseMeta,
	rawRequest string,
	rawResponse string,
) *TraceEvent {
	return &TraceEvent{
		ID:          "ev_" + strconv.FormatInt(startTime.UnixNano(), 36),
		Time:        startTime,
		LogID:       logID,
		SessionID:   sessionID,
		TurnID:      turnID,
		Phase:       phase,
		Format:      format,
		Route:       route,
		Status:      status,
		LatencyMs:   latencyMs,
		Model:       model,
		Upstream:    upstream,
		Request:     reqMeta,
		Response:    respMeta,
		RawRequest:  rawRequest,
		RawResponse: rawResponse,
	}
}

func getLastUserMessage(ev *TraceEvent) string {
	if ev.RawRequest == "" {
		return ""
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(ev.RawRequest), &body); err != nil {
		return ""
	}

	messages, ok := body["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return ""
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "user" {
			return extractMessageText(msg)
		}
	}
	return ""
}

func extractMessageText(msg map[string]interface{}) string {
	if content, ok := msg["content"].(string); ok {
		return content
	}
	if contentArr, ok := msg["content"].([]interface{}); ok {
		var textParts []string
		for _, part := range contentArr {
			if pMap, ok := part.(map[string]interface{}); ok {
				if txt, ok := pMap["text"].(string); ok {
					textParts = append(textParts, txt)
				}
			}
		}
		return strings.Join(textParts, " ")
	}
	return ""
}

func getMD5Hash(text string) string {
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}



func makePreview(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "...(preview)"
}

func (s *AnalysisService) writeToDisk(ev *TraceEvent) {
	dateStr := ev.Time.Format("2006-01-02")
	if s.currentLogDate != dateStr {
		s.writtenHashes = make(map[string]bool)
		s.currentLogDate = dateStr
	}

	evCopy := *ev
	evCopy.StorageError = ""
	evCopy.RawRequest = ""
	evCopy.UpstreamRequest = ""
	evCopy.RawResponse = ""

	if len(ev.ChatHistory) > 0 {
		historyCopy := make([]ChatMessage, len(ev.ChatHistory))
		copy(historyCopy, ev.ChatHistory)
		evCopy.ChatHistory = historyCopy

		for i := range evCopy.ChatHistory {
			msg := &evCopy.ChatHistory[i]
			if msg.Role == "system" && len(msg.Content) > 500 {
				hash := getMD5Hash(msg.Content)
				if s.writtenHashes[hash] {
					msg.Content = "$ref_dict:md5_" + hash
				} else {
					s.writtenHashes[hash] = true
				}
			}
		}
	}

	line, err := json.Marshal(evCopy)
	if err != nil {
		return
	}

	fileName := dateStr + ".jsonl"
	filePath := filepath.Join(s.logDir, fileName)

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[analysis] failed to write event to disk: %v", err)
		return
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("[analysis] failed to write event bytes to disk: %v", err)
	}
}

func (s *AnalysisService) cleanupOldLogs() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	cleanup := func() {
		s.lock.Lock()
		defer s.lock.Unlock()

		files, err := os.ReadDir(s.logDir)
		if err != nil {
			return
		}

		cutoff := time.Now().AddDate(0, 0, -s.config.Get().AnalysisRetentionDays)

		for _, file := range files {
			if filepath.Ext(file.Name()) != ".jsonl" {
				continue
			}
			info, err := file.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				path := filepath.Join(s.logDir, file.Name())
				os.Remove(path)
				dateStr := strings.TrimSuffix(file.Name(), ".jsonl")
				_ = os.RemoveAll(filepath.Join(s.logDir, "content", dateStr))
				log.Printf("[analysis] cleaned up expired log file: %s", file.Name())
			}
		}

		for id, sess := range s.sessions {
			if sess.EndTime.Before(cutoff) {
				delete(s.sessions, id)
			}
		}
	}

	cleanup()

	for {
		select {
		case <-ticker.C:
			cleanup()
		case <-s.shutdownCh:
			return
		}
	}
}

func (s *AnalysisService) loadHistoryFromDisk() {
	s.lock.Lock()
	defer s.lock.Unlock()

	files, err := os.ReadDir(s.logDir)
	if err != nil {
		return
	}

	var logFiles []string
	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".jsonl" {
			logFiles = append(logFiles, file.Name())
		}
	}
	sort.Strings(logFiles)

	cutoff := time.Now().AddDate(0, 0, -s.config.Get().AnalysisRetentionDays)

	for _, fileName := range logFiles {
		filePath := filepath.Join(s.logDir, fileName)
		info, err := os.Stat(filePath)
		if err == nil && info.ModTime().Before(cutoff) {
			continue
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("[analysis] failed to read log file %s: %v", fileName, err)
			continue
		}

		lines := strings.Split(string(data), "\n")
		localDict := make(map[string]string)
		logDate := strings.TrimSuffix(fileName, ".jsonl")

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			var ev TraceEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}

			if ev.Time.Before(cutoff) {
				continue
			}

			// 还原哈希字典引用
			if len(ev.ChatHistory) > 0 {
				for i := range ev.ChatHistory {
					msg := &ev.ChatHistory[i]
					if msg.Role == "system" {
						if strings.HasPrefix(msg.Content, "$ref_dict:md5_") {
							hash := strings.TrimPrefix(msg.Content, "$ref_dict:md5_")
							if original, ok := localDict[hash]; ok {
								msg.Content = original
							}
						} else if len(msg.Content) > 500 {
							hash := getMD5Hash(msg.Content)
							localDict[hash] = msg.Content

							// 若是今日日志，则同步到 writtenHashes 维持重启一致性
							todayStr := time.Now().Format("2006-01-02")
							if logDate == todayStr {
								if s.writtenHashes == nil {
									s.writtenHashes = make(map[string]bool)
								}
								s.writtenHashes[hash] = true
								s.currentLogDate = todayStr
							}
						}
					}
				}
			}

			s.reconstructEventInMemory(&ev)
		}
	}
	log.Printf("[analysis] loaded history from disk: %d sessions active", len(s.sessions))
}

func (s *AnalysisService) reconstructEventInMemory(ev *TraceEvent) {
	sess, exists := s.sessions[ev.SessionID]
	if !exists {
		sess = &ConversationSession{
			ID:             ev.SessionID,
			StartTime:      ev.Time,
			EndTime:        ev.Time,
			Turns:          make(map[int]*ConversationTurn),
			GroupingReason: ev.Phase,
		}
		s.sessions[ev.SessionID] = sess
	}

	if ev.Time.Before(sess.StartTime) {
		sess.StartTime = ev.Time
	}
	if ev.Time.After(sess.EndTime) {
		sess.EndTime = ev.Time
	}

	sess.RequestCount++
	sess.PromptTokens += ev.Response.PromptTokens
	sess.CompletionTokens += ev.Response.CompletionTokens
	sess.TotalTokens += ev.Response.TotalTokens
	sess.CacheHitTokens += ev.Response.CacheHitTokens
	sess.CacheMissTokens += ev.Response.CacheMissTokens

	if ev.Status >= 400 {
		sess.Errors++
	}
	if ev.Phase == "retry" {
		sess.Retries++
	}

	foundModel := false
	for _, m := range sess.Models {
		if m == ev.Model {
			foundModel = true
			break
		}
	}
	if !foundModel && ev.Model != "" {
		sess.Models = append(sess.Models, ev.Model)
	}

	turn, turnExists := sess.Turns[ev.TurnID]
	if !turnExists {
		turn = &ConversationTurn{
			TurnID:    ev.TurnID,
			StartTime: ev.Time,
		}
		sess.Turns[ev.TurnID] = turn
	}

	turn.Events = append(turn.Events, ev)

	if ev.Phase == "primary" {
		if ev.Request.LastUserSummary != "" {
			turn.UserMessage = ev.Request.LastUserSummary
		}
		turn.SystemModified = ev.Request.SystemPromptTransformed
		turn.ExtraInjected = ev.Request.ExtraPromptInjected
	}

	if ev.Phase == "primary" || ev.Phase == "retry" {
		if ev.Response.ReasoningContent != "" {
			turn.ReasoningContent = ev.Response.ReasoningContent
		}
		if ev.Response.Content != "" {
			turn.AssistantResponse = ev.Response.Content
		}
		if len(ev.Response.ToolCalls) > 0 {
			turn.ToolCalls = ev.Response.ToolCalls
		}
		if len(ev.ChatHistory) > 0 {
			var prevHistory []ChatMessage
			if ev.TurnID > 1 {
				prevHistory = sess.getFullChatHistoryBefore(ev.TurnID)
			}
			turn.ChatHistory = stripHistoryPrefix(ev.ChatHistory, prevHistory)
		}
	}

	if len(turn.ChatHistory) == 0 {
		var history []ChatMessage
		if turn.UserMessage != "" {
			history = append(history, ChatMessage{
				Role:    "user",
				Content: turn.UserMessage,
			})
		}
		if turn.AssistantResponse != "" || turn.ReasoningContent != "" || len(turn.ToolCalls) > 0 {
			history = append(history, ChatMessage{
				Role:             "assistant",
				Content:          turn.AssistantResponse,
				ReasoningContent: turn.ReasoningContent,
				ToolCalls:        turn.ToolCalls,
			})
		}
		turn.ChatHistory = history
	}
}

// ClearHistory 清空磁盘日志文件以及内存中的所有分析会话记录
func (s *AnalysisService) ClearHistory() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	files, err := os.ReadDir(s.logDir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".jsonl" {
			filePath := filepath.Join(s.logDir, file.Name())
			if err := os.Remove(filePath); err != nil {
				log.Printf("[analysis] failed to delete log file %s: %v", file.Name(), err)
			}
		}
	}

	contentDir := filepath.Join(s.logDir, "content")
	if err := os.RemoveAll(contentDir); err != nil && !os.IsNotExist(err) {
		log.Printf("[analysis] failed to delete content directory: %v", err)
	}

	s.sessions = make(map[string]*ConversationSession)
	log.Printf("[analysis] all analysis history cleared")
	return nil
}

// AssignSessionAndTurn 给新到的请求分配 SessionID 和 TurnID
func (s *AnalysisService) AssignSessionAndTurn(startTime time.Time, rawRequest string, format string, route string) (string, int) {
	if s == nil || !s.config.Get().AnalysisEnabled {
		return "", 0
	}
	// 仅过滤聊天 completions/messages 相关的路由
	if !strings.HasSuffix(route, "/v1/chat/completions") && !strings.HasSuffix(route, "/v1/messages") {
		return "", 0
	}
	s.lock.Lock()
	defer s.lock.Unlock()

	tempEv := &TraceEvent{
		Time:       startTime,
		RawRequest: rawRequest,
		Format:     format,
	}

	s.inferSession(tempEv)

	// 在内存中进行预占，以防止因异步落盘延迟导致并发/流式请求匹配失败
	sess, exists := s.sessions[tempEv.SessionID]
	if !exists {
		sess = &ConversationSession{
			ID:             tempEv.SessionID,
			StartTime:      tempEv.Time,
			EndTime:        tempEv.Time,
			Turns:          make(map[int]*ConversationTurn),
			GroupingReason: "pre-assigned",
		}
		s.sessions[tempEv.SessionID] = sess
	}
	if tempEv.Time.After(sess.EndTime) {
		sess.EndTime = tempEv.Time
	}

	turn, turnExists := sess.Turns[tempEv.TurnID]
	if !turnExists {
		turn = &ConversationTurn{
			TurnID:    tempEv.TurnID,
			StartTime: tempEv.Time,
		}
		sess.Turns[tempEv.TurnID] = turn
	}

	// 提取当前轮次的 UserMessage 写入占位的 Turn 中
	userMsg := getLastUserMessage(tempEv)
	if userMsg != "" && turn.UserMessage == "" {
		turn.UserMessage = userMsg
	}

	return tempEv.SessionID, tempEv.TurnID
}

// 辅助 unmarshal 工具函数
func parseAssistantContent(rawText string, format string) string {
	if rawText == "" {
		return ""
	}

	// 判断是否是流式
	if strings.Contains(rawText, "data: ") {
		var sb strings.Builder
		lines := strings.Split(rawText, "\n")
		for _, line := range lines {
			if delta, err := ParseSSELine(line); err == nil && delta != nil {
				if delta.Content != "" {
					sb.WriteString(delta.Content)
				}
			}
		}
		return sb.String()
	} else {
		// 非流式，单体 JSON
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(rawText), &data); err != nil {
			return ""
		}
		// 兼容 OpenAI
		choices, _ := data["choices"].([]interface{})
		if len(choices) > 0 {
			choice, _ := choices[0].(map[string]interface{})
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				content, _ := msg["content"].(string)
				return content
			}
		}
		// 兼容 Anthropic 非流式
		if contentArr, ok := data["content"].([]interface{}); ok {
			var sb strings.Builder
			for _, item := range contentArr {
				if m, ok := item.(map[string]interface{}); ok {
					if t, _ := m["type"].(string); t == "text" {
						if text, ok := m["text"].(string); ok {
							sb.WriteString(text)
						}
					}
				}
			}
			return sb.String()
		}
		return ""
	}
}

func parseAssistantReasoning(rawText string, format string) string {
	if rawText == "" {
		return ""
	}

	if strings.Contains(rawText, "data: ") {
		var sb strings.Builder
		lines := strings.Split(rawText, "\n")
		for _, line := range lines {
			if delta, err := ParseSSELine(line); err == nil && delta != nil {
				if delta.ReasoningContent != "" {
					sb.WriteString(delta.ReasoningContent)
				}
			}
		}
		return sb.String()
	} else {
		// 非流式
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(rawText), &data); err != nil {
			return ""
		}
		choices, _ := data["choices"].([]interface{})
		if len(choices) > 0 {
			choice, _ := choices[0].(map[string]interface{})
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				reasoning, _ := msg["reasoning_content"].(string)
				return reasoning
			}
		}
		// 兼容 Anthropic 非流式
		if contentArr, ok := data["content"].([]interface{}); ok {
			var sb strings.Builder
			for _, item := range contentArr {
				if m, ok := item.(map[string]interface{}); ok {
					if t, _ := m["type"].(string); t == "thinking" {
						if thinking, ok := m["thinking"].(string); ok {
							sb.WriteString(thinking)
						}
					}
				}
			}
			return sb.String()
		}
		return ""
	}
}

func parseAssistantToolCalls(rawText string, format string) []string {
	if rawText == "" {
		return nil
	}

	var toolCalls []string
	if strings.Contains(rawText, "data: ") {
		if format == "anthropic" {
			type anthropicTcBuilder struct {
				Name  strings.Builder
				Input strings.Builder
			}
			builders := make(map[int]*anthropicTcBuilder)

			lines := strings.Split(rawText, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "data: ") {
					dataStr := strings.TrimPrefix(line, "data: ")
					if dataStr == "[DONE]" {
						continue
					}
					var chunk map[string]interface{}
					if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
						cType, _ := chunk["type"].(string)
						idxVal, hasIdx := chunk["index"].(float64)
						idx := int(idxVal)

						if cType == "content_block_start" {
							if cb, ok := chunk["content_block"].(map[string]interface{}); ok {
								cbType, _ := cb["type"].(string)
								if cbType == "tool_use" {
									tb := &anthropicTcBuilder{}
									if name, _ := cb["name"].(string); name != "" {
										tb.Name.WriteString(name)
									}
									builders[idx] = tb
								}
							}
						} else if cType == "content_block_delta" && hasIdx {
							if tb, exists := builders[idx]; exists {
								if delta, ok := chunk["delta"].(map[string]interface{}); ok {
									dType, _ := delta["type"].(string)
									if dType == "input_json_delta" {
										if pj, _ := delta["partial_json"].(string); pj != "" {
											tb.Input.WriteString(pj)
										}
									}
								}
							}
						}
					}
				}
			}

			var keys []int
			for k := range builders {
				keys = append(keys, k)
			}
			sort.Ints(keys)
			for _, k := range keys {
				tb := builders[k]
				toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", tb.Name.String(), tb.Input.String()))
			}
		} else {
			type tcBuilder struct {
				Name strings.Builder
				Args strings.Builder
			}
			builders := make(map[int]*tcBuilder)

			lines := strings.Split(rawText, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "data: ") {
					dataStr := strings.TrimPrefix(line, "data: ")
					if dataStr == "[DONE]" {
						continue
					}
					var chunk map[string]interface{}
					if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
						if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
							if choice, ok := choices[0].(map[string]interface{}); ok {
								if delta, ok := choice["delta"].(map[string]interface{}); ok {
									if tcs, ok := delta["tool_calls"].([]interface{}); ok {
										for _, tc := range tcs {
											tcm, _ := tc.(map[string]interface{})
											idxVal, ok := tcm["index"].(float64)
											if !ok {
												continue
											}
											idx := int(idxVal)
											tb, exists := builders[idx]
											if !exists {
												tb = &tcBuilder{}
												builders[idx] = tb
											}
											if fn, ok := tcm["function"].(map[string]interface{}); ok {
												if name, _ := fn["name"].(string); name != "" {
													tb.Name.WriteString(name)
												}
												if args, _ := fn["arguments"].(string); args != "" {
													tb.Args.WriteString(args)
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
			var keys []int
			for k := range builders {
				keys = append(keys, k)
			}
			sort.Ints(keys)
			for _, k := range keys {
				tb := builders[k]
				toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", tb.Name.String(), tb.Args.String()))
			}
		}
	} else {
		if format == "anthropic" {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(rawText), &data); err != nil {
				return nil
			}
			content, _ := data["content"].([]interface{})
			for _, partVal := range content {
				if part, ok := partVal.(map[string]interface{}); ok {
					pType, _ := part["type"].(string)
					if pType == "tool_use" {
						name, _ := part["name"].(string)
						var inputStr string
						if input, ok := part["input"].(map[string]interface{}); ok {
							if b, err := json.Marshal(input); err == nil {
								inputStr = string(b)
							}
						}
						toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", name, inputStr))
					}
				}
			}
		} else {
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(rawText), &data); err != nil {
				return nil
			}
			choices, _ := data["choices"].([]interface{})
			if len(choices) > 0 {
				choice, _ := choices[0].(map[string]interface{})
				if msg, ok := choice["message"].(map[string]interface{}); ok {
					tcs, _ := msg["tool_calls"].([]interface{})
					for _, tc := range tcs {
						tcm, _ := tc.(map[string]interface{})
						if fn, ok := tcm["function"].(map[string]interface{}); ok {
							name, _ := fn["name"].(string)
							args, _ := fn["arguments"].(string)
							toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", name, args))
						}
					}
				}
			}
		}
	}
	return toolCalls
}

func formatCacheRatio(hit, miss int) string {
	total := hit + miss
	if total <= 0 {
		return "0%"
	}
	if miss == 0 {
		return "100%"
	}
	if hit == 0 {
		return "0%"
	}
	pct := float64(hit) / float64(total) * 100
	if pct > 99.9 {
		return "99.9%"
	}
	if pct < 0.1 {
		return "0.1%"
	}
	return fmt.Sprintf("%.1f%%", pct)
}

// GetSessionSummaries 获取会话列表摘要，以 StartTime 倒序排序
func (s *AnalysisService) GetSessionSummaries(limit, offset int) []SessionSummary {
	s.lock.RLock()
	defer s.lock.RUnlock()

	var summaries []SessionSummary
	for id, sess := range s.sessions {
		hitTotal := sess.CacheHitTokens + sess.CacheMissTokens
		ratio := 0.0
		if hitTotal > 0 {
			ratio = float64(sess.CacheHitTokens) / float64(hitTotal)
		}

		// 生成 200 字摘要
		var lastUserText string
		var lastAssistantText string
		// 寻找最后一个非空 turn
		var lastTurnID int
		for tid := range sess.Turns {
			if tid > lastTurnID {
				lastTurnID = tid
			}
		}
		if t, ok := sess.Turns[lastTurnID]; ok {
			lastUserText = t.UserMessage
			lastAssistantText = t.AssistantResponse
		}

		summaryText := fmt.Sprintf("User: %s | AI: %s", lastUserText, lastAssistantText)
		if len(summaryText) > 200 {
			summaryText = summaryText[:200] + "..."
		}
		if summaryText == "User:  | AI: " {
			summaryText = "（无对话内容摘要）"
		}

		var format string = "openai"
		var status int = 200
		if len(sess.Turns) > 0 {
			var firstTurnID int = 999999
			var lastTurnIDVal int = 0
			for tid := range sess.Turns {
				if tid < firstTurnID {
					firstTurnID = tid
				}
				if tid > lastTurnIDVal {
					lastTurnIDVal = tid
				}
			}
			if t, ok := sess.Turns[firstTurnID]; ok && len(t.Events) > 0 {
				format = t.Events[0].Format
			}
			if t, ok := sess.Turns[lastTurnIDVal]; ok && len(t.Events) > 0 {
				status = t.Events[len(t.Events)-1].Status
			}
		}

		summaries = append(summaries, SessionSummary{
			ID:               id,
			StartTime:        sess.StartTime,
			EndTime:          sess.EndTime,
			RequestCount:     sess.RequestCount,
			Models:           strings.Join(sess.Models, ", "),
			PromptTokens:     sess.PromptTokens,
			CompletionTokens: sess.CompletionTokens,
			TotalTokens:      sess.TotalTokens,
			CacheHitRatio:    ratio,
			Errors:           sess.Errors,
			Retries:          sess.Retries,
			SummaryText:      summaryText,
			Format:           format,
			Status:           status,
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].StartTime.After(summaries[j].StartTime)
	})

	// 分页
	if offset >= len(summaries) {
		return []SessionSummary{}
	}
	end := offset + limit
	if end > len(summaries) {
		end = len(summaries)
	}
	return summaries[offset:end]
}

// GetSessionDetails 获取特定 Session 详情
func (s *AnalysisService) GetSessionDetails(id string) *ConversationSession {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.sessions[id]
}

func (s *AnalysisService) GetTimelinePage(id string, offset, limit int) (TimelinePage, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	sess, exists := s.sessions[id]
	if !exists {
		return TimelinePage{}, fmt.Errorf("session %s not found", id)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	var keys []int
	for k := range sess.Turns {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	var items []TimelineItem
	for _, k := range keys {
		turn := sess.Turns[k]
		var toolsDetail []ToolMeta
		for _, ev := range turn.Events {
			if ev.Phase == "primary" {
				toolsDetail = ev.Request.ToolsDetail
				break
			}
		}
		for idx, msg := range turn.ChatHistory {
			preview := msg.Content
			if preview == "" && msg.ReasoningContent != "" {
				preview = msg.ReasoningContent
			}
			if preview == "" && len(msg.ToolCalls) > 0 {
				preview = strings.Join(msg.ToolCalls, "\n")
			}
			items = append(items, TimelineItem{
				Type:             "message",
				SessionID:        id,
				TurnID:           turn.TurnID,
				Role:             msg.Role,
				Time:             turn.StartTime,
				Preview:          preview,
				ReasoningContent: msg.ReasoningContent,
				MessageIdx:       idx,
				Tools:            toolsDetail,
				ToolCalls:        msg.ToolCalls,
			})
		}
		for _, ev := range turn.Events {
			if ev.Phase == "analyzer" || ev.Phase == "retry" {
				items = append(items, TimelineItem{
					Type:      "event",
					SessionID: id,
					TurnID:    turn.TurnID,
					Phase:     ev.Phase,
					LogID:     ev.LogID,
					Time:      ev.Time,
					Preview:   ev.Response.AnalyzerJudgment,
					Event:     ev,
				})
			}
		}
	}

	total := len(items)
	if offset >= total {
		return TimelinePage{Items: []TimelineItem{}, Total: total, Offset: offset, Limit: limit}, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return TimelinePage{Items: items[offset:end], Total: total, Offset: offset, Limit: limit}, nil
}

// ExportConfig 控制导出诊断报告时包含的会话内容板块。
// 各字段对应 UI 弹窗中的可选项；ToolResultMaxLen > 0 时将「AI 工具返回内容」截断为指定字符数的摘要。
type ExportConfig struct {
	SystemPrompt     bool `json:"system_prompt"`     // 系统提示词
	UserMessage      bool `json:"user_message"`      // 用户消息
	ToolPrompt       bool `json:"tool_prompt"`       // 工具提示词（可用工具定义）
	Reasoning        bool `json:"reasoning"`         // AI 思考推理过程
	ToolCall         bool `json:"tool_call"`         // AI 工具调用命令
	ToolResult       bool `json:"tool_result"`       // AI 工具返回内容
	Content          bool `json:"content"`           // AI 回复正文
	ToolResultMaxLen int  `json:"tool_result_max_len"` // 0=不截断；>0=工具返回内容摘要到该字符数
}

// defaultFullExportConfig 默认模板：导出所有板块。
func defaultFullExportConfig() *ExportConfig {
	return &ExportConfig{
		SystemPrompt: true,
		UserMessage:  true,
		ToolPrompt:   true,
		Reasoning:    true,
		ToolCall:     true,
		ToolResult:   true,
		Content:      true,
		ToolResultMaxLen: 0,
	}
}

// liteExportConfig 精简模板：仅系统提示词、用户消息、回复正文、工具调用命令、工具返回内容摘要(200字)。
func liteExportConfig() *ExportConfig {
	return &ExportConfig{
		SystemPrompt:     true,
		UserMessage:      true,
		ToolPrompt:       false,
		Reasoning:        false,
		ToolCall:         true,
		ToolResult:       true,
		Content:          true,
		ToolResultMaxLen: 200,
	}
}

// ExportMarkdown 导出特定 Session 的 Markdown 报告。
// cfg 为 nil 时按默认模板（导出全部板块）处理。
func (s *AnalysisService) ExportMarkdown(id string, cfg *ExportConfig) (string, error) {
	s.lock.RLock()
	sess, exists := s.sessions[id]
	s.lock.RUnlock()

	if !exists {
		return "", fmt.Errorf("session %s not found", id)
	}
	if cfg == nil {
		cfg = defaultFullExportConfig()
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# DSPlus 会话诊断报告 (#%s)\n\n", sess.ID))

	// 1. 摘要信息
	sb.WriteString("## 1. 基础摘要\n\n")
	sb.WriteString(fmt.Sprintf("- **开始时间**: %s\n", sess.StartTime.Local().Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("- **结束时间**: %s\n", sess.EndTime.Local().Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("- **请求次数**: %d 次\n", sess.RequestCount))
	sb.WriteString(fmt.Sprintf("- **关联模型**: %s\n", strings.Join(sess.Models, ", ")))
	sb.WriteString(fmt.Sprintf("- **总 Token 消耗**: %d (输入: %d, 输出: %d)\n", sess.TotalTokens, sess.PromptTokens, sess.CompletionTokens))

	ratioStr := formatCacheRatio(sess.CacheHitTokens, sess.CacheMissTokens)
	sb.WriteString(fmt.Sprintf("- **缓存命中率**: %s (命中: %d, 未命中: %d)\n", ratioStr, sess.CacheHitTokens, sess.CacheMissTokens))
	sb.WriteString(fmt.Sprintf("- **系统异常数**: %d\n", sess.Errors))
	sb.WriteString(fmt.Sprintf("- **反循环重试数**: %d\n\n", sess.Retries))

	// 2. 对话时间线
	sb.WriteString("## 2. 轮次对话时间线\n\n")

	// 排序 Turns
	var keys []int
	for k := range sess.Turns {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	// 按板块开关截断工具返回内容
	truncateToolResult := func(text string) string {
		if cfg.ToolResultMaxLen <= 0 || text == "" {
			return text
		}
		runes := []rune(text)
		if len(runes) <= cfg.ToolResultMaxLen {
			return text
		}
		return string(runes[:cfg.ToolResultMaxLen]) + "...(摘要截断)"
	}

	for _, k := range keys {
		turn := sess.Turns[k]
		sb.WriteString(fmt.Sprintf("### 轮次 %d (%s)\n\n", turn.TurnID, turn.StartTime.Local().Format("15:04:05")))

		// 检查系统修改（结构标注，始终保留）
		if turn.SystemModified || turn.ExtraInjected {
			sb.WriteString("> **⚙️ 系统干预**:\n")
			if turn.SystemModified {
				sb.WriteString("> - System Prompt 拼接位置已重组优化\n")
			}
			if turn.ExtraInjected {
				sb.WriteString("> - 注入了额外定义的最高准则指令\n")
			}
			sb.WriteString("\n")
		}

		// 输出干预事件流（analyzer / retry 结构标注，始终保留）
		for _, ev := range turn.Events {
			if ev.Phase == "analyzer" {
				sb.WriteString(fmt.Sprintf("> **🔍 反循环分析器**: 判定结果=`%s` | `%s`\n", ev.Response.AnalyzerJudgment, ev.Response.FinishReason))
			} else if ev.Phase == "retry" {
				sb.WriteString(fmt.Sprintf("> **🔄 自动重试**: 重试模型=`%s` | `%s` | 耗时=%dms\n", ev.Response.RetryModel, ev.Response.FinishReason, ev.LatencyMs))
			}
		}

		// 工具提示词（可用工具定义列表）
		if cfg.ToolPrompt {
			var toolsDetail []ToolMeta
			for _, ev := range turn.Events {
				if ev.Phase == "primary" {
					toolsDetail = ev.Request.ToolsDetail
					break
				}
			}
			if len(toolsDetail) > 0 {
				sb.WriteString("**🔧 工具提示词（可用工具定义）**:\n")
				for _, td := range toolsDetail {
					sb.WriteString(fmt.Sprintf("- `%s`: %s\n", td.Name, td.Description))
				}
				sb.WriteString("\n")
			}
		}

		// 遍历本轮清洗后的对话历史，按板块开关输出
		if len(turn.ChatHistory) > 0 {
			for _, msg := range turn.ChatHistory {
				switch msg.Role {
				case "system":
					if cfg.SystemPrompt && msg.Content != "" {
						sb.WriteString(fmt.Sprintf("**📋 系统提示词**:\n\n%s\n\n", msg.Content))
					}
				case "user":
					if cfg.UserMessage && msg.Content != "" {
						sb.WriteString(fmt.Sprintf("**🧑 用户**: %s\n\n", msg.Content))
					}
				case "tool":
					if cfg.ToolResult && msg.Content != "" {
						sb.WriteString(fmt.Sprintf("**📎 工具返回内容**:\n\n%s\n\n", truncateToolResult(msg.Content)))
					}
				case "assistant":
					if cfg.Reasoning && msg.ReasoningContent != "" {
						sb.WriteString("<details>\n<summary>🧠 <b>模型深度思考过程</b></summary>\n\n")
						sb.WriteString("```\n" + msg.ReasoningContent + "\n```\n")
						sb.WriteString("</details>\n\n")
					}
					if cfg.ToolCall && len(msg.ToolCalls) > 0 {
						sb.WriteString("**🛠️ 工具调用命令**:\n")
						for _, tc := range msg.ToolCalls {
							sb.WriteString(fmt.Sprintf("- `%s`\n", tc))
						}
						sb.WriteString("\n")
					}
					if cfg.Content && msg.Content != "" {
						sb.WriteString(fmt.Sprintf("**🤖 模型回复**:\n\n%s\n\n", msg.Content))
					}
				}
			}
		} else {
			// 兜底：ChatHistory 为空时沿用轮次级聚合字段
			if cfg.UserMessage && turn.UserMessage != "" {
				sb.WriteString(fmt.Sprintf("**🧑 用户**: %s\n\n", turn.UserMessage))
			}
			if cfg.Reasoning && turn.ReasoningContent != "" {
				sb.WriteString("<details>\n<summary>🧠 <b>模型深度思考过程</b></summary>\n\n")
				sb.WriteString("```\n" + turn.ReasoningContent + "\n```\n")
				sb.WriteString("</details>\n\n")
			}
			if cfg.ToolCall && len(turn.ToolCalls) > 0 {
				sb.WriteString("**🛠️ 工具调用命令**:\n")
				for _, tc := range turn.ToolCalls {
					sb.WriteString(fmt.Sprintf("- `%s`\n", tc))
				}
				sb.WriteString("\n")
			}
			if cfg.Content && turn.AssistantResponse != "" {
				sb.WriteString(fmt.Sprintf("**🤖 模型回复**:\n\n%s\n\n", turn.AssistantResponse))
			}
		}

		sb.WriteString("---\n\n")
	}

	return sb.String(), nil
}

// buildRequestMeta 构造 RequestMeta 元数据，供代理层调用
func buildRequestMeta(data map[string]interface{}, format string, transformed, extraInjected bool, semanticType ...string) RequestMeta {
	meta := RequestMeta{
		SystemPromptTransformed: transformed,
		ExtraPromptInjected:     extraInjected,
		RoleCounts:              make(map[string]int),
	}
	if len(semanticType) > 0 {
		meta.SemanticType = semanticType[0]
	}
	if data == nil {
		return meta
	}

	meta.Model, _ = data["model"].(string)
	meta.Stream, _ = data["stream"].(bool)

	if thinking, ok := data["thinking"].(map[string]interface{}); ok {
		meta.ThinkingMode, _ = thinking["type"].(string)
	}
	meta.ReasoningEffort, _ = data["reasoning_effort"].(string)
	if outputCfg, ok := data["output_config"].(map[string]interface{}); ok {
		if effort, ok := outputCfg["effort"].(string); ok {
			meta.ReasoningEffort = effort
		}
	}
	if mt, ok := data["max_tokens"].(float64); ok {
		meta.MaxTokens = int(mt)
	}

	// 提取 tools 和 tools_detail
	if toolsArr, ok := data["tools"].([]interface{}); ok {
		var tools []string
		var toolsDetail []ToolMeta
		for _, tVal := range toolsArr {
			if tMap, ok := tVal.(map[string]interface{}); ok {
				tType, _ := tMap["type"].(string)
				if tType == "function" {
					if fnMap, ok := tMap["function"].(map[string]interface{}); ok {
						fnName, _ := fnMap["name"].(string)
						fnDesc, _ := fnMap["description"].(string)
						if fnName != "" {
							tools = append(tools, fnName)
							toolsDetail = append(toolsDetail, ToolMeta{
								Name:        fnName,
								Description: fnDesc,
							})
						}
					}
				} else {
					// 兼容 Anthropic 直接在最外层定义 name 和 description 的情况
					fnName, _ := tMap["name"].(string)
					fnDesc, _ := tMap["description"].(string)
					if fnName != "" {
						tools = append(tools, fnName)
						toolsDetail = append(toolsDetail, ToolMeta{
							Name:        fnName,
							Description: fnDesc,
						})
					}
				}
			}
		}
		meta.Tools = tools
		meta.ToolsDetail = toolsDetail
	}

	messages, ok := data["messages"].([]interface{})
	if ok {
		meta.MessageCount = len(messages)
		for _, msg := range messages {
			m, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			meta.RoleCounts[role]++
		}
		// 提取最后一条 user 消息
		for i := len(messages) - 1; i >= 0; i-- {
			m, ok := messages[i].(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			if role == "user" {
				if content, ok := m["content"].(string); ok {
					meta.LastUserSummary = content
				} else if contentArr, ok := m["content"].([]interface{}); ok {
					// 兼容多模态/列表格式的 content 字段提取文本
					var textParts []string
					for _, part := range contentArr {
						if pMap, ok := part.(map[string]interface{}); ok {
							if txt, ok := pMap["text"].(string); ok {
								textParts = append(textParts, txt)
							}
						}
					}
					meta.LastUserSummary = strings.Join(textParts, " ")
				}
				break
			}
		}
	}
	return meta
}

func detectModel(body string) string {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(body), &data); err == nil {
		if model, ok := data["model"].(string); ok {
			return model
		}
	}
	return "deepseek-chat"
}

func extractChatHistory(rawRequest string, format string, finalReply string, finalReasoning string, finalTools []string, incrementalOnly bool, lastHistoryLen int) []ChatMessage {
	var history []ChatMessage
	if rawRequest == "" {
		return history
	}

	var body map[string]interface{}
	if err := json.Unmarshal([]byte(rawRequest), &body); err != nil {
		return history
	}

	messages, ok := body["messages"].([]interface{})
	if !ok {
		return history
	}

	startIndex := 0
	if incrementalOnly && lastHistoryLen > 0 && lastHistoryLen <= len(messages) {
		startIndex = lastHistoryLen
	}

	for idx := startIndex; idx < len(messages); idx++ {
		msgVal := messages[idx]
		msg, ok := msgVal.(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		content := ""
		reasoning := ""
		var toolCalls []string

		if cStr, ok := msg["content"].(string); ok {
			content = cStr
		} else if cArr, ok := msg["content"].([]interface{}); ok {
			var textParts []string
			for _, part := range cArr {
				if pMap, ok := part.(map[string]interface{}); ok {
					pType, _ := pMap["type"].(string)
					if pType == "text" {
						if txt, ok := pMap["text"].(string); ok {
							textParts = append(textParts, txt)
						}
					} else if pType == "thinking" {
						if thk, ok := pMap["thinking"].(string); ok {
							reasoning = thk
						}
					} else if pType == "tool_result" {
						role = "tool"
						if toolRes, ok := pMap["content"].(string); ok {
							content = toolRes
						}
					} else if pType == "tool_use" {
						name, _ := pMap["name"].(string)
						var inputStr string
						if input, ok := pMap["input"].(map[string]interface{}); ok {
							if b, err := json.Marshal(input); err == nil {
								inputStr = string(b)
							}
						}
						toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", name, inputStr))
					}
				}
			}
			if len(textParts) > 0 {
				content = strings.Join(textParts, " ")
			}
		}

		if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
			reasoning = rc
		}
		if tcs, ok := msg["tool_calls"].([]interface{}); ok {
			for _, tcVal := range tcs {
				if tc, ok := tcVal.(map[string]interface{}); ok {
					if fn, ok := tc["function"].(map[string]interface{}); ok {
						name, _ := fn["name"].(string)
						args, _ := fn["arguments"].(string)
						toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", name, args))
					}
				}
			}
		}

		history = append(history, ChatMessage{
			Role:             role,
			Content:          content,
			ReasoningContent: reasoning,
			ToolCalls:        toolCalls,
		})
	}

	if finalReply != "" || finalReasoning != "" || len(finalTools) > 0 {
		history = append(history, ChatMessage{
			Role:             "assistant",
			Content:          finalReply,
			ReasoningContent: finalReasoning,
			ToolCalls:        finalTools,
		})
	}

	return history
}

// detectSemanticType 根据当前请求的最新消息判断其初始行为性质
// 返回值: "对话开始" / "工具回传"
// 注意: 防循环、防幻觉等辅助逻辑由调用方在响应时或二阶段中修正
func detectSemanticType(data map[string]interface{}, format string) string {
	if data == nil {
		return "对话开始"
	}

	messagesRaw, ok := data["messages"]
	if !ok {
		return "对话开始"
	}
	messages, ok := messagesRaw.([]interface{})
	if !ok || len(messages) == 0 {
		return "对话开始"
	}

	// 仅检查最后一条消息 (last message)
	lastMsgRaw := messages[len(messages)-1]
	lastMsg, ok := lastMsgRaw.(map[string]interface{})
	if !ok {
		return "对话开始"
	}

	role, _ := lastMsg["role"].(string)

	// 1. OpenAI 格式下，工具回传消息的角色是 "tool"
	if role == "tool" {
		return "工具回传"
	}

	// 2. Anthropic 格式下，工具回传包裹在 "user" 角色的 content 数组中，含有 type == "tool_result" 的元素
	if role == "user" {
		if contentArr, ok := lastMsg["content"].([]interface{}); ok {
			for _, part := range contentArr {
				if pMap, ok := part.(map[string]interface{}); ok {
					pType, _ := pMap["type"].(string)
					if pType == "tool_result" {
						return "工具回传"
					}
				}
			}
		}
	}

	return "对话开始"
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "...(truncated)"
	}
	return s
}

func (sess *ConversationSession) getFullChatHistory() []ChatMessage {
	return sess.getFullChatHistoryBefore(1<<31 - 1)
}

func (sess *ConversationSession) getFullChatHistoryBefore(turnID int) []ChatMessage {
	if len(sess.Turns) == 0 {
		return nil
	}
	var keys []int
	for k := range sess.Turns {
		if k < turnID {
			keys = append(keys, k)
		}
	}
	sort.Ints(keys)
	var history []ChatMessage
	for _, k := range keys {
		history = append(history, sess.Turns[k].ChatHistory...)
	}
	return history
}

func stripHistoryPrefix(history, prefix []ChatMessage) []ChatMessage {
	if len(prefix) == 0 || len(history) < len(prefix) {
		return history
	}
	for i := range prefix {
		if history[i].Role != prefix[i].Role || history[i].Content != prefix[i].Content {
			return history
		}
	}
	return history[len(prefix):]
}
