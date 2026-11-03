package preview

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"govdoc/internal/docmodel"
)

// ---------------------------------------------------------------- edit replay

// editVector pins one case of the on-screen edit replay to the authoritative Go
// result.
type editVector struct {
	Name   string          `json:"name"`
	Note   string          `json:"note"`
	Source string          `json:"source"`
	Edits  []docmodel.Edit `json:"edits"`
	Want   string          `json:"want"`
	Failed []int           `json:"failed"`
}

// editVectors are the cases the TypeScript mirror must reproduce exactly.
//
// The set is chosen so that each entry covers a way the two implementations
// could plausibly disagree, rather than being a random sample: whitespace that
// must not be trimmed, overlapping match candidates, occurrence selection,
// multiple edits on one node, edits that cannot apply, and empty Before/After.
func editVectors() []editVector {
	return []editVector{
		{
			Name:   "typo_middle",
			Note:   "最常见的形态：段中一个错别字",
			Source: "各有关单位必需高度重视。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditTypo, Before: "必需", After: "必须", Reason: "错别字"},
			},
		},
		{
			Name: "full_width_space_in_before",
			Note: "before 含行首全角空格，且 after 与「trim 之后的 before」不同：" +
				"一旦被 trim，替换的位置与结果都会变，因此这个向量必然抓得到",
			Source: "　　为深入贯彻精神。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditWording, Before: "　　为深入", After: "为落实"},
			},
		},
		{
			Name:   "trailing_space_and_newline_in_before",
			Note:   "before 含行尾空格与换行；after 保留换行但去掉空格，trim 会改变输出",
			Source: "第一行留存尾部空格   \n第二行",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditPunctuation, Before: "空格   \n", After: "空格\n"},
			},
		},
		{
			Name:   "overlapping_candidates",
			Note:   "「的的」重叠候选（occurrence=0）：必须命中第一处",
			Source: "目标的的达成",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditTypo, Before: "的的", After: "的"},
			},
		},
		{
			Name: "overlapping_candidates_second_occurrence",
			Note: "重叠候选 + occurrence=1：推进量必须是 1 个字符。若按 len(before) 推进，" +
				"第二个候选会被跳过、改到错误的位置，输出随之不同 —— " +
				"这是唯一能区分两种推进方式的形态（occurrence=0 时两者结果相同）",
			Source: "的的的",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditTypo, Before: "的的", After: "的", Occurrence: 1},
			},
		},
		{
			Name:   "occurrence_second",
			Note:   "occurrence=1 表示替换第二处（0 基）",
			Source: "必须完成，必须上报。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditWording, Before: "必须", After: "应当", Occurrence: 1},
			},
		},
		{
			Name:   "multiple_edits_same_node",
			Note:   "同节点多条：按声明顺序逐条在前一条的结果上匹配",
			Source: "必需及时上报，必需认真整改。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditTypo, Before: "必需", After: "必须"},
				{Kind: docmodel.EditTypo, Before: "必需", After: "必须"},
			},
		},
		{
			Name:   "before_not_found",
			Note:   "before 在原文中找不到：改动不生效，原文原样保留",
			Source: "原文完好无损。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditTypo, Before: "不存在的片段", After: "替换", Reason: "模型记错了字"},
			},
		},
		{
			Name:   "delete_text",
			Note:   "After 为空表示删除该片段",
			Source: "（此处应删除）正文继续。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditOther, Before: "（此处应删除）", After: ""},
			},
		},
		{
			Name:   "insert_with_empty_before",
			Note:   "Before 为空属插入式改动，交由装配器落位，AppliedText 记为无法应用",
			Source: "某段正文。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditHeading, Before: "", After: "一、总体要求"},
			},
		},
		{
			Name:   "sentence_split",
			Note:   "断句：多字符替换",
			Source: "为加强管理，提升效率，现将有关事项通知如下。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditSentenceSplit, Before: "为加强管理，提升效率，", After: "为加强管理，提升效率。"},
			},
		},
		{
			Name:   "one_applied_one_failed",
			Note:   "一成一败：changed 只应收生效的那条，unapplied 只收失败的那条，不得重复计",
			Source: "必需高度重视。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditTypo, Before: "必需", After: "必须", Reason: "正确"},
				{Kind: docmodel.EditTypo, Before: "不存在", After: "X", Reason: "错位"},
			},
		},
		{
			Name:   "no_edits",
			Note:   "无改动：必须逐字返回原文",
			Source: "　　原文逐字不变，含全角空格与标点。",
			Edits:  []docmodel.Edit{},
		},
		{
			Name:   "before_equals_after",
			Note:   "空操作（before==after）：文本不变，也不应被记为「已改正」",
			Source: "无需改动的一段。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditOther, Before: "无需改动", After: "无需改动"},
			},
		},
		{
			Name:   "latin_and_digits",
			Note:   "中英数混排，验证按字节定位不会切坏多字节字符",
			Source: "共28个单位，需2024年5月1日前上报。",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditWording, Before: "需2024年5月1日前", After: "须于2024年5月1日前"},
			},
		},
		{
			Name:   "full_width_quotes",
			Note:   "全角引号与全角句点",
			Source: "要求做到“三个必须”，不得打折扣.",
			Edits: []docmodel.Edit{
				{Kind: docmodel.EditPunctuation, Before: "打折扣.", After: "打折扣。"},
			},
		},
	}
}

