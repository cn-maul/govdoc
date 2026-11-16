package docmodel

import (
	"fmt"
	"sort"
	"strings"

	"govdoc/internal/style"
)

// ---------------------------------------------------------------- diagnostics

// Severity classifies a diagnostic.
type Severity string

const (
	// SevError means the plan is not renderable: it loses, duplicates or
	// invents content, or it is structurally malformed.
	SevError Severity = "error"
	// SevWarning means the plan is renderable but a reviewer should look:
	// text was reordered, a heading was synthesized, boilerplate was
	// dropped deliberately.
	SevWarning Severity = "warning"
	// SevInfo is provenance information, never a problem.
	SevInfo Severity = "info"
)

// Diagnostic is one verification finding. Code is a stable machine-readable
// identifier so the UI and tests can match findings without parsing prose.
type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	// NodeIDs names the content involved, when applicable.
	NodeIDs []string `json:"node_ids,omitempty"`
}

// Report is the outcome of VerifyPlan.
type Report struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
	// ContentNodes is how many prose-carrying nodes the source had.
	ContentNodes int `json:"content_nodes"`
	// CoveredNodes is how many of them the plan accounts for.
	CoveredNodes int `json:"covered_nodes"`
	// DuplicatedNodes is how many were referenced more than once.
	DuplicatedNodes int `json:"duplicated_nodes"`
	// UnknownRefs is how many references point at ids the source lacks.
	UnknownRefs int `json:"unknown_refs"`
	// ReorderedSections counts sections whose body order differs from the
	// source order.
	ReorderedSections int `json:"reordered_sections"`
	// SynthesizedHeadings counts headings the plan supplied itself rather
	// than pointing at existing source text.
	SynthesizedHeadings int `json:"synthesized_headings"`
	// BoundElements counts 公文 element slots bound to source nodes.
	BoundElements int `json:"bound_elements"`
	// HeadingsAsBody counts source heading paragraphs claimed as section body,
	// which makes them render as a second heading of that level.
	HeadingsAsBody int `json:"headings_as_body"`
	// SectionsOutOfOrder counts sections whose content sits before the
	// content of the section emitted above them. Such a plan renders, but the
	// printed order differs from the source's — and when the source headings
	// carry their own 层次序数, the page shows "二、" before "一、".
	SectionsOutOfOrder int `json:"sections_out_of_order"`
	// RecomposedNodes counts source paragraphs the plan split into several
	// paragraphs.
	RecomposedNodes int `json:"recomposed_nodes"`
	// RecomposedParts counts the declared output paragraphs produced by those
	// splits.
	RecomposedParts int `json:"recomposed_parts"`
	// HeavyRecompose counts recomposed paragraphs whose output is much shorter
	// than the source, i.e. cases where the model summarised instead of
	// reorganising. Exportable, but surfaced for review.
	HeavyRecompose int `json:"heavy_recompose"`
}

// OK reports whether the plan is renderable: no error-severity diagnostics
// and every content node accounted for exactly once.
func (r *Report) OK() bool {
	if r == nil {
		return false
	}
	if r.DuplicatedNodes > 0 || r.UnknownRefs > 0 {
		return false
	}
	if r.CoveredNodes != r.ContentNodes {
		return false
	}
	for _, d := range r.Diagnostics {
		if d.Severity == SevError {
			return false
		}
	}
	return true
}

// Errors returns only the error-severity diagnostics.
func (r *Report) Errors() []Diagnostic {
	var out []Diagnostic
	for _, d := range r.Diagnostics {
		if d.Severity == SevError {
			out = append(out, d)
		}
	}
	return out
}

// Summary renders a one-line human summary.
func (r *Report) Summary() string {
	if r == nil {
		return "未校验"
	}
	status := "通过"
	if !r.OK() {
		status = "未通过"
	}
	return fmt.Sprintf("%s：正文节点 %d/%d 已覆盖，重复 %d，无效引用 %d",
		status, r.CoveredNodes, r.ContentNodes, r.DuplicatedNodes, r.UnknownRefs)
}

// validateRecompose checks the shape of Plan.Recompose and returns it indexed
// by source node.
//
// A declaration is accepted only when every part is non-empty, the node exists
// in the source, and the declaration appears once. These are pure shape checks;
// whether the parts actually reproduce the source is decided by
// VerifyVerbatim, which has the assembled text to compare.
func validateRecompose(rep *Report, doc *Document, plan *Plan, contentSet map[string]bool) map[string]Recompose {
	out := map[string]Recompose{}
	if plan == nil {
		return out
	}
	recipientBound := false
	for _, e := range plan.Elements {
		if e.Role == RoleRecipient && e.NodeID != "" {
			recipientBound = true
			break
		}
	}
	recipientOwner := ""
	for i, rec := range plan.Recompose {
		if rec.NodeID == "" {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "bad_recompose", Severity: SevError,
				Message: fmt.Sprintf("重组声明 #%d 缺少 node_id", i+1),
			})
			continue
		}
		if _, ok := doc.Node(rec.NodeID); !ok {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "unknown_ref", Severity: SevError,
				Message: fmt.Sprintf("重组声明引用了原文中不存在的节点 %q", rec.NodeID),
				NodeIDs: []string{rec.NodeID},
			})
			continue
		}
		if !contentSet[rec.NodeID] {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "bad_recompose", Severity: SevError,
				Message: fmt.Sprintf("节点 %s 不携带正文，不能声明重组", rec.NodeID),
				NodeIDs: []string{rec.NodeID},
			})
			continue
		}
		n, ok := doc.Node(rec.NodeID)
		if ok && (n.Kind == KindTable || n.Kind == KindImage) {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "bad_recompose", Severity: SevError,
				Message: fmt.Sprintf("节点 %s 是表格或图片，不能作为文字段落重组", rec.NodeID),
				NodeIDs: []string{rec.NodeID},
			})
			continue
		}
		if _, dup := out[rec.NodeID]; dup {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "duplicate_recompose", Severity: SevError,
				Message: fmt.Sprintf("节点 %s 有多条重组声明，无法确定以哪一条为准", rec.NodeID),
				NodeIDs: []string{rec.NodeID},
			})
			continue
		}
		if len(rec.Parts) == 0 {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "bad_recompose", Severity: SevError,
				Message: fmt.Sprintf("节点 %s 的重组声明没有任何段落", rec.NodeID),
				NodeIDs: []string{rec.NodeID},
			})
			continue
		}
		if len(plan.EditsFor(rec.NodeID)) > 0 {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "recompose_conflict_edits", Severity: SevError,
				Message: fmt.Sprintf(
					"节点 %s 同时声明了重组和逐字修改；重组后的文字请直接写进 parts，不要再给同一节点加 edits",
					rec.NodeID),
				NodeIDs: []string{rec.NodeID},
			})
			continue
		}
		if len(rec.Roles) > 0 && len(rec.Roles) != len(rec.Parts) {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "bad_recompose", Severity: SevError,
				Message: fmt.Sprintf(
					"节点 %s 的 roles 有 %d 项，与 parts 的 %d 段不一致",
					rec.NodeID, len(rec.Roles), len(rec.Parts)),
				NodeIDs: []string{rec.NodeID},
			})
			continue
		}
		badRole := false
		// Role parts are emitted in a fixed skeleton order: 主送机关, then
		// 正文前言, then the section tree. VerifyVerbatim compares the
		// emitted parts against rec.Parts in order, so the role parts must
		// be a prefix of Parts and recipient must come first. Stating the
		// rule here turns a later "recompose order" mismatch into an error
		// that names the offending part.
		bodySeen := false
		for j, role := range rec.Roles {
			if role == "" {
				bodySeen = true
				continue
			}
			if bodySeen {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "bad_recompose", Severity: SevError,
					Message: fmt.Sprintf(
						"节点 %s 的第 %d 段带 role，却排在普通正文之后；"+
							"主送机关和正文前言必须是 parts 开头连续的一段",
						rec.NodeID, j),
					NodeIDs: []string{rec.NodeID},
				})
				badRole = true
			}
			switch role {
			case RecomposeRoleRecipient:
				if j != 0 {
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "bad_recompose", Severity: SevError,
						Message: fmt.Sprintf(
							"节点 %s 的主送机关分片必须是第 0 段（实际在第 %d 段）；"+
								"否则它会排到正文前言之后",
							rec.NodeID, j),
						NodeIDs: []string{rec.NodeID},
					})
					badRole = true
				}
				if recipientOwner != "" {
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "bad_recompose", Severity: SevError,
						Message: fmt.Sprintf(
							"节点 %s 和 %s 都声明了主送机关分片；主送机关只能有一个",
							recipientOwner, rec.NodeID),
						NodeIDs: []string{recipientOwner, rec.NodeID},
					})
					badRole = true
				}
				if recipientBound {
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "bad_recompose", Severity: SevError,
						Message: fmt.Sprintf(
							"节点 %s 用 role=recipient 拆出了主送机关，但 elements 里又绑定了另一个主送机关节点；"+
								"二者只能保留一个",
							rec.NodeID),
						NodeIDs: []string{rec.NodeID},
					})
					badRole = true
				}
				recipientOwner = rec.NodeID
			case RecomposeRolePreamble:
				// preamble may be the first part (no recipient line) or the
				// part right after recipient. A second recipient is rejected
				// by the coverage tally.
			case string(RoleBody):
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "bad_recompose", Severity: SevError,
					Message: fmt.Sprintf(
						"节点 %s 重组后的第 %d 段绑定为 body；正文分片应留空 role，并放进章节的 body_parts",
						rec.NodeID, j),
					NodeIDs: []string{rec.NodeID},
				})
				badRole = true
			default:
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "bad_recompose", Severity: SevError,
					Message: fmt.Sprintf(
						"节点 %s 重组后的第 %d 段使用了不支持的 role %q；"+
							"可用 recipient（主送机关）和 preamble（正文前言），普通正文留空",
						rec.NodeID, j, role),
					NodeIDs: []string{rec.NodeID},
				})
				badRole = true
			}
		}
		if badRole {
			continue
		}
		if ok {
			if srcLen, outLen := runeLen(n.Text), recomposeLen(rec.Parts); srcLen > 0 && outLen*10 < srcLen*6 {
				rep.HeavyRecompose++
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "heavy_recompose", Severity: SevWarning,
					Message: fmt.Sprintf(
						"节点 %s 重组后只有原文的 %d%%（原文 %d 字 → 重组 %d 字）："+
							"这更接近概括而非整理，请确认没有删掉原文信息",
						rec.NodeID, outLen*100/srcLen, srcLen, outLen),
					NodeIDs: []string{rec.NodeID},
				})
			}
		}
		bad := false
		for j, part := range rec.Parts {
			if strings.TrimSpace(part) == "" {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "bad_recompose", Severity: SevError,
					Message: fmt.Sprintf("节点 %s 重组后的第 %d 段为空", rec.NodeID, j),
					NodeIDs: []string{rec.NodeID},
				})
				bad = true
			}
		}
		if bad {
			continue
		}
		out[rec.NodeID] = rec
	}
	return out
}

