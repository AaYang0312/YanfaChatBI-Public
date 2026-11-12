import { BookOpen, Eraser, ListRestart, Loader, MoreHorizontal, PlugZap, RefreshCw, Search, PinIcon, Settings, Server, MessageCircleIcon } from 'lucide-react';
import { useUser } from '../../contexts/UserContext';
import { useCallback, useMemo, useRef, useState, useEffect } from 'react';
import { Chat } from '../../types/chat';
import analyticsService from '../../services/analyticsService';
import DatabaseLogo from '../icons/DatabaseLogos';
import DisconnectionTooltip from './DisconnectionTooltip';

import { DashboardViewMode } from '../../types/dashboard';
import ViewModeToggle from '../dashboard/ViewModeToggle';
import { zhCN } from '../../i18n/zh-CN';
import { chatDisplayName } from '../../utils/chatDisplayName';
import { useSourceAccess } from '../../hooks/useSourceAccess';
import KnowledgeBaseModal from '../modals/KnowledgeBaseModal';

interface ChatHeaderProps {
    chat: Chat;
    isConnecting: boolean;
    isConnected: boolean;
    onClearChat: () => void;
    onEditConnection: () => void;
    onShowCloseConfirm: () => void;
    onReconnect: () => void;
    setShowRefreshSchema: (show: boolean) => void;
    onToggleSearch: () => void;
    viewMode?: 'chats' | 'pinned';
    onViewModeChange?: (mode: 'chats' | 'pinned') => void;
    dashboardViewMode?: DashboardViewMode;
    onDashboardViewModeChange?: (mode: DashboardViewMode) => void;
    dashboardCount?: number;
    onOpenDataSync: (returnFocus: HTMLElement | null) => void;
    syncPending: boolean;
    onOpenModelProviders?: () => void;
}

