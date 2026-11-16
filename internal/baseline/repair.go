package baseline

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"govdoc/internal/docmodel"
	"govdoc/internal/parse"
)

// Repair applies the structural fixes that need no judgement, and reports what
// it did.
//
// # Why this runs before the model is asked again
//
// Most failures of a model plan are mechanical: a paragraph was not listed at
// all, the same id was listed twice, a section's title was copied with a typo
// so it no longer matches the source heading, or a child section was given the
// same level as its parent. Each of these is decidable from the document
// alone — the answer is "put it where the source puts it", "keep the first
// claim", "use the source's text", "make it one level deeper". None of them
// requires another opinion.
//
// Spending a model round on them costs tens of seconds and the user's tokens,
// and the model may well return the same defect again. Repairing locally first
// means a plan that is *structurally* sound but mechanically sloppy is accepted
// in the first pass.
//
// # What it will not do
//
// It never reorders, never invents a chapter, never deletes content, and never
// touches an edit. It only makes the plan satisfy the rules the verifier
// already enforces. Everything it changes is returned in `fixes` so the caller
// can record it — a repair the user cannot see would be exactly the kind of
// silent change this project refuses.
//
// The result is a new plan; the input is not modified.
func Repair(doc *docmodel.Document, plan *docmodel.Plan) (*docmodel.Plan, []string) {
	if doc == nil || plan == nil {
		return plan, nil
	}

	out := clonePlan(plan)
	var fixes []string

	bindHeadingsFromBody(doc, out, &fixes)
	bindRenamedHeadingsFromBody(doc, out, &fixes, &out.Edits)
	fixHeadingTitles(doc, out, &fixes, &out.Edits)
	fixLevels(doc, out, &fixes)
	fixDuplicateClaims(doc, out, &fixes)
	fixCoverage(doc, out, &fixes)

	if len(fixes) == 0 {
		return plan, nil
	}
	return out, fixes
}

// bindHeadingsFromBody points a section at the source heading it already
// carries, instead of leaving that heading in the body and printing a
// model-authored title above it.
//
// # The failure this repairs
//
// A model that recognises "（一）主要规格参数" as a chapter often reproduces its
// text in `title` but forgets `heading_node_id`, or lists the heading paragraph
// in `body_node_ids`. Both produce two heading lines on the page: the
// synthesized one and the source one. The text is identical (or differs only
// in spacing), so the right destination is unambiguous, and the source's own
// paragraph is always the more faithful choice.
//
// Only an exact match (ignoring whitespace) qualifies, and only when it names a
// single source heading. A near match would assert an equivalence the model did
// not mean; that case is reported as a warning instead.
func bindHeadingsFromBody(doc *docmodel.Document, plan *docmodel.Plan, fixes *[]string) {
	if doc == nil || plan == nil {
		return
	}

	claims := map[string]int{}
	for _, e := range plan.Elements {
		claims[e.NodeID]++
	}
	for _, id := range plan.PreambleNodeIDs {
		claims[id]++
	}
	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID != "" {
			claims[s.HeadingNodeID]++
		}
		for _, id := range s.BodyNodeIDs {
			claims[id]++
		}
		for _, bp := range s.BodyParts {
			claims[bp.NodeID]++
		}
		return true
	})

	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID != "" {
			return true
		}
		want := normalizeHeadingText(s.Title)
		if want == "" {
			return true
		}

		// Candidates in source order: a heading-styled node, a node whose text
		// starts with a 层次序数, or a paragraph the model named verbatim as
		// this section's title.
		var candidates []string
		for _, n := range doc.ContentNodes() {
			if normalizeHeadingText(n.Text) != want {
				continue
			}
			if !headingLike(n) && !containsID(s.BodyNodeIDs, n.ID) {
				continue
			}
			candidates = append(candidates, n.ID)
		}
		if len(candidates) != 1 {
			return true
		}

		id := candidates[0]
		// Claimed by another section or bound elsewhere: leave it alone, the
		// duplicate-claim repair will resolve it.
		if inBody := containsID(s.BodyNodeIDs, id); !inBody && claims[id] > 0 {
			return true
		}
		if inBody := containsID(s.BodyNodeIDs, id); inBody && claims[id] > 1 {
			return true
		}

		s.HeadingNodeID = id
		s.BodyNodeIDs = removeID(s.BodyNodeIDs, id)
		*fixes = append(*fixes, fmt.Sprintf(
			"章节 %s 的标题改回原文标题节点 %s（%q）", s.ID, id, doc.Text(id)))
		return true
	})
}

