import { Chat, Connection, TablesResponse, ChatSettings, ObjectSelection } from '../types/chat';
import { ExecuteQueryResponse, MessagesResponse, SendMessageResponse } from '../types/messages';
import axios from './axiosConfig';
import type { VisualizationServiceResponse } from '../types/visualization';
import { getResponseErrorMessage, getErrorMessage } from '../utils/message';
import { sourceAccess } from '../contexts/SourceAccessContext';

const API_URL = import.meta.env.VITE_API_URL;

interface CreateChatResponse {
    error?: string;
    success: boolean;
    data: Chat;
}

interface EditQueryResponse {
    chat_id: string;
    message_id: string;
    query_id: string;
    query: string;
    is_edited: boolean;
}

interface QueryRecommendationsResponse {
    success: boolean;
    data: {
        recommendations: Array<{
            text: string;
        }>;
    };
}

// No frontend tables caching — always fetch fresh from backend to avoid stale data.

const chatService = {
    async createChat(connection: Connection, settings: ChatSettings, displayName?: string | null, selectedObjects?: ObjectSelection): Promise<Chat> {
        const access = sourceAccess();
        const target = access.capture();
        access.assertCurrent(target);
        try {
            // Ensure we send the ssl_mode when it's present
            const connectionToSend = { ...connection };
            
            // Make sure ssl_mode is included if present
            if (connection.use_ssl && connection.ssl_mode) {
                connectionToSend.ssl_mode = connection.ssl_mode;
            }
            
            const response = await axios.post<CreateChatResponse>(`${API_URL}/chats`, {
                display_name: displayName,
                selected_objects: selectedObjects,
                connection: connectionToSend,
                settings: {
                    auto_execute_query: settings.auto_execute_query,
                    non_tech_mode: settings.non_tech_mode
                }
            });

            access.assertCurrent(target);
            if (!response.data.success) {
                throw new Error('Failed to create chat');
            }
            access.register(response.data.data);

            return response.data.data;
        } catch (error: unknown) {
            console.error('Create chat error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to create chat'));
        }
    },
    
    async editChat(chatId: string, connection?: Connection, settings?: ChatSettings, displayName?: string | null): Promise<Chat> {
        const access = sourceAccess();
        const target = connection ? await access.admit(chatId) : access.capture(chatId);
        if (connection) access.assert(target); else access.assertCurrent(target);
        try {
            // Ensure we send the ssl_mode when it's present
            const connectionToSend = { ...connection };
            
            // Make sure ssl_mode is included if present
            if (connection?.use_ssl && connection?.ssl_mode) {
                connectionToSend.ssl_mode = connection.ssl_mode;
            }
            
            const selectionChat = access.view(chatId).chat;
            const selection = connection && selectionChat?.selected_objects
                ? { selected_objects: selectionChat.selected_objects, selection_revision: selectionChat.selection_revision }
                : {};
            const payload = { ...selection, display_name: displayName, connection: connection ? connectionToSend : undefined, settings: {
                auto_execute_query: settings?.auto_execute_query,
                non_tech_mode: settings?.non_tech_mode,
                auto_generate_visualization: settings?.auto_generate_visualization
            } };
            
            const response = await axios.patch<CreateChatResponse>(
                `${API_URL}/chats/${chatId}`,
                payload,
                {
                    withCredentials: true,
                    headers: {
                        'Content-Type': 'application/json',
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );

            access.assertCurrent(target);
            if (!response.data.success) {
                throw new Error('Failed to edit chat');
            }
            if (!access.register(response.data.data)) throw new Error('对象选择或数据源绑定已变化，请重新加载对象。');

            return response.data.data;
        } catch (error: unknown) {
            console.error('Edit chat error:', error);
            throw new Error(getErrorMessage(error, 'Failed to edit chat'));
        }
    },
    async deleteChat(chatId: string): Promise<void> {
        try {
            const response = await axios.delete(`${API_URL}/chats/${chatId}`);

            if (!response.data.success && response.status !== 200) {
                throw new Error('Failed to delete chat');
            }
        } catch (error: unknown) {
            console.error('Delete chat error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to delete chat'));
        }
    },

    async checkConnectionStatus(chatId: string): Promise<boolean> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.get<{ success: boolean; data: { is_connected: boolean } }>(`${API_URL}/chats/${chatId}/connection-status`);
            access.assertCurrent(target);
            return response.data.success && response.data.data?.is_connected === true;
        } catch (error: unknown) {
            console.error('Check connection status error:', error);
            return false;
        }
    },

    async connectToConnection(chatId: string, streamId: string): Promise<void> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.post(`${API_URL}/chats/${chatId}/connect`, { stream_id: streamId });
            access.assertCurrent(target);
            return response.data.success;
        } catch (error: unknown) {
            console.error('Connect to connection error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to connect'));
        }
    },

    async disconnectFromConnection(chatId: string, streamId: string): Promise<void> {
        try {
            const response = await axios.post(`${API_URL}/chats/${chatId}/disconnect`, {
                stream_id: streamId
            }, {
                withCredentials: true,
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': `Bearer ${localStorage.getItem('token')}`
                }
            });
            return response.data.success;
        } catch (error: unknown) {
            console.error('Disconnect from connection error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to disconnect from connection'));
        }
    },

    async getMessages(chatId: string, page: number, perPage: number): Promise<MessagesResponse> {
        try {
            const response = await axios.get<MessagesResponse>(
                `${import.meta.env.VITE_API_URL}/chats/${chatId}/messages?page=${page}&page_size=${perPage}`,
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            return response.data;
        } catch (error: unknown) {
            console.error('Get messages error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to get messages'));
        }
    },
    async sendMessage(chatId: string, messageId: string, streamId: string, content: string, llmModel?: string): Promise<SendMessageResponse> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.post<SendMessageResponse>(
                `${API_URL}/chats/${chatId}/messages`,
                {
                    message_id: messageId,
                    stream_id: streamId,
                    content: content,
                    llm_model: llmModel
                },
                {
                    withCredentials: true,
                    headers: {
                        'Content-Type': 'application/json',
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            access.assertCurrent(target);
            return response.data
        } catch (error: unknown) {
            console.error('Send message error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to send message'));
        }
    },
    async cancelStream(chatId: string, streamId: string): Promise<void> {
        try {
            await axios.post(
                `${import.meta.env.VITE_API_URL}/chats/${chatId}/stream/cancel?stream_id=${streamId}`,
                {},
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
        } catch (error: unknown) {
            console.error('Cancel stream error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to cancel stream'));
        }
    },

    async executeQuery(chatId: string, messageId: string, queryId: string, streamId: string, controller: AbortController): Promise<ExecuteQueryResponse | undefined> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.post<ExecuteQueryResponse>(
                `${API_URL}/chats/${chatId}/queries/execute`,
                {
                    message_id: messageId,
                    query_id: queryId,
                    stream_id: streamId
                },
                {
                    signal: controller.signal,
                    withCredentials: true,
                    headers: {
                        'Content-Type': 'application/json',
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            console.log('chatService executeQuery response', response);
            access.assertCurrent(target);
            return response.data;
        } catch (error: unknown) {
            if (axios.isCancel(error) || (error instanceof Error && error.name === 'AbortError')) {
                return undefined;
            }
            console.error('Execute query error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to execute query'));
        }
    },

    async rollbackQuery(chatId: string, messageId: string, queryId: string, streamId: string, controller: AbortController): Promise<ExecuteQueryResponse | undefined> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.post<ExecuteQueryResponse>(`${API_URL}/chats/${chatId}/queries/rollback`, {
                message_id: messageId,
                query_id: queryId,
                stream_id: streamId
            },
                {
                    signal: controller.signal,
                    withCredentials: true,
                    headers: {
                        'Content-Type': 'application/json',
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            access.assertCurrent(target);
            return response.data;
        } catch (error: unknown) {
            if (axios.isCancel(error) || (error instanceof Error && error.name === 'AbortError')) {
                return undefined;
            }
            console.error('Rollback query error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to rollback query'));
        }
    },

    async refreshSchema(chatId: string, controller: AbortController): Promise<boolean> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.post(`${API_URL}/chats/${chatId}/refresh-schema`, {
                withCredentials: true,
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': `Bearer ${localStorage.getItem('token')}`
                }
            }, {
                signal: controller.signal,
            });
            access.assertCurrent(target);
            return response.data.success;
        } catch (error: unknown) {
            if (axios.isCancel(error) || (error instanceof Error && error.name === 'AbortError')) {
                return false;
            }
            console.error('Refresh knowledge base error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to refresh knowledge base'));
        }
    },

    async editQuery(
        chatId: string,
        messageId: string,
        queryId: string,
        query: string
    ): Promise<{ success: boolean; data: { success: boolean; data: EditQueryResponse } }> {
        try {
            const response = await axios.patch<{ success: boolean; data: EditQueryResponse }>(
                `${import.meta.env.VITE_API_URL}/chats/${chatId}/queries/edit`,
                {
                    "message_id": messageId,
                    "query_id": queryId,
                    "query": query
                },
                {
                    withCredentials: true,
                }
            );
            return { success: true, data: response.data };
        } catch (error: unknown) {
            throw getResponseErrorMessage(error, 'Failed to edit query');
        }
    },

    async updateSelectedObjects(chatId: string, selectedObjects: ObjectSelection, selectionRevision: number): Promise<Chat> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.patch<CreateChatResponse>(`${API_URL}/chats/${chatId}`, {
                selected_objects: selectedObjects,
                selection_revision: selectionRevision,
            });
            access.assertCurrent(target);
            if (!response.data.success) throw new Error(response.data.error || '选择保存失败，请重新加载对象后重试。');
            const updated = response.data.data;
            // A success envelope from an old/incompatible server must not erase v2 scope.
            if (!updated.selected_objects || updated.selection_revision === undefined ||
                updated.selection_revision <= selectionRevision) {
                throw new Error('服务端未确认对象选择版本；保留当前选择，请重新加载对象。');
            }
            const currentRevision = access.view(chatId).chat?.selection_revision ?? 0;
            if (updated.selection_revision < currentRevision) throw new Error('选择已变化，请重新加载对象后重试。');
            if (!access.register(updated)) throw new Error('数据源绑定已变化，请重新选择连接。');
            return updated;
        } catch (error: unknown) {
            if (axios.isAxiosError(error) && error.response?.status === 409) {
                throw new Error('选择已变化或对象功能不可用；保留当前选择，请重新加载对象后重试。');
            }
            throw new Error(getErrorMessage(error, '选择保存失败，请重新加载对象后重试。'));
        }
    },

    async updateSelectedCollections(chatId: string, selectedCollections: string): Promise<Chat> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        if (access.view(chatId).chat?.selected_objects) {
            throw new Error('当前聊天使用对象选择，请重新加载对象并通过 v2 保存。');
        }
        try {
            const response = await axios.patch<CreateChatResponse>(
                `${API_URL}/chats/${chatId}`,
                {
                    selected_collections: selectedCollections
                },
                {
                    withCredentials: true,
                    headers: {
                        'Content-Type': 'application/json',
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );

            access.assertCurrent(target);
            if (access.view(chatId).chat?.selected_objects) throw new Error('对象选择已升级，请重新加载对象后重试。');
            if (!response.data.success) {
                throw new Error('Failed to update selected collections');
            }

            return response.data.data;
        } catch (error: unknown) {
            console.error('Update selected collections error:', error);
            throw new Error(getErrorMessage(error, 'Failed to update selected collections'));
        }
    },

    // Add a method to get a single chat
    async getChat(chatId: string): Promise<Chat> {
        const access = sourceAccess();
        const target = access.capture(chatId);
        try {
            const response = await axios.get<{success: boolean, data: Chat}>(
                `${API_URL}/chats/${chatId}`,
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            access.assertCurrent(target);

            if (!response.data.success) {
                throw new Error('Failed to get chat');
            }
            if (!access.register(response.data.data)) throw new Error('数据源绑定已变化，请重新选择连接');

            return response.data.data;
        } catch (error: unknown) {
            console.error('Get chat error:', error);
            throw new Error(getErrorMessage(error, 'Failed to get chat'));
        }
    },

      // Add a method to get a single chat
      async duplicateChat(chatId: string, duplicateMessages: boolean = false, duplicateDashboards: boolean = false, selection?: ObjectSelection | string): Promise<Chat> {
        const access = sourceAccess();
        const target = access.capture(chatId);
        access.assertCurrent(target);
        try {
            const response = await axios.post<{success: boolean, data: Chat}>(
                `${API_URL}/chats/${chatId}/duplicate?duplicate_messages=${duplicateMessages}&duplicate_dashboards=${duplicateDashboards}${selection === undefined ? '' : typeof selection === 'string' ? `&selected_collections=${encodeURIComponent(selection)}` : `&selected_objects=${encodeURIComponent(JSON.stringify(selection))}`}`,
                {},  // Empty body
                {    // Proper options object with headers
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`,
                        'Content-Type': 'application/json'
                    }
                }
            );

            access.assertCurrent(target);
            if (!response.data.success) {
                throw new Error('Failed to duplicate chat');
            }
            access.register(response.data.data);

            return response.data.data;
        } catch (error: unknown) {
            console.error('Duplicate chat error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to duplicate chat'));
        }
    },

    async getTables(chatId: string): Promise<TablesResponse> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.get<{success: boolean, data: TablesResponse}>(
                `${API_URL}/chats/${chatId}/tables`,
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    },
                    timeout: 120000
                }
            );
            access.assertCurrent(target);

            if (!response.data.success) {
                throw new Error('Failed to get tables');
            }

            return response.data.data;
        } catch (error: unknown) {
            console.error('Get tables error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to get tables'));
        }
    },

    async updateAutoExecuteQuery(chatId: string, autoExecuteQuery: boolean): Promise<Chat> {
        try {
            const response = await axios.patch<CreateChatResponse>(
                `${API_URL}/chats/${chatId}`,
                {
                    auto_execute_query: autoExecuteQuery
                },
                {
                    withCredentials: true,
                    headers: {
                        'Content-Type': 'application/json',
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );

            if (!response.data.success) {
                throw new Error('Failed to update auto execute query setting');
            }

            return response.data.data;
        } catch (error: unknown) {
            console.error('Update auto execute query error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to update auto execute query setting'));
        }
    },

    async getQueryRecommendations(chatId: string, streamId?: string): Promise<QueryRecommendationsResponse> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const params = streamId ? { stream_id: streamId } : {};
            const response = await axios.get<QueryRecommendationsResponse>(
                `${API_URL}/chats/${chatId}/recommendations`,
                {
                    params,
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            access.assertCurrent(target);
            return response.data;
        } catch (error: unknown) {
            console.error('Get query recommendations error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to get query recommendations'));
        }
    },

    async pinMessage(chatId: string, messageId: string): Promise<{success: boolean}> {
        try {
            const response = await axios.post(
                `${API_URL}/chats/${chatId}/messages/${messageId}/pin`,
                {},
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            return response.data;
        } catch (error: unknown) {
            console.error('Pin message error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to pin message'));
        }
    },

    async unpinMessage(chatId: string, messageId: string): Promise<{success: boolean}> {
        try {
            const response = await axios.delete(
                `${API_URL}/chats/${chatId}/messages/${messageId}/pin`,
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            return response.data;
        } catch (error: unknown) {
            console.error('Unpin message error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to unpin message'));
        }
    },

    async getPinnedMessages(chatId: string): Promise<MessagesResponse> {
        try {
            const response = await axios.get(
                `${API_URL}/chats/${chatId}/messages/pinned`,
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`
                    }
                }
            );
            return response.data;
        } catch (error: unknown) {
            console.error('Get pinned messages error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to get pinned messages'));
        }
    },

    async generateVisualization(chatId: string, messageId: string, queryId: string): Promise<VisualizationServiceResponse> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.post<VisualizationServiceResponse>(
                `${API_URL}/chats/${chatId}/messages/${messageId}/visualizations`,
                {
                    query_id: queryId
                },
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`,
                        'Content-Type': 'application/json'
                    }
                }
            );
            access.assertCurrent(target);
            return response.data;
        } catch (error: unknown) {
            console.error('Generate visualization error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to generate visualization'));
        }
    },

    async getVisualizationData(chatId: string, messageId: string, queryId: string, limit: number = 500, offset: number = 0): Promise<VisualizationServiceResponse> {
        const access = sourceAccess();
        const target = await access.admit(chatId);
        access.assert(target);
        try {
            const response = await axios.post<VisualizationServiceResponse>(
                `${API_URL}/chats/${chatId}/visualization-data`,
                {
                    message_id: messageId,
                    query_id: queryId,
                    limit,
                    offset
                },
                {
                    withCredentials: true,
                    headers: {
                        'Authorization': `Bearer ${localStorage.getItem('token')}`,
                        'Content-Type': 'application/json'
                    }
                }
            );
            access.assertCurrent(target);
            return response.data;
        } catch (error: unknown) {
            console.error('Get visualization data error:', error);
            throw new Error(getResponseErrorMessage(error, 'Failed to load visualization data'));
        }
    }
};

export default chatService;
