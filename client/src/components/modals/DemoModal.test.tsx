import { StrictMode, useState } from 'react'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { DemoModal } from './DemoModal'
import { zhCN } from '../../i18n/zh-CN'

function HelpHarness() {
  const [isOpen, setIsOpen] = useState(false)
  return (
    <>
      <button type="button" onClick={() => setIsOpen(true)}>{zhCN.sidebar.howTo}</button>
      <DemoModal isOpen={isOpen} onClose={() => setIsOpen(false)} />
    </>
  )
}

describe('explicit tutorial load boundaries', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  test.each(['error', 'timeout'] as const)('%s stays terminal until a manual retry and rejects old frame events', (failure) => {
    render(<StrictMode><HelpHarness /></StrictMode>)
    fireEvent.click(screen.getByRole('button', { name: zhCN.sidebar.howTo }))
    const failedFrame = screen.getByTitle(zhCN.demo.title)

    if (failure === 'error') {
      fireEvent.error(failedFrame)
    } else {
      act(() => { vi.advanceTimersByTime(14_999) })
      expect(screen.getByRole('status')).toBeInTheDocument()
      act(() => { vi.advanceTimersByTime(1) })
    }

    const feedback = screen.getByRole('alert')
    expect(feedback).toHaveTextContent(/[\u4e00-\u9fff]/)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: zhCN.common.retry })).toBeEnabled()
    act(() => { vi.advanceTimersByTime(60_000) })
    fireEvent.load(failedFrame)
    fireEvent.error(failedFrame)
    expect(screen.getByRole('alert')).toBe(feedback)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: zhCN.common.retry })).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: zhCN.common.retry }))
    const retryFrame = screen.getByTitle(zhCN.demo.title)
    expect(retryFrame).not.toBe(failedFrame)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toBeInTheDocument()

    fireEvent.load(failedFrame)
    fireEvent.error(failedFrame)
    expect(screen.getByRole('status')).toBeInTheDocument()
    fireEvent.load(retryFrame)
    act(() => { vi.advanceTimersByTime(15_000) })
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: zhCN.common.retry })).toBeEnabled()
    expect(screen.getByTitle(zhCN.demo.title)).toBe(retryFrame)
  })

  test('closing a pending load returns focus and late results cannot settle a reopened help session', () => {
    const previousOverflow = document.body.style.overflow
    render(<StrictMode><HelpHarness /></StrictMode>)
    const opener = screen.getByRole('button', { name: zhCN.sidebar.howTo })
    opener.focus()
    fireEvent.click(opener)
    const closedFrame = screen.getByTitle(zhCN.demo.title)
    expect(screen.getByRole('button', { name: zhCN.common.close })).toHaveFocus()
    act(() => { vi.advanceTimersByTime(5_000) })

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(opener).toHaveFocus()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(document.body.style.overflow).toBe(previousOverflow)
    fireEvent.load(closedFrame)
    fireEvent.error(closedFrame)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()

    act(() => { vi.advanceTimersByTime(2_000) })
    fireEvent.click(opener)
    const reopenedFrame = screen.getByTitle(zhCN.demo.title)
    expect(reopenedFrame).not.toBe(closedFrame)
    act(() => { vi.advanceTimersByTime(8_001) })
    fireEvent.load(closedFrame)
    fireEvent.error(closedFrame)
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByTitle(zhCN.demo.title)).toBe(reopenedFrame)
    expect(screen.getByRole('button', { name: zhCN.common.close })).toHaveFocus()
    act(() => { vi.advanceTimersByTime(6_998) })
    expect(screen.getByRole('status')).toBeInTheDocument()

    fireEvent.load(reopenedFrame)
    act(() => { vi.advanceTimersByTime(15_000) })
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: zhCN.common.retry })).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: zhCN.common.close }))
    act(() => { vi.advanceTimersByTime(60_000) })
    fireEvent.load(reopenedFrame)
    fireEvent.error(reopenedFrame)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
    expect(document.body.style.overflow).toBe(previousOverflow)
  })

  test('a loaded frame can be manually retried and reopening after failure starts a fresh deadline', () => {
    render(<StrictMode><HelpHarness /></StrictMode>)
    const opener = screen.getByRole('button', { name: zhCN.sidebar.howTo })
    opener.focus()
    fireEvent.click(opener)
    const loadedFrame = screen.getByTitle(zhCN.demo.title)
    fireEvent.load(loadedFrame)
    act(() => { vi.advanceTimersByTime(30_000) })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: zhCN.common.retry }))
    expect(screen.getByRole('button', { name: zhCN.common.close })).toHaveFocus()
    const retryFrame = screen.getByTitle(zhCN.demo.title)
    expect(retryFrame).not.toBe(loadedFrame)
    fireEvent.load(loadedFrame)
    expect(screen.getByRole('status')).toBeInTheDocument()
    fireEvent.error(retryFrame)
    expect(screen.getByRole('alert')).toHaveTextContent(/[\u4e00-\u9fff]/)

    fireEvent.click(screen.getByRole('button', { name: zhCN.common.close }))
    expect(opener).toHaveFocus()
    fireEvent.click(opener)
    const reopenedFrame = screen.getByTitle(zhCN.demo.title)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    fireEvent.load(retryFrame)
    fireEvent.error(loadedFrame)
    act(() => { vi.advanceTimersByTime(14_999) })
    expect(screen.getByRole('status')).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(1) })
    expect(screen.getByRole('alert')).toHaveTextContent(/[\u4e00-\u9fff]/)
    fireEvent.load(reopenedFrame)
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: zhCN.common.retry })).toBeEnabled()
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(screen.getByRole('alert')).toHaveTextContent(/[\u4e00-\u9fff]/)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: zhCN.common.retry })).toBeEnabled()
  })
})
