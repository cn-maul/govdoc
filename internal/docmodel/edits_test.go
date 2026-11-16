package docmodel

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------- AppliedText

// TestAppliedTextAppliesDeclaredEdit is the basic mechanism behind allowing
// content corrections: a declared Before is replaced by its After.
func TestAppliedTextAppliesDeclaredEdit(t *testing.T) {
	src := "我们要含辛如苦地推进这项工作。"
	edits := []Edit{{
		NodeID: "n0001", Kind: EditTypo,
		Before: "含辛如苦", After: "含辛茹苦",
		Reason: "成语误写",
	}}
	got, failed := AppliedText(src, edits)
	if len(failed) != 0 {
		t.Fatalf("不应有失败项: %+v", failed)
	}
	if want := "我们要含辛茹苦地推进这项工作。"; got != want {
		t.Errorf("AppliedText = %q, 期望 %q", got, want)
	}
}

// TestAppliedTextLeavesTextAloneWithoutEdits: no declaration means no change,
// which is what keeps the old guarantee intact for documents that need none.
func TestAppliedTextLeavesTextAloneWithoutEdits(t *testing.T) {
	src := "原文一字不动。"
	got, failed := AppliedText(src, nil)
	if got != src {
		t.Errorf("无改动时文本应保持不变: %q", got)
	}
	if len(failed) != 0 {
		t.Errorf("不应有失败项: %+v", failed)
	}
}

// TestAppliedTextReportsMissingBefore: an edit whose Before is not in the text
// must be reported, not silently skipped — "asked to fix something that is not
// there" is information the user needs.
func TestAppliedTextReportsMissingBefore(t *testing.T) {
	src := "这段文字里没有那个错字。"
	edits := []Edit{{
		NodeID: "n0001", Kind: EditTypo,
		Before: "子虚乌有", After: "改正",
	}}
	got, failed := AppliedText(src, edits)
	if got != src {
		t.Errorf("找不到时不改动文本, 实际 %q", got)
	}
	if len(failed) != 1 {
		t.Fatalf("应报告 1 项未生效, 实际 %d", len(failed))
	}
	if failed[0].Before != "子虚乌有" {
		t.Errorf("失败项应保留 Before: %+v", failed[0])
	}
}

// TestAppliedTextHonoursOccurrence: when Before appears more than once, the
// declared occurrence is the one replaced — otherwise an edit could silently
// land on the wrong sentence.
func TestAppliedTextHonoursOccurrence(t *testing.T) {
	src := "第一处的的用法，第二处的的用法。"
	// Replace the second "的的".
	got, failed := AppliedText(src, []Edit{{
		NodeID: "n0001", Kind: EditPunctuation,
		Before: "的的", After: "的", Occurrence: 1,
	}})
	if len(failed) != 0 {
		t.Fatalf("不应失败: %+v", failed)
	}
	if want := "第一处的的用法，第二处的用法。"; got != want {
		t.Errorf("AppliedText = %q, 期望 %q", got, want)
	}
}

// TestAppliedTextDefaultOccurrenceIsFirst documents the zero value's meaning.
func TestAppliedTextDefaultOccurrenceIsFirst(t *testing.T) {
	src := "甲甲"
	got, _ := AppliedText(src, []Edit{{Before: "甲", After: "乙"}})
	if want := "乙甲"; got != want {
		t.Errorf("默认应替换第一处: %q, 期望 %q", got, want)
	}
}

// TestAppliedTextAppliesMultipleEditsInOrder: several corrections to one
// paragraph must all land, and each must find its Before in the text as
// produced so far.
func TestAppliedTextAppliesMultipleEditsInOrder(t *testing.T) {
	src := "按排好工做，认真负则。"
	edits := []Edit{
		{NodeID: "n1", Kind: EditTypo, Before: "按排", After: "安排"},
		{NodeID: "n1", Kind: EditTypo, Before: "工做", After: "工作"},
		{NodeID: "n1", Kind: EditTypo, Before: "负则", After: "负责"},
	}
	got, failed := AppliedText(src, edits)
	if len(failed) != 0 {
		t.Fatalf("不应失败: %+v", failed)
	}
	if want := "安排好工作，认真负责。"; got != want {
		t.Errorf("AppliedText = %q, 期望 %q", got, want)
	}
}

