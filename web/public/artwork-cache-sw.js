const ARTWORK_CACHE_PREFIX = 'mebox-artwork-'
const ARTWORK_CACHE = `${ARTWORK_CACHE_PREFIX}v3`
const MIN_CACHEABLE_ARTWORK_BYTES = 128
const STRIP_QUERY_KEYS = ['token', 'profile_id', 'profile_pin_token']

// 浏览器侧作品缓存的容量上限。
//
// 没有上限时 Cache Storage 会一直增长，浏览器在存储压力下会整体清空该
// origin 的缓存，失效时机完全不可控。条数上限每次写入都检查（只是读一遍
// 键，开销小）；字节上限最多每两分钟统计一次，因为需要逐个读取已存响应的
// Content-Length。两者都淘汰到上限的 80%，避免刚清完又立刻触发。
const MAX_CACHE_ENTRIES = 500
const MAX_CACHE_BYTES = 64 * 1024 * 1024
const TRIM_TARGET_RATIO = 0.8
const BYTE_TRIM_INTERVAL_MS = 2 * 60 * 1000

let lastByteTrimAt = 0
let trimInFlight = null

function isArtworkRequest(url) {
  if (url.origin !== self.location.origin) return false
  if (url.pathname === '/api/img') return true
  return url.pathname.startsWith('/api/cloud/play/')
}

function normalizedArtworkRequest(request) {
  const url = new URL(request.url)
  for (const key of STRIP_QUERY_KEYS) {
    url.searchParams.delete(key)
  }
  return new Request(url.toString(), {
    method: 'GET',
    headers: {
      Accept: request.headers.get('Accept') || 'image/avif,image/webp,image/apng,image/*,*/*;q=0.8',
    },
    credentials: 'same-origin',
    mode: 'same-origin',
    redirect: 'follow',
  })
}

self.addEventListener('fetch', (event) => {
  const request = event.request
  if (request.method !== 'GET') return

  const url = new URL(request.url)
  if (!isArtworkRequest(url)) return

  event.respondWith(cacheArtwork(request))
})

self.addEventListener('install', (event) => {
  event.waitUntil(self.skipWaiting())
})

self.addEventListener('activate', (event) => {
  event.waitUntil(deleteOldArtworkCaches().then(() => self.clients.claim()))
})

async function cacheArtwork(request) {
  const cache = await caches.open(ARTWORK_CACHE)
  const cacheKey = normalizedArtworkRequest(request)
  const cached = await cache.match(cacheKey)
  if (cached) return cached

  const response = await fetch(request)
  const cacheResponse = await cloneCacheableArtworkResponse(response)
  if (cacheResponse) {
    await cache.put(cacheKey, cacheResponse)
    await deleteOldArtworkVariants(cache, cacheKey)
    await trimArtworkCache(cache)
  }
  return response
}

// trimArtworkCache 把缓存裁剪回容量上限之内，按写入顺序淘汰（最旧的先删）。
// 同一时刻只允许一次裁剪在跑。
async function trimArtworkCache(cache) {
  if (trimInFlight) return trimInFlight
  trimInFlight = runArtworkTrim(cache).catch(() => undefined)
  try {
    await trimInFlight
  } finally {
    trimInFlight = null
  }
}

async function runArtworkTrim(cache) {
  const keys = await cache.keys()
  if (keys.length > MAX_CACHE_ENTRIES) {
    const keep = Math.floor(MAX_CACHE_ENTRIES * TRIM_TARGET_RATIO)
    await deleteOldestArtwork(cache, keys, keys.length - keep)
    return
  }

  const now = Date.now()
  if (now - lastByteTrimAt < BYTE_TRIM_INTERVAL_MS) return
  lastByteTrimAt = now

  const sizes = []
  let total = 0
  for (const key of keys) {
    const response = await cache.match(key)
    const size = cacheableResponseSize(response)
    sizes.push(size)
    total += size
  }
  if (total <= MAX_CACHE_BYTES) return

  const target = Math.floor(MAX_CACHE_BYTES * TRIM_TARGET_RATIO)
  let remaining = total
  const evicted = []
  for (let i = 0; i < keys.length && remaining > target; i += 1) {
    remaining -= sizes[i]
    evicted.push(keys[i])
  }
  await Promise.all(evicted.map((key) => cache.delete(key)))
}

// cacheableResponseSize 用 Content-Length 估算体积。MeBox 的图片响应由
// ServeContent 生成，始终带该头；缺失时按 0 计（只是少算，不会误删）。
function cacheableResponseSize(response) {
  if (!response) return 0
  const size = Number(response.headers.get('Content-Length') || '0')
  return Number.isFinite(size) && size > 0 ? size : 0
}

// deleteOldestArtwork 按 cache.keys() 的顺序（写入顺序）删除最旧的若干条。
async function deleteOldestArtwork(cache, keys, count) {
  const victims = keys.slice(0, Math.max(0, count))
  await Promise.all(victims.map((key) => cache.delete(key)))
}

async function cloneCacheableArtworkResponse(response) {
  if (!response.ok) return null
  const contentType = response.headers.get('Content-Type') || ''
  if (!contentType.toLowerCase().startsWith('image/')) return null
  const cacheControl = response.headers.get('Cache-Control') || ''
  if (/\bno-store\b/i.test(cacheControl)) return null

  const contentLength = Number(response.headers.get('Content-Length') || '0')
  if (Number.isFinite(contentLength) && contentLength > 0 && contentLength <= MIN_CACHEABLE_ARTWORK_BYTES) {
    return null
  }

  const buffer = await response.clone().arrayBuffer()
  if (buffer.byteLength <= MIN_CACHEABLE_ARTWORK_BYTES) return null
  return new Response(buffer, {
    status: response.status,
    statusText: response.statusText,
    headers: new Headers(response.headers),
  })
}

async function deleteOldArtworkCaches() {
  const names = await caches.keys()
  await Promise.all(names.map((name) => {
    if (!name.startsWith(ARTWORK_CACHE_PREFIX) || name === ARTWORK_CACHE) return undefined
    return caches.delete(name)
  }))
}

async function deleteOldArtworkVariants(cache, currentRequest) {
  const currentURL = new URL(currentRequest.url)
  const currentIdentity = artworkIdentity(currentURL)
  if (!currentIdentity) return

  const keys = await cache.keys()
  await Promise.all(keys.map(async (key) => {
    if (key.url === currentRequest.url) return
    const keyURL = new URL(key.url)
    if (artworkIdentity(keyURL) !== currentIdentity) return
    await cache.delete(key)
  }))
}

function artworkIdentity(url) {
  // 尺寸/质量属于同一源图的不同派生资源，必须参与身份计算；
  // 否则 400px 卡片图会删掉 1600px Hero 图的缓存。
  const variant = ['maxWidth', 'maxHeight', 'quality']
    .map((key) => {
      const value = url.searchParams.get(key)
      return value ? `${key}=${value}` : ''
    })
    .filter(Boolean)
    .join('&')
  const variantSuffix = variant ? `&${variant}` : ''
  if (url.pathname === '/api/img') {
    return `${url.origin}${url.pathname}?url=${url.searchParams.get('url') || ''}${variantSuffix}`
  }
  if (url.pathname.startsWith('/api/cloud/play/')) {
    return `${url.origin}${url.pathname}?ref=${url.searchParams.get('ref') || ''}${variantSuffix}`
  }
  return ''
}
