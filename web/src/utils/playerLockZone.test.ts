import {
  LOCK_ZONE_HEIGHT,
  LOCK_ZONE_WIDTH,
  isPointerInLockZone,
} from './playerLockZone.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`playerLockZone: ${name}`)
}

const H = 800
const MID = H / 2

// 命中：紧贴左边缘、垂直正中。
check('left edge at vertical center hits', isPointerInLockZone(0, MID, H))
check('inside the zone near the left hits', isPointerInLockZone(LOCK_ZONE_WIDTH - 1, MID, H))
check('top of the zone still hits', isPointerInLockZone(10, MID - LOCK_ZONE_HEIGHT / 2, H))
check('bottom of the zone still hits', isPointerInLockZone(10, MID + LOCK_ZONE_HEIGHT / 2, H))

// 未命中：右侧超出宽度、上下超出高度、中心之外。
check('beyond the width misses', !isPointerInLockZone(LOCK_ZONE_WIDTH + 1, MID, H))
check('just past the width edge misses', !isPointerInLockZone(LOCK_ZONE_WIDTH + 0.5, MID, H))
check('above the zone misses', !isPointerInLockZone(10, MID - LOCK_ZONE_HEIGHT / 2 - 1, H))
check('below the zone misses', !isPointerInLockZone(10, MID + LOCK_ZONE_HEIGHT / 2 + 1, H))
check('negative x misses', !isPointerInLockZone(-1, MID, H))
check('top-left corner misses', !isPointerInLockZone(0, 0, H))
check('right edge at center misses', !isPointerInLockZone(1200, MID, H))

// 不同画面高度下感应区跟着中心移动。
check('tracks center on a short stage', isPointerInLockZone(10, 300, 600))
check('short stage: off-center misses', !isPointerInLockZone(10, MID, 600))

// 脏数据一律不命中，避免锁在奇怪的位置冒出来。
check('NaN x misses', !isPointerInLockZone(Number.NaN, MID, H))
check('NaN y misses', !isPointerInLockZone(10, Number.NaN, H))
check('zero stage height misses', !isPointerInLockZone(10, 0, 0))
check('negative stage height misses', !isPointerInLockZone(10, 0, -100))
check('NaN stage height misses', !isPointerInLockZone(10, 0, Number.NaN))

console.log('playerLockZone.test.ts ok')
