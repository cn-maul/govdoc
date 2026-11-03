package preview

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// ---------------------------------------------------------------- helpers

func testSpec() gbstandard.Spec { return gbstandard.Default() }

func testDoc(blocks ...docmodel.OutBlock) *docmodel.Doc {
	return &docmodel.Doc{Meta: docmodel.Meta{Title: "关于测试的通知"}, Blocks: blocks}
}

func render(t *testing.T, d *docmodel.Doc, spec gbstandard.Spec) string {
	t.Helper()
	out, err := Render(d, spec)
	if err != nil {
		t.Fatalf("Render 返回错误: %v", err)
	}
	return string(out)
}

func bodyBlock(text string) docmodel.OutBlock {
	return docmodel.OutBlock{Role: docmodel.RoleBody, Text: text}
}

var pageRe = regexp.MustCompile(`class="gd-page"`)

func pageCount(html string) int { return len(pageRe.FindAllString(html, -1)) }

// markupOf returns the HTML that follows the <style> block.
//
// Attribute assertions must use it: the generated stylesheet contains the very
// same `[data-edited='1']` selector text, so counting occurrences across the
// whole fragment would count the rule as if it were a marked-up block.
func markupOf(html string) string {
	const close = "</style>"
	if i := strings.Index(html, close); i >= 0 {
		return html[i+len(close):]
	}
	return html
}

// openingTagAt returns the whole opening tag that contains offset, so an
// assertion can prove the marker sits on the intended element rather than
// merely that it appears somewhere.
func openingTagAt(s string, offset int) string {
	start := strings.LastIndex(s[:offset], "<")
	if start < 0 {
		start = offset
	}
	end := strings.Index(s[offset:], ">")
	if end < 0 {
		return s[start:]
	}
	return s[start : offset+end+1]
}

// ---------------------------------------------------------------- escaping

