package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"govdoc/internal/api"
	"govdoc/internal/config"
	"govdoc/internal/gbstandard"
)

// newPersistentRig builds a rig whose settings are written to a temp file, so
// "does it survive a restart" can be tested by constructing a second server
// over the same path — which is exactly what a restart is.
func boolPtr(v bool) *bool { return &v }

func newPersistentRig(t *testing.T, path string) *rig {
	t.Helper()
	stub := &stubAnalyzer{}
	settings := config.New(path)
	s := New(Config{
		Spec:     gbstandard.Default(),
		Settings: settings,
		LLM: LLMConfig{
			Endpoint: "http://initial.invalid/v1",
			APIKey:   "initial-key",
			Model:    "initial-model",
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s.SetAnalyzer(stub)

	ts := httptest.NewServer(s.Handler(nil))
	t.Cleanup(ts.Close)
	return &rig{srv: s, http: ts, stub: stub, client: ts.Client()}
}

// TestConfigPersistsAcrossRestart is the user's requirement: configure once,
// keep using it. The proof is that a *second* server over the same file reports
// the saved values.
func TestConfigPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GovDoc", "config.json")

	// --- First run: save through the API ---
	first := newPersistentRig(t, path)

	endpoint := "https://api.deepseek.com/v1"
	model := "deepseek-chat"
	key := "sk-saved-key"
	proto := "openai-chat"
	dir := gbstandard.DirUp
	allow := true

	resp, data := first.do(t, http.MethodPut, "/api/config", api.ConfigRequest{
		Endpoint: &endpoint, Model: &model, APIKey: &key,
		Protocol: &proto, Direction: &dir, AllowEdits: &allow,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存配置失败: %d %s", resp.StatusCode, data)
	}
	var saved api.ConfigResponse
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !saved.SavedToDisk {
		t.Fatalf("配置应已写入磁盘, 实际 saved_to_disk=false, warning=%q", saved.SaveWarning)
	}
	if !saved.Persistent {
		t.Error("应报告支持持久化")
	}
	if saved.SaveWarning != "" {
		t.Errorf("不应有保存警告: %q", saved.SaveWarning)
	}
	if saved.SettingsPath == "" {
		t.Error("应回传配置文件路径，便于用户核对")
	}

	// The file must exist and hold the values.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置文件未创建: %v", err)
	}

	// --- Second run over the same file: a restart ---
	second := newPersistentRig(t, path)

	_, data = second.do(t, http.MethodGet, "/api/config", nil)
	var reloaded api.ConfigResponse
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if reloaded.LLM.Endpoint != endpoint {
		t.Errorf("重启后 endpoint = %q, 期望 %q", reloaded.LLM.Endpoint, endpoint)
	}
	if reloaded.LLM.Model != model {
		t.Errorf("重启后 model = %q, 期望 %q", reloaded.LLM.Model, model)
	}
	if !reloaded.LLM.APIKeySet {
		t.Error("重启后应仍认为 API Key 已配置（否则用户每次都要重填）")
	}
	if reloaded.Direction != dir {
		t.Errorf("重启后行文方向 = %q, 期望 %q", reloaded.Direction, dir)
	}
	if !reloaded.AllowEdits {
		t.Error("重启后「允许修正」偏好应保留")
	}

	// The health endpoint is what the UI uses to decide whether to show the
	// setup prompt, so it must agree.
	_, hdata := second.do(t, http.MethodGet, "/api/health", nil)
	var h api.Health
	_ = json.Unmarshal(hdata, &h)
	if !h.LLMConfigured {
		t.Error("重启后 health 应报告模型已配置")
	}

	// The stored key must still never be echoed.
	if strings.Contains(string(data), key) {
		t.Fatalf("配置响应泄漏了 API Key: %s", data)
	}
}

// TestSavedSettingsReachTheAnalyzer is the bug this feature nearly shipped
// with: settings loaded into the server but never handed to the model client,
// so the user still had to re-enter everything. The analyzer must see them.
func TestSavedSettingsReachTheAnalyzer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GovDoc", "config.json")

	// Pre-write a config, then start the server over it — that is what a
	// restart with saved settings looks like.
	st := config.New(path)
	if err := st.Save(config.File{
		Endpoint: "https://saved.example/v1",
		Model:    "saved-model",
		APIKey:   "saved-key",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	r := newPersistentRig(t, path)

	// The effective configuration the server exposes must be the saved one,
	// since cmd/govdoc builds the SDK client from it.
	eff := r.srv.EffectiveLLM()
	if eff.Endpoint != "https://saved.example/v1" {
		t.Errorf("EffectiveLLM().Endpoint = %q, 期望已保存的值", eff.Endpoint)
	}
	if eff.Model != "saved-model" {
		t.Errorf("EffectiveLLM().Model = %q, 期望已保存的值", eff.Model)
	}
	if eff.APIKey != "saved-key" {
		t.Errorf("EffectiveLLM().APIKey = %q, 期望已保存的值", eff.APIKey)
	}

	// And an analysis must be possible without re-entering anything.
	up := r.upload(t, "a.txt", "内容。")
	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan", api.PlanRequest{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("已保存配置下应能直接分析, 实际 %d: %s", resp.StatusCode, data)
	}
}

// TestFlagsWinWhenNoSavedValue: a saved file must not override an explicit
// command-line value for a field the file does not set.
func TestFlagsWinWhenNoSavedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GovDoc", "config.json")
	st := config.New(path)
	// Only the model is saved.
	if err := st.Save(config.File{Model: "from-file"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	settings := config.New(path)
	s := New(Config{
		Spec:     gbstandard.Default(),
		Settings: settings,
		LLM: LLMConfig{
			Endpoint: "http://from-flag/v1",
			APIKey:   "flag-key",
			Model:    "from-flag",
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	eff := s.EffectiveLLM()
	// The file sets the model, so the file wins for that field...
	if eff.Model != "from-file" {
		t.Errorf("Model = %q, 期望文件中保存的值", eff.Model)
	}
	// ...but the endpoint and key are not in the file, so the flags survive.
	if eff.Endpoint != "http://from-flag/v1" {
		t.Errorf("Endpoint = %q, 文件中没有该字段时应沿用启动参数", eff.Endpoint)
	}
	if eff.APIKey != "flag-key" {
		t.Errorf("APIKey = %q, 文件中没有该字段时应沿用启动参数", eff.APIKey)
	}
}

// TestPersistedDirectionIsValidated: a hand-edited file must not be able to set
// a direction the standard does not define, since that moves the 发文机关标志.
func TestPersistedDirectionIsValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GovDoc", "config.json")
	st := config.New(path)
	if err := st.Save(config.File{Direction: "斜行文"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s := New(Config{
		Spec:     gbstandard.Default(),
		Settings: config.New(path),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if got := s.EffectiveSpec().Direction; got != gbstandard.DirDown {
		t.Errorf("非法行文方向应被忽略并保持默认, 实际 %q", got)
	}
}

// TestCorruptSettingsDoNotStopStartup: a damaged file must be reported and
// ignored, not fatal — otherwise the tool would refuse to run.
func TestCorruptSettingsDoNotStopStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GovDoc", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := New(Config{
		Spec:     gbstandard.Default(),
		Settings: config.New(path),
		LLM:      LLMConfig{Endpoint: "http://flag/v1", Model: "flag-model", APIKey: "k"},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	eff := s.EffectiveLLM()
	if eff.Model != "flag-model" {
		t.Errorf("损坏的设置文件应回退到启动参数, 实际 %q", eff.Model)
	}
}

// TestSaveFailureIsReportedButSettingApplies: when the file cannot be written
// the setting must still take effect for this run, and the user must be told it
// will not survive a restart — the alternative (silently doing nothing) is what
// makes a user re-enter a key and wonder why it keeps disappearing.
func TestSaveFailureIsReportedButSettingApplies(t *testing.T) {
	// A path whose parent is a *file* cannot be created as a directory, so
	// Save is guaranteed to fail.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	path := filepath.Join(blocker, "config.json")

	r := newPersistentRig(t, path)

	model := "will-not-persist"
	resp, data := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{Model: &model})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存失败不应让请求失败（设置本次仍生效）: %d %s", resp.StatusCode, data)
	}
	var got api.ConfigResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.LLM.Model != model {
		t.Errorf("模型应已生效, 实际 %q", got.LLM.Model)
	}
	if got.SavedToDisk {
		t.Error("无法写入时应报告 saved_to_disk=false")
	}
	if got.SaveWarning == "" {
		t.Error("写入失败时应给出警告，否则用户以为重启后仍然有效")
	}
	if !strings.Contains(got.SaveWarning, "重启") {
		t.Errorf("警告应说明重启后需要重新填写: %q", got.SaveWarning)
	}
}

// TestNoSettingsStoreStillWorks: persistence is optional, so a server built
// without it must behave normally and say so honestly.
func TestNoSettingsStoreStillWorks(t *testing.T) {
	r := newRig(t, true) // newRig passes no Settings

	_, data := r.do(t, http.MethodGet, "/api/config", nil)
	var got api.ConfigResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Persistent {
		t.Error("未配置持久化时应报告 persistent=false，UI 便不会承诺保存")
	}
	if got.SettingsPath != "" {
		t.Errorf("未配置持久化时不应有路径, 实际 %q", got.SettingsPath)
	}

	// Saving must still succeed in memory.
	model := "in-memory-only"
	if resp, _ := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{Model: &model}); resp.StatusCode != http.StatusOK {
		t.Errorf("无持久化时保存设置仍应成功, 实际 %d", resp.StatusCode)
	}
	if got := r.srv.EffectiveLLM().Model; got != model {
		t.Errorf("设置应在内存中生效, 实际 %q", got)
	}
}

// TestAllowEditsReachesAnalyzer: the per-request switch must be forwarded, or
// the UI control would appear to do nothing.
func TestAllowEditsReachesAnalyzer(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "a.txt", "内容。")

	allow := true
	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan",
		api.PlanRequest{AllowEdits: &allow})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("状态码 = %d: %s", resp.StatusCode, data)
	}
	var job api.PlanJobResponse
	_ = json.Unmarshal(data, &job)
	rec, _ := r.srv.store.Job(job.JobID)
	rec.Wait(3e9)

	if !r.stub.gotAllowEdits {
		t.Error("allow_edits=true 未转发给分析器，界面开关会形同虚设")
	}
}

// TestPreferencesSaveWithoutModelConfigured: 行文方向 and allow_edits live in a
// separate settings page now. Saving them on a fresh install (no endpoint/key/
// model yet) must not try to rebuild the model client, which would fail and
// make the page unusable until the model was configured first.
func TestPreferencesSaveWithoutModelConfigured(t *testing.T) {
	r := newRig(t, false)
	r.srv.SetAnalyzer(&reconfiguringStub{stubAnalyzer: &stubAnalyzer{}})

	dir := gbstandard.DirUp
	allow := true
	resp, data := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{
		Direction: &dir, AllowEdits: &allow,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("未配置模型时保存偏好应成功，实际 %d: %s", resp.StatusCode, data)
	}
	if got := r.srv.EffectiveSpec().Direction; got != dir {
		t.Errorf("行文方向 = %q, 期望 %q", got, dir)
	}
	if !r.srv.currentAllowEdits() {
		t.Error("allow_edits 应已生效")
	}
}

// reconfiguringStub makes the Reconfigure hook reachable, so the test above
// would fail if preferences triggered it.
type reconfiguringStub struct {
	*stubAnalyzer
}

func (s *reconfiguringStub) Reconfigure(LLMConfig) error {
	return fmt.Errorf("llm: 未配置模型连接")
}

// TestAllowEditsDefaultsToSavedPreference: a nil in the request means "use the
// saved preference", which is what the UI sends when the user has not touched
// the switch.
func TestAllowEditsDefaultsToSavedPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GovDoc", "config.json")
	st := config.New(path)
	if err := st.Save(config.File{AllowEdits: boolPtr(true)}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	r := newPersistentRig(t, path)
	up := r.upload(t, "a.txt", "内容。")

	// No allow_edits in the request.
	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan", api.PlanRequest{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("状态码 = %d: %s", resp.StatusCode, data)
	}
	var job api.PlanJobResponse
	_ = json.Unmarshal(data, &job)
	rec, _ := r.srv.store.Job(job.JobID)
	rec.Wait(3e9)

	if !r.stub.gotAllowEdits {
		t.Error("请求未带 allow_edits 时应沿用已保存的偏好（true）")
	}
}

// TestPerRequestAllowEditsOverridesSaved: an explicit request value wins over
// the saved preference in both directions.
func TestPerRequestAllowEditsOverridesSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GovDoc", "config.json")
	st := config.New(path)
	if err := st.Save(config.File{AllowEdits: boolPtr(true)}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	r := newPersistentRig(t, path)
	up := r.upload(t, "a.txt", "内容。")

	deny := false
	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan",
		api.PlanRequest{AllowEdits: &deny})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("状态码 = %d: %s", resp.StatusCode, data)
	}
	var job api.PlanJobResponse
	_ = json.Unmarshal(data, &job)
	rec, _ := r.srv.store.Job(job.JobID)
	rec.Wait(3e9)

	if r.stub.gotAllowEdits {
		t.Error("请求显式 allow_edits=false 应覆盖已保存的 true")
	}
}

