import http from "@/utils/http";

export interface DedupModelEntry {
  group_id: number;
  group_name: string;
  real_model: string;
  aliases: string[];
}

export interface DedupFamily {
  family: string;
  group_count: number;
  models: DedupModelEntry[];
}

export interface DedupCreateRequest {
  alias: string;
  candidates: { group_id: number; real_model: string }[];
}

// Success-only shape. Failures are surfaced as rejected axios promises with
// `error.response.data: DedupCreateError` — the create call is now atomic
// (transactional on the server), so there is no partial-success branch.
export interface DedupCreateResponse {
  success: true;
  alias: string;
  created: number;
}

export interface DedupCreateError {
  success: false;
  code: string;
  message: string;
}

export const dedupApi = {
  async models(): Promise<DedupFamily[]> {
    const data = await http.get<unknown, { families?: DedupFamily[] }>("/aliases/quick/models");
    return data?.families ?? [];
  },

  async create(req: DedupCreateRequest): Promise<DedupCreateResponse> {
    return await http.post<unknown, DedupCreateResponse>("/aliases/quick/create", req, { hideMessage: true });
  },
};

const VARIANT_TOKENS = new Set([
  "lite","mini","nano","tiny","small","medium","large","xl","xxl",
  "pro","plus","max","ultra",
  "flash","fast","turbo","thinking","reasoner",
  "vision","image","video","audio",
  "tts","embed","embedding","rerank",
  "chat","instruct","code","coder",
  "preview","experimental","exp","beta","rc","free","trial",
  "haiku","sonnet","opus",
]);
const SIZE_RE = /^[0-9]+(?:\.[0-9]+)?[bm]$/;
const DATE_RE = /^(?:[0-9]{6,8}|[0-9]{4}|[vr][0-9]+|rev[0-9]+)$/;

export function deriveFamilyClient(modelID: string): string {
  let s = (modelID ?? "").trim().toLowerCase();
  if (!s) return "";
  const par = s.indexOf("(");
  if (par >= 0) s = s.slice(0, par).trim();
  const colon = s.indexOf(":");
  if (colon >= 0) s = s.slice(0, colon);
  const slash = s.lastIndexOf("/");
  if (slash >= 0) s = s.slice(slash + 1);
  s = s.trim();
  if (!s) return "";
  const parts = s.split("-").filter(p => p);
  const out: string[] = [];
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i];
    if (i > 0 && (VARIANT_TOKENS.has(p) || SIZE_RE.test(p) || DATE_RE.test(p))) break;
    out.push(p);
  }
  return out.join("-");
}
