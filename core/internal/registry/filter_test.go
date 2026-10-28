package registry

import (
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/store"
)

// 用户原话（2026-10-22）就是这组用例的规格：
// "若启用筛选器，筛选器内容设置为 gpt，那么 openai/gpt-5、gpt-6、gpt-5.6-sol 都会因为包含完整的
//  gpt 字符串被导入。（我们不支持 bool，也就是说 * 是无效的）。若筛选器内容是空的，则视作不使用筛选器。"
func filterFixture() []store.Discovered {
	return []store.Discovered{
		{UpstreamID: "openai/gpt-5"},
		{UpstreamID: "gpt-6"},
		{UpstreamID: "openai/gpt-5.6-sol"},
		{UpstreamID: "anthropic/claude-sonnet-4.5"},
		{UpstreamID: "meta-llama/Llama-3.3-70B-Instruct"},
		{UpstreamID: "deepseek/deepseek-chat"},
	}
}

func ids(models []store.Discovered) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.UpstreamID)
	}
	return out
}

func sameIDs(got []store.Discovered, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i].UpstreamID != want[i] {
			return false
		}
	}
	return true
}

// **空 = 不筛**：空串与纯空白都算"没填"，原样全要（连顺序都不动）。
func TestFilterModelsEmptyMeansNoFilter(t *testing.T) {
	all := filterFixture()
	for _, filter := range []string{"", "   ", "\t\n"} {
		kept, dropped := FilterModels(filter, all)
		if !sameIDs(kept, ids(all)...) {
			t.Fatalf("筛选器 %q 该原样全要，得到 %v", filter, ids(kept))
		}
		if dropped != 0 {
			t.Fatalf("筛选器 %q 不该挡掉任何模型，dropped=%d", filter, dropped)
		}
	}
	if err := ValidateModelFilter("   "); err != nil {
		t.Fatalf("纯空白该算「没填」，不该报错：%v", err)
	}
}

// **子串包含**（用户点名的那三个必须命中）：`gpt` 命中 openai/gpt-5、gpt-6、openai/gpt-5.6-sol。
func TestFilterModelsSubstringMatches(t *testing.T) {
	kept, dropped := FilterModels("gpt", filterFixture())
	if !sameIDs(kept, "openai/gpt-5", "gpt-6", "openai/gpt-5.6-sol") {
		t.Fatalf("gpt 该命中那三个，得到 %v", ids(kept))
	}
	if dropped != 3 {
		t.Fatalf("该挡掉 3 个，得到 %d", dropped)
	}
	// 前缀也参与比较：光写 gpt 就能捞到 openai/ 底下的（不必写全 id）
	// 而 "gpt5" 不是子串（中间有连字符）⇒ **不算命中** —— 这正是"完整字符串"的意思：
	// 不做模糊匹配、不忽略 - / . / 这类分隔符。
	if kept, _ := FilterModels("gpt5", filterFixture()); len(kept) != 0 {
		t.Fatalf("gpt5 不是任何 id 的子串，该一个都不命中，得到 %v", ids(kept))
	}
}

// **不分大小写**：模型 id 的大小写是上游随手写的，用户记的是片段。
func TestFilterModelsIsCaseInsensitive(t *testing.T) {
	all := filterFixture()
	for _, filter := range []string{"gpt", "GPT", "Gpt"} {
		kept, _ := FilterModels(filter, all)
		if len(kept) != 3 {
			t.Fatalf("筛选器 %q 该命中 3 个（不分大小写），得到 %v", filter, ids(kept))
		}
	}
	// 大写写在 id 里也一样能捞到
	if kept, _ := FilterModels("llama", all); !sameIDs(kept, "meta-llama/Llama-3.3-70B-Instruct") {
		t.Fatalf("llama 该命中那个大小写混杂的 id，得到 %v", ids(kept))
	}
	if kept, _ := FilterModels("70b", all); len(kept) != 1 {
		t.Fatalf("70b 该命中 70B 那条，得到 %v", ids(kept))
	}
}

