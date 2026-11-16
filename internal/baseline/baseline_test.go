package baseline

import (
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

func docOf(nodes ...docmodel.Node) *docmodel.Document {
	for i := range nodes {
		nodes[i].Order = i
	}
	return &docmodel.Document{SourceName: "通知.docx", Format: "docx", Nodes: nodes}
}

func p(id, text string) docmodel.Node {
	return docmodel.Node{ID: id, Kind: docmodel.KindParagraph, Text: text}
}

func h(id, text string, level int) docmodel.Node {
	return docmodel.Node{ID: id, Kind: docmodel.KindHeading, Text: text, Level: level}
}

// sections flattens a plan's tree for assertions.
func sections(plan *docmodel.Plan) []docmodel.Section {
	var out []docmodel.Section
	var walk func([]docmodel.Section)
	walk = func(list []docmodel.Section) {
		for _, s := range list {
			out = append(out, s)
			walk(s.Children)
		}
	}
	walk(plan.Sections)
	return out
}

// ---------------------------------------------------------------- 覆盖与通过

// TestBuildPassesVerification is the property the whole package rests on: a
// plan derived from the document covers it exactly, so it must satisfy
// VerifyPlan without any repair.
func TestBuildPassesVerification(t *testing.T) {
	docs := map[string]*docmodel.Document{
		"标准层级": docOf(
			p("n0001", "关于开展安全生产检查的通知"),
			h("n0002", "一、总体要求", 1),
			p("n0003", "第一段正文。"),
			h("n0004", "（一）主要目标", 2),
			p("n0005", "目标内容。"),
			h("n0006", "二、检查范围", 1),
			p("n0007", "范围内容。"),
		),
		"无编号": docOf(
			p("n0001", "标题"),
			p("n0002", "一段话。"),
			p("n0003", "另一段话。"),
		),
		"文首有前言": docOf(
			h("n0001", "关于开展检查的通知", 1),
			p("n0002", "为贯彻落实要求，现将有关事项通知如下。"),
			h("n0003", "一、总体要求", 1),
			p("n0004", "正文。"),
		),
		"跳级": docOf(
			h("n0001", "一、总体要求", 1),
			h("n0002", "（1）细项", 4),
			p("n0003", "内容。"),
		),
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			plan := Build(doc)
			rep := docmodel.VerifyPlan(doc, plan)
			if !rep.OK() {
				t.Fatalf("基线方案应通过校验: %s\n诊断: %+v", rep.Summary(), rep.Diagnostics)
			}
			if rep.SynthesizedHeadings > 0 && name != "无编号" {
				t.Errorf("除无编号文档外不应生成标题, 实际 %d", rep.SynthesizedHeadings)
			}
		})
	}
}

// TestBuildCutsAtNumberedParagraphs: the common real-world shape is chapters
// typed as plain paragraphs, which the docx parser calls 正文. The baseline
// must still see them as chapters.
func TestBuildCutsAtNumberedParagraphs(t *testing.T) {
	doc := docOf(
		p("n0001", "一、总体要求"),
		p("n0002", "第一段。"),
		p("n0003", "二、检查范围"),
		p("n0004", "第二段。"),
	)
	plan := Build(doc)
	got := sections(plan)
	if len(got) != 2 {
		t.Fatalf("应切出 2 个章节, 实际 %d: %+v", len(got), got)
	}
	if got[0].HeadingNodeID != "n0001" || got[1].HeadingNodeID != "n0003" {
		t.Errorf("切点错误: %q / %q", got[0].HeadingNodeID, got[1].HeadingNodeID)
	}
	if len(got[0].BodyNodeIDs) != 1 || got[0].BodyNodeIDs[0] != "n0002" {
		t.Errorf("第一章正文 = %v", got[0].BodyNodeIDs)
	}
}

