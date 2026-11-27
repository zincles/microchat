import assert from "node:assert/strict";
import test from "node:test";
import { createAgentsLibraryResource } from "../src/composables/agentsLibrary.ts";
import type { AgentsConfig } from "../src/api/types.ts";

const snapshot = (name: string): AgentsConfig => ({
  default_agent: "default",
  agents: [{ id: name, name, system_prompt: "" }],
  segments: [],
});

function deferred<T>() {
	const { promise, resolve } = Promise.withResolvers<T>();
	return { promise, resolve };
}

test("agents library coalesces reads and leaves snapshots intact on 304", async () => {
	const first = deferred<{ data: AgentsConfig; etag: string | null } | null>();
	let calls = 0;
	let requestedETag: string | undefined;
	const resource = createAgentsLibraryResource({
		agentsSnapshot: async (etag) => {
			calls++;
			requestedETag = etag;
			return calls === 1 ? first.promise : null;
		},
	});
	const a = resource.refresh();
	const b = resource.refresh();
	assert.equal(calls, 1);
	first.resolve({ data: snapshot("first"), etag: "tag-1" });
	await Promise.all([a, b]);
	const original = resource.snapshot.value;
	assert.equal(original?.agents[0]?.id, "first");

	await resource.refresh();
	assert.equal(calls, 2);
	assert.equal(requestedETag, "tag-1");
	assert.equal(resource.snapshot.value, original);
});

test("invalidation during a read queues a request and discards the pre-change result", async () => {
	const before = deferred<{ data: AgentsConfig; etag: string | null } | null>();
	const secondRequest = Promise.withResolvers<void>();
	const calls: Array<{ etag?: string; resolve: (value: { data: AgentsConfig; etag: string | null } | null) => void }> = [];
	const resource = createAgentsLibraryResource({
		agentsSnapshot: (etag) => {
			const pending = deferred<{ data: AgentsConfig; etag: string | null } | null>();
			calls.push({ etag, resolve: pending.resolve });
			if (calls.length === 2) secondRequest.resolve();
			return calls.length === 1 ? before.promise : pending.promise;
		},
	});
	const initial = resource.refresh();
	resource.invalidate();
	before.resolve({ data: snapshot("stale"), etag: "old-tag" });
	await secondRequest.promise;
	assert.equal(resource.snapshot.value, null);
	assert.equal(calls.length, 2);
	assert.equal(calls[1]?.etag, undefined);
	calls[1]!.resolve({ data: snapshot("fresh"), etag: "new-tag" });
	await initial;
	assert.equal(resource.snapshot.value?.agents[0]?.id, "fresh");
});

test("a 304 preserves the shared snapshot", async () => {
	const results: Array<{ data: AgentsConfig; etag: string | null } | null> = [
		{ data: snapshot("kept"), etag: "tag" },
		null,
	];
	const resource = createAgentsLibraryResource({ agentsSnapshot: async () => results.shift() ?? null });
	await resource.refresh();
	const original = resource.snapshot.value;
	await resource.refresh();
	assert.equal(resource.snapshot.value, original);
});

test("failed reads retain the cache and a later refresh retries", async () => {
	const results: Array<() => Promise<{ data: AgentsConfig; etag: string | null } | null>> = [
		async () => ({ data: snapshot("cached"), etag: "cached-tag" }),
		async () => { throw new Error("offline"); },
		async () => ({ data: snapshot("updated"), etag: "updated-tag" }),
	];
	let calls = 0;
	const resource = createAgentsLibraryResource({
		agentsSnapshot: async () => {
			calls++;
			return results.shift()!();
		},
	});
	await resource.refresh();
	const cached = resource.snapshot.value;
	await assert.rejects(resource.refresh(), /offline/);
	assert.equal(resource.snapshot.value, cached);
	await resource.refresh();
	assert.equal(calls, 3);
	assert.equal(resource.snapshot.value?.agents[0]?.id, "updated");
});
