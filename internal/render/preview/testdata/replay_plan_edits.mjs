// Self-check for the plan-mutation helpers in web/src/lib/edits.ts — the code
// behind 逐条接受/拒绝.
//
// These functions are TypeScript-only (there is no Go counterpart), so unlike
// the edit replay they cannot be cross-checked against an authority. What they
// can be held to is the contract the server relies on:
//
//   * 拒绝 = remove the entry at that index of plan.edits. Matching by content
//     would be wrong: two identical edits can exist for the same phrase, and
//     only the index says which one the user clicked.
//   * The other fields of the plan must survive a rejection untouched —
//     dropping `elements` or `preamble_node_ids` would turn every bound node
//     into "uncovered content" on the next PUT.
//   * before/after/reason must never be trimmed: `before` is a verbatim anchor.
//
// Invoked by TestPlanEditHelpers in editsync_test.go.
import { pathToFileURL } from 'node:url'
import { resolve } from 'node:path'

const mirrorPath = process.argv[2]
if (!mirrorPath) {
  console.error('usage: node replay_plan_edits.mjs <compiled-edits.js>')
  process.exit(2)
}

const {
  addEdit,
  clearEdits,
  editKey,
  normalizeEdit,
  planEdits,
  removeEditAt,
  replaceEditAt,
  editCount,
  orphanEdits,
  indexedEditsFor,
} = await import(pathToFileURL(resolve(mirrorPath)).href)

const failures = []
let checks = 0

function check(name, got, want) {
  checks++
  const g = JSON.stringify(got)
  const w = JSON.stringify(want)
  if (g !== w) failures.push({ name, got: g, want: w })
}

// A plan carrying every field that must survive an edit operation.
function basePlan() {
  return {
    meta: { title: '关于测试的通知' },
    elements: [{ role: 'title', node_id: 'n0001' }],
    preamble_node_ids: ['n0002'],
    sections: [{ id: 's0001', title: '总体要求', level: 1, body_node_ids: ['n0003'] }],
    notes: ['模型说明'],
    edits: [
      { node_id: 'n0003', kind: 'typo', before: '必需', after: '必须', reason: '错别字' },
      { node_id: 'n0004', kind: 'wording', before: '必须', after: '须', reason: '用词' },
      { node_id: 'n0003', kind: 'punctuation', before: '必需', after: '必须', reason: '同段第二处' },
    ],
  }
}

// --- 拒绝：按下标删除，且只删那一条 -----------------------------------------

check('removeEditAt(0) kinds', removeEditAt(basePlan(), 0).edits.map((e) => e.kind), [
  'wording',
  'punctuation',
])

check('removeEditAt(1) kinds', removeEditAt(basePlan(), 1).edits.map((e) => e.kind), [
  'typo',
  'punctuation',
])

check('removeEditAt(2) kinds', removeEditAt(basePlan(), 2).edits.map((e) => e.kind), [
  'typo',
  'wording',
])

// Two edits share the same node, kind-adjacent text and reason; an index-based
// removal must still delete exactly the clicked one. A content-matching
// implementation would delete both (or the wrong one).
{
  const plan = {
    meta: {},
    sections: [],
    edits: [
      { node_id: 'n1', kind: 'typo', before: '必需', after: '必须', reason: 'r' },
      { node_id: 'n1', kind: 'typo', before: '必需', after: '必须', reason: 'r' },
    ],
  }
  check('removeEditAt first of identical pair', removeEditAt(plan, 0).edits.length, 1)
  check('removeEditAt second of identical pair', removeEditAt(plan, 1).edits.length, 1)
}

// --- 拒绝不得动到方案的其他字段 ---------------------------------------------

{
  const after = removeEditAt(basePlan(), 0)
  check('elements preserved', after.elements, [{ role: 'title', node_id: 'n0001' }])
  check('preamble preserved', after.preamble_node_ids, ['n0002'])
  check('sections preserved', after.sections, [
    { id: 's0001', title: '总体要求', level: 1, body_node_ids: ['n0003'] },
  ])
  check('notes preserved', after.notes, ['模型说明'])
  check('meta preserved', after.meta, { title: '关于测试的通知' })
}

// --- 不改动原方案（纯函数） --------------------------------------------------

{
  const original = basePlan()
  const snapshot = JSON.stringify(original)
  removeEditAt(original, 0)
  addEdit(original, { kind: 'other', before: 'a', after: 'b' })
  clearEdits(original)
  replaceEditAt(original, 0, { kind: 'other', before: 'x', after: 'y' })
  check('input plan is not mutated', JSON.stringify(original), snapshot)
}

// --- 新增：追加在末尾，且不污染其他字段 -------------------------------------

