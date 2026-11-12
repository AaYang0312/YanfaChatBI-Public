import type { EventSourcePolyfill } from 'event-source-polyfill';
import { useCallback, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { useNavigate } from 'react-router-dom';
import axios from '../services/axiosConfig';
import chatService from '../services/chatService';
import { Chat, ChatSettings, Connection } from '../types/chat';
import { Message } from '../types/query';
import { formatServiceError, zhCN } from '../i18n/zh-CN';
import { sourceAccess } from '../contexts/SourceAccessContext';

const ERROR_TOAST = {
    style: {
        background: '#ff4444', color: '#fff',
        border: '4px solid #cc0000', borderRadius: '12px',
        boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
        padding: '12px 24px', fontSize: '16px', fontWeight: '500', zIndex: 9999,
    },
    duration: 4000,
    icon: '⚠️',
};

const SUCCESS_TOAST = {
    style: {
        background: '#000', color: '#fff', border: '4px solid #000', borderRadius: '12px',
        boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)', padding: '12px 24px',
        fontSize: '14px', fontWeight: '500',
    },
};

interface UseChatActionsParams {
    chats: Chat[];
    setChats: React.Dispatch<React.SetStateAction<Chat[]>>;
    selectedConnection: Chat | undefined;
    setSelectedConnection: (chat: Chat | undefined) => void;
    setMessages: React.Dispatch<React.SetStateAction<Message[]>>;
    setSuccessMessage: (msg: string | null) => void;
    connectionStatuses: Record<string, boolean>;
    handleConnectionStatusChange: (chatId: string, isConnected: boolean, from: string) => void;
    streamId: string | null;
    generateStreamId: () => string;
    eventSource: EventSourcePolyfill | null;
    setEventSource: (es: EventSourcePolyfill | null) => void;
    setupSSEConnection: (chatId: string) => Promise<string>;
}