// BindableRoles lists the 公文 element slots a node may be bound to through
// Plan.Elements: exactly the slots the assembler places in the 版头/主体/版记
// skeleton by looking up a bound node.
//
// The list is closed on purpose. If an unknown role were accepted, VerifyPlan
// would count the node as claimed — so the coverage check would pass — while
// the assembler, which looks the role up by its canonical constant, would
// never emit it. The paragraph would vanish from the output with every check
// reporting success. Rejecting unknown roles here turns that silent data loss
// into a visible error.
func BindableRoles() []Role {
	return []Role{
		RoleCopyNumber, RoleSecurity, RoleUrgency, RoleMasthead,
		RoleDocNumber, RoleSignatory, RoleTitle, RoleRecipient,
		RoleAttachmentNote, RoleSignature, RoleDate, RoleNote,
		RoleCC, RolePrinter,
	}
}

// bindableSet is the lookup form of BindableRoles.
var bindableSet = func() map[Role]bool {
	out := make(map[Role]bool, len(BindableRoles()))
	for _, r := range BindableRoles() {
		out[r] = true
	}
	return out
}()

// ---------------------------------------------------------------- verification

// VerifyPlan checks a plan against its source document and decides whether it
// may be rendered. It is the single enforcement point of the project's
// central promise — 梳理结构但不改变原文 — so it is deliberately strict and
// purely structural: it never inspects "meaning", only identity and coverage.
//
// The rules, in the order they are reported:
//
//  1. unknown_ref  — a plan references a node id the document does not have.
//  2. duplicate    — a content node is claimed twice (as a heading and/or as
//     body in more than one place). Copying content is how a
//     "restructure" silently becomes a "rewrite", so this is
//     an error, not a warning.
//  3. uncovered    — a content node is claimed nowhere, i.e. content was
//     dropped. This is the failure mode the user fears most,
//     so it is an error.
//  4. heading_mismatch — a section points HeadingNodeID at a node but
//     supplies a different Title. Either it should have used
//     the node's text verbatim, or it should not have claimed
//     the node at all.
//  5. bad_level    — a child section is not deeper than its parent.
//  6. reordered    — body nodes within a section are out of source order and
//     PreserveOrder is unset. Renderable, but surfaced.
//  7. synthesized  — a heading with no HeadingNodeID. Renderable, but the
//     user should know the title is model-authored.
func VerifyPlan(doc *Document, plan *Plan) *Report {
	rep := &Report{}
	if plan == nil {
		rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
			Code: "no_plan", Severity: SevError, Message: "未生成结构方案",
		})
		return rep
	}

	byID := doc.ByID()
	contentIDs := doc.ContentIDs()
	contentSet := make(map[string]bool, len(contentIDs))
	for _, id := range contentIDs {
		contentSet[id] = true
	}
	rep.ContentNodes = len(contentIDs)

	// The profile may allow fewer levels than the style table defines (a 报告
	// with two levels, say). A deeper chapter would be numbered, then rendered
	// with a style that does not exist. The plan carries the limit so this check
	// uses the same value the prompt was given.
	maxDepth := plan.MaxHeadingDepth
	if maxDepth <= 0 || maxDepth > MaxHeadingDepth {
		maxDepth = MaxHeadingDepth
	}

	// Recompose declarations define which nodes are allowed to appear as
	// several output paragraphs. They are validated before the claim tally so
	// the coverage rules can treat a recomposed node as a set of parts rather
	// than as one indivisible paragraph.
	recomposed := validateRecompose(rep, doc, plan, contentSet)
	rep.RecomposedNodes = len(recomposed)
	for _, r := range plan.Recompose {
		rep.RecomposedParts += len(r.Parts)
	}

	// order gives each id its source position, for the reorder check.
	order := make(map[string]int, len(doc.Nodes))
	if doc != nil {
		for _, n := range doc.Nodes {
			order[n.ID] = n.Order
		}
	}

	// claims counts how many times each prose-carrying node is claimed.
	claims := make(map[string]int, len(contentIDs))
	claimedAs := make(map[string][]string, len(contentIDs))

	claim := func(id, where string) {
		if id == "" {
			return
		}
		if _, ok := byID[id]; !ok {
			// Reported as a separate, aggregated diagnostic below; but a
			// reference to a node that does not exist must never inflate
			// the coverage count, so it is not recorded as a claim.
			return
		}
		if !contentSet[id] {
			// A non-prose node (page break) referenced as content: it is
			// neither coverage nor a duplicate, but it is a modelling
			// mistake worth flagging once.
			return
		}
		claims[id]++
		claimedAs[id] = append(claimedAs[id], where)
	}

	// partClaims counts claims per (node, part). A whole-node claim sets the
	// per-node count instead; the two forms are mutually exclusive.
	partClaims := map[string][]int{}
	claimPart := func(nodeID string, part int, where string) {
		if _, ok := recomposed[nodeID]; !ok {
			return
		}
		if part < 0 || part >= len(recomposed[nodeID].Parts) {
			return
		}
		claims[nodeID]++
		claimedAs[nodeID] = append(claimedAs[nodeID], where)
		list := partClaims[nodeID]
		for len(list) < len(recomposed[nodeID].Parts) {
			list = append(list, 0)
		}
		list[part]++
		partClaims[nodeID] = list
	}

	// Role parts are claimed by the recompose declaration itself, not by a
	// section. They are emitted in the skeleton (主送机关, then 前言), so a
	// body_parts entry for the same part would print it twice; claimPart's
	// per-part tally is therefore enough to detect that, while this loop
	// makes the skeleton placement count as coverage. A non-empty role must
	// be an element slot (recipient) or the preamble slot, never body.
	for _, rec := range plan.Recompose {
		for j, role := range rec.Roles {
			switch role {
			case "":
			case RecomposeRoleRecipient, RecomposeRolePreamble:
				where := "前言"
				if role == RecomposeRoleRecipient {
					where = "要素:recipient"
				}
				claimPart(rec.NodeID, j, where)
			}
		}
	}

	var unknown []string
	seenUnknown := map[string]bool{}
	noteUnknown := func(id string) {
		if id == "" || seenUnknown[id] {
			return
		}
		if _, ok := byID[id]; !ok {
			seenUnknown[id] = true
			unknown = append(unknown, id)
		}
	}

	// Element bindings are claimed before the tree walk, because a bound
	// node is by definition not body content: claiming it here is what stops
	// the source's own 标题/成文日期 paragraphs from being reported as
	// dropped content.
	//
	// Only the FIRST binding of each role is counted as a claim. A later
	// duplicate is an error (reported below as duplicate_element), and the
	// assembler emits only the first one; counting both would additionally
	// report the same paragraph as duplicate_content, making a single
	// mistake look like a lost-and-duplicated document.
	rep.BoundElements = len(plan.Elements)
	firstRole := map[Role]bool{}
	for i, e := range plan.Elements {
		if e.NodeID == "" {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "bad_element", Severity: SevError,
				Message: fmt.Sprintf("要素绑定 #%d（%s）缺少 node_id", i+1, e.Role),
			})
			continue
		}
		noteUnknown(e.NodeID)
		if rec, ok := recomposed[e.NodeID]; ok && len(rec.Parts) > 0 {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "recomposed_whole_claim", Severity: SevError,
				Message: fmt.Sprintf(
					"节点 %s 已声明重组为 %d 段，不能再整体绑定到要素 %s；"+
						"请把所需文字放进重组后的段落，或取消该重组声明",
					e.NodeID, len(rec.Parts), e.Role),
				NodeIDs: []string{e.NodeID},
			})
			continue
		}
		switch {
		case e.Role == RoleBody:
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "bad_element", Severity: SevError,
				Message: fmt.Sprintf("节点 %s 被绑定为正文（body）；正文内容必须通过章节的 body_node_ids 收录", e.NodeID),
				NodeIDs: []string{e.NodeID},
			})
			// A wrong slot must not count as coverage: the assembler would
			// never emit the node, so crediting it here would hide the loss.
			continue
		case !bindableSet[e.Role]:
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "bad_element", Severity: SevError,
				Message: fmt.Sprintf(
					"节点 %s 被绑定到未知要素 %q；可用要素：标题、主送机关、发文字号、发文机关标志、份号、密级、紧急程度、签发人、附件说明、发文机关署名、成文日期、附注、抄送机关、印发机关",
					e.NodeID, e.Role),
				NodeIDs: []string{e.NodeID},
			})
			continue
		}
		if firstRole[e.Role] {
			continue
		}
		firstRole[e.Role] = true
		claim(e.NodeID, fmt.Sprintf("要素:%s", e.Role))
	}
	seenElementRole := map[Role]string{}
	for _, e := range plan.Elements {
		if e.Role == "" || e.NodeID == "" {
			continue
		}
		if prev, dup := seenElementRole[e.Role]; dup {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "duplicate_element", Severity: SevError,
				Message: fmt.Sprintf("要素 %s 被绑定了多个节点（%s 与 %s）", e.Role, prev, e.NodeID),
				NodeIDs: []string{prev, e.NodeID},
			})
			continue
		}
		seenElementRole[e.Role] = e.NodeID
	}

	// Preamble content is claimed before the tree walk: it belongs under
	// 主送机关 rather than inside any chapter, so it must count as covered
	// without requiring a fabricated section for it.
	for _, id := range plan.PreambleNodeIDs {
		noteUnknown(id)
		if rec, ok := recomposed[id]; ok && len(rec.Parts) > 0 {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "recomposed_whole_claim", Severity: SevError,
				Message: fmt.Sprintf(
					"节点 %s 已声明重组为 %d 段，不能同时整体放进正文前言；"+
						"请改用章节的 body_parts 放置各段",
					id, len(rec.Parts)),
				NodeIDs: []string{id},
			})
			continue
		}
		claim(id, "前言")
	}

	// walk validates the tree and records every claim.
	//
	// Section ids are checked for uniqueness here, not just for presence. The
	// document renders correctly without unique ids — the assembler walks the
	// tree by position — but every client addresses a section BY ID (rename,
	// move, detach, relocate, React keys). Duplicate ids therefore do not
	// corrupt the .docx; they corrupt the review screen, where an edit lands on
	// a different chapter than the one the user clicked. That failure is silent
	// precisely because the output looks right, so it has to be refused where
	// every other structural guarantee is enforced.
	seenSectionIDs := make(map[string]bool)
	var walk func(list []Section, parentLevel int, path string) bool
	walk = func(list []Section, parentLevel int, path string) bool {
		ok := true
		for i := range list {
			s := &list[i]
			where := fmt.Sprintf("%s/%s", path, s.ID)
			if s.ID == "" {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "missing_section_id", Severity: SevError,
					Message: fmt.Sprintf("第 %d 个章节缺少 id（%s）", i+1, where),
				})
				ok = false
			} else if seenSectionIDs[s.ID] {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "duplicate_section_id", Severity: SevError,
					Message: fmt.Sprintf(
						"章节 id %q 重复（%s）：界面按 id 定位章节，"+
							"重复时修改标题、移动段落会落到错误的章节上",
						s.ID, where),
					NodeIDs: []string{s.ID},
				})
				ok = false
			}
			if s.ID != "" {
				seenSectionIDs[s.ID] = true
			}
			if s.Level < 1 {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "bad_level", Severity: SevError,
					Message: fmt.Sprintf("章节 %s 的层级 %d 非法（应为 1-%d）", s.ID, s.Level, maxDepth),
					NodeIDs: []string{s.ID},
				})
				ok = false
			}
			if s.Level > maxDepth {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "bad_level", Severity: SevError,
					Message: fmt.Sprintf(
						"章节 %s 的层级为 %d，超过这套体裁允许的 %d 级",
						s.ID, s.Level, maxDepth),
					NodeIDs: []string{s.ID},
				})
				ok = false
			}
			if parentLevel > 0 && s.Level <= parentLevel {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "bad_level", Severity: SevError,
					Message: fmt.Sprintf("章节 %s 的层级 %d 未深于父级 %d", s.ID, s.Level, parentLevel),
					NodeIDs: []string{s.ID},
				})
				ok = false
			}

			if s.HeadingNodeID != "" {
				noteUnknown(s.HeadingNodeID)
				if rec, ok := recomposed[s.HeadingNodeID]; ok && len(rec.Parts) > 0 {
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "recomposed_whole_claim", Severity: SevError,
						Message: fmt.Sprintf(
							"章节 %s 把重组节点 %s 当标题引用；重组段落只能通过 body_parts 放置",
							s.ID, s.HeadingNodeID),
						NodeIDs: []string{s.HeadingNodeID},
					})
					ok = false
				}
				claim(s.HeadingNodeID, "标题:"+s.ID)
				if n, found := doc.Node(s.HeadingNodeID); found {
					if strings.TrimSpace(s.Title) != strings.TrimSpace(n.Text) {
						rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
							Code: "heading_mismatch", Severity: SevError,
							Message: fmt.Sprintf(
								"章节 %s 的标题与原文不一致：原文 %q，方案 %q",
								s.ID, truncate(n.Text, 30), truncate(s.Title, 30)),
							NodeIDs: []string{s.HeadingNodeID},
						})
						ok = false
					}
				}
			} else {
				rep.SynthesizedHeadings++
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "synthesized_heading", Severity: SevWarning,
					Message: fmt.Sprintf("章节 %s 的标题 %q 由模型生成，原文中无对应标题节点",
						s.ID, truncate(s.Title, 30)),
				})
			}

			if len(s.BodyNodeIDs) == 0 && len(s.BodyParts) == 0 &&
				len(s.Children) == 0 && s.HeadingNodeID == "" {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "empty_section", Severity: SevWarning,
					Message: fmt.Sprintf("章节 %s 没有任何内容", s.ID),
				})
			}

			prev := -1
			ascending := true
			sectionOK := true
			for _, id := range s.BodyNodeIDs {
				noteUnknown(id)
				if rec, isRec := recomposed[id]; isRec && len(rec.Parts) > 0 {
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "recomposed_whole_claim", Severity: SevError,
						Message: fmt.Sprintf(
							"章节 %s 把重组节点 %s 整体放进 body_node_ids；"+
								"请用 body_parts 逐段放置，避免整段重复出现",
							s.ID, id),
						NodeIDs: []string{id},
					})
					sectionOK = false
					continue
				}
				claim(id, "正文:"+s.ID)
				if o, found := order[id]; found {
					if prev >= 0 && o < prev {
						ascending = false
					}
					prev = o
				}
				// A source heading listed as section *body* is rendered as a
				// heading by the assembler (its own level wins), which puts a
				// second heading of that level on the page. The likely cause
				// is a heading paragraph that should have been named by some
				// section's heading_node_id: the coverage tally looks perfect,
				// so nothing else fires, and the duplicate is visible only on
				// the printed page.
				if n, found := doc.Node(id); found && n.Kind == KindHeading {
					rep.HeadingsAsBody++
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "heading_as_body", Severity: SevWarning,
						Message: fmt.Sprintf(
							"章节 %s 把原文标题段 %s（%q）当作正文收录，成品中该标题会再次出现；"+
								"若它本就是章节标题，请改由某个章节的 heading_node_id 指向它",
							s.ID, id, truncate(n.Text, 30)),
						NodeIDs: []string{id},
					})
				}
			}
			for _, bp := range s.BodyParts {
				noteUnknown(bp.NodeID)
				rec, isRec := recomposed[bp.NodeID]
				if !isRec {
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "undeclared_body_part", Severity: SevError,
						Message: fmt.Sprintf(
							"章节 %s 引用了节点 %s 的第 %d 段，但方案没有为该节点声明重组",
							s.ID, bp.NodeID, bp.Part),
						NodeIDs: []string{bp.NodeID},
					})
					sectionOK = false
					continue
				}
				if _, found := rec.PartText(bp.Part); !found {
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "bad_body_part", Severity: SevError,
						Message: fmt.Sprintf(
							"章节 %s 引用了节点 %s 不存在的第 %d 段（该节点共 %d 段）",
							s.ID, bp.NodeID, bp.Part, len(rec.Parts)),
						NodeIDs: []string{bp.NodeID},
					})
					sectionOK = false
					continue
				}
				claimPart(bp.NodeID, bp.Part, "正文:"+s.ID)
				if o, found := order[bp.NodeID]; found {
					if prev >= 0 && o < prev {
						ascending = false
					}
					prev = o
				}
			}
			if !sectionOK {
				ok = false
			}
			if !ascending {
				rep.ReorderedSections++
				// A declared recomposition is allowed to move prose between
				// paragraphs; that is what the declaration is for. The
				// reorder is then information, not a warning.
				sev := SevWarning
				msg := fmt.Sprintf("章节 %s 的正文顺序与原文不一致（原文顺序被调整）", s.ID)
				if s.PreserveOrder || len(plan.Recompose) > 0 {
					// The model declared the reorder deliberately; still
					// visible, but as information rather than a warning.
					sev = SevInfo
					msg = fmt.Sprintf("章节 %s 的正文顺序已由重组声明调整", s.ID)
				} else {
					rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
						Code: "reordered", Severity: sev, Message: msg,
						NodeIDs: s.BodyNodeIDs,
					})
					if !walk(s.Children, s.Level, where) {
						ok = false
					}
					continue
				}
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "reordered", Severity: sev, Message: msg,
					NodeIDs: s.BodyNodeIDs,
				})
			}
			if !walk(s.Children, s.Level, where) {
				ok = false
			}
		}
		return ok
	}
	walk(plan.Sections, 0, "")

	// Whole-document order, across section boundaries.
	//
	// The per-section check inside walk() only compares body nodes *within* one
	// section, so a plan that swaps two entire chapters passes it: each
	// chapter's body is internally ascending. The printed document then shows
	// the second chapter first, and because a source heading carries its own
	// 层次序数 verbatim, the page reads "二、……" before "一、……". That is
	// visible to any reader, so it must not pass silently.
	rep.SectionsOutOfOrder = checkSectionOrder(rep, plan, order)
	checkPreambleOrder(rep, doc, plan, order)

	// A synthesized 标题 that duplicates a paragraph already covered as body
	// prints the title twice. This is the shape of a real mistake, found by
	// inspecting a generated document: an upload whose own first line is the
	// 标题, where the plan both leaves that paragraph in a section's body AND
	// fills Meta.Title, so the document opens with the same sentence twice.
	// Coverage is complete and no node is duplicated, so no other rule fires —
	// the defect is visible only on the printed page.
	//
	// It runs after walk() because it needs the final claim tally.
	if !hasElement(plan, RoleTitle) {
		if mt := strings.TrimSpace(plan.Meta.Title); mt != "" {
			if id := findNodeByExactText(doc, mt); id != "" && claims[id] > 0 {
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "duplicate_title", Severity: SevError,
					Message: fmt.Sprintf(
						"标题重复：原文段落 %s 的内容与 meta.title 相同，且该段落已被收录为正文，"+
							"成品中标题会出现两次。请改为把该段落绑定为标题（elements 中 role=title），"+
							"或清空 meta.title。原文：%q",
						id, truncate(mt, 40)),
					NodeIDs: []string{id},
				})
			}
		}
	}

	// The same shape, one level down: a *section* title the plan supplied
	// itself that happens to be the text of a paragraph already claimed as
	// body. The source paragraph had no heading markup (so the parser called
	// it 正文), the model noticed it reads like a heading and synthesized a
	// section for it — but left the paragraph in body_node_ids. The assembled
	// page prints that line twice, once as the synthesized heading and once as
	// body, and coverage is complete so nothing else fires.
	if dups := synthesizedTitleRepeats(doc, plan, claims); len(dups) > 0 {
		for _, d := range dups {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "duplicate_title", Severity: SevError,
				Message: fmt.Sprintf(
					"章节 %s 的标题 %q 与原文段落 %s 相同，而该段落已被收录为正文，"+
						"成品中这一行会出现两次。请让该章节的 heading_node_id 指向 %s（标题即取该段原文），"+
						"或把该段落从 body_node_ids 中移出",
					d.sectionID, truncate(d.title, 24), d.nodeID, d.nodeID),
				NodeIDs: []string{d.nodeID},
			})
		}
	}

	rep.UnknownRefs = len(unknown)
	for _, id := range unknown {
		rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
			Code: "unknown_ref", Severity: SevError,
			Message: fmt.Sprintf("方案引用了原文中不存在的节点 %q", id),
			NodeIDs: []string{id},
		})
	}

	// Coverage and duplication, reported per node so the UI can highlight
	// the exact paragraphs at issue.
	var missing, dup []string
	for _, id := range contentIDs {
		rec, isRec := recomposed[id]
		if isRec && len(rec.Parts) > 0 {
			got := partClaims[id]
			if len(got) < len(rec.Parts) {
				got = append(got, make([]int, len(rec.Parts)-len(got))...)
			}
			var missingParts, dupParts []int
			for i := 0; i < len(rec.Parts); i++ {
				switch got[i] {
				case 0:
					missingParts = append(missingParts, i)
				case 1:
				default:
					dupParts = append(dupParts, i)
				}
			}
			if len(missingParts) > 0 {
				// The node itself was never wholly covered, so it counts as
				// missing rather than as a partial success.
				missing = append(missing, id)
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "uncovered_body_part", Severity: SevError,
					Message: fmt.Sprintf(
						"原文段落 %s 重组后有 %d 段没有被任何章节收录：%v：%q",
						id, len(missingParts), missingParts, truncate(rec.Parts[missingParts[0]], 60)),
					NodeIDs: []string{id},
				})
			}
			if len(dupParts) > 0 {
				dup = append(dup, id)
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "duplicate_body_part", Severity: SevError,
					Message: fmt.Sprintf(
						"原文段落 %s 重组后的第 %v 段被重复收录",
						id, dupParts),
					NodeIDs: []string{id},
				})
				continue
			}
			if len(missingParts) == 0 && len(got) > 0 {
				// Every part placed exactly once: the node is covered.
				claims[id] = 1
			}
			continue
		}
		switch c := claims[id]; {
		case c == 0:
			missing = append(missing, id)
		case c > 1:
			dup = append(dup, id)
		}
	}
	rep.CoveredNodes = len(contentIDs) - len(missing)
	rep.DuplicatedNodes = len(dup)

	for _, id := range missing {
		rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
			Code: "uncovered_content", Severity: SevError,
			Message: fmt.Sprintf("原文段落 %s 未被任何章节收录（内容丢失）：%q",
				id, truncate(doc.Text(id), 40)),
			NodeIDs: []string{id},
		})
	}
	for _, id := range dup {
		rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
			Code: "duplicate_content", Severity: SevError,
			Message: fmt.Sprintf("原文段落 %s 被重复收录 %d 次（%s）：%q",
				id, claims[id], strings.Join(claimedAs[id], ", "), truncate(doc.Text(id), 40)),
			NodeIDs: []string{id},
		})
	}

	sort.SliceStable(rep.Diagnostics, func(i, j int) bool {
		rank := func(s Severity) int {
			switch s {
			case SevError:
				return 0
			case SevWarning:
				return 1
			default:
				return 2
			}
		}
		if rank(rep.Diagnostics[i].Severity) != rank(rep.Diagnostics[j].Severity) {
			return rank(rep.Diagnostics[i].Severity) < rank(rep.Diagnostics[j].Severity)
		}
		return rep.Diagnostics[i].Code < rep.Diagnostics[j].Code
	})
	return rep
}

