import axios from 'axios';
import { sourceAccess, SourceAccessError, type SourceTarget } from '../contexts/SourceAccessContext';
import {
  Dashboard,
  DashboardListItem,
  CreateDashboardRequest,
  UpdateDashboardRequest,
  RegenerateDashboardRequest,
  AddWidgetRequest,
  EditWidgetRequest,
  CreateFromBlueprintsRequest,
  Widget,
  ValidateImportRequest,
  ValidateImportResponse,
  ImportDashboardRequest,
  ImportDashboardResponse,
} from '../types/dashboard';

const API_URL = import.meta.env.VITE_API_URL;

export interface DashboardExecution {
  operationId: string;
  target: SourceTarget;
}

function assertExecution(chatId: string, execution: DashboardExecution) {
  if (execution.target.chatId !== chatId) throw new SourceAccessError();
  sourceAccess().assert(execution.target);
}

const operationParams = (streamId: string, execution: DashboardExecution) => ({ stream_id: streamId, operation_id: execution.operationId });

export const dashboardService = {
  // === Dashboard CRUD ===

  /**
   * Create a new dashboard via AI generation
   */
  createDashboard: async (
    chatId: string,
    request: CreateDashboardRequest
  ): Promise<Dashboard> => {
    const access = sourceAccess();
    const target = await access.admit(chatId);
    access.assert(target);
    const response = await axios.post(
      `${API_URL}/chats/${chatId}/dashboards`,
      request
    );
    access.assertCurrent(target);
    return response.data.data;
  },

  /**
   * Get a dashboard with all its widgets
   */
  getDashboard: async (
    chatId: string,
    dashboardId: string
  ): Promise<Dashboard> => {
    const response = await axios.get(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}`
    );
    return response.data.data;
  },

  /**
   * List all dashboards for a chat
   */
  listDashboards: async (
    chatId: string
  ): Promise<DashboardListItem[]> => {
    const response = await axios.get(
      `${API_URL}/chats/${chatId}/dashboards`
    );
    return response.data.data || [];
  },

  /**
   * Update dashboard metadata (name, refresh interval, layout, etc.)
   */
  updateDashboard: async (
    chatId: string,
    dashboardId: string,
    request: UpdateDashboardRequest
  ): Promise<Dashboard> => {
    const response = await axios.patch(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}`,
      request
    );
    return response.data.data;
  },

  /**
   * Delete a dashboard and all its widgets
   */
  deleteDashboard: async (
    chatId: string,
    dashboardId: string
  ): Promise<void> => {
    await axios.delete(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}`
    );
  },

  // === Widget Operations ===

  /**
   * Add a new widget via AI prompt
   */
  addWidget: async (
    chatId: string,
    dashboardId: string,
    request: AddWidgetRequest
  ): Promise<Widget> => {
    const access = sourceAccess();
    const target = await access.admit(chatId);
    access.assert(target);
    const response = await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}/widgets`,
      request
    );
    access.assertCurrent(target);
    return response.data.data;
  },

  /**
   * Edit an existing widget via AI prompt
   */
  editWidget: async (
    chatId: string,
    dashboardId: string,
    widgetId: string,
    request: EditWidgetRequest
  ): Promise<Widget> => {
    const access = sourceAccess();
    const target = await access.admit(chatId);
    access.assert(target);
    const response = await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}/widgets/${widgetId}/edit`,
      request
    );
    access.assertCurrent(target);
    return response.data.data;
  },

  /**
   * Delete a widget from a dashboard
   */
  deleteWidget: async (
    chatId: string,
    dashboardId: string,
    widgetId: string
  ): Promise<void> => {
    await axios.delete(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}/widgets/${widgetId}`
    );
  },

  // === AI Operations ===

  /**
   * Trigger AI to generate dashboard blueprint suggestions
   * Results are delivered via SSE events
   */
  generateBlueprints: async (
    chatId: string,
    streamId: string,
    execution: DashboardExecution,
    prompt?: string,
    signal?: AbortSignal
  ): Promise<void> => {
    assertExecution(chatId, execution);
    await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/suggest-templates`,
      prompt ? { prompt } : undefined,
      { signal, params: operationParams(streamId, execution) }
    );
    assertExecution(chatId, execution);
  },

  /**
   * Create dashboards from selected blueprints
   * Results are delivered via SSE events
   */
  createFromBlueprints: async (
    chatId: string,
    streamId: string,
    request: CreateFromBlueprintsRequest,
    execution: DashboardExecution,
    signal?: AbortSignal
  ): Promise<void> => {
    assertExecution(chatId, execution);
    await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/create-from-blueprints`,
      request,
      { signal, params: operationParams(streamId, execution) }
    );
    assertExecution(chatId, execution);
  },

  /**
   * Regenerate an existing dashboard
   * Results are delivered via SSE events
   */
  regenerateDashboard: async (
    chatId: string,
    dashboardId: string,
    streamId: string,
    request: RegenerateDashboardRequest,
    execution: DashboardExecution,
    signal?: AbortSignal
  ): Promise<void> => {
    assertExecution(chatId, execution);
    await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}/regenerate`,
      request,
      { signal, params: operationParams(streamId, execution) }
    );
    assertExecution(chatId, execution);
  },

  // === Data Refresh ===

  /**
   * Trigger a manual refresh of all dashboard widgets
   * Data is delivered via SSE events per widget
   */
  refreshDashboard: async (
    chatId: string,
    dashboardId: string,
    streamId: string,
    execution: DashboardExecution
  ): Promise<void> => {
    assertExecution(chatId, execution);
    await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}/refresh`,
      undefined,
      { params: operationParams(streamId, execution) }
    );
    assertExecution(chatId, execution);
  },

  /**
   * Trigger a refresh of a single widget
   * Data is delivered via SSE events
   * @param cursor - Optional cursor value for pagination
   */
  refreshWidget: async (
    chatId: string,
    dashboardId: string,
    widgetId: string,
    streamId: string,
    execution: DashboardExecution,
    cursor?: string
  ): Promise<void> => {
    assertExecution(chatId, execution);
    await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}/widgets/${widgetId}/refresh`,
      undefined,
      { params: { ...operationParams(streamId, execution), cursor } }
    );
    assertExecution(chatId, execution);
  },

  // === Import/Export ===

  /**
   * Export a dashboard to portable JSON format
   * Returns JSON that can be downloaded and imported elsewhere
   */
  exportDashboard: async (
    chatId: string,
    dashboardId: string
  ): Promise<Blob> => {
    const response = await axios.get(
      `${API_URL}/chats/${chatId}/dashboards/${dashboardId}/export`,
      { responseType: 'blob' }
    );
    return response.data;
  },

  /**
   * Validate import JSON and get connection mapping suggestions
   */
  validateImport: async (
    chatId: string,
    request: ValidateImportRequest
  ): Promise<ValidateImportResponse> => {
    const response = await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/import/validate`,
      request
    );
    return response.data.data;
  },

  /**
   * Import a dashboard from export JSON
   */
  importDashboard: async (
    chatId: string,
    request: ImportDashboardRequest
  ): Promise<ImportDashboardResponse> => {
    const response = await axios.post(
      `${API_URL}/chats/${chatId}/dashboards/import`,
      request
    );
    return response.data.data;
  },
};
