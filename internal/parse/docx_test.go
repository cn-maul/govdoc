package parse

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// ------------------------------------------------------------ docx test builder

// nsDecls are the namespace declarations a real word/document.xml carries. The
// parser matches on local names, but the fixture stays faithful to real output
// so it also exercises prefixed attribute lookup (r:embed, w:type).
const nsDecls = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"` +
	` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"` +
	` xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"` +
	` xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"` +
	` xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"` +
	` xmlns:v="urn:schemas-microsoft-com:vml"` +
	` xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"`

// buildDOCX assembles an in-memory .docx whose word/document.xml body is the
// given XML, plus any extra package parts (relationships, media). No third
// party tooling is involved, which keeps the fixture honest: whatever the test
// asserts is exactly what the parser was handed.
func buildDOCX(t *testing.T, body string, extra map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, data []byte) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	write("word/document.xml", []byte(
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+"\r\n"+
			`<w:document `+nsDecls+`><w:body>`+body+`</w:body></w:document>`))
	for name, data := range extra {
		write(name, data)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// para wraps run XML in a paragraph, optionally with paragraph properties.
func para(pPr, runs string) string { return "<w:p>" + pPr + runs + "</w:p>" }

// run wraps text in a run, preserving spaces the way Word does.
func run(text string) string {
	return `<w:r><w:t xml:space="preserve">` + text + `</w:t></w:r>`
}

// runRaw wraps already-escaped run XML in a run.
func runRaw(inner string) string { return "<w:r>" + inner + "</w:r>" }

func mustParse(t *testing.T, name string, data []byte) *docmodel.Document {
	t.Helper()
	doc, err := Parse(name, data)
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return doc
}

// textsOf returns every node's text in order.
func textsOf(doc *docmodel.Document) []string {
	out := make([]string, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		out = append(out, n.Text)
	}
	return out
}

func kindsOf(doc *docmodel.Document) []docmodel.NodeKind {
	out := make([]docmodel.NodeKind, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		out = append(out, n.Kind)
	}
	return out
}

// ------------------------------------------------------------ verbatim proofs

// TestVerbatimDOCXRunBoundaries is the core proof of the project's central
// invariant for Word input: text split across runs must come back as the exact
// concatenation, including leading/trailing/multiple/full-width spaces, and the
// per-run view must reconstruct the node text exactly.
//
// Every character here is chosen to catch a specific normalisation bug:
//
//	"  前三"      leading ASCII spaces (dropped by a trim)
//	"中　间"      full-width space U+3000 (collapsed into a half-width space)
//	"“引号”"      curly quotes (straightened into " by naive cleanup)
//	"‘单’"        curly single quotes
//	"……"          a double ellipsis (folded into "..." by "smart" tidy-up)
//	"最后  "      trailing spaces
func TestVerbatimDOCXRunBoundaries(t *testing.T) {
	const want = "  前三" + "中　间" + "“引号”" + "‘单’" + "……" + "最后  "
	body := para("", run("  前三")+run("中　间")+run("“引号”")+run("‘单’")+run("……")+run("最后  "))

	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))
	if len(doc.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1: %q", len(doc.Nodes), textsOf(doc))
	}
	n := doc.Nodes[0]
	if n.Text != want {
		t.Errorf("Text not verbatim:\n got %q\nwant %q", n.Text, want)
	}
	if got := joinRuns(n.Runs); got != want {
		t.Errorf("Runs do not reconstruct the text:\n got %q\nwant %q", got, want)
	}
	if len(n.Runs) != 6 {
		t.Errorf("runs = %d, want 6 (run boundaries must be preserved)", len(n.Runs))
	}
	// Each run must carry its own source span unchanged, so nothing was
	// shifted across a run boundary while still summing to the right total.
	spans := []string{"  前三", "中　间", "“引号”", "‘单’", "……", "最后  "}
	for i, want := range spans {
		if i >= len(n.Runs) {
			break
		}
		if n.Runs[i].Text != want {
			t.Errorf("run %d = %q, want %q", i, n.Runs[i].Text, want)
		}
	}
	// Explicit negative checks: a failure here names the exact rewrite.
	// Trimming is covered by the HasPrefix/HasSuffix assertions below, which
	// can distinguish "two spaces" from "one space" without a substring test
	// that the correct text would also satisfy.
	for _, c := range []struct{ name, bad string }{
		{"ASCII straight quote", `"引号"`},
		{"half-width space instead of U+3000", "中 间"},
		{"three-dot ellipsis", "..."},
	} {
		if strings.Contains(n.Text, c.bad) {
			t.Errorf("text was normalised (%s): %q", c.name, n.Text)
		}
	}
	// Two leading and two trailing spaces, counted exactly: a single space
	// would still satisfy a loose HasPrefix check, so the count is asserted.
	if !strings.HasPrefix(n.Text, "  ") || !strings.HasSuffix(n.Text, "  ") {
		t.Errorf("leading/trailing spaces were trimmed: %q", n.Text)
	}
	if strings.HasPrefix(n.Text, "   ") || strings.HasSuffix(n.Text, "   ") {
		t.Errorf("spaces were altered: %q", n.Text)
	}
}

