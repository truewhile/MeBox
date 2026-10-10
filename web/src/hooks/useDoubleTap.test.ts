import { decideDoubleTap } from './useDoubleTap.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`useDoubleTap: ${name}`)
}

// 空闲时的第一下：进入等待窗口，单击不立刻触发（留给定时器）。
const first = decideDoubleTap('none', 'tap')
check('first tap opens a window', first.window === 'tap')
check('first tap does not fire immediately', first.fire === null)

// 窗口里的第二下：判成双击并立刻触发（必须在事件里同步执行，见实现注释）。
const second = decideDoubleTap('tap', 'tap')
check('second tap fires double', second.fire === 'double')
check('second tap closes the window', second.window === 'none')

// 菜单遮罩关菜单：只开窗口，不触发单击，也不影响状态语义。
const armed = decideDoubleTap('none', 'arm')
check('arm opens a window', armed.window === 'armed')
check('arm never fires a single', armed.fire === null)

// 关掉菜单后紧接的第二下要算双击，否则菜单一开一关就永远进不了全屏。
check('tap after arm fires double', decideDoubleTap('armed', 'tap').fire === 'double')

// 已经在双击窗口里再 arm：续期，窗口保持 armed。
check('arm during window stays armed', decideDoubleTap('armed', 'arm').window === 'armed')
check('arm during tap window stays armed', decideDoubleTap('tap', 'arm').window === 'armed')

console.log('useDoubleTap.test.ts ok')
