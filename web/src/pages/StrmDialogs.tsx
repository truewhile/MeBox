import { FormEvent, useEffect, useRef, useState } from 'react'
import toast from 'react-hot-toast'
import {
  ArrowDown,
  ArrowUp,
  ChevronRight,
  Cloud,
  FolderPlus,
  HardDrive,
  Loader2,
  Plus,
  QrCode,
  Trash2,
  Tv,
  X,
} from 'lucide-react'

import { strmAPI, type StrmRemoteEntry } from '../api/strm'
import type { SettingDef } from './SettingsRow'
import { SettingRow } from './SettingsRow'
import type { StrmAccount, StrmSyncPath, StrmSyncPathInput, StrmProvider } from '../types/strm'
import { STRM_PROVIDER_LABELS } from '../types/strm'
import { apiErrorMessage } from './StrmManagePage'
import { Strm115AuthPanel } from './strm-dialogs/Strm115AuthPanel'
import { LocalDirBrowserDialog } from '../components/LocalDirBrowserDialog'
import {
  defaultEmbyRemoteLines,
  encodeEmbyRemoteConfigLines,
  normalizeEmbyRemoteLines,
  type EmbyRemoteLine,
} from '../utils/embyRemoteLines'
import { lastPathSegment, remoteTailNameOf, syncLocalPathWithRemote } from '../utils/strmPaths'

// ─── 弹框外壳 ────────────────────────────────────────────────────────────────

function DialogShell({
  title,
  subtitle,
  children,
  onClose,
  wide,
}: {
  title: string
  subtitle?: string
  children: React.ReactNode
  onClose: () => void
  wide?: boolean
}) {
  return (
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center bg-black/35 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className={
          'flex max-h-[calc(100dvh-2rem)] w-full flex-col overflow-hidden rounded-3xl border border-white/70 bg-white shadow-2xl ' +
          (wide ? 'max-w-3xl' : 'max-w-lg')
        }
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
          <div>
            <h3 className="font-display text-lg font-bold text-ink-600">{title}</h3>
            {subtitle && <p className="text-xs text-sand-500">{subtitle}</p>}
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl p-1.5 text-ink-50 transition hover:bg-gray-100 hover:text-ink-600"
            title="关闭"
          >
            <X size={20} />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">{children}</div>
      </div>
    </div>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-ink-100">{label}</span>
      {children}
      {hint && <span className="block text-xs text-sand-500">{hint}</span>}
    </label>
  )
}

const inputCls = 'input-base w-full'

const SECRET_PLACEHOLDER = '已配置，留空则不修改'

function accountDialogDefaults(existing: StrmAccount | null) {
  const preview = existing?.config_preview
  return {
    provider: (existing?.provider ?? 'cloud115') as StrmProvider,
    name: existing?.name ?? '',
    enabled: existing?.enabled ?? true,
    url: preview?.url ?? '',
    server: preview?.server ?? '',
    username: preview?.username ?? '',
    password: '',
    token: '',
    proxyPlay: existing?.proxy_play ?? false,
    embyLines: defaultEmbyRemoteLines(existing?.provider === 'emby_remote' ? existing?.emby_lines : undefined),
    hasPassword: Boolean(preview?.has_password),
    hasToken: Boolean(preview?.has_token || preview?.has_api_key),
  }
}

// ─── 添加/编辑网盘账号 ────────────────────────────────────────────────────────

const PROVIDER_OPTIONS: { provider: StrmProvider; label: string; desc: string }[] = [
  { provider: 'cloud115', label: '115 网盘', desc: '开放平台 OAuth 授权（扫码/网页）' },
  { provider: 'clouddrive2', label: 'CloudDrive2', desc: 'WebDAV 桥接多种网盘' },
  { provider: 'openlist', label: 'OpenList', desc: 'OpenList / AList 兼容服务' },
]

