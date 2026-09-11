import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

function restoreProperty(
  target: object,
  key: string,
  descriptor: PropertyDescriptor | undefined,
) {
  if (descriptor) {
    Object.defineProperty(target, key, descriptor)
  } else {
    Reflect.deleteProperty(target, key)
  }
}

describe('copyText', () => {
  let clipboardDescriptor: PropertyDescriptor | undefined
  let execCommandDescriptor: PropertyDescriptor | undefined

  beforeEach(() => {
    clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    execCommandDescriptor = Object.getOwnPropertyDescriptor(document, 'execCommand')
  })

  afterEach(() => {
    restoreProperty(navigator, 'clipboard', clipboardDescriptor)
    restoreProperty(document, 'execCommand', execCommandDescriptor)
  })

  it('优先使用标准 Clipboard API', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })
    const execCommand = vi.fn().mockReturnValue(false)
    Object.defineProperty(document, 'execCommand', {
      configurable: true,
      value: execCommand,
    })

    await expect(copyText('alpha@icloud.com')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('alpha@icloud.com')
    expect(execCommand).not.toHaveBeenCalled()
  })

  it('Clipboard API 被拒绝时回退到传统复制 API', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('Permission denied'))
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })
    const execCommand = vi.fn().mockReturnValue(true)
    Object.defineProperty(document, 'execCommand', {
      configurable: true,
      value: execCommand,
    })

    await expect(copyText('46majesty.bazaars@icloud.com')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('46majesty.bazaars@icloud.com')
    expect(execCommand).toHaveBeenCalledWith('copy')
  })
})
