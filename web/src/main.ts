import App from "@/App.vue";
import "@/assets/style.css";
import "@/assets/design-v3.css";
import "@/assets/design-v5.css";
import router from "@/router";
import i18n from "@/locales";
import naive from "naive-ui";
import { createApp } from "vue";
import { loadFreeModelsRegistry } from "@/api/freemodels";

createApp(App).use(router).use(naive).use(i18n).mount("#app");

// 启动时预拉 FreeModels 注册表 (后端缓存代理). 不阻塞首屏 — isFree()
// 在数据落位前会回退到静态清单, 拉到后 reactive ref 触发依赖刷新.
loadFreeModelsRegistry();
