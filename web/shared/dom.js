'use strict';

// DOM plumbing shared by both UIs. Text from agents (screen snapshots, tool
// inputs, approval bodies) only ever goes in through textContent: el() never
// parses markup, so there is nothing to escape and nothing to sanitise.

const $ = (sel, root = document) => root.querySelector(sel);

// el('div.card.tint-2', {onclick, title}, child, 'text', [more]) builds an
// element. Strings become text nodes; null and false are skipped.
function el(spec, attrs, ...children) {
  const [tag, ...classes] = spec.split('.');
  const node = document.createElement(tag || 'div');
  if (classes.length) node.className = classes.join(' ');
  if (attrs && (typeof attrs !== 'object' || attrs instanceof Node || Array.isArray(attrs))) {
    children.unshift(attrs);
    attrs = null;
  }
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else if (k === 'class') node.className += (node.className ? ' ' : '') + v;
    else if (k === 'dataset') Object.assign(node.dataset, v);
    else if (k === 'text') node.textContent = v;
    else node.setAttribute(k, v === true ? '' : v);
  }
  append(node, children);
  return node;
}

function append(node, children) {
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

// svg builds an icon from a list of path strings; the paths are constants
// in this file, never data.
function svg(paths, cls = '') {
  const ns = 'http://www.w3.org/2000/svg';
  const s = document.createElementNS(ns, 'svg');
  s.setAttribute('viewBox', '0 0 24 24');
  s.setAttribute('fill', 'none');
  s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '2');
  s.setAttribute('stroke-linecap', 'round');
  s.setAttribute('stroke-linejoin', 'round');
  s.setAttribute('aria-hidden', 'true');
  if (cls) s.setAttribute('class', cls);
  for (const d of paths) {
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', d);
    s.append(p);
  }
  return s;
}

const ICON = {
  sun: ['M12 3v2', 'M12 19v2', 'M5.6 5.6l1.4 1.4', 'M17 17l1.4 1.4', 'M3 12h2', 'M19 12h2', 'M5.6 18.4L7 17', 'M17 7l1.4-1.4', 'M12 8a4 4 0 1 0 0 8a4 4 0 1 0 0-8'],
  moon: ['M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z'],
  plus: ['M12 5v14', 'M5 12h14'],
  play: ['M7 5l12 7-12 7z'],
  stop: ['M7 7h10v10H7z'],
  term: ['M4 6l6 6-6 6', 'M12 18h8'],
  x: ['M6 6l12 12', 'M18 6L6 18'],
  check: ['M5 12.5l4.5 4.5L19 7'],
  back: ['M15 5l-7 7 7 7'],
  diff: ['M12 4v8', 'M8 8h8', 'M8 18h8'],
  clock: ['M12 7v5l3 2', 'M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18'],
  shield: ['M12 3l7 3v5c0 5-3.5 8.5-7 10c-3.5-1.5-7-5-7-10V6z'],
  bolt: ['M13 3L5 14h6l-1 7 8-11h-6z'],
  send: ['M12 19V5', 'M6 11l6-6 6 6'],
  chevron: ['M9 6l6 6-6 6'],
  pencil: ['M4 20h4L19 9l-4-4L4 16z', 'M13.5 6.5l4 4'],
};

class APIError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}

// api sends JSON (the servers refuse writes that aren't) and throws the
// server's error message on failure.
async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (method !== 'GET') {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body ?? {});
  }
  const res = await fetch(path, opts);
  const ct = res.headers.get('Content-Type') || '';
  const data = ct.includes('application/json') ? await res.json().catch(() => null) : await res.text();
  if (!res.ok) throw new APIError(res.status, (data && data.error) || (typeof data === 'string' && data.trim()) || res.statusText);
  return data;
}

function toast(text, kind = '', action) {
  const t = el('div.toast' + (kind ? '.' + kind : ''), null, el('span', null, text));
  if (action) t.append(el('button', { onclick: () => { t.remove(); action.run() } }, action.label));
  $('#toasts').append(t);
  setTimeout(() => { t.style.transition = 'opacity .3s'; t.style.opacity = '0'; setTimeout(() => t.remove(), 300) }, kind === 'error' ? 5000 : 3200);
}

const prefs = {
  get(k) { try { return localStorage.getItem('skybreaker.' + k) } catch { return null } },
  set(k, v) { try { localStorage.setItem('skybreaker.' + k, v) } catch { } },
};

function setupTheme() {
  const btn = $('#theme-btn');
  const apply = t => {
    document.documentElement.dataset.theme = t;
    btn.replaceChildren(svg(t === 'dark' ? ICON.sun : ICON.moon));
    btn.setAttribute('aria-label', `Switch to ${t === 'dark' ? 'light' : 'dark'} theme`);
    btn.title = btn.getAttribute('aria-label');
    $('meta[name="theme-color"]')?.setAttribute('content', t === 'dark' ? '#191816' : '#FBFAF7');
  };
  const current = () => prefs.get('theme') === 'light' ? 'light' : 'dark';
  btn.addEventListener('click', () => { const n = current() === 'dark' ? 'light' : 'dark'; prefs.set('theme', n); apply(n) });
  apply(current());
}

// liveEvents keeps an EventSource open and calls onEvent for each message.
// It reconnects with backoff.
function liveEvents(onEvent) {
  let wait = 1000;
  const connect = () => {
    const es = new EventSource('/api/events');
    es.onopen = () => { wait = 1000 };
    es.onmessage = e => { try { onEvent(JSON.parse(e.data)) } catch { } };
    es.onerror = () => {
      es.close();
      setTimeout(connect, wait);
      wait = Math.min(wait * 2, 30000);
    };
  };
  connect();
}

// seg is a row of buttons of which one is on. A pick shows at once; if
// onPick returns a promise, other picks wait for it and a failed one is
// undone.
function seg(options, value, onPick, { disabled = false, cls = '' } = {}) {
  const node = el('div.seg' + cls, { role: 'radiogroup' });
  const draw = v => node.querySelectorAll('button').forEach(b => { const on = b.dataset.v === v; b.classList.toggle('on', on); b.setAttribute('aria-checked', String(on)) });
  const pick = async v => {
    if (node.dataset.busy) return;
    const was = node.dataset.v;
    node.dataset.v = v;
    draw(v);
    node.dataset.busy = '1';
    try { await onPick(v) } catch { node.dataset.v = was; draw(was) } finally { delete node.dataset.busy }
  };
  for (const [v, label] of options) {
    node.append(el('button.v-' + v, { type: 'button', role: 'radio', 'data-v': v, disabled: disabled || null, onclick: () => pick(v) }, label));
  }
  node.dataset.v = value;
  draw(value);
  return node;
}

// debounce collapses bursts of events into one refresh.
function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms) };
}
