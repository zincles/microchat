// Package abilities：**能力身份 + 代码侧定义** —— id、默认模板、以及它在 `task` 里算哪种作业
// 都写死在这里（**只此一处**）；开关与覆盖写在 `agents.json` 每个 agent 的 `abilities` 上。
//
// 四条口径（见 `DEFINE.md`「能力（Ability）」与 `AGENTS.md`「后台任务 / 压缩 / 能力」）：
//
//  1. **流程在代码里，开关在 Agent 上**："能力"= 会调模型的那种作业（现在是 `title` / `compact`；
//     `judge` 2026-10-28 退役，见下面 `retired`），
//     能覆盖的只有模板 / 渠道 / 模型 —— **不能造新的**（表里没有的 id 写了也不认，报错）；
//  2. **缺字段 = 默认全开**（现有 `agents.json` 一个字都不用改）；`enabled: false` ⇒
//     调用方**明确拒绝**执行那一次，**不是静默降级**；
//  3. **模板只有一份**：默认模板住这张表（`Definition.Template`；压缩那份住段库、
//     由 `Definition.TemplateSegment` 指过去）；生效模板 = agent 覆盖 ?: 默认，
//     `Template(setting, id)` 是**唯一**的算式，`PromptVersion` 由**生效模板**算（改一个字就变）；
//  4. **一个函数搞定**：`Resolve(agent, id)` 是"这份人格在这个能力上怎么配"的**唯一**解析处 ——
//     别在调用点各读一遍 map（那会让"默认全开"这条口径散成好几份）。
//
// `turn` **不是能力** ⇒ 不在这个枚举里（它是 Agent 存在的方式，关掉就没得聊了）；
// `refresh_models` 也不是（它不调模型）。
package abilities

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"microchat/internal/config"
	"microchat/internal/state"
	"microchat/internal/task"
)

// ID：能力 id。**只有下面这几个** —— 加一个 = 同时加一段流程（在代码里），不是加一个配置项。
type ID = string

const (
	// Title：给会话起标题（一段 → `sessions.title`，落库由调用方）。
	Title ID = "title"
	// Compact：把一段压成摘要（一段 → `summaries` 一行，落库由调用方单事务负责）。
	Compact ID = "compact"
	// Judge：**已退役**（2026-10-28 用户：「暂时砍掉 agent 的 judge 功能」）。
	//
	// 它不在 `definitions` 里 ⇒ 界面不出这一块、`Valid` 不认、新写的配置不会带它。
	// 常量留着是因为两处还引着它：`internal/judge`（JEV 那发的实现，还没接调用点）
	// 与 `task.KindJudgement`（那一发的作业种类）。**流程回来时**把下面 `definitions`
	// 里那行加回去就行 —— 别把这个常量也删掉，否则那边得跟着改。
	Judge ID = "judge"
)

// retired：**已退役**的能力 id（`config.RetiredAbilityIDs`，住 config 是因为读文件时就要摘掉，
// 而 config 不能 import 这个包）。这里只借来看一眼：`Validate` 要把"退役"和"写错"分开 ——
// 老 `agents.json` 里的 `judge` 是历史，不是错，不能让整份人格读不进来。
var retired = config.RetiredAbilityIDs

// Definition：一个能力的**代码侧定义** —— id、面板上的中文名、它在 `task` 里算哪种作业、默认模板。
//
// 这张表就是"流程在代码里"的落点：加一个能力 = 往这儿加一行（同时写下它那段流程）。
// 两行都 `TemplateSegment`：默认模板住段库（可读、可复制、可参与自由组合）。
//
// `Template` 空 = 没有模板（从前 `judge` 就是这种：state+questions 由调用方拼，不走模板）。
//
// `TemplateSegment` = "默认模板其实是段库里的一条内置段"（与 `Template` 二选一）。
// 为什么要这一格：模板也是提示词，用户的抱怨很直接 ——「凭什么这一段我读不到、也拼不了」。
// 住段库 ⇒ 可读、可复制成自定义、可以被 `segments` 引用进"自由组合"。
type Definition struct {
	ID              ID
	Label           string
	TaskKind        task.Kind
	Template        string
	TemplateSegment string
}

// definitions：全部能力（顺序固定 ⇒ 界面与 `-debug abilities` 的输出稳定）。
//
// `judge` 2026-10-28 退役（用户：「暂时砍掉 agent 的 judge 功能」）——
// 它的定义行搬去了上面的 `retired`；流程回来时把这一行加回来：
//
//	{ID: Judge, Label: "判断", TaskKind: task.KindJudgement},
var definitions = []Definition{
	{ID: Title, Label: "起标题", TaskKind: task.KindTitle, TemplateSegment: config.TitleTemplateSegmentID},
	{ID: Compact, Label: "压缩", TaskKind: task.KindCompact, TemplateSegment: config.CompactTemplateSegmentID},
}

