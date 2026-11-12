import type { AnalysisReport, AnalysisRequest, ChartPoint, MetricID, MetricValue, ReportRow } from '../../types/assistant'

const WINDOW = {
  start_date: '2026-09-01',
  end_date_exclusive: '2026-09-08',
  timezone: 'Asia/Shanghai',
}

function metric(id: MetricID, value: string | null, currency?: string): MetricValue {
  return {
    metric_id: id,
    status: value == null ? 'unavailable' : 'available',
    value,
    currency,
    coverage_ref: 'cov:payment',
  }
}

function shop(entityId: string, quantity: string, sales: string, cost: string, margin: string, rate: string): ReportRow {
  return {
    entity_type: 'shop',
    entity_id: entityId,
    currency: 'CNY',
    metrics: [
      metric('quantity', quantity),
      metric('sales', sales, 'CNY'),
      metric('cost', cost, 'CNY'),
      metric('margin', margin, 'CNY'),
      metric('margin_rate', rate),
      metric('avg_price', entityId === 'S1' ? '30' : '60', 'CNY'),
    ],
    coverage_ref: 'cov:payment',
  }
}

function point(entityId: string, metricId: MetricID, value: string | null, businessDate?: string): ChartPoint {
  return {
    dimension: businessDate ?? entityId,
    entity_id: entityId,
    business_date: businessDate,
    metric_id: metricId,
    value,
    status: value == null ? 'unavailable' : 'available',
    currency: metricId === 'margin_rate' ? undefined : 'CNY',
    coverage_ref: 'cov:payment',
  }
}

export function numericFixtureRequest(): AnalysisRequest {
  return {
    schema_version: 1,
    request_id: 'req-numeric',
    workflow: 'product_analysis',
    basis: 'payment',
    period: WINDOW,
    scope: { mode: 'all' },
    product: { product_id: 'P1' },
    group_by: 'shop',
    metrics: ['quantity', 'sales', 'cost', 'margin', 'margin_rate', 'avg_price', 'refund'],
  }
}

export function numericFixtureReport(): AnalysisReport {
  return {
    report_id: 'run-numeric',
    schema_version: 1,
    workflow: 'product_analysis',
    basis: 'payment',
    requested_scope: { mode: 'all' },
    actual_scope: { mode: 'all' },
    window: WINDOW,
    window_start: '2026-09-01T00:00:00+08:00',
    window_end_exclusive: '2026-09-08T00:00:00+08:00',
    contract_version: 'fixture-v1',
    outcome: 'complete',
    coverage: {
      requested: [],
      expected: [],
      covered: [],
      missing: [],
      stale: [],
      unknown: [],
      included: [
        { entity_type: 'shop', entity_id: 'S1', status: 'covered', coverage_ref: 'cov:payment' },
        { entity_type: 'shop', entity_id: 'S2', status: 'covered', coverage_ref: 'cov:payment' },
      ],
      excluded: [],
      data_as_of: '2026-09-08T00:30:00+08:00',
      coverage_ref: 'cov:payment',
    },
    totals: [
      metric('quantity', '3'),
      metric('sales', '120', 'CNY'),
      metric('cost', '72', 'CNY'),
      metric('margin', '48', 'CNY'),
      metric('margin_rate', '0.4'),
      metric('avg_price', '40', 'CNY'),
      metric('refund', '10', 'CNY'),
    ],
    table: {
      dimension: 'shop',
      rows: [
        shop('S1', '2', '60', '30', '30', '0.5'),
        shop('S2', '1', '60', '42', '18', '0.3'),
      ],
    },
    trend: [
      point('S1', 'sales', '20', '2026-09-01'),
      point('S1', 'sales', '40', '2026-09-02'),
      point('S1', 'sales', null, '2026-09-03'),
    ],
    charts: [
      {
        chart_id: 'shops-sales',
        kind: 'bar',
        title: 'S1 / S2',
        metric_ids: ['sales'],
        dimension: 'shop',
        data_ref: 'table.rows',
        coverage_ref: 'cov:payment',
        points: [
          point('S1', 'sales', '60'),
          point('S2', 'sales', '60'),
        ],
      },
      {
        chart_id: 'trend-sales',
        kind: 'line',
        title: 'S1',
        metric_ids: ['sales'],
        dimension: 'business_date',
        data_ref: 'trend',
        coverage_ref: 'cov:payment',
        points: [
          point('S1', 'sales', '20', '2026-09-01'),
          point('S1', 'sales', '40', '2026-09-02'),
          point('S1', 'sales', null, '2026-09-03'),
        ],
      },
    ],
    refunds: {
      label: '未扣退款',
      amount: '10',
      status: 'available',
      currency: 'CNY',
      occurrence_window: { start_date: WINDOW.start_date, end_date_exclusive: WINDOW.end_date_exclusive },
    },
    data_as_of: '2026-09-08T00:30:00+08:00',
    margin_label: '未扣退款',
  }
}
