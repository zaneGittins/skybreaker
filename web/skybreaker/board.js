'use strict';

// The board: a project's bugs, features and chores in five columns, worked
// by you here and by its agents through MCP. Titles, notes and descriptions
// are often agent-written: text goes in through textContent, descriptions
// and notes through markdown(), which builds nodes and never parses HTML.

const COLUMNS = [['backlog', 'Backlog'], ['todo', 'Todo'], ['in_progress', 'In progress'], ['review', 'Review'], ['done', 'Done']];
const ITEM_TYPES = ['bug', 'feature', 'chore'];
const PRIORITIES = ['Low', 'Normal', 'High', 'Urgent'];

Object.assign(ICON, {
  note: ['M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z'],
  trash: ['M3 6h18', 'M8 6V4h8v2', 'M19 6l-1 14H6L5 6'],
  board: ['M3 4h5v16H3z', 'M10 4h5v10h-5z', 'M17 4h4v7h-4z'],
});

const itemPath = ref => '/api/items/' + encodeURIComponent(ref);

// --- the board ---------------------------------------------------------------

// boardView is a project's board tab.
function boardView(name, d) {
  const by = Object.fromEntries(COLUMNS.map(([k]) => [k, []]));
  for (const it of d.items) by[it.status]?.push(it);

  // On a phone one column shows at a time; the tabs pick it.
  let current = prefs.get('boardCol') || 'todo';
  const tabs = COLUMNS.map(([k, label]) => el('button.col-tab.st-' + k, {
    type: 'button', 'data-col': k, 'aria-pressed': 'false',
    onclick: () => { current = k; prefs.set('boardCol', k); show() },
  }, el('span.cdot'), el('span', null, label), el('span.n', null, String(by[k].length))));
  const cols = COLUMNS.map(([k, label]) => column(k, label, by[k], d.tint));
  const show = () => {
    tabs.forEach(t => { const on = t.dataset.col === current; t.classList.toggle('on', on); t.setAttribute('aria-pressed', String(on)) });
    cols.forEach(c => c.classList.toggle('on', c.dataset.col === current));
  };
  show();
  return el('div.board-view', null,
    el('div.col-tabs', { role: 'group', 'aria-label': 'Column' }, tabs),
    el('div.board-cols', null, cols),
    captureBox(name, () => current === 'todo' ? 'todo' : 'backlog'));
}

function column(status, label, items, tint) {
  const list = el('div.col-list', { 'data-status': status }, items.map(it => itemCard(it)));
  if (!items.length) list.append(el('div.col-empty', null, status === 'todo' ? 'Nothing ready. Move items here from Backlog.' : 'Empty'));
  dropTarget(list, status);
  return el('section.col.st-' + status, { 'data-col': status },
    el('div.col-head', null, el('span.cdot'), el('h2', null, label), el('span.n', null, String(items.length))),
    list);
}

function itemCard(it) {
  positions.set(it.ref, it.position);
  const meta = [el('span.tchip.t-' + it.type, null, it.type)];
  for (const l of it.labels || []) meta.push(el('span.label', null, '#' + l));
  if (it.note_count) meta.push(el('span.notes', null, svg(ICON.note), String(it.note_count)));
  return el('button.card' + (it.priority >= 2 ? '.prio-' + it.priority : ''), {
    type: 'button', draggable: 'true', 'data-ref': it.ref,
    onclick: () => openItem(it.ref),
    ondragstart: e => { e.dataTransfer.setData('text/plain', it.ref); e.dataTransfer.effectAllowed = 'move'; e.currentTarget.classList.add('dragging') },
    ondragend: e => e.currentTarget.classList.remove('dragging'),
  },
    el('div.card-top', null, el('span.ref', null, it.ref),
      it.priority >= 2 ? el('span.prio', null, PRIORITIES[it.priority]) : null,
      el('span.grow'), it.assignee ? el('span.who', null, it.assignee) : null),
    el('div.card-title', null, it.title),
    el('div.card-meta', null, meta));
}