export function StrmAccountDialog({
  existing,
  onClose,
  onSaved,
}: {
  existing: StrmAccount | null
  onClose: () => void
  onSaved: () => void
}) {
  const defaults = accountDialogDefaults(existing)
  const [provider, setProvider] = useState<StrmProvider>(defaults.provider)
  const [name, setName] = useState(defaults.name)
  const [enabled, setEnabled] = useState(defaults.enabled)
  const [url, setUrl] = useState(defaults.url)
  const [embyLines, setEmbyLines] = useState<EmbyRemoteLine[]>(defaults.embyLines)
  const [server, setServer] = useState(defaults.server)
  const [username, setUsername] = useState(defaults.username)
  const [password, setPassword] = useState(defaults.password)
  const [token, setToken] = useState(defaults.token)
  const [proxyPlay, setProxyPlay] = useState(defaults.proxyPlay)
  const [hasPassword, setHasPassword] = useState(defaults.hasPassword)
  const [hasToken, setHasToken] = useState(defaults.hasToken)
  const [saving, setSaving] = useState(false)

  const buildConfig = (): Record<string, string> => {
    switch (provider) {
      case 'clouddrive2':
        return {
          ...(url ? { url } : {}),
          ...(username ? { username } : {}),
          ...(password ? { password } : {}),
        }
      case 'openlist':
        return {
          ...(server ? { server } : {}),
          ...(username ? { username } : {}),
          ...(password ? { password } : {}),
          ...(token ? { token } : {}),
        }
      case 'emby_remote': {
        const lineConfig = encodeEmbyRemoteConfigLines(embyLines)
        return {
          ...lineConfig,
          ...(username ? { username } : {}),
          ...(password ? { password } : {}),
          ...(token ? { token } : {}),
          proxy_play: proxyPlay ? 'true' : 'false',
        }
      }
      default:
        return {}
    }
  }

  const canSave = () => {
    switch (provider) {
      case 'clouddrive2':
        return url !== ''
      case 'openlist':
        return server !== '' && (token !== '' || password !== '')
      case 'emby_remote':
        return normalizeEmbyRemoteLines(embyLines).length > 0 && (token !== '' || password !== '')
      default:
        return false
    }
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    try {
      if (existing) {
        await strmAPI.updateAccount(existing.id, {
          name,
          provider: existing.provider,
          enabled,
          config: buildConfig(),
        })
        toast.success('网盘账号已更新')
      } else {
        if (!canSave()) {
          toast.error('请先完成凭据配置')
          return
        }
        await strmAPI.createAccount({ name, provider, config: buildConfig() })
        toast.success('网盘账号已添加')
      }
      onSaved()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <DialogShell
      title={existing ? '编辑网盘账号' : '添加网盘账号'}
      subtitle="115 开放平台授权（内置应用目录 / 中继 / 第三方服务）；CloudDrive2 / OpenList 填写服务地址与凭据"
      onClose={onClose}
      wide
    >
      <form onSubmit={submit} className="space-y-5">
        {/* 提供方选择 */}
        {!existing && (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            {PROVIDER_OPTIONS.map((option) => {
              const Icon =
                option.provider === 'cloud115'
                  ? QrCode
                  : option.provider === 'clouddrive2'
                    ? HardDrive
                    : option.provider === 'emby_remote'
                      ? Tv
                      : FolderPlus
              const active = provider === option.provider
              return (
                <button
                  key={option.provider}
                  type="button"
                  onClick={() => setProvider(option.provider)}
                  className={
                    'rounded-2xl border-2 p-3 text-left transition ' +
                    (active ? 'border-brand-400 bg-brand-50' : 'border-gray-100 bg-white hover:border-gray-200')
                  }
                >
                  <Icon size={20} className={active ? 'text-brand-500' : 'text-sand-500'} />
                  <p className="mt-1.5 text-sm font-bold text-ink-600">{option.label}</p>
                  <p className="text-[11px] text-sand-500">{option.desc}</p>
                </button>
              )
            })}
          </div>
        )}

        <Field label="账号名称">
          <input
            className={inputCls}
            value={name}
            placeholder={STRM_PROVIDER_LABELS[provider]}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>

        {provider === 'cloud115' && (
          <Strm115AuthPanel
            existing={existing}
            accountName={name}
            onAuthed={(account) => {
              toast.success(`「${account.name}」授权成功`)
              onSaved()
            }}
          />
        )}

        {provider === 'clouddrive2' && (
          <Field label="WebDAV 地址" hint="例如 http://192.168.1.10:19798/dav">
            <input className={inputCls} value={url} placeholder="http://host:19798/dav" onChange={(e) => setUrl(e.target.value)} />
          </Field>
        )}

        {provider === 'openlist' && (
          <Field label="服务器地址" hint="OpenList / AList 服务地址，如 http://192.168.1.10:5244">
            <input className={inputCls} value={server} placeholder="http://host:5244" onChange={(e) => setServer(e.target.value)} />
          </Field>
        )}

        {provider === 'emby_remote' && (
          <div className="space-y-3">
            <div className="flex items-start justify-between gap-2">
              <div>
                <p className="text-sm font-medium text-ink-100">接入线路</p>
                <p className="text-xs text-sand-500">可配置内网、外网等多条线路，连接失败时按顺序自动切换</p>
              </div>
              <button
                type="button"
                onClick={() => setEmbyLines((prev) => [...prev, { name: `线路 ${prev.length + 1}`, url: '' }])}
                className="inline-flex shrink-0 items-center gap-1 rounded-lg border border-gray-200 px-2.5 py-1 text-xs font-semibold text-ink-100 hover:bg-gray-50"
              >
                <Plus size={12} />
                添加线路
              </button>
            </div>
            {embyLines.map((line, index) => (
              <div key={`emby-line-${index}`} className="space-y-2 rounded-2xl border border-sand-200 bg-gray-50/70 p-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-bold uppercase tracking-wide text-sand-500">
                    线路 {index + 1}
                    {existing?.emby_active_line === index ? ' · 当前优先' : index === 0 ? ' · 默认优先' : ''}
                  </span>
                  <div className="flex items-center gap-1">
                    <button
                      type="button"
                      onClick={() => {
                        setEmbyLines((prev) => {
                          const target = index - 1
                          if (target < 0) return prev
                          const next = [...prev]
                          const [moved] = next.splice(index, 1)
                          next.splice(target, 0, moved)
                          return next
                        })
                      }}
                      disabled={index === 0}
                      className="rounded-lg border border-gray-200 p-1 text-ink-100 hover:bg-white disabled:opacity-30"
                    >
                      <ArrowUp size={12} />
                    </button>
                    <button
                      type="button"
                      onClick={() => {
                        setEmbyLines((prev) => {
                          const target = index + 1
                          if (target >= prev.length) return prev
                          const next = [...prev]
                          const [moved] = next.splice(index, 1)
                          next.splice(target, 0, moved)
                          return next
                        })
                      }}
                      disabled={index === embyLines.length - 1}
                      className="rounded-lg border border-gray-200 p-1 text-ink-100 hover:bg-white disabled:opacity-30"
                    >
                      <ArrowDown size={12} />
                    </button>
                    <button
                      type="button"
                      onClick={() => setEmbyLines((prev) => (prev.length <= 1 ? prev : prev.filter((_, i) => i !== index)))}
                      disabled={embyLines.length <= 1}
                      className="rounded-lg border border-gray-200 p-1 text-red-500 hover:bg-red-50 disabled:opacity-30"
                    >
                      <Trash2 size={12} />
                    </button>
                  </div>
                </div>
                <input
                  className={inputCls}
                  value={line.name}
                  placeholder="线路名称，如：内网 / 外网 / IPv6"
                  onChange={(e) =>
                    setEmbyLines((prev) => prev.map((item, i) => (i === index ? { ...item, name: e.target.value } : item)))
                  }
                />
                <input
                  className={inputCls}
                  value={line.url}
                  placeholder="http://192.168.1.10:8096 或 https://emby.example.com"
                  onChange={(e) =>
                    setEmbyLines((prev) => prev.map((item, i) => (i === index ? { ...item, url: e.target.value } : item)))
                  }
                />
              </div>
            ))}
          </div>
        )}

        {(provider === 'clouddrive2' || provider === 'emby_remote') && (
          <div className="grid grid-cols-2 gap-3">
            <Field label="用户名">
              <input className={inputCls} value={username} onChange={(e) => setUsername(e.target.value)} />
            </Field>
            <Field label="密码">
              <input
                className={inputCls}
                type="password"
                value={password}
                placeholder={existing && hasPassword ? SECRET_PLACEHOLDER : undefined}
                onChange={(e) => {
                  setPassword(e.target.value)
                  if (e.target.value) setHasPassword(false)
                }}
              />
            </Field>
          </div>
        )}

        {provider === 'openlist' && (
          <div className="grid grid-cols-2 gap-3">
            <Field label="用户名">
              <input className={inputCls} value={username} onChange={(e) => setUsername(e.target.value)} />
            </Field>
            <Field label="密码">
              <input
                className={inputCls}
                type="password"
                value={password}
                placeholder={existing && hasPassword ? SECRET_PLACEHOLDER : undefined}
                onChange={(e) => {
                  setPassword(e.target.value)
                  if (e.target.value) setHasPassword(false)
                }}
              />
            </Field>
          </div>
        )}

        {provider === 'emby_remote' && (
          <Field label="API Key（可选）" hint="留空则用下方用户名/密码自动认证获取">
            <input
              className={inputCls}
              value={token}
              placeholder={existing && hasToken ? SECRET_PLACEHOLDER : '留空自动认证'}
              onChange={(e) => {
                setToken(e.target.value)
                if (e.target.value) setHasToken(false)
              }}
            />
          </Field>
        )}

        {provider === 'openlist' && (
          <Field label="Token（可选，优先于密码）">
            <input
              className={inputCls}
              value={token}
              placeholder={existing && hasToken ? SECRET_PLACEHOLDER : undefined}
              onChange={(e) => {
                setToken(e.target.value)
                if (e.target.value) setHasToken(false)
              }}
            />
          </Field>
        )}

        {provider === 'emby_remote' && (
          <label className="flex cursor-pointer items-start gap-2 text-sm text-ink-100">
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 accent-primary-400"
              checked={proxyPlay}
              onChange={(e) => setProxyPlay(e.target.checked)}
            />
            <span>
              <span className="font-semibold text-ink-600">播放流量经过本服务器代理</span>
              <span className="block text-xs text-sand-500">
                关闭（推荐）：播放时客户端直连远程 Emby，本服务器不参与流量中转
              </span>
            </span>
          </label>
        )}

        {existing && provider !== 'cloud115' && (
          <label className="flex cursor-pointer items-center gap-2 text-sm text-ink-100">
            <input
              type="checkbox"
              className="h-4 w-4 accent-primary-400"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            启用该账号
          </label>
        )}

        {provider !== 'cloud115' && (
          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={onClose} className="rounded-xl border border-gray-200 px-4 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50">
              取消
            </button>
            <button type="submit" disabled={saving} className="neon-button disabled:opacity-50">
              {saving ? <Loader2 size={16} className="animate-spin" /> : <Cloud size={16} />}
              保存
            </button>
          </div>
        )}
      </form>
    </DialogShell>
  )
}

// ─── 115 登录二维码（canvas 渲染；115 返回的是扫码页面链接而非图片） ───────────

// QR 尺寸：物理像素 = CSS 像素（避免 canvas 被 CSS 缩放导致裁切/模糊）
const SETTING_DEFS: SettingDef[] = [
  { key: 'strm.base_url', label: 'STRM 链接基础地址', type: 'text', hint: '生成的 strm 文件指向的播放地址；留空依次自动使用 app.server_url，最后回退到本机地址（http://127.0.0.1:端口）。Emby 在其他设备时请配置局域网/公网地址' },
  { key: 'strm.video_ext', label: '视频扩展名', type: 'text', hint: '逗号分隔，命中即生成 .strm' },
  { key: 'strm.meta_ext', label: '元数据扩展名', type: 'text', hint: '逗号分隔，进入下载/上传队列（nfo/图片/字幕）' },
  { key: 'strm.exclude_name', label: '排除文件名', type: 'text', hint: '逗号分隔，文件名包含任一关键词即跳过' },
  { key: 'strm.min_video_size_mb', label: '最小视频大小(MB)', type: 'number', hint: '小于该大小的视频不生成 STRM，0 表示不限' },
  {
    key: 'strm.add_path',
    label: 'STRM 链接 path 参数',
    type: 'select',
    hint: '1=附带完整远端路径 2=仅文件名 3=不带 path',
    options: [
      { value: '1', label: '完整路径' },
      { value: '2', label: '仅文件名' },
      { value: '3', label: '不带 path' },
    ],
  },
  {
    key: 'strm.keep_ext',
    label: '保留视频扩展名（多版本）',
    type: 'toggle',
    hint: '关闭（默认）：同名不同扩展（如 竞女01.mkv / 竞女01.mp4）择优生成一条 name.strm；开启：分别生成 name.mkv.strm / name.mp4.strm，保留全部版本供播放切换',
  },
  { key: 'strm.115_relay_key', label: '115 中继授权共享密钥', type: 'text', hint: 'QMediaSync/MQFamily 中继授权的共享 AES 密钥；不配置则中继授权不可用' },
  { key: 'strm.download_threads', label: '下载队列线程数', type: 'number', hint: '元数据下载并发数' },
  { key: 'strm.upload_threads', label: '上传队列线程数', type: 'number', hint: '元数据上传并发数' },
]

export function StrmSettingsDialog({ onClose }: { onClose: () => void }) {
  const [values, setValues] = useState<Record<string, string> | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    strmAPI
      .getSettings()
      .then(setValues)
      .catch((err) => toast.error(apiErrorMessage(err)))
  }, [])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!values) return
    setSaving(true)
    try {
      await strmAPI.updateSettings(values)
      toast.success('STRM 设置已保存')
      onClose()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <DialogShell title="STRM 设置" subtitle="全局默认配置；同步目录可单独覆盖" onClose={onClose} wide>
      {!values ? (
        <div className="flex justify-center py-10 text-ink-50">
          <Loader2 className="animate-spin" />
        </div>
      ) : (
        <form onSubmit={submit} className="space-y-4">
          {SETTING_DEFS.map((def) => (
            <SettingRow
              key={def.key}
              def={def}
              value={values[def.key] ?? ''}
              onChange={(value) => setValues((v) => ({ ...v!, [def.key]: value }))}
            />
          ))}
          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={onClose} className="rounded-xl border border-gray-200 px-4 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50">
              取消
            </button>
            <button type="submit" disabled={saving} className="neon-button disabled:opacity-50">
              {saving ? <Loader2 size={16} className="animate-spin" /> : null}
              保存设置
            </button>
          </div>
        </form>
      )}
    </DialogShell>
  )
}

