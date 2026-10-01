import { useCallback, useEffect, useRef, useState } from 'react'
import { Check, ExternalLink, Loader2, RefreshCw, X } from 'lucide-react'

import { readerAPI, type ReaderBrowserPage } from '../../api/reader'

// 书源页面的承载面板（对应 legado 的 BottomWebViewDialog）。
//
// 书源通过 java.startBrowser / java.startBrowserAwait 把页面交给宿主：
//   - startBrowser / showBrowser：只展示，用户看完关掉即可；
//   - startBrowserAwait：服务端会一直阻塞，直到这里的「√」把用户操作后的
//     DOM 回传过去，书源再从 DOM 里解析出结果（光遇聚合的「切换线路」
//     就是从回传页面里抓 #serverValue 写进源变量）。
//
// iframe 刻意不带 allow-same-origin：页面是第三方 HTML，给它同源权限就能读写
// 本应用的 localStorage（JWT）。因此改由服务端注入的脚本用 postMessage 回传
// DOM，父窗口只负责发一个「请把当前 DOM 给我」的消息。

interface Props {
  page: ReaderBrowserPage
  onClose: () => void
}

/** 请求 iframe 回传 DOM 的消息标识（与 browser_panel.go 注入的脚本一致）。 */
const DOM_REQUEST = '__mebox_dom__'

/** iframe 内页面的 fetch/XHR 代理消息（同浏览器 bridge 脚本）。 */
interface ProxyRequest {
  __mebox_proxy__?: boolean
  reqId?: string
  url?: string
  method?: string
  headers?: Record<string, string>
  body?: string
}

