package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"govdoc/internal/api"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// ---------------------------------------------------------------- test rig

// stubAnalyzer is a deterministic Analyzer. It is the reason this package can
// be tested end to end without a model: the entire pipeline — upload, plan,
// verify, edit, render, download — is exercised exactly as in production,
// with only the network call replaced.
type stubAnalyzer struct {
	// plan is returned by Analyze. When nil, an error is returned instead.
	plan *docmodel.Plan
	err  error
	// stages are published before returning.
	stages []Progress
	// gotHint records the hint the server passed through.
	gotHint string
	// release, when non-nil, blocks Analyze until closed (for SSE tests).
	release chan struct{}
	// started is closed as soon as Analyze begins.
	started chan struct{}
	// calls counts invocations, to prove the analysis ran once.
	calls int
	// gotAllowEdits records the per-request edit permission the server passed.
	gotAllowEdits bool
}

func (s *stubAnalyzer) Analyze(ctx context.Context, doc *docmodel.Document, opts AnalysisOptions, progress func(Progress)) (*docmodel.Plan, error) {
	s.calls++
	s.gotHint = opts.Hint
	s.gotAllowEdits = opts.AllowEdits
	if s.started != nil {
		close(s.started)
	}
	for _, st := range s.stages {
		if progress != nil {
			progress(st)
		}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return s.plan, s.err
	}
	return s.plan, nil
}

// planCovering builds a plan that covers every prose node of doc with one
// section, which is the minimum a renderable plan needs.
func planCovering(doc *docmodel.Document, title string) *docmodel.Plan {
	plan := &docmodel.Plan{Meta: docmodel.Meta{Title: title}}
	var body []string
	sec := docmodel.Section{ID: "s1", Title: title, Level: 1}
	for _, n := range doc.ContentNodes() {
		switch n.Kind {
		case docmodel.KindHeading:
			// A heading can head the section; the rest becomes body.
			if sec.HeadingNodeID == "" {
				sec.HeadingNodeID = n.ID
				sec.Title = n.Text
				continue
			}
		}
		body = append(body, n.ID)
	}
	sec.BodyNodeIDs = body
	plan.Sections = []docmodel.Section{sec}
	return plan
}

type rig struct {
	srv    *Server
	http   *httptest.Server
	stub   *stubAnalyzer
	client *http.Client
}

func newRig(t *testing.T, configure bool) *rig {
	t.Helper()
	stub := &stubAnalyzer{}
	cfg := Config{
		Spec: gbstandard.Default(),
		LLM: LLMConfig{
			Endpoint: "http://stub.invalid/v1",
			APIKey:   "test-key",
			Model:    "stub-model",
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if !configure {
		cfg.LLM = LLMConfig{}
	}
	s := New(cfg)
	s.SetAnalyzer(stub)

	// A nil static FS exercises the "UI not packaged" path, which is what a
	// binary built without a front end must report rather than 404 silently.
	ts := httptest.NewServer(s.Handler(nil))
	t.Cleanup(ts.Close)
	return &rig{srv: s, http: ts, stub: stub, client: ts.Client()}
}

// upload posts a plain-text document.
func (r *rig) upload(t *testing.T, name, content string) api.UploadResponse {
	t.Helper()
	return r.uploadWithFields(t, name, content, nil)
}

// uploadWithFields uploads with extra multipart fields.
//
// The profile selection travels as a form field on the upload, so exercising it
// has to go through the same multipart request the browser sends — building the
// body any other way would test a path no client uses.
func (r *rig) uploadWithFields(t *testing.T, name, content string, fields map[string]string) api.UploadResponse {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := io.WriteString(fw, content); err != nil {
		t.Fatalf("write form: %v", err)
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, r.http.URL+"/api/documents", &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("上传失败: %d %s", resp.StatusCode, body)
	}
	var out api.UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode upload: %v", err)
	}
	return out
}

// do performs a JSON request and decodes the response.
func (r *rig) do(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.http.URL+path, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, data
}

// startPlanAndWait runs an analysis to completion.
func (r *rig) startPlanAndWait(t *testing.T, docID string) api.PlanResponse {
	t.Helper()
	resp, data := r.do(t, http.MethodPost, "/api/documents/"+docID+"/plan", api.PlanRequest{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("启动分析失败: %d %s", resp.StatusCode, data)
	}
	var job api.PlanJobResponse
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}

	// The job runs in the background; poll the stored result rather than
	// parsing SSE, which the SSE test covers separately.
	stored, err := r.srv.store.Job(job.JobID)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if !stored.Wait(5 * time.Second) {
		t.Fatal("分析未在时限内完成")
	}
	_, _, err, _ = stored.Result()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}

	resp, data = r.do(t, http.MethodGet, "/api/documents/"+docID+"/plan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("获取方案失败: %d %s", resp.StatusCode, data)
	}
	var out api.PlanResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	return out
}

