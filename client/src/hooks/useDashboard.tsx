import { Trash2 } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { dashboardService, type DashboardExecution } from '../services/dashboardService';
import { formatServiceError, zhCN } from '../i18n/zh-CN';
import { getErrorMessage } from '../utils/message';
import { sourceAccess, isSourceAccessDenial, type SourceTarget } from '../contexts/SourceAccessContext';
import { useSourceAccess } from './useSourceAccess';
import {
  Dashboard,
  DashboardBlueprint,
  DashboardBlueprintEvent,
  DashboardGenerationCompleteEvent as DashboardGenCompleteEvent,
  DashboardGenerationProgressEvent,
  DashboardListItem,
  DashboardWidgetDataEvent,
  DASHBOARD_SSE_EVENTS,
} from '../types/dashboard';

// Module-level cache to prevent re-fetching on tab toggle
const _listCache = new Map<string, { list: DashboardListItem[]; activeDashId?: string }>();
const _dashCache = new Map<string, Dashboard>();

interface UseDashboardProps {
  chatId: string;
  streamId: string | null;
  isConnected: boolean;
  onReconnect?: () => Promise<void>;
}

interface DashboardOperation extends DashboardExecution {
  kind: 'refresh' | 'suggestions' | 'generation';
  remaining: number;
  widgets: Set<string>;
  dashboards: Set<string>;
  httpDone: boolean;
  resetPagination?: boolean;
  batchResult?: DashboardGenerationProgressEvent['batch_result'];
}