// TestBuildNestsByLevel: 一、 then （一） then 一、 again must produce one
// level-2 child, then a sibling at level 1.
func TestBuildNestsByLevel(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "（一）乙", 2), p("n0004", "乙段"),
		h("n0005", "二、丙", 1), p("n0006", "丙段"),
	)
	plan := Build(doc)
	if len(plan.Sections) != 2 {
		t.Fatalf("根级应有 2 个章节, 实际 %d", len(plan.Sections))
	}
	if len(plan.Sections[0].Children) != 1 {
		t.Fatalf("第一章应有 1 个子章节, 实际 %d", len(plan.Sections[0].Children))
	}
	if len(plan.Sections[1].Children) != 0 {
		t.Errorf("第二章不应有子章节")
	}
}

// TestBuildPutsPreHeadingContentInPreamble: prose before the first heading is
// 缘由, not a chapter. Inventing a chapter for it would print a heading the
// author never wrote.
func TestBuildPutsPreHeadingContentInPreamble(t *testing.T) {
	doc := docOf(
		h("n0001", "关于开展检查的通知", 1),
		p("n0002", "为贯彻落实要求，现将有关事项通知如下。"),
		h("n0003", "一、总体要求", 1),
		p("n0004", "正文。"),
	)
	plan := Build(doc)
	if len(plan.PreambleNodeIDs) != 1 || plan.PreambleNodeIDs[0] != "n0002" {
		t.Errorf("前言 = %v", plan.PreambleNodeIDs)
	}
}

// TestBuildBindsOpeningTitle: the 标题 line has no heading style, so without
// binding it the first chapter would swallow it and typeset it as body text.
func TestBuildBindsOpeningTitle(t *testing.T) {
	doc := docOf(
		p("n0001", "关于开展安全生产大检查的通知"),
		p("n0002", "为贯彻落实要求。"),
		p("n0003", "一、总体要求"),
		p("n0004", "正文。"),
	)
	plan := Build(doc)
	if len(plan.Elements) != 1 {
		t.Fatalf("应绑定 1 个要素, 实际 %+v", plan.Elements)
	}
	if plan.Elements[0].Role != docmodel.RoleTitle || plan.Elements[0].NodeID != "n0001" {
		t.Errorf("要素绑定错误: %+v", plan.Elements[0])
	}
	if !docmodel.VerifyPlan(doc, plan).OK() {
		t.Errorf("绑定标题后应通过校验")
	}
}

// TestBuildDoesNotBindTitleWhenHeadingsExist: a document with real heading
// markup has its own idea of what a heading is; the first line is body.
func TestBuildDoesNotBindTitleWhenHeadingsExist(t *testing.T) {
	doc := docOf(
		h("n0001", "一、总体要求", 1),
		p("n0002", "正文。"),
	)
	plan := Build(doc)
	if len(plan.Elements) != 0 {
		t.Errorf("已有标题样式时不应再猜标题: %+v", plan.Elements)
	}
}

// TestBuildClampsSkippedLevels: 一、 straight to （1） cannot be a tree. The
// deeper section is placed one step down rather than two, which keeps the
// structure expressible without inventing a chapter.
func TestBuildClampsSkippedLevels(t *testing.T) {
	doc := docOf(
		h("n0001", "一、总体要求", 1),
		h("n0002", "（1）细项", 4),
		p("n0003", "内容。"),
	)
	plan := Build(doc)
	if len(plan.Sections) != 1 || len(plan.Sections[0].Children) != 1 {
		t.Fatalf("层级应被收敛为父子: %+v", plan.Sections)
	}
	if lv := plan.Sections[0].Children[0].Level; lv != 2 {
		t.Errorf("子章节层级 = %d, 期望 2", lv)
	}
}