// checkSectionOrder compares the source position of each section's first
// heading/body with the position of the previous section's last content, and
// reports every section that starts before its predecessor ended.
//
// It walks in emission order — depth-first, exactly as assemble emits — so the
// comparison reflects the printed page rather than the JSON's nesting. A
// section whose heading is synthesized has no source position of its own, so
// its body stands in; a section with neither is skipped (empty_section already
// covers it).
//
// Returns the number of offending sections, which is also stored on the report
// so the review screen can badge it the way it badges the per-section reorder
// count.
func checkSectionOrder(rep *Report, plan *Plan, order map[string]int) int {
	if plan == nil {
		return 0
	}
	// A recomposed document has deliberately reordered prose inside its
	// source paragraphs, and the source has no chapter order to compare
	// against. Reporting every part as "out of order" would bury the change
	// the user actually needs to review (the recomposition) under warnings.
	if len(plan.Recompose) > 0 {
		return 0
	}
	var bad int
	// The cursor is the highest source position emitted so far. A struct holds
	// it because a Go closure cannot assign to a variable declared after it.
	cursor := &orderCursor{order: order}
	var visit func([]Section)
	visit = func(list []Section) {
		for i := range list {
			s := &list[i]
			pos, ok := sectionStart(s, order)
			if !ok {
				visit(s.Children)
				continue
			}
			if cursor.seen && pos < cursor.high {
				bad++
				rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
					Code: "section_order", Severity: SevWarning,
					Message: fmt.Sprintf(
						"章节 %s（%q）的内容在原文中位于上一章节之前：成品会按方案的顺序输出，"+
							"与原文的阅读顺序不一致",
						s.ID, truncate(s.Title, 24)),
					NodeIDs: []string{s.ID},
				})
			}
			cursor.seen = true
			if end := sectionEnd(s, pos, order); end > cursor.high {
				cursor.high = end
			}
			visit(s.Children)
		}
	}
	visit(plan.Sections)
	return bad
}

