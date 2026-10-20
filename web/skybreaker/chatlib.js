'use strict';

// Pure helpers for the chat UI, shared with chatlib.test.js: a markdown
// parser that returns data (chat.js turns it into elements with
// textContent; nothing here is ever parsed as HTML), a unified diff
// parser, the reducer that folds a session's events into what the page
// draws, and the inbox's grouping.

// --- markdown ---------------------------------------------------------------

const FENCE = /^(\s*)(`{3,}|~{3,})\s*([^\s`]*)/;
const HEADING = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const RULE = /^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/;
const QUOTE = /^\s{0,3}>/;
const ITEM = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const TABLE_SEP = /^\s*\|?\s*:?-{2,}:?\s*(?:\|\s*:?-{2,}:?\s*)*\|?\s*$/;

const blank = l => /^\s*$/.test(l);
const indentOf = l => l.length - l.trimStart().length;

function startsBlock(lines, i) {
  const l = lines[i];
  return FENCE.test(l) || HEADING.test(l) || RULE.test(l) || QUOTE.test(l) || ITEM.test(l) ||
    (l.includes('|') && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1]));
}

function splitRow(line) {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1);
  const cells = [];
  let cur = '';
  for (let i = 0; i < s.length; i++) {
    if (s[i] === '\\' && s[i + 1] === '|') { cur += '|'; i++; continue }
    if (s[i] === '|') { cells.push(cur.trim()); cur = ''; continue }
    cur += s[i];
  }
  cells.push(cur.trim());
  return cells;
}