// The verbatim invariant means source text reaches the preview byte-for-byte,
// so HTML metacharacters must be escaped rather than dropped or interpreted.
// A `<script>` paragraph in an uploaded document must never become a script.
func TestRenderEscapesHTMLSpecialCharacters(t *testing.T) {
	spec := testSpec()
	d := testDoc(bodyBlock(`<script>alert("x")</script> & "引号" '单引号' <b>粗体</b>`))

	got := render(t, d, spec)

	for _, raw := range []string{"<script>", "</script>", "<b>", "</b>"} {
		if strings.Contains(got, raw) {
			t.Errorf("预览中出现了未转义的 %q，源文本被当作 HTML 解释：\n%s", raw, got)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;/script&gt;", "&amp;", "&lt;b&gt;"} {
		if !strings.Contains(got, want) {
			t.Errorf("预览中缺少转义后的 %q", want)
		}
	}
	// The text itself must survive verbatim, only entity-encoded.
	if !strings.Contains(got, `alert(&#34;x&#34;)`) && !strings.Contains(got, `alert("x")`) {
		t.Errorf("原文内容被改写，未逐字保留：\n%s", got)
	}
}

// 单引号/双引号出现在属性位置（图片 alt、data-node-id）时同样必须转义。
func TestRenderEscapesQuotesInAttributes(t *testing.T) {
	spec := testSpec()
	d := &docmodel.Doc{
		Blocks: []docmodel.OutBlock{{
			Role:   docmodel.RoleBody,
			NodeID: `n" onload="evil()`,
			Image:  &docmodel.ImageRef{Name: `he"llo.png`},
		}},
	}

	got := render(t, d, spec)

	if strings.Contains(got, `onload="evil()`) {
		t.Errorf("属性注入未被阻止：\n%s", got)
	}
	if strings.Contains(got, `alt="he"llo.png"`) {
		t.Errorf("alt 属性中的引号未被转义：\n%s", got)
	}
	if !strings.Contains(got, "&#34;") {
		t.Errorf("未出现引号转义实体：\n%s", got)
	}
}

// 原文中的换行、空格、全角空格都属于「逐字不变」的保护对象，不得被 trim。
// 浏览器默认会折叠连续空白，因此样式必须显式声明 white-space: pre-wrap，
// 否则预览里看到的就不再是原文。
func TestRenderPreservesWhitespaceVerbatim(t *testing.T) {
	spec := testSpec()
	text := "第一行保留尾部空格   \n　　第二行以全角空格开头"
	d := testDoc(bodyBlock(text))

	got := render(t, d, spec)

	if !strings.Contains(got, "第一行保留尾部空格   \n") {
		t.Errorf("原文的尾部空格/换行被改写")
	}
	if !strings.Contains(got, "　　第二行以全角空格开头") {
		t.Errorf("原文的行首全角空格被改写")
	}
	if !strings.Contains(got, "white-space: pre-wrap") {
		t.Errorf("缺少 white-space: pre-wrap，浏览器会折叠原文空白，逐字不变将失效")
	}
}

// AutoNumber 是装配器生成的层次序数，必须渲染在原文之前，且不改变原文。
func TestRenderIncludesAutoNumberBeforeVerbatimText(t *testing.T) {
	spec := testSpec()
	d := testDoc(docmodel.OutBlock{
		Role:       docmodel.RoleHeading1,
		Text:       "总体要求",
		AutoNumber: "一、",
		NodeID:     "n0001",
		Level:      1,
	})

	got := render(t, d, spec)

	idxNum := strings.Index(got, "一、")
	idxText := strings.Index(got, "总体要求")
	if idxNum < 0 || idxText < 0 {
		t.Fatalf("层次序数或原文缺失：\n%s", got)
	}
	if idxNum > idxText {
		t.Errorf("层次序数应渲染在原文之前")
	}
	if !strings.Contains(got, `<span class="gd-autonum">一、</span>总体要求`) {
		t.Errorf("层次序数未被标记为自动编号：\n%s", got)
	}
	if !strings.Contains(got, `data-node-id="n0001"`) {
		t.Errorf("缺少 node 溯源属性，校对区无法定位原文：\n%s", got)
	}
}

// ---------------------------------------------------------------- spec values

// The renderer may not carry numbers of its own: the stylesheet has to expose
// the standard's actual values.
func TestStylesheetCarriesSpecValues(t *testing.T) {
	spec := testSpec()
	got := render(t, testDoc(bodyBlock("正文")), spec)

	want := []string{
		"--gd-page-w: 210mm",       // 标准 A4 宽
		"--gd-page-h: 297mm",       // 标准 A4 高
		"--gd-margin-top: 37mm",    // 标准 上37±1
		"--gd-margin-bottom: 35mm", // 标准 下35±1
		"--gd-margin-left: 28mm",   // 标准 左28±1
		"--gd-margin-right: 26mm",  // 标准 右26±1
		"--gd-text-w: 156mm",       // 标准 版心宽
		"--gd-text-h: 225mm",       // 标准 版心高
		"--gd-lines-per-page: 22",  // 标准 每面22行
		"--gd-chars-per-line: 28",  // 标准 每行28字
		"--gd-body-size: 16pt",     // 标准 三号
		"--gd-masthead-top: 35mm",  // 标准 §4.3.2 下行文
		"--gd-pageno-size: 14pt",   // 标准 §7.3.6 四号
		"--gd-title-size: 22pt",    // 标准 §7.3.1 二号
		"--gd-masthead-size: 36pt", // 惯例（标准仅限 22mm×15mm）
		"--gd-heading1-size: 16pt", // 标准 §7.3.3 三号黑体
		"--gd-cc-size: 14pt",       // 惯例 版记四号仿宋
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("生成的样式中缺少 %q", w)
		}
	}

	// 固定行距必须就是 版心高 ÷ 每面行数：225mm ÷ 22 ≈ 28.99pt。
	if !strings.Contains(got, "--gd-line-spacing: "+trimFloat(spec.Page.LineSpacingPT())+"pt") {
		t.Errorf("固定行距未按 spec 输出")
	}
	spacing := varLength(t, got, "--gd-line-spacing")
	if spacing < 28.98 || spacing > 29.0 {
		t.Errorf("固定行距 = %.4fpt，不符合 225mm÷22行 ≈ 28.99pt", spacing)
	}
	if box := varLength(t, got, "--gd-line-box"); box != spacing {
		t.Errorf("正文行盒 %vpt 与版心固定行距 %vpt 不一致，预览会与 docx 分页不符", box, spacing)
	}
}

// varLength reads a CSS length custom property out of the generated stylesheet.
func varLength(t *testing.T, css, name string) float64 {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(name) + `:\s*(-?[0-9.]+)(mm|pt)`)
	m := re.FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("生成的样式中找不到 %s", name)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("%s 的值 %q 无法解析: %v", name, m[1], err)
	}
	if m[2] == "mm" {
		v *= ptPerMM
	}
	return v
}

