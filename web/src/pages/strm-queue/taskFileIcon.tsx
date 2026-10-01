import type { ReactNode } from 'react'
import { File, FileText, Film, Image as ImageIcon, MessageSquare } from 'lucide-react'

export function getFileIcon(filename: string): ReactNode {
  const ext = filename.split('.').pop()?.toLowerCase() ?? ''
  if (['nfo', 'txt', 'xml', 'json'].includes(ext)) {
    return <FileText size={15} className="text-amber-500 shrink-0" />
  }
  if (['jpg', 'jpeg', 'png', 'webp', 'bmp', 'gif', 'svg'].includes(ext)) {
    return <ImageIcon size={15} className="text-blue-500 shrink-0" />
  }
  if (['srt', 'ass', 'ssa', 'sub', 'vtt'].includes(ext)) {
    return <MessageSquare size={15} className="text-purple-500 shrink-0" />
  }
  if (['mkv', 'mp4', 'avi', 'mov', 'wmv', 'ts', 'flv', 'iso', 'm4v', 'strm'].includes(ext)) {
    return <Film size={15} className="text-emerald-500 shrink-0" />
  }
  return <File size={15} className="text-gray-400 shrink-0" />
}
