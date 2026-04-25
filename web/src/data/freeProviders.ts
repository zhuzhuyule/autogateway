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
  modelId: string;    // 上游真实模型 ID
  tier: ModelTier;
  notes?: string;     // 限速/特性等
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
  recommendedGroupName: string;
  recommendedDisplayName: string;
  upstreamHosts: string[];
  badge?: "fast" | "high-quota" | "multi-model";
  verifiedAt: string;
}

export const FREE_PROVIDERS: FreeProvider[] = [
  {
    id: "groq",
    name: "Groq Cloud",
    freeTier: "14400 requests/day",
    description: "LPU 推理极速,Llama / Mixtral / Whisper",
    signupUrl: "https://console.groq.com/keys",
    docsUrl: "https://console.groq.com/docs",
    channelType: "openai",
    baseUrl: "https://api.groq.com/openai",
    testModel: "llama-3.3-70b-versatile",
    models: ["llama-3.3-70b-versatile", "llama-3.1-8b-instant", "mixtral-8x7b-32768"],
    recommendedGroupName: "groq",
    recommendedDisplayName: "Groq Cloud",
    upstreamHosts: ["api.groq.com"],
    badge: "fast",
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
    models: ["llama3.1-8b", "llama-3.3-70b", "qwen-3-235b-a22b-instruct-2507"],
    recommendedGroupName: "cerebras",
    recommendedDisplayName: "Cerebras Cloud",
    upstreamHosts: ["api.cerebras.ai"],
    badge: "fast",
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
      "deepseek/deepseek-chat-v3:free",
      "meta-llama/llama-3.1-8b-instruct:free",
      "google/gemini-2.0-flash-exp:free",
    ],
    recommendedGroupName: "openrouter",
    recommendedDisplayName: "OpenRouter",
    upstreamHosts: ["openrouter.ai"],
    badge: "multi-model",
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
    models: ["@cf/meta/llama-3.1-8b-instruct", "@cf/qwen/qwen2.5-coder-32b-instruct"],
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
    models: ["mistral-small-latest", "open-mistral-nemo", "codestral-latest"],
    recommendedGroupName: "mistral",
    recommendedDisplayName: "Mistral AI",
    upstreamHosts: ["api.mistral.ai"],
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
    models: ["gemini-2.0-flash", "gemini-2.5-flash", "gemini-2.5-pro"],
    recommendedGroupName: "gemini",
    recommendedDisplayName: "Google Gemini",
    upstreamHosts: ["generativelanguage.googleapis.com"],
    badge: "high-quota",
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
    models: ["gpt-4o-mini", "Meta-Llama-3.1-70B-Instruct", "Mistral-large-2407"],
    recommendedGroupName: "github-models",
    recommendedDisplayName: "GitHub Models",
    upstreamHosts: ["models.inference.ai.azure.com"],
    badge: "multi-model",
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
    models: ["meta-llama/Llama-3.1-8B-Instruct", "Qwen/Qwen2.5-72B-Instruct"],
    recommendedGroupName: "huggingface",
    recommendedDisplayName: "Hugging Face Router",
    upstreamHosts: ["router.huggingface.co"],
    badge: "multi-model",
    verifiedAt: "2026-04",
  },
];

export function getProviderById(id: string): FreeProvider | undefined {
  return FREE_PROVIDERS.find((p) => p.id === id);
}

function extractHost(url: string): string {
  if (!url) return "";
  try {
    return new URL(url).host.toLowerCase();
  } catch {
    return "";
  }
}

export function findProviderByUpstreamUrl(url: string): FreeProvider | undefined {
  const host = extractHost(url);
  if (!host) return undefined;
  return FREE_PROVIDERS.find((p) =>
    p.upstreamHosts.some((h) => h.toLowerCase() === host),
  );
}

