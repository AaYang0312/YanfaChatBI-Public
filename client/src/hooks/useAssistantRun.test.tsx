import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react'
import { useEffect, useState } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import AssistantResultCard from '../components/assistant/AssistantResultCard'
import { numericFixtureReport, numericFixtureRequest } from '../components/assistant/numericFixture'
import { ASSISTANT_RUN_UPDATED, type AnalysisReport, type RunView } from '../types/assistant'
import { zhCN } from '../i18n/zh-CN'
import { pendingBasisOf, useAssistantRun } from './useAssistantRun'
import { installSourceAccess, SourceAccessCoordinator } from '../contexts/SourceAccessContext'
import type { Chat } from '../types/chat'

let authority: SourceAccessCoordinator
let uninstall: (() => void) | undefined
const independentChat: Chat = {
  id: 'chat-1', user_id: 'owner', source_classification: 'independent',
  connection: { type: 'postgresql', host: 'independent.invalid', port: '5432', username: 'reader', database: 'isolated', is_example_db: false },
  settings: { auto_execute_query: false, share_data_with_ai: false, non_tech_mode: false, auto_generate_visualization: false },
  created_at: '', updated_at: '',
}

const getRun = vi.fn()
const createRun = vi.fn()

vi.mock('../services/assistantService', () => ({
  default: {
    getRun: (...args: unknown[]) => getRun(...args),
    createRun: (...args: unknown[]) => createRun(...args),
    resolve: vi.fn(),
    cancelRun: vi.fn(),
  },
}))

vi.mock('../components/ChartRenderer', () => ({
  default: () => <div data-testid="chart-renderer" />,
}))

function view(
  report: AnalysisReport,
  status: RunView['status'] = 'completed',
  revision = 1,
  normalized = numericFixtureRequest(),
): RunView {
  return {
    run_id: report.report_id,
    message_id: `msg-${report.report_id}`,
    request_id: normalized.request_id,
    status,
    stage: status,
    revision,
    report: status === 'completed' ? report : undefined,
    normalized_input: normalized,
  }
}

function Harness() {
  const run = useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' })
  const report = run.displayed?.report
  const [renders, setRenders] = useState(0)
  useEffect(() => {
    if (report) setRenders(count => count + 1)
  }, [report])
  if (!report) return <p>loading</p>
  return (
    <>
      <p data-testid="chart-renders">{renders}</p>
      <AssistantResultCard
        report={report}
        sourceRequest={numericFixtureRequest()}
        pendingBasis={pendingBasisOf(run.displayed, run.pending)}
        onSwitchBasis={(_basis, request) => { void run.switchBasis(request) }}
      />
    </>
  )
}