// 上行文把 发文机关标志 下移（标准 §4.3.2: 80mm），样式必须随之变化，
// 否则同一份方案在两种行文方向下会画出一样的版头。
func TestMastheadTopFollowsDirection(t *testing.T) {
	down := testSpec()
	down.Direction = gbstandard.DirDown
	up := testSpec()
	up.Direction = gbstandard.DirUp

	downHTML := render(t, testDoc(bodyBlock("正文")), down)
	upHTML := render(t, testDoc(bodyBlock("正文")), up)

	if !strings.Contains(downHTML, "--gd-masthead-top: 35mm") {
		t.Errorf("下行文 发文机关标志 距离应为 35mm")
	}
	if !strings.Contains(upHTML, "--gd-masthead-top: 80mm") {
		t.Errorf("上行文 发文机关标志 距离应为 80mm")
	}
	if !strings.Contains(upHTML, `data-direction="上行文"`) {
		t.Errorf("未在根元素上标注行文方向")
	}
}

// 字体必须输出回退链，缺字体的机器才能落到可用替代字形。
func TestFontFamilyEmitsFallbackChain(t *testing.T) {
	got := render(t, testDoc(bodyBlock("正文")), testSpec())

	// 回退链的顺序与完整性只在 --gd-body-font 这一条声明上检查，否则
	// "仿宋" 会在 "仿宋_GB2312" 内部被提前命中而误判顺序。
	const decl = "--gd-body-font: "
	start := strings.Index(got, decl)
	if start < 0 {
		t.Fatalf("找不到 --gd-body-font 声明")
	}
	value := got[start+len(decl):]
	value = value[:strings.Index(value, ";")]

	chain := gbstandard.FontChain(gbstandard.FontFangSong)
	idx := make([]int, len(chain))
	for i, name := range chain {
		idx[i] = strings.Index(value, name)
		if idx[i] < 0 {
			t.Fatalf("字体回退链缺少 %q：得到 %q", name, value)
		}
	}
	for i := 1; i < len(idx); i++ {
		if idx[i] < idx[i-1] {
			t.Errorf("字体回退链顺序错误：%q 应保持 FontChain 的顺序 %v", value, chain)
		}
	}
	// 完整值必须与 FontChain 逐项一致（顺序 + 引号规则），而不是另一套字体。
	// 通用的 CSS 关键字不带引号，其余具名字体一律加引号。
	wantParts := make([]string, 0, len(chain)+1)
	hasGeneric := false
	for _, name := range chain {
		if isGenericFamily(name) {
			hasGeneric = true
			wantParts = append(wantParts, name)
			continue
		}
		wantParts = append(wantParts, quoteFont(name))
	}
	if !hasGeneric {
		wantParts = append(wantParts, "serif")
	}
	if want := strings.Join(wantParts, ", "); value != want {
		t.Errorf("正文 font-family = %q，应为 FontChain 展开 %q", value, want)
	}
}

// 版头红色元素（发文机关标志、红色分隔线）必须是红色；正文必须是黑色。
func TestHeaderElementsAreRedAndBodyIsBlack(t *testing.T) {
	got := render(t, testDoc(bodyBlock("正文")), testSpec())

	red := "--gd-" + string(docmodel.RoleMasthead) + "-color: #ff0000"
	if !strings.Contains(got, red) {
		t.Errorf("发文机关标志未使用红色：缺少 %q", red)
	}
	if !strings.Contains(got, "--gd-rule-red: #ff0000") {
		t.Errorf("红色分隔线未使用红色")
	}
	if !strings.Contains(got, "--gd-body-color: #000000") {
		t.Errorf("正文颜色应为黑色")
	}
	if !strings.Contains(got, "border-bottom: 1.5pt solid #ff0000") {
		t.Errorf("红色分隔线未生成下边框规则：\n%s", got)
	}
	// 版记横线用黑色，避免与版头红线混淆。
	if !strings.Contains(got, "border-top: 0.75pt solid #000000") {
		t.Errorf("版记（抄送）上横线应为黑色 0.75pt")
	}
	if !strings.Contains(got, "border-bottom: 0.75pt solid #000000") {
		t.Errorf("版记（印发）下横线应为黑色 0.75pt")
	}
}

// ---------------------------------------------------------------- pagination

