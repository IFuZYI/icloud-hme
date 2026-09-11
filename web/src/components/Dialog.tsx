import { useEffect, useRef, type ReactNode } from 'react'

interface DialogProps {
  title: string
  open: boolean
  onClose: () => void
  children: ReactNode
}

/** 可访问 Dialog:Escape 关闭、焦点圈定、关闭后回到触发按钮 */
export default function Dialog({ title, open, onClose, children }: DialogProps) {
  const ref = useRef<HTMLDivElement>(null)
  const lastFocused = useRef<Element | null>(null)

  useEffect(() => {
    if (!open) return
    lastFocused.current = document.activeElement
    const node = ref.current
    if (!node) return
    const focusables = () =>
      Array.from(
        node.querySelectorAll<HTMLElement>(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
        ),
      ).filter((el) => !el.hasAttribute('disabled'))
    focusables()[0]?.focus()

    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
        return
      }
      if (e.key !== 'Tab') return
      const items = focusables()
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
  }, [open, onClose])

  if (!open) return null

  return (
    <button
      type="button"
      className="dialog-backdrop"
      aria-label="关闭对话框"
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
    </button>
  )
}
