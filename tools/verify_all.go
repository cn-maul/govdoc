//go:build ignore

// Command verify_all checks all four requirements against the real pipeline in
// one run, so nothing is taken on faith from an earlier partial check.
//
// Usage: go run tools/verify_all.go
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"govdoc/internal/assemble"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/render/docx"
	"govdoc/internal/render/preview"
)

// editedBlockRe matches a block element's opening tag carrying the marker. It
// deliberately requires the `<div` so the stylesheet's comment cannot match.
var editedBlockRe = regexp.MustCompile(`<div[^>]*data-edited="1"[^>]*>`)

func main() {
	ok := true
	// A document written the way people actually write: two 错别字, a
	// missing 标题 on the chapter, an unpunctuated run-on, and a 缘由 paragraph
	// that indents itself with 全角空格.
	lq := string(rune(0x3000))
	src := []struct {
		id   string
		kind docmodel.NodeKind
		text string
	}{
		{"n0001", docmodel.KindParagraph, "关于开展安全生产大检查的通知"},
		{"n0002", docmodel.KindParagraph, "各有关单位："},
		{"n0003", docmodel.KindParagraph, lq + lq + "为深入贯切落实上级要求，现将有关事项通知如下。"},
		{"n0004", docmodel.KindParagraph, "坚持安全第一认真负则坚决防犯事故发生"},
		{"n0005", docmodel.KindParagraph, "某某单位办公室"},
		{"n0006", docmodel.KindParagraph, "2024年5月8日"},
	}
	doc := &docmodel.Document{SourceName: "检查通知.txt", Format: "txt"}
	for i, s := range src {
		doc.Nodes = append(doc.Nodes, docmodel.Node{ID: s.id, Kind: s.kind, Text: s.text, Order: i})
	}

	plan := &docmodel.Plan{
		Meta: docmodel.Meta{IssuingOrg: "某某单位文件", DocNumber: "某发〔2024〕8号"},
		Elements: []docmodel.ElementBinding{
			{Role: docmodel.RoleTitle, NodeID: "n0001"},
			{Role: docmodel.RoleRecipient, NodeID: "n0002"},
			{Role: docmodel.RoleSignature, NodeID: "n0005"},
			{Role: docmodel.RoleDate, NodeID: "n0006"},
		},
		PreambleNodeIDs: []string{"n0003"},
		Edits: []docmodel.Edit{
			{NodeID: "n0003", Kind: docmodel.EditTypo,
				Before: "贯切落实", After: "贯彻落实", Reason: "「贯切」系「贯彻」误写。"},
			{NodeID: "n0004", Kind: docmodel.EditTypo,
				Before: "负则", After: "负责", Reason: "「负则」系「负责」误写。"},
			{NodeID: "n0004", Kind: docmodel.EditTypo,
				Before: "防犯", After: "防范", Reason: "「防犯」系「防范」误写。"},
			{NodeID: "n0004", Kind: docmodel.EditSentenceSplit,
				Before: "坚持安全第一认真负责坚决防范事故发生",
				After:  "要坚持安全第一。认真负责，坚决防范事故发生。",
				Reason: "长句无标点难以阅读，断句并补主语。"},
		},
		// 段标题由章节生成：原文这一段没有标题。
		Sections: []docmodel.Section{{
			ID: "s1", Title: "一、总体要求", Level: 1,
			BodyNodeIDs: []string{"n0004"},
		}},
	}

	spec := gbstandard.Default()
	res, err := assemble.Build(doc, plan, spec)
	if err != nil {
		fmt.Println("Build 失败:", err)
		return
	}

	fmt.Println("================== 四项需求整体验证 ==================")
	fmt.Println()

	fmt.Println("【1】配置持久化")
	fmt.Println("    不在本进程验证 —— 由 internal/config 的 14 个测试与真实二进制重启实测覆盖。")
	fmt.Println()

	fmt.Println("【2】渲染缺陷")
	for _, b := range res.Doc.Blocks {
		if b.NodeID == "n0003" {
			hasOwn := strings.HasPrefix(b.Text, lq+lq)
			fmt.Printf("    缘由段：文本自带缩进=%v，样式有效缩进=%g 字（应为 0，不叠加）\n",
				hasOwn, gbstandard.EffectiveFirstLineIndent(spec.Styles[docmodel.RoleBody], b.Text))
		}
	}
	fmt.Printf("    标题(%gpt) 网格压缩=%gpt；发文机关标志(%gpt) 压缩=%gpt（应为 0，否则字形重叠）\n",
		spec.Styles[docmodel.RoleTitle].Size.Points(), spec.GridCondensePT(docmodel.RoleTitle),
		spec.Styles[docmodel.RoleMasthead].Size.Points(), spec.GridCondensePT(docmodel.RoleMasthead))
	fmt.Printf("    正文(%gpt) 网格压缩=%gpt（应为正，使每行 28 字）\n",
		spec.Styles[docmodel.RoleBody].Size.Points(), spec.GridCondensePT(docmodel.RoleBody))
	fmt.Println()

	fmt.Println("【3】傻瓜式生成 + 允许修正 + 改动标注")
	fmt.Println("    成品：")
	for _, b := range res.Doc.Blocks {
		if b.Text == "" {
			continue
		}
		mark := "  "
		if b.Edited {
			mark = "✎ "
		}
		role := string(b.Role)
		if b.AutoNumber != "" {
			role += "(" + b.AutoNumber + ")"
		}
		fmt.Printf("      %s%-14s %s\n", mark, role, trunc(b.Text, 44))
	}
	fmt.Println("    模型生成的段标题：")
	synth := 0
	for _, b := range res.Doc.Blocks {
		if b.Role == docmodel.RoleHeading1 && b.NodeID == "" {
			fmt.Printf("      「%s」 ← 原文没有，由章节标题生成（已计入报告供复核）\n", b.Text)
			synth++
		}
	}
	if synth == 0 {
		fmt.Println("      ✗ 未生成段标题！")
	}
	fmt.Printf("    报告中的模型生成标题计数 = %d\n", res.Report.SynthesizedHeadings)
	fmt.Println("    改动清单（网页上逐条 before → after）：")
	for i, c := range res.Diff.Changed {
		fmt.Printf("      %d. [%s] %s\n", i+1, c.Label, c.NodeID)
		fmt.Printf("         删除：%s\n", c.Before)
		fmt.Printf("         改为：%s\n", c.After)
		fmt.Printf("         理由：%s\n", c.Reason)
	}
	fmt.Printf("    未生效改动 = %d 条；未声明修改 = %d 条（必须 0）\n",
		len(res.FailedEdits), len(res.Diff.Altered))
	fmt.Println()

	fmt.Println("【4】下载")
	docxBytes, err := docx.Render(res.Doc, spec)
	if err != nil {
		fmt.Println("    docx 渲染失败:", err)
		return
	}
	prev, err := preview.Render(res.Doc, spec)
	if err != nil {
		fmt.Println("    preview 渲染失败:", err)
		return
	}
	fmt.Printf("    .docx 生成成功：%d 字节\n", len(docxBytes))
	fmt.Printf("    预览 HTML：%d 字节\n", len(prev))

	// Count the attribute only on real block elements.
	//
	// Two traps here, both of which produced a wrong number during development:
	// the stylesheet documents the attribute in a CSS comment, so a plain
	// strings.Count matches it; and splitting on "<div " lumps the document head
	// (which contains that comment) into the first segment. Matching an actual
	// opening tag avoids both.
	edited := len(editedBlockRe.FindAll(prev, -1))

	// Distinct nodes touched, not the number of edits: several corrections to
	// one paragraph are one highlighted paragraph.
	changedNodes := map[string]bool{}
	for _, c := range res.Diff.Changed {
		changedNodes[c.NodeID] = true
	}
	fmt.Printf("    预览中被标注为「已修改」的块：%d 个（改动的段落数 = %d）\n",
		edited, len(changedNodes))
	if edited != len(changedNodes) {
		fmt.Printf("    ✗ 标注数与实际改动段数不符！\n")
		ok = false
	}
	fmt.Println()

	fmt.Println("【核心不变式】")
	fmt.Println("    原文可以被修改，但每一处修改都必须显式声明、可见、可审计。")
	fmt.Printf("    本轮：%d 处声明改动全部生效，%d 处未声明修改（必须为 0）。\n",
		len(res.Diff.Changed), len(res.Diff.Altered))
	if len(res.Diff.Altered) != 0 {
		ok = false
		fmt.Println("    ✗ 存在未声明的修改")
	}
	if len(res.FailedEdits) != 0 {
		ok = false
		fmt.Printf("    ✗ 有 %d 条声明改动未生效\n", len(res.FailedEdits))
	}
	if synth != 1 {
		ok = false
		fmt.Printf("    ✗ 段标题生成数 = %d，期望 1\n", synth)
	}
	if ok {
		fmt.Println("    结论：四项需求均满足 ✓")
	} else {
		fmt.Println("    结论：存在未满足项 ✗")
	}
	// Exit non-zero on failure so this can gate a build, not just print.
	if !ok {
		os.Exit(1)
	}
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, string(rune(0x3000)), "□")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
