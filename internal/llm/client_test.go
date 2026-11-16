package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cn-maul/rosetta"

	"govdoc/internal/docmodel"
)

// ---------------------------------------------------------------- fixtures

func testDoc() *docmodel.Document {
	return &docmodel.Document{
		SourceName: "通知.docx",
		Format:     "docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindHeading, Level: 1, Text: "一、总体要求", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "第一段正文。", Order: 1},
			{ID: "n0003", Kind: docmodel.KindParagraph, Text: "第二段正文。", Order: 2},
		},
	}
}

// goodPlan covers every prose node of testDoc.
func goodPlanJSON() string {
	return `{
	  "elements": [],
	  "sections": [
	    {
	      "id": "s1",
	      "title": "一、总体要求",
	      "level": 1,
	      "heading_node_id": "n0001",
	      "body_node_ids": ["n0002", "n0003"],
	      "children": []
	    }
	  ],
	  "notes": []
	}`
}

// incompletePlanJSON drops n0003, which VerifyPlan must reject.
func incompletePlanJSON() string {
	return `{
	  "sections": [
	    {
	      "id": "s1",
	      "title": "一、总体要求",
	      "level": 1,
	      "heading_node_id": "n0001",
	      "body_node_ids": ["n0002"]
	    }
	  ]
	}`
}

// chatResponse wraps content in an OpenAI-compatible chat completion body.
func chatResponse(content string, stopReason string) string {
	body := map[string]any{
		"id":    "chatcmpl-test",
		"model": "stub-model",
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": stopReason,
		}},
	}
	b, _ := json.Marshal(body)
	return string(b)
}

// stubEndpoint starts a fake OpenAI-compatible server that answers with the
// given contents in order (repeating the last one if calls exceed the list).
type stubEndpoint struct {
	*httptest.Server
	mu       sync.Mutex
	requests []map[string]any
	status   int
	statusOf func(call int) int
	// errBody customises the JSON body sent with a non-zero status, so a test
	// can reproduce the wording a real provider uses when it rejects a field.
	errBody func(call int) string
	rawBody func(call int) string
}

func newStub(t *testing.T, contents ...string) *stubEndpoint {
	t.Helper()
	s := &stubEndpoint{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)

		s.mu.Lock()
		call := len(s.requests)
		s.requests = append(s.requests, req)
		s.mu.Unlock()

		code := 0
		if s.statusOf != nil {
			code = s.statusOf(call)
		}
		if code == 0 {
			code = s.status
		}
		if code != 0 {
			body := `{"error":{"message":"stub failure","type":"invalid_request_error"}}`
			if s.errBody != nil {
				body = s.errBody(call)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
			return
		}

		var payload string
		if s.rawBody != nil {
			payload = s.rawBody(call)
		} else {
			i := call
			if i >= len(contents) {
				i = len(contents) - 1
			}
			if i < 0 {
				i = 0
			}
			payload = chatResponse(contents[i], "stop")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stubEndpoint) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *stubEndpoint) request(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.requests) {
		return nil
	}
	return s.requests[i]
}

func newClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	c, err := New(Config{
		Endpoint: endpoint,
		APIKey:   "test-key",
		Model:    "stub-model",
		Timeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// ---------------------------------------------------------------- New

func TestNewRequiresConfiguration(t *testing.T) {
	cases := map[string]Config{
		"缺少 endpoint": {APIKey: "k", Model: "m"},
		"缺少 key":      {Endpoint: "http://x/v1", Model: "m"},
		"缺少 model":    {Endpoint: "http://x/v1", APIKey: "k"},
		"全空":          {},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%s：期望 ErrNotConfigured, 得到 %v", name, err)
		}
	}
}

func TestNewRejectsUnknownProtocol(t *testing.T) {
	_, err := New(Config{
		Endpoint: "http://x/v1", APIKey: "k", Model: "m", Protocol: "grpc",
	})
	if err == nil {
		t.Fatal("未知协议应报错")
	}
	if !strings.Contains(err.Error(), "grpc") {
		t.Errorf("错误信息应指明协议名: %v", err)
	}
}

func TestNewAcceptsKnownProtocols(t *testing.T) {
	for _, p := range []string{"", "openai-chat", "openai-responses", "anthropic"} {
		if _, err := New(Config{
			Endpoint: "http://x/v1", APIKey: "k", Model: "m", Protocol: p,
		}); err != nil {
			t.Errorf("协议 %q 应被接受: %v", p, err)
		}
	}
}

// ---------------------------------------------------------------- happy path

func TestPlanParsesAndVerifies(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)

	var stages []Stage
	plan, err := c.Plan(context.Background(), testDoc(), func(s Stage) {
		stages = append(stages, s)
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan == nil {
		t.Fatal("未返回方案")
	}
	if len(plan.Sections) != 1 {
		t.Fatalf("章节数 = %d, 期望 1", len(plan.Sections))
	}
	if got := plan.Sections[0].BodyNodeIDs; len(got) != 2 {
		t.Errorf("正文节点 = %v, 期望 2 个", got)
	}
	if plan.Model != "stub-model" {
		t.Errorf("方案应记录模型来源, 实际 %q", plan.Model)
	}
	if stub.calls() != 1 {
		t.Errorf("一次成功只应调用一次模型, 实际 %d", stub.calls())
	}

	// The stages must include the documented names so the UI can follow.
	names := map[string]bool{}
	for _, s := range stages {
		names[s.Name] = true
	}
	for _, want := range []string{StageConnect, StageAnalyze, StageValidate, StageComplete} {
		if !names[want] {
			t.Errorf("缺少进度阶段 %q（实际 %v）", want, names)
		}
	}
}

// TestPlanRepairsWarningOnlyHeadingDuplicate is the reported defect, at the
// level the user sees it: the model answers with its own section title while
// leaving the source heading in the body. Verification passes — the duplicates
// are warnings — so this used to be returned untouched, and the exported file
// printed the model's title above the author's heading.
func TestPlanRepairsWarningOnlyHeadingDuplicate(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindHeading, Level: 1, Text: "一、项目概况", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "正文。", Order: 1},
		},
	}
	answer := `{
	  "sections": [{
	    "id": "s1",
	    "title": "一、项目基本情况",
	    "level": 1,
	    "body_node_ids": ["n0001", "n0002"],
	    "children": []
	  }]
	}`
	stub := newStub(t, answer)
	c := newClient(t, stub.URL)

	plan, err := c.PlanWithOptions(context.Background(), doc, PlanOptions{AllowEdits: true}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if stub.calls() != 1 {
		t.Errorf("可本地修正时不应再调用模型, 实际 %d 次", stub.calls())
	}
	if len(plan.Sections) != 1 {
		t.Fatalf("章节数 = %d", len(plan.Sections))
	}
	s := plan.Sections[0]
	if s.HeadingNodeID != "n0001" {
		t.Errorf("标题应引用原文节点, 实际 %q", s.HeadingNodeID)
	}
	if s.Title != "一、项目概况" {
		t.Errorf("成品标题应为原文, 实际 %q", s.Title)
	}
	if len(s.BodyNodeIDs) != 1 || s.BodyNodeIDs[0] != "n0002" {
		t.Errorf("原标题节点应从正文移除, 实际 %v", s.BodyNodeIDs)
	}
	var heading *docmodel.Edit
	for i := range plan.Edits {
		if plan.Edits[i].Kind == docmodel.EditHeading {
			heading = &plan.Edits[i]
		}
	}
	if heading == nil {
		t.Fatalf("标题改写应出现在校对项, 实际 %+v", plan.Edits)
	}
	if heading.Before != "一、项目概况" || heading.After != "一、项目基本情况" {
		t.Errorf("校对项内容错误: %q → %q", heading.Before, heading.After)
	}
	if rep := docmodel.VerifyPlan(doc, plan); rep.SynthesizedHeadings != 0 {
		t.Errorf("不应保留自拟标题, 实际 %d 个", rep.SynthesizedHeadings)
	}
}