// 首尾空白要吃掉（用户从别处粘一段过来很常见），但**中间**的空白是字面量。
func TestFilterModelsTrimsEdges(t *testing.T) {
	if kept, _ := FilterModels("  gpt  ", filterFixture()); len(kept) != 3 {
		t.Fatalf("首尾空白该被吃掉，得到 %v", ids(kept))
	}
	if kept, _ := FilterModels("g pt", filterFixture()); len(kept) != 0 {
		t.Fatalf("中间的空白是字面量，g pt 不该命中任何 id，得到 %v", ids(kept))
	}
}

// 一个都不命中 ⇒ 空列表、dropped 是全部（**这不是错**：筛选器就是这么工作的；
// 但刷新时它会顺着"没见到就删行"把该渠道的模型清空 —— 所以 `*` 那种写法必须在写入时拦住）。
func TestFilterModelsNoMatchYieldsEmpty(t *testing.T) {
	kept, dropped := FilterModels("不存在的模型", filterFixture())
	if len(kept) != 0 {
		t.Fatalf("该一个都不命中，得到 %v", ids(kept))
	}
	if dropped != len(filterFixture()) {
		t.Fatalf("dropped 该是全部 %d，得到 %d", len(filterFixture()), dropped)
	}
}

// **`*` 是无效的**（用户原话）：拒绝，而不是"当字面字符去匹配"。
//
// 为什么必须拒绝：没有哪个模型 id 含 `*` ⇒ 筛完是空 ⇒ 刷新时"没见到的删行"会把整个渠道的
// 模型表清空（连用户覆盖一起没）。这种"照字面匹配、你自己看着办"的宽容是**破坏性**的。
func TestValidateModelFilterRejectsWildcard(t *testing.T) {
	for _, filter := range []string{"*", "gpt*", "*gpt", "g*t", " * "} {
		err := ValidateModelFilter(filter)
		if err == nil {
			t.Fatalf("筛选器 %q 该被拒绝", filter)
		}
		// 报错要能指导怎么改，不能只说"错了"
		if !strings.Contains(err.Error(), "不支持通配符") || !strings.Contains(err.Error(), "留空") {
			t.Fatalf("筛选器 %q 的报错没讲清怎么写：%v", filter, err)
		}
	}
	for _, filter := range []string{"", "gpt", "claude", "deepseek", "gpt-5", "a-b_c/d"} {
		if err := ValidateModelFilter(filter); err != nil {
			t.Fatalf("筛选器 %q 该是合法的：%v", filter, err)
		}
	}
}

// ── 筛选器在**刷新链路**里的行为（这才是"有且只有符合筛选器的模型会被导入"落地的地方）──

// 刷新时只导入匹配的：不匹配的既不入库、也要在 Outcome 里报出被挡掉几个。
func TestRefreshOneImportsOnlyMatchingModels(t *testing.T) {
	clearKeyEnv(t)
	st := openStore(t)
	upstream := stubUpstream(t, 200, `{"data":[
	  {"id":"openai/gpt-5"},{"id":"gpt-6"},{"id":"anthropic/claude-sonnet-4.5"}]}`)

	provider := config.Provider{ID: "x", Kind: "openai", BaseURL: upstream.URL, ModelFilter: "gpt"}
	outcome, err := RefreshOne(provider, st)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Models != 2 || outcome.Filtered != 1 {
		t.Fatalf("该导入 2 个、挡掉 1 个，得到 models=%d filtered=%d", outcome.Models, outcome.Filtered)
	}
	rows, err := st.ListModels("x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("库里该只有 2 行，得到 %d：%+v", len(rows), rows)
	}
	for _, row := range rows {
		if !strings.Contains(row.UpstreamID, "gpt") {
			t.Fatalf("不匹配的模型被导入了：%+v", row)
		}
	}
}

