package parse

import (
	"errors"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// TestVerbatimTextAndMarkdownRoundTrip is the strongest statement of the
// verbatim invariant available for text input: each source *line* must appear
// in the parsed nodes exactly as it was written — same code points, same order,
// same terminators — with only the source's own block markup removed.
//
// The corpus deliberately uses characters a careless parser rewrites:
//
//	“ ” ‘ ’    curly quotes (never straightened)
//	　         full-width space U+3000 (never collapsed)
//	"   "      runs of consecutive ASCII spaces (never merged)
//	……         a double ellipsis (never folded to "...")
//	——         an em-dash pair
//	、。；：    full-width punctuation
//	\t         a literal tab, which is content in .txt
//	\r\n       CRLF terminators (never rewritten to LF)
//
// The parser's block grammar is line-based (one node per line for md, one node
// per blank-line-delimited paragraph for txt), so this test compares the
// concatenation of node texts against the source with the grammar's own block
// separators removed — not against a re-serialized Markdown rendering, which
// would be testing the inverse printer rather than the parser.
func TestVerbatimTextAndMarkdownRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		src  string
		// markup lists the source's own block markers, which are structure
		// rather than content and are therefore absent from the node texts.
		markup []string
	}{{
		name: "txt-crlf",
		file: "公 文.txt",
		src: "关于“格式”管理的通知\r\n" +
			"　　各单位：\r\n" +
			"　　为落实……要求，现将有关 事项 通知如下：\r\n" +
			"　　一、总体要求\r\n" +
			"　　（一）提高认识。不得随意增删\t空格。\r\n" +
			"　　二〇二六年九月十日\r\n",
	}, {
		name: "txt-lf",
		file: "公文.txt",
		src: "“引号”在行首\n" +
			"‘单引号’在行首\n" +
			"连续空格：甲   乙\n" +
			"全角空格：甲　　乙\n" +
			"省略号……与破折号——并列\n",
	}, {
		name: "md",
		file: "方案.md",
		src: "# 一、总体要求\n" +
			"\n" +
			"　　“引号”与……省略号 保留。\n" +
			"\n" +
			"- 列表甲  两个空格\n" +
			"- 列表乙\n" +
			"\n" +
			"| 项目 | 内容 |\n" +
			"| --- | --- |\n" +
			"| 甲 | 乙　丙 |\n" +
			"\n" +
			"> 引用“原文”\n" +
			"\n" +
			"## 二、具体要求\n",
		markup: []string{"# ", "## ", "- ", "> ", "| --- | --- |"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse(tc.file, []byte(tc.src))
			if err != nil {
				t.Fatalf("Parse(%s): %v", tc.file, err)
			}

			// Every source line must be reproduced by some node text.
			for i, srcLine := range strings.Split(strings.ReplaceAll(tc.src, "\r\n", "\n"), "\n") {
				if srcLine == "" {
					continue
				}
				content := srcLine
				for _, m := range tc.markup {
					if strings.HasPrefix(content, m) {
						content = strings.TrimPrefix(content, m)
						break
					}
				}
				if content == "" {
					continue
				}
				if !strings.Contains(reconstruct(doc), content) {
					t.Errorf("source line %d (%q) is missing from the parsed nodes:\n%s",
						i, srcLine, dump(doc))
				}
			}

			// No normalisation may appear anywhere in the node texts.
			all := reconstruct(doc)
			for _, bad := range []struct{ name, needle string }{
				{"straight double quote", `"`},
				{"straight single quote", "'"},
				{"three-dot ellipsis", "..."},
				{"merged consecutive spaces", "甲 乙"},
				{"collapsed full-width space pair", "甲 乙"},
			} {
				if strings.Contains(all, bad.needle) && !strings.Contains(tc.src, bad.needle) {
					t.Errorf("normalisation detected (%s): %q", bad.name, all)
				}
			}
			// The exact multi-space and full-width-space runs survive.
			switch tc.name {
			case "txt-lf":
				if !strings.Contains(all, "甲   乙") {
					t.Errorf("three consecutive spaces were not preserved: %q", all)
				}
				if !strings.Contains(all, "甲　　乙") {
					t.Errorf("full-width spaces were not preserved: %q", all)
				}
				if !strings.Contains(all, "……") || !strings.Contains(all, "——") {
					t.Errorf("ellipsis/em-dash were rewritten: %q", all)
				}
			case "md":
				if !strings.Contains(all, "乙　丙") {
					t.Errorf("table cell full-width space was rewritten: %q", all)
				}
				if !strings.Contains(all, "列表甲  两个空格") {
					t.Errorf("list item double space was rewritten: %q", all)
				}
			}
		})
	}
}

