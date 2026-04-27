// Heuristic key-format detector for AUTO_PROVIDER selection.
// Only high-confidence prefix/pattern rules; never guesses beyond what's distinctive.

export interface KeyDetection {
  providerId: string;
  providerName: string;
  confidence: "high" | "medium";
  hint: string;
}

interface Rule {
  test: (k: string) => boolean;
  providerId: string;
  confidence: "high" | "medium";
  hint: string;
}

// Order matters: more-specific rules first.
const RULES: Rule[] = [
  // Google AI Studio — AIza + 35 alphanum chars (always 39 total)
  {
    test: k => /^AIza[A-Za-z0-9_-]{35}$/.test(k),
    providerId: "google-aistudio",
    confidence: "high",
    hint: "AIza 前缀 · 39 位",
  },
  // Anthropic
  {
    test: k => k.startsWith("sk-ant-"),
    providerId: "anthropic",
    confidence: "high",
    hint: "sk-ant- 前缀",
  },
  // OpenRouter
  {
    test: k => k.startsWith("sk-or-v1-"),
    providerId: "openrouter",
    confidence: "high",
    hint: "sk-or-v1- 前缀",
  },
  // OpenAI new project key
  {
    test: k => k.startsWith("sk-proj-"),
    providerId: "openai",
    confidence: "high",
    hint: "sk-proj- 前缀",
  },
  // Groq
  {
    test: k => k.startsWith("gsk_"),
    providerId: "groq",
    confidence: "high",
    hint: "gsk_ 前缀",
  },
  // Hugging Face
  {
    test: k => /^hf_[A-Za-z0-9]{20,}/.test(k),
    providerId: "huggingface",
    confidence: "high",
    hint: "hf_ 前缀",
  },
  // GitHub fine-grained PAT or classic PAT
  {
    test: k => k.startsWith("github_pat_") || /^gh[pousr]_[A-Za-z0-9]{36,}/.test(k),
    providerId: "github-models",
    confidence: "high",
    hint: "github_pat_ / ghp_ 前缀",
  },
  // NVIDIA NIM
  {
    test: k => k.startsWith("nvapi-"),
    providerId: "nvidia-nim",
    confidence: "high",
    hint: "nvapi- 前缀",
  },
  // Cerebras
  {
    test: k => k.startsWith("csk-"),
    providerId: "cerebras",
    confidence: "high",
    hint: "csk- 前缀",
  },
  // Cohere trial keys — UUID v4 shape (36 chars, hyphens at 8-13-18-23)
  {
    test: k => /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(k),
    providerId: "cohere",
    confidence: "medium",
    hint: "UUID 格式 · Cohere Trial Key",
  },
  // Generic sk- — could be OpenAI (old), Together, SiliconFlow, AIHubMix…
  {
    test: k => /^sk-[A-Za-z0-9]{20,}$/.test(k),
    providerId: "openai",
    confidence: "medium",
    hint: "sk- 前缀（通用 OpenAI 兼容格式）",
  },
];

const PROVIDER_NAMES: Record<string, string> = {
  "google-aistudio": "Google AI Studio",
  anthropic: "Anthropic",
  openrouter: "OpenRouter",
  openai: "OpenAI",
  groq: "Groq Cloud",
  huggingface: "Hugging Face",
  "github-models": "GitHub Models",
  "nvidia-nim": "NVIDIA NIM",
  cerebras: "Cerebras",
  cohere: "Cohere",
};

/** Detect provider from a single key string. Returns null if unrecognised. */
export function detectKey(key: string): KeyDetection | null {
  const k = key.trim();
  if (k.length < 8) return null;
  for (const rule of RULES) {
    if (rule.test(k)) {
      return {
        providerId: rule.providerId,
        providerName: PROVIDER_NAMES[rule.providerId] ?? rule.providerId,
        confidence: rule.confidence,
        hint: rule.hint,
      };
    }
  }
  return null;
}

/**
 * Detect the dominant provider from a multi-line / comma-separated key blob.
 * Returns the provider matched by the most keys, or null if nothing recognised.
 */
export function detectFromText(text: string): KeyDetection | null {
  if (!text.trim()) return null;
  const lines = text.split(/[\n,]/).map(s => s.trim()).filter(Boolean);
  if (!lines.length) return null;

  const tally = new Map<string, { det: KeyDetection; count: number }>();
  for (const line of lines) {
    const d = detectKey(line);
    if (!d) continue;
    const entry = tally.get(d.providerId);
    if (entry) entry.count++;
    else tally.set(d.providerId, { det: d, count: 1 });
  }

  if (!tally.size) return null;

  let best: { det: KeyDetection; count: number } | null = null;
  for (const v of tally.values()) {
    if (!best || v.count > best.count) best = v;
  }
  return best?.det ?? null;
}
