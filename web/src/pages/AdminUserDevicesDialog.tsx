import { useCallback, useEffect, useState } from 'react'
import toast from 'react-hot-toast'
import { Loader2, LogOut, MonitorSmartphone, RefreshCw, X } from 'lucide-react'

import { devicesAPI, type DeviceInfo } from '../api/devices'
import type { User } from '../types'

// AdminUserDevicesDialog 让管理员代管某个用户的登录设备。
//
// 使用场景：用户忘记密码/账号疑似外借/需要清掉旧电视端会话时，管理员不必
// 让用户自己操作，也不必进数据库。
type AdminUserDevicesDialogProps = {
  user: User | null
  isOpen: boolean
  onClose: () => void
}

export function AdminUserDevicesDialog({ user, isOpen, onClose }: AdminUserDevicesDialogProps) {
  const [devices, setDevices] = useState<DeviceInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [kickingAll, setKickingAll] = useState(false)

  const load = useCallback(async (userId: string) => {
    setLoading(true)
    try {
      const rows = await devicesAPI.listForUser(userId)
      setDevices(rows)
    } catch {
      toast.error('加载设备列表失败')
      setDevices([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!isOpen || !user) return
    setDevices([])
    void load(user.id)
  }, [isOpen, user, load])

  if (!isOpen || !user) return null

  const kick = async (device: DeviceInfo) => {
    const id = device.device_id || device.id
    setBusyId(id)
    try {
      await devicesAPI.kickForUser(user.id, id)
      toast.success('设备已下线')
      await load(user.id)
    } catch {
      toast.error('踢下线失败')
    } finally {
      setBusyId(null)
    }
  }

  const kickAll = async () => {
    setKickingAll(true)
    try {
      await devicesAPI.kickAllForUser(user.id)
      toast.success(`已下线 ${user.username} 的全部设备`)
      await load(user.id)
    } catch {
      toast.error('操作失败')
    } finally {
      setKickingAll(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="w-full max-w-2xl overflow-hidden rounded-3xl border border-white/70 bg-white shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-sand-100 p-5">
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-2xl bg-brand-50 text-brand-600">
              <MonitorSmartphone size={20} />
            </div>
            <div>
              <h3 className="font-display text-lg font-bold text-ink-600">登录设备管理</h3>
              <p className="text-xs text-sand-500">
                用户：<span className="font-semibold text-ink-600">{user.username}</span>
                <span className="ml-2 text-sand-400">
                  共 {devices.length} 台
                </span>
              </p>
            </div>
          </div>
          <button
            onClick={onClose}
            className="rounded-xl p-2 text-sand-500 transition-colors hover:bg-sand-100 hover:text-ink-600"
          >
            <X size={18} />
          </button>
        </div>

        <div className="max-h-[60vh] overflow-y-auto p-5">
          {loading ? (
            <div className="flex items-center justify-center gap-2 py-12 text-xs text-sand-500">
              <Loader2 size={16} className="animate-spin text-brand-500" />
              正在加载设备…
            </div>
          ) : devices.length === 0 ? (
            <div className="rounded-2xl bg-sand-50/50 py-10 text-center text-xs text-sand-500">
              该用户暂无设备记录
            </div>
          ) : (
            <ul className="space-y-2">
              {devices.map((device) => (
                <li
                  key={device.id}
                  className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-sand-200 p-3"
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2 text-xs font-semibold text-ink-600">
                      <span className="truncate">
                        {device.device_name || device.client || '未知设备'}
                      </span>
                      {device.online && (
                        <span className="rounded-md bg-emerald-50 px-1.5 py-0.5 text-[10px] font-bold text-emerald-600">
                          在线
                        </span>
                      )}
                      {device.playing && (
                        <span className="rounded-md bg-brand-50 px-1.5 py-0.5 text-[10px] font-bold text-brand-600">
                          播放中
                        </span>
                      )}
                      {device.kicked && (
                        <span className="rounded-md bg-rose-50 px-1.5 py-0.5 text-[10px] font-bold text-rose-600">
                          已下线
                        </span>
                      )}
                      {device.warnings > 0 && (
                        <span className="rounded-md bg-amber-50 px-1.5 py-0.5 text-[10px] font-bold text-amber-600">
                          警告 {device.warnings}
                        </span>
                      )}
                    </div>
                    <div className="mt-0.5 flex flex-wrap gap-x-3 text-[11px] text-sand-500">
                      {device.client && <span>{device.client}</span>}
                      {device.last_ip && <span className="font-mono">{device.last_ip}</span>}
                      {device.last_seen_at && <span>最近活跃 {formatTime(device.last_seen_at)}</span>}
                    </div>
                  </div>
                  <button
                    type="button"
                    onClick={() => void kick(device)}
                    disabled={busyId === (device.device_id || device.id)}
                    className="shrink-0 rounded-xl border border-sand-200 px-3 py-1.5 text-[11px] font-semibold text-sand-500 transition-colors hover:border-rose-300 hover:text-rose-500 disabled:opacity-40"
                  >
                    {busyId === (device.device_id || device.id) ? '下线中…' : '踢下线'}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="flex items-center justify-between gap-2.5 border-t border-sand-100 bg-sand-50/60 px-5 py-3.5">
          <button
            type="button"
            onClick={() => void load(user.id)}
            className="inline-flex items-center gap-1.5 rounded-xl border border-sand-200 bg-white px-3 py-2 text-xs font-semibold text-ink-600 transition-colors hover:bg-sand-100"
          >
            <RefreshCw size={13} />
            刷新
          </button>
          <button
            type="button"
            disabled={kickingAll || devices.length === 0}
            onClick={() => void kickAll()}
            className="inline-flex items-center gap-1.5 rounded-xl border border-rose-300 bg-white px-3 py-2 text-xs font-semibold text-rose-500 transition-colors hover:bg-rose-50 disabled:opacity-40"
          >
            {kickingAll ? <Loader2 size={13} className="animate-spin" /> : <LogOut size={13} />}
            全部下线
          </button>
        </div>
      </div>
    </div>
  )
}

function formatTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}
