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
const AV_AMBER = '#E8B04A';

// avRingPoint is the orbit at angle th: an ellipse that wobbles a little,
// tilted about the centre.
function avRingPoint(temper, th, t, amp, tilt) {
  const T = AV_TEMPER[temper] || AV_TEMPER.calm;
  const w = 1 + amp * T.wobble * Math.sin(th * T.lobes + t * 1.6);
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

// avRingHalf is the back (behind the shape) or front half of the orbit.
function avRingHalf(temper, t, amp, tilt, front) {
  const pts = [];
  for (let i = 0; i <= 48; i++) pts.push(avRingPoint(temper, (front ? 0 : Math.PI) + Math.PI * i / 48, t, amp, tilt));
  return avRibbon(pts, 3.2);
}

const avHas = (o, k) => Object.hasOwn(o, k);

// avFrame is everything to draw at time t (seconds) in a state. A mini
// mark has no ring, so rings: false skips working it out.
function avFrame(look, state, t, { rings = true } = {}) {
  const temper = avHas(AV_TEMPER, look.temper) ? look.temper : 'calm';
  const pace = AV_TEMPER[temper].pace;
  const s = AV_STATES.includes(state) ? state : 'idle';
  const tp = t * pace;
  let rot = 0, split = 0, amp = 1, tilt = -16, speed = 0.5, planetR = 3.6, planetColor = null;
  if (s === 'thinking') { rot = (tp * 60) % 360; amp = 1.4; tilt += 8 * Math.sin(tp * 1.5); speed = 2.2 }
  if (s === 'busy') { split = 5 * (0.5 - 0.5 * Math.cos(tp * 2.2)); amp = 0.6; speed = 1.2 }
  if (s === 'needs') { speed = 0; amp = 0.3; planetR = 5 + 1.2 * Math.sin(t * 4); planetColor = AV_AMBER }
  if (s === 'done') { speed = 0.3; amp = 0.4 }
  // The planet waits at the top right while it needs you.
  const th = s === 'needs' ? -Math.PI * 0.2 : tp * speed;
  const [px, py] = avRingPoint(temper, th, s === 'needs' ? 0 : tp, amp, tilt);
  return {
    shape: avHas(AV_SHAPES, look.shape) ? AV_SHAPES[look.shape] : AV_SHAPES.dot,
    color: avHas(AV_COLORS, look.color) ? AV_COLORS[look.color] : AV_COLORS.clay,
    rot, split,
    back: rings ? avRingHalf(temper, tp, amp, tilt, false) : '',
    front: rings ? avRingHalf(temper, tp, amp, tilt, true) : '',
    planet: { x: px, y: py, r: planetR, color: planetColor, front: Math.sin(th) > 0 },
  };
}

if (typeof module !== 'undefined') module.exports = { AV_SHAPES, AV_COLORS, AV_TEMPER, AV_STATES, avRibbon, avFrame };