// TestVerbatimDOCXEmptyAndWhitespaceOnlyParagraphs pins the two opposite
// decisions: a paragraph with no characters at all produces no node (an
// empty-text content node could never satisfy VerifyVerbatim), while a
// paragraph holding only 全角空格 IS content and must survive.
func TestVerbatimDOCXEmptyAndWhitespaceOnlyParagraphs(t *testing.T) {
	body := para("", "") + para("", run("　　")) + para("", run("正文"))
	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))

	want := []string{"　　", "正文"}
	got := textsOf(doc)
	if len(got) != len(want) {
		t.Fatalf("texts = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("text[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestVerbatimDOCXMultiParagraphRoundTrip feeds a mixed document through and
// checks that the sequence of node texts is exactly the sequence of source
// paragraph texts, in order, with nothing dropped, split or merged.
func TestVerbatimDOCXMultiParagraphRoundTrip(t *testing.T) {
	sources := []string{
		"关于进一步加强公文格式管理工作的通知",
		"  各地、各部门：",
		"　　为深入贯彻落实……要求，现将有关事项通知如下。",
		"一、总体要求",
		"（一）提高认识。要“严”字当头，不得 随意 增删。",
		"1. 明确责任。",
		" “引号在行首”  ",
	}
	var sb strings.Builder
	for _, s := range sources {
		sb.WriteString(para("", run(s)))
	}
	doc := mustParse(t, "通知.docx", buildDOCX(t, sb.String(), nil))

	got := textsOf(doc)
	if len(got) != len(sources) {
		t.Fatalf("nodes = %d, want %d: %q", len(got), len(sources), got)
	}
	for i, want := range sources {
		if got[i] != want {
			t.Errorf("node %d was rewritten:\n got %q\nwant %q", i, got[i], want)
		}
	}
}

// TestVerbatimDOCXCharacterReferences proves that markup escapes are decoded
// (they are not content) while the characters they denote are preserved
// exactly — including a literal "&" and "<" that a naive re-encoder would
// mangle.
func TestVerbatimDOCXCharacterReferences(t *testing.T) {
	body := para("", runRaw(`<w:t xml:space="preserve">A&amp;B &lt;C&gt; &#8220;引&#8221; &#x3000;end</w:t>`))
	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))

	const want = "A&B <C> “引” 　end"
	if doc.Nodes[0].Text != want {
		t.Errorf("Text = %q, want %q", doc.Nodes[0].Text, want)
	}
}

// ------------------------------------------------------------ docx structure

// TestDOCXKindFromSourceMarkup checks that classification uses the source's own
// markup rather than guessing: Word styles for headings, numbering properties
// for list items, and plain paragraphs otherwise.
func TestDOCXKindFromSourceMarkup(t *testing.T) {
	body := para(`<w:pPr><w:pStyle w:val="Heading 1"/></w:pPr>`, run("一、总体要求")) +
		para(`<w:pPr><w:pStyle w:val="Heading 2"/></w:pPr>`, run("（一）提高认识")) +
		para(`<w:pPr><w:pStyle w:val="标题 3"/></w:pPr>`, run("1. 明确责任")) +
		para(`<w:pPr><w:pStyle w:val="heading4"/></w:pPr>`, run("（1）细化措施")) +
		para(`<w:pPr><w:pStyle w:val="BodyText"/></w:pPr>`, run("正文段落")) +
		para(`<w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr></w:pPr>`, run("列表项")) +
		para(`<w:pPr><w:outlineLvl w:val="0"/></w:pPr>`, run("大纲一级"))

	want := []struct {
		kind  docmodel.NodeKind
		level int
	}{
		{docmodel.KindHeading, 1},
		{docmodel.KindHeading, 2},
		{docmodel.KindHeading, 3},
		{docmodel.KindHeading, 4},
		{docmodel.KindParagraph, 0},
		{docmodel.KindListItem, 0},
		{docmodel.KindHeading, 1},
	}
	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))
	if len(doc.Nodes) != len(want) {
		t.Fatalf("nodes = %d, want %d", len(doc.Nodes), len(want))
	}
	for i, w := range want {
		n := doc.Nodes[i]
		if n.Kind != w.kind || n.Level != w.level {
			t.Errorf("node %d (%q) = %s/%d, want %s/%d",
				i, n.Text, n.Kind, n.Level, w.kind, w.level)
		}
	}
	if doc.Nodes[2].SourceStyle != "标题 3" {
		t.Errorf("SourceStyle = %q, want %q", doc.Nodes[2].SourceStyle, "标题 3")
	}
}

