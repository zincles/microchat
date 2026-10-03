package tui

import (
	"testing"

	"github.com/mattn/go-runewidth"
)

// padCell：分隔符 `│` 必须在同一格（长短人名/CJK 都一样）。
func TestSplitColumnsAlign(t *testing.T) {
	m := fixture()
	m.width = 80
	m.agents = AgentsConfig{DefaultAgent: "a1", Agents: []Agent{
		{ID: "a1", Name: "默认很长很长很长很长很长的名字"},
		{ID: "a2", Name: "短"},
	}}
	m.agentRows = []string{"a1", "a2"}
	m.agentSel = 0
	m.agentCol = agentColList
	m.syncAgentDetail()
	// 所有带分隔符的行，分隔符必须在同一格
	sep := -1
	for _, line := range m.agentSplitLines() {
		w, found := 0, false
		for _, r := range line {
			w += runewidth.RuneWidth(r)
			if r == '│' {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		if sep < 0 {
			sep = w
		} else if w != sep {
			t.Fatalf("分隔符不在同一格：%d vs %d\n%s", sep, w, line)
		}
	}
	if sep < 0 {
		t.Fatal("没有分隔符行")
	}
}