// IDs：全部能力 id（顺序 = `definitions` 的顺序）。
func IDs() []ID {
	ids := make([]ID, 0, len(definitions))
	for _, definition := range definitions {
		ids = append(ids, definition.ID)
	}
	return ids
}

// DefinitionOf：这个 id 的定义；不认识 ⇒ false（认不认识是 `Valid` / `Validate` 的事）。
func DefinitionOf(id ID) (Definition, bool) {
	for _, definition := range definitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return Definition{}, false
}

// DefaultTemplate：代码里的默认模板（没有 ⇒ 空串）。
func DefaultTemplate(id ID) string {
	definition, ok := DefinitionOf(id)
	if !ok {
		return ""
	}
	if definition.TemplateSegment != "" {
		// 住段库的那一类：正文现读自内置段（唯一一份）。读不到只可能是 id 写错 —— 开发错误，当空。
		if segment, found := config.BuiltinSegmentByID(definition.TemplateSegment); found {
			return segment.Text
		}
		return ""
	}
	return definition.Template
}

// Template：**生效模板** —— agent 上覆盖了就用它，否则用代码里的默认。**只此一处**。
func Template(setting Setting, id ID) string {
	if strings.TrimSpace(setting.Prompt) != "" {
		return setting.Prompt
	}
	return DefaultTemplate(id)
}

// AssemblePrompt：这一发能力的**系统提示词** = 模板 + 追加段。**只此一处**（`title` 与
// `compact` 都调它 —— 从前两条流程各拼一份，`title` 那份根本没拼段）。
//
// 一条必须说清的规矩：**默认模板已经在段列表里时不额外再垫一份**。
// 用户把它挑进 `segments` 就是在说"我要它按这个位置出现" —— 既在开头垫一份、
// 又在中途出现一次，等于同一段说明文读两遍（重复只会让模型更糊涂）。
// 同理：`prompt` 非空时它是 base，此时列表里再选那条内置段就是"再追加一次默认模板"，
// 那是用户显式的选择，不挡。
func AssemblePrompt(setting Setting, id ID, agents config.AgentsConfig) string {
	base := strings.TrimSpace(setting.Prompt)
	if base == "" {
		explicit := false
		if definition, ok := DefinitionOf(id); ok && definition.TemplateSegment != "" {
			for _, segmentID := range setting.Segments {
				if segmentID == definition.TemplateSegment {
					explicit = true
					break
				}
			}
		}
		if !explicit {
			base = DefaultTemplate(id)
		}
	}
	return state.JoinPrompt(base, setting.Segments, agents, string(id))
}

// PromptVersion：生效模板的指纹（`sha256(模板)` 十六进制前 8 位）。
//
// 列是 INTEGER（int64）⇒ 取 32 位那一截：**改一个字就变**（要的就是这个），还永不溢出。
// 别手写版本号 —— 手写的那个迟早与模板本身对不上。
func PromptVersion(template string) int64 {
	sum := sha256.Sum256([]byte(template))
	digits := hex.EncodeToString(sum[:])[:8]
	var version int64
	for index := range len(digits) {
		version = version*16 + int64(digitValue(digits[index]))
	}
	return version
}

// digitValue：一个十六进制字符的值（`hex.EncodeToString` 只吐小写）。
func digitValue(char byte) int {
	if char >= '0' && char <= '9' {
		return int(char - '0')
	}
	return int(char-'a') + 10
}

// Valid：这是个**现在提供**的能力 id 吗（退役的不算 —— 见 `retired`）。
func Valid(id string) bool {
	_, ok := DefinitionOf(id)
	return ok
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
	// Segments：追加段 id（顺序 = 追加顺序）。2026-10-28 起能力也能拼段 ——
	// 与 agent 的 `segments` 共用同一个段库与同一条拼装（`state.JoinPrompt`）。
	Segments []string
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
		Segments: toggle.Segments,
	}
	return setting
}

// Validate：未知的能力 id ⇒ **说得清的错**（在 `POST/PATCH /agents` 与启动读取时都报）。
//
// 为什么不能静默忽略：写了 `"ablities": …` 或 `"compactt": {…}` 的人以为它生效了，
// 而实际什么都没发生 —— 这类"配了不生效"是最难查的一类问题。
//
// **退役的 id 不在此列**（`retired`）：老文件里写着 `judge` 是历史，不是写错；
// 为此把整份人格判死、服务起不来，代价远大于收益。
func Validate(agents config.AgentsConfig) error {
	for _, agent := range agents.Effective() {
		for _, id := range unknownIDs(agent) {
			return fmt.Errorf("agent %s 上的能力 id 不认识：%q（只认 %s）",
				describe(agent), id, strings.Join(IDs(), " / "))
		}
	}
	return nil
}

// unknownIDs：这份人格上写了、但不认识的能力 id（排好序 ⇒ 报错稳定；退役的不算不认识）。
func unknownIDs(agent config.Agent) []string {
	unknown := []string{}
	for id := range agent.Abilities {
		if retired[id] {
			continue
		}
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
