import { useLayoutEffect, useState } from 'react'
import type { Chat } from '../types/chat'
import { installSourceAccess, SourceAccessCoordinator } from '../contexts/SourceAccessContext'

/** App is the sole owner; consumers only subscribe or use the same service bridge. */
export function useSourceAccessCoordinator(owner: string, selected: Chat | undefined, chats: Chat[]) {
  const [coordinator] = useState(() => new SourceAccessCoordinator())
  useLayoutEffect(() => {
    const uninstall = installSourceAccess(coordinator)
    const stop = coordinator.start()
    return () => { stop(); uninstall() }
  }, [coordinator])
  useLayoutEffect(() => { coordinator.configure(owner, selected, chats) }, [coordinator, owner, selected, chats])
  return coordinator
}

export { useSourceAccess } from '../contexts/SourceAccessContext'
