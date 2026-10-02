import { comicImageSizing, isSelfSizedComicFit } from './comicImageFit.ts'
import type { ComicImageFit } from '../stores/readerSettings'

// 漫画图片显示尺寸档位的纯逻辑回归测试（对齐 utils/coverUrl.test.ts 的写法）。

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`comicImageFit: ${name}`)
}

// ── 是否由图片自身约束尺寸 ──
const ALL_FITS: ComicImageFit[] = ['default', 'width', 'height', 'long', 'original']
check('default 不是自尺寸档位', !isSelfSizedComicFit('default'))
check('width 不是自尺寸档位', !isSelfSizedComicFit('width'))
check('height 是自尺寸档位', isSelfSizedComicFit('height'))
check('long 是自尺寸档位', isSelfSizedComicFit('long'))
check('original 是自尺寸档位', isSelfSizedComicFit('original'))
check(
  '自尺寸档位与列宽档位互斥且覆盖全部取值',
  ALL_FITS.every((fit) => isSelfSizedComicFit(fit) === (fit === 'height' || fit === 'long' || fit === 'original')),
)

// ── default / width：宽度交给外层列宽，图片只负责铺满 ──
for (const fit of ['default', 'width'] as ComicImageFit[]) {
  const loaded = comicImageSizing(fit, 800, true)
  check(`${fit} 加载完成时不留占位高度`, loaded.style.minHeight === undefined)
  check(`${fit} 铺满外层列宽`, loaded.className === 'block w-full')
  check(`${fit} 不自己定高`, loaded.style.height === undefined && loaded.style.maxHeight === undefined)
  check(`${fit} 不改 maxWidth`, loaded.style.maxWidth === undefined)
  check(`${fit} 加载中给占位高度`, comicImageSizing(fit, 800, false).style.minHeight === '10rem')
}

// ── height：一屏一页 ──
check('height 用滚动容器的实测高度', comicImageSizing('height', 592, true).style.height === '592px')
check('height 宽度按原始比例', comicImageSizing('height', 592, true).style.width === 'auto')
check('height 防超宽页横向溢出', comicImageSizing('height', 592, true).style.maxWidth === '100vw')
check('height 不额外设 maxHeight', comicImageSizing('height', 592, true).style.maxHeight === undefined)
check('height 类名不带 w-full（w-full 会顶掉高度约束）', comicImageSizing('height', 592, true).className === 'block')

// ── long：整页完整可见 ──
check('long 用容器高度封顶', comicImageSizing('long', 592, true).style.maxHeight === '592px')
check('long 不写死高度', comicImageSizing('long', 592, true).style.height === undefined)
check('long 同时约束宽度', comicImageSizing('long', 592, true).style.maxWidth === '100vw')
check('long 加载中给占位高度', comicImageSizing('long', 592, false).style.minHeight === '10rem')

// ── original：原始像素 1:1 ──
check('original 不限制宽度', comicImageSizing('original', 592, true).style.maxWidth === 'none')
check('original 不限制高度', comicImageSizing('original', 592, true).style.maxHeight === undefined)
check('original 按原始像素渲染', comicImageSizing('original', 592, true).style.width === 'auto')

// ── 视口高度还没量出来时退回视口单位，首帧不塌 ──
for (const fit of ['height', 'long'] as ComicImageFit[]) {
  check(`${fit} 未量到高度时退回 100dvh`, comicImageSizing(fit, 0, true).style[fit === 'height' ? 'height' : 'maxHeight'] === '100dvh')
  check(`${fit} 高度为负同样退回 100dvh`, comicImageSizing(fit, -10, true).style[fit === 'height' ? 'height' : 'maxHeight'] === '100dvh')
}
check('default 与视口高度无关', comicImageSizing('default', 0, true).style.height === undefined)
