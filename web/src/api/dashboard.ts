import type { ChartData, DashboardStatsResponse, Group } from "@/types/models";
import http from "@/utils/http";

/**
 * 获取仪表盘基础统计数据
 */
export const getDashboardStats = () => {
  return http.get<DashboardStatsResponse>("/dashboard/stats");
};

/**
 * 获取仪表盘图表数据
 * @param groupId 可选的分组ID
 * @param hours 请求最近多少小时的数据
 */
export const getDashboardChart = (groupId?: number, hours = 24) => {
  return http.get<ChartData>("/dashboard/chart", {
    params: {
      ...(groupId ? { groupId } : {}),
      hours,
    },
  });
};

/**
 * 获取用于筛选的分组列表
 */
export const getGroupList = () => {
  return http.get<Group[]>("/groups/list");
};

/** 单条 Top Model 统计 */
export interface TopModelStat {
  model: string;
  calls: number;
  avg_ms: number;
  errors: number;
  error_rate: number;
  groups: string[];
}

/** 窗口内 token 用量与折算成本汇总 (①成本可观测性)。 */
export interface UsageSummary {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  /** 按挂牌价折算的等价价值 (免费源实际支出为 0)。 */
  cost_usd: number;
  /** 窗口内成功解析出用量的请求数。 */
  metered_requests: number;
}

/**
 * 获取窗口内的 token 用量与折算成本汇总。
 * window: 1h | 6h | 24h | 7d (默认 24h)
 */
export const getUsageSummary = (window: "1h" | "6h" | "24h" | "7d" = "24h") => {
  return http.get<UsageSummary>("/dashboard/usage-summary", {
    params: { window },
  });
};

/** 长周期(默认 30 天)用量卷积, 来自 group_hourly_stats, 独立于日志保留期。 */
export interface UsageRollup {
  days: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cost_usd: number;
}

/**
 * 获取长周期 token 用量与折算成本(来自小时卷积表, 支撑"本月"等长窗口)。
 * days: 1..365 (默认 30)
 */
export const getUsageRollup = (days = 30) => {
  return http.get<UsageRollup>("/dashboard/usage-rollup", {
    params: { days },
  });
};

/**
 * 获取热门模型排行(按调用量倒序)。
 * window: 1h | 6h | 24h | 7d (默认 24h)
 */
export const getTopModels = (window: "1h" | "6h" | "24h" | "7d" = "24h", limit = 10) => {
  return http.get<TopModelStat[]>("/dashboard/top-models", {
    params: { window, limit },
  });
};

/** 单条 model timing — avg_ms / calls + 窗口内 token/成本,无 group attribution. */
export interface ModelTiming {
  model: string;
  avg_ms: number;
  calls: number;
  /** 窗口内该模型累计 token 与按挂牌价折算成本 (免费源/未知模型 → 0)。 */
  tokens: number;
  cost_usd: number;
}

/**
 * 获取所有出现过的模型在窗口内的平均请求耗时。轻量,前端用于在
 * ModelCatalog / Aliases 卡片上挂一个 "≈ X ms" chip。
 */
export const getModelTimings = (window: "1h" | "6h" | "24h" | "7d" = "24h") => {
  return http.get<ModelTiming[]>("/dashboard/model-timings", {
    params: { window },
  });
};
