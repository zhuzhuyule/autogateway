import http from "@/utils/http";

export interface ProbeResult {
  channel_type: "openai" | "anthropic" | "gemini";
  version_prefix: string;
  normalized_url: string;
  status_code: number;
  latency_ms: number;
}

export const upstreamApi = {
  probe: (url: string, prefer?: string) =>
    http.get<ProbeResult>("/upstream/probe", {
      params: prefer ? { url, prefer } : { url },
      hideMessage: true,
    }),
};