// TestPromptSendsIdsNotContent is the heart of the design: the model is given
// the paragraphs to read but is never asked to retype them, and the request
// must say so.
func TestPromptSendsIdsNotContent(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	req := stub.request(0)
	if req == nil {
		t.Fatal("未收到请求")
	}

	// rosetta's OpenAI adapter renders ChatRequest.System as a leading system
	// message inside `messages` rather than a top-level field, so the prompt is
	// read from the conversation. Reading it from wherever it actually lands
	// keeps this test about the prompt's content rather than about the SDK's
	// wire layout (which TestPromptReachesTheModelViaMessages pins separately).
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("请求中没有 messages")
	}

	var system string
	if s, ok := req["system"].(string); ok {
		system = s
	}
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		if role, _ := msg["role"].(string); role == "system" {
			if c, ok := msg["content"].(string); ok {
				system += c
			}
		}
	}
	if system == "" {
		t.Fatalf("请求中缺少 system prompt: %+v", req)
	}
	for _, must := range []string{"不得改写", "node id", "恰好引用一次"} {
		if !strings.Contains(system, must) {
			t.Errorf("system prompt 缺少「%s」", must)
		}
	}

	// The user message must carry the ids and the text (so the model can
	// judge structure), and the response contract must be JSON.
	var user string
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		if role, _ := msg["role"].(string); role == "user" {
			if c, ok := msg["content"].(string); ok {
				user += c
			}
		}
	}
	for _, must := range []string{"n0001", "n0002", "n0003", "一、总体要求", "JSON"} {
		if !strings.Contains(user, must) {
			t.Errorf("用户提示缺少 %q", must)
		}
	}
}

// TestPlanSendsConfiguredModel: a wrong model id would make every request fail
// at the provider, so the configured one must reach the wire unaltered.
func TestPlanSendsConfiguredModel(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := stub.request(0)["model"]; got != "stub-model" {
		t.Errorf("model = %v, 期望 stub-model", got)
	}
}

// TestTemperatureDefaultsZero: planning is a classification task, so it must
// not silently inherit a provider default of 1.0 — that makes the same
// document produce different trees on different runs. rosetta drops the field
// itself for reasoning-capable models, so sending it is safe everywhere.
func TestTemperatureDefaultsZero(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got, present := stub.request(0)["temperature"]
	if !present {
		t.Fatal("未配置时应发送默认 temperature")
	}
	if f, ok := got.(float64); !ok || f != 0 {
		t.Errorf("temperature = %v, 期望 0", got)
	}
}

// TestSeedDefaultsFixed: temperature 0 removes most sampling variance, but a
// provider may still choose between equally likely continuations. The seed
// makes providers that support it reproduce the same tree.
func TestSeedDefaultsFixed(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got, present := stub.request(0)["seed"]
	if !present {
		t.Fatal("未配置时应发送默认 seed")
	}
	if n, ok := got.(float64); !ok || int(n) != defaultSeed {
		t.Errorf("seed = %v, 期望 %d", got, defaultSeed)
	}
}

// TestExplicitTemperatureWins: a caller that sets one keeps it.
func TestExplicitTemperatureWins(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)
	want := 0.7
	c.cfg.Temperature = &want
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got, _ := stub.request(0)["temperature"].(float64); got != want {
		t.Errorf("temperature = %v, 期望 %v", got, want)
	}
}

// TestMaxOutputGrowsWithDocument: a large document needs a larger cap, and a
// truncated answer is a hard failure the repair loop cannot recover from.
func TestMaxOutputGrowsWithDocument(t *testing.T) {
	c := &Client{cfg: Config{MaxOutputTokens: 0}}
	small := testDoc() // 3 nodes
	want := defaultMaxOutput + 3*tokensPerNode
	for _, node := range small.ContentNodes() {
		want += tokensPerRune * len([]rune(node.Text))
	}
	if got := c.maxOutput(small); got != want {
		t.Errorf("小文档上限 = %d", got)
	}

	big := &docmodel.Document{}
	for i := 0; i < 2000; i++ {
		big.Nodes = append(big.Nodes, docmodel.Node{
			ID: fmt.Sprintf("n%04d", i), Kind: docmodel.KindParagraph, Text: "正文",
		})
	}
	if got := c.maxOutput(big); got != maxOutputCeiling {
		t.Errorf("大文档上限应被压到天花板 %d, 实际 %d", maxOutputCeiling, got)
	}
}

// TestMaxOutputGrowsWithTextNotJustNodeCount: the one-paragraph document is
// the case that failed in practice. Its whole text must be written back inside
// a recomposition, so a cap derived only from the node count is far too small.
func TestMaxOutputGrowsWithTextNotJustNodeCount(t *testing.T) {
	c := &Client{cfg: Config{MaxOutputTokens: 0}}
	long := &docmodel.Document{Nodes: []docmodel.Node{{
		ID: "n0001", Kind: docmodel.KindParagraph,
		Text: strings.Repeat("这是一段很长的正文。", 500),
	}}}
	if got := c.maxOutput(long); got <= defaultMaxOutput+10*tokensPerNode {
		t.Errorf("长单段文档的上限仍按节点数估算: %d", got)
	}
}

