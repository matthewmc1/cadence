import type { ServerEvent } from './types';

export type ConnState = 'connecting' | 'open' | 'closed';

interface Handlers {
  onEvent: (ev: ServerEvent) => void;
  onState: (s: ConnState) => void;
}

/**
 * Connect to the realtime stream with automatic reconnect/backoff. Returns a
 * disposer. The server pushes a `hello` frame on connect (ignored) followed by
 * task/project change events.
 */
export function connectRealtime(url: string, { onEvent, onState }: Handlers): () => void {
  let ws: WebSocket | null = null;
  let disposed = false;
  let backoff = 500;
  let retry: number | undefined;

  const open = () => {
    if (disposed) return;
    onState('connecting');
    ws = new WebSocket(url);

    ws.onopen = () => {
      backoff = 500;
      onState('open');
    };
    ws.onmessage = (e) => {
      try {
        const msg = JSON.parse(e.data as string);
        if (msg && msg.type && msg.type !== 'hello') onEvent(msg as ServerEvent);
      } catch {
        /* ignore malformed frame */
      }
    };
    ws.onerror = () => {
      try {
        ws?.close();
      } catch {
        /* noop */
      }
    };
    ws.onclose = () => {
      onState('closed');
      if (disposed) return;
      retry = window.setTimeout(open, backoff);
      backoff = Math.min(backoff * 2, 8000);
    };
  };

  open();

  return () => {
    disposed = true;
    if (retry) window.clearTimeout(retry);
    try {
      ws?.close();
    } catch {
      /* noop */
    }
  };
}
