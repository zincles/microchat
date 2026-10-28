package providers

import "testing"

// 协议的中文名是**唯一权威**：界面拿它画下拉，不许自己另抄一张表。
func TestProtocolOptionsCarryChineseNames(t *testing.T) {
	p := Provider{ID: "x", Vendor: VendorOpenRouter}
	options := ProtocolOptionsFor(p)
	labels := map[Protocol]string{}
	for _, option := range options {
		labels[option.Protocol] = option.Label
		if option.Hint == "" {
			t.Errorf("协议 %q 没有一句人话（下拉里只有名字，用户凭什么叫得出区别）", option.Protocol)
		}
	}
	for _, want := range []Protocol{ProtocolChatCompletion, ProtocolOpenAIResponse, ProtocolSystemOne} {
		if labels[want] == "" {
			t.Errorf("openrouter 该能换 %q（实际只有 %v）", want, labels)
		}
	}
	// **占位协议绝不出现在下拉里**：列出来等于递一个按下去必炸的选项。
	for _, option := range options {
		if err := (Provider{Vendor: VendorOpenRouter, BaseURL: "https://example.invalid/v1", Protocol: option.Protocol}).Validate(); err != nil {
			t.Errorf("下拉里出现了 Validate 会拒的协议 %q：%v", option.Protocol, err)
		}
	}
}

// 当前渠道自己配的协议必须在名单里 —— 否则界面一改别的格就把协议悄悄换掉了。
func TestProtocolOptionsIncludeTheCurrentOne(t *testing.T) {
	p := Provider{ID: "x", Vendor: VendorOllama, Protocol: ProtocolSystemOne} // ollama × systemone：Validate 会拒
	options := ProtocolOptionsFor(p)
	last := options[len(options)-1]
	if last.Protocol != ProtocolSystemOne {
		t.Fatalf("当前协议 %q 不在下拉里（实际 %v）", ProtocolSystemOne, options)
	}
	if last.Label != "SystemOne" {
		t.Fatalf("标签该是中文名，得到 %q", last.Label)
	}
	// 认不出来的怪值也要有字可显示（不能空白）
	if got := ProtocolLabel("gpt-but-magic"); got != "gpt-but-magic" {
		t.Fatalf("陌生协议该回 id 本身，得到 %q", got)
	}
}

// vendor 的中文名：每个在册的都要有，且不能等于 id（那等于没翻译）。
func TestVendorNamesAreChinese(t *testing.T) {
	for _, preset := range Presets() {
		if preset.Name == "" || preset.Name == string(preset.Vendor) {
			t.Errorf("vendor %q 没有中文名（%q）", preset.Vendor, preset.Name)
		}
	}
	if got := Vendor("没有这家").VendorName(); got != "没有这家" {
		t.Fatalf("陌生 vendor 该回 id 本身，得到 %q", got)
	}
}

// 占位协议不在中文名表里 —— 这是"它们不可能被列进下拉"的**根**。
func TestPlaceholderProtocolsHaveNoName(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolAnthropicMessages, ProtocolGeminiGenerateContent} {
		if _, ok := ProtocolOptionOf(protocol); ok {
			t.Errorf("占位协议 %q 不该有中文名（有了就会被列进下拉，而 Validate 一律拒它）", protocol)
		}
	}
}
