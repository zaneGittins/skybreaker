'use strict';

// An assistant's library: what it made for you (podcasts, reports, pages),
// as rows on Today and in chats, and laid out by collection on its Library
// tab. Audio plays in the one player (player.js); everything else opens in
// a reader that keeps your place. What the agent wrote only goes in through
// textContent; its pages run in a sandboxed frame with no origin.

Object.assign(ICON, {
  pause: ['M7 5h3v14H7z', 'M14 5h3v14h-3z'],
  more: ['M5 12h.01', 'M12 12h.01', 'M19 12h.01'],
  star: ['M12 3.5l2.6 5.3 5.9.9-4.3 4.1 1 5.8-5.2-2.7-5.2 2.7 1-5.8-4.3-4.1 5.9-.9z'],
  trash: ['M4 7h16', 'M10 11v6', 'M14 11v6', 'M6 7l1 13h10l1-13', 'M9 7V4h6v3'],
  grid: ['M4 4h7v7H4z', 'M13 4h7v7h-7z', 'M4 13h7v7H4z', 'M13 13h7v7h-7z'],
  code: ['M8 7l-5 5 5 5', 'M16 7l5 5-5 5'],
});

// libCache is each item seen, by assistant/id, as the server last said.
const libCache = new Map();
const libKey = (name, id) => name + '/' + id;
const libURL = (name, kept) => asstAPI(name) + '/files/' + encodeURIComponent(kept);

function libRemember(name, items) {
  for (const it of items) libCache.set(libKey(name, it.id), it);
}

// libSave changes where you are in an item, and redraws its rows. A save
// as the page goes away is kept alive past it.
async function libSave(name, id, patch, keepalive = false) {
  const key = libKey(name, id);
  const was = libCache.get(key);
  if (was) libSync(name, { ...was, ...libLocal(was, patch) });
  try {
    const res = await fetch(asstAPI(name) + '/library/' + encodeURIComponent(id) + '/state', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(patch), keepalive,
    });
    if (!res.ok) throw new Error((await res.json().catch(() => null))?.error || res.statusText);
    const it = await res.json();
    libSync(name, it);
    return it;
  } catch (e) {
    if (!keepalive) toast(e.message, 'error');
    return null;
  }
}

// libLocal is what a patch does to an item, as the server will, so rows
// change at once.
function libLocal(it, p) {
  const out = { ...p };
  if (p.progress > 0 || p.finished) out.seen = true;
  if (p.seen === false) Object.assign(out, { finished: false, progress: 0 });
  return out;
}

// libSync redraws every row of an item on the page.
function libSync(name, it) {
  libCache.set(libKey(name, it.id), it);
  document.querySelectorAll(`[data-lib="${CSS.escape(libKey(name, it.id))}"]`).forEach(n => n.libSync?.(it));
}

// --- covers -------------------------------------------------------------------

const LIB_MONTHS = ['JAN', 'FEB', 'MAR', 'APR', 'MAY', 'JUN', 'JUL', 'AUG', 'SEP', 'OCT', 'NOV', 'DEC'];

// libCover is an item's or collection's art: its own cover, or one drawn
// in the assistant's colour with the date, or a page for something to read.
function libCover(name, look, it, col, cls = '') {
  const cover = it?.cover || col?.cover;
  if (cover) return el('span.lib-cover.lib-cover-img' + cls, null, el('img', { src: libURL(name, cover), alt: '', loading: 'lazy' }));
  if (it?.kind === 'image') return el('span.lib-cover.lib-cover-img' + cls, null, el('img', { src: libURL(name, it.kept), alt: '', loading: 'lazy' }));
  if (it && (it.kind === 'pdf' || it.kind === 'text')) return el('span.lib-cover.lib-cover-page' + cls, null, libPage());
  if (it && (it.kind === 'view' || it.kind === 'file')) return el('span.lib-cover.lib-cover-icon' + cls, null, svg(it.kind === 'view' ? ICON.grid : ICON.file));
  const d = new Date(it?.at || Date.now());
  const tile = el('span.lib-cover.lib-cover-date' + cls, null, el('span.lib-cover-ring'),
    el('span.lib-cover-day', null, el('small', null, LIB_MONTHS[d.getMonth()]), String(d.getDate())));
  tile.style.setProperty('--tile', AV_COLORS[look?.color] || AV_COLORS.clay);
  return tile;
}

