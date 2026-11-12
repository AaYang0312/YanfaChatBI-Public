import {
  ChevronDown,
  Clock,
  Download,
  MoreHorizontal,
  Plus,
  RefreshCcw,
  Sparkles,
  Trash2,
  Upload,
  X,
} from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import {
  Dashboard,
  DashboardListItem,
  REFRESH_INTERVAL_OPTIONS,
} from '../../types/dashboard';
import { zhCN } from '../../i18n/zh-CN';
import { useSourceAccess } from '../../hooks/useSourceAccess';

interface DashboardHeaderProps {
  dashboardList: DashboardListItem[];
  activeDashboard: Dashboard | null;
  isConnected: boolean;
  isRefreshing: boolean;
  onSelectDashboard: (id: string) => void;
  onRefreshDashboard: () => void;
  onCancelRefresh?: () => void;
  onRefreshIntervalChange: (interval: number) => void;
  onNewDashboard: () => void;
  onAddWidget: () => void;
  onRegenerateDashboard: () => void;
  onDeleteDashboard: () => void;
  onExportDashboard: () => void;
  onImportDashboard: () => void;
}

export default function DashboardHeader({
  dashboardList,
  activeDashboard,
  isRefreshing,
  onSelectDashboard,
  onRefreshDashboard,
  onCancelRefresh,
  onRefreshIntervalChange,
  onNewDashboard,
  onAddWidget,
  onRegenerateDashboard,
  onDeleteDashboard,
  onExportDashboard,
  onImportDashboard,
}: Readonly<DashboardHeaderProps>) {
  const access = useSourceAccess(activeDashboard?.chat_id);
  const [showSelector, setShowSelector] = useState(false);
  const [selectorPosition, setSelectorPosition] = useState<{ top: number; left: number } | null>(null);
  const [showRefreshMenu, setShowRefreshMenu] = useState(false);
  const [refreshMenuPosition, setRefreshMenuPosition] = useState<{ top: number; right: number } | null>(null);
  const [showMoreMenu, setShowMoreMenu] = useState(false);
  const [moreMenuPosition, setMoreMenuPosition] = useState<{ top: number; left: number } | null>(null);

  const selectorBtnRef = useRef<HTMLButtonElement>(null);
  const refreshBtnRef = useRef<HTMLButtonElement>(null);
  const moreMenuBtnRef = useRef<HTMLButtonElement>(null);

  // Close dropdowns on outside click
  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (showSelector && !target.closest('.dashboard-selector-dropdown') && !target.closest('.dashboard-selector-btn')) {
        setShowSelector(false);
        setSelectorPosition(null);
      }
      if (showRefreshMenu && !target.closest('.dashboard-refresh-dropdown') && !target.closest('.dashboard-refresh-btn')) {
        setShowRefreshMenu(false);
        setRefreshMenuPosition(null);
      }
      if (showMoreMenu && !target.closest('.dashboard-more-dropdown') && !target.closest('.dashboard-more-btn')) {
        setShowMoreMenu(false);
        setMoreMenuPosition(null);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [showSelector, showRefreshMenu, showMoreMenu]);

  const handleToggleSelector = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (showSelector) {
      setShowSelector(false);
      setSelectorPosition(null);
      return;
    }
    const rect = e.currentTarget.getBoundingClientRect();
    setSelectorPosition({ top: rect.bottom + 8, left: rect.left });
    setShowSelector(true);
  };

  const handleToggleRefreshMenu = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (showRefreshMenu) {
      setShowRefreshMenu(false);
      setRefreshMenuPosition(null);
      return;
    }
    const rect = e.currentTarget.getBoundingClientRect();
    setRefreshMenuPosition({ top: rect.bottom + 8, right: window.innerWidth - rect.right });
    setShowRefreshMenu(true);
  };

  const handleToggleMoreMenu = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (showMoreMenu) {
      setShowMoreMenu(false);
      setMoreMenuPosition(null);
      return;
    }
    const rect = e.currentTarget.getBoundingClientRect();
    setMoreMenuPosition({
      top: rect.bottom + 8,
      left: rect.right - 214,
    });
    setShowMoreMenu(true);
  };

  // Filter out Manual from the refresh interval options
  const refreshOptions = REFRESH_INTERVAL_OPTIONS.filter((o) => o.value !== 0);

  const intervalLabel = (value: number, fallback: string) => {
    switch (value) {
      case 0: return zhCN.dashboard.off;
      case 15: return '15 秒';
      case 30: return '30 秒';
      case 60: return '1 分钟';
      case 300: return '5 分钟';
      case 600: return '10 分钟';
      case 3600: return '1 小时';
      default: return fallback;
    }
  };

  const currentRefreshLabel = intervalLabel(
    activeDashboard?.refresh_interval ?? 0,
    zhCN.dashboard.off,
  );

  return (
    <>
      <div className="flex items-center justify-between px-4 py-3 border-b-2 border-black backdrop-blur-sm flex-shrink-0">
        <div className="flex items-center gap-3 min-w-0">
          {/* Dashboard Selector Dropdown */}
          <div className="relative">
            <button
              ref={selectorBtnRef}
              onClick={handleToggleSelector}
              className="
                dashboard-selector-btn flex items-center gap-2 px-4 py-2
                bg-white border-2 border-black rounded-lg
                shadow-[2px_2px_0px_0px_rgba(0,0,0,1)]
                hover:shadow-[1px_1px_0px_0px_rgba(0,0,0,1)]
                hover:translate-x-[1px] hover:translate-y-[1px]
                transition-all duration-100
                max-w-[140px] md:max-w-[280px]
              "
            >
              <span className="text-base font-bold text-black truncate">
                {activeDashboard?.name ?? zhCN.dashboard.select}
              </span>
              <ChevronDown className="w-5 h-5 text-black flex-shrink-0" />
            </button>
          </div>
        </div>

        {/* Right side controls */}
        <div className="flex items-center gap-2.5 flex-shrink-0">
          {/* Manual refresh */}
          <div className="relative group flex items-center gap-1">
            {!isRefreshing ? (
              <button
                onClick={onRefreshDashboard}
                disabled={!access.allowed}
                title={access.reason || zhCN.dashboard.refreshNow}
                className="
                  p-2.5 bg-white border-2 border-black rounded-lg
                  shadow-[2px_2px_0px_0px_rgba(0,0,0,1)]
                  hover:shadow-[1px_1px_0px_0px_rgba(0,0,0,1)]
                  hover:translate-x-[1px] hover:translate-y-[1px]
                  transition-all duration-100
                "
              >
                <RefreshCcw className="w-5 h-5 text-black" />
              </button>
            ) : onCancelRefresh ? (
              <button
                onClick={onCancelRefresh}
                className="
                  p-2.5 bg-white border-2 border-black rounded-lg
                  shadow-[2px_2px_0px_0px_rgba(0,0,0,1)]
                  hover:shadow-[1px_1px_0px_0px_rgba(0,0,0,1)]
                  hover:translate-x-[1px] hover:translate-y-[1px]
                  hover:bg-red-50
                  transition-all duration-100
                "
                title={zhCN.dashboard.cancelRefresh}
              >
                <X className="w-5 h-5 text-red-500" />
              </button>
            ) : (
              <button
                disabled
                className="
                  p-2.5 bg-white border-2 border-black rounded-lg
                  shadow-[2px_2px_0px_0px_rgba(0,0,0,1)]
                  opacity-50 cursor-not-allowed
                "
              >
                <RefreshCcw className="w-5 h-5 text-black animate-spin" />
              </button>
            )}
            {!isRefreshing && (
              <div className="absolute invisible opacity-0 group-hover:visible group-hover:opacity-100 transition-opacity duration-200 bottom-[-35px] left-1/2 transform -translate-x-1/2 bg-black text-white text-xs py-1 px-2 rounded whitespace-nowrap z-50 before:content-[''] before:absolute before:top-[-5px] before:left-1/2 before:transform before:-translate-x-1/2 before:border-[5px] before:border-transparent before:border-b-black">
                {zhCN.dashboard.refreshNow}
              </div>
            )}
          </div>

          {/* Refresh interval selector */}
          <div className="relative group">
            <button
              ref={refreshBtnRef}
              onClick={handleToggleRefreshMenu}
              className="
                dashboard-refresh-btn flex items-center gap-1.5 px-3 py-2.5
                text-sm font-bold text-black
                bg-white border-2 border-black rounded-lg
                shadow-[2px_2px_0px_0px_rgba(0,0,0,1)]
                hover:shadow-[1px_1px_0px_0px_rgba(0,0,0,1)]
                hover:translate-x-[1px] hover:translate-y-[1px]
                transition-all duration-100
              "
            >
              <Clock className="w-5 h-5" />
              <span className="text-sm font-bold">{currentRefreshLabel}</span>
            </button>
            {!showRefreshMenu && (
              <div className="absolute invisible opacity-0 group-hover:visible group-hover:opacity-100 transition-opacity duration-200 bottom-[-35px] left-1/2 transform -translate-x-1/2 bg-black text-white text-xs py-1 px-2 rounded whitespace-nowrap z-50 before:content-[''] before:absolute before:top-[-5px] before:left-1/2 before:transform before:-translate-x-1/2 before:border-[5px] before:border-transparent before:border-b-black">
                {zhCN.dashboard.autoRefresh}
              </div>
            )}
          </div>

          {/* More menu */}
          <div className="relative group">
            <button
              ref={moreMenuBtnRef}
              onClick={handleToggleMoreMenu}
              className="
                dashboard-more-btn p-2.5 bg-white border-2 border-black rounded-lg
                shadow-[2px_2px_0px_0px_rgba(0,0,0,1)]
                hover:shadow-[1px_1px_0px_0px_rgba(0,0,0,1)]
                hover:translate-x-[1px] hover:translate-y-[1px]
                transition-all duration-100
              "
            >
              <MoreHorizontal className="w-5 h-5 text-black" />
            </button>
            {!showMoreMenu && (
              <div className="absolute invisible opacity-0 group-hover:visible group-hover:opacity-100 transition-opacity duration-200 bottom-[-35px] left-1/2 transform -translate-x-1/2 bg-black text-white text-xs py-1 px-2 rounded whitespace-nowrap z-50 before:content-[''] before:absolute before:top-[-5px] before:left-1/2 before:transform before:-translate-x-1/2 before:border-[5px] before:border-transparent before:border-b-black">
                {zhCN.dashboard.more}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Dashboard Selector Dropdown (fixed) */}
      {showSelector && selectorPosition && (
        <div
          className="dashboard-selector-dropdown fixed w-80 bg-white border-4 border-black rounded-lg shadow-[4px_4px_0px_0px_rgba(0,0,0,1)] z-[100] py-1 max-h-64 overflow-y-auto"
          style={{ top: `${selectorPosition.top}px`, left: `${selectorPosition.left}px` }}
          onClick={(e) => e.stopPropagation()}
        >
          {dashboardList.map((d) => (
            <button
              key={d.id}
              onClick={() => {
                onSelectDashboard(d.id);
                setShowSelector(false);
                setSelectorPosition(null);
              }}
              className={`
                w-full text-left px-4 py-2.5 font-medium
                hover:bg-gray-200 transition-colors
                ${activeDashboard?.id === d.id ? 'bg-[#FFDB58]' : ''}
              `}
            >
              <div className="text-base font-bold text-black truncate">{d.name}</div>
              {d.description && (
                <div className="text-sm text-gray-500 truncate mt-0.5">{d.description}</div>
              )}
            </button>
          ))}
          <div className="border-t-2 border-gray-200 mt-1 pt-1 mx-2">
            <button
              disabled={!access.allowed}
              title={access.reason}
              onClick={() => {
                setShowSelector(false);
                setSelectorPosition(null);
                onNewDashboard();
              }}
              className="w-full text-left px-3 py-2.5 text-sm font-bold text-black hover:bg-gray-200 flex items-center gap-2 rounded-lg"
            >
              <Plus className="w-5 h-5" />
              {zhCN.dashboard.new}
            </button>
          </div>
        </div>
      )}

      {/* Refresh Interval Dropdown (fixed) */}
      {showRefreshMenu && refreshMenuPosition && (
        <div
          className="dashboard-refresh-dropdown fixed w-36 bg-white border-4 border-black rounded-lg shadow-[4px_4px_0px_0px_rgba(0,0,0,1)] z-[100]"
          style={{ top: `${refreshMenuPosition.top}px`, right: `${refreshMenuPosition.right}px` }}
          onClick={(e) => e.stopPropagation()}
        >
          <div className="px-4 py-2 border-b-2 border-gray-200">
            <span className="text-xs font-bold text-gray-400 uppercase tracking-wider">{zhCN.dashboard.autoRefresh}</span>
          </div>
          <div className="py-1">
            <button
              onClick={() => {
                onRefreshIntervalChange(0);
                setShowRefreshMenu(false);
                setRefreshMenuPosition(null);
              }}
              className={`
                w-full text-left px-4 py-2 text-sm font-semibold
                hover:bg-gray-200 transition-colors
                ${(activeDashboard?.refresh_interval ?? 0) === 0 ? 'bg-[#FFDB58] font-bold' : ''}
              `}
            >
              {zhCN.dashboard.off}
            </button>
            <div className="h-px bg-gray-200 mx-2" />
            {refreshOptions.map((opt) => (
              <button
                key={opt.value}
                onClick={() => {
                  onRefreshIntervalChange(opt.value);
                  setShowRefreshMenu(false);
                  setRefreshMenuPosition(null);
                }}
                className={`
                  w-full text-left px-4 py-2 text-sm font-semibold
                  hover:bg-gray-200 transition-colors
                  ${activeDashboard?.refresh_interval === opt.value ? 'bg-[#FFDB58] font-bold' : ''}
                `}
              >
                {intervalLabel(opt.value, opt.label)}
              </button>
            ))}
          </div>
        </div>
      )}
      {showMoreMenu && moreMenuPosition && (
        <div
          className="dashboard-more-dropdown fixed w-52 bg-white border-4 border-black rounded-lg shadow-[4px_4px_0px_0px_rgba(0,0,0,1)] z-[100]"
          style={{ top: `${moreMenuPosition.top}px`, left: `${moreMenuPosition.left}px` }}
          onClick={(e) => e.stopPropagation()}
        >
          <div className="py-1">
            <button
              disabled={!access.allowed}
              title={access.reason}
              onClick={() => {
                setShowMoreMenu(false);
                setMoreMenuPosition(null);
                onAddWidget();
              }}
              className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors"
            >
              <Plus className="w-4 h-4 mr-2 text-black" />
              {zhCN.dashboard.addWidget}
            </button>
            <div className="h-px bg-gray-200 mx-2" />
            <button
              disabled={!access.allowed}
              title={access.reason}
              onClick={() => {
                setShowMoreMenu(false);
                setMoreMenuPosition(null);
                onRegenerateDashboard();
              }}
              className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors"
            >
              <Sparkles className="w-4 h-4 mr-2 text-black" />
              {zhCN.dashboard.regenerate}
            </button>
            <div className="h-px bg-gray-200 mx-2" />
            <div className="relative group">
              <button className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors">
                <Download className="w-4 h-4 mr-2 text-black" />
                {zhCN.dashboard.importExport}
              </button>
              <div className="hidden group-hover:block absolute right-full top-0 mr-1 w-40 bg-white border-4 border-black rounded-lg shadow-[4px_4px_0px_0px_rgba(0,0,0,1)] z-50 py-1">
                <button
                  onClick={() => {
                    setShowMoreMenu(false);
                    setMoreMenuPosition(null);
                    onExportDashboard();
                  }}
                  className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors"
                >
                  <Download className="w-4 h-4 mr-2" />
                  {zhCN.dashboard.export}
                </button>
                <button
                  onClick={() => {
                    setShowMoreMenu(false);
                    setMoreMenuPosition(null);
                    onImportDashboard();
                  }}
                  className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-black hover:bg-gray-200 transition-colors"
                >
                  <Upload className="w-4 h-4 mr-2" />
                  {zhCN.dashboard.import}
                </button>
              </div>
            </div>
            <div className="h-px bg-gray-200 mx-2" />
            <button
              onClick={() => {
                setShowMoreMenu(false);
                setMoreMenuPosition(null);
                onDeleteDashboard();
              }}
              className="flex items-center w-full text-left px-4 py-2 text-sm font-semibold text-red-500 hover:bg-neo-error hover:text-white transition-colors"
            >
              <Trash2 className="w-4 h-4 mr-2" />
              {zhCN.dashboard.deleteDashboard}
            </button>
          </div>
        </div>
      )}
    </>
  );
}
