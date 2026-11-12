import { useEffect, useState } from 'react';
import { AlertCircle, BarChart3, Loader, PencilRuler, RefreshCcw } from 'lucide-react';
import toast from 'react-hot-toast';
import chatService from '../../services/chatService';
import ChartRenderer from '../ChartRenderer';
import { QueryResult } from '../../types/query';
import { formatServiceError, zhCN } from '../../i18n/zh-CN';
import ServiceErrorView from '../../i18n/ServiceErrorView';

import { useSourceAccess } from '../../hooks/useSourceAccess';
interface VisualizationState {
    loading: boolean;
    error: string | null;
    isGenerating: boolean;
    notSupported?: boolean;
}

interface VisualizationPanelProps {
    chatId: string;
    messageId: string;
    query: QueryResult;
    userId?: string;
    userName?: string;
    /** Initial visualization value pulled from the message (may be undefined). */
    initialVisualization?: QueryResult['visualization'];
    /** Called when a new visualization is saved so the parent can persist it on the message. */
    onVisualizationSaved?: (queryId: string, vizData: NonNullable<QueryResult['visualization']>) => void;
}

const toastStyle = {
    style: {
        background: '#000', color: '#fff', border: '4px solid #000', borderRadius: '12px',
        boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)', padding: '12px 24px',
        fontSize: '14px', fontWeight: '500',
    },
    position: 'bottom-center' as const,
    duration: 2000,
};