// fill computes each vector's expected result with the authoritative Go
// implementation, so the expectations can never drift from the export path.
func fillVectors(vectors []editVector) []editVector {
	out := make([]editVector, len(vectors))
	copy(out, vectors)
	for i := range out {
		v := &out[i]
		got, failed := docmodel.AppliedText(v.Source, v.Edits)
		v.Want = got
		v.Failed = []int{}
		for _, f := range failed {
			for j, e := range v.Edits {
				if e.Before == f.Before && e.After == f.After &&
					e.Kind == f.Kind && e.Occurrence == f.Occurrence {
					v.Failed = append(v.Failed, j)
					break
				}
			}
		}
	}
	return out
}

// frontendTool locates the Node and TypeScript binaries the mirror needs.
//
// Both are pinned to the same bundled runtime the rest of the project uses. A
// missing toolchain is not a failure: this check exists to catch a divergence
// between the two implementations, and it must never be the reason a build
// breaks on a machine that simply has no frontend dependencies installed.
func frontendTool(t *testing.T) (nodeExe, tscExe string, ok bool) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("编辑重演的交叉校验目前只在 Windows 上跑（依赖项目固定的 node/tsc 路径）")
	}
	nodeExe = `C:\Users\louis\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\node\bin\node.exe`
	tscExe = filepath.Join(repoRoot(t), "web", "node_modules", ".bin", "tsc.CMD")
	for _, p := range []string{nodeExe, tscExe} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("缺少前端工具链 %s，跳过交叉校验: %v", p, err)
		}
	}
	return nodeExe, tscExe, true
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("向上找不到 go.mod（从 %s 开始）", dir)
		}
		dir = parent
	}
}