// bindRenamedHeadingsFromBody handles the case bindHeadingsFromBody cannot:
// the section's title is the model's *rewrite* of a source heading, so the text
// does not match and there is nothing exact to bind. The heading paragraph is
// still sitting at the front of the section's body, which means the assembled
// page would print the model's title and then the author's heading directly
// below it — the duplicate heading the user reported.
//
// Binding the source heading is the faithful choice, and the model's wording
// becomes a reviewable EditHeading. That is the same rule fixHeadingTitles
// applies when a heading *was* bound: the source stays until the user accepts,
// and the suggestion is visible instead of silently discarded.
//
// Only the first body node is considered. A heading deeper inside the body may
// belong to a nested chapter the model failed to model, and guessing which one
// it is would assert a structure the model did not state.
func bindRenamedHeadingsFromBody(
	doc *docmodel.Document,
	plan *docmodel.Plan,
	fixes *[]string,
	edits *[]docmodel.Edit,
) {
	if doc == nil || plan == nil {
		return
	}
	// A node already claimed as a heading belongs to another section; binding
	// it again would turn a wrong title into a duplicate-content error.
	claimed := map[string]bool{}
	for _, e := range plan.Elements {
		claimed[e.NodeID] = true
	}
	for _, id := range plan.PreambleNodeIDs {
		claimed[id] = true
	}
	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID != "" {
			claimed[s.HeadingNodeID] = true
		}
		return true
	})

	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID != "" || len(s.BodyNodeIDs) == 0 {
			return true
		}
		proposed := strings.TrimSpace(s.Title)
		if proposed == "" {
			return true
		}
		id := s.BodyNodeIDs[0]
		n, ok := doc.Node(id)
		if !ok || !headingLike(n) {
			return true
		}
		// The source paragraph must be a heading of the same depth. Otherwise
		// it is most likely a child heading the model left in the body (a
		// synthesized parent such as "二、技术要求" above "（一）主要规格参数")
		// and binding it would turn a missing chapter into a wrong title.
		if sourceHeadingLevel(n) != s.Level {
			return true
		}
		if claimed[id] {
			return true
		}
		// An identical title was already handled by bindHeadingsFromBody.
		// Reaching here with equal text means the node is claimed elsewhere;
		// leave that to the duplicate-claim repair.
		source := strings.TrimSpace(n.Text)
		if source == "" || source == proposed {
			return true
		}
		// A same-level heading in the body is not proof that the model meant
		// to retitle this section: it may be a child heading the model failed
		// to model, and rebinding it would move a chapter. Require the two
		// lines to look like the same heading before treating one as a rewrite.
		if !headingRewriteLikely(source, proposed) {
			return true
		}

		claimed[id] = true
		s.HeadingNodeID = id
		s.BodyNodeIDs = removeID(s.BodyNodeIDs, id)
		s.Title = n.Text
		if !headingTextEquivalent(source, proposed) {
			*edits = append(*edits, docmodel.Edit{
				NodeID: id,
				Kind:   docmodel.EditHeading,
				Before: n.Text,
				After:  proposed,
				Reason: fmt.Sprintf(
					"原文标题为「%s」，模型建议改为「%s」", n.Text, proposed),
			})
		}
		*fixes = append(*fixes, fmt.Sprintf(
			"章节 %s 的标题 %q 与原稿标题段 %s（%q）重复，已改为引用原文并把模型写法列为校对项",
			s.ID, proposed, id, n.Text))
		return true
	})
}

// sourceHeadingLevel returns the depth the source paragraph itself declares:
// its Word/list level for a styled heading, else its 层次序数, else 0.
func sourceHeadingLevel(n docmodel.Node) int {
	if n.Level >= 1 && n.Level <= 4 {
		return n.Level
	}
	return parse.NumberingLevel(n.Text)
}

// headingTextEquivalent reports whether two headings differ only in their
// 层次序数 or whitespace. A model that writes "项目概况" for "一、项目概况" is
// copying the source, not proposing new wording, so no review item is needed.
func headingTextEquivalent(a, b string) bool {
	na := normalizeHeadingText(stripHeadingMarker(a))
	nb := normalizeHeadingText(stripHeadingMarker(b))
	return na != "" && na == nb
}

