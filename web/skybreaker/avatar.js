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
// places like the sidebar; state is idle, thinking, busy, needs or done,
// and jobs is how many things a busy mark has in flight.
function avatar(look, { state = 'idle', jobs = 3, mini = false, cls = '' } = {}) {
  const id = 'av' + (++avIds);
  const s = avNode('svg', { viewBox: '0 0 100 100', class: 'av-mark' + (mini ? ' mini' : '') + (cls ? ' ' + cls : ''), 'aria-hidden': 'true' });
  const defs = avNode('defs');
  defs.append(avGrain(id + 'g'));
  const back = avNode('path', { class: 'av-ring' });
  const front = avNode('path', { class: 'av-ring' });
  // The planet and its moons each have a circle behind the shape and one
  // in front, and show whichever side of the orbit they are on.
  const bodies = () => Array.from({ length: AV_MOONS + 1 }, () => avNode('circle', { class: 'av-planet', r: '0' }));
  const behind = bodies(), ahead = bodies();
  const body = avNode('path', { class: 'av-shape' });
  // A light finish: the shape lit from within, a halo behind it, and an
  // amber flare at the planet while it needs you.
  const stops = [0, 0.55, 1].map(o => avNode('stop', { offset: String(o) }));
  const grad = avNode('radialGradient', { id: id + 'r', cx: '46%', cy: '42%', r: '62%' });
  grad.append(...stops);
  defs.append(grad, avBlur(id + 'h'));
  const halo = avNode('path', { class: 'av-halo', filter: `url(#${id}h)` });
  const flare = avNode('circle', { class: 'av-halo', r: '0', fill: AV_AMBER, opacity: '.55', filter: `url(#${id}h)` });
  // The eye: its white also clips the iris, so the lids can cover it.
  const clip = avNode('clipPath', { id: id + 'e' }), lid = avNode('path');
  clip.append(lid);
  defs.append(clip);
  const white = avNode('path', { class: 'av-eye' });
  const iris = avNode('circle', { class: 'av-eye', r: '0', 'clip-path': `url(#${id}e)` });
  const lids = avNode('path', { class: 'av-eye' });
  s.append(defs, halo, flare, back, ...behind, body, white, iris, lids, front, ...ahead);
  s._av = { look, state, jobs, mini, back, front, behind, ahead, body, eye: { lid, white, iris, lids }, light: { stops, halo, flare }, id, born: performance.now() };
  // Drawn where the clock is now, so a redrawn mark doesn't jump.
  avDraw(s, performance.now() / 1000);
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

// avBlur softens a halo, with room to spread past the shape.
function avBlur(id) {
  const f = avNode('filter', { id, filterUnits: 'userSpaceOnUse', x: '-30', y: '-30', width: '160', height: '160' });
  f.append(avNode('feGaussianBlur', { stdDeviation: '8' }));
  return f;
}

const avStill = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

function avDraw(s, t) {
  const a = s._av;
  const showRing = !a.mini;
  const f = avFrame(a.look, a.state, t, { rings: showRing, jobs: a.jobs, blink: !avStill() });
  const turn = `translate(0 ${-f.lift}) rotate(${f.rot} 50 50)`;
  a.body.setAttribute('d', f.shape);
  a.body.setAttribute('transform', turn);
  const g = f.glow, l = a.light;
  if (g) {
    a.body.setAttribute('fill', `url(#${a.id}r)`);
    a.body.removeAttribute('filter');
    [g.core, g.mid, g.edge].forEach((c, i) => l.stops[i].setAttribute('stop-color', c));
  } else {
    a.body.setAttribute('fill', f.color);
    a.body.setAttribute('filter', `url(#${a.id}g)`);
  }
  // A mini mark is too small for a halo.
  const glowing = g && !a.mini;
  l.halo.setAttribute('d', glowing ? f.shape : '');
  l.halo.setAttribute('transform', turn);
  if (glowing) {
    l.halo.setAttribute('fill', g.halo);
    l.halo.setAttribute('opacity', (0.25 + 0.55 * g.level).toFixed(2));
  }
  const flaring = glowing && a.state === 'needs';
  l.flare.setAttribute('r', flaring ? '9' : '0');
  if (flaring) {
    l.flare.setAttribute('cx', f.planet.x.toFixed(2));
    l.flare.setAttribute('cy', f.planet.y.toFixed(2));
  }
  a.back.setAttribute('d', f.back);
  a.front.setAttribute('d', f.front);
  const e = f.eye, ae = a.eye;
  ae.lid.setAttribute('d', e ? e.white : '');
  ae.white.setAttribute('d', e ? e.white : '');
  ae.lids.setAttribute('d', e ? e.lids : '');
  ae.iris.setAttribute('r', e ? e.iris.r.toFixed(2) : '0');
  if (e) {
    ae.white.setAttribute('fill', e.tint);
    ae.lids.setAttribute('fill', e.ink);
    ae.iris.setAttribute('fill', e.ink);
    ae.iris.setAttribute('cx', e.iris.x.toFixed(2));
    ae.iris.setAttribute('cy', e.iris.y.toFixed(2));
  }
  const all = showRing ? [f.planet, ...f.moons] : [];
  for (let i = 0; i < a.behind.length; i++) {
    const p = all[i];
    for (const [c, on] of [[a.behind[i], p && !p.front], [a.ahead[i], p && p.front]]) {
      if (!on) { c.setAttribute('r', '0'); continue }
      c.setAttribute('cx', p.x.toFixed(2));
      c.setAttribute('cy', p.y.toFixed(2));
      c.setAttribute('r', p.r.toFixed(2));
      c.classList.toggle('amber', !!p.color);
    }
  }
}

// avSet changes a mark's look, state or jobs in place.
function avSet(s, { look, state, jobs } = {}) {
  if (!s?._av) return;
  if (look) s._av.look = look;
  if (state) s._av.state = state;
  if (jobs) s._av.jobs = jobs;
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