// TestDOCXTable checks table extraction, including a multi-paragraph cell whose
// internal line break must survive, and header-row detection.
func TestDOCXTable(t *testing.T) {
	body := "<w:tbl>" +
		"<w:tr><w:trPr><w:tblHeader/></w:trPr>" +
		"<w:tc>" + para("", run("序号")) + "</w:tc>" +
		"<w:tc>" + para("", run("事项")) + "</w:tc></w:tr>" +
		"<w:tr><w:tc>" + para("", run("1")) + "</w:tc>" +
		"<w:tc>" + para("", run("第一行")) + para("", run("第二行")) + "</w:tc></w:tr>" +
		"</w:tbl>"
	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))

	if len(doc.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1: %q", len(doc.Nodes), textsOf(doc))
	}
	n := doc.Nodes[0]
	if n.Kind != docmodel.KindTable {
		t.Fatalf("Kind = %s, want %s", n.Kind, docmodel.KindTable)
	}
	if !n.HeaderRow {
		t.Error("HeaderRow = false, want true (w:tblHeader is present)")
	}
	want := [][]string{{"序号", "事项"}, {"1", "第一行\n第二行"}}
	if len(n.Rows) != len(want) {
		t.Fatalf("rows = %d (%v), want %d", len(n.Rows), n.Rows, len(want))
	}
	for i := range want {
		if len(n.Rows[i]) != len(want[i]) {
			t.Fatalf("row %d = %v, want %v", i, n.Rows[i], want[i])
		}
		for j := range want[i] {
			if n.Rows[i][j] != want[i][j] {
				t.Errorf("cell [%d][%d] = %q, want %q", i, j, n.Rows[i][j], want[i][j])
			}
		}
	}
}

// TestDOCXImage checks that a drawing becomes a KindImage node carrying the
// relationship id's target bytes, name and mime type from word/media, and that
// the node's Text is the source file name (so the renderer can echo it back
// verbatim and satisfy VerifyVerbatim).
func TestDOCXImage(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x01, 0x02, 0x03}
	rels := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>
