package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"govdoc/internal/api"
	"govdoc/internal/config"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/store"
)

// newProfileRig builds a server whose settings persist to path, so that
// "does this survive a restart" is testable by constructing a second server over
// the same file.
func newProfileRig(t *testing.T, path string) *rig {
	t.Helper()
	return newPersistentRig(t, path)
}

func mustProfiles(t *testing.T, r *rig) api.ProfilesResponse {
	t.Helper()
	resp, data := r.do(t, http.MethodGet, "/api/profiles", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/profiles = %d: %s", resp.StatusCode, data)
	}
	var got api.ProfilesResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func createProfile(t *testing.T, r *rig, req api.ProfileRequest) (api.Profile, *http.Response, []byte) {
	t.Helper()
	payload, _ := json.Marshal(req)
	resp, data := r.do(t, http.MethodPost, "/api/profiles", json.RawMessage(payload))
	var p api.Profile
	_ = json.Unmarshal(data, &p)
	return p, resp, data
}

// TestProfilesAreSeededOnFirstRun: the settings page must offer something to
// choose from without the user creating anything first.
func TestProfilesAreSeededOnFirstRun(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	got := mustProfiles(t, r)
	if len(got.Profiles) < 2 {
		t.Fatalf("应至少提供两套配置, 实际 %d", len(got.Profiles))
	}
	if got.Active != config.ProfileGovDoc {
		t.Errorf("默认配置 = %q, 期望公文", got.Active)
	}

	// 公文 must be present and must still carry the standard's own grid.
	found := false
	for _, p := range got.Profiles {
		if p.ID != config.ProfileGovDoc {
			continue
		}
		found = true
		if !p.BuiltIn {
			t.Error("公文配置应标记为内置")
		}
		if p.Spec.Page.CharsPerLine != 28 {
			t.Errorf("公文配置丢失了每行字数: %d", p.Spec.Page.CharsPerLine)
		}
	}
	if !found {
		t.Fatal("缺少公文配置")
	}
}

// TestProfilesDifferInOutput is the point of the feature: switching a profile
// has to change the document, otherwise "多套配置" is decoration.
func TestProfilesDifferInOutput(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	govSpec, err := r.srv.ProfileSpec(config.ProfileGovDoc)
	if err != nil {
		t.Fatalf("公文: %v", err)
	}
	genSpec, err := r.srv.ProfileSpec("generic")
	if err != nil {
		t.Fatalf("通用: %v", err)
	}

	govBody := govSpec.Styles[docmodel.RoleBody]
	genBody := genSpec.Styles[docmodel.RoleBody]
	if govBody.Size == genBody.Size && govBody.Font == genBody.Font {
		t.Errorf("公文与通用的正文规则相同（%s/%s），选用不同配置没有意义",
			govBody.Font, govBody.Size)
	}
	if govBody.LineSpacingPT == genBody.LineSpacingPT {
		t.Error("公文与通用的行距相同")
	}
	if govSpec.Page.MarginLeftMM == genSpec.Page.MarginLeftMM {
		t.Error("公文与通用的页边距相同")
	}
}

// TestDocumentRemembersItsProfile is what makes several profiles usable on one
// machine: switching the default must not reformat a document that was already
// processed under a different one.
func TestDocumentRemembersItsProfile(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	resp, data := r.do(t, http.MethodPost, "/api/profiles/activate",
		rawJSONf(`{"id":"generic"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("切换默认配置失败: %d %s", resp.StatusCode, data)
	}

	docID := r.upload(t, "t.txt", "正文。").DocID
	sess, err := r.srv.store.Session(docID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.ProfileID != "generic" {
		t.Fatalf("文档应记住 generic, 实际 %q", sess.ProfileID)
	}

	// The user switches the default back to 公文.
	if resp, data := r.do(t, http.MethodPost, "/api/profiles/activate",
		rawJSONf(`{"id":"govdoc"}`)); resp.StatusCode != http.StatusOK {
		t.Fatalf("切换默认配置失败: %d %s", resp.StatusCode, data)
	}
	if r.srv.ActiveProfile() != config.ProfileGovDoc {
		t.Fatalf("默认配置未切换: %q", r.srv.ActiveProfile())
	}

	// The existing document must still resolve to 通用.
	spec, err := r.srv.specForSession(sess)
	if err != nil {
		t.Fatalf("specForSession: %v", err)
	}
	want, _ := r.srv.ProfileSpec("generic")
	if spec.Styles[docmodel.RoleBody].Size != want.Styles[docmodel.RoleBody].Size {
		t.Errorf("切换默认配置后，文档被改用别的规则排版了（字号 %v，期望 %v）",
			spec.Styles[docmodel.RoleBody].Size, want.Styles[docmodel.RoleBody].Size)
	}

	// A document uploaded now picks up the new default.
	newDoc := r.upload(t, "u.txt", "正文。").DocID
	newSess, err := r.srv.store.Session(newDoc)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if newSess.ProfileID != config.ProfileGovDoc {
		t.Errorf("新文档应使用新的默认配置, 实际 %q", newSess.ProfileID)
	}
}

// TestProfilesSurviveSettingsSave is a regression guard for real data loss.
//
// persistSettings and saveProfiles used to be separate functions that each
// rebuilt the settings file from only the fields they knew about, so saving the
// model connection erased every profile. No single save revealed it — the
// profiles only disappeared on the next restart.
func TestProfilesSurviveSettingsSave(t *testing.T) {
	path := t.TempDir() + "/config.json"
	r := newProfileRig(t, path)

	created, resp, data := createProfile(t, r, api.ProfileRequest{
		Name: "我的公文", CopyFrom: config.ProfileGovDoc,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建配置失败: %d %s", resp.StatusCode, data)
	}

	model := "some-model"
	if resp, data := r.do(t, http.MethodPut, "/api/config",
		api.ConfigRequest{Model: &model}); resp.StatusCode != http.StatusOK {
		t.Fatalf("保存设置失败: %d %s", resp.StatusCode, data)
	}

	f, err := config.New(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := f.ProfileByID(created.ID); !ok {
		t.Fatalf("保存模型设置后，配置 %q 丢失了。磁盘上有：%v", created.ID, f.Profiles)
	}
	if f.Model != model {
		t.Errorf("模型名未保存: %q", f.Model)
	}

	// And it must survive a restart.
	if _, ok := newProfileRig(t, path).srv.profileByID(created.ID); !ok {
		t.Error("重启后配置丢失")
	}
}

// TestSavingProfilesKeepsTheAPIKey is the mirror image of the same bug: the
// profile editor must not erase the credential.
func TestSavingProfilesKeepsTheAPIKey(t *testing.T) {
	path := t.TempDir() + "/config.json"
	r := newProfileRig(t, path)

	key := "sk-should-survive"
	endpoint := "http://example.invalid/v1"
	if resp, _ := r.do(t, http.MethodPut, "/api/config",
		api.ConfigRequest{APIKey: &key, Endpoint: &endpoint}); resp.StatusCode != http.StatusOK {
		t.Fatal("保存 API Key 失败")
	}

	if _, resp, data := createProfile(t, r, api.ProfileRequest{
		Name: "新配置", CopyFrom: "generic",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("创建配置失败: %d %s", resp.StatusCode, data)
	}

	f, _ := config.New(path).Load()
	if f.APIKey != key {
		t.Errorf("编辑配置后 API Key 丢失了（得到 %q）", f.APIKey)
	}
}

// TestBuiltinProfileCannotBeEditedOrDeleted protects the recovery path: if the
// 公文 rules could be destroyed, a user who tweaks them has no way back.
func TestBuiltinProfileCannotBeEditedOrDeleted(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	payload, _ := json.Marshal(api.ProfileRequest{Name: "篡改", Spec: gbstandard.Generic()})
	resp, data := r.do(t, http.MethodPut, "/api/profiles/"+config.ProfileGovDoc,
		rawJSON(payload))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("内置配置应拒绝修改, 实际 %d: %s", resp.StatusCode, data)
	}
	if resp, data = r.do(t, http.MethodDelete,
		"/api/profiles/"+config.ProfileGovDoc, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("内置配置应拒绝删除, 实际 %d: %s", resp.StatusCode, data)
	}
}

// TestCopyingABuiltinProfileIsAllowed is the workflow that replaces editing: the
// user gets an editable copy while the standard stays intact.
func TestCopyingABuiltinProfileIsAllowed(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	created, resp, data := createProfile(t, r, api.ProfileRequest{
		Name: "公文变体", CopyFrom: config.ProfileGovDoc,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("复制配置失败: %d %s", resp.StatusCode, data)
	}
	if created.BuiltIn {
		t.Error("复制出的配置不应标记为内置")
	}
	if created.Spec.Page.CharsPerLine != 28 {
		t.Errorf("复制出的配置未继承原规则（每行字数 %d）", created.Spec.Page.CharsPerLine)
	}

	// It is now editable.
	spec := created.Spec
	body := spec.Styles[docmodel.RoleBody]
	body.Size = gbstandard.SizeXiaoSi
	spec.Styles[docmodel.RoleBody] = body
	payload, _ := json.Marshal(api.ProfileRequest{Name: "公文变体", Spec: spec})
	resp, data = r.do(t, http.MethodPut, "/api/profiles/"+created.ID, rawJSON(payload))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("修改复制配置失败: %d %s", resp.StatusCode, data)
	}
}

// TestDeletingAProfileInUseIsRefused: otherwise an export would silently switch
// to different rules with nothing in the UI saying so.
func TestDeletingAProfileInUseIsRefused(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	if resp, data := r.do(t, http.MethodPost, "/api/profiles/activate",
		rawJSONf(`{"id":"generic"}`)); resp.StatusCode != http.StatusOK {
		t.Fatalf("切换默认失败: %d %s", resp.StatusCode, data)
	}
	docID := r.upload(t, "t.txt", "正文。").DocID

	created, resp, data := createProfile(t, r, api.ProfileRequest{
		Name: "临时", CopyFrom: "generic",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建失败: %d %s", resp.StatusCode, data)
	}

	_, _ = r.srv.store.UpdateSession(docID, func(sess *store.Session) {
		sess.ProfileID = created.ID
	})

	resp, data = r.do(t, http.MethodDelete, "/api/profiles/"+created.ID, nil)
	if resp.StatusCode == http.StatusNoContent {
		t.Fatal("仍在使用的配置不应被删除（导出时会悄悄换规则）")
	}
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("应返回 409, 实际 %d: %s", resp.StatusCode, data)
	}
}

// TestDeletingTheActiveProfileIsRefused stops the tool being left with no rules
// for new uploads.
func TestDeletingTheActiveProfileIsRefused(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	created, resp, data := createProfile(t, r, api.ProfileRequest{
		Name: "将成为默认", CopyFrom: "generic", Activate: true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建失败: %d %s", resp.StatusCode, data)
	}
	if r.srv.ActiveProfile() != created.ID {
		t.Fatalf("创建时未设为默认: %q", r.srv.ActiveProfile())
	}

	resp, data = r.do(t, http.MethodDelete, "/api/profiles/"+created.ID, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("默认配置不应被删除, 实际 %d: %s", resp.StatusCode, data)
	}
}

// TestUnusedProfileCanBeDeleted is the other half: the guard must not make
// profiles undeletable, or the list would grow forever.
func TestUnusedProfileCanBeDeleted(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	created, resp, data := createProfile(t, r, api.ProfileRequest{
		Name: "一次性", CopyFrom: "generic",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建失败: %d %s", resp.StatusCode, data)
	}

	resp, data = r.do(t, http.MethodDelete, "/api/profiles/"+created.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("未被使用的配置应可删除, 实际 %d: %s", resp.StatusCode, data)
	}
	if _, ok := r.srv.profileByID(created.ID); ok {
		t.Error("删除后仍存在")
	}
}

// TestIncompleteProfileIsRefusedWithUsefulMessages: a profile missing a role
// would make a renderer fall back silently, so it must be caught where the user
// can still fix it — and the message has to name what is missing.
func TestIncompleteProfileIsRefusedWithUsefulMessages(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	broken := gbstandard.Generic()
	delete(broken.Styles, docmodel.RoleBody)

	_, resp, data := createProfile(t, r, api.ProfileRequest{Name: "残缺配置", Spec: broken})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("残缺配置应被拒绝, 实际 %d: %s", resp.StatusCode, data)
	}
	var eb api.ErrorBody
	_ = json.Unmarshal(data, &eb)
	if !strings.Contains(eb.Error.Message, "正文") {
		t.Errorf("错误信息应指出缺的是哪个元素: %q", eb.Error.Message)
	}
}

// TestProfileWithUnknownFontSizeIsRefused: an unknown 字号 would render at an
// unpredictable size, which is worse than refusing.
func TestProfileWithUnknownFontSizeIsRefused(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	broken := gbstandard.Generic()
	bodyStyle := broken.Styles[docmodel.RoleBody]
	bodyStyle.Size = gbstandard.FontSize("巨型")
	broken.Styles[docmodel.RoleBody] = bodyStyle

	_, resp, data := createProfile(t, r, api.ProfileRequest{Name: "怪字号", Spec: broken})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未知字号应被拒绝, 实际 %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "字号") {
		t.Errorf("错误信息应提到字号: %s", data)
	}
}

// TestActivateUnknownProfileIsRefused: silently falling back to 公文 would format
// a document under rules the user did not choose, and the mistake would not
// surface until they opened the file.
func TestActivateUnknownProfileIsRefused(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	resp, _ := r.do(t, http.MethodPost, "/api/profiles/activate",
		rawJSONf(`{"id":"nonexistent"}`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("未知配置应被拒绝, 实际 %d", resp.StatusCode)
	}
	if r.srv.ActiveProfile() != config.ProfileGovDoc {
		t.Errorf("拒绝后默认配置不应改变: %q", r.srv.ActiveProfile())
	}
}

// TestUploadWithUnknownProfileFallsBackVisibly: refusing to accept a document
// over a formatting preference would be the wrong trade-off, but the fallback
// must be reported rather than silent.
func TestUploadWithUnknownProfileFallsBackVisibly(t *testing.T) {
	r := newProfileRig(t, t.TempDir()+"/config.json")

	up := r.uploadWithFields(t, "t.txt", "正文。", map[string]string{"profile": "nonexistent"})
	if up.ProfileID != config.ProfileGovDoc {
		t.Errorf("未知配置应回退到公文并在响应中说明, 实际 %q", up.ProfileID)
	}
}

func rawJSON(b []byte) json.RawMessage {
	if len(b) > 0 {
		return json.RawMessage(b)
	}
	return nil
}

func rawJSONf(format string, a ...any) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(format, a...))
}
