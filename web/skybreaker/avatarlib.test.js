'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { AV_SHAPES, AV_COLORS, AV_TEMPER, AV_STATES, avRibbon, avFrame } = require('./avatarlib.js');

test('the six shapes and eight colours the server allows', () => {
  assert.deepEqual(Object.keys(AV_SHAPES), ['dot', 'square', 'triangle', 'diamond', 'hexagon', 'crescent']);
  assert.deepEqual(Object.keys(AV_COLORS), ['clay', 'sage', 'ink', 'ochre', 'rose', 'slate', 'moss', 'plum']);
  assert.deepEqual(Object.keys(AV_TEMPER), ['calm', 'lively', 'precise']);
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
