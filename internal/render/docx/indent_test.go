package docx

import (
	"strings"
	"testing"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// The double-indent defect, and the character-condensation defect, both shipped
// and were only caught by looking at a generated document. These tests pin the
// fixes at the writer level so a regression fails here rather than in a user's
// printed 公文.

// TestNoDoubleIndentWhenTextProvidesIt covers the reported defect: 中文 authors
// indent a paragraph by typing two 全角空格, the parser stores them verbatim
// (correctly — they are part of the file), and the 正文 style then added its own
// 首行缩进2字, producing a four-character indent.
//
// The fix reduces the style's indent by what the text already supplies. The
// characters are NOT stripped, so the verbatim guarantee is untouched.
func TestNoDoubleIndentWhenTextProvidesIt(t *testing.T) {
	lq := string(rune(0x3000))
	spec := gbstandard.Default()

	withOwnIndent := lq + lq + "为深入贯彻落实上级要求，现将有关事项通知如下。"
	withoutIndent := "为深入贯彻落实上级要求，现将有关事项通知如下。"

	withBody := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, NodeID: "n0001", Text: withOwnIndent},
	}}
	withoutBody := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, NodeID: "n0002", Text: withoutIndent},
	}}

	dataWith, err := Render(withBody, spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	dataWithout, err := Render(withoutBody, spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	xmlWith := string(zipEntries(t, dataWith)["word/document.xml"])
	xmlWithout := string(zipEntries(t, dataWithout)["word/document.xml"])

	// The paragraph supplying its own indent must carry no first-line indent
	// from the style — otherwise the two add up.
	indWith := firstIndentOf(xmlWith, "为深入")
	if indWith != "" {
		t.Errorf("原文自带全角空格缩进时不应再施加样式缩进，实际 %s", indWith)
	}

	// The paragraph without its own indent must still get the full 2 字.
	indWithout := firstIndentOf(xmlWithout, "为深入")
	if !strings.Contains(indWithout, `w:firstLineChars="200"`) {
		t.Errorf("无自带缩进的段落仍应首行缩进 2 字，实际 %s", indWithout)
	}

	// And the 全角空格 must survive byte-for-byte: the fix is about the style's
	// indent, never about deleting the author's characters.
	if !strings.Contains(xmlWith, lq+lq) {
		t.Error("原文的全角空格被删除了 —— 修复不应改动文本")
	}
}

// TestGridCondensationDoesNotSqueezeDisplayType covers the other reported
// defect: the per-role letter-spacing was computed from each role's own size,
// so 标题 (22pt) lost 28% of its glyph advance and 发文机关标志 (36pt) lost 56%,
// making the characters visibly overlap.
//
// "每行28字" constrains the 正文 grid at 三号; centred display type is sized
// independently by the standard and must not be condensed.
func TestGridCondensationDoesNotSqueezeDisplayType(t *testing.T) {
	spec := gbstandard.Default()

	// Directly: only a role at the body's own size shares the grid.
	if got := spec.GridCondensePT(docmodel.RoleTitle); got != 0 {
		t.Errorf("标题 不应被压缩字距，实际 %.3fpt", got)
	}
	if got := spec.GridCondensePT(docmodel.RoleMasthead); got != 0 {
		t.Errorf("发文机关标志 不应被压缩字距，实际 %.3fpt", got)
	}
	if got := spec.GridCondensePT(docmodel.RoleBody); got <= 0 {
		t.Errorf("正文 应落在字符网格上（需微压缩），实际 %.3fpt", got)
	}
	// The condensation the grid applies must stay visually negligible: it
	// exists to make 28 characters fit 156mm, not to change the type.
	body := spec.GridCondensePT(docmodel.RoleBody)
	if size := spec.Styles[docmodel.RoleBody].Size.Points(); body/size > 0.03 {
		t.Errorf("正文压缩比例 %.2f%% 过大，会看出来", body/size*100)
	}

	// The written document must carry the grid once, on the section, rather
	// than per-paragraph compression that would hit the title too.
	doc := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleTitle, Text: "关于开展公文格式规范化工作的通知"},
		{Role: docmodel.RoleMasthead, Text: "国务院办公厅文件"},
		{Role: docmodel.RoleBody, Text: "正文内容。"},
	}}
	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	xml := string(zipEntries(t, data)["word/document.xml"])

	// The grid is declared once, in the sectPr.
	if n := strings.Count(xml, "<w:docGrid"); n != 1 {
		t.Errorf("docGrid 应只声明一次（节属性），实际 %d 次", n)
	}
	// No paragraph may carry a negative character spacing, which is what a
	// per-role implementation would have produced.
	if strings.Contains(xml, "<w:spacing") && strings.Contains(xml, `w:val="-"`) {
		t.Error("段落中出现负字距，说明按 role 压缩了展示性文字")
	}
}