// TestBuildKeepsSameNumberedSiblingsAtSameDepth: 三、 followed by “1.” and “2.”
// is one parent with two children. The first “1.” is pulled up from source
// level 3 to tree level 2 so the skip is expressible; the second “2.” must
// still close it and become its sibling, not its child. Getting this wrong
// makes the baseline itself claim a hierarchy the author never wrote, and the
// structural diff then reports a false relocation.
func TestBuildKeepsSameNumberedSiblingsAtSameDepth(t *testing.T) {
	doc := docOf(
		h("n0001", "三、工作要求", 1),
		h("n0002", "1.加强组织领导", 3),
		h("n0003", "2.严格执法检查", 3),
	)
	plan := Build(doc)
	if len(plan.Sections) != 1 {
		t.Fatalf("应只有一个根章节, 实际 %d: %+v", len(plan.Sections), plan.Sections)
	}
	kids := plan.Sections[0].Children
	if len(kids) != 2 {
		t.Fatalf("1./2. 应是两个同级子章节, 实际 %d: %+v", len(kids), kids)
	}
	if kids[0].Level != 2 || kids[1].Level != 2 {
		t.Errorf("两个子章节层级都应为 2: %d / %d", kids[0].Level, kids[1].Level)
	}
}

// TestBuildAssignsUniqueIDs guards the property VerifyPlan now enforces.
func TestBuildAssignsUniqueIDs(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲"),
		h("n0003", "（一）乙", 2), p("n0004", "乙"),
		h("n0005", "（二）丙", 2), p("n0006", "丙"),
		h("n0007", "二、丁", 1), p("n0008", "丁"),
	)
	plan := Build(doc)
	seen := map[string]bool{}
	for _, s := range sections(plan) {
		if s.ID == "" {
			t.Fatal("章节缺少 id")
		}
		if seen[s.ID] {
			t.Errorf("id %q 重复", s.ID)
		}
		seen[s.ID] = true
	}
}

// TestBuildWithoutHeadingsMakesOneSection: with no structure of its own, the
// only honest answer is one unnumbered section — inventing chapters would
// fabricate headings.
func TestBuildWithoutHeadingsMakesOneSection(t *testing.T) {
	doc := docOf(p("n0001", "一段话。"), p("n0002", "另一段话。"))
	plan := Build(doc)
	if len(plan.Sections) != 1 {
		t.Fatalf("应只有 1 个章节, 实际 %d", len(plan.Sections))
	}
	if plan.Sections[0].HeadingNodeID != "" {
		t.Error("无标题文档的章节不应引用原文标题")
	}
	if len(plan.Notes) == 0 {
		t.Error("应说明为什么只有一个章节")
	}
}

// ---------------------------------------------------------------- 结构 diff

// TestCompareIdenticalIsEmpty: a plan that matches the source's own structure
// must produce no diff, or the warning becomes noise.
func TestCompareIdenticalIsEmpty(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "二、乙", 1), p("n0004", "乙段"),
	)
	plan := Build(doc)
	diff := Compare(doc, Build(doc), plan)
	if !diff.Empty() {
		t.Fatalf("相同结构不应有差异: %s\n%+v", diff.Summary(), diff)
	}
}

// TestCompareDetectsParagraphMove is the point of the package: a paragraph the
// model moved from chapter two to chapter one is reported with both ends.
func TestCompareDetectsParagraphMove(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "二、乙", 1), p("n0004", "乙段"),
	)
	base := Build(doc)
	moved := Build(doc)
	// Move n0004 from the second chapter into the first.
	moved.Sections[0].BodyNodeIDs = append(moved.Sections[0].BodyNodeIDs, "n0004")
	moved.Sections[1].BodyNodeIDs = []string{}

	diff := Compare(doc, base, moved)
	if len(diff.Moves) != 1 {
		t.Fatalf("应报告 1 处移动, 实际 %d: %+v", len(diff.Moves), diff.Moves)
	}
	m := diff.Moves[0]
	if m.NodeID != "n0004" || m.Kind != MoveSection {
		t.Errorf("移动记录错误: %+v", m)
	}
	if !strings.Contains(m.From, "二、乙") || !strings.Contains(m.To, "一、甲") {
		t.Errorf("应同时指出起止章节: %+v", m)
	}
}

