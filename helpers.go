package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// errResult returns an MCP error result with the given message.
func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(msg)},
		IsError: true,
	}
}

// jsonResult marshals a Go value to indented JSON and returns it as an MCP tool result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	return mcp.NewToolResultText(string(b)), nil
}

// rawJSONResult pretty-prints raw JSON bytes and returns them as an MCP tool result.
func rawJSONResult(data []byte) (*mcp.CallToolResult, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return mcp.NewToolResultText(string(data)), nil
	}
	return mcp.NewToolResultText(buf.String()), nil
}

// apiErrorResult creates a structured MCP error result from an HTTP error response.
func apiErrorResult(statusCode int, body []byte, method, path string) *mcp.CallToolResult {
	var detail struct {
		Detail string `json:"detail"`
	}
	detailStr := string(body)
	if json.Unmarshal(body, &detail) == nil && detail.Detail != "" {
		detailStr = detail.Detail
	}

	errResp := map[string]any{
		"error":       true,
		"status_code": statusCode,
		"detail":      detailStr,
		"endpoint":    method + " " + path,
	}
	b, _ := json.MarshalIndent(errResp, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(string(b))},
		IsError: true,
	}
}

// doRequest handles the common HTTP response pattern: read body, check status, return result.
// It closes the response body. Returns (result, nil) in all cases — tool-level errors
// are returned as MCP error results, not Go errors.
func doRequest(resp *http.Response, err error, method, path string) (*mcp.CallToolResult, error) {
	if err != nil {
		return errResult(fmt.Sprintf("request failed: %s", err)), nil
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return errResult(fmt.Sprintf("read response: %s", readErr)), nil
	}

	if resp.StatusCode >= 400 {
		return apiErrorResult(resp.StatusCode, body, method, path), nil
	}

	// Handle empty responses (e.g., 204 No Content)
	if len(body) == 0 {
		return mcp.NewToolResultText("success"), nil
	}

	return rawJSONResult(body)
}

// doRequestJSON is like doRequest but decodes the response body as JSON and
// applies transform to the decoded value before returning it. Error responses
// and non-JSON bodies pass through unchanged.
func doRequestJSON(resp *http.Response, err error, method, path string, transform func(v any) any) (*mcp.CallToolResult, error) {
	if err != nil {
		return errResult(fmt.Sprintf("request failed: %s", err)), nil
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return errResult(fmt.Sprintf("read response: %s", readErr)), nil
	}

	if resp.StatusCode >= 400 {
		return apiErrorResult(resp.StatusCode, body, method, path), nil
	}

	if len(body) == 0 {
		return mcp.NewToolResultText("success"), nil
	}

	var v any
	if unmarshalErr := json.Unmarshal(body, &v); unmarshalErr != nil {
		return rawJSONResult(body)
	}
	return jsonResult(transform(v))
}

// defaultContentSnippetLen is the default maximum number of bytes of document
// content kept in list and search results.
const defaultContentSnippetLen = 500

// getContentSnippetLen extracts the content_snippet_bytes parameter.
// Returns the default when absent; 0 disables truncation.
func getContentSnippetLen(request mcp.CallToolRequest) (int, *mcp.CallToolResult) {
	n := int(request.GetFloat("content_snippet_bytes", defaultContentSnippetLen))
	if n < 0 {
		return 0, errResult("content_snippet_bytes must be a non-negative integer")
	}
	return n, nil
}

// truncateUTF8 shortens s to at most n bytes without splitting a UTF-8 rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// truncateContentFields truncates the "content" field of each object in items
// to limit bytes, marking truncated entries with content_truncated.
// A limit of 0 (or less) disables truncation.
func truncateContentFields(items any, limit int) {
	if limit <= 0 {
		return
	}
	arr, ok := items.([]any)
	if !ok {
		return
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		s, ok := m["content"].(string)
		if !ok || len(s) <= limit {
			continue
		}
		m["content"] = truncateUTF8(s, limit)
		m["content_truncated"] = true
	}
}

// stripNotesFields replaces the "notes" array of each object in items with a
// notes_count field, keeping list results compact. Full notes are available
// via document_note_list or document_get.
func stripNotesFields(items any) {
	arr, ok := items.([]any)
	if !ok {
		return
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		notes, ok := m["notes"].([]any)
		if !ok {
			continue
		}
		delete(m, "notes")
		if len(notes) > 0 {
			m["notes_count"] = len(notes)
		}
	}
}

// paginateArray slices a bare JSON array response into a paginated envelope.
// Used for endpoints (e.g., /api/tasks/) that ignore pagination parameters.
func paginateArray(v any, page, pageSize int) any {
	arr, ok := v.([]any)
	if !ok {
		return v
	}
	// page and pageSize are client-controlled; clamp overflow-to-negative
	// results to "past the end" so slicing can never panic.
	start := (page - 1) * pageSize
	if start < 0 || start > len(arr) {
		start = len(arr)
	}
	end := start + pageSize
	if end < start || end > len(arr) {
		end = len(arr)
	}
	out := map[string]any{
		"count":     len(arr),
		"page":      page,
		"page_size": pageSize,
		"results":   arr[start:end],
	}
	if end < len(arr) {
		out["next_page"] = page + 1
	}
	return out
}