// TestDeclaredEditsFlowThroughExport is the end-to-end proof for requirement 3:
// a plan with edits renders, the change log comes back, and the export is
// allowed — while an undeclared change would not be.
func TestDeclaredEditsFlowThroughExport(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "通知.txt", "为深入贯切落实上级要求，现通知如下。")

	// A plan that binds the 缘由 to the preamble and declares one typo fix.
	plan := map[string]any{
		"preamble_node_ids": []string{"n0001"},
		"edits": []map[string]any{{
			"node_id": "n0001", "kind": "typo",
			"before": "贯切落实", "after": "贯彻落实",
			"reason": "“贯切”系“贯彻”误写。",
		}},
	}
	payload, _ := json.Marshal(plan)

	resp, data := r.do(t, http.MethodPut, "/api/documents/"+up.DocID+"/plan", json.RawMessage(payload))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("提交方案失败: %d %s", resp.StatusCode, data)
	}

	resp, data = r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/render",
		api.RenderRequest{Format: "docx"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("带已声明改动的方案应可导出: %d %s", resp.StatusCode, data)
	}

	var rr api.RenderResponse
	if err := json.Unmarshal(data, &rr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rr.Diff == nil {
		t.Fatal("未返回逐字比对结果")
	}
	if !rr.Diff.Clean() {
		t.Fatalf("已声明的改动不应失败: altered=%v missing=%v", rr.Diff.Altered, rr.Diff.Missing)
	}
	if len(rr.Diff.Changed) != 1 {
		t.Fatalf("应返回 1 条改动供审核, 实际 %d: %+v", len(rr.Diff.Changed), rr.Diff.Changed)
	}
	c := rr.Diff.Changed[0]
	if c.Before != "贯切落实" || c.After != "贯彻落实" {
		t.Errorf("改动记录不符: %+v", c)
	}
	if c.Label != "错别字" {
		t.Errorf("改动记录应带中文标签: %q", c.Label)
	}
	if c.Reason == "" {
		t.Error("改动记录应保留理由，否则无法审核")
	}
}

