'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { AV_SHAPES, AV_COLORS, AV_TEMPER, AV_STATES, AV_RINGS, AV_EYES, AV_FINISHES, AV_HATS, AV_EXTRAS, avRibbon, avFrame } = require('./avatarlib.js');

test('the six shapes and eight colours the server allows', () => {
  assert.deepEqual(Object.keys(AV_SHAPES), ['dot', 'square', 'triangle', 'diamond', 'hexagon', 'crescent']);
  assert.deepEqual(Object.keys(AV_COLORS), ['clay', 'sage', 'ink', 'ochre', 'rose', 'slate', 'moss', 'plum']);
  assert.deepEqual(Object.keys(AV_TEMPER), ['calm', 'lively', 'precise']);
  assert.deepEqual(AV_RINGS, ['fine', 'double', 'dotted', 'arc', 'none']);
  assert.deepEqual(AV_EYES, ['none', 'soft', 'lashes']);
  assert.deepEqual(AV_FINISHES, ['paper', 'light']);
  assert.deepEqual(AV_HATS, ['none', 'beanie', 'bowler', 'wizard', 'crown', 'beret', 'party', 'tophat', 'bow', 'cap']);
  assert.deepEqual(AV_EXTRAS, ['none', 'glasses', 'monocle', 'bowtie', 'headphones', 'mustache']);
});

test('every frame is drawable', () => {
  const finite = s => !/NaN|Infinity/.test(s);
  for (const shape of Object.keys(AV_SHAPES)) for (const temper of Object.keys(AV_TEMPER)) for (const state of AV_STATES) {
    for (const t of [0, 0.37, 12.5, 999]) {
      const f = avFrame({ shape, color: 'ink', temper }, state, t);
      assert.ok(finite(f.back) && finite(f.front) && f.back.startsWith('M') && f.front.endsWith('Z'), `${shape} ${temper} ${state}`);
      assert.ok(Number.isFinite(f.planet.x) && Number.isFinite(f.planet.y) && f.planet.r > 0);
      assert.ok(Number.isFinite(f.rot) && Number.isFinite(f.lift));
      assert.ok(f.moons.every(m => Number.isFinite(m.x) && Number.isFinite(m.y) && m.r > 0));
    }
  }
});

test('states read by motion', () => {
  const look = { shape: 'square', color: 'clay', temper: 'calm' };
  const at = (s, t) => avFrame(look, s, t);
  // Idle: the planet orbits, the shape stays put.
  assert.notEqual(at('idle', 0).planet.x, at('idle', 3).planet.x);
  assert.equal(at('idle', 3).rot, 0);
  // Thinking turns the shape; busy floats it with moons on the orbit.
  assert.notEqual(at('thinking', 1).rot, 0);
  assert.notEqual(at('busy', 1).lift, 0);
  assert.equal(at('idle', 1).moons.length, 0);
  // Needs you: the planet stops, amber, where you'll look for it.
  assert.equal(at('needs', 1).planet.color, '#E8B04A');
  assert.equal(at('needs', 1).planet.x, at('needs', 7).planet.x);
  assert.equal(at('idle', 1).planet.color, null);
});

test('busy has a moon for each job, the planet among them', () => {
  const look = { shape: 'dot', color: 'sage', temper: 'calm' };
  const f = n => avFrame(look, 'busy', 2, { jobs: n });
  assert.equal(f(3).moons.length, 2);
  assert.equal(f(undefined).moons.length, 2);
  assert.equal(f(1).moons.length, 1);
  assert.equal(f(99).moons.length, 4);
  // Spread evenly around the orbit, so no two overlap.
  const [a, b] = f(3).moons;
  assert.ok(Math.hypot(a.x - f(3).planet.x, a.y - f(3).planet.y) > 10);
  assert.ok(Math.hypot(a.x - b.x, a.y - b.y) > 10);
  assert.equal(avFrame(look, 'busy', 2, { jobs: 3, rings: false }).moons.length, 0);
});

test('unknown looks fall back rather than break', () => {
  const f = avFrame({ shape: 'pill', color: '#ff0000', temper: 'grumpy' }, 'dancing', 1);
  assert.equal(f.shape, AV_SHAPES.dot);
  assert.equal(f.color, AV_COLORS.clay);
  assert.equal(f.rot, 0);
});

test('a prototype key is no shape, colour or temper', () => {
  const f = avFrame({ shape: 'constructor', color: 'toString', temper: '__proto__' }, 'idle', 1);
  assert.equal(f.shape, AV_SHAPES.dot);
  assert.equal(f.color, AV_COLORS.clay);
  assert.ok(!/NaN/.test(f.back));
});

test('a mini mark skips its ring', () => {
  const f = avFrame({ shape: 'dot', color: 'ink', temper: 'calm' }, 'thinking', 1, { rings: false });
  assert.equal(f.back, '');
  assert.equal(f.front, '');
});

