import axios from 'axios';
import { Boxes } from 'lucide-react';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import toast, { Toaster } from 'react-hot-toast';
import { Routes, Route, useParams, Navigate } from 'react-router-dom';
import AuthForm from './components/auth/AuthForm';
import ChatWindow from './components/chat/ChatWindow';
import { Message } from './types/query';
import SuccessBanner from './components/common/SuccessBanner';
import Sidebar from './components/chat/Sidebar';
import ConnectionModal from './components/modals/ConnectionModal';
import ModelProvidersModal from './components/modals/ModelProvidersModal';
import { useStream } from './contexts/StreamContext';
import { StreamProvider } from './contexts/StreamProvider';
import { useUser } from './contexts/UserContext';
import { UserProvider } from './contexts/UserProvider';
import authService from './services/authService';
import './services/axiosConfig';
import chatService from './services/chatService';
import analyticsService from './services/analyticsService';
import { LoginFormData, SignupFormData } from './types/auth';
import { Chat, ChatSettings, ChatsResponse, Connection } from './types/chat';
import type { LLMModel, SupportedModelsResponse } from './types/chat';
import { SendMessageResponse } from './types/messages';
import WelcomeSection from './components/app/WelcomeSection';
import LoadingComponent from './components/app/Loading';
import GoogleAuthCallback from './pages/GoogleAuthCallback';
import { useSSEConnection } from './hooks/useSSEConnection';
import { useChatActions } from './hooks/useChatActions';
import { formatServiceError, productName, zhCN } from './i18n/zh-CN';
import { useSourceAccessCoordinator } from './hooks/useSourceAccess';
import { SourceAccessContext } from './contexts/SourceAccessContext';

// ─── Toast presets ────────────────────────────────────────────────────────────
const toastStyle = {
    style: {
        background: '#000', color: '#fff', border: '4px solid #000', borderRadius: '12px',
        boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)', padding: '12px 24px',
        fontSize: '16px', fontWeight: '500', zIndex: 9999,
    },
} as const;

const errorToast = {
    style: { ...toastStyle.style, background: '#ff4444', border: '4px solid #cc0000', fontWeight: 'bold' as const },
    duration: 4000,
    icon: '\u26a0\ufe0f',
};

