package gbstandard

import (
	"strings"
	"testing"
)

func TestDefaultOutlineIsGovDocNumbering(t *testing.T) {
	o := DefaultOutline()
	for level, want := range map[int]string{
		1: "一、", 2: "（一）", 3: "1.", 4: "（1）",
	} {
		if got := o.HeadingPrefix(level, 1); got != want {
			t.Errorf("level %d prefix = %q, want %q", level, got, want)
		}
	}
	// The counters must actually advance, which a format-string design with a
	// literal numeral could not do.
	if got := o.HeadingPrefix(1, 2); got != "二、" {
		t.Errorf("level 1 counter 2 = %q, want 二、", got)
	}
	if got := o.HeadingPrefix(3, 11); got != "11." {
		t.Errorf("level 3 counter 11 = %q, want 11.", got)
	}
}

func TestOutlineCanBeChangedByConfiguration(t *testing.T) {
	// A thesis-style outline numbered 第一章 / 1.1 must be expressible as data.
	o := Outline{
		HeadingForms: []HeadingForm{
			{Numeral: "chinese", Prefix: "第", Suffix: "章"},
			{Numeral: "arabic", Prefix: "", Suffix: ""},
		},
		MaxDepth: 2,
	}
	if got := o.HeadingPrefix(1, 3); got != "第三章" {
		t.Errorf("chapter prefix = %q, want 第三章", got)
	}
	if got := o.HeadingPrefix(2, 4); got != "4" {
		t.Errorf("section prefix = %q, want 4", got)
	}
	// Levels beyond MaxDepth must not produce a prefix.
	if got := o.HeadingPrefix(3, 1); got != "" {
		t.Errorf("level 3 prefix = %q, want empty", got)
	}
}

func TestOutlineNormalizesLegacyProfiles(t *testing.T) {
	// A profile saved before the outline existed has an empty value here. It
	// must fall back to 公文 rules rather than rendering unnumbered headings.
	var zero Outline
	n := zero.Normalized()
	if n.MaxDepth != 4 || len(n.HeadingForms) != 4 {
		t.Fatalf("normalized outline = %+v", n)
	}
	if got := n.HeadingPrefix(1, 1); got != "一、" {
		t.Errorf("prefix = %q, want 一、", got)
	}
	if !n.RequiresRecipient {
		t.Error("公文 outline should require a 主送机关")
	}
}

func TestValidateOutlineRejectsUnknownNumeral(t *testing.T) {
	o := Outline{
		HeadingForms: []HeadingForm{{Numeral: "roman", Suffix: "."}},
		MaxDepth:     1,
	}
	problems := ValidateOutline(o)
	if len(problems) == 0 {
		t.Fatal("an unknown numeral system must be reported")
	}
	if !strings.Contains(strings.Join(problems, " "), "roman") {
		t.Errorf("problems = %v, want the bad value named", problems)
	}
}

func TestSpecCarriesOutline(t *testing.T) {
	// Attaching the outline to Spec is what makes a document type one object.
	if s := Default(); s.OutlineOf().MaxDepth == 0 {
		t.Error("Default() lost its outline")
	}
	g := Generic()
	if g.OutlineOf().RequiresRecipient {
		t.Error("generic documents should not require a 主送机关")
	}
}