// headingRewriteLikely reports whether b is a plausible rewrite of a rather
// than an unrelated heading. The test is deliberately conservative: binding a
// heading the model did not mean would move a chapter and create an edit the
// user never asked for, which is worse than leaving a warning in place.
//
// The distance threshold is half the longer heading. Short headings are
// compared on their text after the 层次序数 is removed, so "项目概况" and
// "一、项目基本情况" stay related while "二、技术要求" and "（一）主要规格参数"
// do not.
func headingRewriteLikely(a, b string) bool {
	na := normalizeHeadingText(stripHeadingMarker(a))
	nb := normalizeHeadingText(stripHeadingMarker(b))
	if na == "" || nb == "" {
		return false
	}
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		return true
	}
	ra, rb := []rune(na), []rune(nb)
	longer := len(ra)
	if len(rb) > longer {
		longer = len(rb)
	}
	limit := longer / 2
	if limit < 2 {
		limit = 2
	}
	return levenshtein(ra, rb) <= limit
}

// stripHeadingMarker removes a leading 层次序数 such as "一、", "（一）",
// "1." or "（1）". It is only applied to heading text, and only when the marker
// is short enough to actually be a marker rather than a sentence's own text.
func stripHeadingMarker(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	r := []rune(t)
	// （一） / (1)
	if len(r) > 2 && (r[0] == '（' || r[0] == '(') {
		for i := 1; i < len(r) && i <= 5; i++ {
			if r[i] == '）' || r[i] == ')' {
				return strings.TrimSpace(string(r[i+1:]))
			}
		}
	}
	// 一、 / 1、
	for i := 1; i < len(r) && i <= 4; i++ {
		if r[i] == '、' {
			return strings.TrimSpace(string(r[i+1:]))
		}
	}
	// 1.
	for i := 1; i < len(r) && i <= 4; i++ {
		if r[i] == '.' {
			return strings.TrimSpace(string(r[i+1:]))
		}
	}
	return t
}

// levenshtein returns the edit distance between two short rune slices.
// Headings are at most a few dozen characters, so the simple two-row dynamic
// program is both sufficient and easy to audit.
func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// headingLike reports whether a node carries the source's own heading evidence.
func headingLike(n docmodel.Node) bool {
	if n.Kind == docmodel.KindHeading {
		return true
	}
	return parse.NumberingLevel(n.Text) > 0
}

// normalizeHeadingText strips whitespace so "一、总体 要求" and "一、总体要求"
// compare equal. Interior spaces are a formatting difference, not a wording
// change, and treating them as a mismatch is what produced duplicate headings.
func normalizeHeadingText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s))
}

func containsID(ids []string, id string) bool {
	for _, cur := range ids {
		if cur == id {
			return true
		}
	}
	return false
}

func removeID(ids []string, id string) []string {
	out := ids[:0]
	for _, cur := range ids {
		if cur != id {
			out = append(out, cur)
		}
	}
	return out
}

// fixHeadingTitles makes a section's Title equal the source heading's text.
//
// VerifyPlan requires them to match, and there is only one right answer: the
// text is in the document. A model that paraphrased the heading, dropped a
// space, or normalised a bracket produced a heading_mismatch it cannot fix
// better than the source can.
//
// # Why the model's proposal becomes an Edit instead of disappearing
//
// A changed section title is a proposed change to *content* — the heading
// paragraph the author wrote. Restoring the source and only recording a note
// meant the suggestion was never shown in the review list, so the user could
// not accept it, and "no heading change was offered for review" looked like
// the feature was broken. Emitting an Edit keeps it visible and actionable:
// the plan stays valid (its Title matches the source again) while the user
// decides whether the wording should actually change.
//
// Before is the whole source heading, not a guessed substring. That is the one
// anchor guaranteed to occur verbatim, and a title is short enough that showing
// it whole is clearer than a partial diff.
func fixHeadingTitles(
	doc *docmodel.Document,
	plan *docmodel.Plan,
	fixes *[]string,
	edits *[]docmodel.Edit,
) {
	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID == "" {
			return true
		}
		n, ok := doc.Node(s.HeadingNodeID)
		if !ok {
			return true
		}
		if strings.TrimSpace(s.Title) == strings.TrimSpace(n.Text) {
			return true
		}
		*fixes = append(*fixes, fmt.Sprintf(
			"章节 %s 的标题改回原文：%q → %q", s.ID, s.Title, n.Text))
		proposed := strings.TrimSpace(s.Title)
		if proposed != "" && proposed != strings.TrimSpace(n.Text) {
			*edits = append(*edits, docmodel.Edit{
				NodeID: n.ID,
				Kind:   docmodel.EditHeading,
				Before: n.Text,
				After:  proposed,
				Reason: fmt.Sprintf(
					"原文标题为「%s」，模型建议改为「%s」", n.Text, proposed),
			})
		}
		s.Title = n.Text
		return true
	})
}

