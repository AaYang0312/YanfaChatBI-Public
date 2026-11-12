// Deterministic progress for the data-sync dialog: completed shops over the
// known expected count. An unknown total keeps the bar indeterminate.
export function syncProgressValue(processed: number, total: number | null, succeeded: boolean): number | undefined {
    if (succeeded) return 100
    if (!total || total <= 0) return undefined
    const completed = Number.isFinite(processed) ? Math.max(0, Math.min(processed, total)) : 0
    return Math.round((completed / total) * 100)
}
