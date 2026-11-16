package llm

import (
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// docForEdits is a one-paragraph document whose text exercises the punctuation
// rules and the snippet-length variation seen in real runs.
const editSource = "2 碑身尺寸:长90cm×宽60cm×厚12cm"

func editDoc() *docmodel.Document {
	return &docmodel.Document{
		SourceName: "测试.txt",
		Format:     "txt",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: editSource, Order: 0},
		},
	}
}

// TestNormalizeEditsCollapsesSnippetLengths: two runs may quote different
// amounts of the same line for the same correction. The review list must show
// one card, not two, and must not depend on which run produced it.
func TestNormalizeEditsCollapsesSnippetLengths(t *testing.T) {
	doc := editDoc()
	wide := docmodel.Edit{
		NodeID: "n0001", Kind: docmodel.EditPunctuation,
		Before: editSource,
		After:  "2 碑身尺寸：长90cm×宽60cm×厚12cm",
		Reason: "全角冒号",
	}
	narrow := docmodel.Edit{
		NodeID: "n0001", Kind: docmodel.EditPunctuation,
		Before: "碑身尺寸:长",
		After:  "碑身尺寸：长",
		Reason: "全角冒号",
	}

	a := NormalizeEdits(doc, []docmodel.Edit{wide})
	b := NormalizeEdits(doc, []docmodel.Edit{narrow})
	c := NormalizeEdits(doc, []docmodel.Edit{wide, narrow})

	if len(a) != 1 || len(b) != 1 || len(c) != 1 {
		t.Fatalf("同一修改应收敛为 1 条: wide=%d narrow=%d both=%d", len(a), len(b), len(c))
	}
	if a[0].Before != b[0].Before || a[0].After != b[0].After {
		t.Errorf("片段长度不应影响归一化结果:\nwide  %q → %q\nnarrow %q → %q",
			a[0].Before, a[0].After, b[0].Before, b[0].After)
	}
	if c[0].Before != a[0].Before {
		t.Errorf("重复候选应去重，实际 %q", c[0].Before)
	}
}

// TestNormalizeEditsDropsUnusableCandidates: a no-op and a snippet absent from
// the text can never be applied, so showing them would ask the user to approve
// something that silently fails.
func TestNormalizeEditsDropsUnusableCandidates(t *testing.T) {
	doc := editDoc()
	got := NormalizeEdits(doc, []docmodel.Edit{
		{NodeID: "n0001", Kind: docmodel.EditOther, Before: "碑身尺寸", After: "碑身尺寸"},
		{NodeID: "n0001", Kind: docmodel.EditTypo, Before: "原文没有这句", After: "改后"},
		{NodeID: "n9999", Kind: docmodel.EditTypo, Before: "碑身", After: "碑体"},
	})
	if len(got) != 0 {
		t.Fatalf("无用候选应全部丢弃, 实际 %+v", got)
	}
}

// TestMergeEditsKeepsHeadingRewrite: a title rewrite is a whole-line proposal.
// NormalizeEdits reduces prose edits to the minimal changed span and would
// discard a whitespace-only title change, so heading edits must bypass it.
func TestMergeEditsKeepsHeadingRewrite(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindHeading, Level: 1,
				Text: "一、总体 要求", Order: 0},
		},
	}
	got := mergeEdits(doc, []docmodel.Edit{{
		NodeID: "n0001", Kind: docmodel.EditHeading,
		Before: "一、总体 要求", After: "一、总体要求",
		Reason: "标题内的空格应删除",
	}})
	if len(got) != 1 {
		t.Fatalf("标题改动应保留, 实际 %+v", got)
	}
	if got[0].Before != "一、总体 要求" || got[0].After != "一、总体要求" {
		t.Errorf("标题改动被改写: %q → %q", got[0].Before, got[0].After)
	}
	if applied, failed := docmodel.AppliedText(doc.Nodes[0].Text, got); applied != "一、总体要求" || len(failed) != 0 {
		t.Errorf("应用结果 = %q, failed=%+v", applied, failed)
	}
}

// A model normally calls a retitled heading `wording`, and that is what the
// review card used to display. The change is a heading change; saying so is
// what lets the user recognise it as the title they asked about.
func TestMergeEditsRelabelsHeadingRewrite(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindHeading, Level: 1, Text: "一、项目概况", Order: 0},
		},
	}
	got := mergeEdits(doc, []docmodel.Edit{{
		NodeID:     "n0001",
		Kind:       docmodel.EditWording,
		Before:     "一、项目概况",
		After:      "一、项目基本情况",
		Reason:     "按要求修改标题",
		Occurrence: 1,
	}})
	if len(got) != 1 {
		t.Fatalf("应保留一条改动, 实际 %+v", got)
	}
	if got[0].Kind != docmodel.EditHeading {
		t.Errorf("整行标题改写应标为 heading, 实际 %q", got[0].Kind)
	}
	if got[0].After != "一、项目基本情况" {
		t.Errorf("改后文字被改动: %q", got[0].After)
	}
	if got[0].Occurrence != 0 {
		t.Errorf("整行标题只出现一次, occurrence 应为 0, 实际 %d", got[0].Occurrence)
	}
}