const libPage = () => el('span.lib-page', null, el('b'), el('u'), el('u'), el('u'), el('u'));

// --- rows ---------------------------------------------------------------------

// libRow is an item as a row: on Today, in a chat, or in a list. It keeps
// itself up to date as you play or read it.
function libRow(name, look, it, col, { compact = false, when = true } = {}) {
  const key = libKey(name, it.id);
  libCache.set(key, it);
  const title = el('span.lib-title-text');
  const dot = el('i.lib-new', { 'aria-label': 'New' });
  const status = el('em.lib-status');
  const sub = el('span.lib-sub');
  const bar = el('span.lib-bar', null, el('i'));
  const act = el('button.lib-act', { type: 'button' });
  const more = el('button.icon-btn.bare.lib-more', { type: 'button', 'aria-label': 'More', title: 'More', onclick: e => { e.stopPropagation(); libMenu(e.currentTarget, name, libCache.get(key) || it) } }, svg(ICON.more));
  const row = el('div.lib-row' + (compact ? '.compact' : ''), { 'data-lib': key, role: 'button', tabindex: '0' },
    libCover(name, look, it, col),
    el('span.lib-text', null, el('span.lib-title', null, dot, title), el('span.lib-line', null, status, sub), bar),
    act, more);
  const open = () => libOpen(name, libCache.get(key) || it, col, look);
  row.addEventListener('click', e => { if (!e.target.closest('button, a')) open() });
  row.addEventListener('keydown', e => { if ((e.key === 'Enter' || e.key === ' ') && e.target === row) { e.preventDefault(); open() } });
  act.addEventListener('click', e => {
    e.stopPropagation();
    const cur = libCache.get(key) || it;
    if (cur.kind === 'audio') plToggle(name, cur, col, look);
    else open();
  });
  row.libSync = x => {
    title.textContent = x.title;
    dot.hidden = !!x.seen;
    const playing = plIs(name, x.id);
    const st = playing ? { text: plPlaying() ? 'Playing' : 'Paused', tone: 'going' } : libStatus(x);
    status.textContent = st.text;
    row.dataset.tone = playing ? 'playing' : st.tone;
    const bits = [x.summary, when ? ago(x.at) : ''].filter(Boolean);
    sub.textContent = bits.length ? ' · ' + bits.join(' · ') : '';
    const f = playing ? plFraction() : libFraction(x);
    bar.hidden = f == null;
    bar.firstChild.style.width = ((f || 0) * 100) + '%';
    if (x.kind === 'audio') {
      const on = playing && plPlaying();
      act.className = 'lib-act lib-play' + (on ? ' on' : '');
      act.replaceChildren(svg(on ? ICON.pause : ICON.play));
      act.setAttribute('aria-label', (on ? 'Pause ' : 'Play ') + x.title);
    } else {
      act.className = 'lib-act lib-open';
      act.textContent = x.kind === 'file' ? 'Download' : st.tone === 'going' ? 'Continue' : x.finished ? 'Open again' : 'Open';
      act.removeAttribute('aria-label');
    }
  };
  row.libSync(it);
  return row;
}

// libMenu is an item's other actions.
function libMenu(anchor, name, it) {
  const audio = it.kind === 'audio';
  menu(anchor, [
    { label: it.star ? 'Unstar' : 'Star', desc: it.star ? 'Its collection can let it go' : 'Keep it whatever the collection keeps', run: () => libSave(name, it.id, { star: !it.star }) },
    it.finished || it.seen
      ? { label: 'Mark as new', run: () => libSave(name, it.id, { seen: false }) }
      : { label: audio ? 'Mark as played' : 'Mark as read', run: () => libSave(name, it.id, { finished: true }) },
    { label: 'Download', run: () => libDownload(name, it) },
    { label: 'Delete', danger: true, run: () => libDelete(name, it) },
  ]);
}

function libDownload(name, it) {
  const a = el('a', { href: libURL(name, it.kept) + '?name=' + encodeURIComponent(it.name || it.title), download: it.name || it.title });
  document.body.append(a);
  a.click();
  a.remove();
}

