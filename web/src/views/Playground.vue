<script setup lang="ts">
import { aliasesApi, type ModelAliasRow } from "@/api/aliases";
import { keysApi } from "@/api/keys";
import { useAuthKey } from "@/services/auth";
import {
  AddOutline,
  ChatbubbleEllipsesOutline,
  ChevronDownOutline,
  CloseOutline,
  GridOutline,
  ImageOutline,
  ListOutline,
  OptionsOutline,
  SearchOutline,
  SendOutline,
  TrashOutline,
} from "@vicons/ionicons5";
import { NIcon, NInputNumber, NModal, NPopconfirm, NSwitch, useMessage } from "naive-ui";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import MarkdownIt from "markdown-it";
import DOMPurify from "dompurify";
import ProviderLogo from "@/components/common/ProviderLogo.vue";
import { hasProviderLogo } from "@/data/providerLogos";
import {
  findProviderByUpstreams,
  getProviderById,
  isFree,
  modalityOf,
  type Modality,
} from "@/data/freeProviders";

const { t } = useI18n();

// 单实例复用, 默认开 linkify + 安全配置(no html). DOMPurify 再兜底.
const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: true,
});
function renderMarkdown(text: string): string {
  if (!text) {
    return "";
  }
  return DOMPurify.sanitize(md.render(text), {
    ADD_ATTR: ["target", "rel"],
  });
}

const message = useMessage();
const authKey = useAuthKey();

interface Attachment {
  // 仅 image 走 OpenAI multimodal 协议; 视频协议各家不同 (Gemini 有 video_url
  // 但非标准), 先不做.
  kind: "image";
  name: string;
  mime: string;
  dataUrl: string; // base64 data URL — 不持久化到 localStorage(太大)
}
interface ChatUsage {
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
}
interface ChatMessage {
  role: "user" | "assistant" | "system";
  content: string;
  // 多模态附件(目前仅 image). 不会写入 localStorage — 持久化时 stripped,
  // 刷新后图片消失但文本和元信息保留. 简化避免 localStorage 5MB 上限.
  attachments?: Attachment[];
  // reasoning_content / reasoning 字段累加 — 思维链模型 (DeepSeek R1, Qwen-QwQ,
  // GLM-Zero, OpenAI o-series 等) 把推理过程单独流式发送
  thinking?: string;
  error?: boolean;
  // 流式阶段反馈:
  //   undefined / "done" → 完成态, 隐藏所有 spinner
  //   "thinking" → 正在推理 (拿到 reasoning_content 但还没出 content)
  //   "streaming" → 正在生成正文
  phase?: "thinking" | "streaming" | "done";
  // 元信息 — 气泡外部展示
  sentAt?: number; // 发送时刻 (epoch ms)
  firstByteAt?: number; // 第一个 token 到达 (用于算 TTFB)
  doneAt?: number; // stream 完成时刻
  usage?: ChatUsage; // 上游回报的 token 用量 (最后一帧)
}

// modelKey 编码: "<group_name>::<kind>::<name>"
//   kind=alias  → 用户选了 alias, 后端会做 alias 路由
//   kind=model  → 用户选了具体 real_model, 直接转发上游
interface Session {
  id: string;
  title: string;
  messages: ChatMessage[];
  modelKey: string;
  createdAt: number;
  updatedAt: number;
}

const SESS_KEY = "playground_sessions_v1";
const ACTIVE_ID_KEY = "playground_active_id_v1";
const sessions = ref<Session[]>([]);
const activeId = ref<string | null>(localStorage.getItem(ACTIVE_ID_KEY));
watch(activeId, v => {
  if (v) {
    localStorage.setItem(ACTIVE_ID_KEY, v);
  } else {
    localStorage.removeItem(ACTIVE_ID_KEY);
  }
});
const input = ref("");
const sending = ref(false);
const pendingAttachments = ref<Attachment[]>([]);
const fileInputRef = ref<HTMLInputElement | null>(null);
const MAX_IMAGE_MB = 8;

// 最近上传过的图片(快速复用). 持久化到 localStorage, 总大小软上限 4MB
// 防止撑爆 quota; 满了从末尾 pop 老的.
const RECENT_IMAGES_KEY = "playground_recent_images_v1";
const RECENT_IMAGES_MAX_BYTES = 4 * 1024 * 1024;
const recentImages = ref<Attachment[]>([]);

function loadRecentImages() {
  try {
    const raw = localStorage.getItem(RECENT_IMAGES_KEY);
    if (raw) {
      recentImages.value = JSON.parse(raw);
    }
  } catch {
    // ignore
  }
}
function saveRecentImages() {
  // localStorage 满时, 不停 pop 末尾再重试, 直到能写入或清空
  while (recentImages.value.length > 0) {
    try {
      localStorage.setItem(RECENT_IMAGES_KEY, JSON.stringify(recentImages.value));
      return;
    } catch {
      recentImages.value.pop();
    }
  }
}
function pushRecentImage(a: Attachment) {
  // 重复 dataUrl 视为同一张, 提前 dedup 再 unshift
  recentImages.value = [a, ...recentImages.value.filter(x => x.dataUrl !== a.dataUrl)];
  // 累计大小超 cap 就 pop 末尾
  let total = recentImages.value.reduce((s, x) => s + x.dataUrl.length, 0);
  while (total > RECENT_IMAGES_MAX_BYTES && recentImages.value.length > 1) {
    const removed = recentImages.value.pop();
    if (removed) {
      total -= removed.dataUrl.length;
    }
  }
  saveRecentImages();
}
function removeRecentImage(idx: number) {
  recentImages.value.splice(idx, 1);
  saveRecentImages();
}
function pickFromRecent(a: Attachment) {
  // 已 pending 中就跳过, 避免重复
  if (pendingAttachments.value.some(x => x.dataUrl === a.dataUrl)) {
    imgPickerOpen.value = false;
    return;
  }
  pendingAttachments.value.push(a);
  imgPickerOpen.value = false;
}

// footer toolbar 的几个浮层
const imgPickerOpen = ref(false);
const imgWrapRef = ref<HTMLDivElement | null>(null);
const settingsOpen = ref(false);
const settingsWrapRef = ref<HTMLDivElement | null>(null);
const temperature = ref(0.7);
const maxTokens = ref(1024);
const systemPrompt = ref("");

// 历史模式 — 控制 send 时携带哪些历史 message
//   all    : 全部历史 (完整上下文, 默认)
//   none   : 仅当前 user message, 无上下文 (调试无记忆场景)
//   manual : 手动指定最近 N 条 (含当前)
type HistoryMode = "all" | "none" | "manual";
const HISTORY_KEY = "playground_history_mode_v1";
const HISTORY_COUNT_KEY = "playground_history_manual_count_v1";
function migrateHistoryMode(raw: string | null): HistoryMode {
  // 兼容旧值: "single" 等同新的 "none", "last" 转 manual=3
  if (raw === "single") {
    return "none";
  }
  if (raw === "last") {
    return "manual";
  }
  if (raw === "all" || raw === "none" || raw === "manual") {
    return raw;
  }
  return "all";
}
const historyMode = ref<HistoryMode>(migrateHistoryMode(localStorage.getItem(HISTORY_KEY)));
const historyManualCount = ref<number>(
  Number(localStorage.getItem(HISTORY_COUNT_KEY)) || 4,
);
watch(historyMode, v => localStorage.setItem(HISTORY_KEY, v));
watch(historyManualCount, v =>
  localStorage.setItem(HISTORY_COUNT_KEY, String(Math.max(0, v | 0))),
);

interface GroupInfo {
  id: number;
  name: string;
  display: string;
  group_type?: string; // "standard" | "aggregate"
  exposed_models?: string[]; // 白名单 (specified mode)
  available_models?: string[]; // 上游声明清单
  upstreams?: Array<{ url?: string }>; // 用来反查 FREE_PROVIDERS 中的 provider id
}

// available_models 可能是 string[] / JSON 字符串 / 换行分隔字符串, 统一解析
function parseAvailableModels(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    return raw.filter((m): m is string => typeof m === "string");
  }
  if (typeof raw === "string" && raw.trim()) {
    try {
      const arr = JSON.parse(raw);
      if (Array.isArray(arr)) {
        return arr.filter((m): m is string => typeof m === "string");
      }
    } catch {
      // fall through to newline/comma split
    }
    return raw.split(/[\n,]/).map(s => s.trim()).filter(Boolean);
  }
  return [];
}
const groups = ref<GroupInfo[]>([]);
const aliases = ref<ModelAliasRow[]>([]);
// aggregate group → sub-group ids. 用来在 sections 计算时把 sub-group 的
// alias / model 借给 aggregate 显示, 否则 picker 看不到聚合 group 入口.
const aggregateChildren = ref<Map<number, number[]>>(new Map());

// 每个 group 下的 entries (aliases + real_models),用于弹窗 picker.
// real_models 通过 aliases 表里出现过的 real_model 字段聚合(没接 /api/groups/:id/models
// 那个端点,避免 N+1 请求 — 实际上用户最常用的 model 都被某个 alias 引用了).
interface ModelEntry {
  groupName: string;
  groupDisplay: string;
  groupHost?: string; // group 第一个 upstream URL, 给 ProviderLogo 的 favicon fallback 用
  kind: "alias" | "model";
  name: string; // 显示的 model 名 (alias name or real_model)
  hint?: string; // alias → 显示它指向哪些 real_model;model → 显示哪些 alias 用它
  isFree?: boolean; // 来自 FREE_PROVIDERS / Registry 判断, 仅对 model entries 设置
  // chat / image / video — 用 modalityOf() 算, 决定 send() 走 /v1/chat 还是
  // /v1/images. alias 默认 chat (实际请求时会 resolve 到 real_model 再算).
  modality: Modality;
}
interface GroupSection {
  group: GroupInfo;
  entries: ModelEntry[];
}