// getPagination extracts page and page_size from the request with defaults.
func getPagination(request mcp.CallToolRequest) (page, pageSize int) {
	page = int(request.GetFloat("page", 1))
	if page < 1 {
		page = 1
	}
	pageSize = int(request.GetFloat("page_size", 25))
	if pageSize < 1 {
		pageSize = 25
	}
	return page, pageSize
}

// addPaginationParams adds page and page_size query parameters if provided in the request.
func addPaginationParams(params url.Values, request mcp.CallToolRequest) {
	if page := request.GetFloat("page", 0); page > 0 {
		params.Set("page", strconv.Itoa(int(page)))
	}
	if pageSize := request.GetFloat("page_size", 0); pageSize > 0 {
		params.Set("page_size", strconv.Itoa(int(pageSize)))
	}
}

// addStringParam adds a string query parameter if the value is non-empty.
func addStringParam(params url.Values, request mcp.CallToolRequest, mcpName, apiName string) {
	if v := request.GetString(mcpName, ""); v != "" {
		params.Set(apiName, v)
	}
}

// addIntParam adds an integer query parameter if explicitly provided in the request.
func addIntParam(params url.Values, request mcp.CallToolRequest, mcpName, apiName string) {
	args := request.GetArguments()
	if _, ok := args[mcpName]; ok {
		params.Set(apiName, strconv.Itoa(int(request.GetFloat(mcpName, 0))))
	}
}

// addBoolParam adds a boolean query parameter if explicitly provided in the request.
func addBoolParam(params url.Values, request mcp.CallToolRequest, mcpName, apiName string) {
	args := request.GetArguments()
	if _, ok := args[mcpName]; ok {
		params.Set(apiName, strconv.FormatBool(request.GetBool(mcpName, false)))
	}
}

// getRequiredInt extracts a required integer parameter from the request.
// Returns the value and nil on success, or 0 and an error result if missing.
func getRequiredInt(request mcp.CallToolRequest, name string) (int, *mcp.CallToolResult) {
	args := request.GetArguments()
	if _, ok := args[name]; !ok {
		return 0, errResult(name + " is required")
	}
	return int(request.GetFloat(name, 0)), nil
}

// getRequiredString extracts a required string parameter from the request.
// Returns the value and nil on success, or "" and an error result if missing or empty.
func getRequiredString(request mcp.CallToolRequest, name string) (string, *mcp.CallToolResult) {
	v := request.GetString(name, "")
	if v == "" {
		return "", errResult(name + " is required")
	}
	return v, nil
}

// setNullableInt sets a nullable integer field in a body map for PATCH requests.
// If the argument is present and nil, sets the field to nil (clears it).
// Some MCP clients send null as the string "null" (or ""), which is treated
// the same way. Numbers and integer strings set the field to the int value;
// any other string is an error rather than being coerced to 0.
// If the argument is not present, does nothing.
func setNullableInt(body map[string]any, args map[string]any, request mcp.CallToolRequest, name string) error {
	val, ok := args[name]
	if !ok {
		return nil
	}
	if str, isStr := val.(string); isStr {
		str = strings.TrimSpace(str)
		if str == "" || strings.EqualFold(str, "null") {
			body[name] = nil
			return nil
		}
		n, err := strconv.Atoi(str)
		if err != nil {
			return fmt.Errorf("%s must be an integer or null, got %q", name, val)
		}
		body[name] = n
		return nil
	}
	if val == nil {
		body[name] = nil
	} else {
		body[name] = int(request.GetFloat(name, 0))
	}
	return nil
}

// setJSONField parses a JSON string parameter and sets it in a body map.
// Used for array/object fields passed as JSON strings (e.g., tags, custom_fields).
func setJSONField(body map[string]any, request mcp.CallToolRequest, name string) error {
	s := request.GetString(name, "")
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return fmt.Errorf("invalid %s JSON: %w", name, err)
	}
	body[name] = v
	return nil
}

// withOwnership adds the owner and set_permissions parameters accepted by
// create/update tools of owned objects (tags, correspondents, document types,
// storage paths, saved views). Paperless makes the token user the owner on create unless
// owner is sent explicitly, so null is meaningful here.
func withOwnership() mcp.ToolOption {
	return func(t *mcp.Tool) {
		withNullableNumber("owner", mcp.Description("Owner user ID. null = no owner (visible to every user with the model permission). Omit on create to default to the API token's user."))(t)
		mcp.WithString("set_permissions", mcp.Description(`JSON object granting object permissions, e.g. {"view":{"users":[],"groups":[2]},"change":{"users":[],"groups":[2]}}. Each users/groups list that is given replaces the current one for that action ([] clears it); omitted actions and lists stay unchanged. change implies view. Changing owner or permissions requires the token user to own the object, the object to be unowned, or a superuser token.`))(t)
	}
}

