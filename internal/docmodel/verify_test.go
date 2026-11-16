package docmodel

import (
	"strings"
	"testing"
)

func doc(nodes ...Node) *Document {
	for i := range nodes {
		nodes[i].Order = i
	}
	return &Document{SourceName: "x.docx", Format: "docx", Nodes: nodes}
}

func p(id, text string) Node {
	return Node{ID: id, Kind: KindParagraph, Text: text}
}

func h(id, text string, level int) Node {
	return Node{ID: id, Kind: KindHeading, Text: text, Level: level}
}

// ---------------------------------------------------------------- happy path

func TestVerifyPlanAcceptsCompletePlan(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "乙"), p("n0003", "丙"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "第一章", Level: 1, BodyNodeIDs: []string{"n0001", "n0002"}},
			{ID: "s2", Title: "第二章", Level: 1, BodyNodeIDs: []string{"n0003"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if !rep.OK() {
		t.Fatalf("期望通过，实际: %s\n诊断: %+v", rep.Summary(), rep.Diagnostics)
	}
	if rep.CoveredNodes != 3 || rep.ContentNodes != 3 {
		t.Errorf("覆盖计数错误: %d/%d", rep.CoveredNodes, rep.ContentNodes)
	}
	if rep.DuplicatedNodes != 0 || rep.UnknownRefs != 0 {
		t.Errorf("不应有重复或无效引用: dup=%d unknown=%d", rep.DuplicatedNodes, rep.UnknownRefs)
	}
}

// ---------------------------------------------------------------- core refusals

// TestVerifyPlanCatchesDroppedContent is the single most important check in
// the project: a paragraph the plan forgot must be reported as an error, or
// the user could silently lose part of their document.
func TestVerifyPlanCatchesDroppedContent(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "会被丢掉的乙"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "第一章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("丢失内容必须导致校验失败")
	}
	if rep.CoveredNodes != 1 || rep.ContentNodes != 2 {
		t.Errorf("覆盖计数错误: %d/%d", rep.CoveredNodes, rep.ContentNodes)
	}
	found := false
	for _, dg := range rep.Diagnostics {
		if dg.Code == "uncovered_content" && dg.Severity == SevError {
			found = true
			if !strings.Contains(dg.Message, "n0002") {
				t.Errorf("诊断未指明节点: %s", dg.Message)
			}
		}
	}
	if !found {
		t.Errorf("缺少 uncovered_content 错误: %+v", rep.Diagnostics)
	}
}

