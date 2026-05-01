// 免费 LLM API 提供商精选清单
// 数据参考: https://github.com/mnfst/awesome-free-llm-apis (MIT)
// 同 provider 可能有多个 upstream host,反查时任一命中即视为关联。
// 仅 host 完全相等才匹配,避免误伤(如 *.openai.com 与 api.openai.com 子集冲突)。
//
// FreeModels Registry (zhuzhuyule/FreeModels) 通过 /api/freemodels/registry
// 提供运行时聚合的免费模型数据,优先级高于本文件的静态 FREE_MODELS 清单。
// 见 isFree() 内的 Tier 0 查询。

import { getProviderMeta, isFreeFromRegistry, lookupProviderIdByHost } from "@/api/freemodels";

export type ChannelType = "openai" | "openai-response" | "gemini" | "anthropic";

/** 模型能力档位:
 *  - fast    : 小模型 / 优化速度,适合简单分类、改写、摘要
 *  - balanced: 中型模型(30-70B 级),日常对话/编码主力
 *  - max     : 旗舰 / 多模态 / 长上下文 / 复杂推理
 */
export type ModelTier = "fast" | "balanced" | "max";

export interface FreeModel {
  providerId: string; // FreeProvider.id
  modelId: string; // 上游真实模型 ID
  tier: ModelTier;
  notes?: string; // 限速/特性等
}

export interface FreeProvider {
  id: string;
  name: string;
  freeTier: string;
  description: string;
  signupUrl: string;
  docsUrl: string;
  channelType: ChannelType;
  baseUrl: string;
  testModel: string;
  models: string[];
  paidModels?: string[];
  /**
   * 上游用 obfuscated model ID(讯飞:xop35qwen2b 这类),但管理后台想给用户
   * 看人类友好的名字(Qwen3.5-2B)。这里只放映射 — `models` / `paidModels` /
   * `available_models` / chat completions 请求都仍用 ID,展示层调
   * displayModelName(provider, id) 拿到友好名,查不到就回落到 ID。
   */
  modelNames?: Record<string, string>;
  recommendedGroupName: string;
  recommendedDisplayName: string;
  upstreamHosts: string[];
  badge?: "fast" | "high-quota" | "multi-model";
  /** 每分钟请求数,e.g. "30 RPM" */
  rpm?: string;
  /** 每天请求数,e.g. "1500/day" — 与 freeTier 冗余时省略 */
  rpd?: string;
  /** 并发数,e.g. "5 并发" */
  concurrent?: string;
  /** 上下文窗口,e.g. "1M", "256K" */
  context?: string;
  /** 其他亮点,短标签数组,e.g. ["多模态","推理增强"] */
  highlights?: string[];
  /**
   * true = 上游没有 /v1/models 端点(或不可信),模型清单只能靠手动维护
   * (e.g. LongCat)。建组时直接把 models + paidModels 写入 available_models,
   * 后端不再尝试拉取上游列表 — 刷新按钮也被前端禁用。
   */
  staticModels?: boolean;
  /**
   * 从上游 /models 返回的单条模型 meta 判断是否免费;
   * - true  = 明确免费
   * - false = 明确付费
   * - null  = 无判断依据,交给后续 tier
   */
  freeFromMeta?: (modelMeta: unknown) => boolean | null;
  verifiedAt: string;
}

