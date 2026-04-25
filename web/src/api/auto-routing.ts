export interface GroupMapping {
  simple_group: string;
  medium_group: string;
  complex_group: string;
}

export interface RouteConfig {
  enabled: boolean;
  simple_threshold: number;
  complex_threshold: number;
  group_mapping: Record<string, GroupMapping>;
}

export interface SaveConfigRequest {
  enabled: boolean;
  simple_threshold: number;
  complex_threshold: number;
  group_mapping: Record<string, GroupMapping>;
}

export interface ConfigResponse {
  success: boolean;
  config?: RouteConfig;
  error?: string;
}

export interface RequestAnalysis {
  estimated_tokens: number;
  has_tools: boolean;
  has_vision: boolean;
  tool_count: number;
  message_count: number;
  max_msg_length: number;
  level: 'simple' | 'medium' | 'complex';
}

export interface TestRouteRequest {
  group_name: string;
  request_body: Record<string, any>;
}

export interface TestRouteResponse {
  success: boolean;
  target_group?: string;
  analysis?: RequestAnalysis;
  error?: string;
  fallback_used?: boolean;
}

const API_BASE = '/api/auto-routing';

export const autoRoutingApi = {
  async getConfig(): Promise<ConfigResponse> {
    const resp = await fetch(`${API_BASE}/config`);
    return resp.json();
  },

  async saveConfig(config: SaveConfigRequest): Promise<ConfigResponse> {
    const resp = await fetch(`${API_BASE}/config`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(config),
    });
    return resp.json();
  },

  async testRoute(req: TestRouteRequest): Promise<TestRouteResponse> {
    const resp = await fetch(`${API_BASE}/test`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    });
    return resp.json();
  },
};
