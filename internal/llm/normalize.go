package llm

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"govdoc/internal/docmodel"
	"govdoc/internal/parse"
)

// editContextRunes is how much surrounding text a normalized edit keeps on
// each side of the actual change.
//
// A review card needs enough context to be readable ("碑身尺寸:长90cm" rather
// than a bare ":"), but the amount must be fixed rather than whatever the model
// happened to quote. A fixed window is what makes two runs produce byte-equal
// before/after strings for the same correction.
const editContextRunes = 12

// NormalizeEdits turns proposed edits into a stable, reviewable list.
//
// # Why this exists
//
// Two analyses of the same document returned different review lists: four
// punctuation fixes one time, seven the next, even though the structure tree
// was byte-identical. The variance was entirely editorial: how long a `before`
// snippet the model quoted, whether a full-width space counted, and whether it
// bothered to mention a spacing change at all. A reviewer who re-runs analysis
// and sees a different set of things to approve cannot tell what the tool
// actually checks.
//
// Asking the model to be more consistent is the request that just failed.
// Instead this treats its edits as *candidate findings* and reduces them with
// rules decidable from the text:
//
//  1. Reduce each edit to its minimal changed span by stripping the common
//     prefix and suffix of before/after. A model that quotes a whole line and
//     one that quotes only the wrong colon are describing the same correction.
//  2. Re-expand that span with a fixed, non-overlapping window of context taken
//     from the source node, so the displayed snippet does not depend on the
//     model's quoting style and two accepted edits in one paragraph cannot
//     invalidate each other's `before`.
//  3. Drop no-ops, pure insertions/deletions, and edits whose target text is
//     absent; they cannot be reviewed meaningfully because they never apply.
//  4. Deduplicate and sort by source position, so the list order no longer
//     reshuffles between runs.
//
// The result is deterministic for a given document and set of corrections, so
// the only remaining variance is which corrections the model noticed — which
// the prompt, plus DeterministicEdits, constrains to objective rules.
func NormalizeEdits(doc *docmodel.Document, edits []docmodel.Edit) []docmodel.Edit {
	if len(edits) == 0 {
		return []docmodel.Edit{}
	}

	order := map[string]int{}
	if doc != nil {
		for _, n := range doc.Nodes {
			order[n.ID] = n.Order
		}
	}

	type span struct {
		start, end int // rune offsets of the changed span
		after      string
	}
	type candidate struct {
		nodeID string
		kind   docmodel.EditKind
		reason string
		occ    int
		sp     span
		pos    int
	}

	var candidates []candidate
	seen := map[string]bool{}

	for _, e := range edits {
		e.NodeID = strings.TrimSpace(e.NodeID)
		e.Before = strings.TrimSpace(e.Before)
		e.After = strings.TrimSpace(e.After)
		e.Reason = strings.TrimSpace(e.Reason)
		if e.NodeID == "" {
			continue
		}

		var nodeText string
		if doc != nil {
			n, ok := doc.Node(e.NodeID)
			if !ok {
				continue
			}
			nodeText = n.Text
		}
		if nodeText == "" {
			continue
		}

		start, end, after, ok := minimalSpan(nodeText, e)
		if !ok {
			continue
		}
		if !objectiveCandidate(nodeText, start, end, after) {
			continue
		}
		key := strings.Join([]string{
			e.NodeID, string(e.Kind), itoa(start), itoa(end), after, itoa(e.Occurrence),
		}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, candidate{
			nodeID: e.NodeID, kind: e.Kind, reason: e.Reason,
			occ: e.Occurrence, sp: span{start: start, end: end, after: after},
			pos: order[e.NodeID],
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		if a.nodeID != b.nodeID {
			return a.nodeID < b.nodeID
		}
		return a.sp.start < b.sp.start
	})

	// One changed span, one review item.
	//
	// The model's finding and the rule-based floor often describe the same
	// correction (a half-width colon), quoting different snippets that reduce
	// to the same span. Keeping both showed two cards for one character, and
	// only the first could ever apply: accepting it makes the second's Before
	// disappear, so it would be reported as unapplied. The sort is stable and
	// puts the model's finding first, so that one wins.
	//
	// A partially overlapping candidate is dropped as well. Its review window
	// would straddle text the kept edit already rewrote, so its Before could
	// not be trusted after acceptance.
	kept := candidates[:0]
	keptNode := ""
	keptEnd := -1
	for _, c := range candidates {
		if c.nodeID != keptNode {
			keptNode = c.nodeID
			keptEnd = -1
		}
		if c.sp.start < keptEnd {
			continue
		}
		kept = append(kept, c)
		if c.sp.end > keptEnd {
			keptEnd = c.sp.end
		}
	}
	candidates = kept

	// Build fixed-context windows, split at the midpoints between adjacent
	// changes. Splitting at the midpoint (rather than giving each change a full
	// ±12 window) is what keeps the windows disjoint: accepting one edit then
	// leaves the other's `before` still present in the text.
	windows := map[string][][2]int{} // nodeID -> [start,end) rune ranges
	for _, c := range candidates {
		windows[c.nodeID] = append(windows[c.nodeID], [2]int{c.sp.start, c.sp.end})
	}

	out := make([]docmodel.Edit, 0, len(candidates))
	lastNode := ""
	var nodeRunes []rune
	for _, c := range candidates {
		if c.nodeID != lastNode {
			lastNode = c.nodeID
			if doc != nil {
				n, _ := doc.Node(c.nodeID)
				nodeRunes = []rune(n.Text)
			}
		}
		if len(nodeRunes) == 0 {
			continue
		}

		start := c.sp.start - editContextRunes
		end := c.sp.end + editContextRunes
		if start < 0 {
			start = 0
		}
		if end > len(nodeRunes) {
			end = len(nodeRunes)
		}

		// Trim this window so it does not include another change's span.
		spans := windows[c.nodeID]
		for _, other := range spans {
			if other[0] == c.sp.start && other[1] == c.sp.end {
				continue
			}
			if other[1] <= c.sp.start {
				if mid := (other[1] + c.sp.start) / 2; mid > start {
					start = mid
				}
				continue
			}
			if other[0] >= c.sp.end {
				if mid := (c.sp.end + other[0]) / 2; mid < end {
					end = mid
				}
			}
		}
		if start > c.sp.start {
			start = c.sp.start
		}
		if end < c.sp.end {
			end = c.sp.end
		}

		before := string(nodeRunes[start:end])
		after := string(nodeRunes[start:c.sp.start]) + c.sp.after + string(nodeRunes[c.sp.end:end])
		out = append(out, docmodel.Edit{
			NodeID:     c.nodeID,
			Kind:       c.kind,
			Before:     before,
			After:      after,
			Reason:     c.reason,
			Occurrence: c.occ,
		})
	}
	return out
}

// objectiveCandidate rejects a model edit whose only change is turning a
// semicolon in the middle of a paragraph into a full stop.
//
// In a list such as "材料：营业执照；开户许可证；授权委托书；", the semicolons
// separate items; a model that rewrites one of them to "。" splits the list and
// changes its meaning. The run-to-run variance this caused was visible in the
// review panel: one analysis reported the final semicolon correctly, another
// also "fixed" a semicolon between two items. The final semicolon is handled by
// DeterministicEdits, so this filter only removes the subjective middle case.
func objectiveCandidate(nodeText string, start, end int, after string) bool {
	runes := []rune(nodeText)
	if start < 0 || end > len(runes) || start >= end {
		return true
	}
	before := string(runes[start:end])
	if strings.TrimSpace(before) != "；" || strings.TrimSpace(after) != "。" {
		return true
	}
	rest := strings.TrimSpace(string(runes[end:]))
	return rest == ""
}

// mergeEdits combines the model's proposals with the rule-based findings.
//
// The rule-based set is the floor: those errors are decidable, so a run that
// overlooks one is simply wrong, and a run that returns no edits at all must
// still give the reviewer the punctuation problems the document actually has.
// The model's own findings are merged on top because they cover classes a rule
// cannot (a wrong character, a run-on sentence); normalization then reduces
// both sets to one stable shape.
func mergeEdits(doc *docmodel.Document, proposed []docmodel.Edit) []docmodel.Edit {
	all := make([]docmodel.Edit, 0, len(proposed)+8)
	all = append(all, proposed...)
	all = append(all, DeterministicEdits(doc)...)

	// Heading edits target the whole heading line and must survive
	// normalization even when the change is only whitespace or numbering.
	// NormalizeEdits reduces an edit to its minimal changed span and drops
	// pure deletions, which is right for prose but would silently discard
	// "一、总体 要求" → "一、总体要求". Keep them verbatim and let the
	// assembler apply them to the heading node.
	var headings, rest []docmodel.Edit
	for _, e := range all {
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
	seen := map[string]bool{}
	for _, e := range headings {
		key := e.NodeID + "\x00" + string(e.Kind) + "\x00" + e.Before + "\x00" + e.After
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}

	order := map[string]int{}
	if doc != nil {
		for _, n := range doc.Nodes {
			order[n.ID] = n.Order
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].NodeID] != order[out[j].NodeID] {
			return order[out[i].NodeID] < order[out[j].NodeID]
		}
		return out[i].Before < out[j].Before
	})
	return out
}