// TestVerifyPlanCatchesDuplicatedContent: repeating a paragraph is how a
// "restructure" silently becomes a rewrite.
func TestVerifyPlanCatchesDuplicatedContent(t *testing.T) {
	d := doc(p("n0001", "只应出现一次"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "甲", Level: 1, BodyNodeIDs: []string{"n0001"}},
			{ID: "s2", Title: "乙", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("重复收录必须导致校验失败")
	}
	if rep.DuplicatedNodes != 1 {
		t.Errorf("重复计数 = %d, 期望 1", rep.DuplicatedNodes)
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "duplicate_content" {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 duplicate_content 诊断: %+v", rep.Diagnostics)
	}
}

// TestVerifyPlanCatchesUnknownRef: an id the source lacks must never be
// treated as coverage — otherwise the count could look complete while
// content was actually lost.
func TestVerifyPlanCatchesUnknownRef(t *testing.T) {
	d := doc(p("n0001", "甲"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "甲", Level: 1, BodyNodeIDs: []string{"n0001", "n0042"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("无效引用必须导致校验失败")
	}
	if rep.UnknownRefs != 1 {
		t.Errorf("无效引用计数 = %d, 期望 1", rep.UnknownRefs)
	}
	if rep.CoveredNodes != 1 || rep.ContentNodes != 1 {
		t.Errorf("无效引用不得影响覆盖计数: %d/%d", rep.CoveredNodes, rep.ContentNodes)
	}
}

// TestHeadingMismatchIsError: pointing at a source node but supplying
// different prose would let the model rewrite a heading while appearing to
// preserve it.
func TestHeadingMismatchIsError(t *testing.T) {
	d := doc(h("n0001", "原文标题", 1), p("n0002", "正文"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "被改写的标题", Level: 1,
				HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("标题与原文不一致必须导致校验失败")
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "heading_mismatch" {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 heading_mismatch 诊断: %+v", rep.Diagnostics)
	}
}

func TestHeadingMatchIsAccepted(t *testing.T) {
	d := doc(h("n0001", "原文标题", 1), p("n0002", "正文"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "原文标题", Level: 1,
				HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
		},
	}
	if rep := VerifyPlan(d, plan); !rep.OK() {
		t.Fatalf("标题一致时应通过: %+v", rep.Diagnostics)
	}
}

// ---------------------------------------------------------------- warnings

// TestSynthesizedHeadingIsWarning: a model-authored heading is allowed but
// must be surfaced, so the user knows which titles are not their own words.
func TestSynthesizedHeadingIsWarning(t *testing.T) {
	d := doc(p("n0001", "正文"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "模型生成的标题", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if !rep.OK() {
		t.Fatalf("合成标题应可通过: %+v", rep.Diagnostics)
	}
	if rep.SynthesizedHeadings != 1 {
		t.Errorf("合成标题计数 = %d, 期望 1", rep.SynthesizedHeadings)
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "synthesized_heading" && dg.Severity == SevWarning {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 synthesized_heading 警告: %+v", rep.Diagnostics)
	}
}

// TestReorderIsFlaggedNotFatal: moving paragraphs inside a chapter is a
// legitimate restructuring, but it changes the reading order, so the user
// must be told.
func TestReorderIsFlaggedNotFatal(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "乙"), p("n0003", "丙"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0003", "n0001", "n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if !rep.OK() {
		t.Fatalf("重排不应导致失败: %+v", rep.Diagnostics)
	}
	if rep.ReorderedSections != 1 {
		t.Errorf("重排章节计数 = %d, 期望 1", rep.ReorderedSections)
	}
}

func TestDeclaredReorderIsInfo(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "乙"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, PreserveOrder: true,
				BodyNodeIDs: []string{"n0002", "n0001"}},
		},
	}
	rep := VerifyPlan(d, plan)
	for _, dg := range rep.Diagnostics {
		if dg.Code == "reordered" && dg.Severity != SevInfo {
			t.Errorf("已声明重排应为 info, 实际 %s", dg.Severity)
		}
	}
}

// ---------------------------------------------------------------- structure

func TestBadLevelIsError(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "乙"))
	plan := &Plan{
		Sections: []Section{
			{
				ID: "s1", Title: "父", Level: 2, BodyNodeIDs: []string{"n0001"},
				Children: []Section{
					// Child not deeper than parent.
					{ID: "s1a", Title: "子", Level: 2, BodyNodeIDs: []string{"n0002"}},
				},
			},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("子章节层级未深于父级必须导致失败")
	}
}

func TestZeroLevelIsError(t *testing.T) {
	d := doc(p("n0001", "甲"))
	plan := &Plan{
		Sections: []Section{{ID: "s1", Title: "章", Level: 0, BodyNodeIDs: []string{"n0001"}}},
	}
	if rep := VerifyPlan(d, plan); rep.OK() {
		t.Fatal("层级 0 必须导致失败")
	}
}

func TestMissingSectionIDIsError(t *testing.T) {
	d := doc(p("n0001", "甲"))
	plan := &Plan{
		Sections: []Section{{Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}}},
	}
	if rep := VerifyPlan(d, plan); rep.OK() {
		t.Fatal("缺少章节 id 必须导致失败")
	}
}

func TestNilPlanIsError(t *testing.T) {
	d := doc(p("n0001", "甲"))
	rep := VerifyPlan(d, nil)
	if rep.OK() {
		t.Fatal("空方案必须导致失败")
	}
}

// TestPageBreakIsNotContent: a page break carries no prose, so it must not be
// required for coverage nor counted in the denominator.
func TestPageBreakIsNotContent(t *testing.T) {
	d := doc(
		p("n0001", "甲"),
		Node{ID: "n0002", Kind: KindPageBreak},
		p("n0003", "乙"),
	)
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001", "n0003"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if !rep.OK() {
		t.Fatalf("分页符不应影响校验: %+v", rep.Diagnostics)
	}
	if rep.ContentNodes != 2 {
		t.Errorf("正文节点数 = %d, 期望 2（分页符不计入）", rep.ContentNodes)
	}
}

// ---------------------------------------------------------------- elements

// TestElementBindingCountsAsCoverage is the guard for a real workflow: a 公文
// already contains its own 标题/成文日期 paragraphs. Binding them must
// satisfy coverage rather than being reported as dropped content.
func TestElementBindingCountsAsCoverage(t *testing.T) {
	d := doc(
		p("n0001", "关于某事的通知"),
		p("n0002", "正文内容"),
		p("n0003", "2024年5月1日"),
	)
	plan := &Plan{
		Elements: []ElementBinding{
			{Role: RoleTitle, NodeID: "n0001"},
			{Role: RoleDate, NodeID: "n0003"},
		},
		Sections: []Section{
			{ID: "s1", Title: "通知事项", Level: 1, BodyNodeIDs: []string{"n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if !rep.OK() {
		t.Fatalf("要素绑定应满足覆盖: %+v", rep.Diagnostics)
	}
	if rep.BoundElements != 2 {
		t.Errorf("要素绑定计数 = %d, 期望 2", rep.BoundElements)
	}
}

func TestBindingBodyRoleIsError(t *testing.T) {
	d := doc(p("n0001", "正文"))
	plan := &Plan{
		Elements: []ElementBinding{{Role: RoleBody, NodeID: "n0001"}},
	}
	if rep := VerifyPlan(d, plan); rep.OK() {
		t.Fatal("将节点绑定为正文必须导致失败")
	}
}

func TestDuplicateElementRoleIsError(t *testing.T) {
	d := doc(p("n0001", "标题甲"), p("n0002", "标题乙"))
	plan := &Plan{
		Elements: []ElementBinding{
			{Role: RoleTitle, NodeID: "n0001"},
			{Role: RoleTitle, NodeID: "n0002"},
		},
	}
	if rep := VerifyPlan(d, plan); rep.OK() {
		t.Fatal("同一要素绑定多个节点必须导致失败")
	}
}

// TestUnknownElementRoleIsError is a silent-data-loss guard. The assembler
// places bound nodes by looking up their canonical role constant, so a
// misspelled role (say "titel", from a model's typo) would never be emitted.
// If VerifyPlan credited the node as covered anyway, the paragraph would
// disappear from the output while every check reported success.
func TestUnknownElementRoleIsError(t *testing.T) {
	d := doc(p("n0001", "正文内容"))
	plan := &Plan{
		Elements: []ElementBinding{{Role: Role("titel"), NodeID: "n0001"}},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("未知要素 role 必须导致校验失败")
	}
	// Crucially: it must NOT be counted as coverage.
	if rep.CoveredNodes != 0 {
		t.Errorf("未知要素不得计入覆盖: CoveredNodes = %d, 期望 0", rep.CoveredNodes)
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "bad_element" && dg.Severity == SevError {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 bad_element 诊断: %+v", rep.Diagnostics)
	}
}

// TestBodyElementDoesNotCountAsCoverage: same reasoning — binding to 正文 is
// rejected and must not satisfy the coverage check, or the content would be
// reported present and then never rendered.
func TestBodyElementDoesNotCountAsCoverage(t *testing.T) {
	d := doc(p("n0001", "正文内容"))
	plan := &Plan{
		Elements: []ElementBinding{{Role: RoleBody, NodeID: "n0001"}},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("绑定为正文必须导致失败")
	}
	if rep.CoveredNodes != 0 {
		t.Errorf("绑定为正文不得计入覆盖: CoveredNodes = %d, 期望 0", rep.CoveredNodes)
	}
}

// TestEveryBindableRoleIsActuallyPlaced guards the opposite direction: a role
// listed as bindable but never emitted by the assembler would be the same
// silent-loss bug, reached the other way round.
func TestEveryBindableRoleIsActuallyPlaced(t *testing.T) {
	notBindable := map[Role]bool{
		RoleHeading1: true, RoleHeading2: true, RoleHeading3: true, RoleHeading4: true,
		RoleRedLine: true, RoleBody: true, RoleAttachment: true,
	}
	for _, r := range BindableRoles() {
		if notBindable[r] {
			t.Errorf("要素 %q 不应可绑定：标题层级/分隔线/正文由装配器自行生成", r)
		}
	}
	// And the reverse: every role the assembler looks up by binding must be
	// listed, or a legitimate binding would be rejected.
	for _, r := range []Role{
		RoleCopyNumber, RoleSecurity, RoleUrgency, RoleMasthead, RoleDocNumber,
		RoleSignatory, RoleTitle, RoleRecipient, RoleAttachmentNote,
		RoleSignature, RoleDate, RoleNote, RoleCC, RolePrinter,
	} {
		if !bindableSet[r] {
			t.Errorf("装配器会查找绑定节点 %q，但它不在 BindableRoles 中", r)
		}
	}
}

// TestPreambleCountsAsCoverage addresses a gap that only a realistic document
// exposes: nearly every 公文 opens with a 缘由 paragraph under 主送机关, before
// the first 层次序数 heading. It is body prose but not a chapter, so without a
// place for it the only way to cover it would be to invent a chapter — which
// would print a heading the author never wrote and consume the "一、" number
// the first real heading then repeats.
func TestPreambleCountsAsCoverage(t *testing.T) {
	d := doc(
		p("n0001", "为贯彻落实上级要求，现将有关事项通知如下。"), // 缘由
		h("n0002", "一、总体要求", 1),
		p("n0003", "正文内容。"),
	)
	plan := &Plan{
		PreambleNodeIDs: []string{"n0001"},
		Sections: []Section{
			{ID: "s1", Title: "一、总体要求", Level: 1,
				HeadingNodeID: "n0002", BodyNodeIDs: []string{"n0003"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if !rep.OK() {
		t.Fatalf("前言段落应计入覆盖: %+v", rep.Diagnostics)
	}
	if rep.CoveredNodes != 3 || rep.ContentNodes != 3 {
		t.Errorf("覆盖计数 = %d/%d, 期望 3/3", rep.CoveredNodes, rep.ContentNodes)
	}
}

// TestPreambleUnknownRefIsError: the preamble is a list of ids like any other,
// so an unknown id must be caught rather than silently ignored.
func TestPreambleUnknownRefIsError(t *testing.T) {
	d := doc(p("n0001", "正文"))
	plan := &Plan{PreambleNodeIDs: []string{"n0001", "n9999"}}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("前言中的无效引用必须导致失败")
	}
	if rep.UnknownRefs != 1 {
		t.Errorf("无效引用计数 = %d, 期望 1", rep.UnknownRefs)
	}
}

// TestPreambleDuplicateIsError: claiming a node both as preamble and as a
// chapter body would print it twice.
func TestPreambleDuplicateIsError(t *testing.T) {
	d := doc(p("n0001", "内容"))
	plan := &Plan{
		PreambleNodeIDs: []string{"n0001"},
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("前言与章节重复收录必须导致失败")
	}
	if rep.DuplicatedNodes != 1 {
		t.Errorf("重复计数 = %d, 期望 1", rep.DuplicatedNodes)
	}
}

// TestDuplicateTitleIsError is a defect found by inspecting a real generated
// document: when the source's own first line is the 标题 and the plan both
// leaves that paragraph in a section's body AND fills Meta.Title, the printed
// document opens with the same sentence twice. Coverage is complete, nothing
// is lost or duplicated by node id, so no other rule fires — the defect is
// visible only on the page.
func TestDuplicateTitleIsError(t *testing.T) {
	d := doc(
		p("n0001", "关于开展安全生产大检查的通知"),
		p("n0002", "正文内容。"),
	)
	plan := &Plan{
		Meta: Meta{Title: "关于开展安全生产大检查的通知"},
		Sections: []Section{
			// n0001 claimed as body while meta.title repeats it.
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001", "n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("标题重复必须导致校验失败")
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "duplicate_title" && dg.Severity == SevError {
			found = true
			if !strings.Contains(dg.Message, "n0001") {
				t.Errorf("诊断应指明重复的节点: %s", dg.Message)
			}
			// The message must say how to fix it, not just that it is wrong.
			if !strings.Contains(dg.Message, "elements") {
				t.Errorf("诊断应给出修复办法: %s", dg.Message)
			}
		}
	}
	if !found {
		t.Errorf("缺少 duplicate_title 诊断: %+v", rep.Diagnostics)
	}
}

// TestBoundTitleIsNotDuplicate: the correct modelling — binding the source's
// own title paragraph to the title slot — must pass. This is the fix the
// diagnostic above points the user to.
func TestBoundTitleIsNotDuplicate(t *testing.T) {
	d := doc(
		p("n0001", "关于开展安全生产大检查的通知"),
		p("n0002", "正文内容。"),
	)
	plan := &Plan{
		Meta: Meta{Title: "关于开展安全生产大检查的通知"},
		Elements: []ElementBinding{
			{Role: RoleTitle, NodeID: "n0001"},
		},
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0002"}},
		},
	}
	if rep := VerifyPlan(d, plan); !rep.OK() {
		t.Fatalf("标题已绑定到原文节点时应通过: %+v", rep.Diagnostics)
	}
}

// TestDuplicateElementIsOneError: a plan that binds the same role twice is
// wrong once, not twice. The assembler emits the first binding, so reporting
// the paragraph as both duplicate_element and duplicate_content inflates a
// single mistake and makes the review screen look like content was both lost
// and copied.
func TestDuplicateElementIsOneError(t *testing.T) {
	d := doc(
		p("n0001", "关于开展某项工作的通知"),
		p("n0002", "正文。"),
	)
	plan := &Plan{
		Elements: []ElementBinding{
			{Role: RoleTitle, NodeID: "n0001"},
			{Role: RoleTitle, NodeID: "n0001"},
		},
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("重复绑定要素必须导致校验失败")
	}
	if rep.DuplicatedNodes != 0 {
		t.Errorf("重复绑定不应额外记为正文重复, dup=%d", rep.DuplicatedNodes)
	}
	var badElement int
	for _, dg := range rep.Diagnostics {
		if dg.Code == "duplicate_element" {
			badElement++
		}
	}
	if badElement != 1 {
		t.Errorf("duplicate_element 诊断数 = %d, 期望 1: %+v", badElement, rep.Diagnostics)
	}
}

// TestDuplicateTitleOnlyWhenActuallyRepeated: a Meta.Title that differs from
// every paragraph is a legitimate synthesized title and must not be flagged —
// the check exists to catch a repeat, not to forbid synthesized titles.
func TestDuplicateTitleOnlyWhenActuallyRepeated(t *testing.T) {
	d := doc(p("n0001", "正文内容。"))
	plan := &Plan{
		Meta: Meta{Title: "由模型拟定的标题"},
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	if rep := VerifyPlan(d, plan); !rep.OK() {
		t.Fatalf("不同的合成标题不应被判重复: %+v", rep.Diagnostics)
	}
}

// TestDuplicateTitleIgnoresUnclaimedMatch: if the matching paragraph is not
// claimed anywhere, the ordinary uncovered_content rule reports it — there is
// no repeat to warn about, so duplicate_title must stay quiet.
func TestDuplicateTitleIgnoresUnclaimedMatch(t *testing.T) {
	d := doc(
		p("n0001", "关于开展某项工作的通知"),
		p("n0002", "正文。"),
	)
	plan := &Plan{
		Meta: Meta{Title: "关于开展某项工作的通知"},
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	var dupTitle bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "duplicate_title" {
			dupTitle = true
		}
	}
	if dupTitle {
		t.Error("未收录的段落不应触发 duplicate_title（应由 uncovered_content 报告）")
	}
	if rep.OK() {
		t.Error("未收录的标题段落应导致失败")
	}
}

// TestHeadingAsBodyIsFlagged is the second defect found by reading a generated
// document. A source heading paragraph listed under body_node_ids is rendered
// by the assembler as a heading again (its own level wins over the section's),
// so the page shows a duplicate heading. Coverage is satisfied — the node is
// claimed exactly once — so nothing else reports it.
func TestHeadingAsBodyIsFlagged(t *testing.T) {
	d := doc(
		h("n0001", "一、总体要求", 1),
		p("n0002", "正文。"),
	)
	plan := &Plan{
		Sections: []Section{
			// n0001 is the section's heading AND also listed as body.
			{ID: "s1", Title: "一、总体要求", Level: 1,
				HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0001", "n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.HeadingsAsBody == 0 {
		t.Fatal("标题被当作正文收录应被计数")
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "heading_as_body" {
			found = true
			if !strings.Contains(dg.Message, "n0001") {
				t.Errorf("诊断应指明节点: %s", dg.Message)
			}
			// The message must point at the fix, not just the symptom.
			if !strings.Contains(dg.Message, "heading_node_id") {
				t.Errorf("诊断应给出修复办法: %s", dg.Message)
			}
		}
	}
	if !found {
		t.Errorf("缺少 heading_as_body 诊断: %+v", rep.Diagnostics)
	}
}

// TestHeadingAsBodyInAnotherSectionIsAlsoFlagged: the heading may be listed as
// body of a *different* section, which is the more likely shape when a model
// flattens a chapter tree into one section.
func TestHeadingAsBodyInAnotherSectionIsAlsoFlagged(t *testing.T) {
	d := doc(
		h("n0001", "一、甲", 1),
		p("n0002", "正文甲。"),
		h("n0003", "二、乙", 1),
		p("n0004", "正文乙。"),
	)
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001",
				BodyNodeIDs: []string{"n0002"}},
			{ID: "s2", Title: "二、乙", Level: 1, HeadingNodeID: "n0003",
				BodyNodeIDs: []string{"n0004", "n0001"}}, // n0001 as body here
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.HeadingsAsBody != 1 {
		t.Errorf("应检出 1 处标题被当作正文, 实际 %d", rep.HeadingsAsBody)
	}
}

// TestHeadingClaimedAsHeadingIsNotFlagged: the correct modelling must stay
// clean, or the warning becomes noise the user learns to ignore.
func TestHeadingClaimedAsHeadingIsNotFlagged(t *testing.T) {
	d := doc(
		h("n0001", "一、甲", 1),
		p("n0002", "正文。"),
	)
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001",
				BodyNodeIDs: []string{"n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.HeadingsAsBody != 0 {
		t.Errorf("正确建模不应报告 heading_as_body, 实际 %d", rep.HeadingsAsBody)
	}
	if !rep.OK() {
		t.Fatalf("正确建模应通过: %+v", rep.Diagnostics)
	}
}

// TestOrdinaryParagraphAsBodyIsNotFlagged: the rule must key on the node's own
// Kind, not on position, or every section body would be flagged.
func TestOrdinaryParagraphAsBodyIsNotFlagged(t *testing.T) {
	d := doc(p("n0001", "正文一。"), p("n0002", "正文二。"))
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001", "n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	if rep.HeadingsAsBody != 0 {
		t.Errorf("普通段落不应被误报, 实际 %d", rep.HeadingsAsBody)
	}
	if !rep.OK() {
		t.Fatalf("应通过: %+v", rep.Diagnostics)
	}
}

// ---------------------------------------------------------------- verbatim

func TestVerifyVerbatimDetectsAlteration(t *testing.T) {
	d := doc(p("n0001", "原文"))
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, Text: "被改写", NodeID: "n0001"}}}
	diff := VerifyVerbatim(d, out, nil)
	if diff.Clean() {
		t.Fatal("文本被改写必须被检出")
	}
	if len(diff.Altered) != 1 {
		t.Errorf("Altered = %v, 期望 1 项", diff.Altered)
	}
}

func TestVerifyVerbatimDetectsLoss(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "乙"))
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, Text: "甲", NodeID: "n0001"}}}
	diff := VerifyVerbatim(d, out, nil)
	if diff.Clean() {
		t.Fatal("内容丢失必须被检出")
	}
	if len(diff.Missing) != 1 {
		t.Errorf("Missing = %v, 期望 1 项", diff.Missing)
	}
}

// TestVerifyVerbatimAllowsRelocation: a paragraph moved to another chapter
// keeps its text, so the document is still faithful even though the order
// changed.
func TestVerifyVerbatimAllowsRelocation(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "乙"))
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, Text: "乙", NodeID: "n0002"},
		{Role: RoleBody, Text: "甲", NodeID: "n0001"},
	}}
	if diff := VerifyVerbatim(d, out, nil); !diff.Clean() {
		t.Fatalf("仅位置变化不应视为不忠实: %+v", diff)
	}
}