test('a ribbon tapers to both ends', () => {
  const d = avRibbon([[0, 0], [5, 0], [10, 0]], 4);
  assert.match(d, /^M0 0\.24L5 2L10 0\.24L10 -0\.24L5 -2L0 -0\.24Z$/);
});

const subpaths = d => (d.match(/M/g) || []).length;

test('every ring style is drawable in every state', () => {
  const finite = s => !/NaN|Infinity|undefined/.test(s);
  for (const ring of AV_RINGS) for (const state of AV_STATES) for (const t of [0, 0.37, 12.5]) {
    const f = avFrame({ shape: 'hexagon', color: 'ink', temper: 'lively', ring }, state, t);
    for (const d of [f.back, f.front]) assert.ok(d === '' || (d.startsWith('M') && d.endsWith('Z') && finite(d)), `${ring} ${state}`);
    assert.ok(Number.isFinite(f.planet.x) && Number.isFinite(f.planet.y));
  }
});

test('ring styles: double, dotted, arc and none', () => {
  const at = ring => avFrame({ shape: 'dot', color: 'ink', temper: 'calm', ring }, 'idle', 1);
  assert.equal(subpaths(at('fine').front), 1);
  assert.equal(subpaths(at('double').back), 2);
  assert.equal(subpaths(at('double').front), 2);
  assert.ok(subpaths(at('dotted').back) > 10 && subpaths(at('dotted').front) > 10);
  // An arc is only the part in front; with none the planet keeps its path.
  assert.equal(at('arc').back, '');
  assert.equal(subpaths(at('arc').front), 1);
  assert.equal(at('none').back + at('none').front, '');
  assert.deepEqual(at('none').planet, at('fine').planet);
  // A ring it doesn't know is the fine one.
  assert.equal(at('saturn').front, at('fine').front);
});

const soft = { shape: 'dot', color: 'sage', temper: 'calm', eye: 'soft' };
const eyeAt = (state, t = 1, opts = {}) => avFrame(soft, state, t, { blink: false, ...opts }).eye;
// gaze is where the iris sits from the eye's centre.
const gaze = e => [e.iris.x - e.x, e.iris.y - e.y];

