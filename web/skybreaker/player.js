'use strict';

// The player: one audio element for the whole app, so what's playing
// carries on as you move between pages. A mini player sits at the bottom;
// it opens the full one, with chapters, transcript and what's up next. The
// lock screen and headphones drive it through the Media Session API. Where
// you are is saved every few seconds and as the page goes, and the last
// thing playing comes back, paused, when the app opens again.

const PL_SAVE_EVERY = 10000;
const PL_STORE = 'skybreaker.player';

const pl = {
  audio: null,
  cur: null, // { name, it, col, look }
  queue: [],
  bar: null,
  full: null, // the full player's layer, while it's open
  saved: 0,
  sentDone: false,
  sleep: null, // { mode, until } : a clock time, or 'chapter' or 'episode'
  want: 0, // where to start once the audio knows its length
  dragging: false, // the seek bar is held
  seekTimer: 0,
};

const plIs = (name, id) => !!pl.cur && pl.cur.name === name && pl.cur.it.id === id;
const plPlaying = () => !!pl.audio && !pl.audio.paused;
const plDuration = () => (pl.audio && Number.isFinite(pl.audio.duration) && pl.audio.duration) || pl.cur?.it.duration || 0;
const plTime = () => (pl.audio?.readyState >= 1 ? pl.audio.currentTime : pl.want) || 0;
const plFraction = () => plDuration() ? Math.min(1, plTime() / plDuration()) : null;

function plElement() {
  if (pl.audio) return pl.audio;
  const a = new Audio();
  a.preload = 'metadata';
  a.addEventListener('loadedmetadata', () => {
    if (pl.want && Math.abs(a.currentTime - pl.want) > 1) a.currentTime = pl.want;
    pl.want = 0;
    const d = a.duration;
    if (pl.cur && Number.isFinite(d) && d > 0 && Math.abs((pl.cur.it.duration || 0) - d) > 1) {
      pl.cur.it.duration = d;
      libSave(pl.cur.name, pl.cur.it.id, { duration: d });
    }
    plDraw();
  });
  a.addEventListener('timeupdate', plTick);
  a.addEventListener('play', () => { plDraw(); plMediaState() });
  a.addEventListener('pause', () => { plSave(); plDraw(); plMediaState() });
  // A drag seeks many times: save once it settles.
  a.addEventListener('seeked', () => { plDraw(); clearTimeout(pl.seekTimer); pl.seekTimer = setTimeout(() => { if (!pl.dragging) plSave() }, 800) });
  a.addEventListener('ratechange', plMediaState);
  a.addEventListener('ended', plEnded);
  a.addEventListener('error', () => { if (pl.cur && a.src) toast('Couldn’t play ' + pl.cur.it.title, 'error') });
  pl.audio = a;
  return a;
}

// plSpeed is the speed you last chose for a collection, or what the agent
// set for it.
const plSpeedKey = (name, col) => 'skybreaker.speed.' + name + '/' + (col?.id || '');
const plSpeed = (name, col) => Number(localStorage[plSpeedKey(name, col)]) || col?.speed || 1;

// plLoad makes an item the current one, at where you left it.
function plLoad(name, it, col, look, at) {
  if (pl.cur && !plIs(name, it.id)) plSave(true);
  const a = plElement();
  pl.cur = { name, it: { ...it }, col, look };
  pl.sentDone = !!it.finished;
  // A timed sleep runs on across episodes; one tied to this episode goes.
  if (!pl.sleep?.until) pl.sleep = null;
  pl.want = at ?? libResume(it);
  a.src = libURL(name, it.kept) + (pl.want ? '#t=' + Math.floor(pl.want) : '');
  a.playbackRate = plSpeed(name, col);
  plQueue();
  plRemember();
  plMedia();
  plDraw();
}

// plQueue is what plays after: the rest of the collection, unheard.
async function plQueue() {
  const c = pl.cur;
  if (!c?.it.collection) { pl.queue = []; return }
  try {
    const d = await api('GET', asstAPI(c.name) + '/library');
    libRemember(c.name, d.items);
    if (pl.cur === c) pl.queue = libUpNext(d.items, c.it);
  } catch { }
}

async function plPlay(name, it, col, look) {
  if (!plIs(name, it.id)) plLoad(name, it, col, look);
  try { await plElement().play() } catch (e) { if (e.name !== 'AbortError') toast('Couldn’t play: ' + e.message, 'error') }
  if (!it.seen && pl.cur) libSave(name, it.id, { progress: Math.max(plTime(), 0.1) });
}

