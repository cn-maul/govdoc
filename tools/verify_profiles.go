//go:build ignore

// Command verify_profiles checks the generalisation: the same document, rendered
// under different profiles, must come out materially different — and the 公文
// standard must be untouched by the change.
//
// Usage: go run tools/verify_profiles.go
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"govdoc/internal/assemble"
	"govdoc/internal/config"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/render/docx"
)

func main() {
	doc, plan := sample()

	fmt.Println("========== 通用化验证：同一文档套用不同配置 ==========")
	fmt.Println()

	type row struct {
		name             string
		font, size, page string
		bytes            int
	}
	var rows []row

	for _, p := range config.BuiltinProfiles() {
		spec, err := p.Decode()
		if err != nil {
			fmt.Printf("%-6s 解码失败: %v\n", p.Name, err)
			continue
		}
		res, err := assemble.Build(doc, plan, spec)
		if err != nil {
			fmt.Printf("%-6s 装配失败: %v\n", p.Name, err)
			continue
		}
		data, err := docx.Render(res.Doc, spec)
		if err != nil {
			fmt.Printf("%-6s docx 失败: %v\n", p.Name, err)
			continue
		}
		document, styles := parts(data)
		// The style table is where a typeface and size actually live: a
		// paragraph only *references* a style by id. Reading them out of
		// document.xml finds nothing at all, and a verifier that then compares
		// two columns of "-" concludes that every profile is identical.
		font, size := styleLookup(styles, docmodel.RoleBody)
		rows = append(rows, row{p.Name, font, size, pageSetupOf(document), len(data)})
	}

	fmt.Printf("%-6s %-10s %-9s %-30s %s\n", "配置", "正文字体", "正文字号", "页面 / 页边距(上右下左)", "字节")
	for _, r := range rows {
		fmt.Printf("%-6s %-10s %-9s %-30s %d\n", r.name, r.font, r.size, r.page, r.bytes)
	}
	fmt.Println()

	// Every profile must be distinguishable from every other by something the
	// user can see. Otherwise "选用不同配置" is decoration.
	fmt.Println("---------- 两两对比（每套配置的可见排版必须不同）----------")
	type sig struct{ font, size, page string }
	seen := map[sig][]string{}
	for _, r := range rows {
		seen[sig{r.font, r.size, r.page}] = append(seen[sig{r.font, r.size, r.page}], r.name)
	}
	dupes := 0
	for k, names := range seen {
		if len(names) > 1 {
			dupes++
			fmt.Printf("  ✗ %v 产出完全相同：%s\n", k, strings.Join(names, "、"))
		}
	}
	if dupes == 0 {
		fmt.Printf("  ✓ %d 套配置互不相同\n", len(rows))
	}
	fmt.Println()

	check("公文标准未被通用化改动", func() []string {
		g := gbstandard.Default()
		var bad []string
		if g.Page.LinesPerPage != 22 || g.Page.CharsPerLine != 28 {
			bad = append(bad, fmt.Sprintf("22行×28字 丢失：%d×%d", g.Page.LinesPerPage, g.Page.CharsPerLine))
		}
		if g.Styles[docmodel.RoleBody].Size != gbstandard.SizeSanhao {
			bad = append(bad, fmt.Sprintf("正文应为三号，实际 %s", g.Styles[docmodel.RoleBody].Size))
		}
		if g.Styles[docmodel.RoleBody].Font != gbstandard.FontFangSong {
			bad = append(bad, fmt.Sprintf("正文应为仿宋，实际 %s", g.Styles[docmodel.RoleBody].Font))
		}
		if g.GridCondensePT(docmodel.RoleTitle) != 0 {
			bad = append(bad, "标题被压缩字距（字形会重叠）")
		}
		if g.GridCondensePT(docmodel.RoleBody) <= 0 {
			bad = append(bad, "正文未落在字符网格上")
		}
		return bad
	})

	check("其它配置不应使用字符网格", func() []string {
		var bad []string
		for _, p := range config.BuiltinProfiles() {
			if p.ID == config.ProfileGovDoc {
				continue
			}
			spec, _ := p.Decode()
			if spec.Page.LinesPerPage > 0 || spec.Page.CharsPerLine > 0 {
				bad = append(bad, p.Name+" 声明了字符网格")
			}
		}
		return bad
	})

	check("其它配置不应画公文红线", func() []string {
		var bad []string
		for _, p := range config.BuiltinProfiles() {
			if p.ID == config.ProfileGovDoc {
				continue
			}
			spec, _ := p.Decode()
			if st, ok := spec.Style(docmodel.RoleRedLine); ok && st.BorderBottom != nil {
				bad = append(bad, p.Name+" 的分隔线带边框")
			}
		}
		return bad
	})
}

