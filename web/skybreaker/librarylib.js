'use strict';

// The library's rules, kept apart from the DOM so they can be tested: where
// to pick up, when something counts as heard or read, what to say about it,
// and what plays next. An item's progress is seconds into audio, the page
// of a PDF, or how far through anything else, 0 to 1; its duration is the
// audio's length or the PDF's page count, as the player or reader found it.

// libResume is where audio starts: a few seconds before where you stopped,
// to catch the thread, or the start when it's done or barely begun.
function libResume(it) {
  if (it.finished || !(it.progress > 5)) return 0;
  return Math.max(0, it.progress - 3);
}

// libDone is whether t seconds into audio dur long counts as heard: 95% in,
// or into the last 30 seconds of anything over two minutes (the outro).
function libDone(t, dur) {
  if (!(dur > 0)) return false;
  return t >= dur * 0.95 || (dur >= 120 && dur - t <= 30);
}

function libClock(s) {
  s = Math.max(0, Math.floor(Number(s) || 0));
  const h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, sec = String(s % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

function libMinutes(s) {
  const m = Math.max(1, Math.round(s / 60));
  if (m < 60) return m + ' min';
  const h = Math.floor(m / 60), r = m % 60;
  return r ? `${h} h ${r} min` : `${h} h`;
}

// libStatus is a few words on where you are in an item, and its tone:
// new, going (started) or done.
function libStatus(it) {
  const p = Number(it.progress) || 0, d = Number(it.duration) || 0;
  switch (it.kind) {
    case 'audio':
      if (it.finished) return { text: 'Played', tone: 'done' };
      if (p > 0 && d) return { text: libMinutes(d - p) + ' left', tone: 'going' };
      if (p > 0) return { text: 'Started', tone: 'going' };
      return { text: d ? libMinutes(d) : 'New', tone: 'new' };
    case 'pdf':
      if (it.finished) return { text: 'Read', tone: 'done' };
      if (p >= 1) return { text: d ? `Page ${p} of ${d}` : `Page ${p}`, tone: 'going' };
      break;
    case 'text':
    case 'view':
      if (it.finished) return { text: it.kind === 'view' ? 'Done' : 'Read', tone: 'done' };
      if (p > 0) return { text: Math.round(p * 100) + (it.kind === 'view' ? '% done' : '% read'), tone: 'going' };
      break;
  }
  return it.seen ? { text: 'Opened', tone: 'done' } : { text: 'New', tone: 'new' };
}

// libFraction is how full an item's progress bar is, or null for no bar:
// nothing begun, done, or a kind with no length.
function libFraction(it) {
  if (it.finished) return null;
  const p = Number(it.progress) || 0, d = Number(it.duration) || 0;
  if (it.kind === 'audio' || it.kind === 'pdf') return p > 0 && d > 0 ? Math.min(1, p / d) : null;
  if (it.kind === 'text' || it.kind === 'view') return p > 0 ? Math.min(1, p) : null;
  return null;
}

// libChapterAt is the chapter playing at t, or -1 before the first.
function libChapterAt(chapters, t) {
  let at = -1;
  (chapters || []).forEach((c, i) => { if (c.at <= t) at = i });
  return at;
}

const libTime = it => Date.parse(it.at) || 0;

// libUpNext is what plays after an item: the unfinished audio in its
// collection, the ones newer than it first, each run oldest first.
function libUpNext(items, cur) {
  if (!cur.collection) return [];
  const rest = items.filter(i => i.id !== cur.id && i.kind === 'audio' && !i.finished && i.collection === cur.collection)
    .sort((a, b) => libTime(a) - libTime(b));
  const t = libTime(cur);
  return [...rest.filter(i => libTime(i) > t), ...rest.filter(i => libTime(i) <= t)];
}

// libPlayNew is where "Play new" starts: the oldest unfinished audio.
function libPlayNew(items, collection) {
  return items.filter(i => i.kind === 'audio' && !i.finished && i.collection === collection)
    .sort((a, b) => libTime(a) - libTime(b))[0] || null;
}

const LIB_FILTERS = {
  all: () => true,
  new: i => !i.seen,
  kept: i => i.star,
};

// libGroups is the library as the page shows it: each collection with its
// items newest first, the one with the newest item first; items in none
// make a group of their own (col null) at the end. Empty groups go.
function libGroups(items, collections, filter) {
  const keep = LIB_FILTERS[filter] || LIB_FILTERS.all;
  const groups = new Map((collections || []).map(c => [c.id, { col: c, items: [] }]));
  const loose = { col: null, items: [] };
  for (const it of items) {
    if (!keep(it)) continue;
    (groups.get(it.collection || '') || loose).items.push(it);
  }
  const newest = g => Math.max(...g.items.map(libTime));
  const out = [...groups.values()].filter(g => g.items.length);
  out.forEach(g => g.items.sort((a, b) => libTime(b) - libTime(a)));
  out.sort((a, b) => newest(b) - newest(a));
  loose.items.sort((a, b) => libTime(b) - libTime(a));
  return loose.items.length ? [...out, loose] : out;
}

const libFresh = items => items.filter(i => !i.seen).length;

// libSleepAt is the second a sleep timer stops audio at: the end of the
// chapter playing, or of the episode. Timed sleeps count down by the clock.
function libSleepAt(mode, t, dur, chapters) {
  if (mode === 'episode') return dur;
  if (mode !== 'chapter') return null;
  const next = (chapters || []).find(c => c.at > t);
  return next ? next.at : dur;
}

const LIB_SPEEDS = [0.8, 1, 1.25, 1.5, 1.75, 2];

function libNextSpeed(s) {
  return LIB_SPEEDS.find(x => x > s + 0.001) || LIB_SPEEDS[0];
}

const libSpeedLabel = s => (Math.round(s * 100) / 100) + 'x';

// libReadAnchor is the block at the top of the reader: the last whose top
// is at or above the scroll position. Reading position is kept as a block,
// not pixels, so it lands in the same place on a phone and a desktop.
function libReadAnchor(tops, scrollTop) {
  let at = -1;
  tops.forEach((t, i) => { if (t <= scrollTop + 1) at = i });
  return tops.length && at < 0 ? 0 : at;
}

if (typeof module !== 'undefined') module.exports = {
  libResume, libDone, libClock, libMinutes, libStatus, libFraction, libChapterAt, libUpNext, libPlayNew, libGroups, libFresh,
  libSleepAt, libNextSpeed, libSpeedLabel, libReadAnchor, LIB_SPEEDS,
};
