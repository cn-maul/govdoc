/**
 * 重组「提案 / 接受」状态转换的纯函数测试。
 *
 * 这里锁住两个容易只在界面上暴露的缺陷：
 *   - 模型把主送机关/前言拆成角色分片、正文分片放进章节时，预览骨架不能
 *     丢掉原文（同意前必须先显示原文）；
 *   - 同意后 recompose 与 body_parts 必须原子地一起出现，否则服务端或
 *     逐字校验会拒绝导出。
 */

import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import type { Plan } from '../src/api/types.ts'
import { applyRecompose, splitRecompose } from '../src/lib/recompose.ts'

function plan(): Plan {
  return {
    meta: {},
    elements: [{ role: 'title', node_id: 'n0001' }],
    sections: [
      {
        id: 's1',
        title: '一、提高认识',
        level: 1,
        body_parts: [{ node_id: 'n0002', part: 2 }],
        children: [],
      },
      {
        id: 's2',
        title: '二、压实责任',
        level: 1,
        body_parts: [{ node_id: 'n0002', part: 3 }],
        children: [],
      },
    ],
    recompose: [
      {
        node_id: 'n0002',
        parts: ['各单位：', '为做好工作，现通知如下。', '第一段正文。', '第二段正文。'],
        roles: ['recipient', 'preamble', '', ''],
        reason: '一段到底',
      },
    ],
  }
}

test('splitRecompose 把角色分片的重组也放进待审，并用原文填回骨架', () => {
  const split = splitRecompose(plan())

  assert.equal(split.pending.length, 1)
  assert.deepEqual(split.pending[0].recompose.roles, ['recipient', 'preamble', '', ''])
  assert.deepEqual(split.pending[0].placements, [
    { section_id: 's1', part: 2 },
    { section_id: 's2', part: 3 },
  ])

  assert.equal(split.plan.recompose?.length, 0)
  assert.deepEqual(split.plan.sections[0].body_node_ids, ['n0002'])
  assert.equal(split.plan.sections[1].body_node_ids, undefined)
  assert.equal(split.plan.sections[0].body_parts, undefined)
  assert.equal(split.plan.sections[1].body_parts, undefined)
})

test('applyRecompose 同意后恢复角色分片与各正文落点', () => {
  const split = splitRecompose(plan())
  const accepted = applyRecompose(split.plan, split.pending[0])

  assert.equal(accepted.recompose?.length, 1)
  assert.deepEqual(accepted.recompose?.[0].roles, ['recipient', 'preamble', '', ''])
  assert.deepEqual(accepted.sections[0].body_parts, [{ node_id: 'n0002', part: 2 }])
  assert.deepEqual(accepted.sections[1].body_parts, [{ node_id: 'n0002', part: 3 }])
  assert.equal(accepted.sections[0].body_node_ids, undefined)
})

test('只有角色分片的重组在同意前放进正文前言，不伪造章节归属', () => {
  const p = plan()
  p.sections = [{ id: 's1', title: '一、提高认识', level: 1, body_parts: [], children: [] }]
  p.recompose = [
    {
      node_id: 'n0002',
      parts: ['各单位：', '为做好工作，现通知如下。'],
      roles: ['recipient', 'preamble'],
    },
  ]

  const split = splitRecompose(p)
  assert.deepEqual(split.plan.preamble_node_ids, ['n0002'])
  assert.equal(split.plan.sections[0].body_node_ids, undefined)
  assert.deepEqual(split.pending[0].placements, [])
})