// dropRecomposeConflicts removes declared edits for nodes that are being
// recomposed.
//
// The verifier rejects a node that is both rewritten as several paragraphs and
// edited in place: two declarations would describe the same output, and the
// change log could not say which one produced it. Recompose is the broader
// declaration and already contains the new wording, so it wins; the dropped
// edits are redundant, not lost, because their corrected text is in the parts.
func dropRecomposeConflicts(recompose []docmodel.Recompose, edits []docmodel.Edit) []docmodel.Edit {
	if len(recompose) == 0 || len(edits) == 0 {
		return edits
	}
	recomposed := make(map[string]bool, len(recompose))
	for _, r := range recompose {
		recomposed[r.NodeID] = true
	}
	out := edits[:0]
	for _, e := range edits {
		if recomposed[e.NodeID] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// asHeadingEdit classifies a whole-line rewrite of a source heading as a
// heading change.
//
// The model usually reports one as `wording` (it is, literally, different
// wording), but two things follow from the node being a heading. The review
// card should say 标题 rather than 用词, and the edit must bypass the prose
// normalizer, which strips the common prefix/suffix and drops pure whitespace
// changes — exactly the shapes a retitled heading produces ("一、总体 要求").
//
// Only a Before equal to the entire heading line qualifies. A substring is a
// normal in-line correction and keeps the model's own classification.
func asHeadingEdit(doc *docmodel.Document, e docmodel.Edit) docmodel.Edit {
	if doc == nil || e.NodeID == "" || e.Kind == docmodel.EditHeading {
		return e
	}
	n, ok := doc.Node(e.NodeID)
	if !ok || n.Kind != docmodel.KindHeading {
		return e
	}
	if strings.TrimSpace(e.Before) != strings.TrimSpace(n.Text) {
		return e
	}
	if strings.TrimSpace(e.After) == "" || e.After == e.Before {
		return e
	}
	e.Kind = docmodel.EditHeading
	// The Before is now the whole heading line, which occurs exactly once. A
	// leftover occurrence from the model's original snippet (it may have said
	// "the second 标志牌") would point past the only match, making the edit
	// silently unapplied at export.
	e.Occurrence = 0
	return e
}

// promoteMetaTitle turns a model-authored meta.title into a reviewable edit
// when the source already has a title paragraph.
//
// The user's report: the source had a title, the model proposed a different
// one, and the change never appeared in the review list. meta.title is not a
// source patch — it is a synthesized line — so silently ignoring it (because a
// bound title wins) hides the suggestion, while printing it would duplicate
// the title. Converting it to an Edit keeps both properties: the document
// keeps the source title until the user accepts, and the suggestion is visible.
func promoteMetaTitle(plan *docmodel.Plan, doc *docmodel.Document) {
	if plan == nil || doc == nil {
		return
	}
	proposed := strings.TrimSpace(plan.Meta.Title)
	if proposed == "" {
		return
	}

	if id := titleElementID(plan); id != "" {
		n, ok := doc.Node(id)
		if !ok {
			return
		}
		if strings.TrimSpace(n.Text) == proposed {
			plan.Meta.Title = ""
			return
		}
		plan.Edits = append(plan.Edits, docmodel.Edit{
			NodeID: id,
			Kind:   docmodel.EditHeading,
			Before: n.Text,
			After:  proposed,
			Reason: fmt.Sprintf("原文标题为「%s」，模型建议改为「%s」", n.Text, proposed),
		})
		plan.Meta.Title = ""
		return
	}

	// No element binding: the model may still have proposed a new title for
	// the document's opening line. Only treat a plausible title paragraph as
	// the target; otherwise the value is left alone rather than guessed onto
	// arbitrary prose.
	id := findLikelyTitleNode(doc)
	if id == "" {
		return
	}
	n, _ := doc.Node(id)
	if strings.TrimSpace(n.Text) == proposed {
		plan.Elements = append(plan.Elements, docmodel.ElementBinding{
			Role: docmodel.RoleTitle, NodeID: id,
		})
		plan.Meta.Title = ""
		return
	}
	if !nodeClaimed(plan, id) {
		plan.Elements = append(plan.Elements, docmodel.ElementBinding{
			Role: docmodel.RoleTitle, NodeID: id,
		})
	}
	plan.Edits = append(plan.Edits, docmodel.Edit{
		NodeID: id,
		Kind:   docmodel.EditHeading,
		Before: n.Text,
		After:  proposed,
		Reason: fmt.Sprintf("原文标题为「%s」，模型建议改为「%s」", n.Text, proposed),
	})
	plan.Meta.Title = ""
}

// dropInventedHeaderMeta removes 版头 values the model supplied itself.
//
// 版头 fields (份号/密级/紧急程度/发文机关标志/发文字号/签发人) are only
// trustworthy when they are bound to a paragraph the author actually wrote.
// A value in meta with no matching element is model-authored prose. Emitting
// it is how a generated document acquired a masthead the source never had —
// the "又生成了一个标题" report. Dropping it is not a content change: there is
// no source text to preserve. The note makes the removal visible.
//
// The drop is conditional on the source already having a 标题. A document
// without one may legitimately ask the model to supply 版头 metadata, and
// suppressing that would remove a feature the user still needs. When the
// source has its own title, however, adding a second title-like area above it
// is exactly the duplicate the user reported.
//
// "The value occurs somewhere in the source" is deliberately NOT enough to
// keep it. The reported document contained "殷都区文化广电体育旅游局" inside a
// body sentence, which is why an earlier substring check let the masthead
// through. An unbound value is model-authored regardless of whether the same
// characters happen to appear in unrelated prose; only an element binding
// proves the author wrote it as that element.
func dropInventedHeaderMeta(plan *docmodel.Plan, doc *docmodel.Document) {
	if plan == nil {
		return
	}
	// A title the source wrote but the model forgot to bind still means the
	// source has a 标题. Emitting a model-authored masthead above it would
	// reproduce the reported duplicate even though no element is bound yet.
	if titleElementID(plan) == "" && !sourceHasTitleParagraph(doc) {
		return
	}
	bound := map[docmodel.Role]bool{}
	for _, e := range plan.Elements {
		bound[e.Role] = true
	}
	drop := func(role docmodel.Role, label, value string, clear func()) {
		if bound[role] || strings.TrimSpace(value) == "" {
			return
		}
		// If the author's own paragraph is exactly this value, the value is
		// not invented: bind that paragraph so it keeps its wording and its
		// element styling. Binding is only safe when nothing else claims the
		// node; otherwise emitting it twice would be worse than leaving it
		// where the plan already put it.
		if id := exactNodeID(doc, value); id != "" && !nodeClaimed(plan, id) {
			plan.Elements = append(plan.Elements, docmodel.ElementBinding{
				Role: role, NodeID: id,
			})
			bound[role] = true
			clear()
			return
		}
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"已移除模型自行生成的%s「%s」：原文无对应段落", label, value))
		clear()
	}
	drop(docmodel.RoleCopyNumber, "份号", plan.Meta.CopyNumber, func() { plan.Meta.CopyNumber = "" })
	drop(docmodel.RoleSecurity, "密级", plan.Meta.Security, func() { plan.Meta.Security = "" })
	drop(docmodel.RoleUrgency, "紧急程度", plan.Meta.Urgency, func() { plan.Meta.Urgency = "" })
	drop(docmodel.RoleMasthead, "发文机关标志", plan.Meta.IssuingOrg, func() { plan.Meta.IssuingOrg = "" })
	drop(docmodel.RoleDocNumber, "发文字号", plan.Meta.DocNumber, func() { plan.Meta.DocNumber = "" })
	drop(docmodel.RoleSignatory, "签发人", plan.Meta.Signatory, func() { plan.Meta.Signatory = "" })
}

