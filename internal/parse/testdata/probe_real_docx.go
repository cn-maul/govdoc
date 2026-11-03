//go:build ignore

// probe_real_docx.go — a manual harness that parses a real, externally produced
// .docx and prints the resulting nodes, so the parser can be inspected against
// output it did not generate. Run with:
//
//	go run internal/parse/testdata/probe_real_docx.go <file.docx>
package main

import (
	"fmt"
	"os"

	"govdoc/internal/parse"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe_real_docx <file.docx>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	doc, err := parse.Parse(os.Args[1], data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		os.Exit(1)
	}
	fmt.Printf("format=%s nodes=%d\n", doc.Format, len(doc.Nodes))
	for _, n := range doc.Nodes {
		extra := ""
		if n.SourceStyle != "" {
			extra += " style=" + n.SourceStyle
		}
		if n.Level > 0 {
			extra += fmt.Sprintf(" level=%d", n.Level)
		}
		if n.SourcePage > 0 {
			extra += fmt.Sprintf(" page=%d", n.SourcePage)
		}
		if n.Image != nil {
			extra += fmt.Sprintf(" image=%s(%s,%dB)", n.Image.Name, n.Image.MimeType, len(n.Image.Data))
		}
		if len(n.Rows) > 0 {
			extra += fmt.Sprintf(" rows=%q", n.Rows)
		}
		fmt.Printf("%s %-11s text=%q%s\n", n.ID, n.Kind, n.Text, extra)
	}
}
