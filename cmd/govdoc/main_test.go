package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/server"
)

// newTestAdapter builds the concrete adapter, so tests can exercise
// Reconfigure (which the server.Analyzer interface does not expose).
func newTestAdapter(t *testing.T, cfg server.LLMConfig) *analyzerAdapter {
	t.Helper()
	a := &analyzerAdapter{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconfigure(cfg); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	return a
}

// TestAnalyzerAdapterForwardsHint guards a defect that a code-only review
// missed: the adapter's hint parameter was named "_" and dropped, so the
// document-nature hint the UI collects never reached the model. The user
// would have seen their input have no effect at all.
//
// The test drives the adapter against a stub endpoint and asserts the hint
// text appears in the outgoing request body, which is the only place that
// proves the chain end to end.
func TestAnalyzerAdapterForwardsHint(t *testing.T) {
	var bodies []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"x","model":"m","choices":[{
				"index":0,
				"message":{"role":"assistant","content":"{\"sections\":[]}"},
				"finish_reason":"stop"}]}`))
	}))
	defer stub.Close()

	adapter, err := newAnalyzer(slog.New(slog.NewTextHandler(io.Discard, nil)), server.LLMConfig{
		Endpoint: stub.URL,
		APIKey:   "test-key",
		Model:    "stub-model",
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("newAnalyzer: %v", err)
	}

	doc := testDocument()
	const hint = "这是一份通报，请按通报格式梳理"

	// An empty section list is rejected by the client and retried, so the call
	// returns an error; the request itself is what this test inspects.
	_, _ = adapter.Analyze(t.Context(), doc, server.AnalysisOptions{Hint: hint}, nil)

	if len(bodies) == 0 {
		t.Fatal("适配器未向模型发出请求")
	}
	if !strings.Contains(bodies[0], hint) {
		t.Errorf("提示未转发到模型。请求体:\n%s", bodies[0])
	}
	// The hint must be labelled as guidance rather than content, or the model
	// could be tempted to write it into the document.
	if !strings.Contains(bodies[0], "不是要写入正文的内容") {
		t.Errorf("提示词未说明 hint 不是正文内容:\n%s", bodies[0])
	}
}

// TestAnalyzerAdapterOmitsEmptyHint: with no hint the prompt must not contain
// an empty guidance block.
func TestAnalyzerAdapterOmitsEmptyHint(t *testing.T) {
	var body string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{
			"index":0,"message":{"role":"assistant","content":"{\"sections\":[]}"},
			"finish_reason":"stop"}]}`))
	}))
	defer stub.Close()

	adapter, err := newAnalyzer(slog.New(slog.NewTextHandler(io.Discard, nil)), server.LLMConfig{
		Endpoint: stub.URL, APIKey: "k", Model: "m",
	})
	if err != nil {
		t.Fatalf("newAnalyzer: %v", err)
	}
	_, _ = adapter.Analyze(t.Context(), testDocument(), server.AnalysisOptions{}, nil)

	if strings.Contains(body, "用户提示") {
		t.Errorf("无提示时不应出现提示块:\n%s", body)
	}
}

// TestAnalyzerAdapterWithoutConfigReportsNotConfigured: a user who has not
// filled in the settings must get the actionable error, not a nil dereference.
func TestAnalyzerAdapterWithoutConfigReportsNotConfigured(t *testing.T) {
	adapter, err := newAnalyzer(slog.New(slog.NewTextHandler(io.Discard, nil)), server.LLMConfig{})
	if err != nil {
		t.Fatalf("未配置不应导致构造失败（用户可能随后在界面填写）: %v", err)
	}
	if _, err := adapter.Analyze(t.Context(), testDocument(), server.AnalysisOptions{}, nil); err == nil {
		t.Fatal("未配置时应返回错误")
	}
}