const sections = computed<GroupSection[]>(() => {
  // model 维度同时记关联 aliases + 是否免费(任一 standard sub-group 是免费就标)
  const byGroup = new Map<
    number,
    {
      aliases: Map<string, Set<string>>;
      models: Map<string, { aliases: Set<string>; isFree: boolean }>;
    }
  >();

  // group_id → providerId(用 upstreams 反查 FREE_PROVIDERS). aggregate group
  // 自己 upstreams 通常空, 不参与 free 推断 — 它的 model 通过 sub-group 走标
  const groupToProvider = new Map<number, string>();
  for (const g of groups.value) {
    if (!g.upstreams || g.upstreams.length === 0) {
      continue;
    }
    const p = findProviderByUpstreams(g.upstreams);
    if (p) {
      groupToProvider.set(g.id, p.id);
    }
  }
  function freeFor(originGid: number, model: string): boolean {
    const pid = groupToProvider.get(originGid);
    if (!pid) {
      return false;
    }
    return isFree(pid, model) === true;
  }
  function noteModel(bg: NonNullable<ReturnType<typeof byGroup.get>>, model: string, aliasOrigin: string | null, isFreeFlag: boolean) {
    const e = bg.models.get(model) || { aliases: new Set<string>(), isFree: false };
    if (aliasOrigin) {
      e.aliases.add(aliasOrigin);
    }
    e.isFree = e.isFree || isFreeFlag;
    bg.models.set(model, e);
  }

  // 反向 map: subGroupId → 它属于哪些 aggregate. 一条 alias 同时计入它自己
  // 的 group 和它所在 aggregate(s), 让 aggregate tab 也能展示 sub-group 的
  // alias / model (用户可以直接选 aggregate 入口, 后端 alias resolver 路由).
  const subToAgg = new Map<number, number[]>();
  for (const [aggId, subIds] of aggregateChildren.value.entries()) {
    for (const sid of subIds) {
      const arr = subToAgg.get(sid) || [];
      arr.push(aggId);
      subToAgg.set(sid, arr);
    }
  }

  // Pass 1: 从 alias 表反推 alias entries + model entries (model 自动带"被
  // 哪些 alias 引用"的 hint, isFree 用原始 standard group 反查)
  for (const a of aliases.value) {
    if (!a.enabled) {
      continue;
    }
    const targets = [a.group_id, ...(subToAgg.get(a.group_id) || [])];
    const isFreeFlag = freeFor(a.group_id, a.real_model);
    for (const gid of targets) {
      let g = byGroup.get(gid);
      if (!g) {
        g = { aliases: new Map(), models: new Map() };
        byGroup.set(gid, g);
      }
      const realsForAlias = g.aliases.get(a.alias) || new Set();
      realsForAlias.add(a.real_model);
      g.aliases.set(a.alias, realsForAlias);
      noteModel(g, a.real_model, a.alias, isFreeFlag);
    }
  }

  // Pass 2: 把每个 group 自带的 exposed/available_models 也加进 model 列表,
  // 避免 picker 只显示被 alias 引用过的那几个. exposed_models 优先 (白名单),
  // 否则用 available_models (上游声明). aggregate group 自己没 model 清单,
  // 跳过 — 它的 sub-group 已通过 subToAgg 反向并入它.
  for (const g of groups.value) {
    if (g.group_type === "aggregate") {
      continue;
    }
    // showAllModels=true: exposed + available 并集 (用户想看完整上游清单);
    // false: 优先 exposed 白名单, 没有才退到 available
    const effective = showAllModels.value
      ? Array.from(new Set([...(g.exposed_models || []), ...(g.available_models || [])]))
      : g.exposed_models && g.exposed_models.length > 0
        ? g.exposed_models
        : g.available_models || [];
    if (effective.length === 0) {
      continue;
    }
    const targets = [g.id, ...(subToAgg.get(g.id) || [])];
    for (const gid of targets) {
      let bg = byGroup.get(gid);
      if (!bg) {
        bg = { aliases: new Map(), models: new Map() };
        byGroup.set(gid, bg);
      }
      for (const m of effective) {
        const free = freeFor(g.id, m);
        noteModel(bg, m, null, free);
      }
    }
  }

  // Pass 3: free provider 元数据里声明的 imageModels / videoModels 也注入对应
  // 标准分组. 上游 /v1/models 不一定返回 image/video 模型 (e.g. agnes 的 image
  // 走单独端点), 用户也可能没手动加进 exposed_models, 这里兜底让 picker 能看到.
  // 仅对 standard group + 匹配到 FREE_PROVIDERS 时生效, 不污染普通分组.
  for (const g of groups.value) {
    if (g.group_type === "aggregate") {
      continue;
    }
    const pid = groupToProvider.get(g.id);
    if (!pid) {
      continue;
    }
    const fp = getProviderById(pid);
    if (!fp) {
      continue;
    }
    const extra = [...(fp.imageModels || []), ...(fp.videoModels || [])];
    if (extra.length === 0) {
      continue;
    }
    const targets = [g.id, ...(subToAgg.get(g.id) || [])];
    for (const gid of targets) {
      let bg = byGroup.get(gid);
      if (!bg) {
        bg = { aliases: new Map(), models: new Map() };
        byGroup.set(gid, bg);
      }
      for (const m of extra) {
        // 当前 freeProvider 全部 imageModels / videoModels 都是免费 — 跟 chat 一样标 true
        noteModel(bg, m, null, true);
      }
    }
  }

  const out: GroupSection[] = [];
  for (const g of groups.value) {
    const data = byGroup.get(g.id);
    if (!data) {
      continue;
    }
    const entries: ModelEntry[] = [];
    const pid = groupToProvider.get(g.id);
    const groupHost = g.upstreams?.[0]?.url || "";
    // aliases 排在前面 (高频选择). hint 字段全量列出关联, 由 UI 层用
    // ellipsis 截断 — 列表视图能展示更多, 卡片视图自动收尾.
    // alias 的 isFree 取决于它指向的 real_models 在 byGroup 里的 isFree 是否任一为 true
    for (const [name, reals] of [...data.aliases.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
      let aliasFree = false;
      let aliasModality: Modality = "chat";
      for (const m of reals) {
        const minfo = data.models.get(m);
        if (minfo?.isFree) {
          aliasFree = true;
        }
        // alias 模态取它指向的任一 real_model 的模态; 多 target 时第一个非 chat 优先
        const mod = modalityOf(pid, m);
        if (mod !== "chat") {
          aliasModality = mod;
        }
      }
      entries.push({
        groupName: g.name,
        groupDisplay: g.display,
        groupHost,
        kind: "alias",
        name,
        hint: [...reals].join(", "),
        isFree: aliasFree,
        modality: aliasModality,
      });
    }
    for (const [name, info] of [...data.models.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
      entries.push({
        groupName: g.name,
        groupDisplay: g.display,
        groupHost,
        kind: "model",
        name,
        hint: info.aliases.size > 0 ? [...info.aliases].join(", ") : undefined,
        isFree: info.isFree,
        modality: modalityOf(pid, name),
      });
    }
    out.push({ group: g, entries });
  }
  return out;
});

const pickerOpen = ref(false);
const pickerSearch = ref("");
const pickerWrapRef = ref<HTMLDivElement | null>(null);

// 列表 / 卡片视图切换 — 模型多时列表密度高, 卡片视觉友好但占屏多.
// 默认列表 (聚合平台模型量级大时更实用), localStorage 记用户偏好.
type PickerView = "list" | "grid";
const PICKER_VIEW_KEY = "playground_picker_view_v1";
const pickerView = ref<PickerView>(
  (localStorage.getItem(PICKER_VIEW_KEY) as PickerView | null) || "list",
);
watch(pickerView, v => localStorage.setItem(PICKER_VIEW_KEY, v));

// 列表视图是否按类型 (alias / model) 分组. 默认不分组 — alias 用 tag 标记,
// model 无 tag, 一个表格里全展开更紧凑. 用户可切换.
const GROUP_BY_KEY = "playground_picker_group_v1";
const groupByKind = ref<boolean>(localStorage.getItem(GROUP_BY_KEY) === "1");
watch(groupByKind, v => localStorage.setItem(GROUP_BY_KEY, v ? "1" : "0"));

// 列表视图的过滤 + 排序
type FilterType = "all" | "alias" | "model";
type SortBy = "default" | "name_asc" | "name_desc";
const FILTER_TYPE_KEY = "playground_filter_type_v1";
const FILTER_FREE_KEY = "playground_filter_free_v1";
const SORT_BY_KEY = "playground_sort_by_v1";
const filterType = ref<FilterType>(
  ((localStorage.getItem(FILTER_TYPE_KEY) as FilterType | null) || "all"),
);
const filterFreeOnly = ref<boolean>(localStorage.getItem(FILTER_FREE_KEY) === "1");
const sortBy = ref<SortBy>(
  ((localStorage.getItem(SORT_BY_KEY) as SortBy | null) || "default"),
);
watch(filterType, v => localStorage.setItem(FILTER_TYPE_KEY, v));
watch(filterFreeOnly, v => localStorage.setItem(FILTER_FREE_KEY, v ? "1" : "0"));
watch(sortBy, v => localStorage.setItem(SORT_BY_KEY, v));

// 显示"全部模型" — 默认 false: 优先 exposed_models 白名单, 否则 available_models.
// true: 把 exposed + available 并集都列出来 (适合用户找 "我知道这 provider 有
// 这个 model 但 picker 没显示" 的场景).
const SHOW_ALL_KEY = "playground_show_all_models_v1";
const showAllModels = ref<boolean>(localStorage.getItem(SHOW_ALL_KEY) === "1");
watch(showAllModels, v => localStorage.setItem(SHOW_ALL_KEY, v ? "1" : "0"));

// 手动输入模型名 — picker 里没有但用户想试的
const manualModelName = ref("");
function pickManual() {
  const name = manualModelName.value.trim();
  if (!name) {
    return;
  }
  // 用当前 active group; 如果是 ALL tab 提示先选 provider
  if (activeTabGroupId.value === null) {
    message.warning(t("playground.pickProviderFirst"));
    return;
  }
  const g = groups.value.find(x => x.id === activeTabGroupId.value);
  if (!g) {
    return;
  }
  // 手动输入 model 时反查 provider 算 modality, 让用户手动加 image / video
  // model 时 send() 也能走对路径
  const pUp = findProviderByUpstreams(g.upstreams);
  pickModel({
    groupName: g.name,
    groupDisplay: g.display,
    groupHost: g.upstreams?.[0]?.url,
    kind: "model",
    name,
    modality: modalityOf(pUp?.id, name),
  });
  manualModelName.value = "";
}

// 表头 NAME 列点击循环 default → asc → desc → default
function cycleNameSort() {
  sortBy.value =
    sortBy.value === "default" ? "name_asc"
    : sortBy.value === "name_asc" ? "name_desc"
    : "default";
}

// 应用 filter + sort 的入口. 注意: 分组模式下 alias 和 model 各自单独跑.
function applyFilterSort(entries: ModelEntry[]): ModelEntry[] {
  let out = entries;
  if (filterType.value !== "all") {
    out = out.filter(e => e.kind === filterType.value);
  }
  if (filterFreeOnly.value) {
    out = out.filter(e => e.isFree === true);
  }
  if (sortBy.value === "name_asc") {
    out = [...out].sort((a, b) => a.name.localeCompare(b.name));
  } else if (sortBy.value === "name_desc") {
    out = [...out].sort((a, b) => b.name.localeCompare(a.name));
  }
  return out;
}

// 总 entries (alias + model) 超过这个阈值时, picker 从 dropdown 切换成
// 全屏 modal + provider tabs + 卡片网格. 少量模型不必要的弹窗会让人烦.
const PICKER_MODAL_THRESHOLD = 10;
const totalEntries = computed(() =>
  sections.value.reduce((sum, s) => sum + s.entries.length, 0),
);
const useModalPicker = computed(() => totalEntries.value > PICKER_MODAL_THRESHOLD);

// modal 模式额外状态: 当前激活的 group tab(filteredSections 定义在下方,
// 通过 computed lazy 求值, 运行时引用安全).
const activeTabGroupId = ref<number | null>(null);

function onDocClick(e: MouseEvent) {
  const target = e.target as Node;
  // Modal 模式时 picker 在 body 上 teleport 出来, click outside 应该交给
  // NModal mask 自己管, 这里不参与 — 否则点击 modal 内 tab/搜索框/卡片
  // 都会被误判为"在 pickerWrapRef 外"而关闭弹窗.
  if (
    pickerOpen.value &&
    !useModalPicker.value &&
    pickerWrapRef.value &&
    !pickerWrapRef.value.contains(target)
  ) {
    pickerOpen.value = false;
  }
  if (imgPickerOpen.value && imgWrapRef.value && !imgWrapRef.value.contains(target)) {
    imgPickerOpen.value = false;
  }
  if (settingsOpen.value && settingsWrapRef.value && !settingsWrapRef.value.contains(target)) {
    settingsOpen.value = false;
  }
}
onMounted(() => document.addEventListener("mousedown", onDocClick));
onBeforeUnmount(() => {
  document.removeEventListener("mousedown", onDocClick);
  document.body.classList.remove("pg-route-active");
});

async function reloadAliases() {
  // alias / group 改动是通过 admin UI / curl 触发的, 不会主动通知 Playground.
  // 每次打开 picker 时静默刷新一次 — 否则被删的 alias / 新增的 model 都看
  // 不到. 同时拉 group(含 exposed/available_models 变更).
  try {
    const [g, a] = await Promise.all([keysApi.getGroups(), aliasesApi.list()]);
    groups.value = (g || [])
      .filter(x => x.id != null)
      .map(x => ({
        id: x.id as number,
        name: x.name,
        display: x.display_name || x.name,
        group_type: x.group_type,
        exposed_models: Array.isArray(x.exposed_models)
          ? (x.exposed_models as string[])
          : undefined,
        available_models: parseAvailableModels(x.available_models),
        upstreams: Array.isArray(x.upstreams)
          ? (x.upstreams as Array<{ url?: string }>)
          : undefined,
      }));
    aliases.value =
      (a as unknown as { data?: ModelAliasRow[] }).data ||
      (a as unknown as ModelAliasRow[]) ||
      [];
  } catch {
    // 拉新失败就用旧数据, 不阻断 UI
  }
}

function togglePicker() {
  pickerOpen.value = !pickerOpen.value;
  if (pickerOpen.value) {
    pickerSearch.value = "";
    reloadAliases();
    if (useModalPicker.value) {
      // 优先跳到当前 model 所在 group, 没选过模型才退到 ALL
      activeTabGroupId.value = currentGroupId.value;
    } else {
      nextTick(() => {
        const input = pickerWrapRef.value?.querySelector<HTMLInputElement>(".pg-pick__search input");
        input?.focus();
      });
    }
  }
}

// 当前已选 model 的 group id (用于 picker 自动定位 tab + tab current 标记)
const currentGroupId = computed<number | null>(() => {
  const key = active.value?.modelKey;
  if (!key) {
    return null;
  }
  const parts = key.split("::");
  if (parts.length !== 3) {
    return null;
  }
  const g = groups.value.find(x => x.name === parts[0]);
  return g?.id ?? null;
});

// 当前 entry 是否就是已选 model (UI 高亮)
function isCurrentEntry(e: ModelEntry): boolean {
  return active.value?.modelKey === `${e.groupName}::${e.kind}::${e.name}`;
}

// ALL tab 约定: activeTabGroupId === null
const activeTabEntries = computed<ModelEntry[]>(() => {
  if (activeTabGroupId.value === null) {
    return filteredSections.value.flatMap(s => s.entries);
  }
  const sec = filteredSections.value.find(s => s.group.id === activeTabGroupId.value);
  return sec ? sec.entries : [];
});
const totalFilteredCount = computed(() =>
  filteredSections.value.reduce((s, x) => s + x.entries.length, 0),
);

// 拆 alias / model 两组 — 卡片网格按这两个 section 分别渲染
const activeAliasEntries = computed(() =>
  applyFilterSort(activeTabEntries.value.filter(e => e.kind === "alias")),
);
const activeModelEntries = computed(() =>
  applyFilterSort(activeTabEntries.value.filter(e => e.kind === "model")),
);
// 合并视图: 应用 filter+sort 后整体一起排序(alias/model 不再强制分前后)
const mergedEntries = computed<ModelEntry[]>(() => {
  const all = activeTabEntries.value;
  return applyFilterSort(all);
});

// Provider tab 用的 logo hint — 优先 group.name (短名, 通常匹配 provider id 如
// openai/anthropic), 否则 display
function logoHintForGroup(g: { name: string; display: string }) {
  if (hasProviderLogo(g.name)) {
    return g.name;
  }
  if (hasProviderLogo(g.display)) {
    return g.display;
  }
  return null;
}

const filteredSections = computed<GroupSection[]>(() => {
  const q = pickerSearch.value.trim().toLowerCase();
  if (!q) {
    return sections.value;
  }
  const out: GroupSection[] = [];
  for (const s of sections.value) {
    const groupMatch =
      s.group.name.toLowerCase().includes(q) || s.group.display.toLowerCase().includes(q);
    const entries = s.entries.filter(
      e =>
        groupMatch ||
        e.name.toLowerCase().includes(q) ||
        (e.hint || "").toLowerCase().includes(q),
    );
    if (entries.length > 0) {
      out.push({ group: s.group, entries });
    }
  }
  return out;
});

const active = computed(() => sessions.value.find(s => s.id === activeId.value) || null);

const activeModelLabel = computed(() => {
  const s = active.value;
  if (!s || !s.modelKey) {
    return "";
  }
  const parts = s.modelKey.split("::");
  if (parts.length !== 3) {
    return s.modelKey;
  }
  const [groupName, kind, name] = parts;
  const g = groups.value.find(x => x.name === groupName);
  const kindBadge = kind === "alias" ? "alias" : "model";
  return `${name}  ·  ${g?.display || groupName}  ·  ${kindBadge}`;
});

function loadSessions() {
  try {
    const raw = localStorage.getItem(SESS_KEY);
    if (raw) {
      sessions.value = JSON.parse(raw);
    }
  } catch {
    // ignore
  }
}

// 持久化时 strip 掉 attachments 字段 — base64 数据动辄几 MB, 撑爆 5MB
// localStorage. 当前会话刷新后图片消失但文字和元信息保留, 可接受的折衷.
watch(
  sessions,
  () => {
    const slim = sessions.value.map(s => ({
      ...s,
      messages: s.messages.map(m => {
        if (!m.attachments) {
          return m;
        }
        // eslint-disable-next-line @typescript-eslint/no-unused-vars
        const { attachments, ...rest } = m;
        return rest;
      }),
    }));
    try {
      localStorage.setItem(SESS_KEY, JSON.stringify(slim));
    } catch {
      // quota exceeded 等极端 case 静默忽略, 不阻断 UI
    }
  },
  { deep: true },
);

function newSession() {
  const id = `s-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
  const first = sections.value[0]?.entries[0];
  const defaultModel = first ? `${first.groupName}::${first.kind}::${first.name}` : "";
  const s: Session = {
    id,
    title: t("playground.defaultTitle"),
    messages: [],
    modelKey: defaultModel,
    createdAt: Date.now(),
    updatedAt: Date.now(),
  };
  sessions.value.unshift(s);
  activeId.value = id;
}

function deleteSession(id: string) {
  sessions.value = sessions.value.filter(s => s.id !== id);
  if (activeId.value === id) {
    activeId.value = sessions.value[0]?.id || null;
  }
}

function pickModel(e: ModelEntry) {
  const s = active.value;
  if (!s) {
    return;
  }
  s.modelKey = `${e.groupName}::${e.kind}::${e.name}`;
  pickerOpen.value = false;
  pickerSearch.value = "";
}

function openFilePicker() {
  fileInputRef.value?.click();
}

function fmtBytes(n: number): string {
  if (n < 1024) {
    return `${n} B`;
  }
  if (n < 1024 * 1024) {
    return `${(n / 1024).toFixed(1)} KB`;
  }
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

async function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onerror = () => reject(r.error);
    r.onload = () => resolve(r.result as string);
    r.readAsDataURL(file);
  });
}

async function onFileChange(e: Event) {
  const target = e.target as HTMLInputElement;
  const files = Array.from(target.files || []);
  for (const f of files) {
    if (f.size > MAX_IMAGE_MB * 1024 * 1024) {
      message.error(
        t("playground.attachmentTooLarge", {
          name: f.name,
          size: fmtBytes(f.size),
          max: `${MAX_IMAGE_MB} MB`,
        }),
      );
      continue;
    }
    try {
      const dataUrl = await readAsDataUrl(f);
      const att: Attachment = {
        kind: "image",
        name: f.name,
        mime: f.type || "image/png",
        dataUrl,
      };
      pendingAttachments.value.push(att);
      pushRecentImage(att);
    } catch {
      message.error(t("playground.attachmentReadFailed", { name: f.name }));
    }
  }
  target.value = ""; // 允许重选同名文件
  imgPickerOpen.value = false;
}

function removePending(idx: number) {
  pendingAttachments.value.splice(idx, 1);
}

// Image generation: 调用 OpenAI 兼容 /v1/images/generations, 把生成的图片
// URL 转 markdown 塞到 assistant message — 现有 markdown-it 渲染会自动出
// <img>. 跟 chat 共享 messages 数组 + session, 切回 chat 历史依然可见.
// 暂不暴露 size/n 参数 (硬编码 1×1024), 后续可加控件.
async function sendImage(groupName: string, modelName: string, prompt: string) {
  const s = active.value;
  if (!s) {
    return;
  }
  if (!prompt) {
    message.warning(t("playground.imagePromptRequired"));
    return;
  }
  const now = Date.now();
  s.messages.push({ role: "user", content: prompt, sentAt: now });
  s.messages.push({
    role: "assistant",
    content: "",
    phase: "thinking",
    sentAt: now,
  });
  const asst = s.messages[s.messages.length - 1];
  input.value = "";
  pendingAttachments.value = [];
  sending.value = true;
  s.updatedAt = Date.now();
  if (s.title === t("playground.defaultTitle")) {
    s.title = prompt.slice(0, 24);
  }
  await nextTick();
  scrollToBottom();

  try {
    const resp = await fetch(
      `/proxy/${encodeURIComponent(groupName)}/v1/images/generations`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${authKey.value || ""}`,
          "X-Playground-Trial": "1",
        },
        body: JSON.stringify({
          model: modelName,
          prompt,
          n: 1,
          size: "1024x1024",
        }),
      },
    );
    if (!resp.ok) {
      const errText = await resp.text().catch(() => "");
      asst.content = `[${resp.status} ${resp.statusText}] ${errText || t("playground.requestFailed")}`;
      asst.error = true;
      asst.phase = "done";
      asst.doneAt = Date.now();
      return;
    }
    const json = (await resp.json()) as {
      data?: Array<{ url?: string; b64_json?: string; revised_prompt?: string }>;
      usage?: { total_tokens?: number; input_tokens?: number; output_tokens?: number };
    };
    const items = json.data || [];
    if (items.length === 0) {
      asst.content = t("playground.emptyResponse");
      asst.error = true;
    } else {
      // OpenAI 返回有 url (托管) 或 b64_json (内嵌). 都转 markdown image, 渲
      // 染器会出 <img>. revised_prompt (如果有) 作为图片下方说明.
      const lines: string[] = [];
      for (const it of items) {
        const src = it.url || (it.b64_json ? `data:image/png;base64,${it.b64_json}` : "");
        if (!src) continue;
        lines.push(`![](${src})`);
        if (it.revised_prompt) {
          lines.push(`*${it.revised_prompt}*`);
        }
      }
      asst.content = lines.join("\n\n");
      asst.firstByteAt = Date.now();
    }
    if (json.usage) {
      asst.usage = {
        prompt_tokens: json.usage.input_tokens,
        completion_tokens: json.usage.output_tokens,
        total_tokens: json.usage.total_tokens,
      };
    }
    asst.phase = "done";
    asst.doneAt = Date.now();
  } catch (e) {
    asst.content = `[network error] ${(e as Error).message}`;
    asst.error = true;
    asst.phase = "done";
    asst.doneAt = Date.now();
  } finally {
    sending.value = false;
  }
}

