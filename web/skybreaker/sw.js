'use strict';

// Skybreaker's service worker: it shows web push notifications and opens the
// session one points at. It caches nothing.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));

// Only this origin's pages are opened from a notification.
const own = u => {
  try {
    const url = new URL(u || '/', self.location.origin);
    return url.origin === self.location.origin ? url.href : self.location.origin + '/';
  } catch { return self.location.origin + '/' }
};

self.addEventListener('push', e => {
  let d = {};
  try { d = e.data.json() } catch { }
  e.waitUntil(self.registration.showNotification(d.title || 'Skybreaker', {
    body: d.body || '', tag: d.tag || undefined, renotify: !!d.tag,
    icon: '/icon-192.png?v=halo', badge: '/icon-192.png?v=halo', data: { url: own(d.url) },
  }));
});

self.addEventListener('notificationclick', e => {
  e.notification.close();
  const url = own(e.notification.data && e.notification.data.url);
  e.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(wins => {
    for (const w of wins) {
      if ('focus' in w) return w.focus().then(f => f.navigate ? f.navigate(url) : f);
    }
    return self.clients.openWindow(url);
  }));
});