export function useDashboard({ chatId, streamId, isConnected, onReconnect }: UseDashboardProps) {
  const source = useSourceAccess(chatId);
  const autoRefreshSuspended = useRef(false);
  const autoRefreshTarget = useRef<SourceTarget | null>(null);
  const operations = useRef(new Map<string, DashboardOperation>());
  const widgetOwners = useRef(new Map<string, { operationId: string; dashboardId: string }>());
  const generationOperation = useRef<string | null>(null);
  const refreshOperation = useRef<string | null>(null);
  const refreshDashboardDispatch = useRef<((silent: boolean, retryCount: number, previous: SourceTarget) => Promise<void>) | null>(null);
  const blueprintOrigin = useRef<{ reference: string; target: SourceTarget } | null>(null);
  const sourceIdentity = source.coordinator?.capture(chatId);
  const dashboardRef = useRef<Dashboard | null>(null);
  useEffect(() => { if (!source.allowed && source.chat && source.chat.source_classification !== 'independent') autoRefreshSuspended.current = true }, [source.allowed, source.chat]);
  const beginExecution = useCallback(async (silent = false, previous?: SourceTarget) => {
    try {
      const access = sourceAccess();
      const target = previous ?? await access.admit(chatId);
      access.assert(target);
      return target;
    } catch (error) {
      if (!silent) toast.error(formatServiceError(error).summary);
      return null;
    }
  }, [chatId]);
  // Dashboard list state
  const [dashboardList, setDashboardList] = useState<DashboardListItem[]>([]);
  const [isLoadingList, setIsLoadingList] = useState(true);
  const [activeDashboard, setActiveDashboard] = useState<Dashboard | null>(null);
  dashboardRef.current = activeDashboard;
  const [isLoadingDashboard, setIsLoadingDashboard] = useState(false);

  // UI state
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [individuallyRefreshingWidgets, setIndividuallyRefreshingWidgets] = useState<Set<string>>(new Set());

  // Blueprint / generation state
  const [blueprints, setBlueprints] = useState<DashboardBlueprint[]>([]);
  const [showBlueprintPicker, setShowBlueprintPicker] = useState(false);
  const [selectedBlueprintIndices, setSelectedBlueprintIndices] = useState<Set<number>>(new Set());
  const [generationProgress, setGenerationProgress] = useState<DashboardGenerationProgressEvent | null>(null);
  const [, setIsGeneratingBlueprints] = useState(false);
  const [isCreatingFromBlueprints, setIsCreatingFromBlueprints] = useState(false);
  const [generationContext, setGenerationContext] = useState<'blueprints' | 'creating' | 'custom'>('blueprints');
  const hasFetchedOnceRef = useRef<string | null>(null);

  // Modal visibility state
  const [showPromptModal, setShowPromptModal] = useState(false);
  const [showAddWidgetModal, setShowAddWidgetModal] = useState(false);
  const [isAddingWidget, setIsAddingWidget] = useState(false);
  const [showRegenerateModal, setShowRegenerateModal] = useState(false);
  const [showDeleteDashboardConfirm, setShowDeleteDashboardConfirm] = useState(false);

  // Import/Export state
  const [showImportModal, setShowImportModal] = useState(false);
  const [importJsonContent, setImportJsonContent] = useState<string>('');
  const [isImporting, setIsImporting] = useState(false);

  // Edit widget state
  const [editingWidgetId, setEditingWidgetId] = useState<string | null>(null);
  const [isEditingWidget, setIsEditingWidget] = useState(false);

  // Refs
  const refreshIntervalRef = useRef<NodeJS.Timeout | null>(null);
  const abortControllerRef = useRef<AbortController | null>(null);

  // Refs for latest prop values (needed for async operations after reconnect)
  const streamIdRef = useRef(streamId);
  useEffect(() => { streamIdRef.current = streamId; }, [streamId]);
  const isConnectedRef = useRef(isConnected);
  useEffect(() => { isConnectedRef.current = isConnected; }, [isConnected]);

  // Widget loading timeout tracking
  const widgetLoadingTimeoutsRef = useRef<Map<string, NodeJS.Timeout>>(new Map());
  const WIDGET_LOADING_TIMEOUT_MS = 30_000;

  const clearLoadingTimeout = useCallback((widgetId: string) => {
    const existing = widgetLoadingTimeoutsRef.current.get(widgetId);
    if (existing) {
      clearTimeout(existing);
      widgetLoadingTimeoutsRef.current.delete(widgetId);
    }
  }, []);

  const clearAllLoadingTimeouts = useCallback(() => {
    widgetLoadingTimeoutsRef.current.forEach((t) => clearTimeout(t));
    widgetLoadingTimeoutsRef.current.clear();
  }, []);

  const executionCurrent = useCallback((execution: DashboardExecution) => {
    try { sourceAccess().assert(execution.target); return true } catch { return false }
  }, []);

  const pruneOperation = useCallback((operation: DashboardOperation) => {
    if (operation.httpDone && operation.remaining === 0 && operation.widgets.size === 0) operations.current.delete(operation.operationId);
  }, []);

  const registerOperation = useCallback((target: SourceTarget, kind: DashboardOperation['kind'], remaining = 0): DashboardOperation => {
    sourceAccess().assert(target);
    const operation: DashboardOperation = { operationId: crypto.randomUUID(), target, kind, remaining, widgets: new Set(), dashboards: new Set(), httpDone: false };
    operations.current.set(operation.operationId, operation);
    if (kind !== 'refresh') {
      const previous = generationOperation.current ? operations.current.get(generationOperation.current) : undefined;
      if (previous?.kind === 'suggestions') { previous.remaining = 0; pruneOperation(previous) }
      generationOperation.current = operation.operationId;
    }
    return operation;
  }, [pruneOperation]);

  const finishHTTP = useCallback((operation: DashboardOperation) => {
    operation.httpDone = true;
    pruneOperation(operation);
  }, [pruneOperation]);

  const retireWidget = useCallback((operation: DashboardOperation, widgetId: string) => {
    operation.widgets.delete(widgetId);
    pruneOperation(operation);
  }, [pruneOperation]);

  const associateWidgets = useCallback((operation: DashboardOperation, dashboardId: string, widgetIds: string[]) => {
    operation.dashboards.add(dashboardId);
    for (const widgetId of widgetIds) {
      const previous = widgetOwners.current.get(widgetId);
      const previousOperation = previous ? operations.current.get(previous.operationId) : undefined;
      if (previousOperation && previousOperation !== operation) retireWidget(previousOperation, widgetId);
      widgetOwners.current.set(widgetId, { operationId: operation.operationId, dashboardId });
      operation.widgets.add(widgetId);
    }
  }, [retireWidget]);

  const discardOperation = useCallback((operation: DashboardOperation) => {
    operations.current.delete(operation.operationId);
    const owned = new Set<string>();
    for (const widgetId of operation.widgets) {
      if (widgetOwners.current.get(widgetId)?.operationId === operation.operationId) {
        owned.add(widgetId);
        widgetOwners.current.delete(widgetId);
        clearLoadingTimeout(widgetId);
      }
    }
    if (owned.size) {
      setIndividuallyRefreshingWidgets(prev => new Set([...prev].filter(id => !owned.has(id) || widgetOwners.current.has(id))));
      setActiveDashboard(prev => prev ? { ...prev, widgets: prev.widgets.map(w => owned.has(w.id) && !widgetOwners.current.has(w.id) ? { ...w, is_loading: false } : w) } : prev);
    }
  }, [clearLoadingTimeout]);

  const eventOperation = useCallback((operationId?: string) => {
    const operation = operationId ? operations.current.get(operationId) : undefined;
    return operation && executionCurrent(operation) ? operation : null;
  }, [executionCurrent]);

  const widgetOperation = useCallback((event: DashboardWidgetDataEvent) => {
    const operation = eventOperation(event.operation_id);
    if (!operation || !operation.widgets.has(event.widget_id) || widgetOwners.current.get(event.widget_id)?.operationId !== operation.operationId) return null;
    return operation;
  }, [eventOperation]);

  const cancelWidgetOperation = useCallback((widgetId: string) => {
    const owner = widgetOwners.current.get(widgetId);
    const operation = owner ? operations.current.get(owner.operationId) : undefined;
    if (operation) retireWidget(operation, widgetId);
    widgetOwners.current.delete(widgetId);
  }, [retireWidget]);

  const failOperation = useCallback((operation: DashboardOperation, before?: Dashboard, error?: string) => {
    if (!executionCurrent(operation)) return;
    const owned = new Set([...operation.widgets].filter(id => widgetOwners.current.get(id)?.operationId === operation.operationId));
    discardOperation(operation);
    setActiveDashboard(prev => {
      if (!prev || !executionCurrent(operation)) return prev;
      return { ...prev, widgets: prev.widgets.map(w => {
        if (!owned.has(w.id) || widgetOwners.current.has(w.id)) return w;
        const snapshot = before?.id === prev.id ? before.widgets.find(old => old.id === w.id) : undefined;
        return { ...(snapshot ?? w), is_loading: false, ...(error ? { error } : {}) };
      }) };
    });
  }, [executionCurrent, discardOperation]);

  const startLoadingTimeout = useCallback((widgetId: string, operation: DashboardOperation) => {
    clearLoadingTimeout(widgetId);
    const timeout = setTimeout(() => {
      if (widgetLoadingTimeoutsRef.current.get(widgetId) !== timeout) return;
      widgetLoadingTimeoutsRef.current.delete(widgetId);
      if (!executionCurrent(operation) || widgetOwners.current.get(widgetId)?.operationId !== operation.operationId || !operation.widgets.has(widgetId)) return;
      retireWidget(operation, widgetId);
      setIndividuallyRefreshingWidgets((prev) => { const next = new Set(prev); next.delete(widgetId); return next });
      setActiveDashboard((prev) => {
        if (!prev || !executionCurrent(operation) || widgetOwners.current.get(widgetId)?.operationId !== operation.operationId) return prev;
        return { ...prev, widgets: prev.widgets.map(w => w.id === widgetId && w.is_loading ? { ...w, is_loading: false, error: zhCN.toasts.widgetTimeout } : w) };
      });
    }, WIDGET_LOADING_TIMEOUT_MS);
    widgetLoadingTimeoutsRef.current.set(widgetId, timeout);
  }, [clearLoadingTimeout, executionCurrent, retireWidget]);

  // Cleanup timeouts on unmount
  useEffect(() => () => clearAllLoadingTimeouts(), [clearAllLoadingTimeouts]);

  useEffect(() => {
    if (autoRefreshTarget.current) {
      try { sourceAccess().assert(autoRefreshTarget.current) } catch { autoRefreshSuspended.current = true }
    }
    for (const operation of operations.current.values()) {
      if (executionCurrent(operation)) continue;
      discardOperation(operation);
      if (generationOperation.current === operation.operationId) {
        generationOperation.current = null;
        setGenerationProgress(null);
        setIsGeneratingBlueprints(false);
        setIsCreatingFromBlueprints(false);
      }
      if (refreshOperation.current === operation.operationId) { refreshOperation.current = null; setIsRefreshing(false) }
    }
    const origin = blueprintOrigin.current;
    if (origin) {
      try { sourceAccess().assert(origin.target) } catch {
        blueprintOrigin.current = null;
        setBlueprints([]);
        setSelectedBlueprintIndices(new Set());
        setShowBlueprintPicker(false);
      }
    }
  }, [source.coordinator, source.allowed, sourceIdentity?.owner, sourceIdentity?.session, sourceIdentity?.selection, sourceIdentity?.binding, sourceIdentity?.admission, executionCurrent, discardOperation]);

  // Try to ensure SSE connection, returns true if connected
  const ensureSSEConnection = useCallback(async (silent = false): Promise<boolean> => {
    if (streamIdRef.current && isConnectedRef.current) return true;
    if (!onReconnect) {
      if (!silent) toast.error(zhCN.toasts.notActive, { icon: '🔌' });
      return false;
    }
    try {
      if (!silent) toast.loading(zhCN.toasts.reconnecting, { id: 'sse-ensure', duration: 8000 });
      await onReconnect();
      await new Promise((r) => setTimeout(r, 150));
      if (!silent) toast.success(zhCN.toasts.reconnected, { id: 'sse-ensure' });
      return true;
    } catch {
      if (!silent) toast.error(zhCN.toasts.reconnectFail, { id: 'sse-ensure' });
      return false;
    }
  }, [onReconnect]);

  // Cancel handlers
  const handleCancelDashboardRefresh = useCallback(() => {
    refreshOperation.current = null;
    for (const [widgetId, owner] of widgetOwners.current) if (owner.dashboardId === dashboardRef.current?.id) cancelWidgetOperation(widgetId);
    clearAllLoadingTimeouts();
    setIsRefreshing(false);
    setActiveDashboard((prev) => {
      if (!prev) return null;
      return {
        ...prev,
        widgets: prev.widgets.map((w) => w.is_loading ? { ...w, is_loading: false } : w),
      };
    });
  }, [clearAllLoadingTimeouts, cancelWidgetOperation]);

  const handleCancelWidgetRefresh = useCallback((widgetId: string) => {
    cancelWidgetOperation(widgetId);
    clearLoadingTimeout(widgetId);
    setIndividuallyRefreshingWidgets((prev) => {
      const next = new Set(prev);
      next.delete(widgetId);
      return next;
    });
    setActiveDashboard((prev) => {
      if (!prev) return null;
      return {
        ...prev,
        widgets: prev.widgets.map((w) =>
          w.id === widgetId && w.is_loading ? { ...w, is_loading: false } : w
        ),
      };
    });
  }, [clearLoadingTimeout, cancelWidgetOperation]);

  // =============================================
  // Data Loading
  // =============================================

  const loadDashboard = async (dashboardId: string) => {
    const access = sourceAccess();
    const target = access.capture(chatId);
    try {
      setIsLoadingDashboard(true);
      const dashboard = await dashboardService.getDashboard(chatId, dashboardId);
      access.assertCurrent(target);
      autoRefreshTarget.current = target;
      dashboardRef.current = dashboard;
      setActiveDashboard(dashboard);
      _dashCache.set(dashboardId, dashboard);
      const cached = _listCache.get(chatId);
      if (cached) _listCache.set(chatId, { ...cached, activeDashId: dashboardId });

      hasFetchedOnceRef.current = null;

      const hasUnfetched = dashboard.widgets.some((w) => !w.data && !w.is_loading && !w.error);
      if (hasUnfetched && streamId && isConnected && access.view(chatId).allowed) {
        hasFetchedOnceRef.current = dashboardId;
        const executionTarget = await beginExecution(true, target);
        access.assertCurrent(target);
        if (!executionTarget) return;
        setActiveDashboard(prev => prev?.id === dashboardId ? { ...prev, widgets: prev.widgets.map(w => ({ ...w, is_loading: true })) } : prev);
        const operation = registerOperation(executionTarget, 'refresh');
        associateWidgets(operation, dashboardId, dashboard.widgets.map(w => w.id));
        dashboard.widgets.forEach(w => startLoadingTimeout(w.id, operation));
        try { await dashboardService.refreshDashboard(chatId, dashboardId, streamId, operation) }
        catch (error) { failOperation(operation, dashboard, formatServiceError(error).summary) }
        finally { finishHTTP(operation) }
      }
    } catch (err) {
      console.error('Failed to load dashboard:', err);
      toast.error(formatServiceError(err).summary || zhCN.toasts.loadDashboardFailed);
    } finally {
      setIsLoadingDashboard(false);
    }
  };

  const loadDashboardList = async () => {
    const access = sourceAccess();
    const target = access.capture(chatId);
    try {
      setIsLoadingList(true);
      const list = await dashboardService.listDashboards(chatId);
      access.assertCurrent(target);
      setDashboardList(list);

      if (list.length > 0) {
        const defaultDash = list.find((d: DashboardListItem) => d.is_default) || list[0];
        await loadDashboard(defaultDash.id);
        _listCache.set(chatId, { list, activeDashId: defaultDash.id });
      } else {
        _listCache.set(chatId, { list, activeDashId: undefined });
      }
    } catch (err) {
      console.error('Failed to load dashboards:', err);
    } finally {
      setIsLoadingList(false);
    }
  };

  // Reset state + load list when chatId changes
  useEffect(() => {
    autoRefreshTarget.current = sourceAccess().capture(chatId);
    operations.current.clear();
    widgetOwners.current.clear();
    generationOperation.current = null;
    refreshOperation.current = null;
    blueprintOrigin.current = null;
    setActiveDashboard(null);
    setDashboardList([]);
    setBlueprints([]);
    setShowBlueprintPicker(false);
    setGenerationProgress(null);
    setIsGeneratingBlueprints(false);
    setIsCreatingFromBlueprints(false);
    setShowPromptModal(false);
    setShowAddWidgetModal(false);
    setShowRegenerateModal(false);
    setShowDeleteDashboardConfirm(false);
    setEditingWidgetId(null);
    hasFetchedOnceRef.current = null;

    const cached = _listCache.get(chatId);
    if (cached) {
      setDashboardList(cached.list);
      setIsLoadingList(false);
      if (cached.activeDashId) {
        const cachedDash = _dashCache.get(cached.activeDashId);
        if (cachedDash) {
          setActiveDashboard(cachedDash);
        } else {
          loadDashboard(cached.activeDashId);
        }
      }
      return;
    }
    loadDashboardList();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chatId]);

  // Auto-refresh timer
  useEffect(() => {
    if (refreshIntervalRef.current) {
      clearInterval(refreshIntervalRef.current);
      refreshIntervalRef.current = null;
    }

    if (activeDashboard && activeDashboard.refresh_interval > 0 && isConnected) {
      refreshIntervalRef.current = setInterval(() => {
        if (autoRefreshSuspended.current || !sourceAccess().view(chatId).allowed) return;
        if (autoRefreshTarget.current) void refreshDashboardDispatch.current?.(true, 0, autoRefreshTarget.current);
      }, activeDashboard.refresh_interval * 1000);
    }

    return () => {
      if (refreshIntervalRef.current) {
        clearInterval(refreshIntervalRef.current);
      }
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeDashboard?.id, activeDashboard?.refresh_interval, isConnected]);

  // =============================================
  // SSE Event Listeners
  // =============================================

  useEffect(() => {
    const handleBlueprints = (e: Event) => {
      const data = (e as CustomEvent<DashboardBlueprintEvent>).detail;
      const operation = eventOperation(data.operation_id);
      if (!operation || operation.kind !== 'suggestions' || operation.remaining !== 1 || generationOperation.current !== operation.operationId || !data.blueprint_origin) return;
      operation.remaining = 0;
      blueprintOrigin.current = { reference: data.blueprint_origin, target: operation.target };
      setBlueprints(data.blueprints);
      setSelectedBlueprintIndices(new Set(data.blueprints.map(b => b.index)));
      setShowBlueprintPicker(true);
      setIsGeneratingBlueprints(false);
      setGenerationProgress(null);
      pruneOperation(operation);
    };

    const handleProgress = (e: Event) => {
      const data = (e as CustomEvent<DashboardGenerationProgressEvent>).detail;
      const operation = eventOperation(data.operation_id);
      if (!operation || operation.kind === 'refresh') return;
      if (data.batch_result) {
        const { succeeded, failed } = data.batch_result;
        if (operation.kind !== 'generation' || operation.batchResult || !Number.isSafeInteger(succeeded) || !Number.isSafeInteger(failed)
          || succeeded < operation.dashboards.size || failed < 0 || succeeded + failed !== operation.remaining + operation.dashboards.size) return;
        operation.batchResult = data.batch_result;
        operation.remaining = succeeded - operation.dashboards.size;
        if (generationOperation.current === operation.operationId) {
          setGenerationProgress(null);
          setIsCreatingFromBlueprints(false);
          if (failed > 0) toast.error(`仪表盘创建结束：成功 ${succeeded} 项，失败 ${failed} 项。`);
        }
        pruneOperation(operation);
        return;
      }
      if (operation.batchResult || operation.remaining === 0 || generationOperation.current !== operation.operationId) return;
      setGenerationProgress(data);
    };

    const handleComplete = (e: Event) => {
      const data = (e as CustomEvent<DashboardGenCompleteEvent>).detail;
      const operation = eventOperation(data.operation_id);
      if (!operation || operation.kind !== 'generation' || operation.remaining === 0
        || data.dashboard.chat_id !== chatId || data.dashboard.id !== data.dashboard_id || operation.dashboards.has(data.dashboard_id)) return;
      operation.remaining--;
      associateWidgets(operation, data.dashboard_id, data.dashboard.widgets.map(w => w.id));
      const dashboard = { ...data.dashboard, widgets: data.dashboard.widgets.map(w => ({ ...w, is_loading: true })) };
      const current = generationOperation.current === operation.operationId;
      if (current) dashboardRef.current = dashboard;
      _dashCache.set(dashboard.id, dashboard);
      if (current) setActiveDashboard(dashboard);
      dashboard.widgets.forEach(w => startLoadingTimeout(w.id, operation));
      if (current && operation.remaining === 0) { setGenerationProgress(null); setIsCreatingFromBlueprints(false) }
      setDashboardList(prev => {
        if (prev.some(d => d.id === dashboard.id)) return prev;
        const list = [...prev, { id: dashboard.id, name: dashboard.name, description: dashboard.description ?? '', template_type: dashboard.template_type ?? '',
          is_default: dashboard.is_default, widget_count: dashboard.widgets.length, created_at: dashboard.created_at, updated_at: dashboard.updated_at }];
        _listCache.set(chatId, { list, activeDashId: current ? dashboard.id : dashboardRef.current?.id });
        return list;
      });
      if (current) toast.success(`仪表盘「${dashboard.name}」已创建！`);
      pruneOperation(operation);
    };

    const handleWidgetData = (e: Event) => {
      const data = (e as CustomEvent<DashboardWidgetDataEvent>).detail;
      const operation = widgetOperation(data);
      if (operation) handleWidgetDataUpdate(data.widget_id, data, operation);
    };

    const handleWidgetError = (e: Event) => {
      const data = (e as CustomEvent<DashboardWidgetDataEvent>).detail;
      const operation = widgetOperation(data);
      if (operation && data.error) handleWidgetErrorUpdate(data.widget_id, data.error, operation);
    };

    globalThis.addEventListener(DASHBOARD_SSE_EVENTS.BLUEPRINTS, handleBlueprints);
    globalThis.addEventListener(DASHBOARD_SSE_EVENTS.GENERATION_PROGRESS, handleProgress);
    globalThis.addEventListener(DASHBOARD_SSE_EVENTS.GENERATION_COMPLETE, handleComplete);
    globalThis.addEventListener(DASHBOARD_SSE_EVENTS.WIDGET_DATA, handleWidgetData);
    globalThis.addEventListener(DASHBOARD_SSE_EVENTS.WIDGET_ERROR, handleWidgetError);
    return () => {
      globalThis.removeEventListener(DASHBOARD_SSE_EVENTS.BLUEPRINTS, handleBlueprints);
      globalThis.removeEventListener(DASHBOARD_SSE_EVENTS.GENERATION_PROGRESS, handleProgress);
      globalThis.removeEventListener(DASHBOARD_SSE_EVENTS.GENERATION_COMPLETE, handleComplete);
      globalThis.removeEventListener(DASHBOARD_SSE_EVENTS.WIDGET_DATA, handleWidgetData);
      globalThis.removeEventListener(DASHBOARD_SSE_EVENTS.WIDGET_ERROR, handleWidgetError);
    };
  // The listeners use immutable operation records and refs, including events preceding HTTP completion.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chatId]);

  // =============================================
  // Handlers
  // =============================================

  const handleRefreshDashboard = useCallback(async (silent = false, retryCount = 0, previous?: SourceTarget) => {
    if (!activeDashboard) return;
    const target = await beginExecution(silent, previous);
    if (!target) return;
    const before = activeDashboard;
    let operation: DashboardOperation | null = null;
    // The original dashboard is the stable footprint; operation.widgets shrinks as work is retired.
    const ownsRefresh = () => operation !== null && operations.current.get(operation.operationId) === operation && executionCurrent(operation)
      && refreshOperation.current === operation.operationId && dashboardRef.current?.id === before.id
      && before.widgets.every(widget => widgetOwners.current.get(widget.id)?.operationId === operation?.operationId);
    try {
      if (!await ensureSSEConnection(silent)) return;
      sourceAccess().assert(target);
      const sid = streamIdRef.current;
      if (!sid) return;
      if (!silent) { autoRefreshTarget.current = target; autoRefreshSuspended.current = false }
      const request = registerOperation(target, 'refresh');
      operation = request;
      refreshOperation.current = request.operationId;
      associateWidgets(request, activeDashboard.id, activeDashboard.widgets.map(w => w.id));
      setActiveDashboard(prev => prev?.id === before.id ? { ...prev, widgets: prev.widgets.map(w => ({ ...w, is_loading: true, error: undefined,
        page_data_cache: undefined, cursor_stack: undefined, current_page: 1, next_cursor: null, has_more: false })) } : prev);
      activeDashboard.widgets.forEach(w => startLoadingTimeout(w.id, request));
      setIsRefreshing(true);
      for (let attempt = retryCount; ; attempt++) {
        if (!ownsRefresh()) return;
        try {
          await dashboardService.refreshDashboard(chatId, before.id, streamIdRef.current ?? sid, request);
          break;
        } catch (error) {
          if (!ownsRefresh()) return;
          const message = getErrorMessage(error, '');
          if (isSourceAccessDenial(error) || !sourceAccess().view(chatId).allowed || !onReconnect || attempt >= 3
            || !(message.includes('SSE stream not found') || message.includes('connection not found'))) throw error;
          await onReconnect();
          if (!ownsRefresh()) return;
          await new Promise(resolve => setTimeout(resolve, 300));
          if (!ownsRefresh()) return;
        }
      }
      if (!silent && ownsRefresh()) toast.success(zhCN.toasts.dashboardRefreshing, { icon: '✅' });
    } catch (error) {
      if (!sourceAccess().isCurrent(target) || (operation && !ownsRefresh())) return;
      if (operation) failOperation(operation, before, isSourceAccessDenial(error) ? undefined : zhCN.toasts.refreshFailed);
      if (!silent) toast.error(formatServiceError(error).summary || zhCN.toasts.refreshDashboardFailed);
    } finally {
      if (operation) {
        finishHTTP(operation);
        if (refreshOperation.current === operation.operationId) { refreshOperation.current = null; setIsRefreshing(false) }
      }
    }
  }, [activeDashboard, chatId, ensureSSEConnection, startLoadingTimeout, onReconnect, beginExecution, registerOperation, associateWidgets, executionCurrent, failOperation, finishHTTP]);
  refreshDashboardDispatch.current = handleRefreshDashboard;

  // One-time auto-fetch: when a dashboard loads with unfetched widgets, trigger a silent refresh once
  useEffect(() => {
    if (!activeDashboard || !streamId || !isConnected) return;
    if (!sourceAccess().view(chatId).allowed) { hasFetchedOnceRef.current = activeDashboard.id; return; }
    const dashId = activeDashboard.id;
    if (hasFetchedOnceRef.current === dashId) return;
    const hasUnfetchedWidgets = activeDashboard.widgets.some(
      (w) => !w.data && !w.is_loading && !w.error
    );
    if (!hasUnfetchedWidgets) return;
    hasFetchedOnceRef.current = dashId;
    if (autoRefreshTarget.current) void handleRefreshDashboard(true, 0, autoRefreshTarget.current);
  }, [activeDashboard, streamId, isConnected, handleRefreshDashboard]);

  const handleRefreshWidget = useCallback(async (widgetId: string, retryCount = 0, cursor?: string, previous?: SourceTarget, intent: 'page' | 'first-page' = 'page'): Promise<boolean> => {
    if (!activeDashboard) return false;
    const target = await beginExecution(false, previous);
    if (!target) return false;
    const before = activeDashboard;
    let operation: DashboardOperation | null = null;
    try {
      if (!await ensureSSEConnection()) return false;
      sourceAccess().assert(target);
      const sid = streamIdRef.current;
      if (!sid) return false;
      const request = registerOperation(target, 'refresh');
      request.resetPagination = intent === 'first-page';
      operation = request;
      associateWidgets(request, activeDashboard.id, [widgetId]);
      setIndividuallyRefreshingWidgets(prev => new Set(prev).add(widgetId));
      setActiveDashboard(prev => prev?.id === before.id ? { ...prev, widgets: prev.widgets.map(w => w.id === widgetId ? { ...w, is_loading: true, error: undefined } : w) } : prev);
      startLoadingTimeout(widgetId, request);
      await dashboardService.refreshWidget(chatId, activeDashboard.id, widgetId, sid, request, cursor);
      sourceAccess().assert(target);
      return widgetOwners.current.get(widgetId)?.operationId === request.operationId;
    } catch (error) {
      if (!sourceAccess().isCurrent(target) || (operation && (!executionCurrent(operation) || widgetOwners.current.get(widgetId)?.operationId !== operation.operationId))) return false;
      if (operation) failOperation(operation, before, isSourceAccessDenial(error) ? undefined : zhCN.toasts.refreshFailed);
      if (isSourceAccessDenial(error) || !sourceAccess().view(chatId).allowed) { toast.error(formatServiceError(error).summary); return false }
      const message = getErrorMessage(error, '');
      if ((message.includes('SSE stream not found') || message.includes('connection not found')) && onReconnect && retryCount < 3) {
        try {
          await onReconnect();
          sourceAccess().assert(target);
          await new Promise(resolve => setTimeout(resolve, 300));
          sourceAccess().assert(target);
          if (widgetOwners.current.has(widgetId)) return false;
          return await handleRefreshWidget(widgetId, retryCount + 1, cursor, target, intent);
        } catch (reconnectError) { toast.error(formatServiceError(reconnectError).summary || zhCN.toasts.connectionFailedManual) }
      }
      toast.error(formatServiceError(error).summary || zhCN.toasts.refreshFailed);
      return false;
    } finally { if (operation) finishHTTP(operation) }
  }, [activeDashboard, chatId, ensureSSEConnection, startLoadingTimeout, onReconnect, beginExecution, registerOperation, associateWidgets, executionCurrent, failOperation, finishHTTP]);

  const handleWidgetNextPage = useCallback(async (widgetId: string) => {
    if (!activeDashboard) return;

    const widget = activeDashboard.widgets.find(w => w.id === widgetId);
    if (!widget || !widget.table_config?.cursor_field || !widget.has_more || widget.is_loading) return;

    const cursorToUse = widget.next_cursor ?? null;
    const currentPage = widget.current_page || 1;
    const nextPage = currentPage + 1;
    const currentData = widget.data;
    const cachedNextData = widget.page_data_cache?.[nextPage];

    if (cachedNextData) {
      setActiveDashboard((prev) => {
        if (!prev) return null;
        return {
          ...prev,
          widgets: prev.widgets.map((w) =>
            w.id === widgetId ? {
              ...w,
              current_page: nextPage,
              cursor_stack: [...(w.cursor_stack ?? [null]), cursorToUse],
              data: cachedNextData.data,
              next_cursor: cachedNextData.next_cursor,
              has_more: cachedNextData.has_more,
              page_data_cache: {
                ...(w.page_data_cache ?? {}),
                [currentPage]: { data: currentData ?? [], next_cursor: cursorToUse, has_more: true },
              },
            } : w
          ),
        };
      });
      return;
    }
    if (!sourceAccess().view(chatId).allowed) { toast.error(sourceAccess().view(chatId).reason); return; }

    if (!await handleRefreshWidget(widgetId, 0, cursorToUse || undefined)) return;

    setActiveDashboard((prev) => {
      if (!prev) return null;
      return {
        ...prev,
        widgets: prev.widgets.map((w) =>
          w.id === widgetId ? {
            ...w,
            current_page: nextPage,
            cursor_stack: [...(w.cursor_stack ?? [null]), cursorToUse],
            page_data_cache: {
              ...(w.page_data_cache ?? {}),
              [currentPage]: { data: currentData ?? [], next_cursor: cursorToUse, has_more: true },
            },
          } : w
        ),
      };
    });
  }, [activeDashboard, handleRefreshWidget]);

  const handleWidgetPreviousPage = useCallback(async (widgetId: string) => {
    if (!activeDashboard) return;

    const widget = activeDashboard.widgets.find(w => w.id === widgetId);
    if (!widget || !widget.table_config?.cursor_field || widget.is_loading) return;

    const currentStack = widget.cursor_stack ?? [null];
    if (currentStack.length <= 1) return;

    const newStack = currentStack.slice(0, -1);
    const currentPage = widget.current_page || 1;
    const prevPage = Math.max(1, currentPage - 1);
    const cachedData = widget.page_data_cache?.[prevPage];
    const poppedCursor = currentStack[currentStack.length - 1];

    if (cachedData) {
      const currentData = widget.data;
      setActiveDashboard((prev) => {
        if (!prev) return null;
        return {
          ...prev,
          widgets: prev.widgets.map((w) =>
            w.id === widgetId ? {
              ...w,
              current_page: prevPage,
              cursor_stack: newStack,
              data: cachedData.data,
              next_cursor: poppedCursor,
              has_more: true,
              page_data_cache: {
                ...(w.page_data_cache ?? {}),
                [currentPage]: { data: currentData ?? [], next_cursor: w.next_cursor ?? null, has_more: w.has_more ?? false },
              },
            } : w
          ),
        };
      });
    } else {
      if (!sourceAccess().view(chatId).allowed) { toast.error(sourceAccess().view(chatId).reason); return; }
      const prevCursor = newStack[newStack.length - 1];
      if (!await handleRefreshWidget(widgetId, 0, prevCursor ?? undefined)) return;
      setActiveDashboard((prev) => {
        if (!prev) return null;
        return {
          ...prev,
          widgets: prev.widgets.map((w) =>
            w.id === widgetId ? { ...w, current_page: prevPage, cursor_stack: newStack } : w
          ),
        };
      });
    }
  }, [activeDashboard, handleRefreshWidget]);

  const handleManualRefreshWidget = useCallback((widgetId: string) => {
    if (!sourceAccess().view(chatId).allowed) { toast.error(sourceAccess().view(chatId).reason); return; }
    void handleRefreshWidget(widgetId, 0, undefined, undefined, 'first-page');
  }, [handleRefreshWidget]);

  const handleWidgetDataUpdate = useCallback((widgetId: string, event: DashboardWidgetDataEvent, operation: DashboardOperation) => {
    const owner = widgetOwners.current.get(widgetId);
    if (!owner || owner.operationId !== operation.operationId || !executionCurrent(operation)) return;
    clearLoadingTimeout(widgetId);
    retireWidget(operation, widgetId);
    setIndividuallyRefreshingWidgets(prev => {
      if (widgetOwners.current.get(widgetId)?.operationId !== operation.operationId) return prev;
      const next = new Set(prev); next.delete(widgetId); return next;
    });
    const update = (dashboard: Dashboard): Dashboard => ({ ...dashboard, widgets: dashboard.widgets.map(w => w.id === widgetId ? { ...w,
      ...(operation.resetPagination ? { current_page: 1, cursor_stack: [null], page_data_cache: undefined } : {}),
      data: event.data ?? undefined, is_loading: false, error: undefined, last_refreshed_at: new Date().toISOString(), next_cursor: event.next_cursor ?? null, has_more: event.has_more } : w) });
    const cached = _dashCache.get(owner.dashboardId);
    if (cached) _dashCache.set(owner.dashboardId, update(cached));
    if (dashboardRef.current?.id === owner.dashboardId) dashboardRef.current = update(dashboardRef.current);
    setActiveDashboard(prev => prev?.id === owner.dashboardId && executionCurrent(operation) && widgetOwners.current.get(widgetId)?.operationId === operation.operationId ? update(prev) : prev);
  }, [clearLoadingTimeout, retireWidget, executionCurrent]);

  const isReconnectingRef = useRef(false);

  const handleWidgetErrorUpdate = useCallback((widgetId: string, error: string, operation: DashboardOperation) => {
    const owner = widgetOwners.current.get(widgetId);
    if (!owner || owner.operationId !== operation.operationId || !executionCurrent(operation)) return;
    clearLoadingTimeout(widgetId);
    retireWidget(operation, widgetId);
    setIndividuallyRefreshingWidgets(prev => {
      if (widgetOwners.current.get(widgetId)?.operationId !== operation.operationId) return prev;
      const next = new Set(prev); next.delete(widgetId); return next;
    });
    const stillOwned = () => executionCurrent(operation) && widgetOwners.current.get(widgetId)?.operationId === operation.operationId;
    const showError = () => setActiveDashboard(prev => prev?.id === owner.dashboardId && stillOwned()
      ? { ...prev, widgets: prev.widgets.map(w => w.id === widgetId ? { ...w, is_loading: false, error } : w) } : prev);
    const dashboard = dashboardRef.current;
    if (dashboard?.id === owner.dashboardId && error.toLowerCase().includes('no connection found') && onReconnect && !isReconnectingRef.current) {
      isReconnectingRef.current = true;
      toast.loading('正在重新连接数据库…', { id: 'dashboard-reconnect', duration: 5000 });
      onReconnect().then(async () => {
        if (!stillOwned() || !streamIdRef.current) return;
        const currentDashboard = dashboardRef.current;
        if (!currentDashboard || currentDashboard.id !== owner.dashboardId) return;
        if (currentDashboard.widgets.some(w => {
          const slot = widgetOwners.current.get(w.id);
          const pending = slot ? operations.current.get(slot.operationId) : undefined;
          return pending && pending.operationId !== operation.operationId && pending.widgets.has(w.id);
        })) return;
        sourceAccess().assert(operation.target);
        const recovery = registerOperation(operation.target, 'refresh');
        associateWidgets(recovery, currentDashboard.id, currentDashboard.widgets.map(w => w.id));
        currentDashboard.widgets.forEach(w => startLoadingTimeout(w.id, recovery));
        setActiveDashboard(prev => prev?.id === currentDashboard.id ? { ...prev, widgets: prev.widgets.map(w => ({ ...w, is_loading: true, error: undefined })) } : prev);
        try { await dashboardService.refreshDashboard(chatId, currentDashboard.id, streamIdRef.current, recovery) }
        catch (failure) { failOperation(recovery, currentDashboard, formatServiceError(failure).summary) }
        finally { finishHTTP(recovery) }
      }).catch(() => { if (stillOwned()) { toast.error('重连失败，请手动重试。', { id: 'dashboard-reconnect' }); showError() } })
        .finally(() => { isReconnectingRef.current = false });
      return;
    }
    showError();
  }, [onReconnect, chatId, clearLoadingTimeout, retireWidget, executionCurrent, registerOperation, associateWidgets, startLoadingTimeout, failOperation, finishHTTP]);

  const ensureConnection = (): boolean => {
    if (!sourceAccess().view(chatId).allowed) { toast.error(sourceAccess().view(chatId).reason); return false; }
    if (!streamId || !isConnected) {
      toast.error(zhCN.toasts.notActive);
      return false;
    }
    return true;
  };

  const handleRefreshIntervalChange = async (interval: number) => {
    if (!activeDashboard) return;
    const target = sourceAccess().capture(chatId);
    try {
      await dashboardService.updateDashboard(chatId, activeDashboard.id, {
        // Interval changes are metadata; a future tick still needs source admission.
        refresh_interval: interval,
      });
      sourceAccess().assertCurrent(target);
      setActiveDashboard((prev) => prev ? { ...prev, refresh_interval: interval } : null);
      autoRefreshTarget.current = target;
      try { sourceAccess().assert(target); autoRefreshSuspended.current = false } catch { autoRefreshSuspended.current = true }
    } catch {
      toast.error('更新刷新间隔失败');
    }
  };

  const handleDeleteDashboard = async () => {
    if (!activeDashboard) return;
    try {
      await dashboardService.deleteDashboard(chatId, activeDashboard.id);
      const deletedId = activeDashboard.id;
      setActiveDashboard(null);
      _dashCache.delete(deletedId);
      setDashboardList((prev) => {
        const newList = prev.filter((d) => d.id !== deletedId);
        _listCache.set(chatId, { list: newList, activeDashId: newList[0]?.id });
        return newList;
      });
      setShowDeleteDashboardConfirm(false);
      toast.success('仪表盘已删除', { icon: '🗑️' });

      const remaining = dashboardList.filter((d) => d.id !== deletedId);
      if (remaining.length > 0) {
        await loadDashboard(remaining[0].id);
      }
    } catch {
      toast.error('删除仪表盘失败');
    }
  };

  const handleDeleteWidget = async (widgetId: string) => {
    if (!activeDashboard) return;
    try {
      await dashboardService.deleteWidget(chatId, activeDashboard.id, widgetId);
      setActiveDashboard((prev) => {
        if (!prev) return null;
        return {
          ...prev,
          widgets: prev.widgets.filter((w) => w.id !== widgetId),
          layout: prev.layout.filter((l) => l.widget_id !== widgetId),
        };
      });
      toast.success('组件已移除', { icon: <Trash2 size={18} className='text-red-500' /> });
    } catch {
      toast.error('删除组件失败');
    }
  };

  const refreshMutatedWidget = async (dashboardId: string, widgetId: string, target: SourceTarget) => {
    const sid = streamIdRef.current;
    if (!sid || !isConnectedRef.current) return;
    const operation = registerOperation(target, 'refresh');
    associateWidgets(operation, dashboardId, [widgetId]);
    startLoadingTimeout(widgetId, operation);
    try { await dashboardService.refreshWidget(chatId, dashboardId, widgetId, sid, operation) }
    catch (error) { failOperation(operation, undefined, formatServiceError(error).summary) }
    finally { finishHTTP(operation) }
  };

  const handleEditWidget = async (widgetId: string, prompt: string) => {
    if (!activeDashboard) return;
    setIsEditingWidget(true);
    try {
      const target = await beginExecution();
      if (!target) return;
      sourceAccess().assert(target);
      const updatedWidget = await dashboardService.editWidget(
        chatId,
        activeDashboard.id,
        widgetId,
        { prompt }
      );
      sourceAccess().assert(target);
      setActiveDashboard((prev) => {
        if (!prev) return null;
        return {
          ...prev,
          widgets: prev.widgets.map((w) =>
            w.id === widgetId ? { ...w, ...updatedWidget, data: w.data, is_loading: true, error: undefined } : w
          ),
        };
      });
      setEditingWidgetId(null);
      toast.success('组件已更新！');

      await refreshMutatedWidget(activeDashboard.id, widgetId, target);
    } catch {
      toast.error('更新组件失败');
    } finally {
      setIsEditingWidget(false);
    }
  };

  const requestSuggestions = async (prompt?: string) => {
    const target = await beginExecution();
    if (!target || !ensureConnection() || !streamId) return;
    const operation = registerOperation(target, 'suggestions', 1);
    blueprintOrigin.current = null;
    setBlueprints([]);
    setSelectedBlueprintIndices(new Set());
    setShowBlueprintPicker(false);
    setIsGeneratingBlueprints(true);
    setGenerationContext(prompt ? 'custom' : 'blueprints');
    setGenerationProgress({ status: 'generating', message: '正在分析数据库…', progress: 5 });
    const controller = new AbortController();
    abortControllerRef.current = controller;
    try {
      await dashboardService.generateBlueprints(chatId, streamId, operation, prompt, controller.signal);
      sourceAccess().assert(target);
      if (generationOperation.current === operation.operationId && prompt) setShowPromptModal(false);
    } catch (error) {
      if (generationOperation.current !== operation.operationId || controller.signal.aborted) return;
      toast.error(formatServiceError(error).summary || '生成推荐失败');
      setIsGeneratingBlueprints(false);
      setGenerationProgress(null);
    } finally { finishHTTP(operation) }
  };

  const handleExploreSuggestions = () => requestSuggestions();

  const handleCreateFromBlueprints = async () => {
    const origin = blueprintOrigin.current;
    const indices = Array.from(selectedBlueprintIndices);
    if (!streamId || !isConnected || indices.length === 0) return;
    let operation: DashboardOperation | null = null;
    try {
      if (!origin) { toast.error(zhCN.sourceAccess.changed); return }
      sourceAccess().assert(origin.target);
      const target = await beginExecution();
      if (!target) return;
      sourceAccess().assert(origin.target);
      if (blueprintOrigin.current !== origin) { toast.error(zhCN.sourceAccess.changed); return }
      const request = registerOperation(target, 'generation', indices.length);
      operation = request;
      setIsCreatingFromBlueprints(true);
      setGenerationContext('creating');
      setGenerationProgress({ status: 'generating', message: '正在构建仪表盘…', progress: 10 });
      const controller = new AbortController();
      abortControllerRef.current = controller;
      await dashboardService.createFromBlueprints(chatId, streamId, { blueprint_indices: indices, blueprint_origin: origin.reference }, request, controller.signal);
      sourceAccess().assert(origin.target);
      sourceAccess().assert(target);
      if (generationOperation.current === request.operationId) setShowBlueprintPicker(false);
    } catch (error) {
      if (operation) {
        if (generationOperation.current !== operation.operationId) return;
        setIsCreatingFromBlueprints(false);
        setGenerationProgress(null);
      }
      toast.error(formatServiceError(error).summary || '创建仪表盘失败');
    } finally { if (operation) finishHTTP(operation) }
  };

  const handleCreateWithAI = () => {
    if (!ensureConnection()) return;
    setShowPromptModal(true);
  };

  const handleSubmitAIPrompt = (prompt: string) => requestSuggestions(prompt);

  const handleAddWidget = async (prompt: string) => {
    if (!activeDashboard) return;
    setIsAddingWidget(true);
    try {
      const target = await beginExecution();
      if (!target) return;
      sourceAccess().assert(target);
      const widget = await dashboardService.addWidget(chatId, activeDashboard.id, { prompt });
      sourceAccess().assert(target);
      setActiveDashboard((prev) => {
        if (!prev) return null;
        return { ...prev, widgets: [...prev.widgets, { ...widget, data: undefined, is_loading: true, error: undefined }] };
      });
      setShowAddWidgetModal(false);
      toast.success(`组件「${widget.title}」已添加！`);

      await refreshMutatedWidget(activeDashboard.id, widget.id, target);
    } catch {
      toast.error('添加组件失败');
    } finally {
      setIsAddingWidget(false);
    }
  };

  const handleRegenerateDashboard = async (reason: 'try_another_variant' | 'schema_changed', customInstructions?: string) => {
    const target = await beginExecution();
    if (!target) return;
    if (!activeDashboard) return;

    if (!streamId || !isConnected) {
      toast.error('连接未激活，请先重连后再重新生成仪表盘。', {
        icon: '🔌',
        duration: 4000,
      });
      return;
    }
    const operation = registerOperation(target, 'generation', 1);

    setGenerationContext('creating');
    setGenerationProgress({ status: 'generating', message: '正在重新生成仪表盘…', progress: 5 });

    const controller = new AbortController();
    abortControllerRef.current = controller;

    try {
      sourceAccess().assert(target);
      await dashboardService.regenerateDashboard(chatId, activeDashboard.id, streamId, {
        reason,
        custom_instructions: customInstructions,
      }, operation, controller.signal);
      sourceAccess().assert(target);
      if (generationOperation.current === operation.operationId) setShowRegenerateModal(false);
    } catch (error) {
      if (generationOperation.current !== operation.operationId || controller.signal.aborted) return;
      toast.error(formatServiceError(error).summary || '重新生成仪表盘失败');
      setGenerationProgress(null);
    } finally { finishHTTP(operation) }
  };

  const handleCancelGeneration = () => {
    const operation = generationOperation.current ? operations.current.get(generationOperation.current) : undefined;
    if (operation) discardOperation(operation);
    generationOperation.current = null;
    blueprintOrigin.current = null;
    setSelectedBlueprintIndices(new Set());
    abortControllerRef.current?.abort();
    abortControllerRef.current = null;
    setGenerationProgress(null);
    setIsGeneratingBlueprints(false);
    setIsCreatingFromBlueprints(false);
    setBlueprints([]);
    setShowBlueprintPicker(false);
    toast.error('已取消生成', { icon: '✕' });
  };

  const handleNewDashboard = () => {
    if (!ensureConnection()) return;
    setShowPromptModal(true);
  };

  const handleExportDashboard = async () => {
    if (!activeDashboard) return;

    try {
      const blob = await dashboardService.exportDashboard(chatId, activeDashboard.id);
      const url = globalThis.URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${activeDashboard.name.replace(/\s+/g, '-').toLowerCase()}-export.json`;
      document.body.appendChild(a);
      a.click();
      globalThis.URL.revokeObjectURL(url);
      a.remove();
      toast.success('仪表盘导出成功', { icon: '📥' });
    } catch (error) {
      console.error('Export failed:', error);
      toast.error('导出仪表盘失败');
    }
  };

  const handleImportDashboard = () => {
    setShowImportModal(true);
  };

  const handleImportSubmit = async (jsonContent: string) => {
    setImportJsonContent(jsonContent);
    setIsImporting(true);

    try {
      const result = await dashboardService.importDashboard(chatId, {
        json: jsonContent,
        mappings: {},
        options: {
          skipInvalidWidgets: false,
          autoCreateConnections: false,
        },
      });

      setShowImportModal(false);
      setImportJsonContent('');

      await loadDashboardList();
      await loadDashboard(result.dashboardId);

      const warnings = result.summary.warnings?.length || 0;

      if (warnings > 0) {
        toast.success(
          `已导入仪表盘（${result.summary.widgetsImported} 个组件）。部分查询可能需要重新生成。`,
          { icon: '⚠️', duration: 5000 }
        );
      } else {
        toast.success(
          `仪表盘导入成功，共 ${result.summary.widgetsImported} 个组件！`,
          { icon: '✅' }
        );
      }
    } catch (error) {
      console.error('Import failed:', error);
      toast.error(formatServiceError(error).summary || '导入仪表盘失败');
    } finally {
      setIsImporting(false);
    }
  };

  return {
    // State
    isLoadingList,
    dashboardList,
    activeDashboard,
    isLoadingDashboard,
    isRefreshing,
    individuallyRefreshingWidgets,
    blueprints,
    showBlueprintPicker,
    selectedBlueprintIndices,
    isCreatingFromBlueprints,
    generationProgress,
    generationContext,
    showPromptModal,
    showAddWidgetModal,
    isAddingWidget,
    showRegenerateModal,
    showDeleteDashboardConfirm,
    showImportModal,
    isImporting,
    importJsonContent,
    editingWidgetId,
    isEditingWidget,
    // Setters (for inline handlers in render)
    setSelectedBlueprintIndices,
    setShowBlueprintPicker,
    setShowPromptModal,
    setShowAddWidgetModal,
    setEditingWidgetId,
    setShowRegenerateModal,
    setShowDeleteDashboardConfirm,
    setShowImportModal,
    // Navigation / loading
    loadDashboard,
    // Dashboard actions
    handleRefreshDashboard,
    handleCancelDashboardRefresh,
    handleRefreshIntervalChange,
    handleNewDashboard,
    handleRegenerateDashboard,
    handleDeleteDashboard,
    handleExportDashboard,
    handleImportDashboard,
    handleImportSubmit,
    // Widget actions
    handleManualRefreshWidget,
    handleCancelWidgetRefresh,
    handleWidgetNextPage,
    handleWidgetPreviousPage,
    handleDeleteWidget,
    handleEditWidget,
    handleAddWidget,
    // AI / blueprint actions
    handleExploreSuggestions,
    handleCreateFromBlueprints,
    handleCreateWithAI,
    handleSubmitAIPrompt,
    handleCancelGeneration,
  };
}