</Relationships>`
	drawing := `<w:r><w:drawing><wp:inline><a:graphic><a:graphicData><pic:pic>` +
		`<pic:blipFill><a:blip r:embed="rId7"/></pic:blipFill>` +
		`</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>`
	body := para("", drawing)

	doc := mustParse(t, "s.docx", buildDOCX(t, body, map[string][]byte{
		"word/_rels/document.xml.rels": []byte(rels),
		"word/media/image1.png":        png,
	}))

	if len(doc.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1: %q", len(doc.Nodes), textsOf(doc))
	}
	n := doc.Nodes[0]
	if n.Kind != docmodel.KindImage {
		t.Fatalf("Kind = %s, want %s", n.Kind, docmodel.KindImage)
	}
	if n.Image == nil {
		t.Fatal("Image = nil")
	}
	if n.Image.Name != "image1.png" {
		t.Errorf("Image.Name = %q, want %q", n.Image.Name, "image1.png")
	}
	if n.Image.MimeType != "image/png" {
		t.Errorf("Image.MimeType = %q, want %q", n.Image.MimeType, "image/png")
	}
	if !bytes.Equal(n.Image.Data, png) {
		t.Errorf("Image.Data = %v, want %v", n.Image.Data, png)
	}
	if n.Text != "image1.png" {
		t.Errorf("Text = %q, want the source file name %q", n.Text, "image1.png")
	}
}

// TestDOCXPageBreak covers both spellings of an explicit page break and checks
// that a break never swallows the text around it.
func TestDOCXPageBreak(t *testing.T) {
	body := para("", `<w:r><w:br w:type="page"/></w:r>`) +
		para("", run("第一页末")+`<w:r><w:br w:type="page"/></w:r>`+run("第二页首"))

	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))
	wantKinds := []docmodel.NodeKind{
		docmodel.KindPageBreak,
		docmodel.KindParagraph,
		docmodel.KindPageBreak,
		docmodel.KindParagraph,
	}
	got := kindsOf(doc)
	if len(got) != len(wantKinds) {
		t.Fatalf("kinds = %v, want %v", got, wantKinds)
	}
	for i := range wantKinds {
		if got[i] != wantKinds[i] {
			t.Errorf("kind[%d] = %s, want %s", i, got[i], wantKinds[i])
		}
	}
	if doc.Nodes[1].Text != "第一页末" {
		t.Errorf("text before the break = %q, want %q", doc.Nodes[1].Text, "第一页末")
	}
	if doc.Nodes[3].Text != "第二页首" {
		t.Errorf("text after the break = %q, want %q", doc.Nodes[3].Text, "第二页首")
	}
	// Page accounting: the leading standalone break sits on page 1 and turns the
	// page, so the paragraph that follows starts on page 2; its own
	// mid-paragraph break turns the page again, putting the text after it on
	// page 3. This keeps SourcePage usable even though this fixture carries no
	// w:lastRenderedPageBreak markers.
	wantPages := []int{1, 2, 2, 3}
	for i, want := range wantPages {
		if doc.Nodes[i].SourcePage != want {
			t.Errorf("node %d (%s %q) SourcePage = %d, want %d",
				i, doc.Nodes[i].Kind, doc.Nodes[i].Text, doc.Nodes[i].SourcePage, want)
		}
	}
	for _, n := range doc.Nodes {
		if n.Kind == docmodel.KindPageBreak && n.Text != "" {
			t.Errorf("page break node carries text %q", n.Text)
		}
	}
}

// TestDOCXPieceOrder proves that a paragraph mixing text, a picture and more
// text is split into nodes in the source's own order — the picture is neither
// dropped nor hoisted to one end of the paragraph.
func TestDOCXPieceOrder(t *testing.T) {
	rels := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/>
</Relationships>`
	drawing := `<w:r><w:drawing><a:blip r:embed="rId9"/></w:drawing></w:r>`
	body := para("", run("图前")+drawing+run("图后"))

	doc := mustParse(t, "s.docx", buildDOCX(t, body, map[string][]byte{
		"word/_rels/document.xml.rels": []byte(rels),
		"word/media/pic.png":           []byte("PNGDATA"),
	}))

	if len(doc.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3: %q", len(doc.Nodes), textsOf(doc))
	}
	if doc.Nodes[0].Text != "图前" || doc.Nodes[0].Kind != docmodel.KindParagraph {
		t.Errorf("node 0 = %s %q, want paragraph %q", doc.Nodes[0].Kind, doc.Nodes[0].Text, "图前")
	}
	if doc.Nodes[1].Kind != docmodel.KindImage {
		t.Errorf("node 1 = %s, want %s", doc.Nodes[1].Kind, docmodel.KindImage)
	}
	if doc.Nodes[2].Text != "图后" {
		t.Errorf("node 2 = %q, want %q", doc.Nodes[2].Text, "图后")
	}
}

// TestDOCXIDsAndOrder pins the id/order contract: Order is 0-based and dense,
// ids are n0001-style and in ascending order.
func TestDOCXIDsAndOrder(t *testing.T) {
	body := para("", run("甲")) + para("", run("乙")) + para("", run("丙"))
	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))

	wantIDs := []string{"n0001", "n0002", "n0003"}
	if len(doc.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(doc.Nodes))
	}
	for i, n := range doc.Nodes {
		if n.Order != i {
			t.Errorf("node %s Order = %d, want %d", n.ID, n.Order, i)
		}
		if n.ID != wantIDs[i] {
			t.Errorf("node %d ID = %q, want %q", i, n.ID, wantIDs[i])
		}
	}
}