// TestVerifyVerbatimReportsScaffolding: synthesized text is permitted, but
// must be listed so a reviewer can confirm each extra string is scaffolding.
func TestVerifyVerbatimReportsScaffolding(t *testing.T) {
	d := doc(p("n0001", "正文"))
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleMasthead, Text: "某某机关文件"}, // no NodeID → synthesized
		{Role: RoleBody, Text: "正文", NodeID: "n0001"},
	}}
	diff := VerifyVerbatim(d, out, nil)
	if !diff.Clean() {
		t.Fatalf("合成文本不应导致不忠实: %+v", diff)
	}
	if len(diff.Extra) != 1 || diff.Extra[0] != "某某机关文件" {
		t.Errorf("Extra = %v, 期望列出合成文本", diff.Extra)
	}
}

// TestVerbatimAcceptsSplitRuns: the writer may emit a node's text as several
// blocks (e.g. an auto-number run plus the text run); their concatenation is
// what must match.
func TestVerbatimAcceptsSplitRuns(t *testing.T) {
	d := doc(p("n0001", "完整文本"))
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, Text: "完整", NodeID: "n0001"},
		{Role: RoleBody, Text: "文本", NodeID: "n0001"},
	}}
	if diff := VerifyVerbatim(d, out, nil); !diff.Clean() {
		t.Fatalf("拼接后一致应通过: %+v", diff)
	}
}

