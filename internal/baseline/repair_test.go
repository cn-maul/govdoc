package baseline

import (
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// TestRepairFixesHeadingMismatch: the correct title is in the document, so a
// paraphrased one is repairable without asking the model again.
func TestRepairFixesHeadingMismatch(t *testing.T) {
	doc := docOf(h("n0001", "一、总体要求", 1), p("n0002", "正文。"))
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、总体 要求", Level: 1, HeadingNodeID: "n0001",
			BodyNodeIDs: []string{"n0002"}},
	}}
	if docmodel.VerifyPlan(doc, plan).OK() {
		t.Fatal("测试前提：标题不一致应不合格")
	}

	fixed, fixes := Repair(doc, plan)
	if len(fixes) == 0 {
		t.Fatal("应报告修正内容")
	}
	rep := docmodel.VerifyPlan(doc, fixed)
	if !rep.OK() {
		t.Fatalf("修正后应通过: %+v", rep.Diagnostics)
	}
	if plan.Sections[0].Title != "一、总体 要求" {
		t.Error("原始方案不应被就地修改")
	}

	// A changed section title is a proposed content change. It must reach the
	// review list, otherwise the user can neither accept nor reject it.
	if len(fixed.Edits) != 1 {
		t.Fatalf("标题改动应成为一条校对项, 实际 %+v", fixed.Edits)
	}
	e := fixed.Edits[0]
	if e.NodeID != "n0001" || e.Kind != docmodel.EditHeading {
		t.Errorf("校对项指向错误: %+v", e)
	}
	if e.Before != "一、总体要求" || e.After != "一、总体 要求" {
		t.Errorf("校对项前后文字错误: %q → %q", e.Before, e.After)
	}
	if e.Reason == "" {
		t.Error("校对项应说明理由")
	}
	if fixed.Sections[0].Title != "一、总体要求" {
		t.Errorf("方案标题应改回原文, 实际 %q", fixed.Sections[0].Title)
	}
}

// TestRepairBindsHeadingLeftInBody: the model names a section after a source
// heading but leaves that heading in body_node_ids. Printing both produces two
// identical heading lines; the source node is the authoritative one.
func TestRepairBindsHeadingLeftInBody(t *testing.T) {
	doc := docOf(
		h("n0001", "一、项目概况", 1),
		p("n0002", "正文。"),
	)
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、项目概况", Level: 1,
			BodyNodeIDs: []string{"n0001", "n0002"}},
	}}
	fixed, fixes := Repair(doc, plan)
	if len(fixes) == 0 {
		t.Fatal("应报告标题绑定修正")
	}
	if fixed.Sections[0].HeadingNodeID != "n0001" {
		t.Errorf("heading_node_id = %q, 期望 n0001", fixed.Sections[0].HeadingNodeID)
	}
	if got := fixed.Sections[0].BodyNodeIDs; len(got) != 1 || got[0] != "n0002" {
		t.Errorf("原标题节点应从正文移除, 实际 %v", got)
	}
	if rep := docmodel.VerifyPlan(doc, fixed); !rep.OK() {
		t.Fatalf("修正后应通过: %+v", rep.Diagnostics)
	}
}

// TestRepairDoesNotInventHeadingBinding: a title the source does not contain
// must stay synthesized rather than being attached to unrelated text.
func TestRepairDoesNotInventHeadingBinding(t *testing.T) {
	doc := docOf(h("n0001", "一、项目概况", 1), p("n0002", "正文。"))
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "工作要求", Level: 1,
			BodyNodeIDs: []string{"n0001", "n0002"}},
	}}
	fixed, _ := Repair(doc, plan)
	if fixed.Sections[0].HeadingNodeID != "" {
		t.Errorf("部分匹配不应绑定标题节点, 实际 %q", fixed.Sections[0].HeadingNodeID)
	}
}

// A title that differs only by its 层次序数 is the source heading, not a
// rewrite. Binding it suppresses the duplicate and produces no review item,
// because the user did not ask for a wording change.
func TestRepairBindsHeadingWithoutNumberAsEquivalent(t *testing.T) {
	doc := docOf(h("n0001", "一、项目概况", 1), p("n0002", "正文。"))
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "项目概况", Level: 1,
			BodyNodeIDs: []string{"n0001", "n0002"}},
	}}
	fixed, fixes := Repair(doc, plan)
	if len(fixes) == 0 {
		t.Fatal("应报告标题重复的修正")
	}
	s := fixed.Sections[0]
	if s.HeadingNodeID != "n0001" || s.Title != "一、项目概况" {
		t.Errorf("应绑定原文标题, 实际 heading=%q title=%q", s.HeadingNodeID, s.Title)
	}
	if len(fixed.Edits) != 0 {
		t.Errorf("仅序数不同的标题不应产生校对项: %+v", fixed.Edits)
	}
}

