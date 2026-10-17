package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

// All state passed via parameters; no package-level globals needed.
func handleGUI(w http.ResponseWriter, r *http.Request, l *Logger, cfg *SafeConfig, svc *AnalysisService) {
	if r.URL.Path == "/" || r.URL.Path == "/index_v2.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		data, err := webFS.ReadFile("web/index_v2.html")
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Write(data)
		return
	}
	if r.URL.Path == "/index.html" {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
		return
	}

	// 静态资源分发（支持语言文件 .json）
	if strings.HasSuffix(r.URL.Path, ".css") || strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".png") || strings.HasSuffix(r.URL.Path, ".svg") || strings.HasSuffix(r.URL.Path, ".json") {
		filePath := "web" + r.URL.Path
		data, err := webFS.ReadFile(filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(filePath, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(filePath, ".js"):
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		case strings.HasSuffix(filePath, ".png"):
			w.Header().Set("Content-Type", "image/png")
		case strings.HasSuffix(filePath, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
		case strings.HasSuffix(filePath, ".json"):
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		w.Write(data)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	setAPICORS(w, r)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api")

	switch {
	case path == "/status":
		handleAPIStatus(w, r, l, cfg)
	case path == "/analysis/status":
		handleAPIAnalysisStatus(w, r, cfg)
	case path == "/analysis/sessions" && r.Method == "GET":
		handleAPIAnalysisSessions(w, r, svc)
	case path == "/analysis/sessions" && r.Method == "DELETE":
		handleAPIClearAnalysisHistory(w, r, svc)
	case strings.HasPrefix(path, "/analysis/sessions/") && strings.HasSuffix(path, "/export.md"):
		handleAPIAnalysisExport(w, r, path, svc)
	case strings.HasPrefix(path, "/analysis/sessions/") && strings.HasSuffix(path, "/timeline"):
		handleAPIAnalysisTimeline(w, r, path, svc)

	case strings.HasPrefix(path, "/analysis/sessions/"):
		handleAPIAnalysisSessionDetail(w, r, path, svc)
	case path == "/logs" && r.Method == "DELETE":
		l.Clear()
		json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
	case path == "/logs":
		handleAPILogs(w, r, l)
	case strings.HasPrefix(path, "/logs/"):
		handleAPILogDetail(w, r, l, path)
	case path == "/config" && r.Method == "GET":
		handleAPIGetConfig(w, r, cfg)
	case path == "/config" && r.Method == "POST":
		handleAPISaveConfig(w, r, cfg)
	case path == "/config/reveal" && r.Method == "POST":
		handleAPIRevealKey(w, r, cfg)
	case path == "/providers/ping" && r.Method == "POST":
		handleAPIProviderPing(w, r, cfg)
	case path == "/providers/fetch-models" && r.Method == "POST":
		handleAPIFetchModels(w, r, cfg)
	case path == "/providers/test-llm" && r.Method == "POST":
		handleAPIProviderTestLLM(w, r, cfg)
	case path == "/route/preview" && r.Method == "GET":
		handleAPIRoutePreview(w, r, cfg)
	case path == "/restart" && r.Method == "POST":
		handleAPIRestart(w, r)
	default:
		http.NotFound(w, r)
	}
}

func handleAPIStatus(w http.ResponseWriter, r *http.Request, l *Logger, cfg *SafeConfig) {
	c := cfg.Get()
	stats := l.Stats()
	reasons := []string{}
	restartRequired := false
	if c.Port != runtimePort {
		restartRequired = true
		reasons = append(reasons, "服务端口")
	}
	if c.LANAccess != runtimeLANAccess {
		restartRequired = true
		reasons = append(reasons, "局域网/WSL 访问")
	}

	// 平均 token 速率 = 流式请求总量加权解码吞吐率 (总有效输出tokens / 总有效解码耗时秒)
	tokensRate := l.AvgOutputTokensPerSec()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"port":              c.Port,
		"total":             stats["total"],
		"today":             stats["today"],
		"total_tokens":      stats["total_tokens"],
		"prompt_tokens":     stats["prompt_tokens"],
		"completion_tokens": stats["completion_tokens"],
		"cache_hit_tokens":  stats["cache_hit_tokens"],
		"cache_miss_tokens": stats["cache_miss_tokens"],
		"tokens_rate":       tokensRate,
		"max_logs":          l.maxSize,
		"restart_required":  restartRequired,
		"restart_reasons":   reasons,
	})
}

func handleAPILogs(w http.ResponseWriter, r *http.Request, l *Logger) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	offset := 0
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
		offset = v
	}

	entries := l.GetAll(limit, offset)
	if entries == nil {
		entries = []LogEntry{}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": entries,
		"total":   l.Stats()["total"],
	})
}

