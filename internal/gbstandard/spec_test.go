package gbstandard

import (
	"math"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// approx compares floats with a tolerance, since the standard's millimetre
// figures are converted to points and back.
func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// ---------------------------------------------------------------- 字号

// TestFontSizePoints pins the 字号→pt table. These equivalences are what the
// whole document depends on: if 三号 were wrong, every paragraph would be
// wrong while still looking plausible.
func TestFontSizePoints(t *testing.T) {
	cases := map[FontSize]float64{
		SizeChuhao: 42, SizeXiaoChuhao: 36, SizeYihao: 26, SizeXiaoYi: 24,
		SizeErhao: 22, SizeXiaoEr: 18, SizeSanhao: 16, SizeXiaoSan: 15,
		SizeSihao: 14, SizeXiaoSi: 12, SizeWuhao: 10.5, SizeXiaoWu: 9,
	}
	for size, want := range cases {
		if got := size.Points(); got != want {
			t.Errorf("%s.Points() = %v, 期望 %v", size, got, want)
		}
		if !size.Known() {
			t.Errorf("%s 应被识别为已知字号", size)
		}
	}
}

// TestUnknownFontSizeIsNotPlausible: an unknown 字号 must report 0 rather
// than a default, so a typo cannot produce a wrong-but-plausible document.
func TestUnknownFontSizeIsNotPlausible(t *testing.T) {
	var bad FontSize = "三號"
	if bad.Known() {
		t.Error("未知字号不应被识别")
	}
	if got := bad.Points(); got != 0 {
		t.Errorf("未知字号的磅值应为 0，实际 %v", got)
	}
}

// ---------------------------------------------------------------- 页面

// TestPageSetupMatchesStandard checks GB/T 9704-2012 §6.1: A4, margins
// 37/35/28/26mm (±1mm), 版心 156mm×225mm, 每面22行×每行28字.
func TestPageSetupMatchesStandard(t *testing.T) {
	p := DefaultPageSetup()

	if p.WidthMM != 210 || p.HeightMM != 297 {
		t.Errorf("纸张应为 A4 210×297mm，实际 %.0f×%.0f", p.WidthMM, p.HeightMM)
	}
	if p.MarginTopMM != 37 {
		t.Errorf("上白边应为 37mm，实际 %.0f", p.MarginTopMM)
	}
	if p.MarginBottomMM != 35 {
		t.Errorf("下白边应为 35mm，实际 %.0f", p.MarginBottomMM)
	}
	if p.MarginLeftMM != 28 {
		t.Errorf("左白边应为 28mm，实际 %.0f", p.MarginLeftMM)
	}
	if p.MarginRightMM != 26 {
		t.Errorf("右白边应为 26mm，实际 %.0f", p.MarginRightMM)
	}

	// 版心 156mm × 225mm
	if got := p.TextWidthMM(); !approx(got, 156, 0.01) {
		t.Errorf("版心宽度应为 156mm，实际 %.2f", got)
	}
	if got := p.TextHeightMM(); !approx(got, 225, 0.01) {
		t.Errorf("版心高度应为 225mm，实际 %.2f", got)
	}

	if p.LinesPerPage != 22 {
		t.Errorf("每面应为 22 行，实际 %d", p.LinesPerPage)
	}
	if p.CharsPerLine != 28 {
		t.Errorf("每行应为 28 字，实际 %d", p.CharsPerLine)
	}
}

// TestLineSpacingFitsExactly22Lines is the link between the standard's
// prose ("每面22行") and a number a renderer can use: 225mm ÷ 22.
func TestLineSpacingFitsExactly22Lines(t *testing.T) {
	p := DefaultPageSetup()
	sp := p.LineSpacingPT()

	// 225mm in points.
	wantPT := 225.0 * 72.0 / 25.4 // ≈ 637.795
	if !approx(sp, wantPT/22, 0.01) {
		t.Errorf("固定行距 = %.4fpt, 期望 225mm/22 ≈ %.4fpt", sp, wantPT/22)
	}
	// 22 lines must fill the 版心 height.
	if !approx(sp*22, wantPT, 0.02) {
		t.Errorf("22 行合计 %.2fpt, 期望版心高度 %.2fpt", sp*22, wantPT)
	}
	// Sanity: practitioners use ≈28.99pt.
	if !approx(sp, 28.99, 0.02) {
		t.Errorf("固定行距应约为 28.99pt，实际 %.4f", sp)
	}
}

// TestCharPitchMatches28Chars: 每行28字 implies ≈15.79pt per character.
func TestCharPitchMatches28Chars(t *testing.T) {
	p := DefaultPageSetup()
	pitch := p.CharPitchPT()
	// 156mm = 442.205pt; ÷28 ≈ 15.79pt
	if !approx(pitch, 156.0*72.0/25.4/28, 0.01) {
		t.Errorf("字距 = %.4fpt, 期望 %.4fpt", pitch, 156.0*72.0/25.4/28)
	}
}

// TestStandardFiguresImplyCondensation documents a real tension inside
// GB/T 9704-2012 rather than papering over it: 版心宽 156mm, 每行28字 and
// 三号 (16pt) cannot all hold at once, because 28 × 16pt = 158.0mm overshoots
// the 156mm 版心 by about 2mm.
//
// The renderer's job is therefore well defined: pin 28 characters per line
// and condense the advance between them, which is what Word's 文档网格 does
// in the official 公文 templates. This test exists so that behaviour is a
// documented decision with a number attached, not an accident.
func TestStandardFiguresImplyCondensation(t *testing.T) {
	p := DefaultPageSetup()
	body := SizeSanhao

	// The overshoot is real and small: a couple of millimetres on a 156mm
	// line, i.e. well under 2%.
	natural := body.Points() * float64(p.CharsPerLine)
	avail := mmToPT(p.TextWidthMM())
	if natural <= avail {
		t.Fatalf("预期 28 个三号字自然间距会超出版心，实际 %.2fpt ≤ %.2fpt", natural, avail)
	}
	overMM := (natural - avail) * 25.4 / 72.0
	if overMM > 3 {
		t.Errorf("超出量 %.2fmm 过大，说明页面几何或字号理解有误", overMM)
	}
	t.Logf("28×三号 = %.2fpt vs 版心 %.2fpt，超出 %.2fmm",
		natural, avail, overMM)

	// Expressed as a per-character condensation.
	cond := p.CharCondensePT(body)
	if cond <= 0 {
		t.Fatal("应当需要压缩字距")
	}
	if want := body.Points() - p.CharPitchPT(); !approx(cond, want, 1e-9) {
		t.Errorf("压缩量 = %.4fpt, 期望 %.4fpt", cond, want)
	}
	// The condensation must be small enough to be typographically invisible.
	ratio := cond / body.Points()
	if ratio > 0.03 {
		t.Errorf("压缩比例 %.2f%% 过大，正文会显得挤压", ratio*100)
	}
	t.Logf("每字需压缩 %.3fpt（%.2f%%）", cond, ratio*100)

	if p.FitsNaturalSpacing(body) {
		t.Error("三号字在 156mm 内放 28 字应当需要压缩")
	}
}

// TestSmallerBodyFitsWithoutCondensation: the condensation is a consequence
// of 三号 specifically, not of the page geometry in general. A 四号 body
// (14pt) fits 28 characters naturally, which confirms the formula is not
// simply always compressing.
func TestSmallerBodyFitsWithoutCondensation(t *testing.T) {
	p := DefaultPageSetup()
	if !p.FitsNaturalSpacing(SizeSihao) {
		t.Errorf("四号字（14pt）应当无需压缩即可每行28字（可用 %.2fpt, 需要 %.2fpt）",
			mmToPT(p.TextWidthMM()), SizeSihao.Points()*28)
	}
}

// TestCondenseHandlesDegenerateInput: a malformed spec must yield 0 rather
// than a NaN or a negative letter-spacing that would spread text out.
func TestCondenseHandlesDegenerateInput(t *testing.T) {
	var p PageSetup // zero value: no lines, no columns
	if got := p.CharCondensePT(SizeSanhao); got != 0 {
		t.Errorf("零值 PageSetup 的压缩量应为 0，实际 %v", got)
	}
	p2 := DefaultPageSetup()
	if got := p2.CharCondensePT("未知字号"); got != 0 {
		t.Errorf("未知字号的压缩量应为 0，实际 %v", got)
	}
}

// TestMMToPT confirms the mm→pt conversion OOXML depends on.
func TestMMToPT(t *testing.T) {
	if got := mmToPT(25.4); !approx(got, 72, 1e-9) {
		t.Errorf("25.4mm 应为 72pt，实际 %v", got)
	}
	if got := mmToPT(210); !approx(got, 595.276, 0.01) {
		t.Errorf("210mm 应为 595.28pt，实际 %.3f", got)
	}
}

// ---------------------------------------------------------------- 行文方向

// TestMastheadTopPerDirection covers §4.3.2: 35mm for 平行文/下行文, 80mm for
// 上行文 (which must leave room for 签发人).
func TestMastheadTopPerDirection(t *testing.T) {
	if got := DirDown.MastheadTopMM(); got != 35 {
		t.Errorf("下行文发文机关标志应在版心上边缘下 35mm，实际 %.0f", got)
	}
	if got := DirParallel.MastheadTopMM(); got != 35 {
		t.Errorf("平行文应为 35mm，实际 %.0f", got)
	}
	if got := DirUp.MastheadTopMM(); got != 80 {
		t.Errorf("上行文应为 80mm，实际 %.0f", got)
	}
}

// ---------------------------------------------------------------- 样式条文

// TestBodyStylePerStandard covers §7.3.3: 正文用3号仿宋体字，一般每面22行、
// 每行28字，每自然段左空二字.
func TestBodyStylePerStandard(t *testing.T) {
	s := Default()
	body := s.MustStyle(docmodel.RoleBody)

	if body.Font != FontFangSong {
		t.Errorf("正文应为仿宋，实际 %q", body.Font)
	}
	if body.Size != SizeSanhao {
		t.Errorf("正文应为三号，实际 %q", body.Size)
	}
	if body.Size.Points() != 16 {
		t.Errorf("三号应为 16pt，实际 %.1f", body.Size.Points())
	}
	if body.FirstLineIndentChars != 2 {
		t.Errorf("正文每自然段应左空二字，实际 %.1f", body.FirstLineIndentChars)
	}
	if body.LineRule != LineExact {
		t.Errorf("正文应使用固定行距，实际 %q", body.LineRule)
	}
	if !approx(body.LineSpacingPT, 28.99, 0.02) {
		t.Errorf("正文固定行距应约 28.99pt，实际 %.3f", body.LineSpacingPT)
	}
}

// TestTitleStylePerStandard covers §7.3.1: 标题用2号小标宋体字，居中.
func TestTitleStylePerStandard(t *testing.T) {
	s := Default()
	title := s.MustStyle(docmodel.RoleTitle)

	if title.Size != SizeErhao {
		t.Errorf("标题应为二号，实际 %q", title.Size)
	}
	if title.Size.Points() != 22 {
		t.Errorf("二号应为 22pt，实际 %.1f", title.Size.Points())
	}
	if title.Font != FontXiaoBiaoSong {
		t.Errorf("标题应为小标宋体，实际 %q", title.Font)
	}
	if title.Align != AlignCenter {
		t.Errorf("标题应居中，实际 %q", title.Align)
	}
	if title.SpaceBeforeLines != 200 {
		t.Errorf("标题应在红色分隔线下空二行，实际 %.0f 百分之一行", title.SpaceBeforeLines)
	}
}

// TestHeadingFontsPerStandard covers §7.3.3: 一级标题用黑体，二级用楷体，
// 三级及以下用仿宋（层次序数分别为 一、/（一）/1./（1））.
func TestHeadingFontsPerStandard(t *testing.T) {
	s := Default()
	cases := []struct {
		role docmodel.Role
		font string
	}{
		{docmodel.RoleHeading1, FontHeiti},
		{docmodel.RoleHeading2, FontKaiti},
		{docmodel.RoleHeading3, FontFangSong},
		{docmodel.RoleHeading4, FontFangSong},
	}
	for _, c := range cases {
		st := s.MustStyle(c.role)
		if st.Font != c.font {
			t.Errorf("%s 应为 %q，实际 %q", c.role, c.font, st.Font)
		}
		if st.Size != SizeSanhao {
			t.Errorf("%s 应为三号，实际 %q", c.role, st.Size)
		}
		if st.FirstLineIndentChars != 2 {
			t.Errorf("%s 应左空二字，实际 %.1f", c.role, st.FirstLineIndentChars)
		}
		if !st.KeepWithNext {
			t.Errorf("%s 应设置与下段同页，避免标题孤立在页脚", c.role)
		}
	}
}

// TestHeaderStylesPerStandard covers §7.2: 份号/密级/紧急程度 用3号黑体，
// 发文字号 用3号仿宋.
func TestHeaderStylesPerStandard(t *testing.T) {
	s := Default()

	for _, role := range []docmodel.Role{docmodel.RoleCopyNumber, docmodel.RoleSecurity, docmodel.RoleUrgency} {
		st := s.MustStyle(role)
		if st.Font != FontHeiti {
			t.Errorf("%s 应为黑体，实际 %q", role, st.Font)
		}
		if st.Size != SizeSanhao {
			t.Errorf("%s 应为三号，实际 %q", role, st.Size)
		}
	}

	dn := s.MustStyle(docmodel.RoleDocNumber)
	if dn.Font != FontFangSong || dn.Size != SizeSanhao {
		t.Errorf("发文字号应为 3 号仿宋，实际 %q/%q", dn.Font, dn.Size)
	}
	if dn.Align != AlignCenter {
		t.Errorf("发文字号应居中，实际 %q", dn.Align)
	}
}

// TestMastheadIsRedPerStandard covers §7.2.4: 发文机关标志用红色小标宋体字.
func TestMastheadIsRedPerStandard(t *testing.T) {
	s := Default()
	m := s.MustStyle(docmodel.RoleMasthead)
	if m.Color != ColorRed {
		t.Errorf("发文机关标志应为红色，实际 %#v", m.Color)
	}
	if m.Font != FontXiaoBiaoSong {
		t.Errorf("发文机关标志应为小标宋体，实际 %q", m.Font)
	}
	if m.Align != AlignCenter {
		t.Errorf("发文机关标志应居中，实际 %q", m.Align)
	}
}

// TestRedLineIsRedPerStandard covers §7.2.6: 发文字号之下 4mm 处的红色分隔线.
func TestRedLineIsRedPerStandard(t *testing.T) {
	s := Default()
	rl := s.MustStyle(docmodel.RoleRedLine)
	if rl.BorderBottom == nil {
		t.Fatal("红色分隔线应有下边框")
	}
	if rl.BorderBottom.Color != ColorRed {
		t.Errorf("分隔线应为红色，实际 %q", rl.BorderBottom.Color)
	}
	if rl.BorderBottom.SizeEighthPT <= 0 {
		t.Errorf("分隔线线宽应为正数，实际 %d", rl.BorderBottom.SizeEighthPT)
	}
}

// TestSignatureAndDatePerStandard covers §7.3.5: 发文机关署名右空二字,
// 成文日期右空四字.
func TestSignatureAndDatePerStandard(t *testing.T) {
	s := Default()

	sig := s.MustStyle(docmodel.RoleSignature)
	if sig.RightIndentChars != 2 {
		t.Errorf("发文机关署名应右空二字，实际 %.1f", sig.RightIndentChars)
	}
	if sig.Align != AlignRight {
		t.Errorf("发文机关署名应右对齐，实际 %q", sig.Align)
	}

	date := s.MustStyle(docmodel.RoleDate)
	if date.RightIndentChars != 4 {
		t.Errorf("成文日期应右空四字，实际 %.1f", date.RightIndentChars)
	}
	if date.Align != AlignRight {
		t.Errorf("成文日期应右对齐，实际 %q", date.Align)
	}
}

// TestRecipientAndNotePerStandard covers §7.3.2 (主送机关顶格) and §7.3.7
// (附注左空二字).
func TestRecipientAndNotePerStandard(t *testing.T) {
	s := Default()

	rc := s.MustStyle(docmodel.RoleRecipient)
	if rc.FirstLineIndentChars != 0 {
		t.Errorf("主送机关应顶格，实际缩进 %.1f 字", rc.FirstLineIndentChars)
	}
	if rc.Align != AlignLeft {
		t.Errorf("主送机关应左对齐，实际 %q", rc.Align)
	}

	note := s.MustStyle(docmodel.RoleNote)
	if note.LeftIndentChars != 2 {
		t.Errorf("附注应左空二字，实际 %.1f", note.LeftIndentChars)
	}
}

// TestAttachmentNoteIndent covers §7.3.4: 附件说明左空二字.
func TestAttachmentNoteIndent(t *testing.T) {
	s := Default()
	an := s.MustStyle(docmodel.RoleAttachmentNote)
	if an.LeftIndentChars != 2 {
		t.Errorf("附件说明应左空二字，实际 %.1f", an.LeftIndentChars)
	}
	if an.SpaceBeforeLines != 100 {
		t.Errorf("附件说明应在正文下空一行，实际 %.0f", an.SpaceBeforeLines)
	}
}

// TestAttachmentStartsNewPage covers §7.4: 附件另面编排.
func TestAttachmentStartsNewPage(t *testing.T) {
	s := Default()
	att := s.MustStyle(docmodel.RoleAttachment)
	if !att.PageBreakBefore {
		t.Error("附件应另起一页")
	}
}

// TestPageNumberPerStandard covers §7.3.6: 页码用4号半角宋体阿拉伯数字，
// 数字左右各放一条一字线.
func TestPageNumberPerStandard(t *testing.T) {
	s := Default()
	if s.PageNumberSize != SizeSihao {
		t.Errorf("页码应为四号，实际 %q", s.PageNumberSize)
	}
	if s.PageNumberSize.Points() != 14 {
		t.Errorf("四号应为 14pt，实际 %.1f", s.PageNumberSize.Points())
	}
	if s.PageNumberFont != FontSongti {
		t.Errorf("页码应为宋体，实际 %q", s.PageNumberFont)
	}
	if s.PageNumberDash == "" {
		t.Error("页码两侧应有一字线")
	}
}

// TestBanjiPerStandard covers §7.5: 版记中的字体一般用4号仿宋，
// 上下各加一条与版心等宽的线.
func TestBanjiPerStandard(t *testing.T) {
	s := Default()

	cc := s.MustStyle(docmodel.RoleCC)
	if cc.Size != SizeSihao {
		t.Errorf("抄送机关应为四号，实际 %q", cc.Size)
	}
	if cc.BorderTop == nil {
		t.Error("抄送机关上方应有分隔线")
	}

	pr := s.MustStyle(docmodel.RolePrinter)
	if pr.Size != SizeSihao {
		t.Errorf("印发机关应为四号，实际 %q", pr.Size)
	}
	if pr.BorderBottom == nil {
		t.Error("印发机关下方应有分隔线")
	}
}

// ---------------------------------------------------------------- 完整性

// TestDefaultSpecIsValid is the gate every renderer relies on: if the default
// table is incomplete, nothing downstream can be trusted.
func TestDefaultSpecIsValid(t *testing.T) {
	problems := ValidateSpec(Default())
	if len(problems) > 0 {
		t.Fatalf("默认格式规范不完整:\n  %s", strings.Join(problems, "\n  "))
	}
}

// TestEveryRoleHasAStyle is the specific gap that would otherwise surface as
// a panic or a silently wrong font at render time.
func TestEveryRoleHasAStyle(t *testing.T) {
	s := Default()
	for _, role := range docmodel.AllRoles() {
		st, ok := s.Style(role)
		if !ok {
			t.Errorf("缺少角色 %q 的样式", role)
			continue
		}
		if st.Role != role {
			t.Errorf("角色 %q 的样式 Role 字段 = %q", role, st.Role)
		}
		if st.Name == "" {
			t.Errorf("角色 %q 缺少中文名称", role)
		}
		if st.Font == "" || st.Size == "" || st.Align == "" {
			t.Errorf("角色 %q 的字体/字号/对齐不完整", role)
		}
		if st.Standard == "" {
			t.Errorf("角色 %q 未标注对应的国标条款", role)
		}
	}
}

// TestValidateSpecCatchesMissingRole: removing a style must be detected, not
// tolerated by falling back silently.
func TestValidateSpecCatchesMissingRole(t *testing.T) {
	s := Default()
	delete(s.Styles, docmodel.RoleBody)
	problems := ValidateSpec(s)
	if len(problems) == 0 {
		t.Fatal("缺少正文样式应被检出")
	}
	found := false
	for _, p := range problems {
		if strings.Contains(p, string(docmodel.RoleBody)) {
			found = true
		}
	}
	if !found {
		t.Errorf("问题列表未指明缺失的正文样式: %v", problems)
	}
}

func TestValidateSpecCatchesBadGeometry(t *testing.T) {
	s := Default()
	s.Page.MarginTopMM = 200
	s.Page.MarginBottomMM = 200
	problems := ValidateSpec(s)
	if len(problems) == 0 {
		t.Fatal("非法页边距应被检出")
	}
}

func TestValidateSpecCatchesUnknownSize(t *testing.T) {
	s := Default()
	st := s.Styles[docmodel.RoleBody]
	st.Size = "三號"
	s.Styles[docmodel.RoleBody] = st
	problems := ValidateSpec(s)
	if len(problems) == 0 {
		t.Fatal("未知字号应被检出")
	}
}

func TestValidateSpecCatchesBadColor(t *testing.T) {
	s := Default()
	st := s.Styles[docmodel.RoleMasthead]
	st.Color = "red"
	s.Styles[docmodel.RoleMasthead] = st
	problems := ValidateSpec(s)
	if len(problems) == 0 {
		t.Fatal("非法颜色应被检出")
	}
}

func TestValidateSpecCatchesNegativeIndent(t *testing.T) {
	s := Default()
	st := s.Styles[docmodel.RoleBody]
	st.FirstLineIndentChars = -2
	s.Styles[docmodel.RoleBody] = st
	if len(ValidateSpec(s)) == 0 {
		t.Fatal("负缩进应被检出")
	}
}

// TestValidateSpecCatchesExactRuleWithoutSpacing: 固定行距 without a value
// would collapse every line in the document.
func TestValidateSpecCatchesExactRuleWithoutSpacing(t *testing.T) {
	s := Default()
	st := s.Styles[docmodel.RoleBody]
	st.LineRule = LineExact
	st.LineSpacingPT = 0
	s.Styles[docmodel.RoleBody] = st
	if len(ValidateSpec(s)) == 0 {
		t.Fatal("固定行距未给值应被检出")
	}
}

// ---------------------------------------------------------------- 字体

// TestFontChains: every 公文 typeface must have a fallback, so a machine
// without 仿宋_GB2312 still renders something faithful rather than a default
// sans-serif.
func TestFontChains(t *testing.T) {
	for _, f := range []string{FontFangSong, FontHeiti, FontKaiti, FontXiaoBiaoSong, FontSongti, FontLatin} {
		chain := FontChain(f)
		if len(chain) == 0 {
			t.Errorf("%s 缺少回退链", f)
			continue
		}
		if chain[0] != f {
			t.Errorf("%s 的回退链首项应为自身，实际 %q", f, chain[0])
		}
	}
	// An unknown font yields itself rather than nothing.
	if got := FontChain("某字体"); len(got) != 1 || got[0] != "某字体" {
		t.Errorf("未知字体应返回自身，实际 %v", got)
	}
}

// TestFallbackChainsEndInGeneric: the last resort must be a CSS generic
// family. Falling through to a browser default would render Chinese text in a
// sans face, making a correct 公文 look wrong on screen.
func TestFallbackChainsEndInGeneric(t *testing.T) {
	for _, f := range []string{FontFangSong, FontHeiti, FontKaiti, FontXiaoBiaoSong, FontSongti, FontLatin} {
		chain := FontChain(f)
		last := chain[len(chain)-1]
		if !genericFamilies[last] {
			t.Errorf("%s 的回退链末尾 %q 不是通用字体族", f, last)
		}
	}
}

// TestSerifFacesFallBackToSerif: 仿宋/楷体/小标宋 are all 宋体-family faces,
// so their chains must terminate in "serif" rather than a sans family — the
// distinction is visually obvious on screen.
func TestSerifFacesFallBackToSerif(t *testing.T) {
	for _, f := range []string{FontFangSong, FontKaiti, FontXiaoBiaoSong, FontSongti, FontLatin} {
		chain := FontChain(f)
		if last := chain[len(chain)-1]; last != "serif" {
			t.Errorf("%s 是宋体族字体，回退链末尾应为 serif，实际 %q", f, last)
		}
	}
	// 黑体 is the one sans face in the standard.
	chain := FontChain(FontHeiti)
	if last := chain[len(chain)-1]; last != "sans-serif" {
		t.Errorf("黑体回退链末尾应为 sans-serif，实际 %q", last)
	}
}

// ---------------------------------------------------------------- 其他

func TestMustStyleFallsBackToBody(t *testing.T) {
	s := Default()
	got := s.MustStyle(docmodel.Role("不存在的角色"))
	want := s.Styles[docmodel.RoleBody]
	if got.Role != want.Role {
		t.Errorf("未知角色应回退到正文样式，实际 %q", got.Role)
	}
}

func TestAlignValues(t *testing.T) {
	cases := []struct {
		a     Align
		ooxml string
		css   string
	}{
		{AlignLeft, "left", "left"},
		{AlignCenter, "center", "center"},
		{AlignRight, "right", "right"},
		{AlignJustify, "both", "justify"},
		{Align(""), "left", "left"},
	}
	for _, c := range cases {
		if got := c.a.OOXMLValue(); got != c.ooxml {
			t.Errorf("%q.OOXMLValue() = %q, 期望 %q", c.a, got, c.ooxml)
		}
		if got := c.a.CSSValue(); got != c.css {
			t.Errorf("%q.CSSValue() = %q, 期望 %q", c.a, got, c.css)
		}
	}
}

func TestDescribeIsReadable(t *testing.T) {
	s := Default()
	out := s.Describe()
	for _, want := range []string{"GB/T 9704-2012", "210", "297", "28.99", "仿宋"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe 输出缺少 %q", want)
		}
	}
}

// TestIsHex6 covers the colour validator directly.
func TestIsHex6(t *testing.T) {
	yes := []string{"FF0000", "000000", "abcdef", "AbCdEf"}
	no := []string{"", "FFF", "FF00000", "#FF0000", "GGGGGG", "ff 000"}
	for _, s := range yes {
		if !isHex6(s) {
			t.Errorf("isHex6(%q) 应为 true", s)
		}
	}
	for _, s := range no {
		if isHex6(s) {
			t.Errorf("isHex6(%q) 应为 false", s)
		}
	}
}