export const FREE_PROVIDERS: FreeProvider[] = [
  {
    id: "groq",
    name: "Groq Cloud",
    freeTier: "14400 requests/day",
    description: "LPU 推理极速,Llama 4 / Qwen3 / Kimi K2",
    signupUrl: "https://console.groq.com/keys",
    docsUrl: "https://console.groq.com/docs",
    channelType: "openai",
    baseUrl: "https://api.groq.com/openai/v1",
    testModel: "llama-3.3-70b-versatile",
    models: [
      "llama-3.3-70b-versatile",
      "llama-3.1-8b-instant",
      "llama-4-scout-17b-16e-instruct",
      "llama-4-maverick-17b-128e-instruct",
      "qwen3-32b",
      "gpt-oss-120b",
      "kimi-k2-instruct",
      "deepseek-r1-distill-70b",
    ],
    recommendedGroupName: "groq",
    recommendedDisplayName: "Groq Cloud",
    upstreamHosts: ["api.groq.com"],
    badge: "fast",
    rpm: "30 RPM",
    context: "128K",
    verifiedAt: "2026-04",
  },
  {
    id: "cerebras",
    name: "Cerebras",
    freeTier: "Free tier(限速,无每日封顶)",
    description: "晶圆级推理,Llama 3.1 / 3.3 70B",
    signupUrl: "https://cloud.cerebras.ai",
    docsUrl: "https://inference-docs.cerebras.ai",
    channelType: "openai",
    baseUrl: "https://api.cerebras.ai/v1",
    testModel: "llama3.1-8b",
    models: ["llama3.1-8b", "llama-3.3-70b", "gpt-oss-120b", "qwen-3-235b-a22b-instruct-2507"],
    recommendedGroupName: "cerebras",
    recommendedDisplayName: "Cerebras Cloud",
    upstreamHosts: ["api.cerebras.ai"],
    badge: "fast",
    rpm: "30 RPM",
    context: "128K",
    verifiedAt: "2026-04",
  },
  {
    id: "openrouter",
    name: "OpenRouter",
    freeTier: "含免费模型(`:free` 后缀)",
    description: "300+ 模型聚合,统一计费",
    signupUrl: "https://openrouter.ai/keys",
    docsUrl: "https://openrouter.ai/docs",
    channelType: "openai",
    baseUrl: "https://openrouter.ai/api/v1",
    testModel: "mistralai/mistral-7b-instruct:free",
    models: [
      "openrouter/auto",
      "deepseek/deepseek-r1-0528:free",
      "deepseek/deepseek-chat-v3-0324:free",
      "qwen/qwen3.6-plus:free",
      "meta-llama/llama-4-scout:free",
      "meta-llama/llama-4-maverick:free",
      "meta-llama/llama-3.3-70b-instruct:free",
      "nvidia/nemotron-3-super-120b-a12b:free",
      "mistralai/devstral-2512:free",
    ],
    recommendedGroupName: "openrouter",
    recommendedDisplayName: "OpenRouter",
    upstreamHosts: ["openrouter.ai"],
    badge: "multi-model",
    rpm: "20 RPM",
    rpd: "50/day",
    freeFromMeta: meta => {
      if (typeof meta !== "object" || meta === null) {
        return null;
      }
      const m = meta as Record<string, unknown>;
      // OpenRouter /models 暴露 pricing 字段,字符串型小数,e.g. "0" "0.000005"
      const pricing = m.pricing as Record<string, unknown> | undefined;
      if (pricing && typeof pricing === "object") {
        const prompt = parseFloat(String(pricing.prompt ?? "NaN"));
        const completion = parseFloat(String(pricing.completion ?? "NaN"));
        if (Number.isFinite(prompt) && Number.isFinite(completion)) {
          return prompt === 0 && completion === 0;
        }
      }
      // 兜底:id 以 :free 结尾
      if (typeof m.id === "string" && m.id.endsWith(":free")) {
        return true;
      }
      return null;
    },
    verifiedAt: "2026-04",
  },
  {
    id: "together",
    name: "Together AI",
    freeTier: "$1 注册赠金 + 免费模型",
    description: "开源大模型托管,Llama / DeepSeek / Qwen",
    signupUrl: "https://api.together.xyz/settings/api-keys",
    docsUrl: "https://docs.together.ai",
    channelType: "openai",
    baseUrl: "https://api.together.xyz/v1",
    testModel: "meta-llama/Llama-3.3-70B-Instruct-Turbo-Free",
    models: [
      "meta-llama/Llama-3.3-70B-Instruct-Turbo-Free",
      "deepseek-ai/DeepSeek-R1-Distill-Llama-70B-free",
    ],
    recommendedGroupName: "together",
    recommendedDisplayName: "Together AI",
    upstreamHosts: ["api.together.xyz"],
    badge: "multi-model",
    verifiedAt: "2026-04",
  },
  {
    id: "cloudflare",
    name: "Cloudflare Workers AI",
    freeTier: "10000 neuron/day(免费档)",
    description: "边缘节点推理,集成 R2/D1 生态",
    signupUrl: "https://dash.cloudflare.com/profile/api-tokens",
    docsUrl: "https://developers.cloudflare.com/workers-ai/configuration/open-ai-compatibility",
    channelType: "openai",
    baseUrl: "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai",
    testModel: "@cf/meta/llama-3.1-8b-instruct",
    models: [
      "@cf/meta/llama-3.3-70b-instruct-fp8-fast",
      "@cf/meta/llama-3.1-8b-instruct-fp8-fast",
      "@cf/meta/llama-4-scout-17b-16e-instruct",
      "@cf/mistralai/mistral-small-3.1-24b-instruct",
      "@cf/qwen/qwq-32b",
      "@cf/deepseek-ai/deepseek-r1-distill-qwen-32b",
    ],
    recommendedGroupName: "cloudflare-ai",
    recommendedDisplayName: "Cloudflare Workers AI",
    upstreamHosts: ["api.cloudflare.com"],
    verifiedAt: "2026-04",
  },
  {
    id: "mistral",
    name: "Mistral La Plateforme",
    freeTier: "Experimental tier(限速免费)",
    description: "Mistral 官方 API,Codestral / Large",
    signupUrl: "https://console.mistral.ai/api-keys",
    docsUrl: "https://docs.mistral.ai",
    channelType: "openai",
    baseUrl: "https://api.mistral.ai/v1",
    testModel: "mistral-small-latest",
    models: [
      "mistral-small-latest",
      "mistral-medium-latest",
      "mistral-large-latest",
      "open-mistral-nemo",
      "codestral-latest",
      "pixtral-large-latest",
    ],
    recommendedGroupName: "mistral",
    recommendedDisplayName: "Mistral AI",
    upstreamHosts: ["api.mistral.ai"],
    rpm: "1 RPS",
    rpd: "500K tk/min",
    verifiedAt: "2026-04",
  },
  {
    id: "google",
    name: "Google AI Studio",
    freeTier: "免费档(每模型独立配额)",
    description: "Gemini 2.0/2.5 Flash,原生多模态",
    signupUrl: "https://aistudio.google.com/apikey",
    docsUrl: "https://ai.google.dev/gemini-api/docs",
    channelType: "gemini",
    baseUrl: "https://generativelanguage.googleapis.com/v1beta",
    testModel: "gemini-2.0-flash",
    models: ["gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.0-flash"],
    recommendedGroupName: "gemini",
    recommendedDisplayName: "Google Gemini",
    upstreamHosts: ["generativelanguage.googleapis.com"],
    badge: "high-quota",
    rpm: "15 RPM",
    rpd: "1500/day",
    context: "1M",
    verifiedAt: "2026-04",
  },
  {
    id: "cohere",
    name: "Cohere",
    freeTier: "Trial key(限速免费)",
    description: "Command R / R+,搜索/RAG 友好",
    signupUrl: "https://dashboard.cohere.com/api-keys",
    docsUrl: "https://docs.cohere.com/v2/docs/compatibility-api",
    channelType: "openai",
    baseUrl: "https://api.cohere.ai/compatibility/v1",
    testModel: "command-r-08-2024",
    models: ["command-r-plus-08-2024", "command-r-08-2024", "command-r7b-12-2024"],
    recommendedGroupName: "cohere",
    recommendedDisplayName: "Cohere",
    upstreamHosts: ["api.cohere.ai"],
    rpm: "20 RPM",
    rpd: "1K/month",
    verifiedAt: "2026-04",
  },
  {
    id: "github-models",
    name: "GitHub Models",
    freeTier: "GitHub 账号免费(限速)",
    description: "Azure 托管多家旗舰模型,GPT/Llama/Mistral",
    signupUrl: "https://github.com/settings/tokens",
    docsUrl: "https://docs.github.com/github-models",
    channelType: "openai",
    baseUrl: "https://models.inference.ai.azure.com",
    testModel: "gpt-4o-mini",
    models: [
      "gpt-4.1",
      "gpt-4.1-mini",
      "gpt-4o",
      "o4-mini",
      "DeepSeek-R1",
      "Llama-4-Scout-17B-16E",
      "Llama-4-Maverick-17B-128E",
      "Meta-Llama-3.3-70B",
      "Mistral-Small-3.1",
    ],
    recommendedGroupName: "github-models",
    recommendedDisplayName: "GitHub Models",
    upstreamHosts: ["models.inference.ai.azure.com"],
    badge: "multi-model",
    rpm: "10 RPM",
    rpd: "150/day",
    verifiedAt: "2026-04",
  },
  {
    id: "huggingface",
    name: "Hugging Face Inference",
    freeTier: "免费模型(限速)",
    description: "Router 端点,任选 Inference Providers",
    signupUrl: "https://huggingface.co/settings/tokens",
    docsUrl: "https://huggingface.co/docs/inference-providers",
    channelType: "openai",
    baseUrl: "https://router.huggingface.co/v1",
    testModel: "meta-llama/Llama-3.1-8B-Instruct",
    models: [
      "meta-llama/Llama-3.1-8B-Instruct",
      "Qwen/Qwen2.5-72B-Instruct",
      "mistralai/Mistral-7B-Instruct-v0.3",
      "microsoft/Phi-3.5-mini-instruct",
    ],
    recommendedGroupName: "huggingface",
    recommendedDisplayName: "Hugging Face Router",
    upstreamHosts: ["router.huggingface.co"],
    badge: "multi-model",
    verifiedAt: "2026-04",
  },
  {
    id: "bigmodel",
    name: "智谱 AI (Z AI)",
    freeTier: "GLM-Flash 系列永久免费",
    description: "GLM-4-Flash 文本+视觉,无需绑卡",
    signupUrl: "https://open.bigmodel.cn/usercenter/apikeys",
    docsUrl: "https://open.bigmodel.cn/dev/api",
    channelType: "openai",
    baseUrl: "https://open.bigmodel.cn/api/paas/v4",
    testModel: "GLM-4-Flash",
    models: ["GLM-4-Flash", "GLM-4V-Flash", "GLM-Z1-Flash"],
    recommendedGroupName: "zhipu",
    recommendedDisplayName: "智谱 AI",
    upstreamHosts: ["open.bigmodel.cn"],
    verifiedAt: "2026-04",
  },
  {
    id: "nvidia",
    name: "NVIDIA NIM",
    freeTier: "100+ 模型,无每日 Token 上限",
    description: "加入 NVIDIA Developer Program 免费,DeepSeek / Nemotron / Llama",
    signupUrl: "https://build.nvidia.com/explore/discover",
    docsUrl: "https://docs.api.nvidia.com",
    channelType: "openai",
    baseUrl: "https://integrate.api.nvidia.com/v1",
    testModel: "nvidia/llama-3.1-nemotron-ultra-253b-v1",
    models: [
      "deepseek-ai/deepseek-r1",
      "nvidia/llama-3.1-nemotron-ultra-253b-v1",
      "nvidia/nemotron-3-super-120b-a12b",
      "nvidia/nemotron-3-nano-30b-a3b",
      "meta/llama-3.1-405b-instruct",
      "qwen/qwen2.5-72b-instruct",
      "google/gemma-4-31b",
    ],
    recommendedGroupName: "nvidia-nim",
    recommendedDisplayName: "NVIDIA NIM",
    upstreamHosts: ["integrate.api.nvidia.com"],
    badge: "multi-model",
    verifiedAt: "2026-04",
  },
  {
    id: "siliconflow",
    name: "SiliconFlow",
    freeTier: "永久免费模型 + 14 CNY 注册赠金",
    description: "国内主流推理平台,Qwen / DeepSeek / GLM,高并发限额",
    signupUrl: "https://cloud.siliconflow.cn/account/ak",
    docsUrl: "https://docs.siliconflow.cn",
    channelType: "openai",
    baseUrl: "https://api.siliconflow.cn/v1",
    testModel: "Qwen/Qwen3-8B",
    models: [
      "Qwen/Qwen3-8B",
      "deepseek-ai/DeepSeek-R1-Distill-Qwen-7B",
      "deepseek-ai/DeepSeek-R1-0528-Qwen3-8B",
      "THUDM/glm-4-9b-chat",
      "THUDM/GLM-4.1V-9B-Thinking",
    ],
    recommendedGroupName: "siliconflow",
    recommendedDisplayName: "SiliconFlow",
    upstreamHosts: ["api.siliconflow.cn"],
    badge: "high-quota",
    verifiedAt: "2026-04",
  },
  {
    id: "llm7",
    name: "LLM7.io",
    freeTier: "免注册基础访问,30+ 模型",
    description: "零摩擦 API 网关,DeepSeek / Gemini / GPT,注册后限速翻倍",
    signupUrl: "https://token.llm7.io",
    docsUrl: "https://llm7.io",
    channelType: "openai",
    baseUrl: "https://api.llm7.io/v1",
    testModel: "deepseek-v3-0324",
    models: [
      "deepseek-r1-0528",
      "deepseek-v3-0324",
      "gemini-2.5-flash-lite",
      "gpt-4o-mini",
      "qwen2.5-coder-32b",
    ],
    recommendedGroupName: "llm7",
    recommendedDisplayName: "LLM7.io",
    upstreamHosts: ["api.llm7.io"],
    verifiedAt: "2026-04",
  },
  {
    id: "modelscope",
    name: "ModelScope",
    freeTier: "每日 2000 次(注册用户)",
    description: "阿里魔搭社区,Qwen 系列 + 千余开源模型,需实名认证",
    signupUrl: "https://modelscope.cn/my/myaccesstoken",
    docsUrl: "https://modelscope.cn/docs",
    channelType: "openai",
    baseUrl: "https://api-inference.modelscope.cn/v1",
    testModel: "Qwen/Qwen3.5-35B-A3B",
    models: ["Qwen/Qwen3.5-35B-A3B", "Qwen/Qwen3.5-27B"],
    recommendedGroupName: "modelscope",
    recommendedDisplayName: "ModelScope",
    upstreamHosts: ["api-inference.modelscope.cn"],
    verifiedAt: "2026-04",
  },
  {
    id: "longcat",
    name: "美团 LongCat",
    freeTier: "Flash-Lite 50M · 其余共享 500K tokens/day",
    description: "美团长文本模型,Chat/Thinking/Lite/Omni 多档,OpenAI+Anthropic 兼容",
    signupUrl: "https://longcat.chat/platform",
    docsUrl: "https://longcat.chat/platform/docs/zh/",
    channelType: "openai",
    baseUrl: "https://api.longcat.chat/openai/v1",
    // 模型 ID 是 PascalCase (官方文档 longcat.chat/platform/docs/zh/)
    testModel: "LongCat-Flash-Lite",
    models: [
      "LongCat-Flash-Lite",
      "LongCat-Flash-Chat",
      "LongCat-Flash-Thinking",
      "LongCat-Flash-Thinking-2601",
      "LongCat-Flash-Omni-2603",
      "LongCat-Flash-Chat-2602-Exp",
      "LongCat-2.0-Preview",
    ],
    recommendedGroupName: "longcat",
    recommendedDisplayName: "美团 LongCat",
    upstreamHosts: ["api.longcat.chat"],
    badge: "high-quota",
    staticModels: true,
    verifiedAt: "2026-04",
  },
  {
    id: "xunfei",
    name: "讯飞星辰",
    freeTier: "永久免费 (Qwen3.5-2B / Qwen3-1.7B / Hunyuan-MT-7B)",
    description: "科大讯飞 MaaS,65+ 模型,Key 格式 APIKey:APISecret",
    signupUrl: "https://maas.xfyun.cn",
    docsUrl: "https://maas.xfyun.cn/modelService",
    channelType: "openai",
    baseUrl: "https://maas-api.cn-huabei-1.xf-yun.com/v2",
    // 数据源:maas.xfyun.cn/api/v1/gpt-finetune/model/base/list-v2 实际枚举
    // 过滤:price.inferencePrice 的 inTokensPrice 与 outTokensPrice 都为 0
    // 仅保留可走 chat/completions 的文本生成类
    testModel: "xop35qwen2b",
    models: ["xop35qwen2b", "test_ent", "xophunyuan7bmt"],
    paidModels: [
      // 多模态
      "xopqwen36v35b",
      "xopqwen35397b",
      "xopkimik25",
      "xopqwen35v35b",
      // 旗舰文本
      "xopglm51",
      "xopglm5",
      "xopglm47blth2",
      "xopkimik2blth",
      "xopkimik2blins",
      "xopdeepseekv32",
      "xdeepseekv3",
      "xdeepseekr1",
      "xminimaxm25",
      // Qwen3 系列
      "xop3qwen235b2507",
      "xop3qwen235b",
      "xop3qwen32b",
      "xop3qwen30b2507",
      "xop3qwen30b",
      "xop3qwen14b",
      "xop3qwen8b",
      "xop3qwen4b",
      "xop3qwen0b6",
      "xop3qwencodernext",
      "xop3qwen80bnext",
      // Flash + Distill
      "xopglmv47flash",
      "xdeepseekr1qwen32b",
      "xdeepseekr1qwen14b",
      "xdeepseekr1qwen7b",
      "xdeepseekr1llama8b",
      // QwQ
      "xopqwenqwq32b",
    ],
    recommendedGroupName: "xfyun",
    recommendedDisplayName: "讯飞星辰",
    upstreamHosts: ["maas-api.cn-huabei-1.xf-yun.com"],
    // 讯飞 MaaS 的 model 字段是 obfuscated ID,展示时还原成人类可读名
    // 数据源:maas.xfyun.cn/api/v1/gpt-finetune/model/base/list-v2 的 serviceName
    modelNames: {
      // 永久免费
      xop35qwen2b: "Qwen3.5-2B",
      test_ent: "Qwen3-1.7B",
      xophunyuan7bmt: "Hunyuan-MT-7B",
      // 多模态
      xopqwen36v35b: "Qwen3-VL-235B-A22B-Instruct",
      xopqwen35397b: "Qwen3-235B-A22B-Instruct",
      xopkimik25: "Kimi-K2-Instruct-0905",
      xopqwen35v35b: "Qwen3-VL-30B-A3B-Instruct",
      // 旗舰文本
      xopglm51: "GLM-4.6",
      xopglm5: "GLM-4.5",
      xopglm47blth2: "GLM-4.5-Air",
      xopkimik2blth: "Kimi-K2-Thinking",
      xopkimik2blins: "Kimi-K2-Instruct",
      xopdeepseekv32: "DeepSeek-V3.2-Exp",
      xdeepseekv3: "DeepSeek-V3",
      xdeepseekr1: "DeepSeek-R1",
      xminimaxm25: "MiniMax-M2",
      // Qwen3 系列
      xop3qwen235b2507: "Qwen3-235B-A22B-Instruct-2507",
      xop3qwen235b: "Qwen3-235B-A22B",
      xop3qwen32b: "Qwen3-32B",
      xop3qwen30b2507: "Qwen3-30B-A3B-Instruct-2507",
      xop3qwen30b: "Qwen3-30B-A3B",
      xop3qwen14b: "Qwen3-14B",
      xop3qwen8b: "Qwen3-8B",
      xop3qwen4b: "Qwen3-4B",
      xop3qwen0b6: "Qwen3-0.6B",
      xop3qwencodernext: "Qwen3-Coder-Next",
      xop3qwen80bnext: "Qwen3-Next-80B-A3B-Instruct",
      // Flash + Distill
      xopglmv47flash: "GLM-4-Flash",
      xdeepseekr1qwen32b: "DeepSeek-R1-Distill-Qwen-32B",
      xdeepseekr1qwen14b: "DeepSeek-R1-Distill-Qwen-14B",
      xdeepseekr1qwen7b: "DeepSeek-R1-Distill-Qwen-7B",
      xdeepseekr1llama8b: "DeepSeek-R1-Distill-Llama-8B",
      // QwQ
      xopqwenqwq32b: "QwQ-32B",
    },
    verifiedAt: "2026-04",
  },
  {
    id: "gitee",
    name: "Gitee AI",
    freeTier: "永久免费小模型 + 注册体验额度",
    description: "模力方舟 Serverless API,Qwen3 / GLM-Flash / DeepSeek-Distill",
    signupUrl: "https://ai.gitee.com/dashboard/settings/tokens",
    docsUrl: "https://ai.gitee.com/docs/openapi/v1",
    channelType: "openai",
    baseUrl: "https://ai.gitee.com/v1",
    testModel: "internlm3-8b-instruct",
    // 取自 https://ai.gitee.com/api/pay/{user}/services?type=serverless 实际枚举
    // 过滤条件: operation_summary.min_price === 0 && tags 含 'text-generation'
    // 仅含 text-generation 类(chat/completions),非 embedding/asr/tts 等专用类
    models: [
      "internlm3-8b-instruct",
      "Qwen3-8B",
      "Qwen3-4B",
      "Qwen3-0.6B",
      "Qwen2-7B-Instruct",
      "DeepSeek-R1-Distill-Qwen-14B",
      "DeepSeek-R1-Distill-Qwen-7B",
      "DeepSeek-R1-Distill-Qwen-1.5B",
      "GLM-4.7-Flash",
      "GLM-4-9B-0414",
      "glm-4-9b-chat",
    ],
    paidModels: [
      "DeepSeek-V4-Flash",
      "DeepSeek-V3.2",
      "GLM-4.7",
      "GLM-5",
      "Qwen3-235B-A22B",
      "Qwen3-Coder-30B-A3B-Instruct",
    ],
    recommendedGroupName: "gitee-ai",
    recommendedDisplayName: "Gitee AI",
    upstreamHosts: ["ai.gitee.com"],
    badge: "multi-model",
    verifiedAt: "2026-04",
  },
  {
    id: "aihubmix",
    name: "AIHubMix",
    freeTier: "注册赠余额,聚合 GPT / Claude / Gemini / 国产模型",
    description: "多厂商模型聚合网关,OpenAI 兼容接口,边缘节点加速",
    signupUrl: "https://console.aihubmix.com/sign-up",
    docsUrl: "https://docs.aihubmix.com",
    channelType: "openai",
    baseUrl: "https://aihubmix.com/v1",
    testModel: "gpt-4o-mini",
    models: [
      "gpt-4o-mini",
      "gpt-4o",
      "claude-opus-4-7",
      "gemini-2.0-flash",
      "deepseek-v4-flash",
      "deepseek-r1",
      "glm-4.7",
    ],
    recommendedGroupName: "aihubmix",
    recommendedDisplayName: "AIHubMix",
    upstreamHosts: ["aihubmix.com", "api.aihubmix.com"],
    badge: "multi-model",
    verifiedAt: "2026-04",
  },
  {
    id: "kilo",
    name: "Kilo Code",
    freeTier: "免费模型,无需绑卡",
    description: "多厂商免费模型聚合,NVIDIA / Bytedance / xAI,~200 req/hr",
    signupUrl: "https://kilo.ai",
    docsUrl: "https://kilo.ai",
    channelType: "openai",
    baseUrl: "https://api.kilo.ai/api/gateway",
    testModel: "kilo-auto/free",
    models: [
      "kilo-auto/free",
      "nvidia/nemotron-3-super-120b-a12b:free",
      "x-ai/grok-code-fast-1:optimized:free",
    ],
    recommendedGroupName: "kilo",
    recommendedDisplayName: "Kilo Code",
    upstreamHosts: ["api.kilo.ai"],
    verifiedAt: "2026-04",
  },
];