export function findProviderByUpstreams(
  upstreams: Array<{ url?: string }> | undefined | null,
): FreeProvider | undefined {
  if (!upstreams || upstreams.length === 0) return undefined;
  for (const u of upstreams) {
    const matched = findProviderByUpstreamUrl(u.url || "");
    if (matched) return matched;
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
  { providerId: "groq", modelId: "mixtral-8x7b-32768", tier: "balanced" },
  { providerId: "groq", modelId: "deepseek-r1-distill-llama-70b", tier: "max", notes: "推理增强" },

  // Cerebras
  { providerId: "cerebras", modelId: "llama3.1-8b", tier: "fast" },
  { providerId: "cerebras", modelId: "llama-3.3-70b", tier: "balanced" },
  { providerId: "cerebras", modelId: "qwen-3-235b-a22b-instruct-2507", tier: "max", notes: "MoE 235B" },

  // OpenRouter (含 :free 后缀)
  { providerId: "openrouter", modelId: "deepseek/deepseek-chat-v3:free", tier: "balanced" },
  { providerId: "openrouter", modelId: "meta-llama/llama-3.1-8b-instruct:free", tier: "fast" },
  { providerId: "openrouter", modelId: "google/gemini-2.0-flash-exp:free", tier: "balanced", notes: "实验版" },
  { providerId: "openrouter", modelId: "qwen/qwen-2.5-72b-instruct:free", tier: "balanced" },
  { providerId: "openrouter", modelId: "mistralai/mistral-7b-instruct:free", tier: "fast" },

  // Together AI(免费档模型)
  { providerId: "together", modelId: "meta-llama/Llama-3.3-70B-Instruct-Turbo-Free", tier: "balanced" },
  { providerId: "together", modelId: "deepseek-ai/DeepSeek-R1-Distill-Llama-70B-free", tier: "max", notes: "推理增强" },

  // Cloudflare Workers AI
  { providerId: "cloudflare", modelId: "@cf/meta/llama-3.1-8b-instruct", tier: "fast" },
  { providerId: "cloudflare", modelId: "@cf/qwen/qwen2.5-coder-32b-instruct", tier: "balanced", notes: "代码专用" },

  // Mistral Experimental
  { providerId: "mistral", modelId: "mistral-small-latest", tier: "fast" },
  { providerId: "mistral", modelId: "open-mistral-nemo", tier: "fast" },
  { providerId: "mistral", modelId: "codestral-latest", tier: "balanced", notes: "代码专用" },

  // Google AI Studio
  { providerId: "google-aistudio", modelId: "gemini-2.0-flash", tier: "balanced", notes: "原生多模态" },
  { providerId: "google-aistudio", modelId: "gemini-2.5-flash", tier: "balanced", notes: "原生多模态" },
  { providerId: "google-aistudio", modelId: "gemini-2.5-pro", tier: "max", notes: "长上下文+视觉" },

  // Cohere
  { providerId: "cohere", modelId: "command-r7b-12-2024", tier: "fast" },
  { providerId: "cohere", modelId: "command-r-08-2024", tier: "balanced" },
  { providerId: "cohere", modelId: "command-r-plus-08-2024", tier: "max" },

  // GitHub Models (Azure 托管)
  { providerId: "github-models", modelId: "gpt-4o-mini", tier: "fast" },
  { providerId: "github-models", modelId: "gpt-4o", tier: "max", notes: "多模态" },
  { providerId: "github-models", modelId: "Meta-Llama-3.1-70B-Instruct", tier: "balanced" },
  { providerId: "github-models", modelId: "Mistral-large-2407", tier: "balanced" },

  // Hugging Face Router
  { providerId: "huggingface", modelId: "meta-llama/Llama-3.1-8B-Instruct", tier: "fast" },
  { providerId: "huggingface", modelId: "Qwen/Qwen2.5-72B-Instruct", tier: "balanced" },
];

const freeModelMap = new Map<string, FreeModel>();
for (const m of FREE_MODELS) {
  freeModelMap.set(m.modelId, m);
  freeModelMap.set(m.modelId.toLowerCase(), m);
}

/** 按 modelId 反查是否为已知免费模型(返回 tier/provider 等). */
export function findFreeModel(modelId: string): FreeModel | undefined {
  if (!modelId) return undefined;
  return freeModelMap.get(modelId) || freeModelMap.get(modelId.toLowerCase());
}
