// 别名页各视图共用的类型。
//
// 为什么单独放一个 .ts: `<script setup>` 里不能 `export` 类型, 而四个视图
// (卡片/表格/分栏/健康) 需要共享同一份数据结构。放在这里比在组件之间
// import 类型更清楚, 也避免每个视图各定义一套导致字段漂移。
import type { ModelAliasRow } from "@/api/aliases";

/**
 * 一个候选在运行时不参与选路的原因。
 * 判定见 AliasManageTab.candidateState(), 优先级: disabled > blocked > unexposed。
 */
export type CandidateState = "usable" | "disabled" | "unexposed" | "blocked";

/** 候选行 + 渲染所需的全部派生信息。由父组件算好, 视图只负责画。 */
export interface CandidateRowView {
  row: ModelAliasRow;
  provider: string;
  /** 交给 ProviderLogo 做关键词匹配的提示串(分组名 + 模型名)。 */
  logoHint: string;
  groupName: string;
  /** 24h 平均耗时(ms), 0 表示无样本。 */
  avgMs: number;
  /** 24h 折算成本文案, 空串表示无数据。 */
  cost: string;
  /** 配置占比(按 weight 归一化的百分比), 同别名内相加约 100。 */
  share: number;
  /** 24h 实际调用数(按 分组+模型 统计), 无数据为 0。 */
  calls: number;
  state: CandidateState;
}

/**
 * 状态 -> i18n key。四个视图共用一份, 否则各写一套必然漂移。
 * 注意这里只管"不参与选路"的三种状态要显示什么词; usable 在卡片视图里
 * 靠绿点表达、不渲染文字。
 */
export const STATE_LABEL_KEY: Record<CandidateState, string> = {
  usable: "v3.aliasStateUsable",
  disabled: "v3.aliasStateDisabled",
  unexposed: "v3.aliasStateUnexposed",
  blocked: "v3.aliasStateBlocked",
};

/** 排障视图的分组顺序 —— 越靠前越需要处理。 */
export const STATE_TRIAGE_ORDER: CandidateState[] = ["unexposed", "blocked", "disabled"];

/** 一个别名 + 它的候选池。 */
export interface AliasView {
  alias: string;
  isReserved: boolean;
  rows: CandidateRowView[];
  /** rows 里 state==="usable" 的条数。 */
  usable: number;
  /** rows 里 state!=="usable" 的条数。 */
  unusable: number;
  total: number;
  /** 该别名全部候选在 24h 内的实际调用合计。 */
  calls: number;
}