async function libDelete(name, it) {
  if (!confirm(`Delete "${it.title}"? It goes from the library for good.`)) return;
  try {
    if (plIs(name, it.id)) plClose();
    await api('DELETE', asstAPI(name) + '/library/' + encodeURIComponent(it.id));
    toast('Deleted');
    refresh();
  } catch (e) { toast(e.message, 'error') }
}

// libChatCard is a library item an agent shared in a chat: its row, once
// it's fetched, or a note when the library let it go.
function libChatCard(name, s) {
  const box = el('div.lib-chat', null, el('div.lib-row.compact.loading', null, el('span.spinner')));
  api('GET', asstAPI(name) + '/library/' + encodeURIComponent(s.item))
    .then(async d => {
      const look = state.asst?.p?.name === name ? state.asst.a.look : (await api('GET', asstAPI(name)).catch(() => null))?.look;
      box.replaceChildren(libRow(name, look, d.item, d.collection, { compact: true, when: false }));
    })
    .catch(() => box.replaceChildren(el('div.lib-gone', null, svg(ICON.file), el('span', null, reveal(s.title || s.path)), el('em', null, 'No longer in the library'))));
  return box;
}

// --- opening ------------------------------------------------------------------

// libOpen opens an item: audio in the player, the rest in its reader.
function libOpen(name, it, col, look) {
  switch (it.kind) {
    case 'audio': plOpen(name, it, col, look); return;
    case 'pdf': libReader(name, it, look, libPdf); return;
    case 'text': libReader(name, it, look, libText); return;
    case 'view': libReader(name, it, look, libView); return;
    case 'image': {
      const a = el('a', { href: libURL(name, it.kept), 'data-name': it.name, 'data-title': it.title }, el('img', { src: libURL(name, it.kept), alt: it.title }));
      openViewer([a], 0);
      break;
    }
    default: libDownload(name, it);
  }
  if (!it.seen) libSave(name, it.id, { seen: true });
}

// libOverlay puts a full-screen layer up: Back, Escape or its close button
// take it down again. A layer has one history step, so Back closes it;
// closing it any other way takes that step back, which the router must not
// see. This runs before the router's listener.
let libLayer = null, libPopping = 0;
addEventListener('popstate', e => {
  if (libPopping) { libPopping--; e.stopImmediatePropagation(); return }
  if (!libLayer) return;
  e.stopImmediatePropagation();
  libLayer.close(false);
});

function libOverlay(root, onClose) {
  // A layer opened over another takes its place, and its history step.
  const had = libLayer;
  had?.close(false, true);
  const back = had ? had.back : document.activeElement;
  if (!had) history.pushState({ ...history.state, layer: true }, '');
  const keys = e => { if (e.key === 'Escape' && !document.querySelector('.menu')) { e.preventDefault(); layer.close(true) } };
  const layer = {
    root,
    back,
    close(pop, replaced) {
      if (libLayer !== layer) return;
      libLayer = null;
      removeEventListener('keydown', keys);
      try { onClose?.() } finally { leave([root], 180) }
      if (replaced) return;
      back?.focus?.({ preventScroll: true });
      if (pop) { libPopping++; history.back() }
    },
  };
  libLayer = layer;
  addEventListener('keydown', keys);
  document.body.append(root);
  requestAnimationFrame(() => root.focus({ preventScroll: true }));
  return layer;
}

// libReader is the reader's frame: a bar with the title and where you are,
// and a body each kind fills.
function libReader(name, it, look, fill) {
  const where = el('span.rd-where');
  const shut = el('button.icon-btn.bare.rd-btn', { type: 'button', 'aria-label': 'Close', title: 'Close' }, svg(ICON.back));
  const star = el('button.icon-btn.bare.rd-btn', { type: 'button', 'aria-label': 'Star', title: 'Star' }, svg(ICON.star));
  const get = el('button.icon-btn.bare.rd-btn', { type: 'button', 'aria-label': 'Download', title: 'Download', onclick: () => libDownload(name, it) }, svg(ICON.down));
  const body = el('div.rd-body');
  const root = el('div.rd.rd-' + it.kind, { role: 'dialog', 'aria-modal': 'true', 'aria-label': it.title, tabindex: '-1' },
    el('div.rd-bar', null, shut, el('div.rd-what', null, el('b', null, it.title), where), star, get), body);
  asstAccent(root, look?.color);
  const drawStar = x => star.classList.toggle('on', !!x.star);
  drawStar(it);
  star.addEventListener('click', async () => { const x = await libSave(name, it.id, { star: !(libCache.get(libKey(name, it.id)) || it).star }); if (x) drawStar(x) });
  let stop = null;
  const layer = libOverlay(root, () => stop?.());
  shut.addEventListener('click', () => layer.close(true));
  stop = fill({ name, it, body, where, root });
}

