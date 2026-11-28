package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestHistoryRevisionHTTPContract(t *testing.T) {
	server := newCORSServer(t)
	session, err := server.store.CreateSession("p", "m", "", "")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/sessions/" + session.ID
	status := corsRequest(server, http.MethodGet, base+"/status", nil)
	if status.Code != http.StatusOK {
		t.Fatalf("status code = %d: %s", status.Code, status.Body)
	}
	statusRevision := status.Header().Get("X-Microchat-History-Revision")
	if statusRevision == "" || !strings.Contains(status.Body.String(), `"history_revision":"`+statusRevision+`"`) {
		t.Fatalf("status revision/header mismatch: %q %s", statusRevision, status.Body)
	}
	messages := corsRequest(server, http.MethodGet, base+"/messages", nil)
	if messages.Code != http.StatusOK || messages.Header().Get("X-Microchat-History-Revision") != statusRevision || !strings.Contains(messages.Body.String(), "[]") {
		t.Fatalf("empty messages contract: status=%d revision=%q body=%s", messages.Code, messages.Header().Get("X-Microchat-History-Revision"), messages.Body)
	}
	batch := call(server, http.MethodPost, base+"/messages/batch", `{"ranges":[[1,1]]}`)
	if batch.Code != http.StatusOK || batch.Header().Get("X-Microchat-History-Revision") != statusRevision || !strings.Contains(batch.Body.String(), "[]") {
		t.Fatalf("批量消息快照：status=%d revision=%q body=%s", batch.Code, batch.Header().Get("X-Microchat-History-Revision"), batch.Body)
	}
	summaries := corsRequest(server, http.MethodGet, base+"/summaries", nil)
	if summaries.Code != http.StatusOK || summaries.Header().Get("X-Microchat-History-Revision") != statusRevision || !strings.Contains(summaries.Body.String(), "[]") {
		t.Fatalf("empty summaries contract: status=%d revision=%q body=%s", summaries.Code, summaries.Header().Get("X-Microchat-History-Revision"), summaries.Body)
	}
}