// orderCursor tracks how far through the source the emitted plan has reached.
type orderCursor struct {
	order map[string]int
	seen  bool
	high  int
}

// sectionStart is the lowest source position the section claims: its heading
// node when it has one, otherwise the first of its body nodes.
func sectionStart(s *Section, order map[string]int) (int, bool) {
	if s == nil {
		return 0, false
	}
	if s.HeadingNodeID != "" {
		if o, ok := order[s.HeadingNodeID]; ok {
			return o, true
		}
	}
	for _, id := range s.BodyNodeIDs {
		if o, ok := order[id]; ok {
			return o, true
		}
	}
	for _, bp := range s.BodyParts {
		if o, ok := order[bp.NodeID]; ok {
			return o, true
		}
	}
	return 0, false
}

// sectionEnd is the highest source position the section itself claims,
// defaulting to start. Children are visited separately, so they are not
// included here.
func sectionEnd(s *Section, start int, order map[string]int) int {
	end := start
	if o, ok := order[s.HeadingNodeID]; ok && o > end {
		end = o
	}
	for _, id := range s.BodyNodeIDs {
		if o, ok := order[id]; ok && o > end {
			end = o
		}
	}
	for _, bp := range s.BodyParts {
		if o, ok := order[bp.NodeID]; ok && o > end {
			end = o
		}
	}
	return end
}

