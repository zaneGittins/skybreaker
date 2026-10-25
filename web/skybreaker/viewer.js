'use strict';

// The image viewer: a link marked data-view opens its image over the page
// instead of in a new tab, with the other images of its [data-gallery] a
// swipe or an arrow key away. A modified click still opens a tab.

const VW_ICON = {
  prev: ['M15 5l-7 7 7 7'],
  next: ['M9 5l7 7-7 7'],
  down: ['M12 4v11', 'M7 10l5 5 5-5', 'M5 20h14'],
  out: ['M14 4h6v6', 'M20 4l-9 9', 'M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5'],
};

let viewer = null;

document.addEventListener('click', e => {
  const a = e.target.closest('a[data-view]');
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  const items = [...(a.closest('[data-gallery]') || a.parentNode).querySelectorAll('a[data-view]')];
  openViewer(items, Math.max(0, items.indexOf(a)));
});

// Back closes the viewer rather than leaving the page under it. This runs
// before the router's listener, which it keeps from seeing these steps.
addEventListener('popstate', e => {
  if (!viewer && !viewerPopping) return;
  e.stopImmediatePropagation();
  if (viewerPopping) { viewerPopping = false; return }
  viewer.close(false);
});
let viewerPopping = false;

function openViewer(links, start) {
  if (viewer || !links.length) return;
  const items = links.map(a => {
    const img = a.querySelector('img');
    const name = a.dataset.name || decodeURIComponent(new URL(a.href).pathname.split('/').pop());
    return { a, href: a.href, name, preview: img?.currentSrc || a.href, title: a.dataset.title || img?.alt || name };
  });
  const many = items.length > 1;
  const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const back = document.activeElement;
  let i = start, nat = { w: 0, h: 0 }, full = false, view = { s: 1, x: 0, y: 0 }, drag = null, tapTimer = 0, lastTap = 0;

  const img = el('img.vw-img', { alt: '', draggable: 'false' });
  const stage = el('div.vw-stage', null, img);
  const count = el('span.vw-count');
  const title = el('span.vw-title');
  const save = el('a.vw-btn', { 'aria-label': 'Download', title: 'Download' }, svg(VW_ICON.down));
  const out = el('a.vw-btn', { target: '_blank', rel: 'noopener noreferrer', 'aria-label': 'Open in a new tab', title: 'Open in a new tab' }, svg(VW_ICON.out));
  const shut = el('button.vw-btn', { type: 'button', 'aria-label': 'Close', title: 'Close', onclick: () => close(true) }, svg(ICON.x));
  const prev = el('button.vw-btn.vw-nav.vw-prev', { type: 'button', 'aria-label': 'Previous image', onclick: () => go(-1) }, svg(VW_ICON.prev));
  const next = el('button.vw-btn.vw-nav.vw-next', { type: 'button', 'aria-label': 'Next image', onclick: () => go(1) }, svg(VW_ICON.next));
  const root = el('div.vw', { role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Image viewer', tabindex: '-1' },
    el('div.vw-shade'), stage,
    el('div.vw-bar', null, el('div.vw-what', null, many ? count : null, title), save, out, shut),
    many ? [prev, next] : null);

  const box = () => ({ w: stage.clientWidth, h: stage.clientHeight });
  // size is the image's fitted size, clear of the bar and arrows on a wide
  // screen; a preview fills the space until the full image gives its size.
  const size = () => {
    const b = box(), wide = innerWidth >= 640;
    const w = b.w - (wide ? 176 : 0), h = b.h - (wide ? 128 : 0);
    return full ? vwFit(nat.w, nat.h, w, h) : vwFit(nat.w * 1e3, nat.h * 1e3, w, h);
  };
  const paint = (animate, extra = '') => {
    const f = size();
    img.style.width = f.w + 'px';
    img.style.height = f.h + 'px';
    img.style.transition = animate && !reduced ? 'transform .26s cubic-bezier(.2, .8, .2, 1), opacity .2s' : 'none';
    img.style.transform = `translate(-50%, -50%) translate(${view.x}px, ${view.y}px) scale(${view.s})${extra}`;
    root.classList.toggle('zoomed', view.s > 1.01);
  };
  const settle = animate => {
    const f = size();
    view = vwClamp(view, f.w, f.h, box().w, box().h);
    paint(animate);
  };

  // show puts item i up: its preview at once, then the full image.
  const show = () => {
    const it = items[i];
    full = false;
    view = { s: 1, x: 0, y: 0 };
    count.textContent = `${i + 1} / ${items.length}`;
    title.textContent = it.title;
    save.href = out.href = it.href;
    save.setAttribute('download', it.name);
    prev.disabled = i === 0;
    next.disabled = i === items.length - 1;
    const want = it.href;
    img.src = it.preview;
    const pv = it.a.querySelector('img');
    nat = { w: pv?.naturalWidth || 4, h: pv?.naturalHeight || 3 };
    paint(false);
    const big = new Image();
    big.onload = () => {
      if (items[i].href !== want || !root.isConnected) return;
      full = true;
      nat = { w: big.naturalWidth, h: big.naturalHeight };
      img.src = want;
      settle(false);
    };
    big.src = want;
  };

  // go slides to the image d along, if there is one.
  const go = d => {
    const j = vwStep(i, items.length, d);
    if (j === i) { settle(true); return }
    i = j;
    if (reduced) { show(); return }
    view.x = -d * box().w;
    img.style.opacity = '0';
    paint(true);
    setTimeout(() => {
      if (!root.isConnected) return;
      show();
      img.style.opacity = '0';
      paint(false, ` translateX(${d * 60}px)`);
      requestAnimationFrame(() => { img.style.opacity = '1'; paint(true) });
    }, 160);
  };

  const zoomTo = (factor, px, py) => {
    const f = size();
    view = vwZoomAt(view, factor, px, py, vwMaxZoom(full ? nat.w : 0, f.w));
    settle(true);
  };
  // toggleZoom fits a zoomed image, or zooms in on the point at (cx, cy).
  const toggleZoom = (cx, cy) => {
    if (view.s > 1.01) { view = { s: 1, x: 0, y: 0 }; settle(true); return }
    const r = stage.getBoundingClientRect(), f = size();
    const real = full && f.w ? nat.w / f.w : 1;
    zoomTo(Math.max(2.5, Math.min(real, vwMaxZoom(nat.w, f.w))), cx - r.left - r.width / 2, cy - r.top - r.height / 2);
  };

  // from is where the image was on the page, to grow from and shrink back to.
  const from = () => {
    const t = items[i].a.querySelector('img') || items[i].a;
    const r = t.getBoundingClientRect(), s = stage.getBoundingClientRect(), f = size();
    if (!r.width || r.bottom < 0 || r.top > innerHeight || !f.w) return null;
    return ` translate(${r.left + r.width / 2 - s.left - s.width / 2}px, ${r.top + r.height / 2 - s.top - s.height / 2}px) scale(${r.width / f.w})`;
  };

  // --- pointers: drag to pan or swipe, pinch to zoom, tap and double tap ---
  const pts = new Map();
  const rel = (x, y) => { const r = stage.getBoundingClientRect(); return { x: x - r.left - r.width / 2, y: y - r.top - r.height / 2 } };
  const pinch = () => {
    const [a, b] = [...pts.values()];
    return { d: Math.hypot(a.x - b.x, a.y - b.y) || 1, m: rel((a.x + b.x) / 2, (a.y + b.y) / 2) };
  };
  stage.addEventListener('pointerdown', e => {
    if (e.button !== 0) return;
    stage.setPointerCapture(e.pointerId);
    pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pts.size === 2) drag = { pinch: pinch(), view: { ...view } };
    else if (pts.size === 1) drag = { x: e.clientX, y: e.clientY, t: performance.now(), view: { ...view }, axis: '', moved: false, onImg: e.target === img, touch: e.pointerType !== 'mouse' };
  });
  stage.addEventListener('pointermove', e => {
    if (!pts.has(e.pointerId) || !drag) return;
    pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (drag.pinch) {
      const p = pinch(), f = size();
      const v = vwZoomAt(drag.view, p.d / drag.pinch.d, drag.pinch.m.x, drag.pinch.m.y, vwMaxZoom(full ? nat.w : 0, f.w));
      view = { s: v.s, x: v.x + p.m.x - drag.pinch.m.x, y: v.y + p.m.y - drag.pinch.m.y };
      paint(false);
      return;
    }
    const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
    if (!drag.moved && Math.hypot(dx, dy) < 6) return;
    drag.moved = true;
    if (drag.view.s > 1.01) {
      const f = size();
      view = vwClamp({ s: view.s, x: drag.view.x + dx, y: drag.view.y + dy }, f.w, f.h, box().w, box().h);
      paint(false);
      return;
    }
    drag.axis ||= Math.abs(dx) > Math.abs(dy) ? 'x' : 'y';
    if (drag.axis === 'x') view = { s: 1, x: many ? dx : dx / 4, y: 0 };
    else {
      const down = Math.max(0, dy);
      view = { s: 1, x: 0, y: down };
      root.style.setProperty('--vw-fade', String(Math.max(0, 1 - down / (box().h * 0.6))));
    }
    paint(false, drag.axis === 'y' ? ` scale(${1 - Math.min(0.25, Math.max(0, dy) / box().h / 2)})` : '');
  });
  const up = e => {
    if (!pts.delete(e.pointerId) || !drag) return;
    if (drag.pinch) {
      if (pts.size === 0) { drag = null; settle(true) }
      else drag = null; // a lifted finger of two ends the gesture
      return;
    }
    const d = drag;
    drag = null;
    root.style.removeProperty('--vw-fade');
    if (!d.moved) { tap(e, d); return }
    if (d.view.s > 1.01) return;
    const b = box();
    const what = vwSwipe(e.clientX - d.x, e.clientY - d.y, performance.now() - d.t, b.w, b.h);
    if (what === 'close') close(true, true);
    else if (what && many) go(what === 'next' ? 1 : -1);
    else { view = { s: 1, x: 0, y: 0 }; paint(true) }
  };
  stage.addEventListener('pointerup', up);
  stage.addEventListener('pointercancel', e => { pts.delete(e.pointerId); drag = null; root.style.removeProperty('--vw-fade'); settle(true) });

  // A mouse click on the image zooms and off it closes; a tap shows or
  // hides the controls, and a double tap zooms.
  const tap = (e, d) => {
    if (!d.touch) {
      if (d.onImg) toggleZoom(e.clientX, e.clientY);
      else close(true);
      return;
    }
    const now = performance.now();
    if (now - lastTap < 280) {
      clearTimeout(tapTimer);
      lastTap = 0;
      toggleZoom(e.clientX, e.clientY);
      return;
    }
    lastTap = now;
    tapTimer = setTimeout(() => root.classList.toggle('bare'), 280);
  };

  stage.addEventListener('wheel', e => {
    e.preventDefault();
    const p = rel(e.clientX, e.clientY);
    const f = size();
    view = vwZoomAt(view, Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.002)), p.x, p.y, vwMaxZoom(full ? nat.w : 0, f.w));
    view = vwClamp(view, f.w, f.h, box().w, box().h);
    paint(false);
  }, { passive: false });

  // Keys are the viewer's while it's open, ahead of the page's shortcuts.
  const keys = e => {
    if (e.key === 'Escape') close(true);
    else if (e.key === 'ArrowLeft') go(-1);
    else if (e.key === 'ArrowRight') go(1);
    else if (e.key === '+' || e.key === '=') zoomTo(1.5, 0, 0);
    else if (e.key === '-') zoomTo(1 / 1.5, 0, 0);
    else if (e.key === '0') { view = { s: 1, x: 0, y: 0 }; settle(true) }
    else if (e.key === 'Tab') {
      const f = [...root.querySelectorAll('a.vw-btn, button.vw-btn:not(:disabled)')];
      const k = f.indexOf(document.activeElement);
      e.preventDefault();
      f[(k + (e.shiftKey ? -1 : 1) + f.length) % f.length]?.focus();
      return e.stopImmediatePropagation();
    } else return e.stopImmediatePropagation();
    e.preventDefault();
    e.stopImmediatePropagation();
  };
  const resize = () => settle(false);

  // close shrinks the image back to its place on the page, or fades it.
  // unwind steps Back past the history entry the viewer added.
  function close(unwind, swiped) {
    if (viewer !== handle) return;
    viewer = null;
    removeEventListener('keydown', keys, true);
    removeEventListener('resize', resize);
    clearTimeout(tapTimer);
    if (unwind && history.state?.viewer) { viewerPopping = true; history.back() }
    root.classList.add('leaving');
    root.setAttribute('inert', '');
    const to = !swiped && view.s <= 1.01 ? from() : null;
    if (to) { view = { s: 1, x: 0, y: 0 }; paint(true, to) }
    else { img.style.transition = reduced ? 'none' : 'transform .2s ease-in, opacity .2s'; img.style.opacity = '0' }
    setTimeout(() => root.remove(), reduced ? 0 : 240);
    if (back?.isConnected) back.focus({ preventScroll: true });
  }
  const handle = { close };
  viewer = handle;

  document.body.append(root);
  addEventListener('keydown', keys, true);
  addEventListener('resize', resize);
  history.pushState({ ...history.state, viewer: true }, '', location.href);
  show();
  const grow = from();
  if (grow && !reduced) {
    paint(false, grow);
    img.style.opacity = '0.4';
    requestAnimationFrame(() => requestAnimationFrame(() => { img.style.opacity = '1'; paint(true) }));
  }
  root.focus({ preventScroll: true });
}