export function getProviderById(id: string): FreeProvider | undefined {
  return FREE_PROVIDERS.find(p => p.id === id);
}

function extractHost(url: string): string {
  if (!url) {
    return "";
  }
  try {
    return new URL(url).host.toLowerCase();
  } catch {
    return "";
  }
}

export function findProviderByUpstreamUrl(url: string): FreeProvider | undefined {
  const host = extractHost(url);
  if (!host) {
    return undefined;
  }
  // 1) Registry 优先: FreeModels providerMeta 由 host 反查 providerId.
  //    匹配上时, 用本地 entry (有更全的 testModel/upstreamHosts/badge 等) 兜
  //    回, 没本地 entry 时合成最小 FreeProvider 让 UI 仍能显示 displayName.
  //    设计目的: FreeModels 加新 provider 后 api-center 自动认得, 不必手
  //    动同步 upstreamHosts[].
  const providerId = lookupProviderIdByHost(host);
  if (providerId) {
    const local = FREE_PROVIDERS.find(p => p.id === providerId);
    if (local) {
      return local;
    }
    // FreeModels 已收录但本地 entry 缺失: 用 registry meta 合成最小占位,
    // 至少让"是不是认得这个分组"层面回答正确. 真要建组操作仍需补本地 entry.
    const meta = getProviderMeta(providerId);
    if (meta) {
      return synthesizeProviderFromMeta(providerId, meta);
    }
  }
  // 2) Fallback: 本地 freeProviders.ts.upstreamHosts (FreeModels 没覆盖
  //    的 13 家国内 provider 走这条路 — siliconflow / llm7 / kilo 等).
  return FREE_PROVIDERS.find(p => p.upstreamHosts.some(h => h.toLowerCase() === host));
}