// dropTarget lets cards be dragged into a column, between two others; the
// new position is the midpoint of the neighbours'.
function dropTarget(list, status) {
  const marker = el('div.drop-marker');
  const after = y => [...list.querySelectorAll('.card:not(.dragging)')].find(c => {
    const r = c.getBoundingClientRect();
    return y < r.top + r.height / 2;
  });
  list.addEventListener('dragover', e => {
    e.preventDefault();
    const next = after(e.clientY);
    next ? list.insertBefore(marker, next) : list.append(marker);
  });
  list.addEventListener('dragleave', e => { if (!list.contains(e.relatedTarget)) marker.remove() });
  list.addEventListener('drop', async e => {
    e.preventDefault();
    const ref = e.dataTransfer.getData('text/plain');
    const cards = [...list.children].filter(c => c.classList.contains('card') || c === marker).filter(c => c.dataset.ref !== ref);
    const i = cards.indexOf(marker);
    marker.remove();
    if (!ref) return;
    const pos = i < 0 ? undefined : dropPosition(posOf(cards[i - 1]), posOf(cards[i + 1]));
    try {
      await api('PATCH', itemPath(ref), pos === undefined ? { status } : { status, position: pos });
      refresh();
    } catch (err) { toast(err.message, 'error') }
  });
}

// Positions ride on the cards so a drop can work out its neighbours.
const positions = new Map();
const posOf = card => card ? positions.get(card.dataset.ref) : undefined;

// captureBox is the add box: one line in the quick syntax, such as
// "bug: crash on save #ui !".
function captureBox(project, status) {
  const input = el('input.capture-input', { type: 'text', placeholder: 'bug: crash on save #ui !', 'aria-label': 'New item', enterkeyhint: 'send' });
  let busyAdding = false;
  const add = async e => {
    e.preventDefault();
    const text = input.value.trim();
    if (!text || busyAdding) return;
    busyAdding = true;
    try {
      const it = await api('POST', `/api/projects/${encodeURIComponent(project)}/items`, { text, status: status() });
      input.value = '';
      toast(`Added ${it.ref}`);
      refresh();
    } catch (err) { toast(err.message, 'error') }
    busyAdding = false;
  };
  return el('form.capture', { onsubmit: add }, input,
    el('button.send-btn', { type: 'submit', 'aria-label': 'Add' }, svg(ICON.plus)));
}

// --- one item ----------------------------------------------------------------

