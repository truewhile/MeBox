import { isRenderableCover, needsCoverProxy } from './coverUrl.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`coverUrl: ${name}`)
}

check('empty is not renderable', !isRenderableCover(''))
check('null is not renderable', !isRenderableCover(null))
check('undefined is not renderable', !isRenderableCover(undefined))
check('blank string is not renderable', !isRenderableCover('   '))
check('https cover is renderable', isRenderableCover('https://img.example.com/a.jpg'))
check('http cover is renderable', isRenderableCover('http://img.example.com/a.jpg'))
check('leading spaces are tolerated', isRenderableCover('  https://img.example.com/a.jpg  '))
check('uppercase scheme is renderable', isRenderableCover('HTTPS://img.example.com/a.jpg'))
check('inline image data uri is renderable', isRenderableCover('data:image/png;base64,iVBORw0KGgo='))
check('local asset path is renderable', isRenderableCover('/api/reader/local/asset?b=1&p=2&s=3'))
check('protocol relative cover is renderable', isRenderableCover('//img.example.com/a.jpg'))
// 聚合书源把搜索参数信封当封面返回的真实取值
check(
  'search envelope data uri is not renderable',
  !isRenderableCover(
    'data:;base64,eyJrZXkiOiLlhajnkIPpq5jmraYiLCJ0YWIiOiLlsI/or7QiLCJzb3VyY2VzS2V5Ijoi5YWo6YOoIiwicGFnZSI6MSwiZGlzYWJsZWRfc291cmNlcyI6IjAifQ==',
  ),
)
check('text data uri is not renderable', !isRenderableCover('data:text/plain;base64,aGk='))
check('source name is not renderable', !isRenderableCover('光遇聚合'))
check('bare relative path is not renderable', !isRenderableCover('files/a.jpg'))
check('javascript url is not renderable', !isRenderableCover('javascript:alert(1)'))

// needsCoverProxy：只有明文 http 的远端封面需要绕后端代理，否则 https 部署下
// 会被混合内容策略拦掉（书源图床酷我等没有 https，浏览器升级请求必然失败）。
check('http cover needs proxy', needsCoverProxy('http://img4.sycdn.kuwo.cn/star/albumcover/240/a.jpg'))
check('uppercase http cover needs proxy', needsCoverProxy('HTTP://img4.sycdn.kuwo.cn/a.jpg'))
check('padded http cover needs proxy', needsCoverProxy('  http://img4.sycdn.kuwo.cn/a.jpg  '))
check('https cover needs no proxy', !needsCoverProxy('https://imagev2.xmcdn.com/a.jpg'))
check('protocol relative cover needs no proxy', !needsCoverProxy('//img.example.com/a.jpg'))
check('local asset path needs no proxy', !needsCoverProxy('/api/reader/local/asset?b=1&p=2&s=3'))
check('data uri needs no proxy', !needsCoverProxy('data:image/png;base64,iVBORw0KGgo='))
check('empty needs no proxy', !needsCoverProxy(''))
check('name that merely contains http needs no proxy', !needsCoverProxy('http 书籍'))

console.log('coverUrl.test.ts ok')
