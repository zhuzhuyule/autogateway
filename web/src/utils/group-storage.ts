// Per-group UI state kept in localStorage: model probe results, pinned probe
// modality, and the model-list filters. Nothing else removes these, so a
// deleted group would otherwise leave its keys behind forever.
export const GROUP_STORAGE_PREFIXES = [
  "model_test_results_",
  "model_probe_modality_",
  "group_models_filter_",
];

export function clearGroupStorage(groupId: number): void {
  for (const prefix of GROUP_STORAGE_PREFIXES) {
    localStorage.removeItem(`${prefix}${groupId}`);
  }
}