// TestRepairTurnsRenamedHeadingIntoReviewItem: the model rewrote the heading
// and left the source heading at the top of the body. The plan verifies — the
// duplicate is only a warning — so this defect used to reach the exported file:
// the page showed the model's title and then the author's heading. The source
// heading must win and the rewrite must become a reviewable change.
func TestRepairTurnsRenamedHeadingIntoReviewItem(t *testing.T) {
	doc := docOf(
		h("n0001", "一、项目概况", 1),
		p("n0002", "正文。"),
	)
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、项目基本情况", Level: 1,
			BodyNodeIDs: []string{"n0001", "n0002"}},
	}}
	if rep := docmodel.VerifyPlan(doc, plan); !rep.OK() {
		t.Fatalf("测试前提：该方案只应有警告, 实际 %+v", rep.Diagnostics)
	}

	fixed, fixes := Repair(doc, plan)
	if len(fixes) == 0 {
		t.Fatal("应报告标题重复的修正")
	}
	s := fixed.Sections[0]
	if s.HeadingNodeID != "n0001" {
		t.Errorf("应绑定原文标题节点, 实际 %q", s.HeadingNodeID)
	}
	if s.Title != "一、项目概况" {
		t.Errorf("方案标题应回到原文, 实际 %q", s.Title)
	}
	if got := s.BodyNodeIDs; len(got) != 1 || got[0] != "n0002" {
		t.Errorf("原标题节点应从正文移除, 实际 %v", got)
	}
	if len(fixed.Edits) != 1 {
		t.Fatalf("标题改写应成为校对项, 实际 %+v", fixed.Edits)
	}
	e := fixed.Edits[0]
	if e.NodeID != "n0001" || e.Kind != docmodel.EditHeading {
		t.Errorf("校对项指向错误: %+v", e)
	}
	if e.Before != "一、项目概况" || e.After != "一、项目基本情况" {
		t.Errorf("校对项前后文字错误: %q → %q", e.Before, e.After)
	}
	if rep := docmodel.VerifyPlan(doc, fixed); !rep.OK() {
		t.Fatalf("修正后应通过: %+v", rep.Diagnostics)
	}
	if rep := docmodel.VerifyPlan(doc, fixed); rep.SynthesizedHeadings != 0 {
		t.Errorf("不应留下模型自拟标题, 实际 %d 个", rep.SynthesizedHeadings)
	}
}

// TestRepairDoesNotPromoteDeeperHeadingToParentTitle: a synthesized parent
// whose body starts with a real *child* heading is a missing chapter, not a
// retitled one. Binding the child would move a level-2 heading into level 1.
func TestRepairDoesNotPromoteDeeperHeadingToParentTitle(t *testing.T) {
	doc := docOf(
		h("n0001", "（一）主要规格参数", 2),
		p("n0002", "参数正文。"),
	)
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "二、技术要求", Level: 1,
			BodyNodeIDs: []string{"n0001", "n0002"}},
	}}
	fixed, _ := Repair(doc, plan)
	if fixed.Sections[0].HeadingNodeID != "" {
		t.Errorf("层级不同的标题不应被提升为父章节标题, 实际 %q",
			fixed.Sections[0].HeadingNodeID)
	}
	if len(fixed.Edits) != 0 {
		t.Errorf("不应凭空产生标题校对项: %+v", fixed.Edits)
	}
}

// TestRepairLeavesHeadingAlreadyBoundAlone: a correct plan must not be
// rewritten just because repair now runs on valid plans too.
func TestRepairLeavesHeadingAlreadyBoundAlone(t *testing.T) {
	doc := docOf(h("n0001", "一、项目概况", 1), p("n0002", "正文。"))
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、项目概况", Level: 1,
			HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
	}}
	fixed, fixes := Repair(doc, plan)
	if len(fixes) != 0 {
		t.Errorf("正确方案不应被修改: %v", fixes)
	}
	if len(fixed.Edits) != 0 {
		t.Errorf("正确方案不应产生校对项: %+v", fixed.Edits)
	}
}

// TestRepairDeepensMismatchedChild: the tree already says the child is a
// child; only the number is wrong.
func TestRepairDeepensMismatchedChild(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "（一）乙", 2), p("n0004", "乙段"),
	)
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001",
			BodyNodeIDs: []string{"n0002"},
			Children: []docmodel.Section{
				{ID: "s2", Title: "（一）乙", Level: 1, HeadingNodeID: "n0003",
					BodyNodeIDs: []string{"n0004"}},
			}},
	}}
	fixed, fixes := Repair(doc, plan)
	if len(fixes) == 0 {
		t.Fatal("应报告层级修正")
	}
	if !docmodel.VerifyPlan(doc, fixed).OK() {
		t.Fatalf("修正后应通过")
	}
	if fixed.Sections[0].Children[0].Level != 2 {
		t.Errorf("子级 = %d, 期望 2", fixed.Sections[0].Children[0].Level)
	}
}

