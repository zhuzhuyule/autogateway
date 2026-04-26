/**
 * AutoGateway v3 — design-bundled provider directory and model catalog.
 * Used by Dashboard, Auto Routing and Group flows when no live data is
 * available yet. Replace with API-fed data once backend endpoints land.
 */

export type V3ChannelKind = "openai" | "anthropic" | "gemini";

export interface V3Provider {
  id: string;
  name: string;
  short: string;
  channel: V3ChannelKind;
  free: string;
  keyUrl: string;
  host: string;
  badge?: "fast" | "high" | "multi";
}

export const V3_PROVIDER_DIR: Record<string, V3Provider> = {
  groq: {
    id: "groq",
    name: "Groq Cloud",
    short: "GR",
    channel: "openai",
    free: "14400 req/day",
    keyUrl: "https://console.groq.com/keys",
    host: "api.groq.com",
    badge: "fast",
  },
  cerebras: {
    id: "cerebras",
    name: "Cerebras",
    short: "CB",
    channel: "openai",
    free: "限速 · 无每日封顶",
    keyUrl: "https://cloud.cerebras.ai/platform",
    host: "api.cerebras.ai",
    badge: "fast",
  },
  openrouter: {
    id: "openrouter",
    name: "OpenRouter",
    short: "OR",
    channel: "openai",
    free: ":free 模型免费",
    keyUrl: "https://openrouter.ai/keys",
    host: "openrouter.ai",
    badge: "multi",
  },
  together: {
    id: "together",
    name: "Together AI",
    short: "TG",
    channel: "openai",
    free: "$1 赠金 + 免费档",
    keyUrl: "https://api.together.ai/settings/api-keys",
    host: "api.together.xyz",
  },
  cloudflare: {
    id: "cloudflare",
    name: "Cloudflare AI",
    short: "CF",
    channel: "openai",
    free: "10000 neuron/day",
    keyUrl: "https://dash.cloudflare.com",
    host: "api.cloudflare.com",
  },
  mistral: {
    id: "mistral",
    name: "Mistral",
    short: "MI",
    channel: "openai",
    free: "Experimental tier",
    keyUrl: "https://console.mistral.ai/api-keys",
    host: "api.mistral.ai",
  },
  google: {
    id: "google",
    name: "Google AI Studio",
    short: "GG",
    channel: "gemini",
    free: "高免费配额",
    keyUrl: "https://aistudio.google.com/apikey",
    host: "generativelanguage.googleapis.com",
    badge: "high",
  },
  cohere: {
    id: "cohere",
    name: "Cohere",
    short: "CO",
    channel: "openai",
    free: "Trial Key",
    keyUrl: "https://dashboard.cohere.com/api-keys",
    host: "api.cohere.ai",
  },
  github: {
    id: "github",
    name: "GitHub Models",
    short: "GH",
    channel: "openai",
    free: "GitHub 账号免费",
    keyUrl: "https://github.com/settings/tokens",
    host: "models.inference.ai.azure.com",
    badge: "multi",
  },
  anthropic: {
    id: "anthropic",
    name: "Anthropic",
    short: "AN",
    channel: "anthropic",
    free: "付费",
    keyUrl: "https://console.anthropic.com/settings/keys",
    host: "api.anthropic.com",
  },
};

export type ModelTier = "simple" | "medium" | "complex";

export interface V3Model {
  id: string;
  provider: string;
  context: string;
  level: ModelTier;
  speed: "slow" | "med" | "fast" | "ultra";
  price: number;
  tools: boolean;
  vision: boolean;
}

