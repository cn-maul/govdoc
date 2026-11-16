package assemble

import (
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// TestBuildRecomposesUnstructuredParagraph covers the whole path a
// structureless document takes: one source paragraph is declared as three
// paragraphs in two chapters, verification accepts it, the output carries the
// declared order, and the change log reports it as a recomposition rather than
// an undeclared rewrite.
func TestBuildRecomposesUnstructuredParagraph(t *testing.T) {
	source := "为做好本年度工作现就有关事项通知如下一是提高认识加强组织领导二是明确责任抓好任务落实三是强化督导确保工作实效"
	doc := src(para("n0001", source))
	plan := &docmodel.Plan{
		Recompose: []docmodel.Recompose{{
			NodeID: "n0001",
			Parts: []string{
				"为做好本年度工作，现就有关事项通知如下。",
				"一是提高认识，加强组织领导。",
				"二是明确责任，抓好任务落实；三是强化督导，确保工作实效。",
			},
			Reason: "原文一段到底，按语义拆为三段",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、总体要求", Level: 1,
				BodyParts: []docmodel.BodyPart{{NodeID: "n0001", Part: 0}}},
			{ID: "s2", Title: "二、主要任务", Level: 1,
				BodyParts: []docmodel.BodyPart{
					{NodeID: "n0001", Part: 1},
					{NodeID: "n0001", Part: 2},
				}},
		},
	}

	rep := docmodel.VerifyPlan(doc, plan)
	if !rep.OK() {
		t.Fatalf("重组方案应通过校验: %s\n%+v", rep.Summary(), rep.Diagnostics)
	}
	if rep.RecomposedNodes != 1 || rep.RecomposedParts != 3 {
		t.Errorf("重组计数 = %d 节点 / %d 段", rep.RecomposedNodes, rep.RecomposedParts)
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !res.Diff.Clean() {
		t.Fatalf("已声明的重组不应导致逐字校验失败: %+v", res.Diff)
	}
	if len(res.Diff.Changed) != 1 {
		t.Fatalf("应记录 1 条重组改动, 实际 %+v", res.Diff.Changed)
	}
	changed := res.Diff.Changed[0]
	if changed.Kind != "recompose" || changed.Label != "重组段落" {
		t.Errorf("改动日志类型不符: %+v", changed)
	}
	if !strings.Contains(changed.After, "一是提高认识") {
		t.Errorf("改动日志应列出重组后的文字: %+v", changed)
	}

	text := res.Doc.Text()
	for _, part := range plan.Recompose[0].Parts {
		if !strings.Contains(text, part) {
			t.Errorf("输出缺少重组段落 %q:\n%s", part, text)
		}
	}
}

// TestVerifyPlanRejectsPartialRecompose: placing only some of a declared
// paragraph's parts must fail. Otherwise a model could quietly drop the
// remainder of a long paragraph while every part it did place looked valid.
func TestVerifyPlanRejectsPartialRecompose(t *testing.T) {
	doc := src(para("n0001", "第一句。第二句。第三句。"))
	plan := &docmodel.Plan{
		Recompose: []docmodel.Recompose{{
			NodeID: "n0001",
			Parts:  []string{"第一句。", "第二句。", "第三句。"},
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、要求", Level: 1,
				BodyParts: []docmodel.BodyPart{{NodeID: "n0001", Part: 0}}},
		},
	}

	rep := docmodel.VerifyPlan(doc, plan)
	if rep.OK() {
		t.Fatal("未放置全部重组分片必须导致校验失败")
	}
	var found bool
	for _, d := range rep.Diagnostics {
		if d.Code == "uncovered_body_part" {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少重组分片漏收诊断: %+v", rep.Diagnostics)
	}
}

// TestBuildRecomposeRoles: a one-block notice often starts with
// "各有关单位：" and a reason paragraph in the same node. Those two pieces
// must land in 主送机关 and the unnumbered preamble, not inside a fabricated
// chapter, and the remaining prose must keep its declared order.
func TestBuildRecomposeRoles(t *testing.T) {
	source := "各有关单位：为做好本年度工作，现就有关事项通知如下。一是提高认识，加强组织领导。二是明确责任，抓好任务落实。"
	doc := src(para("n0001", source))
	plan := &docmodel.Plan{
		Recompose: []docmodel.Recompose{{
			NodeID: "n0001",
			Parts: []string{
				"各有关单位：",
				"为做好本年度工作，现就有关事项通知如下。",
				"一是提高认识，加强组织领导。",
				"二是明确责任，抓好任务落实。",
			},
			Roles:  []string{"recipient", "preamble", "", ""},
			Reason: "一段到底，拆出主送机关与前言",
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、总体要求", Level: 1,
				BodyParts: []docmodel.BodyPart{{NodeID: "n0001", Part: 2}}},
			{ID: "s2", Title: "二、主要任务", Level: 1,
				BodyParts: []docmodel.BodyPart{{NodeID: "n0001", Part: 3}}},
		},
	}

	rep := docmodel.VerifyPlan(doc, plan)
	if !rep.OK() {
		t.Fatalf("带角色分片的方案应通过校验: %s\n%+v", rep.Summary(), rep.Diagnostics)
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !res.Diff.Clean() {
		t.Fatalf("角色分片不应导致逐字校验失败: %+v", res.Diff)
	}

	var recipient, preamble int
	var seenBody bool
	for _, b := range res.Doc.Blocks {
		switch {
		case b.Role == docmodel.RoleRecipient:
			recipient++
			if b.Text != plan.Recompose[0].Parts[0] {
				t.Errorf("主送机关文字不符: %q", b.Text)
			}
		case b.NodeID == "n0001" && b.Text == plan.Recompose[0].Parts[1]:
			preamble++
			if b.Role != docmodel.RoleBody {
				t.Errorf("前言应以无编号正文输出，实际 role=%s", b.Role)
			}
			if seenBody {
				t.Error("前言排在章节正文之后，导出顺序不符合声明")
			}
		case b.NodeID == "n0001":
			seenBody = true
		}
	}
	if recipient != 1 {
		t.Errorf("主送机关应输出 1 次，实际 %d 次", recipient)
	}
	if preamble != 1 {
		t.Errorf("正文前言应输出 1 次，实际 %d 次", preamble)
	}
}

func TestVerifyPlanRejectsBadRecomposeRoles(t *testing.T) {
	doc := src(para("n0001", "各有关单位：为做好工作，现通知如下。正文。"))
	base := docmodel.Recompose{
		NodeID: "n0001",
		Parts:  []string{"各有关单位：", "为做好工作，现通知如下。", "正文。"},
	}

	cases := []struct {
		name string
		rec  docmodel.Recompose
		code string
	}{
		{
			name: "roles 长度不符",
			rec:  docmodel.Recompose{NodeID: "n0001", Parts: base.Parts, Roles: []string{"recipient"}},
			code: "bad_recompose",
		},
		{
			name: "未知 role",
			rec:  docmodel.Recompose{NodeID: "n0001", Parts: base.Parts, Roles: []string{"title", "", ""}},
			code: "bad_recompose",
		},
		{
			name: "正文排在角色之前",
			rec:  docmodel.Recompose{NodeID: "n0001", Parts: base.Parts, Roles: []string{"", "recipient", ""}},
			code: "bad_recompose",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := &docmodel.Plan{
				Recompose: []docmodel.Recompose{tc.rec},
				Sections: []docmodel.Section{
					{ID: "s1", Title: "一、要求", Level: 1,
						BodyParts: []docmodel.BodyPart{{NodeID: "n0001", Part: 2}}},
				},
			}
			rep := docmodel.VerifyPlan(doc, plan)
			if rep.OK() {
				t.Fatal("非法 role 方案必须校验失败")
			}
			found := false
			for _, d := range rep.Diagnostics {
				if d.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Errorf("缺少 %s 诊断: %+v", tc.code, rep.Diagnostics)
			}
		})
	}
}

// A role part is already emitted through the skeleton. Listing the same part
// in body_parts would print it twice, so the per-part tally must reject it.
func TestVerifyPlanRejectsRolePartAlsoInBodyParts(t *testing.T) {
	doc := src(para("n0001", "各有关单位：为做好工作，现通知如下。正文。"))
	plan := &docmodel.Plan{
		Recompose: []docmodel.Recompose{{
			NodeID: "n0001",
			Parts:  []string{"各有关单位：", "为做好工作，现通知如下。", "正文。"},
			Roles:  []string{"recipient", "preamble", ""},
		}},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、要求", Level: 1,
				BodyParts: []docmodel.BodyPart{
					{NodeID: "n0001", Part: 0},
					{NodeID: "n0001", Part: 2},
				}},
		},
	}

	rep := docmodel.VerifyPlan(doc, plan)
	if rep.OK() {
		t.Fatal("角色分片又被放进 body_parts 时必须校验失败")
	}
	found := false
	for _, d := range rep.Diagnostics {
		if d.Code == "duplicate_body_part" {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少重复分片诊断: %+v", rep.Diagnostics)
	}
}
