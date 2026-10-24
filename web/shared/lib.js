'use strict';

// Pure helpers shared by both UIs and lib.test.js. Nothing in here touches
// the DOM, so it runs under Node as well as in the browser.

function ago(iso, now = Date.now()) {
  if (!iso) return '';
  const d = new Date(iso);
  const s = (now - d) / 1000;
  if (s < 45) return 'just now';
  if (s < 3600) return Math.round(s / 60) + 'm ago';
  if (s < 86400) return Math.round(s / 3600) + 'h ago';
  if (s < 604800) return Math.round(s / 86400) + 'd ago';
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

function until(iso, now = Date.now()) {
  const s = (Date.parse(iso) - now) / 1000;
  if (!(s > 0)) return 'now';
  if (s < 60) return 'in under a minute';
  if (s < 3600) return 'in ' + Math.round(s / 60) + 'm';
  if (s < 36 * 3600) return 'in ' + Math.round(s / 3600) + 'h';
  return 'in ' + Math.round(s / 86400) + 'd';
}

function duration(from, to) {
  if (!from || !to) return '';
  let s = Math.max(0, Math.round((Date.parse(to) - Date.parse(from)) / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  s %= 60;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s}s`;
  return `${s}s`;
}

const PROJECT = /^[a-z0-9][a-z0-9-]{0,39}$/;
const NAME = /^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$/;
const SETTINGS_SECTIONS = ['instructions', 'prompts', 'rules', 'github', 'secrets', 'bundles', 'access'];

// route maps a URL path to a view. Names are validated here so a crafted URL
// never reaches an API path.
function route(path) {
  const m = /^\/(p|s|r)\/([^/]+)$/.exec(path);
  if (path === '/' || path === '') return { name: 'home' };
  if (path === '/activity') return { name: 'activity' };
  if (path === '/settings') return { name: 'settings' };
  // Settings: a section, or a bundle or a project's access to edit.
  const set = /^\/settings\/([a-z]+)(?:\/([^/]+))?$/.exec(path);
  if (set) {
    const [, section, item] = set;
    if (!SETTINGS_SECTIONS.includes(section)) return { name: 'notfound' };
    if (item === undefined) return { name: 'settings', section };
    let v;
    try { v = decodeURIComponent(item) } catch { return { name: 'notfound' } }
    if (section === 'bundles' && (v === 'new' || NAME.test(v))) return { name: 'settings', section, item: v };
    if (section === 'access' && PROJECT.test(v)) return { name: 'settings', section, item: v };
    return { name: 'notfound' };
  }
  const t = /^\/p\/([^/]+)\/(board|runs)$/.exec(path);
  if (t) {
    let v;
    try { v = decodeURIComponent(t[1]) } catch { return { name: 'notfound' } }
    return PROJECT.test(v) ? { name: 'project', project: v, tab: t[2] } : { name: 'notfound' };
  }
  if (!m) return { name: 'notfound' };
  let v;
  try { v = decodeURIComponent(m[2]) } catch { return { name: 'notfound' } }
  if (m[1] === 'p' && PROJECT.test(v)) return { name: 'project', project: v, tab: 'chats' };
  if (m[1] === 's' && NAME.test(v)) return { name: 'session', id: v };
  if (m[1] === 'r' && NAME.test(v)) return { name: 'run', id: v };
  return { name: 'notfound' };
}

// reveal shows characters that would hide or reorder what an approver
// reads (bidi overrides, zero-width and tag characters, controls, lone
// surrogates) as <U+XXXX>. Tabs and line breaks stay.
const HIDDEN = /[\p{Cc}\p{Cf}\p{Cs}\u2028\u2029]/gu;
function reveal(s) {
  return String(s ?? '').replace(HIDDEN, c =>
    c === '\n' || c === '\t' ? c : '<U+' + c.codePointAt(0).toString(16).toUpperCase().padStart(4, '0') + '>');
}

// toolSummary is the headline an approval card shows for a tool call.
function toolSummary(tool, input) {
  if (!input || typeof input !== 'object') return '';
  const v = input.command ?? input.file_path ?? input.path ?? input.url ?? input.pattern;
  let s = typeof v === 'string' ? v : JSON.stringify(input);
  if (s.length > 240) s = s.slice(0, 240) + '…';
  return reveal(s);
}

// toolInput is the whole tool input, uncut, for under the headline: an
// Edit's headline is only its path.
function toolInput(input) {
  if (input == null) return '';
  return reveal(JSON.stringify(input, null, 2));
}

// messageBytes is what the message box types into a terminal. Several lines
// go as one bracketed paste, so a newline doesn't submit early; the paste-end
// marker is dropped from the text so the rest can't be read as keystrokes.
function messageBytes(text) {
  const s = String(text).replace(/\r\n?/g, '\n');
  if (!s.includes('\n')) return s;
  return '\x1b[200~' + s.split('\x1b[201~').join('') + '\x1b[201~';
}

// termFont is the terminal font size: the saved one, kept in range.
function termFont(saved) {
  const n = parseInt(saved, 10);
  return Number.isFinite(n) ? Math.min(24, Math.max(10, n)) : 14;
}

// The terminal's 16 colours (xterm.js's defaults), then the 6x6x6 cube and
// the grey ramp, for history rows drawn outside the terminal.
const ANSI = ['#2e3436', '#cc0000', '#4e9a06', '#c4a000', '#3465a4', '#75507b', '#06989a', '#d3d7cf',
  '#555753', '#ef2929', '#8ae234', '#fce94f', '#729fcf', '#ad7fa8', '#34e2e2', '#eeeeec'];
function colour256(n) {
  if (n < 16) return ANSI[n];
  const hex = v => v.toString(16).padStart(2, '0');
  if (n >= 232) { const g = hex(8 + 10 * (n - 232)); return '#' + g + g + g }
  const lv = [0, 95, 135, 175, 215, 255], i = n - 16;
  return '#' + hex(lv[Math.floor(i / 36)]) + hex(lv[Math.floor(i / 6) % 6]) + hex(lv[i % 6]);
}

// sgrRuns splits one row of tmux's history (capture-pane -e) into runs of
// text with a style: colours as CSS values, and bold, dim, italic, underline,
// inverse and strike. Every other escape sequence and control character is
// dropped, so nothing in a row can do more than colour its own text.
function sgrRuns(row) {
  const runs = [];
  let style = {}, text = '';
  const flush = () => { if (text) runs.push({ text, style: { ...style } }); text = '' };
  const apply = params => {
    const p = params === '' ? [0] : params.split(';').map(x => parseInt(x, 10) || 0);
    for (let i = 0; i < p.length; i++) {
      const n = p[i];
      if (n === 0) style = {};
      else if (n === 1) style.bold = true;
      else if (n === 2) style.dim = true;
      else if (n === 3) style.italic = true;
      else if (n === 4) style.underline = true;
      else if (n === 7) style.inverse = true;
      else if (n === 9) style.strike = true;
      else if (n === 22) { delete style.bold; delete style.dim }
      else if (n === 23) delete style.italic;
      else if (n === 24) delete style.underline;
      else if (n === 27) delete style.inverse;
      else if (n === 29) delete style.strike;
      else if (n >= 30 && n <= 37) style.fg = ANSI[n - 30];
      else if (n >= 90 && n <= 97) style.fg = ANSI[n - 82];
      else if (n >= 40 && n <= 47) style.bg = ANSI[n - 40];
      else if (n >= 100 && n <= 107) style.bg = ANSI[n - 92];
      else if (n === 39) delete style.fg;
      else if (n === 49) delete style.bg;
      else if (n === 38 || n === 48) {
        const key = n === 38 ? 'fg' : 'bg';
        if (p[i + 1] === 5 && i + 2 < p.length) { style[key] = colour256(p[i + 2] & 255); i += 2 }
        else if (p[i + 1] === 2 && i + 4 < p.length) { style[key] = `rgb(${p[i + 2] & 255},${p[i + 3] & 255},${p[i + 4] & 255})`; i += 4 }
      }
    }
  };
  const re = /\x1b\[([0-9;]*)m|\x1b\[[0-?]*[ -\/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b.?|[\x00-\x08\x0a-\x1f\x7f]/g;
  let last = 0, m;
  while ((m = re.exec(row))) {
    text += row.slice(last, m.index);
    last = re.lastIndex;
    if (m[1] !== undefined) { flush(); apply(m[1]) }
  }
  text += row.slice(last);
  flush();
  return runs;
}

// dropPosition is where a card dropped between two others goes: the
// midpoint of its neighbours' positions, or one past the end.
function dropPosition(above, below) {
  if (above === undefined && below === undefined) return 0;
  if (above === undefined) return below - 1;
  if (below === undefined) return above + 1;
  return (above + below) / 2;
}

// columnPage picks the cards a board column shows: an open column's first 20
// until it's expanded, or every Done card loaded. more labels the button for
// the rest; below is the position of the first card left out.
function columnPage(status, items, total, expanded, cap = 20) {
  const shown = status === 'done' || expanded ? items : items.slice(0, cap);
  const hidden = total - shown.length;
  const more = hidden <= 0 ? null : status === 'done' ? `Show ${Math.min(50, hidden)} older` : `Show ${hidden} more`;
  return { shown, more, below: items[shown.length]?.position };
}

// auditRow turns a broker audit entry into one line of the activity page: a
// tone, a verb and what it was about.
const AUDIT = {
  allow: ['ok', 'Allowed'], approve: ['ok', 'Approved'], reject: ['bad', 'Denied'], deny: ['bad', 'Blocked'], ask: ['wait', 'Asked'],
  mint: ['info', 'Token issued'], revoke: ['bad', 'Revoked'], config: ['info', 'Settings'],
};
function auditRow(e) {
  const [tone, verb] = AUDIT[e.decision] || ['info', e.decision || ''];
  const target = e.host ? (e.method && e.method !== 'CONNECT' ? e.method + ' ' : '') + e.host + (e.path || '') : e.reason || '';
  return { tone, verb, target: reveal(target) };
}

// hostsFromText splits a list of hosts typed with commas, spaces or lines.
const hostsFromText = text => String(text).split(/[\s,]+/).map(h => h.trim()).filter(Boolean);

// boardSeries buckets a board's history (see board.History) into the last
// `days` days in local time: items finished each day, and how many were
// open when it ended (today: now).
function boardSeries(h, now = new Date(), days = 14) {
  const created = (h.created || []).map(Date.parse);
  const done = (h.done || []).map(Date.parse);
  const upTo = (ts, t) => ts.filter(x => x < t).length;
  const out = [];
  for (let i = days - 1; i >= 0; i--) {
    const day = new Date(now.getFullYear(), now.getMonth(), now.getDate() - i);
    const next = new Date(day.getFullYear(), day.getMonth(), day.getDate() + 1);
    const end = Math.min(next.getTime(), now.getTime() + 1);
    out.push({
      day,
      done: done.filter(x => x >= day.getTime() && x < next.getTime()).length,
      open: (h.open_at_start || 0) + upTo(created, end) - upTo(done, end),
    });
  }
  return out;
}

// daySeries is the last `days` days in local time, each starting as
// zero(), with add(day, hour) putting each hour's counts into its day.
function daySeries(hours, zero, add, now, days) {
  const out = [];
  for (let i = days - 1; i >= 0; i--) {
    out.push({ day: new Date(now.getFullYear(), now.getMonth(), now.getDate() - i), ...zero() });
  }
  for (const h of hours || []) {
    const t = new Date(h.t);
    const d = out.find(x => x.day.getFullYear() === t.getFullYear() && x.day.getMonth() === t.getMonth() && x.day.getDate() === t.getDate());
    if (d) add(d, h);
  }
  return out;
}

// activitySeries adds the broker's hourly counts up into the last `days`
// days in local time.
function activitySeries(hours, now = new Date(), days = 14) {
  return daySeries(hours, () => ({ allowed: 0, asked: 0, blocked: 0 }),
    (d, h) => { d.allowed += h.allowed || 0; d.asked += h.asked || 0; d.blocked += h.blocked || 0 }, now, days);
}

// usageSeries adds the tokens harnesses reported each hour up into days,
// a count per harness that reported any in them (Claude, then Codex, then
// the rest by name).
function usageSeries(hours, now = new Date(), days = 14) {
  const series = daySeries(hours, () => ({}), (d, h) => {
    d[h.harness] = (d[h.harness] || 0) + (h.input || 0) + (h.cached || 0) + (h.output || 0);
  }, now, days);
  const order = ['claude', 'codex'];
  const rank = h => order.includes(h) ? order.indexOf(h) : order.length;
  const harnesses = [...new Set(series.flatMap(d => Object.keys(d).filter(k => k !== 'day')))]
    .sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
  for (const d of series) for (const h of harnesses) d[h] = d[h] || 0;
  return { days: series, harnesses };
}

// compactCount writes a big count short: 12.4K, 3.3M.
function compactCount(n) {
  for (const [v, s] of [[1e9, 'B'], [1e6, 'M'], [1e3, 'K']]) {
    if (n >= v) return (n / v).toFixed(1).replace(/\.0$/, '') + s;
  }
  return String(n);
}

// limitLabel names an account usage window by its length, or failing
// that by what the harness calls it.
function limitLabel(l) {
  const named = { 300: '5-hour', 1440: 'Daily', 10080: 'Weekly', 43200: 'Monthly' }[l.minutes];
  if (named) return named;
  if (l.minutes && l.minutes % 60 === 0) return `${l.minutes / 60}-hour`;
  const words = String(l.name || '').replace(/_/g, ' ');
  return words.charAt(0).toUpperCase() + words.slice(1);
}

if (typeof module !== 'undefined') module.exports = { activitySeries, usageSeries, compactCount, limitLabel, boardSeries, hostsFromText, ago, until, duration, route, auditRow, dropPosition, columnPage, messageBytes, termFont, sgrRuns, toolSummary, toolInput, reveal };
