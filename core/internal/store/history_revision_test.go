package store

import (
	"testing"

	"microchat/internal/model"
)

func TestHistoryRevisionTracksArchivedMutations(t *testing.T) {
	path := t.TempDir() + "/history.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.CreateSession("p", "m", "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateSession("p", "m", "", "")
	if err != nil {
		t.Fatal(err)
	}
	get := func(id string) string {
		t.Helper()
		r, err := st.HistoryRevision(id)
		if err != nil {
			t.Fatal(err)
		}
		if r == "" {
			t.Fatal("empty history revision")
		}
		return r
	}
	initial := get(first.ID)
	other := get(second.ID)
	if initial == other {
		t.Fatal("sessions unexpectedly share a history revision")
	}
	if got := get(first.ID); got != initial {
		t.Fatalf("unchanged history revision = %q, want %q", got, initial)
	}
	message := model.Message{ID: "message-1", SessionID: first.ID, Role: model.RoleAssistant, Content: "before"}
	if _, err := st.InsertMessage(message); err != nil {
		t.Fatal(err)
	}
	inserted := get(first.ID)
	if inserted == initial || get(second.ID) != other {
		t.Fatal("message insert did not isolate revision change to its session")
	}
	if _, err := st.UpdateMessage(first.ID, message.ID, MessageEdit{Reasoning: ptrString("think")}); err != nil {
		t.Fatal(err)
	}
	edited := get(first.ID)
	if edited == inserted {
		t.Fatal("reasoning-only message edit did not change revision")
	}
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE messages SET content = 'rolled back' WHERE id = ?1", message.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := get(first.ID); got != edited {
		t.Fatalf("rolled-back edit changed revision: got %q want %q", got, edited)
	}
	summaryTx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := insertSummary(summaryTx, model.Summary{ID: "summary-1", SessionID: first.ID, Type: model.TypeMessages, Text: "summary", SourceIDs: []string{message.ID}}); err != nil {
		_ = summaryTx.Rollback()
		t.Fatal(err)
	}
	if err := summaryTx.Commit(); err != nil {
		t.Fatal(err)
	}
	summarized := get(first.ID)
	if summarized == edited {
		t.Fatal("summary insert did not change revision")
	}
	window, windowRevision, err := st.ListMessagesWindowWithRevision(first.ID, MessageWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if windowRevision != summarized || len(window) != 1 || window[0].Content != "before" {
		t.Fatalf("message snapshot = (%q, %#v), want revision %q and archived body", windowRevision, window, summarized)
	}
	ranges, rangeRevision, err := st.ListMessageRangesWithRevision(first.ID, [][2]int{{1, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if rangeRevision != summarized || len(ranges) != 1 || ranges[0].ID != message.ID {
		t.Fatalf("range snapshot = (%q, %#v), want revision %q and message %q", rangeRevision, ranges, summarized, message.ID)
	}
	summaries, summariesRevision, err := st.ListSummariesWithRevision(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summariesRevision != summarized || len(summaries) != 1 || summaries[0].ID != "summary-1" {
		t.Fatalf("summary snapshot = (%q, %#v), want revision %q and summary-1", summariesRevision, summaries, summarized)
	}
	if _, err := st.db.Exec("UPDATE sessions SET history_revision = '' WHERE id = ?1", first.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reopened, err := st.HistoryRevision(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened == "" {
		t.Fatal("migration left the session revision empty")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	stable, err := st.HistoryRevision(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stable != reopened {
		t.Fatalf("revision after unchanged restart = %q, want %q", stable, reopened)
	}
}

func TestHistoryRevisionBackfillsLegacyDatabase(t *testing.T) {
	path := t.TempDir() + "/legacy-history.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := st.CreateSession("p", "m", "", "")
	if err != nil {
		t.Fatal(err)
	}
	message := model.Message{ID: "legacy-message", SessionID: session.ID, Role: model.RoleUser, Content: "preserved"}
	if _, err := st.InsertMessage(message); err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []string{
		"sessions_revision_insert",
		"messages_revision_insert",
		"messages_revision_update",
		"messages_revision_delete",
		"summaries_revision_insert",
		"summaries_revision_update",
		"summaries_revision_delete",
	} {
		if _, err := st.db.Exec("DROP TRIGGER IF EXISTS " + trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec("ALTER TABLE sessions DROP COLUMN history_revision"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	revision, err := st.HistoryRevision(session.ID)
	if err != nil || revision == "" {
		t.Fatalf("legacy revision = %q, %v", revision, err)
	}
	messages, err := st.ListMessages(session.ID)
	if err != nil || len(messages) != 1 || messages[0].Content != "preserved" {
		t.Fatalf("legacy messages = %#v, %v", messages, err)
	}
}
func ptrString(value string) *string { return &value }