// TestCompareIgnoresChapterIdScheme: the baseline numbers chapters `b1…` and
// the model numbers `s1…`. A faithfully copied paragraph must not be reported
// as moved just because the ids differ; the source heading is the shared
// anchor.
func TestCompareIgnoresChapterIdScheme(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "二、乙", 1), p("n0004", "乙段"),
	)
	base := Build(doc)
	plan := Build(doc)
	plan.Sections[0].ID = "s1"
	plan.Sections[1].ID = "s2"

	diff := Compare(doc, base, plan)
	if len(diff.Moves) != 0 {
		t.Fatalf("同一章换了 id 不算移动: %+v", diff.Moves)
	}
	if len(diff.MovedSections) != 0 {
		t.Errorf("同一章换了 id 不算换位置: %+v", diff.MovedSections)
	}
}

// TestCompareDetectsPremableMove: taking a chapter paragraph out and placing it
// directly under 主送机关 is a structural change too.
func TestCompareDetectsPremableMove(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
	)
	base := Build(doc)
	out := Build(doc)
	out.Sections[0].BodyNodeIDs = []string{}
	out.PreambleNodeIDs = []string{"n0002"}

	diff := Compare(doc, base, out)
	if len(diff.Moves) != 1 || diff.Moves[0].Kind != MoveToPreamble {
		t.Fatalf("应报告移入前言: %+v", diff.Moves)
	}
}

// TestCompareDetectsSectionReorder: swapping two chapters is invisible to the
// per-section reorder check but obvious here.
func TestCompareDetectsSectionReorder(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "二、乙", 1), p("n0004", "乙段"),
	)
	base := Build(doc)
	swap := Build(doc)
	swap.Sections[0], swap.Sections[1] = swap.Sections[1], swap.Sections[0]

	diff := Compare(doc, base, swap)
	if len(diff.MovedSections) != 2 {
		t.Fatalf("两个章节都换了位置, 实际 %d: %+v", len(diff.MovedSections), diff.MovedSections)
	}
}

// TestCompareDetectsRetitle: the model rewording a chapter title is a
// structural change the user should see.
func TestCompareDetectsRetitle(t *testing.T) {
	doc := docOf(h("n0001", "一、甲", 1), p("n0002", "甲段"))
	base := Build(doc)
	other := Build(doc)
	other.Sections[0].Title = "一、甲（修订）"

	diff := Compare(doc, base, other)
	if len(diff.Sections) != 1 || diff.Sections[0].Kind != "retitled" {
		t.Fatalf("应报告改名: %+v", diff.Sections)
	}
	if diff.Sections[0].BaselineTitle != "一、甲" {
		t.Errorf("应保留原名称: %+v", diff.Sections[0])
	}
}

// TestCompareReportsElementBinding: a paragraph the plan turns into an element
// (date, recipient) left the body, which the user should see.
func TestCompareReportsElementBinding(t *testing.T) {
	doc := docOf(h("n0001", "一、甲", 1), p("n0002", "甲段"))
	base := Build(doc)
	bound := Build(doc)
	bound.Sections[0].BodyNodeIDs = []string{}
	bound.Elements = append(bound.Elements, docmodel.ElementBinding{
		Role: docmodel.RoleDate, NodeID: "n0002",
	})

	diff := Compare(doc, base, bound)
	if len(diff.Moves) != 1 || diff.Moves[0].Kind != MoveToElement {
		t.Fatalf("应报告绑定为要素: %+v", diff.Moves)
	}
	if !strings.Contains(diff.Moves[0].To, "成文日期") {
		t.Errorf("应使用中文要素名: %q", diff.Moves[0].To)
	}
}

