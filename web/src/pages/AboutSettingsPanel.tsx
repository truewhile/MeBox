import { useEffect, useState } from 'react'
import {
  BookOpen,
  Check,
  Copy,
  Cpu,
  Database,
  ExternalLink,
  Film,
  FolderOpen,
  Github,
  HardDrive,
  Info,
  Layers,
  ListChecks,
  MessageSquare,
  Play,
  Server,
  Sparkles,
  Tag,
  Tv,
  Users,
} from 'lucide-react'
import toast from 'react-hot-toast'

import { systemAPI } from '../api/system'

type SystemInfo = {
  name: string
  version: string
  go: string
  os: string
  arch: string
  data_dir: string
  cache_dir: string
}

const GITHUB_REPO_URL = 'https://github.com/truewhile/MeBox'

const MODULE_GUIDES = [
  {
    icon: Film,
    title: '媒体库管理',
    badge: '核心存储',
    description:
      '支持创建电影、电视剧、动漫、纪录片等多种类型媒体库。支持本地目录与网络挂载路径，集成文件监控（Inotify / 定时轮询），新增与修改文件自动入库。剧集支持智能按季、集归类与多版本合并。',
  },
  {
    icon: Sparkles,
    title: '智能刮削与元数据',
    badge: '海报与信息',
    description:
      '内置 TMDb、Bangumi、TheTVDB、豆瓣等元数据源。入库时自动识别文件名匹配海报、背景图、演职员及中文简介。支持手动精确搜刮、重命名整理规则、识别词过滤（过滤广告词与压制组标签）及 NFO 本地文件导出。',
  },
  {
    icon: Play,
    title: '流媒体播放与转码',
    badge: '播放引擎',
    description:
      '现代化 Web 播放器支持原画直连（Direct Play）与 HLS 实时兼容转码。支持 Intel QSV、NVIDIA NVENC、VAAPI 等硬件加速。支持内嵌与外挂字幕（ASS/SRT/VTT）渲染、多音轨切换，并支持一键调用 PotPlayer、VLC、IINA、Infuse 等本地播放器。',
  },
  {
    icon: MessageSquare,
    title: '实时弹幕系统',
    badge: '弹幕互动',
    description:
      '播放时自动/手动匹配对应剧集的弹幕源。支持设置弹幕字号、滚动速度、透明度、屏幕占比限制（1/4屏、半屏、全屏）及防遮挡字幕开关，打造沉浸式的观影交流体验。',
  },
  {
    icon: Tv,
    title: 'Emby 挂载与远程同步',
    badge: '扩展聚合',
    description:
      '支持以客户端形式直接挂载远程 Emby 媒体服务器，无需本地下载即可无缝串流播放远程资源，同时保留本地的刮削、播放历史、收藏与弹幕能力，实现多端影视资源大聚合。',
  },
  {
    icon: Layers,
    title: 'STRM 管理与云盘转存',
    badge: '海量存储',
    description:
      '针对 115、阿里云盘、OpenList 等网盘挂载场景，提供批量的 STRM 索引文件生成与下载队列管理。本地仅占用微量磁盘空间，即可享受数百 TB 云端影视的高速直链秒播。',
  },
  {
    icon: FolderOpen,
    title: '内置文件管理器',
    badge: '资源维护',
    description:
      '内置可视化文件浏览器，支持对本地媒体目录及云盘目录进行直接浏览、批量移动、重命名及删除操作，方便快速定位未刮削或命名不规范的文件并进行整理。',
  },
  {
    icon: ListChecks,
    title: '后台任务队列',
    badge: '异步作业',
    description:
      '全生命周期监控媒体扫描、元数据批量刮削、STRM 生成及音视频探测等后台异步任务。实时展示队列进度百分比、执行状态及错误日志，支持手动重试与任务取消。',
  },
  {
    icon: Users,
    title: '多用户与权限体系',
    badge: '安全访问',
    description:
      '支持管理员与普通用户角色。管理员可精细化控制各账号的访问边界，包括特定媒体库可见性（如敏感/Adult媒体库）、是否允许调用外部播放器、是否允许下载原始文件等。',
  },
  {
    icon: Database,
    title: '数据库与系统设置',
    badge: '底层基建',
    description:
      '支持轻量级单文件 SQLite 与高并发 PostgreSQL 数据库，并提供一键平滑数据热迁移工具；支持自定义 HTTPS 证书绑定、转码缓存自动清理及实时系统资源监控。',
  },
]

