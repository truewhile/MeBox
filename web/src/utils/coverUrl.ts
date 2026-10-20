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

// needsCoverProxy 判断封面是否必须绕道后端图片代理才能显示。
//
// 明文 http 的封面在 https 部署下会被浏览器当作「混合内容」：浏览器先把 http
// 自动升级成 https 再发请求，而不少书源图床（酷我 sycdn.kuwo.cn 等）根本没有
// https，升级失败后图片被直接丢弃，界面上只剩占位图标。同一份书源在本地 http
// 部署（127.0.0.1:8080）却一切正常，差异就出在这里。
//
// 其余地址不需要代理：https 本身是安全协议，data: 图片不经过网络，应用自身的
// 相对地址天然同源。代理它们只会白白多一次服务端抓取。
export function needsCoverProxy(url?: string | null): boolean {
  return (url ?? '').trim().toLowerCase().startsWith('http://')
}