// 页数必须随内容增加而增加：这是「模拟 A4 分页」的可检验后果。
func TestPageCountGrowsWithContent(t *testing.T) {
	spec := testSpec()

	short := render(t, testDoc(bodyBlock("很短的一段正文。")), spec)

	var longBlocks []docmodel.OutBlock
	for i := 0; i < 200; i++ {
		longBlocks = append(longBlocks, bodyBlock(
			"这是一段用于测试分页的正文内容，长度足以占据多个版心行，从而推动内容进入下一页。"+
				"每面二十二行的固定行距网格决定了它不可能全部容纳在同一张纸上。"))
	}
	long := render(t, testDoc(longBlocks...), spec)

	s := pageCount(short)
	l := pageCount(long)
	if s < 1 {
		t.Fatalf("短文档至少要有一页，得到 %d 页", s)
	}
	if l <= s {
		t.Errorf("页数未随内容增长：短文档 %d 页，长文档 %d 页", l, s)
	}
	// 200 段 × 约 2 行 ÷ 每面 22 行 → 远多于 5 页。
	if l < 5 {
		t.Errorf("长文档页数偏少，分页可能未生效：得到 %d 页", l)
	}
	if !strings.Contains(long, `data-page-count="`+itoa(l)+`"`) {
		t.Errorf("根元素上的 data-page-count 与实际页数不一致")
	}
}

// 显式分页（附件另面编排）必须真的换页。
func TestPageBreakBeforeStartsNewPage(t *testing.T) {
	spec := testSpec()
	d := testDoc(
		bodyBlock("正文第一段。"),
		docmodel.OutBlock{
			Role:            docmodel.RoleAttachment,
			Text:            "附件正文",
			PageBreakBefore: true,
		},
	)

	got := render(t, d, spec)
	if pageCount(got) < 2 {
		t.Errorf("PageBreakBefore 未产生新页面：共 %d 页\n%s", pageCount(got), got)
	}
}

// 版头只出现在第一页；版记只出现在末页。
func TestMastheadOnFirstPageAndFooterOnLast(t *testing.T) {
	spec := testSpec()
	blocks := []docmodel.OutBlock{
		{Role: docmodel.RoleCopyNumber, Text: "000012"},
		{Role: docmodel.RoleMasthead, Text: "国务院办公厅文件"},
		{Role: docmodel.RoleDocNumber, Text: "国办发〔2024〕15号"},
		{Role: docmodel.RoleRedLine},
		{Role: docmodel.RoleTitle, Text: "关于测试的通知"},
	}
	for i := 0; i < 80; i++ {
		blocks = append(blocks, bodyBlock("用于撑满多页的正文段落内容，重复出现以触发分页逻辑。"))
	}
	blocks = append(blocks,
		docmodel.OutBlock{Role: docmodel.RoleCC, Text: "抄送：各有关单位。"},
		docmodel.OutBlock{Role: docmodel.RolePrinter, Text: "国务院办公厅秘书局　2024年5月1日印发"},
	)

	got := render(t, testDoc(blocks...), spec)

	if pageCount(got) < 2 {
		t.Fatalf("测试文档未分页，无法验证版头/版记位置：%d 页", pageCount(got))
	}
	if n := strings.Count(got, "国务院办公厅文件"); n != 1 {
		t.Errorf("发文机关标志应只出现一次，实际 %d 次", n)
	}
	if n := strings.Count(got, "抄送：各有关单位。"); n != 1 {
		t.Errorf("抄送机关应只出现一次，实际 %d 次", n)
	}

	// 版头位于第一张纸内，版记位于最后一张纸内。
	firstPage := got[:strings.Index(got, `data-page="2"`)]
	lastPage := got[strings.LastIndex(got, `class="gd-page"`):]
	if !strings.Contains(firstPage, "gd-head") {
		t.Errorf("版头未出现在第一页")
	}
	if !strings.Contains(firstPage, "国务院办公厅文件") {
		t.Errorf("发文机关标志未出现在第一页")
	}
	if strings.Contains(lastPage, "国务院办公厅文件") {
		t.Errorf("发文机关标志不该出现在末页")
	}
	if !strings.Contains(lastPage, "gd-foot") {
		t.Errorf("版记未出现在最后一页")
	}
	if !strings.Contains(lastPage, "抄送：各有关单位。") {
		t.Errorf("抄送机关未出现在最后一页")
	}
}

