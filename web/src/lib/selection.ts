// 群组批量选择的纯逻辑：同 bot 锁定 + 一次最多 100 个（与后端 bulk_update 上限一致）。
// 页面只负责把 rejected 原因变成 toast；边界规则集中在这里便于单测。

export const BULK_CHAT_LIMIT = 100

/** SelectionReject 是拒绝加入的原因：limit=已选满；bot=与已选项不同 bot。 */
export type SelectionReject = 'limit' | 'bot'

export interface SelectionOutcome<T> {
  selected: T[]
  rejected?: SelectionReject
}

/**
 * toggleChatSelection 在「同 bot、最多 limit 个」的约束下切换一项：
 * - 已选中 → 取消（永远允许，腾出名额）；
 * - 选满 limit → 拒绝（limit）；
 * - 与首个选中项不同 bot → 拒绝（bot）。
 * 返回新数组；被拒绝时原样返回原数组并带上 rejected，页面据此提示。
 */
export function toggleChatSelection<T>(
  selected: readonly T[],
  item: T,
  isSame: (a: T, b: T) => boolean,
  botOf: (entry: T) => number,
  limit: number = BULK_CHAT_LIMIT,
): SelectionOutcome<T> {
  if (selected.some((entry) => isSame(entry, item))) {
    return { selected: selected.filter((entry) => !isSame(entry, item)) }
  }
  if (selected.length >= limit) {
    return { selected: [...selected], rejected: 'limit' }
  }
  if (selected.length > 0 && botOf(selected[0]) !== botOf(item)) {
    return { selected: [...selected], rejected: 'bot' }
  }
  return { selected: [...selected, item] }
}
