//go:build ignore

// Command verify_edits demonstrates requirement 3 end to end: a document with
// real 错别字 and 语病 goes through the assembler with declared edits, and the
// result shows (a) the corrections applied, (b) the rejected/accepted change
// log, and (c) that nothing else moved.
//
// Usage: go run tools/verify_edits.go
package main

import (
	"fmt"
	"strings"

	"govdoc/internal/assemble"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/render/docx"
)

func main() {
	// A document written the way people actually write: a run-on sentence, a
	// couple of 错别字, a missing 标题 for the second chapter.
	src := []struct {
		id   string
		kind docmodel.NodeKind
		text string
	}{
		{"n0001", docmodel.KindParagraph, "关于开展安全生产检查的通知"},
		{"n0002", docmodel.KindParagraph, "各有关单位："},
		{"n0003", docmodel.KindParagraph,
			"为深入贯切落实上级要求，现将有关事项通知如下。"},
		{"n0004", docmodel.KindParagraph, "一、总体要求"},
		{"n0005", docmodel.KindParagraph,
			"坚持安全第一，认真负则，坚决防犯各类事故发生，确保安全生产形式持续稳定好转。"},
		{"n0006", docmodel.KindParagraph, "某某单位办公室"},
		{"n0007", docmodel.KindParagraph, "2024年5月8日"},
	}

	doc := &docmodel.Document{SourceName: "检查通知.txt", Format: "txt"}
	for i, s := range src {
		doc.Nodes = append(doc.Nodes, docmodel.Node{
			ID: s.id, Kind: s.kind, Text: s.text, Order: i,
		})
	}

	plan := &docmodel.Plan{
		Meta: docmodel.Meta{
			IssuingOrg: "某某单位文件",
			DocNumber:  "某发〔2024〕8号",
		},
		Elements: []docmodel.ElementBinding{
			{Role: docmodel.RoleTitle, NodeID: "n0001"},
			{Role: docmodel.RoleRecipient, NodeID: "n0002"},
			{Role: docmodel.RoleSignature, NodeID: "n0006"},
			{Role: docmodel.RoleDate, NodeID: "n0007"},
		},
		PreambleNodeIDs: []string{"n0003"},
		Edits: []docmodel.Edit{
			{
				NodeID: "n0003", Kind: docmodel.EditTypo,
				Before: "贯切落实", After: "贯彻落实",
				Reason: "“贯切”系“贯彻”误写。",
			},
			{
				NodeID: "n0005", Kind: docmodel.EditTypo,
				Before: "负则", After: "负责",
				Reason: "“负则”系“负责”误写。",
			},
			{
				NodeID: "n0005", Kind: docmodel.EditTypo,
				Before: "防犯", After: "防范",
				Reason: "“防犯”系“防范”误写。",
			},
			{
				NodeID: "n0005", Kind: docmodel.EditWording,
				Before: "安全生产形式", After: "安全生产形势",
				Reason: "“形式”与“形势”混淆,此处应为“形势”。",
			},
			{
				NodeID: "n0005", Kind: docmodel.EditGrammar,
				Before: "坚持安全第一，认真负责，坚决防范各类事故发生，确保安全生产形势持续稳定好转。",
				After:  "要坚持安全第一，认真负责，坚决防范各类事故发生，确保安全生产形势持续稳定好转。",
				Reason: "缺主语，补“要”字使语句完整。",
			},
		},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、总体要求", Level: 1,
				HeadingNodeID: "n0004", BodyNodeIDs: []string{"n0005"}},
		},
		Notes: []string{"修正 4 处错别字/用词，1 处缺主语；未改动其他内容。"},
	}

	spec := gbstandard.Default()
	res, err := assemble.Build(doc, plan, spec)
	if err != nil {
		fmt.Println("Build 失败:", err)
		return
	}

	fmt.Println("=========== 需求 3 验证：傻瓜式生成 + 允许修正 + 改动可见 ==========")
	fmt.Println()

	fmt.Println("【1】原文（用户上传的样子）")
	for _, s := range src {
		if s.id == "n0001" || s.id == "n0002" || s.id == "n0003" || s.id == "n0005" {
			fmt.Printf("   %s: %s\n", s.id, s.text)
		}
	}
	fmt.Println()

	fmt.Println("【2】成品正文（修正后的样子）")
	for _, b := range res.Doc.Blocks {
		if b.NodeID == "" || b.Text == "" {
			continue
		}
		mark := "  "
		if b.Edited {
			mark = "✎ "
		}
		fmt.Printf("   %s%-18s %s\n", mark, b.Role, truncate(b.Text, 50))
	}
	fmt.Println("   （✎ 表示该段被修改过）")
	fmt.Println()

	fmt.Println("【3】改动清单（网页上会逐条标注 before → after）")
	for i, c := range res.Diff.Changed {
		fmt.Printf("   %d. [%s] %s\n", i+1, c.Label, c.NodeID)
		fmt.Printf("        删除：%s\n", c.Before)
		fmt.Printf("        改为：%s\n", c.After)
		fmt.Printf("        理由：%s\n", c.Reason)
	}
	fmt.Println()

	fmt.Println("【4】审计结论")
	fmt.Printf("   已声明并生效的改动 : %d 处\n", len(res.Diff.Changed))
	fmt.Printf("   未生效的改动       : %d 处（%v）\n", len(res.Diff.Unapplied), res.Diff.Unapplied)
	fmt.Printf("   未声明的修改       : %d 处（必须为 0）\n", len(res.Diff.Altered))
	fmt.Printf("   丢失的原文段落     : %d 处（必须为 0）\n", len(res.Diff.Missing))
	fmt.Printf("   程序生成的文字     : %d 处（版头/编号）\n", len(res.SynthText))
	if len(res.Diff.Altered) > 0 {
		fmt.Println("   ✗ 存在未声明的修改！")
	}
	fmt.Println()

	// 逐字核对：除了声明的 5 处，其余文字必须一字不差
	fmt.Println("【5】除声明改动外，原文是否一字未动")
	changed := map[string]bool{}
	for _, c := range res.Diff.Changed {
		changed[c.NodeID] = true
	}
	for _, s := range src {
		n, _ := doc.Node(s.id)
		out := ""
		for _, b := range res.Doc.Blocks {
			if b.NodeID == s.id {
				out += b.Text
			}
		}
		if out == "" {
			fmt.Printf("   %s 未被渲染（可能绑为要素，正常）\n", s.id)
			continue
		}
		if out == n.Text {
			fmt.Printf("   %s 逐字一致 ✓\n", s.id)
		} else if changed[s.id] {
			fmt.Printf("   %s 有已声明的改动 ✓（%d 处）\n", s.id, countEdits(plan, s.id))
		} else {
			fmt.Printf("   ✗ %s 被改动了但未声明！\n", s.id)
		}
	}
	fmt.Println()

	data, err := docx.Render(res.Doc, spec)
	if err != nil {
		fmt.Println("docx 渲染失败:", err)
		return
	}
	fmt.Printf("【6】生成的 .docx：%d 字节，可直接下载\n", len(data))
	fmt.Println()
	fmt.Println("结论：模型可以修正错别字/语病/用词，每一处都带 before/after/理由，")
	fmt.Println("      未声明的修改仍会被拒绝导出——「允许改」与「不能偷偷改」同时成立。")
}

func countEdits(p *docmodel.Plan, id string) int {
	return len(p.EditsFor(id))
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
