package fonts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// TestCheckCoversEveryStandardTypeface: the report exists so a user learns
// before printing that a 公文 typeface is missing. If a face the spec uses
// never appears in the report, that warning can never be shown.
func TestCheckCoversEveryStandardTypeface(t *testing.T) {
	spec := gbstandard.Default()
	statuses := Check(spec)

	seen := map[string]Status{}
	for _, st := range statuses {
		seen[st.Name] = st
	}

	// Every typeface the spec assigns to a role, plus the page-number face.
	for _, role := range docmodel.AllRoles() {
		font := spec.Styles[role].Font
		if font == "" {
			continue
		}
		if _, ok := seen[font]; !ok {
			t.Errorf("样式 %s 使用的字体 %q 未出现在检测报告中", role, font)
		}
	}
	if _, ok := seen[spec.PageNumberFont]; !ok {
		t.Errorf("页码字体 %q 未出现在检测报告中", spec.PageNumberFont)
	}

	// The standard's four defining faces must always be reported, even if a
	// future spec stopped using one — otherwise a user could be told nothing
	// is wrong while a required face is absent.
	for _, want := range []string{
		gbstandard.FontFangSong, gbstandard.FontHeiti,
		gbstandard.FontKaiti, gbstandard.FontXiaoBiaoSong,
	} {
		if _, ok := seen[want]; !ok {
			t.Errorf("标准要求的字体 %q 未出现在检测报告中", want)
		}
	}
}

// TestCheckReportsFallbacksAndUsage: a "missing font" warning is only
// actionable if it also says what will be substituted and which elements are
// affected.
func TestCheckReportsFallbacksAndUsage(t *testing.T) {
	statuses := Check(gbstandard.Default())
	for _, st := range statuses {
		if len(st.Fallbacks) == 0 {
			t.Errorf("字体 %s 缺少回退链", st.Name)
		}
		if st.Fallbacks[0] != st.Name {
			t.Errorf("字体 %s 的回退链首项应为自身, 实际 %q", st.Name, st.Fallbacks[0])
		}
	}
	// 仿宋 is the body face, so its usage list must name 正文.
	for _, st := range statuses {
		if st.Name != gbstandard.FontFangSong {
			continue
		}
		var sawBody bool
		for _, u := range st.Used {
			if u == "正文" {
				sawBody = true
			}
		}
		if !sawBody {
			t.Errorf("仿宋 的用途应包含「正文」, 实际 %v", st.Used)
		}
	}
}

// TestRequiredFacesAreMarked: 仿宋 and 小标宋 define a 公文's appearance, so
// losing them matters more than losing a heading face. The distinction drives
// how loudly the UI warns.
func TestRequiredFacesAreMarked(t *testing.T) {
	got := map[string]bool{}
	for _, st := range Check(gbstandard.Default()) {
		got[st.Name] = st.Required
	}
	if !got[gbstandard.FontFangSong] {
		t.Error("仿宋（正文）应标记为必需")
	}
	if !got[gbstandard.FontXiaoBiaoSong] {
		t.Error("小标宋（标题）应标记为必需")
	}
}

// TestCheckIsHonestAboutAvailabilityOnThisMachine: the result depends on the
// host, so the test asserts consistency rather than a fixed answer — a face
// reported available must name the file that satisfied it, and one reported
// missing must not.
func TestCheckIsHonestAboutAvailabilityOnThisMachine(t *testing.T) {
	for _, st := range Check(gbstandard.Default()) {
		if st.Available && st.MatchedFile == "" {
			t.Errorf("字体 %s 报告为可用，但未给出匹配文件", st.Name)
		}
		if !st.Available && st.MatchedFile != "" {
			t.Errorf("字体 %s 报告为缺失，却给出了匹配文件 %q", st.Name, st.MatchedFile)
		}
	}
}

// TestMissingAgreesWithCheck keeps the convenience helper consistent with the
// detailed report; a UI using one and a test using the other must not disagree.
func TestMissingAgreesWithCheck(t *testing.T) {
	spec := gbstandard.Default()
	want := map[string]bool{}
	for _, st := range Check(spec) {
		if !st.Available {
			want[st.Name] = true
		}
	}
	got := Missing(spec)
	if len(got) != len(want) {
		t.Fatalf("Missing 返回 %d 项, Check 报告 %d 项缺失: %v vs %v",
			len(got), len(want), got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("Missing 返回了 Check 认为可用的字体 %q", name)
		}
	}
}

