package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cn-maul/rosetta"

	"govdoc/internal/docmodel"
)

// RevisionRequest is one chat turn from the review workbench.
type RevisionRequest struct {
	Instruction string
	Selection   string
	NodeID      string
}

// RevisionResult is a model answer: declared edits plus a short reply.
type RevisionResult struct {
	Edits []docmodel.Edit
	Reply string
}

// Revise asks the model to turn a user instruction into declared edits.
//
// It is intentionally not a general chat endpoint. The answer has to be a list
// of edits that can be shown one by one and accepted independently; free-form
// prose would be unverifiable, and a rewritten document would bypass the
// before/after audit the rest of the program depends on.
func (c *Client) Revise(ctx context.Context, doc *docmodel.Document, req RevisionRequest) (RevisionResult, error) {
	if c == nil || c.client == nil {
		return RevisionResult{}, ErrNotConfigured
	}
	if doc == nil {
		return RevisionResult{}, errors.New("llm: 缺少文档")
	}
	if strings.TrimSpace(req.Instruction) == "" {
		return RevisionResult{}, errors.New("llm: 修改要求为空")
	}

	user := revisionPrompt(doc, req)
	chatReq := &rosetta.ChatRequest{
		Model:           c.cfg.Model,
		System:          revisionSystemPrompt,
		Messages:        []rosetta.Message{rosetta.User(user)},
		MaxOutputTokens: 4096,
		Temperature:     c.temperature(),
		Seed:            c.seed(),
	}
	if !c.noJSONMode.Load() {
		chatReq.ResponseFormat = revisionResponseFormat()
	}

	resp, err := c.client.Chat(ctx, chatReq)
	if err != nil {
		if chatReq.ResponseFormat != nil && rejectsResponseFormat(err) && !c.noJSONMode.Swap(true) {
			chatReq.ResponseFormat = nil
			resp, err = c.client.Chat(ctx, chatReq)
		}
		if err != nil {
			return RevisionResult{}, classify(err)
		}
	}
	if resp.StopReason == rosetta.StopLength {
		return RevisionResult{}, ErrTruncated
	}
	out, err := parseRevision(resp.Text())
	if err != nil {
		return RevisionResult{}, err
	}
	// A whole-line rewrite of a heading is a 标题 change even when the model
	// calls it `wording`, and it must bypass the prose normalizer: that
	// reduces an edit to its minimal changed span and drops pure whitespace
	// changes, which would silently discard "一、总体 要求" → "一、总体要求".
	// The chat panel shows one card per edit, so the rest is normalized to keep
	// a repeated request over the same sentence stable.
	out.Edits = normalizeRevisionEdits(doc, out.Edits)
	return out, nil
}

// normalizeRevisionEdits classifies heading rewrites, then normalizes prose
// edits as usual.
func normalizeRevisionEdits(doc *docmodel.Document, edits []docmodel.Edit) []docmodel.Edit {
	var headings, rest []docmodel.Edit
	for _, e := range edits {
		e = asHeadingEdit(doc, e)
		if e.Kind == docmodel.EditHeading {
			if e.NodeID == "" || e.Before == "" || e.After == "" {
				continue
			}
			if n, ok := doc.Node(e.NodeID); ok && strings.Contains(n.Text, e.Before) {
				headings = append(headings, e)
			}
			continue
		}
		rest = append(rest, e)
	}

	out := NormalizeEdits(doc, rest)
	// Restore source order across both groups. The chat panel lists these in
	// the document's own reading order, and NormalizeEdits already sorted the
	// prose subset.
	all := append(out, headings...)
	sort.SliceStable(all, func(i, j int) bool {
		if editOrder(doc, all[i].NodeID) != editOrder(doc, all[j].NodeID) {
			return editOrder(doc, all[i].NodeID) < editOrder(doc, all[j].NodeID)
		}
		return strings.Index(docTextOf(doc, all[i].NodeID), all[i].Before) <
			strings.Index(docTextOf(doc, all[j].NodeID), all[j].Before)
	})
	return all
}

// docTextOf returns a node's source text, or "" when it is unknown.
func docTextOf(doc *docmodel.Document, nodeID string) string {
	if doc == nil {
		return ""
	}
	if n, ok := doc.Node(nodeID); ok {
		return n.Text
	}
	return ""
}

