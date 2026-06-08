import vue from "@vitejs/plugin-vue";
import path from "path";
import { defineConfig, loadEnv } from "vite";
import pkg from "./package.json" with { type: "json" };

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  // 加载环境变量
  const env = loadEnv(mode, path.resolve(__dirname, "../"), "");

  // VITE_VERSION 来源: CI/Dockerfile 注入优先, fallback 到 package.json.version
  // 这样本地 `npm run build` 也能显示真实版本号, 不再 fallback 到 "dev".
  const version = env.VITE_VERSION || `v${pkg.version}`;

  return {
    define: {
      "import.meta.env.VITE_VERSION": JSON.stringify(version),
    },
    plugins: [vue()],
    // 解析配置
    resolve: {
      // 配置路径别名
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    // 开发服务器配置
    server: {
      // 默认 vite 在 macOS/Linux 优先监听 [::1] (IPv6 only), 但 proxy target
      // 是 127.0.0.1 (IPv4), 浏览器走 localhost (AAAA 优先) 能进 vite, 但
      // curl/某些客户端走 127.0.0.1 直连 5173 会 "Connection refused". 显式
      // 监听 IPv4 + IPv6 双栈, 排除一切回环歧义.
      host: "0.0.0.0",
      // 代理后端 API + proxy 路径 (Playground 直调上游)
      proxy: {
        "/api": {
          target: env.VITE_API_BASE_URL || "http://127.0.0.1:3001",
          changeOrigin: true,
        },
        // Playground 走 /proxy/:group/v1/chat/completions, 也得代理过去,
        // 否则前端直接打到 vite dev 自己, 返回 404 让人误以为是上游 404.
        "/proxy": {
          target: env.VITE_API_BASE_URL || "http://127.0.0.1:3001",
          changeOrigin: true,
        },
      },
    },
    // 构建配置
    build: {
      outDir: "dist",
      assetsDir: "assets",
    },
  };
});
