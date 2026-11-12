import { ArrowDown, Loader2, Pin, RefreshCcw } from 'lucide-react';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { useStream } from '../../contexts/StreamContext';
import axios from '../../services/axiosConfig';
import chatService from '../../services/chatService';
import analyticsService from '../../services/analyticsService';
import { Chat, Connection } from '../../types/chat';
import type { LLMModel } from '../../types/chat';
import { Message } from '../../types/query';
import { ChatSettings } from '../../types/chat';
import { DashboardViewMode } from '../../types/dashboard';
import { useMessageSearch } from '../../hooks/useMessageSearch';
import { useScrollManagement } from '../../hooks/useScrollManagement';
import { useMessageFetching } from '../../hooks/useMessageFetching';
import { useConnectionActions } from '../../hooks/useConnectionActions';
import ConfirmationModal from '../modals/ConfirmationModal';
import ConnectionModal from '../modals/ConnectionModal';
import ChatHeader from './ChatHeader';
import MessageInput from './MessageInput';
import MessageTile from './MessageTile';
import SearchBar from './SearchBar';
import DashboardView from '../dashboard/DashboardView';
import { formatDateDivider, formatServiceError, zhCN } from '../../i18n/zh-CN';
import AssistantInputPanel from '../assistant/AssistantInputPanel';
import WorkflowMenu from '../assistant/WorkflowMenu';
import type { AnalysisRequest, Workflow } from '../../types/assistant';
import { sourceAccess, type SourceTarget } from '../../contexts/SourceAccessContext';
import { useSourceAccess } from '../../hooks/useSourceAccess';
import { useDataSyncJob } from '../../hooks/useDataSyncJob';
import DataSyncModal from '../modals/DataSyncModal';


const groupMessagesByDate = (messages: Message[]) => {
    const groups: { [key: string]: Message[] } = {};
    const sorted = [...messages].sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime());
    sorted.forEach(message => {
        const date = new Date(message.created_at).toDateString();
        if (!groups[date]) groups[date] = [];
        groups[date].push(message);
    });
    return Object.fromEntries(
        Object.entries(groups).sort((a, b) => new Date(a[0]).getTime() - new Date(b[0]).getTime()),
    );
};

// ─── Props ────────────────────────────────────────────────────────────────────
interface ChatWindowProps {
    chat: Chat;
    isExpanded: boolean;
    messages: Message[];
    setMessages: React.Dispatch<React.SetStateAction<Message[]>>;
    onSendMessage: (message: string, llmModel?: string) => Promise<boolean>;
    onEditMessage: (id: string, content: string) => Promise<boolean>;
    onClearChat: () => void;
    onCloseConnection: () => void;
    onEditConnection?: (id: string, connection: Connection, settings: ChatSettings) => Promise<{ success: boolean; error?: string; updatedChat?: Chat }>;
    onConnectionStatusChange?: (chatId: string, isConnected: boolean, from: string) => void;
    isConnected: boolean;
    onCancelStream: () => void;
    onRefreshSchema: () => Promise<void>;
    onCancelRefreshSchema: () => void;
    checkSSEConnection: () => Promise<void>;
    onUpdateSelectedCollections?: (chatId: string, selectedCollections: string) => Promise<void>;
    onEditConnectionFromChatWindow?: () => void;
    userId?: string;
    userName?: string;
    recoVersion?: number;
    llmModels?: LLMModel[];
    onOpenModelProviders?: () => void;
    isLoadingModels?: boolean;
}

interface QueryState {
    isExecuting: boolean;
    isExample: boolean;
}