test('no eye unless asked, and none on a mini mark', () => {
  assert.equal(avFrame({ ...soft, eye: 'none' }, 'idle', 1).eye, null);
  assert.equal(avFrame({ ...soft, eye: 'googly' }, 'idle', 1).eye, null);
  assert.equal(avFrame(soft, 'idle', 1, { rings: false }).eye, null);
  for (const shape of Object.keys(AV_SHAPES)) for (const state of AV_STATES) {
    const e = avFrame({ ...soft, shape }, state, 2.3).eye;
    assert.ok(Number.isFinite(e.x + e.y + e.open + e.iris.x + e.iris.y + e.iris.r), `${shape} ${state}`);
    assert.ok(!/NaN|Infinity/.test(e.white + e.lids) && e.white.startsWith('M') && e.lids.startsWith('M'));
    assert.match(e.ink, /^rgb\(/);
    assert.match(e.tint, /^rgb\(/);
  }
});

test('the eye tells the state', () => {
  // Idle rests a little down and away; thinking glances up.
  assert.ok(gaze(eyeAt('idle'))[1] > 0);
  assert.ok(gaze(eyeAt('thinking'))[1] < 0);
  // Busy follows the planet round; needs you looks at it, wide open.
  for (const t of [0.5, 2, 4.5]) {
    const f = avFrame(soft, 'busy', t, { blink: false });
    const [gx, gy] = gaze(f.eye);
    assert.ok(gx * (f.planet.x - f.eye.x) + gy * (f.planet.y - f.eye.y) > 0, `busy at ${t}`);
  }
  const n = avFrame(soft, 'needs', 1, { blink: false });
  const [nx, ny] = gaze(n.eye);
  assert.ok(nx * (n.planet.x - n.eye.x) + ny * (n.planet.y - n.eye.y) > 0);
  assert.ok(n.eye.open > eyeAt('idle').open);
  // Done: the lid drops, at ease.
  assert.ok(eyeAt('done').open < 0.6);
});

test('it blinks now and then, never on a still mark', () => {
  const opens = blink => Array.from({ length: 600 }, (_, i) => avFrame(soft, 'idle', i * 0.02, { blink }).eye.open);
  assert.ok(Math.min(...opens(true)) < 0.2);
  assert.ok(Math.min(...opens(false)) >= 0.9);
});

test('the eye rides the shape as it turns, upright', () => {
  const look = { ...soft, shape: 'crescent' };
  const c = t => avFrame(look, 'thinking', t, { blink: false }).eye;
  const r = e => Math.hypot(e.x - 50, e.y - 50);
  assert.ok(Math.abs(r(c(0.3)) - r(c(2.1))) < 0.01);
  assert.notEqual(c(0.3).x, c(2.1).x);
});

const lit = { shape: 'dot', color: 'ink', temper: 'calm', finish: 'light' };
const glowAt = (state, t) => avFrame(lit, state, t).glow;
const levels = state => Array.from({ length: 80 }, (_, i) => glowAt(state, i * 0.1).level);

test('paper has no glow; light glows', () => {
  assert.equal(avFrame({ ...lit, finish: 'paper' }, 'idle', 1).glow, null);
  assert.equal(avFrame({ ...lit, finish: 'neon' }, 'idle', 1).glow, null);
  for (const state of AV_STATES) for (const color of Object.keys(AV_COLORS)) {
    const g = avFrame({ ...lit, color }, state, 1.7).glow;
    assert.ok(g.level > 0 && g.level <= 1, `${state} ${color}`);
    assert.match(g.core, /^rgb\(/);
    assert.match(g.mid, /^rgb\(/);
    assert.equal(g.edge, AV_COLORS[color]);
  }
});

test('how bright it glows tells the state', () => {
  // Idle breathes, dim; thinking is always brighter; done fades below idle.
  assert.ok(Math.max(...levels('idle')) - Math.min(...levels('idle')) > 0.1);
  assert.ok(Math.min(...levels('thinking')) > Math.max(...levels('idle')));
  assert.ok(Math.max(...levels('done')) < Math.min(...levels('idle')));
  // Needs you flares amber, the brightest of all; otherwise the halo is its colour.
  assert.equal(glowAt('needs', 1).halo, '#E8B04A');
  assert.equal(glowAt('idle', 1).halo, AV_COLORS.ink);
  assert.ok(glowAt('needs', 1).level >= Math.max(...levels('thinking')));
});

const dressed = { shape: 'dot', color: 'plum', temper: 'calm' };
const wear = (look, opts) => avFrame({ ...dressed, ...look }, 'idle', 1, opts).wear;

test('every hat and extra is drawable on every shape', () => {
  for (const shape of Object.keys(AV_SHAPES)) for (const hat of AV_HATS) for (const extra of AV_EXTRAS) {
    const w = wear({ shape, hat, extra });
    for (const [tag, attrs] of w) {
      assert.match(tag, /^(path|circle|rect)$/);
      assert.ok(!/NaN|Infinity|undefined/.test(JSON.stringify(attrs)), `${shape} ${hat} ${extra}`);
    }
    if (hat === 'none' && extra === 'none') assert.equal(w.length, 0);
    if (hat !== 'none') assert.ok(w.length > 0, `${shape} ${hat}`);
  }
});

test('nothing to wear unless asked, and none on a mini mark', () => {
  assert.deepEqual(wear({}), []);
  assert.deepEqual(wear({ hat: 'fez', extra: 'cape' }), []);
  assert.deepEqual(wear({ hat: 'constructor', extra: '__proto__' }), []);
  assert.deepEqual(wear({ hat: 'wizard', extra: 'glasses' }, { rings: false }), []);
});

test('glasses take the eye\'s place; a monocle sits over it', () => {
  const soft = { eye: 'soft', hat: 'none' };
  assert.equal(avFrame({ ...dressed, ...soft, extra: 'glasses' }, 'idle', 1).eye, null);
  const f = avFrame({ ...dressed, ...soft, shape: 'triangle', extra: 'monocle' }, 'idle', 1, { blink: false });
  const lens = f.wear.find(([tag]) => tag === 'circle');
  assert.match(lens[1].transform, new RegExp(`translate\\(${f.eye.x} ${f.eye.y}\\)`));
});

test('headphones sit out a triangle, which has no sides for them', () => {
  assert.deepEqual(wear({ shape: 'triangle', extra: 'headphones' }), []);
  assert.ok(wear({ shape: 'hexagon', extra: 'headphones' }).length > 0);
});

test('lashes are the soft eye with lashes on its upper lid', () => {
  const lashed = avFrame({ ...soft, eye: 'lashes' }, 'idle', 1, { blink: false }).eye;
  const plain = eyeAt('idle');
  assert.equal(lashed.white, plain.white);
  assert.equal(subpaths(lashed.lids), subpaths(plain.lids) + 4);
  assert.equal(avFrame({ ...soft, eye: 'lashes', extra: 'glasses' }, 'idle', 1).eye, null);
});

test('a mustache sits under where glasses go', () => {
  const at = (shape, extra) => wear({ shape, extra })[0][1].transform.match(/translate\(([\d.]+) ([\d.]+)\)/).slice(1).map(Number);
  for (const shape of Object.keys(AV_SHAPES)) {
    const [mx, my] = at(shape, 'mustache'), [gx, gy] = at(shape, 'glasses');
    assert.ok(mx === gx && my > gy + 4, shape);
  }
});
