package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ThinkingTestResult struct {
	Mode            string
	Effort          string
	StatusCode      int
	Success         bool
	ErrorMsg        string
	ReasoningTokens int
	OutputTokens    int
	TotalTokens     int
	Cost            string
	ReasoningBrief  string
	AnswerBrief     string
	DurationMs      int64
}

func main() {
	baseURL := "http://127.0.0.1:8188"
	testPrompt := "我打算洗车，洗车店距离我有50米，我应该步行过去还是开车过去"

	type TestCase struct {
		Mode   string
		Effort string
		Label  string
	}

	testCases := []TestCase{
		{Mode: "disabled", Effort: "none", Label: "强制关闭思考 (disabled)"},
		{Mode: "enabled", Effort: "minimal", Label: "极简推理 (minimal)"},
		{Mode: "enabled", Effort: "low", Label: "低强度推理 (low)"},
		{Mode: "enabled", Effort: "medium", Label: "中等推理 (medium)"},
		{Mode: "enabled", Effort: "high", Label: "标准推理 (high)"},
		{Mode: "enabled", Effort: "xhigh", Label: "超强推理 (xhigh)"},
		{Mode: "enabled", Effort: "max", Label: "最强推理 (max)"},
		{Mode: "enabled", Effort: "ultra", Label: "极限推理 (ultra)"},
	}

	fmt.Println("==================================================================================")
	fmt.Printf("开始思考档位自动化对比测试\n测试问题: %s\n", testPrompt)
	fmt.Println("==================================================================================")

	var results []ThinkingTestResult

	client := &http.Client{Timeout: 90 * time.Second}

	for _, tc := range testCases {
		fmt.Printf("\n--> 测试档位: %s (%s, %s)...\n", tc.Label, tc.Mode, tc.Effort)

		// 1. 更新 DSPlus 配置
		cfgPayload := map[string]interface{}{
			"thinking_mode":    tc.Mode,
			"reasoning_effort": tc.Effort,
		}
		cfgBytes, _ := json.Marshal(cfgPayload)
		reqCfg, _ := http.NewRequest("POST", baseURL+"/api/config", bytes.NewReader(cfgBytes))
		reqCfg.Header.Set("Content-Type", "application/json")
		respCfg, err := client.Do(reqCfg)
		if err != nil {
			fmt.Printf("    [!] 配置更新失败: %v\n", err)
		} else {
			respCfg.Body.Close()
		}

		time.Sleep(100 * time.Millisecond)

		// 2. 发起对话请求
		startTime := time.Now()
		reqBody := map[string]interface{}{
			"model": "cline-pass/deepseek-v4.1-flash",
			"messages": []map[string]string{
				{"role": "user", "content": testPrompt},
			},
			"stream": false,
		}
		bodyBytes, _ := json.Marshal(reqBody)

		req, err := http.NewRequest("POST", baseURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
		if err != nil {
			results = append(results, ThinkingTestResult{
				Mode: tc.Mode, Effort: tc.Effort, Success: false, ErrorMsg: err.Error(),
			})
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		duration := time.Since(startTime).Milliseconds()

		if err != nil {
			results = append(results, ThinkingTestResult{
				Mode: tc.Mode, Effort: tc.Effort, Success: false, ErrorMsg: err.Error(), DurationMs: duration,
			})
			fmt.Printf("    [X] 请求报错: %v\n", err)
			continue
		}

		respBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		res := ThinkingTestResult{
			Mode:       tc.Mode,
			Effort:     tc.Effort,
			StatusCode: resp.StatusCode,
			DurationMs: duration,
		}

		if resp.StatusCode != 200 {
			res.Success = false
			res.ErrorMsg = string(respBytes)
			fmt.Printf("    [X] 响应失败 (HTTP %d): %s\n", resp.StatusCode, string(respBytes))
			results = append(results, res)
			continue
		}

		var respJSON map[string]interface{}
		if err := json.Unmarshal(respBytes, &respJSON); err != nil {
			res.Success = false
			res.ErrorMsg = "JSON 解析失败"
			results = append(results, res)
			continue
		}

		res.Success = true

		// 兼容可能存在的 data 外层包裹
		targetMap := respJSON
		if dataMap, ok := respJSON["data"].(map[string]interface{}); ok {
			targetMap = dataMap
		}

		// 提取 usage
		if usage, ok := targetMap["usage"].(map[string]interface{}); ok {
			if dt, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
				if rt, ok := dt["reasoning_tokens"].(float64); ok {
					res.ReasoningTokens = int(rt)
				}
			}
			if ct, ok := usage["completion_tokens"].(float64); ok {
				res.OutputTokens = int(ct)
			}
			if tt, ok := usage["total_tokens"].(float64); ok {
				res.TotalTokens = int(tt)
			}
			if cost, ok := usage["cost"].(float64); ok {
				res.Cost = fmt.Sprintf("$%.6f", cost)
			}
		}

		// 提取 choices
		if choices, ok := targetMap["choices"].([]interface{}); ok && len(choices) > 0 {
			if c0, ok := choices[0].(map[string]interface{}); ok {
				if msg, ok := c0["message"].(map[string]interface{}); ok {
					if content, ok := msg["content"].(string); ok {
						res.AnswerBrief = strings.ReplaceAll(content, "\n", " ")
						if len([]rune(res.AnswerBrief)) > 60 {
							res.AnswerBrief = string([]rune(res.AnswerBrief)[:60]) + "..."
						}
					}
					if reasoning, ok := msg["reasoning"].(string); ok && reasoning != "" {
						res.ReasoningBrief = strings.ReplaceAll(reasoning, "\n", " ")
						if len([]rune(res.ReasoningBrief)) > 50 {
							res.ReasoningBrief = string([]rune(res.ReasoningBrief)[:50]) + "..."
						}
					} else if reasoningContent, ok := msg["reasoning_content"].(string); ok && reasoningContent != "" {
						res.ReasoningBrief = strings.ReplaceAll(reasoningContent, "\n", " ")
						if len([]rune(res.ReasoningBrief)) > 50 {
							res.ReasoningBrief = string([]rune(res.ReasoningBrief)[:50]) + "..."
						}
					}
				}
			}
		}

		fmt.Printf("    [√] 成功! 耗时: %dms | 思考Tokens: %d | 输出Tokens: %d | 总Tokens: %d\n",
			res.DurationMs, res.ReasoningTokens, res.OutputTokens, res.TotalTokens)
		if res.ReasoningBrief != "" {
			fmt.Printf("        思考摘要: %s\n", res.ReasoningBrief)
		} else {
			fmt.Printf("        思考摘要: (无思考内容 - 思考已关闭)\n")
		}
		fmt.Printf("        回答摘要: %s\n", res.AnswerBrief)

		results = append(results, res)
		time.Sleep(500 * time.Millisecond)
	}

	// 打印汇总报告
	fmt.Println("\n==================================================================================")
	fmt.Println("全档位思考模式对比测试结果汇总")
	fmt.Println("==================================================================================")
	fmt.Printf("%-18s | %-8s | %-12s | %-12s | %-10s | %-8s\n", "档位", "HTTP状态", "思考Tokens", "输出Tokens", "耗时(ms)", "调用结果")
	fmt.Println("-------------------+----------+--------------+--------------+------------+----------")
	for _, r := range results {
		statusStr := fmt.Sprintf("%d", r.StatusCode)
		resStr := "成功"
		if !r.Success {
			resStr = "失败"
		}
		tag := r.Effort
		if r.Mode == "disabled" {
			tag = "disabled"
		}
		fmt.Printf("%-18s | %-8s | %-12d | %-12d | %-10d | %-8s\n",
			tag, statusStr, r.ReasoningTokens, r.OutputTokens, r.DurationMs, resStr)
	}
	fmt.Println("==================================================================================")
}
