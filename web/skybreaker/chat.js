'use strict';

// The chat view: a session run in its harness's structured mode, drawn as a
// conversation from its event stream (see chatlib.js for the reducer and
// the markdown parser). Agent text goes into the page only as text nodes:
// markdown is parsed into data and built with el(), never as HTML.

const HARNESS_LABEL = { claude: 'Claude', codex: 'Codex', shell: 'Shell', pi: 'pi', qwen: 'Qwen', opencode: 'OpenCode' };
const harnessLabel = h => HARNESS_LABEL[h] || h;
const chatName = s => s.title || `New ${harnessLabel(s.harness)} chat`;

const MODE_INFO = {
  ask: { label: 'Ask first', short: 'Ask', desc: 'Asks before editing files or running commands.' },
  edits: { label: 'Auto-edit', short: 'Edits', desc: 'Edits files freely; asks before other commands.' },
  plan: { label: 'Plan only', short: 'Plan', desc: 'Reads and plans, changes nothing.' },
  auto: { label: 'Full auto', short: 'Auto', desc: 'Never asks. The sandbox and broker still apply.' },
};

const SHIP_MESSAGE = 'Ship it: commit the work with a clear message on a feature/, fix/, docs/ or chore/ branch (never main), push it, and open a pull request. Reply with the PR link.';

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

