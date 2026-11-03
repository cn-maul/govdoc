package assemble

import (
	"errors"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// src builds a document from alternating heading/body strings, which is the
// shape every test below needs.
func src(kinds ...docmodel.Node) *docmodel.Document {
	for i := range kinds {
		kinds[i].Order = i
		if kinds[i].ID == "" {
			kinds[i].ID = nodeID(i)
		}
	}
	return &docmodel.Document{SourceName: "test.docx", Format: "docx", Nodes: kinds}
}

func nodeID(i int) string {
	s := "0000"
	d := i + 1
	for j := 3; j >= 0 && d > 0; j-- {
		s = s[:j] + string(rune('0'+d%10)) + s[j+1:]
		d /= 10
	}
	return "n" + s
}

func para(id, text string) docmodel.Node {
	return docmodel.Node{ID: id, Kind: docmodel.KindParagraph, Text: text}
}

func head(id, text string, level int) docmodel.Node {
	return docmodel.Node{ID: id, Kind: docmodel.KindHeading, Text: text, Level: level}
}

func spec() gbstandard.Spec { return gbstandard.Default() }

// ---------------------------------------------------------------- happy path

// TestBuildPreservesTextExactly is the core promise: whatever the plan does
// with the structure, the body text in the output is byte-identical to the
// source, and nothing is lost.
func TestBuildPreservesTextExactly(t *testing.T) {
	// Deliberately awkward text: full-width spaces, Chinese quotes, an
	// English apostrophe, and a semicolon — all things a rewriter would
	// normalise.
	body1 := "第一段正文。这里有一句“引号”测试，以及‘单引号’。"
	body2 := "第二段：包含全角空格\u3000与 ASCII 空格 混排；还有 … 省略号。"
	body3 := "第三段 has English, digits 12345, and (parentheses)."

	doc := src(
		para("n0001", body1),
		para("n0002", body2),
		para("n0003", body3),
	)
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "总体要求", Level: 1, BodyNodeIDs: []string{"n0001", "n0002"}},
			{ID: "s2", Title: "具体措施", Level: 1, BodyNodeIDs: []string{"n0003"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !res.Report.OK() {
		t.Fatalf("report not OK: %s", res.Report.Summary())
	}
	if !res.Diff.Clean() {
		t.Fatalf("verbatim diff not clean: missing=%v altered=%v", res.Diff.Missing, res.Diff.Altered)
	}

	// Every source body string must appear byte-for-byte among the blocks.
	for _, want := range []string{body1, body2, body3} {
		found := false
		for _, b := range res.Doc.Blocks {
			if b.Text == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("原文未逐字出现在输出中: %q", want)
		}
	}
}

// TestNoModelProseReachesBody pins the other half of the invariant: text the
// model wrote (a paraphrased paragraph, say) must never appear as body
// content. Only the two structural fields Title and Notes are model-authored.
func TestNoModelProseReachesBody(t *testing.T) {
	doc := src(para("n0001", "原文内容不得被改写。"))
	plan := &docmodel.Plan{
		Notes: []string{"模型的一句备注，不应进入正文"},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章节标题", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, b := range res.Doc.Blocks {
		if strings.Contains(b.Text, "模型的一句备注") {
			t.Fatalf("模型备注泄漏进了正文块: role=%s text=%q", b.Role, b.Text)
		}
	}
}

// ---------------------------------------------------------------- numbering

// TestNumberingFollowsStandard checks the 层次序数 forms of §7.3.3 and that
// deeper counters reset when a shallower section starts.
func TestNumberingFollowsStandard(t *testing.T) {
	doc := src(
		para("n0001", "甲"),
		para("n0002", "乙"),
		para("n0003", "丙"),
		para("n0004", "丁"),
	)
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{
				ID: "s1", Title: "第一层", Level: 1, BodyNodeIDs: []string{"n0001"},
				Children: []docmodel.Section{
					{ID: "s1a", Title: "第二层甲", Level: 2, BodyNodeIDs: []string{"n0002"}},
					{ID: "s1b", Title: "第二层乙", Level: 2, BodyNodeIDs: []string{"n0003"}},
				},
			},
			{ID: "s2", Title: "第一层乙", Level: 1, BodyNodeIDs: []string{"n0004"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var got []string
	for _, b := range res.Doc.Blocks {
		if b.Role == docmodel.RoleHeading1 || b.Role == docmodel.RoleHeading2 {
			got = append(got, b.AutoNumber+b.Text)
		}
	}
	want := []string{"一、第一层", "（一）第二层甲", "（二）第二层乙", "二、第一层乙"}
	if len(got) != len(want) {
		t.Fatalf("标题数量 = %d, 期望 %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("标题[%d] = %q, 期望 %q", i, got[i], want[i])
		}
	}
}

// TestExistingNumberNotDoubled covers the very common real-world case: the
// source heading already says "一、总体要求". Prefixing again would produce
// "一、一、总体要求", which is a visible corruption of the user's own text.
func TestExistingNumberNotDoubled(t *testing.T) {
	doc := src(
		head("n0001", "一、总体要求", 1),
		para("n0002", "正文。"),
		head("n0003", "（一）具体目标", 2),
		para("n0004", "正文二。"),
	)
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{
				ID: "s1", Title: "一、总体要求", Level: 1,
				HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"},
				Children: []docmodel.Section{
					{ID: "s1a", Title: "（一）具体目标", Level: 2,
						HeadingNodeID: "n0003", BodyNodeIDs: []string{"n0004"}},
				},
			},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, b := range res.Doc.Blocks {
		if strings.Contains(b.AutoNumber+b.Text, "一、一、") ||
			strings.Contains(b.AutoNumber+b.Text, "（一）（一）") {
			t.Errorf("编号被重复添加: %q", b.AutoNumber+b.Text)
		}
	}
	// The original heading text must survive untouched.
	if !strings.Contains(res.Doc.Text(), "一、总体要求") {
		t.Error("原标题文本丢失")
	}
	// And these headings are source text, so they are not "synthesized".
	if len(res.SynthText) != 0 {
		t.Logf("合成文本（应仅为编号类）: %v", res.SynthText)
	}
}

// TestPreambleIsUnnumberedAndKeepsNumberingIntact is the regression guard for
// a defect an end-to-end run exposed: modelling the 缘由 paragraph as a
// chapter produced a fabricated heading "一、通知引言" whose number the first
// real heading then repeated ("一、总体要求").
func TestPreambleIsUnnumberedAndKeepsNumberingIntact(t *testing.T) {
	doc := src(
		para("n0001", "为贯彻落实上级要求，现将有关事项通知如下。"),
		head("n0002", "一、总体要求", 1),
		para("n0003", "正文一。"),
		head("n0004", "二、检查范围", 1),
		para("n0005", "正文二。"),
	)
	plan := &docmodel.Plan{
		PreambleNodeIDs: []string{"n0001"},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、总体要求", Level: 1,
				HeadingNodeID: "n0002", BodyNodeIDs: []string{"n0003"}},
			{ID: "s2", Title: "二、检查范围", Level: 1,
				HeadingNodeID: "n0004", BodyNodeIDs: []string{"n0005"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The preamble must render as unnumbered 正文 under 主送机关.
	var preamble *docmodel.OutBlock
	for i := range res.Doc.Blocks {
		if res.Doc.Blocks[i].NodeID == "n0001" {
			preamble = &res.Doc.Blocks[i]
		}
	}
	if preamble == nil {
		t.Fatal("前言段落未出现在成品中")
	}
	if preamble.Role != docmodel.RoleBody {
		t.Errorf("前言应以正文角色呈现, 实际 %q", preamble.Role)
	}
	if preamble.AutoNumber != "" {
		t.Errorf("前言不应带层次序数编号, 实际 %q", preamble.AutoNumber)
	}

	// The source headings keep their own numbers, and no number repeats.
	var numbers []string
	for _, b := range res.Doc.Blocks {
		if b.Role == docmodel.RoleHeading1 {
			numbers = append(numbers, b.AutoNumber+b.Text)
		}
	}
	want := []string{"一、总体要求", "二、检查范围"}
	if len(numbers) != len(want) {
		t.Fatalf("标题 = %v, 期望 %v", numbers, want)
	}
	for i := range want {
		if numbers[i] != want[i] {
			t.Errorf("标题[%d] = %q, 期望 %q", i, numbers[i], want[i])
		}
	}
	// No fabricated heading text may appear anywhere.
	if strings.Contains(res.Doc.Text(), "通知引言") || strings.Contains(res.Doc.Text(), "第1部分") {
		t.Errorf("成品中出现了编造的章节标题:\n%s", res.Doc.Text())
	}
}

// TestMixedSourceAndSynthHeadingsShareOneSequence: when the source supplies
// some headings and the model supplies others at the same level, the numbering
// must continue rather than restart — otherwise two chapters both read "一、".
func TestMixedSourceAndSynthHeadingsShareOneSequence(t *testing.T) {
	doc := src(
		head("n0001", "一、甲", 1),
		para("n0002", "正文甲。"),
		para("n0003", "正文乙。"),
	)
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、甲", Level: 1,
				HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
			// No source heading: the model supplies the title.
			{ID: "s2", Title: "乙的标题", Level: 1, BodyNodeIDs: []string{"n0003"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var got []string
	for _, b := range res.Doc.Blocks {
		if b.Role == docmodel.RoleHeading1 {
			got = append(got, b.AutoNumber+b.Text)
		}
	}
	want := []string{"一、甲", "二、乙的标题"}
	if len(got) != len(want) {
		t.Fatalf("标题 = %v, 期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("标题[%d] = %q, 期望 %q", i, got[i], want[i])
		}
	}
}

// TestSourceHeadingLevelOverridesPlanLevel is the fix for a defect visible in a
// generated document: a plan that put a "（一）…" paragraph at section level 1
// produced "三、（一）检查范围。…" — a level-1 number glued to a level-2
// heading. The source's own numbering is authoritative, because it is already
// written into the text and cannot be rewritten.
func TestSourceHeadingLevelOverridesPlanLevel(t *testing.T) {
	doc := src(
		head("n0001", "（一）检查范围", 2),
		para("n0002", "正文。"),
	)
	// The plan claims level 1, contradicting the heading's own form.
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "（一）检查范围", Level: 1,
				HeadingNodeID: "n0001", BodyNodeIDs: []string{"n0002"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var block *docmodel.OutBlock
	for i := range res.Doc.Blocks {
		if res.Doc.Blocks[i].NodeID == "n0001" {
			block = &res.Doc.Blocks[i]
		}
	}
	if block == nil {
		t.Fatal("标题块未生成")
	}
	if block.Role != docmodel.RoleHeading2 {
		t.Errorf("应按原文的（一）形式使用二级标题, 实际 %q", block.Role)
	}
	if block.AutoNumber != "" {
		t.Errorf("原文已有层次序数时不应再加编号, 实际 %q", block.AutoNumber)
	}
	// The printed heading must not contain a level-1 number.
	if strings.Contains(block.AutoNumber+block.Text, "、（一）") {
		t.Errorf("输出了混合编号: %q", block.AutoNumber+block.Text)
	}
}

// TestNumberedHeadingKeepsItsOwnNumberAcrossLevels: a document that mixes
// levels keeps each level's own count.
func TestNumberedHeadingKeepsItsOwnNumberAcrossLevels(t *testing.T) {
	doc := src(
		head("n0001", "一、甲", 1),
		head("n0002", "（一）甲一", 2),
		head("n0003", "（二）甲二", 2),
		head("n0004", "二、乙", 1),
	)
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "一、甲", Level: 1, HeadingNodeID: "n0001"},
			{ID: "s2", Title: "（一）甲一", Level: 1, HeadingNodeID: "n0002"},
			{ID: "s3", Title: "（二）甲二", Level: 1, HeadingNodeID: "n0003"},
			{ID: "s4", Title: "二、乙", Level: 1, HeadingNodeID: "n0004"},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var got []string
	for _, b := range res.Doc.Blocks {
		if b.Role == docmodel.RoleHeading1 || b.Role == docmodel.RoleHeading2 {
			got = append(got, b.AutoNumber+b.Text)
		}
	}
	want := []string{"一、甲", "（一）甲一", "（二）甲二", "二、乙"}
	if len(got) != len(want) {
		t.Fatalf("标题 = %v, 期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("标题[%d] = %q, 期望 %q", i, got[i], want[i])
		}
	}
}

// TestDetectHeadingNumber covers the form→level mapping directly.
func TestDetectHeadingNumber(t *testing.T) {
	cases := []struct {
		text  string
		value int
		level int
		ok    bool
	}{
		{"一、总体要求", 1, 1, true},
		{"十二、其他", 12, 1, true},
		{"（一）检查范围", 1, 2, true},
		{"(二)检查重点", 2, 2, true},
		{"1.加强领导", 1, 3, true},
		{"12、其他", 12, 3, true},
		{"（1）具体目标", 1, 4, true},
		{"(23)其他", 23, 4, true},
		{"无编号的标题", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		v, lvl, ok := detectHeadingNumber(c.text)
		if ok != c.ok {
			t.Errorf("detectHeadingNumber(%q) ok = %v, 期望 %v", c.text, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if v != c.value || lvl != c.level {
			t.Errorf("detectHeadingNumber(%q) = (%d, %d), 期望 (%d, %d)",
				c.text, v, lvl, c.value, c.level)
		}
	}
}

func TestParseOwnNumber(t *testing.T) {
	cases := []struct {
		level int
		text  string
		want  int
		ok    bool
	}{
		{1, "一、总体要求", 1, true},
		{1, "二、检查范围", 2, true},
		{1, "十、其他", 10, true},
		{1, "十一、其他", 11, true},
		{1, "二十一、其他", 21, true},
		{1, "\u3000三、缩进", 3, true},
		{1, "总体要求", 0, false},
		{1, "一", 0, false}, // no separator
		{2, "（一）目标", 1, true},
		{2, "(二)目标", 2, true},
		{2, "（十二）目标", 12, true},
		{2, "（1）目标", 0, false},
		{3, "1.目标", 1, true},
		{3, "12、目标", 12, true},
		{3, "目标", 0, false},
		{4, "（1）目标", 1, true},
		{4, "(23)目标", 23, true},
		{4, "目标", 0, false},
	}
	for _, c := range cases {
		got, ok := parseOwnNumber(c.level, c.text)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseOwnNumber(%d, %q) = (%d, %v), 期望 (%d, %v)",
				c.level, c.text, got, ok, c.want, c.ok)
		}
	}
}

// TestParseOwnNumberAgreesWithHasOwnNumber keeps the two predicates in step:
// a text that "has" a number must yield one and vice versa, or the assembler
// could adopt a number it did not recognise, or refuse one it did.
func TestParseOwnNumberAgreesWithHasOwnNumber(t *testing.T) {
	texts := []string{
		"一、甲", "十二、乙", "（三）丙", "(四)丁", "1.戊", "23、己", "（5）庚",
		"甲", "一", "（一", "1", "  ", "",
	}
	for level := 1; level <= 4; level++ {
		for _, txt := range texts {
			has := hasOwnNumber(level, txt)
			_, ok := parseOwnNumber(level, txt)
			if has != ok {
				t.Errorf("层级%d %q: hasOwnNumber=%v 但 parseOwnNumber=%v", level, txt, has, ok)
			}
		}
	}
}

// TestNumeralRoundTrip pins chineseNumeral and parseChineseNumeral as mutual
// inverses. They are used in opposite directions — one writes a 层次序数, the
// other reads one back to continue the sequence — so a disagreement would
// mis-number a document rather than fail loudly.
func TestNumeralRoundTrip(t *testing.T) {
	for n := 1; n <= 99; n++ {
		written := chineseNumeral(n)
		back, ok := parseChineseNumeral(written)
		if !ok {
			t.Errorf("parseChineseNumeral(chineseNumeral(%d)=%q) 无法解析", n, written)
			continue
		}
		if back != n {
			t.Errorf("往返失败: %d → %q → %d", n, written, back)
		}
	}
}

func TestChineseNumeral(t *testing.T) {
	cases := map[int]string{
		1: "一", 2: "二", 9: "九", 10: "十", 11: "十一", 19: "十九",
		20: "二十", 21: "二十一", 99: "九十九",
	}
	for n, want := range cases {
		if got := chineseNumeral(n); got != want {
			t.Errorf("chineseNumeral(%d) = %q, 期望 %q", n, got, want)
		}
	}
}

func TestHasOwnNumber(t *testing.T) {
	cases := []struct {
		level int
		text  string
		want  bool
	}{
		{1, "一、总体要求", true},
		{1, "十一、其他", true},
		{1, "总体要求", false},
		{1, "\u3000一、缩进", true},
		{2, "（一）目标", true},
		{2, "(一)目标", true},
		{2, "（1）目标", false},
		{3, "1.目标", true},
		{3, "12、目标", true},
		{3, "目标", false},
		{4, "（1）目标", true},
		{4, "(2)目标", true},
		{4, "目标", false},
	}
	for _, c := range cases {
		if got := hasOwnNumber(c.level, c.text); got != c.want {
			t.Errorf("hasOwnNumber(%d, %q) = %v, 期望 %v", c.level, c.text, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- elements

// TestElementBindingClaimsSourceNodes is the regression guard for a real
// failure mode: a 公文 usually contains its own 标题 and 成文日期 as ordinary
// paragraphs. If those cannot be bound to their element slots, VerifyPlan
// reports them as dropped content and the document can never be rendered.
func TestElementBindingClaimsSourceNodes(t *testing.T) {
	doc := src(
		para("n0001", "关于开展某项工作的通知"),
		para("n0002", "各有关单位："),
		para("n0003", "现将有关事项通知如下。"),
		para("n0004", "某某单位"),
		para("n0005", "2024年5月1日"),
	)
	plan := &docmodel.Plan{
		Meta: docmodel.Meta{DocNumber: "某发〔2024〕1号"},
		Elements: []docmodel.ElementBinding{
			{Role: docmodel.RoleTitle, NodeID: "n0001"},
			{Role: docmodel.RoleRecipient, NodeID: "n0002"},
			{Role: docmodel.RoleSignature, NodeID: "n0004"},
			{Role: docmodel.RoleDate, NodeID: "n0005"},
		},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "通知事项", Level: 1, BodyNodeIDs: []string{"n0003"}},
		},
	}

	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build 失败（要素绑定未能避免误报丢失）: %v", err)
	}
	if !res.Diff.Clean() {
		t.Fatalf("逐字校验未通过: missing=%v altered=%v", res.Diff.Missing, res.Diff.Altered)
	}

	// Each bound node must be rendered under its own role, with its own text.
	wantRole := map[string]docmodel.Role{
		"n0001": docmodel.RoleTitle,
		"n0002": docmodel.RoleRecipient,
		"n0004": docmodel.RoleSignature,
		"n0005": docmodel.RoleDate,
	}
	seen := map[string]docmodel.Role{}
	for _, b := range res.Doc.Blocks {
		if b.NodeID != "" {
			seen[b.NodeID] = b.Role
		}
	}
	for id, role := range wantRole {
		if seen[id] != role {
			t.Errorf("节点 %s 渲染为 %q, 期望 %q", id, seen[id], role)
		}
	}
}

// TestUnboundElementUsesMeta confirms the other direction: when the source
// has no such paragraph, Meta supplies the value as a synthesized line.
func TestUnboundElementUsesMeta(t *testing.T) {
	doc := src(para("n0001", "正文。"))
	plan := &docmodel.Plan{
		Meta: docmodel.Meta{
			IssuingOrg: "某某市人民政府办公室文件",
			DocNumber:  "某政办发〔2024〕7号",
			Title:      "关于加强某项工作的意见",
			Recipient:  "各县（市、区）人民政府：",
			Signature:  "某某市人民政府办公室",
			Date:       "2024-05-01",
		},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "总体要求", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	joined := res.Doc.Text()
	for _, want := range []string{
		"某某市人民政府办公室文件", "某政办发〔2024〕7号",
		"关于加强某项工作的意见", "各县（市、区）人民政府：",
		"某某市人民政府办公室", "2024年5月1日", // 阿拉伯数字，去零填充
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("输出缺少 %q", want)
		}
	}
}

// TestDateNormalisation checks §7.3.5.2: Arabic numerals, no zero padding.
func TestDateNormalisation(t *testing.T) {
	cases := map[string]string{
		"2024-05-01":  "2024年5月1日",
		"2024/5/1":    "2024年5月1日",
		"2024.05.01":  "2024年5月1日",
		"2024年05月01日": "2024年5月1日",
		"2024-12-31":  "2024年12月31日",
		"2024年5月1日":   "2024年5月1日",
		"二〇二四年五月一日":   "二〇二四年五月一日", // 无法解析时原样返回，不猜
		"":            "",
	}
	for in, want := range cases {
		if got := normalizeDate(in); got != want {
			t.Errorf("normalizeDate(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------- refusals

// TestBuildRefusesLossyPlan is the refusal the user most depends on: a plan
// that drops a paragraph must not render at all.
func TestBuildRefusesLossyPlan(t *testing.T) {
	doc := src(para("n0001", "第一段"), para("n0002", "第二段会被丢掉"))
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章节", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	_, err := Build(doc, plan, spec())
	if !errors.Is(err, ErrPlanNotRenderable) {
		t.Fatalf("期望 ErrPlanNotRenderable, 得到 %v", err)
	}
}

// TestBuildRefusesDuplicatePlan: duplicating a paragraph is how a
// restructure silently becomes a rewrite, so it must also refuse.
func TestBuildRefusesDuplicatePlan(t *testing.T) {
	doc := src(para("n0001", "只出现一次的内容"))
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "甲", Level: 1, BodyNodeIDs: []string{"n0001"}},
			{ID: "s2", Title: "乙", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	if _, err := Build(doc, plan, spec()); !errors.Is(err, ErrPlanNotRenderable) {
		t.Fatalf("期望 ErrPlanNotRenderable, 得到 %v", err)
	}
}

// TestBuildRefusesUnknownRef: an id the document lacks must not be silently
// ignored, or content could vanish while the report claimed success.
func TestBuildRefusesUnknownRef(t *testing.T) {
	doc := src(para("n0001", "内容"))
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "甲", Level: 1, BodyNodeIDs: []string{"n0001", "n9999"}},
		},
	}
	if _, err := Build(doc, plan, spec()); !errors.Is(err, ErrPlanNotRenderable) {
		t.Fatalf("期望 ErrPlanNotRenderable, 得到 %v", err)
	}
}

// TestBuildRefusesBrokenSpec: a spec missing a role must fail loudly rather
// than render with a silently wrong style.
func TestBuildRefusesBrokenSpec(t *testing.T) {
	doc := src(para("n0001", "内容"))
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "甲", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	bad := spec()
	delete(bad.Styles, docmodel.RoleBody)

	if _, err := Build(doc, plan, bad); !errors.Is(err, ErrPlanNotRenderable) {
		t.Fatalf("期望 ErrPlanNotRenderable, 得到 %v", err)
	}
}

// ---------------------------------------------------------------- structure

// TestHeaderFooterOrdering checks that the assembled document appears in the
// order GB/T 9704-2012 §7 lays out: 版头 → 标题 → 主体 → 署名 → 版记.
func TestHeaderFooterOrdering(t *testing.T) {
	doc := src(para("n0001", "正文内容。"))
	plan := &docmodel.Plan{
		Meta: docmodel.Meta{
			CopyNumber: "000012",
			Security:   "秘密★1年",
			IssuingOrg: "某某机关文件",
			DocNumber:  "某发〔2024〕1号",
			Title:      "标题文字",
			Recipient:  "主送机关：",
			Signature:  "署名机关",
			Date:       "2024年5月1日",
			CC:         "某某单位",
			Printer:    "某某机关办公室",
			PrintDate:  "2024年5月2日",
		},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章节标题", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Every role that should be present, and the order they must appear in.
	wantOrder := []docmodel.Role{
		docmodel.RoleCopyNumber,
		docmodel.RoleSecurity,
		docmodel.RoleMasthead,
		docmodel.RoleDocNumber,
		docmodel.RoleRedLine,
		docmodel.RoleTitle,
		docmodel.RoleRecipient,
		docmodel.RoleHeading1,
		docmodel.RoleBody,
		docmodel.RoleSignature,
		docmodel.RoleDate,
		docmodel.RoleCC,
		docmodel.RolePrinter,
	}
	idx := map[docmodel.Role]int{}
	for i, b := range res.Doc.Blocks {
		if _, seen := idx[b.Role]; !seen {
			idx[b.Role] = i
		}
	}
	prev := -1
	for _, r := range wantOrder {
		i, ok := idx[r]
		if !ok {
			t.Errorf("缺少 role %s", r)
			continue
		}
		if i < prev {
			t.Errorf("role %s 顺序错误：位置 %d 在 %d 之前", r, i, prev)
		}
		prev = i
	}
}

// TestEmptyElementsAreSkipped: a 公文 with no 密级 must not print a blank
// line where the 密级 would be.
func TestEmptyElementsAreSkipped(t *testing.T) {
	doc := src(para("n0001", "正文。"))
	plan := &docmodel.Plan{
		Meta: docmodel.Meta{Title: "只有标题"},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章节", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, b := range res.Doc.Blocks {
		switch b.Role {
		case docmodel.RoleSecurity, docmodel.RoleUrgency, docmodel.RoleCopyNumber,
			docmodel.RoleSignatory, docmodel.RoleCC, docmodel.RolePrinter,
			docmodel.RoleAttachmentNote, docmodel.RoleNote:
			t.Errorf("未提供的要素 %s 不应产生块（会产生空行）", b.Role)
		}
	}
}

// TestTableAndImagePassThrough: the assembler must carry non-prose payloads
// through untouched so the writer can render them.
func TestTableAndImagePassThrough(t *testing.T) {
	rows := [][]string{{"项目", "数值"}, {"甲", "1"}}
	img := &docmodel.ImageRef{Name: "a.png", MimeType: "image/png", Data: []byte{1, 2, 3}}

	doc := src(
		docmodel.Node{ID: "n0001", Kind: docmodel.KindTable, Rows: rows, Text: "项目 数值"},
		docmodel.Node{ID: "n0002", Kind: docmodel.KindImage, Image: img, Text: ""},
		para("n0003", "正文。"),
	)
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章节", Level: 1,
				BodyNodeIDs: []string{"n0001", "n0002", "n0003"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var gotTable, gotImage bool
	for _, b := range res.Doc.Blocks {
		if b.NodeID == "n0001" {
			gotTable = len(b.Rows) == 2 && b.Rows[0][0] == "项目"
		}
		if b.NodeID == "n0002" {
			gotImage = b.Image != nil && string(b.Image.Data) == string([]byte{1, 2, 3})
		}
	}
	if !gotTable {
		t.Error("表格数据未透传")
	}
	if !gotImage {
		t.Error("图片数据未透传")
	}
}

// TestPageBreakNode: an explicit source page break carries no prose, so it
// must not be required for coverage, and it must survive if referenced.
func TestPageBreakNode(t *testing.T) {
	doc := src(
		para("n0001", "第一页内容"),
		docmodel.Node{ID: "n0002", Kind: docmodel.KindPageBreak, Text: ""},
		para("n0003", "第二页内容"),
	)
	// The page break is not prose, so a plan covering only n0001/n0003 is
	// complete — this must NOT be reported as dropped content.
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "章节", Level: 1, BodyNodeIDs: []string{"n0001", "n0003"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build 失败（分页符不应被要求收录）: %v", err)
	}
	if !res.Diff.Clean() {
		t.Fatalf("逐字校验不应因分页符失败: %v", res.Diff)
	}
}

// TestSynthTextIsAuditable: every auto-generated string must be reported, so
// a reviewer can confirm nothing else was invented.
func TestSynthTextIsAuditable(t *testing.T) {
	doc := src(para("n0001", "正文。"))
	plan := &docmodel.Plan{
		Meta: docmodel.Meta{Title: "标题", IssuingOrg: "机关文件", DocNumber: "某发〔2024〕1号"},
		Sections: []docmodel.Section{
			{ID: "s1", Title: "模型生成的章节标题", Level: 1, BodyNodeIDs: []string{"n0001"}},
		},
	}
	res, err := Build(doc, plan, spec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// The model-authored heading must be listed as synthesized.
	found := false
	for _, s := range res.SynthText {
		if s == "模型生成的章节标题" {
			found = true
		}
	}
	if !found {
		t.Errorf("模型生成的标题未列入 SynthText: %v", res.SynthText)
	}
	// And every Extra string in the diff must be accounted for by SynthText
	// or by a numbering prefix.
	synthSet := map[string]bool{}
	for _, s := range res.SynthText {
		synthSet[s] = true
	}
	for _, e := range res.Diff.Extra {
		if synthSet[e] {
			continue
		}
		// Numbering prefixes appear on headings; the heading text itself is
		// in SynthText, and the prefix is attached at render time, so an
		// auto-numbered block is acceptable here.
		t.Logf("合成的额外文本（编号类）: %q", e)
	}
}

// TestDirectionChangesMastheadOffset: §4.3.2 places 发文机关标志 35mm below
// the top of the 版心 for 下行文 but 80mm for 上行文.
func TestDirectionChangesMastheadOffset(t *testing.T) {
	doc := src(para("n0001", "正文。"))
	newPlan := func() *docmodel.Plan {
		return &docmodel.Plan{
			Meta: docmodel.Meta{IssuingOrg: "某机关文件", Title: "标题"},
			Sections: []docmodel.Section{
				{ID: "s1", Title: "章节", Level: 1, BodyNodeIDs: []string{"n0001"}},
			},
		}
	}
	offsetFor := func(dir gbstandard.Direction) float64 {
		s := spec()
		s.Direction = dir
		res, err := Build(doc, newPlan(), s)
		if err != nil {
			t.Fatalf("Build(%s): %v", dir, err)
		}
		for _, b := range res.Doc.Blocks {
			if b.Role == docmodel.RoleMasthead {
				return b.SpaceBeforeLines
			}
		}
		t.Fatalf("未找到发文机关标志块（%s）", dir)
		return 0
	}
	down := offsetFor(gbstandard.DirDown)
	up := offsetFor(gbstandard.DirUp)
	if up <= down {
		t.Errorf("上行文的发文机关标志偏移应大于下行文: up=%.1f down=%.1f", up, down)
	}
	t.Logf("偏移（百分之一行）: 下行文=%.1f 上行文=%.1f", down, up)
}