// 页码按 spec 的字体与字号渲染，且每页各一个。
func TestPageNumbersFollowSpec(t *testing.T) {
	spec := testSpec()
	spec.PageNumberDash = "—"
	var blocks []docmodel.OutBlock
	for i := 0; i < 120; i++ {
		blocks = append(blocks, bodyBlock("页码测试用的正文段落，重复以产生多页内容。"))
	}

	got := render(t, testDoc(blocks...), spec)

	if n := strings.Count(got, "gd-pageno"); n < pageCount(got) {
		t.Errorf("页码数量少于页数：页码 %d，页数 %d", n, pageCount(got))
	}
	if !strings.Contains(got, "--gd-pageno-font: "+fontFamily(gbstandard.FontSongti)) {
		t.Errorf("页码字体未按 spec 输出：\n%s", got)
	}
	// 奇数页页码在右，偶数页在左（单面印刷惯例）。
	if !strings.Contains(got, "gd-pageno--right") || !strings.Contains(got, "gd-pageno--left") {
		t.Errorf("奇数页/偶数页的页码位置未区分")
	}
}

// 一个段落被改多次，仍然只是一个「已修改」的段落。
//
// 这条测试锁住一个容易搞错的计数口径：标注数应当等于**被改动的段落数**，
// 而不是改动条数。两者混淆时，用户看到 3 个高亮却只有 2 段有改动，
// 会以为自己漏看了一段，而「高亮」这个信号的可靠性正是「可审计」的前提。
func TestEditedMarkCountsParagraphsNotEdits(t *testing.T) {
	got := render(t, &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{
			Role: docmodel.RoleBody, NodeID: "n0001", Text: "改过两处的段落",
			Edited: true, EditKinds: []string{"typo", "grammar"},
		},
		{
			Role: docmodel.RoleBody, NodeID: "n0002", Text: "改过一处的段落",
			Edited: true, EditKinds: []string{"wording"},
		},
		{Role: docmodel.RoleBody, NodeID: "n0003", Text: "没改过的段落"},
	}}, testSpec())

	markup := markupOf(got)

	// Two paragraphs are marked, even though there are three edit kinds.
	if n := len(editedBlockRe.FindAllString(markup, -1)); n != 2 {
		t.Fatalf("应有 2 个段落被标注（改动段落数），实际 %d：\n%s", n, markup)
	}
	// The kind list must stay on the paragraph it belongs to, so the two
	// paragraphs are distinguishable rather than merged.
	if !strings.Contains(markup, `data-edit-kinds="typo,grammar"`) &&
		!strings.Contains(markup, `data-edit-kinds="typo grammar"`) {
		t.Errorf("多类型段落的改动类型未完整标注：\n%s", markup)
	}
	// And the paragraph with no edits must not be marked.
	for _, tag := range editedBlockRe.FindAllString(markup, -1) {
		if strings.Contains(tag, `data-node-id="n0003"`) {
			t.Errorf("未改动的段落不应被标注：%s", tag)
		}
	}
}

// editedBlockRe matches a marked block's opening tag across the whole fragment,
// including the stylesheet's comment — which is precisely the trap this guards.
var editedBlockRe = regexp.MustCompile(`<div[^>]*data-edited="1"[^>]*>`)

// ---------------------------------------------------------------- 改动标注

// 被声明的改动必须在预览里可见。
//
// 规则放宽为「原文可以被修改，但每一处修改都必须显式声明、可见、可审计」之后，
// 「可见」就成了渲染器的责任：装配器在块上置 Edited，预览必须把它变成用户
// 看得见的东西。若这里不输出标记，用户拿到的仍是一份看不出被改过的稿子，
// 「可审计」也就无从谈起。
func TestEditedBlockIsMarkedAndHighlighted(t *testing.T) {
	spec := testSpec()
	d := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, NodeID: "n0001", Text: "原文被改过的段落"},
		{
			Role: docmodel.RoleBody, NodeID: "n0002", Text: "这段的错别字已修正",
			Edited: true, EditKinds: []string{"typo"},
		},
	}}

	got := render(t, d, spec)
	markup := markupOf(got)

	// 只有被改动的块带标记，未改动的块不能带 —— 否则「高亮」失去信息量。
	if n := strings.Count(markup, `data-edited="1"`); n != 1 {
		t.Fatalf("应有且仅有 1 个块带 data-edited=\"1\"，实际 %d 个：\n%s", n, markup)
	}
	editedDiv := openingTagAt(markup, strings.Index(markup, `data-edited="1"`))
	if !strings.Contains(editedDiv, `data-node-id="n0002"`) {
		t.Errorf("标记落在了错误的块上：%s", editedDiv)
	}
	if !strings.Contains(editedDiv, `data-edit-kinds="typo"`) {
		t.Errorf("缺少改动类型属性，图例无法区分是错别字还是新增标题：%s", editedDiv)
	}

	// 高亮规则本身必须存在，并且读的是变量而不是写死的颜色。
	if !strings.Contains(got, ".gd-block[data-edited='1']") {
		t.Errorf("样式表里没有已改动块的高亮规则")
	}
	if !strings.Contains(got, "--gd-edited-bg:") || !strings.Contains(got, "--gd-edited-bar:") {
		t.Errorf("高亮配色未以 CSS 变量暴露，评审者无法核对")
	}
}

