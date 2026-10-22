'use strict';

// The chat view: a session run in its harness's structured mode, drawn as a
// conversation from its event stream (see chatlib.js for the reducer and
// the markdown parser). Agent text goes into the page only as text nodes:
// markdown is parsed into data and built with el(), never as HTML.

const HARNESS_LABEL = { claude: 'Claude', codex: 'Codex', shell: 'Shell', pi: 'pi' };
const harnessLabel = h => HARNESS_LABEL[h] || h;
const chatName = s => s.title || `New ${harnessLabel(s.harness)} chat`;

// Each agent's mark, drawn in its own colour wherever it's named: a spark
// for Claude, a hex for Codex, π for pi, a prompt for the shell.
const AGENT_MARK = {
  claude: ['M12 3.5v17', 'M3.5 12h17', 'M6 6l12 12', 'M18 6L6 18'],
  codex: ['M12 2.8l8 4.6v9.2l-8 4.6-8-4.6V7.4z', 'M9 9.5l2.5 2.5L9 14.5', 'M13 14.5h2.5'],
  pi: ['M5 7.5h14', 'M9.5 7.5V18', 'M14.5 7.5v8c0 1.5.8 2.5 2.5 2.5'],
  shell: ['M5 8l4 4-4 4', 'M12 17h7'],
};
// agentMark is an agent's avatar: its mark, or its initials if it has none.
function agentMark(h, cls = '') {
  return el('span.avatar' + cls + '.h-' + h, { title: harnessLabel(h) }, AGENT_MARK[h] ? svg(AGENT_MARK[h], 'mark') : String(h).slice(0, 2));
}

// TOOL_WORD names what a tool does, the same for every agent.
const TOOL_WORD = { command: 'Command', edit: 'Edit', read: 'Read', search: 'Search', web: 'Web', agent: 'Agent', mcp: 'MCP', todo: 'Plan' };
// toolWord is that word for a tool known only by its harness's name.
function toolWord(name = '') {
  const n = String(name);
  if (/^(bash|shell|exec|exec_command|local_shell|run)$/i.test(n)) return 'Command';
  if (/^(edit|multiedit|write|apply_patch|notebookedit)$/i.test(n)) return 'Edit';
  if (/^(webfetch|websearch|web_search)$/i.test(n)) return 'Web';
  if (/^(read|glob|grep|ls)$/i.test(n)) return 'Read';
  if (/^mcp__/.test(n)) return 'MCP';
  return n;
}

const MODE_INFO = {
  ask: { label: 'Ask first', short: 'Ask', desc: 'Asks before editing files or running commands.' },
  edits: { label: 'Auto-edit', short: 'Edits', desc: 'Edits files freely; asks before other commands.' },
  plan: { label: 'Plan only', short: 'Plan', desc: 'Reads and plans, changes nothing.' },
  auto: { label: 'Full auto', short: 'Auto', desc: 'Never asks. The sandbox and broker still apply.' },
};

const SHIP_MESSAGE = 'Ship it: commit the work with a clear message on a feature/, fix/, docs/ or chore/ branch (never main; rename the branch you\'re on if it isn\'t one), push it, and open a pull request. Reply with the PR link.';

// ownBranch is whether new sessions get a branch of their own, in their own
// copy of the repository: yes, unless you've turned it off in the picker.
const ownBranch = () => prefs.get('ownBranch') !== 'off';

Object.assign(ICON, {
  flag: ['M5 21V4', 'M5 4h11l-2 4 2 4H5'],
  more: ['M5 12h.01', 'M12 12h.01', 'M19 12h.01'],
  file: ['M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z', 'M14 3v5h5'],
  search: ['M11 4a7 7 0 1 0 0 14a7 7 0 1 0 0-14', 'M20 20l-4-4'],
  globe: ['M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18', 'M3 12h18', 'M12 3c3 3.5 3 14.5 0 18', 'M12 3c-3 3.5-3 14.5 0 18'],
  agent: ['M12 3l2.2 4.6 5 .7-3.6 3.5.9 5-4.5-2.4-4.5 2.4.9-5L4.8 8.3l5-.7z'],
  list: ['M9 6h11', 'M9 12h11', 'M9 18h11', 'M4 6h.01', 'M4 12h.01', 'M4 18h.01'],
  plug: ['M9 2v6', 'M15 2v6', 'M6 8h12v4a6 6 0 0 1-12 0z', 'M12 18v4'],
  copy: ['M9 9h11v11H9z', 'M5 15H4V4h11v1'],
  fork: ['M6 3v12', 'M18 9a3 3 0 1 0 0-6a3 3 0 1 0 0 6', 'M6 21a3 3 0 1 0 0-6a3 3 0 1 0 0 6', 'M18 9a9 9 0 0 1-9 9'],
  down: ['M12 5v14', 'M6 13l6 6 6-6'],
  brain: ['M12 5a3 3 0 0 0-6 1 3 3 0 0 0-2 5 3 3 0 0 0 3 5 3 3 0 0 0 5 1z', 'M12 5a3 3 0 0 1 6 1 3 3 0 0 1 2 5 3 3 0 0 1-3 5 3 3 0 0 1-5 1z'],
  ship: ['M12 3v12', 'M7 8l5-5 5 5', 'M5 21h14'],
  key: ['M15 7a4 4 0 1 1-3.5 6L5 19.5V22H2v-3l6.5-6.5A4 4 0 0 1 15 7z'],
  alert: ['M12 3l10 18H2z', 'M12 10v4', 'M12 17h.01'],
  branch: ['M6 3v12', 'M18 3a3 3 0 1 0 0 6a3 3 0 1 0 0-6', 'M6 15a3 3 0 1 0 0 6a3 3 0 1 0 0-6', 'M18 9a9 9 0 0 1-9 9'],
  image: ['M4 5h16v14H4z', 'M4 16l5-5 4 4 3-3 4 4', 'M15.5 8.5h.01'],
  clip: ['M20 11.5l-7.8 7.8a5 5 0 0 1-7.1-7.1l8.1-8.1a3.3 3.3 0 0 1 4.7 4.7l-8.1 8.1a1.7 1.7 0 0 1-2.4-2.4l7.4-7.4'],
});

const TOOL_ICON = { command: 'term', edit: 'pencil', read: 'file', search: 'search', web: 'globe', agent: 'agent', todo: 'list', mcp: 'plug', other: 'bolt' };