function plToggle(name, it, col, look) {
  if (plIs(name, it.id) && plPlaying()) pl.audio.pause();
  else plPlay(name, it, col, look);
}

// plOpen shows an item in the full player, loading it if it isn't playing.
function plOpen(name, it, col, look) {
  if (!plIs(name, it.id)) plLoad(name, it, col, look);
  plFull();
}

function plSeek(t) {
  const a = pl.audio;
  if (!a) return;
  t = Math.max(0, Math.min(t, plDuration() || t));
  // Before the audio knows its length, a seek is where it'll start.
  if (a.readyState < 1) { pl.want = t; plDrawTime(); return }
  a.currentTime = t;
}
const plSkip = d => plSeek(plTime() + d);

function plTick() {
  const c = pl.cur;
  if (!c) return;
  const t = plTime(), d = plDuration();
  // Only listening counts as heard: dragging past the end doesn't.
  if (!pl.sentDone && plPlaying() && !pl.dragging && libDone(t, d)) {
    pl.sentDone = true;
    c.it.finished = true;
    libSave(c.name, c.it.id, { progress: t, finished: true });
  }
  if (Date.now() - pl.saved > PL_SAVE_EVERY && plPlaying()) plSave();
  // A sleep at the episode's end is plEnded's: it stops what plays next.
  const s = pl.sleep;
  if (s && ((s.until && Date.now() >= s.until) || (s.at != null && s.at < d - 0.5 && t >= s.at))) {
    pl.sleep = null;
    pl.audio.pause();
  }
  plDrawTime();
}

// plSave keeps where you are, on the server, so another device picks up
// there, and here, so a reload does.
function plSave(keepalive = false) {
  const c = pl.cur;
  if (!c || !pl.audio) return;
  const t = plTime();
  pl.saved = Date.now();
  plRemember();
  if (Math.abs(t - (c.it.progress || 0)) < 0.5) return;
  // Kept as saved only once it is: a failed save is tried again.
  libSave(c.name, c.it.id, { progress: t }, keepalive).then(x => { if (x && pl.cur === c) c.it.progress = x.progress });
}

function plRemember() {
  const c = pl.cur;
  if (c) localStorage[PL_STORE] = JSON.stringify({ name: c.name, id: c.it.id, t: plTime() || pl.want || 0 });
  else delete localStorage[PL_STORE];
}

addEventListener('pagehide', () => plSave(true));
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') plSave(true) });

function plEnded() {
  const c = pl.cur;
  if (!c) return;
  if (!pl.sentDone) { pl.sentDone = true; c.it.finished = true }
  libSave(c.name, c.it.id, { progress: plDuration(), finished: true });
  // A sleep set for this episode's end, or its last chapter's, stops here.
  const stop = pl.sleep && !pl.sleep.until;
  if (stop) pl.sleep = null;
  const next = stop ? null : pl.queue[0];
  if (next) plPlay(c.name, next, c.col, c.look);
  else plDraw();
}

function plNext() {
  const c = pl.cur;
  if (!c) return;
  const ch = c.it.chapters || [];
  const later = ch.find(x => x.at > plTime() + 1);
  if (later) return plSeek(later.at);
  if (pl.queue[0]) plPlay(c.name, pl.queue[0], c.col, c.look);
}

function plPrev() {
  const ch = pl.cur?.it.chapters || [];
  const i = libChapterAt(ch, plTime());
  // Within a few seconds of a chapter's start, go to the one before.
  if (i >= 0 && plTime() - ch[i].at < 4 && i > 0) return plSeek(ch[i - 1].at);
  plSeek(i >= 0 ? ch[i].at : 0);
}

function plClose() {
  plSave(true);
  pl.audio?.pause();
  pl.audio?.removeAttribute('src');
  pl.audio?.load();
  pl.cur = null;
  pl.queue = [];
  pl.sleep = null;
  pl.full?.close(true);
  plRemember();
  plDraw();
  if ('mediaSession' in navigator) navigator.mediaSession.metadata = null;
}

function plSetSpeed(r) {
  const c = pl.cur;
  if (!c) return;
  pl.audio.playbackRate = r;
  localStorage[plSpeedKey(c.name, c.col)] = String(r);
  plDraw();
}

