package main

import (
	"net/http"
	"testing"
)

func TestSavedViewList(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("GET", "/api/saved_views/", jsonHandler(t, 200, paginatedResponse([]map[string]any{{"id": 1, "name": "Inbox"}}, 1)))
	client := testClientAndServer(t, rh)
	result := callTool(t, handlePaginatedList(client, "/api/saved_views/"), nil)
	assertNotError(t, result)
}

func TestSavedViewGet(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("GET", "/api/saved_views/1/", jsonHandler(t, 200, map[string]any{"id": 1, "name": "Inbox"}))
	client := testClientAndServer(t, rh)
	result := callTool(t, handleGetByID(client, "/api/saved_views/%d/"), map[string]any{"id": float64(1)})
	assertNotError(t, result)
}

func TestSavedViewCreate(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/saved_views/", jsonHandler(t, 201, map[string]any{"id": 1, "name": "Inbox"}))
	client := testClientAndServer(t, rh)
	result := callTool(t, handleSavedViewCreate(client), map[string]any{"name": "Inbox"})
	assertNotError(t, result)
}

func TestSavedViewCreateRequiresName(t *testing.T) {
	client := NewClient("http://unused", "unused")
	result := callTool(t, handleSavedViewCreate(client), map[string]any{})
	assertIsError(t, result)
}

func TestSavedViewUpdate(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("PATCH", "/api/saved_views/1/", jsonHandler(t, 200, map[string]any{"id": 1, "name": "Updated"}))
	client := testClientAndServer(t, rh)
	result := callTool(t, handleSavedViewUpdate(client), map[string]any{"id": float64(1), "name": "Updated"})
	assertNotError(t, result)
}

func TestSavedViewUpdateNoFields(t *testing.T) {
	client := NewClient("http://unused", "unused")
	result := callTool(t, handleSavedViewUpdate(client), map[string]any{"id": float64(1)})
	assertIsError(t, result)
}

func TestSavedViewDelete(t *testing.T) {
	rh := newRouteHandler(t)
	rh.Handle("DELETE", "/api/saved_views/1/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	client := testClientAndServer(t, rh)
	result := callTool(t, handleDeleteByID(client, "/api/saved_views/%d/"), map[string]any{"id": float64(1)})
	assertNotError(t, result)
}

func TestSavedViewCreateSendsNullOwnerAndPermissions(t *testing.T) {
	var body map[string]any
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/saved_views/", captureJSONBody(t, &body))

	client := testClientAndServer(t, rh)
	assertNotError(t, callTool(t, handleSavedViewCreate(client), map[string]any{
		"name":            "Inbox",
		"owner":           nil,
		"set_permissions": `{"view":{"users":[],"groups":[1,2]}}`,
	}))

	if v, ok := body["owner"]; !ok || v != nil {
		t.Errorf("owner = %v (present %v), want explicit null", v, ok)
	}
	if _, ok := body["set_permissions"].(map[string]any); !ok {
		t.Errorf("set_permissions = %v, want object", body["set_permissions"])
	}
}

func TestSavedViewCreateOmitsOwnerByDefault(t *testing.T) {
	var body map[string]any
	rh := newRouteHandler(t)
	rh.Handle("POST", "/api/saved_views/", captureJSONBody(t, &body))

	client := testClientAndServer(t, rh)
	assertNotError(t, callTool(t, handleSavedViewCreate(client), map[string]any{"name": "Inbox"}))

	if _, ok := body["owner"]; ok {
		t.Error("owner must be omitted when not given")
	}
}

func TestSavedViewUpdateOwnerOnly(t *testing.T) {
	var body map[string]any
	rh := newRouteHandler(t)
	rh.Handle("PATCH", "/api/saved_views/4/", captureJSONBody(t, &body))

	client := testClientAndServer(t, rh)
	assertNotError(t, callTool(t, handleSavedViewUpdate(client), map[string]any{"id": float64(4), "owner": nil}))

	if v, ok := body["owner"]; !ok || v != nil || len(body) != 1 {
		t.Errorf("body = %v, want {owner: null}", body)
	}
}

func TestSavedViewUpdateRejectsNonObjectPermissions(t *testing.T) {
	client := NewClient("http://unused", "unused")
	result := callTool(t, handleSavedViewUpdate(client), map[string]any{
		"id":              float64(4),
		"set_permissions": `[1]`,
	})
	assertIsError(t, result)
}

func TestSavedViewFullPerms(t *testing.T) {
	var listQuery, getQuery string
	rh := newRouteHandler(t)
	rh.Handle("GET", "/api/saved_views/", func(w http.ResponseWriter, r *http.Request) {
		listQuery = r.URL.Query().Get("full_perms")
		jsonHandler(t, 200, paginatedResponse([]any{}, 0))(w, r)
	})
	rh.Handle("GET", "/api/saved_views/1/", func(w http.ResponseWriter, r *http.Request) {
		getQuery = r.URL.Query().Get("full_perms")
		jsonHandler(t, 200, map[string]any{"id": 1})(w, r)
	})

	client := testClientAndServer(t, rh)
	assertNotError(t, callTool(t, handleOwnedPaginatedList(client, "/api/saved_views/"), map[string]any{"full_perms": true}))
	assertNotError(t, callTool(t, handleOwnedGetByID(client, "/api/saved_views/%d/"), map[string]any{"id": float64(1), "full_perms": true}))

	if listQuery != "true" || getQuery != "true" {
		t.Errorf("full_perms list=%q get=%q, want true/true", listQuery, getQuery)
	}
}
