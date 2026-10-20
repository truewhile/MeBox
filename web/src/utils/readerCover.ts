import { imageURL } from '../api/client'
import { needsCoverProxy } from './coverUrl'

// readerCoverSrc 把书源给出的封面地址解析成 <img> 可以直接用的地址。
//
// 书源封面常挂在没有 https 的图床上（酷我 sycdn.kuwo.cn 等）。https 部署下直接
// 引用这类明文 http 地址会被浏览器的混合内容策略拦掉，封面退化成占位图标；改由
// 后端 /api/img 抓取后同源下发就没有这个问题。其余地址原样返回，不给本就能直连
// 的封面增加一次服务端中转。
export function readerCoverSrc(url?: string | null): string {
  const value = (url ?? '').trim()
  if (!value) return ''
  return needsCoverProxy(value) ? imageURL(value) : value
}