function plSetSleep(mode) {
  if (!mode) pl.sleep = null;
  else if (typeof mode === 'number') pl.sleep = { mode: mode + ' min', until: Date.now() + mode * 60000 };
  else pl.sleep = { mode, at: libSleepAt(mode, plTime(), plDuration(), pl.cur?.it.chapters) };
  plDraw();
}

const plSleepLabel = () => {
  const s = pl.sleep;
  if (!s) return 'Sleep';
  if (s.until) return Math.max(1, Math.ceil((s.until - Date.now()) / 60000)) + ' min';
  return s.mode === 'chapter' ? 'End of chapter' : 'End of episode';
};

// --- lock screen ----------------------------------------------------------------

// plArt is the lock screen's artwork: the cover, or one drawn like the
// app's, in the assistant's colour with the date.
function plArt(c) {
  const kept = c.it.cover || c.col?.cover;
  if (kept) return [{ src: new URL(libURL(c.name, kept), location.href).href, sizes: '512x512' }];
  const cv = document.createElement('canvas');
  cv.width = cv.height = 512;
  const g = cv.getContext('2d');
  g.fillStyle = AV_COLORS[c.look?.color] || AV_COLORS.clay;
  g.fillRect(0, 0, 512, 512);
  const d = new Date(c.it.at || Date.now());
  g.fillStyle = 'rgba(255,255,255,.92)';
  g.font = '700 46px system-ui, sans-serif';
  g.fillText(LIB_MONTHS[d.getMonth()], 56, 330);
  g.font = '800 170px system-ui, sans-serif';
  g.fillText(String(d.getDate()), 48, 470);
  g.strokeStyle = 'rgba(255,255,255,.85)';
  g.lineWidth = 10;
  g.beginPath();
  g.ellipse(390, 120, 80, 26, -0.3, 0, Math.PI * 2);
  g.stroke();
  g.beginPath();
  g.arc(390, 120, 34, 0, Math.PI * 2);
  g.fill();
  return [{ src: cv.toDataURL('image/png'), sizes: '512x512', type: 'image/png' }];
}

function plMedia() {
  if (!('mediaSession' in navigator) || !pl.cur) return;
  const c = pl.cur, ms = navigator.mediaSession;
  ms.metadata = new MediaMetadata({ title: c.it.title, artist: c.look?.name || c.name, album: c.col?.title || '', artwork: plArt(c) });
  const on = (action, fn) => { try { ms.setActionHandler(action, fn) } catch { } };
  on('play', () => pl.audio.play());
  on('pause', () => pl.audio.pause());
  on('seekbackward', e => plSkip(-(e.seekOffset || 15)));
  on('seekforward', e => plSkip(e.seekOffset || 30));
  on('seekto', e => plSeek(e.seekTime));
  on('previoustrack', plPrev);
  on('nexttrack', plNext);
}

function plMediaState() {
  if (!('mediaSession' in navigator) || !pl.cur) return;
  navigator.mediaSession.playbackState = plPlaying() ? 'playing' : 'paused';
  const d = plDuration();
  if (d) try { navigator.mediaSession.setPositionState({ duration: d, playbackRate: pl.audio.playbackRate, position: Math.min(plTime(), d) }) } catch { }
}

// --- drawing --------------------------------------------------------------------

const plChapterTitle = c => {
  const ch = c.it.chapters || [];
  const i = libChapterAt(ch, plTime());
  return i >= 0 ? `Chapter ${i + 1} · ${ch[i].title}` : (c.col?.title || c.look?.name || '');
};