export default function VisualizationPanel({
    chatId,
    messageId,
    query,
    initialVisualization,
    onVisualizationSaved,
}: VisualizationPanelProps) {
    const access = useSourceAccess(chatId);
    const [vizState, setVizState] = useState<VisualizationState>(() => {
        if (initialVisualization?.can_visualize === false) {
            return {
                loading: false,
                error: initialVisualization.reason || zhCN.viz.notSupported,
                isGenerating: false,
                notSupported: true,
            };
        }
        if (initialVisualization?.can_visualize) {
            return { loading: false, error: null, isGenerating: false };
        }
        return { loading: false, error: null, isGenerating: false };
    });

    const [visualization, setVisualization] = useState<QueryResult['visualization'] | null>(
        initialVisualization?.can_visualize && (initialVisualization.chart_data?.length ?? 0) > 0
            ? initialVisualization
            : null,
    );
    const [dataLoading, setDataLoading] = useState(false);
    const [dataError, setDataError] = useState('');

    // Sync when the parent message updates the query's visualization field
    useEffect(() => {
        if (!initialVisualization) return;
        if (initialVisualization.can_visualize === false) {
            setVizState({
                loading: false,
                error: initialVisualization.reason || zhCN.viz.notSupported,
                isGenerating: false,
                notSupported: true,
            });
        } else if (initialVisualization.can_visualize && (initialVisualization.chart_data?.length ?? 0) > 0) {
            setVisualization(initialVisualization);
            setVizState({ loading: false, error: null, isGenerating: false });
        }
    }, [initialVisualization]);

    const handleGenerate = async () => {
        if (!access.allowed) { toast.error(access.reason); return; }
        setVizState({ loading: false, error: null, isGenerating: true });
        try {
            const response = await chatService.generateVisualization(chatId, messageId, query.id);
            if (response?.success && response?.data) {
                if (response.data.can_visualize === false) {
                    const reason = response.data.reason || zhCN.viz.notSupported;
                    setVizState({ loading: false, error: reason, isGenerating: false, notSupported: true });
                    return;
                }
                if (!response.data.chart_data || response.data.chart_data.length === 0) {
                    setVizState({ loading: false, error: zhCN.viz.noData, isGenerating: false });
                    return;
                }
                setVisualization(response.data);
                setVizState({ loading: false, error: null, isGenerating: false });
                onVisualizationSaved?.(query.id, response.data);
                toast(zhCN.viz.generated, { ...toastStyle, icon: '📊' });
            } else {
                const msg = response?.data?.reason || zhCN.viz.failed;
                setVizState({ loading: false, error: msg, isGenerating: false });
                toast.error(`${zhCN.viz.failedPrefix}${msg}`);
            }
        } catch (error: unknown) {
            const mapped = formatServiceError(error);
            if (visualization) {
                setVizState({ loading: false, error: null, isGenerating: false });
                setDataError(mapped.summary);
            } else setVizState({ loading: false, error: mapped.summary, isGenerating: false });
            toast.error(`${zhCN.viz.errorPrefix}${mapped.summary}`);
        }
    };

    const handleLoadData = async () => {
        if (!access.allowed) { setDataError(access.reason); return; }
        if (!query.visualization) return;
        const savedVisualization = query.visualization;
        setDataLoading(true);
        setDataError('');
        try {
            const response = await chatService.getVisualizationData(chatId, messageId, query.id);
            if (response?.success && response?.data) {
                const data = response.data;
                setVisualization(prev => ({
                    ...savedVisualization,
                    ...prev,
                    chart_data: data.chart_data,
                    total_records: data.total_records,
                    returned_count: data.returned_count,
                    updated_at: data.updated_at,
                }));
            } else {
                setDataError(response?.data?.reason || zhCN.viz.loadFailed);
            }
        } catch (error: unknown) {
            setDataError(formatServiceError(error).summary || zhCN.viz.loadFailed);
        } finally {
            setDataLoading(false);
        }
    };

    // ── Rendering ──────────────────────────────────────────────────────────────

    if (vizState.isGenerating) {
        return (
            <div className="flex flex-row items-center justify-center py-12 gap-3">
                <Loader className="w-6 h-6 animate-spin text-gray-400" />
                <p className="text-gray-400">{zhCN.viz.generating}</p>
            </div>
        );
    }

    if (vizState.error) {
        if (vizState.notSupported) {
            const reason = formatServiceError(vizState.error);
            return (
                <div className="bg-gray-800/50 rounded-lg p-8 text-center border border-gray-700">
                    <div className="flex justify-center mb-4">
                        <AlertCircle className="w-8 h-8 text-gray-400" />
                    </div>
                    <div className="text-white font-semibold mb-2 text-base">{zhCN.viz.notSupportedTitle}</div>
                    <p className="text-gray-400 text-sm mb-4">{reason.summary}</p>
                </div>
            );
        }
        return (
            <div className="bg-neo-error/10 rounded-lg p-8 text-center text-neo-error">
                <div className="font-semibold mb-3">{zhCN.viz.couldNot}</div>
                <ServiceErrorView error={vizState.error} />
                <button disabled={!access.allowed} title={access.reason} onClick={handleGenerate} className="px-4 py-2 mt-6 bg-white text-gray-900 font-semibold rounded hover:bg-gray-200 transition-colors">
                    <RefreshCcw className="w-4 h-4 inline-block mr-2" />
                    {zhCN.chat.tryAgain}
                </button>
            </div>
        );
    }

    if (visualization?.chart_configuration && visualization.chart_data && visualization.chart_data.length > 0) {
        return (
            <div className="w-full" data-query-id={query.id}>
                {dataError && <p role="alert">{dataError}</p>}
                <ChartRenderer
                    config={visualization.chart_configuration}
                    data={visualization.chart_data}
                    onRetry={access.allowed ? handleGenerate : undefined}
                    onRegenerate={access.allowed ? handleGenerate : undefined}
                    updatedAt={visualization.updated_at}
                />
            </div>
        );
    }

    // Visualization metadata exists but data not yet loaded
    if (query.visualization?.can_visualize && !visualization?.chart_data) {
        return (
            <div className="bg-green-900/20 rounded-lg p-8 text-center border border-green-700/40">
                <div className="mb-6">
                    <BarChart3 className="w-8 h-8 text-green-400 mx-auto mb-4" />
                    <p className="text-white text-base font-semibold mb-2">{zhCN.viz.ready}</p>
                    <p className="text-gray-400 text-sm">{zhCN.viz.clickToLoad}</p>
                </div>
                <button
                    onClick={handleLoadData}
                    disabled={dataLoading || !access.allowed}
                    title={access.reason}
                    className="px-4 py-2 bg-green-600 text-white font-semibold rounded hover:bg-green-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                >
                    {dataLoading ? (
                        <><Loader className="w-4 h-4 inline-block mr-2 animate-spin" />{zhCN.common.loading}</>
                    ) : (
                        <><PencilRuler className="w-4 h-4 inline-block mr-2" />{zhCN.viz.load}</>
                    )}
                </button>
                {dataError && <div className="text-red-400 text-xs mt-3">{dataError}</div>}
            </div>
        );
    }

    // No visualization yet — prompt to generate
    return (
        <div className="bg-blue-900/20 rounded-lg p-8 text-center border border-blue-700/40">
            <div className="mb-6">
                <PencilRuler className="w-8 h-8 text-blue-400 mx-auto mb-4" />
                <p className="text-white text-base font-semibold mb-2">{zhCN.viz.title}</p>
                <p className="text-gray-400 text-sm">
                    {zhCN.viz.subtitle}
                </p>
            </div>
            <button disabled={!access.allowed} title={access.reason} onClick={handleGenerate} className="px-4 py-2 bg-blue-600 text-white font-semibold rounded hover:bg-blue-700 transition-colors">
                <PencilRuler className="w-4 h-4 inline-block mr-2" />
                {zhCN.viz.generate}
            </button>
        </div>
    );
}
