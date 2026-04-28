// 免费 LLM API 提供商精选清单
// 数据参考: https://github.com/mnfst/awesome-free-llm-apis (MIT)
// 同 provider 可能有多个 upstream host,反查时任一命中即视为关联。
// 仅 host 完全相等才匹配,避免误伤(如 *.openai.com 与 api.openai.com 子集冲突)。

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
    baseUrl: "https://api.groq.com/openai",
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
    baseUrl: "https://api.cerebras.ai",
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
    baseUrl: "https://openrouter.ai/api",
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
    baseUrl: "https://api.together.xyz",
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
    baseUrl: "https://api.mistral.ai",
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
    id: "google-aistudio",
    name: "Google AI Studio",
    freeTier: "免费档(每模型独立配额)",
    description: "Gemini 2.0/2.5 Flash,原生多模态",
    signupUrl: "https://aistudio.google.com/apikey",
    docsUrl: "https://ai.google.dev/gemini-api/docs",
    channelType: "gemini",
    baseUrl: "https://generativelanguage.googleapis.com",
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
    id: "zhipu",
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
    id: "nvidia-nim",
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
    freeTier: "500K tokens/day · Flash-Lite 5000万 tokens/day",
    description: "美团旗下长文本模型，256K上下文，OpenAI 兼容",
    signupUrl: "https://longcat.chat/platform",
    docsUrl: "https://longcat.chat/platform/docs/zh/",
    channelType: "openai",
    baseUrl: "https://api.longcat.chat/openai",
    testModel: "longcat-flash-lite",
    models: [
      "longcat-flash-lite",
      "longcat-flash-chat",
      "longcat-flash-thinking",
      "longcat-2.0-preview",
    ],
    recommendedGroupName: "longcat",
    recommendedDisplayName: "美团 LongCat",
    upstreamHosts: ["api.longcat.chat"],
    badge: "high-quota",
    verifiedAt: "2026-04",
  },
  {
    id: "xfyun",
    name: "讯飞星辰",
    freeTier: "含永久免费模型（Qwen3.5-2B 等）",
    description: "科大讯飞 MaaS 平台，65+ 模型，Key 格式 APIKey:APISecret",
    signupUrl: "https://maas.xfyun.cn",
    docsUrl: "https://maas.xfyun.cn/modelService",
    channelType: "openai",
    baseUrl: "https://maas-api.cn-huabei-1.xf-yun.com/v2",
    testModel: "xop35qwen2b",
    models: ["xop35qwen2b", "test_ent"],
    paidModels: [
      "xop3qwen32b",
      "xop3qwen30b",
      "xop3qwen8b",
      "xdeepseekv3",
      "xdeepseekr1",
      "xdeepseekr1qwen32b",
      "xdeepseekr1qwen7b",
      "xopglm47blth2",
      "xopkimik25",
      "xopglm5",
      "xopdeepseekv32",
    ],
    recommendedGroupName: "xfyun",
    recommendedDisplayName: "讯飞星辰",
    upstreamHosts: ["maas-api.cn-huabei-1.xf-yun.com"],
    verifiedAt: "2026-04",
  },
  {
    id: "gitee-ai",
    name: "Gitee AI",
    freeTier: "永久免费小模型 + 注册体验额度",
    description: "模力方舟 Serverless API,Qwen3 / GLM-Flash / DeepSeek-Distill",
    signupUrl: "https://ai.gitee.com/dashboard/settings/tokens",
    docsUrl: "https://ai.gitee.com/docs/openapi/v1",
    channelType: "openai",
    baseUrl: "https://ai.gitee.com/v1",
    testModel: "internlm3-8b-instruct",
    models: [
      "internlm3-8b-instruct",
      "Qwen3-8B",
      "Qwen3-4B",
      "DeepSeek-R1-Distill-Qwen-14B",
      "GLM-4.7-Flash",
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
  return FREE_PROVIDERS.find(p => p.upstreamHosts.some(h => h.toLowerCase() === host));
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
    providerId: "google-aistudio",
    modelId: "gemini-2.5-flash",
    tier: "balanced",
    notes: "原生多模态,1M 上下文",
  },
  {
    providerId: "google-aistudio",
    modelId: "gemini-2.5-flash-lite",
    tier: "fast",
    notes: "1M 上下文",
  },
  { providerId: "google-aistudio", modelId: "gemini-2.0-flash", tier: "fast", notes: "原生多模态" },

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
  { providerId: "zhipu", modelId: "GLM-4-Flash", tier: "fast", notes: "免费旗舰" },
  { providerId: "zhipu", modelId: "GLM-4V-Flash", tier: "fast", notes: "视觉+文本" },
  { providerId: "zhipu", modelId: "GLM-Z1-Flash", tier: "balanced", notes: "推理增强" },

  // NVIDIA NIM
  { providerId: "nvidia-nim", modelId: "deepseek-ai/deepseek-r1", tier: "max", notes: "推理增强" },
  { providerId: "nvidia-nim", modelId: "nvidia/llama-3.1-nemotron-ultra-253b-v1", tier: "max" },
  { providerId: "nvidia-nim", modelId: "nvidia/nemotron-3-super-120b-a12b", tier: "max" },
  { providerId: "nvidia-nim", modelId: "nvidia/nemotron-3-nano-30b-a3b", tier: "balanced" },
  { providerId: "nvidia-nim", modelId: "meta/llama-3.1-405b-instruct", tier: "max" },
  { providerId: "nvidia-nim", modelId: "qwen/qwen2.5-72b-instruct", tier: "balanced" },
  { providerId: "nvidia-nim", modelId: "google/gemma-4-31b", tier: "balanced" },

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

  // 美团 LongCat
  {
    providerId: "longcat",
    modelId: "longcat-flash-lite",
    tier: "fast",
    notes: "5000万 tokens/day 免费",
  },
  {
    providerId: "longcat",
    modelId: "longcat-flash-chat",
    tier: "balanced",
    notes: "500K tokens/day",
  },
  {
    providerId: "longcat",
    modelId: "longcat-flash-thinking",
    tier: "max",
    notes: "推理模型,500K tokens/day",
  },

  // 讯飞星辰（免费模型 price=0）
  { providerId: "xfyun", modelId: "xop35qwen2b", tier: "fast", notes: "Qwen3.5-2B 免费" },
  { providerId: "xfyun", modelId: "test_ent", tier: "fast", notes: "Qwen3-1.7B 免费" },
  { providerId: "xfyun", modelId: "xop3qwen8b", tier: "balanced" },
  { providerId: "xfyun", modelId: "xdeepseekr1", tier: "max", notes: "推理增强" },

  // Gitee AI (永久免费小模型)
  { providerId: "gitee-ai", modelId: "internlm3-8b-instruct", tier: "fast", notes: "永久免费" },
  { providerId: "gitee-ai", modelId: "Qwen3-8B", tier: "fast", notes: "永久免费" },
  { providerId: "gitee-ai", modelId: "Qwen3-4B", tier: "fast", notes: "永久免费" },
  {
    providerId: "gitee-ai",
    modelId: "DeepSeek-R1-Distill-Qwen-14B",
    tier: "balanced",
    notes: "推理蒸馏 · 永久免费",
  },
  { providerId: "gitee-ai", modelId: "GLM-4.7-Flash", tier: "fast", notes: "30B SOTA · 免费" },

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