// TestMaxOutputExplicitWins: an operator who set a cap gets exactly that.
func TestMaxOutputExplicitWins(t *testing.T) {
	c := &Client{cfg: Config{MaxOutputTokens: 1234}}
	if got := c.maxOutput(testDoc()); got != 1234 {
		t.Errorf("上限 = %d, 期望 1234", got)
	}
}

// TestResponseFormatIsRequested: the schema is what stops a whole class of
// repair rounds (fenced JSON, trailing commas, malformed ids).
func TestResponseFormatIsRequested(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rf, ok := stub.request(0)["response_format"].(map[string]any)
	if !ok {
		t.Fatal("请求应带 response_format")
	}
	if rf["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, 期望 json_schema", rf["type"])
	}
	js, _ := rf["json_schema"].(map[string]any)
	if js == nil || js["schema"] == nil {
		t.Errorf("缺少 json_schema.schema: %v", rf)
	}
}

// TestSchemaMismatchFallsBackOnce: a gateway that does not know
// response_format must not make analysis impossible. The field is dropped and
// the request retried; the flag is sticky so later turns do not pay for a
// second rejection each time.
func TestSchemaMismatchFallsBackOnce(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	stub.statusOf = func(call int) int {
		if call == 0 {
			return 400
		}
		return 0
	}
	stub.errBody = func(int) string {
		return `{"error":{"message":"unsupported parameter: response_format","type":"invalid_request_error"}}`
	}
	c := newClient(t, stub.URL)

	plan, err := c.Plan(context.Background(), testDoc(), nil)
	if err != nil {
		t.Fatalf("被拒绝的 response_format 应回退而不是失败: %v", err)
	}
	if plan == nil {
		t.Fatal("回退后应拿到方案")
	}
	if stub.calls() != 2 {
		t.Fatalf("应调用 2 次（首次被拒 + 回退），实际 %d", stub.calls())
	}
	if _, present := stub.request(0)["response_format"]; !present {
		t.Error("首次请求应带 response_format")
	}
	if _, present := stub.request(1)["response_format"]; present {
		t.Error("回退请求不应再带 response_format")
	}
	if !c.noJSONMode.Load() {
		t.Error("回退后应记住该端点不支持，避免每次都试探")
	}
}

// TestSchemaFallbackIsSticky: after one rejection, later plans skip the field
// entirely rather than paying for a rejection every time.
func TestSchemaFallbackIsSticky(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	stub.statusOf = func(call int) int {
		if call == 0 {
			return 400
		}
		return 0
	}
	stub.errBody = func(int) string {
		return `{"error":{"message":"unsupported parameter: response_format"}}`
	}
	c := newClient(t, stub.URL)
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("首次 Plan: %v", err)
	}
	before := stub.calls()
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("第二次 Plan: %v", err)
	}
	if stub.calls()-before != 1 {
		t.Errorf("已记住不支持后，第二次应只用 1 次调用，实际 %d", stub.calls()-before)
	}
	if _, present := stub.request(before)["response_format"]; present {
		t.Error("已记住不支持后仍带了 response_format")
	}
}

// TestUnrelated400IsNotSwallowed: only a rejection that names the field
// triggers the fallback. A 400 about something else must surface.
func TestUnrelated400IsNotSwallowed(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	stub.status = 400
	stub.errBody = func(int) string {
		return `{"error":{"message":"invalid model id","type":"invalid_request_error"}}`
	}
	c := newClient(t, stub.URL)
	_, err := c.Plan(context.Background(), testDoc(), nil)
	if err == nil {
		t.Fatal("与 response_format 无关的 400 应报错")
	}
	if stub.calls() != 1 {
		t.Errorf("不应重试: %d 次调用", stub.calls())
	}
}

// TestRejectsResponseFormatClassification pins the narrow matcher: only
// messages that name the field qualify, so a genuine 400 is not masked.
func TestRejectsResponseFormatClassification(t *testing.T) {
	cases := []struct {
		status int
		msg    string
		want   bool
	}{
		{400, "unsupported parameter: response_format", true},
		{400, "response_format is not supported by this model", true},
		{422, "unknown field json_schema", true},
		{400, "invalid temperature", false},
		{401, "invalid api key", false},
		{500, "internal server error", false},
		{400, "model not found", false},
	}
	for _, c := range cases {
		err := &rosetta.APIError{StatusCode: c.status, Message: c.msg}
		if got := rejectsResponseFormat(err); got != c.want {
			t.Errorf("rejectsResponseFormat(%d, %q) = %v, 期望 %v", c.status, c.msg, got, c.want)
		}
	}
	if rejectsResponseFormat(rosetta.ErrNotSupported) != true {
		t.Error("ErrNotSupported 应视为不支持结构化输出")
	}
	if rejectsResponseFormat(nil) {
		t.Error("nil 不应视为拒绝")
	}
}

// ---------------------------------------------------------------- parsing

// TestParsePlanHandlesMarkdownFence: models wrap JSON in a fence constantly.
// Rejecting it would burn a repair round on a cosmetic problem.
func TestParsePlanHandlesMarkdownFence(t *testing.T) {
	fenced := "```json\n" + goodPlanJSON() + "\n```"
	plan, err := ParsePlan(fenced, testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Sections) != 1 {
		t.Errorf("章节数 = %d", len(plan.Sections))
	}
}

func TestParsePlanHandlesPlainFence(t *testing.T) {
	fenced := "```\n" + goodPlanJSON() + "\n```"
	if _, err := ParsePlan(fenced, testDoc()); err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
}

// TestParsePlanHandlesSurroundingProse: a preamble or a closing remark is
// common and harmless, so the object is extracted rather than rejected.
func TestParsePlanHandlesSurroundingProse(t *testing.T) {
	wrapped := "好的，以下是根据文档梳理的结构方案：\n\n" + goodPlanJSON() +
		"\n\n希望这个方案符合要求。"
	plan, err := ParsePlan(wrapped, testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Sections) != 1 {
		t.Errorf("章节数 = %d", len(plan.Sections))
	}
}