// libTracker saves where you are a few seconds after you stop moving, and
// at once when the reader closes or the page goes.
function libTracker(name, it) {
  let pending = null, timer = 0, done = !!it.finished;
  const flush = keep => {
    clearTimeout(timer);
    if (pending) libSave(name, it.id, pending, keep);
    pending = null;
  };
  const hide = () => { if (document.visibilityState === 'hidden') flush(true) };
  document.addEventListener('visibilitychange', hide);
  return {
    set(patch) {
      if (patch.finished) { if (done) delete patch.finished; else done = true }
      pending = { ...pending, ...patch };
      clearTimeout(timer);
      timer = setTimeout(() => flush(false), 2500);
    },
    stop() { document.removeEventListener('visibilitychange', hide); flush(true) },
  };
}

// libPdf shows a PDF page by page with PDF.js, drawing pages as they come
// into view, and keeps the page you're on.
function libPdf({ name, it, body, where }) {
  const track = libTracker(name, it);
  const pages = el('div.rd-pages');
  body.replaceChildren(el('div.rd-loading', null, el('span.spinner')));
  let task = null, doc = null, observer = null, gone = false, current = 0;
  (async () => {
    try {
      const pdfjs = await import('/vendor/pdfjs/pdf.min.mjs');
      pdfjs.GlobalWorkerOptions.workerSrc = '/vendor/pdfjs/pdf.worker.min.mjs';
      task = pdfjs.getDocument({ url: libURL(name, it.kept), isEvalSupported: false });
      doc = await task.promise;
      if (gone) return task.destroy();
      const n = doc.numPages;
      if (it.duration !== n) track.set({ duration: n });
      const first = await doc.getPage(1);
      const vp = first.getViewport({ scale: 1 });
      const width = () => Math.min(body.clientWidth - 32, 920);
      const slots = Array.from({ length: n }, (_, i) => {
        const s = el('div.rd-page', { 'data-page': String(i + 1) });
        s.style.aspectRatio = `${vp.width} / ${vp.height}`;
        return s;
      });
      pages.replaceChildren(...slots);
      body.replaceChildren(pages);
      const drawn = new Set();
      const draw = async i => {
        if (drawn.has(i) || gone) return;
        drawn.add(i);
        const page = await doc.getPage(i + 1);
        const scale = width() / page.getViewport({ scale: 1 }).width * (devicePixelRatio || 1);
        const v = page.getViewport({ scale });
        const c = el('canvas');
        c.width = Math.floor(v.width);
        c.height = Math.floor(v.height);
        await page.render({ canvas: c, canvasContext: c.getContext('2d'), viewport: v }).promise;
        if (!gone) slots[i].replaceChildren(c);
      };
      observer = new IntersectionObserver(es => es.forEach(e => { if (e.isIntersecting) draw(Number(e.target.dataset.page) - 1) }), { root: body, rootMargin: '800px 0px' });
      slots.forEach(s => observer.observe(s));
      const onScroll = () => {
        const mid = body.scrollTop + body.clientHeight / 3;
        let p = 1;
        slots.forEach((s, i) => { if (s.offsetTop <= mid) p = i + 1 });
        const end = body.scrollTop + body.clientHeight >= body.scrollHeight - 8;
        if (end) p = n;
        where.textContent = `Page ${p} of ${n}`;
        if (p !== current) {
          current = p;
          track.set({ progress: p, ...(p === n ? { finished: true } : {}) });
        }
      };
      body.addEventListener('scroll', onScroll, { passive: true });
      const start = Math.min(n, Math.max(1, Math.round(it.finished ? 1 : it.progress || 1)));
      requestAnimationFrame(() => {
        body.scrollTop = slots[start - 1].offsetTop - 12;
        current = start;
        where.textContent = `Page ${start} of ${n}`;
        if (!it.seen) track.set({ progress: start });
        // A PDF that fits on the screen is read once it's seen.
        if (body.scrollHeight <= body.clientHeight + 8) onScroll();
      });
    } catch (e) {
      if (!gone) body.replaceChildren(el('div.rd-note', null, 'Couldn’t open this PDF. ', el('button.btn.sm', { type: 'button', onclick: () => libDownload(name, it) }, 'Download it')));
    }
  })();
  return () => { gone = true; observer?.disconnect(); track.stop(); task?.destroy() };
}

