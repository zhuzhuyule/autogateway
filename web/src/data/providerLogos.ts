// 内置官方品牌 logo,从 @lobehub/icons-static-svg 取静态 SVG。
// SVG 用 fill="currentColor",随容器 color 改变,无需在线 favicon。
//
// 新加 provider:
//   1. 在 LOGO_MAP 加一行: <key>: <import-of-svg-raw>
//   2. 在 KEYWORD_TO_KEY 加关键字 → key 映射
//      (顺序敏感, 先匹配最具体的; 用于按 system_role / group name / upstream host 反查)

import openaiSvg from "@lobehub/icons-static-svg/icons/openai.svg?raw";
import anthropicSvg from "@lobehub/icons-static-svg/icons/anthropic.svg?raw";
import claudeSvg from "@lobehub/icons-static-svg/icons/claude-color.svg?raw";
import geminiSvg from "@lobehub/icons-static-svg/icons/gemini-color.svg?raw";
import googleSvg from "@lobehub/icons-static-svg/icons/google-color.svg?raw";
import groqSvg from "@lobehub/icons-static-svg/icons/groq.svg?raw";
import cerebrasSvg from "@lobehub/icons-static-svg/icons/cerebras-color.svg?raw";
import openrouterSvg from "@lobehub/icons-static-svg/icons/openrouter.svg?raw";
import togetherSvg from "@lobehub/icons-static-svg/icons/together-color.svg?raw";
import cohereSvg from "@lobehub/icons-static-svg/icons/cohere-color.svg?raw";
import mistralSvg from "@lobehub/icons-static-svg/icons/mistral-color.svg?raw";
import cloudflareSvg from "@lobehub/icons-static-svg/icons/cloudflare-color.svg?raw";
import huggingfaceSvg from "@lobehub/icons-static-svg/icons/huggingface-color.svg?raw";
import githubSvg from "@lobehub/icons-static-svg/icons/github.svg?raw";
import deepseekSvg from "@lobehub/icons-static-svg/icons/deepseek-color.svg?raw";
import qwenSvg from "@lobehub/icons-static-svg/icons/qwen-color.svg?raw";
import kimiSvg from "@lobehub/icons-static-svg/icons/kimi-color.svg?raw";
import moonshotSvg from "@lobehub/icons-static-svg/icons/moonshot.svg?raw";
import perplexitySvg from "@lobehub/icons-static-svg/icons/perplexity-color.svg?raw";
import xaiSvg from "@lobehub/icons-static-svg/icons/xai.svg?raw";
import grokSvg from "@lobehub/icons-static-svg/icons/grok.svg?raw";

export const LOGO_MAP: Record<string, string> = {
  openai: openaiSvg,
  anthropic: anthropicSvg,
  claude: claudeSvg,
  gemini: geminiSvg,
  google: googleSvg,
  groq: groqSvg,
  cerebras: cerebrasSvg,
  openrouter: openrouterSvg,
  together: togetherSvg,
  cohere: cohereSvg,
  mistral: mistralSvg,
  cloudflare: cloudflareSvg,
  huggingface: huggingfaceSvg,
  github: githubSvg,
  deepseek: deepseekSvg,
  qwen: qwenSvg,
  kimi: kimiSvg,
  moonshot: moonshotSvg,
  perplexity: perplexitySvg,
  xai: xaiSvg,
  grok: grokSvg,
};

const KEYWORD_TO_KEY: Array<[string, string]> = [
  // System aggregate roles (前缀 default- 必须先于裸 openai/anthropic/gemini)
  ["default-openai", "openai"],
  ["default-anthropic", "anthropic"],
  ["default-gemini", "gemini"],
  // 复合关键字优先 (cerebras 先于 cere; openrouter 先于 openai 子串)
  ["openrouter", "openrouter"],
  ["openai", "openai"],
  ["anthropic", "anthropic"],
  ["claude", "claude"],
  ["gemini", "gemini"],
  ["google", "google"],
  ["groq", "groq"],
  ["cerebras", "cerebras"],
  ["together", "together"],
  ["cohere", "cohere"],
  ["mistral", "mistral"],
  ["cloudflare", "cloudflare"],
  ["huggingface", "huggingface"],
  ["github", "github"],
  ["deepseek", "deepseek"],
  ["qwen", "qwen"],
  ["kimi", "kimi"],
  ["moonshot", "moonshot"],
  ["perplexity", "perplexity"],
  ["xai", "xai"],
  ["grok", "grok"],
];

/** 同步判断给定 hint 是否能匹配到内置 provider logo SVG (字符串)。 */
export function resolveProviderLogo(hint: string): string | null {
  const lower = (hint || "").toLowerCase();
  for (const [kw, key] of KEYWORD_TO_KEY) {
    if (lower.includes(kw)) {
      return LOGO_MAP[key] || null;
    }
  }
  return null;
}

/** 是否有内置 logo (避免重复 resolve)。 */
export function hasProviderLogo(hint: string): boolean {
  return resolveProviderLogo(hint) !== null;
}