// 高亮**不得**改变盒模型：一旦它动到字号/行距/边距，屏幕上的分页就不再等于
// 成品，预览也就失去了作为「成品长什么样」的凭证的意义。
func TestEditedHighlightDoesNotTouchTypography(t *testing.T) {
	got := render(t, &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, Text: "段落", Edited: true, EditKinds: []string{"typo"}},
	}}, testSpec())

	block := ruleBlock(t, got, ".gd-block[data-edited='1']")
	for _, forbidden := range []string{
		"font-size", "line-height", "margin", "padding", "text-indent", "letter-spacing",
	} {
		if strings.Contains(block, forbidden) {
			t.Errorf("改动高亮里出现了 %q，会改变版面度量：\n%s", forbidden, block)
		}
	}
	// outline 不参与布局，正是因此才选它来画色条。
	if !strings.Contains(block, "outline") {
		t.Errorf("左侧色条应使用 outline（不影响布局），实际规则：\n%s", block)
	}
}

// 未被改动的块不得带任何改动标记 —— 否则「高亮=已修改」这条图例就是假的。
func TestUneditedBlockHasNoEditMarker(t *testing.T) {
	got := render(t, testDoc(bodyBlock("完全没被改过的原文")), testSpec())
	markup := markupOf(got)

	if strings.Contains(markup, `data-edited=`) {
		t.Errorf("未经改动的块出现了改动标记：\n%s", markup)
	}
	if strings.Contains(markup, `data-edit-kinds=`) {
		t.Errorf("未经改动的块出现了改动类型属性")
	}
}

// 高亮是屏幕上的复核提示，不是排版量值：正文与标题的国标字号必须原样保留，
// 无论块是否被改动。
func TestEditedBlockKeepsSpecTypography(t *testing.T) {
	spec := testSpec()
	got := render(t, &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, Text: "正文", Edited: true, EditKinds: []string{"wording"}},
		{Role: docmodel.RoleTitle, Text: "标题", Edited: true, EditKinds: []string{"typo"}},
	}}, spec)

	for _, want := range []string{"--gd-body-size: 16pt", "--gd-title-size: 22pt"} {
		if !strings.Contains(got, want) {
			t.Errorf("改动高亮影响了国标字号，缺少 %q", want)
		}
	}
}

// ---------------------------------------------------------------- robustness

// 空文档不应 panic，也要给出可渲染的空白页。
func TestRenderEmptyDocument(t *testing.T) {
	got := render(t, &docmodel.Doc{}, testSpec())
	if pageCount(got) != 1 {
		t.Errorf("空文档应输出 1 张空白纸，得到 %d 张", pageCount(got))
	}
}

// nil 文档返回错误而不是 panic：服务端要把它变成中文 500 提示。
func TestRenderNilDocument(t *testing.T) {
	if _, err := Render(nil, testSpec()); err == nil {
		t.Errorf("nil 文档应返回错误")
	}
}

// 未知 role 落到正文样式（Spec.MustStyle 的保证），不得 panic 或丢字。
func TestRenderUnknownRoleFallsBackToBody(t *testing.T) {
	got := render(t, testDoc(docmodel.OutBlock{
		Role: docmodel.Role("未来新增的元素"),
		Text: "内容仍然必须逐字出现",
	}), testSpec())

	if !strings.Contains(got, "内容仍然必须逐字出现") {
		t.Errorf("未知 role 的文本被丢弃：\n%s", got)
	}
	if !strings.Contains(got, "gd-role-") {
		t.Errorf("未知 role 未生成可样式化的 class")
	}
}