// setOwnership copies owner (nullable) and set_permissions (JSON object)
// from the request into body when present.
func setOwnership(body map[string]any, request mcp.CallToolRequest) error {
	if err := setNullableInt(body, request.GetArguments(), request, "owner"); err != nil {
		return err
	}
	return setJSONObjectField(body, request, "set_permissions")
}

// setJSONObjectField is like setJSONField but requires a JSON object.
func setJSONObjectField(body map[string]any, request mcp.CallToolRequest, name string) error {
	if err := setJSONField(body, request, name); err != nil {
		return err
	}
	if v, ok := body[name]; ok {
		if _, isObj := v.(map[string]any); !isObj {
			return fmt.Errorf("%s must be a JSON object", name)
		}
	}
	return nil
}

// withFullPerms adds the full_perms parameter to read tools of owned objects.
func withFullPerms() mcp.ToolOption {
	return mcp.WithBoolean("full_perms", mcp.Description("Include the permissions field (view/change users and groups). Replaces user_can_change in the response."))
}

// withNumber defines a tool parameter that accepts both integer and string values.
// MCP clients may send numbers as strings; the handler uses GetFloat/GetInt
// which handle coercion automatically. Strings are constrained to integer format
// so invalid values like "abc" or "1.5" are rejected at schema validation.
func withNumber(name string, opts ...mcp.PropertyOption) mcp.ToolOption {
	allOpts := append([]mcp.PropertyOption{func(schema map[string]any) {
		schema["type"] = []string{"integer", "string"}
		schema["pattern"] = `^-?\d+$`
	}}, opts...)
	return mcp.WithAny(name, allOpts...)
}

// withNullableNumber defines a tool parameter that accepts integer, string, or null.
// Used for fields that can be cleared by sending null (e.g., correspondent, document_type).
func withNullableNumber(name string, opts ...mcp.PropertyOption) mcp.ToolOption {
	allOpts := append([]mcp.PropertyOption{func(schema map[string]any) {
		schema["type"] = []string{"integer", "string", "null"}
		schema["pattern"] = `^-?\d+$`
	}}, opts...)
	return mcp.WithAny(name, allOpts...)
}

// --- Generic CRUD handlers ---

// handleSimpleGet returns a handler that GETs a fixed path with no parameters.
func handleSimpleGet(client *Client, path string) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resp, err := client.Get(ctx, path, nil)
		return doRequest(resp, err, "GET", path)
	}
}

// stripAllIDs removes the unbounded "all" ID array Paperless includes
// alongside each page of a list response. Only document_list offers an
// opt-out (include_all_ids) for bulk-select workflows.
func stripAllIDs(v any) any {
	if m, ok := v.(map[string]any); ok {
		delete(m, "all")
	}
	return v
}

// handlePaginatedList returns a handler that GETs a paginated list endpoint.
func handlePaginatedList(client *Client, path string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params := url.Values{}
		addPaginationParams(params, request)
		resp, err := client.Get(ctx, path, params)
		return doRequestJSON(resp, err, "GET", path, stripAllIDs)
	}
}

// handleOwnedPaginatedList is like handlePaginatedList but forwards the
// full_perms flag so the results include each object's permissions.
func handleOwnedPaginatedList(client *Client, path string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params := url.Values{}
		addPaginationParams(params, request)
		addBoolParam(params, request, "full_perms", "full_perms")
		resp, err := client.Get(ctx, path, params)
		return doRequestJSON(resp, err, "GET", path, stripAllIDs)
	}
}

// handleGetByID returns a handler that GETs a resource by integer ID.
// pathFmt must contain exactly one %d verb for the ID.
func handleGetByID(client *Client, pathFmt string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := getRequiredInt(request, "id")
		if errRes != nil {
			return errRes, nil
		}
		path := fmt.Sprintf(pathFmt, id)
		resp, err := client.Get(ctx, path, nil)
		return doRequest(resp, err, "GET", path)
	}
}

// handleOwnedGetByID is like handleGetByID but forwards the full_perms flag
// so the response includes the object's permissions.
func handleOwnedGetByID(client *Client, pathFmt string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := getRequiredInt(request, "id")
		if errRes != nil {
			return errRes, nil
		}
		params := url.Values{}
		addBoolParam(params, request, "full_perms", "full_perms")
		path := fmt.Sprintf(pathFmt, id)
		resp, err := client.Get(ctx, path, params)
		return doRequest(resp, err, "GET", path)
	}
}

// handleDeleteByID returns a handler that DELETEs a resource by integer ID.
// pathFmt must contain exactly one %d verb for the ID.
func handleDeleteByID(client *Client, pathFmt string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := getRequiredInt(request, "id")
		if errRes != nil {
			return errRes, nil
		}
		path := fmt.Sprintf(pathFmt, id)
		resp, err := client.Delete(ctx, path, nil)
		return doRequest(resp, err, "DELETE", path)
	}
}