// fixLevels deepens a child section that is not strictly deeper than its
// parent, and raises a level-0 section to 1.
//
// The depth is a property of the tree the model built, so the tree already
// says what it meant; only the number is wrong.
func fixLevels(doc *docmodel.Document, plan *docmodel.Plan, fixes *[]string) {
	var walk func(list []docmodel.Section, parentLevel int)
	walk = func(list []docmodel.Section, parentLevel int) {
		for i := range list {
			s := &list[i]
			want := 1
			if parentLevel > 0 {
				want = parentLevel + 1
			}
			if want > 4 {
				want = 4
			}
			if s.Level < want {
				*fixes = append(*fixes, fmt.Sprintf(
					"章节 %s 的层级 %d 调整为 %d（与父级匹配）", s.ID, s.Level, want))
				s.Level = want
			} else if s.Level > 4 {
				*fixes = append(*fixes, fmt.Sprintf(
					"章节 %s 的层级 %d 调整为 4（层次序数最深四级）", s.ID, s.Level))
				s.Level = 4
			}
			walk(s.Children, s.Level)
		}
	}
	walk(plan.Sections, 0)
}

// fixDuplicateClaims keeps the first claim on each node and removes the rest.
//
// "First" is deliberate: the model listed the placement it thought of first,
// and the assembler emits in tree order, so the first claim is also the one
// that would have rendered. Keeping it means the document does not move.
// Claim sites are checked in the same priority order the verifier uses —
// elements, then preamble, then sections in tree order.
func fixDuplicateClaims(doc *docmodel.Document, plan *docmodel.Plan, fixes *[]string) {
	seen := map[string]bool{}
	keep := func(id string) bool {
		if id == "" {
			return false
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		return true
	}

	// Elements: a role may only be bound once, and the first binding wins.
	seenRole := map[docmodel.Role]bool{}
	elems := plan.Elements[:0]
	for _, e := range plan.Elements {
		if e.Role == "" || e.NodeID == "" {
			elems = append(elems, e)
			continue
		}
		if seenRole[e.Role] {
			*fixes = append(*fixes, fmt.Sprintf(
				"要素 %s 重复绑定到 %s，已保留先出现的一处", e.Role, e.NodeID))
			continue
		}
		if !keep(e.NodeID) {
			*fixes = append(*fixes, fmt.Sprintf(
				"节点 %s 已被别处收录，解除其与要素 %s 的绑定", e.NodeID, e.Role))
			continue
		}
		seenRole[e.Role] = true
		elems = append(elems, e)
	}
	plan.Elements = elems

	pre := plan.PreambleNodeIDs[:0]
	for _, id := range plan.PreambleNodeIDs {
		if !keep(id) {
			*fixes = append(*fixes, fmt.Sprintf("前言中的节点 %s 重复出现，已去重", id))
			continue
		}
		pre = append(pre, id)
	}
	plan.PreambleNodeIDs = pre

	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID != "" && !keep(s.HeadingNodeID) {
			*fixes = append(*fixes, fmt.Sprintf(
				"章节 %s 的标题节点 %s 已被别处收录，改为自拟标题 %q",
				s.ID, s.HeadingNodeID, s.Title))
			s.HeadingNodeID = ""
		}
		body := s.BodyNodeIDs[:0]
		for _, id := range s.BodyNodeIDs {
			if !keep(id) {
				*fixes = append(*fixes, fmt.Sprintf(
					"章节 %s 重复收录了节点 %s，已移除该处", s.ID, id))
				continue
			}
			body = append(body, id)
		}
		s.BodyNodeIDs = body
		return true
	})
}