func handleAPILogDetail(w http.ResponseWriter, r *http.Request, l *Logger, path string) {
	idStr := strings.TrimPrefix(path, "/logs/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}

	entry := l.Get(id)
	if entry == nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(entry)
}

func handleAPIGetConfig(w http.ResponseWriter, r *http.Request, cfg *SafeConfig) {
	// 所有密钥一律脱敏后返回；需要明文时由前端显式调用 /api/config/reveal。
	json.NewEncoder(w).Encode(configForClient(cfg.Get()))
}

type revealKeyReq struct {
	ProviderID string `json:"provider_id"`
}

func handleAPIRevealKey(w http.ResponseWriter, r *http.Request, cfg *SafeConfig) {
	// 明文密钥只允许本机页面按需获取：同源校验 + Host 必须是 IP 字面量，防 DNS rebinding。
	if !sameOriginRequest(r) || !isLiteralHost(r.Host) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	var req revealKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	c := cfg.Get()
	key := ""
	found := false
	if req.ProviderID == "" {
		key, found = c.APIKey, true
	} else {
		for _, p := range c.Providers {
			if p.ID == req.ProviderID {
				key, found = p.APIKey, true
				break
			}
		}
	}
	if !found {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"api_key": key})
}

func handleAPISaveConfig(w http.ResponseWriter, r *http.Request, cfg *SafeConfig) {
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	var reasons []string
	var warnings []string
	var restartRequired bool
	var err error
	changed := false

	cfg.Update(func(c *Config) {
		// API key: 忽略“未修改”的情况（当前真实密钥或掩码形式都视为无变化）。
		if v, ok := updates["api_key"]; ok {
			if s, ok := v.(string); ok && s != "" && s != c.APIKey && s != maskAPIKey(c.APIKey) {
				// 防御：若前端回传的是已加密值（旧实例未重启的过渡窗口），先还原明文，避免二次加密。
				if strings.HasPrefix(s, apiKeyEncPrefix) {
					if dec, err := decryptAPIKey(s); err == nil {
						s = dec
					}
				}
				c.APIKey = s
				changed = true
			}
		}

		// 供应商列表：前端整体提交，后端整体替换。
		if v, ok := updates["providers"]; ok {
			if arr, ok := v.([]interface{}); ok && len(arr) > 0 {
				var provs []Provider
				oldProvs := c.Providers
				for i, item := range arr {
					m, ok := item.(map[string]interface{})
					if !ok {
						continue
					}
					p := Provider{}
					if n, ok := m["name"].(string); ok {
						p.Name = n
					}
					if id, ok := m["id"].(string); ok {
						p.ID = strings.TrimSpace(id)
					}
					if en, ok := m["enabled"].(bool); ok {
						p.Enabled = boolPtr(en)
					}
					if b, ok := m["base_url"].(string); ok {
						p.BaseURL = strings.TrimRight(b, "/")
					}
					if ab, ok := m["anthropic_base_url"].(string); ok {
						p.AnthropicBaseURL = strings.TrimRight(ab, "/")
					}
					if k, ok := m["api_key"].(string); ok {
						// 前端拿到的是脱敏值；原样回传说明用户没改，沿用旧密钥，避免把掩码写进配置。
						if old := findProviderKey(oldProvs, p.ID, i); old != "" && k == maskAPIKey(old) {
							k = old
						}
						// 防御：若前端回传的是已加密值（旧实例未重启的过渡窗口），先还原明文，避免二次加密。
						if strings.HasPrefix(k, apiKeyEncPrefix) {
							if dec, err := decryptAPIKey(k); err == nil {
								k = dec
							}
						}
						p.APIKey = k
					}
					if modelsRaw, ok := m["models"].([]interface{}); ok {
						for _, mItem := range modelsRaw {
							if mm, ok := mItem.(map[string]interface{}); ok {
								mc := ModelConfig{}
								if id, ok := mm["id"].(string); ok {
									mc.ID = strings.TrimSpace(id)
								}
								if name, ok := mm["name"].(string); ok {
									mc.Name = strings.TrimSpace(name)
								}
								if group, ok := mm["group"].(string); ok {
									mc.Group = strings.TrimSpace(group)
								}
								if proto, ok := mm["protocol"].(string); ok {
									mc.Protocol = strings.TrimSpace(proto)
								}
								if mc.ID != "" {
									p.Models = append(p.Models, mc)
								}
							}
						}
					}
					if p.Name == "" {
						p.Name = "未命名供应商"
					}
					provs = append(provs, p)
				}
				if len(provs) > 0 {
					// 服务端兜底：ID 缺失、非法或重复时自动补齐修正，并强制默认供应商（第一位）保持启用。
					before := providerIDsSignature(provs)
					normalizeProviders(provs)
					if providerIDsSignature(provs) != before {
						warnings = append(warnings, "供应商 ID 存在缺失、非法或重复，已自动修正")
					}
					c.Providers = provs
					changed = true
				}
			}
		}
		// active_provider 为保留字段：默认供应商由供应商列表第一位决定，
		// 前端提交的旧值不再生效（保存时统一对齐到第一位）。
		// Port: must be in valid range.
		if v, ok := updates["port"]; ok {
			if f, ok := v.(float64); ok && f > 0 && f < 65536 {
				if c.Port != int(f) {
					c.Port = int(f)
					changed = true
				}
			}
		}
		// Upstreams: trim trailing slashes.
		if v, ok := updates["openai_upstream"]; ok {
			if s, ok := v.(string); ok && s != "" {
				trimmed := strings.TrimRight(s, "/")
				if c.OpenAIUpstream != trimmed {
					c.OpenAIUpstream = trimmed
					changed = true
				}
			}
		}
		if v, ok := updates["anthropic_upstream"]; ok {
			if s, ok := v.(string); ok && s != "" {
				trimmed := strings.TrimRight(s, "/")
				if c.AnthropicUpstream != trimmed {
					c.AnthropicUpstream = trimmed
					changed = true
				}
			}
		}

		// The remaining fields follow a simple pattern.
		changed = setStringField(updates, "thinking_mode", &c.ThinkingMode) || changed
		changed = setStringField(updates, "reasoning_effort", &c.ReasoningEffort) || changed
		changed = setStringField(updates, "system_prompt_placement", &c.SystemPromptPlacement) || changed
		changed = setStringField(updates, "extra_prompt", &c.ExtraPrompt) || changed
		changed = setStringField(updates, "extra_prompt_placement", &c.ExtraPromptPlacement) || changed
		changed = setStringField(updates, "max_tokens_mode", &c.MaxTokensMode) || changed
		changed = setStringField(updates, "antiloop_retry_model", &c.AntiLoopRetryModel) || changed
		changed = setStringField(updates, "antiloop_retry_thinking", &c.AntiLoopRetryThinking) || changed
		changed = setStringField(updates, "antiloop_retry_effort", &c.AntiLoopRetryEffort) || changed
		changed = setBoolField(updates, "verbose_logging", &c.VerboseLogging) || changed
		changed = setStringField(updates, "language", &c.Language) || changed
		changed = setBoolField(updates, "anti_loop_enabled", &c.AntiLoopEnabled) || changed
		changed = setBoolField(updates, "debug_mode", &c.DebugMode) || changed
		changed = setBoolField(updates, "auto_reasoning_content", &c.AutoReasoningContent) || changed
		changed = setBoolField(updates, "auto_fix_empty_content", &c.AutoFixEmptyContent) || changed
		changed = setBoolField(updates, "analysis_enabled", &c.AnalysisEnabled) || changed
		changed = setBoolField(updates, "lan_access", &c.LANAccess) || changed
		changed = setIntField(updates, "max_tokens_custom", &c.MaxTokensCustom, 0) || changed
		changed = setIntField(updates, "antiloop_check_tokens", &c.AntiLoopCheckTokens, 0) || changed
		changed = setIntField(updates, "analysis_retention_days", &c.AnalysisRetentionDays, 1) || changed
		changed = setBoolField(updates, "anti_hallucination_enabled", &c.AntiHallucinationEnabled) || changed
		changed = setStringField(updates, "anti_hallucination_prompt", &c.AntiHallucinationPrompt) || changed
		if c.AnalysisPersistence != c.AnalysisEnabled {
			c.AnalysisPersistence = c.AnalysisEnabled
			changed = true
		}
		if c.AnalysisPersistRawBodies != c.AnalysisEnabled {
			c.AnalysisPersistRawBodies = c.AnalysisEnabled
			changed = true
		}

		if changed {
			if err = SaveConfig(*c); err != nil {
				return
			}
			if c.Port != runtimePort {
				restartRequired = true
				reasons = append(reasons, "服务端口")
			}
			if c.LANAccess != runtimeLANAccess {
				restartRequired = true
				reasons = append(reasons, "局域网/WSL 访问")
			}
		}
	})

	if err != nil {
		http.Error(w, `{"error":"failed to save config"}`, http.StatusInternalServerError)
		return
	}

	if changed {
		resp := map[string]interface{}{
			"status":           "saved",
			"restart_required": restartRequired,
			"restart_reasons":  reasons,
		}
		if len(warnings) > 0 {
			resp["warnings"] = warnings
		}
		json.NewEncoder(w).Encode(resp)
	} else {
		json.NewEncoder(w).Encode(map[string]string{"status": "unchanged"})
	}
}