// mdBlocks parses markdown into blocks: p, h, code, hr, quote, ul, ol,
// table. It is forgiving, as streamed text is often half-written.
function mdBlocks(src) {
  const lines = String(src ?? '').replace(/\r\n?/g, '\n').split('\n');
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (blank(line)) { i++; continue }
    let m = FENCE.exec(line);
    if (m) {
      const fence = m[2], pad = m[1].length, body = [];
      i++;
      while (i < lines.length && !(lines[i].trimStart().startsWith(fence[0].repeat(fence.length)) && lines[i].trim().replace(/[`~]/g, '') === '')) {
        const l = lines[i];
        body.push(indentOf(l) >= pad ? l.slice(pad) : l.trimStart());
        i++;
      }
      i++;
      out.push({ t: 'code', lang: m[3] || '', text: body.join('\n') });
      continue;
    }
    if ((m = HEADING.exec(line))) { out.push({ t: 'h', level: m[1].length, text: m[2] }); i++; continue }
    if (RULE.test(line)) { out.push({ t: 'hr' }); i++; continue }
    if (QUOTE.test(line)) {
      const q = [];
      while (i < lines.length && QUOTE.test(lines[i])) { q.push(lines[i].replace(/^\s{0,3}>\s?/, '')); i++ }
      out.push({ t: 'quote', blocks: mdBlocks(q.join('\n')) });
      continue;
    }
    if (line.includes('|') && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1])) {
      const head = splitRow(line);
      const align = splitRow(lines[i + 1]).map(c => c.startsWith(':') && c.endsWith(':') ? 'center' : c.endsWith(':') ? 'right' : c.startsWith(':') ? 'left' : '');
      const rows = [];
      i += 2;
      while (i < lines.length && lines[i].includes('|') && !blank(lines[i])) {
        const r = splitRow(lines[i]);
        while (r.length < head.length) r.push('');
        rows.push(r.slice(0, head.length));
        i++;
      }
      out.push({ t: 'table', head, align, rows });
      continue;
    }
    if ((m = ITEM.exec(line))) {
      i = list(lines, i, out);
      continue;
    }
    const para = [line.trim()];
    i++;
    while (i < lines.length && !blank(lines[i]) && !startsBlock(lines, i)) { para.push(lines[i].trim()); i++ }
    out.push({ t: 'p', text: para.join('\n') });
  }
  return out;
}

// list parses the list starting at lines[i] into out and returns the index
// after it. An item's body is every following line indented past its
// marker, parsed as blocks of its own: nested lists, code, paragraphs.
function list(lines, i, out) {
  const first = ITEM.exec(lines[i]);
  const base = first[1].length;
  const ordered = /\d/.test(first[2]);
  const node = { t: ordered ? 'ol' : 'ul', start: ordered ? parseInt(first[2], 10) : 1, items: [] };
  while (i < lines.length) {
    const m = ITEM.exec(lines[i]);
    if (!m || m[1].length !== base || /\d/.test(m[2]) !== ordered) break;
    const contentAt = m[1].length + m[2].length + 1;
    let text = m[3];
    let check = null;
    const task = /^\[([ xX])\]\s+(.*)$/.exec(text);
    if (task) { check = task[1] !== ' '; text = task[2] }
    const body = [];
    i++;
    // Lazy continuation: unindented text right after the item joins it.
    while (i < lines.length && !blank(lines[i]) && !ITEM.exec(lines[i]) && indentOf(lines[i]) <= base && !startsBlock(lines, i)) {
      text += '\n' + lines[i].trim();
      i++;
    }
    while (i < lines.length) {
      const l = lines[i];
      if (blank(l)) {
        let j = i;
        while (j < lines.length && blank(lines[j])) j++;
        if (j < lines.length && indentOf(lines[j]) > base) { while (i < j) { body.push(''); i++ } continue }
        break;
      }
      if (indentOf(l) <= base) break;
      const cut = Math.min(indentOf(l), contentAt);
      body.push(l.slice(cut));
      i++;
    }
    const item = { text, check, children: [] };
    if (body.length) {
      const blocks = mdBlocks(body.join('\n'));
      // A paragraph straight after the first line continues it.
      if (blocks[0]?.t === 'p' && body[0] !== '' && !ITEM.test(body[0])) item.text += '\n' + blocks.shift().text;
      item.children = blocks;
    }
    node.items.push(item);
    let j = i;
    while (j < lines.length && blank(lines[j])) j++;
    if (j < lines.length) {
      const n = ITEM.exec(lines[j]);
      if (n && n[1].length === base && /\d/.test(n[2]) === ordered) { i = j; continue }
    }
    break;
  }
  out.push(node);
  return i;
}

// CHAT_COLORS are the tints a chat can be given; the server keeps the same
// list (app.ChatColors).
const CHAT_COLORS = ['sun', 'planet', 'red', 'green', 'blue', 'purple', 'pink', 'teal'];

const COMMANDS = [
  { cmd: 'rename', usage: '/rename <title>', desc: 'Name this chat' },
  { cmd: 'color', usage: '/color <colour>', desc: 'Tint this chat, to tell it apart' },
  { cmd: 'goal', usage: '/goal <what you want done>', desc: 'Pin a goal and give it to the agent' },
  { cmd: 'clear', usage: '/clear', desc: 'End this chat and start a fresh one' },
  { cmd: 'fork', usage: '/fork', desc: 'Copy this conversation into a new chat' },
  { cmd: 'compact', usage: '/compact', desc: 'Summarise the conversation to free up context' },
  { cmd: 'prompt', usage: '/prompt <name>', desc: 'Send one of the prompts saved in Settings' },
];

// parseCommand reads Skybreaker's own chat commands. Anything else starting
// with a slash (the harness's own commands) is left for the agent.
function parseCommand(text) {
  const m = /^\/(rename|color|goal|clear|fork|compact|prompt)(?:\s+([\s\S]*))?$/.exec(String(text ?? '').trim());
  return m ? { cmd: m[1], arg: (m[2] || '').trim() } : null;
}

// commandHints is what to suggest while you type a command, with the
// prompts saved in Settings for /prompt.
function commandHints(text, prompts = []) {
  const s = String(text ?? '');
  const color = /^\/color\s+(\S*)$/.exec(s);
  if (color) return CHAT_COLORS.filter(c => c.startsWith(color[1])).map(c => ({ cmd: 'color', color: c }));
  const prompt = /^\/prompt\s+(\S*)$/.exec(s);
  if (prompt) {
    return prompts.filter(p => p.name.startsWith(prompt[1]))
      .map(p => ({ cmd: 'prompt', prompt: p.name, desc: clipLabel(firstLine(p.text)) }));
  }
  const m = /^\/([a-z]*)$/.exec(s);
  return m ? COMMANDS.filter(c => c.cmd.startsWith(m[1])) : [];
}

// promptText is what /prompt <name> [more] sends: the saved prompt, with
// anything typed after its name added below it. An unknown name is null.
function promptText(arg, prompts = []) {
  const m = /^(\S+)(?:\s+([\s\S]*))?$/.exec(String(arg ?? '').trim());
  const p = m && prompts.find(p => p.name === m[1]);
  if (!p) return null;
  return m[2] ? p.text + '\n\n' + m[2].trim() : p.text;
}

// previewURL accepts a preview's address only as a bare http(s) origin:
// Skybreaker sets it, and nothing else becomes the card's link.
function previewURL(u) {
  return typeof u === 'string' && /^https?:\/\/[A-Za-z0-9.-]+(:\d{1,5})?$/.test(u) ? u : null;
}

// safeHref allows web and mail links only.
function safeHref(u) {
  const s = String(u ?? '').trim();
  return /^(https?:\/\/[^\s]+|mailto:[^\s]+)$/i.test(s) ? s : null;
}

// signInHost is where a harness's sign-in link goes, and whether that's
// one of the vendors' own sign-in hosts. The link comes from the harness in
// the sandbox, so the page shows the host, and warns about any other.
const SIGN_IN_HOSTS = ['auth.openai.com', 'chatgpt.com', 'claude.ai', 'platform.claude.com', 'console.anthropic.com'];
function signInHost(u) {
  if (!safeHref(u) || !/^https:/i.test(u)) return null;
  try {
    const host = new URL(u).hostname.toLowerCase();
    return { host, known: SIGN_IN_HOSTS.includes(host) };
  } catch { return null }
}

const SPECIAL = /[\\`\n[<*_~]|https?:\/\//g;
const CODE_SPAN = /(`+)([\s\S]*?[^`])\1(?!`)/y;
const LINK = /\[((?:[^\[\]\\]|\\.)*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)/y;
const AUTOLINK = /<(https?:\/\/[^>\s]+)>/y;
const BARE = /https?:\/\/[^\s<>"'`]+/y;
const STRONG = /(\*\*|__)(?=\S)([\s\S]*?\S)\1/y;
const STRIKE = /~~(?=\S)([\s\S]*?\S)~~/y;
const EM = /([*_])(?=\S)([\s\S]*?\S)\1(?![*_\w])/y;

const sticky = (re, s, i) => { re.lastIndex = i; return re.exec(s) };
const word = c => !!c && /[\p{L}\p{N}_]/u.test(c);

// mdInline parses one block's text into runs: text, code, b, i, s, a, br.
function mdInline(src) {
  const s = String(src ?? '');
  const out = [];
  let text = '';
  const push = node => {
    if (text) { out.push({ t: 'text', text }); text = '' }
    if (node) out.push(node);
  };
  // Where a full scan for a delimiter's closer first failed: from any later
  // opener of the same kind it fails too (the closers it could use are a
  // subset), so it isn't scanned again. Without this, text full of
  // unmatched markers rescans to the end for each one.
  const failed = {};
  const scan = (key, re, scanned) => {
    if (failed[key] !== undefined && i >= failed[key]) return null;
    const m = sticky(re, s, i);
    if (!m && scanned && failed[key] === undefined) failed[key] = i;
    return m;
  };
  const space = j => j >= s.length || /\s/.test(s[j]);
  let i = 0;
  while (i < s.length) {
    SPECIAL.lastIndex = i;
    const sp = SPECIAL.exec(s);
    if (!sp) { text += s.slice(i); break }
    text += s.slice(i, sp.index);
    i = sp.index;
    const c = s[i], prev = s[i - 1];
    let m;
    if (c === '\\' && i + 1 < s.length && /[\\`*_{}[\]()#+\-.!~|>]/.test(s[i + 1])) { text += s[i + 1]; i += 2; continue }
    if (c === '\n') { push({ t: 'br' }); i++; continue }
    let run = 0;
    if (c === '`') while (s[i + run] === '`') run++;
    if (c === '`' && (m = scan('`'.repeat(run), CODE_SPAN, true))) {
      let t = m[2];
      if (t.length > 2 && t.startsWith(' ') && t.endsWith(' ')) t = t.slice(1, -1);
      push({ t: 'code', text: t });
      i += m[0].length;
      continue;
    }
    if (c === '[' && (m = sticky(LINK, s, i))) {
      const href = safeHref(m[2]);
      if (href) { push({ t: 'a', href, children: mdInline(m[1]) }); i += m[0].length; continue }
    }
    if (c === '<' && (m = sticky(AUTOLINK, s, i))) { push({ t: 'a', href: m[1], children: [{ t: 'text', text: m[1] }] }); i += m[0].length; continue }
    if (c === 'h' && !word(prev) && (m = sticky(BARE, s, i))) {
      // Trailing punctuation ends the sentence, not the URL.
      let url = m[0].replace(/[.,:;!?)\]}]+$/, '');
      if (url.length > 8) { push({ t: 'a', href: url, children: [{ t: 'text', text: url }] }); i += url.length; continue }
    }
    if ((c === '*' || c === '_') && s[i + 1] === c && !(c === '_' && word(prev)) && (m = scan(c + c, STRONG, !space(i + 2)))) {
      push({ t: 'b', children: mdInline(m[2]) });
      i += m[0].length;
      continue;
    }
    if (c === '~' && s[i + 1] === '~' && (m = scan('~~', STRIKE, !space(i + 2)))) { push({ t: 's', children: mdInline(m[1]) }); i += m[0].length; continue }
    if ((c === '*' || c === '_') && !(c === '_' && word(prev)) && (m = scan(c, EM, !space(i + 1))) && !/^\s/.test(m[2])) {
      push({ t: 'i', children: mdInline(m[2]) });
      i += m[0].length;
      continue;
    }
    text += c;
    i++;
  }
  push();
  return out;
}

// --- diffs ------------------------------------------------------------------

const HUNK = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$/;

// diffFiles splits a unified diff into files and hunks, numbering lines.
// Hunk line counts decide where a hunk ends, so a removed line that starts
// "--" isn't taken for the next file's header.
function diffFiles(diff) {
  const files = [];
  let f = null, h = null, remOld = 0, remNew = 0, o = 0, n = 0;
  const strip = p => p.replace(/^"|"$/g, '').replace(/^[ab]\//, '');
  const file = () => { f = { path: '', old: '', hunks: [], added: 0, removed: 0, isNew: false, isDeleted: false }; files.push(f); h = null };
  for (const line of String(diff ?? '').split('\n')) {
    if (h && (remOld > 0 || remNew > 0)) {
      const k = line[0];
      if (k === '+') { h.lines.push({ k: 'add', text: line.slice(1), n: n++ }); f.added++; remNew--; continue }
      if (k === '-') { h.lines.push({ k: 'del', text: line.slice(1), o: o++ }); f.removed++; remOld--; continue }
      if (k === ' ' || line === '') { h.lines.push({ k: 'ctx', text: line.slice(1), o: o++, n: n++ }); remOld--; remNew--; continue }
      if (k === '\\') continue;
    }
    let m;
    if (line.startsWith('diff --git ')) {
      file();
      const p = / b\/(.*)$/.exec(line);
      if (p) f.path = p[1];
      continue;
    }
    if (line.startsWith('--- ')) {
      if (!f || f.hunks.length) file();
      f.old = strip(line.slice(4).trim());
      if (f.old === '/dev/null') f.isNew = true;
      continue;
    }
    if (line.startsWith('+++ ') && f) {
      const p = strip(line.slice(4).trim());
      if (p === '/dev/null') { f.isDeleted = true; f.path = f.path || f.old } else f.path = p;
      continue;
    }
    if (line.startsWith('new file mode') && f) { f.isNew = true; continue }
    if (line.startsWith('deleted file mode') && f) { f.isDeleted = true; continue }
    if ((m = HUNK.exec(line))) {
      if (!f) file();
      o = +m[1]; n = +m[3];
      remOld = m[2] === undefined ? 1 : +m[2];
      remNew = m[4] === undefined ? 1 : +m[4];
      h = { header: line, context: m[5], lines: [] };
      f.hunks.push(h);
      continue;
    }
    if (line.startsWith('\\')) continue;
    if (h && line[0] === '+') { h.lines.push({ k: 'add', text: line.slice(1), n: n++ }); f.added++; continue }
    if (h && line[0] === '-') { h.lines.push({ k: 'del', text: line.slice(1), o: o++ }); f.removed++; continue }
  }
  for (const x of files) if (!x.path) x.path = x.old;
  return files.filter(x => x.path || x.hunks.length);
}

// --- the conversation -------------------------------------------------------

function newChat() {
  return { items: [], byId: new Map(), seq: 0, working: false, turnStart: null, asks: new Map(), info: {}, login: null, lastUser: '' };
}

// foldChat applies one event and says what to draw: an added item, or a
// changed one (redrawn in place). It returns null for an event already
// seen, which a reconnect replays.
function foldChat(c, e) {
  if (e.seq) {
    if (e.seq <= c.seq) return null;
    c.seq = e.seq;
  }
  const add = item => {
    c.items.push(item);
    if (item.id) c.byId.set(item.id, item);
    return { added: item };
  };
  const auto = () => 'e' + (e.seq || c.items.length) + ':' + c.items.length;
  switch (e.kind) {
    case 'user':
      c.lastUser = e.text;
      return add({ type: 'user', id: e.id || auto(), text: e.text, time: e.time });
    case 'delta': {
      const it = c.byId.get(e.id);
      if (it) { it.text += e.text; return { changed: it } }
      return add({ type: 'text', id: e.id, text: e.text, streaming: true });
    }
    case 'text':
    case 'thinking': {
      const type = e.kind;
      const it = c.byId.get(e.id);
      if (it) { it.text = e.text; it.streaming = false; it.type = type; return { changed: it } }
      return add({ type, id: e.id || auto(), text: e.text, streaming: false });
    }
    case 'tool': {
      const it = c.byId.get(e.id);
      if (it) {
        if (e.tool.status === 'running' && it.tool.status !== 'running') it.since = e.time || null;
        it.tool = e.tool;
        return { changed: it.parent ? c.byId.get(it.parent) || it : it };
      }
      const item = { type: 'tool', id: e.id, tool: e.tool, children: [], parent: '', since: e.time || null };
      const parent = e.tool.parent && c.byId.get(e.tool.parent);
      if (parent) {
        item.parent = parent.id;
        parent.children.push(item);
        c.byId.set(item.id, item);
        return { changed: parent };
      }
      return add(item);
    }
    case 'ask': {
      const r = add({ type: 'ask', id: e.id, ask: e.ask, state: 'open' });
      c.asks.set(e.id, r.added);
      return r;
    }
    case 'answered': {
      c.asks.delete(e.id);
      const it = c.byId.get(e.id);
      if (!it) return { changed: null };
      it.state = e.text || 'answered';
      return { changed: it };
    }
    case 'turn': {
      const t = e.turn || {};
      if (t.state === 'working') {
        c.working = true;
        c.turnStart = e.time || null;
        return { changed: null };
      }
      c.working = false;
      // Text still marked as streaming is done, whatever became of it.
      for (const it of c.items) if (it.streaming) it.streaming = false;
      if (t.state === 'failed') return add({ type: 'error', id: auto(), text: t.error || 'The turn failed.' });
      if (t.state === 'interrupted') return add({ type: 'notice', id: auto(), text: 'Stopped' });
      return { changed: null };
    }
    case 'show':
      // The same file can be shown twice, so the item's ID isn't the file's.
      return add({ type: 'show', id: auto(), file: e.id, show: e.show || {} });
    case 'preview':
      return add({ type: 'preview', id: auto(), preview: e.preview || {} });
    case 'info': {
      const prev = c.info.context;
      c.info = { ...c.info, ...e.info };
      // Usage comes without the window until the harness has said it.
      if (e.info?.context) c.info.context = { ...prev, ...e.info.context };
      else if (prev) c.info.context = prev;
      return { changed: null };
    }
    case 'login': {
      const l = e.login || {};
      if (l.done) {
        c.login = null;
        return add({ type: 'notice', id: auto(), text: 'Signed in.' });
      }
      c.login = l;
      return { changed: null };
    }
    case 'notice':
      return add({ type: 'notice', id: auto(), text: e.text });
  }
  return { changed: null };
}

// --- the inbox --------------------------------------------------------------

const when = s => Date.parse(s.updated || s.ended || s.created || 0) || 0;

// sessionTone says how a session's row looks: a tone for its colour and a
// short label.
function sessionTone(s) {
  if (s.status !== 'running') return { tone: 'ended', label: 'Ended' };
  if (s.mode !== 'chat') return { tone: 'live', label: 'Terminal' };
  switch (s.state) {
    case 'waiting': return { tone: 'wait', label: s.note || 'Waiting for you' };
    case 'login': return { tone: 'wait', label: 'Sign-in needed' };
    case 'failed': return { tone: 'bad', label: s.note || 'Hit a problem' };
    case 'working': return { tone: 'work', label: s.note || 'Working' };
  }
  const unseen = s.updated && (!s.seen || Date.parse(s.updated) > Date.parse(s.seen));
  return unseen ? { tone: 'done', label: 'Finished' } : { tone: 'idle', label: 'Idle' };
}

// inboxGroups sorts sessions into what needs you (approvals, problems,
// finished work you haven't seen), what's working, the rest, and ended.
function inboxGroups(sessions, pending = []) {
  const waiting = new Set(pending.map(p => p.session));
  const g = { forYou: [], working: [], recent: [], ended: [] };
  const rank = { wait: 0, bad: 1, done: 2 };
  for (const s of sessions) {
    const { tone } = sessionTone(s);
    if (tone === 'ended') g.ended.push(s);
    else if (tone in rank || waiting.has(s.id)) g.forYou.push(s);
    else if (tone === 'work') g.working.push(s);
    else g.recent.push(s);
  }
  const urgency = s => waiting.has(s.id) ? 0 : rank[sessionTone(s).tone] ?? 3;
  const newest = (a, b) => when(b) - when(a);
  g.forYou.sort((a, b) => urgency(a) - urgency(b) || newest(a, b));
  g.working.sort(newest);
  g.recent.sort(newest);
  g.ended.sort(newest);
  g.ended = g.ended.slice(0, 20);
  return g;
}

// currentModel finds the model a chat runs among those its harness listed:
// the one the user chose, else the one the harness reports (by ID, or by
// what an alias resolves to, preferring the default), else the default.
// A model missing from the list stands for itself.
function currentModel(models = [], chosen = '', reported = '') {
  const own = id => ({ id, name: id });
  if (chosen) return models.find(m => m.id === chosen) || own(chosen);
  if (reported) {
    const hit = models.find(m => m.id === reported)
      || models.find(m => m.default && m.resolved === reported)
      || models.find(m => m.resolved === reported);
    return hit || own(reported);
  }
  return models.find(m => m.default) || null;
}

// contextUse says how full a chat's context window is, or null until the
// harness has given the window.
function contextUse(info) {
  const c = info?.context;
  if (!c || !(c.window > 0)) return null;
  const used = Math.max(0, Number(c.used) || 0);
  const frac = Math.min(1, used / c.window);
  return { used, window: c.window, frac, pct: used > 0 ? Math.max(1, Math.round(frac * 100)) : 0 };
}

// fmtTokens writes a token count short: 950, 1.5k, 24k, 1.3M.
function fmtTokens(n) {
  const short = (x, unit) => (x < 10 ? String(Number(x.toFixed(1))) : String(Math.round(x))) + unit;
  if (n < 1000) return String(Math.round(n));
  if (n < 999500) return short(n / 1e3, 'k');
  return short(n / 1e6, 'M');
}

// activityLabel says what a working agent is doing right now, for the line
// under the conversation: its newest running tool (a subagent's step
// included), else whether it is writing or thinking.
const ACTIVITY = {
  command: t => 'Running ' + firstLine(t),
  edit: t => 'Editing ' + baseName(t),
  read: t => 'Reading ' + baseName(t),
  search: t => 'Searching for ' + firstLine(t),
  web: t => 'Browsing ' + hostOf(t),
  agent: t => 'Delegating: ' + firstLine(t),
  mcp: t => 'Using ' + firstLine(t),
  todo: () => 'Planning',
};
const firstLine = t => String(t || '').split('\n')[0].trim();
const baseName = t => firstLine(t).split('/').filter(Boolean).pop() || firstLine(t);
const hostOf = t => { try { return new URL(firstLine(t)).hostname } catch { return firstLine(t) } };
const clipLabel = s => s.length > 48 ? s.slice(0, 47).trimEnd() + '…' : s;

function activityLabel(c) {
  const toolLabel = t => clipLabel((ACTIVITY[t.kind] || (x => firstLine(x) || 'Working'))(t.title) || 'Working');
  for (const it of c.items.slice(-8).reverse()) {
    if (it.type === 'tool') {
      const child = [...(it.children || [])].reverse().find(x => x.tool.status === 'running');
      if (child) return toolLabel(child.tool);
      if (it.tool.status === 'running') return toolLabel(it.tool);
    }
    if (it.streaming && it.type === 'text') return 'Writing';
    if (it.streaming && it.type === 'thinking') return 'Thinking';
  }
  return 'Working';
}

// runningAgents lists the subagents still at work, the longest-running
// first. A subagent can outlast the turn that started it.
function runningAgents(c) {
  const out = [];
  const walk = items => {
    for (const it of items) {
      if (it.type !== 'tool') continue;
      if (it.tool.kind === 'agent' && it.tool.status === 'running') out.push(it);
      if (it.children?.length) walk(it.children);
    }
  };
  walk(c.items);
  const at = it => Date.parse(it.since || 0) || 0;
  return out.sort((a, b) => at(a) - at(b));
}

// agentActivity says what a subagent is doing: its newest running step.
function agentActivity(it) {
  const step = [...(it.children || [])].reverse().find(x => x.tool.status === 'running');
  if (step) return clipLabel((ACTIVITY[step.tool.kind] || (x => firstLine(x) || 'Working'))(step.tool.title) || 'Working');
  return it.children?.length ? 'Thinking' : 'Starting';
}

if (typeof module !== 'undefined') module.exports = { signInHost, mdBlocks, mdInline, safeHref, previewURL, parseCommand, commandHints, promptText, CHAT_COLORS, diffFiles, newChat, foldChat, sessionTone, inboxGroups, currentModel, activityLabel, runningAgents, agentActivity, contextUse, fmtTokens };
