import { useEffect, useEffectEvent, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

interface DialogProps {
  title: string
  open: boolean
  onClose: () => void
  children: ReactNode
}

/**
 * 当前打开的弹层栈(按打开顺序)。用于保证:
 *  - 只有最上层响应 Escape, 否则叠放的确认框会连带关掉底下的详情弹层;
 *  - 只有最上层圈定焦点。
 */
const openStack: symbol[] = []

/** 可聚焦元素(排除 disabled)。 */
function focusablesIn(node: HTMLElement): HTMLElement[] {
  return Array.from(
    node.querySelectorAll<HTMLElement>(
      'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((el) => !el.hasAttribute('disabled'))
}

/** 可访问 Dialog:Escape 关闭、焦点圈定、关闭后回到触发按钮 */
export default function Dialog({ title, open, onClose, children }: DialogProps) {
  const ref = useRef<HTMLDivElement>(null)
  const lastFocused = useRef<Element | null>(null)
  const idRef = useRef<symbol>(Symbol('dialog'))
  const closeDialog = useEffectEvent(() => onClose())

  const isTopmost = () => openStack[openStack.length - 1] === idRef.current

  // 入栈/出栈: 记录打开顺序
  useEffect(() => {
    if (!open) return
    const id = idRef.current
    openStack.push(id)
    return () => {
      const idx = openStack.lastIndexOf(id)
      if (idx >= 0) openStack.splice(idx, 1)
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    lastFocused.current = document.activeElement

    const handleKey = (e: KeyboardEvent) => {
      // 叠放的弹层里只有最上层响应 Esc / Tab
      if (!isTopmost()) return
      if (e.key === 'Escape') {
        closeDialog()
        return
      }
      if (e.key !== 'Tab') return
      const node = ref.current
      if (!node) return
      const items = focusablesIn(node)
      if (items.length === 0) return
      const first = items[0]
      const last = items[items.length - 1]
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', handleKey)
    return () => {
      document.removeEventListener('keydown', handleKey)
      if (lastFocused.current instanceof HTMLElement) {
        lastFocused.current.focus()
      }
    }
  }, [open])

  // 焦点必须真的落进弹层。弹层可能先渲染"读取中…"(此时没有任何可聚焦元素),
  // 内容后到, 所以每次渲染都检查一次: 只要焦点还在弹层外就拉回来。
  useEffect(() => {
    if (!open) return
    const node = ref.current
    if (!node) return
    if (node.contains(document.activeElement)) return
    focusablesIn(node)[0]?.focus()
  })

  if (!open) return null

  return createPortal(
    <div
      className="dialog-backdrop"
      role="presentation"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={ref}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <h3>{title}</h3>
        {children}
      </div>
    </div>,
    document.body,
  )
}
