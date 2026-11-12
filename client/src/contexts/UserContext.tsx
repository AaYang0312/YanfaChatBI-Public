import { createContext, useContext } from 'react';
import type { User } from '../types/auth';

interface UserContextType {
    user: Pick<User, 'id' | 'username' | 'created_at' | 'is_admin'> | null;
    setUser: (user: Pick<User, 'id' | 'username' | 'created_at' | 'is_admin'> | null) => void;
}

export const UserContext = createContext<UserContextType | undefined>(undefined);


export function useUser() {
    const context = useContext(UserContext);
    if (context === undefined) {
        throw new Error('useUser must be used within a UserProvider');
    }
    return context;
}