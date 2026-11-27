import { readonly, shallowRef, type ShallowRef } from "vue";
import type { AgentsConfig } from "../api/types.ts";

export interface AgentsSnapshotApi {
  agentsSnapshot(etag?: string): Promise<{ data: AgentsConfig; etag: string | null } | null>;
}

export interface AgentsLibraryResource {
  snapshot: Readonly<ShallowRef<AgentsConfig | null>>;
  refresh(): Promise<void>;
  invalidate(): void;
}

export function createAgentsLibraryResource(api: AgentsSnapshotApi): AgentsLibraryResource {
  const value = shallowRef<AgentsConfig | null>(null);
  let etag: string | undefined;
  let generation = 0;
  let inflight: Promise<void> | null = null;

  function refresh(): Promise<void> {
    if (inflight) return inflight;
    const requestedGeneration = generation;
    let request: Promise<void>;
	request = api.agentsSnapshot(etag).then((result) => {
		if (requestedGeneration !== generation || result === null) return;
		value.value = result.data;
		etag = result.etag ?? undefined;
	}).finally(async () => {
		if (inflight === request) inflight = null;
		if (requestedGeneration !== generation) await refresh();
	});
	inflight = request;
	return request;
}

function invalidate(): void {
	generation++;
	if (!inflight) void refresh().catch(() => {});
}

  return { snapshot: readonly(value) as Readonly<ShallowRef<AgentsConfig | null>>, refresh, invalidate };
}