export function useChatActions({
    chats,
    setChats,
    selectedConnection,
    setSelectedConnection,
    setMessages,
    setSuccessMessage,
    connectionStatuses,
    handleConnectionStatusChange,
    streamId,
    generateStreamId,
    eventSource,
    setEventSource,
    setupSSEConnection,
}: UseChatActionsParams) {
    const navigate = useNavigate();
    const [isSelectingConnection, setIsSelectingConnection] = useState(false);
    const [newlyCreatedChat, setNewlyCreatedChat] = useState<Chat | null>(null);
    const [, setShowSelectTablesModal] = useState(false);
    const selectionSequence = useRef(0);

    const handleAddConnection = useCallback(async (
        connection: Connection,
        settings: ChatSettings,
    ): Promise<{ success: boolean; error?: string; chatId?: string }> => {
        try {
            const access = sourceAccess();
            const target = access.capture();
            const newChat = await chatService.createChat(connection, settings);
            access.assertCurrent(target);
            setChats(prev => [...prev, newChat]);
            setSuccessMessage(zhCN.toasts.connectionAdded);
            return { success: true, chatId: newChat.id };
        } catch (error: unknown) {
            toast.error(formatServiceError(error).summary, ERROR_TOAST);
            return { success: false, error: formatServiceError(error).summary };
        }
    }, [setChats, setSuccessMessage]);

    const handleDeleteConnection = useCallback(async (id: string) => {
        setChats(prev => prev.filter(c => c.id !== id));
        if (selectedConnection?.id === id) {
            setSelectedConnection(undefined);
            setMessages([]);
            navigate('/');
        }
        setSuccessMessage(zhCN.toasts.connectionDeleted);
    }, [selectedConnection, setChats, setSelectedConnection, setMessages, navigate, setSuccessMessage]);

    const handleEditConnection = useCallback(async (
        id: string,
        data: Connection,
        settings: ChatSettings,
    ): Promise<{ success: boolean; error?: string; updatedChat?: Chat }> => {
        const loadingId = toast.loading(zhCN.toasts.updatingConnection, {
            style: { background: '#000', color: '#fff', borderRadius: '12px', border: '4px solid #000' },
        });

        try {
            const credentialsChanged = data && selectedConnection && (
                selectedConnection.connection.database !== data.database ||
                selectedConnection.connection.host !== data.host ||
                selectedConnection.connection.port !== data.port ||
                selectedConnection.connection.username !== data.username
            );

            const access = sourceAccess();
            const target = access.capture();
            const response = await chatService.editChat(id, data, settings);
            access.assertCurrent(target);
            if (response) {
                setChats(prev => prev.map(c => c.id === id ? response : c));
                if (selectedConnection?.id === id) setSelectedConnection(response);

                if (credentialsChanged && streamId) {
                    try {
                        const nextTarget = await access.admit(id);
                        access.assertCurrent(target);
                        await chatService.disconnectFromConnection(id, streamId);
                        await new Promise(resolve => setTimeout(resolve, 1000));
                        access.assertCurrent(target);
                        access.assert(nextTarget);
                        await chatService.connectToConnection(id, streamId);
                        handleConnectionStatusChange(id, true, 'edit-connection');
                    } catch {
                        toast.error(zhCN.toasts.reconnectManual);
                    }
                } else if (credentialsChanged) {
                    toast.error(zhCN.toasts.reconnectNeeded);
                    handleConnectionStatusChange(id, false, 'edit-connection-no-stream');
                }

                toast.success(zhCN.toasts.connectionUpdated, SUCCESS_TOAST);
                toast.dismiss(loadingId);
                return { success: true, updatedChat: response };
            }

            toast.dismiss(loadingId);
            return { success: false, error: zhCN.toasts.updateConnectionFailed };
        } catch (error: unknown) {
            toast.dismiss(loadingId);
            return { success: false, error: formatServiceError(error).summary || zhCN.toasts.updateConnectionFailed };
        }
    }, [selectedConnection, setChats, setSelectedConnection, streamId, handleConnectionStatusChange]);

    const handleSelectConnection = useCallback(async (id: string) => {
        if (id === selectedConnection?.id) return;
        const cached = chats.find(c => c.id === id);
        if (!cached) return;
        const access = sourceAccess();
        access.select(id);
        const target = access.capture();
        const sequence = ++selectionSequence.current;
        const current = () => sequence === selectionSequence.current && access.isCurrent(target);
        setIsSelectingConnection(true);
        setMessages([]);
        setSelectedConnection(cached);
        navigate(`/chat/${id}`);
        try {
            const connection = await chatService.getChat(id);
            if (!current()) return;
            setChats(prev => prev.map(chat => chat.id === id ? connection : chat));
            setSelectedConnection(connection);
            await setupSSEConnection(id);
            const permit = await access.admit(id);
            if (!current()) return;
            access.assert(permit);
            if (!current()) return;
            const status = connectionStatuses[id] || await chatService.checkConnectionStatus(id);
            if (!current()) return;
            if (status) handleConnectionStatusChange(id, true, 'app-select-connection');
            else {
                if (!current()) return;
                access.assert(permit);
                await chatService.connectToConnection(id, streamId || generateStreamId());
                if (!current()) return;
                handleConnectionStatusChange(id, true, 'app-select-connection');
            }
        } catch (error) {
            if (current() && access.view(id).allowed) toast.error(formatServiceError(error).summary, ERROR_TOAST);
        } finally {
            if (current()) setIsSelectingConnection(false);
        }
    }, [chats, connectionStatuses, selectedConnection, streamId, generateStreamId, setMessages,
        setSelectedConnection, setChats, navigate, handleConnectionStatusChange, setupSSEConnection]);

    const handleCloseConnection = useCallback(async () => {
        selectionSequence.current++;
        sourceAccess().select(undefined);
        setIsSelectingConnection(true);
        navigate('/');

        if (eventSource) {
            eventSource.close();
            setEventSource(null);
            await chatService.disconnectFromConnection(selectedConnection?.id || '', streamId || '');
            handleConnectionStatusChange(selectedConnection?.id || '', false, 'app-close-connection');
        }

        if (selectedConnection) {
            handleConnectionStatusChange(selectedConnection.id, false, 'app-close-connection');
        }

        setMessages([]);
        setSelectedConnection(undefined);
        setTimeout(() => setIsSelectingConnection(false), 200);
    }, [eventSource, selectedConnection, streamId, setEventSource, setMessages, setSelectedConnection, navigate, handleConnectionStatusChange]);

    const handleDuplicateConnection = useCallback(async (chatId: string) => {
        try {
            const access = sourceAccess();
            const target = access.capture();
            const duplicated = await chatService.getChat(chatId);
            access.assertCurrent(target);
            setChats(prev => prev.some(c => c.id === duplicated.id) ? prev : [...prev, duplicated]);
            setSelectedConnection(duplicated);
            setSuccessMessage(zhCN.toasts.chatDuplicated);

            try {
                await setupSSEConnection(chatId);
                handleConnectionStatusChange(chatId, true, 'duplicate-connection');
            } catch {
                toast(zhCN.toasts.chatDuplicatedNoConn, {
                    style: { background: '#ffcc00', color: '#000', border: '4px solid #e6b800', borderRadius: '12px', padding: '12px 24px', boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)' },
                    icon: '⚠️',
                });
            }
        } catch (error: unknown) {
            toast.error(`${zhCN.toasts.duplicateFailed}${formatServiceError(error).summary}`, ERROR_TOAST);
        }
    }, [setChats, setSelectedConnection, setSuccessMessage, setupSSEConnection, handleConnectionStatusChange]);

    const handleUpdateSelectedCollections = useCallback(async (chatId: string, selectedCollections: string): Promise<void> => {
        const loadingId = toast.loading(zhCN.toasts.updatingTables, {
            style: { background: '#000', color: '#fff', borderRadius: '12px', border: '4px solid #000' },
        });

        try {
            const access = sourceAccess();
            const target = access.capture(chatId);
            await chatService.updateSelectedCollections(chatId, selectedCollections);
            access.assertCurrent(target);
            setChats(prev => prev.map(c => c.id === chatId ? { ...c, selected_collections: selectedCollections } : c));
            if (selectedConnection?.id === chatId) {
                setSelectedConnection({ ...selectedConnection, selected_collections: selectedCollections });
            }
            toast.dismiss(loadingId);

            if (newlyCreatedChat?.id === chatId) {
                setShowSelectTablesModal(false);
                setNewlyCreatedChat(null);
                await handleSelectConnection(chatId);
            }
        } catch (error: unknown) {
            toast.dismiss(loadingId);
            toast.error(formatServiceError(error).summary || zhCN.toasts.updateTablesFailed);
            throw error;
        }
    }, [selectedConnection, setChats, setSelectedConnection, newlyCreatedChat, handleSelectConnection]);

    const handleClearChat = useCallback(async () => {
        try {
            await axios.delete(`${import.meta.env.VITE_API_URL}/chats/${selectedConnection?.id}/messages`, {
                withCredentials: true,
                headers: { 'Authorization': `Bearer ${localStorage.getItem('token')}` },
            });
            setMessages([]);
        } catch (error: unknown) {
            toast.error(formatServiceError(error).summary, ERROR_TOAST);
        }
    }, [selectedConnection, setMessages]);

    return {
        isSelectingConnection,
        newlyCreatedChat,
        setNewlyCreatedChat,
        handleAddConnection,
        handleDeleteConnection,
        handleEditConnection,
        handleSelectConnection,
        handleCloseConnection,
        handleDuplicateConnection,
        handleUpdateSelectedCollections,
        handleClearChat,
    };
}
