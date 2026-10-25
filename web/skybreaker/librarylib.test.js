'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const L = require('./librarylib.js');

test('libResume: a little before where you stopped, from the start when done or barely begun', () => {
  assert.equal(L.libResume({ progress: 100 }), 97);
  assert.equal(L.libResume({ progress: 4 }), 0);
  assert.equal(L.libResume({ progress: 100, finished: true }), 0);
  assert.equal(L.libResume({}), 0);
});

test('libDone: 95% in, or the last 30 seconds of a long one', () => {
  assert.equal(L.libDone(95, 100), true);
  assert.equal(L.libDone(90, 100), false);
  assert.equal(L.libDone(575, 600), true);
  assert.equal(L.libDone(560, 600), false);
  // A short clip isn't done 30 seconds from its end.
  assert.equal(L.libDone(31, 60), false);
  assert.equal(L.libDone(10, 0), false);
  assert.equal(L.libDone(10, NaN), false);
});

test('libClock', () => {
  assert.equal(L.libClock(0), '0:00');
  assert.equal(L.libClock(552.7), '9:12');
  assert.equal(L.libClock(3723), '1:02:03');
  assert.equal(L.libClock(NaN), '0:00');
  assert.equal(L.libClock(-5), '0:00');
});

test('libMinutes', () => {
  assert.equal(L.libMinutes(20), '1 min');
  assert.equal(L.libMinutes(360), '6 min');
  assert.equal(L.libMinutes(3900), '1 h 5 min');
  assert.equal(L.libMinutes(3600), '1 h');
});

test('libStatus: what to say about where you are', () => {
  assert.deepEqual(L.libStatus({ kind: 'audio', duration: 360 }), { text: '6 min', tone: 'new' });
  assert.deepEqual(L.libStatus({ kind: 'audio' }), { text: 'New', tone: 'new' });
  assert.deepEqual(L.libStatus({ kind: 'audio', seen: true, progress: 120, duration: 840 }), { text: '12 min left', tone: 'going' });
  assert.deepEqual(L.libStatus({ kind: 'audio', seen: true, finished: true, progress: 840, duration: 840 }), { text: 'Played', tone: 'done' });
  assert.deepEqual(L.libStatus({ kind: 'pdf', seen: true, progress: 3, duration: 8 }), { text: 'Page 3 of 8', tone: 'going' });
  assert.deepEqual(L.libStatus({ kind: 'pdf', seen: true, progress: 3 }), { text: 'Page 3', tone: 'going' });
  assert.deepEqual(L.libStatus({ kind: 'pdf', seen: true, finished: true }), { text: 'Read', tone: 'done' });
  assert.deepEqual(L.libStatus({ kind: 'text', seen: true, progress: 0.4 }), { text: '40% read', tone: 'going' });
  assert.deepEqual(L.libStatus({ kind: 'view', seen: true, progress: 0.58 }), { text: '58% done', tone: 'going' });
  assert.deepEqual(L.libStatus({ kind: 'view', seen: true, finished: true }), { text: 'Done', tone: 'done' });
  assert.deepEqual(L.libStatus({ kind: 'file', seen: true }), { text: 'Opened', tone: 'done' });
  assert.deepEqual(L.libStatus({ kind: 'image' }), { text: 'New', tone: 'new' });
});

test('libFraction: how much of the bar is filled, or null for none', () => {
  assert.equal(L.libFraction({ kind: 'audio', progress: 90, duration: 360 }), 0.25);
  assert.equal(L.libFraction({ kind: 'audio', progress: 90 }), null);
  assert.equal(L.libFraction({ kind: 'audio', finished: true, progress: 360, duration: 360 }), null);
  assert.equal(L.libFraction({ kind: 'pdf', progress: 2, duration: 8 }), 0.25);
  assert.equal(L.libFraction({ kind: 'text', progress: 0.5 }), 0.5);
  assert.equal(L.libFraction({ kind: 'text' }), null);
  assert.equal(L.libFraction({ kind: 'file', progress: 1 }), null);
});

