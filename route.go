package main

import (
	"sort"
	"strings"
)

// RouteEntry 描述一个对外模型与上游供应商之间的映射关系。
type RouteEntry struct {
	PublicModelID   string // 对外模型名：无冲突时为原 ID，跨供应商重名时为 "供应商ID/模型ID"
	UpstreamModelID string // 转发给上游时使用的真实模型名（已剥离前缀）
	Group           string // 模型分组（用于 /v1/models 的 owned_by 回显）
	ProviderIndex   int    // 目标供应商在 Providers 中的下标
	ProviderID      string
	ProviderName    string
	Conflicted      bool // 该模型 ID 跨供应商重名（对外名已加前缀）
	Unroutable      bool // 加前缀后仍与他人冲突，被排序靠前的供应商遮蔽，不可路由
}

// RouteTable 是全局模型路由表，由配置快照构建，本身不持有可变状态。
type RouteTable struct {
	providers  []Provider          // 配置中的供应商快照（按数组顺序，第一位为默认供应商）
	entries    []RouteEntry        // 全部对外模型条目（含不可路由项，供界面展示）
	byName     map[string]int      // 对外模型名 → entries 下标（仅可路由项）
	defaultIdx int                 // 默认供应商下标（列表第一位）；-1 表示配置中没有任何供应商
}

// providerInRoute 判断供应商是否参与路由表：必须处于启用状态且配置了模型清单。
// 模型清单为空的默认供应商（第一位）不贡献具体模型条目，但仍是路由未命中时的兜底目标。
func providerInRoute(p Provider) bool {
	return p.IsEnabled() && len(p.Models) > 0
}

// buildRouteTable 根据配置快照构建路由表（纯函数）。
// 规则：
//  1. 只统计「已启用 且 配置了模型清单」的供应商；同一供应商内的重复模型 ID 只计一次。
//  2. 模型 ID 跨供应商唯一 → 对外名保持原样；出现重复 → 所有重复项加前缀，对外名 = "供应商ID/模型ID"。
//  3. 加前缀后仍与他人重名 → 保留排序靠前的供应商所提供者，其余标记为不可路由。
func buildRouteTable(cfg Config) RouteTable {
	t := RouteTable{defaultIdx: -1, byName: make(map[string]int)}
	t.providers = append([]Provider(nil), cfg.Providers...)
	for i := range t.providers {
		if len(t.providers[i].Models) > 0 {
			t.providers[i].Models = append([]ModelConfig(nil), t.providers[i].Models...)
		}
	}
	if len(t.providers) > 0 {
		t.defaultIdx = 0
	}

	// 第一遍：统计每个模型 ID 被多少个供应商提供，用于判断是否需要加前缀。
	counts := make(map[string]int, 16)
	for i := range t.providers {
		if !providerInRoute(t.providers[i]) {
			continue
		}
		seen := make(map[string]bool, len(t.providers[i].Models))
		for _, m := range t.providers[i].Models {
			id := strings.TrimSpace(m.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			counts[id]++
		}
	}

	// 第二遍：按供应商顺序生成条目，先到先得地占用对外模型名。
	usedPublic := make(map[string]int, 16)
	for i := range t.providers {
		if !providerInRoute(t.providers[i]) {
			continue
		}
		seen := make(map[string]bool, len(t.providers[i].Models))
		for _, m := range t.providers[i].Models {
			id := strings.TrimSpace(m.ID)
			if id == "" || seen[id] {
				continue // 空 ID 或同一供应商内重复：后者不具备独立的外对模型身份
			}
			seen[id] = true
			conflicted := counts[id] > 1
			public := id
			if conflicted {
				public = t.providers[i].ID + "/" + id
			}
			e := RouteEntry{
				PublicModelID:   public,
				UpstreamModelID: id,
				Group:           m.Group,
				ProviderIndex:   i,
				ProviderID:      t.providers[i].ID,
				ProviderName:    t.providers[i].Name,
				Conflicted:      conflicted,
			}
			if _, taken := usedPublic[public]; taken {
				e.Unroutable = true
			}
			t.entries = append(t.entries, e)
			idx := len(t.entries) - 1
			if !e.Unroutable {
				usedPublic[public] = idx
				t.byName[public] = idx
			}
		}
	}
	return t
}

// DefaultProvider 返回默认供应商（列表第一位）及其下标。
func (t RouteTable) DefaultProvider() (Provider, int, bool) {
	if t.defaultIdx < 0 || t.defaultIdx >= len(t.providers) {
		return Provider{}, -1, false
	}
	return t.providers[t.defaultIdx], t.defaultIdx, true
}

// Resolve 解析请求中的模型名，返回目标供应商与转发时使用的真实模型名。
// 匹配顺序：对外模型名精确匹配 → 按供应商 ID 前缀剥离后匹配 → 未命中则回退默认供应商并原样透传。
// 返回的 hit 为 false 表示走了默认供应商兜底。
func (t RouteTable) Resolve(model string) (RouteEntry, Provider, bool) {
	model = strings.TrimSpace(model)
	if model != "" {
		if idx, ok := t.byName[model]; ok {
			e := t.entries[idx]
			return e, t.providers[e.ProviderIndex], true
		}
		if e, p, ok := t.resolvePrefixed(model); ok {
			return e, p, true
		}
	}
	if p, idx, ok := t.DefaultProvider(); ok {
		return RouteEntry{
			PublicModelID:   model,
			UpstreamModelID: model,
			ProviderIndex:   idx,
			ProviderID:      p.ID,
			ProviderName:    p.Name,
		}, p, false
	}
	return RouteEntry{PublicModelID: model, UpstreamModelID: model}, Provider{}, false
}

// resolvePrefixed 处理 "供应商ID/模型ID" 形式的模型名：供应商 ID 前缀按长度降序尝试（避免短 ID 抢占长 ID），
// 前缀比较不区分大小写；模型 ID 本身可能含 "/"，因此剩余部分整体作为模型名再在目标供应商内查找。
func (t RouteTable) resolvePrefixed(model string) (RouteEntry, Provider, bool) {
	ids := make([]string, 0, len(t.providers))
	for i := range t.providers {
		if providerInRoute(t.providers[i]) {
			ids = append(ids, t.providers[i].ID)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return len(ids[a]) > len(ids[b]) })

	for _, id := range ids {
		if len(model) <= len(id)+1 || model[len(id)] != '/' {
			continue
		}
		if !strings.EqualFold(model[:len(id)], id) {
			continue
		}
		rest := model[len(id)+1:]
		for idx := range t.entries {
			e := t.entries[idx]
			if e.Unroutable || e.ProviderID != id || e.UpstreamModelID != rest {
				continue
			}
			return e, t.providers[e.ProviderIndex], true
		}
	}
	return RouteEntry{}, Provider{}, false
}

// ListModels 返回全部对外模型条目（含不可路由项，供界面与接口展示），顺序与供应商列表一致。
func (t RouteTable) ListModels() []RouteEntry {
	out := make([]RouteEntry, len(t.entries))
	copy(out, t.entries)
	return out
}

// HasModels 判断路由表中是否存在任何可路由的模型条目。
func (t RouteTable) HasModels() bool {
	return len(t.byName) > 0
}

// Stats 汇总路由表状态：conflicted 为跨供应商重名（已加前缀）的模型条目数，
// unroutable 为加前缀后仍冲突、被遮蔽而无法路由的模型条目数。
func (t RouteTable) Stats() (conflicted, unroutable int) {
	for _, e := range t.entries {
		if e.Conflicted {
			conflicted++
		}
		if e.Unroutable {
			unroutable++
		}
	}
	return conflicted, unroutable
}