// handleAPIRoutePreview 返回当前配置构建出的模型路由表，供界面展示对外模型名、冲突与遮蔽状态。
// 前端不重复实现冲突计算，统一以此接口为单一真相源。
func handleAPIRoutePreview(w http.ResponseWriter, r *http.Request, cfg *SafeConfig) {
	rt := buildRouteTable(cfg.Get())
	conflicted, unroutable := rt.Stats()
	defaultProvider := ""
	if p, _, ok := rt.DefaultProvider(); ok {
		defaultProvider = p.Name
	}

	type routeModelItem struct {
		PublicID     string `json:"public_id"`
		UpstreamID   string `json:"upstream_id"`
		Group        string `json:"group,omitempty"`
		ProviderID   string `json:"provider_id"`
		ProviderName string `json:"provider_name"`
		Conflicted   bool   `json:"conflicted"`
		Unroutable   bool   `json:"unroutable"`
	}
	items := make([]routeModelItem, 0, 16)
	for _, e := range rt.ListModels() {
		items = append(items, routeModelItem{
			PublicID:     e.PublicModelID,
			UpstreamID:   e.UpstreamModelID,
			Group:        e.Group,
			ProviderID:   e.ProviderID,
			ProviderName: e.ProviderName,
			Conflicted:   e.Conflicted,
			Unroutable:   e.Unroutable,
		})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"models":           items,
		"conflicted":       conflicted,
		"unroutable":       unroutable,
		"default_provider": defaultProvider,
	})
}

