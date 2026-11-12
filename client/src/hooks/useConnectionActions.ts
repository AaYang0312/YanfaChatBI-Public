import { useCallback, useState } from 'react';
import toast from 'react-hot-toast';
import { useStream } from '../contexts/StreamContext';
import { formatServiceError, zhCN } from '../i18n/zh-CN';
import chatService from '../services/chatService';
import { sourceAccess } from '../contexts/SourceAccessContext';

interface UseConnectionActionsParams {
    chatId: string;
    checkSSEConnection: () => Promise<void>;
    onCloseConnection: () => void;
    onConnectionStatusChange?: (chatId: string, isConnected: boolean, from: string) => void;
}

export function useConnectionActions({
    chatId,
    checkSSEConnection,
    onCloseConnection,
    onConnectionStatusChange,
}: UseConnectionActionsParams) {
    const [isConnecting, setIsConnecting] = useState(false);
    const { streamId, generateStreamId } = useStream();

    const checkConnectionStatus = useCallback(async (id: string) => {
        return { is_connected: await chatService.checkConnectionStatus(id) };
    }, []);

    const connectToDatabase = useCallback(async (id: string, sid: string) => {
        await chatService.connectToConnection(id, sid);
    }, []);

    const handleReconnect = useCallback(async () => {
        try {
            setIsConnecting(true);
            const access = sourceAccess();
            const target = await access.admit(chatId);
            access.assert(target);
            const sid = streamId || generateStreamId();
            await checkSSEConnection();
            access.assert(target);
            await new Promise(resolve => setTimeout(resolve, 200));
            access.assert(target);
            const status = await checkConnectionStatus(chatId);
            access.assert(target);
            if (!status?.is_connected) {
                await connectToDatabase(chatId, sid);
            }
            access.assertCurrent(target);
            onConnectionStatusChange?.(chatId, true, 'chat-window-reconnect');
        } catch (error) {
            console.error('Failed to reconnect to data source:', error);
            onConnectionStatusChange?.(chatId, false, 'chat-window-reconnect');
            toast.error(`${zhCN.toasts.reconnectFailed}${formatServiceError(error).summary}`, {
                style: {
                    background: '#ff4444', color: '#fff',
                    border: '4px solid #cc0000', borderRadius: '12px',
                    boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)', padding: '12px 24px',
                },
            });
            throw error;
        } finally {
            setIsConnecting(false);
        }
    }, [chatId, streamId, generateStreamId, checkSSEConnection, onConnectionStatusChange, checkConnectionStatus, connectToDatabase]);

    const handleDisconnect = useCallback(async () => {
        try {
            await onCloseConnection();
        } catch (error) {
            console.error('Failed to disconnect:', error);
        }
    }, [onCloseConnection]);

    return {
        isConnecting,
        setIsConnecting,
        handleReconnect,
        handleDisconnect,
        connectToDatabase,
        checkConnectionStatus,
    };
}
