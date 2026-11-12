import { createContext, useContext } from 'react';

interface StreamContextType {
    streamId: string | null; // 32 bytes strictly
    setStreamId: (id: string | null) => void;
    generateStreamId: () => string; // Add function to generate stream ID
}

export const StreamContext = createContext<StreamContextType | undefined>(undefined);


export function useStream() {
    const context = useContext(StreamContext);
    if (context === undefined) {
        throw new Error('useStream must be used within a StreamProvider');
    }
    return context;
} 