func handleAPIRestart(w http.ResponseWriter, r *http.Request) {
	if appRestartCh != nil {
		select {
		case appRestartCh <- struct{}{}:
		default:
		}
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "restarting"})
}

// ── Config field update helpers ────────────────────────────────────────────

func setStringField(updates map[string]interface{}, key string, dest *string) bool {
	v, ok := updates[key]
	if !ok {
		return false
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	*dest = s
	return true
}

func setBoolField(updates map[string]interface{}, key string, dest *bool) bool {
	v, ok := updates[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	*dest = b
	return true
}

func setIntField(updates map[string]interface{}, key string, dest *int, min int) bool {
	v, ok := updates[key]
	if !ok {
		return false
	}
	f, ok := v.(float64) // JSON numbers unmarshal as float64
	if !ok {
		return false
	}
	val := int(f)
	if val < min {
		return false
	}
	*dest = val
	return true
}

func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:5] + strings.Repeat("*", len(key)-8) + key[len(key)-3:]
}

// configForClient 返回给前端的配置副本：所有密钥脱敏，避免网页直接读到明文。
func configForClient(c Config) Config {
	out := c
	out.APIKey = maskAPIKey(c.APIKey)
	if c.Providers != nil {
		ps := make([]Provider, len(c.Providers))
		copy(ps, c.Providers)
		for i := range ps {
			ps[i].APIKey = maskAPIKey(ps[i].APIKey)
		}
		out.Providers = ps
	}
	return out
}

// findProviderKey 按 ID（优先）或位置取出旧供应商的明文密钥。
func findProviderKey(old []Provider, id string, idx int) string {
	if id != "" {
		for _, p := range old {
			if p.ID == id {
				return p.APIKey
			}
		}
	}
	if idx >= 0 && idx < len(old) {
		return old[idx].APIKey
	}
	return ""
}