{
  const added = addEdit(basePlan(), {
    node_id: 'n0009',
    kind: 'grammar',
    before: '因为……所以',
    after: '由于……因此',
    reason: '语病',
  })
  check('addEdit appends last', added.edits[added.edits.length - 1].kind, 'grammar')
  check('addEdit keeps count', added.edits.length, 4)
  check('addEdit keeps elements', added.elements.length, 1)
}

// --- 清空：退回「原文逐字不变」，但不丢其他声明 ------------------------------

{
  const cleared = clearEdits(basePlan())
  check('clearEdits empties edits', cleared.edits, [])
  check('clearEdits keeps elements', cleared.elements.length, 1)
  check('clearEdits keeps preamble', cleared.preamble_node_ids, ['n0002'])
  check('clearEdits keeps sections', cleared.sections.length, 1)
}

// --- 替换：只改指定下标 ------------------------------------------------------

{
  const replaced = replaceEditAt(basePlan(), 1, {
    node_id: 'n0004',
    kind: 'punctuation',
    before: '必须',
    after: '须',
    reason: '改为标点问题',
  })
  check('replaceEditAt target kind', replaced.edits[1].kind, 'punctuation')
  check('replaceEditAt target reason', replaced.edits[1].reason, '改为标点问题')
  check('replaceEditAt others untouched', replaced.edits[0].kind, 'typo')
  check('replaceEditAt keeps length', replaced.edits.length, 3)
}

// --- before / after / reason 一律不 trim -------------------------------------

{
  const e = normalizeEdit({
    node_id: 'n1',
    kind: 'punctuation',
    before: '　　为深入  ',
    after: '  为落实\n',
    reason: '  保留空白  ',
  })
  // before must stay byte-identical: trimming it would break the anchor and the
  // edit would silently stop applying.
  check('normalizeEdit keeps before verbatim', e.before, '　　为深入  ')
  check('normalizeEdit keeps after verbatim', e.after, '  为落实\n')
  check('normalizeEdit keeps reason verbatim', e.reason, '  保留空白  ')
}

// occurrence 0 is the default and must be omitted, not serialized as 0.
{
  const e = normalizeEdit({ node_id: 'n1', kind: 'typo', before: 'a', after: 'b', occurrence: 0 })
  check('occurrence 0 omitted', 'occurrence' in e, false)
  const e2 = normalizeEdit({ node_id: 'n1', kind: 'typo', before: 'a', after: 'b', occurrence: 2 })
  check('occurrence 2 kept', e2.occurrence, 2)
}

// --- 遍历与标识 --------------------------------------------------------------

check('editCount', editCount(basePlan()), 3)
check('planEdits null-safe', planEdits(null), [])
check('planEdits undefined-safe', planEdits(undefined), [])

// An insertion edit (no node_id) must be reported separately, not swallowed:
// the UI lists those apart so the user still sees them.
{
  const plan = {
    meta: {},
    sections: [],
    edits: [
      { kind: 'heading', before: '', after: '一、总体要求', reason: '新增标题' },
      { node_id: 'n1', kind: 'typo', before: 'a', after: 'b' },
    ],
  }
  check('orphanEdits finds insertions', orphanEdits(plan).map((x) => x.index), [0])
}

// editKey must distinguish two edits that differ only by occurrence, and must
// not depend on the index (an index-based key would make the "accepted" mark
// jump to a different edit after a rejection).
{
  const a = { node_id: 'n1', kind: 'typo', before: 'x', after: 'y', occurrence: 0 }
  const b = { node_id: 'n1', kind: 'typo', before: 'x', after: 'y', occurrence: 1 }
  check('editKey distinguishes occurrence', editKey(a) !== editKey(b), true)
  check('editKey is stable for equal edits', editKey(a), editKey({ ...a }))
}

// indexedEditsFor must return the plan-wide index, not a per-node ordinal:
// rejection is defined against the plan-wide index.
{
  const plan = {
    meta: {},
    sections: [],
    edits: [
      { node_id: 'n2', kind: 'typo', before: 'a', after: 'b' },
      { node_id: 'n1', kind: 'wording', before: 'c', after: 'd' },
      { node_id: 'n1', kind: 'other', before: 'e', after: 'f' },
    ],
  }
  check(
    'indexedEditsFor indexes into plan.edits',
    indexedEditsFor(plan, 'n1').map((x) => x.index),
    [1, 2],
  )
}

if (failures.length === 0) {
  console.log(`plan-edit helpers OK: ${checks}/${checks} checks pass`)
  process.exit(0)
}

console.error(`plan-edit helpers FAILED: ${failures.length}/${checks} checks failed`)
for (const f of failures) {
  console.error(`- ${f.name}\n    got : ${f.got}\n    want: ${f.want}`)
}
process.exit(1)
