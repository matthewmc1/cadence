/**
 * Registers the offline-shell service worker (public/sw.js) — production
 * builds only, after load so it never competes with the first paint. The
 * worker caches the hashed build assets and one copy of index.html; it
 * passes every /api/ request straight through, so failing to register is a
 * lost nicety, never a broken app.
 */
export function registerServiceWorker() {
  if (!import.meta.env.PROD || !('serviceWorker' in navigator)) return;
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      /* an http:// origin other than localhost, or a browser without SW: fine */
    });
  });
}
