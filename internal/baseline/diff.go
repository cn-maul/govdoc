package baseline

import (
	"fmt"
	"sort"
	"strings"

	"govdoc/internal/docmodel"
	"govdoc/internal/parse"
)

// MoveKind classifies one structural difference between a baseline and a plan.
type MoveKind string

const (
	// MoveSection means a paragraph sits in a different chapter than the one
	// the source's own numbering puts it in.
	MoveSection MoveKind = "moved"
	// MoveToPreamble means a paragraph was taken out of a chapter and placed
	// directly under 主送机关.
	MoveToPreamble MoveKind = "to_preamble"
	// MoveFromPreamble is the reverse: 缘由 prose promoted into a chapter.
	MoveFromPreamble MoveKind = "from_preamble"
	// MoveToElement means a paragraph was bound to a 公文 element slot
	// (标题, 成文日期, …) rather than kept as chapter body.
	MoveToElement MoveKind = "to_element"
)

// Move is one paragraph whose chapter归属 changed.
type Move struct {
	NodeID string `json:"node_id"`
	// Text is a short excerpt, for the review screen to show without a
	// second round trip.
	Text string   `json:"text"`
	Kind MoveKind `json:"kind"`
	// From is the title of the baseline section (or "前言"), FromID its id.
	From   string `json:"from"`
	FromID string `json:"from_id"`
	// To is where the plan puts it.
	To   string `json:"to"`
	ToID string `json:"to_id"`
	// Count is set on a grouped entry: a flat source has no chapters, so the
	// paragraphs that one new chapter absorbs are reported as one item
	// instead of one card per paragraph. NodeID is empty on such an entry.
	Count int `json:"count,omitempty"`
}

// SectionChange is one chapter that the plan added, removed, or retitled
// relative to the source's own structure.
type SectionChange struct {
	// Kind is "added", "missing", "retitled", "releveled", "reparented" or
	// "synthesized".
	Kind string `json:"kind"`
	// ID is the plan's section id, or the baseline's for "missing".
	ID string `json:"id"`
	// Title is the plan's title for added/retitled, the baseline's for
	// missing.
	Title string `json:"title"`
	// BaselineTitle is the source's own title, for "retitled" and "added".
	BaselineTitle string `json:"baseline_title,omitempty"`
	// HeadingNodeID is the source heading paragraph this chapter is built
	// from, when there is one. It lets the review screen offer "restore this
	// chapter" without guessing which paragraph the title came from.
	HeadingNodeID string `json:"heading_node_id,omitempty"`
	// Level and BaselineLevel describe the chapter's depth before and after.
	// A model may leave the title untouched but move the chapter a level
	// deeper, which changes the printed 层次序数 just as visibly as a retitle.
	Level         int `json:"level,omitempty"`
	BaselineLevel int `json:"baseline_level,omitempty"`
	// Parent is where the chapter hangs in the rendered tree, BaselineParent
	// where it hung in the source's own tree. A model can leave the title and
	// the level untouched but lift a child chapter to the root (or bury it
	// under another chapter); the printed indentation and the chapter's
	// relationship to its neighbours both change, so this is a structural
	// change in its own right.
	Parent                      string `json:"parent,omitempty"`
	BaselineParent              string `json:"baseline_parent,omitempty"`
	ParentHeadingNodeID         string `json:"parent_heading_node_id,omitempty"`
	BaselineParentHeadingNodeID string `json:"baseline_parent_heading_node_id,omitempty"`
}

// ElementChange is one paragraph the plan turned into a 公文 element that the
// source's own structure did not treat as one.
//
// The baseline binds only the opening 标题; every other binding (主送机关,
// 成文日期, 抄送…) is a decision the model made and the reviewer should see.
type ElementChange struct {
	// Kind is "bound" for a new paragraph→element binding, "unbound" for a
	// baseline binding the plan dropped (for example, using the source 标题 as
	// ordinary body text).
	Kind   string `json:"kind"`
	Role   string `json:"role"`
	NodeID string `json:"node_id"`
	Text   string `json:"text"`
}