// sourceHasTitleParagraph reports whether the document opens with a line that
// reads like a 公文 标题, using the same test the deterministic baseline uses
// to decide whether to bind one.
func sourceHasTitleParagraph(doc *docmodel.Document) bool {
	if doc == nil {
		return false
	}
	for _, n := range doc.ContentNodes() {
		if n.Kind == docmodel.KindTable || n.Kind == docmodel.KindImage {
			continue
		}
		t := strings.TrimSpace(n.Text)
		if t == "" {
			continue
		}
		if len([]rune(t)) > 60 || strings.ContainsAny(t, "。；;") {
			return false
		}
		if parse.NumberingLevel(t) > 0 {
			return false
		}
		return true
	}
	return false
}

// exactNodeID returns the id of a prose node whose whole text equals want
// (ignoring surrounding whitespace), or "". Unlike a substring match, a whole
// paragraph equality is evidence the author wrote that line as this value.
func exactNodeID(doc *docmodel.Document, want string) string {
	if doc == nil {
		return ""
	}
	want = strings.TrimSpace(want)
	if want == "" {
		return ""
	}
	for _, n := range doc.ContentNodes() {
		if strings.TrimSpace(n.Text) == want {
			return n.ID
		}
	}
	return ""
}

// titleElementID returns the node bound to RoleTitle, or "".
func titleElementID(plan *docmodel.Plan) string {
	if plan == nil {
		return ""
	}
	for _, e := range plan.Elements {
		if e.Role == docmodel.RoleTitle && e.NodeID != "" {
			return e.NodeID
		}
	}
	return ""
}

