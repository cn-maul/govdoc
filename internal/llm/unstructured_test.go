package llm

import (
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

func longDoc(text string) *docmodel.Document {
	return &docmodel.Document{Nodes: []docmodel.Node{{
		ID: "n0001", Kind: docmodel.KindParagraph, Text: text,
	}}}
}

// TestNeedsRecomposeOnlyForRunOnDocuments pins the narrow trigger: one long
// paragraph and no structural evidence. A normal multi-paragraph upload must
// not be rewritten just because the model could phrase it better.
func TestNeedsRecomposeOnlyForRunOnDocuments(t *testing.T) {
	runOn := longDoc(strings.Repeat("为做好本年度工作，现就有关事项通知如下，", 20))
	if !needsRecompose(runOn, true) {
		t.Error("无结构长段落应触发重组")
	}
	if needsRecompose(runOn, false) {
		t.Error("关闭原文修改时不得触发重组")
	}

	multi := &docmodel.Document{Nodes: []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: "第一段。"},
		{ID: "n0002", Kind: docmodel.KindParagraph, Text: "第二段。"},
	}}
	if needsRecompose(multi, true) {
		t.Error("普通多段文档不应触发重组")
	}

	numbered := &docmodel.Document{
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindHeading, Text: "一、总体要求", Level: 1},
			{ID: "n0002", Kind: docmodel.KindParagraph,
				Text: strings.Repeat("这里是很长的一段正文。", 20)},
		},
	}
	if needsRecompose(numbered, true) {
		t.Error("已有层次序数的文档不应触发重组")
	}
}

// TestRunOnDirectiveNamesTargets: a generic "you may recompose" rule was not
// enough in practice. The directive must name the exact node and state that a
// single untouched section is not an acceptable answer.
func TestRunOnDirectiveNamesTargets(t *testing.T) {
	doc := longDoc(strings.Repeat("这是一段较长的正文。", 40))
	var b strings.Builder
	writeRunOnDirective(&b, doc)
	out := b.String()
	if !strings.Contains(out, "n0001") {
		t.Errorf("指令未指明目标节点: %s", out)
	}
	if !strings.Contains(out, "不得只输出一个章节") {
		t.Errorf("指令未明确拒绝原样收录: %s", out)
	}
}
