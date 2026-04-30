import i18n from "@/locales";
import { useAuthService } from "@/services/auth";
import axios from "axios";
import { appState } from "./app-state";

// 定义不需要显示 loading 的 API 地址列表
const noLoadingUrls = ["/tasks/status"];

declare module "axios" {
  interface AxiosRequestConfig {
    hideMessage?: boolean;
  }
}

const http = axios.create({
  baseURL: "/api",
  timeout: 60000,
  headers: { "Content-Type": "application/json" },
});

// 请求拦截器
http.interceptors.request.use(config => {
  // 检查当前请求的 URL 是否在屏蔽列表中
  if (config.url && !noLoadingUrls.includes(config.url)) {
    appState.loading = true;
  }
  const authKey = localStorage.getItem("authKey");
  if (authKey) {
    config.headers.Authorization = `Bearer ${authKey}`;
  }
  // 添加语言头
  const locale = localStorage.getItem("locale") || "zh-CN";
  config.headers["Accept-Language"] = locale;
  return config;
});

// 响应拦截器
http.interceptors.response.use(
  response => {
    appState.loading = false;
    if (response.config.method !== "get" && !response.config.hideMessage) {
      window.$message.success(response.data.message ?? i18n.global.t("common.operationSuccess"));
    }
    return response.data;
  },
  error => {
    appState.loading = false;
    // 401 始终 logout, 与 hideMessage 无关 — 不能让无声请求把用户卡在登录态过期里
    if (error.response?.status === 401) {
      if (window.location.pathname !== "/login") {
        const { logout } = useAuthService();
        logout();
        window.location.href = "/login";
      }
    }
    // hideMessage: 调用方自己处理失败 (probe / 后台轮询等), 不弹全局 toast.
    // 否则用户看到 "no known upstream protocol …" 之类 502 会误以为是保存失败.
    if (error.config?.hideMessage) {
      return Promise.reject(error);
    }
    if (error.response) {
      window.$message.error(
        error.response.data?.message ||
          i18n.global.t("common.requestFailed", { status: error.response.status }),
        {
          keepAliveOnHover: true,
          duration: 5000,
          closable: true,
        }
      );
    } else if (error.request) {
      window.$message.error(i18n.global.t("common.networkError"));
    } else {
      window.$message.error(i18n.global.t("common.requestSetupError"));
    }
    return Promise.reject(error);
  }
);

export default http;
