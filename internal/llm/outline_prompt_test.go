package llm

import (
	"strings"
	"testing"

	"govdoc/internal/gbstandard"
	"govdoc/internal/style"
)

// The prompt is the only place the model learns the document type's rules and
// its rewrite budget. If either is hard-coded, editing a profile changes the
// typesetting but not how the document is organised — the feature would look
// present in the UI and have no effect on the output.
func TestPromptUsesConfiguredOutlineAndBudget(t *testing.T) {
	outline := gbstandard.DefaultOutline()
	outline.MaxDepth = 3
	outline.HeadingForms = []gbstandard.HeadingForm{
		{Numeral: "arabic", Prefix: "第", Suffix: "章"},
		{Numeral: "arabic", Suffix: "."},
		{Numeral: "arabic", Prefix: "(", Suffix: ")"},
	}
	outline.Tone = "平实、克制，不使用口号式表达"
	outline.Guidance = "结尾必须写明联系人"

	limits := style.Limits{Enabled: true, FormalMax: 9, MixedMax: 22, ColloquialMax: 77}
	p := systemPromptWith(testDoc(), PlanOptions{
		AllowEdits: true, Outline: &outline, Limits: limits,
	})

	// The custom numbering must reach both the recognition table and the
	// level table. A hard-coded 一、/（一）sequence would fail here.
	if !strings.Contains(p, "第1章 第2章") {
		t.Errorf("提示词未使用配置的第一级编号:\n%s", p)
	}
	if strings.Contains(p, "一、 二、 三、") {
		t.Error("提示词仍在使用写死的中文序号")
	}
	for _, must := range []string{
		"平实、克制，不使用口号式表达",
		"结尾必须写明联系人",
		"标题最多 3 级",
		"最多改动 9%",
		"最多改动 22%",
		"最多改动 77%",
	} {
		if !strings.Contains(p, must) {
			t.Errorf("提示词缺少配置项 %q", must)
		}
	}
}

// A document with headings is not "unstructured", but its prose may still be
// spoken. The prompt must allow rewriting those paragraphs; the budget, not a
// document-level veto, is what keeps the change small.
func TestPromptAllowsPerParagraphRewriteInStructuredDocument(t *testing.T) {
	p := systemPromptWith(testDoc(), PlanOptions{AllowEdits: true})
	if !strings.Contains(p, "判断对象是段落，不是整篇") {
		t.Error("提示词仍把重组限制为整篇无结构文档")
	}
	if !strings.Contains(p, "有标题的文档也可以只") {
		t.Error("提示词未说明有标题的文档也可重组个别段落")
	}
}
