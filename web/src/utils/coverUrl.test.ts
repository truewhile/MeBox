import { isRenderableCover } from './coverUrl.ts'

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

console.log('coverUrl.test.ts ok')