// plScrub is a seek bar with chapter marks: drag or tap it, or use the
// arrow keys.
function plScrub() {
  const fill = el('i'), knob = el('b'), marks = el('span.pl-marks');
  const track = el('div.pl-track', { role: 'slider', tabindex: '0', 'aria-label': 'Position' }, fill, marks, knob);
  const now = el('span.pl-now'), left = el('span.pl-left');
  const at = e => {
    const r = track.getBoundingClientRect();
    return Math.max(0, Math.min(1, (e.clientX - r.left) / r.width)) * plDuration();
  };
  let drag = false;
  track.addEventListener('pointerdown', e => { drag = pl.dragging = true; track.setPointerCapture(e.pointerId); plSeek(at(e)) });
  track.addEventListener('pointermove', e => { if (drag) plSeek(at(e)) });
  const end = () => { if (!drag) return; drag = pl.dragging = false; plSave() };
  track.addEventListener('pointerup', end);
  track.addEventListener('pointercancel', end);
  track.addEventListener('keydown', e => {
    const d = { ArrowLeft: -15, ArrowRight: 30, Home: -1e9, End: 1e9 }[e.key];
    if (d != null) { e.preventDefault(); plSkip(d) }
  });
  const box = el('div.pl-scrub', null, now, track, left);
  box.draw = () => {
    const d = plDuration(), t = plTime(), f = d ? Math.min(1, t / d) : 0;
    fill.style.width = knob.style.left = f * 100 + '%';
    now.textContent = libClock(t);
    left.textContent = d ? '-' + libClock(d - t) : '';
    track.setAttribute('aria-valuetext', libClock(t) + (d ? ' of ' + libClock(d) : ''));
    const ch = pl.cur?.it.chapters || [];
    const key = d + ':' + ch.length;
    if (marks.dataset.key !== key) {
      marks.dataset.key = key;
      marks.replaceChildren(...(d ? ch.filter(x => x.at > 0 && x.at < d).map(x => { const s = el('s'); s.style.left = x.at / d * 100 + '%'; return s }) : []));
    }
  };
  return box;
}

function plControls(big) {
  const pp = el('button.pl-pp', { type: 'button', onclick: () => plPlaying() ? pl.audio.pause() : pl.audio.play() });
  const box = el('div.pl-ctl', null,
    el('button.pl-skip', { type: 'button', 'aria-label': 'Back 15 seconds', title: 'Back 15 seconds', onclick: () => plSkip(-15) }, svg(ICON.ccw || ['M4 12a8 8 0 1 0 2.5-5.8', 'M4 4v4h4']), el('small', null, '15')),
    pp,
    el('button.pl-skip', { type: 'button', 'aria-label': 'Forward 30 seconds', title: 'Forward 30 seconds', onclick: () => plSkip(30) }, svg(['M20 12a8 8 0 1 1-2.5-5.8', 'M20 4v4h-4']), el('small', null, '30')));
  box.draw = () => {
    const on = plPlaying();
    pp.replaceChildren(svg(on ? ICON.pause : ICON.play));
    pp.setAttribute('aria-label', on ? 'Pause' : 'Play');
  };
  if (big) box.classList.add('big');
  return box;
}

function plSpeedButton() {
  const b = el('button.pl-chip', { type: 'button', title: 'Playback speed', onclick: () => {
    menu(b, LIB_SPEEDS.map(s => ({ label: libSpeedLabel(s), on: Math.abs(s - pl.audio.playbackRate) < 0.01, run: () => plSetSpeed(s) })));
  } });
  b.draw = () => { b.textContent = libSpeedLabel(pl.audio?.playbackRate || 1) };
  return b;
}

function plSleepButton() {
  const b = el('button.pl-chip', { type: 'button', title: 'Sleep timer', onclick: () => {
    const has = (pl.cur?.it.chapters || []).length > 0;
    menu(b, [
      { label: 'Off', on: !pl.sleep, run: () => plSetSleep(null) },
      ...[15, 30, 60].map(m => ({ label: m === 60 ? '1 hour' : m + ' minutes', run: () => plSetSleep(m) })),
      has ? { label: 'End of chapter', on: pl.sleep?.mode === 'chapter', run: () => plSetSleep('chapter') } : null,
      { label: 'End of episode', on: pl.sleep?.mode === 'episode', run: () => plSetSleep('episode') },
    ].filter(Boolean));
  } });
  b.draw = () => b.replaceChildren(svg(ICON.moon), el('span', null, plSleepLabel()));
  return b;
}

