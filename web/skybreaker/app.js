'use strict';

// Skybreaker's UI. Views: home (the inbox: every session, what needs you
// first), project (its sandbox, sessions and schedules), session (a chat, see
// chat.js, or a terminal), run (a scheduled run's log), activity (what the
// broker let out, tokens and the kill switch) and settings (settings.js).
// Agent text only ever goes in through textContent; see dom.js.

const state = { meta: null, route: null, seq: 0, timers: [], term: null, chat: null };

function navigate(path) {
  if (location.pathname + location.search !== path) history.pushState(null, '', path);
  state.moved = true;
  go();
}

document.addEventListener('click', e => {
  const a = e.target.closest('a[data-link]');
  if (!a || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
  e.preventDefault();
  navigate(a.getAttribute('href'));
});
window.addEventListener('popstate', () => { state.moved = true; go() });

// enter fades a new page in. Opacity alone: it composites cheaply, and a
// transform would carry the page's fixed buttons with it. Live refreshes
// redraw in place without it.
function enter() {
  if (!state.moved) return;
  state.moved = false;
  if (matchMedia('(prefers-reduced-motion: reduce)').matches || !$('#app').animate) return;
  $('#app').animate([{ opacity: 0.35 }, { opacity: 1 }], { duration: 160, easing: 'ease-out' });
}

function clearTimers() {
  state.timers.forEach(clearInterval);
  state.timers = [];
}

function crumb(project, extra) {
  const c = $('#crumb');
  c.replaceChildren();
  if (!project) return;
  c.append(el('span.sep', null, '/'), el('a.crumb-pill.tint-' + project.tint, { href: '/p/' + project.name, 'data-link': true }, el('span.pdot'), el('span', null, project.name)));
  if (extra) c.append(el('span.sep', null, '/'), el('span.crumb-pill', null, el('span', null, extra)));
}

async function go() {
  const seq = ++state.seq;
  state.stale = false;
  clearTimers();
  closeTerminal();
  closeChat();
  closeSheet();
  closeMenu();
  state.route = route(location.pathname);
  document.body.classList.toggle('term-mode', state.route.name === 'session');
  document.body.classList.remove('chat-mode');
  try {
    switch (state.route.name) {
      case 'home': await renderHome(seq); break;
      case 'project': await renderProject(seq, state.route.project, state.route.tab); break;
      case 'session': await renderSession(seq, state.route.id); break;
      case 'run': await renderRun(seq, state.route.id); break;
      case 'activity': await renderActivity(seq); break;
      case 'settings': await renderSettings(seq); break;
      default: renderNotFound();
    }
    if (seq === state.seq) { markSidebar(); enter() }
  } catch (e) {
    if (seq === state.seq) {
      toast(e.message, 'error');
      if (e.status === 404) renderNotFound();
    }
  }
}

// busy is true while the user is reading a sheet or typing: a live update
// then waits rather than wiping what they were doing.
function busy() {
  const a = document.activeElement;
  return !!$('#sheet-root').firstChild || !!(a && a.tagName === 'INPUT' && a.value);
}

// refresh re-renders the current view in place after a live event, except
// the terminal, which streams on its own.
const refresh = debounce(() => {
  if (!state.route || state.route.name === 'session') return;
  if (busy()) { state.stale = true; return }
  const y = scrollY;
  go().then(() => scrollTo(0, y));
}, 400);

function renderNotFound() {
  crumb(null);
  $('#app').replaceChildren(el('div.wrap', null, el('div.empty-note', null,
    el('h2', null, 'Nothing here'), el('p', null, 'That page does not exist. '), el('a.btn', { href: '/', 'data-link': true }, 'Back home'))));
}

// --- home: the inbox -----------------------------------------------------------

async function renderHome(seq) {
  const d = await api('GET', '/api/inbox');
  if (seq !== state.seq) return;
  crumb(null);
  state.projects = d.projects;
  sidebar(d);
  const tints = Object.fromEntries(d.projects.map(p => [p.name, p.tint]));
  const g = inboxGroups(d.sessions, d.pending);
  const pendingFor = id => d.pending.filter(a => a.session === id);
  const row = s => sessionRow(s, { project: true, tint: tints[s.project], pending: pendingFor(s.id).length });
  const requests = d.requests || [];
  const review = d.review || [];
  const next = d.next || [];
  const orphans = d.pending.filter(a => !d.sessions.some(s => s.id === a.session));
  // Open chats stay in view; Earlier is only what has ended.
  const earlier = g.ended;
  const needs = g.forYou.length + requests.length + review.length + orphans.length;
  const parts = [
    requests.length + orphans.length ? plural(requests.length + orphans.length, 'approval') : '',
    g.forYou.length ? plural(g.forYou.length, 'chat') : '',
    review.length ? `${review.length} to review` : '',
  ].filter(Boolean);
  const summary = needs ? listing(parts) : g.working.length ? `${plural(g.working.length, 'agent')} working on their own` : 'Nothing is waiting on you.';

  // On a phone, review items sit in the one list of what needs you; on a
  // desktop they have the side rail.
  const reviewRows = review.map(it => { const r = itemRow(it, tints[it.project]); r.classList.add('phone-only'); return r });
  const forYou = [...g.forYou.map(row), ...reviewRows];
  const main = el('div.home-main', null,
    el('header.page-head', null, el('h1', null, needs ? 'For you' : 'All quiet'), el('p', null, summary)),
    requests.length || orphans.length ? el('div.approvals', null, requests.map(a => requestCard(a, tints[a.project])), orphans.map(a => approvalCard(a, tints[a.project]))) : null,
    forYou.length ? el('div.row-list' + (g.forYou.length ? '' : '.phone-only'), null, forYou) : null,
    g.forYou.some(s => pendingFor(s.id).length) ? el('div.approvals', null, d.pending.filter(a => g.forYou.some(s => s.id === a.session)).map(a => approvalCard(a, tints[a.project]))) : null,
    g.working.length ? el('section.list-sec', null, secHead('Working', g.working.length), el('div.row-list', null, g.working.map(row))) : null,
    g.recent.length ? el('section.list-sec', null, secHead('Open', g.recent.length), el('div.row-list', null, g.recent.map(row))) : null,
    !d.sessions.some(s => s.status === 'running') ? el('button.empty-add', { type: 'button', onclick: () => newSessionSheet(d.projects) }, svg(ICON.plus), 'Start a chat') : null,
    el('section.list-sec.phone-only', null, secHead('Projects'),
      el('div.tiles', null, d.projects.map(p => projectTile(p, review)),
        el('button.tile.np-tile', { type: 'button', onclick: newProjectSheet },
          el('span.tile-name', null, svg(ICON.plus), 'New project'), el('span.tile-sub', null, 'From a GitHub repo')))),
    earlier.length ? el('details.earlier', { open: prefs.get('earlierOpen') === '1', ontoggle: e => prefs.set('earlierOpen', e.target.open ? '1' : '0') },
      el('summary', null, el('span', null, 'Earlier'), el('span.n', null, String(earlier.length)), svg(ICON.chevron, 'chev')),
      el('div.row-list', null, earlier.map(row))) : null);
  const rail = el('aside.home-rail.desk-only', null,
    review.length ? el('section.rail-card', null, el('h2', null, 'Ready for review'), review.map(it => railItem(it))) : null,
    next.length ? el('section.rail-card', null, el('h2', null, 'Up next'), next.map(it => railItem(it))) : null);
  $('#app').replaceChildren(el('div.page.home', null, main, rail),
    el('button.fab', { type: 'button', onclick: () => newSessionSheet(d.projects), 'aria-label': 'New chat' }, svg(ICON.plus)));
}

const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;
const listing = parts => parts.length < 2 ? parts.join('') : parts.slice(0, -1).join(', ') + ' and ' + parts[parts.length - 1];

function secHead(title, n) {
  return el('div.sec-title', null, el('h2', null, title), n ? el('span.n', null, String(n)) : null);
}

// projectTile is a project on the phone's home: its colour, what's live and
// what's on its board.
function projectTile(p, review) {
  const r = review.filter(it => it.project === p.name).length;
  const sub = [p.live ? plural(p.live, 'chat') : p.running ? 'running' : 'stopped', r ? `${r} to review` : p.open ? `${p.open} on the board` : ''].filter(Boolean).join(' · ');
  return el('a.tile.tint-' + p.tint, { href: '/p/' + encodeURIComponent(p.name), 'data-link': true },
    el('span.tile-name', null, el('span.pdot'), p.name), el('span.tile-sub', null, sub));
}

// railItem is a board item in the desktop home's side rail.
function railItem(it) {
  return el('button.rail-item', { type: 'button', onclick: () => openItem(it.ref) },
    el('span.ref', null, it.ref), el('span.rail-title', null, it.title),
    it.priority >= 2 ? el('span.prio-chip.p' + it.priority, null, PRIORITIES[it.priority]) : null);
}

// sessionRow is one session on one line: who, what it's doing, where and
// when. Tap it to open it.
function sessionRow(s, opts = {}) {
  const t = sessionTone(s);
  const label = opts.pending ? 'Waiting for approval' : t.label;
  const tone = opts.pending ? 'wait' : t.tone;
  const tint = CHAT_COLORS.includes(s.color) ? '.sc-' + s.color : '';
  return el('a.srow.srow-open.tone-' + tone + tint, { href: '/s/' + s.id, 'data-link': true },
    el('span.avatar.sm.h-' + s.harness, { title: s.harness }, s.harness.slice(0, 2)),
    el('div.who', null,
      el('div.title', null, s.mode === 'chat' ? chatName(s) : sessionName(s)),
      el('div.meta', null,
        el('span.tone-dot'),
        el('span.tone-label', null, label),
        opts.project ? el('span.sep', null, '·') : null,
        opts.project ? el('span.proj.tint-' + (opts.tint ?? 0), null, el('span.pdot'), s.project) : null,
        el('span.sep', null, '·'),
        el('span', null, ago(s.updated && !s.updated.startsWith('0001') ? s.updated : s.ended || s.created)))),
    svg(ICON.chevron, 'chev'));
}

// newSessionSheet starts a session: pick a project (when there are a few),
// then an agent. Agents that can chat open a chat; the shell, a terminal.
function newSessionSheet(projects) {
  if (!projects?.length) return;
  let current = projects.find(p => p.name === prefs.get('lastProject')) || projects[0];
  const list = el('div.picker-list');
  const chips = projects.length > 1 ? el('div.chips.picker-projects', null, projects.map(p =>
    el('button.chip' + (p.name === current.name ? '.on' : ''), {
      type: 'button', onclick: e => {
        current = p;
        chips.querySelectorAll('.chip').forEach(c => c.classList.remove('on'));
        e.currentTarget.classList.add('on');
        draw();
      },
    }, p.name))) : null;
  const draw = () => list.replaceChildren(...harnessPicks(current));
  draw();
  $('#sheet-root').replaceChildren(
    el('div.backdrop', { onclick: closeSheet }),
    el('div.picker', { role: 'dialog', 'aria-modal': 'true', 'aria-label': 'New session' },
      el('div.picker-grip'),
      el('div.picker-head', null, el('h2', null, 'New session')),
      chips, list,
      el('button.btn.ghost.picker-cancel', { type: 'button', onclick: closeSheet }, 'Cancel')));
  $('.pick', list)?.focus({ preventScroll: true });
}

// approvalCard is a harness approval: may this agent run this tool inside
// its own sandbox? requestCard is the other kind: may it reach out of the
// sandbox with this request?
function approvalCard(a, tint) {
  const decide = async allow => {
    try {
      await api('POST', '/api/approvals/' + encodeURIComponent(a.id), { allow });
      toast(allow ? 'Allowed' : 'Denied');
      refresh();
    } catch (e) { toast(e.message, 'error') }
  };
  return el('div.approval.tint-' + (tint ?? 0), null,
    el('div.head', null, el('b', null, a.tool || 'Tool'), el('span.muted', null, 'in'), el('b', null, a.project),
      a.session ? el('a.mono.muted', { href: '/s/' + a.session, 'data-link': true }, a.session) : null,
      el('span.grow'), el('span.muted', null, ago(a.created))),
    el('pre', null, toolSummary(a.tool, a.input) || toolInput(a.input)),
    el('details.input', null, el('summary.muted', null, 'Full input'), el('pre', null, toolInput(a.input))),
    el('div.acts', null,
      el('button.btn.primary.sm', { onclick: () => decide(true) }, svg(ICON.check), 'Allow'),
      el('button.btn.danger.sm', { onclick: () => decide(false) }, svg(ICON.x), 'Deny'),
      a.session ? el('a.btn.ghost.sm', { href: '/s/' + a.session, 'data-link': true }, svg(ICON.term), 'Terminal') : null),
  );
}

// requestCard is a network request the broker is holding for you: a merge,
// a deploy, a kubectl write. Everything on it is the broker's own parse of
// the request, never what the agent says about it; approving lets that
// identical request through once.
function requestCard(a, tint, opts = {}) {
  const view = a.view || {};
  let busy = false;
  const decide = async approve => {
    if (busy) return;
    busy = true;
    try {
      await api('POST', `/api/requests/${encodeURIComponent(a.id)}/${approve ? 'approve' : 'deny'}`, {});
      toast(approve ? 'Approved: the agent can go ahead' : 'Denied');
      refresh();
      opts.done?.();
    } catch (e) { toast(e.message, 'error'); refresh() }
    busy = false;
  };
  const fields = (view.fields || []).filter(f => f.value && f.label !== 'Project'); // the head names it
  return el('div.approval.request.tint-' + (tint ?? 0) + (opts.dock ? '.dock-card' : ''), null,
    el('div.head', null, svg(ICON.globe), el('b', null, 'Network request'), el('span.muted', null, 'from'),
      el('span.proj', null, el('span.pdot'), a.project), el('span.grow'), el('span.muted', null, 'expires ' + until(a.expires))),
    el('div.req-title', null, view.title || `${a.method} ${a.host}${a.path}`),
    // Cards fold the details away, except on Activity, where they're the point.
    !opts.full ? el('details.input', null, el('summary.muted', null, 'Details'),
      el('dl.fields', null, fields.map(f => [el('dt', null, f.label), el('dd', null, f.value)])), view.body ? el('pre', null, view.body) : null)
      : fields.length ? el('dl.fields', null, fields.map(f => [el('dt', null, f.label), el('dd', null, f.value)])) : null,
    opts.full && view.body ? el('details.input', null, el('summary.muted', null, 'Request body'), el('pre', null, view.body)) : null,
    el('div.acts', null,
      el('button.btn.danger', { type: 'button', onclick: () => decide(false) }, svg(ICON.x), 'Deny'),
      el('button.btn.primary', { type: 'button', onclick: () => decide(true) }, svg(ICON.check), 'Approve once')));
}

// --- activity: what left the sandboxes ---------------------------------------

// renderActivity pages through the broker's log on the server, newest
// first; the page is in the address (?page=2), the filter a preference.
async function renderActivity(seq) {
  const filter = prefs.get('activityFilter') || 'all';
  const page = Math.max(1, parseInt(new URLSearchParams(location.search).get('page'), 10) || 1);
  const q = new URLSearchParams({ page: String(page) });
  if (filter === 'blocked') q.set('blocked', '1');
  else if (filter.startsWith('p:')) q.set('project', filter.slice(2));
  const cq = filter.startsWith('p:') ? '?project=' + encodeURIComponent(filter.slice(2)) : '';
  const [d, reqs, projects, ch] = await Promise.all([api('GET', '/api/activity?' + q), api('GET', '/api/requests'), api('GET', '/api/projects'), api('GET', '/api/activity/chart' + cq)]);
  if (seq !== state.seq) return;
  if (filter !== 'all' && filter !== 'blocked' && !projects.some(p => 'p:' + p.name === filter)) {
    prefs.set('activityFilter', 'all'); // a project that's gone
    return navigate('/activity');
  }
  crumb(null);
  const tints = Object.fromEntries(projects.map(p => [p.name, p.tint]));
  const pending = reqs.filter(a => a.state === 'pending');
  const decided = reqs.filter(a => a.state !== 'pending').slice(0, 10);
  const at = n => n > 1 ? '/activity?page=' + n : '/activity';
  const chip = (f, label) => el('button.chip' + (f === filter ? '.on' : ''), { type: 'button', 'aria-pressed': String(f === filter), onclick: () => { prefs.set('activityFilter', f); navigate('/activity') } }, label);
  const chips = [chip('all', 'Everything'), chip('blocked', 'Blocked'), ...projects.map(p => chip('p:' + p.name, p.name))];
  const turn = n => () => { navigate(at(n)); $('#net-log')?.scrollIntoView({ block: 'start' }) };
  const pager = page > 1 || d.more ? el('nav.pager', { 'aria-label': 'Pages' },
    el('button.btn.sm.ghost', { type: 'button', disabled: page === 1 || null, onclick: turn(page - 1) }, svg(ICON.back), 'Newer'),
    el('span.pager-at', null, 'Page ' + page),
    el('button.btn.sm.ghost', { type: 'button', disabled: !d.more || null, onclick: turn(page + 1) }, 'Older', svg(ICON.chevron))) : null;
  const list = el('div.row-list.audit', null,
    d.log.length ? d.log.map(e => auditItem(e, tints)) : el('div.empty-note.flat', null, page > 1 ? 'Nothing older.' : 'Nothing here yet.'));

  const revoke = async t => {
    if (!confirm(`Cut ${t.project}'s sandbox off? It loses all network access until its sandbox restarts.`)) return;
    try { await api('POST', `/api/tokens/${encodeURIComponent(t.id)}/revoke`, {}); toast('Revoked'); go() } catch (e) { toast(e.message, 'error') }
  };
  const killInput = el('input.input', { placeholder: 'type: revoke all', autocomplete: 'off', 'aria-label': 'Confirmation phrase' });
  const kill = async e => {
    e.preventDefault();
    try {
      const r = await api('POST', '/api/kill', { confirm: killInput.value.trim() });
      toast(`Cut off ${r.revoked} sandbox${r.revoked === 1 ? '' : 'es'}`);
      go();
    } catch (err) { toast(err.message, 'error') }
  };

  $('#app').replaceChildren(el('div.wrap.activity', null,
    el('section.inbox-head', null, el('div', null, el('h1', null, 'Activity'),
      el('p', null, 'Everything your agents reached outside their sandboxes, and what they asked for.'))),
    pending.length ? el('section', null,
      el('div.sec-head', null, el('h2', null, 'Waiting for you'), el('span.count', null, String(pending.length))),
      el('div.approvals', null, pending.map(a => requestCard(a, tints[a.project], { full: true })))) : null,
    decided.length && page === 1 ? el('section.inbox-sec', null,
      el('div.sec-head', null, el('h2', null, 'Decided')),
      el('div.row-list', null, decided.map(a => el('div.arow', null,
        el('span.tag.' + (a.state === 'approved' || a.state === 'used' ? 'ok' : 'bad'), null, a.state),
        el('span.target.mono', null, a.view?.title || `${a.method} ${a.host}${a.path}`),
        el('span.when', null, [a.decided_by, ago(a.decided || a.created)].filter(Boolean).join(' · ')))))) : null,
    el('section.inbox-sec', { id: 'net-log' },
      el('div.sec-head', null, el('h2', null, 'Network')),
      el('div.chips', null, chips),
      activityChart(ch),
      list, pager),
    el('section.inbox-sec', null,
      el('div.sec-head', null, el('h2', null, 'Sandbox access')),
      el('p.note', null, 'Each running sandbox holds one token for the broker. Revoking it cuts that sandbox off at once, open connections included.'),
      d.tokens.length ? el('div.row-list', null, d.tokens.map(t => el('div.arow', null,
        el('span.proj.tint-' + (tints[t.project] ?? 0), null, el('span.pdot'), t.project),
        el('span.target.muted', null, 'issued ' + ago(t.created) + ' · expires ' + until(t.expires)),
        el('button.btn.sm.ghost', { type: 'button', onclick: () => revoke(t) }, 'Revoke'))))
        : el('div.empty-note.flat', null, 'No sandbox is running.')),
    el('section.inbox-sec.phone-only', null,
      el('div.sec-head', null, el('h2', null, 'Appearance')),
      el('button.btn.sm', { type: 'button', onclick: () => { $('#theme-btn').click(); go() } }, svg(document.documentElement.dataset.theme === 'light' ? ICON.moon : ICON.sun),
        document.documentElement.dataset.theme === 'light' ? 'Switch to dark' : 'Switch to light')),
    el('section.danger-zone', null,
      el('div.sec-head', null, el('h2', null, 'Kill switch')),
      el('p.note', null, 'Cuts every sandbox off from the network at once. Restart a sandbox to give it access again.'),
      el('form.kill-row', { onsubmit: kill }, killInput, el('button.btn.danger', { type: 'submit' }, 'Cut off everything')))));
}

// activityChart is the broker's last two weeks: what it let out, asked
// about and blocked each day, for the project picked (or all).
function activityChart(ch) {
  const days = activitySeries(ch.hours);
  const sum = k => days.reduce((n, d) => n + d[k], 0);
  return el('div.act-chart', null, dayChart(days, {
    label: `Requests allowed, asked about and blocked per day, last ${days.length} days`,
    stack: [['allowed', 'bc-allowed'], ['asked', 'bc-asked'], ['blocked', 'bc-blocked']],
    title: d => `${d.allowed} allowed, ${d.asked} asked, ${d.blocked} blocked`,
    stats: [[sum('allowed'), 'allowed', 'done'], [sum('asked'), 'asked', 'asked'], [sum('blocked'), 'blocked', 'blocked']],
  }));
}

// auditItem is one broker decision; tap it for the details.
function auditItem(e, tints) {
  const r = auditRow(e);
  const extra = [e.reason && e.reason !== r.target ? e.reason : '', e.status ? 'status ' + e.status : '', e.by ? 'by ' + e.by : '', e.mode].filter(Boolean);
  return el('details.arow.audit-item', null,
    el('summary', null,
      el('span.tag.' + r.tone, null, r.verb),
      el('span.target.mono', null, r.target),
      el('span.when', null, e.project ? el('span.proj.tint-' + (tints[e.project] ?? 0), null, el('span.pdot'), e.project) : null, el('span', null, ago(e.time)))),
    extra.length ? el('div.audit-more', null, extra.map(x => el('div', null, reveal(x)))) : null);
}

// --- project ----------------------------------------------------------------

async function renderProject(seq, name, tab = 'chats') {
  const enc = encodeURIComponent(name);
  const [d, b] = await Promise.all([api('GET', '/api/projects/' + enc), api('GET', `/api/projects/${enc}/board`)]);
  if (seq !== state.seq) return;
  const p = d.project;
  crumb(p);
  markSidebar();
  const live = d.sessions.filter(s => s.status === 'running' && !s.parent);
  const ended = d.sessions.filter(s => s.status !== 'running' && !s.parent);
  const open = b.items.filter(i => i.status !== 'done').length;
  const tabs = [['chats', 'Chats', live.length], ['board', 'Board', open], ['runs', 'Runs', null]];

  const head = el('header.p-head.tint-' + p.tint, null,
    el('div.p-title', null,
      el('div.p-name', null, el('span.pdot'), el('h1', null, p.name)),
      el('button.p-meta', { type: 'button', onclick: () => sandboxSheet(p, live.length), 'aria-label': 'Sandbox: ' + statusLabel(p) },
        p.repo ? el('span.mono', null, p.repo) : null, p.repo ? el('span.sep', null, '·') : null,
        el('span.p-status' + (p.running ? '.on' : p.error ? '.bad' : ''), null, el('span.dot'), statusLabel(p)))),
    el('div.p-acts', null,
      el('button.btn.sm.ghost.desk-only', { type: 'button', onclick: () => openReview(p.name) }, svg(ICON.diff), 'Changes'),
      el('button.btn.sm.ghost.desk-only', { type: 'button', onclick: () => openShell(p) }, svg(ICON.term), 'Terminal'),
      el('button.btn.sm.ghost.desk-only', { type: 'button', onclick: () => previewSheet(p) }, svg(ICON.window), 'Preview'),
      el('button.btn.sm.ghost.desk-only', { type: 'button', onclick: () => vscodeSheet(p) }, svg(ICON.code), 'VS Code'),
      el('button.icon-btn.p-more', { type: 'button', onclick: () => sandboxSheet(p, live.length), 'aria-label': 'Sandbox and project actions', title: 'Sandbox and project actions' }, svg(ICON.more))));
  const tabBar = el('nav.p-tabs', { 'aria-label': 'Project' }, tabs.map(([k, label, n]) =>
    el('a.p-tab' + (k === tab ? '.on' : ''), { href: projectPath(p.name, k), 'data-link': true, 'aria-current': k === tab ? 'page' : null },
      el('span', null, label), n ? el('span.n', null, String(n)) : null)));
  const body = tab === 'board' ? boardView(p.name, b) : tab === 'runs' ? runsView(d) : chatsView(p, d, b, live, ended);
  const page = el('div.page.project.tab-' + tab, null, head, tabBar, body);
  // The board has its own add box; elsewhere the + starts a chat here.
  if (tab === 'board') $('#app').replaceChildren(page);
  else $('#app').replaceChildren(page, el('button.fab', { type: 'button', onclick: () => pickHarness(p), 'aria-label': 'New chat in ' + p.name }, svg(ICON.plus)));
}

const projectPath = (name, tab) => '/p/' + encodeURIComponent(name) + (tab && tab !== 'chats' ? '/' + tab : '');
const statusLabel = p => p.error ? 'Error' : p.running ? 'Running' : 'Stopped';

// chatsView is a project's chats, with what's waiting on you first; a
// desktop shows the board beside them.
function chatsView(p, d, b, live, ended) {
  const waiting = [...(d.requests || []).map(a => requestCard(a, p.tint)), ...d.pending.map(a => approvalCard(a, p.tint))];
  const main = el('div.p-main', null,
    waiting.length ? el('div.approvals', null, waiting) : null,
    live.length ? el('div.row-list', null, live.map(s => sessionRow(s)))
      : el('button.empty-add', { type: 'button', onclick: () => pickHarness(p) }, svg(ICON.plus), 'Start a chat'),
    ended.length ? el('details.earlier', null,
      el('summary', null, el('span', null, plural(ended.length, 'ended chat')), svg(ICON.chevron, 'chev')),
      el('div.row-list', null, ended.slice(0, 40).map(s => s.mode === 'chat' ? sessionRow(s) : endedRow(s)))) : null,
    upNext(p, b),
    b.history && boardChart(b.history) ? el('section.list-sec.phone-only.p-chart', null, el('div.sec-title', null, el('h2', null, 'Board, last 14 days')), boardChart(b.history)) : null);
  return el('div.p-cols', null, main, el('aside.p-side.desk-only', null, boardSnapshot(p, b)));
}

// runsView is the project's schedules and their runs.
function runsView(d) {
  if (!d.jobs.length && !d.runs.length) {
    return el('div.empty-note', null, el('b', null, 'No schedules'), el('p', null, 'Add jobs to the config to run agents on a schedule, such as a nightly pass over the board.'));
  }
  return el('div.runs-view', null,
    d.jobs.length ? el('div.jobs', null, d.jobs.map(jobRow)) : null,
    d.runs.length ? el('div.table-wrap', null, runsTable(d.runs)) : null);
}

// openShell opens a terminal in the project's sandbox.
async function openShell(p) {
  try {
    const s = await api('POST', `/api/projects/${encodeURIComponent(p.name)}/sessions`, { harness: 'shell', title: '' });
    closeSheet();
    navigate('/s/' + s.id);
  } catch (e) { toast(e.message, 'error') }
}

// sandboxSheet holds what you do to a project's sandbox, so its page can
// stay quiet: review, a terminal, schedules, and stopping it.
function sandboxSheet(p, live) {
  const act = (icon, label, run, opts = {}) => el('button.sheet-act' + (opts.cls || ''), { type: 'button', onclick: run },
    svg(icon), el('span.sa-text', null, el('span', null, label), opts.sub ? el('span.sa-sub', null, opts.sub) : null),
    opts.right ? el('span.sa-right', null, opts.right) : null);
  const go = path => () => { closeSheet(); navigate(path) };
  $('#sheet-root').replaceChildren(
    el('div.backdrop', { onclick: closeSheet }),
    el('div.picker.sandbox-sheet', { role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Sandbox' },
      el('div.picker-grip'),
      el('div.sb-status' + (p.running ? '.on' : ''), null, el('span.dot'),
        el('div', null, el('b', null, p.running ? 'Sandbox running' : p.error ? 'Sandbox error' : 'Sandbox stopped'),
          el('span', null, p.error || (p.running ? `gVisor · ${live ? plural(live, 'chat') + ' live' : 'nothing live'}` : 'It starts when you open a chat')))),
      el('div.sheet-acts', null,
        act(ICON.diff, 'Review changes', () => { closeSheet(); openReview(p.name) }),
        act(ICON.term, 'Open a terminal', () => openShell(p)),
        act(ICON.window, 'Preview a web page', () => previewSheet(p), { sub: 'A dev server in the sandbox' }),
        act(ICON.code, 'Open in VS Code', () => vscodeSheet(p), { sub: 'Remote - SSH, from your computer' }),
        act(ICON.clock, 'Schedules', go(projectPath(p.name, 'runs'))),
        p.running
          ? act(ICON.stop, 'Stop sandbox', () => { closeSheet(); if (!live || confirm(`Stop ${p.name}'s sandbox? ${plural(live, 'chat')} pause and resume later.`)) sandboxAction(p.name, 'stop') },
            { cls: '.danger', sub: live ? `${plural(live, 'chat')} pause and resume later` : '' })
          : act(ICON.play, 'Start sandbox', () => { closeSheet(); sandboxAction(p.name, 'start') })),
      el('button.btn.ghost.picker-cancel', { type: 'button', onclick: closeSheet }, 'Close')));
}

// --- previews ---------------------------------------------------------------

Object.assign(ICON, {
  window: ['M3 5h18v14H3z', 'M3 9h18', 'M6 7h.01', 'M9 7h.01'],
  external: ['M14 4h6v6', 'M20 4l-9 9', 'M18 14v6H4V6h6'],
});

// previewSheet shows a web server running in the project's sandbox, on a
// preview address of the project's own: another port of Skybreaker's name,
// so the page can't reach into Skybreaker. Pick a port, then open it.
async function previewSheet(p) {
  const path = `/api/projects/${encodeURIComponent(p.name)}/preview`;
  let d;
  try { d = await api('GET', path) } catch (e) { toast(e.message, 'error'); return }
  const use = async port => {
    try {
      await api('POST', path, { port });
      d.port = port;
      draw();
    } catch (e) { toast(e.message, 'error') }
  };
  // A real link, so the new tab opens on the tap itself.
  const open = port => el('a.sheet-act.pv-open', { href: d.url, target: '_blank', rel: 'noopener noreferrer', onclick: closeSheet },
    svg(ICON.external), el('span.sa-text', null, el('span', null, 'Open :' + port), el('span.sa-sub', null, d.url.replace(/^https?:\/\//, ''))));
  const row = l => !l.public
    ? el('div.sheet-act.pv-local', null, svg(ICON.window),
      el('span.sa-text', null, el('span', null, ':' + l.port), el('span.sa-sub', null, 'Listening on localhost only. Start it on 0.0.0.0 to preview it.')))
    : l.port === d.port ? open(l.port)
      : el('button.sheet-act', { type: 'button', onclick: () => use(l.port) }, svg(ICON.window), el('span.sa-text', null, el('span', null, 'Preview :' + l.port)));
  const field = el('input.pv-port', { type: 'number', min: '1', max: '65535', inputmode: 'numeric', placeholder: 'Another port', 'aria-label': 'Port' });
  const form = el('form.pv-form', { onsubmit: e => { e.preventDefault(); const n = Number(field.value); if (n) use(n) } },
    field, el('button.btn.sm', { type: 'submit' }, 'Preview'));
  const draw = () => {
    const listed = d.listening.some(l => l.port === d.port);
    $('#sheet-root').replaceChildren(
      el('div.backdrop', { onclick: closeSheet }),
      el('div.picker.preview-sheet', { role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Preview' },
        el('div.picker-grip'),
        el('div.pv-head', null, el('b', null, 'Preview'), el('span', null, `A web server in ${p.name}'s sandbox, on its own address apart from Skybreaker.`)),
        el('div.sheet-acts', null,
          d.port && !listed ? open(d.port) : null,
          d.listening.map(row),
          d.listening.length ? null : el('p.pv-empty', null, d.error || 'Nothing is listening yet. Start a dev server in the sandbox on 0.0.0.0, then open this again.')),
        form,
        el('button.btn.ghost.picker-cancel', { type: 'button', onclick: closeSheet }, 'Close')));
  };
  draw();
}

// --- VS Code ------------------------------------------------------------------

Object.assign(ICON, {
  code: ['M8 7l-5 5 5 5', 'M16 7l5 5-5 5'],
});

// vscodeSheet opens the project's sandbox in desktop VS Code over SSH:
// `skybreaker vscode setup` on your computer adds its key and the ssh
// config, then a vscode:// link opens Remote-SSH on <project>.skybreaker.
// The keys added so far are listed here, to remove a lost machine's.
async function vscodeSheet(p) {
  let keys;
  try { keys = await api('GET', '/api/ssh-keys') } catch (e) { toast(e.message, 'error'); return }
  const remove = async k => {
    if (!confirm(`Remove ${k.name}'s key? Its open VS Code windows disconnect.`)) return;
    try {
      await api('DELETE', '/api/ssh-keys/' + encodeURIComponent(k.id));
      keys = keys.filter(x => x.id !== k.id);
      draw();
    } catch (e) { toast(e.message, 'error') }
  };
  const setup = 'skybreaker vscode setup';
  const href = 'vscode://vscode-remote/ssh-remote+' + encodeURIComponent(p.name) + '.skybreaker/workspace';
  const draw = () => $('#sheet-root').replaceChildren(
    el('div.backdrop', { onclick: closeSheet }),
    el('div.picker.vscode-sheet', { role: 'dialog', 'aria-modal': 'true', 'aria-label': 'VS Code' },
      el('div.picker-grip'),
      el('div.pv-head', null, el('b', null, 'VS Code'),
        el('span', null, `Desktop VS Code with Remote - SSH, in ${p.name}'s sandbox. It's another shell there: same network, same broker.`)),
      el('div.vs-step', null,
        el('span.vs-n', null, '1'),
        el('div', null, el('span', null, keys.length ? 'On a new computer, run' : 'On your computer, run'),
          el('div.vs-cmd', null, el('code', null, setup),
            el('button.icon-btn.bare', { type: 'button', onclick: () => copyText(setup), 'aria-label': 'Copy' }, svg(ICON.copy))))),
      el('div.vs-step', null,
        el('span.vs-n', null, '2'),
        el('div', null, el('span', null, 'Then open the project'),
          el('a.btn.sm.vs-open', { href, onclick: closeSheet }, svg(ICON.code), 'Open in VS Code'))),
      keys.length ? el('div.vs-keys', null,
        el('span.vs-label', null, 'Computers with access'),
        keys.map(k => el('div.vs-key', null, svg(ICON.key),
          el('span.sa-text', null, el('span', null, k.name),
            el('span.sa-sub', null, k.fingerprint.slice(0, 19) + '…', ' · ', ago(k.added))),
          el('button.btn.sm.ghost', { type: 'button', onclick: () => remove(k), 'aria-label': 'Remove ' + k.name + "'s key" }, 'Remove')))) : null,
      el('button.btn.ghost.picker-cancel', { type: 'button', onclick: closeSheet }, 'Close')));
  draw();
}

// --- new project ----------------------------------------------------------------

// projectNameFrom suggests a project name from a repo: "acme/Web.App" → "web-app".
function projectNameFrom(repo) {
  const n = String(repo).split('/').pop().toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '');
  return n.slice(0, 40);
}

// newProjectSheet creates a project from the bundles defined in the config
// file or in Settings. Skybreaker restarts to run it, once no agent is
// working.
async function newProjectSheet() {
  let caps;
  try { caps = await api('GET', '/api/capabilities') } catch (e) { toast(e.message, 'error'); return }
  const agents = (state.meta?.harnesses || ['claude', 'codex']).filter(h => h !== 'shell');
  const field = (label, input, hint) => el('label.np-field', null, el('span.np-label', null, label), input, hint ? el('span.np-hint', null, hint) : null);
  const repo = el('input.np-input', { type: 'text', placeholder: 'owner/name', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', required: true });
  const name = el('input.np-input', { type: 'text', placeholder: 'project-name', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', required: true, pattern: '[a-z0-9][a-z0-9-]{0,39}' });
  let named = false;
  name.addEventListener('input', () => { named = name.value !== '' });
  repo.addEventListener('input', () => { if (!named) name.value = projectNameFrom(repo.value) });
  const box = (value, checked, label, sub, reach) => el('label.np-check', null,
    el('input', { type: 'checkbox', value, checked: checked || null }),
    el('span.np-check-text', null, el('b', null, label), sub ? el(reach ? 'span.np-reach' : 'span', null, sub) : null));
  const agentBoxes = agents.map(h => box(h, h === 'claude' || h === 'codex', harnessLabel(h), HARNESS_INFO[h] || ''));
  const wanted = ['github', ...(agents.includes('claude') ? ['claude-login'] : []), ...(agents.includes('codex') ? ['codex-login'] : [])];
  const capBoxes = caps.map(c => box(c.name, wanted.includes(c.name), c.name, c.reaches.join(', '), true));
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const create = el('button.btn.primary', { type: 'submit' }, 'Create project');
  const checked = boxes => boxes.map(b => b.querySelector('input')).filter(i => i.checked).map(i => i.value);
  const form = el('form.np-form', {
    onsubmit: async e => {
      e.preventDefault();
      error.hidden = true;
      create.disabled = true;
      try {
        const out = await api('POST', '/api/projects', { name: name.value.trim(), repo: repo.value.trim(), harnesses: checked(agentBoxes), capabilities: checked(capBoxes) });
        created(out, repo.value.trim());
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
        create.disabled = false;
      }
    },
  },
    field('Repository', repo, 'On GitHub. The agents clone it and open pull requests to it.'),
    field('Name', name, 'Lowercase letters, digits and dashes.'),
    el('fieldset.np-group', null, el('legend', null, 'Agents'), agentBoxes),
    el('fieldset.np-group', null, el('legend', null, 'Access'), capBoxes.length ? capBoxes : el('p.np-hint', null, 'No bundles yet: make one in Settings.'),
      el('p.np-hint', null, 'What the sandbox may reach. Anything else is denied; add hosts later in Settings.')),
    error, create);
  const sheet = (...body) => $('#sheet-root').replaceChildren(
    el('div.backdrop', { onclick: closeSheet }),
    el('div.picker.np-sheet', { role: 'dialog', 'aria-modal': 'true', 'aria-label': 'New project' },
      el('div.picker-grip'), el('div.pv-head', null, el('b', null, 'New project')), ...body,
      el('button.btn.ghost.picker-cancel', { type: 'button', onclick: closeSheet }, 'Close')));
  sheet(form);
  repo.focus();

  // created says what happens next, then waits for Skybreaker to come back
  // with the project.
  function created(out, repoName) {
    const g = out.github;
    const github = !g ? null
      : g.installed ? el('p.np-ok', null, svg(ICON.check), `The GitHub App can reach ${repoName}.`)
        : g.install_url && /^https:\/\/github\.com\//.test(g.install_url)
          ? el('p.np-warn', null, `The GitHub App isn't installed on ${repoName} yet, so agents can't clone or push. `,
            el('a', { href: g.install_url, target: '_blank', rel: 'noopener noreferrer' }, 'Install it'), '.')
          : el('p.np-warn', null, `Couldn't check GitHub access to ${repoName}: ${g.error || 'unknown error'}.`);
    const status = el('p.np-status', null, el('span.spinner'), out.restart === 'now'
      ? 'Skybreaker is restarting to add it. This takes a few seconds.'
      : 'It\'s added once no agent is working, so chats in progress carry on.');
    const open = el('button.btn.primary', { type: 'button', disabled: true, onclick: () => { closeSheet(); navigate(projectPath(out.name)) } }, 'Open ' + out.name);
    sheet(el('div.np-done', null, el('p', null, el('b', null, out.name), ' is saved.'), status, github, open));
    const started = Date.now();
    const poll = async () => {
      if (!document.body.contains(open)) return;
      try {
        const ps = await api('GET', '/api/projects');
        if (ps.some(p => p.name === out.name)) {
          status.replaceChildren(svg(ICON.check), 'Ready.');
          open.disabled = false;
          return;
        }
      } catch { /* restarting */ }
      if (Date.now() - started < 15 * 60 * 1000) setTimeout(poll, 1500);
    };
    setTimeout(poll, 1500);
  }
}

const HARNESS_INFO = {
  claude: 'Claude Code', codex: 'OpenAI Codex', shell: 'A terminal in the sandbox',
  pi: 'pi coding agent', qwen: 'Qwen Code', opencode: 'OpenCode',
};

// harnessPicks are the agents a project can start, chats first.
function harnessPicks(p) {
  let busy = false;
  const start = async h => {
    if (busy) return;
    busy = true;
    try {
      prefs.set('lastProject', p.name);
      const s = await api('POST', `/api/projects/${p.name}/sessions`, { harness: h, title: '' });
      closeSheet();
      navigate('/s/' + s.id);
    } catch (err) { toast(err.message, 'error') } finally { busy = false }
  };
  const chat = h => !!state.meta?.chat?.[h];
  const order = [...p.harnesses].sort((a, b) => chat(b) - chat(a));
  return order.map(h =>
    el('button.pick', { type: 'button', onclick: () => start(h) },
      el('span.avatar.h-' + h, null, h.slice(0, 2)),
      el('span.pick-text', null, el('b', null, harnessLabel(h)), el('span', null, (HARNESS_INFO[h] || h) + (chat(h) ? '' : ' · terminal'))),
      el('span.pick-kind' + (chat(h) ? '.chat' : ''), null, chat(h) ? 'Chat' : 'Terminal'),
      svg(ICON.chevron, 'chev')));
}

// pickHarness asks which agent to start in project p.
function pickHarness(p) {
  const list = el('div.picker-list', null, harnessPicks(p));
  $('#sheet-root').replaceChildren(
    el('div.backdrop', { onclick: closeSheet }),
    el('div.picker', { role: 'dialog', 'aria-modal': 'true', 'aria-label': 'New session' },
      el('div.picker-grip'),
      el('div.picker-head', null,
        el('h2', null, 'New session'),
        el('p', null, p.running ? 'in ' + p.name : `in ${p.name} · starts the sandbox first`)),
      list,
      el('button.btn.ghost.picker-cancel', { type: 'button', onclick: closeSheet }, 'Cancel')));
  $('.pick', list)?.focus({ preventScroll: true });
}

// sessionName is a session's title, or its harness when it has none.
const sessionName = s => s.title || s.harness + ' session';

async function sandboxAction(project, action) {
  try {
    toast(action === 'start' ? 'Starting sandbox…' : 'Stopping sandbox…');
    await api('POST', `/api/projects/${project}/sandbox`, { action });
    refresh();
  } catch (e) { toast(e.message, 'error') }
}

// endedRow is one line per ended session: its last screen, if tmux kept it,
// and a way to drop that screen.
function endedRow(s) {
  const close = async () => {
    try { await api('DELETE', '/api/sessions/' + s.id); refresh() } catch (e) { toast(e.message, 'error') }
  };
  const how = s.status + (s.exit != null ? ` ${s.exit}` : '');
  return el('div.srow', null,
    el('div.srow-open', null,
      el('span.avatar.sm.h-' + s.harness, { title: s.harness }, s.harness.slice(0, 2)),
      el('div.who', null,
        el('div.title', null, sessionName(s)),
        el('div.meta', null, `${how} · ${s.harness} · ${ago(s.ended || s.created)}`))),
    s.screen ? el('a.icon-btn.bare', { href: '/s/' + s.id, 'data-link': true, 'aria-label': 'Last screen', title: 'Last screen' }, svg(ICON.term)) : null,
    s.screen ? el('button.icon-btn.bare', { type: 'button', onclick: close, 'aria-label': 'Drop last screen', title: 'Drop last screen' }, svg(ICON.x)) : null);
}

function jobRow(j) {
  const run = async () => {
    try {
      const r = await api('POST', `/api/jobs/${j.name}/run`, {});
      toast('Run started', '', { label: 'Open', run: () => navigate('/r/' + r.id) });
      refresh();
    } catch (e) { toast(e.message, 'error') }
  };
  return el('div.job', null,
    svg(ICON.clock),
    el('b', null, j.name),
    el('span.mono.muted', null, j.schedule),
    el('span.muted', null, `${j.kind} · ${j.harness}${j.budget_usd ? ' · $' + j.budget_usd : ''}`),
    el('span.grow'),
    el('button.btn.sm', { onclick: run }, svg(ICON.play), 'Run now'));
}

function runState(r) {
  const cls = { ok: '.on', running: '.wait', failed: '.bad', timeout: '.bad' }[r.status] || '';
  return el('span.state' + cls, null, el('span.dot'), r.status);
}

function runsTable(runs) {
  return el('table.table', null,
    el('thead', null, el('tr', null, ['Status', 'Job', 'Started', 'Took', 'Cost', 'Commit'].map((h, i) => el('th' + (i > 2 ? '.opt' : ''), null, h)))),
    el('tbody', null, runs.map(r => el('tr.link', { onclick: () => navigate('/r/' + r.id) },
      el('td', null, runState(r)),
      el('td', null, r.job),
      el('td', null, ago(r.started)),
      el('td.opt', null, duration(r.started, r.finished)),
      el('td.opt', null, r.cost_usd ? '$' + r.cost_usd.toFixed(2) : ''),
      el('td.opt.mono', null, (r.git_sha || '').slice(0, 10))))));
}

// --- sheets -----------------------------------------------------------------

function closeSheet() {
  state.itemSheet = null;
  if (!$('#sheet-root').firstChild) return;
  $('#sheet-root').replaceChildren();
  if (state.stale) refresh();
}

document.addEventListener('focusout', () => setTimeout(() => { if (state.stale && !busy()) refresh() }, 0));

function sheet(title, pre) {
  const close = el('button.icon-btn.bare', { onclick: closeSheet, 'aria-label': 'Close' }, svg(ICON.x));
  $('#sheet-root').replaceChildren(
    el('div.backdrop', { onclick: closeSheet }),
    el('div.sheet', { role: 'dialog', 'aria-label': title }, el('div.sheet-bar', null, el('h2', null, title), el('span.grow'), close), pre));
}

document.addEventListener('keydown', e => { if (e.key === 'Escape' && $('#sheet-root').firstChild) closeSheet() });

// --- run --------------------------------------------------------------------

async function renderRun(seq, id) {
  const runs = await api('GET', '/api/runs');
  const r = runs.find(x => x.id === id);
  if (!r) throw Object.assign(new Error('No such run'), { status: 404 });
  const projects = await api('GET', '/api/projects');
  if (seq !== state.seq) return;
  const p = projects.find(x => x.name === r.project);
  crumb(p, r.job);
  const log = el('pre.term-fallback.run-log', null, 'Loading…');
  $('#app').replaceChildren(el('div.wrap', null,
    el('section.phead', null,
      el('div', null, el('h1', null, r.job), el('div.repo', null, `${r.id} · ${r.harness} · ${r.git_sha ? r.git_sha.slice(0, 12) : 'no commit yet'}`)),
      runState(r),
      el('div.acts', null, r.cost_usd ? el('span.state', null, '$' + r.cost_usd.toFixed(2)) : null)),
    r.error ? el('div.approval', null, el('b', null, r.error)) : null,
    el('details', null, el('summary', null, 'Prompt'), el('pre.term-fallback', null, r.prompt)),
    r.session_id ? el('p.muted', null, 'Reopen in a terminal with ', el('span.mono', null, `claude --resume ${r.session_id}`)) : null,
    log));
  const load = async () => {
    try { log.textContent = (await api('GET', `/api/runs/${id}/log`)) || '(no output yet)' } catch (e) { log.textContent = e.message }
  };
  await load();
  if (r.status === 'running') state.timers.push(setInterval(load, 3000));
}

// --- terminal ---------------------------------------------------------------

function loadScript(src) {
  return new Promise((ok, fail) => {
    if ([...document.scripts].some(s => s.src.endsWith(src))) return ok();
    const s = document.createElement('script');
    s.src = src;
    s.onload = ok;
    s.onerror = () => fail(new Error('could not load ' + src));
    document.head.append(s);
  });
}

function loadStyle(href) {
  if ([...document.styleSheets].some(s => s.href && s.href.endsWith(href))) return;
  document.head.append(el('link', { rel: 'stylesheet', href }));
}

// versioned is an asset's URL as the page names it (with ?v=, cached for
// good), from the prefetch links in index.html.
const versioned = p => $(`link[rel="prefetch"][href^="${p}?"]`)?.getAttribute('href') || p;

// xterm.js is vendored under /vendor; it loads only for the terminal view,
// usually from the cache the page prefetched it into.
async function loadXterm() {
  loadStyle(versioned('/vendor/xterm.css'));
  await loadScript(versioned('/vendor/xterm.js'));
  await loadScript(versioned('/vendor/addon-fit.js'));
  return { Terminal: window.Terminal, FitAddon: window.FitAddon.FitAddon };
}

function closeTerminal() {
  const t = state.term;
  if (!t) return;
  state.term = null;
  t.closed = true;
  clearInterval(t.ping);
  t.ws?.close();
  t.term?.dispose();
  window.removeEventListener('resize', t.onResize);
  window.visualViewport?.removeEventListener('resize', t.onViewport);
  window.visualViewport?.removeEventListener('scroll', t.onViewport);
  document.documentElement.style.removeProperty('--vvh');
  document.documentElement.style.removeProperty('--vv-top');
}

// trackViewport keeps the terminal page inside the visible viewport, so the
// on-screen keyboard pushes the key bar up instead of covering it.
function trackViewport() {
  const vv = window.visualViewport;
  if (!vv) return () => { };
  const root = document.documentElement.style;
  return () => {
    root.setProperty('--vvh', vv.height + 'px');
    root.setProperty('--vv-top', vv.offsetTop + 'px');
  };
}

async function renderSession(seq, id) {
  // The lookup reads the store only, so it's quick; then the page is a
  // chat or a terminal.
  const found = api('GET', '/api/sessions/' + id).catch(e => e.status === 404 ? Promise.reject(e) : null);
  const d = await found;
  if (seq !== state.seq) return;
  if (d?.session?.mode === 'chat') {
    document.body.classList.replace('term-mode', 'chat-mode');
    return renderChat(seq, d);
  }
  return renderTerminal(seq, id, found);
}

async function renderTerminal(seq, id, found) {
  const xterm = loadXterm().catch(() => null);
  const host = el('div.term-host');
  const past = el('div.term-past');
  const scroller = el('div.term-scroll', null, past, host);
  const title = el('span.title', null, id);
  const sub = el('span.sub');
  const status = el('span.state', { role: 'status' }, el('span.dot'), el('span.txt', null, 'Connecting'));
  const setStatus = (cls, text) => { status.className = 'state' + (cls ? ' ' + cls : ''); status.replaceChildren(el('span.dot'), el('span.txt', null, text)) };
  const fontBtn = (label, d) => el('button.icon-btn.bare.font-btn', {
    type: 'button', 'aria-label': d < 0 ? 'Smaller text' : 'Larger text',
    onmousedown: e => e.preventDefault(), onclick: () => state.term?.zoom(d),
  }, label);
  let harness = '', compose = () => { };
  const composeBtn = el('button.icon-btn.bare.coarse-only' + (prefs.get('compose') === '1' ? '.on' : ''), {
    type: 'button', 'aria-label': 'Message box', title: 'Message box',
    onmousedown: e => e.preventDefault(), onclick: () => compose(),
  }, svg(ICON.pencil));
  let parent = '';
  const toChat = el('button.btn.sm.to-chat', { type: 'button', hidden: true, onclick: () => navigate('/s/' + parent) }, svg(ICON.back), 'Chat');
  const page = el('div.term-page' + (prefs.get('compose') === '1' ? '.composing' : ''), null,
    el('div.term-bar', null,
      el('button.icon-btn.bare', { onclick: () => parent ? navigate('/s/' + parent) : history.length > 1 ? history.back() : navigate('/'), 'aria-label': 'Back' }, svg(ICON.back)),
      el('div.term-title', null, title, sub),
      status,
      toChat,
      composeBtn,
      el('div.font-group', null, fontBtn('A−', -1), fontBtn('A+', 1))),
    scroller);
  $('#app').replaceChildren(page);

  const d = await found;
  if (seq !== state.seq) return;
  if (d) {
    const s = d.session;
    crumb(d.project, sessionName(s));
    harness = s.harness;
    title.textContent = sessionName(s);
    sub.textContent = `${s.harness} · ${d.project.name}`;
    if (s.parent) {
      // A chat continued here: the chat resumes when you go back to it.
      parent = s.parent;
      toChat.hidden = false;
      sub.textContent = `${s.harness} terminal · back to the chat when you're done`;
    }
  }

  const X = await xterm;
  if (seq !== state.seq) return;
  if (!X) {
    // Without xterm.js, still show the screen, read-only.
    const pre = el('pre.term-fallback', null, 'xterm.js is not bundled in this build (see README: make vendor). Showing the screen read-only.\n');
    scroller.replaceWith(pre);
    setStatus('', 'Read-only');
    const update = async () => { try { pre.textContent = (await api('GET', `/api/sessions/${id}/snapshot?lines=200`)).text } catch { } };
    update();
    state.timers.push(setInterval(update, 2000));
    return;
  }
  const dark = document.documentElement.dataset.theme !== 'light';
  const term = new X.Terminal({
    fontFamily: '"JetBrains Mono", ui-monospace, Menlo, monospace', fontSize: termFont(prefs.get('termFont')),
    // No scrollback of its own: the history above it is (see pull).
    lineHeight: 1.15, cursorBlink: true, scrollback: 0, allowProposedApi: false,
    theme: dark ? { background: '#141412', foreground: '#F1EEE7', cursor: '#F6C443', selectionBackground: '#4A3F1A' }
      : { background: '#FBFAF7', foreground: '#1C1B18', cursor: '#9A5B00', selectionBackground: '#FBEBB5' },
  });
  const fit = new X.FitAddon();
  term.loadAddon(fit);
  term.open(host);
  const t = { term, fit, ws: null, closed: false };
  state.term = t;

  const enc = new TextEncoder();
  const send = s => { if (s && t.ws?.readyState === WebSocket.OPEN) t.ws.send(enc.encode(s)) };
  const sendSize = () => { if (t.ws?.readyState === WebSocket.OPEN) t.ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols })) };
  const refit = () => {
    const was = term.cols + 'x' + term.rows;
    fit.fit();
    sendSize();
    // tmux reflows its history to the new size: load it again.
    if (term.cols + 'x' + term.rows !== was) t.resetHistory?.();
  };
  const setFont = n => scroller.style.setProperty('--term-font', n + 'px');
  setFont(term.options.fontSize);
  t.send = send;
  t.onResize = debounce(refit, 120);
  const viewport = trackViewport();
  t.onViewport = () => { viewport(); t.onResize() };
  viewport();
  window.addEventListener('resize', t.onResize);
  window.visualViewport?.addEventListener('resize', t.onViewport);
  window.visualViewport?.addEventListener('scroll', t.onViewport);
  t.zoom = d => {
    const n = termFont(term.options.fontSize + d);
    if (n === term.options.fontSize) return;
    term.options.fontSize = n;
    setFont(n);
    prefs.set('termFont', String(n));
    refit();
  };
  term.onData(send);

  // Copying: drag to select (tmux leaves the mouse alone), then Ctrl/Cmd+C,
  // which sends ^C when nothing is selected. The history above is page text.
  term.attachCustomKeyEventHandler(e => {
    if (e.type === 'keydown' && (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'c' && term.hasSelection()) {
      copyText(term.getSelection());
      term.clearSelection();
      return false;
    }
    return true;
  });
  const msg = messageBox(t, harness);
  compose = () => {
    const on = page.classList.toggle('composing');
    prefs.set('compose', on ? '1' : '0');
    composeBtn.classList.toggle('on', on);
    on ? $('.msg-input', msg).focus() : $('.msg-input', msg).blur();
    t.onResize();
  };
  page.append(msg);
  fit.fit();

  // Scrolling. What has scrolled off the screen is tmux's history, drawn
  // above the terminal as page text, in one native scroller with the
  // terminal at its foot: it follows the finger, keeps a flick going and can
  // be selected. Rows are added as output arrives; while you're scrolled up,
  // what you're reading stays where it is.
  let size = -1, stale = false, pulling = false, again = false, timer = 0;
  const atBottom = () => scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 24;
  const pull = async () => {
    if (pulling) { again = true; return }
    pulling = true;
    try {
      const h = await api('GET', `/api/sessions/${id}/history` + (size >= 0 ? `?since=${size}` : ''));
      // A fresh load (opening, a resize) starts at the bottom.
      const bottom = size < 0 || atBottom();
      // A replacement moves everything; it waits until you're at the bottom.
      if (h.replace && size >= 0 && !bottom) { stale = true; return }
      stale = false;
      if (h.replace) past.replaceChildren();
      if (h.text) past.append(historyRows(h.text));
      size = h.size;
      if (bottom) {
        while (past.childElementCount > 6000) past.firstChild.remove();
        scroller.scrollTop = scroller.scrollHeight;
      }
    } catch { } finally {
      pulling = false;
      if (again) { again = false; schedule() }
    }
  };
  const schedule = () => { if (!timer) timer = setTimeout(() => { timer = 0; pull() }, 300) };
  t.resetHistory = () => { size = -1; schedule() };
  scroller.addEventListener('scroll', () => { if (stale && atBottom()) schedule() }, { passive: true });
  // xterm.js has no history to scroll and would turn the wheel into arrow
  // keys; the scroller takes it instead.
  host.addEventListener('wheel', e => e.stopPropagation(), { capture: true, passive: true });
  host.addEventListener('touchmove', e => e.stopPropagation(), { capture: true, passive: true });

  const connect = () => {
    if (t.closed) return;
    setStatus('', 'Connecting');
    const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/sessions/${id}/attach`);
    ws.binaryType = 'arraybuffer';
    t.ws = ws;
    ws.onopen = () => {
      refit();
      // The server drops viewers that go quiet for 90s.
      clearInterval(t.ping);
      t.ping = setInterval(() => { if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'ping' })) }, 25000);
      setStatus('on', 'Attached');
      if (!coarse()) term.focus();
      // Once tmux has resized the pane to this viewer.
      setTimeout(t.resetHistory, 250);
    };
    ws.onmessage = e => {
      term.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data));
      schedule();
    };
    ws.onclose = e => {
      clearInterval(t.ping);
      if (t.closed) return;
      setStatus('', e.reason || 'Disconnected');
      const box = el('div.term-overlay', null, el('div.box', null,
        el('b', null, e.reason === 'session ended' ? 'The session ended' : 'Disconnected'),
        el('div.row', null,
          el('button.btn.primary.sm', { onclick: () => { box.remove(); connect() } }, 'Reconnect'),
          el('button.btn.sm', { onclick: () => history.back() }, 'Back'))));
      host.append(box);
    };
  };
  connect();
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast('Copied');
  } catch { toast('Copy failed', 'error') }
}

// historyRows draws tmux history rows (capture-pane -e) as page text. The
// colours come from sgrRuns; the text only ever goes in as text.
function historyRows(text) {
  const frag = document.createDocumentFragment();
  for (const row of text.split('\n')) {
    const line = el('div.pl');
    for (const run of sgrRuns(row)) {
      const st = run.style;
      if (!Object.keys(st).length) { line.append(run.text); continue }
      const span = el('span', null, run.text);
      let fg = st.fg, bg = st.bg;
      if (st.inverse) [fg, bg] = [bg || 'var(--term-bg)', fg || 'var(--term-fg)'];
      if (fg) span.style.color = fg;
      if (bg) span.style.backgroundColor = bg;
      if (st.bold) span.style.fontWeight = '700';
      if (st.dim) span.style.opacity = '.6';
      if (st.italic) span.style.fontStyle = 'italic';
      const deco = [st.underline && 'underline', st.strike && 'line-through'].filter(Boolean).join(' ');
      if (deco) span.style.textDecoration = deco;
      line.append(span);
    }
    frag.append(line);
  }
  return frag;
}

// coarse is true on touch screens, where the message box can show.
const coarse = () => matchMedia('(pointer: coarse), (max-width: 760px)').matches;

// messageBox is a real text field for phones: autocorrect and dictation
// work, and the text reaches the agent as one paste followed by Enter. Sent
// empty, it is just Enter, to confirm a prompt.
function messageBox(t, harness) {
  const input = el('textarea.msg-input', {
    rows: '1', placeholder: harness ? `Message ${harness}` : 'Message', 'aria-label': 'Message to the terminal',
    enterkeyhint: 'send', autocapitalize: 'sentences', spellcheck: 'true',
  });
  const grow = () => { input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, 132) + 'px' };
  const submit = e => {
    e?.preventDefault();
    const text = input.value.replace(/\s+$/, '');
    if (text) t.send(messageBytes(text));
    // A separate write, so the harness sees Enter, not part of the paste.
    setTimeout(() => t.send('\r'), text ? 60 : 0);
    input.value = '';
    grow();
  };
  input.addEventListener('input', grow);
  input.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) submit(e) });
  return el('form.msg-bar', { onsubmit: submit },
    input,
    el('button.msg-send', { type: 'submit', 'aria-label': 'Send', onmousedown: e => e.preventDefault() }, svg(ICON.send)));
}

// --- notifications ----------------------------------------------------------

Object.assign(ICON, {
  bell: ['M6 8a6 6 0 1 1 12 0c0 7 3 9 3 9H3s3-2 3-9', 'M10.3 21a1.94 1.94 0 0 0 3.4 0'],
  bellOff: ['M6 8a6 6 0 0 1 9.3-5', 'M18 8c0 7 3 9 3 9H9', 'M6 8c0 7-3 9-3 9h3', 'M10.3 21a1.94 1.94 0 0 0 3.4 0', 'M3 3l18 18'],
});

const pushSupported = () => 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
const standalone = () => matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;
const isIOS = () => /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

function b64url(s) {
  const b = atob((s + '='.repeat((4 - s.length % 4) % 4)).replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(b, c => c.charCodeAt(0));
}

// setupPush offers notifications on this device: approvals, sign-ins,
// failures and finished work reach the phone even with Skybreaker closed. On an
// iPhone that needs Skybreaker added to the home screen.
async function setupPush() {
  const btns = [...document.querySelectorAll('.push-btn')];
  if (!('serviceWorker' in navigator)) return;
  let reg;
  try { reg = await navigator.serviceWorker.register('/sw.js') } catch { return }
  const iosBrowser = isIOS() && !standalone();
  if (!pushSupported() && !iosBrowser) return;
  btns.forEach(b => { b.hidden = false });
  const draw = async () => {
    const sub = pushSupported() ? await reg.pushManager.getSubscription() : null;
    const on = !!sub && Notification.permission === 'granted';
    for (const btn of btns) {
      btn.classList.toggle('on', on);
      btn.replaceChildren(svg(on ? ICON.bell : ICON.bellOff));
      btn.title = on ? 'Notifications are on' : 'Turn on notifications';
      btn.setAttribute('aria-label', btn.title);
    }
    return on ? sub : null;
  };
  const toggle = async () => {
    if (iosBrowser || !pushSupported()) { toast('On iPhone, add Skybreaker to your home screen first (Share, then Add to Home Screen), and turn notifications on from there.'); return }
    try {
      const sub = await reg.pushManager.getSubscription();
      if (sub && Notification.permission === 'granted') {
        if (!confirm('Turn off notifications on this device?')) return;
        await api('DELETE', '/api/push/subscribe', { endpoint: sub.endpoint });
        await sub.unsubscribe();
        toast('Notifications off');
      } else {
        if (await Notification.requestPermission() !== 'granted') { toast('Notifications are blocked for Skybreaker in your settings', 'error'); return }
        const { key } = await api('GET', '/api/push/key');
        const s = sub || await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64url(key) });
        await api('POST', '/api/push/subscribe', s.toJSON());
        toast('Notifications on: you\'ll hear when an agent needs you');
      }
    } catch (e) { toast(e.message, 'error') }
    draw();
  };
  btns.forEach(b => b.addEventListener('click', toggle));
  // Send an existing subscription again: the server may have lost it.
  const sub = await draw();
  if (sub) api('POST', '/api/push/subscribe', sub.toJSON()).catch(() => { });
}

// activityBadge lights the top bar's Activity button while a network request
// waits for you.
const activityBadge = debounce(async () => {
  try {
    const n = (await api('GET', '/api/requests')).filter(a => a.state === 'pending').length;
    for (const b of document.querySelectorAll('.activity-link')) {
      b.classList.toggle('has', n > 0);
      b.title = n ? `Activity: ${n} waiting` : 'Activity';
    }
  } catch { }
}, 500);

// --- the sidebar (desktop) ------------------------------------------------------

const wide = () => matchMedia('(min-width: 1024px)').matches;

// sidebar draws the desktop's navigation from the inbox: how much needs
// you, and each project with its live chats.
function sidebar(d) {
  state.inbox = d;
  const g = inboxGroups(d.sessions, d.pending);
  const needs = g.forYou.length + (d.requests || []).length + (d.review || []).length;
  // The logo's ring turns while any agent is working, like the halo in a chat.
  const working = d.sessions.some(s => s.status === 'running' && s.state === 'working');
  for (const b of document.querySelectorAll('.brand')) b.classList.toggle('working', working);
  const count = $('#inbox-count');
  count.hidden = !needs;
  count.textContent = String(needs);
  $('#side-user').textContent = state.meta?.user || '';
  $('#side-projects').replaceChildren(...d.projects.map(p => el('a.side-link.side-project.tint-' + p.tint, { href: '/p/' + encodeURIComponent(p.name), 'data-link': true, 'data-project': p.name },
    el('span.pdot'), el('span', null, p.name),
    p.running ? el('span.side-live', { title: p.live ? plural(p.live, 'live chat') : 'Sandbox running' }, el('span.dot'), p.live ? String(p.live) : '') : null)),
    el('button.side-link.side-new', { type: 'button', onclick: newProjectSheet }, svg(ICON.plus), el('span', null, 'New project')));
  markSidebar();
}

// refreshSidebar keeps the sidebar current on pages that don't fetch the
// inbox themselves.
const refreshSidebar = debounce(async () => {
  if (!wide()) return;
  try { sidebar(await api('GET', '/api/inbox')) } catch { }
}, 800);

// markSidebar highlights where you are, and points New chat at the project
// you're in.
function markSidebar() {
  const r = state.route || {};
  const project = r.name === 'project' ? r.project : r.name === 'session' ? state.chat?.project : null;
  $('#nav-inbox').classList.toggle('on', r.name === 'home');
  $('#nav-activity').classList.toggle('on', r.name === 'activity');
  $('#nav-settings').classList.toggle('on', r.name === 'settings');
  for (const a of document.querySelectorAll('.side-project')) a.classList.toggle('on', a.dataset.project === project);
  $('#new-chat-label').textContent = project ? 'New chat in ' + project : 'New chat';
  state.newChatIn = project;
}

function startNewChat() {
  const projects = state.inbox?.projects || state.projects || [];
  const p = projects.find(x => x.name === state.newChatIn);
  p ? pickHarness(p) : newSessionSheet(projects);
}

// --- start ------------------------------------------------------------------

async function boot() {
  setupTheme();
  try {
    state.meta = await api('GET', '/api/meta');
  } catch (e) { toast(e.message, 'error') }
  setupPush();
  activityBadge();
  $('#new-chat').addEventListener('click', startNewChat);
  addEventListener('keydown', e => {
    if (e.key === 'n' && !e.metaKey && !e.ctrlKey && !e.altKey && !/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName) && !document.activeElement?.isContentEditable && !$('#sheet-root').firstChild && state.route?.name !== 'session') {
      e.preventDefault();
      startNewChat();
    }
  });
  liveEvents(ev => {
    const here = state.route?.name === 'session' && state.route.id === ev.session;
    if (ev.type === 'hook' && ev.data?.kind === 'permission' && state.route?.name === 'session' && !here) {
      toast('An agent is waiting for approval', '', { label: 'Review', run: () => navigate('/') });
    }
    if (ev.type === 'session' && state.route?.name === 'session' && !here && (ev.data === 'waiting' || ev.data === 'login')) {
      toast(`A session in ${ev.project} needs you`, '', { label: 'Open', run: () => navigate('/s/' + ev.session) });
    }
    if (ev.type === 'request' && ev.data === 'pending' && state.route?.name !== 'home' && state.route?.name !== 'activity'
      && !(state.chat && state.chat.project === ev.project)) {
      toast(`${ev.project} wants to reach out`, '', { label: 'Review', run: () => navigate('/') });
    }
    if (ev.type === 'request') { state.chat?.refreshRequests?.(); activityBadge() }
    if (ev.type === 'board' && state.itemSheet && state.itemSheet.ref === ev.data) state.itemSheet.reload();
    if (here && state.chat) state.chat.refreshMeta();
    refreshSidebar();
    refresh();
  });
  go();
  refreshSidebar();
}

boot();