// TestVerbatimAccountsTableByRows is a correctness fix with a user-visible
// consequence. A table node's Text is deliberately empty — the parser refuses
// to invent a flattened string for it — so a check that only compared Text
// would report every correctly rendered table as lost content. Since this
// function gates export, that would make a valid document unexportable.
func TestVerbatimAccountsTableByRows(t *testing.T) {
	rows := [][]string{{"项目", "数值"}, {"甲", "1"}}
	d := &Document{SourceName: "t.docx", Nodes: []Node{
		{ID: "n0001", Kind: KindTable, Rows: rows, Order: 0}, // Text empty on purpose
	}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Rows: rows},
	}}
	diff := VerifyVerbatim(d, out, nil)
	if !diff.Clean() {
		t.Fatalf("正确渲染的表格不应被判为丢失: missing=%v altered=%v", diff.Missing, diff.Altered)
	}
}

// TestVerbatimDetectsTableAlteration: the table comparison must be real, not
// a rubber stamp — changed cells have to be caught.
func TestVerbatimDetectsTableAlteration(t *testing.T) {
	src := [][]string{{"项目", "数值"}, {"甲", "1"}}
	d := &Document{SourceName: "t.docx", Nodes: []Node{
		{ID: "n0001", Kind: KindTable, Rows: src, Order: 0},
	}}

	changed := [][]string{{"项目", "数值"}, {"甲", "999"}} // a rewritten cell
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, NodeID: "n0001", Rows: changed}}}
	if diff := VerifyVerbatim(d, out, nil); diff.Clean() {
		t.Fatal("表格单元格被改写必须被检出")
	} else if len(diff.Altered) == 0 {
		t.Errorf("应报告 Altered: %+v", diff)
	}

	// A dropped row is also a change, not merely missing text.
	shrunk := [][]string{{"项目", "数值"}}
	out2 := &Doc{Blocks: []OutBlock{{Role: RoleBody, NodeID: "n0001", Rows: shrunk}}}
	if diff := VerifyVerbatim(d, out2, nil); diff.Clean() {
		t.Fatal("表格行数改变必须被检出")
	}
}

