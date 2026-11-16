package docmodel

import (
	"testing"
)

func rewriteFixture() (*Document, *Plan, *Doc) {
	src := "为贯彻落实上级部署，现将有关事项通知如下。请各单位认真执行。"
	doc := &Document{Nodes: []Node{
		{ID: "n0001", Kind: KindParagraph, Order: 0, Text: src},
	}}
	plan := &Plan{
		Sections: []Section{{
			ID: "s1", Title: "一、总体要求", Level: 1,
			BodyParts: []BodyPart{{NodeID: "n0001", Part: 0}},
		}},
		Recompose: []Recompose{{
			NodeID: "n0001",
			Parts:  []string{src},
			Reason: "原样保留",
		}},
		RewriteLimits: DefaultRewriteLimits(),
	}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleHeading1, Text: "总体要求", Level: 1, AutoNumber: "一、"},
		{Role: RoleBody, Text: src, NodeID: "n0001"},
	}}
	return doc, plan, out
}

func TestRewriteThatReplacesTextIsRefused(t *testing.T) {
	doc, plan, out := rewriteFixture()

	// The model claims a rewrite but replaces the sentence with prose of its
	// own. Before the budget existed this passed every check, because the
	// verifier compared the output with the model's own Parts.
	plan.Recompose[0].Parts = []string{"各地各部门要高度重视，确保政策落地见效。"}
	out.Blocks[1].Text = "各地各部门要高度重视，确保政策落地见效。"

	diff := VerifyVerbatim(doc, out, plan)
	if len(diff.Rewrites) != 1 {
		t.Fatalf("expected one rewrite check, got %d", len(diff.Rewrites))
	}
	check := diff.Rewrites[0]
	if !check.OverBudget {
		t.Errorf("change %d%% was accepted against a %d%% budget", check.Change, check.Budget)
	}
	if diff.Clean() {
		t.Error("a wholesale replacement must not pass the export gate")
	}
	if len(diff.RewriteFailures()) == 0 {
		t.Error("RewriteFailures must explain why the export is refused")
	}
}

func TestRewriteDroppingNumbersIsRefused(t *testing.T) {
	src := "自2024年5月1日起，补助标准由10%提高至30%。"
	doc := &Document{Nodes: []Node{{ID: "n0001", Kind: KindParagraph, Text: src}}}
	// A rewrite that keeps the gist but loses two hard facts.
	rewritten := "补助标准已作出调整，请各单位按新标准执行。"
	plan := &Plan{
		Sections: []Section{{
			ID: "s1", Title: "一、补助标准", Level: 1,
			BodyParts: []BodyPart{{NodeID: "n0001", Part: 0}},
		}},
		Recompose: []Recompose{{
			NodeID: "n0001", Parts: []string{rewritten}, Reason: "精简表述",
		}},
		RewriteLimits: DefaultRewriteLimits(),
	}
	out := &Doc{Blocks: []OutBlock{
		{Role: RoleBody, Text: rewritten, NodeID: "n0001"},
	}}

	diff := VerifyVerbatim(doc, out, plan)
	if diff.Clean() {
		t.Fatal("dropping a date and a percentage must not pass")
	}
	var hard []string
	for _, f := range diff.Rewrites[0].MissingFacts {
		if f.Hard {
			hard = append(hard, f.Text)
		}
	}
	if len(hard) == 0 {
		t.Fatalf("expected hard facts to be reported, got %v", diff.Rewrites[0].MissingFacts)
	}
}

func TestRewriteWithinBudgetPasses(t *testing.T) {
	doc, plan, out := rewriteFixture()

	// A rewrite that keeps nearly every character: fixing a clumsy ending.
	edited := "为贯彻落实上级部署，现将有关事项通知如下。请各单位认真贯彻执行。"
	plan.Recompose[0].Parts = []string{edited}
	out.Blocks[1].Text = edited

	diff := VerifyVerbatim(doc, out, plan)
	if diff.Rewrites[0].OverBudget {
		t.Fatalf("change %d%% exceeded budget %d%% for tier %s",
			diff.Rewrites[0].Change, diff.Rewrites[0].Budget, diff.Rewrites[0].Tier)
	}
	if !diff.Clean() {
		t.Fatalf("a small rewrite should pass, failures: %v", diff.RewriteFailures())
	}
}

func TestFormalSourceGetsTighterBudget(t *testing.T) {
	formal, _, _ := rewriteFixture()
	l := DefaultRewriteLimits()
	if l.MaxFor("formal") >= l.MaxFor("colloquial") {
		t.Fatalf("formal budget %d must be tighter than colloquial %d",
			l.MaxFor("formal"), l.MaxFor("colloquial"))
	}
	if got := l.MaxFor("formal"); got != 15 {
		t.Errorf("formal budget = %d, want 15", got)
	}
	_ = formal
}