// checkPreambleOrder reports preamble nodes that come after content the plan
// places earlier in the document.
//
// The preamble is emitted under 主送机关, ahead of every chapter. A node taken
// from the end of the source would therefore be moved to the front silently:
// coverage is satisfied (it is claimed exactly once) and VerifyVerbatim
// compares text by node, not by position, so the export would pass with the
// document reordered.
func checkPreambleOrder(rep *Report, doc *Document, plan *Plan, order map[string]int) {
	if plan == nil || doc == nil || len(plan.PreambleNodeIDs) == 0 {
		return
	}
	if len(plan.Recompose) > 0 {
		return
	}

	var firstSection int = -1
	var visit func([]Section)
	visit = func(list []Section) {
		for i := range list {
			if p, ok := sectionStart(&list[i], order); ok {
				if firstSection < 0 || p < firstSection {
					firstSection = p
				}
			}
			visit(list[i].Children)
		}
	}
	visit(plan.Sections)

	for _, id := range plan.PreambleNodeIDs {
		o, ok := order[id]
		if !ok {
			continue
		}
		// Out of place in either direction: after the first chapter, or out
		// of order within the preamble itself. The second case is normalised
		// by the click-order of the review screen, so only the first is
		// reported — otherwise every manual addition to the preamble would
		// raise a warning.
		if firstSection >= 0 && o > firstSection {
			rep.Diagnostics = append(rep.Diagnostics, Diagnostic{
				Code: "preamble_out_of_place", Severity: SevWarning,
				Message: fmt.Sprintf(
					"正文前言里的段落 %s 在原文中位于第一个章节之后："+
						"成品会把它移到主送机关之下（文首），与原文的顺序不一致",
					id),
				NodeIDs: []string{id},
			})
		}
	}
}