// TestVerbatimDetectsTableDropped: a table that never reaches the output is
// lost content even though it carries no text.
func TestVerbatimDetectsTableDropped(t *testing.T) {
	d := &Document{SourceName: "t.docx", Nodes: []Node{
		{ID: "n0001", Kind: KindTable, Rows: [][]string{{"甲"}}, Order: 0},
	}}
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, Text: "别的", NodeID: "n0002"}}}
	diff := VerifyVerbatim(d, out, nil)
	if diff.Clean() {
		t.Fatal("表格被丢弃必须被检出")
	}
	// The message must describe a table rather than quote empty text, or the
	// user sees "n0001: " with nothing after it.
	if len(diff.Missing) != 1 || !strings.Contains(diff.Missing[0], "表格") {
		t.Errorf("Missing 应说明是表格: %v", diff.Missing)
	}
}

// TestVerbatimAccountsImageByBytes: an image's identity is its payload, so a
// figure that vanished must be detected even if its caption text survived.
func TestVerbatimAccountsImageByBytes(t *testing.T) {
	img := &ImageRef{Name: "图1.png", MimeType: "image/png", Data: []byte{1, 2, 3, 4}}
	d := &Document{SourceName: "t.docx", Nodes: []Node{
		{ID: "n0001", Kind: KindImage, Image: img, Text: "图1.png", Order: 0},
	}}

	// Present with the caption but no picture: the figure was lost.
	captionOnly := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "图1.png"},
	}}
	if diff := VerifyVerbatim(d, captionOnly, nil); diff.Clean() {
		t.Fatal("图片数据丢失（只剩题注）必须被检出")
	}

	// Present with the picture: clean.
	withImage := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "图1.png", Image: img},
	}}
	if diff := VerifyVerbatim(d, withImage, nil); !diff.Clean() {
		t.Fatalf("图片与题注都在时应通过: %+v", diff)
	}

	// Picture present with different bytes: altered.
	wrong := &ImageRef{Name: "图1.png", MimeType: "image/png", Data: []byte{9, 9}}
	mismatch := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Image: wrong},
	}}
	if diff := VerifyVerbatim(d, mismatch, nil); diff.Clean() {
		t.Fatal("图片数据被替换必须被检出")
	}
}