// resolveMaskedAPIKey 把前端回传的脱敏值还原成真实密钥（连通性测试等接口用）。
func resolveMaskedAPIKey(cfg *SafeConfig, key string) string {
	if key == "" || !strings.Contains(key, "*") {
		return key
	}
	c := cfg.Get()
	if c.APIKey != "" && key == maskAPIKey(c.APIKey) {
		return c.APIKey
	}
	for _, p := range c.Providers {
		if p.APIKey != "" && key == maskAPIKey(p.APIKey) {
			return p.APIKey
		}
	}
	return key
}

// setAPICORS 只为同源请求回显 CORS 头，跨站页面无法读取任何接口响应。
func setAPICORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" || !originMatchesHost(origin, r.Host) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Add("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func originMatchesHost(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// sameOriginRequest 判断请求是否来自本机页面（同源）。浏览器发起的跨站请求必带 Origin，可直接拒绝。
func sameOriginRequest(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return originMatchesHost(origin, r.Host)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && strings.EqualFold(u.Host, r.Host)
	}
	// 无 Origin/Referer：非浏览器客户端（curl、本地脚本），放行。
	return true
}

// isLiteralHost 拒绝以域名形式访问的明文接口，防止 DNS rebinding 把恶意域名指向本机。
func isLiteralHost(host string) bool {
	h := host
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		h = hostOnly
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	return net.ParseIP(h) != nil
}

func handleAPIAnalysisStatus(w http.ResponseWriter, r *http.Request, cfg *SafeConfig) {
	c := cfg.Get()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"analysis_enabled":        c.AnalysisEnabled,
		"analysis_persistence":    c.AnalysisPersistence,
		"analysis_persist_raw":    c.AnalysisPersistRawBodies,
		"analysis_retention_days": c.AnalysisRetentionDays,
	})
}

func handleAPIAnalysisSessions(w http.ResponseWriter, r *http.Request, svc *AnalysisService) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	offset := 0
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}
	if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
		offset = v
	}

	if svc == nil {
		json.NewEncoder(w).Encode([]SessionSummary{})
		return
	}

	summaries := svc.GetSessionSummaries(limit, offset)
	if summaries == nil {
		summaries = []SessionSummary{}
	}
	json.NewEncoder(w).Encode(summaries)
}

func handleAPIAnalysisSessionDetail(w http.ResponseWriter, r *http.Request, path string, svc *AnalysisService) {
	id := strings.TrimPrefix(path, "/analysis/sessions/")
	if svc == nil {
		http.Error(w, `{"error":"analysis service not initialized"}`, http.StatusInternalServerError)
		return
	}

	sess := svc.GetSessionDetails(id)
	if sess == nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(sess)
}

func handleAPIAnalysisTimeline(w http.ResponseWriter, r *http.Request, path string, svc *AnalysisService) {
	id := strings.TrimPrefix(path, "/analysis/sessions/")
	id = strings.TrimSuffix(id, "/timeline")
	if svc == nil {
		http.Error(w, `{"error":"analysis service not initialized"}`, http.StatusInternalServerError)
		return
	}

	limit := 100
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}

	page, err := svc.GetTimelinePage(id, offset, limit)
	if err != nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(page)
}

