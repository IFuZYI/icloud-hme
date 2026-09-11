import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import type { ReactNode } from 'react'

export interface MenuItem { key: string; label: string; danger?: boolean; icon?: ReactNode }

interface Props {
  items: MenuItem[]
  onSelect: (key: string) => void
  disabled?: boolean
  ariaLabel?: string
}

export default function RowActionMenu({ items, onSelect, disabled = false, ariaLabel = '更多操作' }: Props) {
  const [open, setOpen] = useState(false)
  const [position, setPosition] = useState({ top: 0, left: 0 })
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuId = useRef(`menu-${Math.random().toString(36).slice(2)}`).current

  const updatePosition = useCallback(() => {
    const button = buttonRef.current
    if (!button) return
    const rect = button.getBoundingClientRect()
    const width = menuRef.current?.offsetWidth ?? 144
    const height = menuRef.current?.offsetHeight ?? 132
    const gap = 6
    const below = rect.bottom + gap
    const top = below + height <= window.innerHeight - 8 ? below : Math.max(8, rect.top - height - gap)
    const left = Math.min(Math.max(8, rect.right - width), window.innerWidth - width - 8)
    setPosition({ top, left })
  }, [])

  useEffect(() => {
    if (!open) return
    updatePosition()
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node
      if (!buttonRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false)
    }
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') { setOpen(false); buttonRef.current?.focus() }
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    menuRef.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus()
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [open, updatePosition])

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const buttons = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? [])
    const index = buttons.indexOf(document.activeElement as HTMLButtonElement)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      buttons[(index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length]?.focus()
    } else if (event.key === 'Home') { event.preventDefault(); buttons[0]?.focus() }
    else if (event.key === 'End') { event.preventDefault(); buttons[buttons.length - 1]?.focus() }
    else if (event.key === 'Tab') setOpen(false)
  }

  function choose(key: string) { setOpen(false); onSelect(key) }

  const menu = open ? createPortal(
    <div id={menuId} ref={menuRef} className="row-action-popover" role="menu" aria-label={ariaLabel} style={{ top: position.top, left: position.left }} onKeyDown={handleKeyDown}>
      {items.map((item) => (
        <button key={item.key} type="button" role="menuitem" className={item.danger ? 'danger' : ''} onClick={() => choose(item.key)}>
          {item.icon}{item.label}
        </button>
      ))}
    </div>, document.body,
  ) : null

  return <>
    <button ref={buttonRef} type="button" className="row-action-button" aria-haspopup="menu" aria-controls={menuId} aria-expanded={open} aria-label={ariaLabel} disabled={disabled} onClick={() => setOpen((value) => !value)} />
    {menu}
  </>
}
