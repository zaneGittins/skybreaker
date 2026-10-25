'use strict';

// The image viewer's geometry, kept apart from the DOM so it can be tested.
// A view is { s, x, y }: the scale over the fitted size and the offset of
// the image's centre from the stage's, in screen pixels.

// vwFit is an image's size contained in a box, never larger than its own.
function vwFit(w, h, boxW, boxH) {
  if (!w || !h) return { w: 0, h: 0 };
  const k = Math.min(1, boxW / w, boxH / h);
  return { w: Math.round(w * k), h: Math.round(h * k) };
}

// vwZoomAt scales a view by factor about (px, py), measured from the
// stage's centre, so that point of the image stays under the pointer.
function vwZoomAt(v, factor, px, py, max) {
  const s = Math.min(max, Math.max(1, v.s * factor));
  const k = s / v.s;
  return { s, x: px - (px - v.x) * k, y: py - (py - v.y) * k };
}

// vwClamp keeps a view's image covering the box where it's larger than it,
// and centred where it isn't.
function vwClamp(v, w, h, boxW, boxH) {
  const lim = (len, box) => Math.max(0, (len * v.s - box) / 2);
  const hold = (n, m) => Math.min(m, Math.max(-m, n)) || 0;
  return { s: v.s, x: hold(v.x, lim(w, boxW)), y: hold(v.y, lim(h, boxH)) };
}

// vwMaxZoom is how far an image fitted at fitW may be zoomed: to twice its
// real pixels, and at least 4x.
function vwMaxZoom(natW, fitW) {
  return natW && fitW ? Math.max(4, 2 * natW / fitW) : 4;
}

// vwSwipe is what a drag of (dx, dy) over dt ms on an unzoomed image means:
// 'next', 'prev', 'close' (down), or null. Far enough or fast enough decides.
function vwSwipe(dx, dy, dt, w, h) {
  const fast = d => Math.abs(d) > 20 && Math.abs(d) / Math.max(dt, 1) > 0.5;
  if (Math.abs(dx) > Math.abs(dy)) {
    if (Math.abs(dx) > w * 0.25 || fast(dx)) return dx < 0 ? 'next' : 'prev';
    return null;
  }
  return dy > 0 && (dy > h * 0.15 || fast(dy)) ? 'close' : null;
}

// vwStep is the index d along from i in a gallery of n, held to its ends.
function vwStep(i, n, d) {
  return Math.min(n - 1, Math.max(0, i + d));
}

if (typeof module !== 'undefined') module.exports = { vwFit, vwZoomAt, vwClamp, vwMaxZoom, vwSwipe, vwStep };