// nodeClaimed reports whether a plan already places a node somewhere.
func nodeClaimed(plan *docmodel.Plan, id string) bool {
	if plan == nil || id == "" {
		return false
	}
	for _, e := range plan.Elements {
		if e.NodeID == id {
			return true
		}
	}
	for _, pid := range plan.PreambleNodeIDs {
		if pid == id {
			return true
		}
	}
	var found bool
	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID == id {
			found = true
			return false
		}
		for _, bid := range s.BodyNodeIDs {
			if bid == id {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// bindForgottenTitle attaches a source title paragraph the model never bound.
//
// A document whose first line is the 标题 must have it in the title slot. If
// the model leaves it out, the paragraph is either reported as dropped content
// or swept into a section's body, where the assembler prints it as 正文 under a
// chapter heading — visibly wrong even though every character is preserved.
// Binding it is the only reading that keeps the line in the 版头 and stops a
// model-authored masthead from being emitted above it.
func bindForgottenTitle(plan *docmodel.Plan, doc *docmodel.Document) {
	if plan == nil || doc == nil || titleElementID(plan) != "" {
		return
	}
	id := findLikelyTitleNode(doc)
	if id == "" || nodeClaimed(plan, id) {
		return
	}
	plan.Elements = append(plan.Elements, docmodel.ElementBinding{
		Role: docmodel.RoleTitle, NodeID: id,
	})
}

// findLikelyTitleNode returns the first paragraph that reads like a 公文 标题.
// It is only used to attach a model-proposed title change to real source text;
// when no such paragraph exists, nothing is guessed.
func findLikelyTitleNode(doc *docmodel.Document) string {
	if doc == nil {
		return ""
	}
	for _, n := range doc.ContentNodes() {
		if n.Kind == docmodel.KindTable || n.Kind == docmodel.KindImage {
			continue
		}
		t := strings.TrimSpace(n.Text)
		if t == "" || len([]rune(t)) > 60 {
			continue
		}
		if strings.ContainsAny(t, "。；;") {
			continue
		}
		if parse.NumberingLevel(t) > 0 {
			continue
		}
		return n.ID
	}
	return ""
}

// minimalSpan locates the smallest changed span of one edit inside nodeText.
func minimalSpan(nodeText string, e docmodel.Edit) (start, end int, after string, ok bool) {
	beforeRunes := []rune(e.Before)
	afterRunes := []rune(e.After)
	if len(beforeRunes) == 0 || len(afterRunes) == 0 {
		return 0, 0, "", false
	}

	p := 0
	for p < len(beforeRunes) && p < len(afterRunes) && beforeRunes[p] == afterRunes[p] {
		p++
	}
	bEnd := len(beforeRunes)
	aEnd := len(afterRunes)
	for bEnd > p && aEnd > p && beforeRunes[bEnd-1] == afterRunes[aEnd-1] {
		bEnd--
		aEnd--
	}
	minBefore := string(beforeRunes[p:bEnd])
	minAfter := string(afterRunes[p:aEnd])
	if minBefore == "" || minAfter == "" || minBefore == minAfter {
		return 0, 0, "", false
	}

	abs := nthRuneIndex(nodeText, e.Before, e.Occurrence)
	if abs < 0 {
		abs = nthRuneIndex(nodeText, minBefore, e.Occurrence)
		p = 0
	}
	if abs < 0 {
		return 0, 0, "", false
	}
	return abs + p, abs + p + utf8.RuneCountInString(minBefore), minAfter, true
}

// nthRuneIndex returns the rune offset of the n-th occurrence of sub within s,
// or -1. Occurrence below zero is treated as zero.
func nthRuneIndex(s, sub string, occurrence int) int {
	if sub == "" {
		return -1
	}
	if occurrence < 0 {
		occurrence = 0
	}
	from := 0
	seen := 0
	for {
		i := strings.Index(s[from:], sub)
		if i < 0 {
			return -1
		}
		pos := from + i
		if seen == occurrence {
			return utf8.RuneCountInString(s[:pos])
		}
		seen++
		from = pos + 1
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