// libText reads Markdown or text, keeping your place as the block at the
// top of the page.
function libText({ name, it, body, where }) {
  const track = libTracker(name, it);
  body.replaceChildren(el('div.rd-loading', null, el('span.spinner')));
  let gone = false;
  fetch(libURL(name, it.kept), { credentials: 'same-origin' })
    .then(r => r.ok ? r.text() : Promise.reject(new Error('HTTP ' + r.status)))
    .then(text => {
      if (gone) return;
      const article = /\.md$/.test(it.kept) ? el('article.rd-article.show-md', null, markdown(text)) : el('article.rd-article.rd-plain', null, reveal(text));
      body.replaceChildren(article);
      // The rendered Markdown comes wrapped: its blocks are inside.
      const box = article.children.length === 1 && article.firstElementChild.children.length ? article.firstElementChild : article;
      const blocks = [...box.children];
      const n = Math.max(1, blocks.length);
      const topOf = b => b.getBoundingClientRect().top - body.getBoundingClientRect().top + body.scrollTop;
      const onScroll = () => {
        const top = body.scrollTop;
        const end = top + body.clientHeight >= body.scrollHeight - 8;
        const at = libReadAnchor(blocks.map(topOf), top);
        const frac = end ? 1 : Math.max(0, at) / n;
        where.textContent = Math.round(frac * 100) + '%';
        track.set({ progress: Math.max(frac, 0.001), ...(end ? { finished: true } : {}) });
      };
      body.addEventListener('scroll', onScroll, { passive: true });
      requestAnimationFrame(() => {
        const at = it.finished ? 0 : Math.round((it.progress || 0) * n);
        if (at > 0 && blocks[Math.min(at, n - 1)]) body.scrollTop = topOf(blocks[Math.min(at, n - 1)]);
        where.textContent = it.finished ? 'Read' : Math.round((it.progress || 0) * 100) + '%';
        // Text that fits on the screen is read once it's seen.
        if (!it.finished && body.scrollHeight <= body.clientHeight + 8) onScroll();
      });
    })
    .catch(() => { if (!gone) body.replaceChildren(el('div.rd-note', null, 'Couldn’t load this file.')) });
  return () => { gone = true; track.stop() };
}

// libFrame is a page the agent made, framed: sandboxed with script but no
// origin, told the theme and where you were, telling back how far you got
// and how tall it is.
function libFrame(name, it, look, { inline = false, track } = {}) {
  const frame = el('iframe.lib-frame', { sandbox: 'allow-scripts', src: libURL(name, it.kept), title: it.title, loading: inline ? 'lazy' : 'eager', referrerpolicy: 'no-referrer' });
  const onMessage = e => {
    if (e.source !== frame.contentWindow) return;
    const m = e.data;
    if (!m || typeof m !== 'object') return;
    if (m.sky === 'height' && inline && Number.isFinite(m.value)) frame.style.height = Math.max(120, Math.min(720, m.value)) + 'px';
    if (m.sky === 'progress' && Number.isFinite(m.value)) track?.set({ progress: Math.max(0.001, Math.min(1, m.value)) });
    if (m.sky === 'done') track?.set({ finished: true });
  };
  addEventListener('message', onMessage);
  // The page may load once. A second load is it navigating its frame
  // elsewhere, which its sandbox allows: that page is taken down, and
  // never told anything.
  let loads = 0;
  frame.addEventListener('load', () => {
    if (++loads > 1) {
      frame.stop();
      frame.replaceWith(el('div.rd-note.lib-frame-gone', null, 'This page tried to open another site, so it was closed.'));
      return;
    }
    frame.contentWindow?.postMessage({
      sky: 'init', theme: document.documentElement.dataset.theme === 'light' ? 'light' : 'dark',
      accent: AV_COLORS[look?.color] || AV_COLORS.clay, progress: it.progress || 0, finished: !!it.finished,
    }, '*');
  });
  frame.stop = () => removeEventListener('message', onMessage);
  return frame;
}