// A substring fix inside a heading is ordinary prose editing and must keep its
// own kind; only a whole-line rewrite is a heading change.
func TestMergeEditsKeepsInlineHeadingTypoKind(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindHeading, Level: 1, Text: "一、项目概况", Order: 0},
		},
	}
	got := mergeEdits(doc, []docmodel.Edit{{
		NodeID: "n0001",
		Kind:   docmodel.EditTypo,
		Before: "概况",
		After:  "概覧",
		Reason: "错字",
	}})
	if len(got) != 1 {
		t.Fatalf("应保留一条改动, 实际 %+v", got)
	}
	if got[0].Kind != docmodel.EditTypo {
		t.Errorf("段内错字不应被标成标题改动, 实际 %q", got[0].Kind)
	}
}

// The model and the rule-based floor both report the same half-width colon,
// quoting different snippets. They must collapse to one review card: two cards
// for one character is noise, and only the first can ever take effect.
func TestMergeEditsCollapsesSameSpanFromModelAndRules(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph,
				Text: "3 碑座尺寸: 上长110cm×宽16cm", Order: 0},
		},
	}
	got := mergeEdits(doc, []docmodel.Edit{{
		NodeID: "n0001",
		Kind:   docmodel.EditPunctuation,
		Before: "碑座尺寸:",
		After:  "碑座尺寸：",
		Reason: "中文行文应使用全角冒号",
	}})
	if len(got) != 1 {
		t.Fatalf("同一处冒号只应有一条改动, 实际 %d: %+v", len(got), got)
	}
	if applied, failed := docmodel.AppliedText(doc.Nodes[0].Text, got); applied != "3 碑座尺寸： 上长110cm×宽16cm" || len(failed) != 0 {
		t.Errorf("应用结果 = %q, failed=%+v", applied, failed)
	}
}

// The chat panel normalizes its own edits separately from the analysis path;
// a heading rewrite requested there must also be labelled 标题 and survive the
// whitespace-change rule that applies to prose.
func TestNormalizeRevisionEditsKeepsHeadingRewrite(t *testing.T) {
	doc := &docmodel.Document{
		SourceName: "t.docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindHeading, Level: 1,
				Text: "一、总体 要求", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "正文。", Order: 1},
		},
	}
	got := normalizeRevisionEdits(doc, []docmodel.Edit{
		{NodeID: "n0002", Kind: docmodel.EditTypo, Before: "正文。", After: "正文。"},
		{NodeID: "n0001", Kind: docmodel.EditWording,
			Before: "一、总体 要求", After: "一、总体要求"},
	})
	if len(got) != 1 {
		t.Fatalf("只应保留标题改动, 实际 %+v", got)
	}
	if got[0].NodeID != "n0001" || got[0].Kind != docmodel.EditHeading {
		t.Errorf("标题改写应标为 heading: %+v", got[0])
	}
}

// TestDeterministicEditsFindHalfWidthPunctuation: the review panel must not go
// empty because the model overlooked an objectively wrong character.
func TestDeterministicEditsFindHalfWidthPunctuation(t *testing.T) {
	doc := editDoc()
	got := DeterministicEdits(doc)
	if len(got) != 1 {
		t.Fatalf("应找到 1 处半角冒号, 实际 %d: %+v", len(got), got)
	}
	if got[0].NodeID != "n0001" || got[0].Kind != docmodel.EditPunctuation {
		t.Errorf("节点或类型错误: %+v", got[0])
	}
	applied, failed := docmodel.AppliedText(editSource, got)
	if len(failed) != 0 {
		t.Fatalf("规则生成的修改应能生效: %+v", failed)
	}
	if applied != "2 碑身尺寸：长90cm×宽60cm×厚12cm" {
		t.Errorf("替换结果错误: %q", applied)
	}
}

