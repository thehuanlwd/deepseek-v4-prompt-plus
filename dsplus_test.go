package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── detectFormat ─────────────────────────────────────────────────────────────

func TestDetectFormat_OpenAI(t *testing.T) {
	tests := []string{
		// OpenAI: messages + system role (with or without max_tokens).
		`{"messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"hi"}]}`,
		`{"messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"hi"}],"max_tokens":100}`,
		`{"messages": [{"role": "system", "content": "be nice"}, {"role": "user", "content": "hello"}]}`,
	}
	for _, body := range tests {
		if got := detectFormat("/v1/chat/completions", body); got != "openai" {
			t.Errorf("detectFormat(%q) = %q, want openai", body, got)
		}
	}
}

func TestDetectFormat_Anthropic(t *testing.T) {
	tests := []string{
		// Top-level system field is the strongest signal.
		`{"system":"You are helpful","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`,
		`{"system":[{"type":"text","text":"be nice"}],"messages":[{"role":"user","content":"hi"}],"max_tokens":100}`,
	}
	for _, body := range tests {
		if got := detectFormat("/v1/messages", body); got != "anthropic" {
			t.Errorf("detectFormat(%q) = %q, want anthropic", body, got)
		}
	}
}

func TestDetectFormat_Unknown(t *testing.T) {
	tests := []string{
		``,
		`not json`,
		`{"no_messages":true}`,
		`{"messages":"not an array"}`,
	}
	for _, body := range tests {
		if got := detectFormat("/v1/chat/completions", body); got != "unknown" {
			t.Errorf("detectFormat(%q) = %q, want unknown", body, got)
		}
	}
}

func TestDetectFormat_UserContentDoesNotConfuse(t *testing.T) {
	// "max_tokens" and "role":"system" in user content must not affect detection.
	// This body has a real system role → it's OpenAI regardless of user content.
	body := `{"messages":[{"role":"system","content":"be helpful"},{"role":"user","content":"please use max_tokens and role system"}],"max_tokens":50}`
	if got := detectFormat("/v1/chat/completions", body); got != "openai" {
		t.Errorf("detectFormat with keywords in user content = %q, want openai", got)
	}

	// No system role + has max_tokens → Anthropic (by design).
	body2 := `{"messages":[{"role":"user","content":"hello"}],"max_tokens":100}`
	if got := detectFormat("/v1/messages", body2); got != "anthropic" {
		t.Errorf("detectFormat(no system + max_tokens) = %q, want anthropic", got)
	}
}

// ── transformOpenAIInPlace ───────────────────────────────────────────────────

func TestTransformOpenAI_Basic(t *testing.T) {
	body := `{"messages":[{"role":"system","content":"be helpful"},{"role":"user","content":"hello"}]}`
	var data map[string]interface{}
	json.Unmarshal([]byte(body), &data)

	changed := transformOpenAIInPlace(data, "first", "", "none")
	if !changed {
		t.Fatal("expected change")
	}

	msgs := data["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message after transform, got %d", len(msgs))
	}
	user := msgs[0].(map[string]interface{})
	content := user["content"].(string)
	if !strings.Contains(content, "hello") {
		t.Error("user content should still contain original message")
	}
	if !strings.Contains(content, "<system_prompt>") {
		t.Error("user content should contain system_prompt wrapper")
	}
	if !strings.Contains(content, "be helpful") {
		t.Error("user content should contain system text")
	}
	// System message must be removed.
	if user["role"] != "user" {
		t.Error("only remaining message should be user role")
	}
}

func TestTransformOpenAI_NoSystem(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hello"}]}`
	var data map[string]interface{}
	json.Unmarshal([]byte(body), &data)

	changed := transformOpenAIInPlace(data, "first", "", "none")
	if changed {
		t.Error("should not change when no system prompt")
	}
}

func TestTransformOpenAI_PlacementLast(t *testing.T) {
	body := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"first"},{"role":"user","content":"last"}]}`
	var data map[string]interface{}
	json.Unmarshal([]byte(body), &data)

	transformOpenAIInPlace(data, "last", "", "none")

	msgs := data["messages"].([]interface{})
	// After removing system, we have 2 user messages. The system should be appended to "last".
	lastUser := msgs[1].(map[string]interface{})
	content := lastUser["content"].(string)
	if !strings.Contains(content, "sys") {
		t.Error("system text should be appended to last user message")
	}
}

func TestTransformOpenAI_PlacementNone(t *testing.T) {
	body := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`
	var data map[string]interface{}
	json.Unmarshal([]byte(body), &data)

	changed := transformOpenAIInPlace(data, "none", "", "none")
	if changed {
		t.Error("placement=none should not transform")
	}
}

