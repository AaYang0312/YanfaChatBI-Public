import type { Chat } from '../types/chat';
import { zhCN } from '../i18n/zh-CN';

export function chatDisplayName(chat: Chat): string {
    return chat.display_name?.trim() || (chat.connection.is_example_db
        ? zhCN.common.sampleDatabase
        : chat.connection.database);
}
