import { useCallback, useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { StreamContext } from './StreamContext';

const generateId = () => {
    const timestamp = Date.now().toString(36);
    const randomStr = Math.random().toString(36).substring(2, 8);
    return `${timestamp}-${randomStr}`;
};

export function StreamProvider({ children }: { children: ReactNode }) {
    const [streamId, setStreamId] = useState<string | null>(null);
    const generateStreamId = useCallback(() => {
        const newStreamId = generateId();
        setStreamId(newStreamId);
        return newStreamId;
    }, []);

    useEffect(() => {
        if (!streamId) generateStreamId();
    }, [generateStreamId, streamId]);

    return (
        <StreamContext.Provider value={{ streamId, setStreamId, generateStreamId }}>
            {children}
        </StreamContext.Provider>
    );
}