async function openItem(ref) {
  let it;
  try { it = await api('GET', itemPath(ref)) } catch (e) { toast(e.message, 'error'); return }
  const body = el('div.item-sheet');
  sheet(it.ref, body);
  const patch = async p => {
    try { it = await api('PATCH', itemPath(it.ref), p); draw() } catch (e) { toast(e.message, 'error') }
  };
  let editing = false;

  const draw = () => {
    const title = el('textarea.item-title', { rows: '1', 'aria-label': 'Title' });
    title.value = it.title;
    title.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); title.blur() } });
    title.addEventListener('change', () => title.value.trim() && title.value !== it.title && patch({ title: title.value.replace(/\s+/g, ' ') }));
    // Safari has no field-sizing: grow the title to its text by hand.
    const fit = CSS.supports('field-sizing', 'content') ? null : () => { title.style.height = 'auto'; title.style.height = title.scrollHeight + title.offsetHeight - title.clientHeight + 'px' };
    if (fit) { title.addEventListener('input', fit); requestAnimationFrame(fit) }

    const seg = (values, current, label, onpick) => el('div.seg', { role: 'group', 'aria-label': label },
      values.map(([v, text, cls]) => el('button' + (cls || '') + (v === current ? '.on' : ''), { type: 'button', onclick: () => v !== current && onpick(v) }, text)));

    const labels = el('input.input.labels', { type: 'text', placeholder: 'labels, comma separated', 'aria-label': 'Labels' });
    labels.value = (it.labels || []).join(', ');
    labels.addEventListener('change', () => patch({ labels: labels.value.split(',').map(s => s.trim()).filter(Boolean) }));

    const harnesses = state.boardChat || Object.keys(state.meta?.chat || {});
    const work = it.status === 'done' || !harnesses.length ? null : el('div.work', null,
      el('span.muted', null, 'Start a chat on it:'),
      harnesses.map((h, i) => el('button.btn.sm' + (i ? '' : '.primary'), { type: 'button', onclick: () => workOn(it, h) }, svg(ICON.bolt), harnessLabel(h))));

    body.replaceChildren(
      title,
      el('div.item-meta', null, el('span.muted', null, `${it.project} · created ${ago(it.created_at)}`),
        it.assignee ? el('span.who', null, it.assignee) : null),
      seg(COLUMNS.map(([k, l]) => [k, l, '.st-' + k]), it.status, 'Status', v => patch({ status: v })),
      el('div.item-row', null,
        seg(ITEM_TYPES.map(t => [t, t, '.t-' + t]), it.type, 'Type', v => patch({ type: v })),
        seg(PRIORITIES.map((p, i) => [i, p]), it.priority, 'Priority', v => patch({ priority: v }))),
      labels,
      description(it, patch),
      work,
      el('section.activity', null, el('h3', null, 'Activity'),
        el('div.notes', null, (it.notes || []).map(n => noteRow(it, n))),
        noteBox(it)),
      el('button.btn.danger.sm.delete-item', { type: 'button', onclick: () => deleteItem(it) }, svg(ICON.trash), 'Delete item'));
  };

  const reload = async () => {
    try { it = await api('GET', itemPath(it.ref)); if (!editing) draw() } catch { }
  };
  state.itemSheet = { ref: it.ref, reload };
  draw();

  function description(it, patch) {
    const box = el('section.desc');
    const view = () => {
      editing = false;
      box.replaceChildren(
        it.body ? markdown(it.body) : el('p.muted', null, 'No description yet.'),
        el('button.btn.sm.ghost', { type: 'button', onclick: edit }, svg(ICON.pencil), it.body ? 'Edit description' : 'Add a description'));
    };
    const edit = () => {
      editing = true;
      const ta = el('textarea.textarea', { rows: '8', 'aria-label': 'Description' });
      ta.value = it.body || '';
      box.replaceChildren(ta, el('div.row', null,
        el('button.btn.sm.primary', { type: 'button', onclick: () => { editing = false; patch({ body: ta.value }) } }, 'Save'),
        el('button.btn.sm.ghost', { type: 'button', onclick: view }, 'Cancel')));
      ta.focus();
    };
    view();
    return box;
  }

  function noteRow(it, n) {
    if (n.kind === 'event') {
      return el('div.event', null, el('span.who', null, n.author || 'you'), el('span', null, ' ' + n.body), el('span.muted', null, ' · ' + ago(n.created_at)));
    }
    return el('div.note', null,
      el('div.note-head', null, el('b', null, n.author || 'you'), el('span.muted', null, ago(n.created_at)), el('span.grow'),
        el('button.icon-btn.bare.sm', {
          type: 'button', 'aria-label': 'Delete note', title: 'Delete note',
          onclick: async () => {
            if (!confirm('Delete this note?')) return;
            try { await api('DELETE', `${itemPath(it.ref)}/notes/${n.id}`); reload() } catch (e) { toast(e.message, 'error') }
          },
        }, svg(ICON.x))),
      markdown(n.body));
  }

  function noteBox(it) {
    const ta = el('textarea.textarea', { rows: '2', placeholder: 'Add a note for the agent or yourself', 'aria-label': 'New note' });
    return el('form.note-box', {
      onsubmit: async e => {
        e.preventDefault();
        if (!ta.value.trim()) return;
        try { await api('POST', `${itemPath(it.ref)}/notes`, { body: ta.value }); ta.value = ''; reload() } catch (err) { toast(err.message, 'error') }
      },
    }, ta, el('button.btn.sm', { type: 'submit' }, 'Add note'));
  }
}