export function AboutSettingsPanel() {
  const [info, setInfo] = useState<SystemInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    systemAPI
      .info()
      .then((res) => {
        setInfo({
          name: res.name || 'MeBox',
          version: res.version || '0.1.0',
          go: res.go || '',
          os: res.os || '',
          arch: res.arch || '',
          data_dir: res.data_dir || '',
          cache_dir: res.cache_dir || '',
        })
      })
      .catch(() => {
        setInfo({
          name: 'MeBox',
          version: '0.1.0',
          go: '',
          os: '',
          arch: '',
          data_dir: '',
          cache_dir: '',
        })
      })
      .finally(() => setLoading(false))
  }, [])

  const handleCopyGitURL = async () => {
    try {
      await navigator.clipboard.writeText(GITHUB_REPO_URL)
      setCopied(true)
      toast.success('GitHub 地址已复制到剪贴板')
      setTimeout(() => setCopied(false), 2000)
    } catch {
      toast.error('复制失败，请手动复制')
    }
  }

  return (
    <div className="space-y-6">
      {/* 顶部概览与项目卡片 */}
      <div className="glass-panel overflow-hidden p-6 sm:p-8">
        <div className="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-3">
              <h2 className="font-display text-2xl sm:text-3xl font-bold text-ink-600">
                MeBox 影音管理系统
              </h2>
              <span className="inline-flex items-center gap-1.5 rounded-full bg-brand-500/10 px-3 py-1 text-xs font-semibold text-brand-600 border border-brand-500/20">
                <Tag size={13} />
                {loading ? '加载中...' : `v${info?.version || '0.1.0'}`}
              </span>
            </div>
            <p className="max-w-2xl text-sm leading-relaxed text-sand-600">
              MeBox 是一款专为个人与家庭设计的现代化开源媒体库管理与流媒体点播系统。
              集媒体资源归纳、智能多源刮削、弹幕交互、海量云盘 STRM 转存与多端播放于一体，让观影体验更轻快自由。
            </p>
          </div>

          {/* 仓库链接操作区 */}
          <div className="flex flex-wrap items-center gap-3">
            <a
              href={GITHUB_REPO_URL}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-2 rounded-xl bg-ink-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-ink-700 hover:shadow"
            >
              <Github size={16} />
              <span>访问 GitHub 仓库</span>
              <ExternalLink size={14} className="opacity-70" />
            </a>
            <button
              type="button"
              onClick={handleCopyGitURL}
              className="inline-flex items-center gap-2 rounded-xl border border-gray-200 bg-white/80 px-4 py-2.5 text-sm font-medium text-ink-600 shadow-sm transition hover:bg-white hover:text-brand-600"
            >
              {copied ? <Check size={16} className="text-emerald-500" /> : <Copy size={16} />}
              <span>{copied ? '已复制' : '复制地址'}</span>
            </button>
          </div>
        </div>

        {/* 运行环境规格栏 */}
        <div className="mt-6 grid grid-cols-2 gap-3 sm:grid-cols-4 border-t border-gray-200/60 pt-6">
          <div className="rounded-xl bg-sand-100/50 p-3.5">
            <div className="flex items-center gap-1.5 text-xs text-sand-500">
              <Tag size={13} />
              <span>系统版本</span>
            </div>
            <p className="mt-1 font-mono text-sm font-semibold text-ink-600">
              {loading ? '...' : info?.version || '0.1.0'}
            </p>
          </div>
          <div className="rounded-xl bg-sand-100/50 p-3.5">
            <div className="flex items-center gap-1.5 text-xs text-sand-500">
              <Server size={13} />
              <span>运行环境</span>
            </div>
            <p className="mt-1 font-mono text-sm font-semibold text-ink-600">
              {loading ? '...' : `${info?.os || 'linux'} / ${info?.arch || 'amd64'}`}
            </p>
          </div>
          <div className="rounded-xl bg-sand-100/50 p-3.5">
            <div className="flex items-center gap-1.5 text-xs text-sand-500">
              <Cpu size={13} />
              <span>Go 运行时</span>
            </div>
            <p className="mt-1 font-mono text-sm font-semibold text-ink-600">
              {loading ? '...' : info?.go || 'go1.x'}
            </p>
          </div>
          <div className="rounded-xl bg-sand-100/50 p-3.5">
            <div className="flex items-center gap-1.5 text-xs text-sand-500">
              <HardDrive size={13} />
              <span>数据目录</span>
            </div>
            <p className="mt-1 truncate font-mono text-xs font-semibold text-ink-600" title={info?.data_dir}>
              {loading ? '...' : info?.data_dir || './data'}
            </p>
          </div>
        </div>
      </div>

      {/* 快速入门与使用流程 */}
      <div className="glass-panel p-6 sm:p-8 space-y-4">
        <div className="flex items-center gap-2">
          <BookOpen className="text-brand-500" size={20} />
          <h3 className="font-display text-lg font-bold text-ink-600">使用说明与快速上手</h3>
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          <div className="flex flex-col gap-2 rounded-2xl border border-gray-200/80 bg-white/60 p-4 shadow-sm">
            <div className="flex items-center gap-2">
              <span className="flex h-6 w-6 items-center justify-center rounded-full bg-brand-500 text-xs font-bold text-white">
                1
              </span>
              <span className="font-semibold text-ink-600 text-sm">创建与关联媒体库</span>
            </div>
            <p className="text-xs leading-relaxed text-sand-500">
              在侧边栏进入「媒体库」，点击添加媒体库。选择媒体类型（电影/电视剧），指定服务器上的实际媒体路径或挂载目录，保存后系统将自动创建索引并监控变动。
            </p>
          </div>

          <div className="flex flex-col gap-2 rounded-2xl border border-gray-200/80 bg-white/60 p-4 shadow-sm">
            <div className="flex items-center gap-2">
              <span className="flex h-6 w-6 items-center justify-center rounded-full bg-brand-500 text-xs font-bold text-white">
                2
              </span>
              <span className="font-semibold text-ink-600 text-sm">智能识别与刮削</span>
            </div>
            <p className="text-xs leading-relaxed text-sand-500">
              扫描入库后，后台服务会自动提取文件名并通过 TMDb / 豆瓣 / Bangumi 等抓取高清海报、演职员与分集信息；如遇特殊命名，可在详情页中一键进行「手动搜刮」。
            </p>
          </div>

          <div className="flex flex-col gap-2 rounded-2xl border border-gray-200/80 bg-white/60 p-4 shadow-sm">
            <div className="flex items-center gap-2">
              <span className="flex h-6 w-6 items-center justify-center rounded-full bg-brand-500 text-xs font-bold text-white">
                3
              </span>
              <span className="font-semibold text-ink-600 text-sm">多端随心畅享观影</span>
            </div>
            <p className="text-xs leading-relaxed text-sand-500">
              直接在浏览器中享受原画秒开与实时弹幕互动；对于高码率或特殊封装格式，支持开启 HLS 转码播放或直接一键唤醒本地 PotPlayer、Infuse 等外部播放器。
            </p>
          </div>
        </div>
      </div>

      {/* 各功能模块详细使用说明 */}
      <div className="space-y-4">
        <div className="flex items-center justify-between px-1">
          <div className="flex items-center gap-2">
            <Info className="text-brand-500" size={20} />
            <h3 className="font-display text-lg font-bold text-ink-600">各功能模块详细说明</h3>
          </div>
          <span className="text-xs text-sand-500">涵盖系统十大核心子系统</span>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          {MODULE_GUIDES.map((mod) => {
            const Icon = mod.icon
            return (
              <div
                key={mod.title}
                className="group flex flex-col justify-between rounded-2xl border border-gray-200/90 bg-white/70 p-5 shadow-sm transition hover:border-brand-500/40 hover:bg-white hover:shadow-md"
              >
                <div className="space-y-2.5">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2.5">
                      <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-brand-500/10 text-brand-500 transition group-hover:bg-brand-500 group-hover:text-white">
                        <Icon size={18} />
                      </div>
                      <h4 className="font-semibold text-ink-600 text-sm sm:text-base">
                        {mod.title}
                      </h4>
                    </div>
                    <span className="rounded-md bg-sand-100 px-2 py-0.5 text-[11px] font-medium text-sand-600">
                      {mod.badge}
                    </span>
                  </div>
                  <p className="text-xs leading-relaxed text-sand-600 sm:text-[13px]">
                    {mod.description}
                  </p>
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
