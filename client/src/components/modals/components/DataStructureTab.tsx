import React, { useState, useEffect, useRef, useCallback } from 'react';
import { 
  ChevronDown, 
  ChevronRight, 
  Loader2, 
  Trash2, 
  Download, 
  Copy, 
  ChevronLeft,
  ChevronRight as ChevronRightIcon,
  ChevronsLeft,
  ChevronsRight,
  AlertCircle
} from 'lucide-react';
import ConfirmationModal from '../../modals/ConfirmationModal';
import toast from 'react-hot-toast';
import chatService from '../../../services/chatService';
import uploadService from '../../../services/uploadService';
import { formatServiceError, zhCN } from '../../../i18n/zh-CN';
import type { QueryRow } from '../../../types/query';
import { sourceAccess } from '../../../contexts/SourceAccessContext';
import { useSourceAccess } from '../../../hooks/useSourceAccess';

interface DataStructureTabProps {
  chatId: string;
  isLoadingData?: boolean;
  onDeleteTable?: (tableName: string) => void;
  onDownloadData?: (tableName: string) => void;
  onRefreshData?: () => void;
}

interface TableData {
  name: string;
  columns: Array<{
    name: string;
    type: string;
  }>;
  rowCount: number;
  sizeBytes: number;
  sourceFile: string;
  previewData: QueryRow[];
}

interface PaginationState {
  page: number;
  pageSize: number;
  totalRows: number;
}