// TestDOCXHyperlinkAndTrackedInsertion checks that runs nested one level deep
// (a hyperlink, a tracked insertion, a content control) are still part of the
// paragraph's text rather than being silently dropped.
func TestDOCXHyperlinkAndTrackedInsertion(t *testing.T) {
	body := para("", run("详见")+
		`<w:hyperlink r:id="rId3">`+run("链接文字")+`</w:hyperlink>`+
		`<w:ins w:id="1" w:author="a">`+run("插入内容")+`</w:ins>`+
		`<w:sdt><w:sdtContent>`+run("控件内容")+`</w:sdtContent></w:sdt>`+
		run("。"))
	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))

	const want = "详见链接文字插入内容控件内容。"
	if len(doc.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1: %q", len(doc.Nodes), textsOf(doc))
	}
	if doc.Nodes[0].Text != want {
		t.Errorf("Text = %q, want %q", doc.Nodes[0].Text, want)
	}
}

// TestDOCXRunsCarryFormatting checks that inline styling is preserved for the
// runs of a paragraph, since the assembler and writer depend on it.
func TestDOCXRunsCarryFormatting(t *testing.T) {
	body := para("",
		`<w:r><w:rPr><w:b/><w:rFonts w:eastAsia="仿宋_GB2312"/></w:rPr><w:t xml:space="preserve">加粗仿宋</w:t></w:r>`+
			`<w:r><w:rPr><w:i/></w:rPr><w:t xml:space="preserve">斜体</w:t></w:r>`+
			`<w:r><w:rPr><w:b w:val="0"/></w:rPr><w:t xml:space="preserve">非粗体</w:t></w:r>`)

	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))
	n := doc.Nodes[0]
	if n.Text != "加粗仿宋斜体非粗体" {
		t.Fatalf("Text = %q", n.Text)
	}
	if len(n.Runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(n.Runs))
	}
	if !n.Runs[0].Bold || n.Runs[0].SourceFont != "仿宋_GB2312" {
		t.Errorf("run 0 = %+v, want bold 仿宋_GB2312", n.Runs[0])
	}
	if !n.Runs[1].Italic || n.Runs[1].Bold {
		t.Errorf("run 1 = %+v, want italic non-bold", n.Runs[1])
	}
	if n.Runs[2].Bold {
		t.Errorf("run 2 = %+v, want w:b w:val=\"0\" to mean non-bold", n.Runs[2])
	}
}

// TestDOCXTabIsContentWithinParagraph checks that a w:tab stays a tab
// character in the paragraph text: it is source content inside a paragraph,
// unlike the tabs that indent a Markdown list.
func TestDOCXTabIsContentWithinParagraph(t *testing.T) {
	body := para("", run("姓名")+runRaw("<w:tab/>")+run("　张三"))
	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))

	const want = "姓名\t　张三"
	if doc.Nodes[0].Text != want {
		t.Errorf("Text = %q, want %q", doc.Nodes[0].Text, want)
	}
}

// TestDOCXSourcePageFromRenderedBreak checks the page counter derived from
// Word's own w:lastRenderedPageBreak markers: the paragraph that begins the new
// page reports the new page, and the ones after it keep reporting it.
func TestDOCXSourcePageFromRenderedBreak(t *testing.T) {
	body := para("", run("第一页")) +
		para("", `<w:r><w:lastRenderedPageBreak/><w:t xml:space="preserve">第二页首</w:t></w:r>`) +
		para("", run("第二页次"))

	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))
	want := []int{1, 2, 2}
	if len(doc.Nodes) != len(want) {
		t.Fatalf("nodes = %d, want %d", len(doc.Nodes), len(want))
	}
	for i, w := range want {
		if doc.Nodes[i].SourcePage != w {
			t.Errorf("node %d (%q) SourcePage = %d, want %d",
				i, doc.Nodes[i].Text, doc.Nodes[i].SourcePage, w)
		}
	}
}

