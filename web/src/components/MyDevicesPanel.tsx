import { useCallback, useEffect, useRef, useState } from 'react'
import toast from 'react-hot-toast'
import { Loader2, LogOut, MonitorSmartphone, RefreshCw, Smartphone, Tv } from 'lucide-react'

import { devicesAPI, type DeviceInfo } from '../api/devices'

// MyDevicesPanel 让用户自己看清「谁在用我的账号」，并能把可疑终端踢下线。
//
// 踢下线后该终端必须重新登录；这正是防共享策略生效时的自救路径。
export function MyDevicesPanel() {
  const [devices, setDevices] = useState<DeviceInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [kickingAll, setKickingAll] = useState(false)
  const mountedRef = useRef(true)

  const load = useCallback(async () => {
    try {
      const rows = await devicesAPI.listMine()
      if (mountedRef.current) setDevices(rows)
    } catch {
      if (mountedRef.current) toast.error('设备列表加载失败')
    } finally {
      if (mountedRef.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    mountedRef.current = true
    void load()
    return () => {
      mountedRef.current = false
    }
  }, [load])

  const kick = async (device: DeviceInfo) => {
    const id = device.device_id || device.id
    setBusyId(id)
    try {
      await devicesAPI.kickMine(id)
      toast.success('该设备已下线，需要重新登录')
      await load()
    } catch {
      toast.error('踢下线失败')
    } finally {
      setBusyId(null)
    }
  }

  const kickAll = async () => {
    setKickingAll(true)
    try {
      await devicesAPI.kickAllMine()
      toast.success('全部设备已下线')
      await load()
    } catch {
      toast.error('操作失败')
    } finally {
      setKickingAll(false)
    }
  }

  return (
    <section className="glass-panel space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="flex items-center gap-2 font-display text-lg font-semibold text-ink-600">
            <MonitorSmartphone size={20} className="text-brand-500" />
            我的设备
          </h2>
          <p className="mt-1 text-sm text-ink-50">
            网页、手机与电视端登录过的终端都会出现在这里。发现陌生设备时请踢下线并修改密码。
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={() => void load()}
            className="rounded-xl border border-[var(--app-border)] p-2 text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
            title="刷新"
          >
            <RefreshCw size={16} />
          </button>
          <button
            type="button"
            onClick={() => void kickAll()}
            disabled={kickingAll || devices.length === 0}
            className="inline-flex items-center gap-2 rounded-xl border border-rose-300 px-3 py-2 text-sm font-semibold text-rose-500 transition-colors hover:bg-rose-50 disabled:opacity-40"
          >
            {kickingAll ? <Loader2 size={16} className="animate-spin" /> : <LogOut size={16} />}
            全部下线
          </button>
        </div>
      </div>

      {loading ? (
        <div className="flex justify-center py-8 text-ink-50">
          <Loader2 className="animate-spin" />
        </div>
      ) : devices.length === 0 ? (
        <p className="rounded-2xl border border-dashed border-[var(--app-border)] p-6 text-center text-sm text-[var(--app-muted)]">
          暂无设备记录。使用 Emby / Infuse 等客户端登录后会自动出现在这里。
        </p>
      ) : (
        <ul className="space-y-2">
          {devices.map((device) => (
            <li
              key={device.id}
              className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-3"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]">
                  {deviceIcon(device)}
                  <span className="truncate">
                    {device.device_name || device.client || '未知设备'}
                  </span>
                  {device.playing && (
                    <span className="rounded-md bg-emerald-500/15 px-1.5 py-0.5 text-[10px] font-bold text-emerald-500">
                      播放中
                    </span>
                  )}
                  {device.kicked && (
                    <span className="rounded-md bg-rose-500/15 px-1.5 py-0.5 text-[10px] font-bold text-rose-500">
                      已下线
                    </span>
                  )}
                </div>
                <div className="mt-0.5 flex flex-wrap gap-x-3 text-xs text-[var(--app-muted)]">
                  {device.client && <span>{device.client}</span>}
                  {device.last_ip && <span className="font-mono">{device.last_ip}</span>}
                  {device.last_seen_at && (
                    <span>最近活跃 {formatTime(device.last_seen_at)}</span>
                  )}
                </div>
              </div>
              <button
                type="button"
                onClick={() => void kick(device)}
                disabled={busyId === (device.device_id || device.id)}
                className="shrink-0 rounded-xl border border-[var(--app-border)] px-3 py-1.5 text-xs font-semibold text-[var(--app-muted)] transition-colors hover:border-rose-300 hover:text-rose-500 disabled:opacity-40"
              >
                {busyId === (device.device_id || device.id) ? '下线中…' : '踢下线'}
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function deviceIcon(device: DeviceInfo) {
  const client = (device.client || '').toLowerCase()
  if (client.includes('tv') || client.includes('infuse') || client.includes('emby')) {
    return <Tv size={16} className="shrink-0 text-brand-500" />
  }
  return <Smartphone size={16} className="shrink-0 text-brand-500" />
}

function formatTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}