// plMini is the bar at the bottom while something's loaded.
function plMini() {
  const cover = el('span.pl-cover');
  const title = el('b'), sub = el('span');
  const scrub = plScrub(), ctl = plControls(false), speed = plSpeedButton();
  const line = el('span.pl-line', null, el('i'));
  const bar = el('div.pl-mini', { role: 'region', 'aria-label': 'Player' },
    el('button.pl-mini-open', { type: 'button', 'aria-label': 'Open the player', onclick: plFull }, cover, el('span.pl-mini-text', null, title, sub)),
    ctl, scrub, speed,
    el('button.icon-btn.bare.pl-icon.pl-wide', { type: 'button', 'aria-label': 'Up next', title: 'Up next', onclick: () => plFull('next') }, svg(['M4 6h12', 'M4 12h12', 'M4 18h8', 'M17 15v6l4-3z'])),
    el('button.icon-btn.bare.pl-icon.pl-wide', { type: 'button', 'aria-label': 'Open the player', title: 'Open the player', onclick: plFull }, svg(['M6 15l6-6 6 6'])),
    el('button.icon-btn.bare.pl-icon', { type: 'button', 'aria-label': 'Close the player', title: 'Close the player', onclick: plClose }, svg(ICON.x)),
    line);
  bar.draw = full => {
    const c = pl.cur;
    if (!c) return;
    if (full) {
      cover.replaceChildren(libCover(c.name, c.look, c.it, c.col));
      title.textContent = c.it.title;
      asstAccent(bar, c.look?.color);
    }
    sub.textContent = plChapterTitle(c);
    ctl.draw();
    speed.draw();
    scrub.draw();
    line.firstChild.style.width = (plFraction() || 0) * 100 + '%';
  };
  return bar;
}

function plDraw() {
  const c = pl.cur;
  document.body.classList.toggle('has-player', !!c);
  if (!c) { pl.bar?.remove(); pl.bar = null; plSyncRows(); return }
  if (!pl.bar) { pl.bar = plMini(); document.body.append(pl.bar) }
  pl.bar.draw(true);
  pl.full?.draw(true);
  plSyncRows();
}

function plDrawTime() {
  pl.bar?.draw(false);
  pl.full?.draw(false);
  // Rows of what's playing move their bars as it plays.
  if (pl.cur) document.querySelectorAll(`[data-lib="${CSS.escape(libKey(pl.cur.name, pl.cur.it.id))}"]`).forEach(n => n.libSync?.(libCache.get(libKey(pl.cur.name, pl.cur.it.id)) || pl.cur.it));
}

// plSyncRows redraws every audio row: which one is playing changed.
let plLast = '';
function plSyncRows() {
  const now = pl.cur ? libKey(pl.cur.name, pl.cur.it.id) + (plPlaying() ? '+' : '') : '';
  const keys = new Set([plLast.replace(/\+$/, ''), now.replace(/\+$/, '')].filter(Boolean));
  plLast = now;
  for (const k of keys) document.querySelectorAll(`[data-lib="${CSS.escape(k)}"]`).forEach(n => { const it = libCache.get(k); if (it) n.libSync?.(it) });
}

