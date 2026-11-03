// Replay harness for the cross-check test in preview: it feeds vectors produced
// by the authoritative Go implementation of docmodel.AppliedText through the
// TypeScript mirror in web/src/lib/edits.ts.
//
// Why this file exists: the browser replays a plan's edits locally to show the
// user "this paragraph will become X" before anything is exported. That replay
// is a second implementation of the one function in the program that is allowed
// to change source text. If the two drift apart, the UI would show a paragraph
// that is not what the file contains — the exact opposite of what a change log
// is for. This harness is what turns "they look the same" into a checked fact.
//
// It is invoked by preview_test.go, not by any build. It expects:
//   - argv[2]: path to a JSON array of vectors written by the test
//   - argv[3]: path to the compiled mirror (the out/lib/edits.js tsc emitted)
import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { resolve } from 'node:path'

const vectorsPath = process.argv[2]
const mirrorPath = process.argv[3]
if (!vectorsPath || !mirrorPath) {
  console.error('usage: node replay_edits.mjs <vectors.json> <compiled-edits.js>')
  process.exit(2)
}

// Imported by absolute file URL: this script lives in the repository while the
// mirror is compiled into a temp directory, so a relative specifier would
// resolve against the wrong base.
const { applyEdits } = await import(pathToFileURL(resolve(mirrorPath)).href)

const vectors = JSON.parse(readFileSync(vectorsPath, 'utf8'))
if (!Array.isArray(vectors) || vectors.length === 0) {
  console.error('vectors file is empty or not an array')
  process.exit(2)
}

const failures = []
let pass = 0

for (const v of vectors) {
  const { text, failed } = applyEdits(v.source, v.edits)

  // Map the failed edits back to their indices in the input list, the same way
  // the Go side reports them, so the two can be compared directly.
  const failedIdx = failed
    .map((f) =>
      v.edits.findIndex(
        (e) =>
          e.before === f.before &&
          e.after === f.after &&
          e.kind === f.kind &&
          (e.occurrence ?? 0) === (f.occurrence ?? 0),
      ),
    )
    .filter((i) => i >= 0)
    .sort((a, b) => a - b)

  const wantFailed = [...v.failed].sort((a, b) => a - b)
  const textOK = text === v.want
  const failedOK = JSON.stringify(failedIdx) === JSON.stringify(wantFailed)

  if (textOK && failedOK) {
    pass++
    continue
  }
  failures.push({ v, text, failedIdx, wantFailed, textOK, failedOK })
}

if (failures.length === 0) {
  console.log(`cross-check OK: ${pass}/${vectors.length} vectors match Go's docmodel.AppliedText`)
  process.exit(0)
}

console.error(
  `cross-check FAILED: ${failures.length}/${vectors.length} vectors diverge from Go's docmodel.AppliedText`,
)
for (const f of failures) {
  console.error(`\n- ${f.v.name} — ${f.v.note}`)
  console.error(`  source: ${JSON.stringify(f.v.source)}`)
  if (!f.textOK) {
    console.error(`  TS text : ${JSON.stringify(f.text)}`)
    console.error(`  Go text : ${JSON.stringify(f.v.want)}`)
  }
  if (!f.failedOK) {
    console.error(`  TS failed: ${JSON.stringify(f.failedIdx)}`)
    console.error(`  Go failed: ${JSON.stringify(f.wantFailed)}`)
  }
}
process.exit(1)
