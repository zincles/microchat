import assert from "node:assert/strict";
import test from "node:test";
import { createApi } from "../src/api/client.ts";

test("agents snapshot revalidates by ETag and leaves 304 body untouched", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  let response = 0;
  const api = createApi({
    base: "http://x/api/v1",
    fetchFn: async (input, init) => {
      requests.push({ url: String(input), init });
      if (response++ === 0) {
        return new Response(JSON.stringify({ default_agent: "default", agents: [], segments: [] }), {
          status: 200,
          headers: { ETag: '"agents-v1"', "Content-Type": "application/json" },
        });
      }
      return new Response(null, { status: 304 });
    },
  });

  const first = await api.agentsSnapshot();
  assert.deepEqual(first, {
    data: { default_agent: "default", agents: [], segments: [] },
    etag: '"agents-v1"',
  });
  const unchanged = await api.agentsSnapshot(first?.etag ?? undefined);
  assert.equal(unchanged, null);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].init?.cache, "no-store");
  assert.equal(new Headers(requests[1].init?.headers).get("If-None-Match"), '"agents-v1"');
});

test("history endpoints expose response revision headers and tolerate older servers", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  const api = createApi({
    base: "http://x/api/v1",
    fetchFn: async (input, init) => {
      requests.push({ url: String(input), init });
      if (String(input).endsWith("/status")) {
        return new Response(JSON.stringify({ phase: "idle", history_revision: "r1" }), {
          status: 200,
          headers: { "X-Microchat-History-Revision": "r1", "Content-Type": "application/json" },
        });
      }
      return new Response("[]", { status: 200, headers: { "Content-Type": "application/json" } });
    },
  });
  const revisions: Array<string | null> = [];
  const capture = (value: string | null) => revisions.push(value);

  await api.status("s", capture);
  await api.listSummaries("s", capture);
  await api.listMessagesLast("s", 5, capture);
  await api.messageRanges("s", [], capture);

  assert.deepEqual(revisions, ["r1", null, null, null]);
  assert.deepEqual(requests.map(({ url }) => url), [
    "http://x/api/v1/sessions/s/status",
    "http://x/api/v1/sessions/s/summaries",
    "http://x/api/v1/sessions/s/messages?last=5",
    "http://x/api/v1/sessions/s/messages/batch",
  ]);
});
