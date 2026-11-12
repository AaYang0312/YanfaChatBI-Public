import axios from 'axios';
import { sourceAccess } from '../contexts/SourceAccessContext';
import { 
  ChartConfiguration,
  ChartDataResponse,
  VisualizationServiceResponse
} from '../types/visualization';
import type { QueryRow } from '../types/query';

const API_URL = import.meta.env.VITE_API_URL;

export const visualizationService = {

  /**
   * Execute the optimized chart query to fetch actual data for rendering
   * Uses the configuration returned by AI to get properly formatted data
   */
  executeChartQuery: async (
    chatId: string,
    chartConfig: ChartConfiguration,
    limit: number = 500
  ): Promise<ChartDataResponse> => {
    const access = sourceAccess();
    const target = await access.admit(chatId);
    access.assert(target);
    const response = await axios.post<{ success: boolean; data: ChartDataResponse }>(
      `${API_URL}/chats/${chatId}/execute-chart`,
      {
        chart_configuration: chartConfig,
        limit
      }
    );
    access.assertCurrent(target);
    return response.data.data;
  },

  /**
   * Lazy-load visualization data on demand
   * Fetches chart data for a specific query based on stored visualization config
   */
  getVisualizationData: async (
    chatId: string,
    messageId: string,
    queryId: string,
    limit: number = 500,
    offset: number = 0
  ): Promise<VisualizationServiceResponse> => {
    const access = sourceAccess();
    const target = await access.admit(chatId);
    access.assert(target);
    const response = await axios.post<VisualizationServiceResponse>(
      `${API_URL}/chats/${chatId}/visualization-data`,
      {
        message_id: messageId,
        query_id: queryId,
        limit,
        offset
      }
    );
    access.assertCurrent(target);
    return response.data;
  },

  /**
   * Transform query results to match expected column mapping for chart rendering
   * Handles nested objects and special cases
   */
  transformDataForChart: (
    data: QueryRow[],
    config: ChartConfiguration
  ): QueryRow[] => {
    const xAxisKey = config.chart_render.x_axis?.data_key;
    const seriesKeys = config.chart_render.series?.map(s => s.data_key) || [];

    return data.map(row => {
      const transformedRow: QueryRow = {};
      
      // Always include x-axis key
      if (xAxisKey && xAxisKey in row) {
        transformedRow[xAxisKey] = row[xAxisKey];
      }

      // Include all series keys
      seriesKeys.forEach(key => {
        if (key in row) {
          transformedRow[key] = row[key];
        }
      });

      // For pie charts, include both data_key and name_key
      if (config.chart_type === 'pie' && config.chart_render.pie) {
        const { data_key, name_key } = config.chart_render.pie;
        if (data_key in row) transformedRow[data_key] = row[data_key];
        if (name_key in row) transformedRow[name_key] = row[name_key];
      }

      return transformedRow;
    });
  },

  /**
   * Determine if data is too large for visualization and needs aggregation
   * For 10k+ rows, chart will be aggregated or sampled by backend
   */
  shouldAggregate: (rowCount: number, threshold: number = 5000): boolean => {
    return rowCount > threshold;
  },

  /**
   * Get appropriate chart height based on data density
   * Sparse data: smaller charts, Moderate: medium, Dense: larger charts
   */
  getChartHeight: (dataDensity: string, rows: number): number => {
    if (dataDensity === 'sparse') return 300;
    if (dataDensity === 'dense') return 500;
    return Math.max(300, Math.min(500, rows / 10)); // Scale with data
  }
};