// 输出必须是片段而非完整文档：它要被嵌进应用页面。
func TestRenderEmitsFragmentNotFullDocument(t *testing.T) {
	got := render(t, testDoc(bodyBlock("正文")), testSpec())

	for _, forbidden := range []string{"<!doctype", "<html", "<head>", "<body"} {
		if strings.Contains(strings.ToLower(got), forbidden) {
			t.Errorf("预览应为 HTML 片段，却含有 %q", forbidden)
		}
	}
	if !strings.HasPrefix(got, "<style>") {
		t.Errorf("片段应以 <style> 块开头")
	}
}

// 输出必须可重复：同样的输入逐字节相同（服务端可能缓存或做 ETag）。
func TestRenderIsDeterministic(t *testing.T) {
	d := testDoc(bodyBlock("正文"), bodyBlock("第二段"))

	first := render(t, d, testSpec())
	second := render(t, d, testSpec())

	if first != second {
		t.Errorf("同样的输入产生了不同的输出，说明存在未排序的 map 迭代")
	}
}

// 表格与图片节点也要渲染，且内容逐字保留。
func TestRenderTableAndImage(t *testing.T) {
	spec := testSpec()
	d := &docmodel.Doc{Blocks: []docmodel.OutBlock{
		{Role: docmodel.RoleBody, Rows: [][]string{{"项目", "金额"}, {"差旅费", "1,000"}}},
		{Role: docmodel.RoleBody, NodeID: "n0007", Image: &docmodel.ImageRef{Name: "chart.png", MimeType: "image/png"}},
	}}

	got := render(t, d, spec)

	if !strings.Contains(got, "<td>差旅费</td>") {
		t.Errorf("表格单元格未渲染：\n%s", got)
	}
	if !strings.Contains(got, "<td>1,000</td>") {
		t.Errorf("表格单元格未逐字渲染")
	}
	if !strings.Contains(got, `src="images/n0007"`) {
		t.Errorf("图片未指向 images 端点：\n%s", got)
	}
	if !strings.Contains(got, `alt="chart.png"`) {
		t.Errorf("图片缺少 alt 文本")
	}
}

