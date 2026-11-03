package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// The prompt is the only channel through which the model learns whether it may
// correct content. These tests pin the two modes apart, because getting it
// wrong in either direction is a real failure: telling the model it may edit
// when the user said no would produce corrections the user did not ask for,
// and telling it it may not when the user said yes silently disables the
// feature.

func TestSystemPromptForbidsEditsByDefault(t *testing.T) {
	doc := testDoc()
	p := systemPrompt(doc, false)

	// The prohibition must be present and unambiguous.
	for _, must := range []string{"不得改写", "一个字都不能改", "不要输出 edits"} {
		if !strings.Contains(p, must) {
			t.Errorf("禁止模式下提示词缺少「%s」", must)
		}
	}
	// And the edit vocabulary must not be offered.
	if strings.Contains(p, "typo            错别字") {
		t.Error("禁止模式下不应列出可修改的错误类型")
	}
}

func TestSystemPromptAllowsOnlyDeclaredEdits(t *testing.T) {
	doc := testDoc()
	p := systemPrompt(doc, true)

	// Every kind the model may propose as an edit must be named, so it does not
	// invent its own vocabulary that the enum cannot represent.
	//
	// EditHeading is deliberately excluded, and this exclusion is the point:
	// adding a heading is structural (a Section title with no heading_node_id),
	// NOT an edit. Listing it as an edit kind invited the model to emit
	// `{kind:"heading", node_id:""}`, which cannot be applied — it was silently
	// dropped before, and is now reported as unapplied. The prompt must
	// therefore (a) name the other kinds, (b) not offer `heading` as an edit
	// kind, and (c) still tell the model how to add a heading.
	for _, kind := range docmodel.AllEditKinds() {
		if kind == docmodel.EditOther || kind == docmodel.EditHeading {
			continue
		}
		if !strings.Contains(p, string(kind)) {
			t.Errorf("允许模式下提示词未列出改动类型 %q", kind)
		}
	}

	// (b) `heading` must not appear in the edit-kind list. Check the list block
	// itself rather than the whole prompt, since heading_node_id is mentioned
	// legitimately elsewhere — a substring test over the whole text would pass
	// for the wrong reason.
	listStart := strings.Index(p, "用户允许你对原文中的下列问题提出修改")
	if listStart < 0 {
		t.Fatal("提示词缺少改动类型清单")
	}
	listEnd := strings.Index(p[listStart:], "修改**必须**写进 edits")
	if listEnd < 0 {
		t.Fatal("提示词缺少 edits 写法说明")
	}
	list := p[listStart : listStart+listEnd]
	if strings.Contains(list, "heading") {
		t.Errorf("改动类型清单里不应出现 heading（新增标题走章节 title）:\n%s", list)
	}

	// (c) The working mechanism for a generated heading must still be explained,
	// including the requirement that the model leaves heading_node_id empty.
	if !strings.Contains(p, "heading_node_id") {
		t.Error("提示词应说明生成标题要用 heading_node_id 留空的章节")
	}
	if !strings.Contains(p, "留空") && !strings.Contains(p, "空") {
		t.Error("提示词应说明生成标题时 heading_node_id 要留空")
	}
	// The structure requirement is what makes the change auditable.
	for _, must := range []string{"before", "after", "reason", "edits"} {
		if !strings.Contains(p, must) {
			t.Errorf("提示词缺少 %q，模型可能不按结构输出", must)
		}
	}
	// And the restraint instruction: this is the difference between "fix
	// errors" and "rewrite the document for me".
	for _, must := range []string{"不要顺手润色", "能不改就不改", "不要改变原意"} {
		if !strings.Contains(p, must) {
			t.Errorf("提示词缺少克制要求「%s」", must)
		}
	}
	// The Before must be pinned to the source, or the edit cannot be applied.
	if !strings.Contains(p, "逐字出现") {
		t.Error("提示词应要求 before 必须是原文中逐字出现的片段")
	}
}

// TestSystemPromptAlwaysForbidsSilentRewrites: even with edits allowed, an
// undeclared change must never be permitted. That distinction is the whole
// safety argument for relaxing the rule.
func TestSystemPromptAlwaysForbidsSilentRewrites(t *testing.T) {
	for _, allow := range []bool{false, true} {
		p := systemPrompt(testDoc(), allow)
		// In both modes the model is told the body text is referenced by id,
		// not retyped — which is what keeps an undeclared rewrite impossible.
		if !strings.Contains(p, "引用段落编号") && !strings.Contains(p, "node id") {
			t.Errorf("allowEdits=%v 时未说明正文只能通过编号引用", allow)
		}
		if !strings.Contains(p, "恰好引用一次") {
			t.Errorf("allowEdits=%v 时未说明每段必须恰好引用一次", allow)
		}
	}
}

func TestUserPromptMentionsEditsOnlyWhenAllowed(t *testing.T) {
	doc := testDoc()

	allowed := buildUserPrompt(doc, "", true)
	if !strings.Contains(allowed, "edits") {
		t.Error("允许模式下用户提示应提到 edits")
	}
	if !strings.Contains(allowed, "逐字出现") {
		t.Error("允许模式下用户提示应强调 before 必须逐字匹配")
	}

	forbidden := buildUserPrompt(doc, "", false)
	if strings.Contains(forbidden, "edits") {
		t.Error("禁止模式下用户提示不应提到 edits")
	}
}

