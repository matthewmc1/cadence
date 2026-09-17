/*
 * Cadence service worker — an offline app shell, nothing more.
 *
 * What it caches: same-origin GET responses for the hashed build assets
 * (/assets/*, which Vite content-hashes so an entry never goes stale), the
 * icons and the manifest, and one copy of the app shell (index.html) so a
 * navigation still opens the app when the network is down.
 *
 * Staying fresh: the hashed assets are cache-first, and on every activate the
 * fresh index says which of them are still live — the rest are dropped, so a
 * deploy doesn't leave last build's bundles behind forever. The icons and the
 * manifest are NOT hashed, so they are stale-while-revalidate: the cached copy
 * answers, a background fetch replaces it for next time.
 *
 * What it never caches: anything under /api/ or /models/, any non-GET, and
 * anything cross-origin. API responses are fetched live every time, so no
 * private data lands in CacheStorage; the shell fallback only fires when the
 * network request itself fails (offline), never in place of a live response.
 */
const SHELL = 'cadence-shell-v2';
const STATIC = 'cadence-static-v2';
const SHELL_KEY = '/';

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(SHELL)
      .then((cache) => cache.add(new Request(SHELL_KEY, { cache: 'reload' })))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== SHELL && k !== STATIC).map((k) => caches.delete(k))))
      .then(() => pruneAssets())
      .then(() => self.clients.claim()),
  );
});

/** Live-only paths: the API, the realtime stream and any hosted model weights. */
function isLive(pathname) {
  return pathname === '/api' || pathname.startsWith('/api/') || pathname.startsWith('/models/');
}

/** Content-hashed build output: a given name never changes, so the cache can answer first. */
function isHashed(pathname) {
  return pathname.startsWith('/assets/');
}

/** Static, but not hashed: a deploy can change these under the same URL. */
function isUnhashedStatic(pathname) {
  return pathname.startsWith('/icons/') || pathname === '/manifest.webmanifest';
}

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (isLive(url.pathname)) return; // pass straight through, uncached

  if (req.mode === 'navigate') {
    event.respondWith(shellNetworkFirst(req));
    return;
  }
  if (isHashed(url.pathname)) {
    event.respondWith(staticCacheFirst(req));
    return;
  }
  if (isUnhashedStatic(url.pathname)) {
    event.respondWith(staleWhileRevalidate(event));
  }
});

/** Navigations go to the network; the cached shell is only the offline fallback. */
async function shellNetworkFirst(req) {
  try {
    const res = await fetch(req);
    if (res.ok && (res.headers.get('content-type') || '').includes('text/html')) {
      const cache = await caches.open(SHELL);
      cache.put(SHELL_KEY, res.clone()).catch(() => {});
    }
    return res;
  } catch {
    const cached = await caches.match(SHELL_KEY, { cacheName: SHELL });
    return cached || Response.error();
  }
}

/** Hashed assets never change under the same name, so the cache answers first. */
async function staticCacheFirst(req) {
  const cache = await caches.open(STATIC);
  const hit = await cache.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  if (res.ok) cache.put(req, res.clone()).catch(() => {});
  return res;
}

/** Unhashed statics answer from cache, then refresh it for the next load. */
async function staleWhileRevalidate(event) {
  const req = event.request;
  const cache = await caches.open(STATIC);
  const hit = await cache.match(req);
  const fresh = fetch(req).then((res) => {
    if (res.ok) cache.put(req, res.clone()).catch(() => {});
    return res;
  });
  if (!hit) return fresh;
  event.waitUntil(fresh.catch(() => {})); // keep the worker alive long enough to store it
  return hit;
}

/**
 * Drop the previous build's bundles. The cache name is a constant, so nothing
 * else would ever evict them: read the fresh index, work out which /assets/
 * belong to this build, and delete every cached asset outside that set. A
 * failed fetch (offline) or an index with no assets prunes nothing.
 *
 * The index only names the entry chunk and its stylesheet, so those two are
 * read as well: the lazy imports and workers they name ("./chunk-hash.js",
 * "/assets/worker-hash.js") and the fonts the stylesheet loads belong to this
 * build too. A file reachable only from a lazy chunk isn't followed — it is
 * re-fetched when it's next needed.
 */
async function pruneAssets() {
  try {
    const res = await fetch(new Request(SHELL_KEY, { cache: 'reload' }));
    if (!res.ok) return;
    const html = await res.text();
    const live = new Set();
    const addRef = (ref) => {
      const path = new URL(ref, self.location.origin + '/assets/').pathname;
      if (isHashed(path)) live.add(path);
    };
    for (const m of html.matchAll(/(?:src|href)\s*=\s*"([^"]+)"/g)) addRef(m[1]);
    if (live.size === 0) return;

    const cache = await caches.open(STATIC);
    for (const path of [...live]) {
      if (!path.endsWith('.js') && !path.endsWith('.css')) continue;
      const entry = (await cache.match(path)) || (await fetch(path).catch(() => null));
      if (!entry || !entry.ok) continue;
      const text = await entry.text();
      for (const m of text.matchAll(/["'](\.?\/(?:assets\/)?[A-Za-z0-9._-]+\.[a-z0-9]{2,5})["']/g)) addRef(m[1]); // imports, workers
      for (const m of text.matchAll(/url\(\s*["']?([^"')]+)["']?\s*\)/g)) addRef(m[1]); // fonts and images
    }

    const stale = (await cache.keys()).filter((req) => {
      const path = new URL(req.url).pathname;
      return isHashed(path) && !live.has(path);
    });
    await Promise.all(stale.map((req) => cache.delete(req)));
  } catch {
    /* offline at activate: keep what we have and prune on the next one */
  }
}
