package docmodel

import (
	"strings"
	"testing"
)

// TestSynthesizedHeadingIsTheWayToAddOne documents the supported path for
// "原文缺少标题": a Section with a Title and no HeadingNodeID. Generating a
// heading is structural, not a text substitution, and it is counted so the user
// can review it.
func TestSynthesizedHeadingIsTheWayToAddOne(t *testing.T) {
	d := doc(p("n0001", "正文内容。"))
	plan := &Plan{Sections: []Section{{
		ID: "s1", Title: "一、总体要求", Level: 1,
		BodyNodeIDs: []string{"n0001"},
	}}}

	rep := VerifyPlan(d, plan)
	if !rep.OK() {
		t.Fatalf("无 heading_node_id 的章节应合法（它就是要生成标题）: %+v", rep.Diagnostics)
	}
	// And it must be flagged as model-authored, so the user knows the heading
	// did not come from their document.
	if rep.SynthesizedHeadings != 1 {
		t.Errorf("应记为 1 个模型生成标题, 实际 %d", rep.SynthesizedHeadings)
	}
}

// TestEditWithoutNodeIsReportedNotIgnored is a regression guard for a real
// silent-loss defect: an edit with an empty NodeID was skipped outright, so a
// model asking for a heading got no heading, no change-log entry and no
// diagnostic. Edits replace text *within* a node, so this can never be applied
// — but "cannot be applied" must be said, not swallowed.
func TestEditWithoutNodeIsReportedNotIgnored(t *testing.T) {
	d := doc(p("n0001", "正文内容。"))
	plan := &Plan{Edits: []Edit{{
		NodeID: "", Kind: EditHeading,
		After: "一、总体要求", Reason: "原文缺少标题",
	}}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "正文内容。"},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if !diff.Clean() {
		t.Fatalf("无目标节点的改动不会损坏文档: %+v", diff.Altered)
	}
	if len(diff.Unapplied) != 1 {
		t.Fatalf("必须报告为未生效，不能静默丢弃: %+v", diff.Unapplied)
	}
	u := diff.Unapplied[0]
	if u.Kind != string(EditHeading) {
		t.Errorf("应保留改动类型: %q", u.Kind)
	}
	// The diagnostic must be actionable, not merely honest: it should name the
	// mechanism that does work.
	for _, must := range []string{"node_id", "title", "heading_node_id"} {
		if !strings.Contains(u.Reason, must) {
			t.Errorf("未生效原因应说明可行做法（缺 %q）: %q", must, u.Reason)
		}
	}
}

// TestEditWithUnknownNodeStillReported confirms the pre-existing behaviour is
// intact alongside the new empty-node case, and that the two are distinguished
// so the user knows which mistake to fix.
func TestEditWithUnknownNodeStillReported(t *testing.T) {
	d := doc(p("n0001", "正文。"))
	plan := &Plan{Edits: []Edit{
		{NodeID: "", Kind: EditTypo, After: "x"},
		{NodeID: "n9999", Kind: EditTypo, Before: "甲", After: "乙"},
	}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "正文。"},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if len(diff.Unapplied) != 2 {
		t.Fatalf("两种无法应用的改动都应被报告, 实际 %d: %+v", len(diff.Unapplied), diff.Unapplied)
	}
	reasons := diff.Unapplied[0].Reason + " " + diff.Unapplied[1].Reason
	if !strings.Contains(reasons, "node_id 为空") {
		t.Errorf("应区分「未指定目标」: %q", reasons)
	}
	if !strings.Contains(reasons, "节点不存在") {
		t.Errorf("应区分「目标节点不存在」: %q", reasons)
	}
}

// TestSentenceSplitEditApplies covers 断句, one of the four corrections the user
// asked for: splitting one long sentence into two is expressed as replacing the
// long span with the split version.
func TestSentenceSplitEditApplies(t *testing.T) {
	src := "现就有关事项通知如下各单位要高度重视认真抓好贯彻落实确保取得实效"
	d := doc(p("n0001", src))
	plan := &Plan{Edits: []Edit{{
		NodeID: "n0001", Kind: EditSentenceSplit,
		Before: "通知如下各单位要高度重视",
		After:  "通知如下。各单位要高度重视",
		Reason: "长句无标点，建议断句。",
	}}}

	applied, failed := AppliedText(src, plan.Edits)
	if len(failed) != 0 {
		t.Fatalf("断句改动应能应用: %+v", failed)
	}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: applied, Edited: true},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if !diff.Clean() {
		t.Fatalf("已声明的断句不应失败: %+v", diff.Altered)
	}
	if len(diff.Changed) != 1 {
		t.Fatalf("应记录 1 条断句改动: %+v", diff.Changed)
	}
	if diff.Changed[0].Label != "断句" {
		t.Errorf("标签应为「断句」, 实际 %q", diff.Changed[0].Label)
	}
}

// TestEveryEditKindCanBeApplied walks the whole enum, so a kind added later
// cannot end up declared but unusable.
func TestEveryEditKindCanBeApplied(t *testing.T) {
	src := "原文甲部分乙部分"
	for _, kind := range AllEditKinds() {
		plan := &Plan{Edits: []Edit{{
			NodeID: "n0001", Kind: kind,
			Before: "甲", After: "丙", Reason: "测试",
		}}}
		applied, failed := AppliedText(src, plan.Edits)
		if len(failed) != 0 {
			t.Errorf("类型 %q 的改动无法应用: %+v", kind, failed)
			continue
		}
		if !strings.Contains(applied, "丙") {
			t.Errorf("类型 %q 的改动未生效: %q", kind, applied)
		}

		d := doc(p("n0001", src))
		out := &Doc{Blocks: []OutBlock{
			{Role: RoleBody, NodeID: "n0001", Text: applied, Edited: true},
		}}
		diff := VerifyVerbatim(d, out, plan)
		if !diff.Clean() {
			t.Errorf("类型 %q 的已声明改动不应失败: %+v", kind, diff.Altered)
		}
		if len(diff.Changed) != 1 {
			t.Errorf("类型 %q 应记入 1 条改动, 实际 %d", kind, len(diff.Changed))
		}
		if got := diff.Changed[0].Label; got != EditLabel(kind) {
			t.Errorf("类型 %q 的标签 = %q, 期望 %q", kind, got, EditLabel(kind))
		}
	}
}
