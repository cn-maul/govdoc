// Command e2e exercises the whole GovDoc pipeline against a running server
// with a stubbed model, then verifies the produced .docx with python-docx.
//
// It exists because the unit tests each check one stage; the promise the user
// actually cares about spans all of them (原文逐字不变 + 国标格式), and only
// an end-to-end run can demonstrate that the stages agree with each other.
//
// Usage:
//
//	go run ./tools/e2e            # drives the packages directly
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"govdoc/internal/assemble"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/render/docx"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e 失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	outDir, err := os.MkdirTemp("", "govdoc-e2e-")
	if err != nil {
		return err
	}
	fmt.Printf("工作目录: %s\n\n", outDir)

	// ---- 1. 模拟一份"排版混乱"的公文源文档 ----------------------------
	// The text deliberately includes the kind of content a real upload has:
	// a title, a 主送机关, chapters whose numbering the author typed by hand,
	// body paragraphs with Chinese punctuation and full-width spaces, and a
	// closing 署名/日期. The awkward characters are what prove the verbatim
	// guarantee is real rather than approximate.
	doc := &docmodel.Document{
		SourceName: "关于开展安全生产检查的通知.docx",
		Format:     "docx",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "关于开展安全生产大检查的通知", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "各县（市、区）人民政府，市直各部门：", Order: 1},
			{ID: "n0003", Kind: docmodel.KindParagraph, Text: "为深入贯彻落实上级关于安全生产工作的部署要求，经研究决定，在全市范围内开展安全生产大检查。现将有关事项通知如下。", Order: 2},
			{ID: "n0004", Kind: docmodel.KindParagraph, Text: "一、总体要求", Order: 3},
			{ID: "n0005", Kind: docmodel.KindParagraph, Text: "坚持“人民至上、生命至上”，全面落实安全生产责任制，坚决防范遏制重特大事故。", Order: 4},
			{ID: "n0006", Kind: docmodel.KindParagraph, Text: "二、检查范围与重点", Order: 5},
			{ID: "n0007", Kind: docmodel.KindParagraph, Text: "（一）检查范围。全市各类生产经营单位，重点是危险化学品、矿山、建筑施工、交通运输等行业领域。", Order: 6},
			{ID: "n0008", Kind: docmodel.KindParagraph, Text: "（二）检查重点。安全生产责任制落实情况；风险分级管控与隐患排查治理双重预防机制建设情况；应急预案制定与演练情况。", Order: 7},
			{ID: "n0009", Kind: docmodel.KindParagraph, Text: "三、工作要求", Order: 8},
			{ID: "n0010", Kind: docmodel.KindParagraph, Text: "1.加强组织领导。各地区、各部门要高度重视，主要负责同志要亲自部署、亲自督办。", Order: 9},
			{ID: "n0011", Kind: docmodel.KindParagraph, Text: "2.严格执法检查。对检查发现的违法违规行为，要依法依规严肃查处，绝不姑息。", Order: 10},
			{ID: "n0012", Kind: docmodel.KindParagraph, Text: "3.强化宣传引导。要充分运用各类媒体，广泛宣传安全生产法律法规和安全知识。", Order: 11},
			{ID: "n0013", Kind: docmodel.KindTable, Order: 12,
				// Text is left empty, exactly as internal/parse produces it:
				// the payload is Rows, and inventing a flattened sentence for
				// Text would violate the verbatim rule at the source.
				Rows: [][]string{
					{"检查项目", "责任单位", "完成时限"},
					{"危险化学品专项检查", "市应急管理局", "2024年6月底前"},
					{"建筑施工专项检查", "市住房和城乡建设局", "2024年7月底前"},
					{"交通运输专项检查", "市交通运输局", "2024年8月底前"},
				},
				HeaderRow: true},
			{ID: "n0014", Kind: docmodel.KindParagraph, Text: "某某市人民政府办公室", Order: 13},
			{ID: "n0015", Kind: docmodel.KindParagraph, Text: "2024年5月8日", Order: 14},
		},
	}

	// ---- 2. 模拟 LLM 产出的结构方案 -----------------------------------
	// The model's job is to *group*, and here it does the two things the
	// prompt demands: binds the document's own 标题/署名/日期 to their element
	// slots, and references every remaining paragraph exactly once, reusing
	// the headings the author already wrote (heading_node_id) instead of
	// retyping them.
	plan := &docmodel.Plan{
		Meta: docmodel.Meta{
			IssuingOrg: "某某市人民政府办公室文件",
			DocNumber:  "某政办发〔2024〕15号",
			CC:         "市委办公室，市人大常委会办公室，市政协办公室。",
			Printer:    "某某市人民政府办公室",
			PrintDate:  "2024年5月9日",
		},
		Elements: []docmodel.ElementBinding{
			{Role: docmodel.RoleTitle, NodeID: "n0001"},
			{Role: docmodel.RoleRecipient, NodeID: "n0002"},
			{Role: docmodel.RoleSignature, NodeID: "n0014"},
			{Role: docmodel.RoleDate, NodeID: "n0015"},
		},
		Sections: []docmodel.Section{
			{
				ID: "s1", Title: "一、总体要求", Level: 1,
				HeadingNodeID: "n0004", BodyNodeIDs: []string{"n0005"},
			},
			{
				ID: "s2", Title: "二、检查范围与重点", Level: 1,
				HeadingNodeID: "n0006", BodyNodeIDs: []string{"n0007", "n0008"},
			},
			{
				ID: "s3", Title: "三、工作要求", Level: 1,
				HeadingNodeID: "n0009", BodyNodeIDs: []string{"n0010", "n0011", "n0012", "n0013"},
			},
		},
		// The 缘由 paragraph belongs under 主送机关, before the first
		// heading. Modelling it as a chapter would fabricate a heading the
		// author never wrote.
		PreambleNodeIDs: []string{"n0003"},
		Notes:           []string{"“一、总体要求”等标题沿用原文，未作改写。"},
	}

	// ---- 3. 校验 -------------------------------------------------------
	fmt.Println("【1】结构方案校验")
	report := docmodel.VerifyPlan(doc, plan)
	fmt.Printf("  %s\n", report.Summary())
	fmt.Printf("  正文节点 %d 个，已覆盖 %d 个，重复 %d 个，无效引用 %d 个\n",
		report.ContentNodes, report.CoveredNodes, report.DuplicatedNodes, report.UnknownRefs)
	fmt.Printf("  绑定公文要素 %d 个，模型生成标题 %d 个\n",
		report.BoundElements, report.SynthesizedHeadings)
	if !report.OK() {
		for _, d := range report.Errors() {
			fmt.Printf("  ✗ [%s] %s\n", d.Code, d.Message)
		}
		return fmt.Errorf("方案未通过校验")
	}
	// 正文段落：n0003 plus the three chapters' bodies.
	fmt.Println("  ✓ 校验通过：无内容丢失、无重复、无改写")
	fmt.Println()

	// ---- 4. 装配 -------------------------------------------------------
	fmt.Println("【2】装配为渲染文档")
	spec := gbstandard.Default()
	spec.Direction = gbstandard.DirDown

	res, err := assemble.Build(doc, plan, spec)
	if err != nil {
		return fmt.Errorf("装配失败: %w", err)
	}
	fmt.Printf("  生成 %d 个排版块\n", len(res.Doc.Blocks))
	roleCount := map[docmodel.Role]int{}
	for _, b := range res.Doc.Blocks {
		roleCount[b.Role]++
	}
	for _, r := range []docmodel.Role{
		docmodel.RoleMasthead, docmodel.RoleDocNumber, docmodel.RoleRedLine,
		docmodel.RoleTitle, docmodel.RoleRecipient,
		docmodel.RoleHeading1, docmodel.RoleBody, docmodel.RoleSignature,
		docmodel.RoleDate, docmodel.RoleCC, docmodel.RolePrinter,
	} {
		if n := roleCount[r]; n > 0 {
			fmt.Printf("    %-16s × %d\n", r, n)
		}
	}
	fmt.Printf("  自动生成（非原文）的文字 %d 处\n", len(res.SynthText))
	fmt.Println()

	// ---- 5. 逐字比对 ---------------------------------------------------
	fmt.Println("【3】逐字比对（核心承诺：不改变原文）")
	diff := res.Diff
	if !diff.Clean() {
		fmt.Printf("  ✗ 丢失 %v\n  ✗ 改写 %v\n", diff.Missing, diff.Altered)
		return fmt.Errorf("逐字比对未通过")
	}
	fmt.Println("  ✓ 所有原文段落均逐字出现在成品中（无丢失、无改写）")
	fmt.Printf("  ✓ 成品中非原文的文字共 %d 处（均为版头要素与层次序数编号）：\n", len(diff.Extra))
	for _, e := range diff.Extra {
		fmt.Printf("      · %s\n", e)
	}
	fmt.Println()

	// ---- 6. 渲染 docx --------------------------------------------------
	fmt.Println("【4】生成 .docx")
	data, err := docx.Render(res.Doc, spec)
	if err != nil {
		return fmt.Errorf("渲染失败: %w", err)
	}
	outPath := filepath.Join(outDir, "关于开展安全生产大检查的通知.docx")
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("  已写出 %s（%d 字节）\n", outPath, len(data))

	// A manifest the verification script reads, so the numbers asserted are
	// the ones this run actually produced rather than constants.
	manifest := map[string]any{
		"docx":        outPath,
		"source_name": doc.SourceName,
		"nodes":       len(doc.Nodes),
		"content":     len(doc.ContentIDs()),
		"blocks":      len(res.Doc.Blocks),
		"synth":       res.SynthText,
		"extra":       diff.Extra,
		"texts":       sourceTexts(doc),
	}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	mpath := filepath.Join(outDir, "manifest.json")
	if err := os.WriteFile(mpath, mb, 0o644); err != nil {
		return err
	}
	fmt.Printf("  清单 %s\n\n", mpath)

	fmt.Println("【5】python-docx 读回验证")
	if err := verifyDocx(outPath); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("端到端验证全部通过 ✓")
	fmt.Printf("输出目录: %s\n", outDir)
	return nil
}