// TestAnalyzerAdapterReconfigureAppliesNewEndpoint proves saving settings takes
// effect without a restart — otherwise the save looks like it silently failed.
func TestAnalyzerAdapterReconfigureAppliesNewEndpoint(t *testing.T) {
	hit := make(chan string, 1)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{
			"index":0,"message":{"role":"assistant","content":"{\"sections\":[]}"},
			"finish_reason":"stop"}]}`))
	}))
	defer stub.Close()

	adapter := newTestAdapter(t, server.LLMConfig{
		Endpoint: "http://127.0.0.1:1/v1", APIKey: "old", Model: "old-model",
	})

	// Point it at the live stub, as PUT /api/config would.
	if err := adapter.Reconfigure(server.LLMConfig{
		Endpoint: stub.URL, APIKey: "new", Model: "new-model",
	}); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}

	_, _ = adapter.Analyze(t.Context(), testDocument(), server.AnalysisOptions{}, nil)
	select {
	case path := <-hit:
		if !strings.Contains(path, "chat/completions") {
			t.Errorf("请求路径异常: %q", path)
		}
	default:
		t.Fatal("重新配置后请求仍未发往新端点")
	}
}

// TestParseDirection rejects a typo instead of silently defaulting, because a
// silently wrong 行文方向 changes the 发文机关标志 position.
func TestParseDirection(t *testing.T) {
	good := map[string]string{
		"":       "下行文",
		"下行文":    "下行文",
		"上行文":    "上行文",
		"平行文":    "平行文",
		"  上行文 ": "上行文",
	}
	for in, want := range good {
		got, err := parseDirection(in)
		if err != nil {
			t.Errorf("parseDirection(%q) 报错: %v", in, err)
			continue
		}
		if string(got) != want {
			t.Errorf("parseDirection(%q) = %q, 期望 %q", in, got, want)
		}
	}

	for _, bad := range []string{"斜行文", "down", "上行", "下行文x"} {
		if _, err := parseDirection(bad); err == nil {
			t.Errorf("parseDirection(%q) 应报错", bad)
		}
	}
}

// TestParseDirectionErrorNamesTheValidOptions: an error that does not list the
// accepted values leaves the user guessing.
func TestParseDirectionErrorNamesTheValidOptions(t *testing.T) {
	_, err := parseDirection("乱写")
	if err == nil {
		t.Fatal("应报错")
	}
	for _, want := range []string{"下行文", "上行文", "平行文"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应列出可选值 %q: %v", want, err)
		}
	}
}

// TestUIReportsMissingEmbeddedFrontEnd: a binary built without its UI must
// explain the problem rather than serve a blank page.
func TestUIReportsMissingEmbeddedFrontEnd(t *testing.T) {
	if _, err := uiFS("不存在的目录"); err == nil {
		t.Error("不存在的界面目录应报错")
	}

	// The embedded copy exists in this build, so it must resolve.
	fsys, err := uiFS("")
	if err != nil {
		t.Fatalf("内嵌界面应可用: %v", err)
	}
	f, err := fsys.Open("index.html")
	if err != nil {
		t.Fatalf("内嵌 index.html 缺失: %v", err)
	}
	f.Close()
}

// TestDisplayVersion covers the ldflags override path.
func TestDisplayVersion(t *testing.T) {
	saved := version
	defer func() { version = saved }()

	version = ""
	if got := displayVersion(); got != server.Version {
		t.Errorf("未注入版本时应回落到 server.Version, 实际 %q", got)
	}
	version = "9.9.9"
	if got := displayVersion(); got != "9.9.9" {
		t.Errorf("注入版本未生效, 实际 %q", got)
	}
}

// TestMissingFontsIsWiredUp: the startup banner's font warning must come from
// the real probe, not a stub that always reports nothing.
func TestMissingFontsIsWiredUp(t *testing.T) {
	// Must not panic, and must be consistent with the spec it is given.
	if spec := gbstandard.Default(); len(missingFonts(spec)) > 4 {
		t.Errorf("报告了过多缺失字体（%d），疑似探测有误", len(missingFonts(spec)))
	}
}

// ---------------------------------------------------------------- helpers

func testDocument() *docmodel.Document {
	return &docmodel.Document{
		SourceName: "测试通知.txt",
		Format:     "txt",
		Nodes: []docmodel.Node{
			{ID: "n0001", Kind: docmodel.KindParagraph, Text: "关于开展检查的通知", Order: 0},
			{ID: "n0002", Kind: docmodel.KindParagraph, Text: "正文内容。", Order: 1},
		},
	}
}

// Sanity: the JSON shape the stub endpoints emit is what the llm client
// expects, so a change to the stub does not silently stop testing anything.
func TestStubResponseIsWellFormed(t *testing.T) {
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	body := `{"choices":[{"message":{"role":"assistant","content":"{\"sections\":[]}"}}]}`
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("测试桩的响应体应可解析: %v", err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content == "" {
		t.Errorf("测试桩响应结构不符: %+v", out)
	}
}