export default function DataStructureTab({
  chatId,
  isLoadingData = false,
  onDeleteTable,
}: DataStructureTabProps) {
  const access = useSourceAccess(chatId);
  const [tables, setTables] = useState<TableData[]>([]);
  const [expandedTables, setExpandedTables] = useState<Record<string, boolean>>({});
  const [selectedRows, setSelectedRows] = useState<Record<string, Set<number>>>({});
  const [pagination, setPagination] = useState<Record<string, PaginationState>>({});
  const [loading, setLoading] = useState(false);
  const [loadingPreview, setLoadingPreview] = useState<Record<string, boolean>>({});
  const [deleteConfirmation, setDeleteConfirmation] = useState<{ show: boolean; tableName: string | null; isSelectedRows?: boolean; rowId?: string | number }>({ show: false, tableName: null, isSelectedRows: false });
  const [downloadMenu, setDownloadMenu] = useState<{ show: boolean; tableName: string | null; x: number; y: number }>({ show: false, tableName: null, x: 0, y: 0 });
  const [error, setError] = useState<string | null>(null);
  
  // Track which tables are currently loading preview data to prevent duplicates
  const loadingPreviewRef = useRef<Record<string, boolean>>({});



  const loadTablePreviewData = useCallback(async (tableName: string, page: number = 1, pageSize: number = 15) => {
    // Prevent duplicate calls - check ref synchronously
    const loadingKey = `${tableName}-${page}-${pageSize}`;
    if (loadingPreviewRef.current[loadingKey]) {
      console.log(`⏭️ Skipping duplicate preview load for ${tableName} (already loading)`);
      return;
    }
    
    console.log(`📊 Loading preview for ${tableName} page=${page} size=${pageSize}`);
    loadingPreviewRef.current[loadingKey] = true;
    setLoadingPreview(prev => ({ ...prev, [tableName]: true }));
    
    try {
      const target = await sourceAccess().admit(chatId);
      sourceAccess().assert(target);
      const responseData = await uploadService.getTablePreviewData(chatId, tableName, page, pageSize);
      sourceAccess().assertCurrent(target);
      setTables(prev => prev.map(table => 
        table.name === tableName 
          ? { ...table, previewData: responseData.rows || [] }
          : table
      ));
      // Update pagination total if provided
      if (responseData.total_rows !== undefined) {
        setPagination(prev => ({
          ...prev,
          [tableName]: {
            ...prev[tableName],
            totalRows: responseData.total_rows
          }
        }));
      }
    } catch (error) {
      console.error('Failed to load preview data:', error);
    } finally {
      setLoadingPreview(prev => ({ ...prev, [tableName]: false }));
      delete loadingPreviewRef.current[loadingKey];
      console.log(`✅ Finished loading preview for ${tableName}`);
    }
  }, [chatId]);
  const loadTableData = useCallback(async () => {
    console.log('DataStructureTab: loadTableData called for chatId:', chatId);
    setLoading(true);
    setError(null);
    try {
      const target = await sourceAccess().admit(chatId);
      const tablesResponse = await chatService.getTables(chatId);
      sourceAccess().assert(target);
      
      if (tablesResponse.tables && tablesResponse.tables.length > 0) {
        // Transform API response to match TableData structure
        const transformedTables: TableData[] = tablesResponse.tables.map(table => ({
          name: table.name,
          columns: table.columns || [],
          rowCount: table.row_count || 0,
          sizeBytes: table.size_bytes || 0,
          sourceFile: table.source_file || 'uploaded file',
          previewData: []
        }));
        setTables(transformedTables);
        
        // Initialize pagination for each table
        const initialPagination: Record<string, PaginationState> = {};
        transformedTables.forEach(table => {
          initialPagination[table.name] = {
            page: 1,
            pageSize: 15,
            totalRows: table.rowCount || 0
          };
        });
        setPagination(initialPagination);
        
        // Load preview data for first table by default
        if (transformedTables.length > 0) {
          setExpandedTables({ [transformedTables[0].name]: true });
          loadTablePreviewData(transformedTables[0].name, 1, 15);
        }
      } else {
        setTables([]);
      }
    } catch (error: unknown) {
      console.error('Failed to load table data:', error);
      setError(formatServiceError(error).summary);
      // Keep the displayed snapshot when a refresh is denied.
      
      // Show error toast
      toast.error(formatServiceError(error).summary, {
        duration: 4000,
        style: {
          background: '#ff4444',
          color: '#fff',
          border: '4px solid #cc0000',
          borderRadius: '12px',
          boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
          padding: '12px 24px',
          fontSize: '16px',
          fontWeight: 'bold',
        },
      });
    } finally {
      setLoading(false);
    }
  }, [chatId, loadTablePreviewData]);
  useEffect(() => {
    console.log('DataStructureTab useEffect triggered:', { chatId });
    if (chatId) {
      loadTableData();
    }
  }, [chatId, loadTableData]);

  const toggleTableExpansion = async (tableName: string) => {
    const isExpanding = !expandedTables[tableName];
    setExpandedTables(prev => ({
      ...prev,
      [tableName]: isExpanding
    }));
    
    // Load preview data when expanding
    if (isExpanding) {
      const table = tables.find(t => t.name === tableName);
      if (table && table.previewData.length === 0) {
        const currentPagination = pagination[tableName] || { page: 1, pageSize: 15, totalRows: 0 };
        await loadTablePreviewData(tableName, currentPagination.page, currentPagination.pageSize);
      }
    }
  };

  const handleRowSelection = (tableName: string, rowIndex: number) => {
    setSelectedRows(prev => {
      const tableSelections = new Set(prev[tableName] || []);
      if (tableSelections.has(rowIndex)) {
        tableSelections.delete(rowIndex);
      } else {
        tableSelections.add(rowIndex);
      }
      return {
        ...prev,
        [tableName]: tableSelections
      };
    });
  };

  const handleSelectAllRows = (tableName: string, data: QueryRow[]) => {
    setSelectedRows(prev => {
      const isAllSelected = prev[tableName]?.size === data.length;
      if (isAllSelected) {
        return {
          ...prev,
          [tableName]: new Set()
        };
      } else {
        return {
          ...prev,
          [tableName]: new Set(data.map((_, index) => index))
        };
      }
    });
  };

  const handleCopyRow = (row: QueryRow) => {
    const text = JSON.stringify(row, null, 2);
    navigator.clipboard.writeText(text).then(() => {
      toast.success(zhCN.common.copied, {
        duration: 2000,
        style: {
          background: '#000',
          color: '#fff',
          border: '4px solid #000',
          borderRadius: '12px',
          boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
          padding: '12px 24px',
          fontSize: '16px',
          fontWeight: '500',
        },
      });
    }).catch(() => {
      toast.error('复制行数据失败', {
        duration: 2000,
        style: {
          background: '#ff4444',
          color: '#fff',
          border: '4px solid #cc0000',
          borderRadius: '12px',
          boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
          padding: '12px 24px',
          fontSize: '16px',
          fontWeight: 'bold',
        },
      });
    });
  };

  const handleDownloadClick = (e: React.MouseEvent, tableName: string) => {
    e.stopPropagation();
    const rect = e.currentTarget.getBoundingClientRect();
    setDownloadMenu({ show: true, tableName, x: rect.left, y: rect.bottom + 5 });
  };

  const handleDownload = async (format: 'csv' | 'xlsx') => {
    if (downloadMenu.tableName) {
      const loadingToast = toast.loading('正在准备下载…', {
        style: {
          background: '#000',
          color: '#fff',
          border: '4px solid #000',
          borderRadius: '12px',
          boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
          padding: '12px 24px',
          fontSize: '16px',
          fontWeight: '500',
        },
      });

      try {
        // Check if we're downloading selected rows
        const selectedRowIndices = selectedRows[downloadMenu.tableName];
        const isSelectedDownload = selectedRowIndices && selectedRowIndices.size > 0;
        
        // Get the table data
        const table = tables.find(t => t.name === downloadMenu.tableName);
        if (!table) {
          throw new Error('Table not found');
        }

        // If we're downloading selected rows but no data is loaded, we can't proceed
        if (isSelectedDownload && (!table.previewData || table.previewData.length === 0)) {
          toast.dismiss(loadingToast);
          toast.error('请先展开表以加载数据，再下载选中行', {
            duration: 3000,
            style: {
              background: '#ff4444',
              color: '#fff',
              border: '4px solid #cc0000',
              borderRadius: '12px',
              boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
              padding: '12px 24px',
              fontSize: '16px',
              fontWeight: 'bold',
            },
          });
          return;
        }

        // For full table download, we'll use the server endpoint directly
        // For selected rows with CSV, we need the preview data
        if (format === 'csv' && isSelectedDownload) {
          const dataToDownload = table.previewData.filter((_, index) => selectedRowIndices.has(index));
          
          if (dataToDownload.length === 0) {
            toast.dismiss(loadingToast);
            toast.error('没有可下载的数据', {
              duration: 2000,
              style: {
                background: '#ff4444',
                color: '#fff',
                border: '4px solid #cc0000',
                borderRadius: '12px',
                boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
                padding: '12px 24px',
                fontSize: '16px',
                fontWeight: 'bold',
              },
            });
            return;
          }

          // Create CSV content for selected rows
          const headers = table.columns.map(col => col.name).join(',');
          const rows = dataToDownload.map(row => 
            table.columns.map(col => {
              const value = row[col.name];
              if (value === null || value === undefined) return '';
              // Escape quotes and wrap in quotes if contains comma or newline
              const stringValue = String(value);
              if (stringValue.includes(',') || stringValue.includes('\n') || stringValue.includes('"')) {
                return `"${stringValue.replace(/"/g, '""')}"`;
              }
              return stringValue;
            }).join(',')
          );
          
          const csvContent = [headers, ...rows].join('\n');
          const blob = new Blob([csvContent], { type: 'text/csv;charset=utf-8;' });
          const url = window.URL.createObjectURL(blob);
          const a = document.createElement('a');
          a.href = url;
          a.download = `${downloadMenu.tableName}_selected.csv`;
          document.body.appendChild(a);
          a.click();
          window.URL.revokeObjectURL(url);
          document.body.removeChild(a);
        } else {
          // For full table downloads (CSV or XLSX) or selected XLSX, use server endpoint
          let rowIds: string[] | undefined;
          if (isSelectedDownload && format === 'xlsx') {
            if (table.previewData && table.previewData.length > 0) {
              const selectedRowIds = table.previewData
                .filter((_, index) => selectedRowIndices.has(index))
                .map(row => row._id ? String(row._id) : '')
                .filter(id => id !== '');
              if (selectedRowIds.length > 0) {
                rowIds = selectedRowIds;
              }
            }
          }

          const target = await sourceAccess().admit(chatId);
          sourceAccess().assert(target);
          const blob = await uploadService.downloadTableData(chatId, downloadMenu.tableName, format, rowIds);
          sourceAccess().assertCurrent(target);
          const blobUrl = window.URL.createObjectURL(blob);
          const a = document.createElement('a');
          a.href = blobUrl;
          a.download = `${downloadMenu.tableName}${isSelectedDownload ? '_selected' : ''}.${format}`;
          document.body.appendChild(a);
          a.click();
          window.URL.revokeObjectURL(blobUrl);
          document.body.removeChild(a);
        }

        toast.dismiss(loadingToast);
        toast.success('下载完成', {
          duration: 2000,
          style: {
            background: '#000',
            color: '#fff',
            border: '4px solid #000',
            borderRadius: '12px',
            boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
            padding: '12px 24px',
            fontSize: '16px',
            fontWeight: '500',
          },
        });
      } catch (error) {
        toast.dismiss(loadingToast);
        toast.error('下载失败', {
          duration: 2000,
          style: {
            background: '#ff4444',
            color: '#fff',
            border: '4px solid #cc0000',
            borderRadius: '12px',
            boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
            padding: '12px 24px',
            fontSize: '16px',
            fontWeight: 'bold',
          },
        });
        console.error('Download failed:', error);
      }
    }
    setDownloadMenu({ show: false, tableName: null, x: 0, y: 0 });
  };

  const handleDeleteRow = async (tableName: string, rowId: number | string) => {
    const loadingToast = toast.loading('正在删除行…', {
      style: {
        background: '#000',
        color: '#fff',
        border: '4px solid #000',
        borderRadius: '12px',
        boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
        padding: '12px 24px',
        fontSize: '16px',
        fontWeight: '500',
      },
    });

    try {
      const target = await sourceAccess().admit(chatId);
      sourceAccess().assert(target);
      const ok = await uploadService.deleteTableRow(chatId, tableName, rowId);
      sourceAccess().assert(target);

      if (ok) {
        toast.dismiss(loadingToast);
        toast.success('行已删除', {
          duration: 2000,
          style: {
            background: '#000',
            color: '#fff',
            border: '4px solid #000',
            borderRadius: '12px',
            boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
            padding: '12px 24px',
            fontSize: '16px',
            fontWeight: '500',
          },
        });
        
        // Reload table data to update row count and size
        await loadTableData();
        sourceAccess().assert(target);
        
        // Reload the preview data
        const currentPagination = pagination[tableName] || { page: 1, pageSize: 15, totalRows: 0 };
        await loadTablePreviewData(tableName, currentPagination.page, currentPagination.pageSize);
      } else {
        throw new Error('Failed to delete row');
      }
    } catch (error) {
      toast.dismiss(loadingToast);
      toast.error('删除行失败', {
        duration: 2000,
        style: {
          background: '#ff4444',
          color: '#fff',
          border: '4px solid #cc0000',
          borderRadius: '12px',
          boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
          padding: '12px 24px',
          fontSize: '16px',
          fontWeight: 'bold',
        },
      });
      console.error('Delete row failed:', error);
    }
  };

  const handleDeleteTable = async (tableName: string) => {
    // Dismiss the modal first
    setDeleteConfirmation({ show: false, tableName: null });
    
    try {
      const target = await sourceAccess().admit(chatId);
      sourceAccess().assert(target);
      if (deleteConfirmation.rowId !== undefined) {
        // Delete individual row
        await handleDeleteRow(tableName, deleteConfirmation.rowId);
      } else if (deleteConfirmation.isSelectedRows) {
        // Delete selected rows one by one
        const selectedRowIndices = selectedRows[tableName];
        const table = tables.find(t => t.name === tableName);
        if (!table || !selectedRowIndices) {
          throw new Error('Table or selected rows not found');
        }

        const loadingToast = toast.loading(`正在删除 ${selectedRowIndices.size} 行…`, {
          style: {
            background: '#000',
            color: '#fff',
            border: '4px solid #000',
            borderRadius: '12px',
            boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
            padding: '12px 24px',
            fontSize: '16px',
            fontWeight: '500',
          },
        });

        let successCount = 0;
        let errorCount = 0;

        // Get the row IDs to delete
        const rowsToDelete = Array.from(selectedRowIndices).map(index => {
          const row = table.previewData[index];
          const id = row?._id;
          return typeof id === 'number' || typeof id === 'string' ? id || index : index;
        });

        // Delete each row
        for (const rowId of rowsToDelete) {
          try {
            sourceAccess().assert(target);
            const ok = await uploadService.deleteTableRow(chatId, tableName, rowId);
            sourceAccess().assert(target);
            if (ok) {
              successCount++;
            } else {
              errorCount++;
            }
          } catch {
            errorCount++;
          }
        }

        toast.dismiss(loadingToast);
        
        if (errorCount === 0) {
          toast.success(`已成功删除 ${successCount} 行`, {
            duration: 2000,
            style: {
              background: '#000',
              color: '#fff',
              border: '4px solid #000',
              borderRadius: '12px',
              boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
              padding: '12px 24px',
              fontSize: '16px',
              fontWeight: '500',
            },
          });
        } else {
          toast.error(`已删除 ${successCount} 行，${errorCount} 行失败`, {
            duration: 2000,
            style: {
              background: '#ff4444',
              color: '#fff',
              border: '4px solid #cc0000',
              borderRadius: '12px',
              boxShadow: '4px 4px 0px 0px rgba(0,0,0,1)',
              padding: '12px 24px',
              fontSize: '16px',
              fontWeight: 'bold',
            },
          });
        }

        // Clear selected rows after deletion
        setSelectedRows(prev => ({
          ...prev,
          [tableName]: new Set()
        }));

        // Reload the table data and preview
        sourceAccess().assert(target);
        await loadTableData();
        sourceAccess().assert(target);
        const currentPagination = pagination[tableName] || { page: 1, pageSize: 15, totalRows: 0 };
        await loadTablePreviewData(tableName, currentPagination.page, currentPagination.pageSize);
      } else {
        // Delete entire table
        sourceAccess().assert(target);
        const ok = await uploadService.deleteTable(chatId, tableName);
        sourceAccess().assertCurrent(target);
        if (ok) {
          setTables(prev => prev.filter(t => t.name !== tableName));
          onDeleteTable?.(tableName);
        }
      }
    } catch (error) {
      console.error('Delete failed:', error);
    }
  };

  const formatBytes = (bytes: number): string => {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
  };

  const handlePageChange = async (tableName: string, newPage: number) => {
    if (!access.coordinator?.view(chatId).allowed) { toast.error(access.reason); return; }
    const currentPagination = pagination[tableName] || { page: 1, pageSize: 15, totalRows: 0 };
    setPagination(prev => ({
      ...prev,
      [tableName]: {
        ...prev[tableName],
        page: newPage
      }
    }));
    // Load new page data
    await loadTablePreviewData(tableName, newPage, currentPagination.pageSize);
  };

  const handlePageSizeChange = async (tableName: string, newSize: number) => {
    if (!access.coordinator?.view(chatId).allowed) { toast.error(access.reason); return; }
    setPagination(prev => ({
      ...prev,
      [tableName]: {
        ...prev[tableName],
        pageSize: newSize,
        page: 1 // Reset to first page
      }
    }));
    // Reload data with new page size
    await loadTablePreviewData(tableName, 1, newSize);
  };

  if (loading || isLoadingData) {
    return (
      <div className="flex flex-col items-center justify-center h-64 gap-4">
        <Loader2 className="w-8 h-8 animate-spin text-gray-500" />
        <div className="text-center text-gray-600 max-w-md">
          <p className="font-medium">{zhCN.dataStructure.loading}</p>
          <p className="text-sm mt-2">
            正在获取表结构、列信息与数据统计。
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {!access.allowed && <p role="status">{access.reason}</p>}
      <div className="flex justify-between items-center mb-4">
        <h3 className="text-lg font-semibold">{zhCN.schema.tables}</h3>
        {/* Commented for now, will be added back later */}
        {/* <Tooltip content="刷新表数据">
          <button
            onClick={onRefreshData}
            className="neo-button-secondary flex items-center gap-2 px-3 py-1.5 text-sm"
          >
            <RefreshCw className="w-4 h-4" />
            刷新
          </button>
        </Tooltip> */}
      </div>

      {/* Error Display */}
      {error && (
        <div className="p-4 bg-red-50 rounded-lg border-2 border-red-200 mb-4">
          <div className="flex items-center gap-3">
            <AlertCircle className="w-5 h-5 text-red-600 flex-shrink-0" />
            <div>
              <h4 className="font-medium text-red-800">{zhCN.dataStructure.failed}</h4>
              <p className="text-sm text-red-700 mt-1">{error}</p>
              <button
                onClick={() => {
                  setError(null);
                  loadTableData();
                }}
                className="mt-2 text-sm text-red-600 hover:text-red-800 underline"
              >
                {zhCN.dataStructure.tryAgain}
              </button>
            </div>
          </div>
        </div>
      )}

      {tables.length === 0 && !error ? (
        <div className="text-center py-8 text-gray-500">
          {zhCN.dataStructure.none}
        </div>
      ) : tables.length > 0 ? (
        <div className="space-y-4">
          {tables.map((table) => (
            <div key={table.name} className="border-2 border-gray-200 rounded-lg overflow-hidden">
              {/* Table Header */}
              <div className="bg-gray-50 p-4 flex items-center justify-between">
                <div className="flex items-center gap-3">
                  <button
                    onClick={() => toggleTableExpansion(table.name)}
                    disabled={!expandedTables[table.name] && !table.previewData.length && !access.allowed}
                    className="p-1 hover:bg-gray-200 rounded transition-colors"
                    title={expandedTables[table.name] ? zhCN.dataStructure.collapseTable : zhCN.dataStructure.expandTable}
                  >
                    {expandedTables[table.name] ? (
                      <ChevronDown className="w-5 h-5" />
                    ) : (
                      <ChevronRight className="w-5 h-5" />
                    )}
                  </button>
                  <div>
                    <h4 className="font-semibold text-lg">{table.name}</h4>
                    <div className="text-sm text-gray-600">
                      数据：{table.rowCount.toLocaleString()} 行 · 大小：{formatBytes(table.sizeBytes)}
                    </div>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    onClick={(e) => handleDownloadClick(e, table.name)}
                    className="p-1.5 py-0.5 hover:bg-gray-200 rounded transition-colors"
                    title={zhCN.dataStructure.downloadTable}
                  >
                    <Download className="w-4 w-4" />
                  </button>
                  <button
                    onClick={() => setDeleteConfirmation({ show: true, tableName: table.name })}
                    disabled={!access.allowed}
                    className="p-1.5 hover:bg-red-100 text-red-600 rounded transition-colors"
                    title={zhCN.dataStructure.deleteTable}
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>
              </div>

              {/* Expanded Content */}
              {expandedTables[table.name] && (
                <div className="border-t-2 border-gray-200">
                  {/* Data Preview */}
                  <div className="p-4">
                    <div className="flex justify-start items-center -mb-3">
                      {/* <h5 className="font-medium text-sm text-gray-700">预览</h5> */}
                      <div className={`flex items-center gap-2 ${selectedRows[table.name]?.size > 0 ? 'mb-2' : ''}`}>
                        {selectedRows[table.name]?.size > 0 && (
                          <>
                            <span className="text-sm text-gray-500 mr-2">
                              {selectedRows[table.name].size} {zhCN.schema.selected}
                            </span>
                            <button
                              onClick={(e) => {
                                const rect = e.currentTarget.getBoundingClientRect();
                                setDownloadMenu({ show: true, tableName: table.name, x: rect.left, y: rect.bottom + 4 });
                              }}
                              className="p-1.5 hover:bg-gray-200 rounded transition-colors"
                              title={zhCN.dataStructure.downloadSelected}
                            >
                              <Download className="w-4 h-4" />
                            </button>
                            <button
                              onClick={() => setDeleteConfirmation({ show: true, tableName: table.name, isSelectedRows: true })}
                              disabled={!access.allowed}
                              className="p-1.5 hover:bg-red-100 text-red-600 rounded transition-colors"
                              title={zhCN.dataStructure.deleteSelected}
                            >
                              <Trash2 className="w-4 h-4" />
                            </button>
                          </>
                        )}
                      </div>
                    </div>

                    <div className="overflow-x-auto relative">
                      {loadingPreview[table.name] && (
                        <div className="absolute inset-0 bg-white bg-opacity-75 flex items-center justify-center z-10">
                          <Loader2 className="w-6 h-6 animate-spin text-gray-500" />
                        </div>
                      )}
                      <table className="w-full border-collapse">
                        <thead>
                          <tr className="border-b-2 border-gray-300">
                            <th className="p-2 text-left">
                              <input
                                type="checkbox"
                                checked={selectedRows[table.name]?.size === table.previewData.length}
                                onChange={() => handleSelectAllRows(table.name, table.previewData)}
                                className="rounded border-gray-300 mt-2"
                              />
                            </th>
                            {table.columns.map((col) => (
                              <th key={col.name} className="p-4 text-left font-semibold text-sm">
                                {col.name}
                              </th>
                            ))}
                            <th className="p-2 text-sm text-left">{zhCN.dataStructure.actions}</th>
                          </tr>
                        </thead>
                        <tbody>
                          {table.previewData.length > 0 ? table.previewData.map((row, rowIndex) => (
                            <tr key={rowIndex} className="border-b hover:bg-gray-200 group">
                              <td className="p-2">
                                <input
                                  type="checkbox"
                                  checked={selectedRows[table.name]?.has(rowIndex) || false}
                                  onChange={() => handleRowSelection(table.name, rowIndex)}
                                  className="rounded border-gray-300 mt-2"
                                />
                              </td>
                              {table.columns.map((col) => (
                                <td key={col.name} className="p-4 text-sm">
                                  <span className="truncate max-w-xs block" title={String(row[col.name] ?? '-')}>
                                    {String(row[col.name] ?? '-')}
                                  </span>
                                </td>
                              ))}
                              <td className="p-2">
                                <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100">
                                  <button
                                    onClick={() => handleCopyRow(row)}
                                    className="p-1 hover:bg-gray-200 rounded"
                                    title={zhCN.dataStructure.copyRow}
                                  >
                                    <Copy className="w-3.5 h-3.5" />
                                  </button>
                                  <button
                                    disabled={!access.allowed}
                                    onClick={() => {
                                      const id = row._id;
                                      setDeleteConfirmation({ show: true, tableName: table.name, rowId: (typeof id === 'string' || typeof id === 'number') && id ? id : rowIndex });
                                    }}
                                    className="p-1 hover:bg-red-100 text-red-600 rounded"
                                    title={zhCN.dataStructure.deleteRow}
                                  >
                                    <Trash2 className="w-4 h-4" />
                                  </button>
                                </div>
                              </td>
                            </tr>
                          )) : (
                            <tr>
                              <td colSpan={table.columns.length + 2} className="text-center py-8 text-gray-500">
                                暂无数据
                              </td>
                            </tr>
                          )}
                        </tbody>
                      </table>
                    </div>

                    {/* Pagination Controls */}
                    <div className="flex items-center justify-between mt-4">
                      <div className="flex items-center gap-2">
                        <span className="text-sm text-gray-600">{zhCN.dataStructure.rowsPerPage}</span>
                        <select
                          value={pagination[table.name]?.pageSize || 15}
                          onChange={(e) => handlePageSizeChange(table.name, Number(e.target.value))}
                          disabled={!access.allowed}
                          className="neo-input px-2 py-1 text-sm"
                        >
                          <option value={10}>10</option>
                          <option value={15}>15</option>
                          <option value={25}>25</option>
                          <option value={50}>50</option>
                          <option value={100}>100</option>
                        </select>
                      </div>

                      <div className="flex items-center gap-2">
                        <span className="text-sm text-gray-600">
                          {table.previewData.length > 0 ? (
                            <>{zhCN.dataStructure.showing} {((pagination[table.name]?.page || 1) - 1) * (pagination[table.name]?.pageSize || 15) + 1}-
                            {((pagination[table.name]?.page || 1) - 1) * (pagination[table.name]?.pageSize || 15) + table.previewData.length} {zhCN.dates.of} {pagination[table.name]?.totalRows || table.rowCount}</>
                          ) : (
                            '暂无数据'
                          )}
                        </span>
                        <div className="flex gap-1">
                          <button
                            onClick={() => handlePageChange(table.name, 1)}
                            disabled={!access.allowed || (pagination[table.name]?.page || 1) === 1}
                            className="p-1 hover:bg-gray-200 rounded disabled:opacity-50 disabled:cursor-not-allowed"
                            title={zhCN.dataStructure.firstPage}
                          >
                            <ChevronsLeft className="w-4 h-4" />
                          </button>
                          <button
                            onClick={() => handlePageChange(table.name, (pagination[table.name]?.page || 1) - 1)}
                            disabled={!access.allowed || (pagination[table.name]?.page || 1) === 1}
                            className="p-1 hover:bg-gray-200 rounded disabled:opacity-50 disabled:cursor-not-allowed"
                            title={zhCN.common.previous}
                          >
                            <ChevronLeft className="w-4 h-4" />
                          </button>
                          <button
                            onClick={() => handlePageChange(table.name, (pagination[table.name]?.page || 1) + 1)}
                            disabled={!access.allowed || (pagination[table.name]?.page || 1) * (pagination[table.name]?.pageSize || 15) >= (pagination[table.name]?.totalRows || table.rowCount)}
                            className="p-1 hover:bg-gray-200 rounded disabled:opacity-50 disabled:cursor-not-allowed"
                            title={zhCN.common.next}
                          >
                            <ChevronRightIcon className="w-4 h-4" />
                          </button>
                          <button
                            onClick={() => handlePageChange(table.name, Math.ceil((pagination[table.name]?.totalRows || table.rowCount) / (pagination[table.name]?.pageSize || 15)))}
                            disabled={!access.allowed || (pagination[table.name]?.page || 1) * (pagination[table.name]?.pageSize || 15) >= (pagination[table.name]?.totalRows || table.rowCount)}
                            className="p-1 hover:bg-gray-200 rounded disabled:opacity-50 disabled:cursor-not-allowed"
                            title={zhCN.dataStructure.lastPage}
                          >
                            <ChevronsRight className="w-4 h-4" />
                          </button>
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              )}
            </div>
          ))}
        </div>
      ) : null}

      {/* Download Menu Popup */}
      {downloadMenu.show && (
        <>
          <div
            className="fixed inset-0 z-10"
            onClick={() => setDownloadMenu({ show: false, tableName: null, x: 0, y: 0 })}
          />
          <div
            className="fixed z-20 bg-white border-2 border-gray-200 rounded-lg shadow-lg py-1"
            style={{ left: downloadMenu.x, top: downloadMenu.y }}
          >
            <button
              onClick={() => handleDownload('csv')}
              disabled={!access.allowed && !(downloadMenu.tableName && selectedRows[downloadMenu.tableName]?.size)}
              title={access.reason}
              className="w-full px-4 py-2 text-left hover:bg-gray-100 text-sm"
            >
              {zhCN.dataStructure.downloadCsv}
            </button>
            <button
              onClick={() => handleDownload('xlsx')}
              disabled={!access.allowed}
              title={access.reason}
              className="w-full px-4 py-2 text-left hover:bg-gray-100 text-sm"
            >
              {zhCN.dataStructure.downloadXlsx}
            </button>
          </div>
        </>
      )}

      {/* Delete Confirmation Modal */}
      {deleteConfirmation.show && deleteConfirmation.tableName && (
        <ConfirmationModal
          title={
            deleteConfirmation.rowId !== undefined 
              ? zhCN.dataStructure.deleteRowTitle 
              : deleteConfirmation.isSelectedRows 
                ? zhCN.dataStructure.deleteRowsTitle 
                : zhCN.dataStructure.deleteTableTitle
          }
          message={
            deleteConfirmation.rowId !== undefined
              ? `确定要从“${deleteConfirmation.tableName}”删除该行吗？此操作不可撤销。`
              : deleteConfirmation.isSelectedRows
                ? `确定要从“${deleteConfirmation.tableName}”删除 ${selectedRows[deleteConfirmation.tableName]?.size || 0} 行吗？此操作不可撤销。`
                : `确定要删除表“${deleteConfirmation.tableName}”吗？此操作不可撤销，表中全部数据将被永久删除。`
          }
          onConfirm={() => handleDeleteTable(deleteConfirmation.tableName!)}
          onCancel={() => setDeleteConfirmation({ show: false, tableName: null, isSelectedRows: false, rowId: undefined })}
        />
      )}
    </div>
  );
}