// sourceTexts returns the source prose that must appear verbatim in the
// generated document, plus the table cells that must appear as cell text.
//
// Tables are gathered by cell rather than by Node.Text on purpose: the parser
// leaves a table's Text empty (flattening a cell matrix into a sentence would
// itself be a fabrication), so the payload lives in Rows. Mirrors
// docmodel.VerifyVerbatim's per-kind accounting.
func sourceTexts(doc *docmodel.Document) []string {
	var out []string
	for _, n := range doc.ContentNodes() {
		switch n.Kind {
		case docmodel.KindTable:
			for _, row := range n.Rows {
				for _, cell := range row {
					if strings.TrimSpace(cell) != "" {
						out = append(out, cell)
					}
				}
			}
		case docmodel.KindImage:
			// An image is accounted by its bytes, not by text; its caption
			// (if the source had one) is a separate paragraph node.
			continue
		default:
			out = append(out, n.Text)
		}
	}
	return out
}

// verifyDocx shells out to python-docx and checks the国标 geometry and the
// verbatim guarantee against the real file.
func verifyDocx(path string) error {
	script := `
import io, json, sys, zipfile, re
from docx import Document

# Force UTF-8 on stdout: the default console encoding on a Chinese Windows
# install is GBK, which mangles the 仿宋 font name the checks look for.
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8")

d = Document(sys.argv[1])
sec = d.sections[0]
mm = lambda v: round(v / 36000.0, 3)

# python-docx returns line spacing as a Length (EMU-scaled) when the rule is
# EXACTLY, but as a float multiple when it is MULTIPLE. Normalising to points
# here keeps the Go-side assertion about the 国标 grid (≈28.99pt) meaningful.
def line_pt(pf):
    v = pf.line_spacing
    if v is None:
        return None
    try:
        return round(v.pt, 4)
    except AttributeError:
        return None

# --- 页面几何 ---
geo = {
    "page_w": mm(sec.page_width), "page_h": mm(sec.page_height),
    "top": mm(sec.top_margin), "bottom": mm(sec.bottom_margin),
    "left": mm(sec.left_margin), "right": mm(sec.right_margin),
}

# --- 正文样式 ---
# The writer names styles with the 公文 element name (正文, 标题, 一级标题（一、）…)
# rather than an English style id, so the lookup matches on that name. Falling
# back to the first non-empty paragraph keeps the check meaningful if the
# naming ever changes.
body = None
for p in d.paragraphs:
    name = p.style.name if p.style is not None else ""
    if name == "正文" and p.text.strip():
        body = p
        break
if body is None:
    for p in d.paragraphs:
        if p.text.strip():
            body = p
            break

body_info = {}
if body is not None:
    r = body.runs[0] if body.runs else None
    body_info = {
        "style": body.style.name if body.style is not None else None,
        "size_pt": r.font.size.pt if (r is not None and r.font.size) else None,
        "east_asia": r._element.rPr.rFonts.get(
            "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}eastAsia")
            if (r is not None and r._element.rPr is not None and r._element.rPr.rFonts is not None) else None,
        "line": line_pt(body.paragraph_format),
        "rule": body.paragraph_format.line_spacing_rule.name if body.paragraph_format.line_spacing_rule else None,
        "first_line_pt": body.paragraph_format.first_line_indent.pt if body.paragraph_format.first_line_indent else None,
    }

# --- 标题样式（二号小标宋居中）---
title_info = {}
for p in d.paragraphs:
    name = p.style.name if p.style is not None else ""
    if name == "标题" and p.text.strip():
        r = p.runs[0] if p.runs else None
        title_info = {
            "size_pt": r.font.size.pt if (r is not None and r.font.size) else None,
            "align": str(p.paragraph_format.alignment),
            "text": p.text,
        }
        break

# --- 表格 ---
tables = []
for t in d.tables:
    tables.append([[c.text for c in row.cells] for row in t.rows])

# --- document.xml 中的正文文本（逐字校验用）---
with zipfile.ZipFile(sys.argv[1]) as z:
    xml = z.read("word/document.xml").decode("utf-8")
    names = sorted(z.namelist())
# One entry per paragraph, each being the concatenation of its w:t runs.
# The tag pattern must be <w:t> or <w:t ...>, NOT <w:t[^>]*> — the loose form
# also matches <w:tabs>, <w:top> and <w:tblPr>, which silently injected raw
# XML into the extracted strings and made every verbatim comparison fail.
paras = re.findall(r"<w:p[ >].*?</w:p>", xml, re.S)
ptexts = []
for p in paras:
    ts = re.findall(r"<w:t(?:\s[^>]*)?>(.*?)</w:t>", p, re.S)
    ptexts.append("".join(ts))
# Table cell text lives inside w:tbl, which the paragraph regex above does not
# descend into as a unit; collect all runs so a table's cells are searchable.
all_runs = re.findall(r"<w:t(?:\s[^>]*)?>(.*?)</w:t>", xml, re.S)

print(json.dumps({
    "geo": geo, "body": body_info, "title": title_info, "tables": tables,
    "entries": names, "paras": ptexts, "runs": all_runs,
}, ensure_ascii=False))
`
	// Write the script beside the manifest so a reviewer can re-run it.
	dir := filepath.Dir(path)
	scriptPath := filepath.Join(dir, "verify.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return err
	}

	out, err := runPython(scriptPath, path)
	if err != nil {
		return err
	}

	var got struct {
		Geo struct {
			PageW  float64 `json:"page_w"`
			PageH  float64 `json:"page_h"`
			Top    float64 `json:"top"`
			Bottom float64 `json:"bottom"`
			Left   float64 `json:"left"`
			Right  float64 `json:"right"`
		} `json:"geo"`
		Body struct {
			Style       string   `json:"style"`
			SizePt      *float64 `json:"size_pt"`
			EastAsia    string   `json:"east_asia"`
			Line        *float64 `json:"line"`
			Rule        string   `json:"rule"`
			FirstLinePt *float64 `json:"first_line_pt"`
		} `json:"body"`
		Title struct {
			SizePt *float64 `json:"size_pt"`
			Align  string   `json:"align"`
		} `json:"title"`
		Tables  [][][]string `json:"tables"`
		Entries []string     `json:"entries"`
		Paras   []string     `json:"paras"`
		Runs    []string     `json:"runs"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		return fmt.Errorf("读回结果无法解析: %w\n%s", err, out)
	}

	fail := 0
	check := func(name string, ok bool, detail string) {
		mark := "✓"
		if !ok {
			mark = "✗"
			fail++
		}
		fmt.Printf("  %s %-22s %s\n", mark, name, detail)
	}
	near := func(a float64, b float64, tol float64) bool {
		d := a - b
		return d < tol && d > -tol
	}

	check("纸张 A4", near(got.Geo.PageW, 210, 0.5) && near(got.Geo.PageH, 297, 0.5),
		fmt.Sprintf("%.2f×%.2f mm（标准 210×297）", got.Geo.PageW, got.Geo.PageH))
	check("上边距 37mm", near(got.Geo.Top, 37, 0.5), fmt.Sprintf("%.2f mm", got.Geo.Top))
	check("下边距 35mm", near(got.Geo.Bottom, 35, 0.5), fmt.Sprintf("%.2f mm", got.Geo.Bottom))
	check("左边距 28mm", near(got.Geo.Left, 28, 0.5), fmt.Sprintf("%.2f mm", got.Geo.Left))
	check("右边距 26mm", near(got.Geo.Right, 26, 0.5), fmt.Sprintf("%.2f mm", got.Geo.Right))

	sizeOK := got.Body.SizePt != nil && near(*got.Body.SizePt, 16, 0.01)
	detail := "未找到正文段落"
	if got.Body.SizePt != nil {
		detail = fmt.Sprintf("%.2f pt（三号=16）", *got.Body.SizePt)
	}
	check("正文三号", sizeOK, detail)

	check("正文仿宋", strings.Contains(got.Body.EastAsia, "仿宋"),
		fmt.Sprintf("eastAsia=%s", got.Body.EastAsia))

	lineOK := got.Body.Line != nil && near(*got.Body.Line, 28.99, 0.05)
	lineDetail := "无固定行距"
	if got.Body.Line != nil {
		lineDetail = fmt.Sprintf("%.3f pt，rule=%s（标准 225mm/22≈28.99）", *got.Body.Line, got.Body.Rule)
	}
	check("固定行距", lineOK && strings.Contains(got.Body.Rule, "EXACT"), lineDetail)

	indentOK := got.Body.FirstLinePt != nil && near(*got.Body.FirstLinePt, 32, 0.5)
	indentDetail := "无首行缩进"
	if got.Body.FirstLinePt != nil {
		indentDetail = fmt.Sprintf("%.2f pt = 2 字（三号 16pt×2）", *got.Body.FirstLinePt)
	}
	check("首行缩进 2 字", indentOK, indentDetail)

	// 标题：二号小标宋居中（§7.3.1）
	titleOK := got.Title.SizePt != nil && near(*got.Title.SizePt, 22, 0.01)
	titleDetail := "未找到标题段落"
	if got.Title.SizePt != nil {
		titleDetail = fmt.Sprintf("%.2f pt（二号=22），对齐=%s", *got.Title.SizePt, got.Title.Align)
	}
	check("标题二号居中", titleOK && strings.Contains(got.Title.Align, "CENTER"), titleDetail)

	check("表格", len(got.Tables) == 1 && len(got.Tables[0]) == 4,
		fmt.Sprintf("%d 个表格，%d 行", len(got.Tables), firstLen(got.Tables)))

	hasDoc := false
	for _, e := range got.Entries {
		if e == "word/document.xml" {
			hasDoc = true
		}
	}
	check("docx 结构", hasDoc, fmt.Sprintf("%d 个部件", len(got.Entries)))

	// 逐字校验：每个原文段落必须作为成品中某个段落的（子）串出现。
	// Both the per-paragraph concatenation and every individual run are
	// searched, because a table's cells live in runs that are not part of any
	// paragraph-level match, and a long paragraph may be split across runs.
	var texts []string
	if mb, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		var m struct {
			Texts []string `json:"texts"`
		}
		if json.Unmarshal(mb, &m) == nil {
			texts = m.Texts
		}
	}
	joined := strings.Join(got.Paras, "\n") + "\n" + strings.Join(got.Runs, "\n")
	missing := 0
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !strings.Contains(joined, t) {
			missing++
			fmt.Printf("      ✗ 原文未逐字出现: %s\n", truncate(t, 40))
		}
	}
	check("原文逐字不变", missing == 0, fmt.Sprintf("%d/%d 个原文段落均在成品中逐字出现", len(texts)-missing, len(texts)))

	if fail > 0 {
		return fmt.Errorf("%d 项验证未通过", fail)
	}
	return nil
}

func firstLen(t [][][]string) int {
	if len(t) == 0 {
		return 0
	}
	return len(t[0])
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// runPython invokes the bundled interpreter with the script and one argument.
func runPython(script, arg string) ([]byte, error) {
	return execPython(script, arg)
}

// unusedContext keeps the context import meaningful for future expansion.
var _ = context.Background
