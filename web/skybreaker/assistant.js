'use strict';

// An assistant's pages: Today (its brief), Memory, Routines, Look, and its
// browser, to watch or take over. What the assistant wrote (memories,
// updates, page frames) only goes in through textContent or a canvas.

// asstLookOf is a project's assistant look, or a plain one until it has its own.
const asstLookOf = p => p?.look || { name: p?.name || '', shape: 'dot', color: 'clay', temper: 'calm', ring: 'fine', eye: 'none', finish: 'paper' };
const asstCap = s => s.charAt(0).toUpperCase() + s.slice(1);

// asstPicker is a row of choices as one radio group: arrow keys move and
// choose, as in any radio group. draw(value) re-marks the choice.
function asstPicker(label, cls, values, value, onPick, face) {
  const group = el('div.' + cls, { role: 'radiogroup', 'aria-label': label });
  const draw = v => {
    value = v;
    group.replaceChildren(...values.map(x => {
      const on = x === value;
      const b = el('button.a-pick' + (on ? '.on' : ''), { type: 'button', role: 'radio', 'aria-checked': String(on), tabindex: on ? '0' : '-1', 'aria-label': asstCap(x), title: asstCap(x), onclick: () => onPick(x) }, face(x));
      return b;
    }));
  };
  group.addEventListener('keydown', e => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
    if (!step) return;
    e.preventDefault();
    const next = values[(values.indexOf(value) + step + values.length) % values.length];
    onPick(next);
    // A pick can draw the picker anew: focus goes to the one on the page.
    requestAnimationFrame(() => {
      const live = group.isConnected ? group : document.querySelector(`.${cls}[aria-label="${CSS.escape(label)}"]`);
      live?.querySelector('[aria-checked="true"]')?.focus();
    });
  });
  draw(value);
  group.draw = draw;
  return group;
}

// asstShapes and asstColors pick a mark's shape and colour.
const asstShapes = (look, onPick, big) => asstPicker('Shape', big ? 'a-shapes' : 'na-shapes', Object.keys(AV_SHAPES), look.shape, onPick,
  sh => big ? [avatar({ ...look, shape: sh }), el('span', null, asstCap(sh))] : avatar({ ...look, shape: sh }, { mini: true }));
const asstColors = (look, onPick) => asstPicker('Colour', 'a-colors', Object.keys(AV_COLORS), look.color, onPick, k => {
  const dot = el('span.a-swatch');
  dot.style.background = AV_COLORS[k];
  return dot;
});

const asstPath = (name, tab) => '/a/' + encodeURIComponent(name) + (tab && tab !== 'today' ? '/' + tab : '');
const asstAPI = name => '/api/assistants/' + encodeURIComponent(name);

Object.assign(ICON, {
  keyboard: ['M3 7h18v10H3z', 'M7 10h.01', 'M11 10h.01', 'M15 10h.01', 'M8 14h8'],
  lock: ['M6 11h12v9H6z', 'M8.5 11V8a3.5 3.5 0 0 1 7 0v3'],
  memory: ['M12 3a7 7 0 0 0-4 12.7V19h8v-3.3A7 7 0 0 0 12 3z', 'M9 22h6'],
  sparkle: ['M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z'],
  hand: ['M8 13V5.5a1.5 1.5 0 0 1 3 0V12', 'M11 11.5V4a1.5 1.5 0 0 1 3 0v8', 'M14 11.5V6a1.5 1.5 0 0 1 3 0v8a7 7 0 0 1-7 7h-.5A6.5 6.5 0 0 1 4 14.5V11a1.5 1.5 0 0 1 3 0v2'],
});

// asstMotion is how the mark moves: amber while something waits on you,
// turning while one job works, with a moon for each when several do.
function asstMotion(needs, jobs) {
  if (needs) return { state: 'needs' };
  if (jobs > 1) return { state: 'busy', jobs };
  return { state: jobs ? 'thinking' : 'idle' };
}

const asstState = (needs, sessions) =>
  asstMotion(needs, (sessions || []).filter(s => s.status === 'running' && s.state === 'working').length);

// asstAccent gives a page the assistant's colour in place of Skybreaker's
// yellow: its spinner, its send button, its working line.
function asstAccent(node, color) {
  const c = AV_COLORS[color] || AV_COLORS.clay;
  node.style.setProperty('--sun', c);
  node.style.setProperty('--working', c);
  node.style.setProperty('--working-bg', `color-mix(in srgb, ${c} 18%, var(--surface))`);
}

function closeAssistant() {
  state.asstLive?.close();
  state.asstLive = null;
}

// asstOwn keeps the way to stop a browser view on its own page, for when
// you leave it.
const asstOwn = close => { state.asstLive = { close } };

async function renderAssistant(seq, name, tab) {
  const enc = encodeURIComponent(name);
  const d = await api('GET', '/api/projects/' + enc);
  if (seq !== state.seq) return;
  const p = d.project;
  // A repository's project has a page of its own.
  if (p.kind !== 'assistant') { navigate('/p/' + enc); return }
  const a = await api('GET', asstAPI(name));
  if (seq !== state.seq) return;
  state.asst = { p, a };
  // A live update to the Library redraws its rows in place when it can.
  if (tab === 'library' && !state.moved && await libRefresh(seq, name)) return;
  asstCrumb(p);
  const needs = a.proposals.length + d.pending.length + (d.requests || []).length;
  const look = a.look;
  const memoryAsks = a.proposals.filter(x => x.kind === 'memory').length;
  const routineAsks = a.proposals.filter(x => x.kind === 'routine').length;
  const head = el('header.a-head', null,
    // Today's greeting carries the mark; the other tabs show it here.
    tab === 'today' ? null : avatar(look, { ...asstState(needs, d.sessions), cls: 'a-head-mark' }),
    el('div.a-title', null, el('h1', null, look.name),
      el('span.a-sub', null, (p.harnesses || []).filter(h => h !== 'shell').map(harnessLabel).join(' · '),
        el('span.sep', null, '·'), asstMemories(a.memories?.length || 0),
        el('span.sep', null, '·'), el('a.a-access', { href: '/settings/access/' + enc, 'data-link': true }, 'Access'))));
  const tabs = [['today', 'Today', 0], ['library', 'Library', 0], ['chat', 'Chat', 0], ['memory', 'Memory', memoryAsks], ['routines', 'Routines', routineAsks], ['look', 'Look', 0], ['browser', 'Browser', 0]];
  const tabBar = tab === 'takeover' ? null : el('nav.p-tabs.a-tabs', { 'aria-label': look.name }, tabs.map(([k, label, n]) =>
    el('a.p-tab' + (k === tab ? '.on' : ''), { href: asstPath(name, k), 'data-link': true, 'aria-current': k === tab ? 'page' : null },
      el('span', null, label), n ? el('span.n.warn', null, String(n)) : null)));
  let body;
  switch (tab) {
    case 'library': body = await asstLibrary(seq, name, look); break;
    case 'memory': body = asstMemory(name, a); break;
    case 'routines': body = await asstRoutines(seq, name, a); break;
    case 'look': body = asstLook(name, a); break;
    case 'browser': body = asstWatch(name, look); break;
    case 'takeover': body = asstTakeover(name, look); break;
    default: body = await asstToday(seq, name, p, a, d);
  }
  if (seq !== state.seq) return;
  const page = el('div.page.assistant.a-tab-' + tab, null, tab === 'takeover' ? null : head, tabBar, body);
  asstAccent(page, look.color);
  // On a phone, which has no sidebar, the + opens its thread.
  const fab = tab === 'takeover' ? null : el('a.fab', { href: asstPath(name, 'chat'), 'data-link': true, 'aria-label': 'Message ' + look.name }, svg(ICON.plus));
  if (fab) asstAccent(fab, look.color);
  $('#app').replaceChildren(...[page, fab].filter(Boolean));
  // A phone scrolls its tabs: the one you're on stays in sight.
  tabBar?.querySelector('.on')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}