// TestTitleKeepsItsFontSize confirms the fix did not take the other way out:
// shrinking the font to make the line fit. The standard fixes 标题 at 二号 and
// only the character advance may be condensed.
func TestTitleKeepsItsFontSize(t *testing.T) {
	spec := gbstandard.Default()
	doc := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleTitle, Text: "关于开展公文格式规范化工作的通知"},
	}}
	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	xml := string(zipEntries(t, data)["word/document.xml"])

	// 二号 = 22pt → w:sz = 44 half-points.
	if !strings.Contains(xml, `w:sz w:val="44"`) {
		t.Errorf("标题应仍为二号（w:sz=44），实际 XML:\n%s", snippet(xml))
	}
}

// TestBodyIndentStillTwoCharsWithoutOwnIndent: the fix must not weaken the norm.
// A paragraph that does not supply its own indent still gets 空二字.
func TestBodyIndentStillTwoCharsWithoutOwnIndent(t *testing.T) {
	spec := gbstandard.Default()
	doc := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, Text: "这是一段普通的正文。"},
	}}
	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	xml := string(zipEntries(t, data)["word/document.xml"])
	ind := firstIndentOf(xml, "这是一段")
	if !strings.Contains(ind, `w:firstLineChars="200"`) {
		t.Errorf("普通正文应首行缩进 2 字（w:firstLineChars=200），实际 %s", ind)
	}
	// And the point-valued twin keeps readers without char support correct:
	// 2 字 × 16pt = 32pt = 640 twips.
	if !strings.Contains(ind, `w:firstLine="640"`) {
		t.Errorf("缺少等价磅值 w:firstLine=640, 实际 %s", ind)
	}
}

// TestOwnIndentDoesNotStripText is the guarantee that makes this fix safe: the
// author's 全角空格 stay in the document. Only the style's indent changes.
func TestOwnIndentDoesNotStripText(t *testing.T) {
	lq := string(rune(0x3000))
	spec := gbstandard.Default()
	text := lq + lq + "正文内容。"
	doc := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, NodeID: "n0001", Text: text},
	}}

	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	xml := string(zipEntries(t, data)["word/document.xml"])

	// Both 全角空格 must be present, inside a run declared as space-preserving.
	if !strings.Contains(xml, `xml:space="preserve"`) {
		t.Error("含前导全角空格的段落必须使用 xml:space=\"preserve\"，否则空格会被吞掉")
	}
	if !strings.Contains(xml, lq+lq+"正文内容。") {
		t.Errorf("原文文本未逐字写入，实际 XML:\n%s", snippet(xml))
	}
}

// ---------------------------------------------------------------- helpers

// snippet shortens XML for a failure message, on rune boundaries so a Chinese
// excerpt is not cut mid-character.
func snippet(s string) string {
	const max = 400
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// firstIndentOf returns the <w:ind .../> of the first paragraph containing the
// marker, or "" when that paragraph has none.
func firstIndentOf(xml, marker string) string {
	idx := strings.Index(xml, marker)
	if idx < 0 {
		return ""
	}
	// Look backwards for the paragraph start.
	start := strings.LastIndex(xml[:idx], "<w:p>")
	if start < 0 {
		start = strings.LastIndex(xml[:idx], "<w:p ")
	}
	if start < 0 {
		return ""
	}
	para := xml[start:idx]
	i := strings.Index(para, "<w:ind ")
	if i < 0 {
		return ""
	}
	rest := para[i:]
	if e := strings.Index(rest, "/>"); e >= 0 {
		return rest[:e+2]
	}
	return ""
}