export const V3_MODEL_CATALOG: V3Model[] = [
  {
    id: "llama-3.3-70b-versatile",
    provider: "groq",
    context: "131k",
    level: "complex",
    speed: "ultra",
    price: 0.59,
    tools: true,
    vision: false,
  },
  {
    id: "llama-3.3-70b-versatile",
    provider: "cerebras",
    context: "131k",
    level: "complex",
    speed: "ultra",
    price: 0.85,
    tools: true,
    vision: false,
  },
  {
    id: "llama-3.3-70b-versatile",
    provider: "together",
    context: "131k",
    level: "complex",
    speed: "fast",
    price: 0.88,
    tools: true,
    vision: false,
  },
  {
    id: "llama-3.1-8b-instant",
    provider: "groq",
    context: "131k",
    level: "simple",
    speed: "ultra",
    price: 0.05,
    tools: true,
    vision: false,
  },
  {
    id: "llama-3.1-8b-instant",
    provider: "cerebras",
    context: "131k",
    level: "simple",
    speed: "ultra",
    price: 0.1,
    tools: true,
    vision: false,
  },
  {
    id: "deepseek/deepseek-chat-v3:free",
    provider: "openrouter",
    context: "164k",
    level: "complex",
    speed: "med",
    price: 0.0,
    tools: true,
    vision: false,
  },
  {
    id: "deepseek/deepseek-r1:free",
    provider: "openrouter",
    context: "164k",
    level: "complex",
    speed: "slow",
    price: 0.0,
    tools: false,
    vision: false,
  },
  {
    id: "google/gemini-2.0-flash-exp:free",
    provider: "openrouter",
    context: "1m",
    level: "medium",
    speed: "fast",
    price: 0.0,
    tools: true,
    vision: true,
  },
  {
    id: "gemini-2.5-flash",
    provider: "google",
    context: "1m",
    level: "medium",
    speed: "fast",
    price: 0.3,
    tools: true,
    vision: true,
  },
  {
    id: "gemini-2.5-pro",
    provider: "google",
    context: "2m",
    level: "complex",
    speed: "med",
    price: 2.5,
    tools: true,
    vision: true,
  },
  {
    id: "gemini-2.0-flash",
    provider: "google",
    context: "1m",
    level: "medium",
    speed: "ultra",
    price: 0.1,
    tools: true,
    vision: true,
  },
  {
    id: "gpt-4o-mini",
    provider: "github",
    context: "128k",
    level: "medium",
    speed: "fast",
    price: 0.15,
    tools: true,
    vision: true,
  },
  {
    id: "gpt-4o",
    provider: "github",
    context: "128k",
    level: "complex",
    speed: "med",
    price: 2.5,
    tools: true,
    vision: true,
  },
  {
    id: "Meta-Llama-3.1-70B-Instruct",
    provider: "github",
    context: "128k",
    level: "complex",
    speed: "med",
    price: 0.0,
    tools: true,
    vision: false,
  },
  {
    id: "Meta-Llama-3.1-70B-Instruct",
    provider: "together",
    context: "128k",
    level: "complex",
    speed: "fast",
    price: 0.88,
    tools: true,
    vision: false,
  },
  {
    id: "command-r-plus-08-2024",
    provider: "cohere",
    context: "128k",
    level: "complex",
    speed: "med",
    price: 2.5,
    tools: true,
    vision: false,
  },
  {
    id: "command-r7b-12-2024",
    provider: "cohere",
    context: "128k",
    level: "simple",
    speed: "fast",
    price: 0.04,
    tools: true,
    vision: false,
  },
  {
    id: "mistral-small-latest",
    provider: "mistral",
    context: "32k",
    level: "simple",
    speed: "fast",
    price: 0.2,
    tools: true,
    vision: false,
  },
  {
    id: "codestral-latest",
    provider: "mistral",
    context: "32k",
    level: "medium",
    speed: "fast",
    price: 0.3,
    tools: true,
    vision: false,
  },
  {
    id: "claude-sonnet-4-5",
    provider: "anthropic",
    context: "200k",
    level: "complex",
    speed: "med",
    price: 3.0,
    tools: true,
    vision: true,
  },
  {
    id: "claude-haiku-4-5",
    provider: "anthropic",
    context: "200k",
    level: "medium",
    speed: "fast",
    price: 0.8,
    tools: true,
    vision: true,
  },
];

export interface V3TopModel {
  id: string;
  calls: number;
  providers: string[];
  avgMs: number;
  errors: number;
  tier: ModelTier;
  trend: number[];
}

export const V3_TOP_MODELS: V3TopModel[] = [
  {
    id: "llama-3.3-70b-versatile",
    calls: 142800,
    providers: ["groq", "cerebras", "together"],
    avgMs: 612,
    errors: 0.018,
    tier: "complex",
    trend: [3, 5, 4, 7, 9, 8, 11, 12],
  },
  {
    id: "gemini-2.5-flash",
    calls: 88420,
    providers: ["google"],
    avgMs: 538,
    errors: 0.012,
    tier: "medium",
    trend: [2, 3, 3, 5, 6, 7, 8, 9],
  },
  {
    id: "llama-3.1-8b-instant",
    calls: 64200,
    providers: ["groq", "cerebras"],
    avgMs: 184,
    errors: 0.008,
    tier: "simple",
    trend: [4, 5, 6, 5, 7, 8, 7, 9],
  },
  {
    id: "claude-sonnet-4-5",
    calls: 22100,
    providers: ["anthropic"],
    avgMs: 1240,
    errors: 0.022,
    tier: "complex",
    trend: [1, 2, 2, 3, 4, 4, 5, 6],
  },
  {
    id: "gpt-4o-mini",
    calls: 18900,
    providers: ["github"],
    avgMs: 824,
    errors: 0.061,
    tier: "medium",
    trend: [2, 4, 3, 5, 4, 3, 5, 4],
  },
  {
    id: "gemini-2.5-pro",
    calls: 9420,
    providers: ["google"],
    avgMs: 1820,
    errors: 0.024,
    tier: "complex",
    trend: [1, 1, 2, 2, 3, 3, 4, 5],
  },
];

export interface V3HeatRow {
  name: string;
  cells: (number | "e")[];
}

export function buildHeatRow(errAt = -1, len = 48): (number | "e")[] {
  return Array.from({ length: len }, (_, i) => {
    if (i === errAt) {
      return "e";
    }
    return Math.floor(Math.random() * 5);
  });
}

export const V3_HEAT_DATA: V3HeatRow[] = [
  { name: "groq", cells: buildHeatRow(31) },
  { name: "cerebras", cells: buildHeatRow() },
  { name: "openrouter", cells: buildHeatRow() },
  { name: "google", cells: buildHeatRow(38) },
  { name: "anthropic", cells: buildHeatRow() },
  { name: "github", cells: buildHeatRow(14) },
];

export function pavClass(providerId?: string): string {
  if (!providerId) {
    return "v3-pav v3-pav-default";
  }
  const known = V3_PROVIDER_DIR[providerId] ? providerId : "default";
  return `v3-pav v3-pav-${known}`;
}
