import { ActionButton, Message, QueryResult, RawQueryResult } from "./query";
import { formatServiceError } from "../i18n/zh-CN";



// Update MessagesResponse to use BackendMessage instead of Message
export interface MessagesResponse {
    success: boolean;
    data: {
        messages: BackendMessage[];
        total: number;
    };
}

// Add interface for backend message format
export interface BackendMessage {
    id: string;
    chat_id: string;
    type: 'user' | 'assistant';
    content: string;
    user_message_id?: string;
    is_edited: boolean;
    non_tech_mode?: boolean;
    is_pinned?: boolean;
    pinned_at?: string;
    llm_model?: string;
    llm_model_name?: string; // Human-readable display name for the LLM model
    action_buttons?: ActionButton[];
    queries?: QueryResult[];
    assistant_run_id?: string;
    created_at: string;
}

function isExplicitServiceContent(content: string): boolean {
    const trimmed = content.trim()
    return trimmed === 'Operation cancelled by user' || /^Error:\s*\S[^\n]*$/.test(trimmed)
}

// Add transform function
export const transformBackendMessage = (msg: BackendMessage): Message => {
    const serviceError = msg.type === 'assistant' && isExplicitServiceContent(msg.content)
        ? formatServiceError(msg.content)
        : undefined

    return {
        id: msg.id,
        type: msg.type,
        content: serviceError?.summary ?? msg.content,
        serviceError,
        queries: msg.queries || [],

        user_message_id: msg.user_message_id,
        is_loading: false,
        loading_steps: [],
        is_streaming: false,
        is_edited: msg.is_edited,
        created_at: msg.created_at,
        action_buttons: msg.action_buttons || [],
        non_tech_mode: msg.non_tech_mode,
        is_pinned: msg.is_pinned,
        pinned_at: msg.pinned_at,
        llm_model: msg.llm_model,
        llm_model_name: msg.llm_model_name,
        assistant_run_id: msg.assistant_run_id,
    };
};

// Add interface for the API response
export interface SendMessageResponse {
    success: boolean;
    data: {
        id: string;
        chat_id: string;
        type: 'user' | 'assistant';
        content: string;
        created_at: string;
    };
}

export interface ExecuteQueryResponse {
    success: boolean;
    data: {
        chat_id: string;
        message_id: string;
        query_id: string;
        execution_time?: number | null;
        execution_result?: RawQueryResult;
        total_records_count: number | null;
        is_rolled_back: boolean;
        is_executed: boolean;
        error?: {
            code: string;
            message: string;
            details?: string;
        };
        action_buttons?: ActionButton[];
        action_at?: string;
    };
}