'use strict';

// An assistant's mark: a shape in a muted colour, with an ink orbit passing
// behind it and a small planet riding it, like Skybreaker's own logo. Its
// state is told by motion alone. Pure: avatar.js draws what this computes.

const AV_SHAPES = {
  dot: 'M50 26a24 24 0 1 1 0 48a24 24 0 1 1 0-48Z',
  square: 'M34 28h32q6 0 6 6v32q0 6-6 6h-32q-6 0-6-6v-32q0-6 6-6Z',
  triangle: 'M45.5 25q4.5-7 9 0l20 36q4 7.5-4.5 7.5h-40q-8.5 0-4.5-7.5Z',
  diamond: 'M46 25q4-4 8 0l21 21q4 4 0 8l-21 21q-4 4-8 0l-21-21q-4-4 0-8Z',
  hexagon: 'M72.5 63L50 76L27.5 63L27.5 37L50 24L72.5 37Z',
  crescent: 'M50.73 25.28A25 25 0 1 0 69.69 60.5A20 20 0 1 1 50.73 25.28Z',
};

const AV_COLORS = {
  clay: '#C8714F', sage: '#8FA283', ink: '#6F8DB3', ochre: '#CFA556',
  rose: '#C08893', slate: '#8D8FA0', moss: '#7E8B5A', plum: '#9C7BA0',
};

// A temper sets the pace and how much the orbit wobbles.
const AV_TEMPER = {
  calm: { pace: 1, lobes: 2, wobble: 0.035 },
  lively: { pace: 1.8, lobes: 3, wobble: 0.05 },
  precise: { pace: 0.7, lobes: 4, wobble: 0.02 },
};

const AV_STATES = ['idle', 'thinking', 'busy', 'needs', 'done'];
const AV_RINGS = ['fine', 'double', 'dotted', 'arc', 'none'];
const AV_EYES = ['none', 'soft'];
const AV_FINISHES = ['paper', 'light'];
const AV_AMBER = '#E8B04A';

// avRingPoint is the orbit at angle th: an ellipse that wobbles a little,
// tilted about the centre. scale shrinks it, for a double ring's inner one.
function avRingPoint(temper, th, t, amp, tilt, scale = 1) {
  const T = AV_TEMPER[temper] || AV_TEMPER.calm;
  const w = scale * (1 + amp * T.wobble * Math.sin(th * T.lobes + t * 1.6));
  const x = 40 * Math.cos(th) * w, y = 11 * Math.sin(th) * w, a = tilt * Math.PI / 180;
  return [50 + x * Math.cos(a) - y * Math.sin(a), 50 + x * Math.sin(a) + y * Math.cos(a)];
}

const avNum = v => (Math.round(v * 100) / 100).toString();

// avRibbon is a pen stroke along points: a filled outline, thickest in the
// middle and tapering to both ends.
function avRibbon(pts, width) {
  const n = pts.length - 1, left = [], right = [];
  pts.forEach(([x, y], i) => {
    const [x2, y2] = pts[Math.min(n, i + 1)], [x0, y0] = pts[Math.max(0, i - 1)];
    let nx = -(y2 - y0), ny = x2 - x0;
    const l = Math.hypot(nx, ny) || 1, u = i / n;
    const w = width * (0.12 + 0.88 * Math.pow(Math.min(1, u / 0.18, (1 - u) / 0.18), 0.7)) / 2;
    nx = nx / l * w; ny = ny / l * w;
    left.push(avNum(x + nx) + ' ' + avNum(y + ny));
    right.push(avNum(x - nx) + ' ' + avNum(y - ny));
  });
  return 'M' + left.join('L') + 'L' + right.reverse().join('L') + 'Z';
}

// avRingHalf is the back (behind the shape) or front half of the orbit,
// or the part of it from a0 to a1.
function avRingHalf(temper, t, amp, tilt, front, { scale = 1, width = 3.2, a0 = 0, a1 = 1 } = {}) {
  const pts = [];
  for (let i = 0; i <= 48; i++) pts.push(avRingPoint(temper, (front ? 0 : Math.PI) + Math.PI * (a0 + (a1 - a0) * i / 48), t, amp, tilt, scale));
  return avRibbon(pts, width);
}

// avDots is a dotted orbit's back or front half: small round dots.
function avDots(temper, t, amp, tilt, front) {
  let d = '';
  for (let i = 0; i < 18; i++) {
    const [x, y] = avRingPoint(temper, (front ? 0 : Math.PI) + Math.PI * (i + 0.5) / 18, t, amp, tilt);
    d += `M${avNum(x - 1.15)} ${avNum(y)}a1.15 1.15 0 1 0 2.3 0a1.15 1.15 0 1 0 -2.3 0Z`;
  }
  return d;
}

