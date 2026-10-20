// 书源的 kind 规则普遍是逗号拼接的多段，例如聚合源的
// `{{$.status}},{{$.score}},{{$.tags}},{{$.last_chapter_update_time}}`。
//
// 取不到值的段会是空串（如 "已完结,,,"），直接展示会变成 ",,有声小说," 这种噪音，
// 所以按逗号切分后丢掉空段，只保留有效标签（legado 也是按段展示）。
export function splitKindTags(kind?: string | null): string[] {
  return (kind ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}
