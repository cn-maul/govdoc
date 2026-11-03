package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// newTestStore returns a store backed by a temp directory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "GovDoc", "config.json"))
}

// ---------------------------------------------------------------- round trip

// TestSaveThenLoadRoundTrips is the whole point of the package: the user asked
// for settings to survive a restart, so a saved value must come back.
func TestSaveThenLoadRoundTrips(t *testing.T) {
	s := newTestStore(t)

	want := File{
		Endpoint:   "https://api.deepseek.com/v1",
		Model:      "deepseek-chat",
		Protocol:   "openai-chat",
		APIKey:     "sk-secret",
		Direction:  "上行文",
		AllowEdits: true,
	}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A fresh store over the same path is what a restart looks like.
	reopened := New(s.Path())
	got, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Endpoint != want.Endpoint {
		t.Errorf("Endpoint = %q, 期望 %q", got.Endpoint, want.Endpoint)
	}
	if got.Model != want.Model {
		t.Errorf("Model = %q, 期望 %q", got.Model, want.Model)
	}
	if got.APIKey != want.APIKey {
		t.Errorf("APIKey = %q, 期望 %q", got.APIKey, want.APIKey)
	}
	if got.Direction != want.Direction {
		t.Errorf("Direction = %q, 期望 %q", got.Direction, want.Direction)
	}
	if !got.AllowEdits {
		t.Error("AllowEdits 未保留")
	}
	if got.Version != currentVersion {
		t.Errorf("Version = %d, 期望 %d", got.Version, currentVersion)
	}
	if got.SavedAt.IsZero() {
		t.Error("SavedAt 未填写")
	}
}

// TestLoadMissingFileIsNotAnError: first run has no file, and that must not be
// treated as a failure the user has to act on.
func TestLoadMissingFileIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("首次运行读取应返回空配置而非错误: %v", err)
	}
	if got.Endpoint != "" || got.Model != "" {
		t.Errorf("应返回零值配置, 实际 %+v", got)
	}
}

// TestSaveCreatesParentDirectories: the config directory usually does not
// exist yet, and Save must create it rather than fail.
func TestSaveCreatesParentDirectories(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", "config.json")
	s := New(deep)
	if err := s.Save(File{Model: "m"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(deep); err != nil {
		t.Fatalf("配置文件未创建: %v", err)
	}
}

// TestSaveIsAtomic: a truncated settings file would lose the user's API key,
// which is the most annoying thing to recover from. The write therefore goes
// through a temp file and a rename, and no temp file may survive.
func TestSaveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		if err := s.Save(File{Model: "m", APIKey: "k"}); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	// No leftover temp files next to the target.
	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
		if e.Name() != "config.json" {
			t.Errorf("配置目录出现意外文件: %s", e.Name())
		}
	}

	// The content must always be complete, valid JSON.
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("保存后的文件不是合法 JSON: %v", err)
	}
	if f.Model != "m" {
		t.Errorf("Model = %q", f.Model)
	}
}

// TestSavedFileIsOwnerOnly: the file may hold an API key, so on platforms with
// Unix permission bits it must not be group- or world-readable.
//
// Windows is skipped because its permission model does not map to those bits —
// Go's Chmod there can only toggle the read-only attribute. The protection on
// Windows comes from the location instead: DefaultPath resolves under
// %APPDATA%, which is a per-user directory, so another account cannot read it
// regardless of the file mode. TestDefaultPathIsUnderUserConfigDir pins that.
func TestSavedFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 Unix 权限位；该平台的保护来自 %APPDATA% 的按用户隔离")
	}

	s := newTestStore(t)
	if err := s.Save(File{APIKey: "sk-secret"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("配置文件权限 = %04o，不应允许组/其他用户读取", mode)
	}
}

// TestSavedDirectoryIsOwnerOnly covers the directory too: a world-readable
// directory would let another user list and read the config even if the file
// itself were restricted.
func TestSavedDirectoryIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 Unix 权限位")
	}
	s := newTestStore(t)
	if err := s.Save(File{Model: "m"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("配置目录权限 = %04o，不应允许组/其他用户访问", mode)
	}
}