// TestAppliedTextEmptyBeforeIsReportedRatherThanGuessed: an empty Before would
// otherwise mean "insert at position 0", which is not what an edit means.
func TestAppliedTextEmptyBeforeIsReportedRatherThanGuessed(t *testing.T) {
	src := "内容"
	got, failed := AppliedText(src, []Edit{{Before: "", After: "新内容"}})
	if got != src {
		t.Errorf("空 before 不应改动文本, 实际 %q", got)
	}
	if len(failed) != 1 {
		t.Fatalf("空 before 应被报告为未生效, 实际 %d", len(failed))
	}
}

// TestAppliedTextDeletionAndInsertion: After may be empty (delete) and Before
// may be a short marker (insert), and both must work.
func TestAppliedTextDeletionAndInsertion(t *testing.T) {
	// Deletion: remove a duplicated word.
	src := "这项工作要要做好。"
	got, _ := AppliedText(src, []Edit{{Before: "要要", After: "要"}})
	if want := "这项工作要做好。"; got != want {
		t.Errorf("删除式改动失败: %q", got)
	}

	// Insertion: add a missing punctuation mark by replacing its neighbour.
	src = "现通知如下"
	got, _ = AppliedText(src, []Edit{{Before: "如下", After: "如下："}})
	if want := "现通知如下："; got != want {
		t.Errorf("插入式改动失败: %q", got)
	}
}

// TestAppliedTextOverlappingCandidates: short Before values can overlap, and
// the nth-occurrence walk must still find them.
func TestAppliedTextOverlappingCandidates(t *testing.T) {
	src := "啊啊啊"
	// Occurrence 1 of "啊啊" starts at index 1.
	got, failed := AppliedText(src, []Edit{{Before: "啊啊", After: "X", Occurrence: 1}})
	if len(failed) != 0 {
		t.Fatalf("不应失败: %+v", failed)
	}
	if want := "啊X"; got != want {
		t.Errorf("AppliedText = %q, 期望 %q", got, want)
	}
}

// TestAppliedTextPreservesEverythingElse: an edit must change only the declared
// span. This is the property that makes "modified" different from "rewritten".
func TestAppliedTextPreservesEverythingElse(t *testing.T) {
	// Awkward characters either side of the edit: quotes, full-width space,
	// ellipsis, an ASCII space run.
	src := "“前面”有\u3000全角空格……和  ASCII  空格，错别字在这里，后面“也有”。"
	got, _ := AppliedText(src, []Edit{{Before: "错别字", After: "正确字"}})

	if want := strings.Replace(src, "错别字", "正确字", 1); got != want {
		t.Errorf("只应改动声明的片段:\n得到 %q\n期望 %q", got, want)
	}
	// The surrounding rarities must survive byte-for-byte.
	for _, must := range []string{"“前面”", "\u3000", "……", "和  ASCII  空格", "“也有”"} {
		if !strings.Contains(got, must) {
			t.Errorf("改动波及了未声明的文本 %q: %q", must, got)
		}
	}
}

// ---------------------------------------------------------------- verify

// TestVerifyVerbatimAcceptsDeclaredEdit is the heart of the relaxed rule: a
// declared change is not a failure, and it shows up in the change log.
func TestVerifyVerbatimAcceptsDeclaredEdit(t *testing.T) {
	d := doc(p("n0001", "含辛如苦地推进。"))
	plan := &Plan{Edits: []Edit{{
		NodeID: "n0001", Kind: EditTypo, Before: "含辛如苦", After: "含辛茹苦",
		Reason: "成语误写",
	}}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "含辛茹苦地推进。", Edited: true},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if !diff.Clean() {
		t.Fatalf("已声明的改动不应判为失败: altered=%v", diff.Altered)
	}
	if len(diff.Changed) != 1 {
		t.Fatalf("应记录 1 条改动, 实际 %d: %+v", len(diff.Changed), diff.Changed)
	}
	c := diff.Changed[0]
	if c.NodeID != "n0001" || c.Before != "含辛如苦" || c.After != "含辛茹苦" {
		t.Errorf("改动记录内容不符: %+v", c)
	}
	if c.Label != "错别字" {
		t.Errorf("改动记录应带中文标签, 实际 %q", c.Label)
	}
	if c.Reason != "成语误写" {
		t.Errorf("改动记录应保留理由, 实际 %q", c.Reason)
	}
}