// TestParsePlanRoundTripsEdits: the edits the model emits must survive parsing
// with every field intact, or the UI has nothing to show and the assembler
// nothing to apply.
func TestParsePlanRoundTripsEdits(t *testing.T) {
	answer := `{
	  "edits": [
	    {"node_id": "n0002", "kind": "typo", "before": "按排", "after": "安排",
	     "reason": "错别字"},
	    {"node_id": "n0002", "kind": "grammar", "before": "我们要做好。",
	     "after": "我们要做好这项工作。", "reason": "句子不完整", "occurrence": 1}
	  ],
	  "sections": [
	    {"id": "s1", "title": "一、总体要求", "level": 1,
	     "heading_node_id": "n0001", "body_node_ids": ["n0002", "n0003"]}
	  ]
	}`
	plan, err := ParsePlan(answer, testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Edits) != 2 {
		t.Fatalf("改动数 = %d, 期望 2", len(plan.Edits))
	}

	first := plan.Edits[0]
	if first.NodeID != "n0002" || first.Kind != docmodel.EditTypo {
		t.Errorf("第 1 条改动不符: %+v", first)
	}
	if first.Before != "按排" || first.After != "安排" || first.Reason != "错别字" {
		t.Errorf("第 1 条改动字段丢失: %+v", first)
	}

	second := plan.Edits[1]
	if second.Kind != docmodel.EditGrammar {
		t.Errorf("第 2 条类型不符: %q", second.Kind)
	}
	if second.Occurrence != 1 {
		t.Errorf("Occurrence 未保留: %d", second.Occurrence)
	}
}

// TestParsePlanKeepsEditsThroughFenceAndProse: the model wraps its answer in a
// fence and adds a preamble constantly; edits must survive that too.
func TestParsePlanKeepsEditsThroughFenceAndProse(t *testing.T) {
	answer := "好的，以下是结构方案：\n\n```json\n" + `{
	  "edits": [{"node_id":"n0002","kind":"typo","before":"按排","after":"安排"}],
	  "sections":[{"id":"s1","title":"一、总体要求","level":1,
	    "heading_node_id":"n0001","body_node_ids":["n0002","n0003"]}]
	}` + "\n```\n\n希望对您有帮助。"
	plan, err := ParsePlan(answer, testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Edits) != 1 {
		t.Fatalf("围栏与前后文中的 edits 应被解析, 实际 %d 条", len(plan.Edits))
	}
	if plan.Edits[0].Before != "按排" {
		t.Errorf("改动内容不符: %+v", plan.Edits[0])
	}
}

// TestParsePlanNormalisesNilEdits: the UI iterates plan.edits directly, so a
// missing array must become an empty one rather than JSON null.
func TestParsePlanNormalisesNilEdits(t *testing.T) {
	answer := `{"sections":[{"id":"s1","title":"章","level":1,
	  "heading_node_id":"n0001","body_node_ids":["n0002","n0003"]}]}`
	plan, err := ParsePlan(answer, testDoc())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Edits == nil {
		t.Error("Edits 应规范化为非 nil 切片，避免前端处理 null")
	}
	// It must also marshal back as a usable shape.
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"edits":null`) {
		t.Errorf("Edits 不应序列化为 null: %s", b)
	}
}

// TestParsePlanToleratesUnknownEditKind: a model may invent a kind. Storing it
// verbatim keeps the change visible (with a generic label) instead of dropping
// a correction the user might want.
func TestParsePlanToleratesUnknownEditKind(t *testing.T) {
	answer := `{
	  "edits": [{"node_id":"n0002","kind":"style","before":"甲","after":"乙"}],
	  "sections":[{"id":"s1","title":"一、总体要求","level":1,
	    "heading_node_id":"n0001","body_node_ids":["n0002","n0003"]}]
	}`
	plan, err := ParsePlan(answer, testDoc())
	if err != nil {
		t.Fatalf("未知改动类型不应导致解析失败: %v", err)
	}
	if len(plan.Edits) != 1 {
		t.Fatalf("改动应被保留, 实际 %d 条", len(plan.Edits))
	}
	if plan.Edits[0].Kind != docmodel.EditKind("style") {
		t.Errorf("类型应原样保留: %q", plan.Edits[0].Kind)
	}
	// And it must still render as a label rather than an empty cell.
	if label := docmodel.EditLabel(plan.Edits[0].Kind); label == "" {
		t.Error("未知类型也应有可显示的标签")
	}
}

// TestParsePlanAcceptsEmptyEditsList: a clean document legitimately produces no
// corrections, and that must not be treated as a malformed answer.
func TestParsePlanAcceptsEmptyEditsList(t *testing.T) {
	answer := `{
	  "edits": [],
	  "sections":[{"id":"s1","title":"一、总体要求","level":1,
	    "heading_node_id":"n0001","body_node_ids":["n0002","n0003"]}]
	}`
	plan, err := ParsePlan(answer, testDoc())
	if err != nil {
		t.Fatalf("空 edits 应被接受: %v", err)
	}
	if len(plan.Edits) != 0 {
		t.Errorf("不应有改动, 实际 %d", len(plan.Edits))
	}
	if rep := docmodel.VerifyPlan(testDoc(), plan); !rep.OK() {
		t.Errorf("无改动的完整方案应通过校验: %+v", rep.Diagnostics)
	}
}

// TestPlanOptionsDefaultsAreSafe: AllowEdits is opt-in, so the zero value must
// mean "do not change anything".
func TestPlanOptionsDefaultsAreSafe(t *testing.T) {
	var opts PlanOptions
	if opts.AllowEdits {
		t.Error("AllowEdits 的零值应为 false（不修改原文）")
	}
	if opts.Hint != "" {
		t.Error("Hint 的零值应为空")
	}
}
