import { ReaderHomeContent } from './ReaderHomeContent'

// 阅读主页（/reader）：书架 + 搜索/书源入口。
export default function ReaderHomePage() {
  return (
    <div className="mx-auto min-h-[100dvh] w-full max-w-7xl px-4 pb-16 pt-4 sm:px-6">
      <ReaderHomeContent />
    </div>
  )
}