/**
 * 当 FreeModels 已收录但本地 freeProviders.ts 缺 entry 时, 用 registry
 * 元数据合成一个"最小可用" FreeProvider, 让分组识别 / 展示链接不至于报
 * "未知 provider". 字段不全 (无 testModel / models[] / 限额信息), 真要建
 * 组操作仍建议在 freeProviders.ts 补完整 entry.
 */
function synthesizeProviderFromMeta(
  providerId: string,
  meta: { displayName: string; website?: string; apiBaseUrl?: string; channelType?: "openai" | "anthropic" | "gemini" }
): FreeProvider {
  return {
    id: providerId,
    name: meta.displayName,
    freeTier: "见 FreeModels Registry",
    description: "",
    signupUrl: meta.website || "",
    docsUrl: meta.website || "",
    channelType: meta.channelType || "openai",
    baseUrl: meta.apiBaseUrl || "",
    testModel: "",
    models: [],
    recommendedGroupName: providerId,
    recommendedDisplayName: meta.displayName,
    upstreamHosts: meta.apiBaseUrl ? [extractHost(meta.apiBaseUrl)].filter(Boolean) : [],
    verifiedAt: "",
  };
}

export function findProviderByUpstreams(
  upstreams: Array<{ url?: string }> | undefined | null
): FreeProvider | undefined {
  if (!upstreams || upstreams.length === 0) {
    return undefined;
  }
  for (const u of upstreams) {
    const matched = findProviderByUpstreamUrl(u.url || "");
    if (matched) {
      return matched;
    }
  }
  return undefined;
}