async function workOn(it, harness) {
  try {
    const s = await api('POST', `${itemPath(it.ref)}/work`, { harness });
    closeSheet();
    navigate('/s/' + s.id);
  } catch (e) { toast(e.message, 'error') }
}

async function deleteItem(it) {
  if (!confirm(`Delete ${it.ref}? Its notes go with it.`)) return;
  try {
    await api('DELETE', itemPath(it.ref));
    closeSheet();
    toast(`Deleted ${it.ref}`);
    refresh();
  } catch (e) { toast(e.message, 'error') }
}

// itemRow is an item on one line, for the inbox.
function itemRow(it, tint) {
  return el('button.srow.item-row-btn.tone-wait', { type: 'button', onclick: () => openItem(it.ref) },
    el('span.avatar.sm.item-av', null, svg(ICON.board)),
    el('div.who', null,
      el('div.title', null, it.title),
      el('div.meta', null, el('span.mono', null, it.ref), el('span.sep', null, '·'),
        el('span.proj.tint-' + (tint ?? 0), null, el('span.pdot'), it.project),
        it.assignee ? el('span.sep', null, '·') : null, it.assignee ? el('span', null, it.assignee) : null)),
    svg(ICON.chevron, 'chev'));
}

// boardSnapshot is the board beside a project's chats on a desktop: how the
// columns stand, what's in progress and in review, and what's next.
function boardSnapshot(p, b) {
  const counts = b.board.counts || {};
  const total = COLUMNS.reduce((n, [k]) => n + (k === 'done' ? 0 : counts[k] || 0), 0);
  const items = s => b.items.filter(i => i.status === s);
  const group = (status, label, list) => list.length ? el('div.snap-group.st-' + status, null,
    el('div.snap-head', null, el('span.cdot'), el('span', null, label), el('span.n', null, String(list.length))),
    list.slice(0, 3).map(snapCard)) : null;
  const queue = items('todo').length ? ['todo', 'Todo', items('todo')] : ['backlog', 'Backlog', items('backlog')];
  return el('section.snapshot', { 'aria-label': 'Board' },
    el('div.snap-top', null, el('h2', null, 'Board'), el('a.snap-open', { href: projectPath(p.name, 'board'), 'data-link': true }, 'Open board →')),
    total ? el('div.snap-bar', { role: 'img', 'aria-label': COLUMNS.filter(([k]) => k !== 'done').map(([k, l]) => `${counts[k] || 0} ${l.toLowerCase()}`).join(', ') },
      // One segment per item (up to 60), so the bar needs no inline style.
      COLUMNS.filter(([k]) => k !== 'done').flatMap(([k]) => Array.from({ length: Math.min(counts[k] || 0, Math.ceil(60 * (counts[k] || 0) / total)) }, () => el('span.st-' + k)))) : null,
    total ? el('div.snap-legend', null, COLUMNS.filter(([k]) => k !== 'done').map(([k, l]) => el('span', null, `${counts[k] || 0} ${l.toLowerCase()}`)))
      : el('p.muted', null, 'Nothing on the board. Capture work from the Board tab.'),
    b.history ? boardChart(b.history) : null,
    group('in_progress', 'In progress', items('in_progress')),
    group('review', 'Review', items('review')),
    group(queue[0], queue[1], queue[2]));
}

// boardChart is the board's last two weeks: a bar for what was finished
// each day, and a line for how much was open at its end.
function boardChart(h) {
  const days = boardSeries(h);
  return dayChart(days, {
    label: `Finished per day and open items, last ${days.length} days`,
    stack: [['done', 'bc-bar']], line: 'open',
    title: d => `${d.done} finished, ${d.open} open`,
    stats: [[days.reduce((n, d) => n + d.done, 0), 'finished', 'done'], [days[days.length - 1].open, 'open now', 'open']],
  });
}