// TestVerbatimImageWithoutPayloadPassesOnText: an image node the parser could
// not extract bytes for (a broken relationship) still has its filename as
// text, and emitting that is all the document can do.
func TestVerbatimImageWithoutPayloadPassesOnText(t *testing.T) {
	d := &Document{SourceName: "t.docx", Nodes: []Node{
		{ID: "n0001", Kind: KindImage, Image: &ImageRef{Name: "图1.png"}, Text: "图1.png", Order: 0},
	}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "图1.png"},
	}}
	if diff := VerifyVerbatim(d, out, nil); !diff.Clean() {
		t.Fatalf("无图片字节时按文本记账应通过: %+v", diff)
	}
}

// ---------------------------------------------------------------- helpers

func TestDocumentHelpers(t *testing.T) {
	d := doc(h("n0001", "标题", 1), p("n0002", "正文"), Node{ID: "n0003", Kind: KindPageBreak})

	if got := len(d.ContentNodes()); got != 2 {
		t.Errorf("ContentNodes = %d, 期望 2", got)
	}
	if ids := d.ContentIDs(); len(ids) != 2 || ids[0] != "n0001" {
		t.Errorf("ContentIDs = %v", ids)
	}
	if d.Text("n0002") != "正文" {
		t.Errorf("Text(n0002) = %q", d.Text("n0002"))
	}
	if d.Text("missing") != "" {
		t.Error("未知 id 应返回空串")
	}
	if n, ok := d.Node("n0001"); !ok || n.Level != 1 {
		t.Errorf("Node(n0001) = %+v, %v", n, ok)
	}
	if len(d.ByID()) != 3 {
		t.Errorf("ByID 大小 = %d, 期望 3", len(d.ByID()))
	}
}