// MetaChange is a 公文 element whose value the model wrote itself, with no
// source paragraph behind it. Such a value is printed but cannot be traced
// back to the upload, so it belongs in the change log.
type MetaChange struct {
	Field string `json:"field"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// BodyOrder records a section (or the preamble) whose text sequence differs
// from the source order. It is the per-section counterpart of SectionMove.
type BodyOrder struct {
	Kind      string   `json:"kind"` // "section" | "preamble"
	SectionID string   `json:"section_id,omitempty"`
	Title     string   `json:"title"`
	NodeIDs   []string `json:"node_ids"`
	// HasParts means the section also places recomposed fragments. Reordering
	// those cannot be expressed by sorting body ids alone, so the review
	// screen offers the item without pretending it can auto-restore it.
	HasParts bool `json:"has_parts,omitempty"`
}

// StructureDiff is the structural counterpart of VerbatimDiff: what changed,
// where, in terms a reviewer can judge.
//
// # Why this exists
//
// VerifyPlan proves nothing was lost or duplicated. It cannot say whether the
// plan put things where the document's author did. That question is
// answerable only by comparison — the plan's tree against the tree the source
// itself declares. Without it, the only way to notice that a paragraph moved
// two chapters up is to re-read both documents side by side.
type StructureDiff struct {
	// Moves are paragraphs whose section changed.
	Moves []Move `json:"moves"`
	// Sections are chapters added, dropped, or renamed.
	Sections []SectionChange `json:"sections"`
	// MovedSections counts chapters whose reading position changed.
	MovedSections []SectionMove `json:"moved_sections"`
	// Elements are paragraph→element bindings the baseline did not make.
	Elements []ElementChange `json:"elements"`
	// Meta lists model-authored 公文 elements with no source paragraph.
	Meta []MetaChange `json:"meta"`
	// BodyOrders are sections whose internal text order was changed.
	BodyOrders []BodyOrder `json:"body_orders"`
	// BaselineFlat reports that the source itself had no chapter structure:
	// the baseline is a single unnumbered bucket. Every paragraph then
	// "moves" when the model builds chapters, which is the intended outcome
	// rather than a suspicious relocation — the review screen groups those
	// moves instead of printing one card per paragraph.
	BaselineFlat bool `json:"baseline_flat,omitempty"`
}

// SectionMove records a chapter that appears in both trees but at a different
// place in the reading order.
type SectionMove struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Baseline int    `json:"baseline_pos"`
	Rendered int    `json:"rendered_pos"`
}

// Empty reports whether the plan's structure matches the source's.
func (d *StructureDiff) Empty() bool {
	return d == nil || (len(d.Moves) == 0 && len(d.Sections) == 0 &&
		len(d.MovedSections) == 0 && len(d.Elements) == 0 &&
		len(d.Meta) == 0 && len(d.BodyOrders) == 0)
}

// Compare reports how plan differs structurally from base.
//
// Both are plans over the same document, so every node id in one is meaningful
// in the other. Chapters are matched by their heading node — the paragraph the
// source itself provides — rather than by id or title: a model renumbering its
// own sections is not a structural change, and two chapters may legitimately
// share a title.
func Compare(doc *docmodel.Document, base, plan *docmodel.Plan) *StructureDiff {
	// Non-nil slices: a nil slice marshals to null, and the client would then
	// have to guard every access. The empty case is the common one — most
	// plans follow the source's structure — so it must travel as [].
	diff := &StructureDiff{
		Moves:         []Move{},
		Sections:      []SectionChange{},
		MovedSections: []SectionMove{},
		Elements:      []ElementChange{},
		Meta:          []MetaChange{},
		BodyOrders:    []BodyOrder{},
	}
	if base == nil || plan == nil || doc == nil {
		return diff
	}
	// A flat baseline is the "no headings at all" case: one synthetic bucket
	// holding every content node. Moves out of it are new structure, not
	// relocation, and the UI collapses them by destination.
	diff.BaselineFlat = len(base.Sections) == 1 &&
		base.Sections[0].HeadingNodeID == "" &&
		len(base.Sections[0].Children) == 0

	baseWhere := indexSections(doc, base)
	planWhere := indexSections(doc, plan)

	// --- paragraph moves --------------------------------------------------
	//
	// A flat baseline has no chapters to move *between*: every paragraph leaves
	// the single bucket because the model created structure, which is the
	// requested behaviour. It is still a change the reviewer has to see —
	// which chapter absorbed which paragraphs — so those moves are grouped by
	// destination instead of printed one per paragraph.
	if diff.BaselineFlat {
		diff.Moves = groupedMoves(doc, baseWhere, planWhere)
	} else {
		diff.Moves = paragraphMoves(doc, baseWhere, planWhere)
	}

	// --- chapters ---------------------------------------------------------
	for heading, baseSec := range baseWhere.byHeading {
		planSec, ok := planWhere.byHeading[heading]
		if !ok {
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "missing", ID: baseSec.id, Title: baseSec.title,
				HeadingNodeID: heading, BaselineLevel: baseSec.level,
			})
			continue
		}
		titleChanged := strings.TrimSpace(baseSec.title) != strings.TrimSpace(planSec.title)
		if titleChanged {
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "retitled", ID: planSec.id, Title: planSec.title,
				BaselineTitle: baseSec.title, HeadingNodeID: heading,
				Level: planSec.level, BaselineLevel: baseSec.level,
			})
		}
		// Same title, different depth: the printed 层次序数 changes (「二、」
		// becomes 「（二）」), which is just as visible as a rename and just as
		// likely to be unintended.
		//
		// Not when the heading carries its own 层次序数: the assembler lets that
		// number win over the plan's level, so the printed page is unchanged and
		// reporting it would be a change the user cannot see.
		numbered := parse.NumberingLevel(doc.Text(heading)) > 0
		levelChanged := baseSec.level != planSec.level && !numbered
		if levelChanged {
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "releveled", ID: planSec.id, Title: planSec.title,
				HeadingNodeID: heading, Level: planSec.level, BaselineLevel: baseSec.level,
				Parent:                      parentLabel(planSec),
				BaselineParent:              parentLabel(baseSec),
				ParentHeadingNodeID:         planSec.parentHeading,
				BaselineParentHeadingNodeID: baseSec.parentHeading,
			})
		}
		// A chapter can keep its title and its own 层次序数 while moving to a
		// different parent. That is invisible to the title and level checks,
		// and it is exactly what happened when a “（一）…” child is lifted to
		// the root: the printed numbering may stay the same, but its place in
		// the chapter tree does not.
		//
		// When the level itself changed, the releveled card already tells the
		// user where the chapter moved; adding a second card for the same
		// structural act would only duplicate it.
		if !levelChanged && !sameParent(baseSec, planSec) {
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "reparented", ID: planSec.id, Title: planSec.title,
				HeadingNodeID: heading, Level: planSec.level, BaselineLevel: baseSec.level,
				Parent:                      parentLabel(planSec),
				BaselineParent:              parentLabel(baseSec),
				ParentHeadingNodeID:         planSec.parentHeading,
				BaselineParentHeadingNodeID: baseSec.parentHeading,
			})
		}
	}
	for heading, planSec := range planWhere.byHeading {
		if _, ok := baseWhere.byHeading[heading]; !ok {
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "added", ID: planSec.id, Title: planSec.title,
				HeadingNodeID: heading, Level: planSec.level,
			})
		}
	}
	// A chapter with no source heading carries a title the model wrote. It
	// never appears in byHeading, so the loops above cannot see it — which is
	// exactly why the review list used to omit the most visible structural
	// change of all: a heading printed on the page that the source never had.
	plan.Walk(func(s *docmodel.Section) bool {
		if s.HeadingNodeID == "" {
			// A flat baseline already is one synthesized bucket titled after
			// the file. A plan that left it untouched should not be reported
			// as having invented that same title.
			if diff.BaselineFlat && len(plan.Sections) == 1 &&
				strings.TrimSpace(plan.Sections[0].Title) == strings.TrimSpace(base.Sections[0].Title) {
				return true
			}
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "synthesized", ID: s.ID, Title: s.Title, Level: s.Level,
			})
		}
		return true
	})

	// --- reading order ----------------------------------------------------
	//
	// Only chapters present in both are compared: a chapter the plan invented
	// has no baseline position to be "moved" from, and one it dropped was
	// reported above.
	for heading, baseSec := range baseWhere.byHeading {
		planSec, ok := planWhere.byHeading[heading]
		if !ok {
			continue
		}
		if baseSec.pos != planSec.pos {
			diff.MovedSections = append(diff.MovedSections, SectionMove{
				ID:       planSec.id,
				Title:    planSec.title,
				Baseline: baseSec.pos + 1,
				Rendered: planSec.pos + 1,
			})
		}
	}

	// --- element bindings -------------------------------------------------
	//
	// The baseline binds the opening 标题 and nothing else. Any other binding
	// moved a paragraph out of the body and into a 版头/版记 slot; the printed
	// page is where that becomes visible, so it must be listed here too.
	//
	// The reverse matters just as much: dropping the source 标题 binding turns
	// the title paragraph into body text, so the page gains a body paragraph
	// and loses its 标题. Reporting only new bindings used to hide exactly
	// that.
	baseElements := map[string]docmodel.ElementBinding{}
	for _, e := range base.Elements {
		if e.NodeID != "" {
			baseElements[e.NodeID+"\x00"+string(e.Role)] = e
		}
	}
	planElements := map[string]bool{}
	for _, e := range plan.Elements {
		if e.NodeID == "" {
			continue
		}
		key := e.NodeID + "\x00" + string(e.Role)
		planElements[key] = true
		if _, ok := baseElements[key]; ok {
			continue
		}
		// A node that moved straight from chapter body into an element slot is
		// already listed in Moves as "改作公文要素". Listing it again here
		// would double-count one change and show the user two cards for it.
		// This applies to every role, including 标题: binding a body paragraph
		// as the 标题 is one act, not two.
		if movedTo(diff, e.NodeID) {
			continue
		}
		diff.Elements = append(diff.Elements, ElementChange{
			Kind: "bound", Role: string(e.Role), NodeID: e.NodeID,
			Text: excerpt(doc.Text(e.NodeID), 40),
		})
	}
	for key, e := range baseElements {
		if planElements[key] {
			continue
		}
		diff.Elements = append(diff.Elements, ElementChange{
			Kind: "unbound", Role: string(e.Role), NodeID: e.NodeID,
			Text: excerpt(doc.Text(e.NodeID), 40),
		})
	}

	// --- model-authored meta ----------------------------------------------
	diff.Meta = modelMeta(plan)

	// --- order within a chapter -------------------------------------------
	sourceOrder := map[string]int{}
	for _, n := range doc.Nodes {
		sourceOrder[n.ID] = n.Order
	}
	diff.BodyOrders = bodyOrders(plan, sourceOrder)

	sort.SliceStable(diff.Moves, func(i, j int) bool {
		return diff.Moves[i].NodeID < diff.Moves[j].NodeID
	})
	sort.SliceStable(diff.Sections, func(i, j int) bool {
		if diff.Sections[i].Kind != diff.Sections[j].Kind {
			return sectionRank(diff.Sections[i].Kind) < sectionRank(diff.Sections[j].Kind)
		}
		return diff.Sections[i].ID < diff.Sections[j].ID
	})
	sort.SliceStable(diff.MovedSections, func(i, j int) bool {
		return diff.MovedSections[i].Rendered < diff.MovedSections[j].Rendered
	})
	sort.SliceStable(diff.Elements, func(i, j int) bool {
		if diff.Elements[i].NodeID != diff.Elements[j].NodeID {
			return diff.Elements[i].NodeID < diff.Elements[j].NodeID
		}
		if diff.Elements[i].Kind != diff.Elements[j].Kind {
			return diff.Elements[i].Kind < diff.Elements[j].Kind
		}
		return diff.Elements[i].Role < diff.Elements[j].Role
	})
	sort.SliceStable(diff.Meta, func(i, j int) bool {
		return diff.Meta[i].Field < diff.Meta[j].Field
	})
	sort.SliceStable(diff.BodyOrders, func(i, j int) bool {
		if diff.BodyOrders[i].Kind != diff.BodyOrders[j].Kind {
			return diff.BodyOrders[i].Kind < diff.BodyOrders[j].Kind
		}
		return diff.BodyOrders[i].SectionID < diff.BodyOrders[j].SectionID
	})

	return diff
}

// modelMeta lists the 公文 element values the plan carries as text rather than
// as a bound paragraph. Only non-empty values produce anything on the page, so
// only those are changes.
func modelMeta(plan *docmodel.Plan) []MetaChange {
	out := []MetaChange{}
	if plan == nil {
		return out
	}
	// A bound element wins over the meta value in the assembler, so a value
	// that can never be printed must not be listed as a change.
	bound := map[docmodel.Role]bool{}
	for _, e := range plan.Elements {
		bound[e.Role] = true
	}
	add := func(role docmodel.Role, field, label, value string) {
		if bound[role] {
			return
		}
		if v := strings.TrimSpace(value); v != "" {
			out = append(out, MetaChange{Field: field, Label: label, Value: v})
		}
	}
	m := plan.Meta
	add(docmodel.RoleCopyNumber, "copy_number", "份号", m.CopyNumber)
	add(docmodel.RoleSecurity, "security", "密级", m.Security)
	add(docmodel.RoleUrgency, "urgency", "紧急程度", m.Urgency)
	add(docmodel.RoleMasthead, "issuing_org", "发文机关标志", m.IssuingOrg)
	add(docmodel.RoleDocNumber, "doc_number", "发文字号", m.DocNumber)
	add(docmodel.RoleSignatory, "signatory", "签发人", m.Signatory)
	add(docmodel.RoleTitle, "title", "标题", m.Title)
	add(docmodel.RoleRecipient, "recipient", "主送机关", m.Recipient)
	add(docmodel.RoleAttachmentNote, "attachment_note", "附件说明", m.AttachmentNote)
	add(docmodel.RoleSignature, "signature", "发文机关署名", m.Signature)
	add(docmodel.RoleDate, "date", "成文日期", m.Date)
	add(docmodel.RoleNote, "note", "附注", m.Note)
	add(docmodel.RoleCC, "cc", "抄送机关", m.CC)
	add(docmodel.RolePrinter, "printer", "印发机关", m.Printer)
	// 印发日期 has no dedicated role; it is printed together with 印发机关,
	// so it is only suppressed when that slot is bound.
	if v := strings.TrimSpace(m.PrintDate); v != "" && !bound[docmodel.RolePrinter] {
		out = append(out, MetaChange{Field: "print_date", Label: "印发日期", Value: v})
	}
	return out
}

// movedTo reports whether a paragraph→element change is already in Moves.
func movedTo(diff *StructureDiff, nodeID string) bool {
	for _, m := range diff.Moves {
		if m.NodeID == nodeID && m.Kind == MoveToElement {
			return true
		}
	}
	return false
}

// bodyOrders lists sections whose body sequence differs from the source's.
//
// This mirrors VerifyPlan's per-section check but reports it as a structural
// change rather than a diagnostic: the plan is still renderable, and the
// reviewer may well have asked for it.
func bodyOrders(plan *docmodel.Plan, order map[string]int) []BodyOrder {
	out := []BodyOrder{}
	if plan == nil {
		return out
	}
	plan.Walk(func(s *docmodel.Section) bool {
		if !outOfOrder(s.BodyNodeIDs, s.BodyParts, order) {
			return true
		}
		out = append(out, BodyOrder{
			Kind: "section", SectionID: s.ID, Title: s.Title,
			NodeIDs: append([]string{}, s.BodyNodeIDs...), HasParts: len(s.BodyParts) > 0,
		})
		return true
	})
	if outOfOrder(plan.PreambleNodeIDs, nil, order) {
		out = append(out, BodyOrder{
			Kind: "preamble", Title: "正文前言",
			NodeIDs: append([]string{}, plan.PreambleNodeIDs...),
		})
	}
	return out
}

// outOfOrder reports whether the listed nodes are not ascending in source
// order. Unknown ids are ignored, matching VerifyPlan.
func outOfOrder(ids []string, parts []docmodel.BodyPart, order map[string]int) bool {
	prev := -1
	check := func(id string) bool {
		o, ok := order[id]
		if !ok {
			return true
		}
		if prev >= 0 && o < prev {
			return false
		}
		prev = o
		return true
	}
	for _, id := range ids {
		if !check(id) {
			return true
		}
	}
	for _, bp := range parts {
		if !check(bp.NodeID) {
			return true
		}
	}
	return false
}

// sectionRank orders chapter changes by how much they need attention:
// a dropped chapter first, then a renamed or re-levelled one, then invented
// chapters, and finally the synthesized titles that are normal for an
// unstructured upload.
func sectionRank(kind string) int {
	switch kind {
	case "missing":
		return 0
	case "retitled":
		return 1
	case "releveled":
		return 2
	case "reparented":
		return 3
	case "added":
		return 4
	case "synthesized":
		return 5
	default:
		return 6
	}
}

// paragraphMoves lists the nodes whose chapter changed between base and plan.
func paragraphMoves(doc *docmodel.Document, baseWhere, planWhere *index) []Move {
	var out []Move
	for _, n := range doc.ContentNodes() {
		from, inBase := baseWhere.node[n.ID]
		to, inPlan := planWhere.node[n.ID]
		if !inBase && !inPlan {
			continue
		}
		// Only compare nodes both trees claim as chapter body or preamble.
		// An element binding is a different kind of claim and is reported
		// separately; a node one side claims and the other does not is a
		// coverage difference VerifyPlan already reports.
		if !inBase || !inPlan {
			continue
		}
		// Leaving an element slot is reported by the element pass as
		// "unbound", which says exactly which slot was lost. The generic
		// "changed chapter" wording would be misleading there.
		if from.kind == "element" && to.kind != "element" {
			continue
		}
		if samePlace(from, to) {
			continue
		}
		out = append(out, Move{
			NodeID: n.ID,
			Text:   excerpt(n.Text, 40),
			Kind:   moveKind(from.kind, to.kind),
			From:   from.title,
			FromID: from.id,
			To:     to.title,
			ToID:   to.id,
		})
	}
	return out
}

// groupedMoves lists the same information for a flat source, one entry per
// destination chapter instead of one per paragraph.
//
// A document with no headings has exactly one bucket in the baseline, so
// "which chapter does this paragraph belong to" is not a question the source
// answers. The model answers it by creating chapters, and the review screen
// still has to show which paragraphs each new chapter absorbed — otherwise
// the reader cannot check the split without comparing the upload and the
// output line by line. Grouping keeps that auditable without printing one
// card per paragraph.
func groupedMoves(doc *docmodel.Document, baseWhere, planWhere *index) []Move {
	var (
		out   []Move
		index = map[string]int{}
	)
	for _, n := range doc.ContentNodes() {
		from, inBase := baseWhere.node[n.ID]
		to, inPlan := planWhere.node[n.ID]
		if !inBase || !inPlan || samePlace(from, to) {
			continue
		}
		key := to.kind + "\x00" + to.id + "\x00" + to.title
		if i, ok := index[key]; ok {
			out[i].Count++
			continue
		}
		index[key] = len(out)
		out = append(out, Move{
			Kind: moveKind(from.kind, to.kind),
			// The flat bucket is a synthetic chapter, not something the author
			// wrote; naming it as the source would suggest the upload had one.
			From: "原文无分章", FromID: from.id,
			To: to.title, ToID: to.id,
			Count: 1,
		})
	}
	return out
}

// samePlace reports whether two locations are the same chapter/preamble/element
// slot.
//
// Section ids are not comparable across plans: the baseline numbers its own
// chapters `b1…`, the model numbers `s1…`. Comparing ids reported every
// paragraph of a faithfully copied document as "moved", which buried the real
// changes. Chapters are therefore identified by the source heading they are
// built from — the one thing both plans can agree on.
func samePlace(from, to where) bool {
	if from.kind != to.kind {
		return false
	}
	switch from.kind {
	case "section":
		if from.heading != "" || to.heading != "" {
			return from.heading == to.heading
		}
		// Two synthesized chapters have no shared anchor; their titles are the
		// only evidence, and treating a rename as a move would double-report it
		// (the rename is already a SectionChange).
		return strings.TrimSpace(from.title) == strings.TrimSpace(to.title)
	case "preamble":
		return true
	default: // element
		return from.title == to.title
	}
}

// where is where one node or chapter sits in a plan.
type where struct {
	// kind is "section", "preamble" or "element".
	kind    string
	id      string // section id, "" for preamble
	title   string
	heading string
	level   int
	pos     int // preorder index of the owning section, -1 for preamble
	// parentHeading identifies the owning chapter by the source heading it is
	// built from. That is the only stable cross-plan identity: baseline ids
	// are b1…, model ids are s1….
	parentHeading string
	parentID      string
	parentTitle   string
}

// index is a document-wide view of one plan's structure.
type index struct {
	node      map[string]where
	byHeading map[string]where
}

// indexSections walks a plan once, recording where each node sits.
//
// The walk is preorder, identical to the order the assembler emits, so `pos`
// is the chapter's reading position rather than its JSON depth.
func indexSections(doc *docmodel.Document, plan *docmodel.Plan) *index {
	ix := &index{
		node:      make(map[string]where, len(doc.Nodes)),
		byHeading: make(map[string]where, 8),
	}
	pos := 0
	var walk func([]docmodel.Section, *where)
	walk = func(list []docmodel.Section, parent *where) {
		for i := range list {
			s := &list[i]
			w := where{
				kind: "section", id: s.ID, title: sectionLabel(doc, s),
				heading: s.HeadingNodeID, level: s.Level, pos: pos,
			}
			pos++
			if parent != nil {
				w.parentHeading = parent.heading
				w.parentID = parent.id
				w.parentTitle = parent.title
			}
			if s.HeadingNodeID != "" {
				ix.byHeading[s.HeadingNodeID] = w
				ix.node[s.HeadingNodeID] = w
			}
			for _, id := range s.BodyNodeIDs {
				ix.node[id] = w
			}
			for _, bp := range s.BodyParts {
				ix.node[bp.NodeID] = w
			}
			walk(s.Children, &w)
		}
	}
	walk(plan.Sections, nil)
	for _, id := range plan.PreambleNodeIDs {
		ix.node[id] = where{kind: "preamble", title: "前言", level: -1, pos: -1}
	}
	for _, e := range plan.Elements {
		if e.NodeID == "" {
			continue
		}
		// Element bindings win over a section claim, matching the
		// assembler's precedence: a bound node is placed in the 版头/版记
		// slot, not in the body.
		ix.node[e.NodeID] = where{
			kind: "element", title: "要素：" + elementLabel(e.Role), level: -1, pos: -1,
		}
	}
	return ix
}

// sameParent reports whether two chapters hang under the same parent.
//
// Parent section ids cannot be compared across plans (b… versus s…), so the
// source heading of the parent is the anchor. A synthesized parent has no
// heading; its title is then the only available evidence.
func sameParent(from, to where) bool {
	if from.parentHeading != "" || to.parentHeading != "" {
		return from.parentHeading == to.parentHeading
	}
	return strings.TrimSpace(from.parentTitle) == strings.TrimSpace(to.parentTitle)
}

// parentLabel names a chapter's parent for the review screen. The empty title
// is the root of the document, where chapters are printed directly under the
// body — not a missing value.
func parentLabel(w where) string {
	if t := strings.TrimSpace(w.parentTitle); t != "" {
		return t
	}
	return "顶层"
}

// sectionLabel names a chapter for the diff: its title, or its id when the
// title is empty.
func sectionLabel(doc *docmodel.Document, s *docmodel.Section) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	if s.HeadingNodeID != "" {
		if t := strings.TrimSpace(doc.Text(s.HeadingNodeID)); t != "" {
			return t
		}
	}
	return s.ID
}

// elementLabel renders a role in Chinese for the review screen. Unknown roles
// fall back to the machine name, which is what the wire value is anyway.
func elementLabel(r docmodel.Role) string {
	if label, ok := roleNames[r]; ok {
		return label
	}
	return string(r)
}

// roleNames mirrors the vocabulary VerifyPlan uses in its own diagnostics, so
// the review screen says 「发文字号」 in both places.
var roleNames = map[docmodel.Role]string{
	docmodel.RoleCopyNumber:     "份号",
	docmodel.RoleSecurity:       "密级",
	docmodel.RoleUrgency:        "紧急程度",
	docmodel.RoleMasthead:       "发文机关标志",
	docmodel.RoleDocNumber:      "发文字号",
	docmodel.RoleSignatory:      "签发人",
	docmodel.RoleTitle:          "标题",
	docmodel.RoleRecipient:      "主送机关",
	docmodel.RoleAttachmentNote: "附件说明",
	docmodel.RoleSignature:      "发文机关署名",
	docmodel.RoleDate:           "成文日期",
	docmodel.RoleNote:           "附注",
	docmodel.RoleCC:             "抄送机关",
	docmodel.RolePrinter:        "印发机关",
}

// moveKind classifies a transition between two locations.
func moveKind(from, to string) MoveKind {
	switch {
	case from == "section" && to == "preamble":
		return MoveToPreamble
	case from == "preamble" && to == "section":
		return MoveFromPreamble
	case to == "element":
		return MoveToElement
	default:
		return MoveSection
	}
}

// excerpt shortens text for a diff entry without cutting a character in half.
func excerpt(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// Summary renders a one-line description, used in reports and logs.
func (d *StructureDiff) Summary() string {
	if d.Empty() {
		return "结构与原文一致"
	}
	return fmt.Sprintf(
		"结构有调整：%d 段换了归属，%d 个章节增删改名，%d 个章节换了位置，"+
			"%d 处要素绑定，%d 个自拟要素，%d 处正文顺序",
		len(d.Moves), len(d.Sections), len(d.MovedSections),
		len(d.Elements), len(d.Meta), len(d.BodyOrders))
}