// TestCompareReportsTitleBindingOnce: binding a body paragraph as the 标题 is
// one change, not two. The move already says "改作公文要素"; repeating it in
// the element list shows the user two cards for one decision.
func TestCompareReportsTitleBindingOnce(t *testing.T) {
	doc := docOf(h("n0001", "一、甲", 1), p("n0002", "甲段"))
	base := Build(doc)
	bound := Build(doc)
	bound.Sections[0].BodyNodeIDs = []string{}
	bound.Elements = append(bound.Elements, docmodel.ElementBinding{
		Role: docmodel.RoleTitle, NodeID: "n0002",
	})

	diff := Compare(doc, base, bound)
	if len(diff.Moves) != 1 || diff.Moves[0].Kind != MoveToElement {
		t.Fatalf("应报告绑定为标题: %+v", diff.Moves)
	}
	for _, e := range diff.Elements {
		if e.Kind == "bound" && e.NodeID == "n0002" {
			t.Errorf("同一处改动不应再出现在要素项: %+v", diff.Elements)
		}
	}
}

// TestCompareReportsUnboundTitle: the baseline binds the opening 标题. If the
// plan drops that binding, the title paragraph is printed as body text instead
// of the 标题 — the page visibly changes, so it must be listed.
func TestCompareReportsUnboundTitle(t *testing.T) {
	doc := docOf(
		p("n0001", "关于开展甲的通知"),
		h("n0002", "一、总体要求", 1), p("n0003", "正文。"),
	)
	plan := Build(doc)
	plan.Elements = []docmodel.ElementBinding{}
	plan.Sections[0].BodyNodeIDs = append(plan.Sections[0].BodyNodeIDs, "n0001")

	diff := Compare(doc, Build(doc), plan)
	var found bool
	for _, e := range diff.Elements {
		if e.Kind == "unbound" && e.NodeID == "n0001" && e.Role == string(docmodel.RoleTitle) {
			found = true
		}
	}
	if !found {
		t.Fatalf("应报告标题被取消绑定: %+v", diff.Elements)
	}
}

// TestSummaryIsReadable: the summary appears in logs and reports, so it has to
// say something useful rather than dump a struct.
func TestSummaryIsReadable(t *testing.T) {
	doc := docOf(h("n0001", "一、甲", 1), p("n0002", "甲段"))
	other := Build(doc)
	other.Sections[0].Title = "改名"
	if s := Compare(doc, Build(doc), other).Summary(); !strings.Contains(s, "结构有调整") {
		t.Errorf("摘要应说明有调整: %q", s)
	}
	if s := Compare(doc, Build(doc), Build(doc)).Summary(); s != "结构与原文一致" {
		t.Errorf("无差异时的摘要 = %q", s)
	}
}

// TestCompareReportsSynthesizedHeading: a heading the model wrote itself is
// printed on the page, so it must appear in the structural change log. It has
// no heading node, which is exactly why the by-heading comparison cannot see
// it and a dedicated pass is required.
func TestCompareReportsSynthesizedHeading(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		p("n0003", "乙段"),
	)
	plan := Build(doc)
	plan.Sections = append(plan.Sections, docmodel.Section{
		ID: "s9", Title: "二、模型新写的章", Level: 1, BodyNodeIDs: []string{"n0003"},
	})
	plan.Sections[0].BodyNodeIDs = []string{"n0002"}

	diff := Compare(doc, Build(doc), plan)
	var found bool
	for _, c := range diff.Sections {
		if c.Kind == "synthesized" && c.Title == "二、模型新写的章" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应报告模型自拟标题: %+v", diff.Sections)
	}
}