// plainOut is a command's output as text: colour codes and other escapes
// are dropped, and any control character left shows as <U+XXXX>.
const plainOut = s => reveal(String(s ?? '').replace(/\x1b\[[0-9;?]*[ -\/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\r(?=\n)/g, ''));

// --- markdown to elements ---------------------------------------------------

function inlineNodes(runs) {
  return runs.map(r => {
    switch (r.t) {
      case 'text': return r.text;
      case 'br': return el('br');
      case 'code': return el('code', null, r.text);
      case 'b': return el('strong', null, inlineNodes(r.children));
      case 'i': return el('em', null, inlineNodes(r.children));
      case 's': return el('del', null, inlineNodes(r.children));
      case 'a': return el('a', { href: r.href, target: '_blank', rel: 'noopener noreferrer' }, inlineNodes(r.children));
    }
    return null;
  });
}

function blockNodes(blocks) {
  return blocks.map(b => {
    switch (b.t) {
      case 'p': return el('p', null, inlineNodes(mdInline(b.text)));
      case 'h': return el('h' + Math.min(6, b.level + 2), null, inlineNodes(mdInline(b.text)));
      case 'hr': return el('hr');
      case 'quote': return el('blockquote', null, blockNodes(b.blocks));
      case 'code': return codeBlock(b.text, b.lang);
      case 'ul':
      case 'ol': return el(b.t, b.t === 'ol' && b.start !== 1 ? { start: String(b.start) } : null, b.items.map(it =>
        el('li' + (it.check != null ? '.task' : ''), null,
          it.check != null ? el('span.check' + (it.check ? '.on' : ''), { 'aria-label': it.check ? 'done' : 'not done' }, it.check ? svg(ICON.check) : null) : null,
          inlineNodes(mdInline(it.text)), blockNodes(it.children))));
      case 'table': return el('div.md-table', null, el('table', null,
        el('thead', null, el('tr', null, b.head.map((h, i) => el('th' + (b.align[i] ? '.' + b.align[i] : ''), null, inlineNodes(mdInline(h)))))),
        el('tbody', null, b.rows.map(r => el('tr', null, r.map((c, i) => el('td' + (b.align[i] ? '.' + b.align[i] : ''), null, inlineNodes(mdInline(c)))))))));
    }
    return null;
  });
}

function markdown(text) {
  return el('div.md', null, blockNodes(mdBlocks(text)));
}

function codeBlock(text, lang) {
  return el('div.codeblock', null,
    el('div.code-head', null, el('span', null, lang || 'code'),
      el('button.code-copy', { type: 'button', onclick: () => copyText(text) }, svg(ICON.copy), 'Copy')),
    el('pre', null, el('code', null, text)));
}

// --- diffs ------------------------------------------------------------------

// diffView draws a unified diff, one collapsible card per file. Long files
// show their first lines and a button for the rest.
function diffView(diff, { open = 3, limit = 400 } = {}) {
  const files = diffFiles(diff);
  // Diffs are agent text a person approves or ships: characters that would
  // hide or reorder code (bidi overrides, zero-width) are shown, not obeyed.
  if (!files.length) return el('pre.diff-raw', null, reveal(diff));
  return el('div.diffs', null, files.map((f, i) => {
    const rows = [];
    let shown = 0;
    const lines = el('div.diff-lines');
    const draw = max => {
      lines.replaceChildren();
      shown = 0;
      for (const h of f.hunks) {
        if (shown >= max) break;
        lines.append(el('div.dl.hunk', null, el('span.ln'), el('span.ln'), el('span.dt', null, h.context ? '@@ ' + reveal(h.context) : '⋯')));
        for (const l of h.lines) {
          if (shown++ >= max) break;
          lines.append(el('div.dl.' + l.k, null,
            el('span.ln', null, l.o != null ? String(l.o) : ''),
            el('span.ln', null, l.n != null ? String(l.n) : ''),
            el('span.dt', null, (l.k === 'add' ? '+' : l.k === 'del' ? '−' : ' ') + reveal(l.text))));
        }
      }
      const total = f.hunks.reduce((n, h) => n + h.lines.length, 0);
      if (total > max) lines.append(el('button.btn.ghost.sm.more-lines', { type: 'button', onclick: () => draw(Infinity) }, `Show ${total - max} more lines`));
    };
    draw(limit);
    rows.push(lines);
    return el('details.diff-file', { open: i < open },
      el('summary', null,
        svg(ICON.chevron, 'chev'),
        el('span.df-path', null, reveal(f.path)),
        f.isNew ? el('span.df-tag', null, 'new') : null,
        f.isDeleted ? el('span.df-tag.del', null, 'deleted') : null,
        el('span.grow'),
        el('span.plus', null, '+' + f.added), el('span.minus', null, '−' + f.removed)),
      el('div.diff-body', null, rows));
  }));
}

// --- the page -----------------------------------------------------------------

// sunMark is the working indicator, the halo: a small sun inside a ring
// broken into three arcs, yellow, orange and pale gold, that chase round at
// different speeds, stretching and shrinking, so the ring keeps breaking
// and nearly closing.
function sunMark() {
  const ns = 'http://www.w3.org/2000/svg';
  const s = document.createElementNS(ns, 'svg');
  s.setAttribute('viewBox', '16.5 12.5 12 12');
  s.setAttribute('class', 'sunwork');
  s.setAttribute('aria-hidden', 'true');
  for (const [r, cls] of [[2, 'core'], [5, 'arc y'], [5, 'arc o'], [5, 'arc g']]) {
    const c = document.createElementNS(ns, 'circle');
    c.setAttribute('cx', '22.5');
    c.setAttribute('cy', '18.5');
    c.setAttribute('r', String(r));
    c.setAttribute('class', cls);
    s.append(c);
  }
  return s;
}

// contextRing is the chat's session button: a ring that fills as the
// conversation uses up its context window.
function contextRing() {
  const ns = 'http://www.w3.org/2000/svg';
  const s = document.createElementNS(ns, 'svg');
  s.setAttribute('viewBox', '0 0 24 24');
  s.setAttribute('class', 'ctx-ring');
  s.setAttribute('aria-hidden', 'true');
  const circle = cls => {
    const c = document.createElementNS(ns, 'circle');
    c.setAttribute('cx', '12');
    c.setAttribute('cy', '12');
    c.setAttribute('r', '8');
    c.setAttribute('pathLength', '100');
    c.setAttribute('class', cls);
    s.append(c);
    return c;
  };
  circle('track');
  const fill = circle('fill');
  const set = use => {
    const pct = use ? use.frac * 100 : 0;
    fill.setAttribute('stroke-dasharray', `${pct} 100`);
    s.classList.toggle('none', !use || use.used === 0);
    s.classList.toggle('warn', !!use && use.frac >= 0.6 && use.frac < 0.85);
    s.classList.toggle('full', !!use && use.frac >= 0.85);
  };
  set(null);
  return { node: s, set };
}

// prepareFile reads a file to attach. An image is decoded once: scaled
// down if it's too large for the models (a screenshot staying PNG where it
// can, a photo becoming JPEG, WebP becoming one the server can check), and
// given a thumbnail for its previews unless it's small enough to be one.
async function prepareFile(file) {
  let blob = file;
  let name = file.name || 'file';
  let thumb = '';
  const bmp = scalable(file.type) ? await createImageBitmap(file).catch(() => null) : null;
  if (bmp) {
    try {
      blob = await fitImage(file, bmp).catch(() => file);
      if (blob !== file && blob.type === 'image/jpeg') name = name.replace(/\.[^.]*$/, '') + '.jpg';
      thumb = await makeThumb(bmp, blob).catch(() => '');
    } finally { bmp.close() }
  }
  // url's base64 is what's sent.
  return { name, size: blob.size, image: /^image\/(png|jpeg|gif|webp)$/.test(blob.type), url: await dataURL(blob), thumb };
}

const dataURL = blob => new Promise((ok, fail) => {
  const r = new FileReader();
  r.onload = () => ok(r.result);
  r.onerror = () => fail(r.error);
  r.readAsDataURL(blob);
});

const base64 = url => url.slice(url.indexOf(',') + 1);

// drawScaled draws an image at w×h, on white for a format without
// transparency.
function drawScaled(bmp, w, h, white) {
  const c = document.createElement('canvas');
  c.width = w;
  c.height = h;
  const g = c.getContext('2d');
  if (white) { g.fillStyle = '#fff'; g.fillRect(0, 0, w, h) }
  g.drawImage(bmp, 0, 0, w, h);
  return c;
}

const encode = (c, type, q) => new Promise((ok, fail) => c.toBlob(b => b ? ok(b) : fail(new Error('encode')), type, q));

// encodeFit is the image as PNG if it may have transparency and fits in
// max bytes, else as JPEG.
async function encodeFit(bmp, w, h, type, max, quality) {
  if (/^image\/(png|webp)$/.test(type)) {
    const png = await encode(drawScaled(bmp, w, h, false), 'image/png');
    if (png.size <= max) return png;
  }
  return encode(drawScaled(bmp, w, h, true), 'image/jpeg', quality);
}

async function fitImage(file, bmp) {
  const { w, h } = fitSize(bmp.width, bmp.height, ATTACH.imageSide);
  if (/^image\/(png|jpeg)$/.test(file.type) && w === bmp.width && file.size <= ATTACH.imageBytes) return file;
  return encodeFit(bmp, w, h, file.type, ATTACH.imageBytes, 0.85);
}

// makeThumb is a small copy of an image for its previews, or '' if the
// image is as small as a thumbnail or its thumbnail comes out too large.
async function makeThumb(bmp, blob) {
  const { w, h } = thumbSize(bmp.width, bmp.height);
  if (w === bmp.width && blob.size <= ATTACH.thumb) return '';
  const t = await encodeFit(bmp, w, h, blob.type, ATTACH.thumb, 0.8);
  return t.size <= ATTACH.thumb ? dataURL(t) : '';
}

async function renderChat(seq, d) {
  const id = d.session.id;
  let session = d.session, project = d.project;
  const chat = newChat();
  const nodes = new Map(); // item id → its element
  const openAgents = new Set(); // subagent cards opened, kept open as they redraw
  const modes = state.meta?.chat?.[session.harness] || ['ask'];
  // The prompts saved in Settings, for /prompt.
  let prompts = [];

  const title = el('span.title');
  const sub = el('span.sub');
  const mark = el('span.chat-mark');
  const log = el('div.chat-log');
  // The agents chip shows while subagents run, which can outlast the turn;
  // it takes you to the newest.
  const agentsBtn = el('button.wagents', { type: 'button', hidden: true, onclick: () => showAgent() });
  const working = el('div.chat-working', { hidden: true }, sunMark(),
    el('span.wtext', { role: 'status' }, 'Working'), el('span.wtime'), agentsBtn);
  const jump = el('button.jump', { type: 'button', hidden: true, onclick: () => toBottom(true) }, svg(ICON.down), 'Latest');
  const scroller = el('div.chat-scroll', null, el('div.chat-col', null, log, working));
  const dock = el('div.dock');
  const input = el('textarea.composer-input', {
    rows: '1', 'aria-label': 'Message', enterkeyhint: coarse() ? 'enter' : 'send', autocapitalize: 'sentences', spellcheck: 'true',
  });
  // With nothing written, the send button attaches files instead.
  const sendBtn = el('button.send-btn', {
    type: 'submit', 'aria-label': 'Send', onmousedown: e => e.preventDefault(),
    onclick: e => { if (sendBtn.classList.contains('attach')) { e.preventDefault(); picker.click() } },
  });
  const modeBtn = el('button.mode-chip', { type: 'button', onclick: () => modeMenu() });
  const picker = el('input', { type: 'file', multiple: true, hidden: true, onchange: () => { addFiles([...picker.files]); picker.value = '' } });
  const tray = el('div.attach-tray', { hidden: true });
  // What a screen reader hears of the tray: a line, not every tile again.
  const trayStatus = el('span.sr-only', { role: 'status' });
  const composer = el('form.composer', { onsubmit: e => { e.preventDefault(); submit() } }, modeBtn, input, sendBtn, picker);
  const ended = el('div.ended-bar', { hidden: true });
  const hints = el('div.cmd-hints', { hidden: true });
  const footer = el('div.chat-foot', null, dock, hints, tray, trayStatus, composer, ended);
  const goalBar = el('div.chat-goal', { hidden: true });
  const ring = contextRing();
  // The ring is how full the context window is, and says so in figures.
  const ringPct = el('span.ctx-pct');
  const ringBtn = el('button.icon-btn.bare.ctx-btn', { onclick: e => sessionMenu(e.currentTarget), 'aria-label': 'Context and session' }, ring.node, ringPct);

  const back = () => history.length > 1 ? history.back() : navigate('/');
  const page = el('div.chat-page', null,
    el('div.chat-bar', null,
      el('button.icon-btn.bare', { onclick: back, 'aria-label': 'Back' }, svg(ICON.back)),
      mark, el('div.chat-title', null, title, sub),
      el('button.icon-btn.bare', { onclick: () => openReview(session.project, id), 'aria-label': 'Review changes', title: 'Review changes' }, svg(ICON.branch)),
      el('button.icon-btn.bare', { onclick: () => openTerminal(), 'aria-label': 'Open in terminal', title: 'Open in terminal' }, svg(ICON.term)),
      ringBtn),
    goalBar, scroller, jump, footer);
  $('#app').replaceChildren(page);
  const viewport = trackViewport();
  viewport();
  const onViewport = () => { const stick = nearBottom(); viewport(); if (stick) toBottom() };
  window.visualViewport?.addEventListener('resize', onViewport);
  window.visualViewport?.addEventListener('scroll', onViewport);

  const v = { closed: false, es: null, timer: 0 };
  // startNode greets a chat with nothing in it yet; the first item replaces it.
  let startNode = null;
  state.chat = v;
  // Ends listeners outside the page, which outlive it.
  const unlisten = new AbortController();
  v.close = () => {
    v.closed = true;
    unlisten.abort();
    v.es?.close();
    clearInterval(v.timer);
    window.visualViewport?.removeEventListener('resize', onViewport);
    window.visualViewport?.removeEventListener('scroll', onViewport);
    document.documentElement.style.removeProperty('--vvh');
    document.documentElement.style.removeProperty('--vv-top');
    document.removeEventListener('visibilitychange', onVisible);
  };

  // --- header and state ---
  const head = () => {
    crumb(project, chatName(session));
    title.textContent = chatName(session);
    for (const c of CHAT_COLORS) page.classList.toggle('sc-' + c, session.color === c);
    goalBar.hidden = !session.goal;
    goalBar.replaceChildren(svg(ICON.flag), el('span.goal-text', null, session.goal || ''),
      el('button.icon-btn.bare.sm', { type: 'button', 'aria-label': 'Unpin the goal', title: 'Unpin the goal', onclick: () => runCommand({ cmd: 'goal', arg: '' }) }, svg(ICON.x)));
    // Who, on what, where: agent, model, project, branch. The same on a
    // phone and a desktop; the model opens the model menu.
    mark.replaceChildren(agentMark(session.harness, '.sm'));
    const model = modelLabel(chatModel());
    const dot = () => el('span.sep', null, '·');
    const models = session.status === 'running' && chat.info.models?.length;
    sub.replaceChildren();
    append(sub, [
      el('span.sub-agent', null, harnessLabel(session.harness)),
      model ? [dot(), models ? el('button.sub-model', { type: 'button', title: 'Change the model', onclick: e => modelMenu(e.currentTarget) }, model) : el('span.sub-model', null, model)] : null,
      dot(), el('span.proj.tint-' + (project.tint ?? 0), null, el('span.pdot'), project.name),
      dot(), session.worktree
        ? el('span.sub-branch', { title: session.worktree_gone ? 'Its copy of the repository was tidied away; the branch stays' : 'Its own branch, in its own copy of the repository' }, svg(ICON.branch), el('span.bname', null, session.branch || 'its own branch'))
        : el('span.sub-branch', { title: 'Works in the project\'s shared workspace' }, svg(ICON.branch), el('span.bname', null, 'workspace'))]);
    const m = MODE_INFO[session.perm_mode] || MODE_INFO.ask;
    modeBtn.replaceChildren(svg(ICON.shield), el('span', null, m.short));
    modeBtn.title = m.label + ': ' + m.desc;
    const use = contextUse(chat.info);
    ring.set(use);
    ringPct.textContent = use?.pct ? use.pct + '%' : '';
    ringBtn.title = use ? `Context ${use.pct}% used: ${fmtTokens(use.used)} of ${fmtTokens(use.window)} tokens` : 'Context and session';
    const over = session.status !== 'running';
    composer.hidden = over;
    ended.hidden = !over;
    if (over) ended.replaceChildren(el('span', null, 'This session has ended.'),
      session.resume ? el('button.btn.sm', { type: 'button', onclick: () => runCommand({ cmd: 'fork', arg: '' }) }, svg(ICON.fork), 'Fork') : null,
      el('button.btn.sm', { type: 'button', onclick: () => newChatIn(project.name, session.harness) }, svg(ICON.plus), 'New chat'));
    syncComposer();
  };
  v.refreshMeta = debounce(async () => {
    try {
      const x = await api('GET', '/api/sessions/' + id);
      if (v.closed) return;
      session = x.session;
      project = x.project;
      head();
    } catch { }
  }, 300);

  // --- scrolling ---
  const nearBottom = () => scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 80;
  const toBottom = smooth => {
    scroller.scrollTo({ top: scroller.scrollHeight, behavior: smooth ? 'smooth' : 'instant' });
    jump.hidden = true;
  };
  scroller.addEventListener('scroll', () => { if (nearBottom()) jump.hidden = true }, { passive: true });

  // --- drawing items ---
  const drawItem = it => {
    switch (it.type) {
      case 'user': return userBubble(it);
      case 'text': return el('div.msg.agent' + (it.streaming ? '.streaming' : ''), null, markdown(it.text));
      case 'thinking': return thinkingRow(it);
      case 'tool': return toolRow(it);
      case 'ask': return askLine(it);
      case 'error': return errorCard(it);
      case 'notice': return el('div.notice', null, it.text);
      case 'show': return showCard(it);
      case 'preview': return previewCard(it);
    }
    return el('div');
  };
  // Tool rows that follow each other share a group, drawn as one block.
  const place = (it, node) => {
    const prev = log.lastElementChild;
    if (it.type === 'tool') {
      if (prev?.classList.contains('tools')) prev.append(node);
      else log.append(el('div.tools', null, node));
    } else log.append(node);
  };
  const dirty = new Set();
  let frame = 0, stick = true;
  const redraw = it => {
    dirty.add(it);
    if (!frame) frame = requestAnimationFrame(flush);
  };
  const flush = () => {
    frame = 0;
    for (const it of dirty) {
      const old = nodes.get(it.id);
      if (!old) continue;
      const n = drawItem(it);
      old.replaceWith(n);
      nodes.set(it.id, n);
    }
    dirty.clear();
    after();
  };
  const after = () => {
    drawWorking();
    drawDock();
    if (stick) toBottom();
    else if (!nearBottom()) jump.hidden = false;
  };

  const apply = e => {
    const r = foldChat(chat, e);
    if (!r) return;
    if (r.added) {
      const n = drawItem(r.added);
      nodes.set(r.added.id, n);
      place(r.added, n);
    }
    if (r.changed && nodes.has(r.changed.id)) redraw(r.changed);
    if (r.also && nodes.has(r.also.id)) redraw(r.also);
    if (r.added && startNode) { startNode.remove(); startNode = null }
    if (e.kind === 'info') head();
    if (e.kind === 'turn' && e.turn?.state !== 'working') { markSeen(); v.refreshMeta() }
    if (e.kind === 'tool' && e.tool?.kind === 'agent' && e.tool.status !== 'running' && !chat.working) { markSeen(); v.refreshMeta() }
    if (e.kind === 'turn' && e.turn?.state === 'working') v.refreshMeta();
    if (e.kind === 'ask' || e.kind === 'answered' || e.kind === 'login') v.refreshMeta();
  };

  // Events that arrive together draw together.
  let batch = [], batching = 0;
  const enqueue = e => {
    batch.push(e);
    if (!batching) batching = requestAnimationFrame(() => {
      batching = 0;
      stick = nearBottom() || !log.firstChild;
      const b = batch;
      batch = [];
      for (const x of b) apply(x);
      if (!frame) after();
    });
  };

  // --- the live stream ---
  const connect = () => {
    if (v.closed) return;
    const es = new EventSource(`/api/sessions/${encodeURIComponent(id)}/events` + (chat.seq ? `?after=${chat.seq}` : ''));
    v.es = es;
    es.onmessage = e => { try { enqueue(JSON.parse(e.data)) } catch { } };
    es.onerror = () => {
      // EventSource retries by itself, from the last id; if it gave up,
      // start again from what we have.
      if (es.readyState === EventSource.CLOSED && !v.closed) setTimeout(connect, 2000);
    };
  };

  // --- seen ---
  const markSeen = debounce(() => {
    if (document.visibilityState === 'visible' && !v.closed) api('POST', `/api/sessions/${id}/seen`, {}).catch(() => { });
  }, 800);
  const onVisible = () => { if (document.visibilityState === 'visible') markSeen() };
  document.addEventListener('visibilitychange', onVisible);

  // --- composer ---
  const grow = () => { input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, 180) + 'px' };
  // A question with one part is answered here, in your own words; one with
  // several has a field per part on its card, so this one steps aside.
  const oneQuestion = ask => ask?.ask.kind === 'question' && (ask.ask.questions || []).length === 1 && !ask.ask.questions[0].multi;
  const syncComposer = () => {
    const ask = [...chat.asks.values()][0];
    composer.hidden = session.status !== 'running' || (ask?.ask.kind === 'question' && !oneQuestion(ask));
    input.placeholder = oneQuestion(ask) ? 'Or answer in your own words…' : ask ? 'Or tell it what to do instead…' : chat.working ? 'Add to what it\'s doing…' : `Message ${harnessLabel(session.harness)}`;
    const action = composerAction({ working: chat.working, text: input.value.trim(), files: pending.length, ask: !!ask });
    sendBtn.classList.toggle('stop', action === 'stop');
    sendBtn.classList.toggle('attach', action === 'attach');
    sendBtn.setAttribute('aria-label', { stop: 'Stop', attach: 'Attach files', send: 'Send' }[action]);
    sendBtn.title = action === 'attach' ? 'Attach files' : '';
    sendBtn.replaceChildren(svg({ stop: ICON.stop, attach: ICON.clip, send: ICON.send }[action]));
    sendBtn.disabled = action === 'send' && !input.value.trim() && !pending.length;
    tray.hidden = !pending.length || composer.hidden;
  };
  input.addEventListener('input', () => { grow(); syncComposer(); saveDraft(); showHints() });

  // --- attachments: picked, pasted or dropped; images scaled down first ---
  let pending = []; // { name, size, image, url, thumb } or, while it's read, { name, busy }
  let uploading = []; // what's being sent, which can't be removed
  // drawTray redraws the attachments, keeping focus on the remove button it
  // was on; after a removal, focus goes to the one now in that place.
  const drawTray = removedAt => {
    const was = [...tray.querySelectorAll('.att-x')].indexOf(document.activeElement);
    tray.replaceChildren(...pending.map(p => {
      const name = reveal(p.name);
      const remove = uploading.includes(p) ? null : el('button.att-x', {
        type: 'button', 'aria-label': 'Remove ' + name, title: 'Remove',
        onclick: () => {
          const i = pending.indexOf(p);
          pending = pending.filter(x => x !== p);
          trayStatus.textContent = 'Removed ' + name;
          drawTray(i);
        },
      }, svg(ICON.x));
      if (p.busy) return el('div.att.att-file', { 'aria-busy': 'true', title: name }, el('span.spinner'), el('span.att-name', null, name), el('span.sr-only', null, ', reading'), remove);
      if (p.image) return el('div.att.att-img', { title: name }, el('img', { src: p.thumb || p.url, alt: name }), remove);
      return el('div.att.att-file', { title: name }, svg(ICON.file), el('span.att-name', null, name), el('span.att-size', null, fmtSize(p.size)), remove);
    }));
    // Once files wait to go, the button sends; more come from here.
    if (pending.length < ATTACH.files && !uploading.length) {
      tray.append(el('button.att-add', { type: 'button', 'aria-label': 'Attach more files', title: 'Attach more files', onclick: () => picker.click() }, svg(ICON.plus)));
    }
    syncComposer();
    const xs = tray.querySelectorAll('.att-x');
    const at = removedAt ?? was;
    if (at < 0) return;
    if (xs.length) (xs[at] || xs[xs.length - 1]).focus();
    // Not on a phone, where focusing the input opens the keyboard.
    else if (removedAt !== undefined && !coarse()) input.focus();
  };
  // Files are read one at a time, across batches: decoding several large
  // photos at once can use up a phone's memory.
  let reading = Promise.resolve();
  const read = f => (reading = reading.then(() => prepareFile(f)).catch(() => null));
  const addFiles = async files => {
    if (!files.length) return;
    const early = readProblem(pending, files);
    if (early) { toast(early, 'error'); return }
    const slots = files.map(f => ({ name: f.name || 'file', busy: true }));
    pending = [...pending, ...slots];
    trayStatus.textContent = 'Reading ' + (files.length === 1 ? reveal(slots[0].name) : files.length + ' files');
    drawTray();
    const refused = [];
    for (const [i, f] of files.entries()) {
      const slot = slots[i];
      const ready = await read(f);
      if (!pending.includes(slot)) continue; // removed while it was read
      // Files join while they fit; the rest are named.
      const name = reveal(slot.name);
      const problem = ready ? attachProblem(pending.filter(p => p !== slot), [ready]) : `Couldn't read ${name}.`;
      if (problem) refused.push(problem.includes(name) ? problem : `${name} was left out: ${problem[0].toLowerCase()}${problem.slice(1)}`);
      pending = pending.flatMap(p => p !== slot ? [p] : problem ? [] : [ready]);
      drawTray();
    }
    const n = pending.filter(p => !p.busy).length;
    trayStatus.textContent = n + (n === 1 ? ' file' : ' files') + ' attached';
    if (refused.length) toast(refused.join(' '), 'error');
  };
  input.addEventListener('paste', e => {
    const files = [...(e.clipboardData?.files || [])];
    // Text with a picture of itself, as from a spreadsheet, pastes as text.
    if (!files.length || e.clipboardData.types.includes('text/plain')) return;
    e.preventDefault();
    addFiles(files);
  });
  // Files dragged anywhere stay in the app, even with nowhere to attach them.
  let drags = 0;
  const fileDrag = e => e.dataTransfer?.types.includes('Files');
  const on = (type, fn) => document.addEventListener(type, fn, { signal: unlisten.signal });
  on('dragenter', e => { if (fileDrag(e)) { drags++; page.classList.toggle('dropping', !composer.hidden) } });
  on('dragleave', e => { if (fileDrag(e) && --drags <= 0) { drags = 0; page.classList.remove('dropping') } });
  on('dragover', e => { if (fileDrag(e)) e.preventDefault() });
  on('drop', e => {
    if (!fileDrag(e)) return;
    e.preventDefault();
    drags = 0;
    page.classList.remove('dropping');
    if (!composer.hidden) addFiles([...e.dataTransfer.files]);
  });

  // --- Skybreaker's own commands: /rename, /color, /goal, /clear, /fork, /compact, /prompt ---
  api('GET', '/api/prompts').then(r => { prompts = r.prompts || []; showHints() }).catch(() => { });
  const edit = async body => {
    session = await api('PATCH', '/api/sessions/' + encodeURIComponent(id), body);
    head();
  };
  const runCommand = async c => {
    try {
      if (c.cmd === 'rename') {
        if (!c.arg) { toast('Give the new name: /rename <title>'); return false }
        await edit({ title: c.arg });
      } else if (c.cmd === 'color') {
        if (c.arg && !CHAT_COLORS.includes(c.arg)) { toast('Colours: ' + CHAT_COLORS.join(', ')); return false }
        await edit({ color: c.arg });
      } else if (c.cmd === 'goal') {
        await edit({ goal: c.arg });
        if (c.arg) await send('Goal for this chat: ' + c.arg);
      } else if (c.cmd === 'prompt') {
        if (!prompts.length) { toast('No prompts yet. Add them in Settings.'); return false }
        const text = promptText(c.arg, prompts);
        if (text === null) { toast('Prompts: ' + prompts.map(p => p.name).join(', ')); return false }
        await api('POST', `/api/sessions/${id}/messages`, { text });
        stick = true;
      } else if (c.cmd === 'compact') {
        await api('POST', `/api/sessions/${id}/compact`, {});
        stick = true;
      } else if (c.cmd === 'fork') {
        const f = await api('POST', `/api/sessions/${id}/fork`, {});
        navigate('/s/' + f.id);
      } else if (c.cmd === 'clear') {
        if (chat.working) { toast('Stop the agent first, then clear.'); return false }
        await clearChat();
      }
      return true;
    } catch (e) { toast(e.message, 'error'); return false }
  };
  // clearChat starts a fresh chat like this one (same agent, mode, model
  // and branch) and ends this one, which stays readable.
  const clearChat = async () => {
    // A chat on its own branch carries on in it.
    const n = await api('POST', `/api/projects/${project.name}/sessions`, session.worktree ? { harness: session.harness, continues: id } : { harness: session.harness });
    const mode = session.perm_mode || 'ask';
    if (mode !== 'ask') await api('POST', `/api/sessions/${n.id}/mode`, { mode }).catch(() => { });
    if (session.chosen_model) await api('POST', `/api/sessions/${n.id}/model`, { model: session.chosen_model }).catch(() => { });
    await api('DELETE', '/api/sessions/' + id);
    navigate('/s/' + n.id);
  };
  const clearInput = () => {
    input.value = '';
    prefs.set(draftKey, '');
    grow();
    showHints();
    syncComposer();
  };
  const showHints = () => {
    const hs = commandHints(input.value, prompts);
    hints.hidden = !hs.length;
    hints.replaceChildren(...hs.map(h => h.color
      ? el('button.cmd-hint.swatch-hint.sc-' + h.color, { type: 'button', onmousedown: e => e.preventDefault(),
        onclick: async () => { if (await runCommand({ cmd: 'color', arg: h.color })) clearInput() } }, el('span.swatch'), h.color)
      : h.prompt
        ? el('button.cmd-hint', { type: 'button', onmousedown: e => e.preventDefault(),
          onclick: async () => { if (await runCommand({ cmd: 'prompt', arg: h.prompt })) clearInput() } },
          el('b', null, h.prompt), el('span', null, h.desc))
        : el('button.cmd-hint', { type: 'button', onmousedown: e => e.preventDefault(),
          onclick: () => complete(h) },
          el('b', null, h.usage), el('span', null, h.desc))));
  };
  // complete fills in a hint, leaving you to add to it before sending.
  const complete = h => {
    input.value = h.color ? '/color ' + h.color : h.prompt ? '/prompt ' + h.prompt + ' ' : '/' + h.cmd + ' ';
    input.focus();
    grow();
    showHints();
    syncComposer();
    saveDraft();
  };
  input.addEventListener('keydown', e => {
    if (e.key === 'Tab' && !e.shiftKey && !hints.hidden) {
      const h = commandHints(input.value, prompts)[0];
      if (h) { e.preventDefault(); complete(h) }
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !coarse()) { e.preventDefault(); submit() }
  });
  const draftKey = 'draft.' + id;
  const saveDraft = debounce(() => prefs.set(draftKey, input.value), 300);
  input.value = prefs.get(draftKey) || '';
  grow();

  let sending = false;
  const submit = async () => {
    if (sending) return;
    const text = input.value.trim();
    // A command runs as it would without files, which stay attached;
    // except one that leaves this chat, which would lose them.
    const command = parseCommand(text);
    if (command && pending.length && (command.cmd === 'clear' || command.cmd === 'fork')) {
      toast('Send or remove the attached files first.');
      return;
    }
    if (!command && pending.length) return sendFiles(text);
    if (!text) {
      if (chat.working) interrupt();
      return;
    }
    if (command) {
      sending = true;
      if (await runCommand(command)) clearInput();
      sending = false;
      return;
    }
    sending = true;
    sendBtn.disabled = true;
    try {
      const ask = [...chat.asks.values()][0];
      if (oneQuestion(ask)) {
        await api('POST', `/api/sessions/${id}/answer`, { ask: ask.id, allow: true, answers: { [ask.ask.questions[0].id]: [text] } });
      } else if (ask && ask.ask.kind === 'permission') {
        // Typing while it waits on an approval says no, and why.
        await api('POST', `/api/sessions/${id}/answer`, { ask: ask.id, allow: false, message: text });
      } else {
        await api('POST', `/api/sessions/${id}/messages`, { text });
      }
      input.value = '';
      prefs.set(draftKey, '');
      grow();
      stick = true;
      toBottom(true);
    } catch (e) { toast(e.message, 'error') } finally {
      sending = false;
      syncComposer();
    }
  };
  const sendFiles = async text => {
    if (pending.some(p => p.busy)) { toast('Still reading the files. Send again in a moment.'); return }
    if (chat.asks.size) { toast('Answer the agent first, then send the files.'); return }
    const sent = pending;
    const problem = attachProblem([], sent);
    if (problem) { toast(problem, 'error'); return }
    sending = true;
    uploading = sent;
    drawTray();
    sendBtn.disabled = true;
    sendBtn.setAttribute('aria-label', 'Sending');
    sendBtn.setAttribute('aria-busy', 'true');
    sendBtn.replaceChildren(el('span.spinner'));
    try {
      await api('POST', `/api/sessions/${id}/messages`, { text, files: sent.map(p => ({ name: p.name, data: base64(p.url), thumb: p.thumb ? base64(p.thumb) : undefined })) });
      // Files added, or words typed, while this one went stay for the next.
      pending = pending.filter(p => !sent.includes(p));
      if (input.value.trim() === text) {
        input.value = '';
        prefs.set(draftKey, '');
        grow();
      }
      trayStatus.textContent = 'Sent';
      stick = true;
      toBottom(true);
    } catch (e) { toast(e.message, 'error') } finally {
      sending = false;
      uploading = [];
      sendBtn.removeAttribute('aria-busy');
      drawTray();
    }
  };
  const interrupt = async () => {
    try { await api('POST', `/api/sessions/${id}/interrupt`, {}) } catch (e) { toast(e.message, 'error') }
  };
  const send = async text => {
    try {
      await api('POST', `/api/sessions/${id}/messages`, { text });
      stick = true;
    } catch (e) { toast(e.message, 'error') }
  };
  v.send = send;

  const modeMenu = () => {
    menu(modeBtn, modes.map(m => ({
      label: MODE_INFO[m]?.label || m, desc: MODE_INFO[m]?.desc, on: (session.perm_mode || 'ask') === m,
      run: async () => {
        try {
          await api('POST', `/api/sessions/${id}/mode`, { mode: m });
          session.perm_mode = m;
          head();
        } catch (e) { toast(e.message, 'error') }
      },
    })));
  };

  // chatModel is the model this chat runs, from the list its harness gave.
  const chatModel = () => currentModel(chat.info.models, session.chosen_model, chat.info.model || session.model);

  const modelMenu = anchor => {
    const cur = chatModel();
    menu(anchor, (chat.info.models || []).map(m => ({
      label: m.name || m.id, desc: m.description, on: cur?.id === m.id,
      run: async () => {
        try {
          await api('POST', `/api/sessions/${id}/model`, { model: m.id });
          session.chosen_model = m.id;
          head();
        } catch (e) { toast(e.message, 'error') }
      },
    })));
  };

  // sessionMenu is the ring's: how full the context is, and what to do
  // about it.
  const sessionMenu = anchor => {
    const running = session.status === 'running';
    const use = contextUse(chat.info);
    menu(anchor, [
      { note: use ? `${use.pct}% of context used` : 'Context not reported yet',
        desc: use ? `${fmtTokens(use.used)} of ${fmtTokens(use.window)} tokens` : 'It shows after the agent\'s first reply.' },
      running && session.resume ? { label: 'Compact', desc: 'Summarise the conversation to free up context', run: () => runCommand({ cmd: 'compact', arg: '' }) } : null,
      { label: 'New chat', desc: 'Start fresh; this one stays as it is', run: () => newChatIn(project.name, session.harness) },
      session.resume ? { label: 'Fork', desc: 'Copy this conversation into a new chat', run: () => runCommand({ cmd: 'fork', arg: '' }) } : null,
      running && chat.info.models?.length ? { label: 'Model', desc: chatModel()?.name, run: () => modelMenu(anchor) } : null,
      { label: 'Project ' + project.name, run: () => navigate('/p/' + project.name) },
      session.status === 'running' ? {
        label: 'End session', danger: true, run: async () => {
          if (!confirm('End this session? The conversation stays here to read.')) return;
          try { await api('DELETE', '/api/sessions/' + id); v.refreshMeta() } catch (e) { toast(e.message, 'error') }
        },
      } : null,
    ].filter(Boolean));
  };

  const openTerminal = async () => {
    try {
      toast('Opening the terminal…');
      const t = await api('POST', `/api/sessions/${id}/terminal`, {});
      navigate('/s/' + t.id);
    } catch (e) { toast(e.status === 409 ? 'Stop the agent first, then open the terminal.' : e.message, 'error') }
  };

  // --- items ---
  function userBubble(it) {
    const long = it.text.split('\n').length > 14 || it.text.length > 1400;
    const body = it.text ? el('div.bubble' + (long ? '.clamped' : ''), null, it.text) : null;
    return el('div.msg.user', null, it.attach.length ? attachRow(it.attach) : null, body,
      long ? el('button.show-more', { type: 'button', onclick: e => { body.classList.remove('clamped'); e.currentTarget.remove() } }, 'Show all') : null);
  }

  // attachRow shows what a message carried, from the chat's own copies.
  function attachRow(attach) {
    const url = name => `/api/sessions/${encodeURIComponent(id)}/shown/${encodeURIComponent(name)}`;
    return el('div.att-row', null, attach.map(a => {
      if (a.kind !== 'image') {
        return el('a.att.att-file', { href: url(a.kept) + '?name=' + encodeURIComponent(a.name), download: a.name, title: reveal(a.name) },
          svg(ICON.file), el('span.att-name', null, reveal(a.name)), el('span.att-size', null, fmtSize(a.size)));
      }
      // A thumbnail that won't load gives way to the image itself.
      let fellBack = !a.thumb;
      const img = el('img', { src: url(a.thumb || a.kept), alt: reveal(a.name), loading: 'lazy',
        onerror: () => { if (!fellBack) { fellBack = true; img.src = url(a.kept) } } });
      return el('a.att.att-img', { href: url(a.kept), target: '_blank', rel: 'noopener noreferrer', title: reveal(a.name) }, img);
    }));
  }

  function thinkingRow(it) {
    return el('details.thinking', null,
      el('summary', null, svg(ICON.brain), el('span', null, 'Thought'), svg(ICON.chevron, 'chev')),
      markdown(it.text));
  }

  function toolLine(it) {
    const t = it.tool;
    const waiting = t.status === 'running' && !!it.waiting && chat.asks.has(it.waiting);
    const running = t.status === 'running' && !waiting;
    const verb = waiting ? { command: 'Run', edit: 'Edit', read: 'Read', search: 'Search', web: 'Fetch' }[t.kind] || t.name : {
      command: running ? 'Running' : 'Ran', edit: running ? 'Editing' : 'Edited', read: running ? 'Reading' : 'Read',
      search: running ? 'Searching' : 'Searched', web: running ? 'Fetching' : 'Fetched', agent: 'Agent', todo: 'Updated the plan', mcp: t.name, other: t.name,
    }[t.kind] || t.name;
    const agent = t.kind === 'agent';
    const status = waiting ? el('span.tstat.wait', null, svg(ICON.shield), 'Needs approval')
      : running ? el('span.spinner')
      : t.status === 'failed' ? el('span.tstat.bad', { title: 'Failed' }, svg(ICON.x))
        : t.status === 'declined' ? el('span.tstat.muted', null, agent ? 'stopped' : 'declined') : null;
    const n = it.children?.length;
    const title = t.kind === 'todo' ? '' : reveal(t.title || '');
    return [
      el('span.ticon', null, svg(ICON[TOOL_ICON[t.kind] || 'bolt'])),
      el('span.tverb', null, verb),
      // A running subagent says what it's doing under its name.
      agent && running && !it.parent
        ? el('span.ttitle.tstack', null, el('span', null, title), el('span.tnow', null, reveal(agentActivity(it))))
        : el('span.ttitle' + (t.kind === 'command' || t.kind === 'edit' || t.kind === 'read' ? '.mono' : ''), null, title),
      n ? el('span.tcount', null, `${n} step${n === 1 ? '' : 's'}`) : null,
      t.added || t.removed ? el('span.tdiff', null, el('span.plus', null, '+' + (t.added || 0)), el('span.minus', null, '−' + (t.removed || 0))) : null,
      t.exit ? el('span.tstat.bad', null, 'exit ' + t.exit) : null,
      status,
    ];
  }

  function toolRow(it) {
    const t = it.tool;
    let body = null;
    if (t.kind === 'todo' && t.output) {
      return el('div.tool.todo', null, el('div.tline', null, toolLine(it)),
        el('ul.todos', null, t.output.split('\n').map(l => {
          const m = /^\[(.)\] (.*)$/.exec(l) || [null, ' ', l];
          const st = m[1] === 'x' ? 'done' : m[1] === '~' ? 'doing' : 'todo';
          return el('li.' + st, null, el('span.box', null, st === 'done' ? svg(ICON.check) : null), reveal(m[2]));
        })));
    }
    if (t.diff) body = diffView(t.diff, { open: 1, limit: 120 });
    else if (t.kind === 'agent' && t.output && !it.children?.length) body = el('div.tagent-out', null, markdown(t.output));
    else if (t.kind === 'command') body = el('pre.tout', null, '$ ' + reveal(t.title) + (t.output ? '\n' + plainOut(t.output) : ''));
    else if (t.output) body = el('pre.tout', null, plainOut(t.output));
    else if (t.input && t.kind !== 'read' && t.kind !== 'search') body = el('pre.tout', null, toolInput(t.input));
    if (it.children?.length) body = el('div.tchildren', null, it.children.map(c => el('div.tool.child.' + c.tool.status, null, el('div.tline', null, toolLine(c)))),
      t.kind === 'agent' && t.output ? el('div.tagent-out', null, markdown(t.output)) : null);
    const cls = 'tool.' + (it.waiting && t.status === 'running' && chat.asks.has(it.waiting) ? 'waiting' : t.status) + (t.kind === 'agent' ? '.agent' : '');
    if (!body) return el('div.' + cls, null, el('div.tline', null, toolLine(it)));
    return el('details.' + cls, { open: (t.status === 'failed' && t.kind === 'command') || openAgents.has(it.id) ? true : null, ontoggle: t.kind === 'agent' ? e => { if (e.currentTarget.open) openAgents.add(it.id); else openAgents.delete(it.id) } : null },
      el('summary.tline', null, toolLine(it), svg(ICON.chevron, 'chev')), body);
  }

  // showCard is a file the agent showed: Skybreaker's kept copy, checked
  // when it was shown. Images load as images; text comes as text and goes
  // in through textContent, line by line, with hidden characters revealed.
  function showCard(it) {
    const s = it.show;
    const url = `/api/sessions/${encodeURIComponent(id)}/shown/${encodeURIComponent(it.file)}`;
    const image = s.kind === 'image';
    const lines = !image && s.start ? (s.start === s.end ? `line ${s.start}` : `lines ${s.start}–${s.end}`) : '';
    const head = el('div.show-head', null,
      el('span.ticon', null, svg(image ? ICON.image : ICON.file)),
      el('span.show-path', { title: reveal(s.path) }, reveal(s.path)),
      lines ? el('span.show-lines', null, lines) : null,
      el('a.icon-btn.bare', { href: url, target: '_blank', rel: 'noopener noreferrer', title: 'Open', 'aria-label': 'Open' }, svg(ICON.external)));
    const caption = s.title ? el('div.show-title', null, reveal(s.title)) : null;
    const note = s.truncated ? el('div.show-note', null, 'Cut short. The agent can show the rest as a range of lines.') : null;
    if (image) {
      return el('figure.show-card', null, head, caption,
        el('a.show-img', { href: url, target: '_blank', rel: 'noopener noreferrer' }, el('img', { src: url, alt: s.title || s.path, loading: 'lazy' })));
    }
    const body = el('div.show-body', null, el('span.spinner'));
    fetch(url, { credentials: 'same-origin' })
      .then(r => r.ok ? r.text() : Promise.reject(new Error('HTTP ' + r.status)))
      .then(text => {
        if (s.kind === 'markdown') {
          body.replaceChildren(el('div.show-md', null, markdown(text)));
          return;
        }
        const rows = text.replace(/\n$/, '').split('\n').map((l, i) =>
          el('div.sl', null, el('span.ln', null, String((s.start || 1) + i)), el('span.st', null, reveal(l))));
        body.replaceChildren(el('div.show-lines-wrap', null, el('div.show-code', null, rows)));
      })
      .catch(() => body.replaceChildren(el('div.show-note', null, "Couldn't load this file.")));
    return el('div.show-card', null, head, caption, body, note);
  }

  // previewCard is a web page in the sandbox the agent put up: a link to
  // the project's preview address, which Skybreaker set.
  function previewCard(it) {
    const p = it.preview;
    const url = previewURL(p.url);
    const head = el('div.show-head', null,
      el('span.ticon', null, svg(ICON.window)),
      el('span.show-path', null, 'Preview · port ' + Number(p.port)),
      url ? el('a.icon-btn.bare', { href: url, target: '_blank', rel: 'noopener noreferrer', title: 'Open', 'aria-label': 'Open' }, svg(ICON.external)) : null);
    const caption = p.title ? el('div.show-title', null, reveal(p.title)) : null;
    const open = url ? el('a.btn.sm.pv-card-open', { href: url, target: '_blank', rel: 'noopener noreferrer' }, svg(ICON.external), 'Open preview') : null;
    return el('div.show-card.pv-card', null, head, caption, el('div.pv-card-body', null, open, el('span.pv-card-addr', null, url ? url.replace(/^https?:\/\//, '') : 'No address')));
  }

  // askLine is what became of an ask. An open one is the dock's to show.
  function askLine(it) {
    if (it.state === 'open') return el('div.ask-line.open', { hidden: true });
    const how = { allowed: 'Allowed', denied: 'Denied', answered: 'Answered', withdrawn: 'No longer needed' }[it.state] || it.state;
    const q = it.ask.kind === 'question';
    const what = q ? it.ask.questions?.[0]?.question || it.ask.title : it.ask.title;
    return el('div.ask-line.' + it.state, null, svg(it.state === 'denied' ? ICON.x : it.state === 'withdrawn' ? ICON.shield : ICON.check),
      el('span', null, how + ': '), el('span' + (q ? '' : '.mono'), null, reveal(what)));
  }

  function errorCard(it) {
    // A failure because the harness isn't signed in is the sign-in card's
    // business; here it's a quiet line.
    if (chat.login && !chat.login.done) return el('div.notice', null, 'Needs signing in');
    const latest = chat.items.at(-1) === it;
    return el('div.err-card', null, svg(ICON.alert),
      el('div.grow', null, el('b', null, 'The agent stopped'), el('pre', null, plainOut(it.text))),
      latest && chat.lastUser && !chat.login ? el('button.btn.sm', { type: 'button', onclick: () => send(chat.lastUser) }, 'Try again') : null);
  }

  // --- the dock: whatever needs an answer, above the composer ---
  let dockKey = '';
  function drawDock() {
    const ask = [...chat.asks.values()][0];
    // A network request the broker holds for this project waits here too:
    // the agent's command is stuck on it.
    const req = v.requests?.[0];
    const key = ask ? 'ask:' + ask.id + ':' + chat.asks.size : chat.login ? 'login:' + JSON.stringify(chat.login) : req ? 'req:' + req.id : '';
    syncComposer();
    if (key === dockKey) return;
    dockKey = key;
    page.classList.toggle('asking', !!key);
    if (ask) dock.replaceChildren(ask.ask.kind === 'question' ? questionCard(ask) : permissionCard(ask, chat.asks.size));
    else if (chat.login) dock.replaceChildren(loginCard(chat.login));
    else if (req) dock.replaceChildren(requestCard(req, project.tint, { dock: true }));
    else leave([...dock.children].filter(n => !n.classList.contains('leaving')), 160);
  }
  v.project = session.project;
  v.refreshRequests = debounce(async () => {
    try {
      const all = await api('GET', '/api/requests');
      if (v.closed) return;
      v.requests = all.filter(a => a.state === 'pending' && a.project === session.project);
      drawDock();
    } catch { }
  }, 300);
  v.refreshRequests();

  const answer = async (ask, body) => {
    try { await api('POST', `/api/sessions/${id}/answer`, { ask: ask.id, ...body }) } catch (e) { toast(e.message, 'error') }
  };

  function permissionCard(ask, n) {
    const a = ask.ask;
    const detail = a.detail || '';
    const isDiff = detail.startsWith('--- ');
    // The tool's kind, as the chat names it, else its harness's name made plain.
    const tool = a.tool_id && chat.byId.get(a.tool_id);
    const word = tool?.type === 'tool' && TOOL_WORD[tool.tool.kind] || toolWord(a.tool);
    return el('div.dock-card.ask', null,
      el('div.dc-head', null, svg(ICON.shield), el('span.dc-kicker', null, n > 1 ? `Approval · 1 of ${n}` : 'Approval'), el('span.grow'),
        word ? el('span.dc-tool', null, word) : null),
      el('div.dc-title', null, reveal(a.title)),
      a.reason ? el('div.dc-reason', null, reveal(a.reason)) : null,
      detail && detail !== a.title.replace(/^Run /, '') ? (isDiff ? el('div.dc-detail', null, diffView(detail, { open: 1, limit: 200 })) : el('pre.dc-detail', null, reveal(detail))) : null,
      el('div.dc-acts', null,
        el('button.btn.danger', { type: 'button', onclick: () => answer(ask, { allow: false }) }, svg(ICON.x), 'Deny'),
        el('button.btn.primary', { type: 'button', onclick: () => answer(ask, { allow: true }) }, svg(ICON.check), 'Allow')),
      a.always ? el('button.dc-always', { type: 'button', onclick: () => answer(ask, { allow: true, always: true }) }, 'Allow, and don\'t ask again for this') : null);
  }

  function questionCard(ask) {
    const qs = ask.ask.questions || [];
    const picks = new Map(qs.map(q => [q.id, new Set()]));
    const other = new Map();
    const single = qs.length === 1 && !qs[0].multi;
    // One question takes your own words in the composer; several, here.
    const submitAll = () => {
      const answers = {};
      for (const q of qs) {
        const v = [...picks.get(q.id)];
        const o = other.get(q.id)?.value.trim();
        if (o) v.push(o);
        if (v.length) answers[q.id] = v;
      }
      if (!Object.keys(answers).length) { toast('Pick an answer first'); return }
      answer(ask, { allow: true, answers });
    };
    return el('div.dock-card.question', null,
      el('div.dc-head', null, svg(ICON.agent), el('span.dc-kicker', null, 'A question for you')),
      qs.map(q => {
        const opts = el('div.q-opts');
        for (const o of q.options || []) {
          const b = el('button.q-opt', {
            type: 'button', onclick: () => {
              const set = picks.get(q.id);
              if (single) { set.clear(); set.add(o.label); submitAll(); return }
              if (!q.multi) { set.clear(); opts.querySelectorAll('.q-opt').forEach(x => x.classList.remove('on')) }
              set.has(o.label) ? set.delete(o.label) : set.add(o.label);
              b.classList.toggle('on', set.has(o.label));
            },
          }, el('b', null, reveal(o.label)), o.description ? el('span', null, reveal(o.description)) : null);
          opts.append(b);
        }
        const free = single ? null : el('input.input.q-other', { placeholder: 'Something else…', 'aria-label': 'Your own answer' });
        if (free) {
          other.set(q.id, free);
          free.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); submitAll() } });
        }
        return el('div.q', null,
          q.header ? el('span.q-head', null, reveal(q.header)) : null,
          el('div.q-text', null, reveal(q.question)), opts, free);
      }),
      // One question answers with a tap, or in the composer.
      el('div.dc-acts' + (single ? '.single' : ''), null,
        el('button.btn.ghost', { type: 'button', onclick: () => answer(ask, { allow: false }) }, 'Skip'),
        single ? null : el('button.btn.primary', { type: 'button', onclick: submitAll }, svg(ICON.check), 'Answer')));
  }

  function loginCard(l) {
    const who = harnessLabel(session.harness);
    if (l.terminal) {
      return el('div.dock-card.login', null,
        el('div.dc-head', null, svg(ICON.key), el('span.dc-kicker', null, 'Sign-in needed')),
        el('div.dc-title', null, `${who} needs you to sign in`),
        el('div.dc-reason', null, `It signs in from its own terminal: type /login there, follow the link, then come back here.`),
        el('div.dc-acts', null, el('button.btn.primary', { type: 'button', onclick: () => openTerminal() }, svg(ICON.term), 'Open the terminal')));
    }
    if (l.url && l.code) {
      // The link comes from the harness in the sandbox: its host is shown,
      // and one that isn't the vendor's own sign-in host is flagged.
      const where = signInHost(l.url);
      return el('div.dock-card.login', null,
        el('div.dc-head', null, svg(ICON.key), el('span.dc-kicker', null, `Sign in to ${who}`)),
        el('div.dc-reason', null, 'Open the sign-in page, sign in with your account, and enter this code:'),
        el('button.device-code', { type: 'button', onclick: () => copyText(l.code), title: 'Copy' }, reveal(l.code), svg(ICON.copy)),
        where && !where.known ? el('div.dc-reason.dc-warn', null, `This sign-in page is on ${where.host}, not ${who}'s own site. Don't enter your password unless you expected that.`) : null,
        el('div.dc-acts', null, where
          ? el('a.btn' + (where.known ? '.primary' : ''), { href: l.url, target: '_blank', rel: 'noopener noreferrer' }, 'Open sign-in page on ' + where.host)
          : el('span.mono', null, reveal(l.url))),
        el('div.dc-wait', null, el('span.spinner'), 'Waiting for you to finish signing in…'));
    }
    return el('div.dock-card.login', null,
      el('div.dc-head', null, svg(ICON.key), el('span.dc-kicker', null, 'Sign-in needed')),
      el('div.dc-title', null, `Sign in to ${who}`),
      el('div.dc-reason', null, l.error ? reveal(l.error) : `Use your ${session.harness === 'codex' ? 'ChatGPT' : who} account. Your subscription covers it.`),
      el('div.dc-acts', null, el('button.btn.primary', {
        type: 'button', onclick: async () => {
          try { await api('POST', `/api/sessions/${id}/login`, {}) } catch (e) {
            if (e.status === 409) openTerminal(); else toast(e.message, 'error');
          }
        },
      }, svg(ICON.key), 'Sign in')));
  }

  // --- the working line ---
  // It shows while the turn runs, and while subagents do after it: what's
  // happening now, and for how long.
  const drawWorking = () => {
    const agents = runningAgents(chat);
    working.hidden = !(chat.working || agents.length) || chat.asks.size > 0;
    const lead = agents[agents.length - 1];
    agentsBtn.hidden = !lead;
    if (lead) {
      agentsBtn.textContent = agents.length === 1 ? '1 agent' : agents.length + ' agents';
      agentsBtn.setAttribute('aria-label', `${agentsBtn.textContent} working: show ${lead.tool.title ? reveal(lead.tool.title) : 'it'}`);
    }
    if (working.hidden) return;
    const text = reveal(chat.working ? activityLabel(chat) : [lead.tool.title, agentActivity(lead)].filter(Boolean).join(' · '));
    const since = chat.working ? chat.turnStart : agents[0].since;
    const wtext = working.querySelector('.wtext');
    if (wtext.textContent !== text) wtext.textContent = text;
    const secs = since ? Math.max(0, Math.round((Date.now() - Date.parse(since)) / 1000)) : -1;
    working.querySelector('.wtime').textContent = secs < 0 ? '' : secs < 60 ? secs + 's' : Math.floor(secs / 60) + 'm ' + (secs % 60) + 's';
  };
  // showAgent opens the newest running subagent's card and brings it into view.
  const showAgent = () => {
    let it = runningAgents(chat).pop();
    while (it?.parent) it = chat.byId.get(it.parent);
    const node = it && nodes.get(it.id);
    if (!node) return;
    if (node.tagName === 'DETAILS') { node.open = true; openAgents.add(it.id) }
    node.scrollIntoView({ block: 'center', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' });
    node.classList.remove('flash');
    void node.offsetWidth;
    node.classList.add('flash');
  };

  // --- the start: who this is, and the board's next work to begin on ---
  const drawStart = async () => {
    if (v.closed || chat.items.length || session.status !== 'running') return;
    let next = [];
    try {
      const b = await api('GET', `/api/projects/${encodeURIComponent(project.name)}/board`);
      next = b.items.filter(i => i.status === 'todo').sort((x, y) => y.priority - x.priority).slice(0, 3);
    } catch { }
    if (v.closed || chat.items.length) return;
    const begin = it => {
      input.value = `Work on ${it.ref}: ${it.title}`;
      grow();
      syncComposer();
      saveDraft();
      input.focus();
    };
    startNode = el('div.chat-start', null,
      agentMark(session.harness, '.lg'),
      el('h2', null, `${harnessLabel(session.harness)} in ${project.name}`),
      el('p', null, next.length ? 'Ask it anything, or start on what\'s next on the board.' : 'Ask it anything.'),
      next.length ? el('div.start-items', null, next.map(it => el('button.start-item', { type: 'button', onclick: () => begin(it) },
        el('span.ref', null, it.ref), el('span.start-title', null, it.title),
        it.priority >= 2 ? el('span.prio-chip.p' + it.priority, null, PRIORITIES[it.priority]) : null))) : null);
    log.before(startNode);
  };

  // --- go ---
  head();
  connect();
  setTimeout(drawStart, 500);
  markSeen();
  v.timer = setInterval(drawWorking, 1000);
  if (!coarse()) input.focus({ preventScroll: true });
}

function closeChat() {
  const v = state.chat;
  if (!v) return;
  state.chat = null;
  v.close();
}

// menu shows a small list of actions by its anchor.
function menu(anchor, items) {
  closeMenu();
  const r = anchor.getBoundingClientRect();
  const box = el('div.menu', { role: 'menu' }, items.map(it => it.note
    ? el('div.menu-note', null, el('b', null, it.note), it.desc ? el('span', null, it.desc) : null)
    : el('button.menu-item' + (it.danger ? '.danger' : '') + (it.on ? '.on' : ''), {
      type: 'button', role: 'menuitem', onclick: () => { closeMenu(); it.run() },
    }, el('span.mi-text', null, el('b', null, it.label), it.desc ? el('span', null, it.desc) : null), it.on ? svg(ICON.check) : null)));
  const shade = el('div.menu-shade', { onclick: closeMenu });
  document.body.append(shade, box);
  // Below the anchor if it fits, else above; if neither, on the roomier
  // side, scrolling, so a long list (a harness's models) never runs off
  // the screen.
  const w = box.offsetWidth, h = box.offsetHeight;
  const below = innerHeight - r.bottom - 14, above = r.top - 14;
  const down = h <= below || (h > above && below >= above);
  const room = down ? below : above;
  if (h > room) box.style.maxHeight = Math.max(room, 120) + 'px';
  const shown = Math.min(h, Math.max(room, 120));
  box.style.left = Math.max(8, Math.min(r.left, innerWidth - w - 8)) + 'px';
  box.style.top = (down ? r.bottom + 6 : Math.max(8, r.top - shown - 6)) + 'px';
}

function closeMenu() {
  const going = [...document.querySelectorAll('.menu:not(.leaving), .menu-shade:not(.leaving)')];
  if (going.length) leave(going, 120);
}

// --- review ---------------------------------------------------------------------

// openReview shows what the project's workspace has changed or, from a
// chat, what the chat has (on its own branch, if it has one), and a button
// that asks the agent to ship it.
async function openReview(project, chatId) {
  const body = el('div.review', null, el('div.review-load', null, el('span.spinner'), 'Reading the workspace…'));
  const shipBar = el('div.review-foot', { hidden: true });
  sheet('Changes', el('div.review-wrap', null, body, shipBar));
  let r;
  try { r = await api('GET', chatId ? `/api/sessions/${encodeURIComponent(chatId)}/review` : `/api/projects/${project}/review`) } catch (e) {
    body.replaceChildren(el('div.empty-note', null, e.message));
    return;
  }
  const added = r.files.reduce((n, f) => n + f.added, 0), removed = r.files.reduce((n, f) => n + f.removed, 0);
  body.replaceChildren();
  append(body, [
    el('div.review-sum', null,
      el('span.branch', null, svg(ICON.branch), r.branch),
      r.base ? el('span.muted', null, 'against ' + r.base) : null,
      el('span.grow'),
      r.files.length ? el('span.tdiff', null, el('span.plus', null, '+' + added), el('span.minus', null, '−' + removed)) : null),
    r.clean ? el('div.empty-note', null, el('b', null, 'Nothing changed yet'), el('p', null, 'The workspace matches ' + (r.base || 'its last commit') + '.')) : null,
    r.commits.length ? el('section.review-sec', null, el('h3', null, `${r.commits.length} commit${r.commits.length === 1 ? '' : 's'}`),
      el('div.commits', null, r.commits.map(c => el('div.commit', null, el('span.mono.muted', null, c.sha), el('span', null, reveal(c.subject)), el('span.muted', null, ago(c.when)))))) : null,
    r.files.length ? el('section.review-sec', null, el('h3', null, `${r.files.length} file${r.files.length === 1 ? '' : 's'}`),
      diffView(r.diff, { open: r.files.length <= 4 ? 4 : 0, limit: 600 })) : null]);
  if (chatId && !r.clean && state.chat?.send) {
    shipBar.hidden = false;
    shipBar.replaceChildren(
      el('button.btn.ghost', { type: 'button', onclick: () => { closeSheet(); $('.composer-input')?.focus() } }, 'Ask for changes'),
      el('button.btn.primary', {
        type: 'button', onclick: async () => {
          const send = state.chat?.send;
          closeSheet();
          if (send) { await send(SHIP_MESSAGE); toast('Asked the agent to ship it') }
        },
      }, svg(ICON.ship), 'Ship it'));
  }
}

async function newChatIn(projectName, harness) {
  try {
    const s = await api('POST', `/api/projects/${projectName}/sessions`, { harness, own_branch: ownBranch() });
    navigate('/s/' + s.id);
  } catch (e) { toast(e.message, 'error') }
}
