import { useState } from 'react';
import type { ReactNode } from 'react';
import type { User } from '../types/auth';
import { UserContext } from './UserContext';

export function UserProvider({ children }: { children: ReactNode }) {
    const [user, setUser] = useState<Pick<User, 'id' | 'username' | 'created_at' | 'is_admin'> | null>(null);
    return (
        <UserContext.Provider value={{ user, setUser }}>
            {children}
        </UserContext.Provider>
    );
}