// TestCompareReportsLevelChange: keeping the title but moving the chapter a
// level deeper changes the printed 层次序数, which a reviewer must see.
func TestCompareReportsLevelChange(t *testing.T) {
	// Styled headings with no 层次序数 of their own: the plan's level is what
	// decides the printed number, so a level change is visible on the page.
	doc := docOf(
		p("n0001", "关于甲的通知"),
		h("n0002", "甲", 1), p("n0003", "甲段"),
		h("n0004", "乙", 1), p("n0005", "乙段"),
	)
	plan := Build(doc)
	child := plan.Sections[1]
	child.Level = 2
	parent := plan.Sections[0]
	parent.Children = append(parent.Children, child)
	plan.Sections = []docmodel.Section{parent}

	diff := Compare(doc, Build(doc), plan)
	var found bool
	for _, c := range diff.Sections {
		if c.Kind == "releveled" && c.ID == child.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("应报告层级变化: %+v", diff.Sections)
	}
}

// TestCompareReportsReparentedSection: a child chapter lifted to the root keeps
// its title, its own 层次序数 and its reading position, so neither the retitle,
// relevel nor section-move checks can see it. The printed tree is still
// different — that chapter no longer belongs to its parent — and the reviewer
// must be told.
func TestCompareReportsReparentedSection(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "甲段"),
		h("n0003", "（一）乙", 2), p("n0004", "乙段"),
	)
	plan := Build(doc)
	child := plan.Sections[0].Children[0]
	plan.Sections[0].Children = []docmodel.Section{}
	plan.Sections = append(plan.Sections, child)

	diff := Compare(doc, Build(doc), plan)
	var found *SectionChange
	for i := range diff.Sections {
		if diff.Sections[i].Kind == "reparented" && diff.Sections[i].ID == child.ID {
			found = &diff.Sections[i]
		}
	}
	if found == nil {
		t.Fatalf("应报告换了上级章节: %+v", diff.Sections)
	}
	if !strings.Contains(found.BaselineParent, "一、甲") {
		t.Errorf("应指出原文的父章节: %+v", *found)
	}
	if found.Parent != "顶层" {
		t.Errorf("应指出它现在在顶层: %+v", *found)
	}
	if found.BaselineParentHeadingNodeID != "n0001" {
		t.Errorf("应给出原文父章节的标题节点，供一键挂回: %+v", *found)
	}
}

// TestCompareReportsOneCardForLevelMove: a chapter moved deeper changes both
// its level and its parent. One structural act must produce one card, not two.
func TestCompareReportsOneCardForLevelMove(t *testing.T) {
	doc := docOf(
		p("n0001", "关于甲的通知"),
		h("n0002", "甲", 1), p("n0003", "甲段"),
		h("n0004", "乙", 1), p("n0005", "乙段"),
	)
	plan := Build(doc)
	child := plan.Sections[1]
	child.Level = 2
	parent := plan.Sections[0]
	parent.Children = append(parent.Children, child)
	plan.Sections = []docmodel.Section{parent}

	diff := Compare(doc, Build(doc), plan)
	var releveled, reparented int
	for _, c := range diff.Sections {
		switch c.Kind {
		case "releveled":
			releveled++
		case "reparented":
			reparented++
		}
	}
	if releveled != 1 || reparented != 0 {
		t.Fatalf("层级变化只应报一条: %+v", diff.Sections)
	}
}

// TestCompareReportsModelMeta: an element the model wrote itself has no source
// paragraph, so it must be listed — otherwise a 主送机关 the author never
// wrote appears in the output with nothing to review.
func TestCompareReportsModelMeta(t *testing.T) {
	doc := docOf(h("n0001", "一、甲", 1), p("n0002", "甲段"))
	plan := Build(doc)
	plan.Meta.Recipient = "各县（市、区）人民政府"

	diff := Compare(doc, Build(doc), plan)
	if len(diff.Meta) != 1 || diff.Meta[0].Label != "主送机关" {
		t.Fatalf("应报告模型自拟主送机关: %+v", diff.Meta)
	}
}