// ─── Inner component (needs router hooks) ─────────────────────────────────────
function AppContent() {
    const { chatId: chatIdFromUrl } = useParams<{ chatId?: string }>();
    const { user, setUser } = useUser();
    const { generateStreamId } = useStream();

    // ── Core state ────────────────────────────────────────────────────────────
    const [isAuthenticated, setIsAuthenticated] = useState(false);
    const [isLoading, setIsLoading] = useState(true);
    const [hasCheckedAuth, setHasCheckedAuth] = useState(false);
    const [showConnectionModal, setShowConnectionModal] = useState(false);
    const [showModelProviders, setShowModelProviders] = useState(false);
    const [isEditingConnection, setIsEditingConnection] = useState(false);
    const [isSidebarExpanded, setIsSidebarExpanded] = useState(true);
    const [selectedConnection, setSelectedConnection] = useState<Chat>();
    const [messages, setMessages] = useState<Message[]>([]);
    const [authError, setAuthError] = useState<string | null>(null);
    const [successMessage, setSuccessMessage] = useState<string | null>(null);
    const [chats, setChats] = useState<Chat[]>([]);
    const [isLoadingChats, setIsLoadingChats] = useState(false);
    const [connectionStatuses, setConnectionStatuses] = useState<Record<string, boolean>>({});
    const [isMessageSending, setIsMessageSending] = useState(false);
    const [temporaryMessage, setTemporaryMessage] = useState<Message | null>(null);
    const [refreshSchemaController, setRefreshSchemaController] = useState<AbortController | null>(null);
    const [recoRefreshToken, setRecoRefreshToken] = useState(0);
    const [llmModels, setLlmModels] = useState<LLMModel[]>([]);
    const [isLoadingModels, setIsLoadingModels] = useState(false);

    const source = useSourceAccessCoordinator(isAuthenticated ? user?.id || '' : '', selectedConnection, chats);
    const sending = useRef(false);
    const viewGeneration = useMemo(() => ({}), [user?.id, selectedConnection?.id, selectedConnection?.source_id, selectedConnection?.source_binding_revision]);
    const activeView = useRef(viewGeneration);
    activeView.current = viewGeneration;
    const setScopedMessages = useCallback<React.Dispatch<React.SetStateAction<Message[]>>>(update => {
        setMessages(current => activeView.current === viewGeneration ? (typeof update === 'function' ? update(current) : update) : current);
    }, [viewGeneration]);
    const setScopedTemporary = useCallback((message: Message | null) => {
        if (activeView.current === viewGeneration) setTemporaryMessage(message);
    }, [viewGeneration]);
    const setScopedRecommendations = useCallback<React.Dispatch<React.SetStateAction<number>>>(update => {
        if (activeView.current === viewGeneration) setRecoRefreshToken(update);
    }, [viewGeneration]);

    const handleConnectionStatusChange = useCallback((chatId: string, isConnected: boolean) => {
        if (chatId && typeof isConnected === 'boolean') {
            setConnectionStatuses(prev => ({ ...prev, [chatId]: isConnected }));
        }
    }, []);

    // ── SSE hook ──────────────────────────────────────────────────────────────
    const sse = useSSEConnection({
        selectedConnectionId: selectedConnection?.id,
        messages,
        temporaryMessage,
        setMessages: setScopedMessages,
        setTemporaryMessage: setScopedTemporary,
        setRecoRefreshToken: setScopedRecommendations,
        onConnectionStatusChange: handleConnectionStatusChange,
    });
    const streamOwner = useRef(user?.id);
    useLayoutEffect(() => {
        const wrongOwner = streamOwner.current !== user?.id;
        streamOwner.current = user?.id;
        const stream = sse.eventSource;
        if (stream && (wrongOwner || !selectedConnection?.id || !stream.url.includes(`/chats/${encodeURIComponent(selectedConnection.id)}/stream?`))) {
            stream.close();
            sse.setEventSource(null);
        }
    }, [sse.eventSource, selectedConnection?.id, user?.id, sse.setEventSource]);

    // ── Chat actions hook ─────────────────────────────────────────────────────
    const chatActions = useChatActions({
        chats, setChats,
        selectedConnection, setSelectedConnection,
        setMessages,
        setSuccessMessage,
        connectionStatuses,
        handleConnectionStatusChange,
        streamId: sse.streamId,
        generateStreamId,
        eventSource: sse.eventSource,
        setEventSource: sse.setEventSource,
        setupSSEConnection: sse.setupSSEConnection,
    });

    // ── Document title ────────────────────────────────────────────────────────
    useEffect(() => {
        document.title = productName;
    }, [isAuthenticated, selectedConnection]);


    const checkAuth = useCallback(async () => {
        const authTarget = source.capture();
        try {
            const response = await authService.getUser();
            if (!source.isOwnerCurrent(authTarget)) return;
            setIsAuthenticated(response.success);
            if (response.success && response.data) {
                const userData = { id: response.data.id, username: response.data.username, created_at: response.data.created_at, is_admin: response.data.is_admin };
                setUser(userData);
                analyticsService.identifyUser(userData.id, userData.username, userData.created_at);
            }
        } catch (error: unknown) {
            if (!source.isOwnerCurrent(authTarget)) return;
            setIsAuthenticated(false);
            const status = error !== null && typeof error === 'object' && 'status' in error
                ? error.status : axios.isAxiosError(error) ? error.response?.status : undefined;
            if (!localStorage.getItem('token') && (status === 401 || (error instanceof Error && error.message === 'Get user failed'))) return;
            setAuthError(formatServiceError(error).summary);

        } finally {
            setIsLoading(false);
        }
    }, [setUser, source]);

    useEffect(() => {
        if (!hasCheckedAuth) { setHasCheckedAuth(true); checkAuth(); }
    }, [hasCheckedAuth, checkAuth]);

    useEffect(() => {
        if (isAuthenticated && authError) setAuthError(null);
    }, [authError, isAuthenticated]);


    // ── Load chats + LLM models on auth ───────────────────────────────────────
    useEffect(() => {
        if (!isAuthenticated) return;
        const ownerTarget = source.capture();

        const loadChats = async () => {
            setIsLoadingChats(true);
            try {
                // Load models in parallel
                axios.get<SupportedModelsResponse>(`${import.meta.env.VITE_API_URL}/llm-models`).then(r => {
                    if (!source.isOwnerCurrent(ownerTarget)) return;
                    if (r.data.success && r.data.data.models) setLlmModels(r.data.data.models);
                }).catch(() => {}).finally(() => { if (source.isOwnerCurrent(ownerTarget)) setIsLoadingModels(false) });
                setIsLoadingModels(true);

                const response = await axios.get<ChatsResponse>(`${import.meta.env.VITE_API_URL}/chats`, {
                    withCredentials: true,
                    headers: { 'Authorization': `Bearer ${localStorage.getItem('token')}`, 'Accept': 'application/json', 'Content-Type': 'application/json' },
                });
                if (!source.isOwnerCurrent(ownerTarget)) return;
                if (response.data?.data?.chats) {
                    setChats(response.data.data.chats);
                }
            } catch { /* ignore */ } finally {
                if (source.isOwnerCurrent(ownerTarget)) setIsLoadingChats(false);
            }
        };
        loadChats();
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [isAuthenticated, user?.id]);

    // ── Handle chat from URL on subsequent navigation ─────────────────────────
    useEffect(() => {
        if (chatIdFromUrl && chats.length > 0 && !isLoadingChats && !chatActions.isSelectingConnection) {
            const fromUrl = chats.find(c => c.id === chatIdFromUrl);
            if (fromUrl && (!selectedConnection || selectedConnection.id !== chatIdFromUrl)) {
                chatActions.handleSelectConnection(chatIdFromUrl);
            }
        }
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [chatIdFromUrl, chats, isLoadingChats, selectedConnection]);

    // ── Auth handlers ─────────────────────────────────────────────────────────
    const handleLogin = async (data: LoginFormData) => {
        const response = await authService.login(data);
        const userData = { id: response.user.id, username: response.user.username, created_at: response.user.created_at, is_admin: response.is_admin };
        setUser(userData);
        setIsAuthenticated(true);
        setSuccessMessage(zhCN.auth.login.welcomeBack(userData.username));
        analyticsService.trackLogin(userData.id, userData.username);
        analyticsService.identifyUser(userData.id, userData.username, userData.created_at);
    };

    const handleSignup = async (data: SignupFormData) => {
        const response = await authService.signup(data);
        const userData = { id: response.user.id, username: response.user.username, created_at: response.user.created_at, is_admin: response.is_admin };
        setIsAuthenticated(true);
        setUser(userData);
        setSuccessMessage(zhCN.auth.signup.welcome(userData.username));
        analyticsService.trackSignup(userData.id, userData.username);
        analyticsService.identifyUser(userData.id, userData.username, userData.created_at);
    };

    const handleLogout = async () => {
        source.configure('', undefined, []);
        try { authService.logout(); } catch { /* ignore */ }
        setUser(null);
        setSuccessMessage(zhCN.auth.loggedOut);
        setIsAuthenticated(false);
        setSelectedConnection(undefined);
        setMessages([]);
        window.location.href = '/';
    };

    // ── Schema refresh ────────────────────────────────────────────────────────
    const handleRefreshSchema = async (chatId = selectedConnection?.id) => {
        try {
            const controller = new AbortController();
            setRefreshSchemaController(controller);
            const ok = await chatService.refreshSchema(chatId || '', controller);
            if (ok) toast.success(zhCN.toasts.kbRefreshed);
            else toast.error(zhCN.toasts.kbCancelled);
        } catch (error) {
            toast.error(zhCN.toasts.kbFailed + formatServiceError(error).summary);
        }
    };

    const handleCancelRefreshSchema = async () => {
        if (refreshSchemaController) { refreshSchemaController.abort(); setRefreshSchemaController(null); }
    };

    // ── Cancel stream ─────────────────────────────────────────────────────────
    const handleCancelStream = async () => {
        if (!selectedConnection?.id || !sse.streamId) return;
        try {
            await chatService.cancelStream(selectedConnection.id, sse.streamId);
            setMessages(prev => prev.filter(m => !m.is_streaming).map(m => ({ ...m, is_streaming: false })));
        } catch { /* ignore */ }
    };

    // ── Send message ──────────────────────────────────────────────────────────
    const handleSendMessage = async (content: string, llmModel?: string): Promise<boolean> => {
        if (!selectedConnection?.id || !sse.streamId || sending.current) return false;
        const chatId = selectedConnection.id;
        const streamId = sse.streamId;
        try {
            sending.current = true;
            setIsMessageSending(true);
            const target = await source.admit(chatId);
            source.assert(target);
            await sse.checkSSEConnection();
            source.assert(target);
            const response = await chatService.sendMessage(chatId, 'temp', streamId, content, llmModel);
            source.assertCurrent(target);
            if (response.success) {
                const userMessage: Message = {
                    id: response.data.id, type: 'user', content: response.data.content,
                    is_loading: false, queries: [], is_streaming: false,
                    created_at: new Date().toISOString(),
                };
                const tempMsg: Message = {
                    id: `temp-${Date.now()}`, type: 'assistant', content: '',
                    queries: [], action_buttons: [], is_loading: true,
                    loading_steps: [{ text: zhCN.chat.analyzing, done: false }],
                    is_streaming: true, created_at: new Date().toISOString(),
                };
                setMessages(prev => [...prev, userMessage, tempMsg]);
                setTemporaryMessage(tempMsg);
                sse.scheduleStreamTimeout(tempMsg.id);
                return true;
            }
            return false;
        } catch (error) {
            toast.error(formatServiceError(error).summary, errorToast);
            return false;
        } finally {
            sending.current = false;
            setIsMessageSending(false);
        }
    };

    // ── Edit message ──────────────────────────────────────────────────────────
    const handleEditMessage = async (id: string, content: string): Promise<boolean> => {
        if (!selectedConnection?.id || !sse.streamId || isMessageSending || !content) return false;
        const chatId = selectedConnection.id;
        const sid = sse.streamId;
        try {
            const target = await source.admit(chatId);
            source.assert(target);
            await sse.checkSSEConnection();
            source.assert(target);
            const response = await axios.patch<SendMessageResponse>(
                `${import.meta.env.VITE_API_URL}/chats/${chatId}/messages/${id}`,
                { stream_id: sid, content },
                { withCredentials: true, headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${localStorage.getItem('token')}` } },
            );
            source.assertCurrent(target);

            if (response.data.success) {
                setMessages(prev => prev.map(msg =>
                    msg.id === id ? { ...msg, content, is_edited: true, updated_at: new Date().toISOString() } : msg,
                ));

                const aiMessage = messages.find(msg => msg.user_message_id === id);
                if (aiMessage) {
                    setMessages(prev => prev.map(msg =>
                        msg.id === aiMessage.id
                            ? { ...msg, is_edited: true, content: '', queries: [], action_buttons: [], updated_at: new Date().toISOString(), loading_steps: [{ text: zhCN.chat.analyzing, done: false }], is_streaming: true }
                            : msg,
                    ));
                    setTemporaryMessage(messages.find(m => m.id === aiMessage.id) || null);
                } else {
                    const tempMsg: Message = {
                        id: 'temp', type: 'assistant', content: '',
                        queries: [], action_buttons: [], is_loading: true,
                        loading_steps: [{ text: zhCN.chat.analyzing, done: false }],
                        is_streaming: true, created_at: new Date().toISOString(),
                    };
                    setMessages(prev => [...prev, tempMsg]);
                    setTemporaryMessage(tempMsg);
                    sse.scheduleStreamTimeout(tempMsg.id);
                }
                return true;
            }
            return false;
        } catch (error) {
            toast.error(formatServiceError(error).summary, errorToast);
            return false;
        }
    };

    const handleEditConnectionFromChatWindow = (connection?: Chat) => {
        if (connection) setSelectedConnection(connection);
        setIsEditingConnection(true);
        setShowConnectionModal(true);
    };

    // ── Connection status cleanup ─────────────────────────────────────────────
    useEffect(() => {
        if (!selectedConnection) setConnectionStatuses({});
    }, [selectedConnection]);

    // ── Render ────────────────────────────────────────────────────────────────
    if (isLoading) return <LoadingComponent />;
    if (!isAuthenticated) return (
        <AuthForm onLogin={handleLogin} onSignup={handleSignup} startupError={authError} />
    );


    return (
        <SourceAccessContext.Provider value={source}>
        <div className="flex flex-col md:flex-row bg-[#FFDB58]/10 min-h-screen">
            {/* Mobile header */}
            <div className={`${!isSidebarExpanded && selectedConnection ? 'hidden' : 'fixed'} md:fixed top-0 left-0 right-0 h-16 bg-white border-b-4 border-black md:hidden z-50 flex items-center justify-between px-4`}>
                <div className="flex items-center gap-2">
                    <Boxes className="w-8 h-8" />
                    <h1 className="text-2xl font-bold">{productName}</h1>
                </div>
            </div>

            <Sidebar
                isExpanded={isSidebarExpanded}
                onToggleExpand={() => setIsSidebarExpanded(prev => !prev)}
                connections={chats}
                isLoadingConnections={isLoadingChats}
                onSelectConnection={chatActions.handleSelectConnection}
                onAddConnection={() => { setIsEditingConnection(false); setShowConnectionModal(true); }}
                onLogout={handleLogout}
                selectedConnection={selectedConnection}
                onDeleteConnection={chatActions.handleDeleteConnection}
                onEditConnection={handleEditConnectionFromChatWindow}
                onDuplicateConnection={chatActions.handleDuplicateConnection}
                onConnectionStatusChange={handleConnectionStatusChange}
                eventSource={sse.eventSource}
            />

            <div className="flex-1 transition-all duration-200 ease-in-out">
                {selectedConnection ? (
                    <ChatWindow
                        key={`${user?.id}:${selectedConnection.id}`}
                        chat={selectedConnection}
                        isExpanded={isSidebarExpanded}
                        messages={messages}
                        checkSSEConnection={sse.checkSSEConnection}
                        setMessages={setScopedMessages}
                        onSendMessage={handleSendMessage}
                        onClearChat={chatActions.handleClearChat}
                        onEditMessage={handleEditMessage}
                        onCloseConnection={chatActions.handleCloseConnection}
                        onEditConnection={chatActions.handleEditConnection}
                        onConnectionStatusChange={handleConnectionStatusChange}
                        isConnected={!!connectionStatuses[selectedConnection.id]}
                        onCancelStream={handleCancelStream}
                        onRefreshSchema={handleRefreshSchema}
                        onCancelRefreshSchema={handleCancelRefreshSchema}
                        onUpdateSelectedCollections={chatActions.handleUpdateSelectedCollections}
                        onEditConnectionFromChatWindow={handleEditConnectionFromChatWindow}
                        userId={user?.id || ''}
                        userName={user?.username || ''}
                        recoVersion={recoRefreshToken}
                        llmModels={llmModels}
                        onOpenModelProviders={() => setShowModelProviders(true)}
                        isLoadingModels={isLoadingModels}
                    />
                ) : (
                    <WelcomeSection
                        isSidebarExpanded={isSidebarExpanded}
                        setShowConnectionModal={setShowConnectionModal}
                        toastStyle={toastStyle}
                    />
                )}
            </div>

            {showConnectionModal && (
                <ConnectionModal
                    key={`${user?.id}:${isEditingConnection ? selectedConnection?.id : 'new'}`}
                    onClose={updatedChat => {
                        setShowConnectionModal(false);
                        setIsEditingConnection(false);
                        if (updatedChat) {
                            setChats(prev => prev.map(c => c.id === updatedChat.id ? updatedChat : c));
                            if (selectedConnection?.id === updatedChat.id) setSelectedConnection(updatedChat);
                        }
                    }}
                    onSubmit={chatActions.handleAddConnection}
                    onUpdateSelectedCollections={chatActions.handleUpdateSelectedCollections}
                    onRefreshSchema={handleRefreshSchema}
                    initialData={isEditingConnection ? selectedConnection : undefined}
                    onEdit={isEditingConnection ? async (connection, settings) => {
                        try {
                            const updated = await chatService.editChat(selectedConnection!.id, connection as Connection, settings as ChatSettings);
                            setChats(prev => prev.map(c => c.id === updated.id ? updated : c));
                            if (selectedConnection?.id === updated.id) setSelectedConnection(updated);
                            toast.success(zhCN.toasts.connectionUpdated, toastStyle);
                            return { success: true, updatedChat: updated };
                        } catch (error: unknown) {
                            toast.error(formatServiceError(error).summary, errorToast);
                            return { success: false, error: formatServiceError(error).summary };
                        }
                    } : undefined}
                />
            )}

            <Toaster
                position="bottom-center"
                reverseOrder={false}
                gutter={8}
                containerClassName="!fixed"
                containerStyle={{ zIndex: 9999, bottom: '2rem' }}
                toastOptions={{
                    success: { style: toastStyle.style, duration: 2000, icon: '\ud83d\udc4b' },
                    error: { style: { ...toastStyle.style, background: '#ff4444', border: '4px solid #cc0000', fontWeight: 'bold' }, duration: 4000, icon: '\u26a0\ufe0f' },
                }}
            />

            {showModelProviders && (
                <ModelProvidersModal
                    onClose={() => setShowModelProviders(false)}
                    onChanged={() => {
                        axios.get<SupportedModelsResponse>(`${import.meta.env.VITE_API_URL}/llm-models`)
                            .then(r => { if (r.data.success && r.data.data.models) setLlmModels(r.data.data.models); })
                            .catch(() => {});
                    }}
                />
            )}

            {successMessage && <SuccessBanner message={successMessage} onClose={() => setSuccessMessage(null)} />}
        </div>
        </SourceAccessContext.Provider>
    );
}

// ─── Root App ─────────────────────────────────────────────────────────────────
function App() {
    useEffect(() => {
        try { analyticsService.initAnalytics(); } catch { /* ignore */ }
    }, []);

    return (
        <UserProvider>
            <StreamProvider>
                <Routes>
                    <Route path="/" element={<AppContent />} />
                    <Route path="/chat/:chatId" element={<AppContent />} />
                    <Route path="/auth/google/callback" element={<GoogleAuthCallback />} />
                    <Route path="*" element={<Navigate to="/" replace />} />
                </Routes>
            </StreamProvider>
        </UserProvider>
    );
}

export default App;
