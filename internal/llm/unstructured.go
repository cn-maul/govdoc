package llm

import (
	"fmt"
	"strings"

	"govdoc/internal/docmodel"
	"govdoc/internal/parse"
)

// runOnMinRunes is the length at which one paragraph stops being a normal
// opening paragraph and becomes a run-on document that needs real
// paragraph-level reconstruction.
const runOnMinRunes = 180

// needsRecompose reports whether the source is the specific shape this feature
// exists for: no heading evidence anywhere, and one long paragraph that carries
// most of the document.
//
// The check is deliberately narrow. A document with headings, numbering, or
// several ordinary paragraphs must not be rewritten merely because the model
// thinks the prose could flow better; those documents only need grouping. The
// rule here is about a single paragraph containing what should have been
// several paragraphs, which is precisely when recompose is the right tool.
func needsRecompose(doc *docmodel.Document, allowEdits bool) bool {
	if !allowEdits || doc == nil {
		return false
	}
	content := doc.ContentNodes()
	if len(content) == 0 {
		return false
	}

	// Any heading anywhere is structural evidence. A Word heading style on the
	// title, or a numbered chapter, means the author already expressed the
	// hierarchy; the document then needs grouping, not recomposition.
	for _, node := range content {
		if node.Kind == docmodel.KindHeading || parse.NumberingLevel(node.Text) > 0 {
			return false
		}
	}

	total, longest, prose := 0, 0, 0
	for _, node := range content {
		if node.Kind == docmodel.KindTable || node.Kind == docmodel.KindImage ||
			node.Kind == docmodel.KindPageBreak {
			continue
		}
		n := len([]rune(node.Text))
		total += n
		prose++
		if n > longest {
			longest = n
		}
	}
	if prose == 0 || longest < runOnMinRunes {
		return false
	}
	// A long paragraph inside a large, otherwise normal document is not the
	// one-block upload this rule targets. Requiring the long paragraph to hold
	// most of the prose keeps ordinary reports out of the rewrite path.
	return longest*10 >= total*6
}

// recomposeTargets lists the paragraphs the model must split. A title and a
// recipient line are short and therefore never reach the threshold; the body
// paragraph is the only target in the normal one-block document.
func recomposeTargets(doc *docmodel.Document) []docmodel.Node {
	if doc == nil {
		return nil
	}
	var out []docmodel.Node
	for _, node := range doc.ContentNodes() {
		if node.Kind == docmodel.KindHeading || node.Kind == docmodel.KindTable ||
			node.Kind == docmodel.KindImage || node.Kind == docmodel.KindPageBreak {
			continue
		}
		if len([]rune(node.Text)) >= runOnMinRunes {
			out = append(out, node)
		}
	}
	return out
}

// writeRunOnDirective puts a document-specific instruction in front of the
// model. The generic rules explain when recompose is allowed; this block says
// that for *this* document it is required, and names the exact node. Generic
// wording was not enough in practice: a real one-paragraph upload came back
// as a single section with the whole paragraph untouched.
func writeRunOnDirective(b *strings.Builder, doc *docmodel.Document) {
	targets := recomposeTargets(doc)
	if len(targets) == 0 {
		return
	}
	b.WriteString("\n【系统判定：本文档必须重建段落层次】\n")
	b.WriteString("本文档没有标题样式，也没有层次序数，且以下段落是一段到底或长句堆叠：\n")
	for _, node := range targets {
		fmt.Fprintf(b, "  %s（%d 字）| %s\n",
			node.ID, len([]rune(node.Text)), firstLine(node.Text, 72))
	}
	b.WriteString(`必须执行：
- 对上述每个节点使用 recompose，把它拆成 2～8 个语义完整、逻辑通顺的段落；
  优先保留原句和原意，只做必要的断句、语序调整、重复删减和主语补足，
  不得概括，不得增加原文没有的事实、数字、日期、机关、依据、承诺或要求。
- 新建 2～6 个章节（可按内容设置二级章节），标题简洁、符合公文风格；
  标题不得直接抄正文句子。
- 首句若是“各……：”这类主送机关，必须单独拆成一段并令 roles 对应项为
  "recipient"；紧跟其后的缘由段（“为……，现通知如下。”）若不属于任何章节，
  令 roles 对应项为 "preamble"。这两段必须在 parts 最前面，且**不要**再放进
  body_parts。
- 除 recipient/preamble 外，每个重组分片都必须通过 body_parts 恰好放置一次；
  不得把被重组节点整体放进 body_node_ids。
- 原文已有标题或主送机关时，先绑定为 elements 的 title / recipient，
  不得把它们写进重组段落，也不得另造一份标题。
- 不得只输出一个章节后把整段原样放进去；那等于没有完成本次任务。
`)
}

// recomposeRepairPrompt is used when the model returned a valid plan but
// ignored the required recomposition. It repeats the concrete targets instead
// of the generic rules, because the failure is not a misunderstanding of the
// schema — it is skipping the one operation the document needs.
func recomposeRepairPrompt(doc *docmodel.Document) string {
	var b strings.Builder
	b.WriteString("你上一版的方案虽然能解析，但仍然没有重建段落层次。\n")
	b.WriteString("本文档已被系统判定为无标题、无层次序数的长段落文档，不能只输出一个章节后原样收录。\n")
	writeRunOnDirective(&b, doc)
	b.WriteString("\n请重新输出完整的 JSON 对象，不要解释。\n")
	return b.String()
}