// titleRepeat is one synthesized section title that a body paragraph already
// carries.
type titleRepeat struct {
	sectionID string
	title     string
	nodeID    string
}

// synthesizedTitleRepeats finds synthesized section titles that duplicate a
// paragraph claimed as body.
//
// Only sections without a HeadingNodeID qualify: when the heading comes from
// the source, VerifyPlan already requires Title to equal that node's text and
// the node is claimed as the heading rather than as body, so the line appears
// exactly once. The defect is specific to a model that authored the title
// itself while leaving the source's own copy in the body.
func synthesizedTitleRepeats(doc *Document, plan *Plan, claims map[string]int) []titleRepeat {
	var out []titleRepeat
	seen := make(map[string]bool)
	var walk func([]Section)
	walk = func(list []Section) {
		for i := range list {
			s := &list[i]
			if s.HeadingNodeID == "" {
				title := strings.TrimSpace(s.Title)
				if title != "" {
					if id := findNodeByExactText(doc, title); id != "" && claims[id] > 0 {
						key := s.ID + "\x00" + id
						if !seen[key] {
							seen[key] = true
							out = append(out, titleRepeat{sectionID: s.ID, title: title, nodeID: id})
						}
					}
				}
			}
			walk(s.Children)
		}
	}
	walk(plan.Sections)
	return out
}

// hasElement reports whether a role is bound in the plan.
func hasElement(plan *Plan, role Role) bool {
	if plan == nil {
		return false
	}
	for _, e := range plan.Elements {
		if e.Role == role && e.NodeID != "" {
			return true
		}
	}
	return false
}

// findNodeByExactText returns the id of the first prose node whose text equals
// want once surrounding whitespace is ignored, or "".
//
// Comparison ignores leading/trailing whitespace only: an interior difference
// means the two are genuinely different strings, and treating them as equal
// would let a near-miss slip through.
func findNodeByExactText(doc *Document, want string) string {
	want = strings.TrimSpace(want)
	if want == "" || doc == nil {
		return ""
	}
	for _, n := range doc.ContentNodes() {
		if strings.TrimSpace(n.Text) == want {
			return n.ID
		}
	}
	return ""
}

// ------------------------------------------------------- verbatim enforcement

// VerbatimDiff explains where an assembled document deviates from its source.
type VerbatimDiff struct {
	// Missing are source texts absent from the output.
	Missing []string `json:"missing,omitempty"`
	// Extra are output texts absent from the source. For a correctly
	// assembled document these must all be synthesized scaffolding
	// (masthead, numbering, generated dates) or heading titles.
	Extra []string `json:"extra,omitempty"`
	// Altered are source nodes whose output text differs from the source
	// while sharing the same node id, and where the difference is NOT
	// covered by a declared edit. This list must always be empty: it is the
	// definition of an undeclared (i.e. silent) rewrite.
	Altered []string `json:"altered,omitempty"`
	// Changed are the declared edits that were actually applied, in order.
	// These are not failures — they are the audit trail the user reviews.
	Changed []AppliedEdit `json:"changed,omitempty"`
	// Unapplied are declared edits whose Before text was not found, so they
	// had no effect. They are not failures of the document (the source text
	// is intact), but they mean the plan asked for something impossible and
	// the user should know.
	Unapplied []UnappliedEdit `json:"unapplied,omitempty"`
	// Rewrites describe every recomposed paragraph with its measured change,
	// its register tier and the budget it was allowed. A rewrite over its
	// budget, or one that dropped a hard fact, is a failure.
	//
	// This exists because "改动尽量少" cannot be enforced by checking that the
	// model declared a rewrite — declaring it is what Recompose already does.
	// The budget turns the promise into a number the gate can compare against.
	Rewrites []RewriteCheck `json:"rewrites,omitempty"`
}

// RewriteCheck is the audit record for one rewritten paragraph.
type RewriteCheck struct {
	NodeID string `json:"node_id"`
	// Tier is the register the source paragraph was read as.
	Tier string `json:"tier"`
	// TierLabel is the Chinese label for the tier, for the review panel.
	TierLabel string `json:"tier_label"`
	// Budget is the maximum change percentage allowed for that tier.
	Budget int `json:"budget"`
	// Change is the measured distance from source to output, 0-100.
	Change int `json:"change"`
	// OverBudget is true when Change exceeds Budget. Such a rewrite blocks
	// the export.
	OverBudget bool `json:"over_budget,omitempty"`
	// MissingFacts are values present in the source but absent from the
	// rewrite. A missing hard fact blocks the export; a soft fact only warns.
	MissingFacts []FactLoss `json:"missing_facts,omitempty"`
	// InventedFacts are hard facts the rewrite introduced that the source never
	// contained. Every one blocks the export: a number the author did not
	// write is a fabrication, however small the wording change was.
	InventedFacts []FactLoss `json:"invented_facts,omitempty"`
	// Reason is the model's own explanation, shown next to the numbers.
	Reason string `json:"reason,omitempty"`
}

// FactLoss is one value a rewrite failed to preserve.
//
// Hard facts (numbers, dates, 发文字号) block the export: losing "2024年5月1日"
// changes what the document says, which no amount of good prose repairs. Soft
// facts (organisation names, quoted phrases) are surfaced for review, because
// the extractor cannot always distinguish an organisation from an ordinary
// noun.
type FactLoss struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Hard bool   `json:"hard"`
}