// TestRepairDropsDuplicateClaims: keeping the first claim means the document
// does not move, which is what the model intended when it listed it first.
func TestRepairDropsDuplicateClaims(t *testing.T) {
	doc := docOf(p("n0001", "甲"), p("n0002", "乙"))
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "A", Level: 1, BodyNodeIDs: []string{"n0001", "n0002"}},
		{ID: "s2", Title: "B", Level: 1, BodyNodeIDs: []string{"n0002"}},
	}}
	fixed, fixes := Repair(doc, plan)
	if len(fixes) == 0 {
		t.Fatal("应报告去重")
	}
	if !docmodel.VerifyPlan(doc, fixed).OK() {
		t.Fatalf("去重后应通过: %+v", docmodel.VerifyPlan(doc, fixed).Diagnostics)
	}
	if len(fixed.Sections[0].BodyNodeIDs) != 2 {
		t.Error("第一次出现的那处应保留")
	}
	if len(fixed.Sections[1].BodyNodeIDs) != 0 {
		t.Error("重复的那处应被移除")
	}
}

// TestRepairFillsMissingNodes: a forgotten paragraph goes where the source
// puts it — the same rule Build uses.
func TestRepairFillsMissingNodes(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "二、乙", 1), p("n0004", "乙段"),
	)
	// The model forgot n0004 entirely.
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001",
			BodyNodeIDs: []string{"n0002"}},
		{ID: "s2", Title: "二、乙", Level: 1, HeadingNodeID: "n0003",
			BodyNodeIDs: []string{}},
	}}
	fixed, fixes := Repair(doc, plan)
	if len(fixes) == 0 {
		t.Fatal("应报告补收")
	}
	if !docmodel.VerifyPlan(doc, fixed).OK() {
		t.Fatalf("补收后应通过: %+v", docmodel.VerifyPlan(doc, fixed).Diagnostics)
	}
	if got := fixed.Sections[1].BodyNodeIDs; len(got) != 1 || got[0] != "n0004" {
		t.Errorf("n0004 应进入第二章, 实际 %v", got)
	}
}

// TestRepairPutsLeadingOrphanInPreamble: a forgotten paragraph before any
// heading belongs to 前言, not to a chapter invented for it.
func TestRepairPutsLeadingOrphanInPreamble(t *testing.T) {
	doc := docOf(
		p("n0001", "为贯彻落实要求。"),
		h("n0002", "一、甲", 1), p("n0003", "甲段"),
	)
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0002",
			BodyNodeIDs: []string{"n0003"}},
	}}
	fixed, fixes := Repair(doc, plan)
	if len(fixes) == 0 {
		t.Fatal("应报告补收")
	}
	if len(fixed.PreambleNodeIDs) != 1 || fixed.PreambleNodeIDs[0] != "n0001" {
		t.Errorf("前言 = %v", fixed.PreambleNodeIDs)
	}
	if !docmodel.VerifyPlan(doc, fixed).OK() {
		t.Errorf("补收后应通过")
	}
}

// TestRepairLeavesGoodPlanAlone: a plan that already verifies must come back
// untouched — and the same pointer, so the caller can tell nothing happened.
func TestRepairLeavesGoodPlanAlone(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "二、乙", 1), p("n0004", "乙段"),
	)
	plan := Build(doc)
	fixed, fixes := Repair(doc, plan)
	if len(fixes) != 0 {
		t.Fatalf("通过的方案不应被修改: %v", fixes)
	}
	if fixed != plan {
		t.Error("未修改时应返回同一个方案")
	}
}

// TestRepairNeverInventsChapters: repair may not create a section, because a
// section with no heading node prints a title the author never wrote.
func TestRepairNeverInventsChapters(t *testing.T) {
	doc := docOf(h("n0001", "一、甲", 1), p("n0002", "甲段"), p("n0003", "散落段"))
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001",
			BodyNodeIDs: []string{"n0002"}},
	}}
	fixed, _ := Repair(doc, plan)
	if len(sections(fixed)) != 1 {
		t.Errorf("不应新增章节, 实际 %d", len(sections(fixed)))
	}
	if got := fixed.Sections[0].BodyNodeIDs; len(got) != 2 {
		t.Errorf("散落段应补进已有章节, 实际 %v", got)
	}
}

// TestRepairReportsEveryFix: a repair the user cannot see is exactly the kind
// of silent change the project forbids.
func TestRepairReportsEveryFix(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"), p("n0003", "乙段"),
	)
	plan := &docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "改过的标题", Level: 1, HeadingNodeID: "n0001",
			BodyNodeIDs: []string{"n0002", "n0002"}},
	}}
	fixed, fixes := Repair(doc, plan)
	if len(fixes) < 3 {
		t.Fatalf("应有 3 处修正（标题/去重/补收），实际 %d: %v", len(fixes), fixes)
	}
	for _, f := range fixes {
		if strings.TrimSpace(f) == "" {
			t.Error("修正说明不能为空")
		}
	}
	if !docmodel.VerifyPlan(doc, fixed).OK() {
		t.Errorf("修正后应通过: %+v", docmodel.VerifyPlan(doc, fixed).Diagnostics)
	}
}