function asstCrumb(p) {
  const c = $('#crumb');
  c.replaceChildren(el('span.sep', null, '/'), el('a.crumb-pill.a-crumb', { href: asstPath(p.name), 'data-link': true },
    avatar(asstLookOf(p), { mini: true }), el('span', null, asstLookOf(p).name)));
}

const asstMemories = n => n === 1 ? '1 memory' : n + ' memories';

const asstGreeting = () => {
  const h = new Date().getHours();
  return h < 5 ? 'Good evening.' : h < 12 ? 'Good morning.' : h < 18 ? 'Good afternoon.' : 'Good evening.';
};

const ASST_WORDS = ['No', 'One', 'Two', 'Three', 'Four', 'Five', 'Six', 'Seven', 'Eight', 'Nine', 'Ten'];
const asstCount = n => ASST_WORDS[n] || String(n);

// asstSummary says in a sentence what it has done lately.
const asstSummary = done => done ? `${asstCount(done)} thing${done === 1 ? '' : 's'} done in the last day.` : 'Nothing new in the last day.';

// asstNeeds is what waits on you, as cards: a sign-in first, since it
// holds the assistant up.
function asstNeeds(name, p, t) {
  return [
    ...t.proposals.filter(x => x.kind === 'takeover').map(x => asstTakeoverCard(name, x)),
    ...t.requests.map(r => requestCard(r, p.tint)),
    ...t.approvals.map(r => approvalCard(r, p.tint)),
    ...t.proposals.filter(x => x.kind === 'memory' || x.kind === 'routine').map(x => x.kind === 'routine' ? asstRoutineAsk(name, x) : asstMemoryAsk(name, x)),
  ];
}

// asstNeedsSheet shows what waits on you, and keeps up as you answer it.
function asstNeedsSheet(name, p) {
  const body = el('div.a-asks');
  const load = async () => {
    try {
      const cards = asstNeeds(name, p, await api('GET', asstAPI(name) + '/today'));
      if (!cards.length) { closeSheet(); return }
      body.replaceChildren(...cards);
    } catch (e) { toast(e.message, 'error') }
  };
  sheet('For you', body);
  state.sheetReload = load;
  load();
}

// Today leads with what the assistant did; what waits on you is one quiet
// pill that opens it.
async function asstToday(seq, name, p, a, d) {
  const t = await api('GET', asstAPI(name) + '/today');
  if (seq !== state.seq) return null;
  const takeovers = t.proposals.filter(x => x.kind === 'takeover');
  const needs = asstNeeds(name, p, t).length;
  const finished = t.runs.filter(r => r.status !== 'running');
  // Tasks it finished from the thread, in the last day.
  const tasks = (d.sessions || []).filter(s => s.task && s.state === 'idle' && s.resume && Date.parse(s.updated) > Date.now() - 864e5);
  const lately = finished.length + tasks.length + t.updates.length;
  const brief = asstBrief(finished);
  const cols = new Map(t.collections.map(c => [c.id, c]));
  // What's new in the library leads, and takes you there.
  const fresh = t.fresh ? el('a.a-fresh', { href: asstPath(name, 'library'), 'data-link': true, onclick: () => libFilter.set(name, 'new') },
    `${asstCount(t.fresh)} new in the library.`) : null;
  const site = asstBrowsing(name, t.browser, takeovers.length > 0);
  const pill = needs ? el('button.a-needs-pill', { type: 'button', onclick: () => asstNeedsSheet(name, p) },
    el('i'), takeovers.length ? 'Sign-in needed' : needs + ' for you') : null;
  const hero = el('div.a-hero', null,
    avatar(t.look, { ...asstState(needs, d.sessions), cls: 'a-hero-mark' }),
    el('div.a-hero-text', null, el('h2.a-greeting', null, asstGreeting()),
      brief ? el('p.a-brief', null, brief.text, ' ', el('a.a-brief-from', { href: '/r/' + encodeURIComponent(brief.run.id), 'data-link': true }, brief.run.job + ', ' + ago(brief.run.finished))) : null,
      el('p', null, fresh || asstSummary(lately), site ? [' ', site] : null, pill)));
  return el('div.a-today', null, hero,
    lately ? el('section.a-sec', null, el('h2.a-label', null, 'What ' + t.look.name + ' did'),
      el('div.a-done', null,
        ...finished.map(r => el('a.a-row', { href: '/r/' + encodeURIComponent(r.id), 'data-link': true },
          el('span.a-ic', null, svg(r.status === 'ok' ? ICON.check : ICON.clock)),
          el('span.a-row-text', null, el('b', null, r.job), el('span.a-row-sub', null, oneLine(r.summary) || runWord(r))),
          el('span.a-row-end', null, ago(r.finished || r.started)))),
        ...tasks.map(s => el('a.a-row', { href: '/s/' + encodeURIComponent(s.id), 'data-link': true },
          el('span.a-ic', null, svg(ICON.check)),
          el('span.a-row-text', null, el('b', null, chatName(s)), el('span.a-row-sub', null, 'Task')),
          el('span.a-row-end', null, ago(s.updated)))),
        ...t.updates.map(u => t.items[u.item]
          ? libRow(name, t.look, t.items[u.item], cols.get(t.items[u.item].collection))
          : el('div.a-row', null,
            el('span.a-ic', null, svg(ICON.note)),
            el('span.a-row-text', null, el('b', null, u.text)),
            el('span.a-row-end', null, ago(u.at)))))) : null);
}

// renderThread opens the assistant's one chat with you, made on the first
// visit and carried on to its preferred agent.
async function renderThread(seq, name) {
  const th = await api('POST', asstAPI(name) + '/thread', {});
  if (seq !== state.seq) return;
  const d = await api('GET', '/api/sessions/' + encodeURIComponent(th.id));
  if (seq !== state.seq) return;
  document.body.classList.replace('term-mode', 'chat-mode');
  return renderChat(seq, d);
}

const runWord = r => r.status === 'ok' ? 'Finished' : r.status === 'running' ? 'Running' : r.status === 'failed' ? 'Failed' + (r.error ? ': ' + r.error : '') : r.status;

// asstTakeoverCard asks you to take the browser over: on its own page, or
// with take, wherever take opens it.
function asstTakeoverCard(name, x, take) {
  return el('div.a-card.a-need', null,
    el('div.a-need-text', null, el('div.a-kick', null, 'The browser needs you'), el('b', null, x.title),
      el('p', null, 'Take over to sign in or enter a code. The assistant can’t see what you type.')),
    el('div.a-acts', null,
      take ? el('button.btn.primary.sm', { type: 'button', onclick: take }, svg(ICON.hand), 'Take over')
        : el('a.btn.primary.sm', { href: asstPath(name, 'takeover'), 'data-link': true }, svg(ICON.hand), 'Take over'),
      el('button.btn.ghost.sm', { type: 'button', onclick: e => asstDo(asstProposal(name, x, 'dismiss'), 'Dismissed', {}, e) }, 'Not now')));
}

