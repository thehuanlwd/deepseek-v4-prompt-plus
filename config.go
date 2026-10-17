package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ModelConfig 表示某个供应商下支持的具体模型配置项
type ModelConfig struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Group    string `json:"group,omitempty"`
	Protocol string `json:"protocol,omitempty"` // "openai" 或 "anthropic"
}

// Provider 表示一个上游供应商配置。
// 每个供应商有独立的基础地址与 API Key；运行时按请求中的模型名路由到对应供应商，
// 数组顺序即语义：第一位是默认供应商（路由未命中时的兜底目标）。
type Provider struct {
	ID               string        `json:"id"`                // 唯一标识：模型 ID 跨供应商重名时作为对外前缀；禁止 "/"
	Name             string        `json:"name"`              // 显示名称；改名不影响 ID
	Enabled          *bool         `json:"enabled,omitempty"` // 是否参与路由；nil 视为启用（旧配置兼容）；第一位强制启用
	BaseURL          string        `json:"base_url"`
	AnthropicBaseURL string        `json:"anthropic_base_url"` // 可选：Anthropic 格式的专用基础地址；留空则自动推断
	APIKey           string        `json:"api_key"`            // 落盘时加密存储
	Models           []ModelConfig `json:"models,omitempty"`   // 可选：该供应商下配置的模型清单；为空时不参与路由表（作为默认供应商时仍可全能透传）
}

// IsEnabled 判断供应商是否参与路由。Enabled 为 nil（旧配置未设置）时视为启用。
func (p Provider) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

func boolPtr(b bool) *bool { return &b }

// isValidProviderID 校验供应商 ID：允许字母、数字、下划线、短横线。
// 允许纯数字；禁止 "/" 等特殊字符——"/" 是冲突前缀的分隔符，出现在 ID 中会导致路由解析歧义。
func isValidProviderID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// uniqueProviderID 在已占用集合内生成唯一 ID：base 冲突时依次尝试 base2、base3……
func uniqueProviderID(base string, used map[string]bool) string {
	id := base
	for n := 2; used[strings.ToLower(id)]; n++ {
		id = fmt.Sprintf("%s%d", base, n)
	}
	used[strings.ToLower(id)] = true
	return id
}

// normalizeProviderIDs 补齐与修正供应商 ID，保证全局唯一且合法。
// 规则：英文名称取同名；非英文（或含非法字符）的名称按 provider1、provider2 顺序生成；
// 与已有 ID 重复时自动追加序号。
func normalizeProviderIDs(provs []Provider) {
	used := make(map[string]bool, len(provs))
	settled := make([]bool, len(provs))
	for i := range provs {
		id := strings.TrimSpace(provs[i].ID)
		if !isValidProviderID(id) {
			continue // 缺失或非法：留到第二遍生成
		}
		key := strings.ToLower(id)
		if used[key] {
			continue // 与前面的 ID 重复：留到第二遍替换
		}
		provs[i].ID = id
		used[key] = true
		settled[i] = true
	}
	seq := 0
	for i := range provs {
		if settled[i] {
			continue
		}
		base := ""
		if name := strings.TrimSpace(provs[i].Name); isValidProviderID(name) {
			base = name // 英文名称自动取同名
		} else {
			seq++
			base = fmt.Sprintf("provider%d", seq)
		}
		provs[i].ID = uniqueProviderID(base, used)
	}
}

// normalizeProviders 对供应商列表做加载期规范化：清理字段、补齐 ID 与启用状态，
// 并强制第一位（默认供应商）处于启用状态。
func normalizeProviders(provs []Provider) {
	for i := range provs {
		provs[i].Name = strings.TrimSpace(provs[i].Name)
		if provs[i].Name == "" {
			provs[i].Name = "未命名供应商"
		}
		if provs[i].Enabled == nil {
			provs[i].Enabled = boolPtr(true)
		}
		provs[i].BaseURL = strings.TrimRight(strings.TrimSpace(provs[i].BaseURL), "/")
		provs[i].AnthropicBaseURL = strings.TrimRight(strings.TrimSpace(provs[i].AnthropicBaseURL), "/")
	}
	normalizeProviderIDs(provs)
	if len(provs) > 0 {
		provs[0].Enabled = boolPtr(true)
	}
}

