package llm

import (
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// TestParsePartialPlanKeepsOnlyCompleteObjects: a snapshot must never show a
// half-written edit or section. The final, still-open object is omitted until
// its closing brace arrives.
func TestParsePartialPlanKeepsOnlyCompleteObjects(t *testing.T) {
	doc := &docmodel.Document{Nodes: []docmodel.Node{
		{ID: "n0001", Kind: docmodel.KindParagraph, Text: "第一段"},
		{ID: "n0002", Kind: docmodel.KindParagraph, Text: "第二段"},
	}}
	text := `{"meta":{},
	  "edits":[{"node_id":"n0001","kind":"punctuation","before":"，","after":"。"}],
	  "sections":[
	    {"id":"s1","title":"一、总体要求","level":1,"body_node_ids":["n0002"]},
	    {"id":"s2","title":"二、未完`

	plan, edits := ParsePartialPlan(text, doc)
	if plan == nil {
		t.Fatal("应解析出已完成的部分")
	}
	if len(plan.Sections) != 1 {
		t.Fatalf("章节数 = %d, 期望 1", len(plan.Sections))
	}
	if plan.Sections[0].Title != "一、总体要求" {
		t.Errorf("章节标题 = %q", plan.Sections[0].Title)
	}
	if len(edits) != 1 || edits[0].NodeID != "n0001" {
		t.Errorf("校对项 = %+v", edits)
	}
}

// TestParsePartialPlanIgnoresBracesInStrings: a title can legally contain a
// brace; it must not confuse the object scanner.
func TestParsePartialPlanIgnoresBracesInStrings(t *testing.T) {
	text := `{"sections":[{"id":"s1","title":"标题 {含括号}","level":1,"body_node_ids":[]}]}`
	plan, _ := ParsePartialPlan(text, nil)
	if plan == nil || len(plan.Sections) != 1 {
		t.Fatalf("应解析出一个章节: %+v", plan)
	}
	if plan.Sections[0].Title != "标题 {含括号}" {
		t.Errorf("标题被破坏: %q", plan.Sections[0].Title)
	}
}

// TestParsePartialPlanEmptyBeforeArrays: progress text may arrive before the
// answer's first useful array; that is not an error, just nothing to show yet.
func TestParsePartialPlanEmptyBeforeArrays(t *testing.T) {
	if plan, edits := ParsePartialPlan(`{"meta":`, nil); plan != nil || edits != nil {
		t.Fatalf("尚无完整对象时不应产出快照: %+v %+v", plan, edits)
	}
}

// TestParsePartialPlanKeepsCompleteRecompose: a recomposition is useful to the
// reviewer as soon as its own object closes, even while later sections are
// still being generated.
func TestParsePartialPlanKeepsCompleteRecompose(t *testing.T) {
	text := `{"recompose":[
	  {"node_id":"n0001","parts":["第一段。","第二段。"],"reason":"一段到底，拆为两段"},
	  {"node_id":"n0002","parts":["尚未写完`

	plan, _ := ParsePartialPlan(text, nil)
	if plan == nil || len(plan.Recompose) != 1 {
		t.Fatalf("应保留一条完整重组: %+v", plan)
	}
	rec := plan.Recompose[0]
	if rec.NodeID != "n0001" || len(rec.Parts) != 2 {
		t.Errorf("重组声明 = %+v", rec)
	}
}

// TestParsePartialPlanKeepsNestedProgress: a completed child inside an
// unfinished parent is useful progress. Waiting for the parent's closing
// brace would hide most of the outline when the model nests every chapter.
func TestParsePartialPlanKeepsNestedProgress(t *testing.T) {
	text := `{"sections":[
	  {"id":"s1","title":"一、总体要求","level":1,"children":[
	    {"id":"s1a","title":"（一）指导思想","level":2,"children":[]},
	    {"id":"s1b","title":"（二）基本目标","level":2,"children":[]`

	plan, _ := ParsePartialPlan(text, nil)
	if plan == nil || len(plan.Sections) != 1 {
		t.Fatalf("应保留尚未闭合的父章节: %+v", plan)
	}
	root := plan.Sections[0]
	if root.Title != "一、总体要求" || root.ID != "s1" {
		t.Errorf("父章节字段 = %+v", root)
	}
	if len(root.Children) != 2 {
		t.Fatalf("子章节数 = %d, 期望 2", len(root.Children))
	}
	if root.Children[0].Title != "（一）指导思想" {
		t.Errorf("子章节 = %+v", root.Children[0])
	}
	// The second child's object is still open, but its title has already
	// arrived complete, so it is useful to show now.
	if root.Children[1].Title != "（二）基本目标" {
		t.Errorf("未闭合子章节的完整标题 = %+v", root.Children[1])
	}
}

// TestPartialEmitterSkipsUnchangedSnapshots: the emitter ticks on a fixed
// interval while the model is still writing inside one object. Those ticks
// produce the same complete-object view, and re-sending it would make the
// review list re-render for no visible change.
func TestPartialEmitterSkipsUnchangedSnapshots(t *testing.T) {
	var got []PartialPlan
	p := &partialEmitter{
		publish: func(snap PartialPlan) { got = append(got, snap) },
	}

	p.reset()
	p.chunk(`{"edits":[{"node_id":"n0001","kind":"typo","before":"甲","after":"乙"}],"sections":`)
	p.flush()
	first := len(got)
	if first != 2 {
		t.Fatalf("首次快照数 = %d, 期望 reset + 1 条快照", first)
	}

	// More text inside the same unfinished section must not produce a
	// duplicate snapshot.
	p.chunk(strings.Repeat("x", 20))
	p.flush()
	if len(got) != first {
		t.Fatalf("未变化时又推送了快照: %d -> %d", first, len(got))
	}

	// A new attempt starts over, so the same content may be emitted again.
	p.reset()
	p.chunk(`{"edits":[{"node_id":"n0001","kind":"typo","before":"甲","after":"乙"}]}`)
	p.flush()
	if len(got) != first+2 {
		t.Fatalf("reset 后应重新推送, 实际 %d", len(got))
	}
}