// ---------------------------------------------------------------- health

func TestHealth(t *testing.T) {
	r := newRig(t, true)
	resp, data := r.do(t, http.MethodGet, "/api/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	var h api.Health
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.Status != "ok" || h.Version == "" {
		t.Errorf("健康检查内容异常: %+v", h)
	}
	if !h.LLMConfigured {
		t.Error("已配置模型时应报告 llm_configured=true")
	}
}

func TestHealthReportsUnconfigured(t *testing.T) {
	r := newRig(t, false)
	_, data := r.do(t, http.MethodGet, "/api/health", nil)
	var h api.Health
	_ = json.Unmarshal(data, &h)
	if h.LLMConfigured {
		t.Error("未配置模型时应报告 llm_configured=false")
	}
}

// ---------------------------------------------------------------- config

// TestAPIKeyIsNeverEchoed is a security property, not a nicety: the settings
// response is fetched on every page load, so echoing the key would put the
// credential in the browser's memory, devtools and any proxy log.
func TestAPIKeyIsNeverEchoed(t *testing.T) {
	r := newRig(t, true)

	resp, data := r.do(t, http.MethodGet, "/api/config", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	if bytes.Contains(data, []byte("test-key")) {
		t.Fatalf("配置响应泄漏了 API Key: %s", data)
	}

	var cfg api.ConfigResponse
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.LLM.APIKeySet {
		t.Error("应报告 api_key_set=true")
	}
	if cfg.LLM.APIKey != "" {
		t.Errorf("APIKey 字段必须为空, 实际 %q", cfg.LLM.APIKey)
	}
}

func TestPutConfigUpdatesWithoutEchoingKey(t *testing.T) {
	r := newRig(t, false)

	newModel := "deepseek-chat"
	newKey := "sk-secret-value"
	resp, data := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{
		Model:  &newModel,
		APIKey: &newKey,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", resp.StatusCode, data)
	}
	if bytes.Contains(data, []byte(newKey)) {
		t.Fatalf("更新响应泄漏了 API Key: %s", data)
	}
	var cfg api.ConfigResponse
	_ = json.Unmarshal(data, &cfg)
	if cfg.LLM.Model != newModel {
		t.Errorf("模型未更新: %q", cfg.LLM.Model)
	}
	if !cfg.LLM.APIKeySet {
		t.Error("保存后应报告 api_key_set=true")
	}
}

// TestPutConfigEmptyFieldsLeaveValuesAlone: the UI saves the model without
// resending the key, so an omitted field must not wipe the stored value.
func TestPutConfigEmptyFieldsLeaveValuesAlone(t *testing.T) {
	r := newRig(t, true)
	before := r.srv.currentLLM()

	model := "another-model"
	if resp, data := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{Model: &model}); resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", resp.StatusCode, data)
	}

	after := r.srv.currentLLM()
	if after.Model != "another-model" {
		t.Errorf("模型未更新: %q", after.Model)
	}
	if after.APIKey != before.APIKey {
		t.Error("未提交 api_key 时不应清空已保存的 Key")
	}
	if after.Endpoint != before.Endpoint {
		t.Error("未提交 endpoint 时不应改动")
	}
}

func TestPutConfigRejectsBadValues(t *testing.T) {
	r := newRig(t, true)

	badProto := "grpc"
	if resp, _ := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{Protocol: &badProto}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法协议应返回 400, 实际 %d", resp.StatusCode)
	}
	badDir := gbstandard.Direction("斜行文")
	if resp, _ := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{Direction: &badDir}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法行文方向应返回 400, 实际 %d", resp.StatusCode)
	}
}