// avRing is a ring style's back and front: double is two finer rings,
// arc only the part in front, none no ring at all.
function avRing(ring, temper, t, amp, tilt) {
  const half = (front, o) => avRingHalf(temper, t, amp, tilt, front, o);
  switch (ring) {
    case 'double': {
      const outer = { width: 2.4 }, inner = { width: 1.5, scale: 0.86 };
      return { back: half(false, outer) + half(false, inner), front: half(true, outer) + half(true, inner) };
    }
    case 'dotted': return { back: avDots(temper, t, amp, tilt, false), front: avDots(temper, t, amp, tilt, true) };
    case 'arc': return { back: '', front: half(true, { width: 3.4, a0: 0.02, a1: 0.98 }) };
    case 'none': return { back: '', front: '' };
    default: return { back: half(false), front: half(true) };
  }
}

// avShade mixes a colour toward black (k < 0) or white (k > 0).
function avShade(hex, k) {
  const n = parseInt(hex.slice(1), 16);
  const c = [n >> 16, (n >> 8) & 255, n & 255].map(x => Math.round(k < 0 ? x * (1 + k) : x + (255 - x) * k));
  return `rgb(${c.join(',')})`;
}

// avCurve is a quadratic curve from p0 through control c to p1.
function avCurve(p0, c, p1, n = 20) {
  const out = [];
  for (let i = 0; i <= n; i++) {
    const t = i / n, u = 1 - t;
    out.push([u * u * p0[0] + 2 * u * t * c[0] + t * t * p1[0], u * u * p0[1] + 2 * u * t * c[1] + t * t * p1[1]]);
  }
  return out;
}

// A pen's small, steady wobble, so lids look drawn, not ruled.
const avWobble = pts => pts.map(([x, y], i) => [x + 0.15 * Math.sin(i * 0.31 + 1), y + 0.3 * Math.sin(i * 0.47)]);

// Where the eye sits on each shape, and how big: lower and smaller in a
// triangle, in the thick of a crescent.
const AV_EYE_AT = { triangle: [50, 54, 0.8], crescent: [38, 57, 0.7] };

// avEye is the soft eye: a tinted almond with an iris, both lids drawn in
// a dark shade of the shape. open is how far the lids are apart (0 shut,
// 1 at rest); gaze is where the iris looks, as a nudge from the centre.
function avEye(color, { x, y, k, open, gaze, lower }) {
  const W = 10.5 * k, H = 6 * k, L = [x - W, y], R = [x + W, y];
  const up = [x, y + 0.5 * H - (2.5 * H) * open], down = [x, y + 0.5 * H + 0.9 * H * Math.min(open, 1.2)];
  const n = avNum;
  const white = `M${n(L[0])} ${n(L[1])}Q${n(up[0])} ${n(up[1])} ${n(R[0])} ${n(R[1])}Q${n(down[0])} ${n(down[1])} ${n(L[0])} ${n(L[1])}Z`;
  const lo = avCurve(L, down, R);
  const lids = avRibbon(avWobble(avCurve(L, up, R)), 2.6 * k) + avRibbon(avWobble(lower ? lo : lo.slice(5, 16)), 1.1 * k);
  return { x, y, open, white, lids, iris: { x: x + gaze[0] * k, y: y + gaze[1] * k, r: 3.8 * k }, ink: avShade(color, -0.62), tint: avShade(color, 0.55) };
}

// avBlink is how open the lids are at time t: shut briefly every few
// seconds.
function avBlink(t) {
  const p = ((t % 5.5) + 5.5) % 5.5;
  return p < 0.16 ? Math.abs(1 - 2 * p / 0.16) : 1;
}

const avHas = (o, k) => Object.hasOwn(o, k);

const AV_MOONS = 4;

// avPlanet is a body riding the orbit at angle th.
function avPlanet(temper, th, t, amp, tilt, r) {
  const [x, y] = avRingPoint(temper, th, t, amp, tilt);
  return { x, y, r, front: Math.sin(th) > 0 };
}

