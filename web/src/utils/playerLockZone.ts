// 播放器「锁」按钮的几何判定。
//
// 锁按钮默认隐身：桌面端靠鼠标移到画面左侧中间把它唤出来，移动端靠点击画面。
// 这里用坐标判断鼠标是否落在感应区里，而不是在画面上摆一个透明的热区元素——
// 隐形热区会吃掉全景拖动的手势，也会挡住画面本身的单击。

/** 感应区宽度（像素），自画面左边缘向右展开。 */
export const LOCK_ZONE_WIDTH = 72
/** 感应区高度（像素），以画面垂直中心为基准上下各展开一半。 */
export const LOCK_ZONE_HEIGHT = 88

/**
 * 判断鼠标位置（相对画面左上角）是否落在锁按钮的感应区内。
 *
 * 参数异常（NaN、画面高度为 0 等）一律返回 false：宁可锁不出现，也不要因为
 * 拿到脏数据让它在莫名其妙的位置冒出来。
 */
export function isPointerInLockZone(
  offsetX: number,
  offsetY: number,
  stageHeight: number,
): boolean {
  if (!Number.isFinite(offsetX) || !Number.isFinite(offsetY)) return false
  if (!Number.isFinite(stageHeight) || stageHeight <= 0) return false
  if (offsetX < 0 || offsetX > LOCK_ZONE_WIDTH) return false
  return Math.abs(offsetY - stageHeight / 2) <= LOCK_ZONE_HEIGHT / 2
}