// TestPutConfigRejectsUnknownFields: a typo in the client must be reported,
// not silently ignored — a silently dropped api_key would look like a save
// that did nothing.
func TestPutConfigRejectsUnknownFields(t *testing.T) {
	r := newRig(t, true)
	req, _ := http.NewRequest(http.MethodPut, r.http.URL+"/api/config",
		strings.NewReader(`{"model":"m","api_ky":"typo"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("未知字段应返回 400, 实际 %d", resp.StatusCode)
	}
}

func TestSpecEndpoint(t *testing.T) {
	r := newRig(t, true)
	resp, data := r.do(t, http.MethodGet, "/api/spec", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	var sr api.SpecResponse
	if err := json.Unmarshal(data, &sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sr.Problems) > 0 {
		t.Errorf("默认格式规范应无问题: %v", sr.Problems)
	}
	if sr.Spec.Page.WidthMM != 210 {
		t.Errorf("页宽 = %v, 期望 210mm", sr.Spec.Page.WidthMM)
	}
	// The font report must cover the standard's typefaces so the UI can warn
	// about a missing 仿宋 before the user prints.
	if len(sr.Fonts) == 0 {
		t.Fatal("字体可用性报告为空")
	}
	names := map[string]bool{}
	for _, f := range sr.Fonts {
		names[f.Name] = true
		if len(f.Fallbacks) == 0 {
			t.Errorf("字体 %s 缺少回退链", f.Name)
		}
	}
	for _, want := range []string{gbstandard.FontFangSong, gbstandard.FontXiaoBiaoSong, gbstandard.FontHeiti} {
		if !names[want] {
			t.Errorf("字体报告缺少 %s", want)
		}
	}
}

// ---------------------------------------------------------------- upload

func TestUploadTextDocument(t *testing.T) {
	r := newRig(t, true)
	content := "关于开展某项工作的通知\n\n各有关单位：\n\n现将有关事项通知如下。"

	up := r.upload(t, "通知.txt", content)
	if up.DocID == "" {
		t.Fatal("未返回 doc_id")
	}
	if up.Format != "txt" {
		t.Errorf("格式 = %q, 期望 txt", up.Format)
	}
	if up.NodeCount == 0 {
		t.Fatal("未解析出任何节点")
	}
	if up.ContentCount != up.NodeCount {
		t.Errorf("正文节点数 %d 与节点总数 %d 不一致（txt 无分页符）", up.ContentCount, up.NodeCount)
	}

	// The uploaded text must appear verbatim in the node list.
	joined := ""
	for _, n := range up.Nodes {
		joined += n.Text
	}
	if !strings.Contains(joined, "现将有关事项通知如下。") {
		t.Errorf("原文未逐字保留: %q", joined)
	}
}

func TestUploadRejectsUnsupportedFormat(t *testing.T) {
	r := newRig(t, true)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "报告.pdf")
	_, _ = io.WriteString(fw, "%PDF-1.4 not really")
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, r.http.URL+"/api/documents", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("不支持的格式应返回 400, 实际 %d", resp.StatusCode)
	}
	var eb api.ErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&eb); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if eb.Error.Code != api.CodeParseFailed {
		t.Errorf("错误码 = %q, 期望 %q", eb.Error.Code, api.CodeParseFailed)
	}
}

func TestUploadRejectsMissingFile(t *testing.T) {
	r := newRig(t, true)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, r.http.URL+"/api/documents", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("缺少 file 字段应返回 400, 实际 %d", resp.StatusCode)
	}
}

func TestGetDocumentAndUnknownID(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容")

	resp, data := r.do(t, http.MethodGet, "/api/documents/"+up.DocID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	var dr api.DocumentResponse
	if err := json.Unmarshal(data, &dr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dr.Doc == nil || len(dr.Doc.Nodes) == 0 {
		t.Error("未返回文档节点")
	}

	resp, _ = r.do(t, http.MethodGet, "/api/documents/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未知文档应返回 404, 实际 %d", resp.StatusCode)
	}
}

// TestUploadSanitisesFileName: a crafted name must not influence the output
// name's directory, and the response must not echo a path.
func TestUploadSanitisesFileName(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, `..\..\evil\公文.txt`, "内容")
	if strings.Contains(up.SourceName, "..") || strings.ContainsAny(up.SourceName, `\/`) {
		t.Errorf("文件名未被清理: %q", up.SourceName)
	}
}

// ---------------------------------------------------------------- plan

func TestPlanRequiresConfiguration(t *testing.T) {
	r := newRig(t, false)
	up := r.upload(t, "a.txt", "内容")

	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan", api.PlanRequest{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未配置模型应返回 400, 实际 %d", resp.StatusCode)
	}
	var eb api.ErrorBody
	_ = json.Unmarshal(data, &eb)
	if eb.Error.Code != api.CodeNoLLMConfig {
		t.Errorf("错误码 = %q, 期望 %q", eb.Error.Code, api.CodeNoLLMConfig)
	}
	// The message must tell the user what to do.
	if !strings.Contains(eb.Error.Message, "API Key") {
		t.Errorf("错误信息应指明如何配置: %q", eb.Error.Message)
	}
}

func TestPlanStoresVerifiedResult(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "通知.txt", "第一段。\n\n第二段。")
	// Build the stub's answer from the real parsed document so it covers
	// everything and is therefore renderable.
	sess, _ := r.srv.store.Session(up.DocID)
	r.stub.plan = planCovering(sess.Doc, "关于测试的通知")
	r.stub.stages = []Progress{
		{Name: "connect", Message: "正在连接模型…"},
		{Name: "analyze", Message: "正在分析文档结构…"},
	}

	got := r.startPlanAndWait(t, up.DocID)
	if got.Plan == nil {
		t.Fatal("未返回方案")
	}
	if got.Report == nil {
		t.Fatal("未返回校验结果")
	}
	if !got.Report.OK() {
		t.Fatalf("方案应通过校验: %s\n%+v", got.Report.Summary(), got.Report.Diagnostics)
	}
	if got.Report.CoveredNodes != got.Report.ContentNodes {
		t.Errorf("覆盖不全: %d/%d", got.Report.CoveredNodes, got.Report.ContentNodes)
	}
	if r.stub.calls != 1 {
		t.Errorf("分析应只执行一次, 实际 %d", r.stub.calls)
	}
}

// TestServerVerifiesEvenWhenAnalyzerClaimsSuccess is the guard on the single
// choke point: an analyzer that returns a lossy plan must not get it stored
// as renderable just because it reported no error.
func TestServerVerifiesEvenWhenAnalyzerClaimsSuccess(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "第一段。\n\n第二段会被丢掉。")

	// A plan that covers nothing: the analyzer "succeeded".
	r.stub.plan = &docmodel.Plan{Sections: []docmodel.Section{}}

	got := r.startPlanAndWait(t, up.DocID)
	if got.Report.OK() {
		t.Fatal("服务器必须自行校验，不能仅凭 analyzer 无错误就认定可渲染")
	}
	if got.Report.CoveredNodes != 0 {
		t.Errorf("覆盖数 = %d, 期望 0", got.Report.CoveredNodes)
	}
	var found bool
	for _, d := range got.Report.Diagnostics {
		if d.Code == "uncovered_content" {
			found = true
		}
	}
	if !found {
		t.Errorf("应报告内容丢失: %+v", got.Report.Diagnostics)
	}
}

// TestPlanFailureWithPartialPlanIsStored: when the self-repair loop gives up,
// showing the user the rejected plan plus the reason is far more useful than
// a blank screen.
func TestPlanFailureWithPartialPlanIsStored(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容。")
	sess, _ := r.srv.store.Session(up.DocID)

	r.stub.plan = planCovering(sess.Doc, "标题")
	r.stub.err = fmt.Errorf("模型未能生成合规结构（尝试 3 次）")

	_, _ = r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan", api.PlanRequest{})

	// Wait for the job to settle, then confirm the partial plan is readable.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, data := r.do(t, http.MethodGet, "/api/documents/"+up.DocID+"/plan", nil)
		if strings.Contains(string(data), "标题") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("失败时仍应保留部分方案供用户查看")
}

func TestGetPlanBeforeAnalysis(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容")
	resp, _ := r.do(t, http.MethodGet, "/api/documents/"+up.DocID+"/plan", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("尚未分析应返回 404, 实际 %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- SSE

// TestSSEStreamsStagesAndResult drives the real EventSource contract: the
// stages arrive in order and the terminal frame carries the plan plus verdict.
func TestSSEStreamsStagesAndResult(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容。")
	sess, _ := r.srv.store.Session(up.DocID)

	r.stub.plan = planCovering(sess.Doc, "标题")
	r.stub.stages = []Progress{
		{Name: "connect", Message: "正在连接模型…"},
		{Name: "analyze", Message: "正在分析文档结构…"},
	}
	r.stub.release = make(chan struct{})

	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan", api.PlanRequest{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("状态码 = %d: %s", resp.StatusCode, data)
	}
	var job api.PlanJobResponse
	_ = json.Unmarshal(data, &job)

	// Open the SSE stream while the analysis is still blocked, so this also
	// proves live delivery rather than replay.
	req, _ := http.NewRequest(http.MethodGet, r.http.URL+"/api/jobs/"+job.JobID+"/events", nil)
	streamResp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("SSE: %v", err)
	}
	defer streamResp.Body.Close()
	if ct := streamResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, 期望 text/event-stream", ct)
	}

	// Unblock the analysis; the stream must then deliver the terminal frame.
	close(r.stub.release)

	body, _ := io.ReadAll(streamResp.Body)
	text := string(body)
	for _, want := range []string{"event: stage", "正在连接模型", "正在分析文档结构", "event: done"} {
		if !strings.Contains(text, want) {
			t.Errorf("SSE 流缺少 %q\n实际内容:\n%s", want, text)
		}
	}

	// The terminal frame must carry the verdict, so the client never has to
	// display a plan without knowing whether it renders.
	idx := strings.Index(text, "event: done")
	if idx < 0 {
		t.Fatal("缺少 done 事件")
	}
	doneLine := text[idx:]
	var payload string
	for _, line := range strings.Split(doneLine, "\n") {
		if strings.HasPrefix(line, "data: ") {
			payload = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	var ev api.DoneEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("done 事件无法解析: %v\n%s", err, payload)
	}
	if ev.Plan == nil || ev.Report == nil {
		t.Errorf("done 事件应包含方案与校验结果: %+v", ev)
	}
	if ev.Error != "" {
		t.Errorf("分析应当成功: %s", ev.Error)
	}
}

// TestSSEReplaysHistoryToLateClient: a browser that reloads mid-analysis must
// still see the stages it missed.
func TestSSEReplaysHistoryToLateClient(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容。")
	sess, _ := r.srv.store.Session(up.DocID)

	r.stub.plan = planCovering(sess.Doc, "标题")
	r.stub.stages = []Progress{{Name: "connect", Message: "正在连接模型…"}}

	// Run the analysis to completion first.
	r.startPlanAndWait(t, up.DocID)

	// Find the job id via a fresh job record: startPlanAndWait discards it,
	// so run one more analysis and let it finish, then connect late.
	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan", api.PlanRequest{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	var job api.PlanJobResponse
	_ = json.Unmarshal(data, &job)
	rec, _ := r.srv.store.Job(job.JobID)
	rec.Wait(3 * time.Second)

	// Connect after completion: the history must be replayed.
	sresp, err := r.client.Get(r.http.URL + "/api/jobs/" + job.JobID + "/events")
	if err != nil {
		t.Fatalf("SSE: %v", err)
	}
	defer sresp.Body.Close()
	body, _ := io.ReadAll(sresp.Body)
	if !strings.Contains(string(body), "event: done") {
		t.Errorf("迟到的客户端应收到完整历史，实际:\n%s", body)
	}
}

func TestSSEUnknownJob(t *testing.T) {
	r := newRig(t, true)
	resp, _ := r.do(t, http.MethodGet, "/api/jobs/nope/events", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未知任务应返回 404, 实际 %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- plan edit

// TestPutPlanAllowsKeepingInvalidPlan is what makes the 校对 workflow work:
// the user must be able to save an intermediate plan and see what is still
// missing, rather than being forced to fix everything before saving.
func TestPutPlanAllowsKeepingInvalidPlan(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "第一段。\n\n第二段。")

	// A plan covering nothing.
	bad := docmodel.Plan{Sections: []docmodel.Section{}}
	resp, data := r.do(t, http.MethodPut, "/api/documents/"+up.DocID+"/plan", bad)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", resp.StatusCode, data)
	}
	var pr api.PlanResponse
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pr.Report.OK() {
		t.Fatal("不完整的方案应报告校验不通过")
	}
	if pr.Report.CoveredNodes != 0 {
		t.Errorf("覆盖数 = %d, 期望 0", pr.Report.CoveredNodes)
	}

	// And it must be readable back, so the UI can resume editing.
	_, data = r.do(t, http.MethodGet, "/api/documents/"+up.DocID+"/plan", nil)
	if !strings.Contains(string(data), "report") {
		t.Error("保存的方案应可读回")
	}
}

func TestPutPlanAcceptsCorrectedPlan(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "第一段。\n\n第二段。")
	sess, _ := r.srv.store.Session(up.DocID)

	// First save a broken plan, then the corrected one — the exact loop the
	// user performs in the UI.
	if resp, _ := r.do(t, http.MethodPut, "/api/documents/"+up.DocID+"/plan",
		docmodel.Plan{Sections: []docmodel.Section{}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}

	good := planCovering(sess.Doc, "关于测试的通知")
	resp, data := r.do(t, http.MethodPut, "/api/documents/"+up.DocID+"/plan", good)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", resp.StatusCode, data)
	}
	var pr api.PlanResponse
	_ = json.Unmarshal(data, &pr)
	if !pr.Report.OK() {
		t.Fatalf("修正后的方案应通过: %s\n%+v", pr.Report.Summary(), pr.Report.Diagnostics)
	}
}

// ---------------------------------------------------------------- render

func TestRenderProducesDownloadableDocx(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "通知.txt", "第一段正文。\n\n第二段正文。")
	sess, _ := r.srv.store.Session(up.DocID)
	r.stub.plan = planCovering(sess.Doc, "关于开展某项工作的通知")
	r.startPlanAndWait(t, up.DocID)

	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/render",
		api.RenderRequest{Format: "docx"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出失败: %d %s", resp.StatusCode, data)
	}
	var rr api.RenderResponse
	if err := json.Unmarshal(data, &rr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rr.FileID == "" {
		t.Fatal("未返回 file_id")
	}
	if !strings.HasSuffix(rr.Filename, ".docx") {
		t.Errorf("文件名 = %q, 期望 .docx 结尾", rr.Filename)
	}
	if rr.Size == 0 {
		t.Error("导出内容为空")
	}
	if rr.Report == nil || !rr.Report.OK() {
		t.Errorf("导出结果应带通过的校验: %+v", rr.Report)
	}
	// The verbatim check must be reported so the download is auditable.
	if rr.Diff == nil {
		t.Fatal("未返回逐字比对结果")
	}
	if !rr.Diff.Clean() {
		t.Errorf("逐字比对未通过: missing=%v altered=%v", rr.Diff.Missing, rr.Diff.Altered)
	}

	// Download it and confirm it is a real zip (docx).
	dresp, ddata := r.do(t, http.MethodGet, "/api/files/"+rr.FileID, nil)
	if dresp.StatusCode != http.StatusOK {
		t.Fatalf("下载失败: %d", dresp.StatusCode)
	}
	if len(ddata) < 4 || ddata[0] != 'P' || ddata[1] != 'K' {
		t.Errorf("下载内容不是 zip/docx: % x", ddata[:min(4, len(ddata))])
	}
	if ct := dresp.Header.Get("Content-Type"); ct != docxMIME {
		t.Errorf("Content-Type = %q", ct)
	}
	// A Chinese file name must survive the header via RFC 5987.
	cd := dresp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition 缺少 UTF-8 文件名: %q", cd)
	}
}

// TestRenderRefusesLossyPlan is the user's real protection: a plan that drops
// a paragraph must not be exportable, even if the client skips its own check.
func TestRenderRefusesLossyPlan(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "第一段。\n\n第二段会被丢掉。")

	// Save a plan that drops content, then try to render it.
	bad := docmodel.Plan{Sections: []docmodel.Section{
		{ID: "s1", Title: "只有一段", Level: 1, BodyNodeIDs: []string{"n0001"}},
	}}
	if resp, _ := r.do(t, http.MethodPut, "/api/documents/"+up.DocID+"/plan", bad); resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}

	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/render",
		api.RenderRequest{Format: "docx"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("丢失内容的方案不应可导出, 状态码 = %d: %s", resp.StatusCode, data)
	}
	var eb api.ErrorBody
	if err := json.Unmarshal(data, &eb); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if eb.Error.Code != api.CodeInvalidPlan {
		t.Errorf("错误码 = %q, 期望 %q", eb.Error.Code, api.CodeInvalidPlan)
	}
	// The details must name the problem so the UI can show it.
	if len(eb.Error.Details) == 0 {
		t.Error("应返回具体原因")
	}
}

func TestRenderWithoutPlan(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容")
	resp, _ := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/render",
		api.RenderRequest{Format: "docx"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("无方案时导出应返回 400, 实际 %d", resp.StatusCode)
	}
}

func TestRenderRejectsUnsupportedFormat(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容")
	resp, _ := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/render",
		api.RenderRequest{Format: "pdf"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("不支持的格式应返回 400, 实际 %d", resp.StatusCode)
	}
}

// TestRenderAcceptsInlinePlanEdit: the UI sends the edited plan with the
// export request, so a user who fixed the structure does not have to save
// first.
func TestRenderAcceptsInlinePlanEdit(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "正文内容。")
	sess, _ := r.srv.store.Session(up.DocID)
	good := planCovering(sess.Doc, "标题")

	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/render",
		api.RenderRequest{Format: "docx", Plan: good})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("随请求提交方案应可导出: %d %s", resp.StatusCode, data)
	}
}

func TestUnknownFileDownload(t *testing.T) {
	r := newRig(t, true)
	resp, _ := r.do(t, http.MethodGet, "/api/files/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未知文件应返回 404, 实际 %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- preview

func TestPreviewServesSelfContainedHTML(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "预览正文内容。")
	sess, _ := r.srv.store.Session(up.DocID)
	r.stub.plan = planCovering(sess.Doc, "预览标题")
	r.startPlanAndWait(t, up.DocID)

	resp, data := r.do(t, http.MethodGet, "/api/documents/"+up.DocID+"/preview", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("预览失败: %d %s", resp.StatusCode, data)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	html := string(data)
	if !strings.Contains(html, "预览正文内容") {
		t.Error("预览未包含正文")
	}
	// The preview is shown in a sandboxed iframe, so a restricting CSP is the
	// second line of defence against document text reaching the app's origin.
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("预览缺少限制性 CSP: %q", csp)
	}
}

func TestPreviewWithoutPlan(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容")
	resp, _ := r.do(t, http.MethodGet, "/api/documents/"+up.DocID+"/preview", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("无方案时预览应返回 404, 实际 %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- routing

// TestUnknownAPIRouteReturnsJSON: a mistyped endpoint must not fall through to
// the SPA and return HTML, which would make a client's JSON parse fail with a
// confusing error.
func TestUnknownAPIRouteReturnsJSON(t *testing.T) {
	r := newRig(t, true)
	resp, data := r.do(t, http.MethodGet, "/api/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, 期望 JSON", ct)
	}
	var eb api.ErrorBody
	if err := json.Unmarshal(data, &eb); err != nil {
		t.Fatalf("未知接口应返回标准错误结构: %v", err)
	}
}

// TestMethodNotAllowed: the mux distinguishes methods, so a GET on a POST-only
// route must not silently be handled.
func TestMethodNotAllowed(t *testing.T) {
	r := newRig(t, true)
	resp, _ := r.do(t, http.MethodGet, "/api/documents", nil)
	if resp.StatusCode == http.StatusOK {
		t.Error("GET /api/documents 不应成功")
	}
}

// TestUIUnavailableIsExplained: a binary built without its front end must say
// so, rather than serving a blank page.
func TestUIUnavailableIsExplained(t *testing.T) {
	r := newRig(t, true)
	resp, data := r.do(t, http.MethodGet, "/", nil)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("未打包界面时不应返回 200:\n%s", data)
	}
	if !strings.Contains(string(data), "界面") && !strings.Contains(string(data), "构建") {
		t.Errorf("应说明界面缺失及如何构建, 实际: %s", data)
	}
}

// ---------------------------------------------------------------- helpers

func TestOutputName(t *testing.T) {
	plan := func(title string) *docmodel.Plan {
		return &docmodel.Plan{Meta: docmodel.Meta{Title: title}}
	}
	cases := []struct {
		source string
		plan   *docmodel.Plan
		want   string
	}{
		{"报告.docx", plan("关于开展工作的通知"), "关于开展工作的通知.docx"},
		{"报告.docx", plan(""), "报告.docx"},
		{"报告.docx", plan("   "), "报告.docx"},
		{"", plan(""), "公文.docx"},
		{"无扩展名", plan(""), "无扩展名.docx"},
		// Even if a path reaches here, the header value must stay a bare
		// name: it is used in Content-Disposition.
		{`..\..\x\b.docx`, plan(""), "b.docx"},
		{"a/b.docx", plan(""), "b.docx"},
	}
	for _, c := range cases {
		if got := outputName(c.source, c.plan); got != c.want {
			t.Errorf("outputName(%q) = %q, 期望 %q", c.source, got, c.want)
		}
	}
}

// TestOutputNameStripsWindowsForbiddenChars: a 标题 may legitimately contain
// ":" or "?" — a 公文 标题 often contains "关于……的通知（试行）" and sometimes a
// colon. Explorer silently drops a trailing dot, and the forbidden characters
// make the download fail, so they are replaced rather than passed through.
func TestOutputNameStripsWindowsForbiddenChars(t *testing.T) {
	got := outputName("x.txt", &docmodel.Plan{Meta: docmodel.Meta{
		Title: `关于"试点":工作/方案?的报告`,
	}})
	for _, bad := range []rune{'<', '>', ':', '"', '/', '\\', '|', '?', '*'} {
		if strings.ContainsRune(got, bad) {
			t.Errorf("文件名仍含非法字符 %q: %q", bad, got)
		}
	}
	if !strings.HasSuffix(got, ".docx") {
		t.Errorf("文件名应以 .docx 结尾: %q", got)
	}
	if strings.Count(got, ".") != 1 {
		t.Errorf("文件名不应含多余的点: %q", got)
	}
}

// TestOutputNameTrimsTrailingDotOrSpace: Explorer strips these silently, so a
// title ending in a period would produce a file the user did not name.
func TestOutputNameTrimsTrailingDotOrSpace(t *testing.T) {
	for _, title := range []string{"报告.", "报告 ", "报告。 "} {
		got := outputName("x.txt", &docmodel.Plan{Meta: docmodel.Meta{Title: title}})
		if strings.HasSuffix(got, " .docx") || strings.HasSuffix(got, "..docx") {
			t.Errorf("文件名末尾未清理干净: %q（标题 %q）", got, title)
		}
	}
}

// TestOutputNameBoundsLength: Windows caps a path component, so a very long
// 标题 must not produce a file the user cannot save.
func TestOutputNameBoundsLength(t *testing.T) {
	long := strings.Repeat("关于某项工作的通知", 30)
	got := outputName("x.txt", &docmodel.Plan{Meta: docmodel.Meta{Title: long}})
	if n := len([]rune(got)); n > 90 {
		t.Errorf("文件名过长（%d 字符）: %q", n, got)
	}
	if !strings.HasSuffix(got, ".docx") {
		t.Errorf("截断后仍应以 .docx 结尾: %q", got)
	}
}

func TestContentDispositionHandlesChinese(t *testing.T) {
	cd := contentDisposition("关于开展某项工作的通知.docx")
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("缺少 UTF-8 文件名: %q", cd)
	}
	// The plain filename fallback must be pure ASCII, or old clients would
	// see mojibake.
	plain := cd[strings.Index(cd, `filename="`)+len(`filename="`):]
	plain = plain[:strings.Index(plain, `"`)]
	for _, r := range plain {
		if r > 126 {
			t.Errorf("回退文件名含非 ASCII 字符: %q", plain)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"a.docx":            "a.docx",
		`..\..\x\b.docx`:    "b.docx",
		"/etc/passwd":       "passwd",
		"":                  "upload",
		".":                 "upload",
		"  spaced.docx  ":   "spaced.docx",
		`C:\Users\me\a.txt`: "a.txt",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestNewIDIsUniqueAndUnpredictable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := newID()
		if id == "" {
			t.Fatal("生成了空 id")
		}
		if seen[id] {
			t.Fatalf("id 重复: %q", id)
		}
		seen[id] = true
		if len(id) < 16 {
			t.Fatalf("id 过短，不具不可预测性: %q", id)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
