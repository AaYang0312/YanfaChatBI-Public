import { useLayoutEffect, useState, type PropsWithChildren, type ReactElement } from 'react'
import { act, cleanup, fireEvent, render as renderView, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import AssistantInputPanel, { type AssistantInputPanelProps } from './AssistantInputPanel'
import WorkflowMenu from './WorkflowMenu'
import { zhCN } from '../../i18n/zh-CN'
import type { AnalysisRequest, Candidate, CreateRunResponse, Resolution, Workflow } from '../../types/assistant'
import { installSourceAccess, SourceAccessContext, SourceAccessCoordinator } from '../../contexts/SourceAccessContext'
import type { Chat } from '../../types/chat'

let authority: SourceAccessCoordinator
let uninstall: (() => void) | undefined
const chats = ['A', 'B'].map((id): Chat => ({
  id, user_id: 'owner', source_classification: 'independent',
  connection: { type: 'postgresql', host: 'independent.invalid', port: '5432', username: 'reader', database: 'isolated', is_example_db: false },
  settings: { auto_execute_query: false, share_data_with_ai: false, non_tech_mode: false, auto_generate_visualization: false },
  created_at: '', updated_at: '',
}))
const wrapper = ({ children }: PropsWithChildren) => <SourceAccessContext.Provider value={authority}>{children}</SourceAccessContext.Provider>
const render = (ui: ReactElement) => renderView(ui, { wrapper })

const resolve = vi.fn()
const createRun = vi.fn()
const cancelRun = vi.fn()
const getRun = vi.fn()

vi.mock('../../services/assistantService', () => ({
  default: {
    resolve: (...args: unknown[]) => resolve(...args),
    createRun: (...args: unknown[]) => createRun(...args),
    getRun: (...args: unknown[]) => getRun(...args),
    cancelRun: (...args: unknown[]) => cancelRun(...args),
  },
}))

const copy = zhCN.assistant.input
const cups: Candidate[] = [
  { entity_type: 'product', entity_id: 'cup-1', label: '杯子 A' },
  { entity_type: 'sku', entity_id: 'sku-2', label: 'SKU sku-2' },
]
const request: AnalysisRequest = {
  schema_version: 1,
  request_id: 'resolved-A',
  workflow: 'product_analysis',
  basis: 'payment',
  period: { start_date: '2026-09-01', end_date_exclusive: '2026-09-08', timezone: 'Asia/Shanghai' },
  scope: { mode: 'all' },
  product: { product_id: 'cup-1' },
  group_by: 'shop',
  metrics: ['sales'],
}

function deferred<T>() {
  // This suite targets ES2020; its Promise lib does not include withResolvers.
  let finish!: (value: T) => void
  const promise = new Promise<T>(done => { finish = done })
  return { promise, finish }
}

function Input(props: Pick<AssistantInputPanelProps, 'chatId' | 'isConnected' | 'onRunCreated'>) {
  const [workflow, setWorkflow] = useState<Workflow>('product_analysis')
  useLayoutEffect(() => { authority.select(props.chatId) }, [props.chatId])
  return <AssistantInputPanel {...props} workflow={workflow} onWorkflowChange={setWorkflow} />
}

function Composer() {
  const [open, setOpen] = useState(false)
  const [workflow, setWorkflow] = useState<Workflow | null>(null)
  return (
    <div>
      <WorkflowMenu open={open} workflow={workflow} onOpenChange={setOpen} onSelect={setWorkflow}>
        <AssistantInputPanel chatId="A" isConnected workflow={workflow ?? 'product_analysis'} onWorkflowChange={setWorkflow} onRunCreated={() => setOpen(false)} />
      </WorkflowMenu>
      <textarea aria-label="普通消息" />
    </div>
  )
}

function explicitProduct(id: string) {
  fireEvent.click(screen.getByRole('button', { name: copy.explicit }))
  fireEvent.change(screen.getByLabelText(copy.productId), { target: { value: id } })
}

function submit() {
  fireEvent.submit(screen.getByRole('form', { name: copy.title }))
}

beforeEach(() => {
  resolve.mockReset()
  createRun.mockReset()
  getRun.mockReset()
  cancelRun.mockReset()
  authority = new SourceAccessCoordinator()
  authority.configure('owner', chats[0], chats)
  uninstall = installSourceAccess(authority)
})
afterEach(() => { cleanup(); uninstall?.() })

describe('assistant workflow boundaries', () => {
  test('requires a candidate decision and never combines the other product with the chosen SKU', async () => {
    resolve.mockResolvedValue({ status: 'needs_input', missing_fields: ['product.product_id'], candidates: cups })
    createRun.mockResolvedValue({ run_id: 'run-sku', message_id: 'msg-sku', status: 'created' })
    render(<Input chatId="A" isConnected onRunCreated={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(copy.question), { target: { value: '杯子' } })
    submit()

    await screen.findByRole('button', { name: '杯子 A' })
    expect(createRun).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'SKU sku-2' }))
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(1))
    const body = createRun.mock.calls[0][1] as AnalysisRequest
    expect(body.product).toEqual({ product_id: '', sku_ids: ['sku-2'] })
    expect(JSON.stringify(body)).not.toContain('cup-1')
  })

  test('retains candidates and input after create failure until an explicit retry succeeds', async () => {
    resolve.mockResolvedValue({ status: 'needs_input', candidates: cups })
    createRun.mockRejectedValueOnce(new Error('isolated create failure'))
      .mockResolvedValueOnce({ run_id: 'retry', message_id: 'msg-retry', status: 'created' })
    render(<Input chatId="A" isConnected onRunCreated={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(copy.question), { target: { value: '杯子草稿' } })
    submit()
    fireEvent.click(await screen.findByRole('button', { name: 'SKU sku-2' }))
    expect(await screen.findByRole('alert')).toBeVisible()
    expect(screen.getByLabelText(copy.question)).toHaveValue('杯子草稿')
    expect(screen.getByRole('button', { name: '杯子 A' })).toBeEnabled()
    expect(createRun).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'SKU sku-2' }))
    await waitFor(() => expect(screen.queryByRole('group', { name: copy.candidates })).not.toBeInTheDocument())
    expect(createRun).toHaveBeenCalledTimes(2)
    expect(resolve).toHaveBeenCalledTimes(1)
  })

  test('rejects a reversed period locally without losing explicit input', () => {
    render(<Input chatId="A" isConnected onRunCreated={vi.fn()} />)
    explicitProduct('cup-1')
    fireEvent.change(screen.getByLabelText(copy.startDate), { target: { value: '2026-09-08' } })
    fireEvent.change(screen.getByLabelText(copy.endDate), { target: { value: '2026-09-01' } })
    submit()
    expect(screen.getByRole('alert')).toBeVisible()
    expect(screen.getByLabelText(copy.productId)).toHaveValue('cup-1')
    expect(createRun).not.toHaveBeenCalled()
    expect(resolve).not.toHaveBeenCalled()
  })

  test('retains a rejected resolve draft and only retries on a new submit', async () => {
    resolve.mockRejectedValueOnce({ response: { status: 422, data: { code: 'invalid_enum' } } })
      .mockResolvedValueOnce({ status: 'needs_input', missing_fields: ['product'] })
    render(<Input chatId="A" isConnected onRunCreated={vi.fn()} />)
    fireEvent.change(screen.getByLabelText(copy.question), { target: { value: '杯子' } })
    submit()
    await screen.findByRole('alert')
    expect(screen.getByLabelText(copy.question)).toHaveValue('杯子')
    expect(resolve).toHaveBeenCalledTimes(1)
    expect(createRun).not.toHaveBeenCalled()
    submit()
    await waitFor(() => expect(resolve).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.getByRole('button', { name: copy.submit })).toBeEnabled())
  })

  test('blocks disconnected execution including a resolve that finishes after disconnection', async () => {
    const late = deferred<Resolution>()
    resolve.mockReturnValue(late.promise)
    const onRunCreated = vi.fn()
    const view = render(<Input chatId="A" isConnected={false} onRunCreated={onRunCreated} />)
    submit()
    expect(resolve).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: copy.submit })).toBeDisabled()
    view.rerender(<Input chatId="A" isConnected onRunCreated={onRunCreated} />)
    submit()
    await waitFor(() => expect(resolve).toHaveBeenCalledTimes(1))
    view.rerender(<Input chatId="A" isConnected={false} onRunCreated={onRunCreated} />)
    await act(async () => { late.finish({ status: 'resolved', request }); await late.promise })
    expect(createRun).not.toHaveBeenCalled()
    expect(onRunCreated).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: copy.submit })).toBeDisabled()
  })

  test('drops A resolve across A to B to A, including old candidates and drafts', async () => {
    const late = deferred<Resolution>()
    resolve.mockReturnValue(late.promise)
    const onRunCreated = vi.fn()
    const view = render(<Input chatId="A" isConnected onRunCreated={onRunCreated} />)
    fireEvent.change(screen.getByLabelText(copy.question), { target: { value: 'A 的杯子' } })
    submit()
    await waitFor(() => expect(resolve).toHaveBeenCalledTimes(1))
    view.rerender(<Input chatId="B" isConnected onRunCreated={onRunCreated} />)
    expect(screen.getByLabelText(copy.question)).toHaveValue('')
    view.rerender(<Input chatId="A" isConnected onRunCreated={onRunCreated} />)
    fireEvent.change(screen.getByLabelText(copy.question), { target: { value: 'A 的新草稿' } })
    await act(async () => { late.finish({ status: 'resolved', request }); await late.promise })
    expect(createRun).not.toHaveBeenCalled()
    expect(onRunCreated).not.toHaveBeenCalled()
    expect(screen.getByLabelText(copy.question)).toHaveValue('A 的新草稿')
    expect(screen.queryByRole('group', { name: copy.candidates })).not.toBeInTheDocument()
  })

  test('drops needs_input from an old Chat instead of offering its candidate in B', async () => {
    const late = deferred<Resolution>()
    resolve.mockReturnValue(late.promise)
    const view = render(<Input chatId="A" isConnected onRunCreated={vi.fn()} />)
    submit()
    await waitFor(() => expect(resolve).toHaveBeenCalledTimes(1))
    view.rerender(<Input chatId="B" isConnected onRunCreated={vi.fn()} />)
    await act(async () => { late.finish({ status: 'needs_input', candidates: cups }); await late.promise })
    expect(screen.queryByRole('button', { name: 'SKU sku-2' })).not.toBeInTheDocument()
    expect(createRun).not.toHaveBeenCalled()
  })

  test('late A create cannot publish to B or unlock B for duplicate submission', async () => {
    const a = deferred<CreateRunResponse>()
    const b = deferred<CreateRunResponse>()
    createRun.mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise)
    const onRunCreated = vi.fn()
    const view = render(<Input chatId="A" isConnected onRunCreated={onRunCreated} />)
    explicitProduct('A-product')
    submit()
    submit()
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(1))
    view.rerender(<Input chatId="B" isConnected onRunCreated={onRunCreated} />)
    explicitProduct('B-product')
    submit()
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(2))
    await act(async () => { a.finish({ run_id: 'A-run', message_id: 'A-msg', status: 'created' }); await a.promise })
    expect(onRunCreated).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: copy.submitting })).toBeDisabled()
    submit()
    expect(createRun).toHaveBeenCalledTimes(2)
    expect(createRun.mock.calls.map(call => [call[0], call[1].product.product_id])).toEqual([
      ['A', 'A-product'], ['B', 'B-product'],
    ])
    await act(async () => { b.finish({ run_id: 'B-run', message_id: 'B-msg', status: 'created' }); await b.promise })
    expect(onRunCreated).toHaveBeenCalledTimes(1)
    expect(onRunCreated.mock.calls[0][0].runId).toBe('B-run')
  })

  test('keyboard close and reopen retain the same Chat draft and candidates without executing', async () => {
    resolve.mockResolvedValue({ status: 'needs_input', candidates: cups })
    render(<Composer />)
    const trigger = screen.getByRole('button', { name: zhCN.assistant.menu.title })
    fireEvent.keyDown(trigger, { key: 'ArrowDown' })
    expect(screen.getByRole('menuitemradio', { name: zhCN.assistant.menu.productAnalysis })).toHaveFocus()
    fireEvent.keyDown(document.activeElement!, { key: 'End' })
    expect(screen.getByRole('menuitemradio', { name: zhCN.assistant.menu.salesComparison })).toHaveFocus()
    fireEvent.click(document.activeElement!)
    expect(screen.getByLabelText(copy.workflow)).toHaveValue('sales_comparison')
    fireEvent.change(screen.getByLabelText(copy.question), { target: { value: '同 Chat 草稿' } })
    submit()
    await screen.findByRole('button', { name: '杯子 A' })
    fireEvent.keyDown(screen.getByLabelText(copy.question), { key: 'Escape' })
    expect(trigger).toHaveFocus()
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(trigger)
    expect(screen.getByLabelText(copy.question)).toHaveValue('同 Chat 草稿')
    expect(screen.getByRole('button', { name: 'SKU sku-2' })).toBeVisible()
    fireEvent.pointerDown(screen.getByLabelText('普通消息'))
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(resolve).toHaveBeenCalledTimes(1)
    expect(createRun).not.toHaveBeenCalled()
  })

  test('closing an in-flight create neither cancels it nor submits another request', async () => {
    const accepted = deferred<CreateRunResponse>()
    createRun.mockReturnValue(accepted.promise)
    render(<Composer />)
    const trigger = screen.getByRole('button', { name: zhCN.assistant.menu.title })
    fireEvent.click(trigger)
    fireEvent.click(screen.getByRole('menuitemradio', { name: zhCN.assistant.menu.productAnalysis }))
    explicitProduct('cup-1')
    submit()
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: zhCN.assistant.menu.close }))
    await act(async () => { accepted.finish({ run_id: 'accepted', message_id: 'msg-accepted', status: 'created' }); await accepted.promise })
    fireEvent.click(trigger)
    expect(screen.getByLabelText(copy.productId)).toHaveValue('cup-1')
    expect(screen.getByRole('button', { name: copy.submit })).toBeEnabled()
    expect(createRun).toHaveBeenCalledTimes(1)
    expect(resolve).not.toHaveBeenCalled()
    expect(cancelRun).not.toHaveBeenCalled()
    expect(getRun).not.toHaveBeenCalled()
  })
})
