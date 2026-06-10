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
// 国内 / FreeModels 收录 provider
import zhipuSvg from "@lobehub/icons-static-svg/icons/zhipu-color.svg?raw";
import nvidiaSvg from "@lobehub/icons-static-svg/icons/nvidia-color.svg?raw";
import siliconcloudSvg from "@lobehub/icons-static-svg/icons/siliconcloud-color.svg?raw";
import modelscopeSvg from "@lobehub/icons-static-svg/icons/modelscope-color.svg?raw";
import longcatSvg from "@lobehub/icons-static-svg/icons/longcat-color.svg?raw";
import sensenovaSvg from "@lobehub/icons-static-svg/icons/sensenova-color.svg?raw";
import sparkSvg from "@lobehub/icons-static-svg/icons/spark-color.svg?raw";
import giteeaiSvg from "@lobehub/icons-static-svg/icons/giteeai.svg?raw";
import aihubmixSvg from "@lobehub/icons-static-svg/icons/aihubmix-color.svg?raw";
import kilocodeSvg from "@lobehub/icons-static-svg/icons/kilocode.svg?raw";
import hunyuanSvg from "@lobehub/icons-static-svg/icons/hunyuan-color.svg?raw";
import minimaxSvg from "@lobehub/icons-static-svg/icons/minimax-color.svg?raw";
import wenxinSvg from "@lobehub/icons-static-svg/icons/wenxin-color.svg?raw";
import baichuanSvg from "@lobehub/icons-static-svg/icons/baichuan-color.svg?raw";
import stepfunSvg from "@lobehub/icons-static-svg/icons/stepfun-color.svg?raw";
import bytedanceSvg from "@lobehub/icons-static-svg/icons/bytedance-color.svg?raw";
import jinaSvg from "@lobehub/icons-static-svg/icons/jina.svg?raw";
import fireworksSvg from "@lobehub/icons-static-svg/icons/fireworks-color.svg?raw";

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
  zhipu: zhipuSvg,
  nvidia: nvidiaSvg,
  siliconcloud: siliconcloudSvg,
  modelscope: modelscopeSvg,
  longcat: longcatSvg,
  sensenova: sensenovaSvg,
  spark: sparkSvg,
  giteeai: giteeaiSvg,
  aihubmix: aihubmixSvg,
  kilocode: kilocodeSvg,
  hunyuan: hunyuanSvg,
  minimax: minimaxSvg,
  wenxin: wenxinSvg,
  baichuan: baichuanSvg,
  stepfun: stepfunSvg,
  bytedance: bytedanceSvg,
  jina: jinaSvg,
  fireworks: fireworksSvg,
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
  // FreeModels 国内/聚合 provider
  ["aihubmix", "aihubmix"],          // host: aihubmix.com
  ["kilocode", "kilocode"],
  ["kilo", "kilocode"],              // host: api.kilo.ai
  ["modelscope", "modelscope"],      // host: api-inference.modelscope.cn
  ["siliconflow", "siliconcloud"],   // host: api.siliconflow.cn
  ["siliconcloud", "siliconcloud"],
  ["longcat", "longcat"],            // host: api.longcat.chat
  ["sensenova", "sensenova"],        // host: token.sensenova.cn / api.sensenova.cn
  ["sensetime", "sensenova"],        // 商汤品牌别名
  ["bigmodel", "zhipu"],             // host: open.bigmodel.cn — 智谱 GLM 平台
  ["zhipu", "zhipu"],
  ["glm", "zhipu"],
  ["nvidia", "nvidia"],              // host: integrate.api.nvidia.com
  // 讯飞 — host 可能含 xf-yun / xinghuo / xingchen / spark
  ["xinghuo", "spark"],              // 星火 Spark API
  ["xingchen", "spark"],             // 星辰 MaaS (同一品牌)
  ["xf-yun", "spark"],               // host: spark-api-open.xf-yun.com / maas-api.cn-huabei-1.xf-yun.com
  ["xfyun", "spark"],
  // gitee 必须先于 github 子串(注意 github 已先匹配,这里作为兜底)
  ["giteeai", "giteeai"],
  ["gitee", "giteeai"],              // host: ai.gitee.com
  // 模型品牌(可能出现在 group name / system_role 但不属于 FreeModels provider)
  ["hunyuan", "hunyuan"],
  ["minimax", "minimax"],
  ["wenxin", "wenxin"],
  ["ernie", "wenxin"],               // 文心一言别名
  ["baichuan", "baichuan"],
  ["stepfun", "stepfun"],
  ["bytedance", "bytedance"],
  ["doubao", "bytedance"],           // 字节豆包
  ["jina", "jina"],
  ["fireworks", "fireworks"],
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