// providerIDsSignature 生成供应商 ID 序列签名，用于判断保存时是否发生了 ID 自动修正。
func providerIDsSignature(provs []Provider) string {
	var b strings.Builder
	for i := range provs {
		b.WriteString(provs[i].ID)
		b.WriteByte('\n')
	}
	return b.String()
}

// providersSignature 生成供应商身份签名（ID + 启用状态），用于判断加载时是否发生了迁移。
func providersSignature(provs []Provider) string {
	var b strings.Builder
	for i := range provs {
		b.WriteString(provs[i].ID)
		if provs[i].IsEnabled() {
			b.WriteString("|1\n")
		} else {
			b.WriteString("|0\n")
		}
	}
	return b.String()
}

type Config struct {
	APIKey                   string `json:"api_key"` // 保留：向后兼容与全局兜底
	Port                     int    `json:"port"`
	LANAccess                bool   `json:"lan_access"`
	OpenAIUpstream           string `json:"openai_upstream"` // 保留：向后兼容兜底
	AnthropicUpstream        string `json:"anthropic_upstream"` // 保留：向后兼容兜底
	Providers                []Provider `json:"providers"`
	ActiveProvider           string     `json:"active_provider"`
	VerboseLogging           bool   `json:"verbose_logging"`
	Language                 string `json:"language"`
	ThinkingMode             string `json:"thinking_mode"`
	ReasoningEffort          string `json:"reasoning_effort"`
	SystemPromptPlacement    string `json:"system_prompt_placement"`
	ExtraPrompt              string `json:"extra_prompt"`
	ExtraPromptPlacement     string `json:"extra_prompt_placement"`
	MaxTokensMode            string `json:"max_tokens_mode"`
	MaxTokensCustom          int    `json:"max_tokens_custom"`
	AntiLoopEnabled          bool   `json:"anti_loop_enabled"`
	AntiLoopRetryModel       string `json:"antiloop_retry_model"`
	AntiLoopRetryThinking    string `json:"antiloop_retry_thinking"`
	AntiLoopRetryEffort      string `json:"antiloop_retry_effort"`
	AntiLoopCheckTokens      int    `json:"antiloop_check_tokens"`
	DebugMode                bool   `json:"debug_mode"`
	AutoReasoningContent     bool   `json:"auto_reasoning_content"`
	AutoFixEmptyContent      bool   `json:"auto_fix_empty_content"`
	AnalysisEnabled          bool   `json:"analysis_enabled"`
	AnalysisPersistence      bool   `json:"analysis_persistence"`
	AnalysisPersistRawBodies bool   `json:"analysis_persist_raw_bodies"`
	AnalysisRetentionDays    int    `json:"analysis_retention_days"`
	AntiHallucinationEnabled bool   `json:"anti_hallucination_enabled"`
	AntiHallucinationPrompt  string `json:"anti_hallucination_prompt"`
}

