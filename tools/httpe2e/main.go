// Command httpe2e drives the running server over real HTTP: it uploads a
// document, runs the analysis through the SSE stream, corrects the plan, and
// downloads the rendered .docx — the exact sequence the browser performs.
//
// The package tests each cover one endpoint with httptest, but they exercise
// handlers directly. This tool exists to prove the *wire* contract holds
// against a real process: multipart upload, EventSource framing, the JSON
// shapes, Content-Disposition on download. Those are the parts a unit test
// with an in-process handler can accidentally paper over.
//
// Usage:
//
//	go run ./tools/httpe2e -addr http://127.0.0.1:8795
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8795", "运行中的 govdoc 服务地址")
	outDir := flag.String("out", "", "输出目录（默认临时目录）")
	flag.Parse()

	base := strings.TrimRight(*addr, "/")
	c := &client{base: base, http: &http.Client{Timeout: 5 * time.Minute}}

	if err := run(c, *outDir); err != nil {
		fmt.Fprintf(os.Stderr, "\nHTTP 端到端验证失败: %v\n", err)
		os.Exit(1)
	}
}

type client struct {
	base string
	http *http.Client
}

func (c *client) do(method, path string, body io.Reader, ctype string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp, data, err
}

func fail(step string, resp *http.Response, data []byte) error {
	return fmt.Errorf("%s: HTTP %d: %s", step, resp.StatusCode, truncate(string(data), 300))
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func run(c *client, outDir string) error {
	if outDir == "" {
		d, err := os.MkdirTemp("", "govdoc-httpe2e-")
		if err != nil {
			return err
		}
		outDir = d
	}
	checks := 0
	ok := func(name, detail string) {
		checks++
		fmt.Printf("  ✓ %-30s %s\n", name, detail)
	}

	// ---- 0. 服务可达 ----
	fmt.Println("【1】服务连通性")
	resp, data, err := c.do(http.MethodGet, "/api/health", nil, "")
	if err != nil {
		return fmt.Errorf("无法连接 %s：%w（服务是否已启动？）", c.base, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fail("GET /api/health", resp, data)
	}
	var health struct {
		Status        string `json:"status"`
		Version       string `json:"version"`
		LLMConfigured bool   `json:"llm_configured"`
	}
	if err := json.Unmarshal(data, &health); err != nil {
		return fmt.Errorf("健康检查响应无法解析: %w", err)
	}
	ok("GET /api/health", fmt.Sprintf("status=%s version=%s llm=%v",
		health.Status, health.Version, health.LLMConfigured))
	if health.Status != "ok" {
		return fmt.Errorf("服务状态异常: %s", health.Status)
	}

	// ---- 1. 上传 ----
	fmt.Println("\n【2】上传文档（multipart）")
	source := sampleDoc()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "关于开展安全生产大检查的通知.txt")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(fw, source); err != nil {
		return err
	}
	mw.Close()

	resp, data, err = c.do(http.MethodPost, "/api/documents", &buf, mw.FormDataContentType())
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fail("POST /api/documents", resp, data)
	}
	var up struct {
		DocID        string `json:"doc_id"`
		SourceName   string `json:"source_name"`
		Format       string `json:"format"`
		NodeCount    int    `json:"node_count"`
		ContentCount int    `json:"content_count"`
		Nodes        []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &up); err != nil {
		return fmt.Errorf("上传响应无法解析: %w", err)
	}
	if up.DocID == "" || up.NodeCount == 0 {
		return fmt.Errorf("上传未返回有效的 doc_id 或节点")
	}
	ok("POST /api/documents", fmt.Sprintf("doc_id=%s 节点=%d 正文=%d",
		up.DocID[:8]+"…", up.NodeCount, up.ContentCount))

	// The verbatim guarantee starts at the wire: what the server parsed must
	// contain the file's own sentences.
	joined := ""
	for _, n := range up.Nodes {
		joined += n.Text + "\n"
	}
	missing := 0
	for _, line := range strings.Split(strings.TrimSpace(source), "\n\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(joined, line) {
			missing++
			fmt.Printf("      ✗ 原文未完整解析: %s\n", truncate(line, 40))
		}
	}
	if missing > 0 {
		return fmt.Errorf("上传解析丢失了 %d 段原文", missing)
	}
	ok("原文逐字解析", "全部段落均在节点中逐字出现")

	// ---- 2. 取回文档 ----
	resp, data, err = c.do(http.MethodGet, "/api/documents/"+up.DocID, nil, "")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fail("GET /api/documents/{id}", resp, data)
	}
	ok("GET /api/documents/{id}", fmt.Sprintf("%d 字节", len(data)))

	// ---- 3. 配置（若未配置，则跳过真实分析）----
	fmt.Println("\n【3】分析（SSE 进度流）")
	if !health.LLMConfigured {
		_, data, _ := c.do(http.MethodPost, "/api/documents/"+up.DocID+"/plan",
			strings.NewReader("{}"), "application/json")
		var eb struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &eb)
		if eb.Error.Code != "llm_not_configured" {
			return fmt.Errorf("未配置模型时应返回 llm_not_configured, 实际 %q: %s",
				eb.Error.Code, truncate(string(data), 200))
		}
		ok("未配置模型的错误处理", eb.Error.Code+"："+truncate(eb.Error.Message, 40))
		fmt.Println("      （未配置模型，跳过真实分析；以下用 PUT 提交一份方案继续验证后续环节）")

		if err := verifyPlanAndRender(c, up.DocID, up.Nodes, outDir, ok); err != nil {
			return err
		}
	} else {
		if err := analyzeAndRender(c, up.DocID, outDir, ok); err != nil {
			return err
		}
	}

	// ---- 5. 预览 ----
	fmt.Println("\n【5】预览")
	resp, data, err = c.do(http.MethodGet, "/api/documents/"+up.DocID+"/preview", nil, "")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fail("GET /preview", resp, data)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		return fmt.Errorf("预览 Content-Type = %q", ct)
	}
	html := string(data)
	// The preview must contain the document's own prose verbatim. The string
	// checked has to be one that is actually in sampleDoc() — a mismatch here
	// reports a renderer bug that does not exist.
	if !strings.Contains(html, "各县（市、区）人民政府") {
		return fmt.Errorf("预览未包含正文内容（收件机关那段）")
	}
	if !strings.Contains(html, "坚持“人民至上、生命至上”") {
		return fmt.Errorf("预览未包含正文内容（已转义的引号段落）")
	}
	// The generated stylesheet must carry the 国标 measurements, which is what
	// makes the preview a faithful stand-in for the printed page.
	for _, want := range []string{"--gd-page-w: 210mm", "--gd-page-h: 297mm", "--gd-text-w: 156mm"} {
		if !strings.Contains(html, want) {
			return fmt.Errorf("预览样式缺少 %q（预览必须由 spec 生成）", want)
		}
	}
	if !strings.Contains(html, "gd-page") {
		return fmt.Errorf("预览缺少分页容器")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		return fmt.Errorf("预览缺少限制性 CSP: %q", csp)
	}
	ok("GET /preview", fmt.Sprintf("%d 字节 HTML，含国标量值与分页容器", len(html)))

	// ---- 6. 错误路径 ----
	fmt.Println("\n【6】错误路径")
	resp, data, _ = c.do(http.MethodGet, "/api/documents/不存在的ID", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("未知文档应返回 404, 实际 %d", resp.StatusCode)
	}
	var eb struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &eb)
	if eb.Error.Code != "not_found" {
		return fmt.Errorf("未知文档的错误码应为 not_found, 实际 %q", eb.Error.Code)
	}
	ok("未知文档 → 404", "code=not_found")

	resp, data, _ = c.do(http.MethodGet, "/api/nonexistent-endpoint", nil, "")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return fmt.Errorf("未知接口应返回 JSON 404, 实际 %d %s",
			resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	ok("未知接口 → JSON 404", "不会误返回 SPA 页面")

	resp, _, _ = c.do(http.MethodGet, "/api/files/nonexistent", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("未知文件应返回 404, 实际 %d", resp.StatusCode)
	}
	ok("未知文件 → 404", "")

	fmt.Printf("\nHTTP 端到端验证全部通过 ✓（%d 项检查）\n", checks)
	fmt.Printf("输出目录: %s\n", outDir)
	return nil
}