// AppliedEdit records one declared change that reached the output.
type AppliedEdit struct {
	NodeID string `json:"node_id"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Before string `json:"before"`
	After  string `json:"after"`
	Reason string `json:"reason,omitempty"`
}

// UnappliedEdit records one declared change that could not be applied.
type UnappliedEdit struct {
	NodeID string `json:"node_id,omitempty"`
	Kind   string `json:"kind"`
	Before string `json:"before"`
	Reason string `json:"reason"`
}

// Clean reports whether no source content was lost and no undeclared change
// was made. Declared edits are permitted and are returned separately in
// Changed for the user to review.
func (v *VerbatimDiff) Clean() bool {
	return v != nil && len(v.Missing) == 0 && len(v.Altered) == 0 && len(v.RewriteFailures()) == 0
}

// RewriteFailures lists the rewrites that must block an export: those over
// budget, and those that dropped a hard fact.
func (v *VerbatimDiff) RewriteFailures() []string {
	if v == nil {
		return nil
	}
	var out []string
	for _, r := range v.Rewrites {
		if r.OverBudget {
			out = append(out, fmt.Sprintf(
				"%s: 改动 %d%%，超过「%s」段落允许的 %d%%",
				r.NodeID, r.Change, r.TierLabel, r.Budget))
		}
		for _, f := range r.MissingFacts {
			if f.Hard {
				out = append(out, fmt.Sprintf("%s: 改后丢失原文数据「%s」", r.NodeID, f.Text))
			}
		}
		for _, f := range r.InventedFacts {
			out = append(out, fmt.Sprintf("%s: 改后出现原文没有的数据「%s」", r.NodeID, f.Text))
		}
	}
	return out
}

// VerifyVerbatim compares an assembled document against its source, taking the
// plan's declared edits into account.
//
// # The rule this enforces
//
//	原文可以被修改，但每一处修改都必须显式声明、可见、可审计。
//
// For each node it compares the output against the source text *with that
// node's declared edits applied*. A difference that the plan declared shows up
// in Changed and is not a failure; a difference it did not declare shows up in
// Altered and fails the export. So the guarantee is no longer "the text is
// untouched" but the stronger, more useful "no change can happen silently" —
// which is what lets the tool fix a typo without becoming a tool that rewrites
// documents behind the user's back.
//
// # What "payload" means per node kind
//
// Prose nodes are compared by text. Tables and images are compared by their
// real payload — Rows and Image bytes — because a table's Node.Text is
// deliberately empty: inventing a flattened string for it would itself be a
// fabrication, and the parser is right to refuse. Comparing only Text would
// therefore report a perfectly rendered table as missing content, which would
// in turn make a correct document fail the export gate. This function is the
// export gate, so it has to understand every kind it can be handed.
//
// Extra output text is returned for inspection but is not itself a failure: a
// 公文 legitimately contains text the source did not, such as 层次序数
// numbering ("一、") and a masthead. Because every extra string is reported, a
// reviewer can confirm that each one is scaffolding rather than model prose.
func VerifyVerbatim(doc *Document, out *Doc, plan *Plan) *VerbatimDiff {
	diff := &VerbatimDiff{}
	if doc == nil || out == nil {
		diff.Missing = doc.ContentIDs()
		return diff
	}

	// Group the output by provenance so each block is compared against the
	// source node it claims to reproduce.
	type emitted struct {
		text  []string
		rows  [][][]string
		image []int
	}
	outByNode := make(map[string]*emitted)
	var extras []string

	get := func(id string) *emitted {
		e, ok := outByNode[id]
		if !ok {
			e = &emitted{}
			outByNode[id] = e
		}
		return e
	}

	for _, b := range out.Blocks {
		if b.NodeID == "" {
			if b.Text != "" {
				extras = append(extras, b.Text)
			}
			continue
		}
		e := get(b.NodeID)
		if b.Text != "" {
			e.text = append(e.text, b.Text)
		}
		if len(b.Rows) > 0 {
			e.rows = append(e.rows, b.Rows)
		}
		if b.Image != nil {
			e.image = append(e.image, len(b.Image.Data))
		}
	}

	for _, n := range doc.ContentNodes() {
		e := outByNode[n.ID]
		if e == nil {
			diff.Missing = append(diff.Missing, describeNode(n))
			continue
		}

		switch n.Kind {
		case KindTable:
			// Compare the actual cell matrix. Text is not consulted: the
			// parser leaves it empty on purpose.
			if len(e.rows) == 0 {
				diff.Missing = append(diff.Missing, describeNode(n))
				continue
			}
			if !sameTable(n.Rows, e.rows[0]) {
				diff.Altered = append(diff.Altered, fmt.Sprintf(
					"%s: 表格内容被改动（原文 %d行×%d列 → 输出 %d行×%d列）",
					n.ID, len(n.Rows), maxCols(n.Rows), len(e.rows[0]), maxCols(e.rows[0])))
			}

		case KindImage:
			// An image's identity is its bytes. A node that carries a
			// filename in Text must also have its picture emitted, or the
			// document lost a figure while still containing its caption.
			if n.Image != nil && len(n.Image.Data) > 0 {
				if len(e.image) == 0 {
					diff.Missing = append(diff.Missing, describeNode(n))
					continue
				}
				if e.image[0] != len(n.Image.Data) {
					diff.Altered = append(diff.Altered, fmt.Sprintf(
						"%s: 图片数据长度改变（原文 %d 字节 → 输出 %d 字节）",
						n.ID, len(n.Image.Data), e.image[0]))
				}
			} else if len(e.text) == 0 && len(e.image) == 0 {
				diff.Missing = append(diff.Missing, describeNode(n))
			}

		default:
			if len(e.text) == 0 {
				diff.Missing = append(diff.Missing, describeNode(n))
				continue
			}

			// A recomposed paragraph is emitted as a sequence of declared
			// parts. Compare it part by part rather than by concatenation:
			// the whole point of the declaration is that the reader sees
			// several paragraphs, and a concatenated comparison would accept
			// the parts glued back into one.
			if rec, ok := plan.RecomposeFor(n.ID); ok {
				if len(e.text) != len(rec.Parts) {
					diff.Altered = append(diff.Altered, fmt.Sprintf(
						"%s: 重组声明为 %d 段，输出为 %d 段（未按声明重组）",
						n.ID, len(rec.Parts), len(e.text)))
					goto nextNode
				}
				mismatch := -1
				for i := range rec.Parts {
					if e.text[i] != rec.Parts[i] {
						mismatch = i
						break
					}
				}
				if mismatch >= 0 {
					diff.Altered = append(diff.Altered, fmt.Sprintf(
						"%s: 重组第 %d 段与声明不一致：声明 %q → 输出 %q",
						n.ID, mismatch, truncate(rec.Parts[mismatch], 60),
						truncate(e.text[mismatch], 60)))
					goto nextNode
				}
				diff.Changed = append(diff.Changed, AppliedEdit{
					NodeID: n.ID,
					Kind:   "recompose",
					Label:  "重组段落",
					Before: truncate(n.Text, 120),
					After:  truncate(strings.Join(rec.Parts, "\n"), 120),
					Reason: rec.Reason,
				})

				// Declaring a rewrite is not the same as being allowed to make
				// it. Measure how far the output actually moved and refuse the
				// ones that exceeded what the register of the source permits.
				// Without this, Recompose is a hole in the "no silent change"
				// guarantee: the model supplies both the replacement text and
				// the value it is compared against.
				diff.Rewrites = append(diff.Rewrites, checkRewrite(n.Text, rec, plan))
				goto nextNode
			}

			// A node may legitimately be emitted as more than one block when
			// the extra pieces are the node's own text split by the
			// assembler; their concatenation is what must match.
			got := strings.Join(e.text, "")

			// What the output is compared against: the source text with this
			// node's declared edits applied. An undeclared difference is then
			// exactly a difference from this expectation.
			var nodeEdits []Edit
			if plan != nil {
				nodeEdits = plan.EditsFor(n.ID)
			}
			expected, failed := AppliedText(n.Text, nodeEdits)

			for _, f := range failed {
				diff.Unapplied = append(diff.Unapplied, UnappliedEdit{
					NodeID: f.NodeID, Kind: string(f.Kind), Before: truncate(f.Before, 60),
					Reason: "在原文中未找到待替换内容，该修改未生效",
				})
			}

			if got != expected {
				diff.Altered = append(diff.Altered,
					fmt.Sprintf("%s: 原文 %q → 输出 %q（未声明的修改）",
						n.ID, truncate(expected, 60), truncate(got, 60)))
				continue
			}

			// Declared edits are one more door into the same rewrite. A
			// single edit whose Before is the whole paragraph and whose
			// After is a different paragraph passes every check above —
			// it is declared, it is found, and the output matches it —
			// while bypassing the budget entirely. Measure what the edits
			// did to the paragraph and apply the same limit.
			if len(nodeEdits) > 0 && expected != n.Text && rewriteBudgetApplies(n.Text) &&
				editCoverage(nodeEdits, n.Text) >= 0.4 {
				assessment := style.Assess(n.Text)
				limits := DefaultRewriteLimits()
				if plan != nil {
					limits = plan.RewriteLimits.Normalized()
				}
				budget := limits.MaxFor(assessment.Tier)
				change := int(style.Distance(n.Text, expected) + 0.5)
				check := RewriteCheck{
					NodeID:     n.ID,
					Tier:       string(assessment.Tier),
					TierLabel:  assessment.Tier.Label(),
					Budget:     budget,
					Change:     change,
					OverBudget: limits.Enabled && change > budget,
					Reason:     "逐条声明修改后的整段改动幅度",
				}
				for _, lost := range style.MissingFacts(n.Text, expected) {
					check.MissingFacts = append(check.MissingFacts, FactLoss{
						Kind: lost.Kind, Text: lost.Text, Hard: lost.Hard,
					})
				}
				for _, added := range style.InventedFacts(n.Text, expected) {
					check.InventedFacts = append(check.InventedFacts, FactLoss{
						Kind: added.Kind, Text: added.Text, Hard: added.Hard,
					})
				}
				diff.Rewrites = append(diff.Rewrites, check)
			}

			// Applied and matching: record it for the change log.
			//
			// Only edits that ACTUALLY reached the text are listed. Listing
			// every declared edit would be wrong twice over: it would advertise
			// a change that did not happen (an edit whose Before was not found),
			// and it would claim credit for a no-op. The change log is what the
			// user reviews to decide whether to trust the document, so it must
			// describe the document, not the plan's intentions.
			for _, ed := range appliedEdits(nodeEdits, n.Text) {
				diff.Changed = append(diff.Changed, AppliedEdit{
					NodeID: n.ID, Kind: string(ed.Kind), Label: EditLabel(ed.Kind),
					Before: truncate(ed.Before, 120), After: truncate(ed.After, 120),
					Reason: ed.Reason,
				})
			}
		}
	nextNode:
	}

	// Edits targeting an unknown node never reach the loop above, so they would
	// otherwise be reported nowhere.
	//
	// A nil plan is treated as "no declared edits", which is what a caller
	// checking a document without one means; it must not panic.
	var declaredEdits []Edit
	if plan != nil {
		declaredEdits = plan.Edits
	}
	for _, ed := range declaredEdits {
		if ed.NodeID == "" {
			// An edit with no target node cannot be applied: Edits are applied
			// *within* one node's text, so there is nothing to anchor this to.
			//
			// This case used to be skipped outright, which meant a model asking
			// for a new heading got no heading, no change-log entry, and no
			// diagnostic — a silent loss, and exactly what the "no change without
			// a declaration" rule exists to prevent. It is reported instead.
			//
			// Generating a heading the source lacks is supported, but through
			// the structure rather than through an Edit: a Section with a Title
			// and no HeadingNodeID emits a heading block and is counted in
			// Report.SynthesizedHeadings. The reason below says so, because a
			// diagnostic the caller cannot act on is only half a diagnostic.
			diff.Unapplied = append(diff.Unapplied, UnappliedEdit{
				Kind:   string(ed.Kind),
				Before: truncate(ed.Before, 60),
				Reason: "该修改没有指定目标段落（node_id 为空），无法应用；" +
					"若要为原文补一个段落标题，请给章节填 title 并留空 heading_node_id。",
			})
			continue
		}
		if _, ok := doc.Node(ed.NodeID); !ok {
			diff.Unapplied = append(diff.Unapplied, UnappliedEdit{
				NodeID: ed.NodeID, Kind: string(ed.Kind), Before: truncate(ed.Before, 60),
				Reason: "目标节点不存在，该修改未生效",
			})
		}
	}

	for _, e := range extras {
		diff.Extra = append(diff.Extra, truncate(e, 60))
	}
	return diff
}

// appliedEdits returns the subset of edits that would actually change src:
// those whose Before is present at its declared occurrence and whose After
// differs from Before.
//
// It exists so the change log can describe what happened to the document rather
// than what the plan intended. Both exclusions matter:
//
//   - an edit whose Before is absent changes nothing, and belongs in Unapplied;
//   - an edit with After == Before changes nothing, and belongs nowhere.
//
// Reported consequences are derived from the same function the assembler used to
// produce the text, so the log cannot drift from the document.
func appliedEdits(edits []Edit, src string) []Edit {
	var out []Edit
	// Walk the text forward the way AppliedText does, tracking each edit's
	// effect in order.
	cur := src
	for _, e := range edits {
		if e.Before == "" || e.Before == e.After {
			continue
		}
		idx := nthIndex(cur, e.Before, e.Occurrence)
		if idx < 0 {
			continue
		}
		out = append(out, e)
		cur = cur[:idx] + e.After + cur[idx+len(e.Before):]
	}
	return out
}

// describeNode renders a node for a Missing diagnostic, using whatever payload
// the node actually has (a table has no text to quote).
func describeNode(n Node) string {
	switch {
	case n.Kind == KindTable:
		return fmt.Sprintf("%s: 表格 %d行×%d列 %s",
			n.ID, len(n.Rows), maxCols(n.Rows), truncate(n.Text, 40))
	case n.Kind == KindImage:
		name := ""
		if n.Image != nil {
			name = n.Image.Name
		}
		return fmt.Sprintf("%s: 图片 %s", n.ID, name)
	default:
		return fmt.Sprintf("%s: %s", n.ID, truncate(n.Text, 60))
	}
}

// runeLen counts characters, not bytes, so a Chinese paragraph's size is
// measured in the unit a reader perceives.
func runeLen(s string) int { return len([]rune(s)) }

// recomposeLen counts the characters in all declared parts.
func recomposeLen(parts []string) int {
	n := 0
	for _, p := range parts {
		n += runeLen(p)
	}
	return n
}

// sameTable reports whether two cell matrices are identical.
func sameTable(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// maxCols returns the widest row's length.
func maxCols(rows [][]string) int {
	n := 0
	for _, r := range rows {
		if len(r) > n {
			n = len(r)
		}
	}
	return n
}

// checkRewrite measures one recomposed paragraph against the budget its source
// register earns.
//
// The budget comes from the plan, which the server fills from the user's
// settings — never from the model, which cannot be allowed to raise its own
// limit. A plan assembled without server involvement (tests, CLI use) falls
// back to DefaultLimits, so the check is never silently absent.
func checkRewrite(src string, rec Recompose, plan *Plan) RewriteCheck {
	assessment := style.Assess(src)
	limits := DefaultRewriteLimits()
	if plan != nil {
		limits = plan.RewriteLimits.Normalized()
	}
	budget := limits.MaxFor(assessment.Tier)

	out := strings.Join(rec.Parts, "")
	change := int(style.Distance(src, out) + 0.5)

	check := RewriteCheck{
		NodeID:     rec.NodeID,
		Tier:       string(assessment.Tier),
		TierLabel:  assessment.Tier.Label(),
		Budget:     budget,
		Change:     change,
		OverBudget: limits.Enabled && change > budget,
		Reason:     rec.Reason,
	}
	for _, lost := range style.MissingFacts(src, out) {
		check.MissingFacts = append(check.MissingFacts, FactLoss{
			Kind: lost.Kind, Text: lost.Text, Hard: lost.Hard,
		})
	}
	for _, added := range style.InventedFacts(src, out) {
		check.InventedFacts = append(check.InventedFacts, FactLoss{
			Kind: added.Kind, Text: added.Text, Hard: added.Hard,
		})
	}
	return check
}

// rewriteBudgetApplies reports whether a paragraph is substantial enough for
// the character-overlap measure to mean anything.
//
// On a very short string the measure is not a proxy for "how much was
// rewritten": "按排好工做，认真负则。" → "安排好工作，认真负责。" fixes three
// typos and replaces six characters of twelve, which scores as a 50% rewrite
// while being exactly the correction the user asked for. A budget applied there
// would reject the product's least controversial feature. Real paragraphs — the
// ones where a wholesale rewrite is a problem — are far longer than this.
func rewriteBudgetApplies(src string) bool {
	return len([]rune(strings.TrimSpace(src))) >= 30
}

// editCoverage is the share of the source that the declared edits delete.
//
// It answers "is this a patch or a replacement?". Patching a typo touches a
// couple of characters; replacing a paragraph names the whole paragraph as
// Before. Only the latter needs to be measured against the rewrite budget; if
// every small correction were measured, a paragraph with several genuine typos
// would fail the gate for being corrected.
func editCoverage(edits []Edit, src string) float64 {
	total := len([]rune(src))
	if total == 0 {
		return 0
	}
	covered := 0
	for _, e := range edits {
		covered += len([]rune(e.Before))
	}
	if covered > total {
		covered = total
	}
	return float64(covered) / float64(total)
}