// TestDOCXListStyleMarksListItem covers the second, very common spelling of a
// list item: Word (and python-docx) apply numbering through a paragraph style
// such as "List Number" or "List Paragraph", so the paragraph itself carries no
// w:numPr. This was found by parsing a real python-docx-produced file, which is
// why the style names are exercised explicitly.
func TestDOCXListStyleMarksListItem(t *testing.T) {
	for _, style := range []string{"ListNumber", "List Paragraph", "List Bullet", "列表段落", "项目符号"} {
		body := para(`<w:pPr><w:pStyle w:val="`+style+`"/></w:pPr>`, run("列表项"))
		doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))
		if len(doc.Nodes) != 1 {
			t.Fatalf("style %q: nodes = %d, want 1", style, len(doc.Nodes))
		}
		if doc.Nodes[0].Kind != docmodel.KindListItem {
			t.Errorf("style %q: Kind = %s, want %s", style, doc.Nodes[0].Kind, docmodel.KindListItem)
		}
	}
	// A non-list style must not be caught by the same heuristic.
	for _, style := range []string{"BodyText", "正文", "Normal", "Quote"} {
		body := para(`<w:pPr><w:pStyle w:val="`+style+`"/></w:pPr>`, run("正文"))
		doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))
		if doc.Nodes[0].Kind != docmodel.KindParagraph {
			t.Errorf("style %q: Kind = %s, want %s", style, doc.Nodes[0].Kind, docmodel.KindParagraph)
		}
	}
}

// TestDOCXRealPythonDocxFixture parses testdata/real_python_docx.docx: a real
// .docx produced by python-docx (see testdata/gen_fixture.py), not hand-written
// XML. It is the regression anchor for the whole-file path, and it caught two
// behaviours that fixtures written to match the parser could never reveal: Word
// marks a list item through the paragraph *style* ("List Number") rather than
// an inline w:numPr, and it records rendered page breaks only after a layout
// pass, so SourcePage must also advance on explicit breaks.
//
// Regenerate with:
//
//	python internal/parse/testdata/gen_fixture.py <out.docx> <tmp.png>
func TestDOCXRealPythonDocxFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "real_python_docx.docx"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc := mustParse(t, "real_python_docx.docx", data)

	byID := doc.ByID()
	type expect struct {
		id    string
		kind  docmodel.NodeKind
		level int
		text  string
	}
	want := []expect{
		{"n0001", docmodel.KindHeading, 1, "一、总体要求"},
		{"n0002", docmodel.KindHeading, 2, "（一）提高认识"},
		{"n0003", docmodel.KindParagraph, 0, "  前导空格全角　空格“引号”省略号……尾随  "},
		{"n0004", docmodel.KindParagraph, 0, "正文段落，包含\t制表符与……省略号。"},
		{"n0005", docmodel.KindListItem, 0, "列表项甲"},
		{"n0006", docmodel.KindTable, 0, ""},
		{"n0007", docmodel.KindPageBreak, 0, ""},
		{"n0008", docmodel.KindParagraph, 0, "第二页正文。"},
		{"n0009", docmodel.KindImage, 0, "image1.png"},
	}
	if len(doc.Nodes) != len(want) {
		t.Fatalf("nodes = %d, want %d:\n%s", len(doc.Nodes), len(want), dump(doc))
	}
	for i, w := range want {
		n, ok := byID[w.id]
		if !ok {
			t.Errorf("node %s missing", w.id)
			continue
		}
		if n.Kind != w.kind || n.Level != w.level || n.Text != w.text {
			t.Errorf("node %s = %s/%d %q, want %s/%d %q",
				w.id, n.Kind, n.Level, n.Text, w.kind, w.level, w.text)
		}
		if n.Order != i {
			t.Errorf("node %s Order = %d, want %d", w.id, n.Order, i)
		}
	}

	// Verbatim text is the point: these exact characters came out of a real
	// Word file, so any normalisation shows up here.
	n3 := byID["n0003"]
	if !strings.HasPrefix(n3.Text, "  ") || !strings.HasSuffix(n3.Text, "  ") {
		t.Errorf("spaces at the ends of n0003 were trimmed: %q", n3.Text)
	}
	if !strings.Contains(n3.Text, "　") {
		t.Errorf("full-width space lost from n0003: %q", n3.Text)
	}
	if strings.Contains(n3.Text, `"`) || strings.Contains(n3.Text, "...") {
		t.Errorf("n0003 was normalised: %q", n3.Text)
	}

	// Table cells and the picture payload.
	tbl := byID["n0006"]
	if len(tbl.Rows) != 2 || strings.Join(tbl.Rows[1], "|") != "1|甲　乙" {
		t.Errorf("table rows = %v, want the source cells verbatim", tbl.Rows)
	}
	img := byID["n0009"]
	if img.Image == nil || len(img.Image.Data) == 0 {
		t.Fatalf("image payload missing: %+v", img.Image)
	}
	if img.Image.MimeType != "image/png" {
		t.Errorf("image MimeType = %q, want %q", img.Image.MimeType, "image/png")
	}
	if !bytes.HasPrefix(img.Image.Data, []byte{0x89, 'P', 'N', 'G'}) {
		t.Errorf("image bytes are not a PNG: % x", img.Image.Data[:8])
	}
}