test('libChapterAt', () => {
  const ch = [{ at: 0, title: 'a' }, { at: 60, title: 'b' }, { at: 120, title: 'c' }];
  assert.equal(L.libChapterAt(ch, 0), 0);
  assert.equal(L.libChapterAt(ch, 59.9), 0);
  assert.equal(L.libChapterAt(ch, 60), 1);
  assert.equal(L.libChapterAt(ch, 500), 2);
  assert.equal(L.libChapterAt([{ at: 10, title: 'late' }], 5), -1);
  assert.equal(L.libChapterAt(undefined, 5), -1);
});

const items = [
  { id: 'a', kind: 'audio', collection: 'daily', at: '2026-10-22T07:00:00Z', seen: true, finished: true },
  { id: 'b', kind: 'audio', collection: 'daily', at: '2026-10-23T07:00:00Z' },
  { id: 'c', kind: 'audio', collection: 'daily', at: '2026-10-24T07:00:00Z', seen: true, progress: 30 },
  { id: 'd', kind: 'audio', collection: 'daily', at: '2026-10-25T07:00:00Z' },
  { id: 'e', kind: 'pdf', collection: 'daily', at: '2026-10-25T08:00:00Z' },
  { id: 'f', kind: 'audio', collection: 'weekly', at: '2026-10-25T09:00:00Z' },
];

test('libUpNext: the unfinished audio in its collection, newer ones after it first, oldest first', () => {
  assert.deepEqual(L.libUpNext(items, items[2]).map(i => i.id), ['d', 'b']);
  assert.deepEqual(L.libUpNext(items, items[1]).map(i => i.id), ['c', 'd']);
  assert.deepEqual(L.libUpNext(items, items[5]).map(i => i.id), []);
});

test('libPlayNew: the oldest unfinished audio in a collection', () => {
  assert.equal(L.libPlayNew(items, 'daily').id, 'b');
  assert.equal(L.libPlayNew(items, 'none'), null);
});

test('libGroups: collections by their newest item, loose items in their own group, filtered', () => {
  const cols = [{ id: 'daily', title: 'Daily' }, { id: 'weekly', title: 'Weekly' }, { id: 'empty', title: 'Empty' }];
  const all = [...items, { id: 'g', kind: 'file', at: '2026-10-21T00:00:00Z', seen: true, star: true }];
  const g = L.libGroups(all, cols, 'all');
  assert.deepEqual(g.map(x => x.col ? x.col.id : null), ['weekly', 'daily', null]);
  // Each group newest first.
  assert.deepEqual(g[1].items.map(i => i.id), ['e', 'd', 'c', 'b', 'a']);
  assert.deepEqual(L.libGroups(all, cols, 'new').map(x => x.items.map(i => i.id)), [['f'], ['e', 'd', 'b']]);
  assert.deepEqual(L.libGroups(all, cols, 'kept').map(x => x.items.map(i => i.id)), [['g']]);
});

test('libFresh counts what is not yet opened', () => {
  assert.equal(L.libFresh(items), 4);
  assert.equal(L.libFresh([]), 0);
});

test('libSleepAt: where a sleep timer stops', () => {
  const ch = [{ at: 0, title: 'a' }, { at: 60, title: 'b' }];
  assert.equal(L.libSleepAt('chapter', 30, 200, ch), 60);
  assert.equal(L.libSleepAt('chapter', 90, 200, ch), 200);
  assert.equal(L.libSleepAt('chapter', 30, 200, []), 200);
  assert.equal(L.libSleepAt('episode', 30, 200, ch), 200);
  assert.equal(L.libSleepAt('', 30, 200, ch), null);
});

test('libNextSpeed cycles through the speeds', () => {
  assert.equal(L.libNextSpeed(1), 1.25);
  assert.equal(L.libNextSpeed(2), 0.8);
  assert.equal(L.libNextSpeed(1.1), 1.25);
  assert.equal(L.libSpeedLabel(1.25), '1.25x');
  assert.equal(L.libSpeedLabel(1), '1x');
});

test('libReadAnchor: the block at the top of what is in view', () => {
  const tops = [0, 120, 400, 900];
  assert.equal(L.libReadAnchor(tops, 0), 0);
  assert.equal(L.libReadAnchor(tops, 130), 1);
  assert.equal(L.libReadAnchor(tops, 898), 2);
  assert.equal(L.libReadAnchor(tops, 5000), 3);
  assert.equal(L.libReadAnchor([], 50), -1);
});
