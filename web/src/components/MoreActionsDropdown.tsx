import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { IconKey, IconMail } from './icons'

export type AccountAction = 'cookies' | 'login' | 'password' | 'mailbox' | 'proxy' | 'aliases' | 'inbox'

type ActionItem = {
  key: AccountAction
  label: string
  icon?: ReactNode
}

const groups: Array<{ title: string; items: ActionItem[] }> = [
  {
    title: '认证',
    items: [
      { key: 'login', label: 'iCloud 登录', icon: <IconKey size={15} /> },
      { key: 'cookies', label: '更新 Cookie', icon: <IconKey size={15} /> },
      { key: 'password', label: 'App 专用密码', icon: <IconKey size={15} /> },
    ],
  },
  {
    title: '配置',
    items: [
      { key: 'mailbox', label: '接入收件邮箱', icon: <IconMail size={15} /> },
      { key: 'proxy', label: '网络代理' },
      { key: 'aliases', label: '别名管理' },
      { key: 'inbox', label: '收件箱', icon: <IconMail size={15} /> },
    ],
  },
]

interface MoreActionsDropdownProps {
  accountName: string
  onAction: (action: AccountAction) => void
  disabled?: boolean
}

export default function MoreActionsDropdown({ accountName, onAction, disabled = false }: MoreActionsDropdownProps) {
  const [open, setOpen] = useState(false)
  const [position, setPosition] = useState({ top: 0, left: 0 })
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuId = `account-actions-${useId()}`

  const updatePosition = useCallback(() => {
    const button = buttonRef.current
    if (!button) return
    const rect = button.getBoundingClientRect()
    const width = menuRef.current?.offsetWidth ?? 208
    const height = menuRef.current?.offsetHeight ?? 300
    const top = rect.bottom + 6 + height <= window.innerHeight - 8
      ? rect.bottom + 6
      : Math.max(8, rect.top - height - 6)
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

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        className="account-more-button"
        aria-haspopup="menu"
        aria-controls={menuId}
        aria-expanded={open}
        aria-label={`${accountName} 更多操作`}
        disabled={disabled}
        onClick={() => setOpen((value) => !value)}
      />
      {open && createPortal(
        <div
          id={menuId}
          ref={menuRef}
          className="account-more-popover"
          role="menu"
          tabIndex={-1}
          aria-label={`${accountName} 更多操作`}
          style={{ top: position.top, left: position.left }}
          onKeyDown={handleKeyDown}
        >
          {groups.map((group, index) => (
            <div className="account-action-group" key={group.title}>
              {index > 0 && <div className="account-action-divider" role="separator" />}
              <p>{group.title}</p>
              {group.items.map((item) => (
                <button
                  type="button"
                  key={item.key}
                  role="menuitem"
                  onClick={() => { setOpen(false); onAction(item.key) }}
                >
                  {item.icon}<span>{item.label}</span>
                </button>
              ))}
            </div>
          ))}
        </div>,
        document.body,
      )}
    </>
  )
}
