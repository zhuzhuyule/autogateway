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
    return next({ path: "/login" });
  }

  if (to.path === "/login" && loggedIn) {
    return next({ path: "/" });
  }

  next();
});

export default router;
