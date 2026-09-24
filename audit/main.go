// 命令 audit 是一个独立的自检脚本：读取「最近一次」代理请求的实际生效记录
// （test/request_audit.jsonl，由代理在每次主请求时写入），与当前设置页面配置
// （config.json）逐项核对，报告两者是否一致。
//
// 用法：
//   go run ./audit [--config=config.json] [--audit=test/request_audit.jsonl]
//
// 退出码：全部一致为 0；存在不一致为 1；无审计记录或读取失败为 2。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// cfgSnapshot 仅需读取设置页面里与请求转换相关的字段（API Key 等加密字段忽略）。
type cfgSnapshot struct {
	SystemPromptPlacement string `json:"system_prompt_placement"`
	ExtraPrompt           string `json:"extra_prompt"`
	ExtraPromptPlacement  string `json:"extra_prompt_placement"`
	ThinkingMode          string `json:"thinking_mode"`
	ReasoningEffort       string `json:"reasoning_effort"`
	MaxTokensMode         string `json:"max_tokens_mode"`
	MaxTokensCustom       int    `json:"max_tokens_custom"`
}

// auditRecord 对应 logger.RequestAudit 的 JSON 结构（只取核对所需字段）。
type auditRecord struct {
	Time                        string `json:"time"`
	Format                      string `json:"format"`
	CfgSystemPromptPlacement    string `json:"cfg_system_prompt_placement"`
	CfgExtraPromptPlacement     string `json:"cfg_extra_prompt_placement"`
	CfgExtraPromptEmpty         bool   `json:"cfg_extra_prompt_empty"`
	CfgThinkingMode             string `json:"cfg_thinking_mode"`
	CfgReasoningEffort          string `json:"cfg_reasoning_effort"`
	CfgMaxTokensMode            string `json:"cfg_max_tokens_mode"`
	ActualHasStandaloneSystem   bool   `json:"actual_has_standalone_system"`
	ActualHasSystemPromptTag    bool   `json:"actual_has_system_prompt_tag"`
	ActualHasSupremeInstruction bool   `json:"actual_has_supreme_instruction"`
	ActualThinkingType          string `json:"actual_thinking_type"`
	ActualReasoningEffort       string `json:"actual_reasoning_effort"`
	ActualMaxTokens             int    `json:"actual_max_tokens"`
}

type result struct {
	name   string
	pass   bool
	detail string
}

func main() {
	configPath := flag.String("config", "config.json", "设置页面配置文件路径")
	auditPath := flag.String("audit", "test/request_audit.jsonl", "请求审计日志路径")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Printf("[ERROR] 读取配置失败: %v\n", err)
		os.Exit(2)
	}

	rec, err := loadLatestAudit(*auditPath)
	if err != nil {
		fmt.Printf("[ERROR] 读取最近一次审计记录失败: %v\n", err)
		os.Exit(2)
	}
	if rec == nil {
		fmt.Printf("[ERROR] 审计日志为空，未找到任何请求记录。请先通过代理发起一次请求。\n")
		os.Exit(2)
	}

	fmt.Printf("=== DSPlus 请求一致性自检 ===\n")
	fmt.Printf("最近一次请求时间 : %s\n", rec.Time)
	fmt.Printf("请求格式         : %s\n", rec.Format)
	fmt.Printf("（审计记录中的配置快照：system_placement=%s extra_placement=%s thinking=%s effort=%s maxtokens=%s）\n\n",
		rec.CfgSystemPromptPlacement, rec.CfgExtraPromptPlacement, rec.CfgThinkingMode, rec.CfgReasoningEffort, rec.CfgMaxTokensMode)

	var results []result

	// 1. 系统提示词重组
	results = append(results, checkSystemPrompt(cfg, rec))
	// 2. 额外提示词注入
	results = append(results, checkExtraPrompt(cfg, rec))
	// 3. 思考模式 / 强度
	results = append(results, checkThinking(cfg, rec))
	// 4. 最大 tokens
	results = append(results, checkMaxTokens(cfg, rec))

	allPass := true
	for _, r := range results {
		mark := "PASS"
		if !r.pass {
			mark = "FAIL"
			allPass = false
		}
		fmt.Printf("[%s] %s\n       %s\n", mark, r.name, r.detail)
	}

	fmt.Println()
	if allPass {
		fmt.Println("结论：最近一次实际请求 与 设置页面配置 完全一致。")
		os.Exit(0)
	}
	fmt.Println("结论：存在不一致项，请检查上方 FAIL 明细。")
	os.Exit(1)
}

func loadConfig(path string) (cfgSnapshot, error) {
	var c cfgSnapshot
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

func loadLatestAudit(path string) (*auditRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var rec auditRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		return &rec, nil
	}
	return nil, nil
}

func normPlacement(p string) string {
	if p == "" {
		return "none"
	}
	return p
}