// reconstruct rebuilds a document's source text from its nodes: prose nodes are
// joined by newlines, tables are rebuilt as Markdown pipe rows (the shape the
// md parser consumes), and page breaks become the form feed-like marker the
// round-trip corpus uses. It is intentionally a faithful inverse of the two
// block grammars, not a re-serializer: anything the parser dropped or rewrote
// shows up as a mismatch.
func reconstruct(doc *docmodel.Document) string {
	var parts []string
	for _, n := range doc.Nodes {
		switch n.Kind {
		case docmodel.KindTable:
			var rows []string
			for i, r := range n.Rows {
				rows = append(rows, "| "+strings.Join(r, " | ")+" |")
				if i == 0 {
					sep := make([]string, len(r))
					for j := range sep {
						sep[j] = "---"
					}
					rows = append(rows, "| "+strings.Join(sep, " | ")+" |")
				}
			}
			parts = append(parts, strings.Join(rows, "\n"))
		default:
			parts = append(parts, n.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// TestVerbatimTextCRLFSurvives checks the terminator specifically, because
// terminator rewriting (CRLF → LF) is the most common silent alteration in
// text ingestion.
func TestVerbatimTextCRLFSurvives(t *testing.T) {
	src := "第一段两行\r\n仍然第一段\r\n\r\n第二段\r\n"
	doc := mustParse(t, "a.txt", []byte(src))
	if len(doc.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2: %q", len(doc.Nodes), textsOf(doc))
	}
	if doc.Nodes[0].Text != "第一段两行\r\n仍然第一段" {
		t.Errorf("paragraph 1 = %q, want %q", doc.Nodes[0].Text, "第一段两行\r\n仍然第一段")
	}
	if doc.Nodes[1].Text != "第二段" {
		t.Errorf("paragraph 2 = %q, want %q", doc.Nodes[1].Text, "第二段")
	}
}

// TestTextParagraphsSplitOnBlankLinesOnly pins the paragraph rule: a blank line
// separates, but a whitespace-only line does not, because those spaces are
// source content.
func TestTextParagraphsSplitOnBlankLinesOnly(t *testing.T) {
	src := "甲\n\n乙\n　　\n丙\n"
	doc := mustParse(t, "a.txt", []byte(src))
	want := []string{"甲", "乙\n　　\n丙"}
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

// TestTextHeadingDetection checks the Chinese title patterns, and that the
// marker itself stays in the text (it is the source's own numbering, which the
// assembler recognises with hasOwnNumber).
func TestTextHeadingDetection(t *testing.T) {
	src := "第一章 总则\n" +
		"第一条 为了规范……\n" +
		"一、总体要求\n" +
		"（一）提高认识\n" +
		"1. 明确责任\n" +
		"（1）细化措施\n" +
		"　　（二）落实到位\n" +
		"普通正文段落。\n"
	doc := mustParse(t, "a.txt", []byte(src))

	want := []struct {
		kind  docmodel.NodeKind
		level int
	}{
		{docmodel.KindHeading, 1},
		{docmodel.KindHeading, 1},
		{docmodel.KindHeading, 1},
		{docmodel.KindHeading, 2},
		{docmodel.KindHeading, 3},
		{docmodel.KindHeading, 4},
		{docmodel.KindHeading, 2},
		{docmodel.KindParagraph, 0},
	}
	if len(doc.Nodes) != len(want) {
		t.Fatalf("nodes = %d (%q), want %d", len(doc.Nodes), textsOf(doc), len(want))
	}
	for i, w := range want {
		n := doc.Nodes[i]
		if n.Kind != w.kind || n.Level != w.level {
			t.Errorf("node %d (%q) = %s/%d, want %s/%d",
				i, n.Text, n.Kind, n.Level, w.kind, w.level)
		}
	}
	// The marker is part of the text, not stripped: the assembler needs it to
	// avoid printing "一、一、总体要求".
	if doc.Nodes[2].Text != "一、总体要求" {
		t.Errorf("heading text = %q, want the marker kept: %q", doc.Nodes[2].Text, "一、总体要求")
	}
	// A heading line does not absorb the prose that follows it.
	if doc.Nodes[7].Text != "普通正文段落。" {
		t.Errorf("trailing prose = %q, want %q", doc.Nodes[7].Text, "普通正文段落。")
	}
}

// TestTextProseIsNeverMistakenForAHeading checks the false-positive side: a
// sentence that merely contains a marker, or a number used mid-sentence, must
// stay a paragraph.
func TestTextProseIsNeverMistakenForAHeading(t *testing.T) {
	src := "本文分为三章。\n" +
		"2026年9月10日印发。\n" +
		"一是要提高认识，二是要落实责任。\n" +
		"（参见附件1）\n"
	doc := mustParse(t, "a.txt", []byte(src))
	for i, n := range doc.Nodes {
		if n.Kind != docmodel.KindParagraph {
			t.Errorf("node %d (%q) = %s, want paragraph", i, n.Text, n.Kind)
		}
	}
}

// TestMarkdownStructure covers headings, lists (unordered and ordered, with
// indentation depth), tables and paragraphs.
func TestMarkdownStructure(t *testing.T) {
	src := "# 一级标题\n" +
		"## 二级标题\n" +
		"###### 六级标题\n" +
		"####### 七个井号不是标题\n" +
		"#hashtag不是标题\n" +
		"- 无序项\n" +
		"* 星号项\n" +
		"+ 加号项\n" +
		"1. 有序项\n" +
		"2) 右括号有序项\n" +
		"  - 缩进项\n" +
		"普通段落\n" +
		"\n" +
		"| 序号 | 事项 |\n" +
		"| :--- | ---: |\n" +
		"| 1 | 甲 |\n" +
		"| 2 | 乙 |\n" +
		"\n" +
		"> 引用段落\n" +
		"\n" +
		"## 标题带闭合井号 ##\n"

	doc := mustParse(t, "a.md", []byte(src))
	want := []struct {
		kind  docmodel.NodeKind
		level int
		text  string
	}{
		{docmodel.KindHeading, 1, "一级标题"},
		{docmodel.KindHeading, 2, "二级标题"},
		{docmodel.KindHeading, 6, "六级标题"},
		{docmodel.KindParagraph, 0, "####### 七个井号不是标题"},
		{docmodel.KindParagraph, 0, "#hashtag不是标题"},
		{docmodel.KindListItem, 1, "无序项"},
		{docmodel.KindListItem, 1, "星号项"},
		{docmodel.KindListItem, 1, "加号项"},
		{docmodel.KindListItem, 1, "有序项"},
		{docmodel.KindListItem, 1, "右括号有序项"},
		{docmodel.KindListItem, 2, "缩进项"},
		{docmodel.KindParagraph, 0, "普通段落"},
		{docmodel.KindTable, 0, ""},
		{docmodel.KindParagraph, 0, "引用段落"},
		{docmodel.KindHeading, 2, "标题带闭合井号"},
	}
	if len(doc.Nodes) != len(want) {
		t.Fatalf("nodes = %d, want %d:\n%s", len(doc.Nodes), len(want), dump(doc))
	}
	for i, w := range want {
		n := doc.Nodes[i]
		if n.Kind != w.kind || n.Level != w.level || n.Text != w.text {
			t.Errorf("node %d = %s/%d %q, want %s/%d %q",
				i, n.Kind, n.Level, n.Text, w.kind, w.level, w.text)
		}
	}

	tbl := doc.Nodes[12]
	if !tbl.HeaderRow {
		t.Error("table HeaderRow = false, want true")
	}
	if len(tbl.Rows) != 3 {
		t.Fatalf("table rows = %v, want 3", tbl.Rows)
	}
	if got := strings.Join(tbl.Rows[1], "|"); got != "1|甲" {
		t.Errorf("table row 1 = %q, want %q", got, "1|甲")
	}
	if got := strings.Join(tbl.Rows[2], "|"); got != "2|乙" {
		t.Errorf("table row 2 = %q, want %q", got, "2|乙")
	}
}

// TestMarkdownTableCellTextIsVerbatim checks that cell text is not trimmed of
// interior whitespace and that alignment markers stay out of the data.
func TestMarkdownTableCellTextIsVerbatim(t *testing.T) {
	src := "| 名称 | 说明 |\n" +
		"| --- | --- |\n" +
		"| “引号” | 甲　乙 |\n" +
		"| 空格 | a  b |\n"
	doc := mustParse(t, "a.md", []byte(src))
	if len(doc.Nodes) != 1 || doc.Nodes[0].Kind != docmodel.KindTable {
		t.Fatalf("want one table, got %s", dump(doc))
	}
	rows := doc.Nodes[0].Rows
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want header + 2 data rows", rows)
	}
	if rows[1][0] != "“引号”" {
		t.Errorf("cell = %q, want %q", rows[1][0], "“引号”")
	}
	if rows[1][1] != "甲　乙" {
		t.Errorf("cell = %q, want %q", rows[1][1], "甲　乙")
	}
	if rows[2][1] != "a  b" {
		t.Errorf("cell = %q, want %q (consecutive spaces kept)", rows[2][1], "a  b")
	}
}

// TestMarkdownThematicBreakIsNotATable guards the delimiter-row heuristic.
func TestMarkdownThematicBreakIsNotATable(t *testing.T) {
	src := "上文\n\n---\n\n下文\n"
	doc := mustParse(t, "a.md", []byte(src))
	for i, n := range doc.Nodes {
		if n.Kind == docmodel.KindTable {
			t.Errorf("node %d (%q) was taken for a table", i, n.Text)
		}
	}
}

// TestMarkdownFencedCodeKeepsContentLineByLine checks that fence markers are
// markup (dropped) while every content line is preserved exactly, including
// its indentation.
func TestMarkdownFencedCodeKeepsContentLineByLine(t *testing.T) {
	src := "```go\n\tif x {  // “注释”\n}\n```\n"
	doc := mustParse(t, "a.md", []byte(src))
	want := []string{"\tif x {  // “注释”", "}"}
	got := textsOf(doc)
	if len(got) != len(want) {
		t.Fatalf("texts = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("code line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestEncodingHandling covers BOM handling and the refusal to guess at a
// non-UTF-8 encoding, which would silently rewrite the text.
func TestEncodingHandling(t *testing.T) {
	t.Run("utf8 bom stripped", func(t *testing.T) {
		doc := mustParse(t, "a.txt", append([]byte{0xEF, 0xBB, 0xBF}, []byte("正文“引号”")...))
		if doc.Nodes[0].Text != "正文“引号”" {
			t.Errorf("Text = %q, want %q (BOM must not leak into text)", doc.Nodes[0].Text, "正文“引号”")
		}
	})

	t.Run("utf16le", func(t *testing.T) {
		le := []byte{0xFF, 0xFE}
		for _, r := range "甲乙“丙”" {
			le = append(le, byte(r), byte(r>>8))
		}
		doc := mustParse(t, "a.txt", le)
		if doc.Nodes[0].Text != "甲乙“丙”" {
			t.Errorf("Text = %q, want %q", doc.Nodes[0].Text, "甲乙“丙”")
		}
	})

	t.Run("gbk refused", func(t *testing.T) {
		// 0xC4 0xE3 0xBA 0xC3 is GBK for 你好; it is not valid UTF-8.
		_, err := Parse("a.txt", []byte{0xC4, 0xE3, 0xBA, 0xC3})
		if !errors.Is(err, ErrUnsupportedEncoding) {
			t.Errorf("err = %v, want ErrUnsupportedEncoding", err)
		}
	})
}

// TestUnsupportedFormat checks the documented error for an unknown extension,
// including the extensionless and empty-name cases.
func TestUnsupportedFormat(t *testing.T) {
	for _, name := range []string{"a.pdf", "a.doc", "a.rtf", "a", "", "a.DOCX.bak", "报表.xlsx"} {
		_, err := Parse(name, []byte("正文"))
		if !errors.Is(err, ErrUnsupportedFormat) {
			t.Errorf("Parse(%q) err = %v, want ErrUnsupportedFormat", name, err)
		}
	}
}

// TestExtensionCaseInsensitiveAndDocumentShape checks that .DOCX/.TXT are
// accepted (real uploads are rarely lower-case) and that the returned Document
// carries its format and source name.
func TestExtensionCaseInsensitiveAndDocumentShape(t *testing.T) {
	cases := []struct {
		name   string
		data   []byte
		format string
	}{
		{"公告.TXT", []byte("正文"), "txt"},
		{"公告.MD", []byte("# 标题"), "md"},
		{"公告.Markdown", []byte("# 标题"), "md"},
		{"公告.docx", buildDOCX(t, para("", run("正文")), nil), "docx"},
	}
	for _, tc := range cases {
		doc, err := Parse(tc.name, tc.data)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.name, err)
		}
		if doc.Format != tc.format {
			t.Errorf("Parse(%q).Format = %q, want %q", tc.name, doc.Format, tc.format)
		}
		if doc.SourceName != tc.name {
			t.Errorf("Parse(%q).SourceName = %q", tc.name, doc.SourceName)
		}
		if doc.Nodes == nil {
			t.Errorf("Parse(%q).Nodes = nil, want a non-nil slice", tc.name)
		}
	}
}

// TestPlainTextIDsAreDense checks the id/order contract for the text parsers.
func TestPlainTextIDsAreDense(t *testing.T) {
	doc := mustParse(t, "a.txt", []byte("甲\n\n乙\n\n丙\n"))
	if len(doc.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(doc.Nodes))
	}
	for i, n := range doc.Nodes {
		if n.Order != i {
			t.Errorf("node %s Order = %d, want %d", n.ID, n.Order, i)
		}
		if want := "n" + pad4(i+1); n.ID != want {
			t.Errorf("node %d ID = %q, want %q", i, n.ID, want)
		}
	}
}

// TestEmptyTextUpload checks that an empty upload is a valid, empty document
// rather than an error or a panic.
func TestEmptyTextUpload(t *testing.T) {
	for _, name := range []string{"a.txt", "a.md"} {
		doc, err := Parse(name, nil)
		if err != nil {
			t.Fatalf("Parse(%q, nil): %v", name, err)
		}
		if len(doc.Nodes) != 0 {
			t.Errorf("Parse(%q, nil) nodes = %q, want none", name, textsOf(doc))
		}
	}
}

func pad4(n int) string {
	s := "0000"
	d := []byte(s)
	for i := 3; i >= 0 && n > 0; i-- {
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d)
}

// dump renders a document for failure messages.
func dump(doc *docmodel.Document) string {
	var sb strings.Builder
	for _, n := range doc.Nodes {
		sb.WriteString("  ")
		sb.WriteString(n.ID)
		sb.WriteString(" ")
		sb.WriteString(string(n.Kind))
		if n.Level > 0 {
			sb.WriteString("/")
			sb.WriteString(pad4(n.Level))
		}
		sb.WriteString(" ")
		sb.WriteString(strings.ReplaceAll(n.Text, "\n", "\\n"))
		sb.WriteString("\n")
	}
	return sb.String()
}