func TestTransformOpenAI_ExtraPrompt(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hi"}]}`
	var data map[string]interface{}
	json.Unmarshal([]byte(body), &data)

	changed := transformOpenAIInPlace(data, "first", "EXTRA RULES", "first")
	if !changed {
		t.Fatal("expected change for extra prompt")
	}
	msgs := data["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].(string)
	if !strings.Contains(content, "<supreme_instruction>") {
		t.Error("extra prompt should be wrapped in supreme_instruction tags")
	}
	if !strings.Contains(content, "EXTRA RULES") {
		t.Error("extra prompt content should appear")
	}
}

// ── transformAnthropicInPlace ────────────────────────────────────────────────

func TestTransformAnthropic_Basic(t *testing.T) {
	body := `{"system":"be helpful","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`
	var data map[string]interface{}
	json.Unmarshal([]byte(body), &data)

	changed := transformAnthropicInPlace(data, "first", "", "none")
	if !changed {
		t.Fatal("expected change")
	}
	if _, hasSystem := data["system"]; hasSystem {
		t.Error("system field should be deleted after transform")
	}
	content := data["messages"].([]interface{})[0].(map[string]interface{})["content"].(string)
	if !strings.Contains(content, "<system_prompt>") {
		t.Error("system text should be injected into user message")
	}
}

func TestTransformAnthropic_SystemContentBlocks(t *testing.T) {
	body := `{"system":[{"type":"text","text":"rule 1"},{"type":"text","text":"rule 2"}],"messages":[{"role":"user","content":"hi"}]}`
	var data map[string]interface{}
	json.Unmarshal([]byte(body), &data)

	transformAnthropicInPlace(data, "first", "", "none")
	content := data["messages"].([]interface{})[0].(map[string]interface{})["content"].(string)
	if !strings.Contains(content, "rule 1") || !strings.Contains(content, "rule 2") {
		t.Error("both system text blocks should be present")
	}
}

func TestTransformAnthropic_UserContentArray(t *testing.T) {
	body := `{"system":"be nice","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	var data map[string]interface{}
	json.Unmarshal([]byte(body), &data)

	transformAnthropicInPlace(data, "first", "", "none")
	blocks := data["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})
	// Should have original block + injected block
	if len(blocks) < 2 {
		t.Fatalf("expected >=2 content blocks, got %d", len(blocks))
	}
	lastBlock := blocks[len(blocks)-1].(map[string]interface{})
	if lastBlock["type"] != "text" {
		t.Error("injected block should be text type")
	}
}

// ── parseUsageFromMap ────────────────────────────────────────────────────────

func TestParseUsageFromMap_Standard(t *testing.T) {
	data := map[string]interface{}{
		"usage": map[string]interface{}{
			"total_tokens":      float64(100),
			"prompt_tokens":     float64(60),
			"completion_tokens": float64(40),
		},
	}
	u := parseUsageFromMap(data)
	if u == nil {
		t.Fatal("expected usage")
	}
	if u.Total != 100 || u.Prompt != 60 || u.Completion != 40 {
		t.Errorf("unexpected values: %+v", u)
	}
}

func TestParseUsageFromMap_CacheFields(t *testing.T) {
	data := map[string]interface{}{
		"usage": map[string]interface{}{
			"prompt_tokens":           float64(100),
			"completion_tokens":       float64(50),
			"prompt_cache_hit_tokens": float64(80),
			"prompt_cache_miss_tokens": float64(20),
		},
	}
	u := parseUsageFromMap(data)
	if u.CacheHit != 80 || u.CacheMiss != 20 {
		t.Errorf("cache fields: hit=%d miss=%d", u.CacheHit, u.CacheMiss)
	}
	if u.Total != 150 {
		t.Errorf("total should be computed: got %d", u.Total)
	}
}

func TestParseUsageFromMap_Nil(t *testing.T) {
	if u := parseUsageFromMap(map[string]interface{}{}); u != nil {
		t.Error("expected nil for missing usage")
	}
}

// ── extractAnthropicSystem ───────────────────────────────────────────────────

func TestExtractAnthropicSystem_String(t *testing.T) {
	if got := extractAnthropicSystem("hello"); got != "hello" {
		t.Errorf("string: got %q", got)
	}
}

func TestExtractAnthropicSystem_Blocks(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{"type": "text", "text": "first"},
		map[string]interface{}{"type": "text", "text": "second"},
	}
	if got := extractAnthropicSystem(blocks); got != "first\nsecond" {
		t.Errorf("blocks: got %q", got)
	}
}

func TestExtractAnthropicSystem_Empty(t *testing.T) {
	if got := extractAnthropicSystem(42); got != "" {
		t.Errorf("unknown type: got %q", got)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func TestUserMessageIndices(t *testing.T) {
	messages := []interface{}{
		map[string]interface{}{"role": "system", "content": "sys"},
		map[string]interface{}{"role": "user", "content": "first"},
		map[string]interface{}{"role": "assistant", "content": "reply"},
		map[string]interface{}{"role": "user", "content": "last"},
	}
	first, last := userMessageIndices(messages)
	if first != 1 || last != 3 {
		t.Errorf("first=%d last=%d, want first=1 last=3", first, last)
	}
}

func TestUserMessageIndices_NoUser(t *testing.T) {
	messages := []interface{}{
		map[string]interface{}{"role": "system", "content": "sys"},
	}
	first, last := userMessageIndices(messages)
	if first != -1 || last != -1 {
		t.Errorf("should be -1,-1, got %d,%d", first, last)
	}
}

func TestResolveTarget(t *testing.T) {
	if got := resolveTarget(1, 3, "first"); got != 1 {
		t.Errorf("first: got %d", got)
	}
	if got := resolveTarget(1, 3, "last"); got != 3 {
		t.Errorf("last: got %d", got)
	}
	if got := resolveTarget(-1, -1, "first"); got != -1 {
		t.Errorf("none: got %d", got)
	}
}

// ── captureBuffer ─────────────────────────────────────────────────────────────

func TestCaptureBuffer(t *testing.T) {
	cb := newCaptureBuffer(100) // large enough to avoid truncation
	cb.Write([]byte("hello"))
	cb.Write([]byte(" world"))
	if s := cb.String(); s != "hello world" {
		t.Errorf("got %q", s)
	}
}

func TestCaptureBuffer_Overflow(t *testing.T) {
	cb := newCaptureBuffer(5)
	cb.Write([]byte("hello world"))
	s := cb.String()
	if !strings.Contains(s, "[stream truncated]") {
		t.Error("overflow should append truncation notice")
	}
}

// ── maskAPIKey ───────────────────────────────────────────────────────────────

func TestMaskAPIKey(t *testing.T) {
	masked := maskAPIKey("sk-1234567890abcdef")
	if !strings.Contains(masked, "sk-12") || !strings.Contains(masked, "def") {
		t.Errorf("masked: %q", masked)
	}
	if len(masked) < len("sk-1234567890abcdef") {
		t.Error("masked key should not be shorter than original")
	}
}

func TestMaskAPIKey_Short(t *testing.T) {
	if maskAPIKey("short") != "short" {
		t.Error("short keys should not be masked")
	}
}

// ── Config round-trip ────────────────────────────────────────────────────────

func TestConfigSaveLoad(t *testing.T) {
	// Verify marshal/unmarshal symmetry.
	cfg := DefaultConfig()
	cfg.Port = 9999
	cfg.APIKey = "sk-test"

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Port != 9999 {
		t.Errorf("Port: %d", restored.Port)
	}
	if restored.APIKey != "sk-test" {
		t.Errorf("APIKey: %q", restored.APIKey)
	}
	if restored.OpenAIUpstream != "https://api.deepseek.com" {
		t.Errorf("OpenAIUpstream: %q", restored.OpenAIUpstream)
	}
}

// Test file-based persistence with a temporary config.
func TestConfigPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	cfg.Port = 5555
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(path, data, 0600)

	// Can't test LoadConfig directly (it reads exe dir), but test the
	// underlying read + unmarshal logic manually.
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	json.Unmarshal(read, &restored)
	if restored.Port != 5555 {
		t.Errorf("Port = %d", restored.Port)
	}
}

// ── API Key 加密混淆 round-trip ───────────────────────────────────────────────

func TestAPIKeyCryptoRoundTrip(t *testing.T) {
	plain := "sk-1234567890abcdefghij"
	enc, err := encryptAPIKey(plain)
	if err != nil {
		t.Fatal(err)
	}
	if enc == plain {
		t.Error("加密后不应等于明文")
	}
	if !strings.HasPrefix(enc, apiKeyEncPrefix) {
		t.Errorf("加密结果应带 %q 前缀: %q", apiKeyEncPrefix, enc)
	}
	dec, err := decryptAPIKey(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != plain {
		t.Errorf("解密结果 %q != 明文 %q", dec, plain)
	}
}

func TestDecryptAPIKeyBackwardCompat(t *testing.T) {
	// 旧版明文（无前缀）应原样返回，保证已有 config.json 可读取。
	legacy := "sk-plaintext-legacy"
	dec, err := decryptAPIKey(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if dec != legacy {
		t.Errorf("旧版明文应原样返回: %q", dec)
	}
	// 空字符串返回空。
	if d, _ := decryptAPIKey(""); d != "" {
		t.Errorf("空字符串应返回空: %q", d)
	}
}

// TestSaveConfigDoesNotCorruptInMemory 回归测试：SaveConfig 必须在落盘时加密，
// 但绝不能把内存中的明文密钥改写成密文（切片浅拷贝曾导致该 bug，使 API Key 失效、
// 前端小眼睛显示密文）。
func TestSaveConfigDoesNotCorruptInMemory(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{Name: "DeepSeek", BaseURL: "https://api.deepseek.com", APIKey: "sk-REAL-PLAINTEXT-KEY"},
		},
		ActiveProvider: "DeepSeek",
	}
	// 用临时文件接管 configPath，避免污染真实配置。
	tmp := t.TempDir()
	configFilePath = filepath.Join(tmp, "config.json")
	defer func() { configFilePath = "" }()

	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// 内存中的明文密钥必须保持原样（未被加密）。
	if cfg.Providers[0].APIKey != "sk-REAL-PLAINTEXT-KEY" {
		t.Errorf("SaveConfig 不应修改内存中的明文密钥，得到: %q", cfg.Providers[0].APIKey)
	}
	// 落盘内容必须是密文（带 ENC: 前缀）。
	raw, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), apiKeyEncPrefix) {
		t.Errorf("落盘应加密存储，但 config.json 未含 %s 前缀: %s", apiKeyEncPrefix, string(raw))
	}
	// 重新加载应能还原明文。
	loaded, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Providers[0].APIKey != "sk-REAL-PLAINTEXT-KEY" {
		t.Errorf("重新加载后应为明文，得到: %q", loaded.Providers[0].APIKey)
	}
}

// ── injectThinkingParams ─────────────────────────────────────────────────────

func TestInjectThinkingParams_Disabled(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "disabled"})}
	data := map[string]interface{}{"model": "test"}
	ps.injectThinkingParams(data, "openai")
	thinking := data["thinking"].(map[string]interface{})
	if thinking["type"] != "disabled" {
		t.Errorf("type = %v", thinking["type"])
	}
}

func TestInjectThinkingParams_Enabled(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled", ReasoningEffort: "max"})}
	data := map[string]interface{}{"model": "test"}
	ps.injectThinkingParams(data, "openai")
	thinking := data["thinking"].(map[string]interface{})
	if thinking["type"] != "enabled" {
		t.Errorf("type = %v", thinking["type"])
	}
	if data["reasoning_effort"] != "max" {
		t.Errorf("effort = %v", data["reasoning_effort"])
	}
}

func TestInjectThinkingParams_Empty(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: ""})}
	data := map[string]interface{}{"model": "test"}
	ps.injectThinkingParams(data, "openai")
	if _, exists := data["thinking"]; exists {
		t.Error("empty mode should not inject thinking param")
	}
}

// ── DeepSeek 系列档位收敛 ────────────────────────────────────────────────────

func TestSanitizeReasoningEffortFor_DeepSeek(t *testing.T) {
	cases := map[string]string{
		"minimal": "low",
		"low":     "low",
		"medium":  "high",
		"high":    "high",
		"xhigh":   "high",
		"max":     "max",
		"ultra":   "max",
		"none":    "none", // 表外取值（关闭思考）保持原样
		"  MAX  ": "max",  // 大小写与空白应被规整
	}
	for in, want := range cases {
		if got := sanitizeReasoningEffortFor(in, true); got != want {
			t.Errorf("DeepSeek %q: got %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeReasoningEffortFor_NonDeepSeek(t *testing.T) {
	// 非 DeepSeek 通道：仅 ultra → max，其余档位原样保留，不做 DeepSeek 收敛。
	cases := map[string]string{
		"minimal": "minimal",
		"low":     "low",
		"medium":  "medium",
		"high":    "high",
		"xhigh":   "xhigh",
		"max":     "max",
		"ultra":   "max",
	}
	for in, want := range cases {
		if got := sanitizeReasoningEffortFor(in, false); got != want {
			t.Errorf("非 DeepSeek %q: got %q, want %q", in, got, want)
		}
	}
}

func TestIsDeepSeekModel_ByModelID(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{})}
	cases := []struct {
		model string
		want  bool
	}{
		{"deepseek-chat", true},
		{"deepseek-reasoner", true},
		{"cline-pass/deepseek-v4.1-flash", true},
		{"DeepSeek-V4-Pro", true},
		{"gpt-5", false},
		{"claude-sonnet-4", false},
		{"", false},
	}
	for _, c := range cases {
		data := map[string]interface{}{}
		if c.model != "" {
			data["model"] = c.model
		}
		if got := ps.isDeepSeekModel(data, "openai"); got != c.want {
			t.Errorf("model %q: got %v, want %v", c.model, got, c.want)
		}
	}
}

func TestIsDeepSeekModel_ByUpstreamHost(t *testing.T) {
	// 模型名不含 deepseek，但上游指向官方域名时，仍视为 DeepSeek 通道。
	ds := &ProxyServer{config: NewSafeConfig(Config{
		ActiveProvider: "DeepSeek",
		Providers:      []Provider{{Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1"}},
	})}
	if !ds.isDeepSeekModel(map[string]interface{}{"model": "v4-flash"}, "openai") {
		t.Error("上游为 api.deepseek.com 时应判定为 DeepSeek")
	}
	// 其他厂商上游且模型名不含 deepseek：不得判定为 DeepSeek。
	other := &ProxyServer{config: NewSafeConfig(Config{
		ActiveProvider: "OpenRouter",
		Providers:      []Provider{{Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1"}},
	})}
	if other.isDeepSeekModel(map[string]interface{}{"model": "gpt-5"}, "openai") {
		t.Error("非 DeepSeek 上游与模型不应判定为 DeepSeek")
	}
}

func TestIsDeepSeekModel_AnthropicBaseURL(t *testing.T) {
	// Anthropic 格式的真实上游由 AnthropicBaseURL 决定，判定必须与格式对齐，
	// 否则会漏判（实际打到 DeepSeek 却不收敛）或误判（实际打到第三方却收敛）。
	split := &ProxyServer{config: NewSafeConfig(Config{
		ActiveProvider: "Split",
		Providers: []Provider{{
			Name:             "Split",
			BaseURL:          "https://openrouter.ai/api/v1",
			AnthropicBaseURL: "https://api.deepseek.com/anthropic",
		}},
	})}
	if !split.isDeepSeekModel(map[string]interface{}{"model": "v4-flash"}, "anthropic") {
		t.Error("Anthropic 格式经 AnthropicBaseURL 命中 DeepSeek 时应判定为 DeepSeek")
	}
	if split.isDeepSeekModel(map[string]interface{}{"model": "v4-flash"}, "openai") {
		t.Error("OpenAI 格式走第三方 BaseURL，不应判定为 DeepSeek")
	}

	// 反向：BaseURL 是 DeepSeek，但 Anthropic 格式走第三方 AnthropicBaseURL。
	reverse := &ProxyServer{config: NewSafeConfig(Config{
		ActiveProvider: "Reverse",
		Providers: []Provider{{
			Name:             "Reverse",
			BaseURL:          "https://api.deepseek.com",
			AnthropicBaseURL: "https://gateway.example.com/anthropic",
		}},
	})}
	if reverse.isDeepSeekModel(map[string]interface{}{"model": "v4-flash"}, "anthropic") {
		t.Error("Anthropic 格式走第三方 AnthropicBaseURL 时不应判定为 DeepSeek")
	}
}

func TestIsDeepSeekModel_ThirdPartyGatewayWithDeepSeekModel(t *testing.T) {
	// 判据一与厂商无关：第三方网关上的 DeepSeek 系列模型同样收敛，其他模型不受影响。
	ps := &ProxyServer{config: NewSafeConfig(Config{
		ActiveProvider: "OpenRouter",
		Providers:      []Provider{{Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1"}},
	})}
	if !ps.isDeepSeekModel(map[string]interface{}{"model": "deepseek/deepseek-v4.1-flash"}, "openai") {
		t.Error("第三方网关上的 DeepSeek 系列模型仍应判定为 DeepSeek")
	}
	if ps.isDeepSeekModel(map[string]interface{}{"model": "openai/gpt-5"}, "openai") {
		t.Error("第三方网关上的非 DeepSeek 模型不应判定为 DeepSeek")
	}
}

func TestIsDeepSeekHost(t *testing.T) {
	cases := map[string]bool{
		"api.deepseek.com":     true,
		"deepseek.com":         true,
		"api.deepseek.com.":    true, // FQDN 尾点
		"API.DeepSeek.com":     true,
		"notdeepseek.com":      false,
		"deepseek.com.evil.io": false,
		"":                     false,
	}
	for in, want := range cases {
		if got := isDeepSeekHost(in); got != want {
			t.Errorf("isDeepSeekHost(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestUpstreamHostname(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":        "api.deepseek.com",
		"https://api.deepseek.com/v1":     "api.deepseek.com",
		"https://api.deepseek.com:443/v1": "api.deepseek.com",
		"api.deepseek.com/v1":             "api.deepseek.com",
		"":                                "",
	}
	for in, want := range cases {
		if got := upstreamHostname(in); got != want {
			t.Errorf("upstreamHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInjectThinkingParams_DeepSeekMapping(t *testing.T) {
	// DeepSeek 模型：medium 实际应发出 high，reasoning.effort 同步。
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled", ReasoningEffort: "medium"})}
	data := map[string]interface{}{"model": "deepseek-chat"}
	ps.injectThinkingParams(data, "openai")
	if data["reasoning_effort"] != "high" {
		t.Errorf("DeepSeek medium 应映射为 high, got %v", data["reasoning_effort"])
	}
	if r, _ := data["reasoning"].(map[string]interface{}); r == nil || r["effort"] != "high" {
		t.Errorf("reasoning.effort 应同步为 high, got %v", data["reasoning"])
	}

	// 非 DeepSeek 模型：medium 必须保持 medium，不被收敛。
	ps2 := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled", ReasoningEffort: "medium"})}
	data2 := map[string]interface{}{"model": "gpt-5"}
	ps2.injectThinkingParams(data2, "openai")
	if data2["reasoning_effort"] != "medium" {
		t.Errorf("非 DeepSeek medium 应保持 medium, got %v", data2["reasoning_effort"])
	}
}

func TestInjectThinkingParams_DeepSeekMinimalToLow(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled", ReasoningEffort: "minimal"})}
	data := map[string]interface{}{"model": "cline-pass/deepseek-v4.1-flash"}
	ps.injectThinkingParams(data, "openai")
	if data["reasoning_effort"] != "low" {
		t.Errorf("DeepSeek minimal 应映射为 low, got %v", data["reasoning_effort"])
	}
}

func TestInjectThinkingParams_AnthropicDeepSeekMapping(t *testing.T) {
	// Anthropic 格式走 output_config.effort，同样适用 DeepSeek 收敛。
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled", ReasoningEffort: "xhigh"})}
	data := map[string]interface{}{"model": "deepseek-chat"}
	ps.injectThinkingParams(data, "anthropic")
	oc, _ := data["output_config"].(map[string]interface{})
	if oc == nil || oc["effort"] != "high" {
		t.Errorf("Anthropic DeepSeek xhigh 应映射为 high, got %v", data["output_config"])
	}

	// 非 DeepSeek 的 Anthropic 请求不受影响。
	ps2 := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled", ReasoningEffort: "xhigh"})}
	data2 := map[string]interface{}{"model": "claude-sonnet-4"}
	ps2.injectThinkingParams(data2, "anthropic")
	oc2, _ := data2["output_config"].(map[string]interface{})
	if oc2 == nil || oc2["effort"] != "xhigh" {
		t.Errorf("非 DeepSeek Anthropic xhigh 应保持 xhigh, got %v", data2["output_config"])
	}
}

func TestInjectThinkingParams_ClientSuppliedDeepSeekMapping(t *testing.T) {
	// thinking_mode 为空时不覆盖客户端档位，但 DeepSeek 系列仍应做收敛。
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: ""})}
	data := map[string]interface{}{
		"model":            "deepseek-chat",
		"reasoning_effort": "xhigh",
	}
	ps.injectThinkingParams(data, "openai")
	if data["reasoning_effort"] != "high" {
		t.Errorf("客户端 xhigh 在 DeepSeek 通道应收敛为 high, got %v", data["reasoning_effort"])
	}

	// 非 DeepSeek 通道下客户端 xhigh 原样保留。
	ps2 := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: ""})}
	data2 := map[string]interface{}{
		"model":            "gpt-5",
		"reasoning_effort": "xhigh",
	}
	ps2.injectThinkingParams(data2, "openai")
	if data2["reasoning_effort"] != "xhigh" {
		t.Errorf("非 DeepSeek 通道客户端 xhigh 应保留, got %v", data2["reasoning_effort"])
	}
}

func TestInjectThinkingParams_ClientSuppliedFieldVariants(t *testing.T) {
	// thinking_mode 为空时，reasoning.effort 与 output_config.effort 两种写法同样要收敛。
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: ""})}
	data := map[string]interface{}{
		"model":     "deepseek-chat",
		"reasoning": map[string]interface{}{"effort": "minimal"},
	}
	ps.injectThinkingParams(data, "openai")
	if r, _ := data["reasoning"].(map[string]interface{}); r == nil || r["effort"] != "low" {
		t.Errorf("reasoning.effort 的 minimal 应收敛为 low, got %v", data["reasoning"])
	}

	ps2 := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: ""})}
	data2 := map[string]interface{}{
		"model":         "deepseek-chat",
		"output_config": map[string]interface{}{"effort": "ultra"},
	}
	ps2.injectThinkingParams(data2, "anthropic")
	if oc, _ := data2["output_config"].(map[string]interface{}); oc == nil || oc["effort"] != "max" {
		t.Errorf("output_config.effort 的 ultra 应收敛为 max, got %v", data2["output_config"])
	}
}

func TestInjectThinkingParams_EnabledWithoutEffortDeepSeek(t *testing.T) {
	// thinking_mode=enabled 但服务端未配置档位：DeepSeek 通道仍需收敛客户端自带值。
	ps := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled"})}
	data := map[string]interface{}{
		"model":            "deepseek-chat",
		"reasoning_effort": "medium",
	}
	ps.injectThinkingParams(data, "openai")
	if data["reasoning_effort"] != "high" {
		t.Errorf("空档位配置下 DeepSeek medium 应收敛为 high, got %v", data["reasoning_effort"])
	}

	// Anthropic 格式：客户端残留的 output_config.effort 同样收敛。
	ps2 := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled"})}
	data2 := map[string]interface{}{
		"model":         "deepseek-chat",
		"output_config": map[string]interface{}{"effort": "xhigh"},
	}
	ps2.injectThinkingParams(data2, "anthropic")
	if oc, _ := data2["output_config"].(map[string]interface{}); oc == nil || oc["effort"] != "high" {
		t.Errorf("空档位配置下 Anthropic DeepSeek xhigh 应收敛为 high, got %v", data2["output_config"])
	}

	// 非 DeepSeek 通道保持既有行为：客户端值原样不动。
	ps3 := &ProxyServer{config: NewSafeConfig(Config{ThinkingMode: "enabled"})}
	data3 := map[string]interface{}{
		"model":            "gpt-5",
		"reasoning_effort": "medium",
	}
	ps3.injectThinkingParams(data3, "openai")
	if data3["reasoning_effort"] != "medium" {
		t.Errorf("非 DeepSeek 空档位配置下 medium 应保持 medium, got %v", data3["reasoning_effort"])
	}
}

// ── applyRetryThinking（防循环重试链路） ─────────────────────────────────────

func TestApplyRetryThinking_DeepSeekMapping(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{
		AntiLoopRetryThinking: "enabled",
		AntiLoopRetryEffort:   "medium",
	})}
	data := map[string]interface{}{"model": "deepseek-chat"}
	ps.applyRetryThinking(data, "openai", nil)
	if data["reasoning_effort"] != "high" {
		t.Errorf("DeepSeek 重试 medium 应收敛为 high, got %v", data["reasoning_effort"])
	}
	if r, _ := data["reasoning"].(map[string]interface{}); r == nil || r["effort"] != "high" {
		t.Errorf("重试 reasoning.effort 应同步为 high, got %v", data["reasoning"])
	}

	// 非 DeepSeek 重试模型：medium 保持 medium。
	ps2 := &ProxyServer{config: NewSafeConfig(Config{
		AntiLoopRetryThinking: "enabled",
		AntiLoopRetryEffort:   "medium",
	})}
	data2 := map[string]interface{}{"model": "gpt-5"}
	ps2.applyRetryThinking(data2, "openai", nil)
	if data2["reasoning_effort"] != "medium" {
		t.Errorf("非 DeepSeek 重试 medium 应保持 medium, got %v", data2["reasoning_effort"])
	}
}

func TestApplyRetryThinking_AnthropicDeepSeekMapping(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{
		AntiLoopRetryThinking: "enabled",
		AntiLoopRetryEffort:   "xhigh",
	})}
	data := map[string]interface{}{"model": "deepseek-chat"}
	ps.applyRetryThinking(data, "anthropic", nil)
	if oc, _ := data["output_config"].(map[string]interface{}); oc == nil || oc["effort"] != "high" {
		t.Errorf("Anthropic 重试 DeepSeek xhigh 应收敛为 high, got %v", data["output_config"])
	}

	// 非 DeepSeek 的 Anthropic 重试保持 xhigh。
	ps2 := &ProxyServer{config: NewSafeConfig(Config{
		AntiLoopRetryThinking: "enabled",
		AntiLoopRetryEffort:   "xhigh",
	})}
	data2 := map[string]interface{}{"model": "claude-sonnet-4"}
	ps2.applyRetryThinking(data2, "anthropic", nil)
	if oc2, _ := data2["output_config"].(map[string]interface{}); oc2 == nil || oc2["effort"] != "xhigh" {
		t.Errorf("非 DeepSeek Anthropic 重试 xhigh 应保持 xhigh, got %v", data2["output_config"])
	}
}

// ── injectMaxTokens ─────────────────────────────────────────────────────────

func TestInjectMaxTokens_Custom(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{MaxTokensMode: "custom", MaxTokensCustom: 8000})}
	data := map[string]interface{}{}
	ps.injectMaxTokens(data)
	if data["max_tokens"] != 8000 {
		t.Errorf("max_tokens = %v", data["max_tokens"])
	}
}

func TestInjectMaxTokens_Empty(t *testing.T) {
	ps := &ProxyServer{config: NewSafeConfig(Config{MaxTokensMode: ""})}
	data := map[string]interface{}{"max_tokens": 100}
	ps.injectMaxTokens(data)
	if data["max_tokens"] != 100 {
		t.Error("empty mode should not overwrite existing max_tokens")
	}
}

// ── truncateBody ─────────────────────────────────────────────────────────────

func TestTruncateBody(t *testing.T) {
	short := "hello"
	if truncateBody(short) != short {
		t.Error("short string should not be truncated")
	}
	long := strings.Repeat("x", truncateBodyMaxLen+100)
	result := truncateBody(long)
	expectedMax := truncateBodyMaxLen + len("\n\n... [truncated]")
	if len(result) > expectedMax {
		t.Errorf("len=%d, want <= %d", len(result), expectedMax)
	}
	if !strings.Contains(result, "[truncated]") {
		t.Error("truncated string should have marker")
	}
}

// ── condStr ──────────────────────────────────────────────────────────────────

func TestCondStr(t *testing.T) {
	if condStr(true, "a", "b") != "a" {
		t.Error("true should return a")
	}
	if condStr(false, "a", "b") != "b" {
		t.Error("false should return b")
	}
}

func TestReplaceFullWidthVerticalBars(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"｜｜DSML｜｜"}}`,
			expected: `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"||DSML||"}}`,
		},
		{
			input:    `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"</｜｜DSML｜｜tool_calls>"}}`,
			expected: `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"</||DSML||tool_calls>"}}`,
		},
		{
			input:    `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"\uff5c\uff5cDSML\uff5c\uff5c"}}`,
			expected: `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"||DSML||"}}`,
		},
		{
			input:    `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>"}}`,
			expected: `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"</||DSML||tool_calls>"}}`,
		},
		{
			input:    `some ｜ text ｜`,
			expected: `some | text |`,
		},
		{
			input:    `some \uff5c text \uFF5C`,
			expected: `some | text |`,
		},
		{
			input:    `normal text`,
			expected: `normal text`,
		},
	}

	for _, tc := range tests {
		got := replaceDSMLMarkers(tc.input)
		if got != tc.expected {
			t.Errorf("Replace failed: got %q, want %q", got, tc.expected)
		}
	}
}