// 行数估算把全角字计为 1 字、半角计为 0.5 字，这是「每行28字」的含义。
func TestWidthUnitsCountsFullAndHalfWidth(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"公文", 2},
		{"abc", 1.5},
		{"公文abc", 3.5},
		{"2024年", 3}, // 4 个半角数字各半字 + 1 个全角汉字 = 3 字
		{"１２３", 3},   // 全角数字各占一字
		{"\t", 1},    // 制表位按一字推进
	}
	for _, c := range cases {
		if got := widthUnits(c.in); got != c.want {
			t.Errorf("widthUnits(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

// 国标的三个数字并非自然相容：版心 156mm、每行 28 字、正文三号 16pt，
// 28×16pt = 158mm 会超出 2mm。国标自带的 Word 模板靠文档网格钉住每行 28 字
// 并轻微压缩字距，预览必须复刻同一压缩，否则屏幕上每行只有 27 字左右，
// 与最终 .docx 不一致。
func TestBodyCondensesToExactlyCharsPerLine(t *testing.T) {
	spec := testSpec()
	got := render(t, testDoc(bodyBlock("正文")), spec)

	body := spec.Styles[docmodel.RoleBody]
	pitch := spec.Page.CharPitchPT()
	condense := spec.Page.CharCondensePT(body.Size)

	// 三号确实需要压缩，且压缩量 = 字号 − 字距。
	if spec.Page.FitsNaturalSpacing(body.Size) {
		t.Fatalf("正文三号按国标应需要压缩字距")
	}
	if condense <= 0 {
		t.Fatalf("CharCondensePT(三号) = %v，应为正值", condense)
	}
	if want := body.Size.Points() - pitch; math.Abs(condense-want) > 1e-9 {
		t.Errorf("CharCondensePT = %v，应为 字号 − 字距 = %v", condense, want)
	}

	// 样式里确实施加了负 letter-spacing，且用的是 API 的值。
	if !strings.Contains(got, "--gd-body-track: "+trimFloat(condense)+"pt") {
		t.Errorf("未输出 CharCondensePT 的值：期望 --gd-body-track: %spt", trimFloat(condense))
	}
	if !strings.Contains(got, "letter-spacing: calc(-1 * var(--gd-body-track))") {
		t.Errorf("正文未施加字距压缩，每行将不足 28 字：\n%s", got)
	}

	// 字号必须保持三号 16pt —— 压缩字距，不是缩小字号。
	if !strings.Contains(got, "--gd-body-size: 16pt") {
		t.Errorf("正文字号被改动，必须保持三号 16pt")
	}

	// 自检：版心宽 ÷ 有效字宽 ≈ 每行字数。
	effective := body.Size.Points() - condense
	perLine := spec.Page.TextWidthMM() * ptPerMM / effective
	if math.Abs(perLine-float64(spec.Page.CharsPerLine)) > 0.01 {
		t.Errorf("版心 %.3fmm ÷ 有效字宽 %.4fpt = %.3f 字/行，应为 %d 字/行",
			spec.Page.TextWidthMM(), effective, perLine, spec.Page.CharsPerLine)
	}
}

// 字号本身窄于「版心宽÷每行字数」时不得压缩（国标网格式排版只在必要时挤字）。
func TestNoLetterSpacingWhenNaturalSpacingFits(t *testing.T) {
	spec := testSpec()
	// 四号（14pt）小于字距 15.79pt，自然即可容纳 28 字。
	if !spec.Page.FitsNaturalSpacing(gbstandard.SizeSihao) {
		t.Fatalf("四号不应需要压缩字距")
	}
	if cond := spec.Page.CharCondensePT(gbstandard.SizeSihao); cond != 0 {
		t.Fatalf("CharCondensePT(四号) = %v，应为 0", cond)
	}

	got := render(t, testDoc(bodyBlock("正文")), spec)

	if !strings.Contains(got, "--gd-cc-track: 0pt") {
		t.Errorf("版记四号的压缩量应为 0pt")
	}
	// 抄送（cc）是四号：其规则块内不得出现 letter-spacing。
	if block := ruleBlock(t, got, ".gd-role-cc"); strings.Contains(block, "letter-spacing") {
		t.Errorf("四号版记不应有字距压缩：\n%s", block)
	}
}

// ruleBlock 取出样式表里某条选择器的规则体。
func ruleBlock(t *testing.T, css, selector string) string {
	t.Helper()
	start := strings.Index(css, selector+" {")
	if start < 0 {
		t.Fatalf("样式中找不到选择器 %s", selector)
	}
	rest := css[start:]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("选择器 %s 的规则体未闭合", selector)
	}
	return rest[:end]
}

// 正文栏宽必须直接取 spec.Page.TextWidthMM()（=156mm），而不是靠页面宽减
// padding 让文字自然流动 —— 后者会让栏宽随浏览器的盒模型变化。
func TestBodyColumnIsTextWidthFromSpec(t *testing.T) {
	spec := testSpec()
	got := render(t, testDoc(bodyBlock("正文")), spec)

	want := trimFloat(spec.Page.TextWidthMM()) + "mm"
	if want != "156mm" {
		t.Fatalf("版心宽应为 156mm，得到 %s", want)
	}
	if !strings.Contains(got, "--gd-text-w: "+want) {
		t.Errorf("未输出 spec 的版心宽")
	}
	if !strings.Contains(got, "width: var(--gd-text-w);") {
		t.Errorf("版心栏宽未引用 --gd-text-w：\n%s", got)
	}
	// 版心几何必须自洽：页面宽 − 左右页边距 = 版心宽。
	if got := spec.Page.WidthMM - spec.Page.MarginLeftMM - spec.Page.MarginRightMM; got != spec.Page.TextWidthMM() {
		t.Errorf("版心宽不自洽：%v", got)
	}
}

// 一页的容量由 spec 决定：22 行 × 28.99pt 应当正好填满 225mm 的版心。
func TestCapacityMatchesSpec(t *testing.T) {
	l := newLayout(testDoc(bodyBlock("正文")), testSpec())

	if l.capacity != 22 {
		t.Errorf("每面行数应取自 spec 的 22，得到 %v", l.capacity)
	}
	if want := 225 * ptPerMM / 22; l.linePT < want-0.01 || l.linePT > want+0.01 {
		t.Errorf("行距应为 %.4fpt，得到 %.4fpt", want, l.linePT)
	}
	if h := l.capacity * l.linePT; h < 225*ptPerMM-0.5 || h > 225*ptPerMM+0.5 {
		t.Errorf("22 行 × 行距 = %.2fpt，不等于版心高 %.2fpt", h, 225*ptPerMM)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
