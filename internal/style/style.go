// Package style measures how formal a passage already reads and how far a
// rewrite moved it from the source.
//
// # Why this package exists
//
// The product rule is "改动尽量少". That is a statement about degree, not a
// binary, so the program has to be able to answer two questions mechanically:
//
//  1. How colloquial is this passage to begin with? A passage written as
//     "咱们得抓紧弄一下" can be rewritten substantially without betraying the
//     author. A passage already in 公文 language must be left almost alone.
//  2. How much did a rewrite actually change? Without a number, "少改" is
//     unverifiable and the model can silently replace whole sentences.
//
// Both answers are computed here, from the text alone, with no model and no
// document-model dependency. The verifier uses them to enforce a budget; the
// prompt builder uses the same numbers to tell the model what it is allowed to
// do. Because both sides read one table, the model's instructions and the
// export gate cannot drift apart.
package style

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Tier is how formal a passage reads.
type Tier string

const (
	// TierFormal is 公文-like prose: official connectives, long balanced
	// sentences, no spoken particles.
	TierFormal Tier = "formal"
	// TierMixed is ordinary written Chinese with some loose phrasing.
	TierMixed Tier = "mixed"
	// TierColloquial is spoken language: 口语词、语气词、短碎句。
	TierColloquial Tier = "colloquial"
)

// Label is the Chinese name shown in the UI.
func (t Tier) Label() string {
	switch t {
	case TierFormal:
		return "已规范"
	case TierColloquial:
		return "偏口语"
	default:
		return "一般书面"
	}
}

// colloquialMarkers are words that are normal in speech and unusual in 公文.
//
// The list is deliberately about register, not about correctness: none of these
// is an error, so they must never be reported as a typo. They only raise the
// rewrite budget, because replacing them is exactly the work the user asked
// for.
var colloquialMarkers = []string{
	"咱们", "咱", "啥", "咋", "咋样", "干啥", "搞定", "弄好", "弄一下", "整一下",
	"挺", "特别", "然后", "就是说", "也就是说", "这个", "那个", "吧", "啊", "呢",
	"嘛", "呗", "俺", "老是", "差不多", "基本上", "反正", "其实", "感觉", "觉得",
	"搞", "做好一下", "说一下", "看一下", "弄", "整", "得先", "得要", "还得",
	"大家", "各位", "咱们大家", "赶紧", "抓紧弄", "加把劲", "好好", "一点儿",
	"有点", "有点儿", "什么的", "之类的", "差不多吧", "对不对", "是吧", "行不行",
}

// officialConnectives are phrases that signal 公文 register. Their presence
// lowers the rewrite budget: a passage that already speaks the target language
// should not be rewritten into it.
var officialConnectives = []string{
	"为贯彻落实", "为深入", "现将", "现通知如下", "特此通知", "特此报告",
	"按照", "根据", "依据", "遵照", "结合", "有关", "现将有关事项",
	"请遵照执行", "请认真贯彻执行", "现将有关要求通知如下", "以上意见",
	"妥否", "请批示", "此复", "鉴于", "为进一步", "确保", "切实", "扎实",
	"统筹", "部署", "落实", "督促", "限期", "会同", "报请", "印发",
}

// Assessment is the register reading of one passage.
type Assessment struct {
	// Tier is the register bucket.
	Tier Tier `json:"tier"`
	// Score is 0-100 colloquialism; higher means more spoken.
	Score int `json:"score"`
	// Markers lists the colloquial words found, for the reviewer.
	Markers []string `json:"markers,omitempty"`
	// Connectives lists the official phrases found.
	Connectives []string `json:"connectives,omitempty"`
	// Sentences is the sentence count.
	Sentences int `json:"sentences"`
	// LongestSentence is the longest sentence in runes. A one-sentence
	// paragraph of 400 characters is the shape that most needs splitting.
	LongestSentence int `json:"longest_sentence"`
}