func TestHeadingRole(t *testing.T) {
	cases := map[int]Role{
		1: RoleHeading1, 2: RoleHeading2, 3: RoleHeading3, 4: RoleHeading4,
		5: RoleHeading4, 9: RoleHeading4, 0: RoleBody, -1: RoleBody,
	}
	for level, want := range cases {
		if got := HeadingRole(level); got != want {
			t.Errorf("HeadingRole(%d) = %q, 期望 %q", level, got, want)
		}
	}
}

func TestAllRolesCoveredBySpec(t *testing.T) {
	// A duplicated role in AllRoles would let a style silently go unchecked.
	seen := map[Role]bool{}
	for _, r := range AllRoles() {
		if seen[r] {
			t.Errorf("AllRoles 中存在重复项 %q", r)
		}
		seen[r] = true
	}
	if len(seen) < 20 {
		t.Errorf("AllRoles 只定义了 %d 个角色，疑似遗漏", len(seen))
	}
}

func TestPlanWalkAndIDs(t *testing.T) {
	plan := &Plan{
		Sections: []Section{
			{
				ID: "s1", Title: "甲", Level: 1, HeadingNodeID: "n0001",
				BodyNodeIDs: []string{"n0002"},
				Children: []Section{
					{ID: "s1a", Title: "乙", Level: 2, HeadingNodeID: "n0003",
						BodyNodeIDs: []string{"n0004"}},
				},
			},
		},
	}
	var ids []string
	plan.Walk(func(s *Section) bool {
		ids = append(ids, s.ID)
		return true
	})
	if len(ids) != 2 || ids[0] != "s1" || ids[1] != "s1a" {
		t.Errorf("Walk 顺序错误: %v", ids)
	}
	if got := plan.BodyIDs(); len(got) != 2 || got[0] != "n0002" || got[1] != "n0004" {
		t.Errorf("BodyIDs = %v", got)
	}
	if got := plan.HeadingIDs(); len(got) != 2 || got[0] != "n0001" || got[1] != "n0003" {
		t.Errorf("HeadingIDs = %v", got)
	}
}

func TestWalkEarlyStop(t *testing.T) {
	plan := &Plan{
		Sections: []Section{
			{ID: "s1", Level: 1}, {ID: "s2", Level: 1}, {ID: "s3", Level: 1},
		},
	}
	var n int
	plan.Walk(func(s *Section) bool {
		n++
		return n < 2
	})
	if n != 2 {
		t.Errorf("提前终止失败: 访问了 %d 个节点", n)
	}
}

func TestSortNodes(t *testing.T) {
	nodes := []Node{{ID: "c", Order: 2}, {ID: "a", Order: 0}, {ID: "b", Order: 1}}
	SortNodes(nodes)
	if nodes[0].ID != "a" || nodes[1].ID != "b" || nodes[2].ID != "c" {
		t.Errorf("排序错误: %v", nodes)
	}
}

// ---------------------------------------------------------------- 顺序与重复 id

// TestDuplicateSectionIDIsError: section ids are the address every client
// operation uses. A plan with two "s1"s renders fine but makes rename/move
// hit the wrong chapter, so it must be refused — the review screen cannot be
// the place the ambiguity is discovered.
func TestDuplicateSectionIDIsError(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "乙"))
	plan := &Plan{Sections: []Section{
		{ID: "s1", Title: "A", Level: 1, BodyNodeIDs: []string{"n0001"}},
		{ID: "s1", Title: "B", Level: 1, BodyNodeIDs: []string{"n0002"}},
	}}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("章节 id 重复必须导致校验失败")
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "duplicate_section_id" && dg.Severity == SevError {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 duplicate_section_id 诊断: %+v", rep.Diagnostics)
	}
}

// TestDuplicateSectionIDAcrossLevelsIsError is the exact shape the id
// allocation used to produce: a parent and its first child both named "s1".
func TestDuplicateSectionIDAcrossLevelsIsError(t *testing.T) {
	d := doc(p("n0001", "甲"), p("n0002", "乙"))
	plan := &Plan{Sections: []Section{
		{ID: "s1", Title: "父", Level: 1, BodyNodeIDs: []string{"n0001"},
			Children: []Section{
				{ID: "s1", Title: "子", Level: 2, BodyNodeIDs: []string{"n0002"}},
			}},
	}}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("父子同名 id 必须导致校验失败")
	}
}

