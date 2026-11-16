package gbstandard

import (
	"fmt"
	"strings"

	"govdoc/internal/docmodel"
)

// Outline describes the *structural* rules of a document type, as opposed to
// the typographic ones in Spec.
//
// # Why this is configuration and not prompt text
//
// A 公文 is not defined only by its fonts. It is defined by having a 标题, a
// 主送机关, an unnumbered 正文 preamble, numbered 层次序数 chapters, a 发文机关署名
// and an 成文日期. A 报告 has a different set; a thesis has another.
//
// Those rules used to live in the model prompt as hard-coded Chinese, which
// meant adding a document type was a code change and every document was
// organised as if it were a 公文. Moving them into the profile makes the
// document type a data choice: the prompt is generated from whichever profile
// the document uses, and the depth/heading form follow it too.
type Outline struct {
	// RequiredElements are the slots a document of this type is expected to
	// fill. A missing one is reported to the reviewer but does not block
	// export: many real documents legitimately omit 抄送 or 份号.
	RequiredElements []docmodel.Role `json:"required_elements,omitempty"`
	// HeadingForms is the 层次序数 form for each depth, index 0 being level 1.
	// A 公文 uses 一、 / （一） / 1. / （1）.
	HeadingForms []HeadingForm `json:"heading_forms,omitempty"`
	// MaxDepth bounds the heading tree.
	MaxDepth int `json:"max_depth"`
	// Preamble is whether an unnumbered opening paragraph under 主送机关 is
	// expected.
	Preamble bool `json:"preamble"`
	// RequiresRecipient is whether 主送机关 is mandatory.
	RequiresRecipient bool `json:"requires_recipient"`
	// Tone is the register the document should be written in, handed to the
	// model as guidance for how far a rewrite may go.
	Tone string `json:"tone,omitempty"`
	// Guidance is free-form instructions appended to the prompt for this
	// document type. It lets a user add a house rule without a code change.
	Guidance string `json:"guidance,omitempty"`
}

// HeadingForm is one heading numbering style.
//
// It is expressed as numeral system plus affixes rather than as a format string
// so the settings page can show it as "中文数字 + 「、」" instead of "%s、" —
// the people who use this tool are not programmers, and a form they cannot read
// is a form they cannot safely edit.
type HeadingForm struct {
	// Numeral is "chinese" (一、二、) or "arabic" (1、2).
	Numeral string `json:"numeral"`
	// Prefix is printed before the numeral, e.g. "（".
	Prefix string `json:"prefix,omitempty"`
	// Suffix is printed after the numeral, e.g. "、".
	Suffix string `json:"suffix,omitempty"`
}

// Render produces the prefix for counter n.
func (f HeadingForm) Render(n int) string {
	num := chineseNumeralText(n)
	if f.Numeral == "arabic" {
		num = fmt.Sprintf("%d", n)
	}
	return f.Prefix + num + f.Suffix
}

// Describe renders the form for the settings page.
func (f HeadingForm) Describe() string {
	system := "中文数字"
	if f.Numeral == "arabic" {
		system = "阿拉伯数字"
	}
	switch {
	case f.Prefix == "" && f.Suffix == "":
		return system
	default:
		return fmt.Sprintf("%s，前「%s」后「%s」", system, f.Prefix, f.Suffix)
	}
}

// DefaultOutline is the 公文 outline, used when a stored profile predates the
// field or leaves it empty.
func DefaultOutline() Outline {
	return Outline{
		RequiredElements: []docmodel.Role{docmodel.RoleTitle, docmodel.RoleRecipient, docmodel.RoleDate},
		HeadingForms: []HeadingForm{
			{Numeral: "chinese", Suffix: "、"},
			{Numeral: "chinese", Prefix: "（", Suffix: "）"},
			{Numeral: "arabic", Suffix: "."},
			{Numeral: "arabic", Prefix: "（", Suffix: "）"},
		},
		MaxDepth:          4,
		Preamble:          true,
		RequiresRecipient: true,
		Tone:              "庄重、简明、规范的公文语体；不口语化，不使用第一人称叙述，不添加原文没有的事实与承诺",
	}
}