// 改筛选器 ⇒ **再刷一次**才生效（而且不匹配的旧行确实会被删掉）。
// 这条把"筛选器只在发现那一步生效"的时序钉住 —— 否则很容易误以为改配置就立刻瘦身。
func TestRefreshOneFilterChangePrunesOnNextRefresh(t *testing.T) {
	clearKeyEnv(t)
	st := openStore(t)
	upstream := stubUpstream(t, 200, `{"data":[
	  {"id":"openai/gpt-5"},{"id":"anthropic/claude-sonnet-4.5"}]}`)

	// ① 先不筛：两个都进来
	plain := config.Provider{ID: "x", Kind: "openai", BaseURL: upstream.URL}
	if _, err := RefreshOne(plain, st); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListModels("x"); len(rows) != 2 {
		t.Fatalf("不筛时该有 2 行，得到 %d", len(rows))
	}

	// ② 挂上筛选器再刷：claude 那行被"没见到就删行"清掉
	filtered := plain
	filtered.ModelFilter = "gpt"
	outcome, err := RefreshOne(filtered, st)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Removed != 1 {
		t.Fatalf("该删掉 1 行（claude），得到 removed=%d", outcome.Removed)
	}
	rows, _ := st.ListModels("x")
	if len(rows) != 1 || rows[0].UpstreamID != "openai/gpt-5" {
		t.Fatalf("该只剩 openai/gpt-5，得到 %+v", rows)
	}
}

// **坏筛选器绝不能把模型表清空**：`*` 在写入时就被拒；万一它从手改的配置文件里钻进来，
// 刷新必须**报错并且一行都不动**（验证那一步在 Probe 里、先于任何落库）。
func TestRefreshOneRejectsWildcardFilterWithoutTouchingData(t *testing.T) {
	clearKeyEnv(t)
	st := openStore(t)
	upstream := stubUpstream(t, 200, `{"data":[{"id":"openai/gpt-5"}]}`)

	// 先铺一行"已有"的数据
	if _, _, _, err := st.RefreshDiscovered("x", []store.Discovered{{UpstreamID: "openai/gpt-5"}}, 1); err != nil {
		t.Fatal(err)
	}

	bad := config.Provider{ID: "x", Kind: "openai", BaseURL: upstream.URL, ModelFilter: "*"}
	if _, err := RefreshOne(bad, st); err == nil {
		t.Fatal("筛选器 `*` 该让刷新报错")
	}
	rows, err := st.ListModels("x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("坏筛选器不许动数据，该还是 1 行，得到 %d", len(rows))
	}
}

// ── 逗号分隔的多条规则（2026-10-28 用户定）────────────────────────────────

// 用户原话就是这组用例的规格：「模型筛选器我希望可以用逗号（半角）分割。例如 deepseek,mimo
// 会吧 deepseek-v4-flash 和 mimo-v2.6-flash 都允许，因为它们分别满足了一个规则。」
//
// 关键在最后半句：这是 **OR**。做成 AND 的话那个例子一个都筛不出来（没有哪个 id 同时含两段）。
func TestFilterModelsCommaSeparatedRulesAreOr(t *testing.T) {
	all := []store.Discovered{
		{UpstreamID: "deepseek-v4-flash"},
		{UpstreamID: "mimo-v2.6-flash"},
		{UpstreamID: "openai/gpt-5"},
	}
	kept, dropped := FilterModels("deepseek,mimo", all)
	if !sameIDs(kept, "deepseek-v4-flash", "mimo-v2.6-flash") {
		t.Fatalf("deepseek,mimo 该留下那两条，得到 %v", ids(kept))
	}
	if dropped != 1 {
		t.Fatalf("该挡掉 1 个（gpt），得到 %d", dropped)
	}
	// 顺序照抄原表（筛选不该重排）—— 界面按这个顺序显示
	if kept[0].UpstreamID != "deepseek-v4-flash" || kept[1].UpstreamID != "mimo-v2.6-flash" {
		t.Fatalf("筛选后顺序该与上游一致，得到 %v", ids(kept))
	}
}

