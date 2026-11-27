package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"microchat/internal/config"
)

func TestAgentsETagTracksEffectiveSnapshot(t *testing.T) {
	box := newDummySandboxWith(t, true)
	first := authCall(box.server, http.MethodGet, "/api/v1/agents", "", testSysToken)
	if first.Code != http.StatusOK {
		t.Fatalf("initial GET /agents = %d (%s)", first.Code, first.Body.String())
	}
	if cache := first.Header().Get("Cache-Control"); !strings.Contains(cache, "no-cache") || !strings.Contains(cache, "no-store") {
		t.Fatalf("GET /agents Cache-Control = %q, want no-cache and no-store", cache)
	}
	tag := first.Header().Get("ETag")
	if tag == "" || !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
		t.Fatalf("invalid initial ETag %q", tag)
	}
	digest := sha256.Sum256(first.Body.Bytes())
	if want := `"` + hex.EncodeToString(digest[:]) + `"`; tag != want {
		t.Fatalf("ETag = %q, want SHA-256 of exact response bytes %q", tag, want)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Header.Set("If-None-Match", "W/"+tag)
	rec := httptest.NewRecorder()
	box.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != tag {
		t.Fatalf("conditional GET = %d body=%q tag=%q, want 304 empty with original tag", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}

	if changed := authCall(box.server, http.MethodPost, "/api/v1/segments", `{"id":"etag-segment","text":"changed <&>"}`, testSysToken); changed.Code != http.StatusCreated {
		t.Fatalf("create segment = %d (%s)", changed.Code, changed.Body.String())
	}
	updated := authCall(box.server, http.MethodGet, "/api/v1/agents", "", testSysToken)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") == tag || !strings.Contains(updated.Body.String(), `"id":"etag-segment"`) || !strings.Contains(updated.Body.String(), "changed <&>") {
		t.Fatalf("segment update did not alter effective response/tag: code=%d tag=%q body=%s", updated.Code, updated.Header().Get("ETag"), updated.Body.String())
	}
}

func TestSaveAgentsPublishesOnlyAfterSuccessfulWrite(t *testing.T) {
	box := newDummySandboxWith(t, true)
	events, unsubscribe := box.server.live.Subscribe()
	defer unsubscribe()
	if err := box.server.saveAgents(config.AgentsConfig{}); err != nil {
		t.Fatalf("successful save: %v", err)
	}
	if event := <-events; event.Name != "agents_changed" {
		t.Fatalf("successful save event = %+v", event)
	}

	path := box.server.paths.Config("agents.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path)
	if err := box.server.saveAgents(config.AgentsConfig{}); err == nil {
		t.Fatal("save to directory unexpectedly succeeded")
	}
	select {
	case event := <-events:
		t.Fatalf("failed save published event %+v", event)
	default:
	}
}
