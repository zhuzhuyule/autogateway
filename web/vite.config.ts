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
      // 代理配置示例
      proxy: {
        "/api": {
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