describe('useAssistantRun', () => {
  beforeEach(() => {
    cleanup()
    getRun.mockReset()
    createRun.mockReset()
    authority = new SourceAccessCoordinator()
    authority.configure('owner', independentChat, [independentChat])
    uninstall = installSourceAccess(authority)
  })
  afterEach(() => { cleanup(); uninstall?.() })

  test('loads the persisted report and ignores financial fields on the SSE notice', async () => {
    getRun.mockResolvedValue(view(numericFixtureReport()))
    render(<Harness />)
    await screen.findByText('48')
    expect(getRun).toHaveBeenCalledTimes(1)
    expect(getRun).toHaveBeenCalledWith('chat-1', 'run-numeric')

    act(() => {
      globalThis.dispatchEvent(new CustomEvent(ASSISTANT_RUN_UPDATED, {
        detail: {
          run_id: 'run-numeric',
          message_id: 'msg-run-numeric',
          revision: 2,
          stage: 'completed',
          status: 'completed',
          sales: '999',
          totals: [{ metric_id: 'sales', value: '999' }],
        },
      }))
    })
    await waitFor(() => expect(getRun).toHaveBeenCalledTimes(1))
    expect(screen.getByText('48')).toBeInTheDocument()
    expect(screen.queryByText('999')).not.toBeInTheDocument()
  })

  test('refetches the same run and does not call the source again when the chart rerenders', async () => {
    const report = numericFixtureReport()
    getRun.mockResolvedValue(view(report))
    const { rerender } = render(<Harness />)
    await screen.findByText('40')
    rerender(<Harness />)
    expect(screen.getByTestId('chart-renders').textContent).not.toBe('0')
    expect(getRun).toHaveBeenCalledTimes(1)
  })

  test('keeps the old card until the new basis run is terminal', async () => {
    const payment = numericFixtureReport()
    const outstock = numericFixtureReport()
    outstock.report_id = 'run-outstock'
    outstock.basis = 'outstock'
    outstock.totals = outstock.totals.map(metric => metric.metric_id === 'sales' ? { ...metric, value: '80' } : metric)
    let outstockDone = false
    getRun.mockImplementation(async (_chatId: string, runId: string) => {
      if (runId === 'run-outstock') return outstockDone ? view(outstock, 'completed', 3) : view(outstock, 'running', 2)
      return view(payment)
    })
    createRun.mockResolvedValue({ run_id: 'run-outstock', message_id: 'msg-outstock', status: 'running' })

    render(<Harness />)
    await screen.findByText(zhCN.assistant.basis.payment)
    fireEvent.click(screen.getByRole('button', { name: zhCN.assistant.card.switchBasis }))

    await screen.findByText(zhCN.assistant.card.loadingNewBasis)
    expect(screen.getByText(zhCN.assistant.basis.payment)).toBeInTheDocument()
    expect(screen.queryByText(zhCN.assistant.basis.outstock)).not.toBeInTheDocument()
    expect(screen.getByText('48')).toBeInTheDocument()
    const body = createRun.mock.calls[0][1] as { request_id: string; basis: string }
    expect(body.basis).toBe('outstock')
    expect(body.request_id).not.toBe('req-numeric')

    outstockDone = true
    act(() => {
      globalThis.dispatchEvent(new CustomEvent(ASSISTANT_RUN_UPDATED, {
        detail: {
          run_id: 'run-outstock',
          message_id: 'msg-outstock',
          revision: 3,
          stage: 'completed',
          status: 'completed',
        },
      }))
    })
    await screen.findByText(zhCN.assistant.basis.outstock)
    expect(screen.queryByText(zhCN.assistant.card.loadingNewBasis)).not.toBeInTheDocument()
  })

  test('does not replace a displayed report with a nonterminal refetch', async () => {
    const payment = numericFixtureReport()
    getRun
      .mockResolvedValueOnce(view(payment))
      .mockResolvedValueOnce(view(payment, 'running', 2))
    const { result } = renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
    await waitFor(() => expect(result.current.displayed?.report?.margin_label).toBe('未扣退款'))
    act(() => {
      globalThis.dispatchEvent(new CustomEvent(ASSISTANT_RUN_UPDATED, {
        detail: {
          run_id: 'run-numeric',
          message_id: 'msg-run-numeric',
          revision: 2,
          stage: 'running',
          status: 'running',
        },
      }))
    })
    await waitFor(() => expect(getRun).toHaveBeenCalledTimes(2))
    expect(result.current.displayed?.status).toBe('completed')
    expect(result.current.displayed?.report?.totals.find(metric => metric.metric_id === 'margin')?.value).toBe('48')
  })

  test('recovers a nonterminal run via authoritative GET without SSE', async () => {
    vi.useFakeTimers()
    try {
      const report = numericFixtureReport()
      getRun
        .mockResolvedValueOnce(view(report, 'running', 1))
        .mockResolvedValueOnce(view(report, 'completed', 2))
      const { result } = renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
      await act(async () => {})
      expect(result.current.displayed?.status).toBe('running')
      expect(result.current.displayed?.report).toBeUndefined()
      await act(async () => {
        vi.advanceTimersByTime(2100)
      })
      expect(result.current.displayed?.report?.margin_label).toBe('未扣退款')
      expect(getRun).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  test('recovers after the initial GET fails', async () => {
    vi.useFakeTimers()
    try {
      const report = numericFixtureReport()
      getRun
        .mockRejectedValueOnce(new Error('network down'))
        .mockResolvedValueOnce(view(report, 'completed', 2))
      const { result } = renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
      await act(async () => {})
      expect(result.current.error).toBeInstanceOf(Error)
      expect(result.current.displayed).toBeNull()
      await act(async () => {
        vi.advanceTimersByTime(2100)
      })
      expect(result.current.displayed?.report?.margin_label).toBe('未扣退款')
      expect(result.current.error).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  test('stops polling once the run is terminal', async () => {
    vi.useFakeTimers()
    try {
      getRun.mockResolvedValue(view(numericFixtureReport(), 'completed', 1))
      renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
      await act(async () => {})
      expect(getRun).toHaveBeenCalledTimes(1)
      await act(async () => {
        vi.advanceTimersByTime(10000)
      })
      expect(getRun).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  test('stops polling after unmount', async () => {
    vi.useFakeTimers()
    try {
      getRun.mockResolvedValue(view(numericFixtureReport(), 'running', 1))
      const { unmount } = renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
      await act(async () => {})
      expect(getRun).toHaveBeenCalledTimes(1)
      unmount()
      await act(async () => {
        vi.advanceTimersByTime(10000)
      })
      expect(getRun).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  test('ignores a late response from a previous chat after remount', async () => {
    let resolveA!: (view: RunView) => void
    const report = numericFixtureReport()
    getRun.mockImplementation((chatId: string) => {
      if (chatId === 'chat-a') return new Promise<RunView>(resolve => { resolveA = resolve })
      return Promise.resolve(view(report))
    })
    const first = renderHook(() => useAssistantRun({ chatId: 'chat-a', runId: 'run-numeric' }))
    first.unmount()
    const second = renderHook(() => useAssistantRun({ chatId: 'chat-b', runId: 'run-numeric' }))
    await act(async () => {})
    expect(second.result.current.displayed?.report?.margin_label).toBe('未扣退款')
    expect(second.result.current.displayed?.revision).toBe(1)
    await act(async () => {
      resolveA(view(report, 'completed', 99))
    })
    expect(second.result.current.displayed?.revision).toBe(1)
  })

  test('surfaces the authoritative GET normalized_input for a reloaded run', async () => {
    const report = numericFixtureReport()
    const authoritative = { ...numericFixtureRequest(), request_id: 'req-authoritative', basis: 'outstock' as const }
    getRun.mockResolvedValue(view(report, 'completed', 1, authoritative))
    const { result } = renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
    await waitFor(() => expect(result.current.displayed?.report?.margin_label).toBe('未扣退款'))
    expect(result.current.displayed?.normalized_input.request_id).toBe('req-authoritative')
    expect(result.current.displayed?.normalized_input.basis).toBe('outstock')
  })
  test('a gate rejection preserves the old report and never starts a basis run', async () => {
    getRun.mockResolvedValue(view(numericFixtureReport()))
    const { result } = renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
    await act(async () => {})
    authority.register({ ...independentChat, source_classification: 'unresolved_erp' })
    await act(async () => { await result.current.switchBasis({ ...numericFixtureRequest(), basis: 'outstock' }) })
    expect(createRun).not.toHaveBeenCalled()
    expect(result.current.displayed?.report?.margin_label).toBe('未扣退款')
    expect(result.current.displayed?.report?.totals.find(metric => metric.metric_id === 'margin')?.value).toBe('48')
    expect(result.current.error).toBeInstanceOf(Error)
  })

  test('a backend race rejection keeps the previous report with no automatic resend', async () => {
    getRun.mockResolvedValue(view(numericFixtureReport()))
    createRun.mockRejectedValue({ response: { status: 423, data: { success: false, error: '数据同步中，暂时无法问数', data: { code: 'DATA_SYNC_IN_PROGRESS' } } } })
    const { result } = renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
    await act(async () => {})
    await act(async () => { await result.current.switchBasis({ ...numericFixtureRequest(), basis: 'outstock' }) })
    expect(result.current.displayed?.report?.margin_label).toBe('未扣退款')
    expect(result.current.pending).toBeNull()
    expect(createRun).toHaveBeenCalledTimes(1)
  })

  test('late create acceptance from an old account cannot replace its displayed report', async () => {
    getRun.mockResolvedValue(view(numericFixtureReport()))
    let finish!: (value: { run_id: string; message_id: string; status: string }) => void
    createRun.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const { result } = renderHook(() => useAssistantRun({ chatId: 'chat-1', runId: 'run-numeric' }))
    await act(async () => {})
    let operation!: Promise<void>
    await act(async () => { operation = result.current.switchBasis({ ...numericFixtureRequest(), basis: 'outstock' }); await Promise.resolve() })
    authority.configure('other-owner', undefined, [])
    await act(async () => { finish({ run_id: 'late', message_id: 'late-message', status: 'created' }); await operation })
    expect(result.current.displayed?.report?.report_id).toBe('run-numeric')
    expect(result.current.pending).toBeNull()
  })
})
