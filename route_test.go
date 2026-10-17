package main

import "testing"

// mkProvider 构造测试用供应商（默认启用）。
func mkProvider(id, name string, models ...string) Provider {
	mcs := make([]ModelConfig, 0, len(models))
	for _, m := range models {
		mcs = append(mcs, ModelConfig{ID: m})
	}
	return Provider{ID: id, Name: name, Enabled: boolPtr(true), BaseURL: "https://" + id + ".example.com", Models: mcs}
}

func TestRouteTableNoConflict(t *testing.T) {
	cfg := Config{Providers: []Provider{
		mkProvider("deepseek", "DeepSeek", "deepseek-chat", "deepseek-reasoner"),
		mkProvider("nvidia", "NVIDIA", "nemotron-70b"),
	}}
	rt := buildRouteTable(cfg)
	for _, id := range []string{"deepseek-chat", "deepseek-reasoner", "nemotron-70b"} {
		e, _, hit := rt.Resolve(id)
		if !hit {
			t.Fatalf("Resolve(%q) 未命中", id)
		}
		if e.PublicModelID != id || e.UpstreamModelID != id {
			t.Fatalf("Resolve(%q) 对外名/上游名应保持原样，得到 %q/%q", id, e.PublicModelID, e.UpstreamModelID)
		}
		if e.Conflicted {
			t.Fatalf("Resolve(%q) 不应标记冲突", id)
		}
	}
	if e, p, hit := rt.Resolve("deepseek-chat"); !hit || p.ID != "deepseek" || e.ProviderID != "deepseek" {
		t.Fatalf("deepseek-chat 应路由到 deepseek，得到 %+v / %s", e, p.ID)
	}
	if e, p, hit := rt.Resolve("nemotron-70b"); !hit || p.ID != "nvidia" || e.ProviderID != "nvidia" {
		t.Fatalf("nemotron-70b 应路由到 nvidia，得到 %+v / %s", e, p.ID)
	}
	if c, u := rt.Stats(); c != 0 || u != 0 {
		t.Fatalf("无冲突时统计应为 0/0，得到 %d/%d", c, u)
	}
}

func TestRouteTableConflictAddsPrefix(t *testing.T) {
	cfg := Config{Providers: []Provider{
		mkProvider("deepseek", "DeepSeek", "chat", "deepseek-chat"),
		mkProvider("siliconflow", "SiliconFlow", "chat"),
	}}
	rt := buildRouteTable(cfg)

	e, p, hit := rt.Resolve("deepseek/chat")
	if !hit || p.ID != "deepseek" || e.UpstreamModelID != "chat" || !e.Conflicted {
		t.Fatalf("deepseek/chat 应命中 deepseek 的 chat 并剥离前缀，得到 %+v / %s / %v", e, p.ID, hit)
	}
	e, p, hit = rt.Resolve("siliconflow/chat")
	if !hit || p.ID != "siliconflow" || e.UpstreamModelID != "chat" {
		t.Fatalf("siliconflow/chat 应命中 siliconflow 的 chat，得到 %+v / %s / %v", e, p.ID, hit)
	}
	// 未冲突的模型对外名保持原样
	e, p, hit = rt.Resolve("deepseek-chat")
	if !hit || p.ID != "deepseek" || e.Conflicted {
		t.Fatalf("deepseek-chat 应原样命中 deepseek，得到 %+v / %s / %v", e, p.ID, hit)
	}
	if c, u := rt.Stats(); c != 2 || u != 0 {
		t.Fatalf("冲突统计应为 2/0，得到 %d/%d", c, u)
	}
}

func TestRouteTableThreeWayConflict(t *testing.T) {
	cfg := Config{Providers: []Provider{
		mkProvider("A", "甲", "chat"),
		mkProvider("B", "乙", "chat"),
		mkProvider("C", "丙", "chat"),
	}}
	rt := buildRouteTable(cfg)
	for _, id := range []string{"A", "B", "C"} {
		e, p, hit := rt.Resolve(id + "/chat")
		if !hit || p.ID != id || e.UpstreamModelID != "chat" {
			t.Fatalf("%s/chat 应命中 %s，得到 %+v / %s / %v", id, id, e, p.ID, hit)
		}
	}
	if _, _, hit := rt.Resolve("chat"); hit {
		t.Fatal("三方冲突时裸模型名 chat 不应命中任何供应商")
	}
	if c, u := rt.Stats(); c != 3 || u != 0 {
		t.Fatalf("冲突统计应为 3/0，得到 %d/%d", c, u)
	}
}

func TestRouteTableUnroutableKeepsFirst(t *testing.T) {
	cfg := Config{Providers: []Provider{
		mkProvider("S1", "一", "chat"),
		mkProvider("S2", "二", "chat", "S2/chat"),
	}}
	rt := buildRouteTable(cfg)

	// "S2/chat" 由 S2 的冲突模型加前缀抢占，S2 原生的 "S2/chat" 被遮蔽
	e, p, hit := rt.Resolve("S2/chat")
	if !hit || p.ID != "S2" || e.UpstreamModelID != "chat" {
		t.Fatalf("S2/chat 应命中 S2 的 chat 模型，得到 %+v / %s / %v", e, p.ID, hit)
	}
	unroutable := 0
	for _, item := range rt.ListModels() {
		if !item.Unroutable {
			continue
		}
		unroutable++
		if item.ProviderID != "S2" || item.UpstreamModelID != "S2/chat" {
			t.Fatalf("被遮蔽项应为 S2 的原生 S2/chat 模型，得到 %+v", item)
		}
	}
	if unroutable != 1 {
		t.Fatalf("应有 1 项被遮蔽，得到 %d", unroutable)
	}
	if c, u := rt.Stats(); c != 2 || u != 1 {
		t.Fatalf("统计应为 冲突2/遮蔽1，得到 %d/%d", c, u)
	}
}