// ============================================================================
// 已知免费模型清单(ModelCatalog 用):为模型打上"免费"徽标和能力档位.
// 不影响后端,纯前端展示数据;同 model id 在不同 provider 都免费的话各列一行.
// ============================================================================

export const FREE_MODELS: FreeModel[] = [
  // Groq
  { providerId: "groq", modelId: "llama-3.1-8b-instant", tier: "fast", notes: "极速小模型" },
  { providerId: "groq", modelId: "llama-3.3-70b-versatile", tier: "balanced", notes: "日常主力" },
  {
    providerId: "groq",
    modelId: "llama-4-scout-17b-16e-instruct",
    tier: "balanced",
    notes: "多模态",
  },
  {
    providerId: "groq",
    modelId: "llama-4-maverick-17b-128e-instruct",
    tier: "max",
    notes: "多模态旗舰",
  },
  { providerId: "groq", modelId: "qwen3-32b", tier: "balanced" },
  { providerId: "groq", modelId: "gpt-oss-120b", tier: "max", notes: "OpenAI 开源 120B" },
  { providerId: "groq", modelId: "kimi-k2-instruct", tier: "max", notes: "262K 上下文" },
  { providerId: "groq", modelId: "deepseek-r1-distill-70b", tier: "max", notes: "推理增强" },

  // Cerebras
  { providerId: "cerebras", modelId: "llama3.1-8b", tier: "fast" },
  { providerId: "cerebras", modelId: "llama-3.3-70b", tier: "balanced" },
  { providerId: "cerebras", modelId: "gpt-oss-120b", tier: "max", notes: "OpenAI 开源 120B" },
  {
    providerId: "cerebras",
    modelId: "qwen-3-235b-a22b-instruct-2507",
    tier: "max",
    notes: "MoE 235B",
  },

  // OpenRouter (含 :free 后缀)
  { providerId: "openrouter", modelId: "openrouter/auto", tier: "balanced", notes: "自动路由" },
  {
    providerId: "openrouter",
    modelId: "deepseek/deepseek-r1-0528:free",
    tier: "max",
    notes: "推理增强",
  },
  { providerId: "openrouter", modelId: "deepseek/deepseek-chat-v3-0324:free", tier: "balanced" },
  { providerId: "openrouter", modelId: "qwen/qwen3.6-plus:free", tier: "max", notes: "1M 上下文" },
  {
    providerId: "openrouter",
    modelId: "meta-llama/llama-4-scout:free",
    tier: "balanced",
    notes: "多模态",
  },
  {
    providerId: "openrouter",
    modelId: "meta-llama/llama-4-maverick:free",
    tier: "max",
    notes: "多模态旗舰",
  },
  { providerId: "openrouter", modelId: "meta-llama/llama-3.3-70b-instruct:free", tier: "balanced" },
  { providerId: "openrouter", modelId: "nvidia/nemotron-3-super-120b-a12b:free", tier: "max" },
  {
    providerId: "openrouter",
    modelId: "mistralai/devstral-2512:free",
    tier: "balanced",
    notes: "代码专用",
  },

  // Together AI(免费档模型)
  {
    providerId: "together",
    modelId: "meta-llama/Llama-3.3-70B-Instruct-Turbo-Free",
    tier: "balanced",
  },
  {
    providerId: "together",
    modelId: "deepseek-ai/DeepSeek-R1-Distill-Llama-70B-free",
    tier: "max",
    notes: "推理增强",
  },

  // Cloudflare Workers AI
  {
    providerId: "cloudflare",
    modelId: "@cf/meta/llama-3.3-70b-instruct-fp8-fast",
    tier: "balanced",
  },
  { providerId: "cloudflare", modelId: "@cf/meta/llama-3.1-8b-instruct-fp8-fast", tier: "fast" },
  {
    providerId: "cloudflare",
    modelId: "@cf/meta/llama-4-scout-17b-16e-instruct",
    tier: "balanced",
    notes: "多模态",
  },
  {
    providerId: "cloudflare",
    modelId: "@cf/mistralai/mistral-small-3.1-24b-instruct",
    tier: "balanced",
  },
  { providerId: "cloudflare", modelId: "@cf/qwen/qwq-32b", tier: "max", notes: "推理模型" },
  {
    providerId: "cloudflare",
    modelId: "@cf/deepseek-ai/deepseek-r1-distill-qwen-32b",
    tier: "max",
    notes: "推理增强",
  },

  // Mistral Experimental
  {
    providerId: "mistral",
    modelId: "mistral-small-latest",
    tier: "fast",
    notes: "Mistral Small 4",
  },
  {
    providerId: "mistral",
    modelId: "mistral-medium-latest",
    tier: "balanced",
    notes: "Mistral Medium 3",
  },
  { providerId: "mistral", modelId: "mistral-large-latest", tier: "max", notes: "Mistral Large 3" },
  { providerId: "mistral", modelId: "open-mistral-nemo", tier: "fast" },
  { providerId: "mistral", modelId: "codestral-latest", tier: "balanced", notes: "代码专用" },
  { providerId: "mistral", modelId: "pixtral-large-latest", tier: "max", notes: "视觉多模态" },

  // Google AI Studio
  {
    providerId: "google",
    modelId: "gemini-2.5-flash",
    tier: "balanced",
    notes: "原生多模态,1M 上下文",
  },
  {
    providerId: "google",
    modelId: "gemini-2.5-flash-lite",
    tier: "fast",
    notes: "1M 上下文",
  },
  { providerId: "google", modelId: "gemini-2.0-flash", tier: "fast", notes: "原生多模态" },

  // Cohere
  { providerId: "cohere", modelId: "command-r7b-12-2024", tier: "fast" },
  { providerId: "cohere", modelId: "command-r-08-2024", tier: "balanced" },
  { providerId: "cohere", modelId: "command-r-plus-08-2024", tier: "max" },

  // GitHub Models (Azure 托管)
  { providerId: "github-models", modelId: "gpt-4.1-mini", tier: "fast" },
  { providerId: "github-models", modelId: "gpt-4.1", tier: "balanced", notes: "1M 上下文" },
  { providerId: "github-models", modelId: "gpt-4o", tier: "balanced", notes: "多模态" },
  { providerId: "github-models", modelId: "o4-mini", tier: "max", notes: "推理模型" },
  { providerId: "github-models", modelId: "DeepSeek-R1", tier: "max", notes: "推理增强" },
  {
    providerId: "github-models",
    modelId: "Llama-4-Scout-17B-16E",
    tier: "balanced",
    notes: "多模态",
  },
  {
    providerId: "github-models",
    modelId: "Llama-4-Maverick-17B-128E",
    tier: "max",
    notes: "多模态",
  },
  { providerId: "github-models", modelId: "Meta-Llama-3.3-70B", tier: "balanced" },
  { providerId: "github-models", modelId: "Mistral-Small-3.1", tier: "fast" },

  // Hugging Face Router
  { providerId: "huggingface", modelId: "meta-llama/Llama-3.1-8B-Instruct", tier: "fast" },
  { providerId: "huggingface", modelId: "Qwen/Qwen2.5-72B-Instruct", tier: "balanced" },
  { providerId: "huggingface", modelId: "mistralai/Mistral-7B-Instruct-v0.3", tier: "fast" },
  { providerId: "huggingface", modelId: "microsoft/Phi-3.5-mini-instruct", tier: "fast" },

  // Zhipu AI (GLM)
  { providerId: "bigmodel", modelId: "GLM-4-Flash", tier: "fast", notes: "免费旗舰" },
  { providerId: "bigmodel", modelId: "GLM-4V-Flash", tier: "fast", notes: "视觉+文本" },
  { providerId: "bigmodel", modelId: "GLM-Z1-Flash", tier: "balanced", notes: "推理增强" },

  // NVIDIA NIM
  { providerId: "nvidia", modelId: "deepseek-ai/deepseek-r1", tier: "max", notes: "推理增强" },
  { providerId: "nvidia", modelId: "nvidia/llama-3.1-nemotron-ultra-253b-v1", tier: "max" },
  { providerId: "nvidia", modelId: "nvidia/nemotron-3-super-120b-a12b", tier: "max" },
  { providerId: "nvidia", modelId: "nvidia/nemotron-3-nano-30b-a3b", tier: "balanced" },
  { providerId: "nvidia", modelId: "meta/llama-3.1-405b-instruct", tier: "max" },
  { providerId: "nvidia", modelId: "qwen/qwen2.5-72b-instruct", tier: "balanced" },
  { providerId: "nvidia", modelId: "google/gemma-4-31b", tier: "balanced" },

  // SiliconFlow
  { providerId: "siliconflow", modelId: "Qwen/Qwen3-8B", tier: "fast" },
  {
    providerId: "siliconflow",
    modelId: "deepseek-ai/DeepSeek-R1-Distill-Qwen-7B",
    tier: "balanced",
    notes: "推理增强",
  },
  {
    providerId: "siliconflow",
    modelId: "deepseek-ai/DeepSeek-R1-0528-Qwen3-8B",
    tier: "max",
    notes: "推理增强",
  },
  { providerId: "siliconflow", modelId: "THUDM/glm-4-9b-chat", tier: "fast" },
  {
    providerId: "siliconflow",
    modelId: "THUDM/GLM-4.1V-9B-Thinking",
    tier: "balanced",
    notes: "视觉+推理",
  },

  // LLM7.io
  { providerId: "llm7", modelId: "deepseek-r1-0528", tier: "max", notes: "推理增强" },
  { providerId: "llm7", modelId: "deepseek-v3-0324", tier: "balanced" },
  { providerId: "llm7", modelId: "gemini-2.5-flash-lite", tier: "fast" },
  { providerId: "llm7", modelId: "gpt-4o-mini", tier: "fast" },
  { providerId: "llm7", modelId: "qwen2.5-coder-32b", tier: "balanced", notes: "代码专用" },

  // ModelScope
  { providerId: "modelscope", modelId: "Qwen/Qwen3.5-35B-A3B", tier: "max", notes: "视觉+文本" },
  { providerId: "modelscope", modelId: "Qwen/Qwen3.5-27B", tier: "balanced" },

  // Kilo Code
  { providerId: "kilo", modelId: "kilo-auto/free", tier: "balanced", notes: "自动路由" },
  { providerId: "kilo", modelId: "nvidia/nemotron-3-super-120b-a12b:free", tier: "max" },
  {
    providerId: "kilo",
    modelId: "x-ai/grok-code-fast-1:optimized:free",
    tier: "balanced",
    notes: "代码专用",
  },

  // 美团 LongCat (PascalCase IDs, 官方文档校对)
  {
    providerId: "longcat",
    modelId: "LongCat-Flash-Lite",
    tier: "fast",
    notes: "5000万 tokens/day 独立免费",
  },
  {
    providerId: "longcat",
    modelId: "LongCat-Flash-Chat",
    tier: "balanced",
    notes: "高性能通用对话",
  },
  {
    providerId: "longcat",
    modelId: "LongCat-Flash-Thinking",
    tier: "max",
    notes: "深度思考(已升级到 -2601)",
  },
  {
    providerId: "longcat",
    modelId: "LongCat-Flash-Thinking-2601",
    tier: "max",
    notes: "深度思考升级版",
  },
  {
    providerId: "longcat",
    modelId: "LongCat-Flash-Omni-2603",
    tier: "max",
    notes: "多模态,仅 OpenAI 格式",
  },
  {
    providerId: "longcat",
    modelId: "LongCat-Flash-Chat-2602-Exp",
    tier: "balanced",
    notes: "实验版,仅 OpenAI 格式",
  },
  {
    providerId: "longcat",
    modelId: "LongCat-2.0-Preview",
    tier: "max",
    notes: "Agentic 旗舰,内测 10M/2h",
  },

  // 讯飞星辰 — 仅含 inferencePrice in/out 都为 0 且属于"文本生成"类
  // (源:list-v2 接口枚举,2026-04 复核;OCR/embedding/SD/rerank/MT 走单独端点不在此列)
  { providerId: "xunfei", modelId: "xop35qwen2b", tier: "fast", notes: "Qwen3.5-2B 永久免费" },
  { providerId: "xunfei", modelId: "test_ent", tier: "fast", notes: "Qwen3-1.7B 永久免费" },
  { providerId: "xunfei", modelId: "xophunyuan7bmt", tier: "fast", notes: "Hunyuan-MT-7B 永久免费" },

  // Gitee AI (永久免费 text-generation,源自 /api/pay/.../services?type=serverless)
  { providerId: "gitee", modelId: "internlm3-8b-instruct", tier: "fast", notes: "InternLM3-8B · 永久免费" },
  { providerId: "gitee", modelId: "Qwen3-8B", tier: "fast", notes: "永久免费" },
  { providerId: "gitee", modelId: "Qwen3-4B", tier: "fast", notes: "永久免费" },
  { providerId: "gitee", modelId: "Qwen3-0.6B", tier: "fast", notes: "极速小模型" },
  { providerId: "gitee", modelId: "Qwen2-7B-Instruct", tier: "fast", notes: "永久免费" },
  {
    providerId: "gitee",
    modelId: "DeepSeek-R1-Distill-Qwen-14B",
    tier: "balanced",
    notes: "推理蒸馏 · 永久免费",
  },
  {
    providerId: "gitee",
    modelId: "DeepSeek-R1-Distill-Qwen-7B",
    tier: "fast",
    notes: "推理蒸馏 7B",
  },
  {
    providerId: "gitee",
    modelId: "DeepSeek-R1-Distill-Qwen-1.5B",
    tier: "fast",
    notes: "推理蒸馏 1.5B 极速",
  },
  { providerId: "gitee", modelId: "GLM-4.7-Flash", tier: "fast", notes: "30B SOTA · 免费" },
  { providerId: "gitee", modelId: "GLM-4-9B-0414", tier: "fast", notes: "智谱 9B" },
  { providerId: "gitee", modelId: "glm-4-9b-chat", tier: "fast", notes: "智谱 9B chat" },

  // AIHubMix
  { providerId: "aihubmix", modelId: "gpt-4o-mini", tier: "fast" },
  { providerId: "aihubmix", modelId: "gpt-4o", tier: "balanced", notes: "多模态" },
  { providerId: "aihubmix", modelId: "claude-opus-4-7", tier: "max" },
  { providerId: "aihubmix", modelId: "gemini-2.0-flash", tier: "fast" },
  { providerId: "aihubmix", modelId: "deepseek-v4-flash", tier: "fast" },
  { providerId: "aihubmix", modelId: "deepseek-r1", tier: "max", notes: "推理增强" },
];