// TestMatchUsesFileStems covers the matching rule directly, including the
// unknown-face fallback that still gives a user-supplied font a sensible
// answer instead of always reporting "missing".
func TestMatchUsesFileStems(t *testing.T) {
	installed := map[string]string{
		"simfang": filepath.Join("C:", "Windows", "Fonts", "simfang.ttf"),
		"simhei":  filepath.Join("C:", "Windows", "Fonts", "simhei.ttf"),
		"myfont":  filepath.Join("C:", "Windows", "Fonts", "myfont.ttf"),
	}

	if _, ok := match(gbstandard.FontFangSong, installed); !ok {
		t.Error("仿宋 应通过 simfang 匹配到")
	}
	if _, ok := match(gbstandard.FontHeiti, installed); !ok {
		t.Error("黑体 应通过 simhei 匹配到")
	}
	if _, ok := match(gbstandard.FontKaiti, installed); ok {
		t.Error("楷体 无对应文件时不应报告可用")
	}
	// An unknown face falls back to an exact stem match.
	if _, ok := match("myfont", installed); !ok {
		t.Error("未知字体名应尝试按文件名精确匹配")
	}
	if _, ok := match("nosuchfont", installed); ok {
		t.Error("不存在的字体不应报告可用")
	}
}

// TestMatchIsCaseInsensitive: font file names appear in several cases across
// Windows versions and Linux packages.
func TestMatchIsCaseInsensitive(t *testing.T) {
	installed := map[string]string{"simfang": "/usr/share/fonts/simfang.ttf"}
	if _, ok := match(gbstandard.FontFangSong, installed); !ok {
		t.Error("匹配应忽略大小写")
	}
}

// TestScanIgnoresNonFontFilesAndMissingDirs: a font directory holds .ttf/.ttc/
// .otf files, and an absent directory is normal rather than an error.
func TestScanIgnoresNonFontFilesAndMissingDirs(t *testing.T) {
	dir := t.TempDir()
	must := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	must("simfang.ttf")
	must("simhei.TTC") // upper-case extension
	must("README.txt") // not a font
	must("notes.md")   // not a font
	if err := os.Mkdir(filepath.Join(dir, "subdir.ttf"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	found := scan([]string{dir, filepath.Join(dir, "does-not-exist")})

	if _, ok := found["simfang"]; !ok {
		t.Error("应找到 simfang.ttf")
	}
	if _, ok := found["simhei"]; !ok {
		t.Error("应找到 simhei.TTC（扩展名大小写不敏感）")
	}
	for _, bad := range []string{"readme", "notes", "subdir"} {
		if _, ok := found[bad]; ok {
			t.Errorf("不应把 %q 当作字体文件", bad)
		}
	}
}

// TestScanPrefersFirstDirectory: the system font directory is scanned before
// the per-user one, matching how the OS resolves a name conflict.
func TestScanPrefersFirstDirectory(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, d := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(d, "simfang.ttf"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	found := scan([]string{first, second})
	if !strings.HasPrefix(found["simfang"], first) {
		t.Errorf("应优先采用先扫描的目录, 实际 %q", found["simfang"])
	}
}

// TestFontDirsAreNonEmpty: a platform whose font directories are unknown would
// make every face look missing, which is worse than no report at all.
func TestFontDirsAreNonEmpty(t *testing.T) {
	if dirs := fontDirs(); len(dirs) == 0 {
		t.Error("当前平台应给出至少一个字体目录")
	}
}

// TestCheckOnEmptySpecStillReportsTheStandard: the UI calls this before any
// configuration exists, so it must not depend on a populated spec.
func TestCheckOnEmptySpecStillReportsTheStandard(t *testing.T) {
	statuses := Check(gbstandard.Spec{})
	if len(statuses) == 0 {
		t.Fatal("空 spec 仍应报告标准要求的字体")
	}
	names := map[string]bool{}
	for _, st := range statuses {
		names[st.Name] = true
	}
	for _, want := range []string{gbstandard.FontFangSong, gbstandard.FontXiaoBiaoSong} {
		if !names[want] {
			t.Errorf("空 spec 时应仍报告 %s", want)
		}
	}
}
