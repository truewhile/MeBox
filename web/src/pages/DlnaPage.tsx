import { useEffect, useState } from 'react'
import toast from 'react-hot-toast'
import { Cast, RefreshCw, Tv } from 'lucide-react'

import { dlnaAPI, type DLNADevice } from '../api/dlna'
import { mediaAPI } from '../api/library'
import { streamURL } from '../api/client'
import type { Media } from '../types'

// DlnaPage scans the LAN for UPnP MediaRenderer devices and lets the
// user push a media item to one of them via SetAVTransportURI + Play.
export function DlnaPage() {
  const [devices, setDevices] = useState<DLNADevice[]>([])
  const [scanning, setScanning] = useState(false)
  const [media, setMedia] = useState<Media[]>([])
  const [selectedMedia, setSelectedMedia] = useState<string>('')

  const scan = (force: boolean) => {
    setScanning(true)
    dlnaAPI
      .list(force)
      .then(setDevices)
      .catch(() => toast.error('设备发现失败，容器网络可能不支持组播'))
      .finally(() => setScanning(false))
  }

  useEffect(() => {
    scan(false)
    mediaAPI.search('', 30).then((d) => {
      setMedia(d.items)
      if (d.items.length > 0) setSelectedMedia(d.items[0].id)
    })
  }, [])

  const cast = async (dev: DLNADevice) => {
    if (!selectedMedia) {
      toast.error('请先选择一个媒体')
      return
    }
    // Build the absolute URL the renderer will pull from.
    const url = window.location.origin + streamURL(selectedMedia)
    try {
      await dlnaAPI.cast(dev.control_url, url)
      toast.success(`已投屏到 ${dev.friendly_name}`)
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '投屏失败'
      toast.error(msg)
    }
  }

  return (
    <div className="space-y-6">
      <header className="flex items-center gap-3">
        <Cast className="h-6 w-6 text-brand-500" />
        <div>
          <h1 className="font-display text-3xl font-bold text-ink-600">DLNA 投屏</h1>
          <p className="text-sm text-ink-50">
            扫描局域网中的 UPnP MediaRenderer 设备(电视、机顶盒等),选择媒体后一键播放。
          </p>
        </div>
      </header>

      <div className="glass-panel space-y-3">
        <label className="block text-sm text-ink-100">选择媒体:</label>
        <select
          className="input-base"
          value={selectedMedia}
          onChange={(e) => setSelectedMedia(e.target.value)}
        >
          {media.length === 0 && <option>暂无媒体</option>}
          {media.map((m) => (
            <option key={m.id} value={m.id}>
              {m.title}
            </option>
          ))}
        </select>
      </div>

      <div className="flex items-center justify-between">
        <h2 className="font-display text-xl font-semibold text-ink-600">
          设备 ({devices.length})
        </h2>
        <button
          onClick={() => scan(true)}
          disabled={scanning}
          className="neon-button !px-3 !py-1 !text-xs"
        >
          <RefreshCw size={12} className={scanning ? 'animate-spin' : ''} /> 重新扫描
        </button>
      </div>

      {devices.length === 0 && !scanning && (
        <div className="glass-panel">
          <p className="text-ink-100">
            未发现任何 DLNA 设备。请确保：服务器与设备在同一局域网，容器使用 host 网络模式，
            目标设备已开启 DLNA / 屏幕镜像。
          </p>
        </div>
      )}

      <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
        {devices.map((dev) => (
          <div key={dev.udn} className="glass-panel space-y-2 !p-4">
            <div className="flex items-center gap-2">
              <Tv size={18} className="text-brand-500" />
              <p className="font-medium text-ink-600">{dev.friendly_name || dev.model_name}</p>
            </div>
            <p className="text-xs text-ink-50">
              {dev.manufacturer} · {dev.ip_address}
            </p>
            <button
              onClick={() => cast(dev)}
              disabled={!dev.control_url}
              className="neon-button w-full !text-xs"
            >
              <Cast size={12} /> 投屏
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
