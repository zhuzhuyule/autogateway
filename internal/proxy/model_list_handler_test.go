package proxy

import "testing"

func TestAppendAliasModelsAddsAliases(t *testing.T) {
	response := map[string]any{
		"object": "list",
		"data": []any{
			map[string]any{"id": "real-model", "object": "model"},
		},
	}

	appendAliasModels(response, []string{"simple", "medium"})

	data, ok := response["data"].([]any)
	if !ok {
		t.Fatalf("expected data slice, got %T", response["data"])
	}
	got := modelIDs(data)
	for _, want := range []string{"real-model", "simple", "medium"} {
		if !got[want] {
			t.Fatalf("expected model list to contain %q, got %#v", want, got)
		}
	}
}

func TestAppendAliasModelsDoesNotDuplicateUpstreamModels(t *testing.T) {
	response := map[string]any{
		"data": []any{
			map[string]any{"id": "simple", "object": "model"},
		},
	}

	appendAliasModels(response, []string{"simple", "medium"})

	data := response["data"].([]any)
	got := modelIDs(data)
	if len(data) != 2 {
		t.Fatalf("expected 2 models after de-dupe, got %d: %#v", len(data), data)
	}
	if !got["simple"] || !got["medium"] {
		t.Fatalf("expected simple and medium, got %#v", got)
	}
}

func modelIDs(data []any) map[string]bool {
	out := make(map[string]bool, len(data))
	for _, item := range data {
		modelObj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		modelID, ok := modelObj["id"].(string)
		if !ok {
			continue
		}
		out[modelID] = true
	}
	return out
}