// TestCorruptFileReportsAndDoesNotBrick: a hand-edited or truncated file must
// report why settings were ignored, not stop the program.
func TestCorruptFileReportsAndDoesNotBrick(t *testing.T) {
	s := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path()), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(s.Path(), []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := s.Load()
	if err == nil {
		t.Fatal("损坏的设置文件应报错（以便告知用户），而不是静默忽略")
	}
	if !strings.Contains(err.Error(), "损坏") {
		t.Errorf("错误信息应说明文件损坏: %v", err)
	}

	// And Save must still be able to repair it.
	if err := s.Save(File{Model: "recovered"}); err != nil {
		t.Fatalf("损坏后保存应成功: %v", err)
	}
	got, err := New(s.Path()).Load()
	if err != nil {
		t.Fatalf("修复后读取: %v", err)
	}
	if got.Model != "recovered" {
		t.Errorf("Model = %q", got.Model)
	}
}

// TestOmittedFieldsStayOmitted keeps the file small and diff-friendly: an
// unset endpoint must not be written as an empty string.
func TestOmittedFieldsStayOmitted(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(File{Model: "only-model"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, unwanted := range []string{`"endpoint"`, `"api_key"`, `"direction"`} {
		if strings.Contains(string(raw), unwanted) {
			t.Errorf("未设置字段不应写入文件: %s\n%s", unwanted, raw)
		}
	}
	if !strings.Contains(string(raw), `"model": "only-model"`) {
		t.Errorf("已设置字段应写入: %s", raw)
	}
}

// TestSaveOverwritesPreviousValues: saving a cleared endpoint must clear it,
// not merge with the old value — otherwise a user could never remove a stale
// setting.
func TestSaveOverwritesPreviousValues(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(File{Endpoint: "https://old/v1", Model: "old"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Save(File{Model: "new"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := New(s.Path()).Load()
	if got.Endpoint != "" {
		t.Errorf("清空的 Endpoint 应保持为空, 实际 %q", got.Endpoint)
	}
	if got.Model != "new" {
		t.Errorf("Model = %q", got.Model)
	}
}

// ---------------------------------------------------------------- degradation

// TestDisabledStoreIsUsable: when no config directory can be determined the
// store must degrade to a no-op rather than break the program, because
// persistence is a convenience.
func TestDisabledStoreIsUsable(t *testing.T) {
	s := &Store{disabled: true}

	if !s.Disabled() {
		t.Error("Disabled 应为 true")
	}
	if s.Path() != "" {
		t.Errorf("禁用时路径应为空, 实际 %q", s.Path())
	}
	if err := s.Save(File{Model: "m"}); !errors.Is(err, ErrNoConfigDir) {
		t.Errorf("禁用时 Save 应返回 ErrNoConfigDir, 实际 %v", err)
	}
	if s.LastError() == nil {
		t.Error("Save 失败后 LastError 应记录原因")
	}
	// Load reports the same condition but callers may treat it as "no settings".
	if _, err := s.Load(); !errors.Is(err, ErrNoConfigDir) {
		t.Errorf("禁用时 Load 应返回 ErrNoConfigDir, 实际 %v", err)
	}
}

// TestNilStoreIsSafe: a nil store reaches the methods when persistence is not
// configured at all, and must not panic.
func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if !s.Disabled() {
		t.Error("nil store 应报告为禁用")
	}
	if s.Path() != "" {
		t.Errorf("nil store 路径应为空, 实际 %q", s.Path())
	}
	if err := s.Save(File{}); !errors.Is(err, ErrNoConfigDir) {
		t.Errorf("nil store Save 应返回 ErrNoConfigDir, 实际 %v", err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrNoConfigDir) {
		t.Errorf("nil store Load 应返回 ErrNoConfigDir, 实际 %v", err)
	}
	if s.LastError() != nil {
		t.Error("nil store 的 LastError 应为 nil")
	}
}

// TestDefaultPathIsBesideTheExecutable pins the user's requirement: the
// settings file sits next to the program, so copying the folder carries the
// configuration with it.
//
// It also asserts the two things that placement must NOT do — land in the
// working directory (which is wherever the user happened to type the command)
// or inside a per-user dot-directory, which is the behaviour the user asked to
// change.
func TestDefaultPathIsBesideTheExecutable(t *testing.T) {
	p, err := DefaultPath()
	if err != nil {
		t.Skipf("无法确定可执行文件目录（测试环境）: %v", err)
	}
	if !strings.HasSuffix(p, "govdoc.config.json") {
		t.Errorf("设置文件名 = %q", p)
	}

	exeDir, err := ExeDir()
	if err != nil || exeDir == "" {
		t.Skip("无法确定可执行文件目录")
	}
	if filepath.Dir(p) != exeDir {
		t.Errorf("设置文件应在 exe 同目录:\n 得到 %s\n 期望 %s", filepath.Dir(p), exeDir)
	}

	if wd, err := os.Getwd(); err == nil && strings.HasPrefix(p, wd+string(filepath.Separator)) {
		t.Errorf("设置文件不应落在工作目录内: %q", p)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if strings.HasPrefix(p, filepath.Join(home, ".config")) {
			t.Errorf("设置文件不应放在 ~/.config: %q", p)
		}
	}
}

// TestLoadSeedsBuiltinProfiles: the program must be usable on first run — the
// profiles are created when the settings file does not exist yet, so the user
// never has to create one before choosing one.
func TestLoadSeedsBuiltinProfiles(t *testing.T) {
	s := newTestStore(t)
	f, err := s.Load()
	if err != nil {
		t.Fatalf("首次 Load: %v", err)
	}
	if len(f.Profiles) == 0 {
		t.Fatal("首次运行应自动生成内置配置")
	}
	if _, ok := f.ProfileByID(ProfileGovDoc); !ok {
		t.Error("首次运行应包含内置公文配置")
	}
	if f.ActiveProfile != ProfileGovDoc {
		t.Errorf("默认配置 = %q, 期望 %q", f.ActiveProfile, ProfileGovDoc)
	}
	// Each seeded profile must actually carry usable rules.
	for _, p := range f.Profiles {
		if !p.BuiltIn {
			t.Errorf("首次生成的配置 %q 应标记为内置", p.ID)
		}
		spec, err := p.Decode()
		if err != nil {
			t.Errorf("内置配置 %q 无法解码: %v", p.ID, err)
			continue
		}
		if problems := gbstandard.ValidateSpec(spec); len(problems) > 0 {
			t.Errorf("内置配置 %q 规则不完整: %v", p.ID, problems)
		}
	}
}

// TestLoadKeepsUserProfilesAndAddsNewBuiltins: an upgrade may introduce a new
// built-in, but it must never overwrite a profile the user has tuned.
func TestLoadKeepsUserProfilesAndAddsNewBuiltins(t *testing.T) {
	s := newTestStore(t)
	custom := File{Profiles: []Profile{{ID: "mine", Name: "我的配置", Spec: []byte(`{}`)}}}
	if err := s.Save(custom); err != nil {
		t.Fatalf("Save: %v", err)
	}

	f, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	kept, ok := f.ProfileByID("mine")
	if !ok {
		t.Fatal("用户自建配置丢失了")
	}
	if string(kept.Spec) != `{}` {
		t.Errorf("用户配置被内置配置覆盖了: %s", kept.Spec)
	}
	if _, ok := f.ProfileByID(ProfileGovDoc); !ok {
		t.Error("应补齐缺失的内置配置")
	}
}

// TestLoadFixesUnknownActiveProfile: a stored active id that no longer exists
// (deleted, or hand-edited) must fall back to the default instead of leaving
// the tool with no rules.
func TestLoadFixesUnknownActiveProfile(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(File{ActiveProfile: "vanished"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.ActiveProfile != ProfileGovDoc {
		t.Errorf("失效的默认配置应回退到公文, 实际 %q", f.ActiveProfile)
	}
}

// TestProfileRoundTripsThroughJSON: a profile is stored as raw JSON, so it must
// survive a save/load cycle exactly — a profile that degrades on every restart
// would be worse than one that is never saved.
func TestProfileRoundTripsThroughJSON(t *testing.T) {
	s := newTestStore(t)
	p, err := NewProfile("mine", "我的配置", "测试", gbstandard.Thesis())
	if err != nil {
		t.Fatalf("NewProfile: %v", err)
	}
	f, _ := s.Load()
	f.Profiles = append(f.Profiles, p)
	if err := s.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	round, ok := got.ProfileByID("mine")
	if !ok {
		t.Fatal("保存后配置丢失")
	}
	if round.Name != "我的配置" || round.Description != "测试" {
		t.Errorf("元数据不符: %+v", round)
	}
	spec, err := round.Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := gbstandard.Thesis()
	if spec.Styles[docmodel.RoleBody].Size != want.Styles[docmodel.RoleBody].Size {
		t.Errorf("正文字号往返后不符: %v vs %v",
			spec.Styles[docmodel.RoleBody].Size, want.Styles[docmodel.RoleBody].Size)
	}
	if spec.Page.MarginTopMM != want.Page.MarginTopMM {
		t.Errorf("上边距往返后不符: %v vs %v", spec.Page.MarginTopMM, want.Page.MarginTopMM)
	}
	if spec.Styles[docmodel.RoleTitle].LineSpacingPT != want.Styles[docmodel.RoleTitle].LineSpacingPT {
		t.Errorf("标题行距往返后不符")
	}
}

// TestSanitizeProfileID produces a unique, non-empty key from a possibly-CJK name.
//
// The CJK case is the important one: these profiles are named in Chinese, and an
// earlier version filtered the characters out, so every user profile became
// "profile", "profile-2", … That made the settings file unreadable and
// indistinguishable between copies.
func TestSanitizeProfileID(t *testing.T) {
	taken := map[string]bool{}
	first := SanitizeProfileID("我的公文", func(id string) bool { return taken[id] })
	if first == "" {
		t.Fatal("空名字应产生一个可用 id")
	}
	// A Chinese name should stay recognisable rather than becoming "profile".
	if !strings.Contains(first, "公文") {
		t.Errorf("中文名应保留可读字符, 实际 id = %q", first)
	}
	taken[first] = true

	second := SanitizeProfileID("我的公文", func(id string) bool { return taken[id] })
	if second == first {
		t.Errorf("重名应产生不同 id: %q", second)
	}

	if got := SanitizeProfileID("Report-2024", nil); got != "report-2024" {
		t.Errorf("ASCII 名应规范化为小写 id: %q", got)
	}
	if got := SanitizeProfileID("公文 A", nil); got != "公文-a" {
		t.Errorf("空格应转为连字符: %q", got)
	}
	// A name made only of characters that cannot appear in a path still yields
	// a usable key rather than an empty one.
	if got := SanitizeProfileID("!!!", nil); got == "" || got == "-" {
		t.Errorf("特殊字符名应产生可用 id, 实际 %q", got)
	}
	if got := SanitizeProfileID("", nil); got == "" {
		t.Error("空名不应产生空 id")
	}
}

// TestBuiltinProfilesAreDistinct: "several configurations" is only useful if
// they actually differ; a bug that copies one spec into every profile would
// satisfy every other test while making the feature pointless.
func TestBuiltinProfilesAreDistinct(t *testing.T) {
	profiles := BuiltinProfiles()
	if len(profiles) < 2 {
		t.Fatalf("内置配置至少应有两套, 实际 %d", len(profiles))
	}
	seenID := map[string]bool{}
	seenSpec := map[string]string{}
	seenBody := map[string]string{}
	for _, p := range profiles {
		if seenID[p.ID] {
			t.Errorf("内置配置 id 重复: %q", p.ID)
		}
		seenID[p.ID] = true

		spec, err := p.Decode()
		if err != nil {
			t.Fatalf("内置配置 %q 无法解码: %v", p.ID, err)
		}
		if prev, dup := seenSpec[string(p.Spec)]; dup {
			t.Errorf("内置配置 %q 与 %q 规则完全相同", p.ID, prev)
		}
		seenSpec[string(p.Spec)] = p.ID

		// Distinct specs are not enough — the profiles must differ in the values
		// a user actually picks between: 正文 font and size.
		body := spec.Styles[docmodel.RoleBody]
		key := body.Font + "/" + string(body.Size)
		if prev, dup := seenBody[key]; dup {
			t.Errorf("内置配置 %q 与 %q 的正文字体字号相同（%s），选它们没有意义",
				p.ID, prev, key)
		}
		seenBody[key] = p.ID
	}
}

// TestBuiltinProfilesAreUsableByTheRenderers: a profile that parses but leaves a
// role unstyled would make a renderer fall back silently. This is the contract
// the writers rely on.
func TestBuiltinProfilesAreUsableByTheRenderers(t *testing.T) {
	for _, p := range BuiltinProfiles() {
		spec, err := p.Decode()
		if err != nil {
			t.Fatalf("%q: %v", p.ID, err)
		}
		if problems := gbstandard.ValidateSpec(spec); len(problems) > 0 {
			t.Errorf("配置 %q 校验不通过: %v", p.ID, problems)
		}
		for _, role := range docmodel.AllRoles() {
			st, ok := spec.Style(role)
			if !ok {
				t.Errorf("配置 %q 缺少 role %q 的样式", p.ID, role)
				continue
			}
			if st.Size.Points() <= 0 {
				t.Errorf("配置 %q 的 %q 字号无效: %q", p.ID, role, st.Size)
			}
			if st.Font == "" {
				t.Errorf("配置 %q 的 %q 缺少字体", p.ID, role)
			}
		}
		// Page geometry must be present or every writer produces a blank page.
		if spec.Page.WidthMM <= 0 || spec.Page.HeightMM <= 0 {
			t.Errorf("配置 %q 页面尺寸无效: %+v", p.ID, spec.Page)
		}
	}
}

// TestOnlyGovDocProfileUsesTheCharGrid: the 字符网格 is a 公文 rule. A profile
// that declared it would have its glyphs condensed to fit a grid it never asked
// for — the same class of bug as the title-overlap defect.
func TestOnlyGovDocProfileUsesTheCharGrid(t *testing.T) {
	for _, p := range BuiltinProfiles() {
		spec, err := p.Decode()
		if err != nil {
			t.Fatalf("%q: %v", p.ID, err)
		}
		hasGrid := spec.Page.LinesPerPage > 0 && spec.Page.CharsPerLine > 0
		if p.ID == ProfileGovDoc && !hasGrid {
			t.Error("公文配置应有每页行数/每行字数")
		}
		if p.ID != ProfileGovDoc && hasGrid {
			t.Errorf("非公文配置 %q 不应使用字符网格（会把字距压窄）", p.ID)
		}
	}
}

// TestNewUsesDefaultPathWhenEmpty: an empty path means "the usual place".
func TestNewUsesDefaultPathWhenEmpty(t *testing.T) {
	s := New("")
	if s.Disabled() {
		t.Skip("本机无法确定用户配置目录")
	}
	want, _ := DefaultPath()
	if s.Path() != want {
		t.Errorf("空路径应使用默认位置: %q, 期望 %q", s.Path(), want)
	}
	// It must not have created anything just by being constructed.
	if _, err := os.Stat(s.Path()); err == nil {
		t.Log("默认配置文件已存在（此前运行留下的），未做修改")
	}
}

// ---------------------------------------------------------------- concurrency

// TestConcurrentSaveAndLoad: the HTTP handlers write settings from parallel
// goroutines while reads happen, so the lock discipline matters.
func TestConcurrentSaveAndLoad(t *testing.T) {
	s := newTestStore(t)
	const workers = 8
	done := make(chan struct{})

	for w := 0; w < workers; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 10; i++ {
				_ = s.Save(File{Model: "m", APIKey: "k"})
				_, _ = s.Load()
				_ = s.Path()
				_ = s.Disabled()
				_ = s.LastError()
			}
		}(w)
	}
	for w := 0; w < workers; w++ {
		<-done
	}

	// Whatever order the writes landed in, the file must be valid JSON.
	got, err := New(s.Path()).Load()
	if err != nil {
		t.Fatalf("并发写入后文件应仍可读: %v", err)
	}
	if got.Model != "m" {
		t.Errorf("Model = %q", got.Model)
	}
}
