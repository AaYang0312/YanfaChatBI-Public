import { ArrowLeft, ArrowRight } from 'lucide-react';
import { zhCN } from '../../i18n/zh-CN';

interface QueryPaginationProps {
    currentPage: number;
    totalRecords: number | null;
    pageSize: number;
    hasMore: boolean;
    useCursorPagination: boolean;
    isLoading: boolean;
    onPageChange: (page: number) => void;
    canViewPage?: (page: number) => boolean;
    blockReason?: string;
}

/**
 * Prev / Next pagination bar for query result views.
 * Stateless — all state lives in the parent (MessageTile / QueryResultView).
 */
export default function QueryPagination({
    currentPage,
    totalRecords,
    pageSize,
    hasMore,
    useCursorPagination,
    isLoading,
    onPageChange,
    canViewPage,
    blockReason,
}: Readonly<QueryPaginationProps>) {
    const totalPages = totalRecords && totalRecords > 0
        ? Math.max(1, Math.ceil(totalRecords / pageSize))
        : null;

    const isNextDisabled = isLoading || (useCursorPagination
        ? !hasMore
        : totalPages !== null && currentPage >= totalPages);

    return (
        <div className="flex justify-center mt-6">
            <div className="flex items-center gap-4 bg-gray-800 rounded-lg p-1.5">
                <button
                    onClick={() => onPageChange(currentPage - 1)}
                    disabled={isLoading || currentPage === 1 || canViewPage?.(currentPage - 1) === false}
                    className="
                        flex items-center justify-center w-8 h-8 rounded transition-colors
                        disabled:opacity-40 disabled:cursor-not-allowed
                        enabled:hover:bg-gray-700 enabled:active:bg-gray-600
                    "
                    title={canViewPage?.(currentPage - 1) === false ? blockReason : zhCN.query.previousPage}
                >
                    <ArrowLeft className="w-4 h-4" />
                </button>

                <div className="flex items-center gap-2 text-sm font-mono">
                    <span className="text-gray-400">{zhCN.dates.page}</span>
                    <span className="bg-gray-700 rounded px-2 py-1 min-w-[2rem] text-center">
                        {currentPage}
                    </span>
                    {totalPages !== null && (
                        <>
                            <span className="text-gray-400">{zhCN.dates.pageOf}</span>
                            <span className="bg-gray-700 rounded px-2 py-1 min-w-[2rem] text-center">
                                {totalPages}
                            </span>
                        </>
                    )}
                </div>

                <button
                    onClick={() => onPageChange(currentPage + 1)}
                    disabled={isNextDisabled || canViewPage?.(currentPage + 1) === false}
                    className="
                        flex items-center justify-center w-8 h-8 rounded transition-colors
                        disabled:opacity-40 disabled:cursor-not-allowed
                        enabled:hover:bg-gray-700 enabled:active:bg-gray-600
                    "
                    title={canViewPage?.(currentPage + 1) === false ? blockReason : zhCN.query.nextPage}
                >
                    <ArrowRight className="w-4 h-4" />
                </button>
            </div>
        </div>
    );
}
