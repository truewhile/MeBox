import { useEffect, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { CheckCircle2, Circle, Database, Film, KeyRound, Server, Wrench } from 'lucide-react'

import { adminAPI, type DatabaseStatus } from '../api/admin'
import { apiConfigsAPI, type APIConfig } from '../api/api_configs'
import { libraryAPI, mediaAPI } from '../api/library'
import { toolsAPI, type FFmpegToolsStatus } from '../api/tools'
import type { Library } from '../types'
import type { SeriesCard } from '../utils/groupSeries'

type CheckResult = { ok: boolean; message: string }
type SetupStepProps = { done: boolean; title: string; detail: string; icon: typeof Database; children?: ReactNode }

function SetupStep({ done, title, detail, icon: Icon, children }: SetupStepProps) {
  return (
    <section className="glass-panel space-y-3">
      <div className="flex items-start gap-3"><div className="mt-0.5 text-brand-500"><Icon size={20} /></div><div className="min-w-0 flex-1"><div className="flex items-center gap-2"><h2 className="font-display text-lg font-bold text-ink-600">{title}</h2>{done ? <CheckCircle2 size={16} className="text-emerald-500" /> : <Circle size={16} className="text-ink-50" />}</div><p className="mt-1 text-sm text-ink-50">{detail}</p></div></div>
      {children}
    </section>
  )
}

export function SetupWizardPage() {
  const [database, setDatabase] = useState<DatabaseStatus | null>(null)
  const [libraries, setLibraries] = useState<Library[]>([])
  const [sources, setSources] = useState<APIConfig[]>([])
  const [ffmpeg, setFFmpeg] = useState<FFmpegToolsStatus | null>(null)
  const [checks, setChecks] = useState<Record<string, CheckResult>>({})
  const [busy, setBusy] = useState('')
  const [recent, setRecent] = useState<SeriesCard | null>(null)
  const [playbackVerified, setPlaybackVerified] = useState(false)

  const refresh = async () => {
    setBusy('refresh')
    try {
      const [db, libs, configs, tools, media] = await Promise.allSettled([
        adminAPI.getDatabaseStatus(), libraryAPI.list(), apiConfigsAPI.list(), toolsAPI.ffToolsStatus(), mediaAPI.recent(1),
      ])
      if (db.status === 'fulfilled') setDatabase(db.value)
      if (libs.status === 'fulfilled') setLibraries(libs.value)
      if (configs.status === 'fulfilled') setSources(configs.value)
      if (tools.status === 'fulfilled') setFFmpeg(tools.value)
      if (media.status === 'fulfilled') setRecent(media.value[0] ?? null)
      const failed = [db, libs, configs, tools, media].some((result) => result.status === 'rejected')
      setChecks((value) => ({ ...value, load: { ok: !failed, message: failed ? '部分检查项读取失败，请刷新状态后重试' : '' } }))
    } finally {
      setBusy('')
    }
  }
  useEffect(() => { void refresh() }, [])

  const testSources = async () => {
    const enabled = sources.filter((source) => source.enabled)
    if (!enabled.length) {
      setChecks((value) => ({ ...value, sources: { ok: false, message: '尚未启用元数据源，可先前往管理来源配置' } }))
      return
    }
    setBusy('sources')
    const results = await Promise.all(enabled.map(async (source) => {
      try { const result = await apiConfigsAPI.test(source.provider); return { name: source.provider, ok: !result.error, message: result.error || result.result } }
      catch { return { name: source.provider, ok: false, message: '连接测试失败' } }
    }))
    const failed = results.filter((result) => !result.ok)
    setChecks((value) => ({ ...value, sources: { ok: failed.length === 0, message: failed.length ? `${failed.length}/${results.length} 个来源测试失败：${failed.map((result) => `${result.name}（${result.message}）`).join('、')}` : `已完成 ${results.length} 个来源的连接测试` } }))
    setBusy('')
  }

  const scanLibrary = async (library: Library) => {
    setBusy(library.id)
    try {
      const result = await libraryAPI.scan(library.id)
      setChecks((value) => ({ ...value, [`scan:${library.id}`]: { ok: !result.queued, message: result.message || (result.queued ? '扫描已提交，请在任务队列确认是否完成' : `扫描完成：新增 ${result.added ?? 0} 项`) } }))
    } catch {
      setChecks((value) => ({ ...value, [`scan:${library.id}`]: { ok: false, message: '扫描失败，请检查容器内路径和目录权限' } }))
    } finally { setBusy('') }
  }

  return (
    <div className="mx-auto max-w-4xl space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3"><div><h1 className="font-display text-3xl font-bold text-ink-600">首次配置向导</h1><p className="mt-1 text-sm text-ink-50">逐项检查配置，完成后可以随时回到对应设置调整。</p></div><button type="button" className="btn-outline" disabled={busy !== ''} onClick={() => void refresh()}>刷新状态</button></header>
      {checks.load && !checks.load.ok && <p role="alert" className="glass-panel text-sm text-red-500">{checks.load.message}</p>}
      <SetupStep done={Boolean(database)} title="数据库" detail={database ? `${database.type.toUpperCase()} · ${database.open_conns} 个连接` : '正在检查数据库'} icon={Database} />
      <SetupStep done={Boolean(ffmpeg?.ffmpeg?.installed && ffmpeg?.ffprobe?.installed)} title="播放工具" detail={ffmpeg?.ffmpeg?.installed ? ffmpeg.ffmpeg.version || 'FFmpeg 已安装' : 'FFmpeg 未安装，浏览器不兼容的媒体无法转码'} icon={Wrench}>
        {!ffmpeg?.ffmpeg?.installed && <Link to="/settings?group=general" className="btn-outline inline-flex">前往安装 FFmpeg</Link>}
      </SetupStep>
      <SetupStep done={Boolean(checks.sources?.ok)} title="元数据源" detail={checks.sources?.message || `${sources.filter((source) => source.enabled).length} 个已启用来源`} icon={KeyRound}>
        <div className="flex flex-wrap items-center gap-3"><button type="button" className="btn-outline" disabled={busy !== ''} onClick={() => void testSources()}>{busy === 'sources' ? '测试中…' : '测试已配置来源'}</button><Link to="/settings?group=api-configs" className="text-sm text-brand-500">管理来源</Link></div>
      </SetupStep>
      <SetupStep done={libraries.length > 0 && libraries.every((library) => library.is_remote_emby || checks[`scan:${library.id}`]?.ok)} title="媒体库与目录" detail={libraries.length ? `已配置 ${libraries.length} 个媒体库；扫描会遍历所选本地库的全部目录，请先确认 Compose 中挂载了正确的容器路径` : '请先创建媒体库并填写容器内路径'} icon={Server}>
        {libraries.length ? <div className="space-y-2">{libraries.map((library) => <div key={library.id} className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-sand-200 p-3"><div className="min-w-0"><p className="text-sm font-semibold text-ink-600">{library.name}{library.is_remote_emby && <span className="ml-2 text-xs font-normal text-ink-50">远程只读</span>}</p><p className="break-all text-xs text-ink-50">{library.path}</p>{checks[`scan:${library.id}`] && <p className={`mt-1 text-xs ${checks[`scan:${library.id}`].ok ? 'text-emerald-600' : 'text-red-500'}`}>{checks[`scan:${library.id}`].message}</p>}</div>{!library.is_remote_emby && <button type="button" className="btn-outline" disabled={busy !== ''} onClick={() => void scanLibrary(library)}>{busy === library.id ? '提交中…' : '扫描整个媒体库'}</button>}</div>)}</div> : <Link to="/libraries" className="btn-outline inline-flex">创建媒体库</Link>}
      </SetupStep>
      <SetupStep done={playbackVerified} title="验证播放" detail={recent ? `最近入库：${recent.rep.title}；播放后请确认画面和声音正常` : '先完成媒体库扫描，找到一个可播放条目'} icon={Film}>
        {recent && <Link to={`/play/${recent.linkMedia?.id ?? recent.rep.id}`} className="btn-primary inline-flex">播放测试片</Link>}
        {recent && <label className="ml-3 inline-flex items-center gap-2 text-sm text-ink-100"><input type="checkbox" checked={playbackVerified} onChange={(event) => setPlaybackVerified(event.target.checked)} />已确认播放正常</label>}
      </SetupStep>
      <div className="flex flex-wrap gap-3"><Link to="/queue" className="btn-outline">查看任务进度</Link><Link to="/settings" className="btn-outline">打开系统设置</Link></div>
    </div>
  )
}
