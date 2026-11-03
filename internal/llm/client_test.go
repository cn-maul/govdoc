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
	rawBody  func(call int) string
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

		if s.statusOf != nil {
			if code := s.statusOf(call); code != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error":{"message":"stub failure","type":"invalid_request_error"}}`))
				return
			}
		}
		if s.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(`{"error":{"message":"stub failure","type":"invalid_request_error"}}`))
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

// TestTemperatureOmittedByDefault: sending a temperature the provider rejects
// would fail every call, so it is only sent when configured.
func TestTemperatureOmittedByDefault(t *testing.T) {
	stub := newStub(t, goodPlanJSON())
	c := newClient(t, stub.URL)
	if _, err := c.Plan(context.Background(), testDoc(), nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, present := stub.request(0)["temperature"]; present {
		t.Error("未配置 temperature 时不应发送该字段")
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

// TestPlanRepairsIncompleteAnswer is the core quality mechanism: when the
// model drops a paragraph, the specific failure is fed back and the corrected
// answer is accepted.
func TestPlanRepairsIncompleteAnswer(t *testing.T) {
	stub := newStub(t, incompletePlanJSON(), goodPlanJSON())
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

	// The accepted plan must cover everything.
	rep := docmodel.VerifyPlan(testDoc(), plan)
	if !rep.OK() {
		t.Fatalf("最终方案应通过校验: %+v", rep.Diagnostics)
	}

	// The repair round must actually tell the model what was wrong.
	msgs, _ := stub.request(1)["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("第二轮请求应包含历史与反馈, 实际 %d 条消息", len(msgs))
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	feedback, _ := last["content"].(string)
	if !strings.Contains(feedback, "n0003") {
		t.Errorf("反馈应指明漏掉的节点 n0003, 实际:\n%s", feedback)
	}
	if !strings.Contains(feedback, "uncovered_content") {
		t.Errorf("反馈应包含诊断代码, 实际:\n%s", feedback)
	}

	// The repair stage must be reported to the user.
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
	stub := newStub(t, incompletePlanJSON()) // always incomplete
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
	// The message must name the missing paragraph so the user can act.
	if !strings.Contains(err.Error(), "n0003") && !strings.Contains(err.Error(), "未被任何章节收录") {
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

	msg := repairPrompt(report, doc)
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