// TestReorderedSectionsAreFlagged: swapping two whole chapters passes the
// per-section check (each chapter's body is internally ascending) yet the
// printed document shows 二、 before 一、. That must be reported.
func TestReorderedSectionsAreFlagged(t *testing.T) {
	d := doc(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "二、乙", 1), p("n0004", "乙段"),
	)
	plan := &Plan{Sections: []Section{
		{ID: "s1", Title: "二、乙", Level: 1, HeadingNodeID: "n0003", BodyNodeIDs: []string{"n0004"}},
		{ID: "s2", Title: "一、甲", Level: 1, HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
	}}
	rep := VerifyPlan(d, plan)
	// Renderable: the user can reorder deliberately, so this is a warning.
	if !rep.OK() {
		t.Fatalf("章节整体换序应可导出（但需提示）: %+v", rep.Diagnostics)
	}
	if rep.SectionsOutOfOrder != 1 {
		t.Errorf("乱序章节计数 = %d, 期望 1", rep.SectionsOutOfOrder)
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "section_order" && dg.Severity == SevWarning {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少 section_order 警告: %+v", rep.Diagnostics)
	}
}

// TestSectionsInSourceOrderAreQuiet: the correct plan must not trip the check,
// or the warning is noise.
func TestSectionsInSourceOrderAreQuiet(t *testing.T) {
	d := doc(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "二、乙", 1), p("n0004", "乙段"),
	)
	plan := &Plan{Sections: []Section{
		{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
		{ID: "s2", Title: "二、乙", Level: 1, HeadingNodeID: "n0003", BodyNodeIDs: []string{"n0004"}},
	}}
	rep := VerifyPlan(d, plan)
	if rep.SectionsOutOfOrder != 0 {
		t.Errorf("顺序正确不应报告乱序, 实际 %d: %+v", rep.SectionsOutOfOrder, rep.Diagnostics)
	}
}

// TestNestedSectionsInOrderAreQuiet guards against a false positive in the
// depth-first walk: a parent's body precedes its children, and the next
// sibling starts after the children end — not after the parent's own body.
func TestNestedSectionsInOrderAreQuiet(t *testing.T) {
	d := doc(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "（一）乙", 2), p("n0004", "乙段"),
		h("n0005", "二、丙", 1), p("n0006", "丙段"),
	)
	plan := &Plan{Sections: []Section{
		{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001",
			BodyNodeIDs: []string{"n0002"},
			Children: []Section{
				{ID: "s1a", Title: "（一）乙", Level: 2, HeadingNodeID: "n0003",
					BodyNodeIDs: []string{"n0004"}},
			}},
		{ID: "s2", Title: "二、丙", Level: 1, HeadingNodeID: "n0005", BodyNodeIDs: []string{"n0006"}},
	}}
	rep := VerifyPlan(d, plan)
	if rep.SectionsOutOfOrder != 0 {
		t.Errorf("嵌套顺序正确不应报告乱序: %+v", rep.Diagnostics)
	}
}

// TestPreambleAfterFirstSectionIsFlagged: the preamble is emitted under
// 主送机关, i.e. at the front. A node taken from later in the source would be
// moved there silently — coverage is satisfied and comparison is per node, so
// nothing else notices.
func TestPreambleAfterFirstSectionIsFlagged(t *testing.T) {
	d := doc(
		h("n0001", "一、甲", 1), p("n0002", "甲段"), p("n0003", "文末段"),
	)
	plan := &Plan{
		PreambleNodeIDs: []string{"n0003"},
		Sections: []Section{
			{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
		},
	}
	rep := VerifyPlan(d, plan)
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "preamble_out_of_place" {
			found = true
		}
	}
	if !found {
		t.Errorf("前言取自章节之后应被提示: %+v", rep.Diagnostics)
	}
}

// TestPreambleBeforeFirstSectionIsQuiet is the normal shape and must be clean.
func TestPreambleBeforeFirstSectionIsQuiet(t *testing.T) {
	d := doc(
		p("n0001", "缘由段。"),
		h("n0002", "一、甲", 1), p("n0003", "甲段"),
	)
	plan := &Plan{
		PreambleNodeIDs: []string{"n0001"},
		Sections: []Section{
			{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0002", BodyNodeIDs: []string{"n0003"}},
		},
	}
	rep := VerifyPlan(d, plan)
	for _, dg := range rep.Diagnostics {
		if dg.Code == "preamble_out_of_place" {
			t.Errorf("正常前言不应被提示: %s", dg.Message)
		}
	}
}

// TestSynthesizedTitleRepeatingBodyIsError: a paragraph without heading markup
// that the model both synthesized a section title from AND left in the body
// prints twice. Coverage is complete, so only this check sees it.
func TestSynthesizedTitleRepeatingBodyIsError(t *testing.T) {
	d := doc(
		p("n0001", "一、总体要求"),
		p("n0002", "正文。"),
	)
	plan := &Plan{Sections: []Section{
		{ID: "s1", Title: "一、总体要求", Level: 1, BodyNodeIDs: []string{"n0001", "n0002"}},
	}}
	rep := VerifyPlan(d, plan)
	if rep.OK() {
		t.Fatal("自拟标题与正文同文必须导致失败")
	}
	var found bool
	for _, dg := range rep.Diagnostics {
		if dg.Code == "duplicate_title" && strings.Contains(dg.Message, "n0001") {
			found = true
			if !strings.Contains(dg.Message, "heading_node_id") {
				t.Errorf("诊断应给出修复办法: %s", dg.Message)
			}
		}
	}
	if !found {
		t.Errorf("缺少 duplicate_title 诊断: %+v", rep.Diagnostics)
	}
}

// TestSynthesizedTitleMatchingHeadingIsQuiet: when the paragraph IS bound as
// the heading, the title legitimately equals it. The line prints once.
func TestSynthesizedTitleMatchingHeadingIsQuiet(t *testing.T) {
	d := doc(h("n0001", "一、总体要求", 1), p("n0002", "正文。"))
	plan := &Plan{Sections: []Section{
		{ID: "s1", Title: "一、总体要求", Level: 1,
			HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
	}}
	rep := VerifyPlan(d, plan)
	if !rep.OK() {
		t.Fatalf("标题绑定到原文节点时应通过: %+v", rep.Diagnostics)
	}
	for _, dg := range rep.Diagnostics {
		if dg.Code == "duplicate_title" {
			t.Errorf("不应报告 duplicate_title: %s", dg.Message)
		}
	}
}
