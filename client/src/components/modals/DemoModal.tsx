import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { X } from 'lucide-react';
import { zhCN } from '../../i18n/zh-CN';

interface DemoModalProps {
  isOpen: boolean;
  onClose: () => void;
}

export function DemoModal({ isOpen, onClose }: DemoModalProps) {
  return isOpen ? <OpenDemoModal onClose={onClose} /> : null;
}

function OpenDemoModal({ onClose }: Pick<DemoModalProps, 'onClose'>) {
  const [attempt, setAttempt] = useState(0);
  const [loadState, setLoadState] = useState<'loading' | 'loaded' | 'error'>('loading');
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();

  useLayoutEffect(() => {
    const iframe = iframeRef.current;
    if (!iframe) return;

    let pending = true;
    const clearPending = () => {
      window.clearTimeout(timeout);
      iframe.removeEventListener('load', handleLoad);
      iframe.removeEventListener('error', handleError);
    };
    const finish = (state: 'loaded' | 'error') => {
      if (!pending) return;
      pending = false;
      clearPending();
      setLoadState(state);
    };
    // Cross-origin load confirms navigation only; keep a manual retry available.
    const handleLoad = () => finish('loaded');
    const handleError = () => finish('error');
    const timeout = window.setTimeout(handleError, 15_000);
    iframe.addEventListener('load', handleLoad);
    iframe.addEventListener('error', handleError);

    return () => {
      pending = false;
      clearPending();
    };
  }, [attempt]);

  useEffect(() => {
    const opener = document.activeElement;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    closeButtonRef.current?.focus();

    return () => {
      document.body.style.overflow = previousOverflow;
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    };
  }, []);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        onClose();
      } else if (event.key === 'Tab') {
        const controls = dialogRef.current?.querySelectorAll<HTMLElement>(
          'button:not(:disabled), iframe[tabindex="0"]',
        );
        if (!controls?.length) return;
        const first = controls[0];
        const last = controls[controls.length - 1];
        if (!dialogRef.current?.contains(document.activeElement)) {
          event.preventDefault();
          first.focus();
        } else if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-2 sm:p-4 bg-black/50 backdrop-blur-sm">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="flex max-h-[calc(100dvh-1rem)] w-full max-w-7xl flex-col overflow-hidden rounded-2xl bg-white dark:bg-surface-dark shadow-2xl"
      >
        <div className="flex shrink-0 items-center justify-between gap-3 border-b border-gray-200 p-3 sm:p-4">
          <h2 id={titleId} className="min-w-0 font-bold text-text-primary-light dark:text-text-primary-dark">
            {zhCN.demo.title}
          </h2>
          <button
            ref={closeButtonRef}
            type="button"
            onClick={onClose}
            aria-label={zhCN.common.close}
            className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full hover:bg-gray-100 dark:hover:bg-gray-700"
          >
            <X className="h-6 w-6 text-text-primary-light dark:text-text-primary-dark" />
          </button>
        </div>
        <div className="relative min-h-0 flex-1 overflow-auto" style={{ height: 'min(70dvh, 680px)' }}>
          {loadState === 'loading' && (
            <div role="status" className="absolute inset-0 flex items-center justify-center p-6 text-center text-text-primary-light dark:text-text-primary-dark">
              {zhCN.demo.loading}
            </div>
          )}
          {loadState === 'error' ? (
            <div className="flex h-full flex-col items-center justify-center gap-4 p-6 text-center text-text-primary-light dark:text-text-primary-dark">
              <p role="alert">{zhCN.demo.loadError}</p>
            </div>
          ) : (
            <iframe
              key={attempt}
              ref={iframeRef}
              src="https://app.supademo.com/embed/cm7u1fvg42cw2hilggnjt2kox?embed_v=2"
              title={zhCN.demo.title}
              allow="clipboard-write"
              tabIndex={loadState === 'loaded' ? 0 : -1}
              aria-hidden={loadState !== 'loaded'}
              className={`h-full w-full border-0 ${loadState === 'loading' ? 'invisible' : ''}`}
            />
          )}
        </div>
        {loadState !== 'loading' && (
          <div className="flex shrink-0 flex-wrap items-center justify-center gap-3 border-t border-gray-200 p-3 text-center text-text-primary-light dark:text-text-primary-dark">
            {loadState === 'loaded' && <p className="text-sm">{zhCN.demo.loadHint}</p>}
            <button
              type="button"
              onClick={() => {
                closeButtonRef.current?.focus();
                setLoadState('loading');
                setAttempt(previous => previous + 1);
              }}
              className="neo-button"
            >
              {zhCN.common.retry}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}