// Assess reads the register of a passage.
//
// The score combines three independent signals, because each alone is easy to
// fool: marker density catches spoken vocabulary, sentence length catches
// run-on prose, and the absence of official connectives catches writing that is
// merely plain rather than 公文.
func Assess(text string) Assessment {
	a := Assessment{Tier: TierFormal}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return a
	}

	for _, m := range colloquialMarkers {
		if strings.Contains(text, m) {
			a.Markers = append(a.Markers, m)
		}
	}
	for _, c := range officialConnectives {
		if strings.Contains(text, c) {
			a.Connectives = append(a.Connectives, c)
		}
	}
	sort.Strings(a.Markers)
	sort.Strings(a.Connectives)

	sentences := SplitSentences(text)
	a.Sentences = len(sentences)
	for _, s := range sentences {
		if n := len([]rune(s)); n > a.LongestSentence {
			a.LongestSentence = n
		}
	}

	// Scoring starts from a neutral baseline. Plain written Chinese is neither
	// 公文 nor speech, and treating "no signals" as "fully formal" would grant
	// the smallest possible budget to exactly the loose drafts that need room.
	// Formality has to be *earned* by official connectives, not assumed.
	score := 14.0

	// Marker density: one marker per ~25 characters is unmistakably spoken.
	per100 := float64(len(a.Markers)) * 100 / float64(len(runes))
	score += minFloat(55, per100*8)

	// Density alone thins out in a long paragraph, where the same spoken voice
	// is spread over hundreds of characters. A handful of distinct markers is
	// its own evidence that the draft was not written in 公文 register.
	if len(a.Markers) >= 5 {
		score += 8
	}

	// A long unbroken sentence is a second, independent signal: 公文 sentences
	// are short and the author of a run-on draft has not shaped it yet.
	if a.LongestSentence >= 200 {
		score += 22
	} else if a.LongestSentence >= 120 {
		score += 12
	} else if a.LongestSentence >= 80 {
		score += 6
	}
	// A very long paragraph made of only one or two sentences is the "一段到底"
	// shape even when each sentence is short.
	if len(sentences) <= 2 && len(runes) >= 150 {
		score += 10
	}

	// Official connectives are positive evidence of register. Each one
	// subtracts a fixed amount so a single 现将 cannot mask a colloquial body,
	// but a properly drafted paragraph is driven to zero.
	if len(a.Connectives) > 0 {
		score -= float64(min(32, 14*len(a.Connectives)))
	}

	a.Score = clamp(int(score), 0, 100)
	switch {
	case a.Score >= 45:
		a.Tier = TierColloquial
	case a.Score >= 8:
		a.Tier = TierMixed
	default:
		a.Tier = TierFormal
	}
	return a
}

// Limits is the rewrite budget, expressed as a maximum change percentage.
//
// The percentage is measured by Distance: 0 means the output is the source
// verbatim, 100 means it shares nothing. Budgets are per register tier so that
// "太口语化的可以大改，已经规范的少改" is enforced rather than merely requested.
type Limits struct {
	// Enabled permits rewriting at all. False keeps the strict
	// character-by-character mode.
	Enabled bool `json:"enabled"`
	// FormalMax applies to passages already in 公文 register.
	FormalMax int `json:"formal_max"`
	// MixedMax applies to ordinary written Chinese.
	MixedMax int `json:"mixed_max"`
	// ColloquialMax applies to spoken-register passages.
	ColloquialMax int `json:"colloquial_max"`
}

// DefaultLimits is the shipped budget.
//
// The numbers follow one rule: the more spoken the source, the more of it a
// legitimate rewrite replaces. A 公文 that already reads well may only be
// touched lightly (15%); ordinary written Chinese has room to be tightened
// (35%); a spoken paragraph rewritten into 公文 shares almost no characters
// with its source, so a character budget there would forbid the very work the
// user asked for. 100% therefore means "no character limit" for that tier —
// its guard is the fact check (no value may be lost or invented) and the
// reviewer, not character overlap.
func DefaultLimits() Limits {
	return Limits{Enabled: true, FormalMax: 15, MixedMax: 35, ColloquialMax: 100}
}

// Normalized fills zero fields from the defaults, so a hand-written or older
// stored config cannot silently disable the budget.
func (l Limits) Normalized() Limits {
	d := DefaultLimits()
	// A zero value means "never configured", not "turned off". Without this
	// branch a fresh install — whose stored settings have no rewrite block —
	// would run with Enabled=false and no budget at all, so the check that
	// stops a wholesale rewrite would silently not exist. Turning rewriting
	// off is done through the edit permission, not by zeroing these numbers.
	if l.FormalMax == 0 && l.MixedMax == 0 && l.ColloquialMax == 0 && !l.Enabled {
		return d
	}
	if l.FormalMax <= 0 {
		l.FormalMax = d.FormalMax
	}
	if l.MixedMax <= 0 {
		l.MixedMax = d.MixedMax
	}
	if l.ColloquialMax <= 0 {
		l.ColloquialMax = d.ColloquialMax
	}
	if l.FormalMax > 100 {
		l.FormalMax = 100
	}
	if l.MixedMax > 100 {
		l.MixedMax = 100
	}
	if l.ColloquialMax > 100 {
		l.ColloquialMax = 100
	}
	return l
}

