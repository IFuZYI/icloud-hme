import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'

interface Props { onToggle: () => void; onEdit: () => void; onDelete: () => void; enabled: boolean; disabled?: boolean }

type Position = { top: number; left: number }

export default function TaskMoreMenu({ onToggle, onEdit, onDelete, enabled, disabled = false }: Props) {
  const [open, setOpen] = useState(false)
  const [position, setPosition] = useState<Position>({ top: 0, left: 0 })
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuId = `task-menu-${useId()}`

  const updatePosition = useCallback(() => {
    const button = buttonRef.current
    if (!button) return
    const rect = button.getBoundingClientRect()
    const menuWidth = menuRef.current?.offsetWidth ?? 128
    const menuHeight = menuRef.current?.offsetHeight ?? 140
    const gap = 6
    const below = rect.bottom + gap
    const top = below + menuHeight <= window.innerHeight - 8 ? below : Math.max(8, rect.top - menuHeight - gap)
    const left = Math.min(Math.max(8, rect.right - menuWidth), window.innerWidth - menuWidth - 8)
    setPosition({ top, left })
  }, [])

  useEffect(() => {
    if (!open) return
    updatePosition()
    const closeOnOutsidePointer = (event: PointerEvent) => {
      const target = event.target as Node
      if (!buttonRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false)
    }
    const closeOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false)
        buttonRef.current?.focus()
      }
    }
    document.addEventListener('pointerdown', closeOnOutsidePointer)
    document.addEventListener('keydown', closeOnEscape)
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    menuRef.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus()
    return () => {
      document.removeEventListener('pointerdown', closeOnOutsidePointer)
      document.removeEventListener('keydown', closeOnEscape)
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [open, updatePosition])

  function handleMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? [])
    const index = items.indexOf(document.activeElement as HTMLButtonElement)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      items[(index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
    } else if (event.key === 'Home') { event.preventDefault(); items[0]?.focus()
    } else if (event.key === 'End') { event.preventDefault(); items[items.length - 1]?.focus()
    } else if (event.key === 'Tab') setOpen(false)
  }

  function choose(action: () => void) { setOpen(false); action() }

  const menu = open ? createPortal(
    <div id={menuId} ref={menuRef} className="task-more-popover" role="menu" tabIndex={-1} aria-label="任务操作" style={{ top: position.top, left: position.left }} onKeyDown={handleMenuKeyDown}>
      <button type="button" role="menuitem" onClick={() => choose(onToggle)}>{enabled ? '暂停' : '启用'}</button>
      <button type="button" role="menuitem" onClick={() => choose(onEdit)}>编辑</button>
      <button type="button" role="menuitem" className="danger" onClick={() => choose(onDelete)}>删除</button>
    </div>, document.body,
  ) : null

  return <>
    <button ref={buttonRef} type="button" className="task-more-button" aria-haspopup="menu" aria-controls={menuId} aria-expanded={open} disabled={disabled} onClick={() => setOpen((value) => !value)}>更多 <span aria-hidden="true">⌄</span></button>
    {menu}
  </>
}