// dayChart draws one bar per day, stacked from o.stack ([key, class] pairs,
// bottom first), and optionally a line for o.line, as SVG from numbers
// only; each day's title carries its counts. It's shared by the project
// page's board chart and the activity page's.
function dayChart(days, o) {
  const ns = 'http://www.w3.org/2000/svg';
  const node = (tag, attrs, ...kids) => {
    const n = document.createElementNS(ns, tag);
    for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
    n.append(...kids);
    return n;
  };
  const W = 20, H = 56, top = 6;
  const total = d => o.stack.reduce((n, [k]) => n + d[k], 0);
  const maxBar = Math.max(1, ...days.map(total));
  const maxLine = o.line ? Math.max(1, ...days.map(d => d[o.line])) : 1;
  const y = (v, max) => top + (H - top) * (1 - v / max);
  const label = d => d.day.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' });
  const bars = (d, i) => {
    const out = [];
    let base = H;
    for (const [k, cls] of o.stack) {
      if (!d[k]) continue;
      // A day with a little of something still shows it.
      const h = Math.max(2, (H - top) * d[k] / maxBar);
      out.push(node('rect', { class: cls, x: i * W + 4, y: base - h, width: W - 8, height: h }));
      base -= h;
    }
    return out.length ? out : [node('rect', { class: 'bc-none', x: i * W + 4, y: H - 1.5, width: W - 8, height: 1.5 })];
  };
  const chart = node('svg', { viewBox: `0 0 ${W * days.length} ${H}`, class: 'bc-svg', preserveAspectRatio: 'none', role: 'img', 'aria-label': o.label },
    ...days.map((d, i) => node('g', { class: 'bc-day' },
      node('title', {}, `${label(d)}: ${o.title(d)}`),
      node('rect', { class: 'bc-hit', x: i * W, y: 0, width: W, height: H }),
      ...bars(d, i))),
    ...(o.line ? [
      node('polyline', { class: 'bc-open', points: days.map((d, i) => `${i * W + W / 2},${y(d[o.line], maxLine)}`).join(' ') }),
      node('circle', { class: 'bc-now', cx: (days.length - 1) * W + W / 2, cy: y(days[days.length - 1][o.line], maxLine), r: 3 })] : []));
  const short = d => d.day.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  return el('div.bchart', null,
    el('div.bc-head', null,
      o.stats.map(([n, text, key]) => el('span.bc-stat', null, el('b', null, n.toLocaleString()), el('span.bc-key.' + key, null, text))),
      el('span.bc-when', null, 'last ' + days.length + ' days')),
    chart,
    el('div.bc-axis', null, el('span', null, short(days[0])), el('span', null, 'Today')));
}

function snapCard(it) {
  return el('button.snap-card', { type: 'button', onclick: () => openItem(it.ref) },
    el('span.snap-meta', null, el('span.ref', null, it.ref),
      it.priority >= 2 ? el('span.prio-chip.p' + it.priority, null, PRIORITIES[it.priority]) : null,
      el('span.grow'), it.assignee ? el('span.who', null, it.assignee) : null),
    el('span.snap-title', null, it.title));
}

// upNext is the top of a project's queue, under its chats on a phone.
function upNext(p, b) {
  const todo = b.items.filter(i => i.status === 'todo');
  const list = (todo.length ? todo : b.items.filter(i => i.status === 'backlog'))
    .slice().sort((x, y) => y.priority - x.priority).slice(0, 3);
  if (!list.length) return null;
  return el('section.list-sec.phone-only', null,
    el('div.sec-title', null, el('h2', null, 'Up next on the board'), el('a.sec-link', { href: projectPath(p.name, 'board'), 'data-link': true }, 'Board')),
    el('div.row-list', null, list.map(it => el('button.next-row', { type: 'button', onclick: () => openItem(it.ref) },
      el('span.ref', null, it.ref), el('span.next-title', null, it.title),
      it.priority >= 2 ? el('span.prio-chip.p' + it.priority, null, PRIORITIES[it.priority]) : null))));
}