// MaxFor returns the budget for a register tier.
func (l Limits) MaxFor(t Tier) int {
	l = l.Normalized()
	switch t {
	case TierFormal:
		return l.FormalMax
	case TierColloquial:
		return l.ColloquialMax
	default:
		return l.MixedMax
	}
}

// Describe renders the budget as one line of prompt text.
func (l Limits) Describe() string {
	l = l.Normalized()
	return fmt.Sprintf("已规范段落最多改动 %d%%，一般书面段落最多改动 %d%%，偏口语段落最多改动 %d%%",
		l.FormalMax, l.MixedMax, l.ColloquialMax)
}

// Distance returns how far out is from src, as a percentage 0-100.
//
// The measure is one minus the Dice coefficient over character bigrams. It is
// chosen over edit distance because it is linear in the text length (a 10k-rune
// paragraph is common in this tool, and O(n·m) would be 100M operations) and
// because it behaves the way a reader judges a rewrite: reordering clauses
// costs little, replacing vocabulary costs a lot.
func Distance(src, out string) float64 {
	a, b := bigrams(src), bigrams(out)
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 || len(b) == 0 {
		return 100
	}
	common := 0
	for g, n := range a {
		if m, ok := b[g]; ok {
			if n < m {
				common += n
			} else {
				common += m
			}
		}
	}
	return clampFloat((1-2*float64(common)/float64(total(a)+total(b)))*100, 0, 100)
}

func bigrams(s string) map[string]int {
	runes := make([]rune, 0, len(s))
	for _, r := range s {
		// Whitespace and punctuation are excluded on purpose. Breaking a run-on
		// paragraph into sentences adds 。，； that were never there, and a
		// measure that counted those would call a pure punctuation fix a 20%
		// rewrite and refuse it — punishing exactly the work this tool exists
		// to do. What must be preserved is the wording, not the marks.
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		runes = append(runes, r)
	}
	out := map[string]int{}
	if len(runes) == 1 {
		out[string(runes)] = 1
		return out
	}
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i:i+2])]++
	}
	return out
}

func total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// isSentenceFinal reports whether r ends a sentence.
//
// 分号 is included: in this tool a semicolon-heavy paragraph is one long
// enumeration the author never broke up, and counting its clauses as separate
// sentences is what makes the rewrite budget notice it.
func isSentenceFinal(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '!', '?', ';':
		return true
	}
	return false
}

// isCloser reports whether r closes a quotation or bracket.
func isCloser(r rune) bool {
	switch r {
	case '"', '\'', '”', '’', ')', '）', '」', '』', '】', '》', '〕':
		return true
	}
	return false
}

