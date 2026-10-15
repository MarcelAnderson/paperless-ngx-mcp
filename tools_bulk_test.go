package main

import (
	"testing"
)

func TestDocumentBulkEdit(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/documents/bulk_edit/", jsonHandler(t, 200, map[string]any{"result": "ok"}))

	client := testClientAndServer(t, rh)
	result := callTool(t, handleDocumentBulkEdit(client), map[string]any{
		"documents":  "[1, 2, 3]",
		"method":     "add_tag",
		"parameters": `{"tag": 5}`,
	})
	assertNotError(t, result)
}

func TestDocumentBulkEditRequiresDocuments(t *testing.T) {
	client := NewClient("http://unused", "unused")
	result := callTool(t, handleDocumentBulkEdit(client), map[string]any{
		"method":     "add_tag",
		"parameters": `{"tag": 5}`,
	})
	assertIsError(t, result)
}

func TestDocumentSelectionData(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/documents/selection_data/", jsonHandler(t, 200, map[string]any{
		"selected_correspondents": []any{map[string]any{"id": 1, "document_count": 5}},
	}))

	client := testClientAndServer(t, rh)
	result := callTool(t, handleDocumentSelectionData(client), map[string]any{
		"documents": "[1, 2, 3]",
	})
	assertNotError(t, result)
}

func TestDocumentSelectionDataFiltersZeroCounts(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/documents/selection_data/", jsonHandler(t, 200, map[string]any{
		"selected_correspondents": []any{
			map[string]any{"id": 1, "document_count": 2},
			map[string]any{"id": 2, "document_count": 0},
			map[string]any{"id": 3, "document_count": 0},
		},
		"selected_tags": []any{
			map[string]any{"id": 9, "document_count": 0},
		},
	}))

	client := testClientAndServer(t, rh)
	result := callTool(t, handleDocumentSelectionData(client), map[string]any{
		"documents": "[1, 2]",
	})
	assertNotError(t, result)

	m := resultJSON(t, result)
	correspondents := m["selected_correspondents"].([]any)
	if len(correspondents) != 1 {
		t.Errorf("selected_correspondents length = %d, want 1", len(correspondents))
	}
	if id := correspondents[0].(map[string]any)["id"]; id != float64(1) {
		t.Errorf("remaining correspondent id = %v, want 1", id)
	}
	if tags := m["selected_tags"].([]any); len(tags) != 0 {
		t.Errorf("selected_tags length = %d, want 0", len(tags))
	}
}

func TestBulkEditObjects(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/bulk_edit_objects/", jsonHandler(t, 200, map[string]any{"result": "ok"}))

	client := testClientAndServer(t, rh)
	result := callTool(t, handleBulkEditObjects(client), map[string]any{
		"object_type": "tags",
		"objects":     "[1, 2]",
		"operation":   "delete",
	})
	assertNotError(t, result)
}

func TestBulkEditObjectsInvalidatesCache(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("GET", "/api/tags/", jsonHandler(t, 200, paginatedResponse([]any{}, 0)))
	rh.Handle("POST", "/api/bulk_edit_objects/", jsonHandler(t, 200, map[string]any{"result": "ok"}))

	client := testClientAndServer(t, rh)

	// Prime the tags cache.
	resp, err := client.Get(t.Context(), "/api/tags/", nil)
	if err != nil {
		t.Fatalf("prime cache: %s", err)
	}
	resp.Body.Close()
	if _, ok := client.cache.Get("/api/tags/"); !ok {
		t.Fatal("expected tags list to be cached")
	}

	result := callTool(t, handleBulkEditObjects(client), map[string]any{
		"object_type": "tags",
		"objects":     "[1, 2]",
		"operation":   "delete",
	})
	assertNotError(t, result)

	if _, ok := client.cache.Get("/api/tags/"); ok {
		t.Fatal("expected tags cache to be invalidated after bulk_edit_objects")
	}
}

func TestBulkEditObjectsSetPermissionsTopLevel(t *testing.T) {
	var body map[string]any
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/bulk_edit_objects/", captureJSONBody(t, &body))

	client := testClientAndServer(t, rh)
	result := callTool(t, handleBulkEditObjects(client), map[string]any{
		"object_type": "tags",
		"objects":     "[1, 2]",
		"operation":   "set_permissions",
		"owner":       nil,
		"permissions": `{"view":{"users":[],"groups":[2]},"change":{"users":[],"groups":[2]}}`,
		"merge":       false,
	})
	assertNotError(t, result)

	if _, ok := body["parameters"]; ok {
		t.Error("parameters must not be sent nested")
	}
	if owner, ok := body["owner"]; !ok || owner != nil {
		t.Errorf("owner = %v (present %v), want explicit null", owner, ok)
	}
	if _, ok := body["permissions"].(map[string]any); !ok {
		t.Errorf("permissions = %v, want object", body["permissions"])
	}
	if body["merge"] != false {
		t.Errorf("merge = %v, want false", body["merge"])
	}
}