// 分隔符是**半角**逗号；顺带钉住"空白怎么处理"（用户从别处粘一串很常见）。
func TestSplitModelFilter(t *testing.T) {
	cases := []struct {
		filter string
		want   []string
	}{
		{"", nil},
		{"   ", nil},
		{"gpt", []string{"gpt"}},
		{"gpt,claude", []string{"gpt", "claude"}},
		{"deepseek,mimo", []string{"deepseek", "mimo"}},
		// 每条首尾空白吃掉
		{" gpt , claude ", []string{"gpt", "claude"}},
		// 空条丢掉：末尾多一个逗号不该变成"必须含空串"的怪规则（空串子串什么都命中 ⇒ 筛选器静默作废）
		{"gpt,,claude,", []string{"gpt", "claude"}},
		{" , , ", nil},
		// 全角逗号**不是**分隔符（它是字面字符）—— 只收一个分隔符，说清楚
		{"gpt，claude", []string{"gpt，claude"}},
	}
	for _, c := range cases {
		got := SplitModelFilter(c.filter)
		// 用 %q 打：`[""]` 与 `[]` 在 %v 下长得一模一样（都是 `[]`），
		// 而"空条有没有被丢掉"恰恰是这组用例要区分的东西 —— 打不出来就等于没验。
		if len(got) != len(c.want) {
			t.Fatalf("筛选器 %q：得到 %q（%d 条），想要 %q（%d 条）", c.filter, got, len(got), c.want, len(c.want))
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Fatalf("筛选器 %q 第 %d 条：得到 %q，想要 %q", c.filter, i, got[i], c.want[i])
			}
		}
	}
}

// 多条规则里只要有一条含通配符 ⇒ 整体拒绝，且报错**点名是哪一条**
// （几条并列时不点名的话，用户得自己一条条试）。
func TestValidateModelFilterRejectsWildcardInAnyRule(t *testing.T) {
	err := ValidateModelFilter("deepseek,mimo*")
	if err == nil {
		t.Fatal("第二条含 `*` 该整体被拒")
	}
	if !strings.Contains(err.Error(), "mimo*") {
		t.Fatalf("报错该点名出问题的那条，得到：%v", err)
	}
	// 全是空条 = 没填 = 合法（与留空等价：都不筛）
	for _, filter := range []string{",", " , , ", ""} {
		if err := ValidateModelFilter(filter); err != nil {
			t.Fatalf("筛选器 %q 该算「没填」，不该报错：%v", filter, err)
		}
	}
	if kept, dropped := FilterModels(",", filterFixture()); len(kept) != len(filterFixture()) || dropped != 0 {
		t.Fatalf("全是逗号该原样全要，得到 %v / dropped=%d", ids(kept), dropped)
	}
}

// 同一模型命中多条规则 ⇒ **只出现一次**（别因为 break 忘了而重复 append）。
func TestFilterModelsNoDuplicateWhenMultipleRulesMatch(t *testing.T) {
	all := []store.Discovered{{UpstreamID: "openai/gpt-5"}, {UpstreamID: "claude-4"}}
	kept, _ := FilterModels("gpt,openai,gpt-5", all)
	if !sameIDs(kept, "openai/gpt-5") {
		t.Fatalf("三条规则都命中同一个模型，该只留一条，得到 %v", ids(kept))
	}
}

// 各条规则各自不分大小写（不是"整体降一次"就完事）。
func TestFilterModelsEachRuleIsCaseInsensitive(t *testing.T) {
	all := []store.Discovered{{UpstreamID: "DeepSeek-V4-Flash"}, {UpstreamID: "MIMO-v2.6"}}
	for _, filter := range []string{"deepseek,mimo", "DEEPSEEK,MIMO", "DeepSeek,MiMo"} {
		if kept, _ := FilterModels(filter, all); len(kept) != 2 {
			t.Fatalf("筛选器 %q 该命中两条，得到 %v", filter, ids(kept))
		}
	}
}