func TestColloquialSourceAllowsRealRewrite(t *testing.T) {
	src := "咱们得赶紧把这个事儿弄好，然后跟大伙儿说一下，别拖。"
	// A spoken paragraph rewritten into 公文 keeps the content but replaces the
	// vocabulary. That is the work the user asked for, so the colloquial tier
	// has to tolerate it — the guard against over-reach there is the facts and
	// the reviewer, not the character budget alone.
	rewritten := "请各单位抓紧办理并及时通报。"
	doc := &Document{Nodes: []Node{{ID: "n0001", Kind: KindParagraph, Text: src}}}
	plan := &Plan{
		Sections: []Section{{
			ID: "s1", Title: "一、办理要求", Level: 1,
			BodyParts: []BodyPart{{NodeID: "n0001", Part: 0}},
		}},
		Recompose: []Recompose{{
			NodeID: "n0001", Parts: []string{rewritten}, Reason: "改为书面表述",
		}},
		RewriteLimits: DefaultRewriteLimits(),
	}
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, Text: rewritten, NodeID: "n0001"}}}

	diff := VerifyVerbatim(doc, out, plan)
	check := diff.Rewrites[0]
	if check.Tier != "colloquial" {
		t.Errorf("tier = %s, want colloquial (score-implied)", check.Tier)
	}
	if check.OverBudget {
		t.Errorf("a spoken paragraph rewritten into 公文 was refused: %d%% vs %d%%",
			check.Change, check.Budget)
	}
}

func TestColloquialTierStillRefusesInventedNumbers(t *testing.T) {
	// The colloquial budget is effectively unlimited, so the fact check is the
	// only thing standing between a rewrite and a fabricated figure. This is
	// the guard that makes that trade-off safe.
	src := "咱们得赶紧把补助标准提上去，别拖。"
	rewritten := "请抓紧将补助标准提高至50%。"
	doc := &Document{Nodes: []Node{{ID: "n0001", Kind: KindParagraph, Text: src}}}
	plan := &Plan{
		Sections: []Section{{
			ID: "s1", Title: "一、补助标准", Level: 1,
			BodyParts: []BodyPart{{NodeID: "n0001", Part: 0}},
		}},
		Recompose: []Recompose{{
			NodeID: "n0001", Parts: []string{rewritten}, Reason: "改为书面表述",
		}},
		RewriteLimits: DefaultRewriteLimits(),
	}
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, Text: rewritten, NodeID: "n0001"}}}

	diff := VerifyVerbatim(doc, out, plan)
	if diff.Clean() {
		t.Fatal("inventing the figure 50% must not pass the export gate")
	}
	if len(diff.Rewrites[0].InventedFacts) == 0 {
		t.Error("the invented figure was not reported")
	}
}

// A per-node edit is the other way to replace a paragraph. It is declared, its
// Before is found, and the output matches it, so every earlier check passes —
// while the budget never sees it. The verifier must measure this path too.
func TestWholeParagraphEditCannotBypassBudget(t *testing.T) {
	src := "为贯彻落实上级部署，现将有关事项通知如下。请各单位认真执行，确保各项任务按期完成。"
	replacement := "各单位要高度重视，统筹推进，狠抓落实，确保工作取得实效。"
	doc := &Document{Nodes: []Node{{ID: "n0001", Kind: KindParagraph, Text: src}}}
	plan := &Plan{
		Sections: []Section{{ID: "s1", Title: "总体要求", Level: 1, BodyNodeIDs: []string{"n0001"}}},
		Edits: []Edit{{
			NodeID: "n0001", Kind: EditWording,
			Before: src, After: replacement, Reason: "整段润色",
		}},
		RewriteLimits: DefaultRewriteLimits(),
	}
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, Text: replacement, NodeID: "n0001"}}}

	diff := VerifyVerbatim(doc, out, plan)
	if len(diff.Rewrites) != 1 {
		t.Fatalf("a declared rewrite must be measured: got %d checks", len(diff.Rewrites))
	}
	if !diff.Rewrites[0].OverBudget {
		t.Errorf("change %d%% passed a %d%% budget",
			diff.Rewrites[0].Change, diff.Rewrites[0].Budget)
	}
	if diff.Clean() {
		t.Error("replacing a formal paragraph through an edit must not pass the export gate")
	}
}

// Fixing several typos in one short paragraph touches a large share of its
// characters, but it is not a rewrite. Measuring every edit against the budget
// would reject the least controversial thing the tool does.
func TestSeveralTypoEditsAreNotTreatedAsRewrite(t *testing.T) {
	src := "按排好工做，认真负则。"
	fixed := "安排好工作，认真负责。"
	doc := &Document{Nodes: []Node{{ID: "n0001", Kind: KindParagraph, Text: src}}}
	plan := &Plan{
		Sections: []Section{{ID: "s1", Title: "要求", Level: 1, BodyNodeIDs: []string{"n0001"}}},
		Edits: []Edit{
			{NodeID: "n0001", Kind: EditTypo, Before: "按排", After: "安排"},
			{NodeID: "n0001", Kind: EditTypo, Before: "工做", After: "工作"},
			{NodeID: "n0001", Kind: EditTypo, Before: "负则", After: "负责"},
		},
		RewriteLimits: DefaultRewriteLimits(),
	}
	out := &Doc{Blocks: []OutBlock{{Role: RoleBody, Text: fixed, NodeID: "n0001"}}}

	diff := VerifyVerbatim(doc, out, plan)
	if !diff.Clean() {
		t.Fatalf("逐字修正不应触发改写配额: %v", diff.RewriteFailures())
	}
}