// compileMirror compiles web/src/lib/edits.ts into a temp dir and returns the
// path of the emitted module.
//
// The real source file is compiled — not a copy kept inside the test — so what
// gets verified is the code the application actually imports.
func compileMirror(t *testing.T, tscExe string) (compiled string, work string) {
	t.Helper()
	root := repoRoot(t)
	work = t.TempDir()

	tsc := exec.Command(tscExe,
		"--target", "es2022",
		"--module", "esnext",
		"--moduleResolution", "bundler",
		"--strict",
		"--outDir", filepath.Join(work, "out"),
		filepath.Join(root, "web", "src", "lib", "edits.ts"),
	)
	tsc.Dir = work
	if out, err := tsc.CombinedOutput(); err != nil {
		t.Fatalf("编译 web/src/lib/edits.ts 失败: %v\n%s", err, out)
	}

	// The emitted module is ESM; a package.json in the output root keeps Node's
	// resolution unambiguous regardless of what is above the temp dir.
	if err := os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"type":"module"}`), 0o644); err != nil {
		t.Fatalf("写 package.json: %v", err)
	}

	compiled = filepath.Join(work, "out", "lib", "edits.js")
	if _, err := os.Stat(compiled); err != nil {
		t.Fatalf("tsc 未产出预期的 %s: %v", compiled, err)
	}
	return compiled, work
}

// TestEditReplayMirrorsGoAppliedText is the guard against the two
// implementations of "apply the declared edits" drifting apart.
//
// The UI replays a plan's edits in the browser to show the user what each
// paragraph will become. internal/docmodel.AppliedText is the authority for
// what the export actually contains. If the mirror and the authority disagree,
// the change log would show a document the user is not going to get, which is
// precisely the trust the declared-edit design is meant to earn. So the mirror
// is compiled and run against vectors computed by the Go function itself.
func TestEditReplayMirrorsGoAppliedText(t *testing.T) {
	nodeExe, tscExe, _ := frontendTool(t)
	root := repoRoot(t)
	compiled, work := compileMirror(t, tscExe)

	vectors := fillVectors(editVectors())
	blob, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatalf("marshal vectors: %v", err)
	}
	vectorPath := filepath.Join(work, "vectors.json")
	if err := os.WriteFile(vectorPath, blob, 0o644); err != nil {
		t.Fatalf("写 vectors.json: %v", err)
	}

	harness := filepath.Join(root, "internal", "render", "preview", "testdata", "replay_edits.mjs")
	cmd := exec.Command(nodeExe, harness, vectorPath, compiled)
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编辑重演交叉校验失败: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "cross-check OK") {
		t.Fatalf("交叉校验未报告成功:\n%s", out)
	}
	t.Logf("%s", strings.TrimSpace(string(out)))
}

// TestPlanEditHelpers pins the behaviour of the 逐条接受/拒绝 helpers.
//
// These are TypeScript-only (there is no Go counterpart to compare against), so
// instead they are held to the contract the server depends on: 拒绝 removes the
// entry at that index of plan.edits, every other field of the plan survives, and
// before/after/reason are never trimmed. Matching by content instead of index is
// the specific mistake this guards against — two identical edits for the same
// phrase are possible, and only the index says which one the user clicked.
func TestPlanEditHelpers(t *testing.T) {
	nodeExe, tscExe, _ := frontendTool(t)
	root := repoRoot(t)
	compiled, work := compileMirror(t, tscExe)

	harness := filepath.Join(root, "internal", "render", "preview", "testdata", "replay_plan_edits.mjs")
	cmd := exec.Command(nodeExe, harness, compiled)
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("改动编辑辅助函数的自检失败: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "plan-edit helpers OK") {
		t.Fatalf("自检未报告成功:\n%s", out)
	}
	t.Logf("%s", strings.TrimSpace(string(out)))
}

// TestEditVectorsExerciseTheContract keeps the vector set from being quietly
// hollowed out: each case must be non-trivial in a way the test names, so a
// future edit cannot reduce this to a set that always passes.
func TestEditVectorsExerciseTheContract(t *testing.T) {
	vectors := fillVectors(editVectors())

	want := map[string]bool{
		"typo_middle":                              false,
		"full_width_space_in_before":               false,
		"trailing_space_and_newline_in_before":     false,
		"overlapping_candidates":                   false,
		"overlapping_candidates_second_occurrence": false,
		"occurrence_second":                        false,
		"multiple_edits_same_node":                 false,
		"before_not_found":                         false,
		"delete_text":                              false,
		"insert_with_empty_before":                 false,
		"sentence_split":                           false,
		"one_applied_one_failed":                   false,
		"no_edits":                                 false,
		"before_equals_after":                      false,
		"latin_and_digits":                         false,
		"full_width_quotes":                        false,
	}

	for _, v := range vectors {
		if _, ok := want[v.Name]; !ok {
			t.Errorf("出现了未登记的向量 %q：请在测试里显式登记，避免向量集被悄悄削弱", v.Name)
			continue
		}
		want[v.Name] = true

		if v.Want == "" && v.Source != "" {
			t.Errorf("%s: 期望结果为空但源文本非空，向量可能写错了", v.Name)
		}
		if v.Note == "" {
			t.Errorf("%s: 缺少说明，后读的人无法判断它为何存在", v.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("向量 %q 缺失：它覆盖的情形不再被校验", name)
		}
	}

	// 这个集合必须同时包含「会生效」与「不会生效」两类，否则一次全失败的
	// 交叉校验也会显示成功。
	var applied, unapplied int
	for _, v := range vectors {
		if len(v.Failed) == 0 {
			applied++
		} else {
			unapplied++
		}
	}
	if applied == 0 || unapplied == 0 {
		t.Errorf("向量集必须同时覆盖生效与不生效两种情形，实际 生效=%d 不生效=%d", applied, unapplied)
	}

	// 未生效的条目必须保留原文（那是「改动没做成，原文完好」的含义）。
	for _, v := range vectors {
		if len(v.Failed) > 0 && v.Name == "before_not_found" && v.Want != v.Source {
			t.Errorf("before 找不到时原文被改动了：%q → %q", v.Source, v.Want)
		}
	}
}
