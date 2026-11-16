// Package baseline derives a structure plan from the source document alone,
// without a model.
//
// # Why a deterministic plan is worth having
//
// The product's promise is that the tool never changes the text silently. The
// stronger version of that promise — every change is visible and auditable —
// currently holds only for *characters*: Plan.Edits carries a before/after for
// every correction. Structure has no equivalent. When the model moves a
// paragraph from chapter three to chapter one, or swaps two whole chapters,
// VerifyPlan counts that the node was claimed exactly once and says nothing
// about where it came from. The user can only find out by re-reading the
// upload against the output.
//
// The source usually *does* say where each paragraph belongs: a 公文 numbers
// its chapters, and the parser already recognises those numbers (KindHeading
// plus the 层次序数 patterns). Cutting the document at its own headings yields
// a plan that is correct by construction, because it is read off the document.
// Comparing a model's plan against that baseline turns every structural change
// into a diffable fact, the same way AppliedText turns every textual change
// into one.
//
// # What it is not
//
// This is a floor, not a competitor to the model. It cannot tell that a
// paragraph belongs under a different chapter than the one it physically
// follows, cannot recognise a missing 标题, and does not understand that two
// adjacent numbered lists are really one section. It renders a usable 公文
// from a well-structured upload, and it is the yardstick for judging a model's
// restructuring. When the source has no headings at all it produces a single
// section, which is exactly as much structure as the document actually has.
package baseline

import (
	"fmt"
	"strings"

	"govdoc/internal/docmodel"
	"govdoc/internal/parse"
)

// Build derives a plan from the document's own structure.
//
// The rules, in order:
//
//  1. Cover exactly the content the source has — never invent a node, never
//     drop one. VerifyPlan must pass untouched.
//  2. If the document opens with a 标题 line and has no styled heading at all,
//     bind that line to the title slot, so it is not swept into the first
//     chapter and typeset as body text.
//  3. Every heading paragraph opens a section at its own level. Everything
//     after it belongs to the innermost open section. Content before the first
//     heading is 前言: body prose that belongs to no 章节.
//  4. A paragraph that is not a heading but begins with a 层次序数 is treated
//     as a heading too. This is the most common shape of a real-world upload:
//     the author typed "一、总体要求" as a plain paragraph.
//  5. Nesting comes from the level sequence. A document that skips a level
//     (一、 straight to （1）) is placed one step deeper, because a tree cannot
//     express a gap without fabricating a level.
//
// The result is returned even when it is imperfect (a document with no
// headings yields one unnumbered section), because the caller's job is to
// judge it, not to gate on it.
func Build(doc *docmodel.Document) *docmodel.Plan {
	plan := &docmodel.Plan{
		Elements:        []docmodel.ElementBinding{},
		PreambleNodeIDs: []string{},
		Sections:        []docmodel.Section{},
		Edits:           []docmodel.Edit{},
		Recompose:       []docmodel.Recompose{},
		Notes:           []string{},
	}
	if doc == nil {
		return plan
	}

	claimed := make(map[string]bool, len(doc.Nodes))
	claim := func(id string) bool {
		if id == "" || claimed[id] {
			return false
		}
		claimed[id] = true
		return true
	}

	bindTitle(doc, plan, claim)

	// cutLevel marks the nodes the document itself presents as headings.
	cutLevel := make(map[string]int)
	for _, n := range doc.Nodes {
		if !n.Kind.CarriesProse() || claimed[n.ID] {
			continue
		}
		if n.Kind == docmodel.KindHeading {
			cutLevel[n.ID] = clampLevel(n.Level)
			continue
		}
		if lvl := parse.NumberingLevel(n.Text); lvl > 0 {
			cutLevel[n.ID] = clampLevel(lvl)
		}
	}
	if len(cutLevel) == 0 {
		return buildFlat(doc, plan, claim)
	}

	// Group the source into sections in one pass. Each node lands in the
	// section that is open at that point, so a section's body is a contiguous
	// run of the source — which is what makes "where does this paragraph sit
	// in the baseline" a well-defined question for the structural diff.
	var (
		roots []*node
		stack []*node // open ancestors, deepest last
	)
	for _, n := range doc.Nodes {
		if !n.Kind.CarriesProse() || claimed[n.ID] {
			continue
		}
		lvl, isCut := cutLevel[n.ID]
		if !isCut {
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.s.BodyNodeIDs = append(top.s.BodyNodeIDs, n.ID)
			} else {
				plan.PreambleNodeIDs = append(plan.PreambleNodeIDs, n.ID)
			}
			claimed[n.ID] = true
			continue
		}

		// Close every section at this level or deeper: a second "（一）"
		// follows the first rather than nesting inside it.
		//
		// The comparison uses the level the *source* declared, not the one the
		// tree ended up storing. A skipped level is pulled up so the tree is
		// expressible (一、 straight to 1. stores 1. as level 2); the next
		// heading at the same source level must then close it rather than
		// nest under it. Using the stored level here turned "1./2." siblings
		// into a parent/child pair, which in turn made the structural diff
		// report a relocation the author never made.
		for len(stack) > 0 && stack[len(stack)-1].srcLevel >= lvl {
			stack = stack[:len(stack)-1]
		}
		// A heading nested under the current stack must be exactly one
		// level deeper. Skipping from 一、 to （1） cannot be expressed as a
		// tree without inventing the missing level, so the deeper section is
		// pulled up to parent+1 — which is what the printed 层次序数 will
		// show anyway, since the assembler derives the number from depth.
		if len(stack) > 0 {
			if want := stack[len(stack)-1].s.Level + 1; lvl > want {
				lvl = want
			}
		}

		// The id is assigned in materialize(), in tree order, so it is unique
		// by construction rather than by luck of append order.
		n2 := &node{s: &docmodel.Section{
			Title:         n.Text,
			Level:         lvl,
			HeadingNodeID: n.ID,
			BodyNodeIDs:   []string{},
		}, srcLevel: cutLevel[n.ID]}
		if len(stack) > 0 {
			parent := stack[len(stack)-1]
			parent.kids = append(parent.kids, n2)
		} else {
			roots = append(roots, n2)
		}
		stack = append(stack, n2)
		claimed[n.ID] = true
	}

	next := 1
	plan.Sections = materialize(roots, &next)
	return plan
}