// TestVerifyVerbatimRejectsUndeclaredChange is the guarantee that survives the
// relaxation: a change nobody declared is still an error. Without this the
// tool could rewrite a document behind the user's back.
func TestVerifyVerbatimRejectsUndeclaredChange(t *testing.T) {
	d := doc(p("n0001", "含辛如苦地推进。"))
	// No edits declared, but the output differs.
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "含辛茹苦地推进。"},
	}}

	diff := VerifyVerbatim(d, out, &Plan{})
	if diff.Clean() {
		t.Fatal("未声明的修改必须导致失败")
	}
	if len(diff.Altered) != 1 {
		t.Fatalf("应报告 1 处未声明修改: %+v", diff.Altered)
	}
	if !strings.Contains(diff.Altered[0], "未声明") {
		t.Errorf("诊断应指明是未声明的修改: %q", diff.Altered[0])
	}
}

// TestVerifyVerbatimRejectsPartiallyDeclaredChange: declaring one edit does not
// license others. This is the case a careless implementation gets wrong —
// comparing against "the source with edits applied" is what makes it correct.
func TestVerifyVerbatimRejectsPartiallyDeclaredChange(t *testing.T) {
	d := doc(p("n0001", "含辛如苦地推进这项工作。"))
	plan := &Plan{Edits: []Edit{{
		NodeID: "n0001", Kind: EditTypo, Before: "含辛如苦", After: "含辛茹苦",
	}}}
	// The declared fix is applied, PLUS an extra undeclared change.
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "含辛茹苦地推进那项工作。", Edited: true},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if diff.Clean() {
		t.Fatal("在已声明改动之外多改的部分必须导致失败")
	}
}

// TestVerifyVerbatimDetectsDeclaredEditNotApplied: if the plan declared a
// change but the output still shows the original text, the edit was lost —
// silent loss of an edit is worth reporting.
func TestVerifyVerbatimDetectsDeclaredEditNotApplied(t *testing.T) {
	d := doc(p("n0001", "含辛如苦地推进。"))
	plan := &Plan{Edits: []Edit{{
		NodeID: "n0001", Kind: EditTypo, Before: "含辛如苦", After: "含辛茹苦",
	}}}
	// Output has the ORIGINAL text.
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "含辛如苦地推进。"},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if diff.Clean() {
		t.Fatal("声明了改动但输出未改，应被检出")
	}
}

// TestVerifyVerbatimReportsUnappliedEdit: a declared Before that does not occur
// means the edit silently did nothing. The document itself is fine (its text is
// the source text), so it is reported as unapplied rather than as a corruption.
func TestVerifyVerbatimReportsUnappliedEdit(t *testing.T) {
	d := doc(p("n0001", "这段里没有那个错字。"))
	plan := &Plan{Edits: []Edit{{
		NodeID: "n0001", Kind: EditTypo, Before: "不存在的错字", After: "改正",
	}}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "这段里没有那个错字。"},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if !diff.Clean() {
		t.Fatalf("未生效的改动不会损坏文档，不应判为失败: %+v", diff.Altered)
	}
	if len(diff.Unapplied) != 1 {
		t.Fatalf("应报告 1 项未生效改动, 实际 %d: %+v", len(diff.Unapplied), diff.Unapplied)
	}
	if !strings.Contains(diff.Unapplied[0].Reason, "未") {
		t.Errorf("未生效项应说明原因: %+v", diff.Unapplied[0])
	}
}

// TestVerifyVerbatimReportsEditOnUnknownNode: an edit pointing at a node that
// does not exist would otherwise be reported nowhere at all.
func TestVerifyVerbatimReportsEditOnUnknownNode(t *testing.T) {
	d := doc(p("n0001", "正文。"))
	plan := &Plan{Edits: []Edit{{
		NodeID: "n9999", Kind: EditTypo, Before: "甲", After: "乙",
	}}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "正文。"},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if len(diff.Unapplied) != 1 {
		t.Fatalf("指向不存在节点的改动应被报告: %+v", diff.Unapplied)
	}
	if !strings.Contains(diff.Unapplied[0].Reason, "节点不存在") {
		t.Errorf("原因应说明节点不存在: %q", diff.Unapplied[0].Reason)
	}
}

