package assemble

import (
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// ---------------------------------------------------------------- declared edits

// TestBuildAppliesDeclaredEdit is the mechanism the user asked for: the tool
// may fix a typo, and the fix reaches the output.
func TestBuildAppliesDeclaredEdit(t *testing.T) {
	doc := src(para("n0001", "我们要含辛如苦地推进这项工作。"))
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{{
			NodeID: "n0001", Kind: docmodel.EditTypo,
			Before: "含辛如苦", After: "含辛茹苦", Reason: "成语误写",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Diff == nil || !res.Diff.Clean() {
		t.Fatalf("已声明的改动不应导致校验失败: %+v", res.Diff)
	}
	if !strings.Contains(res.Doc.Text(), "含辛茹苦") {
		t.Errorf("改动未应用到输出:\n%s", res.Doc.Text())
	}
	if strings.Contains(res.Doc.Text(), "含辛如苦") {
		t.Errorf("原文错误写法仍在输出中:\n%s", res.Doc.Text())
	}

	if len(res.Diff.Changed) != 1 {
		t.Fatalf("应记录 1 条改动, 实际 %+v", res.Diff.Changed)
	}
	if got := res.Diff.Changed[0]; got.Before != "含辛如苦" || got.After != "含辛茹苦" {
		t.Errorf("改动记录不符: %+v", got)
	}
	if got := res.Diff.Changed[0].Label; got != "错别字" {
		t.Errorf("改动记录应带中文标签, 实际 %q", got)
	}
}

// TestBuildMarksEditedBlock: the renderers need to know which paragraphs were
// touched so they can highlight them. Without this the change is real but
// invisible, and "可审计" would not hold.
func TestBuildMarksEditedBlock(t *testing.T) {
	doc := src(
		para("n0001", "这段有错别字：按排工作。"),
		para("n0002", "这段没有问题。"),
	)
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{{
			NodeID: "n0001", Kind: docmodel.EditTypo,
			Before: "按排", After: "安排", Reason: "错别字",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001", "n0002"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var edited, clean *docmodel.OutBlock
	for i := range res.Doc.Blocks {
		switch res.Doc.Blocks[i].NodeID {
		case "n0001":
			edited = &res.Doc.Blocks[i]
		case "n0002":
			clean = &res.Doc.Blocks[i]
		}
	}
	if edited == nil || clean == nil {
		t.Fatal("未找到两个正文块")
	}
	if !edited.Edited {
		t.Error("被改动的块应标记 Edited")
	}
	if len(edited.EditKinds) != 1 || edited.EditKinds[0] != string(docmodel.EditTypo) {
		t.Errorf("被改动的块应带改动类型, 实际 %+v", edited.EditKinds)
	}
	if clean.Edited {
		t.Error("未改动的块不应标记 Edited")
	}
}

// TestBuildWithoutEditsStaysVerbatim is the guarantee that survives allowing
// edits: a plan that declares nothing produces byte-identical text, so
// documents that need no correction are untouched.
func TestBuildWithoutEditsStaysVerbatim(t *testing.T) {
	original := "原文一字不动，包括“引号”和\u3000全角空格。"
	doc := src(para("n0001", original))
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Diff.Changed) != 0 {
		t.Errorf("无改动声明时不应有改动记录: %+v", res.Diff.Changed)
	}
	if !strings.Contains(res.Doc.Text(), original) {
		t.Errorf("原文应逐字保留:\n得到 %q", res.Doc.Text())
	}
	if !res.Diff.Clean() {
		t.Fatalf("无改动声明应通过逐字校验: %+v", res.Diff)
	}
}

// TestBuildReportsEditTargetMissing: an edit whose Before is absent changes
// nothing. The document stays correct so Build succeeds, but the failure must be
// reported — the model asked for something that did not happen.
func TestBuildReportsEditTargetMissing(t *testing.T) {
	doc := src(para("n0001", "这段里没有那个错字。"))
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{{
			NodeID: "n0001", Kind: docmodel.EditTypo,
			Before: "不存在的错字", After: "改正",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !res.Diff.Clean() {
		t.Fatalf("未生效的改动不损坏文档: %+v", res.Diff)
	}
	if len(res.FailedEdits) != 1 {
		t.Fatalf("应报告 1 项未生效改动, 实际 %d: %+v", len(res.FailedEdits), res.FailedEdits)
	}
	if res.FailedEdits[0].Before != "不存在的错字" {
		t.Errorf("未生效项应保留 Before: %+v", res.FailedEdits[0])
	}
	if !strings.Contains(res.Doc.Text(), "这段里没有那个错字。") {
		t.Errorf("文本不应被改动: %q", res.Doc.Text())
	}
}

// TestBuildAppliesEditToElementSlot: a 标题 with a typo is exactly what a user
// wants fixed, so edits must work on bound element nodes too, not only on
// section body paragraphs.
func TestBuildAppliesEditToElementSlot(t *testing.T) {
	doc := src(para("n0001", "关于开展某项工做的通知"))
	plan := &docmodel.Plan{
		Elements: []docmodel.ElementBinding{
			{Role: docmodel.RoleTitle, NodeID: "n0001"},
		},
		Edits: []docmodel.Edit{{
			NodeID: "n0001", Kind: docmodel.EditTypo,
			Before: "工做", After: "工作", Reason: "错别字",
		}},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !res.Diff.Clean() {
		t.Fatalf("要素槽位上的已声明改动不应失败: %+v", res.Diff)
	}
	var title *docmodel.OutBlock
	for i := range res.Doc.Blocks {
		if res.Doc.Blocks[i].Role == docmodel.RoleTitle {
			title = &res.Doc.Blocks[i]
		}
	}
	if title == nil {
		t.Fatal("未生成标题块")
	}
	if title.Text != "关于开展某项工作的通知" {
		t.Errorf("标题改动未生效: %q", title.Text)
	}
	if !title.Edited {
		t.Error("标题块也应标记 Edited")
	}
}

// TestBuildAppliesMultipleEditsToSameNode: several corrections in one paragraph
// must all land, and each must be logged.
func TestBuildAppliesMultipleEditsToSameNode(t *testing.T) {
	doc := src(para("n0001", "按排好工做，认真负则。"))
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{
			{NodeID: "n0001", Kind: docmodel.EditTypo, Before: "按排", After: "安排"},
			{NodeID: "n0001", Kind: docmodel.EditTypo, Before: "工做", After: "工作"},
			{NodeID: "n0001", Kind: docmodel.EditTypo, Before: "负则", After: "负责"},
		},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !res.Diff.Clean() {
		t.Fatalf("已声明的多处改动不应失败: %+v", res.Diff)
	}
	if want := "安排好工作，认真负责。"; !strings.Contains(res.Doc.Text(), want) {
		t.Errorf("多处改动未全部生效:\n得到 %q", res.Doc.Text())
	}
	if len(res.Diff.Changed) != 3 {
		t.Errorf("应记录 3 条改动, 实际 %d: %+v", len(res.Diff.Changed), res.Diff.Changed)
	}
}

// TestBuildEditPreservesSurroundingText is the strictest property of the relaxed
// rule: an edit changes only the declared span. Everything else, including
// awkward whitespace and punctuation, must survive byte-for-byte.
func TestBuildEditPreservesSurroundingText(t *testing.T) {
	original := "“前面”有\u3000全角空格……和  ASCII  空格，错别字在这里，后面“也有”。"
	doc := src(para("n0001", original))
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{{
			NodeID: "n0001", Kind: docmodel.EditTypo, Before: "错别字", After: "正确字",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var body *docmodel.OutBlock
	for i := range res.Doc.Blocks {
		if res.Doc.Blocks[i].NodeID == "n0001" {
			body = &res.Doc.Blocks[i]
		}
	}
	if body == nil {
		t.Fatal("未找到正文块")
	}
	want := strings.Replace(original, "错别字", "正确字", 1)
	if body.Text != want {
		t.Errorf("只应改动声明的片段:\n得到 %q\n期望 %q", body.Text, want)
	}
	for _, must := range []string{"“前面”", "\u3000", "……", "和  ASCII  空格", "“也有”"} {
		if !strings.Contains(body.Text, must) {
			t.Errorf("改动波及了未声明的文本 %q: %q", must, body.Text)
		}
	}
}

// TestBuildStillRejectsDroppedContentWithEdits: allowing content edits must not
// weaken the coverage rule — a plan still has to account for every paragraph.
func TestBuildStillRejectsDroppedContentWithEdits(t *testing.T) {
	doc := src(para("n0001", "甲。"), para("n0002", "乙会被丢掉。"))
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{{
			NodeID: "n0001", Kind: docmodel.EditTypo, Before: "甲", After: "甲改",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}

	if _, err := Build(doc, plan, spec()); err == nil {
		t.Fatal("即使允许改内容，丢失段落仍必须拒绝")
	}
}

// TestBuildFailedEditsAgreeWithDiff is a regression guard for an inconsistency:
// Result.FailedEdits was filled only from edits the assembler happened to try
// applying, so an edit it never reached (one with no target node) appeared in
// Diff.Unapplied but not in FailedEdits. Two lists describing the same thing
// must agree, or the user cannot tell which one to trust.
func TestBuildFailedEditsAgreeWithDiff(t *testing.T) {
	doc := src(para("n0001", "正文内容。"))
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{
			// No target node: cannot be applied by design.
			{NodeID: "", Kind: docmodel.EditHeading, After: "一、总体要求"},
			// Target node does not exist.
			{NodeID: "n9999", Kind: docmodel.EditTypo, Before: "甲", After: "乙"},
			// Applies fine.
			{NodeID: "n0001", Kind: docmodel.EditTypo, Before: "正文", After: "本文"},
		},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(res.Diff.Unapplied) != 2 {
		t.Fatalf("应有 2 条未生效, 实际 %d: %+v", len(res.Diff.Unapplied), res.Diff.Unapplied)
	}
	if len(res.FailedEdits) != len(res.Diff.Unapplied) {
		t.Fatalf("FailedEdits(%d) 应与 Diff.Unapplied(%d) 一致:\n FailedEdits=%+v\n Unapplied=%+v",
			len(res.FailedEdits), len(res.Diff.Unapplied), res.FailedEdits, res.Diff.Unapplied)
	}

	// The applied one must not be listed as failed, and must be logged.
	for _, f := range res.FailedEdits {
		if f.Before == "正文" {
			t.Errorf("已生效的改动不应出现在未生效清单中: %+v", f)
		}
	}
	if len(res.Diff.Changed) != 1 || res.Diff.Changed[0].After != "本文" {
		t.Errorf("应有 1 条生效改动: %+v", res.Diff.Changed)
	}
}

// TestBuildGeneratesSectionHeadingFromTitle covers the supported way to add a
// heading the source lacks (要求「允许生成段标题」): a Section with a Title and
// no HeadingNodeID. The heading is model-authored, so it must be counted in the
// report for review.
func TestBuildGeneratesSectionHeadingFromTitle(t *testing.T) {
	doc := src(para("n0001", "坚持安全第一，认真负责。"))
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{{
			ID: "s1", Title: "一、总体要求", Level: 1,
			BodyNodeIDs: []string{"n0001"},
		}},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var heading *docmodel.OutBlock
	for i := range res.Doc.Blocks {
		if res.Doc.Blocks[i].Role == docmodel.RoleHeading1 {
			heading = &res.Doc.Blocks[i]
		}
	}
	if heading == nil {
		t.Fatal("原文无标题时应生成一个标题块")
	}
	if heading.Text != "一、总体要求" {
		t.Errorf("标题文字 = %q", heading.Text)
	}
	// The 层次序数 must be the real one, not doubled onto the title.
	if heading.AutoNumber != "" {
		t.Errorf("title 已含「一、」时不应再加编号, 实际 %q", heading.AutoNumber)
	}
	// It must be auditable: the user has to know this heading is not theirs.
	if res.Report.SynthesizedHeadings != 1 {
		t.Errorf("模型生成的标题应计入报告, 实际 %d", res.Report.SynthesizedHeadings)
	}
	// And it must be listed as generated text, so "多出来的只有这些" holds.
	found := false
	for _, s := range res.SynthText {
		if strings.Contains(s, "总体要求") {
			found = true
		}
	}
	if !found {
		t.Errorf("生成的标题应计入生成文字清单: %+v", res.SynthText)
	}
	// The source text itself is untouched.
	if !strings.Contains(res.Doc.Text(), "坚持安全第一，认真负责。") {
		t.Errorf("原文应逐字保留: %q", res.Doc.Text())
	}
}

// TestBuildEditDoesNotTouchNumbering: an edit to a heading's text must not
// disturb the 层次序数 machinery.

func TestBuildEditDoesNotTouchNumbering(t *testing.T) {
	doc := src(
		head("n0001", "一、总体要球", 1),
		para("n0002", "正文。"),
	)
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{{
			NodeID: "n0001", Kind: docmodel.EditTypo, Before: "要球", After: "要求",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、总体要球", Level: 1,
				HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var heading *docmodel.OutBlock
	for i := range res.Doc.Blocks {
		if res.Doc.Blocks[i].NodeID == "n0001" {
			heading = &res.Doc.Blocks[i]
		}
	}
	if heading == nil {
		t.Fatal("未生成标题块")
	}
	if heading.AutoNumber != "" {
		t.Errorf("原文已有序数时不应追加编号, 实际 %q", heading.AutoNumber)
	}
	if heading.Text != "一、总体要求" {
		t.Errorf("标题改动未生效或编号被破坏: %q", heading.Text)
	}
	if !strings.HasPrefix(heading.Text, "一、") {
		t.Errorf("原标题序数应保留: %q", heading.Text)
	}
}

// TestBuildEditClearsStaleRuns: a source block carries its inline runs. When a
// declared edit changes the text, those runs no longer spell it, and a
// renderer that trusted them would print the original wording — the reported
// "同意后标题没有变" defect. The assembled block must therefore expose runs
// that either match the new text or are dropped.
func TestBuildEditClearsStaleRuns(t *testing.T) {
	doc := src(docmodel.Node{
		ID: "n0001", Kind: docmodel.KindHeading, Level: 1,
		Text:  "一、总体要求",
		Runs:  []docmodel.Run{{Text: "一、总体要"}, {Text: "求", Bold: true}},
		Order: 0,
	}, para("n0002", "正文。"))
	plan := &docmodel.Plan{
		Edits: []docmodel.Edit{{
			NodeID: "n0001", Kind: docmodel.EditHeading,
			Before: "一、总体要求", After: "一、总体目标",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、总体要求", Level: 1,
				HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var heading *docmodel.OutBlock
	for i := range res.Doc.Blocks {
		if res.Doc.Blocks[i].NodeID == "n0001" {
			heading = &res.Doc.Blocks[i]
		}
	}
	if heading == nil {
		t.Fatal("未生成标题块")
	}
	if heading.Text != "一、总体目标" {
		t.Fatalf("标题文字 = %q", heading.Text)
	}
	if len(heading.Runs) != 0 {
		t.Errorf("改动后不得保留源 runs: %+v", heading.Runs)
	}
	var joined strings.Builder
	for _, run := range heading.Runs {
		joined.WriteString(run.Text)
	}
	if joined.Len() > 0 {
		t.Errorf("改动后遗留的源 runs 会打印旧文字: runs=%q text=%q",
			joined.String(), heading.Text)
	}
}