// headingLevel derives a 层次序数 depth from a heading's own numbering form:
// "一、" → 1, "（一）" → 2, "1." → 3, "（1）" → 4. Text with no number is
// treated as level 1.
func headingLevel(text string) int {
	t := strings.TrimLeft(text, " \t\u3000")
	if t == "" {
		return 1
	}
	r := []rune(t)
	switch {
	case r[0] == '（' || r[0] == '(':
		// （一） is level 2, （1） is level 4.
		inner := []rune(strings.TrimLeft(t, "（("))
		if len(inner) > 0 && inner[0] >= '0' && inner[0] <= '9' {
			return 4
		}
		return 2
	case r[0] >= '0' && r[0] <= '9':
		return 3
	default:
		return 1
	}
}

// sampleDoc is a plain-text 公文 with the shape a real upload has: a title, a
// 主送机关, a 缘由 paragraph, three numbered chapters with sub-levels, a
// table, and a 署名/日期.
func sampleDoc() string {
	return `关于开展安全生产大检查的通知

各县（市、区）人民政府，市直各部门：

为深入贯彻落实上级关于安全生产工作的部署要求，经研究决定，在全市范围内开展安全生产大检查。现将有关事项通知如下。

一、总体要求

坚持“人民至上、生命至上”，全面落实安全生产责任制，坚决防范遏制重特大事故。

二、检查范围与重点

（一）检查范围。全市各类生产经营单位，重点是危险化学品、矿山、建筑施工、交通运输等行业领域。

（二）检查重点。安全生产责任制落实情况；风险分级管控与隐患排查治理双重预防机制建设情况。

三、工作要求

1.加强组织领导。各地区、各部门要高度重视，主要负责同志要亲自部署、亲自督办。

2.严格执法检查。对检查发现的违法违规行为，要依法依规严肃查处，绝不姑息。

某某市人民政府办公室

2024年5月8日`
}

