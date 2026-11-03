package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// jsonKeys returns a type's JSON field names, following omitempty-agnostic
// tags. It is the basis of every contract test below: the web client mirrors
// these names, so a rename here silently breaks the UI.
func jsonKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	rt := reflect.TypeOf(v)
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		out[name] = true
	}
	return out
}

// TestAgentVisibleFieldsAreStable pins the exact JSON shape of the responses
// the browser consumes. The names are the contract (docs/CONTRACTS.md §3), so
// this test is deliberately written as an explicit list rather than a golden
// file: a change here should require a deliberate edit and a matching change
// in web/src/api/types.ts.
func TestAgentVisibleFieldsAreStable(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  []string
	}{
		{"Health", Health{}, []string{"status", "version", "llm_configured"}},
		{"LLMConfig", LLMConfig{}, []string{"endpoint", "model", "protocol", "api_key_set", "api_key"}},
		{"ConfigResponse", ConfigResponse{},
			[]string{"llm", "direction", "spec", "allow_edits", "active_profile",
				"profiles", "persistent", "settings_path", "saved_to_disk", "save_warning"}},
		{"UploadResponse", UploadResponse{},
			[]string{"doc_id", "source_name", "format", "node_count", "content_count",
				"profile_id", "nodes"}},
		{"DocumentResponse", DocumentResponse{},
			[]string{"doc_id", "doc", "profile_id"}},
		{"NodeSummary", NodeSummary{},
			[]string{"id", "kind", "level", "text", "order", "source_style", "source_page", "has_image", "table_cells"}},
		{"PlanResponse", PlanResponse{}, []string{"plan", "report", "job_id"}},
		{"PlanJobResponse", PlanJobResponse{}, []string{"job_id"}},
		{"StageEvent", StageEvent{}, []string{"name", "message", "done"}},
		{"DoneEvent", DoneEvent{}, []string{"plan", "report", "error"}},
		{"RenderRequest", RenderRequest{}, []string{"format", "plan"}},
		{"RenderResponse", RenderResponse{},
			[]string{"file_id", "filename", "size", "report", "diff", "synth_text"}},
		{"ErrorBody", ErrorBody{}, []string{"error"}},
		{"ErrorDetail", ErrorDetail{}, []string{"code", "message", "details"}},
		{"SpecResponse", SpecResponse{}, []string{"spec", "fonts", "problems"}},
		{"FontStatus", FontStatus{}, []string{"name", "available", "fallbacks", "used"}},
	}

	for _, c := range cases {
		got := jsonKeys(t, c.value)
		if len(got) != len(c.want) {
			t.Errorf("%s 字段数 = %d, 期望 %d（实际字段 %v）",
				c.name, len(got), len(c.want), keysOf(got))
			continue
		}
		for _, w := range c.want {
			if !got[w] {
				t.Errorf("%s 缺少字段 %q（实际 %v）", c.name, w, keysOf(got))
			}
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestErrorCodesAreDistinct: the UI branches on these to decide whether to
// blame the key, the network or the document, so a collision would make two
// different problems indistinguishable.
func TestErrorCodesAreDistinct(t *testing.T) {
	codes := []string{
		CodeBadRequest, CodeNotFound, CodeBadKey, CodeUpstream,
		CodeNoLLMConfig, CodeInvalidPlan, CodeParseFailed,
		CodeRenderFailed, CodeInternal,
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if c == "" {
			t.Error("错误码不应为空")
		}
		if seen[c] {
			t.Errorf("错误码重复: %q", c)
		}
		seen[c] = true
		// Codes travel in JSON and are matched by the client; a space or an
		// upper-case letter would make them fragile to compare.
		if strings.ToLower(c) != c || strings.ContainsAny(c, " ") {
			t.Errorf("错误码 %q 应为小写且不含空格", c)
		}
	}
}

// TestErrorBodyShape: every failing endpoint returns the same envelope, which
// is what lets the client have exactly one error path.
func TestErrorBodyShape(t *testing.T) {
	b, err := json.Marshal(ErrorBody{Error: ErrorDetail{
		Code: CodeInvalidPlan, Message: "方案未通过校验", Details: []string{"n0003 未被收录"},
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"error":{"code":"invalid_plan","message":"方案未通过校验","details":["n0003 未被收录"]}}`
	if string(b) != want {
		t.Errorf("错误响应结构不符:\n实际 %s\n期望 %s", b, want)
	}
}

// TestOmittedFieldsStayOmitted: the client treats an absent optional field as
// "not set", so an empty slice must not appear as [] or null where the UI
// would try to iterate it.
func TestOmittedFieldsStayOmitted(t *testing.T) {
	b, err := json.Marshal(ErrorDetail{Code: CodeNotFound, Message: "未找到"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "details") {
		t.Errorf("空的 details 不应出现: %s", b)
	}
}

// TestLLMConfigKeyIsWriteOnly documents the one field whose asymmetry matters:
// APIKey is accepted on write and never populated on read. A test cannot prove
// the handler's restraint, but it can pin the tag shape the client relies on.
func TestLLMConfigKeyIsWriteOnly(t *testing.T) {
	keys := jsonKeys(t, LLMConfig{})
	if !keys["api_key"] || !keys["api_key_set"] {
		t.Fatalf("LLMConfig 应同时有 api_key（写入）与 api_key_set（读取）: %v", keysOf(keys))
	}

	// A struct with only APIKeySet populated must not serialize a key.
	b, err := json.Marshal(LLMConfig{Endpoint: "https://x/v1", Model: "m", APIKeySet: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "api_key\"") {
		t.Errorf("未设置 api_key 时不应序列化该字段: %s", b)
	}

	// And when a key is present it must carry the wire name the client sends.
	b2, err := json.Marshal(LLMConfig{APIKey: "sk-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b2), `"api_key":"sk-1"`) {
		t.Errorf("api_key 的线格式不符: %s", b2)
	}
}

// TestPlanResponseAlwaysCarriesAVerdict: plan and report travel together, so
// the UI can never render a plan without knowing whether it is renderable.
func TestPlanResponseAlwaysCarriesAVerdict(t *testing.T) {
	b, err := json.Marshal(PlanResponse{Plan: &docmodel.Plan{}, Report: &docmodel.Report{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, must := range []string{`"plan"`, `"report"`} {
		if !strings.Contains(s, must) {
			t.Errorf("PlanResponse 应同时包含 %s: %s", must, s)
		}
	}
}

// TestDirectionValuesMatchTheStandard: the three 行文方向 are the wire values,
// and the UI sends them back verbatim.
func TestDirectionValuesMatchTheStandard(t *testing.T) {
	for _, d := range []gbstandard.Direction{
		gbstandard.DirDown, gbstandard.DirUp, gbstandard.DirParallel,
	} {
		if d == "" {
			t.Error("行文方向不应为空")
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(b) != `"`+string(d)+`"` {
			t.Errorf("行文方向的线格式不符: %s", b)
		}
	}
}

// TestConfigRequestUsesPointersSoOmittedMeansUnchanged: the UI saves the model
// name without resending the key, so "absent" must be distinguishable from
// "empty" — that is exactly what the pointer fields buy.
func TestConfigRequestUsesPointersSoOmittedMeansUnchanged(t *testing.T) {
	rt := reflect.TypeOf(ConfigRequest{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.Pointer {
			t.Errorf("ConfigRequest.%s 应为指针，以便区分「未提交」与「清空」", f.Name)
		}
	}

	b, err := json.Marshal(ConfigRequest{}) // everything omitted
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "{}" {
		t.Errorf("空请求应序列化为 {}, 实际 %s", b)
	}
}

// TestNodeSummaryOmitsPayloads: the upload response deliberately excludes image
// bytes and table cells, which the source panel fetches per node instead.
func TestNodeSummaryOmitsPayloads(t *testing.T) {
	keys := jsonKeys(t, NodeSummary{})
	for _, forbidden := range []string{"image", "image_data", "rows", "runs"} {
		if keys[forbidden] {
			t.Errorf("NodeSummary 不应包含 %q（会让大文档的 JSON 变得很大）", forbidden)
		}
	}
	if !keys["has_image"] || !keys["table_cells"] {
		t.Error("NodeSummary 应通过 has_image/table_cells 告知存在性，而不是传内容")
	}
}
