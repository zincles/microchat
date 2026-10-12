package chat

import (
	"context"
	"strings"
	"testing"

	"microchat/internal/compact"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
)

// 自动压缩（2026-10-11 用户定）：到触发线（缺省 = 预算 80%）就压、一次压 N 块（缺省 3）、
// 压到退出线（预算 50%）以下停；开关缺省开、可关；这一步没换来缩减就停（防死循环）。

// compactAdapter：测试里把真 compact 接成 `chat.Compactor`（与 main.go 的适配器同形）。
type compactAdapter struct{ svc *compact.Service }

func (a compactAdapter) Compact(ctx context.Context, session model.Session, blocks int) error {
	_, err := a.svc.Compact(ctx, session, compact.Request{Blocks: blocks})
	return err
}

// fakeCompactor：记账用；**不产生任何进展**（测"没有缩减 ⇒ 停"）。
type fakeCompactor struct{ calls []int }

func (f *fakeCompactor) Compact(_ context.Context, _ model.Session, blocks int) error {
	f.calls = append(f.calls, blocks)
	return nil
}

// seedMessages：按序落 pairs 对 user/assistant（每条 chars 个字）。
func seedMessages(t *testing.T, st *store.Store, sessionID string, pairs, chars int) {
	t.Helper()
	ids, err := store.MintOrderedIDs(pairs * 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pairs*2; i++ {
		role := model.RoleUser
		if i%2 == 1 {
			role = model.RoleAssistant
		}
		if _, err := st.InsertMessage(model.Message{
			ID: ids[i], SessionID: sessionID, Role: role, Content: strings.Repeat("字", chars),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func writeChatConfig(t *testing.T, dir string, chat config.ChatConfig) {
	t.Helper()
	if err := config.SaveJSON(dir+"/config.json", config.Config{Chat: chat}); err != nil {
		t.Fatal(err)
	}
}

// 真 compact 接上：压到退出线以下才停（可能不止一批）。
func TestAutoCompactLoopsUntilBelowExit(t *testing.T) {
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	writeChatConfig(t, h.configDir, config.ChatConfig{ModelContextTokens: 20000, CompactBlocks: 3, AutoCompact: true})
	seedMessages(t, h.store, h.session.ID, 6, 3000) // 12 条 × 3000 字 ≈ 2.7 万 tok
	svc := compact.New(h.store, config.Paths{ConfigDir: h.configDir, DataDir: h.configDir}, h.turns, h.tasks)
	h.service.Compactor = compactAdapter{svc: svc}

	before, err := h.service.ContextUsage(h.session)
	if err != nil {
		t.Fatal(err)
	}
	if before.UsedTokens < before.TriggerTokens {
		t.Fatalf("夹具没到触发线：used=%d trigger=%d", before.UsedTokens, before.TriggerTokens)
	}

	h.service.MaybeAutoCompact(h.session) // 同步跑完（生产里是 go）

	sums, err := h.store.ListSummaries(h.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) == 0 {
		t.Fatal("该压出至少一张摘要")
	}
	after, err := h.service.ContextUsage(h.session)
	if err != nil {
		t.Fatal(err)
	}
	if after.UsedTokens >= after.BudgetTokens/2 {
		t.Fatalf("该压到退出线（50%%）以下：used=%d exit=%d", after.UsedTokens, after.BudgetTokens/2)
	}
}

// 开关与触发线：关 ⇒ 一次都不调；没到线 ⇒ 不调；到线 + 无进展 ⇒ 调一次就停（不无限循环）。
func TestAutoCompactGatesAndNoProgressStop(t *testing.T) {
	// ① 缺省（没写）= 关：有 Compactor 也不许动
	h := newHarness(t, dummyProviders(), "dummy", "dummy")
	writeChatConfig(t, h.configDir, config.ChatConfig{ModelContextTokens: 20000, CompactBlocks: 3})
	seedMessages(t, h.store, h.session.ID, 6, 3000)
	fake := &fakeCompactor{}
	h.service.Compactor = fake
	h.service.MaybeAutoCompact(h.session)
	if len(fake.calls) != 0 {
		t.Fatalf("默认关时不该调压缩：%v", fake.calls)
	}

	// ② 开着但没到触发线：不调
	h2 := newHarness(t, dummyProviders(), "dummy", "dummy")
	writeChatConfig(t, h2.configDir, config.ChatConfig{ModelContextTokens: 20000, CompactBlocks: 3, AutoCompact: true})
	seedMessages(t, h2.store, h2.session.ID, 1, 50)
	fake2 := &fakeCompactor{}
	h2.service.Compactor = fake2
	h2.service.MaybeAutoCompact(h2.session)
	if len(fake2.calls) != 0 {
		t.Fatalf("没到触发线不该压：%v", fake2.calls)
	}

	// ③ 开着 + 到线 + 假压缩器（无进展）⇒ 调一次（块数 = 配置；没配 ⇒ 缺省 3）就停
	h3 := newHarness(t, dummyProviders(), "dummy", "dummy")
	writeChatConfig(t, h3.configDir, config.ChatConfig{ModelContextTokens: 20000, AutoCompact: true}) // CompactBlocks 缺省 ⇒ 3
	seedMessages(t, h3.store, h3.session.ID, 6, 3000)
	fake3 := &fakeCompactor{}
	h3.service.Compactor = fake3
	h3.service.MaybeAutoCompact(h3.session)
	if len(fake3.calls) != 1 || fake3.calls[0] != 3 {
		t.Fatalf("无进展该只调一次、块数 = 缺省 3：%v", fake3.calls)
	}
}