// analyzeAndRender runs a real analysis and follows the SSE stream.
func analyzeAndRender(c *client, docID, outDir string, ok func(string, string)) error {
	resp, data, err := c.do(http.MethodPost, "/api/documents/"+docID+"/plan",
		strings.NewReader(`{"hint":"这是一份通知"}`), "application/json")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusAccepted {
		return fail("POST /plan", resp, data)
	}
	var job struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(data, &job); err != nil {
		return fmt.Errorf("启动分析响应无法解析: %w", err)
	}
	ok("POST /plan → job_id", job.JobID[:8]+"…")

	// Follow the SSE stream exactly as EventSource would.
	req, err := http.NewRequest(http.MethodGet, c.base+"/api/jobs/"+job.JobID+"/events", nil)
	if err != nil {
		return err
	}
	sresp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE 状态码 = %d", sresp.StatusCode)
	}
	if ct := sresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return fmt.Errorf("SSE Content-Type = %q", ct)
	}

	var stages []string
	var donePayload string
	sc := bufio.NewScanner(sresp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			if event == "stage" {
				var st struct {
					Message string `json:"message"`
					Done    bool   `json:"done"`
				}
				if json.Unmarshal([]byte(payload), &st) == nil && st.Message != "" {
					stages = append(stages, st.Message)
				}
			}
			if event == "done" {
				donePayload = payload
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("读取 SSE 流失败: %w", err)
	}
	if len(stages) == 0 {
		return fmt.Errorf("SSE 流未收到任何进度消息")
	}
	if donePayload == "" {
		return fmt.Errorf("SSE 流未收到 done 事件（进度条会永久挂起）")
	}
	ok("SSE 进度流", fmt.Sprintf("%d 条进度：%s", len(stages), strings.Join(stages, " → ")))

	var done struct {
		Plan   json.RawMessage `json:"plan"`
		Report struct {
			ContentNodes int `json:"content_nodes"`
			CoveredNodes int `json:"covered_nodes"`
		} `json:"report"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(donePayload), &done); err != nil {
		return fmt.Errorf("done 事件无法解析: %w", err)
	}
	if done.Error != "" {
		return fmt.Errorf("分析失败（检查模型配置）：%s", done.Error)
	}
	if done.Plan == nil {
		return fmt.Errorf("done 事件未携带方案")
	}
	ok("done 事件", fmt.Sprintf("正文 %d/%d 已覆盖",
		done.Report.CoveredNodes, done.Report.ContentNodes))

	return getPlanAndRender(c, docID, outDir, ok)
}

// verifyPlanAndRender is the path taken when no model is configured: submit a
// plan directly, then render. It still exercises plan storage, verification,
// rendering and download over the wire.
func verifyPlanAndRender(c *client, docID string, nodes []struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}, outDir string, ok func(string, string)) error {
	// Build a plan that claims every paragraph, modelled the way a correct
	// solution actually looks. Earlier versions of this tool made two mistakes
	// that both produced a visibly duplicated document, and both are now
	// caught by VerifyPlan — which is the point: this tool should not be the
	// thing that trips the checks.
	//
	//   - putting the title in meta *and* leaving the source's title paragraph
	//     in the body printed it twice (duplicate_title);
	//   - listing a heading paragraph as section *body* made the assembler
	//     render it as a heading again (heading_as_body).
	//
	// So: bind 标题/主送机关/署名/成文日期 to their element slots, treat the
	// 缘由 as preamble, give each source heading its own section, and put the
	// remaining prose in the section that follows it.
	type section struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Level       int      `json:"level"`
		HeadingID   string   `json:"heading_node_id,omitempty"`
		BodyNodeIDs []string `json:"body_node_ids,omitempty"`
	}

	var preamble []string
	elements := []map[string]any{}
	var sections []section
	var current *section

	for _, n := range nodes {
		if n.Kind == "page_break" {
			continue
		}
		switch {
		case strings.Contains(n.Text, "关于开展安全生产大检查的通知"):
			elements = append(elements, map[string]any{"role": "title", "node_id": n.ID})
		case strings.HasPrefix(n.Text, "各县（市、区）人民政府"):
			elements = append(elements, map[string]any{"role": "recipient", "node_id": n.ID})
		case strings.Contains(n.Text, "某某市人民政府办公室"):
			elements = append(elements, map[string]any{"role": "signature", "node_id": n.ID})
		case strings.HasPrefix(n.Text, "2024年"):
			elements = append(elements, map[string]any{"role": "date", "node_id": n.ID})
		case strings.HasPrefix(n.Text, "为深入贯彻落实"):
			preamble = append(preamble, n.ID)
		case n.Kind == "heading":
			// Each source heading becomes its own chapter, named by that node
			// so the title stays verbatim. The level is derived from the
			// heading's own 层次序数 form ("一、" → 1, "（一）" → 2, …), which
			// is what a correct plan would report; hard-coding 1 here would
			// make the assembler print "三、（一）检查范围。".
			sections = append(sections, section{
				ID:        fmt.Sprintf("s%d", len(sections)+1),
				Title:     n.Text,
				Level:     headingLevel(n.Text),
				HeadingID: n.ID,
			})
			current = &sections[len(sections)-1]
		default:
			if current != nil {
				current.BodyNodeIDs = append(current.BodyNodeIDs, n.ID)
			} else {
				preamble = append(preamble, n.ID)
			}
		}
	}

	plan := map[string]any{
		"meta": map[string]any{
			"issuing_org": "某某市人民政府办公室文件",
			"doc_number":  "某政办发〔2024〕15号",
		},
		"elements":          elements,
		"preamble_node_ids": preamble,
		"sections":          sections,
	}
	payload, _ := json.Marshal(plan)
	resp, data, err := c.do(http.MethodPut, "/api/documents/"+docID+"/plan",
		bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fail("PUT /plan", resp, data)
	}
	var pr struct {
		Report struct {
			OK           bool `json:"-"`
			ContentNodes int  `json:"content_nodes"`
			CoveredNodes int  `json:"covered_nodes"`
			Diagnostics  []struct {
				Code     string `json:"code"`
				Severity string `json:"severity"`
				Message  string `json:"message"`
			} `json:"diagnostics"`
		} `json:"report"`
	}
	if err := json.Unmarshal(data, &pr); err != nil {
		return fmt.Errorf("PUT /plan 响应无法解析: %w", err)
	}
	ok("PUT /plan（提交方案）", fmt.Sprintf("正文 %d/%d 已覆盖",
		pr.Report.CoveredNodes, pr.Report.ContentNodes))

	if pr.Report.CoveredNodes != pr.Report.ContentNodes {
		for _, d := range pr.Report.Diagnostics {
			if d.Severity == "error" {
				fmt.Printf("      ✗ [%s] %s\n", d.Code, d.Message)
			}
		}
		return fmt.Errorf("方案未覆盖全部正文（%d/%d）",
			pr.Report.CoveredNodes, pr.Report.ContentNodes)
	}

	return getPlanAndRender(c, docID, outDir, ok)
}

// getPlanAndRender renders, downloads and inspects the .docx over the wire.
func getPlanAndRender(c *client, docID, outDir string, ok func(string, string)) error {
	fmt.Println("\n【4】导出与下载")

	resp, data, err := c.do(http.MethodPost, "/api/documents/"+docID+"/render",
		strings.NewReader(`{"format":"docx"}`), "application/json")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fail("POST /render", resp, data)
	}
	var rr struct {
		FileID    string   `json:"file_id"`
		Filename  string   `json:"filename"`
		Size      int      `json:"size"`
		SynthText []string `json:"synth_text"`
		Report    struct {
			ContentNodes int `json:"content_nodes"`
			CoveredNodes int `json:"covered_nodes"`
		} `json:"report"`
		Diff struct {
			Missing []string `json:"missing"`
			Altered []string `json:"altered"`
			Extra   []string `json:"extra"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(data, &rr); err != nil {
		return fmt.Errorf("导出响应无法解析: %w", err)
	}
	if rr.FileID == "" || rr.Size == 0 {
		return fmt.Errorf("导出未返回有效文件")
	}
	if len(rr.Diff.Missing) > 0 || len(rr.Diff.Altered) > 0 {
		return fmt.Errorf("逐字比对未通过: 丢失=%v 改写=%v", rr.Diff.Missing, rr.Diff.Altered)
	}
	ok("POST /render", fmt.Sprintf("%s，%d 字节，逐字比对通过", rr.Filename, rr.Size))
	ok("导出审计信息", fmt.Sprintf("非原文文字 %d 处，额外文本 %d 处",
		len(rr.SynthText), len(rr.Diff.Extra)))

	// Download.
	dresp, ddata, err := c.do(http.MethodGet, "/api/files/"+rr.FileID, nil, "")
	if err != nil {
		return err
	}
	if dresp.StatusCode != http.StatusOK {
		return fail("GET /files/{id}", dresp, ddata)
	}
	if len(ddata) < 4 || ddata[0] != 'P' || ddata[1] != 'K' {
		return fmt.Errorf("下载内容不是有效 zip/docx")
	}
	cd := dresp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "filename*=UTF-8''") {
		return fmt.Errorf("下载缺少 UTF-8 文件名: %q", cd)
	}
	if ct := dresp.Header.Get("Content-Type"); !strings.Contains(ct, "wordprocessingml") {
		return fmt.Errorf("下载 Content-Type = %q", ct)
	}
	ok("GET /files/{id}", fmt.Sprintf("%d 字节，中文文件名已正确编码", len(ddata)))

	path := filepath.Join(outDir, rr.Filename)
	// -out may name a directory that does not exist yet.
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	if err := os.WriteFile(path, ddata, 0o644); err != nil {
		return err
	}
	fmt.Printf("      已保存: %s\n", path)
	return nil
}
