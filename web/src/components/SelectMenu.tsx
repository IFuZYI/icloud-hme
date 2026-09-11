import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { IconChevronDown, IconCheck } from './icons'

export interface SelectOption { value: string; label: string }

interface Props {
  value: string
  options: SelectOption[]
  onChange: (value: string) => void
  ariaLabel: string
  className?: string
  ghost?: boolean
}

export default function SelectMenu({ value, options, onChange, ariaLabel, className = '', ghost = false }: Props) {
  const [open, setOpen] = useState(false)
  const [position, setPosition] = useState({ top: 0, left: 0 })
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const listId = `select-${useId()}`
  const selected = options.find((option) => option.value === value)

  const updatePosition = useCallback(() => {
    const trigger = triggerRef.current
    if (!trigger) return
    const rect = trigger.getBoundingClientRect()
    const menuWidth = menuRef.current?.offsetWidth ?? Math.max(160, rect.width)
    const menuHeight = menuRef.current?.offsetHeight ?? 140
    const gap = 6
    const below = rect.bottom + gap
    const top = below + menuHeight <= window.innerHeight - 8 ? below : Math.max(8, rect.top - menuHeight - gap)
    const left = Math.min(Math.max(8, rect.left), window.innerWidth - menuWidth - 8)
    setPosition({ top, left })
  }, [])

  useEffect(() => {
    if (!open) return
    updatePosition()
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node
      if (!triggerRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false)
    }
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') { setOpen(false); triggerRef.current?.focus() }
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    menuRef.current?.querySelector<HTMLButtonElement>('[role="option"][aria-selected="true"]')?.focus()
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [open, updatePosition])

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]') ?? [])
    const index = items.indexOf(document.activeElement as HTMLButtonElement)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      items[(index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
    } else if (event.key === 'Home') { event.preventDefault(); items[0]?.focus() }
    else if (event.key === 'End') { event.preventDefault(); items[items.length - 1]?.focus() }
    else if (event.key === 'Tab') setOpen(false)
  }

  const menu = open ? createPortal(
    <div id={listId} ref={menuRef} className="select-menu-popover" role="listbox" tabIndex={-1} aria-label={ariaLabel} style={{ top: position.top, left: position.left }} onKeyDown={handleKeyDown}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="option"
          aria-selected={option.value === value}
          className={`select-menu-item ${option.value === value ? 'is-selected' : ''}`}
          onClick={() => { setOpen(false); onChange(option.value) }}
        >
          <span className="select-menu-check">{option.value === value ? <IconCheck size={14} /> : null}</span>
          <span className="select-menu-label">{option.label}</span>
        </button>
      ))}
    </div>, document.body,
  ) : null

  return (
    <div className={`select-menu ${className}`}>
      <button
        ref={triggerRef}
        type="button"
        className={`select-trigger ${ghost ? 'is-ghost' : ''}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={ariaLabel}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="select-trigger-label">{selected?.label ?? value}</span>
        <IconChevronDown size={14} className="select-trigger-chevron" aria-hidden="true" />
      </button>
      {menu}
    </div>
  )
}