func DefaultConfig() Config {
	return Config{
		Port:                     8188,
		LANAccess:                false,
		OpenAIUpstream:           "https://api.deepseek.com",
		AnthropicUpstream:        "https://api.deepseek.com/anthropic",
		Providers: []Provider{
			{ID: "DeepSeek", Name: "DeepSeek", Enabled: boolPtr(true), BaseURL: "https://api.deepseek.com", APIKey: ""},
		},
		ActiveProvider: "DeepSeek",
		VerboseLogging:           true,
		Language:                 "",
		ThinkingMode:             "enabled",
		ReasoningEffort:          "max",
		SystemPromptPlacement:    "first",
		ExtraPromptPlacement:     "last",
		ExtraPrompt:              "# 在你的思考过程（<think>标签内）中，请遵守以下规则：\n- **方向明确时**：果断执行，不反复推翻、不过度谨慎。选定路径后直接推进，除非遇到严重阻碍因素（例如硬性报错，逻辑矛盾，用户明确否定）\n- **方向不明时**：将自我修正循环限制在 2 次尝试以内。如果仍然受阻，立即停止自我推演，直接向用户提问澄清。获取关键信息后再继续，不要自行猜测或罗列平行方案。\n- **核心原则**：思考服务于推进，而非风险防御。用决策代替犹豫，用沟通代替假设。\n- **禁止在思考过程中输出完整 file/函数代码** 如需示意，使用 `// ... 省略 ...` 或 diff 摘要，简短的行内代码引用\n- 进行代码任务时，实际的代码变更必须通过提供的工具执行。不要提前在思考或回答中输出完整的新版代码。\n- 思考的目标是确认逻辑正确，而非展示最终结果。",
		AntiLoopRetryModel:       "deepseek-v4-flash",
		AntiLoopRetryThinking:    "",
		AntiLoopRetryEffort:      "high",
		AntiLoopCheckTokens:      0,
		AutoReasoningContent:     true,
		AutoFixEmptyContent:      false,
		AnalysisEnabled:          true,
		AnalysisPersistence:      true,
		AnalysisPersistRawBodies: true,
		AnalysisRetentionDays:    7,
		AntiHallucinationEnabled: false,
		AntiHallucinationPrompt:  "\n\n[意图校准] 用户现在关心的是：{{latest_user_message}}\n\n",
	}
}

// configFilePath 允许在测试（或特殊场景）中覆盖配置文件位置；为空时使用 exe 同目录默认路径。
var configFilePath string

func configPath() string {
	if configFilePath != "" {
		return configFilePath
	}
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "config.json")
}

// detectSystemLanguage 基于系统环境和 Windows API 自动判断首选语言。
// 仅在首次启动（Language 为空）时使用，后续尊重用户在设置中的选择。
func detectSystemLanguage() string {
	// 优先检查常见环境变量（Unix/macOS/Windows 均可能设置）
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if v := os.Getenv(k); v != "" {
			v = strings.ToLower(strings.Split(v, ".")[0])
			if strings.HasPrefix(v, "zh") {
				return "zh"
			}
			if strings.HasPrefix(v, "en") {
				return "en"
			}
		}
	}

	// 平台相关 UI 语言：Windows 用 kernel32，其它平台交由环境变量判断（见 lang_*.go）
	if lang := platformUILanguage(); lang != "" {
		return lang
	}

	// 本项目主要面向中文用户，默认中文
	return "zh"
}