// avGlow is a light finish at time t: how bright it is (0 to 1), the
// colours of its lit centre out to its edge, and its halo's colour. Idle
// breathes dimly, thinking is brighter, done fades, needs you flares amber.
function avGlow(state, t, color) {
  const level = {
    idle: 0.45 + 0.12 * Math.sin(t * 1.3), thinking: 0.75 + 0.15 * Math.sin(t * 3.2),
    busy: 0.65 + 0.1 * Math.sin(t * 2.2), needs: 0.95, done: 0.25,
  }[state];
  return {
    level, core: avShade(color, 0.25 + 0.5 * level), mid: avShade(color, 0.1 + 0.25 * level), edge: color,
    halo: state === 'needs' ? AV_AMBER : color,
  };
}

// avFrame is everything to draw at time t (seconds) in a state. A mini
// mark has no ring or eye, so rings: false skips working them out. Busy
// shows the planet and a moon for each other job in flight. blink: false
// keeps a still mark's eye open.
function avFrame(look, state, t, { rings = true, jobs = 3, blink = true } = {}) {
  const temper = avHas(AV_TEMPER, look.temper) ? look.temper : 'calm';
  const pace = AV_TEMPER[temper].pace;
  const s = AV_STATES.includes(state) ? state : 'idle';
  const tp = t * pace;
  let rot = 0, lift = 0, amp = 1, tilt = -16, speed = 0.5, planetR = 3.6, planetColor = null;
  if (s === 'thinking') { rot = (tp * 60) % 360; amp = 1.4; tilt += 8 * Math.sin(tp * 1.5); speed = 2.2 }
  if (s === 'busy') { lift = 1.5 * Math.sin(tp * 2.6); amp = 0.6; speed = 1.3 }
  if (s === 'needs') { speed = 0; amp = 0.3; planetR = 5 + 1.2 * Math.sin(t * 4); planetColor = AV_AMBER }
  if (s === 'done') { speed = 0.3; amp = 0.4 }
  // The planet waits at the top right while it needs you.
  const th = s === 'needs' ? -Math.PI * 0.2 : tp * speed;
  const wt = s === 'needs' ? 0 : tp;
  const n = s === 'busy' && rings ? Math.min(AV_MOONS + 1, Math.max(2, Math.floor(jobs) || 2)) : 1;
  const moons = [];
  for (let i = 1; i < n; i++) moons.push(avPlanet(temper, th + 2 * Math.PI * i / n, wt, amp, tilt, 3));
  const shape = avHas(AV_SHAPES, look.shape) ? look.shape : 'dot';
  const color = avHas(AV_COLORS, look.color) ? AV_COLORS[look.color] : AV_COLORS.clay;
  const ring = rings ? avRing(AV_RINGS.includes(look.ring) ? look.ring : 'fine', temper, tp, amp, tilt) : { back: '', front: '' };
  const planet = { ...avPlanet(temper, th, wt, amp, tilt, planetR), color: planetColor };
  let eye = null;
  if (rings && look.eye === 'soft') {
    // The eye turns with the shape, staying upright, and floats with it.
    const [ex, ey, ek] = AV_EYE_AT[shape] || [50, 45, 1], a = rot * Math.PI / 180;
    const x = 50 + (ex - 50) * Math.cos(a) - (ey - 50) * Math.sin(a);
    const y = 50 + (ex - 50) * Math.sin(a) + (ey - 50) * Math.cos(a) - lift;
    const k = 1.3 * ek;
    // It looks toward the planet, a nudge of up to 4 across and 2.4 down.
    const toward = m => {
      const dx = planet.x - x, dy = planet.y - y, l = Math.hypot(dx, dy) || 1;
      return [m * 4 * dx / l, m * 2.4 * dy / l];
    };
    const drift = Math.sin(t * 0.4);
    let open = 1, gaze = [-1.5 + 0.8 * drift, 1], lower = false;
    if (s === 'thinking') gaze = [3.6 + 0.6 * drift, -2.6];
    if (s === 'busy') gaze = toward(1);
    if (s === 'needs') { open = 1.15; gaze = toward(0.8); lower = true }
    if (s === 'done') { open = 0.55; gaze = [-0.8, 2] }
    if (blink && s !== 'needs') open *= avBlink(t);
    eye = avEye(color, { x, y, k, open, gaze, lower });
  }
  const glow = look.finish === 'light' ? avGlow(s, t, color) : null;
  return { shape: AV_SHAPES[shape], color, rot, lift, back: ring.back, front: ring.front, planet, moons, eye, glow };
}

if (typeof module !== 'undefined') module.exports = { AV_SHAPES, AV_COLORS, AV_TEMPER, AV_STATES, AV_RINGS, AV_EYES, AV_FINISHES, AV_MOONS, avRibbon, avFrame };
