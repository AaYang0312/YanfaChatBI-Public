import type { QueryRow } from './query';

// Visualization types matching backend DTOs

export interface AxisConfig {
  data_key: string;
  label: string;
  type: 'date' | 'category' | 'number';
  format?: string;
}

export interface SeriesConfig {
  data_key: string;
  name: string;
  type?: 'monotone' | 'natural' | 'stepAfter';
  stroke?: string;
  fill?: string;
  area?: boolean;
}

export interface PieConfig {
  data_key: string;
  name_key: string;
  inner_radius?: number;
}

export interface HeatmapConfig {
  x_key: string;
  y_key: string;
  value_key: string;
  colors?: string[];
}

export interface FunnelConfig {
  stage_key: string;
  value_key: string;
  colors?: string[];
}

export interface BubbleConfig {
  x_key: string;
  y_key: string;
  size_key: string;
  category_key?: string;
  colors?: string[];
}

export interface WaterfallConfig {
  category_key: string;
  value_key: string;
  colors?: {
    increase?: string;
    decrease?: string;
    total?: string;
  };
}

export interface ChartDataFetch {
  query_strategy: 'original_query' | 'aggregated_query' | 'sampled_query';
  optimized_query: string;
  limit?: number;
  sample_every_n?: number;
  projected_rows: number;
  transformation?: 'none' | 'aggregate' | 'interpolate' | 'top_n';
  transformation_details?: string;
}

export interface ChartFeatures {
  tooltip: boolean;
  legend: boolean;
  grid: boolean;
  responsive: boolean;
  zoom_enabled: boolean;
}

export interface ChartRenderConfig {
  type: 'line' | 'bar' | 'pie' | 'area' | 'scatter' | 'combo' | 'heatmap' | 'funnel' | 'bubble' | 'waterfall';
  x_axis: AxisConfig;
  y_axis?: AxisConfig;
  series?: SeriesConfig[];
  pie?: PieConfig;
  heatmap?: HeatmapConfig;
  funnel?: FunnelConfig;
  bubble?: BubbleConfig;
  waterfall?: WaterfallConfig;
  colors: string[];
  features: ChartFeatures;
}

export interface RenderingHints {
  chart_height: number;
  chart_width?: string;
  color_scheme: string;
  should_aggregate_beyond: number;
  projected_row_count: number;
  data_density: 'sparse' | 'moderate' | 'dense';
}

export interface ChartConfiguration {
  chart_type: 'line' | 'bar' | 'pie' | 'area' | 'scatter' | 'combo' | 'heatmap' | 'funnel' | 'bubble' | 'waterfall';
  title: string;
  description?: string;
  data_fetch: ChartDataFetch;
  chart_render: ChartRenderConfig;
  rendering_hints: RenderingHints;
}

export interface VisualizationResponse {
  visualization_id?: string;
  can_visualize: boolean;
  reason?: string;
  chart_configuration?: ChartConfiguration;
  chart_data?: QueryRow[];
  total_records?: number;
  returned_count?: number;
  has_more?: boolean;
  offset?: number;
  limit?: number;
  updated_at?: string;
  error?: string;
}

export interface VisualizationServiceResponse {
  success: boolean;
  data?: VisualizationResponse;
  error?: string;
}


export interface ChartDataResponse {
  data: QueryRow[];
  total_rows: number;
  chart_title: string;
}
