import { zhCN } from '../../i18n/zh-CN'
import type { ChartConfiguration } from '../../types/visualization'
import type { AnalysisReport, ChartPoint, ChartSpec, MetricID } from '../../types/assistant'

const copy = zhCN.assistant
export interface AssistantChartModel {
  chartId: string
  config: ChartConfiguration
  data: Record<string, string | null>[]
}
function seriesMetric(key: string): MetricID {
  const id = key.includes(':') ? key.split(':').pop() : key
  return (id ?? key) as MetricID
}

function pointDimension(point: ChartPoint): string {
  return point.business_date || point.entity_id || point.dimension
}

function seriesKey(point: ChartPoint, chart: ChartSpec): string {
  if (chart.metric_ids.length <= 1) return point.metric_id
  if (point.entity_id) return `${point.entity_id}:${point.metric_id}`
  return point.metric_id
}

export function chartModel(chart: ChartSpec): AssistantChartModel {
  const order: string[] = []
  const rows = new Map<string, Record<string, string | null>>()
  const series: string[] = []
  for (const point of chart.points) {
    const dimension = pointDimension(point)
    if (!rows.has(dimension)) {
      order.push(dimension)
      rows.set(dimension, { dimension })
    }
    const key = seriesKey(point, chart)
    if (!series.includes(key)) series.push(key)
    const row = rows.get(dimension)
    if (row) row[key] = point.value
  }
  const axisType = chart.points.some(point => point.business_date) ? 'date' : 'category'
  return {
    chartId: chart.chart_id,
    config: {
      chart_type: chart.kind,
      title: chart.title,
      description: chart.coverage_ref,
      data_fetch: {
        query_strategy: 'original_query',
        optimized_query: '',
        projected_rows: order.length,
      },
      chart_render: {
        type: chart.kind,
        x_axis: { data_key: 'dimension', label: chart.dimension, type: axisType },
        y_axis: { data_key: series[0] ?? 'value', label: series.map(key => copy.metrics[seriesMetric(key)]).join(' / '), type: 'number' },
        series: series.map(key => ({
          data_key: key,
          name: copy.metrics[seriesMetric(key)],
        })),
        colors: [],
        features: {
          tooltip: true,
          legend: series.length > 1,
          grid: true,
          responsive: true,
          zoom_enabled: false,
        },
      },
      rendering_hints: {
        chart_height: 400,
        color_scheme: 'assistant',
        should_aggregate_beyond: order.length,
        projected_row_count: order.length,
        data_density: 'sparse',
      },
    },
    data: order.map(dimension => rows.get(dimension) as Record<string, string | null>),
  }
}

export function reportChartModels(report: AnalysisReport): AssistantChartModel[] {
  return (report.charts ?? []).map(chartModel)
}
