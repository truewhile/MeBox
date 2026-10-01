import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import {
  forgetListPosition,
  normalizeListPosition,
  readListPosition,
  useRememberedListPosition,
  writeListPosition,
} from './useListPositionMemory.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`useListPositionMemory: ${name}`)
}

check('unknown key falls back', readListPosition('unknown-key', 3) === 3)
check('missing value uses default fallback', readListPosition('unknown-key') === 1)

writeListPosition('demo', 4)
check('round trip keeps value', readListPosition('demo') === 4)

writeListPosition('demo', 9)
check('rewrite keeps latest value', readListPosition('demo') === 9)

forgetListPosition('demo')
check('forget resets to fallback', readListPosition('demo', 2) === 2)

check('normalize parses numeric string', normalizeListPosition('7', 1) === 7)
check('normalize floors decimals', normalizeListPosition(3.8, 1) === 3)
check('normalize rejects zero', normalizeListPosition(0, 2) === 2)
check('normalize rejects negatives', normalizeListPosition(-5, 2) === 2)
check('normalize rejects NaN input', normalizeListPosition('abc', 2) === 2)
check('normalize rejects infinity', normalizeListPosition(Number.POSITIVE_INFINITY, 2) === 2)
check('normalize clamps to max', normalizeListPosition(99, 1, 8) === 8)
check('normalize keeps value under max', normalizeListPosition(6, 1, 8) === 6)

// 详情页返回时列表组件会重新挂载：挂载即可读到上次页码，而不是先回第一页。
function PageProbe({ positionKey, max }: { positionKey: string; max?: number }) {
  const [position] = useRememberedListPosition(positionKey, 1, max)
  return createElement('span', null, String(position))
}

writeListPosition('grid:/libraries', 3)
check(
  'remount restores remembered page',
  renderToStaticMarkup(createElement(PageProbe, { positionKey: 'grid:/libraries' })) === '<span>3</span>',
)
check(
  'remount restores independent key',
  renderToStaticMarkup(createElement(PageProbe, { positionKey: 'grid:/' })) === '<span>1</span>',
)
writeListPosition('shelves', 40)
check(
  'remount clamps to restore cap',
  renderToStaticMarkup(createElement(PageProbe, { positionKey: 'shelves', max: 15 })) === '<span>15</span>',
)
check(
  'live state can grow past restore cap',
  readListPosition('shelves') === 40,
)

console.log('useListPositionMemory.test.ts ok')
