import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import AssistantResultCard from './AssistantResultCard'
import { reportChartModels } from './assistantChartModel'
import { numericFixtureReport, numericFixtureRequest } from './numericFixture'
import { zhCN } from '../../i18n/zh-CN'
import type { AnalysisRequest, Basis } from '../../types/assistant'


vi.mock('../ChartRenderer', () => ({
  default: ({ data }: { data: Record<string, string | null>[] }) => (
    <div data-testid="chart-renderer">{JSON.stringify(data)}</div>
  ),
}))


describe('assistant result card', () => {
  afterEach(() => cleanup())

  test('renders the numeric fixture from one report object', () => {
    const report = numericFixtureReport()
    render(<AssistantResultCard report={report} sourceRequest={numericFixtureRequest()} />)

    for (const text of ['40', '48', '0.4', '10', '未扣退款']) {
      expect(screen.getAllByText(text, { exact: false }).length).toBeGreaterThan(0)
    }
    expect(screen.getByText('2026-09-01 – 2026-09-08 (Asia/Shanghai)')).toBeInTheDocument()
    expect(screen.getByText('CNY')).toBeInTheDocument()
    expect(screen.getByText('2026-09-08T00:30:00+08:00')).toBeInTheDocument()
    expect(screen.getByText(/cov:payment/)).toBeInTheDocument()
    expect(screen.getByText(zhCN.assistant.basis.payment)).toBeInTheDocument()

    const chartInputs = reportChartModels(report).flatMap(model => model.data)
    const rendered = screen.getAllByTestId('chart-renderer').map(node => node.textContent ?? '')
    expect(rendered.join('')).toContain('60')
    expect(rendered.join('')).toContain('null')
    expect(rendered.join('')).not.toContain('"0"')
    expect(JSON.stringify(chartInputs)).toBe(JSON.stringify(rendered.map(text => JSON.parse(text)).flat()))
  })

  test('changes table and chart inputs together when S1 sales change', () => {
    const report = numericFixtureReport()
    const rowMetric = report.table.rows.find(item => item.entity_id === 'S1')?.metrics.find(item => item.metric_id === 'sales')
    const bar = report.charts?.find(chart => chart.chart_id === 'shops-sales')?.points.find(point => point.entity_id === 'S1' && !point.business_date)
    if (rowMetric) rowMetric.value = '77'
    if (bar) bar.value = '77'
    render(<AssistantResultCard report={report} />)
    expect(document.querySelector('tr[data-entity-id="S1"]')?.textContent).toContain('77')
    expect(screen.getAllByTestId('chart-renderer').some(node => node.textContent?.includes('"77"'))).toBe(true)
    expect(reportChartModels(report).some(model => model.data.some(point => Object.values(point).includes('77')))).toBe(true)
  })

  test('marks stores outside the ERP pilot as uncovered and hides unavailable pilot actions', () => {
    const report = numericFixtureReport()
    report.contract_version = 'erp-payment-sale-quantity-pilot-v1'
    render(<AssistantResultCard report={report} sourceRequest={numericFixtureRequest()} onSwitchBasis={vi.fn()} />)

    expect(screen.getByText('仅覆盖试点内获批的 7 家店铺，其他店铺未覆盖。')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: zhCN.assistant.card.switchBasis })).not.toBeInTheDocument()
  })

  test('keeps a missing trend point null and renders unavailable margin', () => {
    const report = numericFixtureReport()
    const unavailable = report.table.rows[0].metrics.find(metric => metric.metric_id === 'margin')
    if (unavailable) {
      unavailable.value = null
      unavailable.status = 'unavailable'
    }
    render(<AssistantResultCard report={report} />)
    expect(screen.getAllByText(zhCN.assistant.card.marginUnavailable).length).toBeGreaterThan(0)
    const line = reportChartModels(report).find(model => model.chartId === 'trend-sales')
    expect(line?.data.find(point => point.dimension === '2026-09-03')?.sales).toBeNull()
  })

  test('shows the new-basis loading copy without replacing the old report', () => {
    const onSwitchBasis = vi.fn()
    render(
      <AssistantResultCard
        report={numericFixtureReport()}
        sourceRequest={numericFixtureRequest()}
        pendingBasis="outstock"
        onSwitchBasis={onSwitchBasis}
      />,
    )
    expect(screen.getByText(zhCN.assistant.card.loadingNewBasis)).toBeInTheDocument()
    expect(screen.queryByText(zhCN.assistant.basis.outstock)).not.toBeInTheDocument()
    expect(screen.getAllByText('48').length).toBeGreaterThan(0)
    fireEvent.click(screen.getByRole('button', { name: zhCN.assistant.card.switchBasis }))
    expect(onSwitchBasis).not.toHaveBeenCalled()
  })

  test('still switches with distinct valid request ids when randomUUID is unavailable', () => {
    const onSwitchBasis = vi.fn<(basis: Basis, request: AnalysisRequest) => void>()
    const originalCrypto = globalThis.crypto
    Object.defineProperty(globalThis, 'crypto', { configurable: true, value: {} })
    try {
      render(
        <AssistantResultCard
          report={numericFixtureReport()}
          sourceRequest={numericFixtureRequest()}
          onSwitchBasis={onSwitchBasis}
        />,
      )
      const button = screen.getByRole('button', { name: zhCN.assistant.card.switchBasis })
      fireEvent.click(button)
      fireEvent.click(button)
      expect(onSwitchBasis).toHaveBeenCalledTimes(2)
      const ids = onSwitchBasis.mock.calls.map(call => call[1].request_id)
      for (const id of ids) {
        expect(typeof id).toBe('string')
        expect(id.length).toBeGreaterThan(0)
      }
      expect(ids[1]).not.toBe(ids[0])
      expect(onSwitchBasis.mock.calls[1][0]).toBe(onSwitchBasis.mock.calls[0][0])
    } finally {
      Object.defineProperty(globalThis, 'crypto', { configurable: true, value: originalCrypto })
    }
  })
})
