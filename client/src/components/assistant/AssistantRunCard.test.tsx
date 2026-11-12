import { act, cleanup, fireEvent, render as renderView, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import AssistantRunCard from './AssistantRunCard'
import { numericFixtureReport, numericFixtureRequest } from './numericFixture'
import { zhCN } from '../../i18n/zh-CN'
import { ASSISTANT_RUN_UPDATED, type AnalysisReport, type AnalysisRequest, type RunView } from '../../types/assistant'
import type { PropsWithChildren, ReactElement } from 'react'
import { installSourceAccess, SourceAccessContext, SourceAccessCoordinator } from '../../contexts/SourceAccessContext'
import type { Chat } from '../../types/chat'

let authority: SourceAccessCoordinator
let uninstall: (() => void) | undefined
const chat: Chat = {
  id: 'chat-1', user_id: 'owner', source_classification: 'independent',
  connection: { type: 'postgresql', host: 'independent.invalid', port: '5432', username: 'reader', database: 'isolated', is_example_db: false },
  settings: { auto_execute_query: false, share_data_with_ai: false, non_tech_mode: false, auto_generate_visualization: false },
  created_at: '', updated_at: '',
}
const wrapper = ({ children }: PropsWithChildren) => <SourceAccessContext.Provider value={authority}>{children}</SourceAccessContext.Provider>
const render = (ui: ReactElement) => renderView(ui, { wrapper })

const getRun = vi.fn()
const createRun = vi.fn()

vi.mock('../../services/assistantService', () => ({
  default: {
    getRun: (...args: unknown[]) => getRun(...args),
    createRun: (...args: unknown[]) => createRun(...args),
    resolve: vi.fn(),
    cancelRun: vi.fn(),
  },
}))

vi.mock('../ChartRenderer', () => ({
  default: ({ data }: { data: Record<string, string | null>[] }) => (
    <div data-testid="chart-renderer">{JSON.stringify(data)}</div>
  ),
}))

function view(report: AnalysisReport): RunView {
  return {
    run_id: report.report_id,
    message_id: 'msg-run-numeric',
    request_id: 'req-numeric',
    status: 'completed',
    stage: 'completed',
    revision: 1,
    report,
    normalized_input: numericFixtureRequest(),
  }
}

describe('assistant run card', () => {
  beforeEach(() => {
    getRun.mockReset(); createRun.mockReset()
    authority = new SourceAccessCoordinator()
    authority.configure('owner', chat, [chat])
    uninstall = installSourceAccess(authority)
  })
  afterEach(() => { cleanup(); uninstall?.() })

  test('renders the persisted report for a message run id with one source read', async () => {
    getRun.mockResolvedValue(view(numericFixtureReport()))
    const { rerender } = render(<AssistantRunCard chatId="chat-1" runId="run-numeric" />)
    expect(await screen.findByText('48')).toBeInTheDocument()
    expect(screen.getByText('40')).toBeInTheDocument()
    expect(screen.getAllByText('未扣退款').length).toBeGreaterThan(0)
    rerender(<AssistantRunCard chatId="chat-1" runId="run-numeric" />)
    expect(getRun).toHaveBeenCalledTimes(1)
  })

  test.each([
    ['failed', '失败', 'alert'],
    ['cancelled', '已取消', 'status'],
    ['interrupted', '已中断', 'status'],
  ] as const)('shows report-less %s as a terminal state', async (status, label, role) => {
    getRun.mockResolvedValue({ ...view(numericFixtureReport()), status, report: undefined })
    render(<AssistantRunCard chatId="chat-1" runId="run-numeric" />)
    expect(await screen.findByRole(role)).toHaveTextContent(label)
    expect(screen.queryByText(/加载中/)).not.toBeInTheDocument()
  })

  test('shows a safe Chinese load error instead of waiting forever', async () => {
    getRun.mockRejectedValue(new Error('upstream token=should-not-display'))
    render(<AssistantRunCard chatId="chat-1" runId="run-numeric" />)
    expect(await screen.findByRole('alert')).toHaveTextContent(/[\u4e00-\u9fff]/)
    expect(screen.queryByText(/should-not-display/)).not.toBeInTheDocument()
    expect(screen.queryByText(/加载中/)).not.toBeInTheDocument()
  })

  test('keeps the initial unresolved load pending', () => {
    getRun.mockReturnValue(new Promise<RunView>(() => {}))
    render(<AssistantRunCard chatId="chat-1" runId="run-numeric" />)
    expect(screen.getByRole('status')).toHaveTextContent(/加载中/)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  test('keeps the existing report and shows the terminal status when a refetch has no report', async () => {
    getRun
      .mockResolvedValueOnce(view(numericFixtureReport()))
      .mockResolvedValueOnce({ ...view(numericFixtureReport()), status: 'interrupted', stage: 'interrupted', report: undefined, revision: 2 })
    render(<AssistantRunCard chatId="chat-1" runId="run-numeric" />)
    expect(await screen.findByText('48')).toBeInTheDocument()
    act(() => {
      globalThis.dispatchEvent(new CustomEvent(ASSISTANT_RUN_UPDATED, {
        detail: {
          run_id: 'run-numeric',
          message_id: 'msg-run-numeric',
          revision: 2,
          stage: 'interrupted',
          status: 'interrupted',
        },
      }))
    })
    expect(await screen.findByText('已中断')).toBeInTheDocument()
    expect(screen.getByText('48')).toBeInTheDocument()
  })

  test('shows a clickable switch button on reload without a local draft and retains input fields while changing basis', async () => {
    createRun.mockResolvedValue({ run_id: 'run-outstock', message_id: 'msg-outstock', status: 'running' })
    getRun.mockResolvedValue(view(numericFixtureReport()))
    render(<AssistantRunCard chatId="chat-1" runId="run-numeric" />)
    expect(await screen.findByText('48')).toBeInTheDocument()

    const button = screen.getByRole('button', { name: zhCN.assistant.card.switchBasis })
    expect(button).toBeEnabled()
    fireEvent.click(button)
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(1))

    const body = createRun.mock.calls[0][1] as AnalysisRequest
    expect(body.basis).toBe('outstock')
    expect(body.request_id).toBeTruthy()
    expect(body.request_id).not.toBe(numericFixtureRequest().request_id)
    expect(body.parent_run_id).toBe('run-numeric')
    expect(body.product).toEqual({ product_id: 'P1' })
    expect(body.scope).toEqual({ mode: 'all' })
    expect(body.period).toEqual(expect.objectContaining({ start_date: '2026-09-01', end_date_exclusive: '2026-09-08' }))
    expect(body.metrics).toContain('sales')
  })

  test('prioritizes the authoritative GET normalized_input over a stale local draft on switch', async () => {
    const localDraft: AnalysisRequest = {
      ...numericFixtureRequest(),
      request_id: 'req-local-draft',
      product: { product_id: 'P-LOCAL' },
      scope: { mode: 'selected', shop_ids: ['S-LOCAL'] },
      period: { start_date: '2026-01-01', end_date_exclusive: '2026-01-08', timezone: 'Asia/Shanghai' },
    }
    const serverNormalized: AnalysisRequest = {
      ...numericFixtureRequest(),
      request_id: 'req-server-normalized',
      product: { product_id: 'P1' },
      scope: { mode: 'all' },
      period: { start_date: '2026-09-01', end_date_exclusive: '2026-09-08', timezone: 'Asia/Shanghai' },
    }
    createRun.mockResolvedValue({ run_id: 'run-outstock', message_id: 'msg-outstock', status: 'running' })
    getRun.mockResolvedValue({ ...view(numericFixtureReport()), normalized_input: serverNormalized })
    render(<AssistantRunCard chatId="chat-1" runId="run-numeric" request={localDraft} />)
    expect(await screen.findByText('48')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: zhCN.assistant.card.switchBasis }))
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(1))

    const body = createRun.mock.calls[0][1] as AnalysisRequest
    expect(body.request_id).toBeTruthy()
    expect(body.request_id).not.toBe(serverNormalized.request_id)
    expect(body.product).toEqual({ product_id: 'P1' })
    expect(body.scope).toEqual({ mode: 'all' })
    expect(body.period).toEqual(expect.objectContaining({ start_date: '2026-09-01' }))
  })

  test('generates new request_id on each explicit switch click after cancelled/failed', async () => {
    const paymentRun = { ...view(numericFixtureReport()), run_id: 'run-payment', report: { ...numericFixtureReport(), report_id: 'run-payment', basis: 'payment' as const } }
    const cancelledRun = { ...paymentRun, run_id: 'run-cancelled', status: 'cancelled' as const, report: undefined }
    createRun
      .mockResolvedValueOnce({ run_id: 'run-cancelled', message_id: 'msg-1', status: 'running' })
      .mockResolvedValueOnce({ run_id: 'run-retry', message_id: 'msg-2', status: 'running' })
    getRun
      .mockResolvedValueOnce(paymentRun)
      .mockResolvedValueOnce(cancelledRun)
      .mockResolvedValueOnce(cancelledRun)

    render(<AssistantRunCard chatId="chat-1" runId="run-payment" />)
    expect(await screen.findByText('48')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: zhCN.assistant.card.switchBasis }))
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(1))
    const firstCall = createRun.mock.calls[0][1] as AnalysisRequest
    expect(firstCall.basis).toBe('outstock')
    expect(firstCall.parent_run_id).toBe('run-payment')

    await waitFor(() => expect(screen.getByText(zhCN.assistant.status.cancelled)).toBeInTheDocument())
    expect(screen.getByText('48')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: zhCN.assistant.card.switchBasis }))
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(2))
    const secondCall = createRun.mock.calls[1][1] as AnalysisRequest
    expect(secondCall.basis).toBe('outstock')
    expect(secondCall.parent_run_id).toBe('run-payment')
    expect(secondCall.request_id).toBeTruthy()
    expect(secondCall.request_id).not.toBe(firstCall.request_id)
  })
})
