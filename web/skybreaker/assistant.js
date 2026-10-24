'use strict';

// An assistant's pages: Today (its brief), Memory, Routines, Look, and its
// browser when you take it over. What the assistant wrote (memories,
// updates, page frames) only goes in through textContent or a canvas.

// asstLookOf is a project's assistant look, or a plain one until it has its own.
const asstLookOf = p => p?.look || { name: p?.name || '', shape: 'dot', color: 'clay', temper: 'calm' };
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
    requestAnimationFrame(() => group.querySelector('[aria-checked="true"]')?.focus());
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

async function renderAssistant(seq, name, tab) {
  const enc = encodeURIComponent(name);
  const [d, a] = await Promise.all([api('GET', '/api/projects/' + enc), api('GET', asstAPI(name))]);
  if (seq !== state.seq) return;
  const p = d.project;
  if (p.kind !== 'assistant') { navigate('/p/' + enc); return }
  state.asst = { p, a };
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
  const tabs = [['today', 'Today', 0], ['chats', 'Chats', 0], ['memory', 'Memory', memoryAsks], ['routines', 'Routines', routineAsks], ['look', 'Look', 0]];
  const tabBar = tab === 'browser' ? null : el('nav.p-tabs.a-tabs', { 'aria-label': look.name }, tabs.map(([k, label, n]) =>
    el('a.p-tab' + (k === tab ? '.on' : ''), { href: asstPath(name, k), 'data-link': true, 'aria-current': k === tab ? 'page' : null },
      el('span', null, label), n ? el('span.n.warn', null, String(n)) : null)));
  let body;
  switch (tab) {
    case 'chats': body = asstChats(p, d); break;
    case 'memory': body = asstMemory(name, a); break;
    case 'routines': body = await asstRoutines(seq, name, a); break;
    case 'look': body = asstLook(name, a); break;
    case 'browser': body = asstBrowser(name, look); break;
    default: body = await asstToday(seq, name, p, a, d);
  }
  if (seq !== state.seq) return;
  const page = el('div.page.assistant.a-tab-' + tab, null, tab === 'browser' ? null : head, tabBar, body);
  asstAccent(page, look.color);
  // On a phone, which has no sidebar, the + starts a chat with it.
  const fab = tab === 'browser' ? null : el('button.fab', { type: 'button', onclick: () => pickHarness(p), 'aria-label': 'Message ' + look.name }, svg(ICON.plus));
  if (fab) asstAccent(fab, look.color);
  $('#app').replaceChildren(page, fab);
}

// asstHomeRow is an assistant on the inbox, with what it asks of you.
function asstHomeRow(p) {
  const look = asstLookOf(p);
  return el('a.srow.a-home-row', { href: asstPath(p.name), 'data-link': true },
    avatar(look, { state: 'needs', cls: 'a-home-mark' }),
    el('span.a-row-text', null, el('b', null, look.name), el('span.a-row-sub', null, plural(p.asks, 'thing') + ' waiting on you')),
    svg(ICON.chevron, 'chev'));
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
  const lately = finished.length + t.updates.length;
  const brief = asstBrief(finished);
  const site = asstBrowsing(name, t.browser, takeovers.length > 0);
  const pill = needs ? el('button.a-needs-pill', { type: 'button', onclick: () => asstNeedsSheet(name, p) },
    el('i'), takeovers.length ? 'Sign-in needed' : needs + ' for you') : null;
  const hero = el('div.a-hero', null,
    avatar(t.look, { ...asstState(needs, d.sessions), cls: 'a-hero-mark' }),
    el('div.a-hero-text', null, el('h2.a-greeting', null, asstGreeting()),
      brief ? el('p.a-brief', null, brief.text, ' ', el('a.a-brief-from', { href: '/r/' + encodeURIComponent(brief.run.id), 'data-link': true }, brief.run.job + ', ' + ago(brief.run.finished))) : null,
      el('p', null, asstSummary(lately), site ? [' ', site] : null, pill)));
  return el('div.a-today', null, hero,
    lately ? el('section.a-sec', null, el('h2.a-label', null, 'What ' + t.look.name + ' did'),
      el('div.a-done', null,
        ...finished.map(r => el('a.a-row', { href: '/r/' + encodeURIComponent(r.id), 'data-link': true },
          el('span.a-ic', null, svg(r.status === 'ok' ? ICON.check : ICON.clock)),
          el('span.a-row-text', null, el('b', null, r.job), el('span.a-row-sub', null, oneLine(r.summary) || runWord(r))),
          el('span.a-row-end', null, ago(r.finished || r.started)))),
        ...t.updates.map(u => el('div.a-row', null,
          el('span.a-ic', null, svg(ICON.note)),
          el('span.a-row-text', null, el('b', null, u.text)),
          el('span.a-row-end', null, ago(u.at)))))) : null);
}