func TestParsePlanHandlesMissingOptionalFields(t *testing.T) {
	minimal := `{"sections":[{"id":"s1","title":"标题","level":1,"body_node_ids":["n0002"]}]}`
	plan, err := ParsePlan(minimal, testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Elements == nil {
		t.Error("Elements 应被规范化为非 nil 切片，避免前端处理 null")
	}
	if len(plan.Sections) != 1 {
		t.Errorf("章节数 = %d", len(plan.Sections))
	}
}

// TestEmptyPlanIsRepairedNotAccepted: `{}` parses cleanly but says nothing, so
// accepting it would store a chapterless plan as a success.
func TestEmptyPlanIsRepairedNotAccepted(t *testing.T) {
	stub := newStub(t, "{}", goodPlanJSON())
	c := newClient(t, stub.URL)

	plan, err := c.Plan(context.Background(), testDoc(), nil)
	if err != nil {
		t.Fatalf("空方案应触发修复并成功: %v", err)
	}
	if len(plan.Sections) == 0 {
		t.Fatal("最终方案不应为空")
	}
	if stub.calls() != 2 {
		t.Errorf("应调用 2 次（空方案退回一次）, 实际 %d", stub.calls())
	}
}

// TestEmptyPlanAloneFails: if the model keeps answering `{}`, the loop must
// give up with the standard bounded-rounds error.
func TestEmptyPlanAloneFails(t *testing.T) {
	stub := newStub(t, "{}")
	c := newClient(t, stub.URL)

	if _, err := c.Plan(context.Background(), testDoc(), nil); err == nil {
		t.Fatal("始终返回空方案应报错")
	}
	if want := 1 + maxRepairRounds; stub.calls() != want {
		t.Errorf("调用次数 = %d, 期望 %d", stub.calls(), want)
	}
}

func TestParsePlanRejectsGarbage(t *testing.T) {
	for _, bad := range []string{
		"",
		"抱歉，我无法完成这个任务。",
		"{不是合法JSON",
		"{}",
	} {
		if _, err := ParsePlan(bad, testDoc()); err == nil {
			t.Errorf("应拒绝无法解析的回答: %q", bad)
		}
	}
}

// TestParsePlanToleratesExtraField: one invented key must not cost a repair
// round, since the plan itself is usable.
func TestParsePlanToleratesExtraField(t *testing.T) {
	withExtra := `{
	  "sections":[{"id":"s1","title":"一、总体要求","level":1,
	    "heading_node_id":"n0001","body_node_ids":["n0002","n0003"],"confidence":0.9}]
	}`
	plan, err := ParsePlan(withExtra, testDoc())
	if err != nil {
		t.Fatalf("含多余字段的回答应能解析: %v", err)
	}
	if len(plan.Sections) != 1 {
		t.Errorf("章节数 = %d", len(plan.Sections))
	}
}

// TestParsePlanBindsTitleFromMeta is a real-workflow repair: the model often
// fills meta.title instead of binding the source's own title paragraph. Left
// alone, that paragraph would be reported as dropped content.
func TestParsePlanBindsTitleFromMeta(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "关于开展某项工作的通知", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "正文。", Order: 1},
		},
	}
	answer := `{
	  "meta": {"title": "关于开展某项工作的通知"},
	  "sections": [{"id":"s1","title":"事项","level":1,"body_node_ids":["n0002"]}]
	}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}

	// The title must now be bound to the source node, not left in meta.
	var bound bool
	for _, e := range plan.Elements {
		if e.Role == docmodel.RoleTitle && e.NodeID == "n0001" {
			bound = true
		}
	}
	if !bound {
		t.Fatalf("标题应被绑定到原文节点 n0001, 实际 elements=%+v meta.Title=%q",
			plan.Elements, plan.Meta.Title)
	}
	if plan.Meta.Title != "" {
		t.Errorf("绑定后应清空 meta.Title, 实际 %q", plan.Meta.Title)
	}
	// And the repaired plan must now verify.
	if rep := docmodel.VerifyPlan(doc, plan); !rep.OK() {
		t.Errorf("修正后应通过校验: %+v", rep.Diagnostics)
	}
}

// TestParsePlanKeepsSourceTitleWhenMetaProposesAnother is the reported defect:
// the source already has a title and the model proposed a different one. The
// document must keep the source title until the user accepts, and the proposal
// must appear as a review item rather than being silently dropped or printed.
func TestParsePlanKeepsSourceTitleWhenMetaProposesAnother(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "原稿标题", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "正文。", Order: 1},
		},
	}
	answer := `{
	  "meta": {"title": "模型改写的标题"},
	  "elements": [{"role":"title","node_id":"n0001"}],
	  "sections": [{"id":"s1","title":"事项","level":1,"body_node_ids":["n0002"]}]
	}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Meta.Title != "" {
		t.Errorf("meta.title 应被清空, 实际 %q", plan.Meta.Title)
	}
	if len(plan.Edits) != 1 {
		t.Fatalf("标题建议应保留为校对项, 实际 %+v", plan.Edits)
	}
	e := plan.Edits[0]
	if e.NodeID != "n0001" || e.Before != "原稿标题" || e.After != "模型改写的标题" {
		t.Errorf("校对项内容错误: %+v", e)
	}
	if e.Kind != docmodel.EditHeading {
		t.Errorf("类型 = %q, 期望 %q", e.Kind, docmodel.EditHeading)
	}
	if rep := docmodel.VerifyPlan(doc, plan); !rep.OK() {
		t.Errorf("应通过校验: %+v", rep.Diagnostics)
	}
}

// TestParsePlanDoesNotDuplicateTitles: the reported generated file had a
// masthead line the source never contained. A source with a title must not
// acquire 版头 values the model invented.
func TestParsePlanDropsInventedMasthead(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "关于某事项的公告", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "正文。", Order: 1},
		},
	}
	answer := `{
	  "meta": {"issuing_org": "某区某局", "doc_number": "某发〔2024〕1号"},
	  "elements": [{"role":"title","node_id":"n0001"}],
	  "sections": [{"id":"s1","title":"事项","level":1,"body_node_ids":["n0002"]}]
	}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Meta.IssuingOrg != "" || plan.Meta.DocNumber != "" {
		t.Errorf("原文没有的版头要素不应保留: issuing_org=%q doc_number=%q",
			plan.Meta.IssuingOrg, plan.Meta.DocNumber)
	}
	if len(plan.Notes) == 0 {
		t.Error("移除模型生成的版头应留下说明")
	}
}

// TestParsePlanDropsMastheadTextAlsoInProse: the real upload contained the
// issuing organ's name inside a body sentence ("递交地址：殷都区文化广电体育
// 旅游局。") and the model reused it as a masthead. A substring match in the
// source is not evidence that the author wrote a 版头; only a binding is.
func TestParsePlanDropsMastheadTextAlsoInProse(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "关于某事项的公告", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "递交地址：殷都区文化广电体育旅游局。", Order: 1},
		},
	}
	answer := `{
	  "meta": {"issuing_org": "殷都区文化广电体育旅游局"},
	  "elements": [{"role":"title","node_id":"n0001"}],
	  "sections": [{"id":"s1","title":"事项","level":1,"body_node_ids":["n0002"]}]
	}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Meta.IssuingOrg != "" {
		t.Errorf("正文里出现过的名称不等于作者写过的版头, 实际保留 %q", plan.Meta.IssuingOrg)
	}
}

