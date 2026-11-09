// 顶栏的模型选择、Agent 列表/状态、会话抬头与标题。Agent 的选人 UI 在 Composer 的 toolbar slot。
//
// 依赖会话域的四个活值（sessionID / epoch / draft / sessionInfo），但自身没有会话窗口逻辑；
// 用 getter 传进来，避免把整个 App 状态对象塞进来。
import { ref } from "vue";
import type { Ref } from "vue";
import { agentOption, groupModelsByProvider, modelKey, modelOption, parseModelKey } from "../utils/format.ts";
import type { ModelChoice, ModelGroup } from "../utils/format.ts";
import type { Agent, ModelListItem, Session, UpdateSessionRequest } from "../api/types.ts";

interface ModelsAgentsApi {
  models(): Promise<ModelListItem[]>;
  agents(): Promise<{ default_agent: string; agents: Agent[]; segments?: unknown[] }>;
  patchSession(id: string, body: UpdateSessionRequest): Promise<Session>;
}
interface TopbarModels {
  setSession(info: Record<string, unknown>, draft: boolean): void;
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
interface DraftPrefs {
  provider: string;
  model: string;
  agent: string;
  thinking_level?: string;
}
interface UseModelsAgentsOptions {
  api: ModelsAgentsApi;
  toast: (text: string, kind?: string) => void;
  topbarRef: Ref<TopbarModels | null>;
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
  toast,
  topbarRef,
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
  let agentsInflight: Promise<{ default_agent: string; agents: Agent[]; segments?: unknown[] }> | null = null;

  function paintSessionHeader(): void {
    topbarRef.value?.setSession(sessionInfo, isDraft());
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
    topbarRef.value?.setModels(groups, cur);
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

  // Agent 列表在"进会话"这一拍会被两条路同时要（顶栏选人 + 草稿首段/状态行）：
  // 让同一拍共享一发 GET /agents。
  function fetchAgentsOnce(): Promise<{ default_agent: string; agents: Agent[]; segments?: unknown[] }> {
    if (!agentsInflight) {
      agentsInflight = api.agents().finally(() => { agentsInflight = null; });
    }
    return agentsInflight;
  }

  function agentLabelOf(id?: string | null): string {
    return vueAgents.value.find((a) => a.id === id)?.name || id || "";
  }

  function fillAgentSelect(): void {
    const my = getEpoch();
    fetchAgentsOnce().then((data) => {
      if (my !== getEpoch()) return;
      vueAgents.value = data.agents ?? [];
      const list = (data.agents ?? []).map(agentOption);
      // 助手入口住在输入栏上方；这里只更新状态行那份“当前是谁”。
      statusAgent.value = list.find((a) => a.id === sessionInfo.agent_id)?.label ?? sessionInfo.agent_id ?? "";
      // 这一发只更新"助手"那格；上下文环由 enterSession 的 refreshStatusline 统一问一次。
      onRefreshStatusline("idle", undefined, { withContext: false });
      // 没 agent 时别猜 list[0]：后端认的是 default_agent。
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

  // refreshAgents：拉生效 agent 列表（**只为草稿的首段对话**与状态行的助手名）。
  async function refreshAgents(): Promise<void> {
    const my = getEpoch();
    try {
      const data = await fetchAgentsOnce();
      if (my !== getEpoch()) return;
      vueAgents.value = data.agents ?? [];
    } catch { /* 拿不到就空着：草稿不画首段、状态行回落到 agent id */ }
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
