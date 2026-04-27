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

/**
 * 获取热门模型排行(按调用量倒序)。
 * window: 1h | 6h | 24h | 7d (默认 24h)
 */
export const getTopModels = (window: "1h" | "6h" | "24h" | "7d" = "24h", limit = 10) => {
  return http.get<TopModelStat[]>("/dashboard/top-models", {
    params: { window, limit },
  });
};
