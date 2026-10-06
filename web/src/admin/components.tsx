// 桌面端专用展示组件：页面头、工具条、数据表、指标卡、确认按钮、键值列表。
//
// 这套是给管理面板单独写的 UI，不复用 Mini App 的 ListRow/TopBar/TabBar 等
// 移动形态；语义色标（Badge）与错误/空态是通用件，直接复用 ui/。
import {
  Box,
  Button,
  Card,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material'
import type { ButtonProps } from '@mui/material'
import type { SxProps, Theme } from '@mui/material/styles'
import type { ReactNode } from 'react'
import { useState } from 'react'
import { Badge } from '../ui'
import type { StatusInfo } from '../lib/status'

/** PageHeader 页面标题 + 说明 + 右侧操作区。 */
export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
}) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 2, mb: 2 }}>
      <Box sx={{ minWidth: 0 }}>
        <Typography sx={{ fontSize: 20, fontWeight: 700, lineHeight: 1.3 }}>{title}</Typography>
        {subtitle !== undefined && (
          <Typography sx={{ mt: 0.25, fontSize: 13, color: 'text.secondary' }}>
            {subtitle}
          </Typography>
        )}
      </Box>
      {actions !== undefined && (
        <Stack direction="row" spacing={1} sx={{ ml: 'auto', flexShrink: 0 }}>
          {actions}
        </Stack>
      )}
    </Box>
  )
}

/** Toolbar 一行筛选控件（搜索框、分段、按钮），自动换行。 */
export function Toolbar({ children }: { children: ReactNode }) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, flexWrap: 'wrap', mb: 1.5 }}>
      {children}
    </Box>
  )
}

/** StatCard 指标卡；onClick 存在时整卡可点。 */
export function StatCard({
  label,
  value,
  hint,
  onClick,
}: {
  label: ReactNode
  value: ReactNode
  hint?: ReactNode
  onClick?: () => void
}) {
  return (
    <Card
      elevation={0}
      onClick={onClick}
      sx={{
        p: 2,
        borderRadius: 2,
        border: '1px solid',
        borderColor: 'divider',
        cursor: onClick ? 'pointer' : 'default',
        '&:hover': onClick ? { borderColor: 'primary.main' } : undefined,
      }}
    >
      <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>{label}</Typography>
      <Typography sx={{ mt: 0.5, fontSize: 24, fontWeight: 700, lineHeight: 1.2 }}>{value}</Typography>
      {hint !== undefined && (
        <Typography sx={{ mt: 0.5, fontSize: 12.5, color: 'text.secondary' }}>{hint}</Typography>
      )}
    </Card>
  )
}

export interface Column<T> {
  key: string
  header: ReactNode
  render: (row: T) => ReactNode
  width?: number | string
  align?: 'left' | 'center' | 'right'
}

export interface DataTableProps<T> {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string | number
  onRowClick?: (row: T) => void
  loading?: boolean
  /** 空态节点；缺省是一句「暂无数据」。 */
  empty?: ReactNode
}

