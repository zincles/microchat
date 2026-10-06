// Package abilities：**能力身份 + 代码侧定义** —— id、默认模板、以及它在 `task` 里算哪种作业
// 都写死在这里（**只此一处**）；开关与覆盖写在 `agents.json` 每个 agent 的 `abilities` 上。
//
// 四条口径（见 `DEFINE.md`「能力（Ability）」与 `AGENTS.md`「后台任务 / 压缩 / 能力」）：
//
//  1. **流程在代码里，开关在 Agent 上**："能力"= 会调模型的那种作业（`title` / `compact` / `judge`），
//     能覆盖的只有模板 / 渠道 / 模型 —— **不能造新的**（表里没有的 id 写了也不认，报错）；
//  2. **缺字段 = 默认全开**（现有 `agents.json` 一个字都不用改）；`enabled: false` ⇒
//     调用方**明确拒绝**执行那一次，**不是静默降级**；
//  3. **模板只有一份**：默认模板住这张表（`Definition.Template`）；生效模板 = agent 覆盖 ?: 默认，
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
	"microchat/internal/task"
)

// ID：能力 id。**只此三个** —— 加一个 = 同时加一段流程（在代码里），不是加一个配置项。
type ID = string

const (
	// Title：给会话起标题（一段 → `sessions.title`，落库由调用方）。
	Title ID = "title"
	// Compact：把一段压成摘要（一段 → `summaries` 一行，落库由调用方单事务负责）。
	Compact ID = "compact"
	// Judge：JEV 决策（原始文本 + 提示词 + 状态表 → 概率表）。**流程还没写**，但它是能力。
	Judge ID = "judge"
)

// titleTemplate / compactTemplate：**代码里的默认模板**（能力只能覆盖，不能新增流程）。
//
// 它们进 `prompt_version`（改一个字就变），所以**模板就是这一份**，别在别处再抄一段。
const (
	titleTemplate = `你是一名起标题助手。给下面这段对话起一个标题。

规矩：
- 只回一行标题本身：不要引号、不要句号、不要"标题："这类前缀。
- 概括这一段在聊什么（人物 / 事件 / 话题），别写成流水账，也别只抄第一句。
- 用与对话相同的语言；中文不超过 16 个字，英文不超过 8 个词。`

	compactTemplate = `你是Compactor，你负责压缩故事。

把下面这段对话收成一段“前情提要”，供后续对话直接接着聊。

规矩：
- 只写叙事：人物、事件、约定、因果、语气、以及还没解决的线索 —— 该记住的都要写上。
- **不要把世界状态写进梗概**：变量由程序单独维护、每轮现算，写进梗概只会变成过期的第二份事实。
- 状态是自动算的：材料前后附的 <current_state> 是程序事实，仅供参考，不要总结它们。你的总结里不许出现任何 <state> 或 <current_state> 块。
- 不要写"用户说了…助手回答了…"这种过程话，直接把内容收拢成一段话。
- 不要加标题、不要分小节、不要复述原文、不要提"这段对话"。
- 用与对话相同的语言，篇幅三四段以内。`
)

// Definition：一个能力的**代码侧定义** —— id、面板上的中文名、它在 `task` 里算哪种作业、默认模板。
//
// 这张表就是"流程在代码里"的落点：加一个能力 = 往这儿加一行（同时写下它那段流程）。
// `Template` 空 = 还没有模板（judge 的 state+questions 由调用方拼，不走模板）。
type Definition struct {
	ID       ID
	Label    string
	TaskKind task.Kind
	Template string
}

// definitions：全部能力（顺序固定 ⇒ 界面与 `-debug abilities` 的输出稳定）。
var definitions = []Definition{
	{ID: Title, Label: "起标题", TaskKind: task.KindTitle, Template: titleTemplate},
	{ID: Compact, Label: "压缩", TaskKind: task.KindCompact, Template: compactTemplate},
	// judge：还没有提示词模板（state+questions 由调用方拼），但登记表里已有它这一种
	{ID: Judge, Label: "判断", TaskKind: task.KindJudgement},
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
	return definition.Template
}

// Template：**生效模板** —— agent 上覆盖了就用它，否则用代码里的默认。**只此一处**。
func Template(setting Setting, id ID) string {
	if strings.TrimSpace(setting.Prompt) != "" {
		return setting.Prompt
	}
	return DefaultTemplate(id)
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

// Valid：这是个认识的能力 id 吗。
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
