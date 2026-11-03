package parse_test

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
	"govdoc/internal/parse"
)

// TestParsedImageAndTableNodesReachTheOutputVerbatim is a cross-package check
// of the contract between the parser and the frozen verifier for the two node
// kinds that carry no prose: KindTable (payload in Rows) and KindImage
// (payload in Image).
//
// The parser fills exactly the fields the downstream stages read:
//
//   - a table node carries Rows, which the assembler copies into OutBlock.Rows
//     and the DOCX writer emits as a real w:tbl;
//   - an image node carries Image.Data, which the writer embeds from
//     word/media, plus Text holding the source's own file name taken from the
//     package relationship.
//
// Neither node's payload can be expressed as Node.Text, so this test pins the
// contract that makes them survive export: VerifyVerbatim accounts for each
// node kind by its own payload — Table by the Rows cell matrix, Image by the
// picture bytes — so a correctly emitted document is Clean() even though a
// table's Text is deliberately empty.
func TestParsedImageAndTableNodesReachTheOutputVerbatim(t *testing.T) {
	const png = "PNGBYTES"
	doc := parseFixture(t)

	var table, image *docmodel.Node
	for i := range doc.Nodes {
		switch doc.Nodes[i].Kind {
		case docmodel.KindTable:
			table = &doc.Nodes[i]
		case docmodel.KindImage:
			image = &doc.Nodes[i]
		}
	}
	if table == nil || image == nil {
		t.Fatalf("fixture must yield one table and one image: %+v", doc.Nodes)
	}

	// The payloads the downstream stages consume are present and intact.
	if len(table.Rows) != 2 || table.Rows[1][1] != "甲　乙" {
		t.Errorf("table Rows = %v, want the source cells verbatim", table.Rows)
	}
	if image.Image == nil || string(image.Image.Data) != png {
		t.Fatalf("image payload = %+v, want %q", image.Image, png)
	}
	if image.Text != "pic.png" {
		t.Errorf("image Text = %q, want the source file name %q", image.Text, "pic.png")
	}

	// Simulate what the assembler does: every node is emitted from its own
	// fields, prose by Text, tables by Rows, pictures by Image.
	var out docmodel.Doc
	for _, n := range doc.Nodes {
		if !n.Kind.CarriesProse() {
			continue
		}
		out.Blocks = append(out.Blocks, docmodel.OutBlock{
			Role: docmodel.RoleBody, NodeID: n.ID, Text: n.Text,
			Rows: n.Rows, Image: n.Image,
		})
	}
	diff := docmodel.VerifyVerbatim(doc, &out, nil)

	// A fully correct document must be exportable: nothing missing, nothing
	// altered, nothing invented. This is the check that would have failed while
	// the verifier compared a table by its (empty) Text.
	if !diff.Clean() {
		t.Errorf("correctly assembled document is not Clean: missing=%v altered=%v",
			diff.Missing, diff.Altered)
	}
	if len(diff.Missing) != 0 {
		t.Errorf("missing = %v, want none (tables are accounted by Rows, images by bytes)", diff.Missing)
	}
	if len(diff.Altered) != 0 {
		t.Errorf("altered content: %v", diff.Altered)
	}
	if len(diff.Extra) != 0 {
		t.Errorf("unexpected extra output text: %v", diff.Extra)
	}

	// The payloads themselves must be byte-identical for the writer to embed
	// them, which is what actually carries images and tables to the output.
	if strings.Join(out.Blocks[1].Rows[1], "\x00") != strings.Join(table.Rows[1], "\x00") {
		t.Error("table cells changed between the source node and the output block")
	}
	if !bytes.Equal(out.Blocks[2].Image.Data, image.Image.Data) {
		t.Error("image bytes changed between the source node and the output block")
	}
}

// TestTableAlterationIsDetected is the negative half of the contract above: if a
// downstream stage silently changes a cell or drops the table, the parser's
// Rows must make VerifyVerbatim refuse to export. It proves the parser exposes
// tables in a form the export gate can actually police.
func TestTableAlterationIsDetected(t *testing.T) {
	doc := parseFixture(t)
	var table docmodel.Node
	for _, n := range doc.Nodes {
		if n.Kind == docmodel.KindTable {
			table = n
		}
	}

	// Emit a table whose cell text was rewritten, which is exactly the failure
	// the verbatim invariant exists to catch.
	tampered := make([][]string, len(table.Rows))
	for i, r := range table.Rows {
		tampered[i] = append([]string(nil), r...)
	}
	tampered[1][1] = "被改写的单元格"

	out := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, NodeID: table.ID, Rows: tampered},
	}}
	if diff := docmodel.VerifyVerbatim(doc, out, nil); len(diff.Altered) == 0 {
		t.Error("a rewritten table cell was not reported as Altered; the export gate is blind to tables")
	}

	// Dropping the table entirely must be reported too.
	out = &docmodel.Doc{}
	if diff := docmodel.VerifyVerbatim(doc, out, nil); len(diff.Missing) == 0 {
		t.Error("a dropped table was not reported as Missing")
	}
}

// parseFixture builds a minimal document containing a paragraph, a table and a
// picture, using the standard library only.
func parseFixture(t *testing.T) *docmodel.Document {
	t.Helper()
	body := "<w:p><w:r><w:t xml:space=\"preserve\">正文“引号”</w:t></w:r></w:p>" +
		"<w:tbl>" +
		"<w:tr><w:tc><w:p><w:r><w:t>名称</w:t></w:r></w:p></w:tc>" +
		"<w:tc><w:p><w:r><w:t>说明</w:t></w:r></w:p></w:tc></w:tr>" +
		"<w:tr><w:tc><w:p><w:r><w:t>甲</w:t></w:r></w:p></w:tc>" +
		"<w:tc><w:p><w:r><w:t xml:space=\"preserve\">甲　乙</w:t></w:r></w:p></w:tc></w:tr>" +
		"</w:tbl>" +
		"<w:p><w:r><w:drawing><a:blip r:embed=\"rId5\"/></w:drawing></w:r></w:p>"
	rels := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/>
</Relationships>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := map[string]string{
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>` +
			`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"` +
			` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"` +
			` xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">` +
			`<w:body>` + body + `</w:body></w:document>`,
		"word/_rels/document.xml.rels": rels,
		"word/media/pic.png":           "PNGBYTES",
	}
	for name, content := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	doc, err := parse.Parse("fixture.docx", buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}