// The model may leave the source's title paragraph entirely unbound while
// emitting its own masthead. Previously the masthead survived because the drop
// depended on a title binding; the source title then fell into a section body
// and was printed as 正文. Both halves must be fixed at once.
func TestParsePlanBindsForgottenTitleAndDropsMasthead(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "关于某事项的公告", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "正文。", Order: 1},
		},
	}
	answer := `{
	  "meta": {"issuing_org": "某区某局"},
	  "sections": [{"id":"s1","title":"事项","level":1,"body_node_ids":["n0002"]}]
	}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	var title *docmodel.ElementBinding
	for i := range plan.Elements {
		if plan.Elements[i].Role == docmodel.RoleTitle {
			title = &plan.Elements[i]
		}
	}
	if title == nil || title.NodeID != "n0001" {
		t.Fatalf("应把原稿标题绑定为 title, 实际 %+v", plan.Elements)
	}
	if plan.Meta.IssuingOrg != "" {
		t.Errorf("原文没有的版头应被移除, 实际 %q", plan.Meta.IssuingOrg)
	}
	if rep := docmodel.VerifyPlan(doc, plan); !rep.OK() {
		t.Errorf("应通过校验: %+v", rep.Diagnostics)
	}
}

// TestParsePlanBindsRealMastheadParagraph: the opposite of the case above.
// When the source really does contain a masthead line, it must be preserved
// as a bound element rather than dropped as invented metadata.
func TestParsePlanBindsRealMastheadParagraph(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "某某区人民政府文件", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "关于某事项的公告", Order: 1},
			{ID: "n0003", Kind: docmodel.KindParagraph, Text: "正文。", Order: 2},
		},
	}
	answer := `{
	  "meta": {"issuing_org": "某某区人民政府文件"},
	  "elements": [{"role":"title","node_id":"n0002"}],
	  "sections": [{"id":"s1","title":"事项","level":1,"body_node_ids":["n0003"]}]
	}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Meta.IssuingOrg != "" {
		t.Errorf("绑定到原文段落后应清空 meta: %q", plan.Meta.IssuingOrg)
	}
	var bound bool
	for _, e := range plan.Elements {
		if e.Role == docmodel.RoleMasthead && e.NodeID == "n0001" {
			bound = true
		}
	}
	if !bound {
		t.Errorf("原文确有版头段时应绑定, elements=%+v", plan.Elements)
	}
}