// permissions sent as an object must reach Paperless next to owner instead
// of being dropped while the owner change still reports OK.
func TestBulkEditObjectsObjectPermissions(t *testing.T) {
	var body map[string]any
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/bulk_edit_objects/", captureJSONBody(t, &body))

	client := testClientAndServer(t, rh)
	result := callTool(t, handleBulkEditObjects(client), map[string]any{
		"object_type": "tags",
		"objects":     "[1]",
		"operation":   "set_permissions",
		"owner":       float64(3),
		"permissions": map[string]any{"view": map[string]any{"groups": []any{float64(2)}}},
	})
	assertNotError(t, result)

	if body["owner"] != float64(3) {
		t.Errorf("owner = %v, want 3", body["owner"])
	}
	if _, ok := body["permissions"].(map[string]any); !ok {
		t.Errorf("permissions = %v, want object", body["permissions"])
	}
}

func TestBulkEditObjectsObjectParameters(t *testing.T) {
	var body map[string]any
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/bulk_edit_objects/", captureJSONBody(t, &body))

	client := testClientAndServer(t, rh)
	result := callTool(t, handleBulkEditObjects(client), map[string]any{
		"object_type": "tags",
		"objects":     "[1]",
		"operation":   "set_permissions",
		"parameters":  map[string]any{"owner": nil},
	})
	assertNotError(t, result)

	if owner, ok := body["owner"]; !ok || owner != nil {
		t.Errorf("owner = %v (present %v), want explicit null", owner, ok)
	}
}

func TestBulkEditObjectsRejectsNonObjectPermissions(t *testing.T) {
	client := NewClient("http://unused", "unused")
	for name, perms := range map[string]any{
		"array":        []any{float64(1)},
		"array string": "[1]",
		"empty object": map[string]any{},
		"empty string": "",
	} {
		t.Run(name, func(t *testing.T) {
			assertIsError(t, callTool(t, handleBulkEditObjects(client), map[string]any{
				"object_type": "tags",
				"objects":     "[1]",
				"operation":   "set_permissions",
				"owner":       float64(3),
				"permissions": perms,
			}))
		})
	}
}

func TestBulkEditObjectsFlattensLegacyParameters(t *testing.T) {
	var body map[string]any
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/bulk_edit_objects/", captureJSONBody(t, &body))

	client := testClientAndServer(t, rh)
	result := callTool(t, handleBulkEditObjects(client), map[string]any{
		"object_type": "tags",
		"objects":     "[1,2,3]",
		"operation":   "set_permissions",
		"parameters":  `{"owner": null, "merge": false}`,
	})
	assertNotError(t, result)

	if _, ok := body["parameters"]; ok {
		t.Error("parameters must not be sent nested")
	}
	if owner, ok := body["owner"]; !ok || owner != nil {
		t.Errorf("owner = %v (present %v), want explicit null", owner, ok)
	}
}

func TestBulkEditObjectsRejectsNoOpSetPermissions(t *testing.T) {
	client := NewClient("http://unused", "unused")
	result := callTool(t, handleBulkEditObjects(client), map[string]any{
		"object_type": "tags",
		"objects":     "[1]",
		"operation":   "set_permissions",
		"merge":       true,
	})
	assertIsError(t, result)
}

func TestBulkEditObjectsRejectsUnknownParameterKey(t *testing.T) {
	client := NewClient("http://unused", "unused")
	result := callTool(t, handleBulkEditObjects(client), map[string]any{
		"object_type": "tags",
		"objects":     "[1]",
		"operation":   "set_permissions",
		"parameters":  `{"owner": null, "set_permissions": {}}`,
	})
	assertIsError(t, result)
}

func TestBulkEditObjectsRejectsNullOwnerWithMerge(t *testing.T) {
	client := NewClient("http://unused", "unused")
	for _, args := range []map[string]any{
		{"owner": nil, "merge": true},
		{"owner": nil, "merge": true, "permissions": `{"view":{"groups":[2]}}`},
		{"parameters": `{"owner": null, "merge": true}`},
	} {
		args["object_type"] = "tags"
		args["objects"] = "[1]"
		args["operation"] = "set_permissions"
		assertIsError(t, callTool(t, handleBulkEditObjects(client), args))
	}
}