func TestRouteTablePassthroughDefault(t *testing.T) {
	cfg := Config{Providers: []Provider{
		{ID: "gw", Name: "网关", Enabled: boolPtr(true), BaseURL: "https://gw.example.com"},
		mkProvider("other", "其它", "some-model"),
	}}
	rt := buildRouteTable(cfg)

	e, p, hit := rt.Resolve("any-unknown-model")
	if hit {
		t.Fatal("未命中时 hit 应为 false")
	}
	if p.ID != "gw" || e.UpstreamModelID != "any-unknown-model" {
		t.Fatalf("未命中应回退默认供应商并原样透传，得到 %+v / %s", e, p.ID)
	}
	if e, p, hit := rt.Resolve("some-model"); !hit || p.ID != "other" || e.UpstreamModelID != "some-model" {
		t.Fatalf("some-model 应命中 other，得到 %+v / %s / %v", e, p.ID, hit)
	}
}

func TestRouteTableDisabledProviderExcluded(t *testing.T) {
	disabled := mkProvider("A", "甲", "chat")
	disabled.Enabled = boolPtr(false)
	cfg := Config{Providers: []Provider{
		mkProvider("B", "乙", "chat"),
		disabled,
	}}
	rt := buildRouteTable(cfg)

	if e, p, hit := rt.Resolve("chat"); !hit || p.ID != "B" || e.Conflicted {
		t.Fatalf("未启用供应商不参与冲突判定，chat 应原样命中 B，得到 %+v / %s / %v", e, p.ID, hit)
	}
	if _, _, hit := rt.Resolve("A/chat"); hit {
		t.Fatal("未启用供应商的模型不应可路由")
	}
}

func TestRouteTableModelIDWithSlash(t *testing.T) {
	cfg := Config{Providers: []Provider{
		mkProvider("P1", "一", "deepseek/deepseek-chat"),
	}}
	rt := buildRouteTable(cfg)

	e, p, hit := rt.Resolve("deepseek/deepseek-chat")
	if !hit || p.ID != "P1" || e.UpstreamModelID != "deepseek/deepseek-chat" || e.Conflicted {
		t.Fatalf("含 / 的模型名应原样命中，得到 %+v / %s / %v", e, p.ID, hit)
	}
	// 带供应商前缀调用时应剥离前缀，而非被含 "/" 的裸名抢占
	e, p, hit = rt.Resolve("P1/deepseek/deepseek-chat")
	if !hit || p.ID != "P1" || e.UpstreamModelID != "deepseek/deepseek-chat" {
		t.Fatalf("带供应商前缀的调用应能剥离前缀，得到 %+v / %s / %v", e, p.ID, hit)
	}
	// 前缀比较不区分大小写
	if _, p, hit := rt.Resolve("p1/deepseek/deepseek-chat"); !hit || p.ID != "P1" {
		t.Fatalf("前缀应不区分大小写，得到 %s / %v", p.ID, hit)
	}
}

func TestNormalizeProviderIDs(t *testing.T) {
	provs := []Provider{
		{Name: "DeepSeek"}, // 英文名 → 同名
		{Name: "深度求索"},     // 非英文 → provider1
		{Name: "deepseek"}, // 与第一个重名（不区分大小写）→ 加序号
		{ID: "bad id!"},    // 非法 ID → 重新生成
		{ID: "SILICON"},    // 合法且唯一 → 保留
		{ID: "silicon"},    // 与上一个大小写重复 → 重新生成
	}
	normalizeProviderIDs(provs)

	want := []string{"DeepSeek", "provider1", "deepseek2", "provider2", "SILICON", "provider3"}
	for i, w := range want {
		if provs[i].ID != w {
			t.Fatalf("第 %d 个供应商 ID 应为 %q，得到 %q", i+1, w, provs[i].ID)
		}
	}
	// 唯一性（不区分大小写）
	seen := map[string]bool{}
	for _, p := range provs {
		key := p.ID
		if seen[key] {
			t.Fatalf("供应商 ID 重复：%q", key)
		}
		seen[key] = true
	}
}

func TestNormalizeProviders(t *testing.T) {
	provs := []Provider{
		{Name: "甲", BaseURL: "https://a.example.com/"},
		{Name: "乙", Enabled: boolPtr(false), BaseURL: "https://b.example.com/"},
	}
	normalizeProviders(provs)

	if !provs[0].IsEnabled() {
		t.Fatal("第一位（默认供应商）必须启用")
	}
	if provs[1].IsEnabled() {
		t.Fatal("显式关闭的供应商应保持未启用")
	}
	if provs[0].BaseURL != "https://a.example.com" {
		t.Fatalf("BaseURL 应去掉尾部斜杠，得到 %q", provs[0].BaseURL)
	}
	if provs[0].ID != "provider1" || provs[1].ID != "provider2" {
		t.Fatalf("非英文名称应按 providerN 生成 ID，得到 %q / %q", provs[0].ID, provs[1].ID)
	}
}

func TestProviderEnabledNilDefaultsTrue(t *testing.T) {
	p := Provider{Name: "x"}
	if !p.IsEnabled() {
		t.Fatal("Enabled 为 nil（旧配置未设置）时应视为启用")
	}
	p.Enabled = boolPtr(false)
	if p.IsEnabled() {
		t.Fatal("Enabled 显式 false 时应视为未启用")
	}
}
