package baseline

import (
	"fmt"
	"strings"

	"govdoc/internal/docmodel"
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
}

// SectionChange is one chapter that the plan added, removed, or retitled
// relative to the source's own structure.
type SectionChange struct {
	Kind string `json:"kind"` // "added" | "missing" | "retitled"
	// ID is the plan's section id, or the baseline's for "missing".
	ID string `json:"id"`
	// Title is the plan's title for added/retitled, the baseline's for
	// missing.
	Title string `json:"title"`
	// BaselineTitle is the source's own title, for "retitled" and "added".
	BaselineTitle string `json:"baseline_title,omitempty"`
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
	return d == nil || (len(d.Moves) == 0 && len(d.Sections) == 0 && len(d.MovedSections) == 0)
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
	}
	if base == nil || plan == nil || doc == nil {
		return diff
	}

	baseWhere := indexSections(doc, base)
	planWhere := indexSections(doc, plan)

	// --- paragraph moves --------------------------------------------------
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
		if from.kind == to.kind && from.id == to.id {
			continue
		}
		diff.Moves = append(diff.Moves, Move{
			NodeID: n.ID,
			Text:   excerpt(n.Text, 40),
			Kind:   moveKind(from.kind, to.kind),
			From:   from.title,
			FromID: from.id,
			To:     to.title,
			ToID:   to.id,
		})
	}

	// --- chapters ---------------------------------------------------------
	for heading, baseSec := range baseWhere.byHeading {
		planSec, ok := planWhere.byHeading[heading]
		if !ok {
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "missing", ID: baseSec.id, Title: baseSec.title,
			})
			continue
		}
		if strings.TrimSpace(baseSec.title) != strings.TrimSpace(planSec.title) {
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "retitled", ID: planSec.id, Title: planSec.title,
				BaselineTitle: baseSec.title,
			})
		}
	}
	for heading, planSec := range planWhere.byHeading {
		if _, ok := baseWhere.byHeading[heading]; !ok {
			diff.Sections = append(diff.Sections, SectionChange{
				Kind: "added", ID: planSec.id, Title: planSec.title,
			})
		}
	}

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

	return diff
}

// where is where one node or chapter sits in a plan.
type where struct {
	// kind is "section", "preamble" or "element".
	kind  string
	id    string // section id, "" for preamble
	title string
	pos   int // preorder index of the owning section, -1 for preamble
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
	plan.Walk(func(s *docmodel.Section) bool {
		w := where{kind: "section", id: s.ID, title: sectionLabel(doc, s), pos: pos}
		pos++
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
		return true
	})
	for _, id := range plan.PreambleNodeIDs {
		ix.node[id] = where{kind: "preamble", title: "前言", pos: -1}
	}
	for _, e := range plan.Elements {
		if e.NodeID == "" {
			continue
		}
		// Element bindings win over a section claim, matching the
		// assembler's precedence: a bound node is placed in the 版头/版记
		// slot, not in the body.
		ix.node[e.NodeID] = where{
			kind: "element", title: "要素：" + elementLabel(e.Role), pos: -1,
		}
	}
	return ix
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
	return fmt.Sprintf("结构有调整：%d 段换了归属，%d 个章节增删改名，%d 个章节换了位置",
		len(d.Moves), len(d.Sections), len(d.MovedSections))
}
