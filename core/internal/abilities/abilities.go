// Package abilities：**能力身份（枚举）** —— 能力的 id 写死在代码里，开关与覆盖写在
// `agents.json` 每个 agent 的 `abilities` 上。
//
// 三条口径（见 `DEFINE.md`「能力（Ability）」与 `AGENTS.md`「后台任务 / 压缩 / 能力」）：
//
//  1. **流程在代码里，开关在 Agent 上**："能力"= 会调模型的那种作业（`title` / `compact` / `judge`），
//     能覆盖的只有模板 / 渠道 / 模型 —— **不能造新的**（表里没有的 id 写了也不认，报错）；
//  2. **缺字段 = 默认全开**（现有 `agents.json` 一个字都不用改）；`enabled: false` ⇒
//     调用方**明确拒绝**执行那一次，**不是静默降级**；
//  3. **一个函数搞定**：`Resolve(agent, id)` 是"这份人格在这个能力上怎么配"的**唯一**解析处 ——
//     别在调用点各读一遍 map（那会让"默认全开"这条口径散成好几份）。
//
// `turn` **不是能力** ⇒ 不在这个枚举里（它是 Agent 存在的方式，关掉就没得聊了）；
// `refresh_models` 也不是（它不调模型）。
package abilities

import (
	"fmt"
	"sort"
	"strings"

	"microchat/internal/config"
)

// ID：能力 id。**只此三个** —— 加一个 = 同时加一段流程（在代码里），不是加一个配置项。
type ID = string

const (
	// Title：给会话起标题（一段 → `sessions.title`）。
	Title ID = "title"
	// Compact：把一段压成摘要（一段 → `summaries` 一行，落库由调用方单事务负责）。
	Compact ID = "compact"
	// Judge：JEV 决策（原始文本 + 提示词 + 状态表 → 概率表）。**流程还没写**，但它是能力。
	Judge ID = "judge"
)

// IDs：全部能力 id（顺序固定 ⇒ 界面与 `-debug abilities` 的输出稳定）。
func IDs() []ID { return []ID{Title, Compact, Judge} }

// Valid：这是个认识的能力 id 吗。
func Valid(id string) bool {
	for _, known := range IDs() {
		if known == id {
			return true
		}
	}
	return false
}

// Setting：一个能力在这份人格上的**生效**配置（覆盖为空 = 用调用方自己的）。
//
// `Provider` / `Model` 空 ⇒ 用**会话的**渠道与模型（辅助调用本来就要骑同一个会话）；
// `Prompt` 空 ⇒ 用代码里的默认模板。
type Setting struct {
	Enabled  bool
	Provider string
	Model    string
	Prompt   string
}

// Resolve：**唯一**的解析处 —— 缺条目 = 默认全开、无覆盖。
//
// 未知的 id 也照这个规则解析（回默认全开）：**认不认识 id 是 `Validate` 的事**
// （写配置时要报错；读配置时不能因为一个不认识的键就把整份人格判死）。
func Resolve(agent config.Agent, id ID) Setting {
	toggle, ok := agent.Abilities[id]
	if !ok {
		return Setting{Enabled: true}
	}
	setting := Setting{
		Enabled:  toggle.Enabled == nil || *toggle.Enabled,
		Provider: trim(toggle.Provider),
		Model:    trim(toggle.Model),
		Prompt:   strings.TrimSpace(deref(toggle.Prompt)),
	}
	return setting
}

// Validate：未知的能力 id ⇒ **说得清的错**（在 `POST/PATCH /agents` 与启动读取时都报）。
//
// 为什么不能静默忽略：写了 `"ablities": …` 或 `"compactt": {…}` 的人以为它生效了，
// 而实际什么都没发生 —— 这类"配了不生效"是最难查的一类问题。
func Validate(agents config.AgentsConfig) error {
	for _, agent := range agents.Effective() {
		for _, id := range unknownIDs(agent) {
			return fmt.Errorf("agent %s 上的能力 id 不认识：%q（只认 %s）",
				describe(agent), id, strings.Join(IDs(), " / "))
		}
	}
	return nil
}

// unknownIDs：这份人格上写了、但不认识的能力 id（排好序 ⇒ 报错稳定）。
func unknownIDs(agent config.Agent) []string {
	unknown := []string{}
	for id := range agent.Abilities {
		if !Valid(id) {
			unknown = append(unknown, id)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// describe：报错里认得出是哪份人格（id 优先，名字其次）。
func describe(agent config.Agent) string {
	if agent.ID != "" {
		return agent.ID
	}
	return agent.Name
}

// deref：可空字符串的可读形式（没有就空串）。
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// trim：同上，但顺带去掉两头的空白（"   " 等于没写）。
func trim(value *string) string { return strings.TrimSpace(deref(value)) }
