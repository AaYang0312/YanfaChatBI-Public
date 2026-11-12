import { describe, expect, it } from 'vitest'
import { syncProgressValue } from './dataSyncProgress'

describe('syncProgressValue', () => {
    it('reports completed shops over the expected total', () => {
        expect(syncProgressValue(0, 7, false)).toBe(0)
        expect(syncProgressValue(3, 7, false)).toBe(43)
        expect(syncProgressValue(7, 7, false)).toBe(100)
    })

    it('clamps out-of-range counts and ignores unknown totals', () => {
        expect(syncProgressValue(9, 7, false)).toBe(100)
        expect(syncProgressValue(-1, 7, false)).toBe(0)
        expect(syncProgressValue(3, null, false)).toBeUndefined()
        expect(syncProgressValue(3, 0, false)).toBeUndefined()
        expect(syncProgressValue(Number.NaN, 7, false)).toBe(0)
    })

    it('completes only when the job succeeded', () => {
        expect(syncProgressValue(7, 7, true)).toBe(100)
        expect(syncProgressValue(0, null, true)).toBe(100)
    })
})