// ─── 添加/编辑同步目录 ────────────────────────────────────────────────────────

/** 推断已有配置当前实际拼在本地输出目录末尾的尾段（目录名或 115 目录 ID）。 */
function initRemoteTail(existing: StrmSyncPath): string {
  const cidTail = lastPathSegment(existing.remote_path)
  const nameTail = existing.remote_display_path ? lastPathSegment(existing.remote_display_path) : ''
  if (nameTail && lastPathSegment(existing.local_path) === nameTail) return nameTail
  return cidTail
}

export function StrmSyncPathDialog({
  accounts,
  existing,
  onClose,
  onSaved,
  onOpenSettings,
}: {
  accounts: StrmAccount[]
  existing: StrmSyncPath | null
  onClose: () => void
  onSaved: () => void
  onOpenSettings?: () => void
}) {
  const [form, setForm] = useState<StrmSyncPathInput>(() => ({
    name: existing?.name ?? '',
    provider: existing?.provider ?? 'cloud115',
    account_id: existing?.account_id ?? '',
    remote_path: existing?.remote_path ?? '',
    remote_display_path: existing?.remote_display_path ?? '',
    local_path: existing?.local_path ?? '',
    strm_base_url: existing?.strm_base_url ?? '',
    video_ext: existing?.video_ext ?? '',
    meta_ext: existing?.meta_ext ?? '',
    exclude_name: existing?.exclude_name ?? '',
    min_video_size_mb: existing?.min_video_size_mb ?? 0,
    add_path: existing?.add_path ?? 1,
    download_meta: existing?.download_meta ?? true,
    upload_meta: existing?.upload_meta ?? false,
    delete_dir: existing?.delete_dir ?? false,
    keep_ext: existing?.keep_ext ?? false,
    cron: existing?.cron ?? '',
    enable_cron: existing?.enable_cron ?? false,
    sync_mode: existing?.sync_mode ?? 'incremental',
    enabled: existing?.enabled ?? true,
  }))
  const [saving, setSaving] = useState(false)
  const [browsing, setBrowsing] = useState(false)
  const [browsingLocal, setBrowsingLocal] = useState<null | 'remote_path' | 'local_path'>(null)
  // 输入框显示的远端目录文本：对于 115 优先显示完整路径，若尚未反查到则显示 ID
  const [remoteInputValue, setRemoteInputValue] = useState(
    () => existing?.remote_display_path || existing?.remote_path || '',
  )
  // 反查展示路径的序号守卫：远端目录快速连续变更时丢弃过期响应
  const resolveSeqRef = useRef(0)
  // prevRemoteTailRef 记录当前拼在本地输出目录末尾、由本弹窗管理的尾段。
  // 兼容两类历史数据：新版保存的 local_path 末段是目录名，旧版是目录 ID。
  const prevRemoteTailRef = useRef(existing ? initRemoteTail(existing) : '')

  useEffect(() => {
    if (existing) return
    strmAPI
      .getSettings()
      .then((settings) => {
        const keepExt = settings['strm.keep_ext']
        if (keepExt === 'true' || keepExt === '1') {
          setForm((f) => ({ ...f, keep_ext: true }))
        }
      })
      .catch(() => undefined)
  }, [existing])

  const set = <K extends keyof StrmSyncPathInput>(key: K, value: StrmSyncPathInput[K]) =>
    setForm((f) => ({ ...f, [key]: value }))

  const updateRemotePath = (remotePath: string, displayPath?: string) => {
    const shown = (displayPath || remotePath).trim()
    setRemoteInputValue(shown)
    setForm((f) => {
      const tail = remoteTailNameOf(remotePath, displayPath)
      const synced = syncLocalPathWithRemote(f.local_path, remotePath, prevRemoteTailRef.current, tail)
      prevRemoteTailRef.current = synced.remoteTail
      return {
        ...f,
        remote_path: remotePath,
        remote_display_path: displayPath ?? '',
        local_path: synced.localPath,
      }
    })
  }

  // 按 ID 反查完整展示路径。默认仅补展示、不改动已配置的本地输出目录；
  // resyncTail 用于手动输入 ID 的新配置：把刚拼上的 ID 尾段替换为目录名。
  const resolveDisplayPath = (accountId: string, remotePath: string, resyncTail = false) => {
    const seq = ++resolveSeqRef.current
    strmAPI
      .resolveRemoteDirPath(accountId, remotePath)
      .then((fullPath) => {
        if (seq !== resolveSeqRef.current) return
        if (fullPath) setRemoteInputValue(fullPath)
        setForm((f) => {
          if (f.remote_path !== remotePath) return f
          if (resyncTail) {
            const tail = remoteTailNameOf(remotePath, fullPath)
            const synced = syncLocalPathWithRemote(f.local_path, remotePath, prevRemoteTailRef.current, tail)
            prevRemoteTailRef.current = synced.remoteTail
            return { ...f, remote_display_path: fullPath, local_path: synced.localPath }
          }
          // 旧配置的 local_path 末段若恰好就是目录名，把它认作受管尾段，
          // 后续重新选择目录时才能正确替换而不是叠加
          const tail = lastPathSegment(fullPath)
          if (tail && prevRemoteTailRef.current !== tail && lastPathSegment(f.local_path) === tail) {
            prevRemoteTailRef.current = tail
          }
          return { ...f, remote_display_path: fullPath }
        })
      })
      .catch(() => undefined)
  }

  const commitLocalPath = (localPath: string) => {
    setForm((f) => {
      const tail = remoteTailNameOf(f.remote_path, f.remote_display_path)
      const synced = syncLocalPathWithRemote(localPath, f.remote_path, prevRemoteTailRef.current, tail)
      prevRemoteTailRef.current = synced.remoteTail
      return { ...f, local_path: synced.localPath }
    })
  }

  const isLocal = form.provider === 'local'
  const availableAccounts = accounts.filter((a) => a.provider === form.provider && a.has_credential)
  const remoteDisplayPath = isLocal ? '' : form.remote_display_path?.trim() ?? ''
  const remoteTail = isLocal ? '' : remoteTailNameOf(form.remote_path, remoteDisplayPath)

  // 编辑旧配置时若尚未存展示路径，按 ID 反查补齐并更新输入框展示
  useEffect(() => {
    if (isLocal || form.provider !== 'cloud115') return
    if (!form.account_id || !form.remote_path || form.remote_display_path) return
    resolveDisplayPath(form.account_id, form.remote_path)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    try {
      if (!existing) {
        const settings = await strmAPI.getSettings()
        if (!settings?.['strm.base_url']?.trim()) {
          toast.error('未填写strm地址')
          onClose()
          onOpenSettings?.()
          return
        }
      }
      if (existing) {
        await strmAPI.updatePath(existing.id, form)
        toast.success('同步目录已更新')
      } else {
        await strmAPI.createPath(form)
        toast.success('同步目录已添加')
      }
      onSaved()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <DialogShell
      title={existing ? '编辑同步目录' : '添加同步目录'}
      subtitle="把网盘 / 本地目录的视频生成 .strm 文件到本地输出目录"
      onClose={onClose}
      wide
    >
      <form onSubmit={submit} className="space-y-4">
        <Field label="目录名称">
          <input className={inputCls} value={form.name} placeholder="例如：电影库 115" onChange={(e) => set('name', e.target.value)} />
        </Field>

        <div className="grid gap-3 md:grid-cols-2">
          <Field label="同步类型">
            <select
              className={inputCls}
              value={form.provider}
              onChange={(e) => {
                const provider = e.target.value as StrmProvider
                set('provider', provider)
                set('account_id', '')
                set('remote_path', '')
                set('remote_display_path', '')
                setRemoteInputValue('')
              }}
            >
              <option value="cloud115">115 网盘</option>
              <option value="clouddrive2">CloudDrive2</option>
              <option value="openlist">OpenList</option>
              <option value="local">本地目录</option>
            </select>
          </Field>
          {!isLocal && (
            <Field label="网盘账号">
              <select
                className={inputCls}
                value={form.account_id ?? ''}
                onChange={(e) => set('account_id', e.target.value)}
                disabled={availableAccounts.length === 0}
              >
                <option value="">{availableAccounts.length === 0 ? '请先添加已授权的网盘账号' : '请选择账号'}</option>
                {availableAccounts.map((account) => (
                  <option key={account.id} value={account.id}>
                    {account.name}{account.enabled ? '' : '（已停用）'}
                  </option>
                ))}
              </select>
            </Field>
          )}
        </div>

        <div className="space-y-1">
          <div className="flex items-end gap-2">
            <div className="flex-1">
              <Field
                label={isLocal ? '本地源目录' : '远端目录'}
                hint={
                  isLocal
                    ? '扫描该目录下的视频生成 strm'
                    : form.provider === 'cloud115'
                      ? form.remote_path
                        ? `115 目录 ID：${form.remote_path}（可通过右侧「浏览」选择更换）`
                        : '可通过右侧「浏览」选择 115 目录，或直接粘贴目录 ID'
                      : '远端路径（可通过浏览选择）'
                }
              >
                <input
                  className={inputCls}
                  value={remoteInputValue}
                  placeholder={isLocal ? 'D:\\movies' : form.provider === 'cloud115' ? '点击右侧「浏览」选择目录' : '/'}
                  onChange={(e) => setRemoteInputValue(e.target.value)}
                  onBlur={(e) => {
                    const value = e.target.value.trim()
                    if (isLocal) {
                      set('remote_path', value)
                      return
                    }
                    if (form.provider === 'cloud115') {
                      if (!value) {
                        updateRemotePath('', '')
                        return
                      }
                      // 若用户直接输入/粘贴纯数字目录 ID，触发反查并自动替换为完整路径
                      if (/^\d+$/.test(value)) {
                        updateRemotePath(value, '')
                        if (form.account_id) resolveDisplayPath(form.account_id, value, true)
                        return
                      }
                      // 若当前展示的是完整路径且未变动，不做处理
                      if (value === form.remote_display_path) return
                    } else {
                      updateRemotePath(value)
                    }
                  }}
                />
              </Field>
            </div>
            {isLocal && (
              <button
                type="button"
                onClick={() => setBrowsingLocal('remote_path')}
                className="mb-1 rounded-xl border border-gray-200 px-3 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50"
              >
                浏览
              </button>
            )}
            {!isLocal && form.account_id && (
              <button type="button" onClick={() => setBrowsing(true)} className="mb-1 rounded-xl border border-gray-200 px-3 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50">
                浏览
              </button>
            )}
          </div>
        </div>

        <div className="space-y-1">
          <div className="flex items-end gap-2">
            <div className="flex-1">
              <Field
                label="本地输出目录"
                hint={
                  !isLocal && remoteTail
                    ? `将自动拼接远端末级目录「${remoteTail}」`
                    : '生成的 .strm 与下载的元数据写到这里的对应目录结构下'
                }
              >
                <input
                  className={inputCls}
                  value={form.local_path}
                  placeholder="D:\\strm\\movies"
                  onChange={(e) => set('local_path', e.target.value)}
                  onBlur={(e) => {
                    if (!isLocal) commitLocalPath(e.target.value)
                  }}
                />
              </Field>
            </div>
            <button
              type="button"
              onClick={() => setBrowsingLocal('local_path')}
              className="mb-1 rounded-xl border border-gray-200 px-3 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50"
            >
              浏览
            </button>
          </div>
        </div>

        <details className="rounded-2xl border border-gray-100 bg-gray-50/50 p-3">
          <summary className="cursor-pointer text-sm font-semibold text-ink-600">高级选项</summary>
          <div className="mt-3 space-y-3">
            <div className="grid gap-3 md:grid-cols-3">
              <Field label="视频扩展名（覆盖全局）">
                <input className={inputCls} value={form.video_ext ?? ''} placeholder="mkv,mp4,avi" onChange={(e) => set('video_ext', e.target.value)} />
              </Field>
              <Field label="元数据扩展名（覆盖全局）">
                <input className={inputCls} value={form.meta_ext ?? ''} placeholder="nfo,jpg,srt" onChange={(e) => set('meta_ext', e.target.value)} />
              </Field>
              <Field label="排除文件名（覆盖全局）">
                <input className={inputCls} value={form.exclude_name ?? ''} placeholder="sample,trailer" onChange={(e) => set('exclude_name', e.target.value)} />
              </Field>
            </div>
            <div className="grid gap-3 md:grid-cols-3">
              <Field label="最小视频大小(MB)" hint="0 表示继承全局设置">
                <input
                  className={inputCls}
                  type="number"
                  min={0}
                  value={form.min_video_size_mb ?? 0}
                  onChange={(e) => set('min_video_size_mb', Number(e.target.value))}
                />
              </Field>
              <Field label="STRM 链接 path 参数">
                <select className={inputCls} value={form.add_path ?? 1} onChange={(e) => set('add_path', Number(e.target.value))}>
                  <option value={1}>完整远端路径</option>
                  <option value={2}>仅文件名</option>
                  <option value={3}>不带 path</option>
                </select>
              </Field>
              <Field label="默认同步模式" hint="定时触发或快速同步时的策略">
                <select className={inputCls} value={form.sync_mode ?? 'incremental'} onChange={(e) => set('sync_mode', e.target.value as 'incremental' | 'full')}>
                  <option value="incremental">增量同步（快速）</option>
                  <option value="full">全量同步（全量校验）</option>
                </select>
              </Field>
            </div>
            <Field label="定时同步 Cron" hint="5 段表达式，如 0 */6 * * *">
              <input className={inputCls} value={form.cron ?? ''} placeholder="0 */6 * * *" onChange={(e) => set('cron', e.target.value)} />
            </Field>
            <div className="grid gap-2 md:grid-cols-2">
              <ToggleRow label="下载元数据" checked={form.download_meta ?? true} onChange={(v) => set('download_meta', v)} />
              <ToggleRow label="上传元数据" checked={form.upload_meta ?? false} onChange={(v) => set('upload_meta', v)} />
              <ToggleRow label="清理空目录" checked={form.delete_dir ?? false} onChange={(v) => set('delete_dir', v)} />
              <ToggleRow
                label="保留视频扩展名（多版本）"
                checked={form.keep_ext ?? false}
                onChange={(v) => set('keep_ext', v)}
              />
              <ToggleRow label="启用定时同步" checked={form.enable_cron ?? false} onChange={(v) => set('enable_cron', v)} />
              <ToggleRow label="启用该目录" checked={form.enabled ?? true} onChange={(v) => set('enabled', v)} />
            </div>
          </div>
        </details>

        <div className="flex justify-end gap-2 pt-2">
          <button type="button" onClick={onClose} className="rounded-xl border border-gray-200 px-4 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50">
            取消
          </button>
          <button type="submit" disabled={saving} className="neon-button disabled:opacity-50">
            {saving ? <Loader2 size={16} className="animate-spin" /> : <FolderPlus size={16} />}
            保存
          </button>
        </div>
      </form>

      {browsing && form.account_id && (
        <StrmDirBrowserDialog
          accountId={form.account_id}
          initialDir={form.remote_path || undefined}
          onSelect={(id, _name, fullPath) => {
            updateRemotePath(id, form.provider === 'cloud115' ? fullPath : undefined)
            setBrowsing(false)
          }}
          onClose={() => setBrowsing(false)}
        />
      )}

      {browsingLocal && (
        <LocalDirBrowserDialog
          initialDir={form[browsingLocal] || undefined}
          onSelect={(path) => {
            if (browsingLocal === 'local_path') {
              commitLocalPath(path)
            } else {
              set(browsingLocal, path)
            }
            setBrowsingLocal(null)
          }}
          onClose={() => setBrowsingLocal(null)}
        />
      )}
    </DialogShell>
  )
}

function ToggleRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex cursor-pointer items-center gap-2 text-sm text-ink-100">
      <input
        type="checkbox"
        className="h-4 w-4 accent-primary-400"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      {label}
    </label>
  )
}