// ─── Component ────────────────────────────────────────────────────────────────
export default function ChatWindow({
    chat, onEditMessage, isExpanded,
    messages, setMessages,
    onSendMessage, onClearChat, onCloseConnection,
    onEditConnection, onConnectionStatusChange,
    isConnected, onCancelStream,
    onRefreshSchema, onCancelRefreshSchema,
    checkSSEConnection,
    onUpdateSelectedCollections, onEditConnectionFromChatWindow,
    userId, userName, recoVersion,
    llmModels = [],
    onOpenModelProviders, isLoadingModels = false,
}: ChatWindowProps) {
    const { streamId } = useStream();
    const access = useSourceAccess(chat.id);
    const [syncWindow, setSyncWindow] = useState<{ target: SourceTarget; returnFocus: HTMLElement | null } | null>(null);
    const syncVisible = !!syncWindow && !!access.coordinator && access.coordinator.isOwnerCurrent(syncWindow.target)
        && syncWindow.target.chatId === chat.id && syncWindow.target.binding === access.coordinator.capture(chat.id).binding;
    const sync = useDataSyncJob(chat, syncVisible);
    const queryTimeouts = useRef<Record<string, NodeJS.Timeout>>({});

    // ── Local UI state ────────────────────────────────────────────────────────
    const [editingMessageId, setEditingMessageId] = useState<string | null>(null);
    const [editInput, setEditInput] = useState('');
    const [showClearConfirm, setShowClearConfirm] = useState(false);
    const [showRefreshSchema, setShowRefreshSchema] = useState(false);
    const [showCloseConfirm, setShowCloseConfirm] = useState(false);
    const [queryStates, setQueryStates] = useState<Record<string, QueryState>>({});
    const [showEditConnection, setShowEditConnection] = useState(false);
    const [openWithSettingsTab, setOpenWithSettingsTab] = useState(false);
    const [_isMessageSending] = useState(false);
    const [viewMode, setViewMode] = useState<'chats' | 'pinned'>('chats');
    const [dashboardViewMode, setDashboardViewMode] = useState<DashboardViewMode>('chat');
    const [selectedLLMModel, setSelectedLLMModel] = useState<string | undefined>(undefined);
    const [selectedReasoningLevel, setSelectedReasoningLevel] = useState<string>('');
    const [recommendations, setRecommendations] = useState<string[]>([]);
    const [isLoadingRecommendations, setIsLoadingRecommendations] = useState(false);
    const [inputPrefill, setInputPrefill] = useState<string | null>(null);
    const [assistantMessageToReveal, setAssistantMessageToReveal] = useState<string | null>(null);
    const [workflowState, setWorkflowState] = useState<{ chatId: string; open: boolean; workflow: Workflow | null }>({ chatId: chat.id, open: false, workflow: null });
    const chatGeneration = useMemo(() => ({ chatId: chat.id }), [chat.id, chat.user_id, chat.source_id, chat.source_binding_revision]);
    const activeChatGeneration = useRef<typeof chatGeneration | null>(chatGeneration);
    const workflowOpen = workflowState.chatId === chat.id && workflowState.open;
    const selectedWorkflow = workflowState.chatId === chat.id ? workflowState.workflow : null;
    useLayoutEffect(() => {
        activeChatGeneration.current = chatGeneration;
        setWorkflowState({ chatId: chat.id, open: false, workflow: null });
        setAssistantMessageToReveal(null);
        return () => { activeChatGeneration.current = null; };
    }, [chat.id, chatGeneration]);
    useEffect(() => {
        if (viewMode !== 'chats' || dashboardViewMode !== 'chat') {
            setWorkflowState(current => ({ ...current, open: false }));
        }
    }, [viewMode, dashboardViewMode]);
    const [, setShimmerTexts] = useState<string[]>([]);
    const lastRecoKeyRef = useRef<string | null>(null);
    const [showEditQueryConfirm, setShowEditQueryConfirm] = useState<{
        show: boolean; messageId: string | null; queryId: string | null; query: string | null;
    }>({ show: false, messageId: null, queryId: null, query: null });

    // ── Search ────────────────────────────────────────────────────────────────
    const search = useMessageSearch({ messages });

    // isLoadingMessages is hoisted here so both scroll + fetching hooks share it
    const [isLoadingMessages, setIsLoadingMessages] = useState(false);

    // ── Scroll management ─────────────────────────────────────────────────────
    const scroll = useScrollManagement({ messages, isLoadingMessages, viewMode });

    // ── Message fetching ──────────────────────────────────────────────────────
    const fetching = useMessageFetching({
        chatId: chat.id,
        pageSize: 25,
        setMessages,
        scrollToBottom: scroll.scrollToBottom,
        preserveScroll: scroll.preserveScroll,
        chatContainerRef: scroll.chatContainerRef,
        messageUpdateSource: scroll.messageUpdateSource,
        isLoadingOldMessages: scroll.isLoadingOldMessages,
        isInitialLoad: scroll.isInitialLoad,
        viewMode,
        showSearch: search.showSearch,
        setShowSearch: search.setShowSearch,
        setSearchQuery: search.setSearchQuery,
        setSearchResults: search.setSearchResults,
        setCurrentSearchIndex: search.setCurrentSearchIndex,
        isLoadingMessages,
        setIsLoadingMessages,
    });

    // ── Connection actions ────────────────────────────────────────────────────
    const conn = useConnectionActions({
        chatId: chat.id,
        checkSSEConnection,
        onCloseConnection,
        onConnectionStatusChange,
    });

    // ── LLM model ─────────────────────────────────────────────────────────────
    useEffect(() => {
        if (chat?.preferred_llm_model) { setSelectedLLMModel(chat.preferred_llm_model); }
        else if (llmModels?.length > 0) {
            const def = llmModels.find(m => 'default' in m && m.default === true);
            if (def) setSelectedLLMModel(def.id);
        }
        setSelectedReasoningLevel(chat?.preferred_reasoning_level ?? '');
    }, [chat?.id, chat?.preferred_llm_model, chat?.preferred_reasoning_level, llmModels]);

    useEffect(() => {
        if (isConnected) conn.setIsConnecting(false);
    }, [isConnected, conn]);

    // ── Recommendations ───────────────────────────────────────────────────────
    useEffect(() => {
        if (!chat?.id) return;
        if (!sourceAccess().view(chat.id).allowed) return;
        let cancelled = false;
        const key = `${chat.id}-${recoVersion}`;
        if (lastRecoKeyRef.current === key) return;

        setIsLoadingRecommendations(true);
        setShimmerTexts([
            '推荐问题加载中…',
            '正在准备推荐问题…',
            '正在分析可用数据…',
            '为你挑选推荐问题…',
        ]);

        (async () => {
            try {
                lastRecoKeyRef.current = key;
                const resp = await chatService.getQueryRecommendations(chat.id, streamId || undefined);
                if (!cancelled) {
                    setRecommendations(resp.success && resp.data?.recommendations
                        ? resp.data.recommendations.map(r => r.text)
                        : []);
                }
            } catch {
                if (!cancelled) setRecommendations([]);
            } finally {
                if (!cancelled) setIsLoadingRecommendations(false);
            }
        })();
        return () => { cancelled = true; };
    }, [recoVersion, chat?.id, streamId]);

    // ── Restore scroll on view mode switch ───────────────────────────────────
    useEffect(() => {
        if (viewMode === 'pinned' && chat?.id && fetching.pinnedMessages.length === 0) {
            fetching.fetchPinnedMessages();
        }
        setTimeout(() => {
            if (scroll.chatContainerRef.current) {
                scroll.chatContainerRef.current.scrollTop = scroll.scrollPositions.current[viewMode];
            }
        }, 50);
    }, [viewMode, chat?.id]); // eslint-disable-line react-hooks/exhaustive-deps

    // ── Message helpers ───────────────────────────────────────────────────────
    const setMessage = useCallback((message: Message) => {
        setMessages(prev => prev.map(m => m.id === message.id ? message : m));
        if (message.is_pinned !== undefined) fetching.fetchPinnedMessages();
    }, [setMessages, fetching]);

    const handleEditMessage = useCallback((id: string) => {
        scroll.messageUpdateSource.current = 'query';
        const msg = messages.find(m => m.id === id);
        if (msg) { setEditingMessageId(id); setEditInput(msg.content); }
        setTimeout(() => { scroll.messageUpdateSource.current = null; }, 200);
    }, [messages, scroll.messageUpdateSource]);

    const handleCancelEdit = useCallback(() => {
        scroll.messageUpdateSource.current = 'query';
        setEditingMessageId(null);
        setEditInput('');
        setTimeout(() => { scroll.messageUpdateSource.current = null; }, 200);
    }, [scroll.messageUpdateSource]);

    const handleLLMModelChange = useCallback(async (modelId: string) => {
        setSelectedLLMModel(modelId);
        try {
            await axios.patch(
                `${import.meta.env.VITE_API_URL}/chats/${chat.id}`,
                { preferred_llm_model: modelId },
                { withCredentials: true, headers: { 'Authorization': `Bearer ${localStorage.getItem('token')}` } },
            );
            if (chat) chat.preferred_llm_model = modelId;
        } catch (error) {
            console.error('Failed to save preferred LLM model:', error);
        }
    }, [chat]);

    const handleReasoningChange = useCallback(async (level: string) => {
        setSelectedReasoningLevel(level);
        try {
            await axios.patch(
                `${import.meta.env.VITE_API_URL}/chats/${chat.id}`,
                { preferred_reasoning_level: level },
                { withCredentials: true, headers: { 'Authorization': `Bearer ${localStorage.getItem('token')}` } },
            );
            if (chat) chat.preferred_reasoning_level = level;
        } catch (error) {
            console.error('Failed to save reasoning level:', error);
        }
    }, [chat]);

    const handleSendMessage = useCallback(async (content: string) => {
        if (chat?.id) analyticsService.trackMessageSent(chat.id, content.length, userId || '', userName || '');
        return await onSendMessage(content, selectedLLMModel);
    }, [chat?.id, onSendMessage, selectedLLMModel, userId, userName]);

    const handleSaveEdit = useCallback(async (id: string, content: string) => {
        if (!content.trim()) return;
        const target = sourceAccess().capture(chat.id);
        const accepted = await onEditMessage(id, content);
        if (!accepted || !sourceAccess().isCurrent(target)) return;
        scroll.messageUpdateSource.current = 'query';
        setRecommendations([]);
        setTimeout(() => { scroll.messageUpdateSource.current = null; }, 200);
        setEditingMessageId(null);
        setEditInput('');
    }, [chat.id, onEditMessage, scroll.messageUpdateSource]);

    const handleMessageSubmit = useCallback(async (content: string) => {
        scroll.messageUpdateSource.current = 'new';
        const accepted = await handleSendMessage(content);
        if (accepted) setTimeout(() => scroll.scrollToBottom(true), 100);
        setTimeout(() => { scroll.messageUpdateSource.current = null; }, 300);
        return accepted;
    }, [handleSendMessage, scroll]);

    const handleAssistantRun = useCallback((run: { runId: string; messageId: string; request: AnalysisRequest }) => {
        if (activeChatGeneration.current !== chatGeneration) return;
        setWorkflowState(current => ({ ...current, open: false }));
        // Reveal the accepted run through the existing message/card chain.
        scroll.messageUpdateSource.current = 'query';
        scroll.isInitialLoad.current = false;
        scroll.cancelScrollToBottom();
        setMessages(prev => {
            if (activeChatGeneration.current !== chatGeneration) return prev;
            if (prev.some(message => message.id === run.messageId || message.assistant_run_id === run.runId)) return prev;
            const now = new Date().toISOString();
            return [
                ...prev,
                {
                    id: `assistant-user-${run.messageId}`,
                    type: 'user',
                    content: run.request.product.sku_ids?.[0] || run.request.product.product_id,
                    created_at: now,
                },
                {
                    id: run.messageId,
                    type: 'assistant',
                    content: zhCN.assistant.card.title,
                    queries: [],
                    created_at: now,
                    assistant_run_id: run.runId,
                },
            ];
        });
        setAssistantMessageToReveal(run.messageId);
    }, [chatGeneration, scroll, setMessages]);

    useEffect(() => {
        if (!assistantMessageToReveal) return;
        const element = search.searchResultRefs.current[`msg-${assistantMessageToReveal}`];
        if (!element) return;
        element.scrollIntoView({ behavior: 'instant', block: 'start' });
        scroll.messageUpdateSource.current = null;
        setAssistantMessageToReveal(null);
    }, [assistantMessageToReveal, messages, scroll.messageUpdateSource, search.searchResultRefs]);

    const handleEditQuery = useCallback((messageId: string, queryId: string, query: string) => {
        setShowEditQueryConfirm({ show: true, messageId, queryId, query });
    }, []);

    const handleConfirmQueryEdit = useCallback(async () => {
        const { messageId, queryId, query } = showEditQueryConfirm;
        if (!messageId || !queryId || !query) return;
        try {
            const response = await chatService.editQuery(chat.id, messageId, queryId, query);
            if (response.success) {
                scroll.preserveScroll(scroll.chatContainerRef.current, () => {
                    setMessages(prev => prev.map(msg => {
                        if (msg.id !== messageId) return msg;
                        return { ...msg, queries: msg.queries?.map(q => q.id === queryId ? { ...q, query: query!, is_edited: true, original_query: q.query } : q) };
                    }));
                });
                toast.success(zhCN.toasts.queryUpdated);
            }
        } catch (error) {
            toast.error(zhCN.toasts.queryUpdateFailed + formatServiceError(error).summary);
        } finally {
            setShowEditQueryConfirm({ show: false, messageId: null, queryId: null, query: null });
        }
    }, [showEditQueryConfirm, chat.id, setMessages, scroll]);

    const handleFixErrorAction = useCallback((message: Message) => {
        const errs = message.queries?.filter(q => q.error) || [];
        if (!errs.length) { toast.error(zhCN.toasts.noErrorsToFix); return; }
        let content = 'Fix Errors:\n';
        errs.forEach(q => { content += `Query: '${q.query}' faced an error: '${q.error?.message || 'Unknown error'}'.\n`; });
        void handleSendMessage(content);
    }, [handleSendMessage]);

    const handleFixRollbackErrorAction = useCallback((message: Message) => {
        const errs = message.queries?.filter(q => q.error) || [];
        if (!errs.length) { toast.error(zhCN.toasts.noErrorsToFix); return; }
        let content = 'Fix Rollback Errors:';
        errs.forEach(q => { content += `Query: '${q.rollback_query || q.rollback_dependent_query}' faced an error: '${q.error?.message || 'Unknown error'}'.\n`; });
        void handleSendMessage(content);
    }, [handleSendMessage]);

    const handleButtonCallback = useCallback((message: Message, action: string, label?: string) => {
        if (action === 'refresh_schema') { setShowRefreshSchema(true); }
        else if (action === 'fix_error') { handleFixErrorAction(message); }
        else if (action === 'fix_rollback_error') { handleFixRollbackErrorAction(message); }
        else if (action === 'try_again') {
            const userMsg = messages.find(m => m.id === message.user_message_id || (m.type === 'user' && m.created_at < message.created_at));
            if (userMsg) handleSendMessage(userMsg.content);
            else toast.error(zhCN.toasts.retryMissing);
        } else if (action === 'open_settings') { setOpenWithSettingsTab(true); setShowEditConnection(true); }
        else { handleSendMessage(`${label}`); }
    }, [messages, handleSendMessage, handleFixErrorAction, handleFixRollbackErrorAction]);

    const handleWelcomeButtonCallback = useCallback((action: string) => {
        if (action === 'refresh_schema') { setShowRefreshSchema(true); }
        else if (action === 'try_again') {
            const idx = messages.findIndex(m => m.is_streaming);
            if (idx > 0 && messages[idx - 1]?.type === 'user') handleSendMessage(messages[idx - 1].content);
            else toast.error(zhCN.toasts.retryMissing);
        } else if (action === 'open_settings') { setOpenWithSettingsTab(true); setShowEditConnection(true); }
    }, [messages, handleSendMessage]);

    const handleConfirmClearChat = useCallback(async () => {
        if (chat?.id) analyticsService.trackChatCleared(chat.id, userId || '', userName || '');
        await onClearChat();
        fetching.setPinnedMessages([]);
        setShowClearConfirm(false);
    }, [chat?.id, onClearChat, userId, userName, fetching]);

    const handleCancelStreamClick = useCallback(() => {
        if (chat?.id) analyticsService.trackQueryCancelled(chat.id, userId || '', userName || '');
        onCancelStream();
    }, [chat?.id, onCancelStream, userId, userName]);

    const handleConfirmRefreshSchema = useCallback(async () => {
        if (chat?.id) analyticsService.trackSchemaRefreshed(chat.id, chat.connection.database, userId || '', userName || '');
        await onRefreshSchema();
        setShowRefreshSchema(false);
    }, [chat?.id, chat?.connection.database, onRefreshSchema, userId, userName]);

    const handleCancelRefreshSchema = useCallback(async () => {
        if (chat?.id) analyticsService.trackSchemaCancelled(chat.id, chat.connection.database, userId || '', userName || '');
        await onCancelRefreshSchema();
        setShowRefreshSchema(false);
    }, [chat?.id, chat?.connection.database, onCancelRefreshSchema, userId, userName]);

    // ── Render ────────────────────────────────────────────────────────────────
    const displayMessages = viewMode === 'chats' ? messages : fetching.pinnedMessages;
    const dateGroups = groupMessagesByDate(displayMessages);

    return (
        <div className={`flex-1 flex flex-col h-screen max-h-screen overflow-hidden transition-all duration-300 ${isExpanded ? 'md:ml-80' : 'md:ml-20'}`}>
            <div className="relative">
                {!access.allowed && <p role="status" className="px-4 py-2 text-sm bg-yellow-50">{access.reason}</p>}
                <ChatHeader
                    chat={chat}
                    isConnecting={conn.isConnecting}
                    isConnected={isConnected}
                    onOpenModelProviders={onOpenModelProviders}
                    onClearChat={() => setShowClearConfirm(true)}
                    onEditConnection={() => onEditConnectionFromChatWindow ? onEditConnectionFromChatWindow() : setShowEditConnection(true)}
                    onShowCloseConfirm={() => setShowCloseConfirm(true)}
                    onReconnect={conn.handleReconnect}
                    setShowRefreshSchema={() => setShowRefreshSchema(true)}
                    onToggleSearch={search.handleToggleSearch}
                    viewMode={viewMode}
                    onViewModeChange={setViewMode}
                    dashboardViewMode={dashboardViewMode}
                    onDashboardViewModeChange={setDashboardViewMode}
                    syncPending={!!sync.pending || !!sync.job && !sync.terminal}
                    onOpenDataSync={returnFocus => {
                        const target = access.coordinator?.capture(chat.id);
                        if (target) setSyncWindow({ target, returnFocus });
                    }}
                />
                {search.showSearch && (
                    <SearchBar
                        onSearch={search.performSearch}
                        onClose={search.handleToggleSearch}
                        onNavigateUp={search.navigateSearchUp}
                        onNavigateDown={search.navigateSearchDown}
                        currentResultIndex={search.currentSearchIndex}
                        totalResults={search.searchResults.length}
                        initialQuery={search.searchQuery}
                    />
                )}
            </div>

            <div className="relative flex-1 min-h-0">
                {/* Dashboard View */}
                <div className={`absolute inset-0 flex flex-col transition-opacity duration-200 ease-in-out ${dashboardViewMode === 'dashboard' ? 'opacity-100 z-[2]' : 'opacity-0 z-0 pointer-events-none'}`}>
                    <DashboardView chatId={chat.id} streamId={streamId} isConnected={isConnected} onReconnect={conn.handleReconnect} />
                </div>

                {/* Chat View */}
                <div className={`absolute inset-0 flex flex-col transition-opacity duration-200 ease-in-out ${dashboardViewMode === 'chat' ? 'opacity-100 z-[2]' : 'opacity-0 z-0 pointer-events-none'}`}>
                    <div
                        ref={scroll.chatContainerRef}
                        data-chat-container
                        className={`flex-1 overflow-y-auto overflow-x-hidden bg-[#FFDB58]/10 relative scroll-smooth ${viewMode === 'chats' ? 'pb-24 md:pb-32' : 'pb-8'} -mt-6 md:mt-0 flex-shrink`}
                    >
                        {viewMode === 'chats' ? (
                            <div ref={fetching.loadingRef} className="h-20 flex items-center justify-center">
                                {fetching.isLoadingMessages && (
                                    <div className="flex items-center justify-center gap-2">
                                        <Loader2 className="w-4 h-4 animate-spin" />
                                        <span className="text-sm text-gray-600">{zhCN.chat.loadingMore}</span>
                                    </div>
                                )}
                            </div>
                        ) : <div className="h-20" />}

                        <div className={`max-w-5xl mx-auto px-4 pt-16 md:pt-0 md:px-2 xl:px-0 transition-all duration-300 ${isExpanded ? 'md:ml-6 lg:ml-6 xl:mx-8 [@media(min-width:1760px)]:ml-[4rem] [@media(min-width:1920px)]:ml-[8.4rem]' : 'md:ml-[19rem] xl:mx-auto'}`}>
                            {Object.entries(dateGroups).map(([date, dateMessages], groupIndex) => (
                                <div key={date}>
                                    <div className={`flex items-center justify-center ${groupIndex === 0 ? 'mb-4' : 'my-6'}`}>
                                        <div className="px-4 py-2 bg-white text-sm font-medium text-black border-2 border-black shadow-[2px_2px_0px_0px_rgba(0,0,0,1)] rounded-full">
                                            {formatDateDivider(date)}
                                        </div>
                                    </div>

                                    {dateMessages.map((message, index) => (
                                        <MessageTile
                                            key={message.id}
                                            checkSSEConnection={checkSSEConnection}
                                            chatId={chat.id}
                                            message={message}
                                            setMessage={setMessage}
                                            onEdit={handleEditMessage}
                                            editingMessageId={editingMessageId}
                                            editInput={editInput}
                                            setEditInput={setEditInput}
                                            onSaveEdit={handleSaveEdit}
                                            onCancelEdit={handleCancelEdit}
                                            queryStates={queryStates}
                                            setQueryStates={setQueryStates}
                                            queryTimeouts={queryTimeouts}
                                            isFirstMessage={index === 0}
                                            onPinMessage={(msgId, shouldPin) => fetching.handlePinMessage(msgId, shouldPin, messages)}
                                            onQueryUpdate={scroll.handleQueryUpdate}
                                            onEditQuery={handleEditQuery}
                                            userId={userId || ''}
                                            userName={userName || ''}
                                            searchQuery={search.showSearch ? search.searchQuery : ''}
                                            searchResultRefs={search.searchResultRefs}
                                            buttonCallback={(action, label) => handleButtonCallback(message, action, label)}
                                        />
                                    ))}

                                    {/* Inline recommendations after the last AI message in the last date group */}
                                    {viewMode === 'chats' && (() => {
                                        const isLastGroup = groupIndex === Object.keys(dateGroups).length - 1;
                                        const isStreaming = messages.length > 0 && messages[messages.length - 1].is_streaming === true;
                                        if (!isLastGroup || isStreaming || isLoadingRecommendations || recommendations.length === 0) return null;
                                        const hasAI = dateMessages.some(m => m.type === 'assistant');
                                        if (!hasAI) return null;
                                        return (
                                            <div className="mt-6 md:mt-4 mb-6 md:mb-2">
                                                <div className="flex items-center gap-2 mb-3">
                                                    <span className="text-sm text-gray-600 font-medium">{zhCN.chat.tryAsking}</span>
                                                </div>
                                                <div className="flex flex-wrap gap-2 items-center">
                                                    {recommendations.slice(0, 4).map((text, idx) => (
                                                        <button
                                                            key={`${text}-${idx}`}
                                                            onClick={async () => {
                                                                analyticsService.trackRecommendationChipClick(chat.id, text, userId || '', userName || '');
                                                                if (await handleMessageSubmit(text)) setRecommendations([]);
                                                            }}
                                                            className="inline-flex items-center gap-2 px-3 py-2 bg-gray-100 hover:bg-gray-200 border-2 border-gray-300 hover:border-gray-400 rounded-full text-sm font-medium text-black transition-all duration-200 max-w-base truncate"
                                                            title={text}
                                                            disabled={!access.allowed}
                                                        >
                                                            <span className="truncate">{text}</span>
                                                        </button>
                                                    ))}
                                                </div>
                                            </div>
                                        );
                                    })()}
                                </div>
                            ))}

                            {displayMessages.length === 0 && (
                                <div className="flex flex-col items-center justify-center h-full">
                                    <div className="px-4 py-2 bg-white text-sm font-medium text-black border-2 border-black shadow-[2px_2px_0px_0px_rgba(0,0,0,1)] rounded-full">
                                        {formatDateDivider(new Date().toISOString())}
                                    </div>
                                    {viewMode === 'chats' ? (
                                        <>
                                            <MessageTile
                                                key="welcome-message"
                                                checkSSEConnection={checkSSEConnection}
                                                chatId={chat.id}
                                                message={{
                                                    id: 'welcome-message',
                                                    type: 'assistant',
                                                    content: zhCN.chat.welcome(userName || ''),
                                                    queries: [],
                                                    action_buttons: [],
                                                    created_at: new Date().toISOString(),
                                                    updated_at: new Date().toISOString(),
                                                }}
                                                setMessage={setMessage}
                                                onEdit={handleEditMessage}
                                                editingMessageId={editingMessageId}
                                                onPinMessage={(msgId, shouldPin) => fetching.handlePinMessage(msgId, shouldPin, messages)}
                                                editInput={editInput}
                                                setEditInput={setEditInput}
                                                onSaveEdit={handleSaveEdit}
                                                onCancelEdit={handleCancelEdit}
                                                queryStates={queryStates}
                                                setQueryStates={setQueryStates}
                                                queryTimeouts={queryTimeouts}
                                                isFirstMessage={false}
                                                onQueryUpdate={scroll.handleQueryUpdate}
                                                onEditQuery={handleEditQuery}
                                                userId={userId || ''}
                                                userName={userName || ''}
                                                searchQuery={search.showSearch ? search.searchQuery : ''}
                                                searchResultRefs={search.searchResultRefs}
                                                buttonCallback={handleWelcomeButtonCallback}
                                            />
                                            {!isLoadingRecommendations && recommendations.length > 0 && (
                                                <div className="mt-6 md:mt-4 mb-6 md:mb-2">
                                                    <div className="flex items-center gap-2 mb-3">
                                                        <span className="text-sm text-gray-600 font-medium">{zhCN.chat.tryAsking}</span>
                                                    </div>
                                                    <div className="flex flex-wrap gap-2 items-center">
                                                        {recommendations.slice(0, 4).map((text, idx) => (
                                                            <button
                                                                key={`${text}-${idx}`}
                                                                onClick={async () => {
                                                                    analyticsService.trackRecommendationChipClick(chat.id, text, userId || '', userName || '');
                                                                    if (await handleMessageSubmit(text)) setRecommendations([]);
                                                                }}
                                                                className="inline-flex items-center gap-2 px-3 py-2 bg-gray-100 hover:bg-gray-200 border-2 border-gray-300 hover:border-gray-400 rounded-full text-sm font-medium text-black transition-all duration-200 max-w-base truncate"
                                                                title={text}
                                                                disabled={!access.allowed}
                                                            >
                                                                <span className="truncate">{text}</span>
                                                            </button>
                                                        ))}
                                                    </div>
                                                </div>
                                            )}
                                        </>
                                    ) : (
                                        <div className="text-center text-gray-600 mt-40">
                                            <Pin className="w-12 h-12 mx-auto mb-4 text-gray-400 rotate-45" />
                                            <p className="text-lg font-medium">{zhCN.chat.noPinned}</p>
                                            <p className="text-sm mt-2">{zhCN.chat.noPinnedHint}</p>
                                        </div>
                                    )}
                                </div>
                            )}
                        </div>
                        <div ref={scroll.messagesEndRef} />

                        {scroll.showScrollButton && (
                            <button
                                onClick={() => scroll.scrollToBottom(true)}
                                className="fixed bottom-28 right-4 md:right-6 p-3 bg-black text-white rounded-full shadow-lg hover:bg-gray-800 transition-all neo-border z-40"
                                title={zhCN.chat.scrollBottom}
                            >
                                <ArrowDown className="w-6 h-6" />
                            </button>
                        )}
                    </div>
                </div>
            </div>

            <div hidden={viewMode !== 'chats' || dashboardViewMode !== 'chat'}>
                    <MessageInput
                        isConnected={isConnected}
                        onSendMessage={handleMessageSubmit}
                        isExpanded={isExpanded}
                        isDisabled={_isMessageSending}
                        chatId={chat.id}
                        userId={userId || ''}
                        userName={userName || ''}
                        isStreaming={messages.some(m => m.is_streaming)}
                        onCancelStream={handleCancelStreamClick}
                        prefillText={inputPrefill || ''}
                        onConsumePrefill={() => setInputPrefill(null)}
                        onModelChange={handleLLMModelChange}
                        selectedReasoningLevel={selectedReasoningLevel}
                        onReasoningChange={handleReasoningChange}
                        generationOnly={!chat.settings.auto_execute_query}
                        selectedModel={selectedLLMModel}
                        llmModels={llmModels}
                        isLoadingModels={isLoadingModels}
                        workflowControl={
                            <WorkflowMenu
                                key={chat.id}
                                open={workflowOpen}
                                workflow={selectedWorkflow}
                                onOpenChange={open => setWorkflowState({ chatId: chat.id, open, workflow: selectedWorkflow })}
                                onSelect={workflow => setWorkflowState({ chatId: chat.id, open: true, workflow })}
                            >
                                <AssistantInputPanel
                                    chatId={chat.id}
                                    isConnected={isConnected}
                                    workflow={selectedWorkflow ?? 'product_analysis'}
                                    onWorkflowChange={workflow => setWorkflowState({ chatId: chat.id, open: true, workflow })}
                                    onRunCreated={handleAssistantRun}
                                />
                            </WorkflowMenu>
                        }
                    />
            </div>
            {syncVisible && syncWindow && <DataSyncModal chat={access.chat ?? chat} sync={sync} returnFocus={syncWindow.returnFocus} onClose={() => setSyncWindow(null)} />}

            {showRefreshSchema && (
                <ConfirmationModal
                    icon={<RefreshCcw className="w-6 h-6 text-black" />}
                    themeColor="black"
                    title={zhCN.chat.refreshKbTitle}
                    buttonText={zhCN.chat.refresh}
                    message={zhCN.chat.refreshKbMessage}
                    onConfirm={handleConfirmRefreshSchema}
                    onCancel={handleCancelRefreshSchema}
                />
            )}

            {showClearConfirm && (
                <ConfirmationModal
                    title={zhCN.chat.clearTitle}
                    message={zhCN.chat.clearMessage}
                    onConfirm={handleConfirmClearChat}
                    onCancel={() => setShowClearConfirm(false)}
                />
            )}

            {showCloseConfirm && (
                <ConfirmationModal
                    title={zhCN.chat.disconnectTitle}
                    message={zhCN.chat.disconnectMessage}
                    onConfirm={conn.handleDisconnect}
                    onCancel={() => setShowCloseConfirm(false)}
                />
            )}

            {showEditConnection && (
                <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/50">
                    <ConnectionModal
                        initialData={chat}
                        initialTab={openWithSettingsTab ? 'settings' : undefined}
                        onClose={updatedChat => {
                            setShowEditConnection(false);
                            setOpenWithSettingsTab(false);
                            if (updatedChat && onEditConnection) onEditConnection(chat.id, updatedChat.connection, updatedChat.settings);
                        }}
                        onEdit={async (data, autoExecuteQuery) => {
                            const result = await onEditConnection?.(chat.id, data!, autoExecuteQuery!);
                            return { success: result?.success || false, error: result?.error, updatedChat: result?.success ? result.updatedChat : undefined };
                        }}
                        onSubmit={async (data, autoExecuteQuery) => {
                            const result = await onEditConnection?.(chat.id, data, autoExecuteQuery);
                            return { success: result?.success || false, error: result?.error };
                        }}
                        onUpdateSelectedCollections={onUpdateSelectedCollections}
                        onRefreshSchema={handleConfirmRefreshSchema}
                    />
                </div>
            )}

            {showEditQueryConfirm.show && (
                <ConfirmationModal
                    title={zhCN.chat.editQueryTitle}
                    message={zhCN.chat.editQueryMessage}
                    onConfirm={handleConfirmQueryEdit}
                    onCancel={() => setShowEditQueryConfirm({ show: false, messageId: null, queryId: null, query: null })}
                />
            )}
        </div>
    );
}