// TestDeterministicEditsIgnoreLegitimateHalfWidth: only marks with no valid
// half-width use in Chinese prose are flagged. Enumeration parentheses are
// common and must be left alone.
func TestDeterministicEditsIgnoreLegitimateHalfWidth(t *testing.T) {
	doc := &docmodel.Document{Nodes: []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: "（1）选项一 (2) 选项二", Order: 0},
		{ID: "n0002", Kind: docmodel.KindParagraph, Text: "ratio 3:1", Order: 1},
	}}
	if got := DeterministicEdits(doc); len(got) != 0 {
		t.Errorf("合法半角不应被标记: %+v", got)
	}
}

// TestDeterministicEditsHandleFullWidthSpace: a space (or U+3000) after the
// half-width mark must not hide the error. This is the exact shape that made
// two runs report different review lists.
func TestDeterministicEditsHandleFullWidthSpace(t *testing.T) {
	for _, src := range []string{
		"3 碑座尺寸: 上长110cm",
		"3 碑座尺寸:\u3000上长110cm",
		"3 碑座尺寸:\u00a0上长110cm",
	} {
		doc := &docmodel.Document{Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: src, Order: 0},
		}}
		got := DeterministicEdits(doc)
		if len(got) != 1 {
			t.Errorf("%q: 应找到 1 处, 实际 %d: %+v", src, len(got), got)
			continue
		}
		applied, failed := docmodel.AppliedText(src, got)
		if len(failed) != 0 {
			t.Errorf("%q: 修改应能生效: %+v", src, failed)
		}
		if !strings.Contains(applied, "尺寸：") {
			t.Errorf("%q: 替换后应含全角冒号, 实际 %q", src, applied)
		}
	}
}

// TestDeterministicEditsCloseTrailingSemicolon: a list that ends the paragraph
// on "；" is objectively unfinished. Keeping this out of the model's judgement
// is what stops one run from reporting it and the next from omitting it.
func TestDeterministicEditsCloseTrailingSemicolon(t *testing.T) {
	src := "需提供以下材料：营业执照；开户许可证；授权委托书；"
	doc := &docmodel.Document{Nodes: []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: src, Order: 0},
	}}
	got := DeterministicEdits(doc)
	if len(got) != 1 {
		t.Fatalf("应找到 1 处段末分号, 实际 %d: %+v", len(got), got)
	}
	applied, failed := docmodel.AppliedText(src, got)
	if len(failed) != 0 {
		t.Fatalf("修改应能生效: %+v", failed)
	}
	if !strings.HasSuffix(applied, "授权委托书。") {
		t.Errorf("段末分号应改为句号, 实际 %q", applied)
	}
}

// TestNormalizeEditsRejectsMiddleListSemicolon: splitting a list at a semicolon
// is an editorial choice, not a correction, and made two runs disagree.
func TestNormalizeEditsRejectsMiddleListSemicolon(t *testing.T) {
	src := "需提供以下材料：营业执照；开户许可证；授权委托书；"
	doc := &docmodel.Document{Nodes: []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: src, Order: 0},
	}}
	got := NormalizeEdits(doc, []docmodel.Edit{{
		NodeID: "n0001", Kind: docmodel.EditPunctuation,
		Before: "营业执照；开户许可证", After: "营业执照。开户许可证",
		Reason: "把列表拆成两句",
	}})
	if len(got) != 0 {
		t.Fatalf("句中分号改句号应被拒绝, 实际 %+v", got)
	}
}

// TestMergeEditsAlwaysIncludesRuleFindings: a run whose model answer has no
// edits must still produce the objective findings — this is the "校对块没有
// 生成校对项" complaint, reduced to a unit test.
func TestMergeEditsAlwaysIncludesRuleFindings(t *testing.T) {
	doc := editDoc()
	got := mergeEdits(doc, nil)
	if len(got) != 1 {
		t.Fatalf("模型无建议时仍应给出规则项, 实际 %d", len(got))
	}
}

// TestNormalizeEditsKeepsAdjacentFixesApplicable: two punctuation errors close
// together must both survive, and applying both must succeed. Overlapping
// context windows used to make the second one fail once the first was accepted.
func TestNormalizeEditsKeepsAdjacentFixesApplicable(t *testing.T) {
	src := "尺寸:长,宽60cm"
	doc := &docmodel.Document{Nodes: []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: src, Order: 0},
	}}
	edits := NormalizeEdits(doc, DeterministicEdits(doc))
	if len(edits) != 2 {
		t.Fatalf("应保留两处标点修改, 实际 %d: %+v", len(edits), edits)
	}
	applied, failed := docmodel.AppliedText(src, edits)
	if len(failed) != 0 {
		t.Fatalf("两条修改应同时生效: %+v", failed)
	}
	if applied != "尺寸：长，宽60cm" {
		t.Errorf("替换结果错误: %q", applied)
	}
}