func checkSystemPrompt(cfg cfgSnapshot, rec *auditRecord) result {
	placement := normPlacement(cfg.SystemPromptPlacement)
	switch placement {
	case "none":
		pass := rec.ActualHasStandaloneSystem && !rec.ActualHasSystemPromptTag
		detail := fmt.Sprintf("配置 placement=none（系统提示词不动）：期望保留独立 system=%v 且不出现 <system_prompt> 标签=%v；实际 独立system=%v 标签=%v",
			true, false, rec.ActualHasStandaloneSystem, rec.ActualHasSystemPromptTag)
		return result{"系统提示词重组 (placement=none)", pass, detail}
	default: // first / last
		// OpenAI 重组后会移除独立 system 消息；Anthropic 会删除顶层 system 字段。
		// 两者都应出现 <system_prompt> 标签，且不应再残留独立 system。
		pass := rec.ActualHasSystemPromptTag && !rec.ActualHasStandaloneSystem
		detail := fmt.Sprintf("配置 placement=%s（重组到用户消息）：期望出现 <system_prompt> 标签=%v 且不再残留独立 system=%v；实际 标签=%v 独立system=%v",
			placement, true, false, rec.ActualHasSystemPromptTag, rec.ActualHasStandaloneSystem)
		return result{"系统提示词重组 (placement=" + placement + ")", pass, detail}
	}
}

func checkExtraPrompt(cfg cfgSnapshot, rec *auditRecord) result {
	placement := normPlacement(cfg.ExtraPromptPlacement)
	expectInjected := placement != "none" && cfg.ExtraPrompt != ""
	if expectInjected {
		pass := rec.ActualHasSupremeInstruction
		detail := fmt.Sprintf("配置 extra_placement=%s 且 extra_prompt 非空：期望出现 <supreme_instruction> 标签=%v；实际 标签=%v",
			placement, true, rec.ActualHasSupremeInstruction)
		return result{"额外提示词注入", pass, detail}
	}
	pass := !rec.ActualHasSupremeInstruction
	reason := "extra_placement=none"
	if cfg.ExtraPrompt == "" {
		reason = "extra_prompt 为空"
	}
	detail := fmt.Sprintf("配置 %s：期望不出现 <supreme_instruction> 标签=%v；实际 标签=%v",
		reason, false, rec.ActualHasSupremeInstruction)
	return result{"额外提示词注入", pass, detail}
}

func checkThinking(cfg cfgSnapshot, rec *auditRecord) result {
	switch cfg.ThinkingMode {
	case "enabled":
		pass := rec.ActualThinkingType == "enabled"
		detail := fmt.Sprintf("配置 thinking=enabled：期望 thinking.type=enabled=%v；实际 type=%q",
			true, rec.ActualThinkingType)
		if pass && cfg.ReasoningEffort != "" {
			effPass := rec.ActualReasoningEffort == cfg.ReasoningEffort
			detail += fmt.Sprintf("；期望 reasoning_effort=%q=%v；实际 %q",
				cfg.ReasoningEffort, effPass, rec.ActualReasoningEffort)
			pass = pass && effPass
		}
		return result{"思考模式/强度", pass, detail}
	case "disabled":
		pass := rec.ActualThinkingType == "disabled" && (rec.ActualReasoningEffort == "" || rec.ActualReasoningEffort == "none")
		detail := fmt.Sprintf("配置 thinking=disabled：期望 thinking.type=disabled=%v 且 reasoning_effort 清除或为 none=%v；实际 type=%q effort=%q",
			true, true, rec.ActualThinkingType, rec.ActualReasoningEffort)
		return result{"思考模式/强度", pass, detail}
	default:
		detail := fmt.Sprintf("配置 thinking 为空（不强制）：跳过思考参数核对（实际 type=%q effort=%q）",
			rec.ActualThinkingType, rec.ActualReasoningEffort)
		return result{"思考模式/强度 (未强制)", true, detail}
	}
}

func checkMaxTokens(cfg cfgSnapshot, rec *auditRecord) result {
	switch cfg.MaxTokensMode {
	case "off":
		// 不发送：强制删除 max_tokens，实际应为 0（不存在）。
		pass := rec.ActualMaxTokens == 0
		detail := fmt.Sprintf("配置 max_tokens=不发送：期望实际不含 max_tokens（值=0）=%v；实际 %d",
			true, rec.ActualMaxTokens)
		return result{"最大 tokens（不发送）", pass, detail}
	case "5000", "32000", "384000":
		want := 5000
		switch cfg.MaxTokensMode {
		case "32000":
			want = 32000
		case "384000":
			want = 384000
		}
		pass := rec.ActualMaxTokens == want
		detail := fmt.Sprintf("配置 max_tokens=%d：期望 actual_max_tokens=%d=%v；实际 %d",
			want, want, pass, rec.ActualMaxTokens)
		return result{"最大 tokens", pass, detail}
	case "custom":
		want := cfg.MaxTokensCustom
		pass := rec.ActualMaxTokens == want
		detail := fmt.Sprintf("配置 max_tokens=custom(%d)：期望 actual_max_tokens=%d=%v；实际 %d",
			want, want, pass, rec.ActualMaxTokens)
		return result{"最大 tokens", pass, detail}
	default:
		detail := fmt.Sprintf("配置 max_tokens 为空（不强制）：跳过核对（实际 %d）", rec.ActualMaxTokens)
		return result{"最大 tokens (未强制)", true, detail}
	}
}