function libView({ name, it, body, root }) {
  const track = libTracker(name, it);
  const look = state.asst?.a?.look;
  const frame = libFrame(name, it, look, { track });
  body.replaceChildren(frame);
  root.querySelector('.rd-where').textContent = 'Runs sandboxed, apart from the app';
  return () => { frame.stop(); track.stop() };
}

// --- the Library tab ----------------------------------------------------------

const libFilter = new Map(); // assistant → the filter chosen

// libShape is what the Library tab shows, without where you are in each:
// while it holds, a live update redraws rows in place, and the pages the
// agent made, running in their frames, are left alone.
const libShape = (d, filter) => JSON.stringify([filter, d.collections, libGroups(d.items, d.collections, filter).map(g => g.items.map(i => [i.id, i.title, i.summary, i.cover]))]);

// libRefresh redraws the Library tab in place if its shape still holds,
// and says whether it did.
async function libRefresh(seq, name) {
  const page = $('#app .lib');
  if (!page || page.dataset.name !== name) return false;
  const d = await api('GET', asstAPI(name) + '/library');
  if (seq !== state.seq) return true;
  const filter = libFilter.get(name) || 'all';
  if (libShape(d, filter) !== page.dataset.shape) return false;
  for (const it of d.items) libSync(name, it);
  const counts = { new: libFresh(d.items), kept: d.items.filter(i => i.star).length };
  page.querySelectorAll('.lib-chip[data-k]').forEach(c => {
    const n = counts[c.dataset.k], tag = c.querySelector('.n');
    if (tag) tag.textContent = n ? String(n) : '';
    if (tag) tag.hidden = !n;
  });
  return true;
}

async function asstLibrary(seq, name, look) {
  const d = await api('GET', asstAPI(name) + '/library');
  if (seq !== state.seq) return null;
  libRemember(name, d.items);
  const filter = libFilter.get(name) || 'all';
  const fresh = libFresh(d.items), kept = d.items.filter(i => i.star).length;
  const chip = (k, label, n) => el('button.lib-chip' + (k === filter ? '.on' : ''), { type: 'button', 'data-k': k, 'aria-pressed': String(k === filter), onclick: () => { libFilter.set(name, k); refresh() } },
    label, k === 'all' ? null : el('span.n', { hidden: n ? null : true }, n ? String(n) : ''));
  if (!d.items.length) {
    return el('div.lib', null, el('div.lib-empty', null, el('b', null, 'Nothing here yet'),
      el('p', null, `What ${look.name} makes for you lands here: podcasts to play, reports to read, pages to use. Ask in a chat, or set up a routine.`)));
  }
  const groups = libGroups(d.items, d.collections, filter);
  return el('div.lib', { 'data-name': name, 'data-shape': libShape(d, filter) },
    el('div.lib-filters', { role: 'group', 'aria-label': 'Show' }, chip('all', 'All'), chip('new', 'New', fresh), chip('kept', 'Kept', kept)),
    groups.length ? groups.map(g => libGroup(name, look, g, d.items)) : el('p.lib-none', null, filter === 'new' ? 'You’re all caught up.' : 'Nothing starred yet.'));
}