// editOrder returns a node's source position, or a large value for an unknown
// id so it sorts last instead of jumping to the front.
func editOrder(doc *docmodel.Document, nodeID string) int {
	if doc == nil {
		return 1 << 30
	}
	if n, ok := doc.Node(nodeID); ok {
		return n.Order
	}
	return 1 << 30
}

// revisionSystemPrompt states the one rule that makes chat editing auditable.
const revisionSystemPrompt = `你是一名中国政府公文编辑。用户会给出修改要求，或选中一段文字再提出要求。

你只能提出**可逐条审阅的文字替换**，不能直接输出一篇改好的文章：
1. 每一条改动必须写进 edits，并给出 node_id、kind、before、after、reason。
2. before 必须是该段原文中**逐字出现**的片段（含标点），否则这条改动不会生效。
3. 只改用户要求的地方；不要顺手润色，不要改变原意，不要调整语序。
4. 如果用户选中了一段文字，只针对这段文字提出改动，并保持 node_id 不变。
5. 如果用户的说法不需要改文字（例如只是询问），返回空 edits，并在 reply 里简短说明。
6. 不要编造原文中不存在的 node_id，也不要提出删除整段或新增段落。

kind 只能用：typo、grammar、wording、punctuation、sentence_split、other。

只输出一个 JSON 对象，不要 Markdown 围栏：
{"reply":"一句话说明","edits":[{"node_id":"n0002","kind":"wording","before":"原文片段","after":"改后片段","reason":"为什么改","occurrence":0}]}
`

// revisionPrompt gives the model enough context to target the right node.
func revisionPrompt(doc *docmodel.Document, req RevisionRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "文档名称：%s\n", doc.SourceName)
	fmt.Fprintf(&b, "用户要求：%s\n", strings.TrimSpace(req.Instruction))
	if strings.TrimSpace(req.Selection) != "" {
		fmt.Fprintf(&b, "\n用户选中的文字：\n%s\n", req.Selection)
	}
	if req.NodeID != "" {
		fmt.Fprintf(&b, "\n选中文字所在段落：%s\n", req.NodeID)
	}

	b.WriteString("\n可用段落（编号 | 内容）：\n")
	for _, n := range doc.ContentNodes() {
		// A chat turn rarely needs every paragraph of a long document, but the
		// node list is what makes node_id answerable. Tables and images are
		// listed without their payload.
		text := strings.ReplaceAll(n.Text, "\n", " ")
		if r := []rune(text); len(r) > 160 {
			text = string(r[:160]) + "…"
		}
		fmt.Fprintf(&b, "%s | %s\n", n.ID, text)
	}
	return b.String()
}

func parseRevision(text string) (RevisionResult, error) {
	raw, err := extractJSONObject(text)
	if err != nil {
		return RevisionResult{}, err
	}
	var out struct {
		Reply string          `json:"reply"`
		Edits []docmodel.Edit `json:"edits"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return RevisionResult{}, fmt.Errorf("无法解析模型返回的 JSON：%v", err)
	}
	if out.Edits == nil {
		out.Edits = []docmodel.Edit{}
	}
	return RevisionResult{Edits: out.Edits, Reply: strings.TrimSpace(out.Reply)}, nil
}

// revisionResponseFormat constrains the answer to the reviewable shape.
func revisionResponseFormat() *rosetta.ResponseFormat {
	strict := true
	return &rosetta.ResponseFormat{
		Type:   "json_schema",
		Name:   "govdoc_revision",
		Schema: revisionSchema,
		Strict: &strict,
	}
}

var revisionSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["reply", "edits"],
  "properties": {
    "reply": {"type": "string"},
    "edits": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["node_id", "kind", "before", "after", "reason", "occurrence"],
        "properties": {
          "node_id": {"type": "string", "pattern": "^n[0-9]{4,}$"},
          "kind": {
            "type": "string",
            "enum": ["typo", "grammar", "wording", "punctuation", "sentence_split", "other"]
          },
          "before": {"type": "string"},
          "after": {"type": "string"},
          "reason": {"type": ["string", "null"]},
          "occurrence": {"type": ["integer", "null"], "minimum": 0}
        }
      }
    }
  }
}`)
