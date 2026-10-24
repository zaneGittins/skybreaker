'use strict';

// Draws assistants' marks (see avatarlib.js) and keeps them moving: one
// animation loop for every mark on the page, still for reduced motion.

const AV_NS = 'http://www.w3.org/2000/svg';
const avLive = new Set();
let avTicking = false, avIds = 0;

const avNode = (tag, attrs) => {
  const n = document.createElementNS(AV_NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) n.setAttribute(k, v);
  return n;
};

// avatar is an assistant's mark. mini draws the shape alone, for small
// places like the sidebar; state is idle, thinking, busy, needs or done.
function avatar(look, { state = 'idle', mini = false, cls = '' } = {}) {
  const id = 'av' + (++avIds);
  const s = avNode('svg', { viewBox: '0 0 100 100', class: 'av-mark' + (mini ? ' mini' : '') + (cls ? ' ' + cls : ''), 'aria-hidden': 'true' });
  const defs = avNode('defs');
  defs.append(
    avGrain(id + 'g'),
    avClip(id + 'a', 0), avClip(id + 'b', 50));
  const back = avNode('path', { class: 'av-ring' });
  const planetBack = avNode('circle', { class: 'av-planet' });
  const body = avNode('g');
  const top = avNode('g', { 'clip-path': `url(#${id}a)` }), bottom = avNode('g', { 'clip-path': `url(#${id}b)` });
  const shape = () => avNode('path', { class: 'av-shape', filter: `url(#${id}g)` });
  top.append(shape());
  bottom.append(shape());
  body.append(top, bottom);
  const front = avNode('path', { class: 'av-ring' });
  const planetFront = avNode('circle', { class: 'av-planet' });
  s.append(defs, back, planetBack, body, front, planetFront);
  s._av = { look, state, mini, back, front, planetBack, planetFront, body, top, bottom, born: performance.now() };
  avDraw(s, 0);
  if (!avStill()) {
    avLive.add(s);
    avTick();
  }
  return s;
}

// avGrain is a fine paper grain over the shape's colour.
function avGrain(id) {
  const f = avNode('filter', { id, x: '-5%', y: '-5%', width: '110%', height: '110%' });
  f.append(
    avNode('feTurbulence', { type: 'fractalNoise', baseFrequency: '1.6', numOctaves: '2', seed: '4', result: 'n' }),
    avNode('feColorMatrix', { in: 'n', type: 'matrix', values: '0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 -.45 1.12', result: 'm' }),
    avNode('feComposite', { in: 'SourceGraphic', in2: 'm', operator: 'in' }));
  return f;
}

// avClip is half the square, for a shape that splits while busy.
function avClip(id, y) {
  const c = avNode('clipPath', { id });
  c.append(avNode('rect', { x: '-20', y: String(y - (y ? 0 : 20)), width: '140', height: '70' }));
  return c;
}

const avStill = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

function avDraw(s, t) {
  const a = s._av;
  const f = avFrame(a.look, a.state, t, { rings: !a.mini });
  const fill = f.color;
  for (const g of [a.top, a.bottom]) {
    const p = g.firstChild;
    p.setAttribute('d', f.shape);
    p.setAttribute('fill', fill);
  }
  a.top.setAttribute('transform', `translate(0 ${-f.split})`);
  a.bottom.setAttribute('transform', `translate(0 ${f.split})`);
  a.body.setAttribute('transform', `rotate(${f.rot} 50 50)`);
  const showRing = !a.mini;
  a.back.setAttribute('d', showRing ? f.back : '');
  a.front.setAttribute('d', showRing ? f.front : '');
  const planet = f.planet;
  for (const [c, on] of [[a.planetBack, !planet.front], [a.planetFront, planet.front]]) {
    if (!showRing || !on) { c.setAttribute('r', '0'); continue }
    c.setAttribute('cx', planet.x.toFixed(2));
    c.setAttribute('cy', planet.y.toFixed(2));
    c.setAttribute('r', planet.r.toFixed(2));
    c.classList.toggle('amber', !!planet.color);
  }
}

// avSet changes a mark's look or state in place.
function avSet(s, { look, state } = {}) {
  if (!s?._av) return;
  if (look) s._av.look = look;
  if (state) s._av.state = state;
  avDraw(s, performance.now() / 1000);
}

function avTick() {
  if (avTicking) return;
  avTicking = true;
  let last = 0;
  const frame = now => {
    // A mark leaves once it has left the page, or never made it there.
    for (const s of avLive) {
      if (s.isConnected) s._av.shown = true;
      else if (s._av.shown || now - s._av.born > 5000) avLive.delete(s);
    }
    if (avStill()) { avTicking = false; return }
    if (!avLive.size) { avTicking = false; return }
    // About 30 frames a second is smooth enough and kinder to a phone.
    if (now - last > 33) {
      last = now;
      // Only what shows, and moves: a mini mark at rest doesn't.
      for (const s of avLive) {
        if (s.isConnected && s.getClientRects().length && !(s._av.mini && s._av.state === 'idle')) avDraw(s, now / 1000);
      }
    }
    requestAnimationFrame(frame);
  };
  requestAnimationFrame(frame);
}