const freeModelMap = new Map<string, FreeModel>();
for (const m of FREE_MODELS) {
  freeModelMap.set(m.modelId, m);
  freeModelMap.set(m.modelId.toLowerCase(), m);
}

/** 按 modelId 反查是否为已知免费模型(返回 tier/provider 等). */
export function findFreeModel(modelId: string): FreeModel | undefined {
  if (!modelId) {
    return undefined;
  }
  return freeModelMap.get(modelId) || freeModelMap.get(modelId.toLowerCase());
}

/**
 * 三层免费检测,统一入口:
 *
 * 1. provider 自己的 freeFromMeta adapter (有 upstream meta 时, 最高置信度)
 * 2. 静态 FREE_MODELS map (provider-aware; 无 providerId 时全局回退)
 * 3. modelId 启发式 (`:free` 后缀等, 跨 provider 通用)
 *
 * 返回值:
 * - true  = 明确免费
 * - false = 明确付费 (仅 tier 1 adapter 能给出)
 * - null  = 未知 (不要当作付费!)
 */
export function isFree(
  providerId: string | undefined,
  modelId: string,
  upstreamMeta?: unknown
): boolean | null {
  if (!modelId) {
    return null;
  }

  // Tier 0: FreeModels Registry (聚合 9 家 provider 的实时清单, 6h 刷新)
  // 在所有静态规则之前查 — 这是单一可信源,优先级最高。
  // 注: 动态 import 避免循环依赖, 模块加载时 registry 可能还没初始化。
  const r0 = isFreeFromRegistry(providerId, modelId);
  if (r0 !== null) {
    return r0;
  }

  // Tier 1: provider adapter
  if (providerId && upstreamMeta != null) {
    const provider = FREE_PROVIDERS.find(p => p.id === providerId);
    if (provider?.freeFromMeta) {
      const r = provider.freeFromMeta(upstreamMeta);
      if (r !== null) {
        return r;
      }
    }
  }

  // Tier 2: 静态 FREE_MODELS map
  if (providerId) {
    const exact = FREE_MODELS.find(
      m => m.providerId === providerId && m.modelId === modelId
    );
    if (exact) {
      return true;
    }
    const ci = FREE_MODELS.find(
      m =>
        m.providerId === providerId && m.modelId.toLowerCase() === modelId.toLowerCase()
    );
    if (ci) {
      return true;
    }
  } else {
    // 无 providerId:全局回退
    if (freeModelMap.has(modelId) || freeModelMap.has(modelId.toLowerCase())) {
      return true;
    }
  }

  // Tier 3: 启发式 (跨 provider 通用)
  const lower = modelId.toLowerCase();
  // OpenRouter / 部分 router 用 :free 标识
  if (lower.endsWith(":free")) {
    return true;
  }
  // 名字里独立的 free 段(如 "llama-3-free", "model_free_v1") — 排除 freeway / freeform
  if (/(^|[/_-])free([/_-]|$)/.test(lower)) {
    return true;
  }

  return null;
}

