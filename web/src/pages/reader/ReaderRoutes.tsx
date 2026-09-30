import { Navigate, Route, Routes } from 'react-router-dom'

import ReaderBookPage from './ReaderBookPage'
import ReaderHomePage from './ReaderHomePage'
import ReaderSearchPage from './ReaderSearchPage'
import ReaderSourcesPage from './ReaderSourcesPage'
import ReaderViewPage from './ReaderViewPage'

// 阅读独立布局路由（不套影视 Layout，全屏沉浸；挂在 /reader/* 下）。
export default function ReaderRoutes() {
  return (
    <Routes>
      <Route index element={<ReaderHomePage />} />
      <Route path="search" element={<ReaderSearchPage />} />
      <Route path="sources" element={<ReaderSourcesPage />} />
      <Route path="book" element={<ReaderBookPage />} />
      <Route path="view/:bookId" element={<ReaderViewPage />} />
      <Route path="*" element={<Navigate to="/reader" replace />} />
    </Routes>
  )
}
