// Command stublm is a fake OpenAI-compatible endpoint that answers with a
// scripted plan, so the full LLM path through the real govdoc binary can be
// exercised without a network or an API key.
//
// # Why it exists
//
// Everything else in the test suite stops at a boundary: the llm package tests
// use httptest in-process, and the httpe2e tool skips analysis when no model is
// configured. Neither proves that a real govdoc.exe — with its rosetta client,
// its SSE job and its assembler — completes the round trip a user performs.
//
// The first answer deliberately drops a paragraph, so the self-repair loop has
// something to fix. Watching the tool recover against a real HTTP peer is the
// point; an in-process stub cannot show that.
//
// # How it decides
//
// It parses the node list out of the prompt and classifies each node by its
// kind and text — the same evidence a real model would use. An earlier version
// guessed from node *position*, which silently modelled the document wrongly
// (binding the 缘由 to 主送机关 and inventing a chapter) and made the
// verification meaningless, because the resulting document still looked
// plausible. A stub must not guess positionally for exactly that reason.
//
// Usage:
//
//	go run ./tools/stublm -addr 127.0.0.1:8899
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8899", "监听地址")
	flag.Parse()

	var calls int64

	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := atomic.AddInt64(&calls, 1)

		nodes := parseNodeList(string(body))
		log.Printf("第 %d 次请求：解析到 %d 个节点", n, len(nodes))

		// The first answer omits the last body paragraph, which VerifyPlan
		// reports as uncovered content; the client must repair it.
		content := buildPlan(nodes, n == 1)

		resp := map[string]any{
			"id":    fmt.Sprintf("stub-%d", n),
			"model": "stub-model",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 200, "total_tokens": 300},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"stub-model","object":"model"}]}`))
	})

	log.Printf("桩模型已启动: http://%s/v1（首个回答故意漏收一段，用于验证自修复）", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// node is one row of the prompt's paragraph list.
type node struct {
	ID   string
	Kind string
	Text string
}

// listLineRe matches the prompt's "n0001 | 正文 | 文本" rows.
//
// It is anchored on "^" and requires the four-digit id, which is what keeps it
// from also matching the example ids inside the system prompt's JSON schema —
// those appear as `"node_id": "n0001"` mid-line. Picking those up was the seed
// of the positional bug this file now avoids.
var listLineRe = regexp.MustCompile(`(?m)^(n\d{4})\s*\|\s*([^|]*?)\s*\|\s*(.*)$`)

// parseNodeList extracts the paragraph list from the request body.
func parseNodeList(body string) []node {
	// The body is JSON, so the prompt's newlines arrive escaped.
	s := strings.ReplaceAll(body, `\n`, "\n")
	s = strings.ReplaceAll(s, `\"`, `"`)
	// Narrow to the list: the prompt delimits it with box-drawing rules.
	if i := strings.Index(s, "段落清单"); i >= 0 {
		s = s[i:]
	}
	if i := strings.Index(s, "请把这"); i >= 0 {
		s = s[:i]
	}

	var out []node
	seen := map[string]bool{}
	for _, m := range listLineRe.FindAllStringSubmatch(s, -1) {
		id, kind, text := m[1], strings.TrimSpace(m[2]), strings.TrimSpace(m[3])
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, node{ID: id, Kind: kind, Text: text})
	}
	return out
}

// buildPlan emits plan JSON. dropLast omits one body paragraph so the repair
// loop has a defect to correct.
func buildPlan(nodes []node, dropLast bool) string {
	type section struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Level       int      `json:"level"`
		HeadingID   string   `json:"heading_node_id,omitempty"`
		BodyNodeIDs []string `json:"body_node_ids,omitempty"`
	}
	type element struct {
		Role   string `json:"role"`
		NodeID string `json:"node_id"`
	}
	plan := struct {
		Meta     map[string]string `json:"meta"`
		Elements []element         `json:"elements"`
		Preamble []string          `json:"preamble_node_ids"`
		Sections []section         `json:"sections"`
		Notes    []string          `json:"notes"`
	}{
		Meta: map[string]string{
			"issuing_org": "某某市人民政府办公室文件",
			"doc_number":  "某政办发〔2024〕15号",
		},
		Notes: []string{"桩模型生成，用于端到端验证。"},
	}

	// Pass 1: recognise 公文 element slots from the text itself.
	//   - the short line before the 主送机关 is the 标题
	//   - a line ending in "：" is the 主送机关
	//   - the date-shaped line is the 成文日期
	//   - the short line before it is the 发文机关署名
	dateIdx, recipientIdx := -1, -1
	for i, n := range nodes {
		switch {
		case dateRe.MatchString(n.Text):
			dateIdx = i
		case recipientIdx < 0 && (strings.HasSuffix(n.Text, "：") || strings.HasSuffix(n.Text, ":")):
			recipientIdx = i
		}
	}
	titleIdx := -1
	if recipientIdx > 0 {
		titleIdx = recipientIdx - 1
	}
	signatureIdx := -1
	if dateIdx > 0 {
		signatureIdx = dateIdx - 1
	}

	claimed := map[int]bool{}
	add := func(role string, i int) {
		if i < 0 || i >= len(nodes) || claimed[i] {
			return
		}
		claimed[i] = true
		plan.Elements = append(plan.Elements, element{role, nodes[i].ID})
	}
	add("title", titleIdx)
	add("recipient", recipientIdx)
	add("signature", signatureIdx)
	add("date", dateIdx)

	// Pass 2: the 缘由 goes in the preamble; headings open sections; the rest
	// is body under the preceding heading. Driven by kind and text only.
	var current *section
	for i, n := range nodes {
		if claimed[i] || strings.Contains(n.Kind, "分页") {
			continue
		}
		if strings.Contains(n.Kind, "标题") || headingRe.MatchString(n.Text) {
			plan.Sections = append(plan.Sections, section{
				ID:        fmt.Sprintf("s%d", len(plan.Sections)+1),
				Title:     n.Text,
				Level:     headingLevel(n.Text),
				HeadingID: n.ID,
			})
			current = &plan.Sections[len(plan.Sections)-1]
			continue
		}
		if current == nil {
			plan.Preamble = append(plan.Preamble, n.ID)
			continue
		}
		current.BodyNodeIDs = append(current.BodyNodeIDs, n.ID)
	}

	// A document with no recognisable heading would otherwise put everything in
	// the preamble, which is not a useful plan; give it one section so the stub
	// still exercises the pipeline.
	if len(plan.Sections) == 0 && len(plan.Preamble) > 0 {
		plan.Sections = append(plan.Sections, section{
			ID: "s1", Title: "正文", Level: 1, BodyNodeIDs: plan.Preamble,
		})
		plan.Preamble = nil
	}

	if dropLast {
		// Drop the last body paragraph of the last section that has more than
		// one, so a paragraph is genuinely uncovered.
		for i := len(plan.Sections) - 1; i >= 0; i-- {
			if b := plan.Sections[i].BodyNodeIDs; len(b) > 1 {
				plan.Sections[i].BodyNodeIDs = b[:len(b)-1]
				break
			}
		}
	}
	return mustJSON(plan)
}

var (
	// dateRe matches the shapes a 成文日期 takes.
	dateRe = regexp.MustCompile(`^\s*\d{4}\s*年\s*\d{1,2}\s*月\s*\d{1,2}\s*日\s*$`)
	// headingRe matches a 层次序数 at the start of a line.
	headingRe = regexp.MustCompile(`^\s*(（[一二三四五六七八九十]+）|\([一二三四五六七八九十]+\)|[一二三四五六七八九十]+、|\d+[.、]|（\d+）|\(\d+\))`)
)

// headingLevel derives the 层次序数 depth from a heading's own numbering form.
func headingLevel(text string) int {
	t := strings.TrimLeft(text, " \t\u3000")
	if t == "" {
		return 1
	}
	r := []rune(t)
	switch {
	case r[0] == '（' || r[0] == '(':
		inner := []rune(strings.TrimLeft(t, "（("))
		if len(inner) > 0 && inner[0] >= '0' && inner[0] <= '9' {
			return 4
		}
		return 2
	case r[0] >= '0' && r[0] <= '9':
		return 3
	default:
		return 1
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"sections":[]}`
	}
	return string(b)
}
