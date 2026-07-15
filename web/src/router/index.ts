import { useAuthService } from "@/services/auth";
import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import Layout from "@/components/Layout.vue";

const routes: Array<RouteRecordRaw> = [
  {
    path: "/",
    component: Layout,
    children: [
      {
        path: "",
        name: "dashboard",
        component: () => import("@/views/Dashboard.vue"),
      },
      {
        path: "keys",
        name: "keys",
        component: () => import("@/views/Keys.vue"),
      },
      {
        path: "logs",
        name: "logs",
        component: () => import("@/views/Logs.vue"),
      },
      {
        path: "settings",
        name: "settings",
        component: () => import("@/views/Settings.vue"),
      },
      {
        path: "sync",
        name: "sync",
        component: () => import("@/views/Sync.vue"),
      },
      {
        // v2.7.0: 邀请加入确认页. 从邀请链接的 query(token/inviter) 跳转过来.
        path: "join",
        name: "join",
        component: () => import("@/views/Join.vue"),
      },
      {
        path: "playground",
        name: "playground",
        component: () => import("@/views/Playground.vue"),
      },
      {
        path: "aliases",
        name: "aliases",
        component: () => import("@/views/Aliases.vue"),
      },
      {
        // Legacy route alias: redirect old /auto-routing to /aliases
        // so any bookmarks still land on the new page.
        path: "auto-routing",
        redirect: { name: "aliases" },
      },
      {
        path: "model-catalog",
        name: "model-catalog",
        component: () => import("@/views/ModelCatalog.vue"),
      },
      {
        // Legacy route alias: /model-dedup folded into the Aliases "快速整理" tab.
        path: "model-dedup",
        redirect: { path: "/aliases", query: { tab: "quick" } },
      },
    ],
  },
  {
    path: "/login",
    name: "login",
    component: () => import("@/views/Login.vue"),
  },
];

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
});

const { checkLogin } = useAuthService();

router.beforeEach((to, _from, next) => {
  const loggedIn = checkLogin();
  if (to.path !== "/login" && !loggedIn) {
    // v2.7.0: 记住鉴权拦截前想去的完整地址(含 query), 登录后回跳。
    // 子站首次点邀请链接 /join?token=...&inviter=... 时多半未登录, 若不存下来
    // 就会被重定向到 /login 且丢掉 token/inviter, 加入流程直接断掉。
    // 排除 "/" 首页 — 存它没意义, 登录后默认就落首页。
    if (to.fullPath && to.fullPath !== "/") {
      sessionStorage.setItem("redirectAfterLogin", to.fullPath);
    }
    return next({ path: "/login" });
  }

  if (to.path === "/login" && loggedIn) {
    return next({ path: "/" });
  }

  next();
});

// v2.7.0: 邀请链接是后端固定拼的 hash 格式 "#/join?token=...&inviter=..."
// (见 internal/handler/sync_handler.go GenerateInvite), 但本应用是
// createWebHistory 而非 hash 路由 — 浏览器把 "#" 后内容当纯 fragment,
// vue-router 不会据此路由到 /join。首次加载时特判解析一次 location.hash,
// 换算成一次 replace 导航(在 router.install 触发默认的"按当前地址导航"之前
// 抢先调用 push/replace 会让 vue-router 跳过默认导航, 官方支持的用法)。
const inviteHashMatch = window.location.hash.match(/^#\/join\?(.*)$/);
if (inviteHashMatch) {
  const params = new URLSearchParams(inviteHashMatch[1]);
  const token = params.get("token");
  const inviter = params.get("inviter");
  if (token && inviter) {
    router.replace({ path: "/join", query: { token, inviter } });
  }
}

export default router;