// TestVerifyVerbatimNilPlanIsSafe: callers without a plan (a plain verbatim
// check) pass nil, and must not crash.
func TestVerifyVerbatimNilPlanIsSafe(t *testing.T) {
	d := doc(p("n0001", "原文。"))
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, NodeID: "n0001", Text: "原文。"}}}

	if diff := VerifyVerbatim(d, out, nil); !diff.Clean() {
		t.Fatalf("nil plan 应视为无改动且通过: %+v", diff)
	}
	// And an undeclared change is still caught with a nil plan.
	out.Blocks[0].Text = "被改了。"
	if diff := VerifyVerbatim(d, out, nil); diff.Clean() {
		t.Fatal("nil plan 下未声明的修改仍应被检出")
	}
}

// TestVerifyVerbatimDoesNotLogNoOpEdits: an edit whose After equals its Before
// changes nothing, so listing it in the change log would overstate what
// happened.
func TestVerifyVerbatimDoesNotLogNoOpEdits(t *testing.T) {
	d := doc(p("n0001", "没有变化。"))
	plan := &Plan{Edits: []Edit{{
		NodeID: "n0001", Kind: EditOther, Before: "没有", After: "没有",
	}}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "没有变化。"},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if !diff.Clean() {
		t.Fatalf("无实际变化的改动不应判为失败: %+v", diff.Altered)
	}
	if len(diff.Changed) != 0 {
		t.Errorf("After == Before 的改动不应进入改动清单: %+v", diff.Changed)
	}
}

// TestEditHelpers covers the enumeration and labels the UI relies on.
func TestEditHelpers(t *testing.T) {
	kinds := AllEditKinds()
	if len(kinds) == 0 {
		t.Fatal("AllEditKinds 不应为空")
	}
	seen := map[EditKind]bool{}
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("AllEditKinds 有重复项 %q", k)
		}
		seen[k] = true
		if k == "" {
			t.Error("改动类型不应为空")
		}
		if label := EditLabel(k); label == "" || label == "其他修改" && k != EditOther {
			t.Errorf("类型 %q 缺少中文标签", k)
		}
	}
	// The labels the UI shows must be the ones documented.
	want := map[EditKind]string{
		EditTypo: "错别字", EditGrammar: "语病/语句不通", EditWording: "用词不严谨",
		EditPunctuation: "标点", EditSentenceSplit: "断句", EditHeading: "标题",
	}
	for k, label := range want {
		if got := EditLabel(k); got != label {
			t.Errorf("EditLabel(%q) = %q, 期望 %q", k, got, label)
		}
	}
	// An unknown kind must still render as something rather than an empty cell.
	if got := EditLabel(EditKind("unknown")); got == "" {
		t.Error("未知类型的标签不应为空")
	}
}

// TestPlanEditCountAndLookup covers the accessors the UI uses.
func TestPlanEditCountAndLookup(t *testing.T) {
	var nilPlan *Plan
	if nilPlan.EditCount() != 0 {
		t.Error("nil plan 的 EditCount 应为 0")
	}
	if nilPlan.EditsFor("n1") != nil {
		t.Error("nil plan 的 EditsFor 应为 nil")
	}

	p := &Plan{Edits: []Edit{
		{NodeID: "n1", Kind: EditTypo, Before: "a", After: "b"},
		{NodeID: "n2", Kind: EditGrammar, Before: "c", After: "d"},
		{NodeID: "n1", Kind: EditWording, Before: "e", After: "f"},
	}}
	if p.EditCount() != 3 {
		t.Errorf("EditCount = %d, 期望 3", p.EditCount())
	}
	got := p.EditsFor("n1")
	if len(got) != 2 {
		t.Fatalf("n1 应有 2 条改动, 实际 %d", len(got))
	}
	if got[0].Kind != EditTypo || got[1].Kind != EditWording {
		t.Errorf("应按声明顺序返回: %+v", got)
	}
	if len(p.EditsFor("nope")) != 0 {
		t.Error("无改动的节点应返回空")
	}
}