// plFull is the full player: the cover big, chapters, transcript and up
// next, the seek bar, speed and the sleep timer. On a phone it fills the
// screen and a swipe down closes it.
function plFull(tab) {
  const c = pl.cur;
  if (!c) return;
  const art = el('div.pl-art');
  const title = el('b'), meta = el('span');
  const scrub = plScrub(), ctl = plControls(true), speed = plSpeedButton(), sleep = plSleepButton();
  const list = el('div.pl-list');
  const tabs = el('div.pl-tabs', { role: 'tablist' });
  let which = typeof tab === 'string' ? tab : (c.it.chapters || []).length ? 'chapters' : c.it.transcript ? 'transcript' : 'next';
  let transcript = null;
  const drawList = () => {
    const cur = pl.cur;
    const has = { chapters: (cur.it.chapters || []).length > 0, transcript: !!cur.it.transcript, next: true };
    if (!has[which]) which = has.chapters ? 'chapters' : has.transcript ? 'transcript' : 'next';
    tabs.replaceChildren(...[['chapters', 'Chapters'], ['transcript', 'Transcript'], ['next', 'Up next']].filter(([k]) => has[k]).map(([k, label]) =>
      el('button.lib-chip' + (k === which ? '.on' : ''), { type: 'button', role: 'tab', 'aria-selected': String(k === which), onclick: () => { which = k; drawList() } },
        label, k === 'next' && pl.queue.length ? el('span.n', null, String(pl.queue.length)) : null)));
    if (which === 'chapters') {
      const ch = cur.it.chapters, i = libChapterAt(ch, plTime()), d = plDuration();
      list.replaceChildren(...ch.map((x, j) => el('button.pl-ch' + (j === i ? '.on' : j < i ? '.past' : ''), { type: 'button', onclick: () => { plSeek(x.at); if (!plPlaying()) pl.audio.play() } },
        el('small', null, libClock(x.at)), el('span', null, x.title), el('small', null, d ? libClock((ch[j + 1]?.at ?? d) - x.at) : ''))));
      list.dataset.at = String(i);
    } else if (which === 'transcript') {
      if (!transcript) {
        transcript = el('div.pl-transcript', null, el('span.spinner'));
        fetch(libURL(cur.name, cur.it.transcript)).then(r => r.ok ? r.text() : Promise.reject()).then(t => {
          transcript.replaceChildren(...t.split(/\n{2,}/).map(p => el('p', null, reveal(p.trim()))));
        }).catch(() => transcript.replaceChildren(el('p.muted', null, 'Couldn’t load the transcript.')));
      }
      list.replaceChildren(transcript);
    } else {
      list.replaceChildren(...(pl.queue.length ? pl.queue.map(x => el('button.pl-next', { type: 'button', onclick: () => plPlay(cur.name, x, cur.col, cur.look) },
        libCover(cur.name, cur.look, x, cur.col), el('span', null, el('b', null, x.title), el('small', null, libStatus(x).text)))) : [el('p.pl-empty', null, 'Nothing else new in ' + (cur.col?.title || 'this collection') + '.')]));
    }
  };
  const root = el('div.pl-full', { role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Player', tabindex: '-1' },
    el('div.pl-full-shade', { onclick: () => layer.close(true) }),
    el('div.pl-full-box', null,
      el('div.pl-grip'),
      el('button.icon-btn.bare.pl-full-close', { type: 'button', 'aria-label': 'Close', onclick: () => layer.close(true) }, svg(ICON.x)),
      el('div.pl-full-art', null, art, el('div.pl-full-meta', null, title, meta)),
      el('div.pl-full-side', null, tabs, list,
        el('div.pl-full-bottom', null, scrub, el('div.pl-full-row', null, speed, ctl, sleep)))));
  asstAccent(root, c.look?.color);
  const layer = libOverlay(root, () => { pl.full = null });
  layer.draw = full => {
    const cur = pl.cur;
    if (!cur) return layer.close(true);
    if (full) {
      art.replaceChildren(libCover(cur.name, cur.look, cur.it, cur.col, '.huge'));
      title.textContent = cur.it.title;
      const day = new Date(cur.it.at).toLocaleDateString(undefined, { month: 'long', day: 'numeric' });
      meta.textContent = [cur.col?.title, cur.look?.name, day].filter(Boolean).join(' · ');
      transcript = full === true && root.dataset.item !== cur.it.id ? null : transcript;
      root.dataset.item = cur.it.id;
      drawList();
    } else if (which === 'chapters' && String(libChapterAt(cur.it.chapters, plTime())) !== list.dataset.at) drawList();
    scrub.draw();
    ctl.draw();
    speed.draw();
    sleep.draw();
  };
  pl.full = layer;
  plSwipeDown(root.querySelector('.pl-full-box'), () => layer.close(true));
  layer.draw(true);
}

// plSwipeDown closes a phone's full player when it's dragged down from its
// top.
function plSwipeDown(box, done) {
  let y0 = null, dy = 0;
  box.addEventListener('touchstart', e => { y0 = box.scrollTop <= 0 && !e.target.closest('.pl-track, .pl-list') ? e.touches[0].clientY : null; dy = 0 }, { passive: true });
  box.addEventListener('touchmove', e => {
    if (y0 == null) return;
    dy = Math.max(0, e.touches[0].clientY - y0);
    box.style.transform = dy ? `translateY(${dy}px)` : '';
  }, { passive: true });
  box.addEventListener('touchend', () => {
    if (y0 == null) return;
    box.style.transform = '';
    if (dy > 110) done();
    y0 = null;
  });
}

// plRestore brings back what was playing when the app last closed, paused
// where the server says you are: another device may have moved it on.
async function plRestore() {
  let saved;
  try { saved = JSON.parse(localStorage[PL_STORE] || 'null') } catch { }
  if (!saved?.name || !saved.id) return;
  try {
    const [d, a] = await Promise.all([
      api('GET', asstAPI(saved.name) + '/library/' + encodeURIComponent(saved.id)),
      api('GET', asstAPI(saved.name)),
    ]);
    if (pl.cur || d.item.kind !== 'audio') return;
    libCache.set(libKey(saved.name, d.item.id), d.item);
    plLoad(saved.name, d.item, d.collection, a.look, d.item.finished ? 0 : libResume({ progress: Math.max(d.item.progress || 0, 0) }));
  } catch {
    delete localStorage[PL_STORE];
  }
}
