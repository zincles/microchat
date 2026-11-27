// 顶栏的模型选择、Agent 列表/状态、会话抬头与标题。Agent 的选人 UI 在 Composer 的 toolbar slot。
//
// 依赖会话域的四个活值（sessionID / epoch / draft / sessionInfo），但自身没有会话窗口逻辑；
// 用 getter 传进来，避免把整个 App 状态对象塞进来。
import { ref, watch } from "vue";
import type { Ref } from "vue";
import { agentOption, groupModelsByProvider, modelKey, modelOption, parseModelKey } from "../utils/format.ts";
import type { ModelGroup } from "../utils/format.ts";
import type { Agent, ModelListItem, Session, UpdateSessionRequest } from "../api/types.ts";
import type { AgentsLibraryResource } from "./agentsLibrary.ts";
import type { DraftPrefs } from "../types/app.ts";

interface ModelsAgentsApi {
  models(): Promise<ModelListItem[]>;
  patchSession(id: string, body: UpdateSessionRequest): Promise<Session>;
}
interface ChatControlsModels {
  setSession(info: Record<string, unknown>): void;
  setModels(groups: ModelGroup[], cur: string): void;
}
interface SessionInfoLike {
  id?: string;
  title?: string;
  provider?: string;
  model?: string;
  agent_id?: string;
  thinking_level?: string;
  [key: string]: unknown;
}
interface UseModelsAgentsOptions {
	api: ModelsAgentsApi;
	agentsLibrary: AgentsLibraryResource;
	toast: (text: string, kind?: string) => void;
	chatControlsRef: Ref<ChatControlsModels | null>;
	getEpoch: () => number;
	getSessionID: () => string | null;
	isDraft: () => boolean;
	sessionInfo: SessionInfoLike;
	draftPrefs: DraftPrefs;
	onRefreshStatusline: (phase?: string, elapsedMs?: number | null, opts?: { withContext?: boolean }) => void;
	onRefreshPreview: () => void;
}

export function useModelsAgents({
	api,
	agentsLibrary,
	toast,
	chatControlsRef,
	getEpoch,
	getSessionID,
	isDraft,
	sessionInfo,
	draftPrefs,
	onRefreshStatusline,
	onRefreshPreview,
}: UseModelsAgentsOptions) {
  const vueAgents = ref<Agent[]>([]);
  // vueModels：模型清单的 UI 镜像 —— 思考档位能力也从这里读（别在前端另抄一张模型表）。
  const vueModels = ref<ModelListItem[]>([]);
  const statusAgent = ref("");
  let modelsCache: ModelListItem[] | null = null;
  let modelsInflight: Promise<ModelListItem[]> | null = null;


  function paintSessionHeader(): void {
    chatControlsRef.value?.setSession(sessionInfo);
    document.title = sessionInfo.title ? `${sessionInfo.title} - MicroChat` : "MicroChat";
  }

  function paintModelOptions(models: ModelListItem[]): void {
    vueModels.value = models;
    const groups = groupModelsByProvider(models ?? []).map((g) => ({
      provider: g.provider,
      items: g.items.map(modelOption),
    }));
    const cur = sessionInfo.provider && sessionInfo.model
      ? modelKey(sessionInfo.provider, sessionInfo.model)
      : "";
    chatControlsRef.value?.setModels(groups, cur);
  }

  function fillModelSelect(): void {
    const my = getEpoch();
    if (modelsCache) paintModelOptions(modelsCache);
    if (!modelsInflight) modelsInflight = api.models().finally(() => { modelsInflight = null; });
    modelsInflight.then((models) => {
      modelsCache = models;
      if (my === getEpoch()) paintModelOptions(models);
    }).catch((err: unknown) => {
      if (my === getEpoch() && !modelsCache) toast(`模型列表失败：${err instanceof Error ? err.message : String(err)}`, "error");
    });
  }

  async function applyModelValue(value: string): Promise<void> {
    const mp = parseModelKey(value);
    if (!mp) return;
    const { provider, model } = mp;
    if (isDraft()) {
      // 草稿：改动只活在客户端，建会话时随 POST /sessions 一起带上。
      draftPrefs.provider = provider;
      draftPrefs.model = model;
      draftPrefs.modelSelected = true;
      Object.assign(sessionInfo, { provider, model });
      paintSessionHeader();
      onRefreshStatusline("idle");
      onRefreshPreview(); // 面板不许停在旧渠道那一发上
      return;
    }
    const my = getEpoch();
    const sid = getSessionID();
    if (!sid) return;
    const updated = await api.patchSession(sid, { provider, model });
    if (my !== getEpoch()) return;
    Object.assign(sessionInfo, updated);
    paintSessionHeader();
    onRefreshStatusline("idle");
    onRefreshPreview();
  }

  function syncAgentSnapshot(): void {
	const data = agentsLibrary.snapshot.value;
	if (!data) return;
	vueAgents.value = data.agents ?? [];
	statusAgent.value = agentLabelOf(sessionInfo.agent_id);
  }

  watch(agentsLibrary.snapshot, (data) => {
	if (!data) return;
	syncAgentSnapshot();
	onRefreshStatusline("idle", undefined, { withContext: false });
	onRefreshPreview();
  }, { immediate: true });

  function agentLabelOf(id?: string | null): string {
	return vueAgents.value.find((a) => a.id === id)?.name || id || "";
  }

  function fillAgentSelect(): void {
	const my = getEpoch();
	agentsLibrary.refresh().then(() => {
		if (my !== getEpoch()) return;
		const data = agentsLibrary.snapshot.value;
		if (!data) return;
		syncAgentSnapshot();
		const list = data.agents.map(agentOption);
		statusAgent.value = list.find((agent) => agent.id === sessionInfo.agent_id)?.label ?? sessionInfo.agent_id ?? "";
		onRefreshStatusline("idle", undefined, { withContext: false });
		if (!sessionInfo.agent_id && list.length) applyAgentValue(data.default_agent || list[0].id);
	}).catch((err: unknown) => {
		if (my === getEpoch()) toast(`Agent 列表失败：${err instanceof Error ? err.message : String(err)}`, "error");
	});
  }

  async function applyAgentValue(id: string): Promise<void> {
    if (!id) return;
    if (isDraft()) {
      draftPrefs.agent = id;
      Object.assign(sessionInfo, { agent_id: id });
      paintSessionHeader();
      await refreshAgents();
      statusAgent.value = agentLabelOf(id);
      onRefreshStatusline("idle");
      onRefreshPreview();
      return;
    }
    const my = getEpoch();
    const sid = getSessionID();
    if (!sid) return;
    const updated = await api.patchSession(sid, { agent_id: id });
    if (my !== getEpoch()) return;
    Object.assign(sessionInfo, updated);
    paintSessionHeader();
    await refreshAgents();
    statusAgent.value = agentLabelOf(id);
    onRefreshStatusline("idle");
    onRefreshPreview();
  }

	async function refreshAgents(): Promise<void> {
		const my = getEpoch();
		await agentsLibrary.refresh();
		if (my === getEpoch()) syncAgentSnapshot();
	}

  return {
    vueAgents,
    vueModels,
    statusAgent,
    paintSessionHeader,
    fillModelSelect,
    applyModelValue,
    fillAgentSelect,
    agentLabelOf,
    applyAgentValue,
    refreshAgents,
  };
}