// TestCompareSuppressesShadowedMeta: a bound element wins over the meta value,
// so a shadowed value never reaches the page and must not be listed as a change.
func TestCompareSuppressesShadowedMeta(t *testing.T) {
	doc := docOf(h("n0001", "一、甲", 1), p("n0002", "甲段"))
	plan := Build(doc)
	plan.Elements = append(plan.Elements, docmodel.ElementBinding{
		Role: docmodel.RoleRecipient, NodeID: "n0002",
	})
	plan.Sections[0].BodyNodeIDs = []string{}
	plan.Meta.Recipient = "被绑定段落盖住的值"

	diff := Compare(doc, Build(doc), plan)
	if len(diff.Meta) != 0 {
		t.Fatalf("被绑定盖住的 meta 不应列出: %+v", diff.Meta)
	}
}

// TestCompareGroupsMovesForFlatBaseline: an upload with no headings has one
// synthetic bucket, so every paragraph "moves" once the model builds chapters.
// Reporting those one by one would bury the real changes in noise, but
// dropping them entirely would hide which chapter absorbed which paragraphs —
// the single most important thing to check in this workflow. They are grouped
// by destination chapter instead.
func TestCompareGroupsMovesForFlatBaseline(t *testing.T) {
	doc := docOf(p("n0001", "一段话。"), p("n0002", "另一段话。"))
	plan := Build(doc)
	plan.Sections = []docmodel.Section{
		{ID: "s1", Title: "一、甲", Level: 1, BodyNodeIDs: []string{"n0001"}},
		{ID: "s2", Title: "二、乙", Level: 1, BodyNodeIDs: []string{"n0002"}},
	}

	diff := Compare(doc, Build(doc), plan)
	if !diff.BaselineFlat {
		t.Fatalf("无标题原文应标记 baseline_flat")
	}
	if len(diff.Moves) != 2 {
		t.Fatalf("应按目标章节汇总成两条: %+v", diff.Moves)
	}
	for _, m := range diff.Moves {
		if m.Count != 1 || m.To == "" {
			t.Errorf("汇总条目应带段落数与目标章节: %+v", m)
		}
	}
	var added int
	for _, c := range diff.Sections {
		if c.Kind == "added" || c.Kind == "synthesized" {
			added++
		}
	}
	if added == 0 {
		t.Errorf("新建章节仍应出现在章节变化里: %+v", diff.Sections)
	}
}

// TestCompareGroupsFlatMovesByChapter: several paragraphs landing in the same
// new chapter are one review item, not one card each.
func TestCompareGroupsFlatMovesByChapter(t *testing.T) {
	doc := docOf(
		p("n0001", "第一段。"), p("n0002", "第二段。"), p("n0003", "第三段。"),
	)
	plan := Build(doc)
	plan.Sections = []docmodel.Section{
		{ID: "s1", Title: "一、甲", Level: 1, BodyNodeIDs: []string{"n0001", "n0002"}},
		{ID: "s2", Title: "二、乙", Level: 1, BodyNodeIDs: []string{"n0003"}},
	}

	diff := Compare(doc, Build(doc), plan)
	if len(diff.Moves) != 2 {
		t.Fatalf("应按章节汇总成两条: %+v", diff.Moves)
	}
	if diff.Moves[0].Count != 2 || diff.Moves[1].Count != 1 {
		t.Errorf("汇总数不对: %+v", diff.Moves)
	}
}

// TestCompareReportsBodyOrder: a section whose paragraphs were reordered is a
// structural change, and the UI needs the section id to offer a restore.
func TestCompareReportsBodyOrder(t *testing.T) {
	doc := docOf(
		h("n0001", "一、甲", 1), p("n0002", "第一段"), p("n0003", "第二段"),
	)
	plan := Build(doc)
	plan.Sections[0].BodyNodeIDs = []string{"n0003", "n0002"}

	diff := Compare(doc, Build(doc), plan)
	if len(diff.BodyOrders) != 1 || diff.BodyOrders[0].SectionID == "" {
		t.Fatalf("应报告节内段序变化: %+v", diff.BodyOrders)
	}
}