export default function ChatHeader({
    chat,
    isConnecting = true,
    isConnected,
    onClearChat,
    onEditConnection,
    onShowCloseConfirm,
    onReconnect,
    setShowRefreshSchema,
    onToggleSearch,
    viewMode,
    onViewModeChange,
    dashboardViewMode,
    onDashboardViewModeChange,
    dashboardCount,
    onOpenDataSync,
    syncPending,
    onOpenModelProviders,
}: ChatHeaderProps) {
    const access = useSourceAccess(chat.id);
    const { user: headerUser } = useUser();
    const [showDisconnectionTooltip, setShowDisconnectionTooltip] = useState(false);
    const [knowledgeChatId, setKnowledgeChatId] = useState<string | null>(null);
    useEffect(() => { setKnowledgeChatId(null); }, [chat.id]);
    const [showDropdown, setShowDropdown] = useState(false);
    const [dropdownPosition, setDropdownPosition] = useState<{ top: number; left: number } | null>(null);
    const menuButton = useRef<HTMLButtonElement | null>(null);
    const menu = useRef<HTMLDivElement>(null);
    useEffect(() => {
        if (showDropdown) menu.current?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus();
    }, [showDropdown]);

    // Show tooltip when connection is lost
    useEffect(() => {
        if (!isConnected && !isConnecting) {
            // Show tooltip after a short delay
            const timer = setTimeout(() => {
                setShowDisconnectionTooltip(true);
            }, 1000);
            
            return () => clearTimeout(timer);
        } else {
            setShowDisconnectionTooltip(false);
        }
    }, [isConnected, isConnecting]);

    // Close dropdown when clicking outside
    useEffect(() => {
        const handleClickOutside = (event: MouseEvent) => {
            if (showDropdown) {
                const target = event.target as HTMLElement;
                if (!target.closest('.chat-header-dropdown') && !target.closest('.chat-header-menu-button')) {
                    setShowDropdown(false);
                    setDropdownPosition(null);
                }
            }
        };
        
        document.addEventListener('mousedown', handleClickOutside);
        return () => {
            document.removeEventListener('mousedown', handleClickOutside);
        };
    }, [showDropdown]);

    const connectionStatus = useMemo(() => {
        if (isConnecting) {
            return (
                <span className="text-yellow-600 text-sm font-medium bg-yellow-100 px-2 py-1 rounded flex items-center gap-2">
                    <Loader className="w-3 h-3 animate-spin" />
                    {zhCN.chat.connecting}
                </span>
            );
        }
        return isConnected ? (
            <span className="text-emerald-700 text-sm font-medium bg-emerald-100 px-2 py-1 rounded">
                {zhCN.chat.connected}
            </span>
        ) : (
            <span className="text-neo-error text-sm font-medium bg-neo-error/10 px-2 py-1 rounded">
                {zhCN.chat.disconnected}
            </span>
        );
    }, [isConnecting, isConnected]);

    // Wrap handlers with analytics events
    const handleClearChat = useCallback(() => {
        // Track clear chat click event
        analyticsService.trackEvent('clear_chat_clicked', { 
            chatId: chat.id,
            connectionName: chat.connection.database
        });
        
        onClearChat();
    }, [chat.id, chat.connection.database, onClearChat]);

    const handleEditConnection = useCallback(() => {
        // Track edit connection click event
        analyticsService.trackEvent('edit_connection_clicked', { 
            chatId: chat.id,
            connectionName: chat.connection.database
        });
        
        onEditConnection();
    }, [chat.id, chat.connection.database, onEditConnection]);

    const handleShowCloseConfirm = useCallback(() => {
        // Track disconnect click event
        analyticsService.trackEvent('disconnect_clicked', { 
            chatId: chat.id,
            connectionName: chat.connection.database
        });
        
        onShowCloseConfirm();
    }, [chat.id, chat.connection.database, onShowCloseConfirm]);

    const handleReconnect = useCallback(() => {
        if (!access.allowed) return;
        // Track reconnect click event
        analyticsService.trackEvent('reconnect_clicked', { 
            chatId: chat.id,
            connectionName: chat.connection.database
        });
        
        onReconnect();
    }, [chat.id, chat.connection.database, onReconnect, access.allowed]);

    const handleShowRefreshSchema = useCallback(() => {
        if (!access.allowed) return;
        // Track refresh schema click event
        analyticsService.trackEvent('refresh_schema_clicked', { 
            chatId: chat.id,
            connectionName: chat.connection.database
        });
        
        setShowRefreshSchema(true);
        setShowDropdown(false);
    }, [chat.id, chat.connection.database, setShowRefreshSchema, access.allowed]);

    const handleToggleDropdown = useCallback((e: React.MouseEvent<HTMLButtonElement>) => {
        e.preventDefault();
        e.stopPropagation();
        
        // Get the position of the button
        const button = e.currentTarget;
        menuButton.current = button;
        const rect = button.getBoundingClientRect();
        
        // Set the position for the dropdown
        setDropdownPosition({
            top: rect.bottom + 8,
            left: rect.right - 200 // Align dropdown to the right of the button
        });
        
        setShowDropdown(!showDropdown);
    }, [showDropdown]);

    const handleDropdownAction = useCallback((action: () => void) => {
        setShowDropdown(false);
        setDropdownPosition(null);
        menuButton.current?.focus();
        action();
    }, []);

    return (
        <>
            <div className="fixed top-0 left-0 right-0 md:relative md:left-auto md:right-auto bg-white border-b-4 border-black h-16 px-4 flex justify-between items-center mt-0 md:mt-0 z-20">
                <div className="flex items-center gap-2 overflow-hidden max-w-[60%]">
                    <DatabaseLogo type={chat.connection.type as "postgresql" | "yugabytedb" | "timescaledb" | "mysql" | "starrocks" | "mongodb" | "redis" | "clickhouse" | "neo4j"} size={32} className="transition-transform hover:scale-110" />
                    <h2 className="text-xl md:text-2xl font-bold truncate" title={chatDisplayName(chat)}>{chatDisplayName(chat)}</h2>
                    <span className="hidden md:inline-flex">{connectionStatus}</span>
                </div>
                <div className="flex items-center gap-2">
                    {/* Dashboard toggle */}
                    {onDashboardViewModeChange && dashboardViewMode && (
                        <ViewModeToggle
                            mode={dashboardViewMode}
                            onChange={onDashboardViewModeChange}
                            dashboardCount={dashboardCount}
                        />
                    )}

                {/* Reconnect button - only show when disconnected */}
                    {!isConnected && !isConnecting && (
                        <div className="relative group">
                            <button
                                onClick={handleReconnect}
                                disabled={!access.allowed}
                                title={access.reason}
                                className="p-2 bg-green-500 hover:bg-green-600 text-white rounded-lg transition-colors neo-border hidden md:block"
                                aria-label={zhCN.chat.reconnect}
                            >
                                <RefreshCw className="w-5 h-5" />
                            </button>
                            <button
                                onClick={handleReconnect}
                                disabled={!access.allowed}
                                title={access.reason}
                                className="p-2 bg-green-500 hover:bg-green-600 text-white rounded-lg transition-colors border-2 border-green-600 md:hidden"
                                aria-label={zhCN.chat.reconnect}
                            >
                                <RefreshCw className="w-5 h-5" />
                            </button>
                            <div className="absolute invisible opacity-0 group-hover:visible group-hover:opacity-100 transition-opacity duration-200 bottom-[-35px] left-1/2 transform -translate-x-1/2 bg-black text-white text-xs py-1 px-2 rounded whitespace-nowrap z-50 before:content-[''] before:absolute before:top-[-5px] before:left-1/2 before:transform before:-translate-x-1/2 before:border-[5px] before:border-transparent before:border-b-black">
                                {zhCN.chat.reconnect}
                            </div>
                            <DisconnectionTooltip
                                isVisible={showDisconnectionTooltip}
                                onClose={() => setShowDisconnectionTooltip(false)}
                            />
                        </div>
                    )}


                    {/* Search button - standalone (hidden in dashboard mode) */}
                    {dashboardViewMode !== 'dashboard' && (
                    <div className="relative group">
                        <button
                            onClick={onToggleSearch}
                            className="p-2 hover:bg-neo-gray rounded-lg transition-colors neo-border text-gray-800 hidden md:block"
                            aria-label={zhCN.chat.searchMessages}
                        >
                            <Search className="w-5 h-5" />
                        </button>
                        <button
                            onClick={onToggleSearch}
                            className="p-2 hover:bg-neo-gray rounded-lg transition-colors text-gray-800 md:hidden"
                            aria-label={zhCN.chat.searchMessages}
                        >
                            <Search className="w-5 h-5" />
                        </button>
                        <div className="absolute invisible opacity-0 group-hover:visible group-hover:opacity-100 transition-opacity duration-200 bottom-[-35px] left-1/2 transform -translate-x-1/2 bg-black text-white text-xs py-1 px-2 rounded whitespace-nowrap z-50 before:content-[''] before:absolute before:top-[-5px] before:left-1/2 before:transform before:-translate-x-1/2 before:border-[5px] before:border-transparent before:border-b-black">
                            {zhCN.chat.searchMessages}
                        </div>
                    </div>
                    )}

                    {/* Dropdown menu button */}
                    <div className="relative">
                        <button
                            onClick={handleToggleDropdown}
                            className="chat-header-menu-button p-2 hover:bg-neo-gray rounded-lg transition-colors neo-border text-gray-800 hidden md:block"
                            aria-label={zhCN.chat.chatOptions}
                            aria-expanded={showDropdown}
                            aria-haspopup="true"
                            onKeyDown={event => { if (event.key === 'ArrowDown') { event.preventDefault(); event.currentTarget.click(); } }}
                        >
                            <MoreHorizontal className="w-5 h-5" />
                        </button>
                        <button
                            onClick={handleToggleDropdown}
                            className="chat-header-menu-button p-2 hover:bg-neo-gray rounded-lg transition-colors text-gray-800 md:hidden"
                            aria-label={zhCN.chat.chatOptions}
                            aria-expanded={showDropdown}
                            aria-haspopup="true"
                            onKeyDown={event => { if (event.key === 'ArrowDown') { event.preventDefault(); event.currentTarget.click(); } }}
                        >
                            <MoreHorizontal className="w-5 h-5" />
                        </button>
                    </div>
                </div>
            </div>

            {knowledgeChatId === chat.id && <KnowledgeBaseModal chatId={chat.id} onClose={() => {
                setKnowledgeChatId(null);
                menuButton.current?.focus();
            }} />}

            {/* Dropdown Menu */}
            {showDropdown && dropdownPosition && (
                <div 
                    ref={menu}
                    role="group"
                    aria-label={zhCN.chat.chatOptions}
                    onKeyDown={event => {
                        if (event.key === 'Escape') { event.preventDefault(); setShowDropdown(false); menuButton.current?.focus(); }
                        if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
                            event.preventDefault();
                            const buttons = Array.from(menu.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []);
                            const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
                            const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length;
                            buttons[next]?.focus();
                        }
                    }}
                    onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) setShowDropdown(false); }}
                    className="chat-header-dropdown fixed w-52 bg-white border-4 border-black rounded-lg shadow-[4px_4px_0px_0px_rgba(0,0,0,1)] z-[100]"
                    style={{
                        top: `${dropdownPosition.top+6}px`,
                        left: `${dropdownPosition.left-6}px`,
                        transform: 'none'
                    }}
                    onClick={(e) => e.stopPropagation()}
                >
                    <div className="py-1">
                        {(access.chat ?? chat).source_classification === 'registered_erp' && <>
                            <button type="button" onClick={() => handleDropdownAction(() => onOpenDataSync(menuButton.current))}
                                className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors">
                                <RefreshCw className="w-4 h-4 mr-2" />
                                {syncPending || access.status?.active_job || access.status?.maintenance_state === 'syncing' || access.status?.maintenance_state === 'recovering' ? zhCN.dataSync.progress : zhCN.dataSync.menu}
                            </button>
                            <div className="h-px bg-gray-200 mx-2" />
                        </>}
                        <button type="button"
                            onClick={() => handleDropdownAction(() => { if (access.allowed) setKnowledgeChatId(chat.id); })}
                            disabled={!access.allowed}
                            title={access.reason}
                            className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors disabled:opacity-50"
                        >
                            <BookOpen className="w-4 h-4 mr-2" />
                            编辑知识库说明
                        </button>
                        <div className="h-px bg-gray-200 mx-2" />
                        <button 
                            onClick={() => handleDropdownAction(handleShowRefreshSchema)}
                            disabled={!access.allowed}
                            title={access.reason}
                            className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors"
                        >
                            <ListRestart className="w-4 h-4 mr-2 text-black" />
                            {zhCN.chat.refreshKnowledge}
                        </button>
                        <div className="h-px bg-gray-200 mx-2"></div>
                        <button 
                            onClick={() => handleDropdownAction(handleEditConnection)}
                            className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors"
                        >
                            <Settings className="w-4 h-4 mr-2 text-black" />
                            {zhCN.sidebar.chatSettings}
                        </button>
                        {headerUser?.is_admin && onOpenModelProviders && (
                            <>
                            <div className="h-px bg-gray-200 mx-2" />
                            <button 
                                onClick={() => handleDropdownAction(onOpenModelProviders)}
                                className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors"
                            >
                                <Server className="w-4 h-4 mr-2 text-black" />
                                模型设置
                            </button>
                            </>
                        )}
                        <div className="h-px bg-gray-200 mx-2"></div>
                        {dashboardViewMode !== 'dashboard' && (
                            <>
                        <button 
                            onClick={() => handleDropdownAction(() => {
                                if (onViewModeChange) {
                                    onViewModeChange(viewMode === 'pinned' ? 'chats' : 'pinned');
                                }
                            })}
                            className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-neo-gray transition-colors"
                        >
                            {viewMode === 'pinned' ? (
                                <>
                                    <MessageCircleIcon className="w-4 h-4 mr-2 text-black" />
                                    {zhCN.chat.showMessages}
                                </>
                            ) : (
                                <>
                                    <PinIcon className="w-4 h-4 mr-2 text-black rotate-45" />
                                    {zhCN.chat.showPinned}
                                </>
                            )}
                        </button>
                        <div className="h-px bg-gray-200 mx-2"></div>
                        <button 
                            onClick={() => handleDropdownAction(handleClearChat)}
                            className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-red-500 hover:bg-neo-error hover:text-white transition-colors"
                        >
                            <Eraser className="w-4 h-4 mr-2" />
                            {zhCN.chat.clearChat}
                        </button>
                        </>
                                                )}
                        {viewMode !== undefined && (
                            <div className="h-px bg-gray-200 mx-2"></div>
                        )}
                        {isConnected ? (
                            <button 
                                onClick={() => handleDropdownAction(handleShowCloseConfirm)}
                                className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-neo-gray transition-colors"
                            >
                                <PlugZap className="w-4 h-4 mr-2 text-black" />
                                {zhCN.chat.disconnect}
                            </button>
                        ) : (
                            <button 
                                onClick={() => handleDropdownAction(handleReconnect)}
                                disabled={!access.allowed}
                                title={access.reason}
                                className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-green-600 hover:bg-green-50 transition-colors"
                            >
                                <RefreshCw className="w-4 h-4 mr-2 text-green-600" />
                                {zhCN.chat.reconnect}
                            </button>
                        )}
                    </div>
                </div>
            )}
        </>
    );
}