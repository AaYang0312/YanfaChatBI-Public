import React, { useState, useRef, useEffect, useCallback } from 'react';
import { Upload, X, FileText, Table, AlertCircle, Info, Loader2, Database } from 'lucide-react';
import { Connection, FileUpload } from '../../../types/chat';
import type { TableInfo } from '../../../types/chat';
import chatService from '../../../services/chatService';
import { zhCN } from '../../../i18n/zh-CN';
import { useSourceAccess } from '../../../hooks/useSourceAccess';

interface FileUploadTabProps {
  formData: Connection;
  handleChange: (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => void;
  onFilesChange: (files: FileUpload[]) => void;
  isEditMode?: boolean;
  chatId?: string;
  preloadedTables?: TableInfo[];
}

const FileUploadTab: React.FC<FileUploadTabProps> = ({
  formData,
  handleChange,
  onFilesChange,
  isEditMode = false,
  chatId,
  preloadedTables,
}) => {
  const access = useSourceAccess(chatId);
  const [uploadedFiles, setUploadedFiles] = useState<FileUpload[]>(formData.file_uploads || []);
  const [isDragging, setIsDragging] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [isProcessing, setIsProcessing] = useState(false);
  const [existingTables, setExistingTables] = useState<TableInfo[]>(preloadedTables || []);
  const [loadingTables, setLoadingTables] = useState(false);
  const [conflictingTables, setConflictingTables] = useState<string[]>([]);
  const fileInputRef = useRef<HTMLInputElement>(null);

  
  // Clear uploaded files when entering edit mode
  useEffect(() => {
    if (isEditMode) {
      setUploadedFiles([]);
      setUploadError(null);
    }
  }, [isEditMode]);

  const loadExistingTables = useCallback(async () => {
    if (!chatId) return;
    if (!access.coordinator?.view(chatId).allowed) return;
    
    setLoadingTables(true);
    try {
      console.log('FileUploadTab: Loading tables for chatId:', chatId);
      const data = await chatService.getTables(chatId);
      setExistingTables(data.tables || []);
    } catch (error) {
      console.error('Failed to load existing tables:', error);
    } finally {
      setLoadingTables(false);
    }
  }, [chatId]);
  // Load existing tables when in edit mode
  useEffect(() => {
    if (isEditMode && chatId) {
      loadExistingTables();
    }
  }, [isEditMode, chatId, loadExistingTables]);
  
  // Preloaded schema is a local snapshot; do not restart a denied table request.
  useEffect(() => {
    if (preloadedTables?.length) setExistingTables(preloadedTables);
  }, [preloadedTables]);

  const handleDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(true);
  };

  const handleDragLeave = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
    
