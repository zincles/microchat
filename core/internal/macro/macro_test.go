package macro

import (
	"reflect"
	"testing"
)

func TestRenderKnownAliasesAndUnknown(t *testing.T) {
	values := Values("Alice", "旁白", "deepseek")
	cases := map[string]string{
		"你好 {{user_name}}":           "你好 Alice",
		"{{user}} 与 {{char}}":        "Alice 与 旁白",
		"{{agent_name}} / {{model}}": "旁白 / deepseek",
		"{{getvar::x}}":              "{{getvar::x}}",
		"{{ user_name }}":            "Alice",
		"{{user_name}}":              "Alice",
	}
	for input, want := range cases {
		if got := Render(input, values); got != want {
			t.Errorf("Render(%q) = %q，想要 %q", input, got, want)
		}
	}
}

func TestRenderDoesNotRecurse(t *testing.T) {
	values := Values("{{char}}", "旁白", "")
	if got := Render("{{user}}", values); got != "{{char}}" {
		t.Fatalf("宏值不该二次展开，得到 %q", got)
	}
}

func TestValuesDefaults(t *testing.T) {
	values := Values("", "", "")
	if values["user"] != DefaultUserName || values["char"] != DefaultAgentName {
		t.Fatalf("空值该回落默认，得到 %#v", values)
	}
}

func TestScanAndCounts(t *testing.T) {
	got := Scan("{{user_name}} {{ user }} {{getvar::x}} {{user_name}}")
	want := map[string]int{"user_name": 2, "user": 1, "getvar::x": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan = %#v，想要 %#v", got, want)
	}
	counts := Counts(got)
	if len(counts) != 3 || counts[0].Name != "user_name" || counts[0].Count != 2 || !counts[0].Known {
		t.Fatalf("Counts = %#v", counts)
	}
	seenUnknown := false
	for _, count := range counts {
		if count.Name == "getvar::x" && !count.Known {
			seenUnknown = true
		}
	}
	if !seenUnknown {
		t.Fatal("未知宏应该保留在统计里")
	}
}

func TestDefinitionsCopy(t *testing.T) {
	first := Definitions()
	first[0].Name = "changed"
	if Definitions()[0].Name != "user_name" {
		t.Fatal("Definitions 必须返回副本")
	}
}