// TestParsePlanAdoptsHeadingNodeFromTitle: a section that quotes a source
// heading verbatim but omits heading_node_id is unambiguous, so it is
// resolved rather than reported.
func TestParsePlanAdoptsHeadingNodeFromTitle(t *testing.T) {
	plan, err := ParsePlan(incompletePlanJSON(), testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Sections[0].HeadingNodeID != "n0001" {
		t.Errorf("应从 title 推断出 heading_node_id, 实际 %q", plan.Sections[0].HeadingNodeID)
	}
}

// TestParsePlanDoesNotGuessHeadingOnPartialMatch: adopting a node on a loose
// match would assert an equivalence the model never meant, and heading
// mismatch detection exists precisely to catch that.
func TestParsePlanDoesNotGuessHeadingOnPartialMatch(t *testing.T) {
	doc := testDoc()
	answer := `{
	  "sections": [{"id":"s1","title":"总体要求","level":1,"body_node_ids":["n0002","n0003"]}]
	}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Sections[0].HeadingNodeID != "" {
		t.Errorf("标题不完全一致时不应推断节点, 实际 %q", plan.Sections[0].HeadingNodeID)
	}
}

func TestParsePlanAssignsMissingSectionIDs(t *testing.T) {
	answer := `{"sections":[{"title":"甲","level":1,"body_node_ids":["n0002"]}]}`
	plan, err := ParsePlan(answer, testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Sections[0].ID == "" {
		t.Error("应自动补上章节 id")
	}
}

func TestParsePlanFillsTitleFromHeadingNode(t *testing.T) {
	answer := `{"sections":[{"id":"s1","title":"","level":1,
	  "heading_node_id":"n0001","body_node_ids":["n0002","n0003"]}]}`
	plan, err := ParsePlan(answer, testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Sections[0].Title != "一、总体要求" {
		t.Errorf("标题应从标题节点回填, 实际 %q", plan.Sections[0].Title)
	}
}

// ---------------------------------------------------------------- self-repair

// unknownRefPlanJSON references a node the document does not contain. It is
// used wherever a test needs a defect the *model* must fix: local repair can
// drop an invented id, but it cannot invent the paragraph it meant.
func unknownRefPlanJSON() string {
	return `{
	  "sections": [
	    {"id": "s1", "title": "一、总体要求", "level": 1,
	     "heading_node_id": "n0001",
	     "body_node_ids": ["n0002", "n9999"]}
	  ]
	}`
}

// TestPlanRepairsIncompleteAnswerLocally: a dropped paragraph is decidable
// from the document alone, so it is fixed without spending a model round. That
// is the whole point — the model's second answer would usually be correct but
// costs tens of seconds and the user's tokens.
func TestPlanRepairsIncompleteAnswerLocally(t *testing.T) {
	stub := newStub(t, incompletePlanJSON(), goodPlanJSON())
	c := newClient(t, stub.URL)

	plan, err := c.Plan(context.Background(), testDoc(), nil)
	if err != nil {
		t.Fatalf("本地修正后应一次通过: %v", err)
	}
	if stub.calls() != 1 {
		t.Fatalf("可本地修正的问题不应再花一轮模型调用, 实际 %d 次", stub.calls())
	}

	rep := docmodel.VerifyPlan(testDoc(), plan)
	if !rep.OK() {
		t.Fatalf("修正后应通过校验: %+v", rep.Diagnostics)
	}
	// The repair must be visible: a change the user cannot see is exactly what
	// this project forbids.
	joined := strings.Join(plan.Notes, "\n")
	if !strings.Contains(joined, "n0003") {
		t.Errorf("补收应记录在 notes 里, 实际: %v", plan.Notes)
	}
}

// TestPlanAsksModelWhenLocalRepairCannotFix: an invented node id is not
// repairable locally, so the failure goes back to the model.
func TestPlanAsksModelWhenLocalRepairCannotFix(t *testing.T) {
	stub := newStub(t, unknownRefPlanJSON(), goodPlanJSON())
	c := newClient(t, stub.URL)

	var stages []Stage
	plan, err := c.Plan(context.Background(), testDoc(), func(s Stage) {
		stages = append(stages, s)
	})
	if err != nil {
		t.Fatalf("第二轮应成功: %v", err)
	}
	if stub.calls() != 2 {
		t.Fatalf("应调用模型 2 次, 实际 %d", stub.calls())
	}
	if !docmodel.VerifyPlan(testDoc(), plan).OK() {
		t.Fatalf("最终方案应通过校验")
	}

	// The repair round must actually tell the model what was wrong.
	msgs, _ := stub.request(1)["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("第二轮请求应包含历史与反馈, 实际 %d 条消息", len(msgs))
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	feedback, _ := last["content"].(string)
	if !strings.Contains(feedback, "n9999") {
		t.Errorf("反馈应指明无效引用 n9999, 实际:\n%s", feedback)
	}
	if !strings.Contains(feedback, "unknown_ref") {
		t.Errorf("反馈应包含诊断代码, 实际:\n%s", feedback)
	}

	var sawRepair bool
	for _, s := range stages {
		if s.Name == StageRepair {
			sawRepair = true
		}
	}
	if !sawRepair {
		t.Error("应报告 repair 进度阶段")
	}
}

// TestPlanRepairsMalformedAnswer: a JSON slip is recoverable when the parse
// error is spelled out.
func TestPlanRepairsMalformedAnswer(t *testing.T) {
	stub := newStub(t, "这不是 JSON", goodPlanJSON())
	c := newClient(t, stub.URL)

	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("第二轮应成功: %v", err)
	}
	if stub.calls() != 2 {
		t.Fatalf("应调用 2 次, 实际 %d", stub.calls())
	}
	msgs, _ := stub.request(1)["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	feedback, _ := last["content"].(string)
	if !strings.Contains(feedback, "JSON") {
		t.Errorf("反馈应说明 JSON 解析失败, 实际:\n%s", feedback)
	}
}

// TestPlanGivesUpAfterBoundedRounds: the loop must terminate. A model that
// keeps dropping the same node would otherwise consume the user's budget
// indefinitely.
func TestPlanGivesUpAfterBoundedRounds(t *testing.T) {
	// An unknown id cannot be repaired locally: the paragraph it names does
	// not exist, so the model has to answer again. Using a locally-fixable
	// defect here would let the first pass succeed and never exercise the
	// bound.
	stub := newStub(t, unknownRefPlanJSON())
	c := newClient(t, stub.URL)

	_, err := c.Plan(context.Background(), testDoc(), nil)
	if err == nil {
		t.Fatal("始终不合格时应返回错误")
	}
	// 1 initial attempt + maxRepairRounds repairs.
	wantCalls := 1 + maxRepairRounds
	if stub.calls() != wantCalls {
		t.Errorf("调用次数 = %d, 期望 %d", stub.calls(), wantCalls)
	}

	// The error must carry the rejected plan and its verdict, so the UI can
	// open the 校对 screen with something to fix.
	var pe *PlanError
	if !errors.As(err, &pe) {
		t.Fatalf("期望 *PlanError, 得到 %T: %v", err, err)
	}
	if pe.Plan == nil {
		t.Error("PlanError 应携带最后一次的方案")
	}
	if pe.Report == nil {
		t.Error("PlanError 应携带校验结果")
	}
	if pe.Rounds != wantCalls {
		t.Errorf("Rounds = %d, 期望 %d", pe.Rounds, wantCalls)
	}
	// The message must name the offending reference so the user can act.
	if !strings.Contains(err.Error(), "n9999") && !strings.Contains(err.Error(), "不存在") {
		t.Errorf("错误信息应指明问题: %v", err)
	}
}

// TestRepairFeedbackListsMissingParagraphs verifies the repair prompt is
// actionable rather than a generic "try again".
func TestRepairFeedbackListsMissingParagraphs(t *testing.T) {
	doc := testDoc()
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、总体要求", Level: 1,
			HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
	}}
	report := docmodel.VerifyPlan(doc, plan)
	if report.OK() {
		t.Fatal("测试前提：该方案应不合格")
	}

	msg := repairPrompt(report, doc, false)
	for _, must := range []string{"n0003", "第二段正文", "uncovered_content", "不得改写"} {
		if !strings.Contains(msg, must) {
			t.Errorf("修复提示缺少 %q:\n%s", must, msg)
		}
	}
}

// ---------------------------------------------------------------- errors

// TestPlanRejectsTruncatedAnswer: a plan cut off by the output cap can look
// complete while missing chapters, so it must be a hard failure.
func TestPlanRejectsTruncatedAnswer(t *testing.T) {
	stub := newStub(t)
	stub.rawBody = func(int) string {
		return chatResponse(`{"sections":[{"id":"s1","title":"被截断`, "length")
	}
	c := newClient(t, stub.URL)

	_, err := c.Plan(context.Background(), testDoc(), nil)
	if err == nil {
		t.Fatal("截断的回答应报错")
	}
	if !errors.Is(err, ErrTruncated) {
		t.Errorf("期望 ErrTruncated, 得到 %v", err)
	}
	if !strings.Contains(err.Error(), "MaxOutputTokens") {
		t.Errorf("错误信息应提示如何解决: %v", err)
	}
}

// TestTruncatedIsNotRepaired: retrying a truncated answer unchanged would
// truncate again, so it fails immediately instead of burning a round.
func TestTruncatedIsNotRepaired(t *testing.T) {
	stub := newStub(t)
	stub.rawBody = func(int) string { return chatResponse("{}", "length") }
	c := newClient(t, stub.URL)

	_, _ = c.Plan(context.Background(), testDoc(), nil)
	if stub.calls() != 1 {
		t.Errorf("截断不应触发重试, 调用次数 = %d", stub.calls())
	}
}

func TestPlanRejectsEmptyAnswer(t *testing.T) {
	stub := newStub(t)
	stub.rawBody = func(int) string { return chatResponse("   ", "stop") }
	c := newClient(t, stub.URL)

	_, err := c.Plan(context.Background(), testDoc(), nil)
	if err == nil {
		t.Fatal("空回答应报错")
	}
	if !strings.Contains(err.Error(), "空回答") {
		t.Errorf("错误信息应说明是空回答: %v", err)
	}
}

// TestClassifyBadKeyTellsUserWhatToDo: distinguishing "your key is wrong" from
// "the provider is down" is what stops a user from re-entering a correct key.
func TestClassifyBadKeyTellsUserWhatToDo(t *testing.T) {
	stub := newStub(t)
	stub.statusOf = func(int) int { return http.StatusUnauthorized }
	c := newClient(t, stub.URL)

	_, err := c.Plan(context.Background(), testDoc(), nil)
	if err == nil {
		t.Fatal("401 应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "API Key") {
		t.Errorf("401 应提示检查 API Key, 实际: %v", err)
	}
	if !strings.Contains(msg, "401") {
		t.Errorf("错误信息应包含状态码: %v", err)
	}
}

func TestClassifyRateLimitSaysRetry(t *testing.T) {
	stub := newStub(t)
	stub.statusOf = func(int) int { return http.StatusTooManyRequests }
	c := newClient(t, stub.URL)

	_, err := c.Plan(context.Background(), testDoc(), nil)
	if err == nil {
		t.Fatal("429 应报错")
	}
	if !strings.Contains(err.Error(), "稍后重试") {
		t.Errorf("429 应提示稍后重试, 实际: %v", err)
	}
}

func TestClassifyServerErrorSaysRetry(t *testing.T) {
	stub := newStub(t)
	stub.statusOf = func(int) int { return http.StatusBadGateway }
	c := newClient(t, stub.URL)

	_, err := c.Plan(context.Background(), testDoc(), nil)
	if err == nil {
		t.Fatal("502 应报错")
	}
	if !strings.Contains(err.Error(), "稍后重试") {
		t.Errorf("5xx 应提示稍后重试, 实际: %v", err)
	}
}

func TestClassifyNotFoundMentionsModel(t *testing.T) {
	stub := newStub(t)
	stub.statusOf = func(int) int { return http.StatusNotFound }
	c := newClient(t, stub.URL)

	_, err := c.Plan(context.Background(), testDoc(), nil)
	if err == nil {
		t.Fatal("404 应报错")
	}
	if !strings.Contains(err.Error(), "模型") {
		t.Errorf("404 应提示检查模型名称, 实际: %v", err)
	}
}

// TestPlanHonoursCanceledContext: a user who closes the tab must not leave a
// request running.
func TestPlanHonoursCanceledContext(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	if _, err := c.Plan(ctx, testDoc(), nil); err == nil {
		t.Fatal("已取消的 context 应报错")
	}
}

// TestPlanHonoursDeadline: a model endpoint that accepts the connection but
// never answers must not hang the caller forever.
func TestPlanHonoursDeadline(t *testing.T) {
	// The handler must not block indefinitely: httptest.Server.Close waits for
	// outstanding handlers, so a handler parked on `<-r.Context().Done()`
	// deadlocks the test rather than failing it. Waiting on a bounded timer
	// keeps the connection open long enough to trip the client's deadline while
	// still letting Close finish.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	defer func() {
		close(release)
		slow.Close()
	}()

	c, err := New(Config{
		Endpoint: slow.URL, APIKey: "k", Model: "m",
		Timeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	if _, err := c.Plan(context.Background(), testDoc(), nil); err == nil {
		t.Fatal("超时应报错")
	}
	// The deadline must actually bound the call, not merely be configured.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("超时未生效, 耗时 %v", elapsed)
	}
}

// TestPlanHonoursCallerContextDeadline: the caller's own deadline must also
// bound the call, since the server passes one in.
func TestPlanHonoursCallerContextDeadline(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	defer func() {
		close(release)
		slow.Close()
	}()

	// No client-side timeout: only the context can end this call.
	c, err := New(Config{Endpoint: slow.URL, APIKey: "k", Model: "m", Timeout: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.Plan(ctx, testDoc(), nil); err == nil {
		t.Fatal("context 超时应报错")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("context 超时未生效, 耗时 %v", elapsed)
	}
}

func TestPlanRejectsNilAndEmptyDocument(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)

	if _, err := c.Plan(context.Background(), nil, nil); err == nil {
		t.Error("nil 文档应报错")
	}
	empty := &docmodel.Document{SourceName: "e.txt"}
	if _, err := c.Plan(context.Background(), empty, nil); err == nil {
		t.Error("空文档应报错")
	}
	// Neither should have reached the network.
	if stub.calls() != 0 {
		t.Errorf("无效输入不应发起请求, 实际 %d 次", stub.calls())
	}
}

func TestNilClientIsNotConfigured(t *testing.T) {
	var c *Client
	if _, err := c.Plan(context.Background(), testDoc(), nil); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("期望 ErrNotConfigured, 得到 %v", err)
	}
}

// ---------------------------------------------------------------- prompt

// TestBuildUserPromptMarksPageBreaks: a page break carries no prose, so the
// model must be told it needs no coverage, or it may invent a section for it.
func TestBuildUserPromptMarksPageBreaks(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "甲", Order: 0},
			{ID: "n0002", Kind: docmodel.KindPageBreak, Order: 1},
		},
	}
	p := buildUserPrompt(doc, "", false)
	if !strings.Contains(p, "分页符") || !strings.Contains(p, "无需收录") {
		t.Errorf("提示应说明分页符无需收录:\n%s", p)
	}
	// The count it states must match what verification will demand.
	if !strings.Contains(p, "需要收录的正文段落 1 个") {
		t.Errorf("提示中的段落数不符:\n%s", p)
	}
}

// TestBuildUserPromptSummarisesTables: dumping a large annex table would waste
// the context window without helping the model judge structure.
func TestBuildUserPromptSummarisesTables(t *testing.T) {
	rows := make([][]string, 200)
	for i := range rows {
		rows[i] = []string{fmt.Sprint(i), "x", "y", "z"}
	}
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindTable, Rows: rows, Text: "表", Order: 0},
		},
	}
	p := buildUserPrompt(doc, "", false)
	if !strings.Contains(p, "200行×4列") {
		t.Errorf("表格应以行列摘要形式呈现:\n%s", p)
	}
	if len(p) > 2000 {
		t.Errorf("提示过长（%d 字节），表格未被摘要", len(p))
	}
}