// Normalized fills empty fields from the default, so a profile written before a
// field existed still validates and renders.
func (o Outline) Normalized() Outline {
	d := DefaultOutline()
	if len(o.HeadingForms) == 0 {
		o.HeadingForms = d.HeadingForms
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = d.MaxDepth
	}
	if o.MaxDepth > len(o.HeadingForms) {
		o.MaxDepth = len(o.HeadingForms)
	}
	if strings.TrimSpace(o.Tone) == "" {
		o.Tone = d.Tone
	}
	// Booleans cannot be "empty", so a profile written before the field
	// existed would read as an explicit false. Requiring a 主送机关 is the
	// safe default: it is easier to delete a slot the user does not need than
	// to notice one that should have been required and was not.
	if !o.RequiresRecipient && len(o.RequiredElements) == 0 {
		o.RequiresRecipient = d.RequiresRecipient
		o.Preamble = d.Preamble
	}
	return o
}

// ValidateOutline reports problems that would make the outline unusable.
func ValidateOutline(o Outline) []string {
	var problems []string
	o = o.Normalized()
	for i, f := range o.HeadingForms {
		switch f.Numeral {
		case "", "chinese", "arabic":
		default:
			problems = append(problems,
				fmt.Sprintf("第 %d 级标题的编号方式 %q 无法识别（应为 chinese 或 arabic）", i+1, f.Numeral))
		}
	}
	for _, r := range o.RequiredElements {
		allowed := false
		for _, b := range docmodel.BindableRoles() {
			if b == r {
				allowed = true
				break
			}
		}
		if !allowed {
			problems = append(problems, fmt.Sprintf("要素 %q 不能由文档绑定", r))
		}
	}
	if o.MaxDepth < 1 {
		problems = append(problems, "最大标题层级至少为 1")
	}
	// The style table defines heading1 .. heading4. A deeper tree has no
	// heading role to render into, so the assembler would silently fall back to
	// body style. Refuse the configuration instead of producing flat chapters.
	if o.MaxDepth > docmodel.MaxHeadingDepth {
		problems = append(problems, fmt.Sprintf(
			"最大标题层级为 %d，但格式标准只定义了 %d 级标题样式",
			o.MaxDepth, docmodel.MaxHeadingDepth))
	}
	return problems
}

// HeadingPrefix returns the 层次序数 prefix for a 1-based level and a counter.
//
// The form comes from the outline so a non-公文 document can number its
// chapters differently ("第一章" / "1.1") without touching the assembler.
func (o Outline) HeadingPrefix(level, n int) string {
	o = o.Normalized()
	if level < 1 || level > len(o.HeadingForms) {
		return ""
	}
	return o.HeadingForms[level-1].Render(n)
}

// chineseNumeralText converts 1..99 to 一..九十九.
func chineseNumeralText(n int) string {
	if n <= 0 {
		return "一"
	}
	digits := []rune("零一二三四五六七八九")
	switch {
	case n < 10:
		return string(digits[n])
	case n == 10:
		return "十"
	case n < 20:
		return "十" + string(digits[n-10])
	case n < 100:
		s := string(digits[n/10]) + "十"
		if r := n % 10; r != 0 {
			s += string(digits[r])
		}
		return s
	default:
		return fmt.Sprintf("%d", n)
	}
}

// OutlineOf returns the outline of a spec, falling back to the 公文 default.
//
// Spec carries the outline so the two cannot be separated: a document renders
// and is organised by one object, not two that could be paired wrongly.
func (s Spec) OutlineOf() Outline {
	return s.Outline.Normalized()
}

// DescribeOutline renders the structural rules for the settings page.
func (o Outline) Describe() string {
	o = o.Normalized()
	var elems []string
	for _, r := range o.RequiredElements {
		elems = append(elems, string(r))
	}
	required := "无"
	if len(elems) > 0 {
		required = strings.Join(elems, "、")
	}
	return fmt.Sprintf("必需要素：%s；标题层级：%s；最多 %d 级",
		required, describeForms(o.HeadingForms[:o.MaxDepth]), o.MaxDepth)
}

func describeForms(forms []HeadingForm) string {
	var out []string
	for i, f := range forms {
		out = append(out, fmt.Sprintf("%d级 %s", i+1, f.Describe()))
	}
	return strings.Join(out, "；")
}