// Ctrl/Cmd+V 粘贴: 剪贴板含图片就当附件处理 (截图、复制图片场景常用),
// 不含图片则不阻止默认 paste — 文字粘贴正常.
async function onPaste(e: ClipboardEvent) {
  const items = e.clipboardData?.items;
  if (!items) {
    return;
  }
  const imageFiles: File[] = [];
  for (let i = 0; i < items.length; i++) {
    const it = items[i];
    if (it.kind === "file" && it.type.startsWith("image/")) {
      const f = it.getAsFile();
      if (f) {
        imageFiles.push(f);
      }
    }
  }
  if (imageFiles.length === 0) {
    return; // 走默认 paste, 不动文字
  }
  e.preventDefault();
  for (const f of imageFiles) {
    if (f.size > MAX_IMAGE_MB * 1024 * 1024) {
      message.error(
        t("playground.attachmentTooLarge", {
          name: f.name || t("playground.pastedImage"),
          size: fmtBytes(f.size),
          max: `${MAX_IMAGE_MB} MB`,
        }),
      );
      continue;
    }
    try {
      const dataUrl = await readAsDataUrl(f);
      const att: Attachment = {
        kind: "image",
        name: f.name || `${t("playground.pastedImage")}-${Date.now()}.png`,
        mime: f.type || "image/png",
        dataUrl,
      };
      pendingAttachments.value.push(att);
      pushRecentImage(att);
    } catch {
      message.error(
        t("playground.attachmentReadFailed", {
          name: f.name || t("playground.pastedImage"),
        }),
      );
    }
  }
}

