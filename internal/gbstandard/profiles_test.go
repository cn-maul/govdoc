package gbstandard

import (
	"testing"

	"govdoc/internal/docmodel"
)

// The non-公文 profiles exist because the user asked for the tool to be
// general. These tests hold them to the contract the writers rely on, so a new
// profile cannot ship half-specified.

// AllNonGovDoc returns the general-purpose profiles.
func AllNonGovDoc() []struct {
	name string
	spec Spec
} {
	return []struct {
		name string
		spec Spec
	}{
		{"Generic", Generic()},
		{"Thesis", Thesis()},
		{"Report", Report()},
	}
}

func TestNonGovDocProfilesPassValidation(t *testing.T) {
	for _, p := range AllNonGovDoc() {
		if problems := ValidateSpec(p.spec); len(problems) > 0 {
			t.Errorf("%s 校验不通过: %v", p.name, problems)
		}
	}
}

func TestNonGovDocProfilesStyleEveryRole(t *testing.T) {
	for _, p := range AllNonGovDoc() {
		for _, role := range docmodel.AllRoles() {
			st, ok := p.spec.Style(role)
			if !ok {
				t.Errorf("%s 缺少 %q 的样式", p.name, role)
				continue
			}
			if st.Font == "" {
				t.Errorf("%s 的 %q 缺少字体", p.name, role)
			}
			if st.Size.Points() <= 0 {
				t.Errorf("%s 的 %q 字号无效: %q", p.name, role, st.Size)
			}
			if st.Align == "" {
				t.Errorf("%s 的 %q 未指定对齐", p.name, role)
			}
		}
	}
}

// TestNonGovDocProfilesHaveNoCharGrid is the guard against reintroducing the
// title-overlap defect under a different name. A profile that declares the
// 公文 grid has its glyphs condensed to force 28 characters onto a line — a rule
// the user never asked for and that visibly squeezes large type.
func TestNonGovDocProfilesHaveNoCharGrid(t *testing.T) {
	for _, p := range AllNonGovDoc() {
		if p.spec.Page.LinesPerPage != 0 || p.spec.Page.CharsPerLine != 0 {
			t.Errorf("%s 不应声明字符网格（每面%d行×每行%d字）",
				p.name, p.spec.Page.LinesPerPage, p.spec.Page.CharsPerLine)
		}
		// And the consequence must actually be zero: no condensation anywhere.
		for _, role := range docmodel.AllRoles() {
			if g := p.spec.GridCondensePT(role); g != 0 {
				t.Errorf("%s 的 %q 被压缩字距 %.3fpt，应为 0", p.name, role, g)
			}
		}
	}
}

// TestGovDocProfileKeepsItsGrid protects the standard we must not regress.
func TestGovDocProfileKeepsItsGrid(t *testing.T) {
	s := Default()
	if s.Page.LinesPerPage != 22 || s.Page.CharsPerLine != 28 {
		t.Errorf("公文配置应保留 22 行×28 字, 实际 %d×%d",
			s.Page.LinesPerPage, s.Page.CharsPerLine)
	}
}

// TestValidateSpecAcceptsAbsentGrid pins the change that let general profiles
// exist: an absent grid is valid, a half-declared one is not.
func TestValidateSpecAcceptsAbsentGrid(t *testing.T) {
	s := Generic()
	if problems := ValidateSpec(s); len(problems) > 0 {
		t.Fatalf("无网格的配置应合法: %v", problems)
	}

	// Half a grid has no correct interpretation and must be refused.
	half := Generic()
	half.Page.LinesPerPage = 30
	problems := ValidateSpec(half)
	if len(problems) == 0 {
		t.Error("只填一半字符网格应被拒绝")
	}
}

// TestProfilesDifferInTheValuesUsersPick distinguishes "several configurations"
// from "several identical configurations".
func TestProfilesDifferInTheValuesUsersPick(t *testing.T) {
	type key struct {
		font, size string
		linePT     float64
	}
	seen := map[key]string{}
	for _, p := range append(AllNonGovDoc(), struct {
		name string
		spec Spec
	}{"GovDoc", Default()}) {
		body := p.spec.Styles[docmodel.RoleBody]
		k := key{body.Font, string(body.Size), body.LineSpacingPT}
		if prev, dup := seen[k]; dup {
			t.Errorf("%s 与 %s 的正文规则完全相同（%s/%s/%.1fpt），选它们没有区别",
				p.name, prev, k.font, k.size, k.linePT)
		}
		seen[k] = p.name
	}
}

// TestGenericProfilesHaveNoRedRule: 红色分隔线 is a 公文 element. Drawing a red
// line through a thesis would be wrong, so the other profiles leave it colourless.
func TestGenericProfilesHaveNoRedRule(t *testing.T) {
	for _, p := range AllNonGovDoc() {
		st, ok := p.spec.Style(docmodel.RoleRedLine)
		if !ok {
			t.Errorf("%s 缺少分隔线样式", p.name)
			continue
		}
		if st.BorderBottom != nil {
			t.Errorf("%s 的分隔线不应有边框（公文才用红线）: %+v", p.name, st.BorderBottom)
		}
		if st.Color == ColorRed {
			t.Errorf("%s 的分隔线不应是红色", p.name)
		}
	}
}

// TestGovDocProfileKeepsItsRedRule is the counterpart, so the fix above cannot be
// achieved by stripping 公文 of its own convention.
func TestGovDocProfileKeepsItsRedRule(t *testing.T) {
	st, ok := Default().Style(docmodel.RoleRedLine)
	if !ok {
		t.Fatal("公文缺少分隔线样式")
	}
	if st.BorderBottom == nil || st.BorderBottom.Color != ColorRed {
		t.Errorf("公文分隔线应为红色: %+v", st.BorderBottom)
	}
}

// TestGenericProfilesOmitPageNumberDash: the 一字线 around the page number is a
// 公文 convention; a thesis expects a plain number.
func TestGenericProfilesOmitPageNumberDash(t *testing.T) {
	for _, p := range AllNonGovDoc() {
		if p.spec.PageNumberDash != "" {
			t.Errorf("%s 的页码不应带一字线, 实际 %q", p.name, p.spec.PageNumberDash)
		}
	}
	if Default().PageNumberDash == "" {
		t.Error("公文页码应保留一字线")
	}
}

// TestBodyIndentIsTwoCharsForEveryProfile keeps the Chinese convention across all
// of them, which is what makes them usable for Chinese prose.
func TestBodyIndentIsTwoCharsForEveryProfile(t *testing.T) {
	for _, p := range AllNonGovDoc() {
		if got := p.spec.Styles[docmodel.RoleBody].FirstLineIndentChars; got != 2 {
			t.Errorf("%s 正文首行缩进 = %g 字, 期望 2", p.name, got)
		}
	}
}

// TestProfileTitlesAreCentredAndLarger: 标题 must be visibly a title, not just
// another line of body text.
func TestProfileTitlesAreCentredAndLarger(t *testing.T) {
	for _, p := range AllNonGovDoc() {
		title := p.spec.Styles[docmodel.RoleTitle]
		body := p.spec.Styles[docmodel.RoleBody]
		if title.Align != AlignCenter {
			t.Errorf("%s 标题未居中: %q", p.name, title.Align)
		}
		if title.Size.Points() <= body.Size.Points() {
			t.Errorf("%s 标题字号 %gpt 不大于正文 %gpt",
				p.name, title.Size.Points(), body.Size.Points())
		}
		if !title.KeepWithNext {
			t.Errorf("%s 标题应与下文同页（避免孤行）", p.name)
		}
	}
}