// asstChats is every chat with the assistant, open ones first.
function asstChats(p, d) {
  const { shown, ended } = chatGroups(d.sessions, new Set(d.pending.map(a => a.session)));
  return el('div.a-chats', null, ...chatList(p, shown, ended));
}

const runWord = r => r.status === 'ok' ? 'Finished' : r.status === 'running' ? 'Running' : r.status === 'failed' ? 'Failed' + (r.error ? ': ' + r.error : '') : r.status;

function asstTakeoverCard(name, x) {
  return el('div.a-card.a-need', null,
    el('div.a-need-text', null, el('div.a-kick', null, 'The browser needs you'), el('b', null, x.title),
      el('p', null, 'Take over to sign in or enter a code. The assistant can’t see what you type.')),
    el('div.a-acts', null,
      el('a.btn.primary.sm', { href: asstPath(name, 'browser'), 'data-link': true }, svg(ICON.hand), 'Take over'),
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
    el('div.a-acts', null,
      el('button.btn.primary.sm', { type: 'button', onclick: e => asstDo(asstProposal(name, x, 'approve'), 'Saved: it runs once Skybreaker restarts', {}, e) }, svg(ICON.check), 'Approve'),
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
  const nameIn = el('input.np-input', { type: 'text', maxlength: '40', value: look.name, 'aria-label': 'Name' });
  nameIn.addEventListener('change', () => { if (nameIn.value.trim()) save({ name: nameIn.value.trim() }).catch(() => { nameIn.value = look.name }) });
  return el('div.a-look', null,
    el('div.a-stage', null, mark, el('b.a-stage-name', null, look.name), cap, states),
    el('div.a-opts', null,
      el('div.a-opt', null, el('h3.a-label', null, 'Shape'), shapes),
      el('div.a-opt', null, el('h3.a-label', null, 'Colour'), colors),
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
  const save = el('button.btn.sm.primary', { type: 'button', disabled: true }, 'Save');
  text.addEventListener('input', () => { save.disabled = false; state.dirty = true });
  save.addEventListener('click', async () => {
    save.disabled = true;
    try {
      await api('PUT', asstAPI(name) + '/personality', { text: text.value });
      state.dirty = false;
      toast('Saved: new chats start with it');
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
          el('button.btn.ghost.sm', { type: 'button', onclick: () => restore(i) }, 'Restore'))).reverse()) : el('span.np-hint', null, 'Every saved version is kept.'),
      save));
}

// --- browser ------------------------------------------------------------------

// asstBrowser shows the assistant's browser live and hands it to you: the
// assistant's browser tools pause until you're done.
function asstBrowser(name, look) {
  const canvas = el('canvas.a-screen', { tabindex: '0', width: '1280', height: '900', 'aria-label': look.name + '\u2019s browser. Escape leaves the page.' });
  // A phone's keyboard comes up for a field, not a canvas: this one passes
  // what you type to the page.
  const keys = el('input.a-keys', { type: 'text', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-label': 'Type into the page' });
  const status = el('span.a-live-status', null, el('span.spinner'), 'Starting the browser\u2026');
  const url = el('input.np-input.a-url', { type: 'url', placeholder: 'https://', 'aria-label': 'Address' });
  const ctx = canvas.getContext('2d');
  let size = { w: 1280, h: 900 }, open = false;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(proto + '//' + location.host + asstAPI(name) + '/browser/live');
  ws.binaryType = 'blob';
  const send = m => { if (ws.readyState === 1) ws.send(JSON.stringify(m)) };
  const done = () => { send({ t: 'done' }); navigate(asstPath(name)) };
  state.asstLive = { close: () => { try { ws.close() } catch { } } };
  ws.onmessage = async e => {
    if (typeof e.data !== 'string') {
      const img = await createImageBitmap(e.data);
      if (canvas.width !== img.width || canvas.height !== img.height) { canvas.width = img.width; canvas.height = img.height }
      ctx.drawImage(img, 0, 0);
      img.close();
      if (!open) { open = true; status.replaceChildren(el('span.dot.ok'), 'You have the browser. ' + look.name + ' waits until you\u2019re done.') }
      return;
    }
    const m = JSON.parse(e.data);
    if (m.t === 'size') size = { w: m.w, h: m.h };
    if (m.t === 'error') toast(m.error, 'error');
    if (m.t === 'state' && m.taken === false && m.why === 'idle') { toast('Handed back after a quiet while'); navigate(asstPath(name)) }
  };
  ws.onclose = () => { if (state.asstLive) status.replaceChildren(el('span.dot.bad'), 'Disconnected.') };
  // Where on the page a pointer is, in the page's own pixels.
  const at = e => {
    const r = canvas.getBoundingClientRect();
    return { x: (e.clientX - r.left) * size.w / r.width, y: (e.clientY - r.top) * size.h / r.height };
  };
  const mods = e => (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
  const buttons = ['left', 'middle', 'right'];
  // Pointer events count no clicks: a press soon after one near it is a
  // double (or triple) click.
  let last = { t: 0, x: 0, y: 0, n: 0 };
  const clicks = e => {
    const near = Math.abs(e.clientX - last.x) < 5 && Math.abs(e.clientY - last.y) < 5;
    last = { t: e.timeStamp, x: e.clientX, y: e.clientY, n: near && e.timeStamp - last.t < 400 ? last.n % 3 + 1 : 1 };
    return last.n;
  };
  canvas.addEventListener('pointerdown', e => {
    canvas.setPointerCapture(e.pointerId);
    if (e.pointerType === 'touch') keys.focus({ preventScroll: true }); else canvas.focus();
    send({ t: 'mouse', type: 'mousePressed', ...at(e), button: buttons[e.button] || 'left', click_count: clicks(e), modifiers: mods(e) });
  });
  canvas.addEventListener('pointerup', e => send({ t: 'mouse', type: 'mouseReleased', ...at(e), button: buttons[e.button] || 'left', click_count: last.n, modifiers: mods(e) }));
  // Moves go once a frame, so a drag or a hover follows you as you move.
  let moved = null;
  canvas.addEventListener('pointermove', e => {
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
  const keyDown = e => {
    if (e.key === 'Escape') { e.target.blur(); return } // the way out, for the keyboard
    if ((e.metaKey || e.ctrlKey) && !e.getModifierState('AltGraph') && e.key.toLowerCase() === 'v') return; // paste comes as text
    e.preventDefault();
    const plain = !e.metaKey && (!e.ctrlKey || e.getModifierState('AltGraph'));
    const text = e.key.length === 1 && plain ? e.key : e.key === 'Enter' ? '\r' : '';
    send({ t: 'key', type: text ? 'keyDown' : 'rawKeyDown', key: e.key, code: e.code, text, key_code: e.keyCode, modifiers: mods(e) });
  };
  const keyUp = e => { if (e.key !== 'Escape') { e.preventDefault(); send({ t: 'key', type: 'keyUp', key: e.key, code: e.code, key_code: e.keyCode, modifiers: mods(e) }) } };
  const paste = e => { const s = e.clipboardData?.getData('text'); if (s) { e.preventDefault(); send({ t: 'text', s }) } };
  canvas.addEventListener('keydown', keyDown);
  canvas.addEventListener('keyup', keyUp);
  canvas.addEventListener('paste', paste);
  // A phone's keyboard sends composed text, and often no key events.
  keys.addEventListener('beforeinput', e => {
    e.preventDefault();
    if (e.inputType === 'insertText' || e.inputType === 'insertReplacementText') send({ t: 'text', s: e.data || '' });
    if (e.inputType === 'deleteContentBackward') send({ t: 'key', type: 'rawKeyDown', key: 'Backspace', code: 'Backspace', key_code: 8 }), send({ t: 'key', type: 'keyUp', key: 'Backspace', code: 'Backspace', key_code: 8 });
    if (e.inputType === 'insertLineBreak' || e.inputType === 'insertParagraph') send({ t: 'key', type: 'keyDown', key: 'Enter', code: 'Enter', text: '\r', key_code: 13 }), send({ t: 'key', type: 'keyUp', key: 'Enter', code: 'Enter', key_code: 13 });
  });
  keys.addEventListener('paste', paste);
  const go = el('form.a-urlbar', { onsubmit: e => { e.preventDefault(); if (url.value) send({ t: 'go', url: url.value }) } },
    url, el('button.btn.sm', { type: 'submit' }, 'Go'));
  return el('div.a-browser', null,
    el('div.a-live-bar', null, avatar(look, { state: 'needs', mini: true }), status, el('span.grow'),
      el('button.btn.primary.sm', { type: 'button', onclick: done }, svg(ICON.check), 'Done')),
    go,
    el('div.a-screen-wrap', null, canvas, keys),
    el('p.np-hint', null, 'Tap or click the page to type. What you type goes straight to the browser; the assistant never sees it.'));
}

// --- making one ------------------------------------------------------------------

// asstTile is an assistant on a phone's home page.
function asstTile(p) {
  const look = asstLookOf(p);
  return el('a.tile.a-tile', { href: asstPath(p.name), 'data-link': true },
    el('span.tile-name', null, avatar(look, { mini: true }), look.name),
    el('span.tile-sub', null, p.asks ? plural(p.asks, 'thing') + ' for you' : p.running ? 'running' : 'resting'));
}

const asstSlug = s => String(s).toLowerCase().normalize('NFKD').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);

// newAssistantSheet makes an assistant: who it is first (its name and its
// mark), then what it runs on and may reach. Skybreaker restarts to add
// it, once no agent is working.
async function newAssistantSheet() {
  let caps;
  try { caps = await api('GET', '/api/capabilities') } catch (e) { toast(e.message, 'error'); return }
  const look = { name: '', shape: 'dot', color: 'clay', temper: 'calm' };
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
      if (!slug) { error.textContent = 'Give it a name with a letter or digit in it.'; error.hidden = false; return }
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