export default function BrowserPanel({ page, onClose }: Props) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const awaitingRef = useRef(false)
  const timerRef = useRef<number | undefined>(undefined)
  const [busy, setBusy] = useState(false)
  const [awaiting, setAwaiting] = useState(false)
  const [error, setError] = useState('')
  const [loaded, setLoaded] = useState(false)

  // 提交回传结果：body 为用户操作后的页面 DOM（服务端作为 StrResponse.body()）
  const submit = useCallback(
    async (html: string | null) => {
      setBusy(true)
      setError('')
      try {
        await readerAPI.submitBrowserResult({
          id: page.id,
          body: html ?? '',
          url: page.target_url ?? '',
          cancelled: html === null,
        })
        onClose()
      } catch (e) {
        setError((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '提交失败，请重试')
      } finally {
        setBusy(false)
      }
    },
    [page.id, page.target_url, onClose],
  )

  // 接收 iframe 的消息：① 回传 DOM；② 代发页面自己的接口请求
  useEffect(() => {
    const onMessage = (ev: MessageEvent) => {
      const data = ev.data as ProxyRequest & { __mebox_dom__?: boolean; html?: string } | null
      if (!data) return

      if (data.__mebox_dom__ === true) {
        if (!awaitingRef.current) return
        awaitingRef.current = false
        setAwaiting(false)
        if (timerRef.current) window.clearTimeout(timerRef.current)
        void submit(data.html ?? '')
        return
      }

      if (data.__mebox_proxy__ === true && data.reqId) {
        const win = iframeRef.current?.contentWindow
        const reply = (payload: Record<string, unknown>) => {
          win?.postMessage({ __mebox_proxy_res__: true, reqId: data.reqId, ...payload }, '*')
        }
        void readerAPI
          .browserXHR({
            id: page.id,
            url: data.url ?? '',
            method: data.method ?? 'GET',
            headers: data.headers ?? {},
            body: data.body ?? '',
          })
          .then((res) => reply({ ...res }))
          .catch((e) => {
            // 让页面自己走失败分支，而不是一直挂着
            reply({
              status: 502,
              contentType: 'text/plain',
              body: String((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '代理请求失败'),
              base64: false,
            })
          })
      }
    }
    window.addEventListener('message', onMessage)
    return () => {
      window.removeEventListener('message', onMessage)
      if (timerRef.current) window.clearTimeout(timerRef.current)
    }
  }, [submit, page.id])

  const confirm = () => {
    const win = iframeRef.current?.contentWindow
    if (!win) {
      setError('页面尚未加载完成')
      return
    }
    setError('')
    awaitingRef.current = true
    setAwaiting(true)
    try {
      win.postMessage(DOM_REQUEST, '*')
    } catch {
      awaitingRef.current = false
      setAwaiting(false)
      setError('无法读取页面内容')
      return
    }
    // 兜底：3 秒没回传就提示用户页面可能还没准备好
    timerRef.current = window.setTimeout(() => {
      if (!awaitingRef.current) return
      awaitingRef.current = false
      setAwaiting(false)
      setError('读取页面内容超时，请确认页面已加载完成后重试')
    }, 3000)
  }

  const externalURL = page.target_url && !page.target_url.startsWith('data:') ? page.target_url : ''
  const needConfirm = page.mode === 'wait'

  return (
    <div
      className="fixed inset-0 z-[60] flex flex-col bg-black/60 p-0 sm:p-6"
      // 面板叠在登录对话框之上，而登录对话框的遮罩是「点一下关闭」。
      // 不拦住冒泡的话，点「完成」会把整个登录对话框一起关掉。
      onClick={(e) => e.stopPropagation()}
    >
      <div className="mx-auto flex h-full w-full max-w-4xl flex-col overflow-hidden rounded-none border border-[var(--app-border)] bg-[var(--app-panel)] sm:rounded-2xl">
        {/* 顶栏：标题 + 外部打开 + 关闭 */}
        <div className="flex items-center gap-3 border-b border-[var(--app-border)] px-4 py-3">
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-bold text-[var(--app-text)]">
              {page.title || '书源页面'}
            </p>
            <p className="truncate text-2xs text-[var(--app-muted)]">
              {page.mode === 'wait' ? '操作完成后点右下角「完成」把结果回传给书源' : '确认完毕后关闭即可'}
              {page.target_url ? ` · ${page.target_url}` : ''}
            </p>
          </div>
          {!loaded && <Loader2 size={15} className="animate-spin text-[var(--app-muted)]" />}
          <button
            type="button"
            title="重新加载"
            onClick={() => {
              setLoaded(false)
              if (iframeRef.current) iframeRef.current.src = page.page_url
            }}
            className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)]"
          >
            <RefreshCw size={15} />
          </button>
          {externalURL && (
            <button
              type="button"
              title="在新标签打开原始地址（浏览器里没有书源登录态）"
              onClick={() => window.open(externalURL, '_blank', 'noopener,noreferrer')}
              className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)]"
            >
              <ExternalLink size={15} />
            </button>
          )}
        </div>

        {error && (
          <div className="border-b border-red-500/30 bg-red-500/10 px-4 py-2 text-xs text-red-500">{error}</div>
        )}

        {/* 页面本体：sandbox 不含 allow-same-origin，避免第三方页面拿到本应用权限 */}
        <div className="min-h-0 flex-1 bg-white">
          <iframe
            ref={iframeRef}
            title={page.title || '书源页面'}
            src={page.page_url}
            onLoad={() => setLoaded(true)}
            sandbox="allow-scripts allow-forms allow-popups allow-modals"
            className="h-full w-full border-0"
          />
        </div>

        {/* 底部动作 */}
        <div className="flex items-center justify-end gap-2 border-t border-[var(--app-border)] px-4 py-3">
          <button
            type="button"
            disabled={busy}
            onClick={() => void submit(null)}
            className="btn-outline text-xs text-red-500 disabled:opacity-50"
          >
            <X size={13} className="mr-1 inline" /> 取消
          </button>
          {needConfirm && (
            <button
              type="button"
              disabled={busy || awaiting}
              onClick={confirm}
              className="btn-primary text-xs disabled:opacity-50"
            >
              {awaiting ? (
                <Loader2 size={13} className="mr-1 inline animate-spin" />
              ) : (
                <Check size={13} className="mr-1 inline" />
              )}
              完成（回传页面）
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
