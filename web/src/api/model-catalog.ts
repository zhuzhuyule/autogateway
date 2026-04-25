export interface CatalogModel {
  id: string;
  display_name: string;
  groups: string[];
  providers: string[];
}

export interface ModelsResponse {
  object: string;
  data: CatalogModel[];
}

const API_BASE = '/v1/models';

export const modelCatalogApi = {
  async getModels(): Promise<ModelsResponse> {
    const resp = await fetch(API_BASE);
    return resp.json();
  },
};