    const files = Array.from(e.dataTransfer.files);
    handleFiles(files);
  };

  const handleFileInput = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      const files = Array.from(e.target.files);
      handleFiles(files);
    }
  };

  const handleFiles = async (files: File[]) => {
    setUploadError(null);
    setIsProcessing(true);

    const validExtensions = ['.csv', '.xlsx', '.xls'];
    const maxFileSize = 100 * 1024 * 1024; // 100MB

    const newFiles: FileUpload[] = [];

    for (const file of files) {
      const extension = file.name.substring(file.name.lastIndexOf('.')).toLowerCase();
      
      if (!validExtensions.includes(extension)) {
        setUploadError(`无效文件类型：${file.name}。仅支持 CSV 和 Excel 文件。`);
        continue;
      }

      if (file.size > maxFileSize) {
        setUploadError(`文件过大：${file.name}。单文件最大 100MB。`);
        continue;
      }

      // Check for duplicate files
      if (uploadedFiles.some(f => f.filename === file.name && f.size === file.size)) {
        setUploadError(`重复文件：${file.name} 已上传。`);
        continue;
      }

      // Generate table name
      const tableName = sanitizeTableName(file.name);

      // Check if we already have a file with the same table name in current batch or already uploaded
      const existingFileWithSameTable = [...uploadedFiles, ...newFiles].find(f => f.tableName === tableName);
      if (existingFileWithSameTable) {
        setUploadError(`多个文件映射到同一表名“${tableName}”。请重命名其中一个文件：${existingFileWithSameTable.filename} 或 ${file.name}`);
        continue;
      }

      const fileUpload: FileUpload = {
        id: `file-${Date.now()}-${Math.random().toString(36).substring(2, 11)}`,
        filename: file.name,
        size: file.size,
        type: extension === '.csv' ? 'csv' : 'excel',
        uploadedAt: new Date(),
        tableName: tableName,
        file: file,
      };

      // In edit mode, track which tables will be replaced
      if (isEditMode && existingTables.some(t => t.name === tableName)) {
        // Always replace existing tables with the same name
        fileUpload.mergeStrategy = 'replace';
        if (!conflictingTables.includes(tableName)) {
          setConflictingTables(prev => [...prev, tableName]);
        }
      }
      
      newFiles.push(fileUpload);
    }

    // Add all files
    if (newFiles.length > 0) {
      const updatedFiles = [...uploadedFiles, ...newFiles];
      setUploadedFiles(updatedFiles);
      onFilesChange(updatedFiles);
      
      // Show info message if tables will be replaced
      if (conflictingTables.length > 0 && isEditMode) {
        setUploadError(`注意：以下表将被新数据替换：${conflictingTables.join(', ')}`);
        setConflictingTables([]);
      }
    }

    setIsProcessing(false);
  };

  const sanitizeTableName = (filename: string): string => {
    // Remove file extension and special characters, replace with underscores
    return filename
      .replace(/\.[^/.]+$/, '') // Remove extension
      .replace(/[^a-zA-Z0-9_]/g, '_') // Replace special chars with underscore
      .replace(/_+/g, '_') // Replace multiple underscores with single
      .replace(/^_|_$/g, '') // Remove leading/trailing underscores
      .toLowerCase();
  };

  const removeFile = (fileId: string) => {
    const updatedFiles = uploadedFiles.filter(f => f.id !== fileId);
    setUploadedFiles(updatedFiles);
    onFilesChange(updatedFiles);
    setUploadError(null); // Clear any info messages when removing files
  };

  const updateTableName = (fileId: string, newTableName: string) => {
    const updatedFiles = uploadedFiles.map(f => 
      f.id === fileId ? { ...f, tableName: newTableName } : f
    );
    setUploadedFiles(updatedFiles);
    onFilesChange(updatedFiles);
  };

  const formatFileSize = (bytes: number): string => {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
  };


  return (
    <>
      {chatId && !access.allowed && <p role="status">{access.reason}</p>}
      {/* Existing Tables Section (Edit Mode Only) */}
      {isEditMode && (
        <div className="mb-6">
          <h3 className="font-bold text-lg mb-3">{zhCN.files.existingTables}</h3>
          {loadingTables ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="w-6 h-6 animate-spin text-gray-500" />
            </div>
          ) : existingTables.length > 0 ? (
            <div className="space-y-2 mb-4">
              {existingTables.map((table) => (
                <div key={table.name} className="p-3 border-2 border-gray-200 rounded-lg bg-gray-50">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <Database className="w-4 h-4 text-gray-600" />
                      <span className="font-medium">{table.name}</span>
                      <span className="text-sm text-gray-500">
                        ({table.row_count || 0} {zhCN.files.rowsCols.split(' · ')[0]} · {table.columns?.length || 0} {zhCN.files.rowsCols.split(' · ')[1]})
                      </span>
                    </div>
                    {/* <span className="text-xs text-gray-500">
                      来源：{table.source_file || '未知来源'}
                    </span> */}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-gray-500 mb-4">{zhCN.files.noExisting}</p>
          )}
          
          <div className="p-4 bg-blue-50 border-2 border-blue-200 rounded-lg mb-6">
            <div className="flex items-start gap-2">
              <Info className="w-5 h-5 text-blue-600 mt-0.5 flex-shrink-0" />
              <div className="text-sm text-blue-800">
                <p className="font-medium mb-1">上传更多数据</p>
                <p>上传文件时：</p>
                <ul className="mt-2 space-y-1 ml-4">
                  <li>• <strong>已有表：</strong>将全部替换为新文件数据</li>
                  <li>• <strong>新文件：</strong>将添加为新表</li>
                  <li>• <strong>重要：</strong>请始终上传包含全部需保留数据的文件</li>
                </ul>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* File Naming Guidelines */}
      <div className="mb-6 p-4 bg-green-50 border-2 border-green-200 rounded-lg">
        <div className="flex items-start gap-2">
          <Info className="w-5 h-5 text-green-600 mt-0.5 flex-shrink-0" />
          <div>
            <h4 className="font-bold text-green-900 mb-2">文件命名建议</h4>
            <ul className="text-sm text-green-800 space-y-2">
              <li><strong>1. 相关数据：</strong>使用前缀归组相关文件（例如 sales_2024_q1.csv、sales_2024_q2.csv，sales 为表名）</li>
              <li><strong>2. 不同数据集：</strong>不相关数据请使用不同名称（例如 customers.csv、inventory.xlsx，customers 与 inventory 为表名）</li>
              <li><strong>3. 多工作表 Excel：</strong>每个工作表将导入为单独表，并以工作表名作为后缀（例如 customer.csv、payments.csv，customer 与 payments 为表名）</li>
              <li><strong>4. 数据排序：</strong>系统会根据列名与日期自动排序，请在数据中包含日期字段以便更好排序（例如 created_at、updated_at）。</li>
            </ul>
          </div>
        </div>
      </div>

      {/* Hidden Connection Fields for Spreadsheet */}
      <input type="hidden" name="host" value="internal-spreadsheet" onChange={handleChange} />
      <input type="hidden" name="port" value="0" onChange={handleChange} />
      <input type="hidden" name="database" value={formData.database || "spreadsheet_db"} onChange={handleChange} />
      <input type="hidden" name="username" value="spreadsheet_user" onChange={handleChange} />
      <input type="hidden" name="password" value="internal" onChange={handleChange} />

      {/* File Upload Area */}
      <div className="mb-6">
        <label className="block font-bold mb-2 text-lg">
          {isEditMode ? zhCN.files.uploadMore : zhCN.files.uploadCsv}
        </label>
        <p className="text-gray-600 text-sm mb-4">
          上传 CSV 或 Excel 文件。文件将加密并安全存储。
        </p>

        <div
          className={`border-2 border-dashed rounded-lg p-8 text-center transition-colors ${
            isDragging 
              ? 'border-blue-500 bg-blue-50' 
              : 'border-gray-300 hover:border-gray-400'
          }`}
          onDragOver={handleDragOver}
          onDragLeave={handleDragLeave}
          onDrop={handleDrop}
        >
          <Upload className="w-12 h-12 mx-auto mb-4 text-gray-400" />
          <p className="text-gray-600 mb-4">
            {zhCN.files.dropHint}
          </p>
          <button
            type="button"
            onClick={() => fileInputRef.current?.click()}
            className="neo-button-secondary"
            disabled={isProcessing}
          >
            {isProcessing ? (
              <div className="flex items-center gap-2">
                <Loader2 className="w-4 h-4 animate-spin" />
                <span>{zhCN.files.processing}</span>
              </div>
            ) : (
              zhCN.files.choose
            )}
          </button>
          <input
            ref={fileInputRef}
            type="file"
            multiple
            accept=".csv,.xlsx,.xls"
            onChange={handleFileInput}
            className="hidden"
          />
          <p className="text-xs text-gray-500 mt-4">
            {zhCN.files.supported}
          </p>
        </div>

        {uploadError && (
          <div className="mt-2 p-3 bg-red-50 border-2 border-red-200 rounded-lg">
            <div className="flex items-center gap-2 text-red-600">
              <AlertCircle className="w-4 h-4" />
              <p className="text-sm">{uploadError}</p>
            </div>
          </div>
        )}
      </div>

      {/* Uploaded Files List */}
      {uploadedFiles.length > 0 && (
        <div className="mb-6">
          <h4 className="font-bold mb-3 text-lg">{zhCN.files.uploaded} ({uploadedFiles.length})</h4>
          <div className="space-y-3">
            {uploadedFiles.map((file) => (
              <div key={file.id} className="p-4 border-2 border-gray-200 rounded-lg bg-gray-50">
                <div className="flex items-start justify-between mb-2">
                  <div className="flex items-center gap-2">
                    {file.type === 'csv' ? (
                      <FileText className="w-5 h-5 text-green-600" />
                    ) : (
                      <Table className="w-5 h-5 text-blue-600" />
                    )}
                    <div>
                      <p className="font-medium">{file.filename}</p>
                      <p className="text-sm text-gray-500">
                        {formatFileSize(file.size)} • {file.type.toUpperCase()}
                      </p>
                    </div>
                  </div>
                  <button
                    type="button"
                    onClick={() => removeFile(file.id)}
                    className="p-1 hover:bg-gray-200 rounded transition-colors"
                  >
                    <X className="w-4 h-4" />
                  </button>
                </div>
                
                <div className="mt-2">
                  <label className="block text-sm font-medium text-gray-700 mb-1">
                    {zhCN.files.tableName}
                  </label>
                  <input
                    type="text"
                    value={file.tableName || ''}
                    onChange={(e) => updateTableName(file.id, e.target.value)}
                    className="neo-input w-full text-sm"
                    placeholder={zhCN.files.tableNamePlaceholder}
                  />
                  <p className="text-xs text-gray-500 mt-3">
                    {zhCN.files.tableNameHint}
                  </p>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Data Privacy Notice */}
      <div className="p-4 bg-gray-100 border border-gray-300 rounded-lg">
        <div className="flex items-start gap-2">
          <AlertCircle className="w-5 h-5 text-gray-600 mt-0.5 flex-shrink-0" />
          <div className="text-sm text-gray-700">
            <p className="font-semibold text-base mb-1 mt-0.5">{zhCN.files.security}</p>
            <p>
              CSV 与 Excel 数据在存储前将使用 AES-GCM 加密，并隔离保存。你可以随时导出或删除数据。
            </p>
          </div>
        </div>
      </div>

    </>
  );
};

export default FileUploadTab;