// asstDo answers a card. Its buttons wait while it does, so a second
// click can't answer it twice.
async function asstDo(path, done, body = {}, e) {
  const btns = e?.target.closest('.a-acts')?.querySelectorAll('button') || [];
  btns.forEach(b => { b.disabled = true });
  try {
    await api('POST', path, body);
    if (done) toast(done);
    refresh();
  } catch (err) {
    toast(err.message, 'error');
    btns.forEach(b => { b.disabled = false });
  }
}
const asstProposal = (name, x, verb) => asstAPI(name) + '/proposals/' + encodeURIComponent(x.id) + '/' + verb;

// asstProvenance says where a memory or suggestion came from.
function asstProvenance(x) {
  const when = new Date(x.at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  if (x.by) return 'Added by ' + x.by.split('@')[0] + ' · ' + when;
  if (x.session) return el('span', null, el('a', { href: '/s/' + encodeURIComponent(x.session), 'data-link': true }, 'From a chat'), ' · ' + when);
  return 'Suggested · ' + when;
}

function asstMemoryAsk(name, x) {
  return el('div.a-card.a-need', null,
    el('div.a-need-text', null, el('div.a-kick', null, 'Wants to remember'), el('b', null, x.title),
      x.body ? el('p', null, x.body) : null, el('span.a-prov', null, asstProvenance(x))),
    el('div.a-acts', null,
      el('button.btn.primary.sm', { type: 'button', onclick: e => asstDo(asstProposal(name, x, 'remember'), 'Remembered', {}, e) }, svg(ICON.check), 'Remember'),
      el('button.btn.ghost.sm', { type: 'button', onclick: () => asstMemorySheet(name, x, true) }, 'Edit'),
      el('button.btn.ghost.sm', { type: 'button', onclick: e => asstDo(asstProposal(name, x, 'dismiss'), 'Not remembered', {}, e) }, 'Don’t')));
}

function asstRoutineAsk(name, x) {
  const r = x.routine || {};
  return el('div.a-card.a-need', null,
    el('div.a-need-text', null, el('div.a-kick', null, 'Suggests a routine'), el('b', null, r.name),
      el('span.a-prov', null, el('span.mono', null, r.schedule), ' · ', asstSchedule(r.schedule)),
      el('p', null, r.prompt)),
    // Routines are settings: only an admin approves one.
    el('div.a-acts', null,
      state.meta?.admin ? el('button.btn.primary.sm', { type: 'button', onclick: e => asstDo(asstProposal(name, x, 'approve'), 'Saved: it runs once Skybreaker restarts', {}, e) }, svg(ICON.check), 'Approve') : el('span.np-hint', null, 'An admin approves routines'),
      el('button.btn.ghost.sm', { type: 'button', onclick: e => asstDo(asstProposal(name, x, 'dismiss'), 'Dismissed', {}, e) }, 'Dismiss')));
}

// --- memory -------------------------------------------------------------------

function asstMemory(name, a) {
  const asks = a.proposals.filter(x => x.kind === 'memory');
  const search = el('input.a-search', { type: 'search', placeholder: 'Search memories', 'aria-label': 'Search memories', autocomplete: 'off' });
  const list = el('div.a-mems');
  const draw = () => {
    const q = search.value.trim().toLowerCase();
    const shown = a.memories.filter(m => !q || (m.title + '\n' + (m.body || '')).toLowerCase().includes(q)).reverse();
    list.replaceChildren(...shown.map(m => el('div.a-mem', null,
      el('div.a-mem-text', null, el('b', null, m.title), m.body ? el('p', null, m.body) : null, el('span.a-prov', null, asstProvenance(m))),
      el('div.a-mem-acts', null,
        el('button.btn.ghost.sm', { type: 'button', onclick: () => asstMemorySheet(name, m, false) }, 'Edit'),
        el('button.btn.ghost.sm', { type: 'button', onclick: () => asstForget(name, m) }, 'Forget')))));
    if (!shown.length) list.append(el('p.a-empty', null, q ? 'No memories match.' : 'Nothing yet. I\u2019ll suggest things to remember as we talk, and you decide what I keep.'));
  };
  search.addEventListener('input', draw);
  draw();
  return el('div.a-memory', null,
    asks.length ? el('div.a-cards', null, asks.map(x => asstMemoryAsk(name, x))) : null,
    el('div.a-mem-bar', null, search, el('button.btn.sm', { type: 'button', onclick: () => asstMemorySheet(name, null, false) }, svg(ICON.plus), 'Add')),
    list);
}

async function asstForget(name, m) {
  if (!confirm(`Forget “${m.title}”?`)) return;
  try {
    await api('DELETE', asstAPI(name) + '/memories/' + encodeURIComponent(m.id));
    toast('Forgotten');
    refresh();
  } catch (e) { toast(e.message, 'error') }
}

// asstMemorySheet adds a memory, edits one, or edits a suggestion before
// remembering it.
function asstMemorySheet(name, m, proposal) {
  const title = el('input.np-input', { type: 'text', maxlength: '80', required: true, value: m?.title || '' });
  const body = el('textarea.np-input.a-body', { rows: '4', maxlength: '2000' });
  body.value = m?.body || '';
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const save = el('button.btn.primary', { type: 'submit' }, proposal ? 'Remember' : 'Save');
  const form = el('form.np-form', {
    onsubmit: async e => {
      e.preventDefault();
      save.disabled = true;
      const v = { title: title.value.trim(), body: body.value.trim() };
      try {
        if (proposal) await api('POST', asstProposal(name, m, 'remember'), v);
        else if (m) await api('PUT', asstAPI(name) + '/memories/' + encodeURIComponent(m.id), v);
        else await api('POST', asstAPI(name) + '/memories', v);
        closeSheet();
        toast(proposal ? 'Remembered' : 'Saved');
        refresh();
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
        save.disabled = false;
      }
    },
  },
    el('label.np-field', null, el('span.np-label', null, 'Remember'), title),
    el('label.np-field', null, el('span.np-label', null, 'Details'), body, el('span.np-hint', null, 'Optional. It sees the first line in every chat, and the rest when it looks.')),
    error, save);
  sheet(proposal ? 'Remember this?' : m ? 'Edit memory' : 'Add a memory', form);
  title.focus();
}

// --- routines -----------------------------------------------------------------

// asstSchedule says a cron schedule in words, for the common shapes.
function asstSchedule(spec) {
  const days = { '*': 'every day', '1-5': 'weekdays', '0,6': 'weekends', '6,0': 'weekends', '0': 'Sundays', '1': 'Mondays', '2': 'Tuesdays', '3': 'Wednesdays', '4': 'Thursdays', '5': 'Fridays', '6': 'Saturdays', '7': 'Sundays' };
  const m = /^(\d{1,2}) (\d{1,2}) \* \* (\S+)$/.exec(String(spec || '').trim());
  if (m && days[m[3]]) {
    const d = new Date(2000, 0, 1, +m[2], +m[1]);
    const day = days[m[3]];
    return day.charAt(0).toUpperCase() + day.slice(1) + ' at ' + d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  }
  const at = { '@hourly': 'Every hour', '@daily': 'Every day', '@weekly': 'Every week', '@monthly': 'Every month' };
  return at[spec] || spec;
}

async function asstRoutines(seq, name, a) {
  const r = await api('GET', asstAPI(name) + '/routines');
  if (seq !== state.seq) return null;
  const asks = a.proposals.filter(x => x.kind === 'routine');
  const rows = r.routines.map(j => el('div.a-card.a-routine', null,
    el('div.a-routine-head', null,
      el('div', null, el('b', null, j.name), el('span.a-prov', null, asstSchedule(j.schedule), ' · ', el('span.mono', null, j.schedule), ' · ', harnessLabel(j.harness))),
      el('div.a-acts', null,
        el('button.btn.ghost.sm', { type: 'button', onclick: () => asstRunNow(j.name) }, svg(ICON.play), 'Run now'),
        r.editable && j.app ? el('button.btn.ghost.sm', { type: 'button', onclick: () => asstRoutineSheet(name, j) }, 'Edit') : null,
        r.editable && j.app ? el('button.btn.ghost.sm', { type: 'button', onclick: () => asstDeleteRoutine(name, j) }, 'Delete') : null)),
    el('p.a-prompt', null, j.prompt),
    j.runs.length ? el('div.a-runs', null, j.runs.map(x => el('a.a-run', { href: '/r/' + encodeURIComponent(x.id), 'data-link': true },
      el('span.dot.' + (x.status === 'ok' ? 'ok' : x.status === 'running' ? 'run' : 'bad')),
      el('span.a-run-text', null, oneLine(x.summary) || runWord(x)), el('span.a-row-end', null, ago(x.started))))) : null));
  return el('div.a-routines', null,
    r.pending ? el('p.np-status', null, svg(ICON.clock), 'Saved changes run once Skybreaker restarts, when no agent is working.') : null,
    asks.length ? el('div.a-cards', null, asks.map(x => asstRoutineAsk(name, x))) : null,
    rows.length ? rows : el('p.a-empty', null, 'Nothing scheduled yet. Add something for me to do regularly, like a weekday morning brief, or ask me to suggest one.'),
    r.editable ? el('button.empty-add', { type: 'button', onclick: () => asstRoutineSheet(name, null) }, svg(ICON.plus), 'Add a routine')
      : el('p.np-hint', null, 'Only admins add routines.'));
}

async function asstRunNow(job) {
  try {
    const run = await api('POST', '/api/jobs/' + encodeURIComponent(job) + '/run');
    navigate('/r/' + encodeURIComponent(run.id));
  } catch (e) { toast(e.message, 'error') }
}

async function asstDeleteRoutine(name, j) {
  if (!confirm(`Delete the routine “${j.name}”?`)) return;
  try {
    await api('DELETE', asstAPI(name) + '/routines/' + encodeURIComponent(j.name));
    toast('Deleted: it stops once Skybreaker restarts');
    refresh();
  } catch (e) { toast(e.message, 'error') }
}

function asstRoutineSheet(name, j) {
  const field = (label, input, hint) => el('label.np-field', null, el('span.np-label', null, label), input, hint ? el('span.np-hint', null, hint) : null);
  const rname = el('input.np-input', { type: 'text', required: true, pattern: '[a-z0-9][a-z0-9-]{0,47}', placeholder: 'morning-brief', value: j?.name || '', autocapitalize: 'off', spellcheck: 'false' });
  const sched = el('input.np-input.mono', { type: 'text', required: true, placeholder: '30 7 * * 1-5', value: j?.schedule || '', autocapitalize: 'off', spellcheck: 'false' });
  const words = el('span.np-hint');
  const say = () => { words.textContent = sched.value ? asstSchedule(sched.value) : 'minute hour day month weekday, e.g. 30 7 * * 1-5 for weekdays at 7:30' };
  sched.addEventListener('input', say);
  say();
  const prompt = el('textarea.np-input.a-body', { rows: '5', required: true, placeholder: 'Write my morning brief: weather, calendar and anything that needs me.' });
  prompt.value = j?.prompt || '';
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const save = el('button.btn.primary', { type: 'submit' }, j ? 'Save' : 'Add routine');
  const form = el('form.np-form', {
    onsubmit: async e => {
      e.preventDefault();
      save.disabled = true;
      const v = { name: rname.value.trim(), schedule: sched.value.trim(), prompt: prompt.value.trim() };
      try {
        if (j) await api('PUT', asstAPI(name) + '/routines/' + encodeURIComponent(j.name), v);
        else await api('POST', asstAPI(name) + '/routines', v);
        closeSheet();
        toast('Saved: it runs once Skybreaker restarts');
        refresh();
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
        save.disabled = false;
      }
    },
  }, field('Name', rname, 'Lowercase letters, digits and dashes.'), el('label.np-field', null, el('span.np-label', null, 'Schedule'), sched, words), field('What to do', prompt), error, save);
  sheet(j ? 'Edit routine' : 'Add a routine', form);
  (j ? prompt : rname).focus();
}

// --- look ---------------------------------------------------------------------

const ASST_CAPS = {
  idle: 'A slow orbit. Nothing needs you.', thinking: 'The shape turns and the orbit quickens.',
  busy: 'A moon joins the orbit for each thing it has on the go.', needs: 'The planet stops and glows amber until you look.',
  done: 'The orbit settles and slows.',
};

function asstLook(name, a) {
  let look = { ...a.look }, st = 'idle';
  const mark = avatar(look, { cls: 'a-stage-mark' });
  const cap = el('p.a-cap', null, ASST_CAPS[st]);
  let shapes, colors;
  const drawPicks = () => {
    const fresh = [asstShapes(look, sh => save({ shape: sh }), true), asstColors(look, k => save({ color: k }))];
    if (shapes) { shapes.replaceWith(fresh[0]); colors.replaceWith(fresh[1]) }
    [shapes, colors] = fresh;
  };
  // save changes the look at once, and undoes it if the server says no;
  // it throws then too, so a seg() undoes its own choice.
  const save = async next => {
    const was = look;
    look = { ...look, ...next };
    avSet(mark, { look });
    drawPicks();
    try {
      await api('PUT', asstAPI(name) + '/look', look);
      if (next.name) refresh();
      else refreshSidebar();
    } catch (e) {
      look = was;
      avSet(mark, { look });
      drawPicks();
      toast(e.message, 'error');
      throw e;
    }
  };
  const states = seg(AV_STATES.map(s => [s, s === 'needs' ? 'Needs you' : asstCap(s)]), st, s => {
    st = s;
    avSet(mark, { state: s });
    cap.textContent = ASST_CAPS[s];
  }, { cls: '.a-states' });
  drawPicks();
  const temper = seg([['calm', 'Calm'], ['lively', 'Lively'], ['precise', 'Precise']], look.temper, v => save({ temper: v }));
  const ring = seg(AV_RINGS.map(r => [r, asstCap(r)]), look.ring, v => save({ ring: v }));
  const eye = seg(AV_EYES.map(e => [e, asstCap(e)]), look.eye, v => save({ eye: v }));
  const finish = seg(AV_FINISHES.map(x => [x, asstCap(x)]), look.finish, v => save({ finish: v }));
  const nameIn = el('input.np-input', { type: 'text', maxlength: '40', value: look.name, 'aria-label': 'Name' });
  // The name is set when it's committed (Enter, or leaving the field), and
  // the field let go so the page can show it.
  nameIn.addEventListener('change', () => { if (nameIn.value.trim()) { nameIn.blur(); save({ name: nameIn.value.trim() }).catch(() => { nameIn.value = look.name }) } });
  return el('div.a-look', null,
    el('div.a-stage', null, mark, el('b.a-stage-name', null, look.name), cap, states),
    el('div.a-opts', null,
      el('div.a-opt', null, el('h3.a-label', null, 'Shape'), shapes),
      el('div.a-opt', null, el('h3.a-label', null, 'Colour'), colors),
      el('div.a-opt', null, el('h3.a-label', null, 'Ring'), ring),
      el('div.a-opt', null, el('h3.a-label', null, 'Eye'), eye),
      el('div.a-opt', null, el('h3.a-label', null, 'Finish'), finish),
      el('div.a-opt', null, el('h3.a-label', null, 'Motion'), temper),
      el('div.a-opt', null, el('h3.a-label', null, 'Name'), nameIn),
      el('div.a-opt', null, el('h3.a-label', null, 'Agents'), agentsEditor(state.asst.p)),
      asstPersonality(name, a)));
}

// asstPersonality is the assistant's personality file, every saved version
// kept, so a change can always be undone.
function asstPersonality(name, a) {
  const versions = a.personality || [];
  const text = el('textarea.np-input.a-persona', { rows: '6', 'aria-label': 'Personality', placeholder: 'Warm but brief. Lead with what needs me; skip the pleasantries.' });
  text.value = versions.length ? versions[versions.length - 1].text : '';
  // Only an admin changes it; everyone else reads it.
  const admin = !!state.meta?.admin;
  text.readOnly = !admin;
  const save = el('button.btn.sm.primary', { type: 'button', disabled: true }, 'Save');
  text.addEventListener('input', () => { save.disabled = false; state.dirty = true });
  save.addEventListener('click', async () => {
    save.disabled = true;
    try {
      await api('PUT', asstAPI(name) + '/personality', { text: text.value });
      state.dirty = false;
      toast('Saved: it takes effect when the agent next starts');
      refresh();
    } catch (e) { save.disabled = false; toast(e.message, 'error') }
  });
  const restore = async i => {
    if (!confirm('Go back to this version? The current one stays in the history' + (save.disabled ? '.' : ', but your unsaved changes here are lost.'))) return;
    try {
      await api('POST', asstAPI(name) + '/personality/restore', { version: i });
      state.dirty = false;
      toast('Restored');
      refresh();
    } catch (e) { toast(e.message, 'error') }
  };
  return el('div.a-opt', null,
    el('div.a-file-head', null, el('h3.a-label', null, 'Personality'), el('span.mono.a-file', null, 'personality.md')),
    text,
    el('div.a-file-foot', null,
      versions.length > 1 ? el('details.a-versions', null, el('summary', null, plural(versions.length, 'version')),
        versions.map((v, i) => i === versions.length - 1 ? null : el('div.a-version', null,
          el('span', null, new Date(v.at).toLocaleString(), ' · ', (v.by || '').split('@')[0]),
          admin ? el('button.btn.ghost.sm', { type: 'button', onclick: () => restore(i) }, 'Restore') : null)).reverse()) : el('span.np-hint', null, 'Every saved version is kept.'),
      admin ? save : null));
}

// --- browser ------------------------------------------------------------------

// asstWatch shows the assistant's browser as it works, with a cursor where
// it last clicked or typed. Watching never pauses it; Take over does. On
// its own page by default; a panel passes take, close and own (which keeps
// the way to stop it).
function asstWatch(name, look, opts = {}) {
  const canvas = el('canvas.a-screen.a-watch-screen', { width: '1280', height: '900', 'aria-label': look.name + '’s browser, live' });
  const arrow = svg(['M5 2.5v16.2l4.3-4.1 2.7 6.2 2.6-1.1-2.7-6.1h5.9z'], 'a-cursor-arrow');
  const cursor = el('div.a-cursor', { 'aria-hidden': 'true' }, arrow);
  const empty = el('p.a-watch-empty', null, look.name + '’s browser opens when it needs a website.');
  const wrap = el('div.a-screen-wrap.a-watch-wrap.closed', null, canvas, cursor, empty);
  const dot = el('span.dot');
  const where = el('span.a-watch-site', null, 'Connecting…');
  const ctx = canvas.getContext('2d');
  let size = { w: 1280, h: 900 }, site = '', cur = null, hide = 0, rest = 0, ws = null, retry = 0, closed = false;
  const say = (cls, text, mono) => {
    dot.className = 'dot' + (cls ? ' ' + cls : '');
    where.replaceChildren(text);
    where.classList.toggle('mono', !!mono);
  };
  const shown = taken => {
    wrap.classList.toggle('taken', taken);
    if (taken) say('warn', 'Taken over');
    else say('ok', site || 'Blank page', !!site);
  };
  // The cursor glides to each click, dips as it presses, then rests faint
  // where it was until the page moves on.
  const draw = () => {
    clearTimeout(hide);
    clearTimeout(rest);
    if (!cur) { cursor.classList.remove('on'); return }
    const fresh = !cursor.classList.contains('on');
    cursor.classList.toggle('glide', !fresh);
    cursor.style.left = cur.left + '%';
    cursor.style.top = cur.top + '%';
    cursor.classList.add('on');
    cursor.classList.remove('rest');
    rest = setTimeout(() => cursor.classList.add('rest'), 3000);
    if (cur.hideAt !== null) hide = setTimeout(() => { cur = null; cursor.classList.remove('on') }, Math.max(0, cur.hideAt - performance.now()));
  };
  const press = () => {
    arrow.classList.remove('press');
    void arrow.getBoundingClientRect(); // a new press restarts the dip
    arrow.classList.add('press');
  };
  const connect = () => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + asstAPI(name) + '/browser/watch');
    ws.binaryType = 'blob';
    ws.onopen = () => { retry = 0 };
    ws.onmessage = async e => {
      if (typeof e.data !== 'string') {
        const img = await createImageBitmap(e.data);
        if (canvas.width !== img.width || canvas.height !== img.height) { canvas.width = img.width; canvas.height = img.height }
        ctx.drawImage(img, 0, 0);
        img.close();
        wrap.classList.remove('closed');
        return;
      }
      const m = JSON.parse(e.data);
      if (m.t === 'size') size = { w: m.w, h: m.h };
      if (m.t === 'state' && !m.running) {
        wrap.classList.add('closed');
        cur = null;
        draw();
        say('', 'Closed');
      }
      if (m.t === 'state' && m.running) { site = m.site || ''; shown(!!m.taken) }
      if (m.t !== 'cue') return;
      if (m.act === 'page') { site = m.site || ''; shown(wrap.classList.contains('taken')) }
      if (m.act === 'taken' || m.act === 'free') shown(m.act === 'taken');
      cur = agentCursor(cur, m, size, performance.now());
      draw();
      if (m.act === 'click' || m.act === 'type') press();
    };
    // A dropped connection (a phone asleep, a network change) comes back.
    ws.onclose = () => {
      if (closed) return;
      say('bad', 'Reconnecting…');
      setTimeout(() => { if (!closed) connect() }, Math.min(1000 * 2 ** retry++, 15000));
    };
  };
  connect();
  (opts.own || asstOwn)(() => { closed = true; clearTimeout(hide); clearTimeout(rest); try { ws.close() } catch { } });
  return el('div.a-browser.a-watch', null,
    el('div.a-live-bar', null, el('span.a-live-status', null, dot, where), el('span.grow'),
      opts.take ? el('button.btn.sm', { type: 'button', onclick: opts.take }, svg(ICON.hand), 'Take over')
        : el('a.btn.sm', { href: asstPath(name, 'takeover'), 'data-link': true }, svg(ICON.hand), 'Take over'),
      opts.close ? el('button.icon-btn.bare.a-panel-x', { type: 'button', onclick: opts.close, 'aria-label': 'Close', title: 'Close' }, svg(ICON.x)) : null),
    wrap);
}