// node is one section under construction, with its children kept as pointers
// so a subtree can be completed before it is copied out.
type node struct {
	s        *docmodel.Section
	srcLevel int
	kids     []*node
}

// materialize converts the pointer tree into the value tree the plan carries,
// numbering sections in preorder and making every slice non-nil.
//
// Children are converted before the parent's copy is appended, so the copy
// holds complete subtrees rather than sections still under construction. The
// counter is shared across the whole tree so the ids are unique by
// construction — the property VerifyPlan now enforces.
func materialize(roots []*node, next *int) []docmodel.Section {
	out := make([]docmodel.Section, 0, len(roots))
	for _, n := range roots {
		s := *n.s
		s.ID = fmt.Sprintf("b%d", *next)
		*next++
		if s.BodyNodeIDs == nil {
			s.BodyNodeIDs = []string{}
		}
		s.Children = materialize(n.kids, next)
		out = append(out, s)
	}
	return out
}

// buildFlat handles a document with no structural markers: one section holding
// every content node, titled after the file. Making up chapters would
// fabricate headings, which is the one thing a baseline must never do.
func buildFlat(doc *docmodel.Document, plan *docmodel.Plan, claim func(string) bool) *docmodel.Plan {
	var ids []string
	for _, n := range doc.ContentNodes() {
		if claim(n.ID) {
			ids = append(ids, n.ID)
		}
	}
	if len(ids) == 0 {
		return plan
	}
	plan.Sections = append(plan.Sections, docmodel.Section{
		ID: "b1", Title: docTitle(doc), Level: 1, BodyNodeIDs: ids,
	})
	plan.Notes = append(plan.Notes,
		"原文没有任何层次标题，因此只生成一个章节；如需分章，请手动新建章节。")
	return plan
}

// bindTitle binds the opening 标题 line to the title slot.
//
// Element binding exists so that a 公文's own front matter is not reported as
// content the plan dropped — the section builder would otherwise sweep the
// title into the first chapter, where it would be typeset as body text under
// a chapter heading ("一、关于开展……的通知", which is visibly wrong).
//
// Only the first content node is considered, and only a line that carries no
// 层次序数 of its own. That is the crucial restriction: a document whose first
// paragraph is "一、总体要求" starts with a chapter, not a title, and binding
// it would drop the first chapter's heading into the 版头. A styled heading is
// eligible too — Word's own 标题 style is how many documents mark it — because
// the test is the absence of a number, not the presence of a style.
func bindTitle(doc *docmodel.Document, plan *docmodel.Plan, claim func(string) bool) {
	for _, n := range doc.ContentNodes() {
		if !looksLikeTitle(n.Text) {
			return
		}
		plan.Elements = append(plan.Elements, docmodel.ElementBinding{
			Role: docmodel.RoleTitle, NodeID: n.ID,
		})
		claim(n.ID)
		return // only the very first content node is considered
	}
}

func clampLevel(l int) int {
	if l < 1 {
		return 1
	}
	if l > 4 {
		return 4
	}
	return l
}

// looksLikeTitle recognises the conventional 公文 标题: a single line near the
// top, with no trailing sentence punctuation.
func looksLikeTitle(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || len([]rune(t)) > 60 {
		return false
	}
	if strings.ContainsAny(t, "。；;") {
		return false
	}
	if parse.NumberingLevel(t) > 0 {
		return false
	}
	return true
}

// docTitle is a fallback section title for a document with no headings: the
// source file name, minus its extension. It is recorded as synthesized (no
// heading node), which is exactly what it is.
func docTitle(doc *docmodel.Document) string {
	name := doc.SourceName
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "正文"
	}
	return name
}