func check(title string, fn func() []string) {
	fmt.Printf("---------- %s ----------\n", title)
	if bad := fn(); len(bad) == 0 {
		fmt.Println("  ✓ 通过")
	} else {
		for _, b := range bad {
			fmt.Printf("  ✗ %s\n", b)
		}
	}
	fmt.Println()
}

// sample builds one ordinary document with a title, a heading and body text.
func sample() (*docmodel.Document, *docmodel.Plan) {
	doc := &docmodel.Document{SourceName: "示例.txt", Format: "txt"}
	doc.Nodes = []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: "关于开展安全生产大检查的通知", Order: 0},
		{ID: "n0002", Kind: docmodel.KindHeading, Level: 1, Text: "一、总体要求", Order: 1},
		{ID: "n0003", Kind: docmodel.KindParagraph, Text: "坚持安全第一，认真负责，坚决防范各类事故发生。", Order: 2},
	}
	plan := &docmodel.Plan{
		Elements: []docmodel.ElementBinding{{Role: docmodel.RoleTitle, NodeID: "n0001"}},
		Sections: []docmodel.Section{{
			ID: "s1", Title: "一、总体要求", Level: 1,
			HeadingNodeID: "n0002", BodyNodeIDs: []string{"n0003"},
		}},
	}
	return doc, plan
}

// ---------------------------------------------------------------- xml helpers

// parts returns document.xml and styles.xml from the package.
//
// Both are needed: a paragraph references a style by id, and the typeface and
// size it resolves to are only in the style table.
func parts(data []byte) (document, styles string) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return
	}
	for _, f := range zr.File {
		var dst *string
		switch f.Name {
		case "word/document.xml":
			dst = &document
		case "word/styles.xml":
			dst = &styles
		default:
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		*dst = string(b)
	}
	return
}

var (
	styleDefRe = regexp.MustCompile(`(?s)<w:style [^>]*w:styleId="([^"]+)"[^>]*>(.*?)</w:style>`)
	eastAsiaRe = regexp.MustCompile(`w:eastAsia="([^"]+)"`)
	szRe       = regexp.MustCompile(`<w:sz w:val="(\d+)"`)
)

// styleLookup resolves a role to the typeface and point size its style declares.
func styleLookup(styles string, role docmodel.Role) (font, size string) {
	font, size = "-", "-"
	// The writer emits GovdocBody, not "body"; matching the bare role silently
	// finds nothing and every column comes back "-".
	want := "Govdoc" + strings.ToUpper(string(role[:1])) + string(role[1:])
	for _, m := range styleDefRe.FindAllStringSubmatch(styles, -1) {
		if m[1] != want {
			continue
		}
		if f := eastAsiaRe.FindStringSubmatch(m[2]); f != nil {
			font = f[1]
		}
		if s := szRe.FindStringSubmatch(m[2]); s != nil {
			if hp, err := strconv.Atoi(s[1]); err == nil {
				size = fmt.Sprintf("%gpt", float64(hp)/2)
			}
		}
		return font, size
	}
	return font, size
}

var (
	pgSzRe  = regexp.MustCompile(`<w:pgSz w:w="(\d+)" w:h="(\d+)"`)
	pgMarRe = regexp.MustCompile(`<w:pgMar w:top="(\d+)" w:right="(\d+)" w:bottom="(\d+)" w:left="(\d+)"`)
)

func pageSetupOf(document string) string {
	m := pgSzRe.FindStringSubmatch(document)
	n := pgMarRe.FindStringSubmatch(document)
	if m == nil || n == nil {
		return "-"
	}
	at := func(s string) int { v, _ := strconv.Atoi(s); return v }
	mm := func(twips int) float64 { return float64(twips) / 56.6929 }
	return fmt.Sprintf("%gx%gmm %g/%g/%g/%g",
		mm(at(m[1])), mm(at(m[2])),
		mm(at(n[1])), mm(at(n[3])), mm(at(n[4])), mm(at(n[2])))
}
