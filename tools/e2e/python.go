package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// pythonExe is the interpreter used for the .docx read-back check.
//
// It is resolved at run time rather than hard-coded, because the path is
// machine-specific: the bundled runtime that ships with the development
// harness lives under the user's local app data, while a distribution may
// provide python3 on PATH. Candidates are tried in order and the first that
// can import python-docx wins.
func pythonCandidates() []string {
	return []string{
		// Bundled harness runtime (has python-docx 1.2.0).
		`C:\Users\louis\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\python\python.exe`,
		"python",
		"python3",
		"py",
	}
}

// execPython runs a verification script and returns its stdout.
func execPython(script, arg string) ([]byte, error) {
	var lastErr error
	for _, exe := range pythonCandidates() {
		// Probe with a trivial import first, so an interpreter that exists but
		// lacks python-docx produces a clear message instead of a traceback
		// buried in the verification output.
		probe := exec.Command(exe, "-c", "import docx; print('ok')")
		if out, err := probe.Output(); err != nil || !bytes.Contains(out, []byte("ok")) {
			lastErr = fmt.Errorf("%s 不可用或缺 python-docx: %v", exe, err)
			continue
		}

		cmd := exec.Command(exe, script, arg)
		// The script prints Chinese font names and messages; without this a
		// GBK console would corrupt them into unreadable bytes.
		cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("python-docx 校验脚本失败: %v\nstderr:\n%s",
				err, strings.TrimSpace(stderr.String()))
		}
		return stdout.Bytes(), nil
	}
	return nil, fmt.Errorf("找不到可用的 Python 解释器（需要 python-docx）: %v", lastErr)
}
