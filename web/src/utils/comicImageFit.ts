import type { CSSProperties } from 'react'

import type { ComicImageFit } from '../stores/readerSettings'

// 漫画图片的显示尺寸：把「档位」翻译成 <img> 的 class/style（纯逻辑，便于单测）。
//
// 只有上下滚动模式用得到：翻页模式是整页缩放进视口（object-contain），本来就没有
// 「太大/太小」的问题，一个档位反而会和双页铺开的排版打架。
//
// 两种控制方式：
//   - default / width 由外层列宽决定（列封顶 900px 或铺满窗口），图片本身 w-full；
//   - height / long / original 由图片自身约束，所以外层必须放开列宽上限
//     （见 ReaderViewPage 的 comicFullWidth），否则 900px 的列会把它们再压回去。

/** 图片还没加载出来时的占位高度，避免懒加载期间整屏塌成 0 高。 */
const PLACEHOLDER_MIN_HEIGHT = '10rem'

export interface ComicImageSizing {
  className: string
  style: CSSProperties
}

/** 该档位是否由 <img> 自身约束尺寸（决定外层列宽要不要放开）。 */
export function isSelfSizedComicFit(fit: ComicImageFit): boolean {
  return fit === 'height' || fit === 'long' || fit === 'original'
}

/**
 * 滚动模式下漫画图片的尺寸样式。
 *
 * @param fit 显示尺寸档位。
 * @param viewportHeight 滚动容器的高度（px）。height/long 按它换算；还没量出来
 *   （0）时退回 `100dvh`，首帧不会因为 0 而塌掉。
 * @param loaded 图片是否已加载完成，未完成时给个占位高度。
 */
export function comicImageSizing(
  fit: ComicImageFit,
  viewportHeight: number,
  loaded: boolean,
): ComicImageSizing {
  const vh = viewportHeight > 0 ? `${viewportHeight}px` : '100dvh'
  // height 档位自带高度，占位高度没有意义也不冲突，统一先铺再被档位覆盖。
  const placeholder: CSSProperties = loaded ? {} : { minHeight: PLACEHOLDER_MIN_HEIGHT }

  switch (fit) {
    case 'height':
      // 一屏一页：高度贴合视口，宽度按原始比例（maxWidth 防止超宽页横向溢出）
      return { className: 'block', style: { ...placeholder, height: vh, width: 'auto', maxWidth: '100vw' } }
    case 'long':
      // 整页完整可见：长边贴合视口，宽高都不超出
      return { className: 'block', style: { ...placeholder, maxHeight: vh, maxWidth: '100vw', width: 'auto' } }
    case 'original':
      // 原始像素 1:1：不放大也不缩小；比窗口宽时由滚动容器横向滚动
      return { className: 'block', style: { ...placeholder, width: 'auto', maxWidth: 'none' } }
    default:
      // default / width：宽度交给外层列（900px 封顶或铺满窗口），这里只管铺满
      return { className: 'block w-full', style: placeholder }
  }
}
