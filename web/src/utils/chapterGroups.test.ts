import { CHAPTER_GROUP_SIZE, buildChapterGroups, chapterGroupIndexOf } from './chapterGroups'

// 目录分段分组的纯逻辑回归测试（对齐 utils/readerBookGroups.test.ts 的写法）。

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`chapterGroups: ${name}`)
}

check('默认组大小是 100', CHAPTER_GROUP_SIZE === 100)

// ── 切组 ──
check('空目录没有分组', buildChapterGroups(0).length === 0)
check('负数没有分组', buildChapterGroups(-5).length === 0)
check('overflow 值没有分组', buildChapterGroups(Number.POSITIVE_INFINITY).length === 0)
check('不足一组时只有一组', (() => {
  const g = buildChapterGroups(37)
  return g.length === 1 && g[0].start === 0 && g[0].end === 36 && g[0].label === '1-37'
})())
check('正好一组时只有一组', buildChapterGroups(100).length === 1)
check('101 条切成两组', (() => {
  const g = buildChapterGroups(101)
  return g.length === 2 && g[1].start === 100 && g[1].end === 100 && g[1].label === '101-101'
})())
check('1317 条切成 14 组', (() => {
  const g = buildChapterGroups(1317)
  return g.length === 14 && g[13].label === '1301-1317' && g[13].end === 1316
})())
check('相邻组首尾相接不重叠', (() => {
  const g = buildChapterGroups(1317)
  return g.every((item, i) => i === 0 || item.start === g[i - 1].end + 1)
})())
check('组序号连续且从 0 起', buildChapterGroups(250).map((x) => x.index).join(',') === '0,1,2')
check('自定义组大小生效', (() => {
  const g = buildChapterGroups(10, 4)
  return g.length === 3 && g[1].label === '5-8' && g[2].label === '9-10'
})())
check('非法组大小不产生分组', buildChapterGroups(10, 0).length === 0)

// ── 下标 → 组 ──
check('第 0 条在第 0 组', chapterGroupIndexOf(0, 1317) === 0)
check('第 99 条仍在第 0 组', chapterGroupIndexOf(99, 1317) === 0)
check('第 100 条进入第 1 组', chapterGroupIndexOf(100, 1317) === 1)
check('末条落在末组', chapterGroupIndexOf(1316, 1317) === 13)
check('负数夹到第 0 组', chapterGroupIndexOf(-3, 1317) === 0)
check('越界夹到末组', chapterGroupIndexOf(99999, 1317) === 13)
check('非数字当作第 0 组', chapterGroupIndexOf(Number.NaN, 1317) === 0)
check('空目录返回 0 组', chapterGroupIndexOf(5, 0) === 0)
check('与切组分段一致', (() => {
  const g = buildChapterGroups(1317)
  return g.every((item) => chapterGroupIndexOf(item.start, 1317) === item.index)
})())

console.log('chapterGroups.test.ts ok')