// SplitSentences breaks Chinese prose on sentence-final punctuation.
//
// A closing quote or bracket that follows the punctuation stays with the
// sentence it closes, so a sentence ending in "……。”" is not split before the
// closing quote. The splitter is intentionally conservative: it is used to
// report structure and to size a rewrite, never to cut text, so a missed
// boundary costs nothing while a wrong one would mislead the review panel.
func SplitSentences(text string) []string {
	runes := []rune(text)
	var out []string
	start := 0
	for i := 0; i < len(runes); i++ {
		if !isSentenceFinal(runes[i]) {
			continue
		}
		end := i + 1
		for end < len(runes) && isCloser(runes[end]) {
			end++
		}
		if s := strings.TrimSpace(string(runes[start:end])); s != "" {
			out = append(out, s)
		}
		start = end
		i = end - 1
	}
	if s := strings.TrimSpace(string(runes[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

// Fact is one piece of content that a rewrite must not lose.
type Fact struct {
	// Kind is "number", "date", "docno", "quote" or "org".
	Kind string `json:"kind"`
	// Text is the exact source string.
	Text string `json:"text"`
	// Hard facts fail the export when lost; soft facts warn.
	Hard bool `json:"hard"`
}

var (
	dateRe = regexp.MustCompile(`\d{4}\s*年\s*\d{1,2}\s*月(?:\s*\d{1,2}\s*日)?|\d{4}-\d{1,2}-\d{1,2}`)
	// 发文字号 is 机关代字 + 〔年〕 + 序号 + 号. The 代字 conventionally ends in
	// 发/办/函/电/字. A connective the character class necessarily swallows
	// ("根据国办发〔…") is removed by trimLeadWord after the match.
	docNoRe  = regexp.MustCompile(`[\p{L}]{2,10}(?:发|办|函|电|字)〔\s*\d{4}\s*〕\d+\s*号`)
	numberRe = regexp.MustCompile(`\d+(?:\.\d+)?\s*(?:%|％|万元|亿元|万|亿|元|个|项|条|人次|人|户|家|所|台|次|天|个月|年)`)
	quoteRe  = regexp.MustCompile(`“[^”]{2,60}”|「[^」]{2,60}」`)
	orgRe    = regexp.MustCompile(`[\p{Han}]{2,15}(?:人民政府|委员会|办公室|管理局|集团公司|有限公司|公司|大学|学院|学校|医院|中心)`)
)

// leadParticles are words a 发文字号 is commonly introduced by. They are
// matched as part of the 机关代字 because both are Han letters, so they are
// stripped afterwards.
var leadParticles = []string{"根据", "依据", "按照", "遵照", "依照", "参照", "关于", "印发"}

func trimLeadWord(s string) string {
	for _, p := range leadParticles {
		if strings.HasPrefix(s, p) {
			return s[len(p):]
		}
	}
	return s
}

// Facts extracts the content a rewrite has to preserve.
//
// Numbers, dates and 发文字号 are hard facts: a document that loses "2024年5月1日"
// or "〔2024〕15号" has changed its meaning, and no amount of good prose
// compensates. Organisations and quoted phrases are soft facts: they matter,
// but the extractor cannot always tell an organisation from an ordinary noun
// ("中心工作" is not an organisation), so a miss is a warning rather than a
// block.
//
// Bare single digits are deliberately excluded: they are far more often list
// numbering ("1." "2.") than content, and flagging them would block the very
// restructuring this tool exists to do.
func Facts(text string) []Fact {
	seen := map[string]bool{}
	var out []Fact
	add := func(kind, s string, hard bool) {
		s = strings.TrimSpace(s)
		if s == "" || seen[kind+"\x00"+s] {
			return
		}
		seen[kind+"\x00"+s] = true
		out = append(out, Fact{Kind: kind, Text: s, Hard: hard})
	}
	for _, m := range dateRe.FindAllString(text, -1) {
		add("date", m, true)
	}
	for _, m := range docNoRe.FindAllString(text, -1) {
		add("docno", trimLeadWord(m), true)
	}
	for _, m := range numberRe.FindAllString(text, -1) {
		add("number", m, true)
	}
	for _, m := range quoteRe.FindAllString(text, -1) {
		add("quote", m, false)
	}
	for _, m := range orgRe.FindAllString(text, -1) {
		add("org", m, false)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Text < out[j].Text
	})
	return out
}

// MissingFacts returns the facts from src that do not appear in out.
//
// Comparison is on the literal source string with all whitespace removed, which
// tolerates the reflow a legitimate rewrite produces ("2024 年 5 月 1 日" vs
// "2024年5月1日") without tolerating a changed value.
func MissingFacts(src, out string) []Fact {
	normalizedOut := strings.Join(strings.Fields(out), "")
	var missing []Fact
	for _, f := range Facts(src) {
		want := strings.Join(strings.Fields(f.Text), "")
		if !strings.Contains(normalizedOut, want) {
			missing = append(missing, f)
		}
	}
	return missing
}

// InventedFacts returns the hard facts that appear in out but nowhere in src.
//
// This is the complement of MissingFacts, and it matters more for the
// colloquial tier, where the character budget is off: without it, a rewrite
// that replaces "补助标准提高" with "补助标准提高至 50%" would pass, because the
// original had no percentage to lose. A number the source never contained is
// a fabrication regardless of how little the wording moved.
//
// Only hard facts are checked. Organisation names and quoted phrases are
// matched by a heuristic that produces false positives on ordinary nouns
// ("服务中心" in "服务中心工作"), and blocking an export on those would make
// the gate untrustworthy.
func InventedFacts(src, out string) []Fact {
	normalizedSrc := strings.Join(strings.Fields(src), "")
	var invented []Fact
	for _, f := range Facts(out) {
		if !f.Hard {
			continue
		}
		want := strings.Join(strings.Fields(f.Text), "")
		if !strings.Contains(normalizedSrc, want) {
			invented = append(invented, f)
		}
	}
	return invented
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
