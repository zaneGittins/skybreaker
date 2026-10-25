'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { vwFit, vwZoomAt, vwClamp, vwMaxZoom, vwSwipe, vwStep } = require('./viewerlib.js');

test('vwFit: contains the image, never blowing up a small one', () => {
  assert.deepEqual(vwFit(2000, 1000, 1000, 800), { w: 1000, h: 500 });
  assert.deepEqual(vwFit(1000, 2000, 1000, 800), { w: 400, h: 800 });
  assert.deepEqual(vwFit(300, 200, 1000, 800), { w: 300, h: 200 });
  assert.deepEqual(vwFit(0, 0, 1000, 800), { w: 0, h: 0 });
});

test('vwZoomAt: the point under the pointer stays put', () => {
  const v = vwZoomAt({ s: 1, x: 0, y: 0 }, 2, 100, -50, 4);
  assert.deepEqual(v, { s: 2, x: -100, y: 50 });
  // The image point under (100, -50) was (100, -50) at scale 1; it still is.
  assert.equal((100 - v.x) / v.s, 100);
  assert.equal((-50 - v.y) / v.s, -50);
});

test('vwZoomAt: held between fit and the most zoom', () => {
  assert.equal(vwZoomAt({ s: 1, x: 0, y: 0 }, 0.5, 0, 0, 4).s, 1);
  assert.equal(vwZoomAt({ s: 3, x: 0, y: 0 }, 2, 0, 0, 4).s, 4);
});

test('vwClamp: no panning past the image edges, centred when it fits', () => {
  // 500x300 shown at 2x is 1000x600 in an 800x800 box: 100 of slack each way across, none down.
  assert.deepEqual(vwClamp({ s: 2, x: 400, y: 90 }, 500, 300, 800, 800), { s: 2, x: 100, y: 0 });
  assert.deepEqual(vwClamp({ s: 2, x: -40, y: 0 }, 500, 300, 800, 800), { s: 2, x: -40, y: 0 });
  assert.deepEqual(vwClamp({ s: 1, x: 30, y: -30 }, 500, 300, 800, 800), { s: 1, x: 0, y: 0 });
});

test('vwMaxZoom: always room to see real pixels, and some', () => {
  assert.equal(vwMaxZoom(1000, 1000), 4);
  assert.equal(vwMaxZoom(4000, 500), 16);
  assert.equal(vwMaxZoom(0, 0), 4);
});

test('vwSwipe: a long or quick drag decides, a nudge does not', () => {
  const w = 400, h = 800;
  assert.equal(vwSwipe(-150, 10, 400, w, h), 'next');
  assert.equal(vwSwipe(150, -10, 400, w, h), 'prev');
  assert.equal(vwSwipe(-40, 0, 60, w, h), 'next'); // a flick
  assert.equal(vwSwipe(-30, 0, 400, w, h), null);
  assert.equal(vwSwipe(5, 160, 400, w, h), 'close');
  assert.equal(vwSwipe(0, 50, 80, w, h), 'close'); // a flick down
  assert.equal(vwSwipe(0, -200, 400, w, h), null); // up does nothing
  assert.equal(vwSwipe(0, 0, 10, w, h), null);
});

test('vwStep: moves within the gallery, stopping at its ends', () => {
  assert.equal(vwStep(0, 3, 1), 1);
  assert.equal(vwStep(2, 3, 1), 2);
  assert.equal(vwStep(0, 3, -1), 0);
  assert.equal(vwStep(1, 1, 1), 0);
});
