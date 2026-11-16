package docx

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// ---------------------------------------------------------------- fixtures

// sampleDoc mirrors what internal/assemble hands the writer: a 版头, a 标题,
// three heading depths, 正文 with an AutoNumber, a real table and a picture.
// The body text is chosen to be hostile to normalisation — leading/trailing
// spaces, a tab, a newline-free run of full-width punctuation and Latin text —
// so any rewriting shows up as a mismatch.
// Fixed indices into sampleDoc's block list, kept in one place so the fixture
// and the assertions cannot drift apart.
const (
	bodyBlockIndex    = 5
	headingBlockIndex = 6
	titleBlockIndex   = 3
	tableBlockIndex   = 10
)

func sampleDoc(t *testing.T) *docmodel.Doc {
	t.Helper()

	body1 := "  现将有关情况报告如下：第一，要 坚持问题导向；第二，\t要压实责任。  "
	body2 := "一、加强组织领导（一）落实主体责任1.明确分工（1）细化措施"

	return &docmodel.Doc{
		Meta: docmodel.Meta{
			Title: "关于开展公文格式规范化工作的通知",
		},
		Blocks: []docmodel.OutBlock{
			{Role: docmodel.RoleMasthead, Text: "国务院办公厅文件", SpaceBeforeLines: 100},
			{Role: docmodel.RoleDocNumber, Text: "国办发〔2024〕15号"},
			{Role: docmodel.RoleRedLine},
			{Role: docmodel.RoleTitle, Text: "关于开展公文格式规范化工作的通知"},
			{Role: docmodel.RoleRecipient, Text: "各省、自治区、直辖市人民政府："},
			{Role: docmodel.RoleBody, Text: body1, NodeID: "n0001"},
			{Role: docmodel.RoleHeading1, Text: "加强组织领导", AutoNumber: "一、", Level: 1, NodeID: "n0002"},
			{Role: docmodel.RoleHeading2, Text: "落实主体责任", AutoNumber: "（一）", Level: 2, NodeID: "n0003"},
			{Role: docmodel.RoleHeading3, Text: "明确分工", AutoNumber: "1.", Level: 3, NodeID: "n0004"},
			{Role: docmodel.RoleBody, Text: body2, NodeID: "n0005"},
			{
				Role:   docmodel.RoleBody,
				Text:   "序号\t名称\t数量\n1\t文件\t2\n2\t表格\t3",
				NodeID: "n0006",
				Rows: [][]string{
					{"序号", "名称", "数量"},
					{"1", "文件", "2"},
					{"2", "表格", "3"},
				},
			},
			{Role: docmodel.RoleSignature, Text: "国务院办公厅"},
			{Role: docmodel.RoleDate, Text: "二〇二四年五月一日"},
			{Role: docmodel.RoleAttachment, Text: "附件：格式对照表", Image: testPNG(t)},
		},
	}
}

// testPNG builds a small valid PNG in memory; the writer reads its header to
// compute the display size, so it must be a real image.
func testPNG(t *testing.T) *docmodel.ImageRef {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for x := 0; x < 64; x++ {
		for y := 0; y < 32; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 8), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("构建测试图片失败: %v", err)
	}
	return &docmodel.ImageRef{Name: "对照表.png", MimeType: "image/png", Data: buf.Bytes()}
}

// ---------------------------------------------------------------- zip helpers

func zipEntries(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("生成的字节不是合法 zip: %v", err)
	}
	out := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开 %s 失败: %v", f.Name, err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", f.Name, err)
		}
		out[f.Name] = raw
	}
	return out
}

// xmlWellFormed parses a part with encoding/xml, which is the same check Word
// effectively performs before it refuses a file.
func xmlWellFormed(t *testing.T, name string, data []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("%s 不是合法 XML: %v", name, err)
		}
	}
}

// ---------------------------------------------------------------- a. package