func handleAPIAnalysisExport(w http.ResponseWriter, r *http.Request, path string, svc *AnalysisService) {
	id := strings.TrimPrefix(path, "/analysis/sessions/")
	id = strings.TrimSuffix(id, "/export.md")

	if svc == nil {
		http.Error(w, "analysis service not initialized", http.StatusInternalServerError)
		return
	}

	// 默认模板（导出全部）；若以 POST 提交 JSON 配置则按配置过滤。
	cfg := defaultFullExportConfig()
	if r.Method == "POST" {
		if body, err := io.ReadAll(r.Body); err == nil && len(body) > 0 {
			// 容忍前端传递 "full"/"lite" 模板标识：仅识别已知字段，未知字段忽略。
			var posted ExportConfig
			if json.Unmarshal(body, &posted) == nil {
				cfg = &posted
			}
		}
	}

	md, err := svc.ExportMarkdown(id, cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="dsplus_session_%s.md"`, id))
	w.Write([]byte(md))
}

func handleAPIClearAnalysisHistory(w http.ResponseWriter, r *http.Request, svc *AnalysisService) {
	if svc == nil {
		http.Error(w, `{"error":"analysis service not initialized"}`, http.StatusInternalServerError)
		return
	}

	if err := svc.ClearHistory(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
}

type providerPingReq struct {
	BaseURL          string `json:"base_url"`
	AnthropicBaseURL string `json:"anthropic_base_url"`
	APIKey           string `json:"api_key"`
	Protocol         string `json:"protocol"` // "openai" 或 "anthropic"
}

func handleAPIProviderPing(w http.ResponseWriter, r *http.Request, cfg *SafeConfig) {
	var req providerPingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	targetURL := strings.TrimRight(req.BaseURL, "/")
	if req.Protocol == "anthropic" {
		if req.AnthropicBaseURL != "" {
			targetURL = strings.TrimRight(req.AnthropicBaseURL, "/")
		} else if strings.Contains(targetURL, "api.deepseek.com") && !strings.Contains(targetURL, "/anthropic") {
			targetURL = targetURL + "/anthropic"
		}
	}

	if targetURL == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":         false,
			"error":      "基础地址不能为空",
			"status":     0,
			"latency_ms": 0,
		})
		return
	}

	testURL := joinUpstream(targetURL, "/v1/models")
	if req.Protocol == "anthropic" {
		testURL = targetURL
	}

	httpReq, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":         false,
			"error":      fmt.Sprintf("创建请求失败: %v", err),
			"status":     0,
			"latency_ms": 0,
		})
		return
	}

	apiKey := resolveMaskedAPIKey(cfg, req.APIKey)
	if strings.HasPrefix(apiKey, apiKeyEncPrefix) {
		if dec, err := decryptAPIKey(apiKey); err == nil {
			apiKey = dec
		}
	}

	if apiKey != "" {
		if req.Protocol == "anthropic" {
			httpReq.Header.Set("x-api-key", apiKey)
			httpReq.Header.Set("anthropic-version", "2023-06-01")
		} else {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	httpReq.Header.Set("User-Agent", "DSPlus/1.0 PingRunner")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":         false,
			"error":      fmt.Sprintf("网络连接失败: %v", err),
			"status":     0,
			"latency_ms": latency,
		})
		return
	}
	defer resp.Body.Close()

	isOK := resp.StatusCode >= 200 && resp.StatusCode < 300
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          isOK,
		"status":      resp.StatusCode,
		"status_text": resp.Status,
		"latency_ms":  latency,
		"endpoint":    testURL,
	})
}

type fetchModelsReq struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

func handleAPIFetchModels(w http.ResponseWriter, r *http.Request, cfg *SafeConfig) {
	var req fetchModelsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	baseURL := strings.TrimRight(req.BaseURL, "/")
	if baseURL == "" {
		http.Error(w, `{"error":"base_url is required"}`, http.StatusBadRequest)
		return
	}

	fetchURL := joinUpstream(baseURL, "/v1/models")
	httpReq, err := http.NewRequest("GET", fetchURL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to create request: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	apiKey := resolveMaskedAPIKey(cfg, req.APIKey)
	if strings.HasPrefix(apiKey, apiKeyEncPrefix) {
		if dec, err := decryptAPIKey(apiKey); err == nil {
			apiKey = dec
		}
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "DSPlus/1.0 ModelFetcher")

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":  fmt.Sprintf("拉取失败: %v", err),
			"models": []interface{}{},
		})
		return
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":  fmt.Sprintf("读取响应失败: %v", err),
			"models": []interface{}{},
		})
		return
	}

	if resp.StatusCode != http.StatusOK {
		errMsg := string(bodyBytes)
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":  fmt.Sprintf("上游返回 HTTP %d: %s", resp.StatusCode, errMsg),
			"models": []interface{}{},
		})
		return
	}

	var openAIResp struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}

	if err := json.Unmarshal(bodyBytes, &openAIResp); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":  fmt.Sprintf("解析上游模型列表失败: %v", err),
			"models": []interface{}{},
		})
		return
	}

	var results []ModelConfig
	for _, item := range openAIResp.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		group := "通用 (General)"
		if strings.Contains(id, "/") {
			parts := strings.Split(id, "/")
			if parts[0] != "" {
				group = parts[0]
			}
		} else if item.OwnedBy != "" && item.OwnedBy != "system" {
			group = item.OwnedBy
		}
		results = append(results, ModelConfig{
			ID:       id,
			Name:     id,
			Group:    group,
			Protocol: "openai",
		})
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"models": results,
		"total":  len(results),
	})
}

type providerTestLLMReq struct {
	BaseURL          string `json:"base_url"`
	AnthropicBaseURL string `json:"anthropic_base_url"`
	APIKey           string `json:"api_key"`
	Model            string `json:"model"`
	Protocol         string `json:"protocol"` // "openai" 或 "anthropic"
	Prompt           string `json:"prompt"`
}

func handleAPIProviderTestLLM(w http.ResponseWriter, r *http.Request, cfg *SafeConfig) {
	var req providerTestLLMReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	prompt := req.Prompt
	if prompt == "" {
		prompt = "连接测试，什么都别想，收到回复：收到了"
	}

	apiKey := resolveMaskedAPIKey(cfg, req.APIKey)
	if strings.HasPrefix(apiKey, apiKeyEncPrefix) {
		if dec, err := decryptAPIKey(apiKey); err == nil {
			apiKey = dec
		}
	}

	var targetURL string
	var reqBody []byte
	var httpReq *http.Request
	var err error

	if req.Protocol == "anthropic" {
		base := req.AnthropicBaseURL
		if base == "" {
			base = req.BaseURL
			if strings.Contains(base, "api.deepseek.com") && !strings.Contains(base, "/anthropic") {
				base = strings.TrimRight(base, "/") + "/anthropic"
			}
		}
		targetURL = joinUpstream(base, "/v1/messages")

		payload := map[string]interface{}{
			"model": req.Model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
			"max_tokens": 64,
		}
		reqBody, _ = json.Marshal(payload)
		httpReq, err = http.NewRequest("POST", targetURL, bytes.NewReader(reqBody))
		if err == nil {
			if apiKey != "" {
				httpReq.Header.Set("x-api-key", apiKey)
			}
			httpReq.Header.Set("anthropic-version", "2023-06-01")
			httpReq.Header.Set("Content-Type", "application/json")
		}
	} else {
		base := strings.TrimRight(req.BaseURL, "/")
		targetURL = joinUpstream(base, "/v1/chat/completions")

		payload := map[string]interface{}{
			"model": req.Model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
			"max_tokens": 64,
			"stream":     false,
		}
		reqBody, _ = json.Marshal(payload)
		httpReq, err = http.NewRequest("POST", targetURL, bytes.NewReader(reqBody))
		if err == nil {
			if apiKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+apiKey)
			}
			httpReq.Header.Set("Content-Type", "application/json")
		}
	}

	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":         false,
			"error":      fmt.Sprintf("创建请求失败: %v", err),
			"target_url": targetURL,
			"status":     0,
			"latency_ms": 0,
		})
		return
	}

	httpReq.Header.Set("User-Agent", "DSPlus/1.0 LLMTestRunner")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":         false,
			"error":      fmt.Sprintf("上游连接异常: %v", err),
			"target_url": targetURL,
			"status":     0,
			"latency_ms": latency,
		})
		return
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":         false,
			"error":      fmt.Sprintf("读取响应体失败: %v", err),
			"target_url": targetURL,
			"status":     resp.StatusCode,
			"latency_ms": latency,
		})
		return
	}

	isOK := resp.StatusCode >= 200 && resp.StatusCode < 300
	var reply string

	if isOK {
		var oaiResp struct {
			Choices []struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"message"`
			} `json:"choices"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(respBytes, &oaiResp); err == nil {
			if len(oaiResp.Choices) > 0 {
				reply = oaiResp.Choices[0].Message.Content
				if reply == "" {
					reply = oaiResp.Choices[0].Message.ReasoningContent
				}
			} else if len(oaiResp.Content) > 0 {
				for _, c := range oaiResp.Content {
					if c.Type == "text" {
						reply += c.Text
					}
				}
			}
		}
		if reply == "" {
			reply = string(respBytes)
		}
	} else {
		reply = string(respBytes)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          isOK,
		"status":      resp.StatusCode,
		"status_text": resp.Status,
		"latency_ms":  latency,
		"target_url":  targetURL,
		"reply":       reply,
		"error":       string(respBytes),
	})
}