/** 把浏览时经过的目录名称链拼成以 / 开头的完整展示路径。 */
function chainFullPath(chain: { id: string; name: string }[]): string {
  const names = chain.map((item) => item.name.trim()).filter(Boolean)
  return names.length > 0 ? '/' + names.join('/') : ''
}

// ─── 远端目录浏览选择器 ───────────────────────────────────────────────────────

function StrmDirBrowserDialog({
  accountId,
  initialDir,
  onSelect,
  onClose,
}: {
  accountId: string
  initialDir?: string
  onSelect: (id: string, name?: string, fullPath?: string) => void
  onClose: () => void
}) {
  const [dir, setDir] = useState(initialDir ?? '')
  const [entries, setEntries] = useState<StrmRemoteEntry[]>([])
  const [loading, setLoading] = useState(true)
  // 已进入目录的名称链：面包屑按名称展示，「选择当前目录」时回传末级名称
  const [dirChain, setDirChain] = useState<{ id: string; name: string }[]>([])
  const dirChainRef = useRef<{ id: string; name: string }[]>([])
  // 递增序号守卫：快速连续进入目录时丢弃过期目录响应
  const loadSeqRef = useRef(0)

  const load = async (target: string, nextChain?: { id: string; name: string }[]) => {
    const seq = ++loadSeqRef.current
    setLoading(true)
    try {
      const list = await strmAPI.listRemoteDir(accountId, target)
      if (seq !== loadSeqRef.current) return
      if (nextChain) dirChainRef.current = nextChain
      setEntries(list)
      setDir(target)
      setDirChain([...dirChainRef.current])
    } catch (err) {
      if (seq !== loadSeqRef.current) return
      toast.error(apiErrorMessage(err))
    } finally {
      if (seq === loadSeqRef.current) setLoading(false)
    }
  }

  useEffect(() => {
    load(initialDir ?? '').catch(() => undefined)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accountId])

  const enterDir = (entry: StrmRemoteEntry) => {
    const chain = [...dirChainRef.current]
    const existingIdx = chain.findIndex((item) => item.id === entry.id)
    if (existingIdx >= 0) chain.splice(existingIdx + 1)
    else chain.push({ id: entry.id, name: entry.name })
    load(entry.id, chain).catch(() => undefined)
  }

  return (
    <div
      className="fixed inset-0 z-[110] flex items-center justify-center bg-black/35 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="flex max-h-[80vh] w-full max-w-2xl flex-col overflow-hidden rounded-3xl border border-white/70 bg-white shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
          <h3 className="font-display text-lg font-bold text-ink-600">选择远端目录</h3>
          <button type="button" onClick={onClose} className="rounded-xl p-1.5 text-ink-50 transition hover:bg-gray-100 hover:text-ink-600">
            <X size={20} />
          </button>
        </div>
        <div className="flex items-center gap-1 border-b border-gray-100 px-6 py-2.5 text-xs text-sand-500">
          <button type="button" className="hover:text-brand-500" onClick={() => load('', [])}>
            根目录
          </button>
          {(dirChain.length > 0 ? dirChain.map((item) => item.name) : dir.split('/').filter(Boolean)).map((label, index) => (
            <span key={label + index} className="flex items-center gap-1">
              <ChevronRight size={12} />
              <span>{label}</span>
            </span>
          ))}
          <span className="ml-2 text-ink-50">{dir || '（根目录 / 0）'}</span>
        </div>
        <div className="min-h-[260px] flex-1 overflow-y-auto p-3">
          {loading ? (
            <div className="flex justify-center py-10 text-ink-50">
              <Loader2 className="animate-spin" />
            </div>
          ) : entries.length === 0 ? (
            <p className="py-10 text-center text-sm text-sand-500">该目录为空</p>
          ) : (
            <div className="space-y-1">
              {entries.map((entry) => {
                const Icon = entry.is_dir ? FolderPlus : HardDrive
                return (
                  <button
                    key={entry.id}
                    type="button"
                    className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left text-sm transition hover:bg-gray-50"
                    onClick={() => (entry.is_dir ? enterDir(entry) : undefined)}
                    onDoubleClick={() => entry.is_dir && enterDir(entry)}
                  >
                    <Icon size={16} className={entry.is_dir ? 'text-brand-400' : 'text-sand-400'} />
                    <span className="flex-1 truncate text-ink-600">{entry.name}</span>
                    {entry.is_dir ? (
                      <button
                        type="button"
                        className="rounded-lg border border-brand-200 bg-brand-50 px-2.5 py-1 text-xs font-semibold text-brand-500 hover:bg-brand-100"
                        onClick={(e) => {
                          e.stopPropagation()
                          onSelect(entry.id, entry.name, chainFullPath([...dirChainRef.current, { id: entry.id, name: entry.name }]))
                        }}
                      >
                        选择此目录
                      </button>
                    ) : (
                      <span className="text-xs text-sand-400">{formatEntrySize(entry.size)}</span>
                    )}
                  </button>
                )
              })}
            </div>
          )}
        </div>
        <div className="flex items-center justify-between border-t border-gray-100 px-6 py-3">
          <span className="text-xs text-sand-500">双击进入目录，点击「选择此目录」使用该目录 ID / 路径</span>
          <button
            type="button"
            className="neon-button"
            onClick={() => onSelect(dir, dirChain[dirChain.length - 1]?.name, chainFullPath(dirChain))}
            disabled={loading}
          >
            选择当前目录
          </button>
        </div>
      </div>
    </div>
  )
}

function formatEntrySize(size: number): string {
  if (!size) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = size
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`
}