// asstTakeover shows the assistant's browser live and hands it to you: the
// assistant's browser tools pause until you're done. On its own page by
// default; a panel passes done (once it's handed back), reconnect and own,
// and has the page laid out to fit it.
function asstTakeover(name, look, opts = {}) {
  const touch = matchMedia('(pointer: coarse)').matches;
  // On a phone the canvas takes no focus, or a tap would take it from the
  // field below and close the keyboard.
  const canvas = el('canvas.a-screen', { tabindex: touch ? null : '0', width: '1280', height: '900', 'aria-label': look.name + '\u2019s browser. Escape leaves the page.' });
  // A phone's keyboard comes up for a field, not a canvas: this one passes
  // what you type to the page.
  const keys = el('input.a-keys', { type: 'text', autocomplete: 'off', autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false', 'aria-label': 'Type into the page' });
  const status = el('span.a-live-status', null, el('span.spinner'), 'Starting the browser\u2026');
  const url = el('input.np-input.a-url', { type: 'url', placeholder: 'https://', 'aria-label': 'Address' });
  const wrap = el('div.a-screen-wrap', null, canvas, keys);
  const ctx = canvas.getContext('2d');
  let size = { w: 1280, h: 900 }, open = false;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(proto + '//' + location.host + asstAPI(name) + '/browser/live');
  ws.binaryType = 'blob';
  const send = m => { if (ws.readyState === 1) ws.send(JSON.stringify(m)) };
  const back = opts.done || (() => navigate(asstPath(name, 'browser')));
  const done = () => { send({ t: 'done' }); back() };
  // A phone gets the page laid out for its own screen, and again when it
  // turns; the keyboard coming and going leaves it be. A panel gets it
  // laid out for the panel.
  let fitted = '';
  const fit = () => {
    const w = Math.round(wrap.clientWidth);
    const h = Math.max(240, Math.round(opts.done ? wrap.clientHeight : Math.min(window.innerHeight - wrap.getBoundingClientRect().top - 16, w * 2)));
    // A panel on a desktop follows its height too: no keyboard comes and goes.
    const key = touch ? String(w) : w + 'x' + h;
    if (!(touch || opts.done) || !w || key === fitted) return;
    fitted = key;
    send({ t: 'fit', w, h });
  };
  // A browser that shows nothing is stuck: restarting it starts another.
  const restart = async () => {
    status.replaceChildren(el('span.spinner'), 'Restarting the browser\u2026');
    stop();
    try { await api('POST', asstAPI(name) + '/browser/restart') } catch (e) { toast(e.message, 'error') }
    (opts.reconnect || moveTo)();
  };
  let stuck;
  ws.onopen = () => {
    requestAnimationFrame(fit);
    stuck = setTimeout(() => {
      if (!open && ws.readyState === 1) status.replaceChildren(el('span.dot.bad'), 'The browser isn\u2019t showing anything.', ' ',
        el('button.btn.ghost.sm', { type: 'button', onclick: restart }, 'Restart browser'));
    }, 15000);
  };
  window.addEventListener('resize', fit);
  let live = true;
  const stop = () => { live = false; clearTimeout(stuck); window.removeEventListener('resize', fit); try { ws.close() } catch { } };
  (opts.own || asstOwn)(stop);
  ws.onmessage = async e => {
    if (typeof e.data !== 'string') {
      const img = await createImageBitmap(e.data);
      if (canvas.width !== img.width || canvas.height !== img.height) { canvas.width = img.width; canvas.height = img.height }
      ctx.drawImage(img, 0, 0);
      img.close();
      // A panel says less: the chat beside it says the rest.
      if (!open) { open = true; status.replaceChildren(el('span.dot.ok'), opts.done ? 'You have the browser' : 'You have the browser. ' + look.name + ' waits until you\u2019re done.') }
      return;
    }
    const m = JSON.parse(e.data);
    if (m.t === 'size') size = { w: m.w, h: m.h };
    if (m.t === 'error') toast(m.error, 'error');
    if (m.t === 'state' && m.taken === false && m.why === 'idle') { toast('Handed back after a quiet while'); back() }
    if (m.t === 'state' && m.taken === false && m.why === 'elsewhere') gone = 'Opened on another screen.';
  };
  // A dropped connection (a phone asleep, a network change) leaves the ask
  // waiting: you can pick up where you were.
  let gone = '';
  ws.onclose = () => {
    if (!live) return;
    status.replaceChildren(el('span.dot.bad'), gone || 'Disconnected.', ' ',
      el('button.btn.ghost.sm', { type: 'button', onclick: () => (opts.reconnect || moveTo)() }, 'Reconnect'));
  };
  // Where on the page a pointer is, in the page's own pixels.
  const at = e => {
    const r = canvas.getBoundingClientRect();
    return { x: (e.clientX - r.left) * size.w / r.width, y: (e.clientY - r.top) * size.h / r.height };
  };
  // The browser runs on Linux, where a Mac's Command shortcuts are Control's.
  const mac = /Mac|iPhone|iPad/.test(navigator.platform);
  const mods = e => (e.altKey ? 1 : 0) | (e.ctrlKey || mac && e.metaKey ? 2 : 0) | (e.metaKey && !mac ? 4 : 0) | (e.shiftKey ? 8 : 0);
  const buttons = ['left', 'middle', 'right'];
  // Pointer events count no clicks: a press soon after one near it is a
  // double (or triple) click.
  let last = { t: 0, x: 0, y: 0, n: 0 };
  const clicks = e => {
    const near = Math.abs(e.clientX - last.x) < 5 && Math.abs(e.clientY - last.y) < 5;
    last = { t: e.timeStamp, x: e.clientX, y: e.clientY, n: near && e.timeStamp - last.t < 400 ? last.n % 3 + 1 : 1 };
    return last.n;
  };
  // A finger taps to click and drags to scroll, as on a phone's own pages;
  // a mouse presses, drags and hovers.
  let finger = null;
  canvas.addEventListener('pointerdown', e => {
    canvas.setPointerCapture(e.pointerId);
    if (e.pointerType === 'touch') {
      // The keyboard's field keeps the focus: no mouse events follow to
      // move it away.
      e.preventDefault();
      keys.focus({ preventScroll: true });
      finger = { x: e.clientX, y: e.clientY, at: at(e), scrolling: false };
      return;
    }
    canvas.focus();
    send({ t: 'mouse', type: 'mousePressed', ...at(e), button: buttons[e.button] || 'left', click_count: clicks(e), modifiers: mods(e) });
  });
  canvas.addEventListener('pointerup', e => {
    if (e.pointerType === 'touch') {
      if (finger && !finger.scrolling) {
        const n = clicks(e);
        send({ t: 'mouse', type: 'mousePressed', ...finger.at, button: 'left', click_count: n });
        send({ t: 'mouse', type: 'mouseReleased', ...finger.at, button: 'left', click_count: n });
      }
      finger = null;
      return;
    }
    send({ t: 'mouse', type: 'mouseReleased', ...at(e), button: buttons[e.button] || 'left', click_count: last.n, modifiers: mods(e) });
  });
  canvas.addEventListener('pointercancel', () => { finger = null });
  // Moves go once a frame, so a drag or a hover follows you as you move.
  let moved = null;
  canvas.addEventListener('pointermove', e => {
    if (e.pointerType === 'touch') {
      if (!finger) return;
      const dx = e.clientX - finger.x, dy = e.clientY - finger.y;
      if (!finger.scrolling && Math.hypot(dx, dy) < 8) return;
      finger.scrolling = true;
      finger.x = e.clientX;
      finger.y = e.clientY;
      const r = canvas.getBoundingClientRect();
      send({ t: 'mouse', type: 'mouseWheel', ...finger.at, dx: -dx * size.w / r.width, dy: -dy * size.h / r.height });
      return;
    }
    const first = !moved;
    moved = e;
    if (first) requestAnimationFrame(() => { send({ t: 'mouse', type: 'mouseMoved', ...at(moved), modifiers: mods(moved) }); moved = null });
  });
  canvas.addEventListener('wheel', e => {
    e.preventDefault();
    const scale = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? size.h : 1;
    send({ t: 'mouse', type: 'mouseWheel', ...at(e), dx: e.deltaX * scale, dy: e.deltaY * scale });
  }, { passive: false });
  canvas.addEventListener('contextmenu', e => e.preventDefault());
  // iOS opens the keyboard only for a focus in a tap's own click.
  canvas.addEventListener('click', () => { if (touch) keys.focus({ preventScroll: true }) });
  const keyDown = e => {
    if (e.key === 'Escape') { e.target.blur(); return } // the way out, for the keyboard
    // Paste comes as text, into the hidden field: some browsers (Safari)
    // only paste into something editable, which the canvas isn't.
    if ((e.metaKey || e.ctrlKey) && !e.getModifierState('AltGraph') && e.key.toLowerCase() === 'v') { keys.focus({ preventScroll: true }); return }
    e.preventDefault();
    const plain = !e.metaKey && (!e.ctrlKey || e.getModifierState('AltGraph'));
    const text = e.key.length === 1 && plain ? e.key : e.key === 'Enter' ? '\r' : '';
    send({ t: 'key', type: text ? 'keyDown' : 'rawKeyDown', key: e.key, code: e.code, text, key_code: e.keyCode, modifiers: mods(e) });
  };
  const keyUp = e => { if (e.key !== 'Escape') { e.preventDefault(); send({ t: 'key', type: 'keyUp', key: e.key, code: e.code, key_code: e.keyCode, modifiers: mods(e) }) } };
  const paste = e => {
    const s = e.clipboardData?.getData('text');
    if (s) { e.preventDefault(); send({ t: 'text', s }) }
    if (!touch && e.target === keys) canvas.focus({ preventScroll: true });
  };
  // A phone has no paste key, and no field to hold a finger on: a button
  // reads the clipboard instead (the phone asks you first).
  const pasteBtn = touch && navigator.clipboard?.readText ? el('button.btn.ghost.sm.a-paste', { type: 'button', onclick: async () => {
    try {
      const s = await navigator.clipboard.readText();
      if (s) send({ t: 'text', s });
      else toast('The clipboard is empty');
    } catch { toast('Couldn\u2019t read the clipboard', 'error') }
    keys.focus({ preventScroll: true });
  } }, 'Paste') : null;
  canvas.addEventListener('keydown', keyDown);
  canvas.addEventListener('keyup', keyUp);
  canvas.addEventListener('paste', paste);
  // A phone's keyboard: some send key events, many only text, some compose
  // whole words. The field always holds one space, so whatever the keyboard
  // does shows as a change to it: text added, or the space deleted.
  const key = k => { send({ t: 'key', type: k.text ? 'keyDown' : 'rawKeyDown', ...k }); send({ t: 'key', type: 'keyUp', key: k.key, code: k.code, key_code: k.key_code }) };
  const BACK = { key: 'Backspace', code: 'Backspace', key_code: 8 };
  const keep = () => { keys.value = ' '; keys.setSelectionRange(1, 1) };
  keys.addEventListener('focus', keep);
  const changed = () => {
    const v = keys.value;
    if (v === '') key(BACK);
    else if (v !== ' ') send({ t: 'text', s: v.startsWith(' ') ? v.slice(1) : v });
    keep();
  };
  keys.addEventListener('input', e => { if (!e.isComposing) changed() });
  keys.addEventListener('compositionend', changed);
  keys.addEventListener('keydown', e => {
    if (e.isComposing || e.keyCode === 229) return;
    const special = { Enter: { key: 'Enter', code: 'Enter', text: '\r', key_code: 13 }, Backspace: BACK, Tab: { key: 'Tab', code: 'Tab', key_code: 9 },
      ArrowLeft: { key: 'ArrowLeft', code: 'ArrowLeft', key_code: 37 }, ArrowRight: { key: 'ArrowRight', code: 'ArrowRight', key_code: 39 },
      ArrowUp: { key: 'ArrowUp', code: 'ArrowUp', key_code: 38 }, ArrowDown: { key: 'ArrowDown', code: 'ArrowDown', key_code: 40 } }[e.key];
    if (!special) return;
    e.preventDefault();
    key(special);
  });
  keys.addEventListener('paste', paste);
  const go = el('form.a-urlbar', { onsubmit: e => { e.preventDefault(); if (url.value) send({ t: 'go', url: url.value }) } },
    url, el('button.btn.sm', { type: 'submit' }, 'Go'));
  return el('div.a-browser', null,
    el('div.a-live-bar', null, avatar(look, { state: 'needs', mini: true }), status, el('span.grow'),
      pasteBtn, el('button.btn.primary.sm', { type: 'button', onclick: done }, svg(ICON.check), 'Done')),
    go,
    wrap,
    // A phone types through a bar where its message box was.
    touch ? el('button.a-typebar', { type: 'button', onpointerdown: e => e.preventDefault(), onclick: () => keys.focus({ preventScroll: true }) },
      svg(ICON.keyboard), el('span.a-typebar-label', null, 'Type into the page'), el('span.a-typebar-lock', null, svg(ICON.lock), look.name + ' can\u2019t see this'))
      : el('p.np-hint', null, 'Click the page to type. What you type goes straight to the browser; ' + look.name + ' never sees it.'));
}

// asstPanel is an assistant's browser beside its chat, so taking it over
// doesn't leave the conversation: you take it over from the dock's card,
// then watch it carry on until you close it. On a narrow screen it's a
// sheet over the chat, which closes once you've handed the browser back.
// Back closes it too. changed runs when it opens, closes or changes hands.
function asstPanel(page, name, look, changed) {
  const body = el('div.a-panel-body');
  const root = el('section.a-panel', { 'aria-label': look.name + '\u2019s browser' }, body);
  let mode = '', stop = null;
  const own = close => { stop = close };
  const narrow = () => !matchMedia('(min-width: 1180px)').matches;
  const show = m => {
    stop?.();
    stop = null;
    mode = m;
    root.classList.toggle('taking', m === 'takeover');
    body.replaceChildren(m === 'takeover'
      ? asstTakeover(name, look, { own, done: () => narrow() ? close() : show('watch'), reconnect: () => show('takeover') })
      : asstWatch(name, look, { own, take: () => show('takeover'), close: () => close() }));
    if (!root.isConnected) {
      page.append(root);
      page.classList.add('with-panel');
      history.pushState({ ...history.state, asstPanel: true }, '', location.href);
      asstPanelOpen = p;
    }
    changed();
  };
  // unwind takes back the step opening it added, when it isn't Back closing it.
  const close = (unwind = true) => {
    if (!mode) return;
    stop?.();
    stop = null;
    mode = '';
    root.remove();
    page.classList.remove('with-panel');
    if (asstPanelOpen === p) asstPanelOpen = null;
    if (unwind && history.state?.asstPanel) { asstPanelPopping = true; history.back() }
    changed();
  };
  const p = { take: () => show('takeover'), close, taking: () => mode === 'takeover' };
  return p;
}

// Back closes an open panel rather than leaving the chat under it. This
// runs before the router's listener, which it keeps from seeing these steps.
let asstPanelOpen = null, asstPanelPopping = false;
addEventListener('popstate', e => {
  if (asstPanelPopping) { asstPanelPopping = false; e.stopImmediatePropagation(); return }
  // Still on the panel's step: Back closed something over it, an image.
  if (!asstPanelOpen || history.state?.asstPanel) return;
  e.stopImmediatePropagation();
  asstPanelOpen.close(false);
});

// --- making one ------------------------------------------------------------------

const asstSlug = s => {
  const v = String(s).toLowerCase().normalize('NFKD').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);
  if (v || !String(s).trim()) return v;
  // A name in another script has no letters an address can take: it gets
  // one of its own, the same each time for the same name.
  let h = 0;
  for (const c of String(s).trim()) h = (h * 31 + c.codePointAt(0)) >>> 0;
  return 'assistant-' + h.toString(36);
};

// newAssistantSheet makes an assistant: who it is first (its name and its
// mark), then what it runs on and may reach. Skybreaker restarts to add
// it, once no agent is working.
async function newAssistantSheet() {
  let caps;
  try { caps = await api('GET', '/api/capabilities') } catch (e) { toast(e.message, 'error'); return }
  const look = { name: '', shape: 'dot', color: 'clay', temper: 'calm', ring: 'fine', eye: 'none', finish: 'paper' };
  const mark = avatar({ ...look }, { cls: 'na-mark' });
  const name = el('input.np-input.na-name', { type: 'text', placeholder: 'Pip', maxlength: '40', required: true, autocomplete: 'off', 'aria-label': 'Name' });
  const address = el('span.np-hint');
  let shapes, colors;
  const draw = () => {
    avSet(mark, { look: { ...look } });
    const fresh = [asstShapes(look, sh => { look.shape = sh; draw() }), asstColors(look, k => { look.color = k; draw() })];
    if (shapes) { shapes.replaceWith(fresh[0]); colors.replaceWith(fresh[1]) }
    [shapes, colors] = fresh;
    address.textContent = asstSlug(name.value) ? 'Its address: /a/' + asstSlug(name.value) : 'Its name, as you\u2019ll call it.';
  };
  name.addEventListener('input', draw);
  draw();
  const agents = (state.meta?.harnesses || ['claude', 'codex']).filter(h => h !== 'shell');
  const agentBoxes = agents.map(h => npBox(h, h === 'claude', harnessLabel(h), HARNESS_INFO[h] || ''));
  const wanted = ['claude-login', 'codex-login'];
  const capBoxes = caps.map(c => npBox(c.name, wanted.includes(c.name), c.name, c.reaches.join(', '), true));
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const create = el('button.btn.primary', { type: 'submit' }, 'Create assistant');
  const form = el('form.np-form.na-form', {
    onsubmit: async e => {
      e.preventDefault();
      look.name = name.value.trim();
      const slug = asstSlug(look.name);
      error.hidden = true;
      if (!slug) { error.textContent = 'Give it a name.'; error.hidden = false; return }
      create.disabled = true;
      try {
        const out = await api('POST', '/api/projects', { name: slug, kind: 'assistant', harnesses: npChecked(agentBoxes), capabilities: npChecked(capBoxes) });
        made(out, slug);
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
        create.disabled = false;
      }
    },
  },
    el('div.na-who', null, mark,
      el('div.na-who-fields', null,
        el('label.np-field', null, el('span.np-label', null, 'Name'), name, address),
        el('div.np-field', null, el('span.np-label', null, 'Shape'), shapes),
        el('div.np-field', null, el('span.np-label', null, 'Colour'), colors))),
    el('fieldset.np-group.np-agents', null, el('legend', null, 'Agents'), agentBoxes),
    el('fieldset.np-group.np-access', null, el('legend', null, 'Access'), capBoxes.length ? capBoxes : el('p.np-hint', null, 'No bundles yet: make one in Settings.'),
      el('p.np-hint', null, 'What it may reach. Anything else is denied; add hosts and its browser’s sites later in Settings.')),
    error, create);
  const show = (...body) => npSheet('New assistant', ...body);
  show(form);
  name.focus();

  // made waits for Skybreaker to come back with the assistant, gives it
  // the look you chose, and opens it.
  function made(out, slug) {
    const status = el('p.np-status', null, el('span.spinner'), out.restart === 'now'
      ? 'Skybreaker is restarting to add it. This takes a few seconds.'
      : 'It’s added once no agent is working, so chats in progress carry on.');
    const open = el('button.btn.primary', { type: 'button', disabled: true, onclick: () => { closeSheet(); navigate(asstPath(slug)) } }, 'Meet ' + look.name);
    const big = avatar({ ...look }, { cls: 'na-mark' });
    show(el('div.np-done.na-done', null, big, el('p', null, el('b', null, look.name), ' is saved.'), status, open));
    const started = Date.now();
    // It keeps trying after the sheet closes: the look you chose is saved
    // whenever it comes back.
    const poll = async () => {
      try {
        await api('PUT', asstAPI(slug) + '/look', look);
        status.replaceChildren(svg(ICON.check), 'Ready.');
        open.disabled = false;
        refreshSidebar();
        return;
      } catch { /* restarting, or not added yet */ }
      if (Date.now() - started < 15 * 60 * 1000) setTimeout(poll, 1500);
    };
    setTimeout(poll, 1500);
  }
}

// asstBrief is today's brief: the first line of the newest routine that
// finished well today, in the assistant's own words.
function asstBrief(runs) {
  const today = new Date().toDateString();
  const run = runs.find(r => r.status === 'ok' && r.summary && r.finished && new Date(r.finished).toDateString() === today);
  if (!run) return null;
  let text = oneLine(run.summary).replace(/^[#>*\-\s]+/, '');
  if (text.length > 180) text = text.slice(0, 177).replace(/\s+\S*$/, '') + '\u2026';
  return text ? { text, run } : null;
}

// asstBrowsing says where its browser is, while it's open and nothing
// waits on you there: the Needs you card covers that.
function asstBrowsing(name, b, waiting) {
  if (!b || waiting) return null;
  const where = b.taken ? 'You have its browser.' : b.site ? 'Its browser is on ' + b.site + '.' : 'Its browser is open.';
  return el('a.a-browsing', { href: asstPath(name, 'browser'), 'data-link': true }, where);
}