// fixCoverage places every unclaimed content node where the source puts it.
//
// The destination is decided by source order alone: a node goes into the
// section whose heading most recently preceded it, or into the preamble when
// no heading has come yet. That is the same rule Build uses, so a node the
// model forgot lands where a structure-only reading would have put it.
//
// Ids the document does not contain are dropped: the verifier would reject
// them as unknown_refs, and there is nothing to place.
func fixCoverage(doc *docmodel.Document, plan *docmodel.Plan, fixes *[]string) {
	// Every existing claim counts, including the sections'. Omitting them
	// would make each already-placed paragraph look missing and append it a
	// second time — turning a coverage fix into a duplication error.
	claimed := map[string]bool{}
	for _, e := range plan.Elements {
		claimed[e.NodeID] = true
	}
	for _, id := range plan.PreambleNodeIDs {
		claimed[id] = true
	}
	// Recomposed nodes are placed part by part, so their node id need not
	// appear in body_node_ids. Counting them here prevents the coverage repair
	// from re-inserting the whole paragraph next to its own parts.
	for _, rec := range plan.Recompose {
		claimed[rec.NodeID] = true
	}
	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID != "" {
			claimed[s.HeadingNodeID] = true
		}
		for _, id := range s.BodyNodeIDs {
			claimed[id] = true
		}
		for _, bp := range s.BodyParts {
			claimed[bp.NodeID] = true
		}
		return true
	})

	// owner maps a source position to the section that should receive a node
	// at that position: the last section whose heading came at or before it.
	type marker struct {
		order int
		sec   *docmodel.Section
	}
	var markers []marker
	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID != "" {
			if n, ok := doc.Node(s.HeadingNodeID); ok {
				markers = append(markers, marker{order: n.Order, sec: s})
			}
		}
		return true
	})
	// Source order, not tree order: the model may have reordered chapters,
	// and a recovered paragraph must land in the chapter the *source* puts it
	// in, not the one the model's tree happens to visit.
	sort.Slice(markers, func(i, j int) bool { return markers[i].order < markers[j].order })

	for _, n := range doc.Nodes {
		if !n.Kind.CarriesProse() || claimed[n.ID] {
			continue
		}
		var target *docmodel.Section
		for _, m := range markers {
			if m.order <= n.Order {
				target = m.sec
			}
		}
		if target == nil {
			plan.PreambleNodeIDs = append(plan.PreambleNodeIDs, n.ID)
			claimed[n.ID] = true
			*fixes = append(*fixes, fmt.Sprintf(
				"补收未收录的段落 %s（%s）到正文前言", n.ID, excerpt(n.Text, 20)))
			continue
		}
		target.BodyNodeIDs = append(target.BodyNodeIDs, n.ID)
		claimed[n.ID] = true
		*fixes = append(*fixes, fmt.Sprintf(
			"补收未收录的段落 %s（%s）到章节 %s", n.ID, excerpt(n.Text, 20), target.ID))
	}
}

// clonePlan deep-copies the plan so Repair can never mutate the plan the
// server is holding.
func clonePlan(p *docmodel.Plan) *docmodel.Plan {
	if p == nil {
		return nil
	}
	out := *p
	out.Meta = p.Meta
	out.Elements = append([]docmodel.ElementBinding(nil), p.Elements...)
	out.PreambleNodeIDs = append([]string(nil), p.PreambleNodeIDs...)
	out.Edits = append([]docmodel.Edit(nil), p.Edits...)
	out.Recompose = make([]docmodel.Recompose, len(p.Recompose))
	for i, r := range p.Recompose {
		out.Recompose[i] = r
		out.Recompose[i].Parts = append([]string(nil), r.Parts...)
		out.Recompose[i].Roles = append([]string(nil), r.Roles...)
	}
	out.Notes = append([]string(nil), p.Notes...)
	out.Sections = cloneSections(p.Sections)
	return &out
}

func cloneSections(list []docmodel.Section) []docmodel.Section {
	if list == nil {
		return nil
	}
	out := make([]docmodel.Section, len(list))
	for i, s := range list {
		out[i] = s
		out[i].BodyNodeIDs = append([]string(nil), s.BodyNodeIDs...)
		out[i].BodyParts = append([]docmodel.BodyPart(nil), s.BodyParts...)
		out[i].Children = cloneSections(s.Children)
	}
	return out
}