func TestBuildUserPromptIncludesHintWithoutClaimingItIsContent(t *testing.T) {
	p := buildUserPrompt(testDoc(), "这是一份通报", false)
	if !strings.Contains(p, "这是一份通报") {
		t.Error("用户提示应出现在提示词中")
	}
	if !strings.Contains(p, "不是要写入正文的内容") {
		t.Error("提示词应说明用户提示不是正文内容，避免模型把它写进正文")
	}
}

func TestFirstLineIsRuneSafe(t *testing.T) {
	// A naive byte slice would split a multi-byte character here.
	long := strings.Repeat("中", 100)
	got := firstLine(long, 10)
	if n := len([]rune(got)); n > 11 { // 10 runes + the ellipsis
		t.Errorf("截断后长度 = %d runes, 期望 ≤11", n)
	}
	if strings.Contains(got, "\ufffd") {
		t.Errorf("截断破坏了字符编码: %q", got)
	}
}

func TestStageNamesAreStable(t *testing.T) {
	// The UI keys progress rendering on these names.
	want := map[string]string{
		StageConnect: "connect", StageAnalyze: "analyze", StageRepair: "repair",
		StageValidate: "validate", StageComplete: "complete",
	}
	for got, expect := range want {
		if got != expect {
			t.Errorf("阶段常量 %q 与约定 %q 不符", got, expect)
		}
	}
}