func TestRenderPackageStructure(t *testing.T) {
	spec := gbstandard.Default()
	data, err := Render(sampleDoc(t), spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Render 返回空字节")
	}
	if !bytes.HasPrefix(data, []byte("PK")) {
		t.Fatalf("输出不是 zip（应以 PK 开头），前 4 字节=%q", data[:min(4, len(data))])
	}

	parts := zipEntries(t, data)
	for _, want := range []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"word/document.xml",
		"word/_rels/document.xml.rels",
		"word/styles.xml",
		"word/settings.xml",
		"word/footer1.xml",
		"docProps/core.xml",
		"docProps/app.xml",
		"word/media/image1.png",
	} {
		raw, ok := parts[want]
		if !ok {
			t.Fatalf("缺少必需部件 %s（实际有 %v）", want, keys(parts))
		}
		if len(raw) == 0 {
			t.Fatalf("部件 %s 为空", want)
		}
	}
	for name, raw := range parts {
		if strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".rels") {
			xmlWellFormed(t, name, raw)
		}
	}

	// Every relationship target must resolve to a part that is actually there.
	var rels struct {
		Relationships []struct {
			ID     string `xml:"Id,attr"`
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(parts["word/_rels/document.xml.rels"], &rels); err != nil {
		t.Fatalf("解析 document.xml.rels 失败: %v", err)
	}
	if len(rels.Relationships) == 0 {
		t.Fatal("document.xml.rels 没有任何关系")
	}
	for _, rel := range rels.Relationships {
		if strings.HasPrefix(rel.Target, "http") {
			continue
		}
		target := "word/" + strings.TrimPrefix(rel.Target, "/")
		if _, ok := parts[target]; !ok {
			t.Errorf("关系 %s 指向不存在的部件 %s", rel.ID, target)
		}
	}

	// The image must be a declared Default extension in [Content_Types].xml.
	if !bytes.Contains(parts["[Content_Types].xml"], []byte(`Extension="png"`)) {
		t.Error("[Content_Types].xml 未声明 png 图片 Default")
	}
	// The footer must be declared as an Override *and* referenced by sectPr.
	if !bytes.Contains(parts["[Content_Types].xml"], []byte("/word/footer1.xml")) {
		t.Error("[Content_Types].xml 缺少 footer1.xml 的 Override")
	}
	if !bytes.Contains(parts["word/document.xml"], []byte(`w:footerReference`)) {
		t.Error("sectPr 未引用 footer")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	spec := gbstandard.Default()
	doc := sampleDoc(t)
	first, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	second, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Error("同一输入两次渲染结果不一致（存在非确定性时间戳或 id 分配）")
	}
}

// ---------------------------------------------------------------- c. verbatim

// textRuns returns the w:t values of a paragraph, in order.
func textRuns(t *testing.T, p paragraphNode) []string {
	t.Helper()
	var out []string
	for _, r := range p.Runs {
		out = append(out, r.Text)
	}
	return out
}

// paragraphNode is a decoded w:p, minimal on purpose.
type paragraphNode struct {
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

type documentNode struct {
	Body struct {
		Paragraphs []paragraphNode `xml:"p"`
	} `xml:"body"`
}

func TestRenderVerbatim(t *testing.T) {
	spec := gbstandard.Default()
	doc := sampleDoc(t)
	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	parts := zipEntries(t, data)

	var document documentNode
	if err := xml.Unmarshal(parts["word/document.xml"], &document); err != nil {
		t.Fatalf("解析 document.xml 失败: %v", err)
	}
	paras := document.Body.Paragraphs
	if len(paras) == 0 {
		t.Fatal("document.xml 没有任何段落")
	}

	// (1) Every prose block's run concatenation must equal the source text
	// verbatim, with the AutoNumber, when present, in front of it.
	for _, blk := range doc.Blocks {
		if blk.Text == "" {
			continue
		}
		if len(blk.Rows) > 0 || blk.Image != nil {
			// Tables and images carry their payload in Rows/media and are
			// asserted by TestPythonDocxReadback and the image tests.
			continue
		}
		wantRuns := blk.AutoNumber + blk.Text
		found := false
		for _, p := range paras {
			if strings.Join(textRuns(t, p), "") == wantRuns {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("未找到与源文逐字相同的段落 role=%s text=%q（AutoNumber=%q）", blk.Role, blk.Text, blk.AutoNumber)
		}
	}

	// (2) The AutoNumber must be its own run, so the source text is never
	// glued into a modified run.
	heading := doc.Blocks[headingBlockIndex]
	if heading.AutoNumber == "" {
		t.Fatalf("夹具错误：块 %d 应有 AutoNumber", 6)
	}
	var headingPara *paragraphNode
	for i := range paras {
		if strings.Join(textRuns(t, paras[i]), "") == heading.AutoNumber+heading.Text {
			headingPara = &paras[i]
			break
		}
	}
	if headingPara == nil {
		t.Fatalf("未找到一级标题段落")
	}
	if len(headingPara.Runs) < 2 {
		t.Fatalf("AutoNumber 必须单独成一个 run：runs=%v", textRuns(t, *headingPara))
	}
	if headingPara.Runs[0].Text != heading.AutoNumber {
		t.Errorf("第一个 run 应为 AutoNumber %q，实际 %q", heading.AutoNumber, headingPara.Runs[0].Text)
	}
	if got := strings.Join(textRuns(t, *headingPara)[1:], ""); got != heading.Text {
		t.Errorf("AutoNumber 之后的 run 拼接应等于源文 %q，实际 %q", heading.Text, got)
	}

	// (3) The 正文 paragraph, which the standard requires to be verbatim, must
	// survive with its interior whitespace intact (xml:space="preserve").
	body := doc.Blocks[bodyBlockIndex]
	if !bytes.Contains(parts["word/document.xml"], []byte(`xml:space="preserve"`)) {
		t.Error("w:t 缺少 xml:space=\"preserve\"，正文内部空白可能被吞掉")
	}
	if !bytes.Contains(parts["word/document.xml"], []byte(esc(body.Text))) {
		t.Errorf("document.xml 中未逐字包含正文 %q", body.Text)
	}
	if !strings.Contains(string(parts["word/document.xml"]), esc("\t")) {
		t.Error("正文制表符未按数值引用转义保留")
	}
}

// ---------------------------------------------------------------- b. python-docx

// pythonExe is the interpreter the task pins, with python-docx 1.2.0.
const pythonExe = `C:\Users\louis\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\python\python.exe`

// pythonReport mirrors testdata/readback.py's JSON.
type pythonReport struct {
	Sections []struct {
		TopMarginMM    float64 `json:"top_margin_mm"`
		BottomMarginMM float64 `json:"bottom_margin_mm"`
		LeftMarginMM   float64 `json:"left_margin_mm"`
		RightMarginMM  float64 `json:"right_margin_mm"`
		PageWidthMM    float64 `json:"page_width_mm"`
		PageHeightMM   float64 `json:"page_height_mm"`
	} `json:"sections"`
	Paragraphs []pyParagraph `json:"paragraphs"`
	Tables     []struct {
		Rows  int        `json:"rows"`
		Cols  int        `json:"cols"`
		Cells [][]string `json:"cells"`
	} `json:"tables"`
	FooterParagraphs []pyParagraph `json:"footer_paragraphs"`
	FooterError      string        `json:"footer_error"`
}

type pyParagraph struct {
	Text            string  `json:"text"`
	StyleID         string  `json:"style_id"`
	Alignment       string  `json:"alignment"`
	LineSpacingPT   float64 `json:"line_spacing_pt"`
	LineSpacingRule string  `json:"line_spacing_rule"`
	FirstLineIndent float64 `json:"first_line_indent_pt"`
	LeftIndentPT    float64 `json:"left_indent_pt"`
	RightIndentPT   float64 `json:"right_indent_pt"`
	PPrSpacing      *struct {
		Line        string `json:"line"`
		LineRule    string `json:"lineRule"`
		BeforeLines string `json:"beforeLines"`
		AfterLines  string `json:"afterLines"`
	} `json:"pPr_spacing"`
	Runs []pyRun `json:"runs"`
}

type pyRun struct {
	Text   string  `json:"text"`
	SizePT float64 `json:"size_pt"`
	Color  string  `json:"color"`
	Fonts  *struct {
		EastAsia string `json:"eastAsia"`
		Ascii    string `json:"ascii"`
		HAnsi    string `json:"hAnsi"`
	} `json:"fonts"`
}

// readBackWithPythonDocx writes the bytes to a temp file, runs the pinned
// interpreter over testdata/readback.py and decodes the JSON it prints. It
// fails (rather than skips) when python-docx is unavailable, because the task
// makes the readback a hard acceptance gate.
func readBackWithPythonDocx(t *testing.T, data []byte) pythonReport {
	t.Helper()

	if _, err := os.Stat(pythonExe); err != nil {
		t.Fatalf("找不到 python 解释器 %s: %v", pythonExe, err)
	}
	script, err := filepath.Abs(filepath.Join("testdata", "readback.py"))
	if err != nil {
		t.Fatalf("解析 readback.py 路径失败: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("找不到读回脚本 %s: %v", script, err)
	}

	docxPath := filepath.Join(t.TempDir(), "out.docx")
	if err := os.WriteFile(docxPath, data, 0o600); err != nil {
		t.Fatalf("写入临时 docx 失败: %v", err)
	}

	cmd := exec.Command(pythonExe, script, docxPath)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("python-docx 读回失败: %v\nstderr:\n%s", err, stderr.String())
	}

	var rep pythonReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("解析 python-docx 输出失败: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return rep
}

func (rep pythonReport) paragraphByText(text string) (pyParagraph, bool) {
	for _, p := range rep.Paragraphs {
		if p.Text == text {
			return p, true
		}
	}
	return pyParagraph{}, false
}

// closeTo is the mm/pt tolerance. The standard is given in whole millimetres
// and points; this only absorbs twip rounding.
func closeTo(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

// xmlContains reports whether a document part contains a fragment. The package
// is deflated, so byte-level searches must go through the decompressed part,
// never the raw render result.
func xmlContains(t *testing.T, data []byte, fragment string) bool {
	t.Helper()
	return strings.Contains(string(zipEntries(t, data)["word/document.xml"]), fragment)
}

// enumContains matches python-docx's str(enum) form, e.g. "CENTER (1)", while
// still accepting a plain name from a version that formats differently.
func enumContains(got, want string) bool {
	return strings.Contains(got, want)
}

// TestPythonDocxReadback is acceptance criterion 5 of docs/CONTRACTS.md §5:
// 页边距 28/26/37/35mm, 正文 三号(16pt), 固定行距 ≈28.99pt, 首行缩进 2 字符,
// 标题 二号(22pt) 居中.
func TestPythonDocxReadback(t *testing.T) {
	spec := gbstandard.Default()
	doc := sampleDoc(t)
	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	rep := readBackWithPythonDocx(t, data)

	// --- 页面与页边距（EMU → mm，emu/36000） ---
	if len(rep.Sections) != 1 {
		t.Fatalf("期望 1 个节，实际 %d", len(rep.Sections))
	}
	sec := rep.Sections[0]
	wantMargins := []struct {
		name      string
		got, want float64
	}{
		{"上边距", sec.TopMarginMM, spec.Page.MarginTopMM},
		{"下边距", sec.BottomMarginMM, spec.Page.MarginBottomMM},
		{"左边距", sec.LeftMarginMM, spec.Page.MarginLeftMM},
		{"右边距", sec.RightMarginMM, spec.Page.MarginRightMM},
	}
	for _, m := range wantMargins {
		if !closeTo(m.got, m.want, 0.05) {
			t.Errorf("%s = %.4fmm，期望 %.4fmm", m.name, m.got, m.want)
		}
		t.Logf("python-docx: %s = %.4fmm（期望 %.1fmm）", m.name, m.got, m.want)
	}
	if !closeTo(sec.PageWidthMM, 210, 0.05) || !closeTo(sec.PageHeightMM, 297, 0.05) {
		t.Errorf("纸张 = %.3f×%.3fmm，期望 210×297mm", sec.PageWidthMM, sec.PageHeightMM)
	}
	// The task's headline numbers: 37/35/28/26mm.
	for _, m := range []struct {
		got, want float64
	}{{sec.TopMarginMM, 37}, {sec.BottomMarginMM, 35}, {sec.LeftMarginMM, 28}, {sec.RightMarginMM, 26}} {
		if !closeTo(m.got, m.want, 0.05) {
			t.Errorf("页边距 %.4fmm 偏离 37/35/28/26mm 口径（期望 %.0f）", m.got, m.want)
		}
	}

	// --- 正文：三号 16pt 仿宋，固定行距 ≈28.99pt，首行缩进 2 字 ---
	body, ok := rep.paragraphByText(doc.Blocks[bodyBlockIndex].Text)
	if !ok {
		t.Fatalf("python-docx 未读到正文段落 %q", doc.Blocks[bodyBlockIndex].Text)
	}
	if len(body.Runs) == 0 {
		t.Fatal("正文段落没有任何 run")
	}
	run := body.Runs[0]
	if !closeTo(run.SizePT, 16, 0.01) {
		t.Errorf("正文 run 字号 = %.4fpt，期望 16pt（三号）", run.SizePT)
	}
	if run.Fonts == nil || run.Fonts.EastAsia != gbstandard.FontFangSong {
		t.Errorf("正文 eastAsia 字体 = %+v，期望 %q", run.Fonts, gbstandard.FontFangSong)
	}
	t.Logf("python-docx: 正文 run 字号 = %.2fpt，eastAsia = %v", run.SizePT, run.Fonts.EastAsia)

	wantLine := spec.Page.LineSpacingPT()
	// OOXML quantises the exact height to twentieths of a point, so the
	// tolerance is one twip (0.05pt).
	if !closeTo(body.LineSpacingPT, wantLine, 0.05) {
		t.Errorf("正文行距 = %.4fpt，期望 %.4fpt（固定行距）", body.LineSpacingPT, wantLine)
	}
	if !closeTo(body.LineSpacingPT, 28.99, 0.05) {
		t.Errorf("正文行距 = %.4fpt，期望 ≈28.99pt", body.LineSpacingPT)
	}
	if body.PPrSpacing == nil || body.PPrSpacing.LineRule != "exact" {
		t.Errorf("正文行距规则 = %+v，期望 exact（固定值）", body.PPrSpacing)
	}
	if body.PPrSpacing != nil && body.PPrSpacing.Line != "580" {
		t.Errorf("w:spacing w:line = %q，期望 580（28.99pt×20）", body.PPrSpacing.Line)
	}
	t.Logf("python-docx: 正文行距 = %.4fpt（rule=%s, w:line=%s）", body.LineSpacingPT, body.LineSpacingRule, body.PPrSpacing.Line)

	// 首行缩进 2 字 = 2 × 16pt = 32pt，且必须带 w:firstLineChars="200".
	if !closeTo(body.FirstLineIndent, 2*16, 0.05) {
		t.Errorf("正文首行缩进 = %.4fpt，期望 %.1fpt（2 字 × 16pt）", body.FirstLineIndent, 2*16.0)
	}
	t.Logf("python-docx: 正文首行缩进 = %.2fpt（= %.0f 字）", body.FirstLineIndent, body.FirstLineIndent/16)

	// --- 标题：二号 22pt 居中 ---
	title, ok := rep.paragraphByText(doc.Blocks[titleBlockIndex].Text)
	if !ok {
		t.Fatalf("python-docx 未读到标题段落")
	}
	if !enumContains(title.Alignment, "CENTER") {
		t.Errorf("标题对齐 = %q，期望 CENTER", title.Alignment)
	}
	if len(title.Runs) == 0 || !closeTo(title.Runs[0].SizePT, 22, 0.01) {
		t.Errorf("标题字号 = %v，期望 22pt（二号）", title.Runs)
	}
	t.Logf("python-docx: 标题 对齐=%s 字号=%.2fpt", title.Alignment, title.Runs[0].SizePT)

	// --- 表格：行列正确，单元格文本逐字保留 ---
	if len(rep.Tables) != 1 {
		t.Fatalf("期望 1 个表格，实际 %d", len(rep.Tables))
	}
	tbl := rep.Tables[0]
	want := doc.Blocks[tableBlockIndex].Rows
	if tbl.Rows != len(want) || tbl.Cols != len(want[0]) {
		t.Errorf("表格尺寸 = %d行×%d列，期望 %d行×%d列", tbl.Rows, tbl.Cols, len(want), len(want[0]))
	}
	for i, row := range want {
		if i >= len(tbl.Cells) {
			break
		}
		for j, cell := range row {
			if j >= len(tbl.Cells[i]) {
				break
			}
			if tbl.Cells[i][j] != cell {
				t.Errorf("单元格[%d][%d] = %q，期望 %q", i, j, tbl.Cells[i][j], cell)
			}
		}
	}
	t.Logf("python-docx: 表格 = %d行×%d列，首格=%q", tbl.Rows, tbl.Cols, tbl.Cells[0][0])

	// --- 页码页脚：四号(14pt)宋体，两侧一字线 ---
	if rep.FooterError != "" {
		t.Fatalf("读取页脚失败: %s", rep.FooterError)
	}
	if len(rep.FooterParagraphs) == 0 {
		t.Fatal("页脚没有段落（页码缺失）")
	}
	ftr := rep.FooterParagraphs[0]
	if !enumContains(ftr.Alignment, "CENTER") {
		t.Errorf("页码对齐 = %q，期望 CENTER", ftr.Alignment)
	}
	var pageRun *pyRun
	for i := range ftr.Runs {
		if ftr.Runs[i].Text == "1" {
			pageRun = &ftr.Runs[i]
			break
		}
	}
	if pageRun == nil {
		t.Fatalf("页脚中没有页码 run，runs=%v", ftr.Runs)
	}
	if !closeTo(pageRun.SizePT, spec.PageNumberSize.Points(), 0.01) {
		t.Errorf("页码字号 = %.4fpt，期望 %.1fpt（%s）", pageRun.SizePT, spec.PageNumberSize.Points(), spec.PageNumberSize)
	}
	if pageRun.Fonts == nil || pageRun.Fonts.EastAsia != spec.PageNumberFont {
		t.Errorf("页码字体 = %+v，期望 eastAsia=%q", pageRun.Fonts, spec.PageNumberFont)
	}
	if !strings.Contains(ftr.Text, spec.PageNumberDash) {
		t.Errorf("页脚缺少一字线 %q：%q", spec.PageNumberDash, ftr.Text)
	}
	t.Logf("python-docx: 页码 字号=%.2fpt 字体=%v 文本=%q", pageRun.SizePT, pageRun.Fonts.EastAsia, ftr.Text)

	// --- 标题样式名出现在 styles.xml，便于用户在 Word 中复用 ---
	foundStyle := false
	for _, p := range rep.Paragraphs {
		if p.StyleID == styleID(docmodel.RoleBody) {
			foundStyle = true
			break
		}
	}
	if !foundStyle {
		t.Errorf("没有段落引用 %s 样式", styleID(docmodel.RoleBody))
	}
}

// TestPythonDocxReadbackCharIndent proves the character-unit indent survives
// the round trip: python-docx does not model w:firstLineChars, so the raw
// attribute is read from the OOXML of the reloaded document.
func TestPythonDocxReadbackCharIndent(t *testing.T) {
	spec := gbstandard.Default()
	doc := sampleDoc(t)
	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	parts := zipEntries(t, data)

	// Both the character unit and its point twin must be present on 正文.
	document := string(parts["word/document.xml"])
	wantInd := `w:firstLineChars="200" w:firstLine="640"`
	if !strings.Contains(document, wantInd) {
		t.Errorf("正文缩进缺少 %q（2 字 = 200，等价磅值 = 2×16pt×20 = 640）", wantInd)
	}

	// The masthead's block-level 空N行 offset must be added to the style's own
	// 空行 (0 + 100 hundredths = 1 line).
	if !strings.Contains(document, `w:beforeLines="100"`) {
		t.Errorf("发文机关标志缺少块级空行偏移 w:beforeLines=\"100\"")
	}
	// Only one paragraph has 正文's indent text; assert the attribute is paired
	// with the 正文 style so a missing style can't pass by accident.
	if !strings.Contains(document, fmt.Sprintf(`<w:pStyle w:val="%s"/>`, styleID(docmodel.RoleBody))) {
		t.Errorf("正文段落未引用样式 %s", styleID(docmodel.RoleBody))
	}
}

// ---------------------------------------------------------------- no hard-coding

// TestRenderFollowsSpecSizes proves the writer reads sizes from the spec: a
// different Spec must produce a different document, and the half-point values
// must track it exactly.
func TestRenderFollowsSpecSizes(t *testing.T) {
	doc := sampleDoc(t)

	small := gbstandard.Default()
	big := gbstandard.Default()

	bodySmall := small.Styles[docmodel.RoleBody]
	bodySmall.Size = gbstandard.SizeXiaoSi // 12pt
	small.Styles[docmodel.RoleBody] = bodySmall

	bodyBig := big.Styles[docmodel.RoleBody]
	bodyBig.Size = gbstandard.SizeErhao // 22pt
	bodyBig.FirstLineIndentChars = 3
	big.Styles[docmodel.RoleBody] = bodyBig

	titleBig := big.Styles[docmodel.RoleTitle]
	titleBig.Size = gbstandard.SizeChuhao // 42pt
	titleBig.Align = gbstandard.AlignLeft
	big.Styles[docmodel.RoleTitle] = titleBig

	// A different page setup must move the margins independently.
	big.Page.MarginLeftMM = 40

	dataSmall, err := Render(doc, small)
	if err != nil {
		t.Fatalf("Render(small) 失败: %v", err)
	}
	dataBig, err := Render(doc, big)
	if err != nil {
		t.Fatalf("Render(big) 失败: %v", err)
	}
	if bytes.Equal(dataSmall, dataBig) {
		t.Fatal("改变 spec 字号/页边距后输出完全相同——说明数值被硬编码了")
	}

	rep := readBackWithPythonDocx(t, dataBig)
	body, ok := rep.paragraphByText(doc.Blocks[bodyBlockIndex].Text)
	if !ok {
		t.Fatal("未读到正文段落")
	}
	if !closeTo(body.Runs[0].SizePT, 22, 0.01) {
		t.Errorf("改 spec 后正文字号 = %.4fpt，期望 22pt（应从 spec 读取）", body.Runs[0].SizePT)
	}
	// 3 字 × 22pt = 66pt，同时 w:firstLineChars 应为 300.
	if !closeTo(body.FirstLineIndent, 66, 0.05) {
		t.Errorf("改 spec 后首行缩进 = %.4fpt，期望 66pt（3 字 × 22pt）", body.FirstLineIndent)
	}
	title, ok := rep.paragraphByText(doc.Blocks[titleBlockIndex].Text)
	if !ok {
		t.Fatal("未读到标题段落")
	}
	if !closeTo(title.Runs[0].SizePT, 42, 0.01) || !enumContains(title.Alignment, "LEFT") {
		t.Errorf("改 spec 后标题 = %.2fpt/%s，期望 42pt/LEFT", title.Runs[0].SizePT, title.Alignment)
	}
	if !closeTo(rep.Sections[0].LeftMarginMM, 40, 0.05) {
		t.Errorf("改 spec 后左边距 = %.4fmm，期望 40mm", rep.Sections[0].LeftMarginMM)
	}
	t.Logf("python-docx（改 spec 后）: 正文 %.2fpt 缩进 %.2fpt 标题 %.2fpt/%s 左边距 %.3fmm",
		body.Runs[0].SizePT, body.FirstLineIndent, title.Runs[0].SizePT, title.Alignment, rep.Sections[0].LeftMarginMM)

	// The character-unit indent must follow the spec's 字数, not a constant.
	if !xmlContains(t, dataBig, `w:firstLineChars="300"`) {
		t.Error("w:firstLineChars 未随 spec 缩进字数变化")
	}
	// The docGrid's line pitch must follow the page grid as well.
	if !xmlContains(t, dataBig, fmt.Sprintf(`w:linePitch="%d"`, int(math.Round(big.Page.LineSpacingPT()*20)))) {
		t.Errorf("docGrid 的 linePitch 未随 spec 每面行数变化（期望 %d）",
			int(math.Round(big.Page.LineSpacingPT()*20)))
	}
}

// TestRenderFollowsSpecLineSpacing proves the fixed line spacing is computed
// from the page grid rather than written once as a constant.
func TestRenderFollowsSpecLineSpacing(t *testing.T) {
	doc := sampleDoc(t)

	for _, lines := range []int{22, 25, 30} {
		spec := gbstandard.Default()
		spec.Page.LinesPerPage = lines
		st := spec.Styles[docmodel.RoleBody]
		st.LineSpacingPT = spec.Page.LineSpacingPT()
		spec.Styles[docmodel.RoleBody] = st

		data, err := Render(doc, spec)
		if err != nil {
			t.Fatalf("Render(lines=%d) 失败: %v", lines, err)
		}
		rep := readBackWithPythonDocx(t, data)
		body, ok := rep.paragraphByText(doc.Blocks[bodyBlockIndex].Text)
		if !ok {
			t.Fatal("未读到正文段落")
		}
		want := spec.Page.LineSpacingPT()
		if !closeTo(body.LineSpacingPT, want, 0.05) {
			t.Errorf("LinesPerPage=%d 时行距 = %.4fpt，期望 %.4fpt", lines, body.LineSpacingPT, want)
		}
		t.Logf("LinesPerPage=%d → python-docx 行距 %.4fpt（期望 %.4fpt）", lines, body.LineSpacingPT, want)
	}
}

// TestRenderPageNumbersFollowSpec proves 页码 reads PageNumberSize/Font.
func TestRenderPageNumbersFollowSpec(t *testing.T) {
	spec := gbstandard.Default()
	spec.PageNumberSize = gbstandard.SizeXiaoWu // 9pt
	spec.PageNumberFont = gbstandard.FontHeiti
	spec.PageNumberDash = "～"

	doc := sampleDoc(t)
	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	rep := readBackWithPythonDocx(t, data)
	if rep.FooterError != "" {
		t.Fatalf("读取页脚失败: %s", rep.FooterError)
	}
	if len(rep.FooterParagraphs) == 0 {
		t.Fatal("页脚没有段落")
	}
	ftr := rep.FooterParagraphs[0]
	var pageRun *pyRun
	for i := range ftr.Runs {
		if ftr.Runs[i].Text == "1" {
			pageRun = &ftr.Runs[i]
		}
	}
	if pageRun == nil {
		t.Fatalf("页脚无页码 run: %v", ftr.Runs)
	}
	if !closeTo(pageRun.SizePT, 9, 0.01) {
		t.Errorf("页码字号 = %.4fpt，期望 9pt", pageRun.SizePT)
	}
	if pageRun.Fonts == nil || pageRun.Fonts.EastAsia != gbstandard.FontHeiti {
		t.Errorf("页码字体 = %+v，期望 %q", pageRun.Fonts, gbstandard.FontHeiti)
	}
	if !strings.Contains(ftr.Text, "～") {
		t.Errorf("页码一字线未按 spec 变化：%q", ftr.Text)
	}
}

// ---------------------------------------------------------------- robustness

// TestRenderImageBlockKeepsText guards the verbatim invariant for a block that
// carries both a picture and prose: the drawing must not swallow the text.
func TestRenderImageBlockKeepsText(t *testing.T) {
	caption := "附件：格式对照表"
	doc := &docmodel.Doc{
		Blocks: []docmodel.OutBlock{{
			Role:  docmodel.RoleAttachment,
			Text:  caption,
			Image: testPNG(t),
		}},
	}
	data, err := Render(doc, gbstandard.Default())
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	parts := zipEntries(t, data)
	document := string(parts["word/document.xml"])

	if !strings.Contains(document, esc(caption)) {
		t.Errorf("带图片的块丢失了正文 %q", caption)
	}
	if !strings.Contains(document, "<w:drawing>") {
		t.Error("带图片的块没有输出 w:drawing")
	}
	// python-docx must see the caption as a paragraph text too.
	rep := readBackWithPythonDocx(t, data)
	if _, ok := rep.paragraphByText(caption); !ok {
		texts := make([]string, 0, len(rep.Paragraphs))
		for _, p := range rep.Paragraphs {
			texts = append(texts, p.Text)
		}
		t.Errorf("python-docx 未读到带图块的正文 %q，实际段落 %v", caption, texts)
	}
}

// TestRenderIncompleteSpecDoesNotPanic covers a spec missing styles: Render
// must fall back rather than crash, and must report nothing worse than a
// document rendered with 正文's style.
func TestRenderIncompleteSpecDoesNotPanic(t *testing.T) {
	doc := sampleDoc(t)

	cases := map[string]func(*gbstandard.Spec){
		"缺标题样式": func(s *gbstandard.Spec) { delete(s.Styles, docmodel.RoleTitle) },
		"缺正文样式": func(s *gbstandard.Spec) { delete(s.Styles, docmodel.RoleBody) },
		"样式表为空": func(s *gbstandard.Spec) { s.Styles = nil },
		"未知字号": func(s *gbstandard.Spec) {
			st := s.Styles[docmodel.RoleBody]
			st.Size = "莫名字号"
			s.Styles[docmodel.RoleBody] = st
		},
		"页码字号未知": func(s *gbstandard.Spec) { s.PageNumberSize = "莫名字号" },
		"行距为零": func(s *gbstandard.Spec) {
			st := s.Styles[docmodel.RoleBody]
			st.LineSpacingPT = 0
			s.Styles[docmodel.RoleBody] = st
		},
		"页边距为零": func(s *gbstandard.Spec) { s.Page = gbstandard.PageSetup{WidthMM: 210, HeightMM: 297} },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := gbstandard.Default()
			mutate(&spec)
			// ValidateSpec must report the problem without panicking.
			_ = gbstandard.ValidateSpec(spec)

			data, err := Render(doc, spec)
			if err != nil {
				t.Fatalf("Render 不应因不完整的 spec 失败: %v", err)
			}
			if len(data) == 0 {
				t.Fatal("Render 返回空字节")
			}
			parts := zipEntries(t, data)
			xmlWellFormed(t, "word/document.xml", parts["word/document.xml"])
			xmlWellFormed(t, "word/styles.xml", parts["word/styles.xml"])
		})
	}

	// A nil document is the one input that must be an error, not a panic.
	if _, err := Render(nil, gbstandard.Default()); err == nil {
		t.Error("Render(nil) 应返回错误")
	}
}

// TestRenderEmptyDocStillValid proves a document with no blocks still yields a
// package Word (and python-docx) accepts.
func TestRenderEmptyDocStillValid(t *testing.T) {
	data, err := Render(&docmodel.Doc{}, gbstandard.Default())
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	parts := zipEntries(t, data)
	xmlWellFormed(t, "word/document.xml", parts["word/document.xml"])

	rep := readBackWithPythonDocx(t, data)
	if len(rep.Sections) != 1 {
		t.Fatalf("空文档应仍有一个节，实际 %d", len(rep.Sections))
	}
	if !closeTo(rep.Sections[0].TopMarginMM, 37, 0.05) {
		t.Errorf("空文档上边距 = %.4fmm，期望 37mm", rep.Sections[0].TopMarginMM)
	}
}

// TestRenderRunsPreserveInlineStyle checks that source inline styling is
// carried into separate runs while the text stays concatenated verbatim.
func TestRenderRunsPreserveInlineStyle(t *testing.T) {
	doc := &docmodel.Doc{
		Blocks: []docmodel.OutBlock{{
			Role: docmodel.RoleBody,
			Text: "加粗部分与斜体部分",
			Runs: []docmodel.Run{
				{Text: "加粗部分", Bold: true},
				{Text: "与"},
				{Text: "斜体部分", Italic: true},
			},
		}},
	}
	data, err := Render(doc, gbstandard.Default())
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	parts := zipEntries(t, data)

	var document documentNode
	if err := xml.Unmarshal(parts["word/document.xml"], &document); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(document.Body.Paragraphs) != 1 {
		t.Fatalf("期望 1 个段落，实际 %d", len(document.Body.Paragraphs))
	}
	runs := document.Body.Paragraphs[0].Runs
	if len(runs) != 3 {
		t.Fatalf("期望 3 个 run，实际 %d", len(runs))
	}
	if got := runs[0].Text + runs[1].Text + runs[2].Text; got != doc.Blocks[0].Text {
		t.Errorf("run 拼接 = %q，期望 %q", got, doc.Blocks[0].Text)
	}
	if !bytes.Contains(parts["word/document.xml"], []byte("<w:b/>")) {
		t.Error("源文加粗未输出 w:b")
	}
	if !bytes.Contains(parts["word/document.xml"], []byte("<w:i/>")) {
		t.Error("源文斜体未输出 w:i")
	}
}

// TestRenderEditedBlockUsesTextOverStaleRuns: an accepted edit changes Text
// but the block may still carry the source's runs. Printing the runs would
// silently reproduce the pre-edit wording — the reported defect where a title
// approved for change still downloaded unchanged.
func TestRenderEditedBlockUsesTextOverStaleRuns(t *testing.T) {
	doc := &docmodel.Doc{
		Blocks: []docmodel.OutBlock{{
			Role:   docmodel.RoleTitle,
			Text:   "一、总体目标",
			Edited: true,
			Runs:   []docmodel.Run{{Text: "一、总体要求"}},
		}},
	}
	data, err := Render(doc, gbstandard.Default())
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	parts := zipEntries(t, data)

	var document documentNode
	if err := xml.Unmarshal(parts["word/document.xml"], &document); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(document.Body.Paragraphs) != 1 {
		t.Fatalf("期望 1 个段落，实际 %d", len(document.Body.Paragraphs))
	}
	var got strings.Builder
	for _, run := range document.Body.Paragraphs[0].Runs {
		got.WriteString(run.Text)
	}
	if got.String() != "一、总体目标" {
		t.Errorf("导出文字 = %q, 期望改动后的文字", got.String())
	}
}

// TestRenderBordersFromSpec proves the 红色分隔线 and 版记 rules come from the
// spec's BorderRule rather than a literal.
func TestRenderBordersFromSpec(t *testing.T) {
	spec := gbstandard.Default()
	doc := &docmodel.Doc{
		Blocks: []docmodel.OutBlock{
			{Role: docmodel.RoleRedLine},
			{Role: docmodel.RoleCC, Text: "抄送：各省、自治区、直辖市人民政府。"},
		},
	}
	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	document := string(zipEntries(t, data)["word/document.xml"])

	red := spec.Styles[docmodel.RoleRedLine].BorderBottom
	wantRed := fmt.Sprintf(`<w:bottom w:val="single" w:sz="%d"`, red.SizeEighthPT)
	if !strings.Contains(document, wantRed) {
		t.Errorf("红色分隔线缺少 %q", wantRed)
	}
	if !strings.Contains(document, fmt.Sprintf(`w:color="%s"`, gbstandard.ColorRed)) {
		t.Error("红色分隔线颜色未取自 spec")
	}
	// BorderTop lives on the BorderRule, while 空N行 lives on the ParaStyle:
	// take both from the style itself rather than confusing the two types.
	ccStyle := spec.Styles[docmodel.RoleCC]
	if ccStyle.BorderTop == nil {
		t.Fatal("抄送机关样式缺少 BorderTop")
	}
	wantCC := fmt.Sprintf(`<w:top w:val="single" w:sz="%d"`, ccStyle.BorderTop.SizeEighthPT)
	if !strings.Contains(document, wantCC) {
		t.Errorf("版记线缺少 %q", wantCC)
	}
	if !strings.Contains(document, fmt.Sprintf(`w:beforeLines="%d"`, int(ccStyle.SpaceBeforeLines))) {
		t.Errorf("抄送机关缺少 w:beforeLines=%v", ccStyle.SpaceBeforeLines)
	}
}

// TestRenderImageGeometryFromSpec proves the picture is scaled to the 版心,
// whose width comes from the spec's page margins.
func TestRenderImageGeometryFromSpec(t *testing.T) {
	doc := sampleDoc(t)

	wide := gbstandard.Default()
	narrow := gbstandard.Default()
	narrow.Page.MarginLeftMM = 60
	narrow.Page.MarginRightMM = 60

	dataWide, err := Render(doc, wide)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	dataNarrow, err := Render(doc, narrow)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	if bytes.Equal(dataWide, dataNarrow) {
		t.Error("改变版心宽度后图片尺寸未变化——说明尺寸被硬编码了")
	}

	// 4:2 aspect ratio must be preserved by the fit.
	document := string(zipEntries(t, dataWide)["word/document.xml"])
	i := strings.Index(document, `<wp:extent cx="`)
	if i < 0 {
		t.Fatal("document.xml 中没有 wp:extent")
	}
	rest := document[i+len(`<wp:extent cx="`):]
	cxStr := rest[:strings.Index(rest, `"`)]
	rest = rest[strings.Index(rest, `cy="`)+len(`cy="`):]
	cyStr := rest[:strings.Index(rest, `"`)]
	var cx, cy int
	fmt.Sscanf(cxStr, "%d", &cx)
	fmt.Sscanf(cyStr, "%d", &cy)
	if cx <= 0 || cy <= 0 {
		t.Fatalf("图片尺寸无效 cx=%d cy=%d", cx, cy)
	}
	if !closeTo(float64(cx)/float64(cy), 2, 0.05) {
		t.Errorf("图片宽高比 = %.4f，期望 2.0（64×32）", float64(cx)/float64(cy))
	}
	maxCX := wide.Page.TextWidthMM() * 36000
	if float64(cx) > maxCX+1 {
		t.Errorf("图片宽度 %.3fmm 超出版心 %.3fmm", float64(cx)/36000, maxCX/36000)
	}
	t.Logf("几何: 图片 %d×%d EMU = %.2f×%.2fmm（版心宽 %.1fmm）",
		cx, cy, float64(cx)/36000, float64(cy)/36000, maxCX/36000)
}

// TestRenderDocGridPinsCharsPerLine gives 「每行28字」 machine evidence.
//
// GB/T 9704-2012's figures do not quite agree — 28 × 三号(16pt) = 448pt =
// 158.0mm against a 156mm 版心 — so the writer pins the character count with
// Word's 文档网格 (w:docGrid) and condenses the advance, exactly as the
// standard's own templates do. The font size must NOT be reduced for this:
// condensation moves the字距, not the 字号.
func TestRenderDocGridPinsCharsPerLine(t *testing.T) {
	spec := gbstandard.Default()
	doc := sampleDoc(t)

	data, err := Render(doc, spec)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	document := string(zipEntries(t, data)["word/document.xml"])

	// 1. The grid must be a lines-and-chars grid with the spec's own counts.
	if !strings.Contains(document, `w:type="linesAndChars"`) {
		t.Errorf("docGrid 未钉住每行字数（期望 w:type=\"linesAndChars\"）")
	}
	wantPitch := int(math.Round(spec.Page.LineSpacingPT() * 20))
	if !strings.Contains(document, fmt.Sprintf(`w:linePitch="%d"`, wantPitch)) {
		t.Errorf("docGrid 缺少 w:linePitch=\"%d\"", wantPitch)
	}

	// 2. The condensation must be the value gbstandard derives, converted to
	//    the 1/4096-em unit w:charSpace uses.
	body := spec.Styles[docmodel.RoleBody]
	condense := spec.Page.CharCondensePT(body.Size)
	wantCharSpace := int(math.Round(condense / body.Size.Points() * 4096))
	if !strings.Contains(document, fmt.Sprintf(`w:charSpace="%d"`, wantCharSpace)) {
		t.Errorf("docGrid 缺少 w:charSpace=\"%d\"（压缩 %.4fpt/字）", wantCharSpace, condense)
	}

	// 3. The geometry the condensation exists for: 28 三号 characters overshoot
	//    the 版心, and the condensation recovers the difference.
	naturalMM := 28 * body.Size.Points() * 25.4 / 72
	overflowMM := naturalMM - spec.Page.TextWidthMM()
	if overflowMM <= 0 {
		t.Fatalf("夹具假设不成立：28 字 %.2fmm 未超出版心 %.2fmm", naturalMM, spec.Page.TextWidthMM())
	}
	if overflowMM >= 3 {
		t.Errorf("三号超出量 %.3fmm 过大（期望 <3mm）", overflowMM)
	}
	if ratio := condense / body.Size.Points(); ratio >= 0.03 {
		t.Errorf("压缩比 %.4f 过大（期望 <3%%）", ratio)
	}

	// 4. 三号 must still be 16pt — condensation is not a font-size change.
	rep := readBackWithPythonDocx(t, data)
	if p, ok := rep.paragraphByText(doc.Blocks[bodyBlockIndex].Text); ok {
		if !closeTo(p.Runs[0].SizePT, 16, 0.01) {
			t.Errorf("正文仍应为 16pt，实际 %.2fpt（压缩不得改字号）", p.Runs[0].SizePT)
		}
	}
	t.Logf("docGrid: linePitch=%d charSpace=%d（%.1fmm/28字自然宽 %.2fmm，压缩 %.4fpt/字 = %.2f%%）",
		wantPitch, wantCharSpace, spec.Page.TextWidthMM(), naturalMM, condense, condense/body.Size.Points()*100)

	// 5. A body size that fits naturally must not request condensation.
	small := gbstandard.Default()
	smallBody := small.Styles[docmodel.RoleBody]
	smallBody.Size = gbstandard.SizeSihao // 四号: 28 × 14pt = 138.3mm < 156mm
	small.Styles[docmodel.RoleBody] = smallBody
	if !small.Page.FitsNaturalSpacing(gbstandard.SizeSihao) {
		t.Fatal("前提错误：四号 28 字应能自然放下")
	}
	smallData, err := Render(doc, small)
	if err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	smallDoc := string(zipEntries(t, smallData)["word/document.xml"])
	if !strings.Contains(smallDoc, `w:charSpace="0"`) {
		t.Errorf("四号无需压缩，但 charSpace 不为 0：%q",
			extractAttr(smallDoc, "w:charSpace"))
	}
	t.Logf("四号(14pt) 无需压缩：w:charSpace=%s", extractAttr(smallDoc, "w:charSpace"))
}

// extractAttr pulls the first value of a w: attribute out of a document part,
// for diagnostics.
func extractAttr(xmlDoc, attr string) string {
	i := strings.Index(xmlDoc, attr+`="`)
	if i < 0 {
		return "<缺失>"
	}
	rest := xmlDoc[i+len(attr)+2:]
	return rest[:strings.Index(rest, `"`)]
}

// ---------------------------------------------------------------- helpers

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestRuntimeIndependent keeps the python interpreter requirement visible: the
// readback is skipped nowhere, but the failure message should name the OS when
// the pinned path is absent, so a reviewer on another machine understands.
func TestRuntimeIndependent(t *testing.T) {
	if _, err := os.Stat(pythonExe); err != nil {
		if runtime.GOOS != "windows" {
			t.Skipf("非 Windows 环境，python-docx 读回由 CI 的 python 提供")
		}
		t.Fatalf("本机缺少 python 解释器 %s", pythonExe)
	}
}