/** DataTable 桌面表格：表头吸顶、行可点、加载与空态内置。 */
export function DataTable<T>({ columns, rows, rowKey, onRowClick, loading, empty }: DataTableProps<T>) {
  if (loading && rows.length === 0) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
        <CircularProgress size={26} />
      </Box>
    )
  }
  if (rows.length === 0) {
    return <>{empty ?? <EmptyRow text="暂无数据" />}</>
  }
  return (
    <TableContainer
      component={Card}
      elevation={0}
      sx={{ borderRadius: 2, border: '1px solid', borderColor: 'divider' }}
    >
      <Table stickyHeader size="small">
        <TableHead>
          <TableRow>
            {columns.map((col) => (
              <TableCell key={col.key} align={col.align ?? 'left'} sx={{ width: col.width, whiteSpace: 'nowrap' }}>
                {col.header}
              </TableCell>
            ))}
          </TableRow>
        </TableHead>
        <TableBody>
          {rows.map((row) => (
            <TableRow
              key={rowKey(row)}
              hover={onRowClick !== undefined}
              onClick={onRowClick ? () => onRowClick(row) : undefined}
              sx={onRowClick ? { cursor: 'pointer' } : undefined}
            >
              {columns.map((col) => (
                <TableCell key={col.key} align={col.align ?? 'left'} sx={{ maxWidth: 420 }}>
                  {col.render(row)}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  )
}

/** EmptyRow 表格内的空态。 */
export function EmptyRow({ text }: { text: ReactNode }) {
  return (
    <Card
      elevation={0}
      sx={{ p: 4, textAlign: 'center', borderRadius: 2, border: '1px solid', borderColor: 'divider' }}
    >
      <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>{text}</Typography>
    </Card>
  )
}

/** StatusBadge 把 lib/status 的 StatusInfo 渲染成彩色徽标。 */
export function StatusBadge({ info }: { info: StatusInfo }) {
  return <Badge tone={info.tone}>{info.label}</Badge>
}

/** ConfirmButton 需要二次确认的危险操作：点击先弹确认框，确认后才执行。 */
export function ConfirmButton({
  title,
  description,
  confirmLabel = '确认',
  cancelLabel = '取消',
  danger = false,
  disabled = false,
  onConfirm,
  children,
  ...buttonProps
}: {
  title: ReactNode
  description?: ReactNode
  confirmLabel?: ReactNode
  cancelLabel?: ReactNode
  danger?: boolean
  disabled?: boolean
  onConfirm: () => void
  children: ReactNode
} & Omit<ButtonProps, 'onClick' | 'children'>) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button {...buttonProps} disabled={disabled} onClick={() => setOpen(true)}>
        {children}
      </Button>
      <Dialog open={open} onClose={() => setOpen(false)} maxWidth="xs" fullWidth>
        <DialogTitle sx={{ fontSize: 17 }}>{title}</DialogTitle>
        {description !== undefined && (
          <DialogContent>
            <DialogContentText sx={{ fontSize: 14, lineHeight: 1.7 }}>{description}</DialogContentText>
          </DialogContent>
        )}
        <DialogActions>
          <Button onClick={() => setOpen(false)}>{cancelLabel}</Button>
          <Button
            color={danger ? 'error' : 'primary'}
            variant="contained"
            onClick={() => {
              setOpen(false)
              onConfirm()
            }}
          >
            {confirmLabel}
          </Button>
        </DialogActions>
      </Dialog>
    </>
  )
}

/** InfoList 键值信息块（详情页通用）。 */
export function InfoList({ items }: { items: { label: ReactNode; value: ReactNode }[] }) {
  return (
    <Box
      sx={{
        display: 'grid',
        gridTemplateColumns: { xs: '1fr', sm: 'auto 1fr' },
        columnGap: 2,
        rowGap: 1,
        alignItems: 'baseline',
      }}
    >
      {items.map((item, index) => (
        <Box key={index} sx={{ display: 'contents' }}>
          <Typography sx={{ fontSize: 13, color: 'text.secondary', whiteSpace: 'nowrap' }}>
            {item.label}
          </Typography>
          <Typography sx={{ fontSize: 13.5, wordBreak: 'break-word' }}>{item.value}</Typography>
        </Box>
      ))}
    </Box>
  )
}

/** CardBlock 一块带标题的内容卡片。 */
export function CardBlock({
  title,
  actions,
  children,
  sx,
}: {
  title?: ReactNode
  actions?: ReactNode
  children: ReactNode
  sx?: SxProps<Theme>
}) {
  return (
    <Card
      elevation={0}
      sx={[{ p: 2, mb: 2, borderRadius: 2, border: '1px solid', borderColor: 'divider' }, ...(Array.isArray(sx) ? sx : [sx])]}
    >
      {(title !== undefined || actions !== undefined) && (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1.5 }}>
          {title !== undefined && (
            <Typography sx={{ fontSize: 15, fontWeight: 600 }}>{title}</Typography>
          )}
          {actions !== undefined && <Box sx={{ ml: 'auto' }}>{actions}</Box>}
        </Box>
      )}
      {children}
    </Card>
  )
}

/** MonoText 正文/正则等等宽展示，长文可换行。 */
export function MonoText({ children }: { children: ReactNode }) {
  return (
    <Typography
      component="span"
      sx={{
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        fontSize: 12.5,
        wordBreak: 'break-all',
      }}
    >
      {children}
    </Typography>
  )
}
