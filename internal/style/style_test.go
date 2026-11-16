package style

import (
	"strings"
	"testing"
)

func TestAssessSeparatesRegisters(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Tier
	}{
		{
			name: "公文腔",
			text: "为贯彻落实上级部署，现将有关事项通知如下。请遵照执行。",
			want: TierFormal,
		},
		{
			name: "口语",
			text: "咱们得赶紧把这个事儿弄好，然后跟大家都说一下，差不多吧。",
			want: TierColloquial,
		},
		{
			name: "普通书面",
			text: "本次检查覆盖三个片区，发现问题主要集中在台账管理方面。",
			want: TierMixed,
		},
	}
	for _, c := range cases {
		if got := Assess(c.text).Tier; got != c.want {
			t.Errorf("%s: Assess(%q).Tier = %s, want %s (score=%d)",
				c.name, c.text, got, c.want, Assess(c.text).Score)
		}
	}
}

func TestAssessLongRunOnParagraphIsColloquial(t *testing.T) {
	// One unbroken paragraph with no official connectives: the exact shape the
	// tool exists for, and the one that must get the largest budget.
	text := strings.Repeat("这个事儿大家都得上心，然后各科室把台账整理一下，有啥问题赶紧报上来。", 4)
	a := Assess(text)
	if a.Tier != TierColloquial {
		t.Fatalf("Tier = %s (score %d), want colloquial", a.Tier, a.Score)
	}
	if a.Sentences < 4 {
		t.Errorf("Sentences = %d, expected the clauses to be counted", a.Sentences)
	}
	// The len-based signals must not fire on this text: it is colloquial
	// because of its vocabulary, and that channel has to stand on its own.
	if a.LongestSentence > 60 {
		t.Errorf("LongestSentence = %d, each clause is short", a.LongestSentence)
	}
}

func TestLimitsMonotonic(t *testing.T) {
	l := DefaultLimits()
	if !(l.MaxFor(TierFormal) < l.MaxFor(TierMixed) &&
		l.MaxFor(TierMixed) < l.MaxFor(TierColloquial)) {
		t.Fatalf("budgets must grow with colloquialism: %d %d %d",
			l.MaxFor(TierFormal), l.MaxFor(TierMixed), l.MaxFor(TierColloquial))
	}
}

func TestDistanceIdentityAndRewrite(t *testing.T) {
	src := "为贯彻落实上级部署，现将有关事项通知如下。"
	if d := Distance(src, src); d != 0 {
		t.Errorf("identical texts: Distance = %v, want 0", d)
	}
	// Reflow (spaces removed/re-added) must not count as a change.
	if d := Distance(src, "为贯彻落实上级部署，\n现将有关事项通知如下。"); d != 0 {
		t.Errorf("whitespace-only difference: Distance = %v, want 0", d)
	}

	// Replacing the whole sentence must land at the maximum.
	replaced := "各地各部门要高度重视，确保政策落地见效。"
	if d := Distance(src, replaced); d < 80 {
		t.Errorf("full replacement: Distance = %v, want near 100", d)
	}
}

func TestDistanceToleratesLightEditing(t *testing.T) {
	src := "为贯彻落实上级部署，现将有关事项通知如下。请各单位认真执行。"
	// A typo fix plus one reworded clause: a legitimate small change.
	out := "为贯彻落实上级部署，现将有关事项通知如下。请各单位认真贯彻执行。"
	d := Distance(src, out)
	if d > 25 {
		t.Errorf("light edit: Distance = %v, want under the formal budget", d)
	}
}

func TestSplitSentencesKeepsClosers(t *testing.T) {
	got := SplitSentences("他说：“第一，要抓紧。”第二，要落实。")
	if len(got) != 2 {
		t.Fatalf("got %d sentences: %q", len(got), got)
	}
	if !strings.HasSuffix(got[0], "”") {
		t.Errorf("first sentence lost its closing quote: %q", got[0])
	}
	if !strings.HasPrefix(got[1], "第二") {
		t.Errorf("second sentence = %q, want it to start with 第二", got[1])
	}
}

func TestSplitSentencesHandlesSemicolonRun(t *testing.T) {
	got := SplitSentences("一是加强领导；二是压实责任；三是强化督查。")
	if len(got) != 3 {
		t.Fatalf("got %d clauses: %q", len(got), got)
	}
}

func TestFactsFindsHardValues(t *testing.T) {
	text := "根据国办发〔2024〕15号，自2024年5月1日起，补助标准提高至30%。"
	facts := Facts(text)
	var hard []string
	for _, f := range facts {
		if f.Hard {
			hard = append(hard, f.Kind+":"+f.Text)
		}
	}
	for _, want := range []string{"docno:国办发〔2024〕15号", "date:2024年5月1日"} {
		found := false
		for _, h := range hard {
			if h == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing hard fact %q in %v", want, hard)
		}
	}
	if !strings.Contains(strings.Join(hard, "|"), "30%") {
		t.Errorf("percentage was not captured as a hard fact: %v", hard)
	}
}

func TestMissingFactsDetectsLostValues(t *testing.T) {
	src := "自2024年5月1日起，补助标准提高至30%。"
	if m := MissingFacts(src, "自2024年5月1日起，补助标准提高至30%。"); len(m) != 0 {
		t.Errorf("unchanged text reported %v", m)
	}
	// A rewrite that drops the date must be caught.
	missing := MissingFacts(src, "补助标准有所提高。")
	if len(missing) == 0 {
		t.Fatal("dropping the date and the percentage was not detected")
	}
	// Whitespace differences must not be treated as a loss.
	if m := MissingFacts("2024 年 5 月 1 日", "2024年5月1日"); len(m) != 0 {
		t.Errorf("reflow reported %v", m)
	}
}
