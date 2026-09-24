// 别名页各视图共用的类型。
//
// 为什么单独放一个 .ts: `<script setup>` 里不能 `export` 类型, 而列表和详情抽屉
// 需要共享同一份数据结构。放在这里比在组件之间 import 类型更清楚, 也避免每个
// 消费方各定义一套导致字段漂移。
import type { ModelAliasRow } from "@/api/aliases";

/**
 * 一个候选在运行时不参与选路的原因。
 * 判定见 services/aliases.ts 的 candidateState(), 优先级: disabled > blocked > unexposed。
 */
export type CandidateState = "usable" | "disabled" | "unexposed" | "blocked";

/** 候选行 + 渲染所需的全部派生信息。由 store 算好, 组件只负责画。 */
export interface CandidateRowView {
  row: ModelAliasRow;
  /** 分组展示名。 */
  groupName: string;
  /** 分组 channel_type, 用作 provider 标签; 缺失时回退到 groupName。 */
  providerLabel: string;
  /** providerLabel 是否是回退值 —— tooltip 要说清楚, 不能让用户以为是真 provider。 */
  providerIsFallback: boolean;
  /** 24h 平均耗时(ms), 0 表示无样本。 */
  avgMs: number;
  /** 24h 折算成本文案, 空串表示无数据。 */
  cost: string;
  /** 配置占比(按 weight 归一化的百分比), 同别名内相加约 100。 */
  share: number;
  /**
   * 24h 实际调用数, 按 **(分组, 请求名=本别名)** 归因。
   * 日志落的是请求名(改写前), 别名路由下它就是别名本身 —— 所以不能用
   * real_model 去查(那样永远是 0)。
   */
  calls: number;
  /** 同一别名里还有别的候选属于同一分组; 此时 calls 是分组级合计。 */
  callsSharedGroup: boolean;
  state: CandidateState;
}

/**
 * 状态 -> i18n key。列表和抽屉共用一份, 否则各写一套必然漂移。
 */
export const STATE_LABEL_KEY: Record<CandidateState, string> = {
  usable: "v3.aliasStateUsable",
  disabled: "v3.aliasStateDisabled",
  unexposed: "v3.aliasStateUnexposed",
  blocked: "v3.aliasStateBlocked",
};

/** 排障筛选顺序 —— 越靠前越需要处理。 */
export const STATE_TRIAGE_ORDER: CandidateState[] = ["unexposed", "blocked", "disabled"];

/** picker 暂存区里的一条待建候选。 */
export interface PendingPick {
  groupId: number;
  groupName: string;
  modelId: string;
}

export type AutoTier = "" | "simple" | "medium" | "complex";

/** 一个别名 + 它的候选池 + 窗口指标。列表一行 = 一个 AliasView。 */
export interface AliasView {
  alias: string;
  isReserved: boolean;
  /** 该别名是否是 auto 的某个档位(名字等于 simple/medium/complex)。 */
  tier: AutoTier;
  rows: CandidateRowView[];
  usable: number;
  unusable: number;
  total: number;
  /** 窗口内以该别名名请求的总调用数; 无数据为 0。 */
  calls: number;
  errors: number;
  /** 错误率, 百分数 0..100(与 top-models / model-timings 同口径); calls 为 0 时也是 0。 */
  errorRate: number;
  /** 24h 平均耗时(ms)。 */
  avgMs: number;
  /** 24h 折算成本(USD)。 */
  costUsd: number;
  /** 是否有任何候选跨档位复用(同一 (group, model) 也属于别的档位)。 */
  crossTier: boolean;
  /** 首要问题: 用于「有问题」筛选与状态列; 无问题为空串。 */
  problem: "" | "no-candidates" | "unexposed" | "blocked" | "disabled";
}

/**
 * 别名级的「首要问题」-> 状态徽标。
 *
 * 列表的状态列复用候选那一套词汇, 不另发明第五种 pill: 空别名(无候选)对用户来说
 * 就是"不可用"。
 */
export const STATE_PILL_FOR: Record<Exclude<AliasView["problem"], "">, CandidateState> = {
  "no-candidates": "disabled",
  unexposed: "unexposed",
  blocked: "blocked",
  disabled: "disabled",
};