func LoadConfig() (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			// 首次启动：自动检测并持久化
			cfg.Language = detectSystemLanguage()
			_ = SaveConfig(cfg)
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultConfig(), err
	}
	// 解密落盘的 API Key（旧版明文无 ENC: 前缀则原样保留，下次保存时加密）。
	if cfg.APIKey != "" {
		if dec, err := decryptAPIKey(cfg.APIKey); err == nil {
			cfg.APIKey = dec
		}
		// 解密失败时保留原值，避免崩溃；用户可在设置页重新填写。
	}
	// 解密各供应商的 API Key。
	for i := range cfg.Providers {
		if cfg.Providers[i].APIKey != "" {
			if dec, err := decryptAPIKey(cfg.Providers[i].APIKey); err == nil {
				cfg.Providers[i].APIKey = dec
			}
		}
	}
	// 向后兼容：旧配置没有 providers 时，用原先的 openai_upstream + 全局密钥生成一个默认供应商。
	if len(cfg.Providers) == 0 {
		name := "DeepSeek"
		base := cfg.OpenAIUpstream
		if base == "" {
			base = "https://api.deepseek.com"
		}
		cfg.Providers = []Provider{{ID: name, Name: name, Enabled: boolPtr(true), BaseURL: base, APIKey: cfg.APIKey}}
	}
	// 旧配置迁移：active_provider 指向的供应商若不在首位，上移到首位，
	// 使用户原先选定的供应商继续作为默认供应商（列表顺序即语义）。
	// 仅在尚未迁移（首位供应商还没有 ID）时执行，避免用户拖拽排序后每次启动被旧字段拉回。
	if cfg.Providers[0].ID == "" && cfg.ActiveProvider != "" && cfg.Providers[0].Name != cfg.ActiveProvider {
		for i := 1; i < len(cfg.Providers); i++ {
			if cfg.Providers[i].Name == cfg.ActiveProvider {
				moved := cfg.Providers[i]
				copy(cfg.Providers[1:i+1], cfg.Providers[0:i])
				cfg.Providers[0] = moved
				break
			}
		}
	}
	// 规范化：补齐供应商 ID 与启用状态，并强制第一位（默认供应商）启用。
	before := providersSignature(cfg.Providers)
	normalizeProviders(cfg.Providers)
	needSave := providersSignature(cfg.Providers) != before
	// active_provider 为保留字段：仅与默认供应商（列表第一位）保持同步，不再作为路由依据。
	cfg.ActiveProvider = cfg.Providers[0].Name
	if cfg.Language == "" {
		cfg.Language = detectSystemLanguage()
		needSave = true
	}
	if needSave {
		_ = SaveConfig(cfg)
	}
	return cfg, nil
}

// encryptAPIKeyForStorage 加密待落盘的 API Key。
// 若值本身已是密文（例如解密失败时按原值保留的旧数据），原样返回，避免二次加密后前端只能看到密文。
func encryptAPIKeyForStorage(k string) (string, error) {
	if strings.HasPrefix(k, apiKeyEncPrefix) {
		return k, nil
	}
	return encryptAPIKey(k)
}

func SaveConfig(cfg Config) error {
	// 落盘前对 API Key 与各供应商密钥进行加密混淆，避免以明文存储。
	// 注意：Providers 是切片，saveCfg := cfg 仅做浅拷贝，会与传入 cfg 共享底层数组，
	// 若直接写 saveCfg.Providers[i].APIKey = enc 会把内存中的明文密钥也改成密文，
	// 导致代理向上游发送密文（API Key 失效）、前端小眼睛看到密文。必须先深拷贝切片。
	saveCfg := cfg
	// active_provider 为保留字段：始终与列表第一位（默认供应商）同步，避免旧值残留导致下次加载误迁移。
	if len(saveCfg.Providers) > 0 {
		saveCfg.ActiveProvider = saveCfg.Providers[0].Name
	}
	if saveCfg.APIKey != "" {
		enc, err := encryptAPIKeyForStorage(saveCfg.APIKey)
		if err != nil {
			return err
		}
		saveCfg.APIKey = enc
	}
	saveCfg.Providers = make([]Provider, len(cfg.Providers))
	for i := range cfg.Providers {
		p := cfg.Providers[i]
		if p.APIKey != "" {
			enc, err := encryptAPIKeyForStorage(p.APIKey)
			if err != nil {
				return err
			}
			p.APIKey = enc
		}
		if len(p.Models) > 0 {
			p.Models = append([]ModelConfig(nil), p.Models...)
		}
		saveCfg.Providers[i] = p
	}
	data, err := json.MarshalIndent(saveCfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0600)
}

type SafeConfig struct {
	mu  sync.RWMutex
	cfg Config
}

func NewSafeConfig(cfg Config) *SafeConfig {
	return &SafeConfig{cfg: cfg}
}

func (sc *SafeConfig) Get() Config {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.cfg
}

func (sc *SafeConfig) Update(fn func(*Config)) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	fn(&sc.cfg)
}