async function send() {
  const s = active.value;
  if (!s || sending.value) {
    return;
  }
  const text = input.value.trim();
  const atts = pendingAttachments.value.slice();
  if (!text && atts.length === 0) {
    return;
  }
  if (!s.modelKey) {
    message.warning(t("playground.pickModelFirst"));
    pickerOpen.value = true;
    return;
  }
  const parts = s.modelKey.split("::");
  if (parts.length !== 3) {
    message.error(t("playground.pickModelExpired"));
    return;
  }
  const [groupName, , modelName] = parts;

  // 按 modality 分流: chat → 现有 streaming 逻辑; image → sendImage();
  // video → 提示尚未支持 (P11.23 范围). 模态从当前 sections entry 反查;
  // 找不到 entry (新 group 未刷新) 时, 用 modalityOf 直接算.
  let modality: Modality = "chat";
  for (const sec of sections.value) {
    if (sec.group.name !== groupName) continue;
    const e = sec.entries.find(x => x.name === modelName);
    if (e) {
      modality = e.modality;
      break;
    }
  }
  if (modality === "video") {
    message.warning(t("playground.videoNotSupported"));
    return;
  }
  if (modality === "image") {
    await sendImage(groupName, modelName, text);
    return;
  }

  const now = Date.now();
  s.messages.push({
    role: "user",
    content: text,
    attachments: atts.length ? atts : undefined,
    sentAt: now,
  });
  s.messages.push({ role: "assistant", content: "", thinking: "", phase: "thinking", sentAt: now });
  const asst = s.messages[s.messages.length - 1];
  input.value = "";
  pendingAttachments.value = [];
  sending.value = true;
  s.updatedAt = Date.now();
  if (s.title === t("playground.defaultTitle")) {
    s.title = (text || atts[0]?.name || "").slice(0, 24);
  }

  // 构造 payload — 如果带附件, content 用 OpenAI multimodal 数组格式
  // [{type:"text", text:"..."}, {type:"image_url", image_url:{url:"data:..."}}]
  type Part = { type: "text"; text: string } | { type: "image_url"; image_url: { url: string } };
  type PayloadMsg = { role: string; content: string | Part[] };
  const payloadMsgs: PayloadMsg[] = [];
  if (systemPrompt.value.trim()) {
    payloadMsgs.push({ role: "system", content: systemPrompt.value.trim() });
  }

  // 历史窗口按 historyMode 截取. s.messages 的最后一条是 assistant placeholder,
  // 上一条是刚 push 的 user, 之前的是历史. payload 的最后一个永远是当前 user.
  const past = s.messages.slice(0, -1); // 去掉 assistant placeholder
  let windowMsgs: ChatMessage[];
  switch (historyMode.value) {
    case "none":
      // 只当前 user (past 的最后一条)
      windowMsgs = past.slice(-1);
      break;
    case "manual": {
      // 末尾 N 条 (N 个最近历史 + 当前). N=0 等同 none.
      const n = Math.max(0, historyManualCount.value | 0);
      windowMsgs = past.slice(-(n + 1));
      break;
    }
    default:
      windowMsgs = past;
  }

  for (const m of windowMsgs) {
    if (m.attachments && m.attachments.length) {
      const parts: Part[] = [];
      if (m.content) {
        parts.push({ type: "text", text: m.content });
      }
      for (const a of m.attachments) {
        if (a.kind === "image") {
          parts.push({ type: "image_url", image_url: { url: a.dataUrl } });
        }
      }
      payloadMsgs.push({ role: m.role, content: parts });
    } else {
      payloadMsgs.push({ role: m.role, content: m.content });
    }
  }

  await nextTick();
  scrollToBottom();

  try {
    const resp = await fetch(
      `/proxy/${encodeURIComponent(groupName)}/v1/chat/completions`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${authKey.value || ""}`,
          // Playground 试模型场景: 即使 key 失败也不应该被熔断 — 用户可能
          // 在用尚未生效/试用中的 token. 后端识别此 header 跳过 UpdateStatus
          // 计数 (后端支持需另外实施).
          "X-Playground-Trial": "1",
        },
        body: JSON.stringify({
          model: modelName,
          messages: payloadMsgs,
          temperature: temperature.value,
          max_tokens: maxTokens.value,
          stream: true,
          // OpenAI 规范: 让上游在最后一帧返回 token usage. 不支持的 provider
          // 会忽略字段, 此时 asst.usage 保持空, UI 上不展示 token meta.
          stream_options: { include_usage: true },
        }),
      },
    );

    if (!resp.ok || !resp.body) {
      const errText = await resp.text().catch(() => "");
      asst.content = `[${resp.status} ${resp.statusText}] ${errText || t("playground.requestFailed")}`;
      asst.error = true;
      asst.phase = "done";
      asst.doneAt = Date.now();
      return;
    }

    const reader = resp.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      buf += dec.decode(value, { stream: true });
      let nl: number;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line.startsWith("data:")) {
          continue;
        }
        const json = line.slice(5).trim();
        if (json === "[DONE]") {
          break;
        }
        try {
          const chunk = JSON.parse(json);
          // usage 通常只在最后一帧出现(stream_options.include_usage=true)
          if (chunk.usage && typeof chunk.usage === "object") {
            asst.usage = {
              prompt_tokens: chunk.usage.prompt_tokens,
              completion_tokens: chunk.usage.completion_tokens,
              total_tokens: chunk.usage.total_tokens,
            };
          }
          const delta = chunk.choices?.[0]?.delta;
          if (!delta) {
            continue;
          }
          // 思维链字段 (DeepSeek R1 reasoning_content / Claude-via-proxy
          // reasoning / GLM thinking 等不同 provider 命名). 都累计到 thinking.
          const reasoning =
            (typeof delta.reasoning_content === "string" && delta.reasoning_content) ||
            (typeof delta.reasoning === "string" && delta.reasoning) ||
            (typeof delta.thinking === "string" && delta.thinking) ||
            "";
          if (reasoning) {
            if (!asst.firstByteAt) {
              asst.firstByteAt = Date.now();
            }
            asst.thinking = (asst.thinking || "") + reasoning;
            if (!asst.content) {
              asst.phase = "thinking";
            }
            scrollToBottom();
          }
          if (typeof delta.content === "string" && delta.content) {
            if (!asst.firstByteAt) {
              asst.firstByteAt = Date.now();
            }
            asst.content += delta.content;
            asst.phase = "streaming";
            scrollToBottom();
          }
        } catch {
          // 单条 chunk 解析失败不影响后续
        }
      }
    }
    if (!asst.content && !asst.thinking) {
      asst.content = t("playground.emptyResponse");
    }
    asst.phase = "done";
    asst.doneAt = Date.now();
  } catch (e) {
    asst.content = `[network error] ${(e as Error).message}`;
    asst.error = true;
    asst.phase = "done";
    asst.doneAt = Date.now();
  } finally {
    sending.value = false;
    s.updatedAt = Date.now();
  }
}

const msgBoxRef = ref<HTMLDivElement | null>(null);
function scrollToBottom() {
  const el = msgBoxRef.value;
  if (!el) {
    return;
  }
  el.scrollTop = el.scrollHeight;
}

onMounted(async () => {
  // Playground 路由激活时给 body 加 marker class, 配合下方 unscoped
  // <style> 强制 Layout 的 .v3-main / .v3-main-content overflow:hidden.
  // 否则父链路允许子元素撑高, 全局滚动条出现, 把 AutoGateway header
  // 留下而 footer 被推到看不见的位置.
  document.body.classList.add("pg-route-active");

  loadSessions();
  loadRecentImages();
  try {
    // 用完整 getGroups (不是 listGroups), 因为我们要 exposed_models /
    // available_models 把 group 自带的可调用模型也展示出来 — alias 反推只能
    // 看到被某个 alias 引用过的 model, 群里其余白名单 model 会漏掉.
    const [g, a] = await Promise.all([keysApi.getGroups(), aliasesApi.list()]);
    groups.value = (g || [])
      .filter(x => x.id != null)
      .map(x => ({
        id: x.id as number,
        name: x.name,
        display: x.display_name || x.name,
        group_type: x.group_type,
        exposed_models: Array.isArray(x.exposed_models)
          ? (x.exposed_models as string[])
          : undefined,
        available_models: parseAvailableModels(x.available_models),
        upstreams: Array.isArray(x.upstreams)
          ? (x.upstreams as Array<{ url?: string }>)
          : undefined,
      }));
    // 拉每个 aggregate group 的 sub-groups (并发, 通常只有几个 aggregate)
    const aggregates = groups.value.filter(g => g.group_type === "aggregate");
    const pairs = await Promise.all(
      aggregates.map(async ag => {
        try {
          const subs = await keysApi.getSubGroups(ag.id);
          return [ag.id, subs.map(s => s.group.id).filter((x): x is number => x != null)] as const;
        } catch {
          return [ag.id, [] as number[]] as const;
        }
      }),
    );
    aggregateChildren.value = new Map(pairs.map(([k, v]) => [k, v]));
    aliases.value =
      (a as unknown as { data?: ModelAliasRow[] }).data ||
      (a as unknown as ModelAliasRow[]) ||
      [];
  } catch (e) {
    message.error(t("playground.loadModelsFailed", { msg: (e as Error).message }));
  }
  if (sessions.value.length === 0) {
    newSession();
  } else {
    // 优先恢复上次活跃的 session (持久化在 ACTIVE_ID_KEY), 失效时退到第一个
    const wantedId = activeId.value;
    const exists = wantedId && sessions.value.some(s => s.id === wantedId);
    activeId.value = exists ? wantedId : sessions.value[0].id;
  }
});

function fmtTime(t: number) {
  const d = new Date(t);
  const now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  if (sameDay) {
    return `${d.getHours().toString().padStart(2, "0")}:${d.getMinutes().toString().padStart(2, "0")}`;
  }
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

// 气泡外部元信息:hh:mm:ss
function fmtClock(t: number) {
  const d = new Date(t);
  return [d.getHours(), d.getMinutes(), d.getSeconds()]
    .map(n => n.toString().padStart(2, "0"))
    .join(":");
}

// 持续时间:小于 1s 用 ms,小于 60s 用 1 位小数 s,大于则 mm:ss
function fmtDuration(ms: number) {
  if (ms < 1000) {
    return `${Math.round(ms)} ms`;
  }
  if (ms < 60_000) {
    return `${(ms / 1000).toFixed(1)} s`;
  }
  const total = Math.round(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}m ${s}s`;
}

// 计算 assistant 的元数据展示行 — 仅在 done 状态展示
function asstMeta(m: ChatMessage): string {
  if (m.role !== "assistant" || m.phase !== "done") {
    return "";
  }
  const parts: string[] = [];
  if (m.sentAt) {
    parts.push(fmtClock(m.sentAt));
  }
  if (m.firstByteAt && m.sentAt) {
    parts.push(t("playground.metaFirstByte", { dur: fmtDuration(m.firstByteAt - m.sentAt) }));
  }
  if (m.doneAt && m.sentAt) {
    parts.push(t("playground.metaTotal", { dur: fmtDuration(m.doneAt - m.sentAt) }));
  }
  if (m.usage) {
    const u = m.usage;
    if (typeof u.prompt_tokens === "number" || typeof u.completion_tokens === "number") {
      parts.push(
        t("playground.metaTok", {
          in: u.prompt_tokens ?? "-",
          out: u.completion_tokens ?? "-",
        }),
      );
    }
    if (
      typeof u.completion_tokens === "number" &&
      m.doneAt &&
      m.firstByteAt &&
      m.doneAt > m.firstByteAt
    ) {
      const rate = (u.completion_tokens / ((m.doneAt - m.firstByteAt) / 1000)).toFixed(1);
      parts.push(t("playground.metaTokRate", { rate }));
    }
  }
  return parts.join("  ·  ");
}

function modalityIcon(m: Modality): string {
  if (m === "image") return "🎨";
  if (m === "video") return "🎬";
  return "💬";
}
function modalityLabel(m: Modality): string {
  if (m === "image") return t("playground.modalityImageLabel");
  if (m === "video") return t("playground.modalityVideoLabel");
  return t("playground.modalityChatLabel");
}
</script>

<template>
  <div class="pg">
    <!-- 左侧 sessions -->
    <aside class="pg__side">
      <button class="pg__new" @click="newSession">
        <n-icon :component="AddOutline" :size="14" />
        {{ t("playground.newSession") }}
      </button>
      <div class="pg__list">
        <div
          v-for="s in sessions"
          :key="s.id"
          class="pg__item"
          :class="{ 'pg__item--active': s.id === activeId }"
          @click="activeId = s.id"
        >
          <div class="pg__item-title">{{ s.title }}</div>
          <div class="pg__item-meta">{{ fmtTime(s.updatedAt) }}</div>
          <n-popconfirm @positive-click="deleteSession(s.id)">
            <template #trigger>
              <button class="pg__item-del" @click.stop>
                <n-icon :component="TrashOutline" :size="12" />
              </button>
            </template>
            {{ t("playground.deleteConfirm") }}
          </n-popconfirm>
        </div>
      </div>
    </aside>

    <!-- 右侧 chat -->
    <main class="pg__main">
      <div v-if="!active" class="pg__empty">
        <n-icon :component="ChatbubbleEllipsesOutline" :size="48" />
        <div>{{ t("playground.emptyNoActive") }}</div>
      </div>
      <template v-else>
        <!-- System prompt — 多行可调高度, 跟下方输入框样式一致 -->
        <div class="pg__sys">
          <textarea
            v-model="systemPrompt"
            class="pg__sys-input"
            rows="2"
            :placeholder="t('playground.systemPromptPlaceholder')"
          />
        </div>

        <!-- 消息流 -->
        <div ref="msgBoxRef" class="pg__msgs">
          <div v-if="active.messages.length === 0" class="pg__empty pg__empty--inset">
            <div>{{ t("playground.emptyConversation") }}</div>
          </div>
          <div
            v-for="(m, i) in active.messages"
            :key="i"
            class="pg__row"
            :class="`pg__row--${m.role}`"
          >
          <div
            class="pg__msg"
            :class="[
              `pg__msg--${m.role}`,
              m.error ? 'pg__msg--error' : '',
              m.phase === 'streaming' || m.phase === 'thinking' ? 'pg__msg--live' : '',
            ]"
          >
            <div class="pg__msg-head">
              <span class="pg__msg-role">{{ m.role }}</span>
              <!-- phase 反馈: 正在思考 / 正在回复 -->
              <span v-if="m.phase === 'thinking'" class="pg__phase pg__phase--thinking">
                <span class="pg__dots"><span /><span /><span /></span>
                {{ t("playground.phaseThinking") }}
              </span>
              <span v-else-if="m.phase === 'streaming'" class="pg__phase pg__phase--streaming">
                <span class="pg__dots"><span /><span /><span /></span>
                {{ t("playground.phaseStreaming") }}
              </span>
            </div>

            <!-- 思维链折叠块: 有 thinking 内容才显示 -->
            <details
              v-if="m.thinking"
              class="pg__thinking"
              :open="m.phase === 'thinking' || !m.content"
            >
              <summary>{{ t("playground.thinkingHeader", { n: m.thinking.length }) }}</summary>
              <div class="pg__thinking-body">{{ m.thinking }}</div>
            </details>

            <!-- user 附件缩略图 (图片) -->
            <div v-if="m.role === 'user' && m.attachments && m.attachments.length" class="pg__atts">
              <img
                v-for="(a, ai) in m.attachments"
                :key="ai"
                :src="a.dataUrl"
                :alt="a.name"
                class="pg__att-thumb"
              />
            </div>
            <!-- 正文: markdown 渲染 (assistant 才走 markdown) -->
            <div
              v-if="m.role === 'assistant' && !m.error && m.content"
              class="pg__msg-body pg__md"
              v-html="renderMarkdown(m.content)"
            />
            <div v-else-if="m.content" class="pg__msg-body">{{ m.content }}</div>
            <!-- 空 body 的占位 (只在尚未拿到 content 且不是 thinking 时显示 ...) -->
            <div
              v-else-if="m.phase === 'streaming' || (m.phase === 'thinking' && !m.thinking)"
              class="pg__msg-body pg__msg-body--placeholder"
            >
              <span class="pg__cursor" />
            </div>
          </div>
          <!-- 气泡外部元信息: 时间 / 耗时 / token / 速率 -->
          <div v-if="m.role === 'user' && m.sentAt" class="pg__meta pg__meta--user">
            {{ fmtClock(m.sentAt) }}
          </div>
          <div v-else-if="asstMeta(m)" class="pg__meta pg__meta--assistant">
            {{ asstMeta(m) }}
          </div>
          </div>
        </div>

        <!-- composer: pending 附件 + textarea + 底部 toolbar -->
        <div class="pg__compose">
          <!-- 隐藏 file input — 通过附件按钮 click 触发 -->
          <input
            ref="fileInputRef"
            type="file"
            accept="image/*"
            multiple
            style="display: none"
            @change="onFileChange"
          />
          <div v-if="pendingAttachments.length" class="pg__pending">
            <div
              v-for="(a, ai) in pendingAttachments"
              :key="ai"
              class="pg__pending-item"
            >
              <img :src="a.dataUrl" :alt="a.name" class="pg__pending-thumb" />
              <button
                class="pg__pending-rm"
                :title="t('playground.attachmentRemove')"
                @click="removePending(ai)"
              >
                <n-icon :component="CloseOutline" :size="10" />
              </button>
            </div>
          </div>
          <textarea
            v-model="input"
            rows="3"
            class="pg__textarea"
            :placeholder="t('playground.composePlaceholder')"
            @keydown.enter.exact.prevent="send"
            @keydown.enter.shift.stop
            @paste="onPaste"
          />

          <!-- footer toolbar -->
          <div class="pg__toolbar">
            <!-- model picker — 向上浮 -->
            <div ref="pickerWrapRef" class="pg__picker pg__picker--up">
              <button class="pg__tool pg__tool--model" :class="{ 'pg__tool--open': pickerOpen }" @click="togglePicker">
                <span class="pg__tool-text">
                  {{ activeModelLabel || t("playground.pickerLabel") }}
                </span>
                <n-icon :component="ChevronDownOutline" :size="12" />
              </button>
              <!-- entries 总数 ≤ 阈值 → dropdown 浮层 -->
              <div v-if="pickerOpen && !useModalPicker" class="pg-pick pg-pick--up">
                <div class="pg-pick__search">
                  <n-icon :component="SearchOutline" :size="14" />
                  <input v-model="pickerSearch" :placeholder="t('playground.pickerSearchPlaceholder')" />
                  <button v-if="pickerSearch" class="pg-pick__clear" @click="pickerSearch = ''">
                    <n-icon :component="CloseOutline" :size="12" />
                  </button>
                </div>
                <div class="pg-pick__body">
                  <div v-if="filteredSections.length === 0" class="pg-pick__empty">
                    {{ sections.length === 0 ? t("playground.noProviders") : t("playground.noMatch") }}
                  </div>
                  <div v-for="sec in filteredSections" :key="sec.group.id" class="pg-pick__section">
                    <div class="pg-pick__sec-head">
                      <span class="pg-pick__sec-name">{{ sec.group.display }}</span>
                      <span class="pg-pick__sec-id">{{ sec.group.name }}</span>
                    </div>
                    <div class="pg-pick__entries">
                      <div
                        v-for="e in sec.entries"
                        :key="`${e.groupName}-${e.kind}-${e.name}`"
                        class="pg-pick__entry"
                        :class="{ 'pg-pick__entry--current': isCurrentEntry(e) }"
                        @click="pickModel(e)"
                      >
                        <span class="pg-pick__entry-kind" :class="`pg-pick__entry-kind--${e.kind}`">
                          {{ e.kind }}
                        </span>
                        <span class="pg-pick__entry-name">{{ e.name }}</span>
                        <span v-if="e.hint" class="pg-pick__entry-hint">{{ e.hint }}</span>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <!-- 图片浮层(最近 + 上传) -->
            <div ref="imgWrapRef" class="pg__picker pg__picker--up">
              <button
                class="pg__tool pg__tool--icon"
                :class="{ 'pg__tool--open': imgPickerOpen }"
                :title="t('playground.attachImage')"
                @click="imgPickerOpen = !imgPickerOpen"
              >
                <n-icon :component="ImageOutline" :size="16" />
              </button>
              <div v-if="imgPickerOpen" class="pg-imgpick">
                <div class="pg-imgpick__head">
                  <span class="pg-imgpick__title">{{ t("playground.recentImages") }}</span>
                  <button class="pg-imgpick__upload" @click="openFilePicker">
                    <n-icon :component="AddOutline" :size="12" />
                    {{ t("playground.uploadNew") }}
                  </button>
                </div>
                <div v-if="recentImages.length === 0" class="pg-imgpick__empty">
                  {{ t("playground.noRecentImages") }}
                </div>
                <div v-else class="pg-imgpick__grid">
                  <div
                    v-for="(a, ai) in recentImages"
                    :key="ai"
                    class="pg-imgpick__item"
                    @click="pickFromRecent(a)"
                  >
                    <img :src="a.dataUrl" :alt="a.name" class="pg-imgpick__thumb" />
                    <button
                      class="pg-imgpick__rm"
                      :title="t('playground.attachmentRemove')"
                      @click.stop="removeRecentImage(ai)"
                    >
                      <n-icon :component="CloseOutline" :size="10" />
                    </button>
                  </div>
                </div>
              </div>
            </div>

            <!-- 参数(temperature / max_tokens) -->
            <div ref="settingsWrapRef" class="pg__picker pg__picker--up">
              <button
                class="pg__tool pg__tool--icon"
                :class="{ 'pg__tool--open': settingsOpen }"
                :title="t('playground.settings')"
                @click="settingsOpen = !settingsOpen"
              >
                <n-icon :component="OptionsOutline" :size="16" />
              </button>
              <div v-if="settingsOpen" class="pg-settings">
                <label class="pg-settings__row">
                  <span>temperature</span>
                  <NInputNumber
                    v-model:value="temperature"
                    :min="0"
                    :max="2"
                    :step="0.1"
                    size="tiny"
                    style="width: 84px"
                  />
                </label>
                <label class="pg-settings__row">
                  <span>max_tokens</span>
                  <NInputNumber
                    v-model:value="maxTokens"
                    :min="1"
                    :max="32768"
                    :step="128"
                    size="tiny"
                    style="width: 96px"
                  />
                </label>
                <div class="pg-settings__row pg-settings__row--col">
                  <span>{{ t("playground.historyMode") }}</span>
                  <div class="pg-seg">
                    <button
                      class="pg-seg__btn"
                      :class="{ 'pg-seg__btn--active': historyMode === 'all' }"
                      :title="t('playground.historyModeAllHint')"
                      @click="historyMode = 'all'"
                    >
                      {{ t("playground.historyModeAll") }}
                    </button>
                    <button
                      class="pg-seg__btn"
                      :class="{ 'pg-seg__btn--active': historyMode === 'none' }"
                      :title="t('playground.historyModeNoneHint')"
                      @click="historyMode = 'none'"
                    >
                      {{ t("playground.historyModeNone") }}
                    </button>
                    <button
                      class="pg-seg__btn"
                      :class="{ 'pg-seg__btn--active': historyMode === 'manual' }"
                      :title="t('playground.historyModeManualHint')"
                      @click="historyMode = 'manual'"
                    >
                      {{ t("playground.historyModeManual") }}
                    </button>
                  </div>
                  <!-- manual 时显示 N 输入 -->
                  <div v-if="historyMode === 'manual'" class="pg-settings__row" style="padding: 4px 0 0">
                    <span>{{ t("playground.historyManualLabel") }}</span>
                    <NInputNumber
                      v-model:value="historyManualCount"
                      :min="0"
                      :max="200"
                      :step="1"
                      size="tiny"
                      style="width: 84px"
                    />
                  </div>
                </div>
              </div>
            </div>

            <div class="pg__toolbar-spacer" />

            <button
              class="pg__send"
              :disabled="sending || (!input.trim() && pendingAttachments.length === 0)"
              @click="send"
            >
              <n-icon :component="SendOutline" :size="14" />
              {{ sending ? t("playground.sending") : t("playground.send") }}
            </button>
          </div>
        </div>
      </template>
    </main>

    <!-- 全屏 model picker modal: 总 entries > 阈值时启用 -->
    <NModal
      :show="pickerOpen && useModalPicker"
      preset="card"
      :title="t('playground.pickerLabel')"
      :style="{ width: '880px', maxHeight: '78vh' }"
      class="pg-modal"
      @update:show="pickerOpen = $event"
    >
      <div class="pg-modal__top">
        <div class="pg-modal__search">
          <n-icon :component="SearchOutline" :size="14" />
          <input
            v-model="pickerSearch"
            :placeholder="t('playground.pickerSearchPlaceholder')"
            autofocus
          />
          <button v-if="pickerSearch" class="pg-pick__clear" @click="pickerSearch = ''">
            <n-icon :component="CloseOutline" :size="12" />
          </button>
        </div>
        <div class="pg-modal__view-toggle">
          <button
            class="pg-modal__view-btn"
            :class="{ 'pg-modal__view-btn--active': pickerView === 'list' }"
            :title="t('playground.viewList')"
            @click="pickerView = 'list'"
          >
            <n-icon :component="ListOutline" :size="14" />
          </button>
          <button
            class="pg-modal__view-btn"
            :class="{ 'pg-modal__view-btn--active': pickerView === 'grid' }"
            :title="t('playground.viewGrid')"
            @click="pickerView = 'grid'"
          >
            <n-icon :component="GridOutline" :size="14" />
          </button>
        </div>
      </div>
      <!-- Provider tab 区 (flex-wrap 折行, 不滚) — ALL 在最前 -->
      <div class="pg-modal__tabs">
        <button
          class="pg-modal__tab pg-modal__tab--all"
          :class="{ 'pg-modal__tab--active': activeTabGroupId === null }"
          @click="activeTabGroupId = null"
        >
          <span class="pg-modal__tab-name">{{ t("playground.allProviders") }}</span>
          <span class="pg-modal__tab-count">{{ totalFilteredCount }}</span>
        </button>
        <button
          v-for="sec in filteredSections"
          :key="sec.group.id"
          class="pg-modal__tab"
          :class="{
            'pg-modal__tab--active': activeTabGroupId === sec.group.id,
            'pg-modal__tab--current': currentGroupId === sec.group.id,
          }"
          @click="activeTabGroupId = sec.group.id"
        >
          <span class="pg-modal__tab-logo">
            <ProviderLogo
              :hint="logoHintForGroup(sec.group) || sec.group.name"
              :host="sec.group.upstreams?.[0]?.url"
              :fallback-initial="sec.group.display"
              :size="16"
              style="border-radius: 3px"
            />
          </span>
          <span class="pg-modal__tab-name">{{ sec.group.display }}</span>
          <span v-if="currentGroupId === sec.group.id" class="pg-modal__tab-dot" />
          <span class="pg-modal__tab-count">{{ sec.entries.length }}</span>
        </button>
      </div>

      <!-- 卡片区:别名 + 模型 两个 section -->
      <div class="pg-modal__sections">
        <div v-if="activeTabEntries.length === 0" class="pg-modal__empty">
          {{ sections.length === 0 ? t("playground.noProviders") : t("playground.noMatch") }}
        </div>

        <!-- ===== 卡片视图(分组保留 — alias/model 两个 section) ===== -->
        <template v-if="pickerView === 'grid'">
          <template v-for="(group, idx) in [
            { kind: 'alias', title: t('playground.sectionAliases'), entries: activeAliasEntries },
            { kind: 'model', title: t('playground.sectionModels'), entries: activeModelEntries },
          ]" :key="`gv-${idx}`">
            <div v-if="group.entries.length" class="pg-modal__section">
              <div class="pg-modal__section-head">
                <span class="pg-modal__section-title">{{ group.title }}</span>
                <span class="pg-modal__section-count">{{ group.entries.length }}</span>
              </div>
              <div class="pg-modal__cards">
                <div
                  v-for="e in group.entries"
                  :key="`g-${e.groupName}-${e.kind}-${e.name}`"
                  class="pg-card"
                  :class="[`pg-card--${e.kind}`, { 'pg-card--current': isCurrentEntry(e) }]"
                  @click="pickModel(e)"
                >
                  <div class="pg-card__top">
                    <span class="pg-card__name" :title="e.name">{{ e.name }}</span>
                    <span v-if="e.modality !== 'chat'"
                      class="pg-list-row__tag" :class="`pg-list-row__tag--${e.modality}`"
                      :title="modalityLabel(e.modality)">
                      {{ modalityIcon(e.modality) }} {{ modalityLabel(e.modality) }}
                    </span>
                    <span v-if="e.isFree && e.kind === 'model'" class="pg-list-row__tag pg-list-row__tag--free">
                      {{ t("playground.freeTag") }}
                    </span>
                  </div>
                  <div class="pg-card__row">
                    <span v-if="activeTabGroupId === null" class="pg-card__group">{{ e.groupDisplay }}</span>
                    <span v-if="e.hint" class="pg-card__hint" :title="e.hint">{{ e.hint }}</span>
                  </div>
                </div>
              </div>
            </div>
          </template>
        </template>

        <!-- ===== 列表视图(table 形态) ===== -->
        <template v-else>
          <!-- 列表视图工具行 — filter chips + switch toggles -->
          <div class="pg-modal__list-tools">
            <div class="pg-filter">
              <button
                class="pg-filter__chip"
                :class="{ 'pg-filter__chip--active': filterType === 'all' }"
                @click="filterType = 'all'"
              >
                {{ t("playground.filterAll") }}
              </button>
              <button
                class="pg-filter__chip pg-filter__chip--alias"
                :class="{ 'pg-filter__chip--active': filterType === 'alias' }"
                @click="filterType = 'alias'"
              >
                {{ t("playground.sectionAliases") }}
              </button>
              <button
                class="pg-filter__chip pg-filter__chip--model"
                :class="{ 'pg-filter__chip--active': filterType === 'model' }"
                @click="filterType = 'model'"
              >
                {{ t("playground.sectionModels") }}
              </button>
              <div class="pg-filter__sw">
                <NSwitch v-model:value="filterFreeOnly" size="small" />
                <span>{{ t("playground.filterFreeOnly") }}</span>
              </div>
              <div class="pg-filter__sw" :title="t('playground.showAllModelsHint')">
                <NSwitch v-model:value="showAllModels" size="small" />
                <span>{{ t("playground.showAllModels") }}</span>
              </div>
            </div>
            <div class="pg-filter__sw">
              <NSwitch v-model:value="groupByKind" size="small" />
              <span>{{ t("playground.groupByKind") }}</span>
            </div>
          </div>

          <!-- 不分组(默认): 一个 table -->
          <template v-if="!groupByKind">
            <div
              class="pg-modal__list"
              :class="{ 'pg-modal__list--with-provider': activeTabGroupId === null }"
            >
              <div class="pg-list-head">
                <div
                  class="pg-list-head__cell pg-list-head__cell--sortable"
                  :class="{ 'pg-list-head__cell--active': sortBy !== 'default' }"
                  @click="cycleNameSort"
                >
                  {{ t("playground.colName") }}
                  <span v-if="sortBy === 'name_asc'" class="pg-list-head__arrow">▲</span>
                  <span v-else-if="sortBy === 'name_desc'" class="pg-list-head__arrow">▼</span>
                  <span v-else class="pg-list-head__arrow pg-list-head__arrow--idle">⇅</span>
                </div>
                <div class="pg-list-head__cell">{{ t("playground.colType") }}</div>
                <div v-if="activeTabGroupId === null" class="pg-list-head__cell">
                  {{ t("playground.colProvider") }}
                </div>
                <div class="pg-list-head__cell">{{ t("playground.colDescription") }}</div>
              </div>
              <div
                v-for="e in mergedEntries"
                :key="`lm-${e.groupName}-${e.kind}-${e.name}`"
                class="pg-list-row"
                :class="[`pg-list-row--${e.kind}`, { 'pg-list-row--current': isCurrentEntry(e) }]"
                @click="pickModel(e)"
              >
                <div class="pg-list-row__name" :title="e.name">{{ e.name }}</div>
                <div class="pg-list-row__type">
                  <span v-if="e.kind === 'alias'" class="pg-list-row__tag pg-list-row__tag--alias">
                    {{ t("playground.aliasTag") }}
                  </span>
                  <span v-if="e.modality !== 'chat'"
                    class="pg-list-row__tag" :class="`pg-list-row__tag--${e.modality}`"
                    :title="modalityLabel(e.modality)">
                    {{ modalityIcon(e.modality) }} {{ modalityLabel(e.modality) }}
                  </span>
                  <span v-if="e.isFree && e.kind === 'model'" class="pg-list-row__tag pg-list-row__tag--free">
                    {{ t("playground.freeTag") }}
                  </span>
                </div>
                <div v-if="activeTabGroupId === null" class="pg-list-row__provider">
                  <ProviderLogo
                    :hint="logoHintForGroup({ name: e.groupName, display: e.groupDisplay }) || e.groupName"
                    :host="e.groupHost"
                    :fallback-initial="e.groupDisplay"
                    :size="14"
                    style="border-radius: 3px"
                  />
                  <span>{{ e.groupDisplay }}</span>
                </div>
                <div class="pg-list-row__hint" :title="e.hint || ''">
                  {{ e.hint || "—" }}
                </div>
              </div>
            </div>
          </template>

          <!-- 分组: 两个 table(ALIASES / MODELS) -->
          <template v-else>
            <template v-for="(group, idx) in [
              { kind: 'alias', title: t('playground.sectionAliases'), entries: activeAliasEntries },
              { kind: 'model', title: t('playground.sectionModels'), entries: activeModelEntries },
            ]" :key="`lvg-${idx}`">
              <div v-if="group.entries.length" class="pg-modal__section">
                <div class="pg-modal__section-head">
                  <span class="pg-modal__section-title">{{ group.title }}</span>
                  <span class="pg-modal__section-count">{{ group.entries.length }}</span>
                </div>
                <div
                  class="pg-modal__list"
                  :class="{ 'pg-modal__list--with-provider': activeTabGroupId === null }"
                >
                  <div class="pg-list-head">
                    <div class="pg-list-head__cell">{{ t("playground.colName") }}</div>
                    <div class="pg-list-head__cell">{{ t("playground.colType") }}</div>
                    <div v-if="activeTabGroupId === null" class="pg-list-head__cell">
                      {{ t("playground.colProvider") }}
                    </div>
                    <div class="pg-list-head__cell">{{ t("playground.colDescription") }}</div>
                  </div>
                  <div
                    v-for="e in group.entries"
                    :key="`l-${e.groupName}-${e.kind}-${e.name}`"
                    class="pg-list-row"
                    :class="[`pg-list-row--${e.kind}`, { 'pg-list-row--current': isCurrentEntry(e) }]"
                    @click="pickModel(e)"
                  >
                    <div class="pg-list-row__name" :title="e.name">{{ e.name }}</div>
                    <div class="pg-list-row__type">
                      <span v-if="e.kind === 'alias'" class="pg-list-row__tag">
                        {{ t("playground.aliasTag") }}
                      </span>
                    </div>
                    <div v-if="activeTabGroupId === null" class="pg-list-row__provider">
                      <ProviderLogo
                        v-if="logoHintForGroup({ name: e.groupName, display: e.groupDisplay })"
                        :hint="logoHintForGroup({ name: e.groupName, display: e.groupDisplay })!"
                        :size="14"
                        style="border-radius: 3px"
                      />
                      <span>{{ e.groupDisplay }}</span>
                    </div>
                    <div class="pg-list-row__hint" :title="e.hint || ''">
                      {{ e.hint || "—" }}
                    </div>
                  </div>
                </div>
              </div>
            </template>
          </template>
        </template>
      </div>

      <!-- 手动输入: 即使列表里没这个 model 也可以试 -->
      <div class="pg-modal__manual">
        <span class="pg-modal__manual-label">{{ t("playground.manualInputLabel") }}</span>
        <input
          v-model="manualModelName"
          class="pg-modal__manual-input"
          :placeholder="t('playground.manualInputPlaceholder')"
          @keydown.enter="pickManual"
        />
        <button
          class="pg-modal__manual-btn"
          :disabled="!manualModelName.trim() || activeTabGroupId === null"
          @click="pickManual"
        >
          {{ t("playground.manualInputBtn") }}
        </button>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.pg {
  flex: 1;
  display: flex;
  /* Layout 的 .v3-main-content > * 强制 flex-direction: column, 显式覆盖回 row 让 sidebar+main 横向排列 */
  flex-direction: row;
  min-height: 0;
  height: 100%;
  overflow: hidden;
  background: #fff;
}
/* ===== 左侧 ===== */
.pg__side {
  width: 220px;
  flex-shrink: 0;
  border-right: 1px solid var(--v3-line, #eee);
  display: flex;
  flex-direction: column;
  background: var(--v3-bg-2, #fafbfc);
}
.pg__new {
  margin: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 8px 12px;
  border: 1px dashed var(--v3-line, #ccd);
  border-radius: 6px;
  background: #fff;
  cursor: pointer;
  font: 500 13px var(--v3-sans, sans-serif);
  color: var(--v3-ink, #333);
}
.pg__new:hover {
  border-color: var(--v3-accent, #2b5cff);
  color: var(--v3-accent, #2b5cff);
}
.pg__list {
  flex: 1;
  overflow-y: auto;
  padding-bottom: 12px;
}
.pg__item {
  position: relative;
  margin: 0 8px 4px;
  padding: 8px 28px 8px 10px;
  border-radius: 6px;
  cursor: pointer;
}
.pg__item:hover {
  background: rgba(0, 0, 0, 0.04);
}
.pg__item--active {
  background: rgba(43, 92, 255, 0.08);
}
.pg__item-title {
  font: 500 13px var(--v3-sans, sans-serif);
  color: var(--v3-ink, #222);
  margin-bottom: 2px;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.pg__item-meta {
  font: 400 11px var(--v3-mono, monospace);
  color: var(--v3-ink-3, #999);
}
.pg__item-del {
  position: absolute;
  top: 8px;
  right: 4px;
  background: none;
  border: none;
  color: var(--v3-ink-3, #aaa);
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  visibility: hidden;
}
.pg__item:hover .pg__item-del,
.pg__item--active .pg__item-del {
  visibility: visible;
}
.pg__item-del:hover {
  color: #e44;
  background: rgba(228, 68, 68, 0.1);
}

/* ===== 右侧 ===== */
.pg__main {
  flex: 1;
  /* grid template: header (system prompt) · 1fr (messages) · auto (composer)
     比 flex 更可靠 — 1fr 自动算精确剩余高度, 配合 overflow:hidden 限定边界. */
  display: grid;
  grid-template-rows: auto 1fr auto;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}
.pg__picker {
  position: relative;
}
/* footer toolbar 上的按钮 — 紧凑形态, 共用 pg__tool */
.pg__tool {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--v3-line, #ddd);
  border-radius: 6px;
  background: #fff;
  cursor: pointer;
  font: 500 12px var(--v3-sans, sans-serif);
  color: var(--v3-ink-2, #444);
}
.pg__tool:hover {
  border-color: var(--v3-accent, #2b5cff);
  color: var(--v3-accent, #2b5cff);
}
.pg__tool--open {
  border-color: var(--v3-accent, #2b5cff);
  color: var(--v3-accent, #2b5cff);
  box-shadow: 0 0 0 2px rgba(43, 92, 255, 0.1);
}
.pg__tool--icon {
  padding: 0;
  width: 32px;
  justify-content: center;
}
.pg__tool--model {
  max-width: 340px;
}
.pg__tool-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pg__sys {
  padding: 10px 14px;
  border-bottom: 1px solid var(--v3-line, #eee);
  flex-shrink: 0;
  background: #fff;
}
.pg__sys-input {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--v3-line, #ddd);
  border-radius: 6px;
  background: #fff;
  font: 400 12.5px var(--v3-mono, monospace);
  color: var(--v3-ink, #222);
  outline: none;
  resize: vertical;
  min-height: 38px;
  line-height: 1.5;
}
.pg__sys-input:focus {
  border-color: var(--v3-accent, #5b8dff);
}
.pg__msgs {
  /* grid 1fr 自动算高度, 不需要 flex:1; 内部超出走自身 overflow-y 滚动 */
  overflow-y: auto;
  padding: 16px 24px;
  background: var(--v3-bg-2, #fafbfc);
  min-height: 0;
  display: flex;
  flex-direction: column;
}
/* row 容器把气泡和 meta 行垂直堆叠, 整 row 再左右靠齐 */
.pg__row {
  display: flex;
  flex-direction: column;
  margin-bottom: 14px;
  max-width: 75%;
}
.pg__row--user {
  margin-left: auto;
  align-items: flex-end;
}
.pg__row--assistant,
.pg__row--system {
  margin-right: auto;
  align-items: flex-start;
}
.pg__msg {
  padding: 10px 14px;
  border-radius: 12px;
  background: #fff;
  border: 1px solid var(--v3-line, #eee);
}
/* user 气泡 */
.pg__msg--user {
  background: var(--v3-accent, #2b5cff);
  border-color: var(--v3-accent, #2b5cff);
  color: #fff;
  border-bottom-right-radius: 4px;
}
.pg__msg--user .pg__msg-role {
  color: rgba(255, 255, 255, 0.65);
}
.pg__msg--user .pg__msg-body {
  color: #fff;
}
/* assistant 气泡 */
.pg__msg--assistant {
  border-bottom-left-radius: 4px;
}
.pg__msg--system,
.pg__msg--error {
  border-bottom-left-radius: 4px;
}
.pg__msg--error {
  background: #fff5f5;
  border-color: #ffb3b3;
}
.pg__msg-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.pg__msg-role {
  font: 600 10px var(--v3-mono, monospace);
  text-transform: uppercase;
  color: var(--v3-ink-3, #888);
}
.pg__msg--error .pg__msg-role {
  color: #c33;
}
.pg__msg-body {
  font: 400 14px/1.55 var(--v3-sans, sans-serif);
  color: var(--v3-ink, #222);
  white-space: pre-wrap;
  word-break: break-word;
}
.pg__msg--error .pg__msg-body {
  color: #a22;
  font-family: var(--v3-mono, monospace);
  font-size: 12px;
}

/* 气泡外部元信息行 */
.pg__meta {
  margin-top: 4px;
  font: 400 11px var(--v3-mono, monospace);
  color: var(--v3-ink-3, #999);
  padding: 0 4px;
}
.pg__meta--user {
  text-align: right;
}
.pg__meta--assistant {
  text-align: left;
}

/* ===== phase 反馈 ===== */
.pg__phase {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font: 500 11px var(--v3-sans, sans-serif);
  padding: 2px 8px;
  border-radius: 10px;
}
.pg__phase--thinking {
  background: rgba(140, 90, 220, 0.12);
  color: #7d3cdb;
}
.pg__phase--streaming {
  background: rgba(43, 92, 255, 0.12);
  color: #2b5cff;
}
.pg__dots {
  display: inline-flex;
  gap: 2px;
}
.pg__dots > span {
  display: inline-block;
  width: 3px;
  height: 3px;
  border-radius: 50%;
  background: currentColor;
  animation: pg-dot 1.2s ease-in-out infinite;
}
.pg__dots > span:nth-child(2) { animation-delay: 0.2s; }
.pg__dots > span:nth-child(3) { animation-delay: 0.4s; }
@keyframes pg-dot {
  0%, 100% { opacity: 0.3; transform: translateY(0); }
  50% { opacity: 1; transform: translateY(-2px); }
}

/* live 状态气泡描边脉动 */
.pg__msg--live {
  animation: pg-pulse 2s ease-in-out infinite;
}
@keyframes pg-pulse {
  0%, 100% { box-shadow: 0 0 0 1px rgba(43, 92, 255, 0.15); }
  50% { box-shadow: 0 0 0 2px rgba(43, 92, 255, 0.35); }
}

/* 占位光标 */
.pg__msg-body--placeholder {
  display: flex;
  align-items: center;
  min-height: 20px;
}
.pg__cursor {
  display: inline-block;
  width: 7px;
  height: 14px;
  background: var(--v3-ink-3, #888);
  opacity: 0.6;
  animation: pg-blink 1s steps(2) infinite;
}
@keyframes pg-blink {
  0%, 49% { opacity: 0.6; }
  50%, 100% { opacity: 0; }
}

/* ===== thinking 折叠块 ===== */
.pg__thinking {
  margin-bottom: 8px;
  border-left: 3px solid rgba(140, 90, 220, 0.4);
  padding: 4px 0 4px 10px;
  background: rgba(140, 90, 220, 0.04);
  border-radius: 0 4px 4px 0;
}
.pg__thinking > summary {
  cursor: pointer;
  font: 500 11px var(--v3-mono, monospace);
  color: #7d3cdb;
  user-select: none;
  outline: none;
}
.pg__thinking > summary:hover { text-decoration: underline; }
.pg__thinking-body {
  margin-top: 6px;
  font: 400 12px/1.5 var(--v3-mono, monospace);
  color: var(--v3-ink-2, #555);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 360px;
  overflow-y: auto;
}

/* ===== Markdown =====
   关键: 覆盖 .pg__msg-body 的 white-space: pre-wrap. markdown-it 渲染出
   的 HTML 块标签之间会有真换行符 (\n), pre-wrap 把它们当成可见换行渲染,
   叠加 <p> 自身 margin → 段落间空隙是预期的 2-3 倍. 正常 markdown 文本流
   应该 white-space: normal, 块元素 margin 自己管间距. */
.pg__md {
  white-space: normal;
}

/* 段落 + 列表 — 间距收紧, 跟 line-height 1.55 协调 */
.pg__md :deep(p) {
  margin: 0 0 0.5em;
}
.pg__md :deep(p:last-child),
.pg__md :deep(:last-child) {
  margin-bottom: 0;
}
.pg__md :deep(ul),
.pg__md :deep(ol) {
  margin: 0 0 0.5em 0;
  padding-left: 1.4em;
}
.pg__md :deep(li) {
  margin: 0.15em 0;
}
.pg__md :deep(li > p) {
  /* 列表项里直接套 <p> 时不要双倍间距 */
  margin: 0;
}
.pg__md :deep(li > ul),
.pg__md :deep(li > ol) {
  margin: 0.15em 0 0.15em 0;
}

/* 标题 — 上方间距比下方大点 (跟前文留呼吸), 但不要太离谱 */
.pg__md :deep(h1),
.pg__md :deep(h2),
.pg__md :deep(h3),
.pg__md :deep(h4),
.pg__md :deep(h5),
.pg__md :deep(h6) {
  margin: 0.9em 0 0.35em;
  font-weight: 600;
  line-height: 1.3;
}
.pg__md :deep(h1:first-child),
.pg__md :deep(h2:first-child),
.pg__md :deep(h3:first-child),
.pg__md :deep(h4:first-child) {
  margin-top: 0;
}
.pg__md :deep(h1) { font-size: 1.4em; }
.pg__md :deep(h2) { font-size: 1.22em; }
.pg__md :deep(h3) { font-size: 1.08em; }
.pg__md :deep(h4) { font-size: 1em; }

/* inline code */
.pg__md :deep(code) {
  background: rgba(140, 140, 140, 0.15);
  padding: 1px 5px;
  border-radius: 3px;
  font: 400 0.9em var(--v3-mono, "SF Mono", monospace);
}
/* code block */
.pg__md :deep(pre) {
  background: #1e2127;
  color: #e6e6e6;
  padding: 12px 14px;
  border-radius: 6px;
  overflow-x: auto;
  margin: 0.5em 0;
  line-height: 1.5;
}
.pg__md :deep(pre code) {
  background: none;
  padding: 0;
  color: inherit;
  font-size: 12.5px;
}

.pg__md :deep(blockquote) {
  border-left: 3px solid var(--v3-line, #ddd);
  padding: 0.1em 0 0.1em 12px;
  margin: 0.5em 0;
  color: var(--v3-ink-2, #666);
}

/* 表格 — 完整边框 + 紧凑 padding */
.pg__md :deep(table) {
  border-collapse: collapse;
  margin: 0.5em 0;
  font-size: 13px;
  width: auto;
  max-width: 100%;
}
.pg__md :deep(th),
.pg__md :deep(td) {
  border: 1px solid var(--v3-line, #e5e7eb);
  padding: 5px 9px;
  vertical-align: top;
}
.pg__md :deep(th) {
  background: var(--v3-bg-2, #fafbfc);
  font-weight: 600;
  text-align: left;
}

.pg__md :deep(a) {
  color: var(--v3-accent, #2b5cff);
  text-decoration: none;
}
.pg__md :deep(a:hover) {
  text-decoration: underline;
}

/* hr — 视觉分隔, 不需要那么多上下空白 */
.pg__md :deep(hr) {
  border: none;
  border-top: 1px solid var(--v3-line, #eee);
  margin: 0.7em 0;
}
.pg__compose {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 14px 12px;
  border-top: 1px solid var(--v3-line, #eee);
  background: #fff;
  flex-shrink: 0;
}
.pg__textarea {
  padding: 8px 10px;
  border: 1px solid var(--v3-line, #ddd);
  border-radius: 6px;
  font: 400 13px var(--v3-sans, sans-serif);
  resize: vertical;
  outline: none;
  width: 100%;
  min-height: 56px;
}
.pg__textarea:focus {
  border-color: var(--v3-accent, #5b8dff);
}
.pg__toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.pg__toolbar-spacer {
  flex: 1;
}
.pg__send {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 38px;
  padding: 0 18px;
  border: none;
  border-radius: 6px;
  background: var(--v3-accent, #2b5cff);
  color: #fff;
  cursor: pointer;
  font: 500 13px var(--v3-sans, sans-serif);
}
.pg__send:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* ===== 附件 ===== */
.pg__pending {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.pg__pending-item {
  position: relative;
  width: 56px;
  height: 56px;
  border-radius: 6px;
  overflow: hidden;
  border: 1px solid var(--v3-line, #ddd);
}
.pg__pending-thumb {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.pg__pending-rm {
  position: absolute;
  top: 2px;
  right: 2px;
  width: 16px;
  height: 16px;
  border: none;
  border-radius: 50%;
  background: rgba(0, 0, 0, 0.55);
  color: #fff;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0;
}
.pg__pending-rm:hover {
  background: rgba(0, 0, 0, 0.75);
}

/* 已发出的 user 气泡里的附件缩略图 */
.pg__atts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 8px;
}
.pg__att-thumb {
  max-width: 240px;
  max-height: 180px;
  border-radius: 6px;
  display: block;
  border: 1px solid rgba(255, 255, 255, 0.2);
}
.pg__empty {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  color: var(--v3-ink-3, #999);
  font: 400 14px var(--v3-sans, sans-serif);
}
/* 无 active session 的 empty 占满整个 .pg__main grid */
.pg__main > .pg__empty {
  grid-row: 1 / -1;
}
.pg__empty--inset {
  flex: 0;
  padding: 60px 0;
}

/* ===== Picker 浮层 dropdown ===== */
.pg-pick {
  position: absolute;
  left: 0;
  width: 520px;
  max-height: 60vh;
  background: #fff;
  border: 1px solid var(--v3-line, #ddd);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  display: flex;
  flex-direction: column;
  padding: 10px;
  z-index: 100;
}
/* 默认向下展开 */
.pg-pick {
  top: calc(100% + 6px);
}
/* 在 footer toolbar 里向上展开 */
.pg__picker--up .pg-pick,
.pg-pick--up {
  top: auto;
  bottom: calc(100% + 6px);
}

/* ===== 图片浮层(最近 + 上传) ===== */
.pg-imgpick {
  position: absolute;
  left: 0;
  bottom: calc(100% + 6px);
  width: 380px;
  max-height: 360px;
  background: #fff;
  border: 1px solid var(--v3-line, #ddd);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  display: flex;
  flex-direction: column;
  padding: 10px;
  z-index: 100;
}
.pg-imgpick__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.pg-imgpick__title {
  font: 600 12px var(--v3-mono, monospace);
  color: var(--v3-ink-2, #555);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.pg-imgpick__upload {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  border: 1px dashed var(--v3-line, #ccc);
  border-radius: 5px;
  background: #fff;
  cursor: pointer;
  font: 500 12px var(--v3-sans, sans-serif);
  color: var(--v3-ink-2, #444);
}
.pg-imgpick__upload:hover {
  border-color: var(--v3-accent, #2b5cff);
  color: var(--v3-accent, #2b5cff);
}
.pg-imgpick__empty {
  padding: 24px 8px;
  text-align: center;
  font: 400 12px var(--v3-sans, sans-serif);
  color: var(--v3-ink-3, #999);
}
.pg-imgpick__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(72px, 1fr));
  gap: 6px;
  overflow-y: auto;
}
.pg-imgpick__item {
  position: relative;
  aspect-ratio: 1;
  border-radius: 5px;
  overflow: hidden;
  border: 1px solid var(--v3-line, #eee);
  cursor: pointer;
}
.pg-imgpick__item:hover {
  border-color: var(--v3-accent, #2b5cff);
  box-shadow: 0 0 0 2px rgba(43, 92, 255, 0.15);
}
.pg-imgpick__thumb {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.pg-imgpick__rm {
  position: absolute;
  top: 2px;
  right: 2px;
  width: 16px;
  height: 16px;
  border: none;
  border-radius: 50%;
  background: rgba(0, 0, 0, 0.55);
  color: #fff;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0;
}

/* ===== settings 浮层 ===== */
.pg-settings {
  position: absolute;
  left: 0;
  bottom: calc(100% + 6px);
  min-width: 220px;
  background: #fff;
  border: 1px solid var(--v3-line, #ddd);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  padding: 10px 12px;
  z-index: 100;
}
.pg-settings__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font: 400 12px var(--v3-mono, monospace);
  color: var(--v3-ink-2, #555);
  padding: 4px 0;
}
.pg-settings__row--col {
  flex-direction: column;
  align-items: stretch;
  gap: 6px;
}
.pg-seg {
  display: inline-flex;
  border: 1px solid var(--v3-line, #ddd);
  border-radius: 6px;
  overflow: hidden;
}
.pg-seg__btn {
  flex: 1;
  border: none;
  background: #fff;
  cursor: pointer;
  padding: 4px 10px;
  font: 500 11px var(--v3-sans, sans-serif);
  color: var(--v3-ink-2, #555);
  border-right: 1px solid var(--v3-line, #ddd);
  white-space: nowrap;
}
.pg-seg__btn:last-child {
  border-right: none;
}
.pg-seg__btn:hover {
  background: rgba(43, 92, 255, 0.04);
}
.pg-seg__btn--active {
  background: var(--v3-accent, #2b5cff);
  color: #fff;
}
.pg-seg__btn--active:hover {
  background: var(--v3-accent, #2b5cff);
}
.pg-pick__search {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid var(--v3-line, #ddd);
  border-radius: 6px;
  margin-bottom: 12px;
  color: var(--v3-ink-3, #888);
}
.pg-pick__search input {
  flex: 1;
  border: none;
  background: transparent;
  outline: none;
  font: 400 14px var(--v3-sans, sans-serif);
  color: var(--v3-ink, #222);
}
.pg-pick__clear {
  background: none;
  border: none;
  color: var(--v3-ink-3, #999);
  cursor: pointer;
  display: flex;
}
.pg-pick__body {
  flex: 1;
  overflow-y: auto;
}
.pg-pick__empty {
  padding: 40px 16px;
  text-align: center;
  color: var(--v3-ink-3, #999);
  font: 400 13px var(--v3-sans, sans-serif);
}
.pg-pick__section {
  margin-bottom: 18px;
}
.pg-pick__sec-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 4px 0;
  border-bottom: 1px solid var(--v3-line, #eee);
  margin-bottom: 6px;
}
.pg-pick__sec-name {
  font: 600 13px var(--v3-sans, sans-serif);
  color: var(--v3-ink, #222);
}
.pg-pick__sec-id {
  font: 400 11px var(--v3-mono, monospace);
  color: var(--v3-ink-3, #888);
}
.pg-pick__entries {
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.pg-pick__entry {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 10px;
  border-radius: 5px;
  cursor: pointer;
}
.pg-pick__entry:hover {
  background: rgba(43, 92, 255, 0.06);
}
.pg-pick__entry-kind {
  font: 600 9px var(--v3-mono, monospace);
  padding: 2px 6px;
  border-radius: 3px;
  text-transform: uppercase;
  flex-shrink: 0;
}
.pg-pick__entry-kind--alias {
  background: rgba(43, 92, 255, 0.1);
  color: #2b5cff;
}
.pg-pick__entry-kind--model {
  background: rgba(140, 140, 140, 0.1);
  color: #666;
}
.pg-pick__entry-name {
  font: 500 13px var(--v3-sans, sans-serif);
  color: var(--v3-ink, #222);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pg-pick__entry-hint {
  font: 400 11px var(--v3-mono, monospace);
  color: var(--v3-ink-3, #999);
  margin-left: auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 360px;
}
</style>

<!-- Unscoped style: 仅当 body.pg-route-active 时(即 Playground 路由),
     把 Layout 的滚动容器锁住, 让 Playground 内部独立滚动. 离开路由时
     onBeforeUnmount 摘掉 class, 其他页恢复原行为. -->
<style>
body.pg-route-active .v3-main {
  overflow: hidden;
}
body.pg-route-active .v3-main-content {
  min-height: 0;
  overflow: hidden;
  padding: 0;
}
body.pg-route-active .v3-main > .app-footer {
  display: none;
}

/* ===== Picker modal (teleport 到 body, scoped 不到) ===== */
.pg-modal .pg-modal__top {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}
.pg-modal .pg-modal__search {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  color: #888;
}
.pg-modal .pg-modal__view-toggle {
  display: inline-flex;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  overflow: hidden;
  flex-shrink: 0;
}
.pg-modal .pg-modal__view-btn {
  border: none;
  background: #fff;
  cursor: pointer;
  padding: 0 10px;
  height: 38px;
  color: #6b7280;
  border-right: 1px solid #e5e7eb;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.pg-modal .pg-modal__view-btn:last-child {
  border-right: none;
}
.pg-modal .pg-modal__view-btn:hover {
  background: rgba(43, 92, 255, 0.05);
  color: #2b5cff;
}
.pg-modal .pg-modal__view-btn--active {
  background: rgba(43, 92, 255, 0.1);
  color: #2b5cff;
}
.pg-modal .pg-modal__search input {
  flex: 1;
  border: none;
  background: transparent;
  outline: none;
  font: 400 14px sans-serif;
  color: #222;
}
.pg-modal .pg-pick__clear {
  background: none;
  border: none;
  cursor: pointer;
  color: #888;
  display: flex;
}
/* Provider tab 区: 折行排列 (flex-wrap), 不滚动 */
.pg-modal .pg-modal__tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding-bottom: 10px;
  margin-bottom: 14px;
  border-bottom: 1px solid #eee;
}
.pg-modal .pg-modal__tab {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 11px 6px 7px;
  border: 1px solid #e5e7eb;
  border-radius: 6px;
  background: #fff;
  cursor: pointer;
  font: 500 13px sans-serif;
  color: #555;
}
.pg-modal .pg-modal__tab--all {
  padding-left: 12px; /* ALL 没有 logo, 平衡左 padding */
}
.pg-modal .pg-modal__tab:hover {
  border-color: #c7d4ff;
  background: rgba(43, 92, 255, 0.04);
  color: #333;
}
.pg-modal .pg-modal__tab--active {
  background: rgba(43, 92, 255, 0.1);
  color: #2b5cff;
  border-color: rgba(43, 92, 255, 0.35);
}
/* current = 当前选中的 model 所在的 provider, 即使没激活也带蓝点提示 */
.pg-modal .pg-modal__tab-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #2b5cff;
  flex-shrink: 0;
}
.pg-modal .pg-modal__tab--current:not(.pg-modal__tab--active) {
  border-color: rgba(43, 92, 255, 0.3);
}
.pg-modal .pg-modal__tab-logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  flex-shrink: 0;
}
.pg-modal .pg-modal__tab-letter {
  width: 18px;
  height: 18px;
  border-radius: 3px;
  background: linear-gradient(135deg, #a5b4fc, #818cf8);
  color: #fff;
  font: 600 10px sans-serif;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.pg-modal .pg-modal__tab-name {
  font-weight: 500;
}
.pg-modal .pg-modal__tab-count {
  font: 400 11px monospace;
  color: #999;
  background: rgba(0, 0, 0, 0.04);
  padding: 1px 6px;
  border-radius: 8px;
}
.pg-modal .pg-modal__tab--active .pg-modal__tab-count {
  color: #2b5cff;
  background: rgba(43, 92, 255, 0.12);
}

/* sections 容器 — 滚动从这里出 */
.pg-modal .pg-modal__sections {
  height: 52vh;
  min-height: 320px;
  overflow-y: auto;
  padding-right: 4px;
}
.pg-modal .pg-modal__section {
  margin-bottom: 18px;
}
.pg-modal .pg-modal__section:last-child {
  margin-bottom: 0;
}
.pg-modal .pg-modal__section-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding-bottom: 6px;
  margin-bottom: 8px;
  border-bottom: 1px solid #f0f2f5;
}
.pg-modal .pg-modal__section-title {
  font: 600 12px monospace;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: #4a5260;
}
.pg-modal .pg-modal__section-count {
  font: 400 11px monospace;
  color: #98a0ad;
}
.pg-modal .pg-modal__empty {
  padding: 36px 8px;
  text-align: center;
  color: #999;
  font: 400 13px sans-serif;
  grid-column: 1 / -1;
}
.pg-modal .pg-modal__cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
  align-content: start;
}

/* ===== 列表视图 ===== */
.pg-modal .pg-modal__list-tools {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
  flex-wrap: wrap;
}
.pg-modal .pg-filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.pg-modal .pg-filter__chip {
  border: 1px solid #e5e7eb;
  background: #fff;
  cursor: pointer;
  padding: 4px 12px;
  border-radius: 14px;
  font: 500 12px sans-serif;
  color: #5b6470;
}
.pg-modal .pg-filter__chip:hover {
  border-color: #c7d4ff;
  color: #2b5cff;
}
.pg-modal .pg-filter__chip--active {
  background: rgba(43, 92, 255, 0.1);
  border-color: rgba(43, 92, 255, 0.4);
  color: #2b5cff;
}
.pg-modal .pg-filter__chip--alias.pg-filter__chip--active {
  background: rgba(43, 92, 255, 0.1);
  border-color: rgba(43, 92, 255, 0.4);
  color: #2b5cff;
}
.pg-modal .pg-filter__chip--model.pg-filter__chip--active {
  background: rgba(140, 140, 140, 0.12);
  border-color: rgba(140, 140, 140, 0.4);
  color: #444;
}
.pg-modal .pg-filter__sw {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font: 400 12px sans-serif;
  color: #5b6470;
  margin-left: 4px;
}
.pg-modal .pg-filter__sw > span {
  user-select: none;
  cursor: pointer;
}
.pg-modal .pg-modal__group-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font: 400 12px sans-serif;
  color: #5b6470;
  cursor: pointer;
  user-select: none;
}
.pg-modal .pg-modal__group-toggle input {
  cursor: pointer;
}
.pg-modal .pg-list-head__cell--sortable {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  user-select: none;
}
.pg-modal .pg-list-head__cell--sortable:hover {
  color: #2b5cff;
}
.pg-modal .pg-list-head__cell--active {
  color: #2b5cff;
}
.pg-modal .pg-list-head__arrow {
  font-size: 9px;
}
.pg-modal .pg-list-head__arrow--idle {
  opacity: 0.45;
  font-size: 10px;
}

/* 手动输入条 */
.pg-modal .pg-modal__manual {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 12px;
  padding: 8px 10px;
  border: 1px dashed #e5e7eb;
  border-radius: 6px;
  background: #fafbfc;
}
.pg-modal .pg-modal__manual-label {
  font: 600 11px monospace;
  text-transform: uppercase;
  color: #6b7280;
  letter-spacing: 0.06em;
  flex-shrink: 0;
}
.pg-modal .pg-modal__manual-input {
  flex: 1;
  padding: 5px 9px;
  border: 1px solid #e5e7eb;
  border-radius: 5px;
  font: 400 13px sans-serif;
  outline: none;
  background: #fff;
}
.pg-modal .pg-modal__manual-input:focus {
  border-color: #2b5cff;
}
.pg-modal .pg-modal__manual-btn {
  border: none;
  background: #2b5cff;
  color: #fff;
  cursor: pointer;
  padding: 6px 14px;
  border-radius: 5px;
  font: 500 12px sans-serif;
}
.pg-modal .pg-modal__manual-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.pg-modal .pg-modal__list {
  display: flex;
  flex-direction: column;
  border: 1px solid #f0f2f5;
  border-radius: 8px;
  overflow: hidden;
}

/* 表头 + 行共享 grid 模板, 通过容器 class 控制是否多 provider 列 */
.pg-modal .pg-list-head,
.pg-modal .pg-list-row {
  display: grid;
  /* 默认 3 列: name | type | description (单 group tab) */
  grid-template-columns: minmax(160px, 1.6fr) 60px minmax(140px, 2.4fr);
  align-items: center;
  gap: 14px;
  padding: 9px 14px;
}
.pg-modal .pg-modal__list--with-provider .pg-list-head,
.pg-modal .pg-modal__list--with-provider .pg-list-row {
  /* ALL tab 时多一列 provider, 总 4 列 */
  grid-template-columns: minmax(150px, 1.6fr) 60px minmax(120px, 1fr) minmax(140px, 2fr);
}

.pg-modal .pg-list-head {
  background: #fafbfc;
  border-bottom: 1px solid #eef0f3;
  padding-top: 8px;
  padding-bottom: 8px;
}
.pg-modal .pg-list-head__cell {
  font: 600 10.5px monospace;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: #6b7280;
}
.pg-modal .pg-list-row {
  border-bottom: 1px solid #f4f5f7;
  cursor: pointer;
  border-left: 3px solid transparent;
}
.pg-modal .pg-list-row:last-child {
  border-bottom: none;
}
.pg-modal .pg-list-row:hover {
  background: rgba(43, 92, 255, 0.04);
}
.pg-modal .pg-list-row--alias {
  border-left-color: #2b5cff;
}
.pg-modal .pg-list-row--model {
  border-left-color: transparent;
}
/* 当前已选 model 行 — 蓝色背景 + 左条加粗 */
.pg-modal .pg-list-row--current {
  background: rgba(43, 92, 255, 0.07);
}
.pg-modal .pg-list-row--current:hover {
  background: rgba(43, 92, 255, 0.12);
}
.pg-modal .pg-list-row--current .pg-list-row__name {
  color: #2b5cff;
  font-weight: 600;
}
.pg-modal .pg-list-row--current.pg-list-row--alias,
.pg-modal .pg-list-row--current.pg-list-row--model {
  border-left-color: #2b5cff;
  border-left-width: 4px;
  padding-left: 13px; /* 抵消多出来的 1px */
}

/* 卡片 current 高亮 */
.pg-modal .pg-card--current {
  background: rgba(43, 92, 255, 0.07);
  border-color: #2b5cff !important;
  box-shadow: 0 0 0 2px rgba(43, 92, 255, 0.15);
}
.pg-modal .pg-card--current .pg-card__name {
  color: #2b5cff;
}

/* dropdown entry current 高亮 */
.pg-pick__entry--current {
  background: rgba(43, 92, 255, 0.08);
}
.pg-pick__entry--current .pg-pick__entry-name {
  color: #2b5cff;
  font-weight: 600;
}
.pg-modal .pg-list-row__name {
  font: 500 13px sans-serif;
  color: #1a1a1a;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pg-modal .pg-list-row__type {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
  font-size: 0;
}
.pg-modal .pg-list-row__tag {
  display: inline-block;
  font: 500 10.5px sans-serif;
  padding: 1px 7px;
  border-radius: 3px;
}
.pg-modal .pg-list-row__tag--alias {
  color: #2b5cff;
  background: rgba(43, 92, 255, 0.1);
}
.pg-modal .pg-list-row__tag--free {
  color: #178f4e;
  background: rgba(23, 143, 78, 0.12);
}
.pg-modal .pg-list-row__tag--image {
  color: #8b5cf6;
  background: rgba(139, 92, 246, 0.12);
}
.pg-modal .pg-list-row__tag--video {
  color: #d97706;
  background: rgba(217, 119, 6, 0.12);
}
.pg-modal .pg-list-row__provider {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font: 400 12px sans-serif;
  color: #5b6470;
  overflow: hidden;
  white-space: nowrap;
}
.pg-modal .pg-list-row__provider > span {
  overflow: hidden;
  text-overflow: ellipsis;
}
.pg-modal .pg-list-row__hint {
  font: 400 11.5px monospace;
  color: #98a0ad;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pg-modal .pg-card {
  position: relative;
  padding: 12px 14px;
  border: 1px solid #ebeef2;
  border-radius: 8px;
  background: #fff;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 4px;
  transition: border-color 0.12s, box-shadow 0.12s;
}
.pg-modal .pg-card:hover {
  border-color: #2b5cff;
  box-shadow: 0 0 0 2px rgba(43, 92, 255, 0.1);
}
/* 左边一条窄色条区分 alias / model,比胶囊更克制 */
.pg-modal .pg-card--alias {
  border-left: 3px solid #2b5cff;
}
.pg-modal .pg-card--model {
  border-left: 3px solid #cfd3d8;
}
.pg-modal .pg-card__top {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pg-modal .pg-card__name {
  font: 500 13px sans-serif;
  color: #1a1a1a;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}
/* kind 标签全弱化为右上角小角标 */
.pg-modal .pg-card__kind {
  font: 500 9.5px monospace;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: #98a0ad;
  background: transparent;
  padding: 0;
  flex-shrink: 0;
}
.pg-modal .pg-card__kind--alias {
  color: #6b7afd;
}
.pg-modal .pg-card__row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 14px;
}
/* group 标签改素色 — 跟卡片名同灰系, 不抢主视觉 */
.pg-modal .pg-card__group {
  font: 400 11px sans-serif;
  color: #6e7480;
  background: transparent;
  padding: 0;
  border-right: 1px solid #e5e7eb;
  padding-right: 6px;
  flex-shrink: 0;
}
.pg-modal .pg-card__hint {
  font: 400 11px monospace;
  color: #98a0ad;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}
</style>
