// 判断一个封面地址能否真正被浏览器渲染成图片。
//
// 书源在「这本书没有封面」时不一定返回空值：聚合类书源（如「光遇聚合」）的
// 搜索请求地址本身是 data:;base64,... 参数信封，规则取值为空时会把它当地址
// 兜底返回，于是 cover_url 变成一串 data: 文本。这类地址 media type 为空，
// 浏览器按 text/plain 解码，必然显示破图。
export function isRenderableCover(url?: string | null): boolean {
  const value = (url ?? '').trim()
  if (!value) return false

  const lower = value.toLowerCase()
  if (lower.startsWith('http://') || lower.startsWith('https://')) return true
  if (lower.startsWith('data:image/')) return true
  // 应用自身生成的相对地址（本地书封面 /api/reader/local/asset?...）以及
  // //host/path 这种协议相对地址。书源给出的相对地址同样落在这里，加载失败
  // 时由 <img> 的 onError 兜底回退成占位图。
  if (value.startsWith('/')) return true

  return false
}