async function renderChat(seq, d) {
  const id = d.session.id;
  let session = d.session, project = d.project;
  const chat = newChat();
  const nodes = new Map(); // item id → its element
  const modes = state.meta?.chat?.[session.harness] || ['ask'];

  const title = el('span.title');
  const sub = el('span.sub');
  const log = el('div.chat-log');
  const working = el('div.chat-working', { hidden: true, role: 'status' }, sunMark(), el('span.wtext', null, 'Working'), el('span.wtime'));
  const jump = el('button.jump', { type: 'button', hidden: true, onclick: () => toBottom(true) }, svg(ICON.down), 'Latest');
  const scroller = el('div.chat-scroll', null, el('div.chat-col', null, log, working));
  const dock = el('div.dock');
  const input = el('textarea.composer-input', {
    rows: '1', 'aria-label': 'Message', enterkeyhint: coarse() ? 'enter' : 'send', autocapitalize: 'sentences', spellcheck: 'true',
  });
  const sendBtn = el('button.send-btn', { type: 'submit', 'aria-label': 'Send', onmousedown: e => e.preventDefault() });
  const modeBtn = el('button.mode-chip', { type: 'button', onclick: () => modeMenu() });
  const composer = el('form.composer', { onsubmit: e => { e.preventDefault(); submit() } }, modeBtn, input, sendBtn);
  const ended = el('div.ended-bar', { hidden: true });
  const hints = el('div.cmd-hints', { hidden: true });
  const footer = el('div.chat-foot', null, dock, hints, composer, ended);
  const goalBar = el('div.chat-goal', { hidden: true });
  const ring = contextRing();
  const ringBtn = el('button.icon-btn.bare.ctx-btn', { onclick: e => sessionMenu(e.currentTarget), 'aria-label': 'Context and session' }, ring.node);

  const back = () => history.length > 1 ? history.back() : navigate('/');
  const page = el('div.chat-page', null,
    el('div.chat-bar', null,
      el('button.icon-btn.bare', { onclick: back, 'aria-label': 'Back' }, svg(ICON.back)),
      el('div.chat-title', null, title, sub),
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
  state.chat = v;
  v.close = () => {
    v.closed = true;
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
    const model = chatModel();
    sub.textContent = [harnessLabel(session.harness), project.name, model && (model.name || model.id)].filter(Boolean).join(' · ');
    const m = MODE_INFO[session.perm_mode] || MODE_INFO.ask;
    modeBtn.replaceChildren(svg(ICON.shield), el('span', null, m.short));
    modeBtn.title = m.label + ': ' + m.desc;
    const use = contextUse(chat.info);
    ring.set(use);
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
    working.hidden = !chat.working || chat.asks.size > 0;
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
    if (e.kind === 'info') head();
    if (e.kind === 'turn' && e.turn?.state !== 'working') { markSeen(); v.refreshMeta() }
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
  const syncComposer = () => {
    const ask = [...chat.asks.values()][0];
    input.placeholder = ask ? 'Or tell it what to do instead…' : chat.working ? 'Add to what it\'s doing…' : `Message ${harnessLabel(session.harness)}`;
    const stop = chat.working && !input.value.trim() && !ask;
    sendBtn.classList.toggle('stop', stop);
    sendBtn.setAttribute('aria-label', stop ? 'Stop' : 'Send');
    sendBtn.replaceChildren(svg(stop ? ICON.stop : ICON.send));
    sendBtn.disabled = !stop && !input.value.trim();
  };
  input.addEventListener('input', () => { grow(); syncComposer(); saveDraft(); showHints() });

  // --- Skybreaker's own commands: /rename, /color, /goal, /clear, /fork, /compact ---
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
  // clearChat starts a fresh chat like this one (same agent, mode and
  // model) and ends this one, which stays readable.
  const clearChat = async () => {
    const n = await api('POST', `/api/projects/${project.name}/sessions`, { harness: session.harness });
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
    const hs = commandHints(input.value);
    hints.hidden = !hs.length;
    hints.replaceChildren(...hs.map(h => h.color
      ? el('button.cmd-hint.swatch-hint.sc-' + h.color, { type: 'button', onmousedown: e => e.preventDefault(),
        onclick: async () => { if (await runCommand({ cmd: 'color', arg: h.color })) clearInput() } }, el('span.swatch'), h.color)
      : el('button.cmd-hint', { type: 'button', onmousedown: e => e.preventDefault(),
        onclick: () => { input.value = '/' + h.cmd + ' '; input.focus(); showHints(); syncComposer() } },
        el('b', null, h.usage), el('span', null, h.desc))));
  };
  input.addEventListener('keydown', e => {
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
    if (!text) {
      if (chat.working) interrupt();
      return;
    }
    const command = parseCommand(text);
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
      if (ask && ask.ask.kind === 'permission') {
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
    const body = el('div.bubble' + (long ? '.clamped' : ''), null, it.text);
    return el('div.msg.user', null, body,
      long ? el('button.show-more', { type: 'button', onclick: e => { body.classList.remove('clamped'); e.currentTarget.remove() } }, 'Show all') : null);
  }

  function thinkingRow(it) {
    return el('details.thinking', null,
      el('summary', null, svg(ICON.brain), el('span', null, 'Thought'), svg(ICON.chevron, 'chev')),
      markdown(it.text));
  }

  function toolLine(it) {
    const t = it.tool;
    const running = t.status === 'running';
    const verb = {
      command: running ? 'Running' : 'Ran', edit: running ? 'Editing' : 'Edited', read: running ? 'Reading' : 'Read',
      search: running ? 'Searching' : 'Searched', web: running ? 'Fetching' : 'Fetched', agent: 'Agent', todo: 'Updated the plan', mcp: t.name, other: t.name,
    }[t.kind] || t.name;
    const status = running ? el('span.spinner')
      : t.status === 'failed' ? el('span.tstat.bad', { title: 'Failed' }, svg(ICON.x))
        : t.status === 'declined' ? el('span.tstat.muted', null, 'declined') : null;
    const n = it.children?.length;
    return [
      el('span.ticon', null, svg(ICON[TOOL_ICON[t.kind] || 'bolt'])),
      el('span.tverb', null, verb),
      el('span.ttitle' + (t.kind === 'command' || t.kind === 'edit' || t.kind === 'read' ? '.mono' : ''), null, t.kind === 'todo' ? '' : reveal(t.title || '')),
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
    else if (t.kind === 'command') body = el('pre.tout', null, '$ ' + reveal(t.title) + (t.output ? '\n' + plainOut(t.output) : ''));
    else if (t.output) body = el('pre.tout', null, plainOut(t.output));
    else if (t.input && t.kind !== 'read' && t.kind !== 'search') body = el('pre.tout', null, toolInput(t.input));
    if (it.children?.length) body = el('div.tchildren', null, it.children.map(c => el('div.tool.child', null, el('div.tline', null, toolLine(c)))));
    if (!body) return el('div.tool.' + t.status, null, el('div.tline', null, toolLine(it)));
    return el('details.tool.' + t.status, { open: t.status === 'failed' && t.kind === 'command' ? true : null },
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

  function askLine(it) {
    const how = { open: 'Waiting for you', allowed: 'Allowed', denied: 'Denied', answered: 'Answered', withdrawn: 'No longer needed' }[it.state] || it.state;
    return el('div.ask-line.' + it.state, null, svg(it.state === 'denied' ? ICON.x : it.state === 'open' ? ICON.shield : ICON.check),
      el('span', null, how + ': '), el('span.mono', null, reveal(it.ask.title)));
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
    else dock.replaceChildren();
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
    return el('div.dock-card.ask', null,
      el('div.dc-head', null, svg(ICON.shield), el('span.dc-kicker', null, n > 1 ? `Approval · 1 of ${n}` : 'Approval'), el('span.grow'),
        a.tool ? el('span.dc-tool', null, a.tool) : null),
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
        const free = el('input.input.q-other', { placeholder: 'Something else…', 'aria-label': 'Your own answer' });
        other.set(q.id, free);
        free.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); submitAll() } });
        return el('div.q', null,
          q.header ? el('span.q-head', null, reveal(q.header)) : null,
          el('div.q-text', null, reveal(q.question)), opts, free);
      }),
      el('div.dc-acts', null,
        el('button.btn.ghost', { type: 'button', onclick: () => answer(ask, { allow: false }) }, 'Skip'),
        el('button.btn.primary', { type: 'button', onclick: submitAll }, svg(ICON.check), 'Answer')));
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

  // --- go ---
  head();
  connect();
  markSeen();
  v.timer = setInterval(() => {
    // What it's doing now, in the agent's own words, and for how long.
    working.querySelector('.wtext').textContent = reveal(activityLabel(chat));
    if (!chat.working || !chat.turnStart) { working.querySelector('.wtime').textContent = ''; return }
    const s = Math.max(0, Math.round((Date.now() - Date.parse(chat.turnStart)) / 1000));
    working.querySelector('.wtime').textContent = s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's';
  }, 1000);
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
  const w = box.offsetWidth, h = box.offsetHeight;
  const left = Math.max(8, Math.min(r.left, innerWidth - w - 8));
  const top = r.bottom + 6 + h > innerHeight ? Math.max(8, r.top - h - 6) : r.bottom + 6;
  box.style.left = left + 'px';
  box.style.top = top + 'px';
}

function closeMenu() {
  document.querySelectorAll('.menu, .menu-shade').forEach(n => n.remove());
}

// --- review ---------------------------------------------------------------------

// openReview shows what the project's workspace has changed, and from a
// chat, a button that asks the agent to ship it.
async function openReview(project, chatId) {
  const body = el('div.review', null, el('div.review-load', null, el('span.spinner'), 'Reading the workspace…'));
  const shipBar = el('div.review-foot', { hidden: true });
  sheet('Changes', el('div.review-wrap', null, body, shipBar));
  let r;
  try { r = await api('GET', `/api/projects/${project}/review`) } catch (e) {
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
    const s = await api('POST', `/api/projects/${projectName}/sessions`, { harness });
    navigate('/s/' + s.id);
  } catch (e) { toast(e.message, 'error') }
}