// TestNoOpEditsAreNotFlagged: the assembler must not advertise a change that
// did not change anything.
func TestNoOpEditsAreNotFlagged(t *testing.T) {
	d := doc(p("n0001", "内容"))
	plan := &Plan{Edits: []Edit{{
		NodeID: "n0001", Kind: EditOther, Before: "内容", After: "内容",
	}}}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: "内容"},
	}}
	diff := VerifyVerbatim(d, out, plan)
	if len(diff.Changed) != 0 {
		t.Errorf("无实际变化不应记入改动清单: %+v", diff.Changed)
	}
}

// TestChangeLogExcludesUnappliedEdits is a regression guard for a real
// inconsistency: the change log initially listed every declared edit, so an
// edit whose Before was not found appeared BOTH as "changed" and as
// "unapplied" — telling the user a correction had been made when it had not.
//
// The change log is what the user reviews to decide whether to trust the
// document, so it must describe what happened to the text, not what the plan
// intended.
func TestChangeLogExcludesUnappliedEdits(t *testing.T) {
	src := "这段里没有那个错字，但是有另一个错字。"
	d := doc(p("n0001", src))
	plan := &Plan{Edits: []Edit{
		// Will not apply: Before is absent.
		{NodeID: "n0001", Kind: EditTypo, Before: "不存在的错字", After: "改正"},
		// Will apply.
		{NodeID: "n0001", Kind: EditTypo, Before: "另一个错字", After: "另一个正字"},
		// A no-op.
		{NodeID: "n0001", Kind: EditOther, Before: "但是", After: "但是"},
	}}

	// The assembler applies what it can, producing this text.
	applied, _ := AppliedText(src, plan.Edits)
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: applied, Edited: true},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if !diff.Clean() {
		t.Fatalf("应通过逐字校验: %+v", diff.Altered)
	}

	if len(diff.Changed) != 1 {
		t.Fatalf("改动清单应只含真正生效的 1 条, 实际 %d: %+v", len(diff.Changed), diff.Changed)
	}
	if got := diff.Changed[0].Before; got != "另一个错字" {
		t.Errorf("清单应记录生效的那条, 实际 %q", got)
	}

	if len(diff.Unapplied) != 1 {
		t.Fatalf("应报告 1 条未生效, 实际 %d: %+v", len(diff.Unapplied), diff.Unapplied)
	}
	if got := diff.Unapplied[0].Before; got != "不存在的错字" {
		t.Errorf("未生效项应为找不到的那条, 实际 %q", got)
	}

	// The same edit must never appear in both lists.
	for _, c := range diff.Changed {
		for _, u := range diff.Unapplied {
			if c.Before == u.Before {
				t.Errorf("改动 %q 同时出现在生效与未生效清单中", c.Before)
			}
		}
	}
}

// TestChangeLogOrderFollowsApplication: edits are applied in order, and the log
// must read in that same order so a reviewer can follow the correction.
func TestChangeLogOrderFollowsApplication(t *testing.T) {
	src := "甲错乙错丙错"
	d := doc(p("n0001", src))
	plan := &Plan{Edits: []Edit{
		{NodeID: "n0001", Kind: EditTypo, Before: "甲错", After: "甲正"},
		{NodeID: "n0001", Kind: EditTypo, Before: "乙错", After: "乙正"},
		{NodeID: "n0001", Kind: EditTypo, Before: "丙错", After: "丙正"},
	}}
	applied, failed := AppliedText(src, plan.Edits)
	if len(failed) != 0 {
		t.Fatalf("不应有失败: %+v", failed)
	}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, NodeID: "n0001", Text: applied, Edited: true},
	}}

	diff := VerifyVerbatim(d, out, plan)
	if len(diff.Changed) != 3 {
		t.Fatalf("应有 3 条改动, 实际 %d", len(diff.Changed))
	}
	for i, w := range []string{"甲错", "乙错", "丙错"} {
		if diff.Changed[i].Before != w {
			t.Errorf("改动[%d].Before = %q, 期望 %q", i, diff.Changed[i].Before, w)
		}
	}
}