func TestHandleAPISaveConfig_LANAccessRequiresRestart(t *testing.T) {
	oldPort := runtimePort
	oldLANAccess := runtimeLANAccess
	oldRestartCh := appRestartCh
	defer func() {
		runtimePort = oldPort
		runtimeLANAccess = oldLANAccess
		appRestartCh = oldRestartCh
	}()

	cfg := DefaultConfig()
	cfg.Port = 8188
	cfg.LANAccess = false
	setRuntimeState(cfg.Port, cfg.LANAccess)

	req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"port":8188,"lan_access":true}`))
	rec := httptest.NewRecorder()
	handleAPISaveConfig(rec, req, NewSafeConfig(cfg))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "saved" {
		t.Fatalf("status = %v", resp["status"])
	}
	if resp["restart_required"] != true {
		t.Fatalf("restart_required = %v", resp["restart_required"])
	}
	reasons, ok := resp["restart_reasons"].([]interface{})
	if !ok || len(reasons) != 1 || reasons[0] != "局域网/WSL 访问" {
		t.Fatalf("restart_reasons = %#v", resp["restart_reasons"])
	}
}

func TestDetectFormat_PathBased(t *testing.T) {
	tests := []struct {
		path string
		body string
		want string
	}{
		{"/v1/models", "", "openai"},
		{"/models", "", "openai"},
		{"/version", "", "version"},
		{"/v1/version", "", "version"},
		{"/props", "", "props"},
		{"/v1/props", "", "props"},
		{"/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, "openai"},
	}

	for _, tc := range tests {
		if got := detectFormat(tc.path, tc.body); got != tc.want {
			t.Errorf("detectFormat(%q, %q) = %q, want %q", tc.path, tc.body, got, tc.want)
		}
	}
}