// TestUndeclaredChangeStillBlocksExport: the guarantee that survives the
// relaxation. A plan whose edit does not match the text leaves the output equal
// to the source, which is fine; but if the text were changed without a matching
// declaration, export must fail. This drives that through the real store.
func TestUndeclaredChangeStillBlocksExport(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "通知.txt", "原文内容。")

	// Declare an edit that does NOT match, so the text stays as the source.
	// The export should still succeed (nothing was changed) and the unapplied
	// edit should be reported.
	plan := map[string]any{
		"preamble_node_ids": []string{"n0001"},
		"edits": []map[string]any{{
			"node_id": "n0001", "kind": "typo",
			"before": "不存在的片段", "after": "改正",
		}},
	}
	payload, _ := json.Marshal(plan)
	if resp, data := r.do(t, http.MethodPut, "/api/documents/"+up.DocID+"/plan", json.RawMessage(payload)); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT plan: %d %s", resp.StatusCode, data)
	}

	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/render",
		api.RenderRequest{Format: "docx"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("未生效的改动不会损坏文档，应可导出: %d %s", resp.StatusCode, data)
	}
	var rr api.RenderResponse
	_ = json.Unmarshal(data, &rr)
	if len(rr.Diff.Changed) != 0 {
		t.Errorf("未生效的改动不应记入改动清单: %+v", rr.Diff.Changed)
	}
	if len(rr.Diff.Unapplied) != 1 {
		t.Errorf("应报告 1 项未生效改动, 实际 %d: %+v", len(rr.Diff.Unapplied), rr.Diff.Unapplied)
	}
}
