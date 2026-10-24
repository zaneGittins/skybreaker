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
  const body = avNode('path', { class: 'av-shape', filter: `url(#${id}g)` });
  s.append(defs, back, ...behind, body, front, ...ahead);
  s._av = { look, state, jobs, mini, back, front, behind, ahead, body, born: performance.now() };
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

const avStill = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

function avDraw(s, t) {
  const a = s._av;
  const showRing = !a.mini;
  const f = avFrame(a.look, a.state, t, { rings: showRing, jobs: a.jobs });
  a.body.setAttribute('d', f.shape);
  a.body.setAttribute('fill', f.color);
  a.body.setAttribute('transform', `translate(0 ${-f.lift}) rotate(${f.rot} 50 50)`);
  a.back.setAttribute('d', f.back);
  a.front.setAttribute('d', f.front);
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