// ---------------------------------------------------------------- id 唯一性

// TestParsePlanAssignsUniqueSectionIDs: the model may omit ids (or repeat
// them). Numbering each level separately produced a parent and its first child
// both named "s1", which renders fine but makes every id-addressed client
// operation land on two chapters at once.
func TestParsePlanAssignsUniqueSectionIDs(t *testing.T) {
	doc := testDoc()
	answer := `{"sections":[{"title":"一、总体要求","level":1,"heading_node_id":"n0001",` +
		`"body_node_ids":["n0002"],"children":[{"title":"（一）子项","level":2,` +
		`"body_node_ids":["n0003"]}]}]}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	seen := map[string]bool{}
	var walk func([]docmodel.Section)
	walk = func(list []docmodel.Section) {
		for _, s := range list {
			if s.ID == "" {
				t.Fatal("章节缺少 id")
			}
			if seen[s.ID] {
				t.Errorf("章节 id %q 重复", s.ID)
			}
			seen[s.ID] = true
			walk(s.Children)
		}
	}
	walk(plan.Sections)
}

// TestParsePlanCanonicalizesSectionIDs: every accepted plan gets preorder ids.
//
// This used to preserve ids the model supplied uniquely, on the theory that
// they were information. In practice the model names sub-sections by its own
// scheme ("s1a", "s2-3"), so two runs over the same document produced the same
// tree with different ids — and the review screen keys rows on the id. A
// canonical preorder is stable for a given tree, unique by construction, and
// still usable as the address for every client operation.
func TestParsePlanCanonicalizesSectionIDs(t *testing.T) {
	doc := testDoc()
	answer := `{"sections":[` +
		`{"id":"intro","title":"一、总体要求","level":1,"heading_node_id":"n0001","body_node_ids":["n0002"]},` +
		`{"id":"intro","title":"二、其他","level":1,"body_node_ids":["n0003"]}]}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if plan.Sections[0].ID != "s1" || plan.Sections[1].ID != "s2" {
		t.Errorf("章节 id 应为前序编号 s1/s2, 实际 %q/%q",
			plan.Sections[0].ID, plan.Sections[1].ID)
	}
}

// TestParsePlanNeverCollidesWithLaterID: renumbering must not invent an id that
// the model already used deeper in the tree.
func TestParsePlanNeverCollidesWithLaterID(t *testing.T) {
	doc := testDoc()
	// First section has no id; a later sibling uses "s1".
	answer := `{"sections":[` +
		`{"title":"一、甲","level":1,"heading_node_id":"n0001","body_node_ids":["n0002"]},` +
		`{"id":"s1","title":"二、乙","level":1,"body_node_ids":["n0003"]}]}`
	plan, err := ParsePlan(answer, doc)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range plan.Sections {
		if seen[s.ID] {
			t.Errorf("id %q 重复：补号与模型已用的 id 撞了", s.ID)
		}
		seen[s.ID] = true
	}
}

// TestRepairPromptKeepsEditsWhenAllowed: the closing restatement used to say
// "不得改写任何原文" regardless of mode, contradicting the turn that had just
// asked the model for declared fixes. Under allowEdits it must instead tell
// the model to keep them.
func TestRepairPromptKeepsEditsWhenAllowed(t *testing.T) {
	doc := testDoc()
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、总体要求", Level: 1, HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
	}}
	report := docmodel.VerifyPlan(doc, plan)
	if report.OK() {
		t.Fatal("测试前提：该方案应不合格")
	}

	msg := repairPrompt(report, doc, true)
	if strings.Contains(msg, "不得改写任何原文") {
		t.Errorf("allowEdits 时不应再说「不得改写任何原文」:\n%s", msg)
	}
	if !strings.Contains(msg, "edits") {
		t.Errorf("allowEdits 时应要求保留 edits:\n%s", msg)
	}

	off := repairPrompt(report, doc, false)
	if !strings.Contains(off, "不得改写任何原文") {
		t.Errorf("未开启时应保持原有约束:\n%s", off)
	}
}

// ---------------------------------------------------------------- 结构摘要

// TestPromptListsUnstyledNumberedHeadings: a .docx whose headings were typed
// as ordinary paragraphs parses as 正文, so the model has no way to tell a
// heading from prose. The prompt must point those lines out.
func TestPromptListsUnstyledNumberedHeadings(t *testing.T) {
	doc := &docmodel.Document{Nodes: []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: "一、总体要求"},
		{ID: "n0002", Kind: docmodel.KindParagraph, Text: "正文。"},
		{ID: "n0003", Kind: docmodel.KindParagraph, Text: "（一）主要目标"},
	}}
	prompt := buildUserPrompt(doc, "", false)
	for _, must := range []string{"n0001", "n0003", "无标题样式", "heading_node_id"} {
		if !strings.Contains(prompt, must) {
			t.Errorf("结构摘要缺少 %q:\n%s", must, prompt)
		}
	}
}

// TestPromptOmitsHintsWithoutHeadings: a document with no structural markers
// must not get an empty section heading, which would be noise.
func TestPromptOmitsHintsWithoutHeadings(t *testing.T) {
	doc := &docmodel.Document{Nodes: []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: "普通一段话，没有任何编号。"},
	}}
	prompt := buildUserPrompt(doc, "", false)
	if strings.Contains(prompt, "原文自带的层次标题") {
		t.Errorf("无标题时不应输出结构摘要:\n%s", prompt)
	}
}

// TestPromptReportsStyledHeadings: a real Heading style is reported as such, so
// the model knows the level came from markup rather than from text.
func TestPromptReportsStyledHeadings(t *testing.T) {
	doc := testDoc()
	prompt := buildUserPrompt(doc, "", false)
	if !strings.Contains(prompt, "标题样式 层级1") {
		t.Errorf("样标题层级未写进提示词:\n%s", prompt)
	}
}