// libGroup is one collection, laid out as it asks.
function libGroup(name, look, g, all) {
  const c = g.col;
  const layout = c?.layout || 'list';
  const newest = g.items[0];
  const keeps = !c ? 'Kept for 30 days' : c.keep && c.keep_days ? `Keeps ${c.keep}, ${c.keep_days} days` : c.keep ? `Keeps ${c.keep}` : c.keep_days ? `Keeps ${c.keep_days} days` : 'Kept until you delete';
  const meta = [c?.about, keeps, c?.speed && c.speed !== 1 ? 'plays at ' + libSpeedLabel(c.speed) : ''].filter(Boolean).join(' · ');
  const playNew = c && libPlayNew(all, c.id);
  const head = el('div.lib-col-head', null,
    layout === 'podcast' || c?.cover ? libCover(name, look, c?.cover ? null : newest, c, '.big') : null,
    el('div.lib-col-title', null, el('h2', null, c ? c.title : 'Shared'), el('span', null, meta)),
    playNew ? el('button.btn.sm.lib-play-new', { type: 'button', onclick: () => plPlay(name, playNew, c, look) }, svg(ICON.play), el('span', null, 'Play new')) : null);
  let body;
  if (layout === 'issues') body = el('div.lib-issues', null, g.items.map(it => libIssue(name, look, it, c)));
  else if (layout === 'gallery') body = el('div.lib-gallery', { 'data-gallery': '' }, g.items.map(it => libTile(name, look, it, c)));
  else {
    // A page the agent keeps up to date shows itself, live, at the top.
    const live = layout === 'list' && newest.kind === 'view' ? newest : null;
    const rest = g.items.filter(it => it !== live);
    body = [live ? libLive(name, look, live) : null,
      rest.length ? el('div.lib-list', null, rest.map(it => libRow(name, look, it, c, { compact: true, when: layout !== 'podcast' }))) : null];
  }
  return el('section.lib-col.lib-layout-' + layout, null, head, body);
}

// libIssue is a report or newsletter as a card.
function libIssue(name, look, it, col) {
  const key = libKey(name, it.id);
  const status = el('span.lib-issue-status');
  const bar = el('span.lib-bar', null, el('i'));
  const dot = el('i.lib-new');
  const card = el('button.lib-issue', { type: 'button', 'data-lib': key, onclick: () => libOpen(name, libCache.get(key) || it, col, look) },
    dot, it.cover || it.kind === 'image' ? libCover(name, look, it, null) : el('span.lib-issue-page', null, libPage()),
    el('b', null, it.title), status, bar);
  card.addEventListener('contextmenu', e => { e.preventDefault(); libMenu(card, name, libCache.get(key) || it) });
  card.libSync = x => {
    dot.hidden = !!x.seen;
    const st = libStatus(x);
    card.dataset.tone = st.tone;
    status.replaceChildren(...[st.tone === 'done' ? svg(ICON.check) : null, st.text === 'New' ? ago(x.at) : st.text].filter(Boolean));
    const f = libFraction(x);
    bar.hidden = f == null;
    bar.firstChild.style.width = ((f || 0) * 100) + '%';
  };
  card.libSync(it);
  return el('div.lib-issue-wrap', null, card,
    el('button.icon-btn.bare.lib-more', { type: 'button', 'aria-label': 'More', onclick: e => libMenu(e.currentTarget, name, libCache.get(key) || it) }, svg(ICON.more)));
}

// libTile is an image in a gallery, opening in the viewer with the rest.
function libTile(name, look, it, col) {
  if (it.kind !== 'image') return libIssue(name, look, it, col);
  const a = el('a.lib-tile', { href: libURL(name, it.kept), 'data-view': '', 'data-name': it.name, 'data-title': it.title, 'data-lib': libKey(name, it.id),
    onclick: () => { if (!it.seen) libSave(name, it.id, { seen: true }) } },
    el('img', { src: libURL(name, it.kept), alt: it.title, loading: 'lazy' }), el('span.lib-tile-title', null, it.title));
  a.libSync = () => {};
  return a;
}

// libLive is a page the agent made, running in the library itself.
function libLive(name, look, it) {
  const track = libTracker(name, it);
  const frame = libFrame(name, it, look, { inline: true, track });
  if (!it.seen) libSave(name, it.id, { seen: true });
  const card = el('div.lib-live', { 'data-lib': libKey(name, it.id) }, frame,
    el('div.lib-live-foot', null, svg(ICON.code), el('span', null, `Made by ${look.name} · runs sandboxed · ${ago(it.at)}`),
      el('span.grow'),
      el('button.btn.ghost.sm', { type: 'button', onclick: () => libOpen(name, libCache.get(libKey(name, it.id)) || it, null, look) }, 'Open'),
      el('button.icon-btn.bare.lib-more', { type: 'button', 'aria-label': 'More', onclick: e => libMenu(e.currentTarget, name, libCache.get(libKey(name, it.id)) || it) }, svg(ICON.more))));
  card.libSync = () => {};
  // The page is redrawn on every live update: let the old frame go.
  new MutationObserver((_, o) => { if (!card.isConnected) { o.disconnect(); frame.stop(); track.stop() } }).observe($('#app'), { childList: true });
  return card;
}
