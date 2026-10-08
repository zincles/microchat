package store

// parseStLines 的映射契约：附件样本行 → 四样（role/content/reasoning/时间），其余全丢。

import (
	"strings"
	"testing"
)

func TestParseStLinesSkipsMetadataBlankSystemAndBadJSON(t *testing.T) {
	raw := "{" + `"chat_metadata": {"integrity": "d1e68691"}, "user_name": "unused"` + "}\n" +
		`{"name": "User", "is_user": true, "send_date": "2026-10-08T09:46:06.189Z", "mes": "你好。"}` + "\n" +
		`{"name": "User", "is_user": true, "send_date": "2026-10-08T09:48:59.095Z", "mes": "   "}` + "\n" +
		`{"name": "System", "is_user": false, "is_system": true, "mes": "旁白行"}` + "\n" +
		"not json at all\n"
	parsed, skipped := ParseSTLines([]byte(raw))
	if len(parsed) != 1 || skipped != 4 {
		t.Fatalf("该 1 条 + 跳 4 行，得到 %d 条 + 跳 %d", len(parsed), skipped)
	}
	if !parsed[0].IsUser || parsed[0].Content != "你好。" {
		t.Fatalf("user 行映射错：%+v", parsed[0])
	}
	if parsed[0].Reasoning != "" {
		t.Fatalf("user 的 reasoning 不该收：%+v", parsed[0])
	}
	if parsed[0].CreatedAt != 1791452766189 {
		t.Fatalf("send_date 该转毫秒：%d", parsed[0].CreatedAt)
	}
}

func TestParseStLinesAssistantReasoningOnly(t *testing.T) {
	raw := `{"extra": {"reasoning": "用户中文。需要问清楚。", "token_count": 575}, "name": "Assistant", "is_user": false, "send_date": "2026-10-08T09:48:20.617Z", "mes": "收到，我在。"}` + "\n" +
		`{"extra": {"reasoning": "", "token_count": 17}, "name": "Assistant", "is_user": false, "mes": "你好！", "swipes": ["x"], "swipe_id": 0}`
	parsed, skipped := ParseSTLines([]byte(raw))
	if len(parsed) != 2 || skipped != 0 {
		t.Fatalf("该 2 条 + 跳 0，得到 %d + %d", len(parsed), skipped)
	}
	if parsed[0].Reasoning != "用户中文。需要问清楚。" {
		t.Fatalf("assistant 的 reasoning 该收：%+v", parsed[0])
	}
	if parsed[0].CreatedAt != 1791452900617 {
		t.Fatalf("毫秒错：%d", parsed[0].CreatedAt)
	}
	if parsed[1].Reasoning != "" || parsed[1].CreatedAt != 0 {
		t.Fatalf("空 reasoning 收空串、缺时间给 0：%+v", parsed[1])
	}
}

func TestStMillisBadTimeIsZero(t *testing.T) {
	for _, bad := range []string{"", "  ", "昨天", "2026-13-99T99:99:99Z"} {
		if got := stMillis(bad); got != 0 {
			t.Fatalf("%q 该是 0，得到 %d", bad, got)
		}
	}
	if !strings.Contains("2026-10-08T09:46:06.189Z", "T") {
		t.Fatal("unreachable")
	}
}
