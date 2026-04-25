export interface DedupSuggestion {
  model_name: string;
  source_groups: string[];
  suggested_aggregate_name: string;
}

export interface SubGroupConfig {
  name: string;
  weight: number;
  redirect: Record<string, string>;
}

export interface AggregateSuggestion {
  aggregate_name: string;
  model_name: string;
  sub_groups: SubGroupConfig[];
}

const API_BASE = '/api/model-dedup';

export const modelDedupApi = {
  async getSuggestions(): Promise<{ success: boolean; data: DedupSuggestion[] }> {
    const resp = await fetch(`${API_BASE}/suggestions`);
    return resp.json();
  },

  async createAggregate(suggestion: AggregateSuggestion): Promise<{ success: boolean; error?: string }> {
    const resp = await fetch(`${API_BASE}/create`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(suggestion),
    });
    return resp.json();
  },
};