// TestDOCXBodyWithoutContent checks that an empty or content-free body yields
// a document with an empty (non-nil) node slice rather than a panic.
func TestDOCXBodyWithoutContent(t *testing.T) {
	for _, body := range []string{"", "<w:p/>", "<w:sectPr><w:pgSz/></w:sectPr>"} {
		doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))
		if doc.Nodes == nil {
			t.Errorf("body %q: Nodes = nil, want empty slice", body)
		}
		if len(doc.Nodes) != 0 {
			t.Errorf("body %q: nodes = %q, want none", body, textsOf(doc))
		}
	}
}

// TestDOCXErrCorrupt covers the failure modes that must not be silently
// accepted: a .docx that is not a zip or lacks word/document.xml.
func TestDOCXErrCorrupt(t *testing.T) {
	cases := map[string][]byte{
		"not a zip":       []byte("this is just plain text, not an OOXML package"),
		"no document.xml": buildDOCXNoDocument(t),
	}
	for name, data := range cases {
		_, err := Parse("broken.docx", data)
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}
}

// buildDOCXNoDocument builds a valid zip that is missing word/document.xml.
func buildDOCXNoDocument(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("docProps/core.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte("<x/>")); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// TestDOCXVerbatimSurvivesVerifyVerbatim closes the loop with the frozen
// verifier: assemble a trivial output document that echoes each parsed node's
// text by id — exactly what internal/assemble does — and check the diff is
// Clean(). If the parser ever rewrote text, this is the check that would catch
// it at the pipeline boundary.
func TestDOCXVerbatimSurvivesVerifyVerbatim(t *testing.T) {
	body := para("", run("通知")) +
		para("", run(" 前导空格与“引号”…… ")) +
		para("", run("全角　空格"))
	doc := mustParse(t, "s.docx", buildDOCX(t, body, nil))

	out := &docmodel.Doc{}
	for _, n := range doc.Nodes {
		if !n.Kind.CarriesProse() {
			continue
		}
		out.Blocks = append(out.Blocks, docmodel.OutBlock{
			Role: docmodel.RoleBody, Text: n.Text, NodeID: n.ID, Level: n.Level,
		})
	}
	diff := docmodel.VerifyVerbatim(doc, out, nil)
	if !diff.Clean() {
		t.Errorf("VerifyVerbatim not clean: missing=%q altered=%q", diff.Missing, diff.Altered)
	}
}

// TestXMLTreeKeepsCharData guards the low-level container directly: the tree
// must expose character data exactly, and attribute lookup must be
// prefix-insensitive.
func TestXMLTreeKeepsCharData(t *testing.T) {
	raw := []byte(`<w:p xmlns:w="urn:w"><w:r><w:t xml:space="preserve">  两 个 空 格  </w:t></w:r>` +
		`<w:br w:type="page"/></w:p>`)
	root, err := parseXMLTree(raw)
	if err != nil {
		t.Fatalf("parseXMLTree: %v", err)
	}
	ws := root.findAll("t")
	if len(ws) != 1 {
		t.Fatalf("w:t count = %d, want 1", len(ws))
	}
	if got := ws[0].rawText(); got != "  两 个 空 格  " {
		t.Errorf("char data = %q, want %q", got, "  两 个 空 格  ")
	}
	br := root.findAll("br")
	if len(br) != 1 {
		t.Fatalf("w:br count = %d, want 1", len(br))
	}
	if v, ok := br[0].attr("type"); !ok || v != "page" {
		t.Errorf("w:br w:type = %q (ok=%v), want \"page\"", v, ok)
	}
	if root.child("r") == nil {
		t.Error("child(r) should find the direct w:r child of w:p")
	}
	if root.child("t") != nil {
		t.Error("child(t) must only match a direct child; w:t is nested inside w:r and must not be found")
	}
}

// TestXMLTreeRejectsMalformed documents the error path of the tree builder.
func TestXMLTreeRejectsMalformed(t *testing.T) {
	if _, err := parseXMLTree([]byte("<a><b></a>")); err == nil {
		t.Error("mismatched tags: want error, got nil")
	}
	if _, err := parseXMLTree([]byte("")); err == nil {
		t.Error("empty input: want error, got nil")
	}
}
