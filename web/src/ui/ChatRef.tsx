import { Link } from '@mui/material'

export interface ChatRefProps {
  /** 群展示名（后端 LookupChatRef 的 Display：群名，未知时为 chat_id）。 */
  name?: string | number | null
  /** 公开群的 t.me 链接；私有群或未知时为空，此时只渲染纯文本。 */
  url?: string | null
  /** name 与 url 都缺省（或 name 为空）时的回退文本。 */
  fallback?: string | number | null
}

/**
 * ChatRef 渲染一个群的引用：公开群是带 t.me 链接的群名，私有群只有群名。
 * 凡是展示 chat_id 的地方都改用它，让群引用对人可读、可点。
 */
export function ChatRef({ name, url, fallback }: ChatRefProps) {
  const text =
    name === undefined || name === null || name === ''
      ? fallback === undefined || fallback === null
        ? ''
        : String(fallback)
      : String(name)
  if (!text) return null
  if (!url) return <>{text}</>
  return (
    <Link href={url} target="_blank" rel="noopener noreferrer" color="inherit" underline="hover">
      {text}
    </Link>
  )
}