/**
 * 建组时计算 exposed_models 初始集 (specified 模式下生效).
 * 取已知 provider 的免费模型并集 + 用户选定的 testModel,做去重.
 *
 * - 已知 provider:FREE_MODELS 命中的 + provider.models 里 isFree() 为 true 的
 * - 用户选定的 testModel:总是包含 (避免"建好了但调不通")
 * - 未知 provider (provider 为 undefined):仅 [testModel]
 */
export function bootstrapExposedModels(
  provider: FreeProvider | undefined,
  selectedTestModel?: string
): string[] {
  const set = new Set<string>();
  if (provider) {
    for (const m of FREE_MODELS) {
      if (m.providerId === provider.id) {
        set.add(m.modelId);
      }
    }
    // provider.models 里靠启发式判定为免费的 (e.g. openrouter/auto, *:free)
    for (const m of provider.models) {
      if (isFree(provider.id, m) === true) {
        set.add(m);
      }
    }
  }
  if (selectedTestModel) {
    set.add(selectedTestModel);
  }
  return Array.from(set);
}

/**
 * 把 provider 的 obfuscated model ID 还原成人类可读名 (讯飞专用 — 其他 provider
 * 没有这层映射, 直接 fallthrough 返回原 ID).
 *
 * 仅用于 UI 展示。`available_models` / chat completions 请求体 / 测试模型探活
 * 都仍然使用原始 ID — 上游 API 不认 friendly name。
 *
 * 当 provider 已知时优先用 provider 自带的 modelNames; 不知道 provider 时 (比如
 * 聚合分组里跨子分组并集后丢失了源信息), 走 GLOBAL_MODEL_NAMES 全局兜底 — 因为
 * 各 provider 的 modelNames key 是 obfuscated ID, 跨 provider 撞名概率极低.
 */
export function displayModelName(
  provider: FreeProvider | undefined,
  modelId: string
): string {
  if (provider?.modelNames?.[modelId]) {
    return provider.modelNames[modelId];
  }
  return GLOBAL_MODEL_NAMES[modelId] || modelId;
}

const GLOBAL_MODEL_NAMES: Record<string, string> = (() => {
  const out: Record<string, string> = {};
  for (const p of FREE_PROVIDERS) {
    if (!p.modelNames) {
      continue;
    }
    for (const [id, name] of Object.entries(p.modelNames)) {
      // 后写入的不会覆盖前面 (撞 key 极少, 但保护一下确定性)
      if (!(id in out)) {
        out[id] = name;
      }
    }
  }
  return out;
})();
