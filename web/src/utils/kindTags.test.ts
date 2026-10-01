import { splitKindTags } from './kindTags.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`kindTags: ${name}`)
}

const eq = (a: string[], b: string[]) => a.length === b.length && a.every((v, i) => v === b[i])

check('空值返回空数组', splitKindTags(undefined).length === 0)
check('空串返回空数组', splitKindTags('').length === 0)
check('全空段返回空数组', splitKindTags(',,,').length === 0)
// 聚合源的真实取值：只有状态/分类有值，其余为空
check('丢掉空段', eq(splitKindTags('已完结,,,'), ['已完结']))
check('保留有效段顺序', eq(splitKindTags('已完结,玄幻,'), ['已完结', '玄幻']))
check('去掉段内空白', eq(splitKindTags(' 连载 , 都市 '), ['连载', '都市']))
check('无逗号时整体作一段', eq(splitKindTags('有声小说'), ['有声小说']))
// 更新时间段也是合法标签（legado 同样展示）
check('保留更新时间段', eq(splitKindTags(',,有声小说,2018-12-11 22:01:22'), ['有声小说', '2018-12-11 22:01:22']))

console.log('kindTags.test.ts ok')
