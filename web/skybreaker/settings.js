'use strict';

// Settings: what agents read (instructions, prompts, commit and pull request
// rules) and what they reach (the GitHub App, secrets, bundles and each
// project's access), one section at a time. The config file stays the base
// and is shown read-only; what's changed here is saved in the data
// directory, and network changes apply when Skybreaker restarts, once no
// agent is working. Secret values go in and never come back out.

Object.assign(ICON, {
  box: ['M21 8l-9-5-9 5 9 5 9-5z', 'M3 8v8l9 5 9-5V8', 'M12 13v8'],
  git: ['M6 3v12', 'M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M18 9a9 9 0 0 1-9 9'],
  prompt: ['M4 5h16v14H4z', 'M8 10l2.5 2L8 14', 'M13 14h3'],
  rules: ['M9 11l2 2 4-4', 'M12 3l7 3v5c0 5-3.5 8.5-7 10c-3.5-1.5-7-5-7-10V6z'],
  find: ['M11 4a7 7 0 1 0 0 14a7 7 0 1 0 0-14', 'M20 20l-4-4'],
});

const githubLink = u => typeof u === 'string' && /^https:\/\/github\.com\//.test(u) ? u : null;
const oneLine = s => String(s || '').split('\n').map(x => x.trim()).find(Boolean) || '';

// The sections, in two groups: what agents read, and what they reach. Each
// says in a few words where it stands, for the index on a phone.
const SETTINGS = [
  ['Agents', [
    { id: 'instructions', label: 'Instructions', icon: 'file', sum: c => oneLine(c.ins?.text) || 'None yet' },
    { id: 'prompts', label: 'Prompts', icon: 'prompt', n: c => c.pr?.prompts.length, sum: c => c.pr?.prompts.length ? plural(c.pr.prompts.length, 'prompt') : 'None yet' },
    { id: 'rules', label: 'Commit rules', icon: 'rules', sum: c => rulesSummary(c.d.content_rules) },
  ]],
  ['Access', [
    { id: 'github', label: 'GitHub', icon: 'git', sum: c => c.d.github.state === 'app' || c.d.github.state === 'file' ? c.d.github.slug || 'App ' + c.d.github.app_id : 'Not set up' },
    { id: 'secrets', label: 'Secrets', icon: 'key', n: c => c.d.secrets.length, sum: c => c.d.secrets.length ? plural(c.d.secrets.length, 'secret') : 'None yet' },
    { id: 'bundles', label: 'Bundles', icon: 'box', n: c => c.d.bundles.length, sum: c => c.d.bundles.length ? plural(c.d.bundles.length, 'bundle') : 'None yet' },
    { id: 'access', label: 'Project access', icon: 'globe', n: c => c.d.projects.length, sum: c => plural(c.d.projects.length, 'project') },
  ]],
];

function rulesSummary(c) {
  const rules = [c?.app, c?.file].filter(Boolean);
  const pats = rules.reduce((n, r) => n + (r.commit?.forbid || []).length + (r.pull_request?.forbid || []).length, 0);
  const limits = rules.some(r => r.commit?.max_subject || r.commit?.max_message || r.pull_request?.max_title || r.pull_request?.max_body);
  return [limits ? 'Length limits' : '', pats ? plural(pats, 'pattern') : ''].filter(Boolean).join(' · ') || 'None set';
}

async function renderSettings(seq, r = { section: null }) {
  const [d, ins, pr, projects] = await Promise.all([api('GET', '/api/settings'), api('GET', '/api/instructions').catch(() => null),
    api('GET', '/api/prompts').catch(() => null), api('GET', '/api/projects').catch(() => [])]);
  if (seq !== state.seq) return;
  crumb(null);
  // GitHub sends you back here after creating the App.
  const q = new URLSearchParams(location.search);
  if (q.has('github')) toast('GitHub App created. Next, install it on your repositories.');
  if (q.has('github_error')) toast(q.get('github_error'), 'error');
  if (q.has('github') || q.has('github_error')) history.replaceState(null, '', location.pathname);

  const c = { d, ins, pr, ed: d.editable, tints: Object.fromEntries(projects.map(p => [p.name, p.tint])) };
  const section = r.section || 'instructions';
  const body = r.item !== undefined
    ? (section === 'bundles' ? bundlePage(c, r.item) : accessPage(c, r.item))
    : { instructions: instructionsSection, prompts: promptsSection, rules: rulesSection, github: githubSection, secrets: secretsSection, bundles: bundlesSection, access: accessSection }[section](c);
  // On a phone a section is its own page, back to the index; an item, back
  // to its section.
  const label = s => SETTINGS.flatMap(([, x]) => x).find(x => x.id === s)?.label;
  const back = r.item !== undefined ? el('a.set-back', { href: '/settings/' + section, 'data-link': true }, svg(ICON.back), label(section))
    : r.section ? el('a.set-back.phone-back', { href: '/settings', 'data-link': true }, svg(ICON.back), 'Settings') : null;
  const banners = [
    d.pending ? el('p.set-banner', null, el('span.spinner'), 'Saved. Skybreaker restarts to apply it once no agent is working.') : null,
    !d.admin ? el('p.set-banner', null, 'Only admins can change settings.')
      : !c.ed ? el('p.set-banner', null, 'Changes here are off: the config file sets app_settings: false.') : null,
  ].filter(Boolean);
  $('#app').replaceChildren(el('div.wrap.settings' + (r.section ? '' : '.set-index'), null,
    el('section.inbox-head.set-head', null, el('div', null, el('h1', null, 'Settings'),
      el('p', null, 'What your agents read, and what they can reach with which credentials. Your config file is the base, shown read-only; what you change here adds to it.'))),
    banners.length ? el('div.set-banners', null, banners) : null,
    el('div.set-layout', null, settingsNav(c, section, r.section),
      el('div.set-content', null, back, body))));
}

// settingsNav is the list of sections: beside the section on a desktop, the
// index on a phone.
function settingsNav(c, section, chosen) {
  return el('nav.set-nav', { 'aria-label': 'Settings' }, SETTINGS.map(([group, items]) =>
    el('div.set-nav-group', null, el('div.set-nav-label', null, group),
      el('div.set-nav-list', null, items.map(s => {
        const on = s.id === section;
        const n = s.n?.(c);
        return el('a.set-nav-link' + (on ? '.on' : ''), { href: '/settings/' + s.id, 'data-link': true, 'aria-current': on && chosen ? 'page' : null },
          svg(ICON[s.icon], 'set-nav-ic'),
          el('span.set-nav-text', null, el('span.set-nav-name', null, s.label), el('span.set-nav-sum', null, s.sum(c))),
          n ? el('span.set-nav-n', null, String(n)) : null,
          svg(ICON.chevron, 'chev'));
      })))));
}

function setSection(title, note, action, ...body) {
  return el('section.set-sec', null,
    el('div.set-sec-head', null, el('div.set-sec-title', null, el('h2', null, title), note ? el('p', null, note) : null), action),
    ...body);
}

// setList is a section's items, one row each; with more than a handful a
// filter sits above them.
function setList(rows, empty, what) {
  if (!rows.length) return el('div.set-empty', null, empty);
  const list = el('div.set-list', null, rows);
  if (rows.length <= 6) return list;
  const none = el('div.set-empty.flat', { hidden: true }, 'Nothing matches.');
  const filter = el('input.set-filter', {
    type: 'search', placeholder: 'Filter ' + what, 'aria-label': 'Filter ' + what, autocomplete: 'off', spellcheck: 'false',
    oninput: () => {
      const q = filter.value.trim().toLowerCase();
      let shown = 0;
      for (const r of rows) { const hit = !q || r.textContent.toLowerCase().includes(q); r.hidden = !hit; shown += hit }
      none.hidden = shown > 0;
    },
  });
  return el('div.set-filtered', null, el('label.set-find', null, svg(ICON.find), filter), list, none);
}

const fileTag = () => el('span.tag', { title: 'Set in the config file; change it there' }, 'Config file');

// saveBar is the bar that rises while a form has unsaved changes. While it
// shows, live updates don't redraw the page and leaving it asks first.
function saveBar(onSave, onDiscard, label = 'Save') {
  const save = el('button.btn.primary.sm', { type: 'button', onclick: () => onSave() }, label);
  const node = el('div.save-bar', { hidden: true, role: 'region', 'aria-label': 'Unsaved changes' },
    el('span.save-note', null, 'Unsaved changes'), el('span.grow'),
    el('button.btn.ghost.sm', { type: 'button', onclick: () => onDiscard() }, 'Discard'), save);
  return {
    node, save,
    // A new item's bar shows from the start; it's unsaved work once typed in.
    set(dirty, ok = true, show = dirty) {
      node.hidden = !show;
      save.disabled = !ok;
      state.dirty = dirty;
    },
  };
}

// --- instructions ------------------------------------------------------------------

// instrDraft keeps what you typed across a redraw, against the text it
// started from.
let instrDraft = null;

function instructionsSection(c) {
  const ins = c.ins;
  if (!ins) return setSection('Instructions', null, null, el('div.set-empty', null, 'Instructions aren\'t available.'));
  if (instrDraft && instrDraft.base !== ins.text) instrDraft = null;
  const box = el('textarea.np-input.set-instr', {
    rows: 10, maxlength: ins.max, readOnly: !ins.editable, spellcheck: 'true', 'aria-label': 'Instructions for every agent',
    placeholder: ins.editable ? 'Conventions every agent should follow, in Markdown. For example: use pnpm, not npm. Write commit messages in the imperative.' : 'None set.',
  });
  box.value = instrDraft ? instrDraft.text : ins.text;
  const count = el('span.set-count');
  const bar = saveBar(async () => {
    bar.save.disabled = true;
    try {
      await api('PUT', '/api/instructions', { text: box.value });
      instrDraft = null;
      bar.set(false);
      toast('Saved. New sessions in every project get them.');
      go();
    } catch (err) { toast(err.message, 'error'); sync() }
  }, () => { instrDraft = null; box.value = ins.text; sync() });
  const sync = () => {
    const n = new TextEncoder().encode(box.value).length;
    count.textContent = `${n < 1024 ? plural(n, 'byte') : (n / 1024).toFixed(1) + ' KiB'} of ${ins.max >> 10} KiB`;
    bar.set(box.value !== ins.text, n <= ins.max);
  };
  box.addEventListener('input', () => { instrDraft = { base: ins.text, text: box.value }; sync() });
  sync();
  return setSection('Instructions', 'Every agent in every project reads these: Claude Code as its managed CLAUDE.md, Codex as developer instructions, pi as part of its system prompt. A project\'s own CLAUDE.md or AGENTS.md still applies alongside them. Sessions started after a save get them.', null,
    el('div.set-field', null, box, el('span.set-hint', null, count)), bar.node);
}

// --- prompts -----------------------------------------------------------------------

function promptsSection(c) {
  const pr = c.pr;
  if (!pr) return setSection('Prompts', null, null, el('div.set-empty', null, 'Prompts aren\'t available.'));
  const add = pr.editable ? el('button.btn.sm', { type: 'button', onclick: () => promptSheet(pr) }, svg(ICON.plus), 'New prompt') : null;
  const rows = pr.prompts.map(p => el('button.set-row', { type: 'button', onclick: () => promptSheet(pr, p) },
    el('span.set-row-main', null, el('span.set-row-title', null, el('b.mono', null, '/' + p.name)), el('span.set-row-sub', null, oneLine(p.text))),
    svg(ICON.chevron, 'chev')));
  return setSection('Prompts', 'Prompts you reuse in any chat, with every agent: type /prompt and pick one, or /prompt name and anything to add to it.', add,
    setList(rows, 'No prompts yet.', 'prompts'));
}

// promptSheet adds a prompt, or edits or deletes one. Prompts save as one
// list, so this writes the whole list with the one change.
function promptSheet(pr, old) {
  const ed = pr.editable;
  const name = el('input.np-input', { type: 'text', value: old?.name || null, placeholder: 'review', maxlength: 40, autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', required: true, readOnly: !ed, 'aria-label': 'Name' });
  const text = el('textarea.np-input.set-prompt-text', { rows: 8, spellcheck: 'true', placeholder: 'What to send, in Markdown.', required: true, readOnly: !ed, 'aria-label': 'Prompt' });
  text.value = old?.text || '';
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const put = async list => {
    error.hidden = true;
    try {
      await api('PUT', '/api/prompts', { prompts: list });
      closeSheet();
      toast('Saved. Type /prompt in any chat to use it.');
      go();
    } catch (err) { error.textContent = err.message; error.hidden = false }
  };
  const others = pr.prompts.filter(p => p !== old);
  const form = el('form.np-form', {
    onsubmit: e => {
      e.preventDefault();
      const p = { name: name.value.trim(), text: text.value };
      put(old ? pr.prompts.map(x => x === old ? p : x) : [...others, p]);
    },
  },
    el('label.np-field', null, el('span.np-label', null, 'Name'), el('span.set-prefixed', null, el('span', null, '/prompt'), name),
      el('span.np-hint', null, 'Lower case: letters, digits, - and _.')),
    el('label.np-field', null, el('span.np-label', null, 'Prompt'), text),
    error,
    ed ? el('div.set-acts', null,
      old ? el('button.btn.ghost.danger-text', { type: 'button', onclick: () => { if (confirm(`Delete /${old.name}?`)) put(others) } }, svg(ICON.trash), 'Delete') : null,
      el('span.grow'), el('button.btn.primary', { type: 'submit' }, old ? 'Save' : 'Add prompt')) : null);
  settingsSheet(old ? '/' + old.name : 'New prompt', null, form);
  (old ? text : name).focus();
}

// --- commit and pull request rules ------------------------------------------------

// rulesDraft keeps what you changed across a redraw, against the rules it
// started from.
let rulesDraft = null;

// rulesSection edits the app's commit and pull request rules. The broker
// checks every push and pull request against them and the config file's,
// the stricter limit and every pattern, and tells the agent what to fix.
function rulesSection(c) {
  const ed = c.ed;
  const rc = c.d.content_rules || {};
  const app = rc.app || {}, file = rc.file || {};
  const base = JSON.stringify(app);
  if (rulesDraft && rulesDraft.base !== base) rulesDraft = null;
  const start = rulesDraft?.values || {
    commit: { max_subject: app.commit?.max_subject || 0, max_message: app.commit?.max_message || 0, forbid: app.commit?.forbid || [] },
    pull_request: { max_title: app.pull_request?.max_title || 0, max_body: app.pull_request?.max_body || 0, forbid: app.pull_request?.forbid || [] },
  };
  const initial = JSON.stringify({
    commit: { max_subject: app.commit?.max_subject || 0, max_message: app.commit?.max_message || 0, forbid: app.commit?.forbid || [] },
    pull_request: { max_title: app.pull_request?.max_title || 0, max_body: app.pull_request?.max_body || 0, forbid: app.pull_request?.forbid || [] },
  });
  const parts = [];
  const changed = () => {
    const v = values();
    rulesDraft = { base, values: v };
    bar.set(JSON.stringify(v) !== initial, parts.every(p => p.ok()));
  };
  const card = (key, title, note, limits) => {
    const nums = limits.map(([k, label]) => {
      const input = el('input', { type: 'number', min: 0, step: 1, placeholder: 'No limit', disabled: !ed || null, 'aria-label': label + ' limit', oninput: changed });
      input.value = start[key][k] || '';
      const fileLimit = file[key]?.[k];
      return { k, input, node: el('label.set-field', null, el('span.np-label', null, label),
        el('span.num-field', null, input, el('span', null, 'characters')),
        fileLimit ? el('span.set-hint', null, `The config file sets ${fileLimit}; the stricter applies.`) : null) };
    });
    const pats = patternList(start[key].forbid, file[key]?.forbid || [], !ed, changed);
    parts.push({ ok: () => nums.every(n => n.input.value === '' || Number.isInteger(Number(n.input.value)) && Number(n.input.value) >= 0) });
    return {
      read: () => ({ ...Object.fromEntries(nums.map(n => [n.k, n.input.value === '' ? 0 : Number(n.input.value)])), forbid: pats.values() }),
      node: el('section.set-card', null, el('div.set-card-head', null, el('h3', null, title), el('p', null, note)),
        el('div.set-limits', null, nums.map(n => n.node)),
        el('div.set-field', null, el('span.np-label', null, 'Forbidden patterns'), pats.node)),
    };
  };
  const commit = card('commit', 'Commits', 'Checked on every push.', [['max_subject', 'Subject'], ['max_message', 'Whole message']]);
  const pull = card('pull_request', 'Pull requests', 'Checked when an agent opens or edits one.', [['max_title', 'Title'], ['max_body', 'Body']]);
  const values = () => ({ commit: commit.read(), pull_request: pull.read() });
  const bar = saveBar(async () => {
    bar.save.disabled = true;
    try {
      await api('PUT', '/api/content-rules', values());
      rulesDraft = null;
      bar.set(false);
      saved();
    } catch (err) { toast(err.message, 'error'); changed() }
  }, () => { rulesDraft = null; state.dirty = false; go() });
  changed();
  return setSection('Commit rules', 'The broker checks what agents push and the pull requests they open, and tells the agent what to fix. Patterns are Go regular expressions: (?i) ignores case, (?m) makes ^ and $ match each line.', null,
    commit.node, pull.node, bar.node);
}

// patternList edits a list of patterns, one row each; the config file's
// show too, locked.
function patternList(initial, fromFile, readOnly, changed) {
  const list = el('div.pat-list');
  const rows = [];
  const row = text => {
    const r = el('div.pat-row', null, el('code', null, reveal(text)),
      readOnly ? null : el('button.icon-btn.bare.sm', { type: 'button', 'aria-label': 'Remove ' + text, title: 'Remove', onclick: () => { r.remove(); rows.splice(rows.findIndex(x => x.node === r), 1); sync(); changed() } }, svg(ICON.x)));
    rows.push({ text, node: r });
    list.append(r);
  };
  for (const t of fromFile) list.append(el('div.pat-row.locked', null, el('code', null, reveal(t)), fileTag()));
  initial.forEach(row);
  const none = el('div.pat-none', null, 'None');
  const sync = () => { none.hidden = rows.length + fromFile.length > 0 };
  const input = el('input', { type: 'text', placeholder: 'Add a pattern, such as (?i)\\bwip\\b', autocomplete: 'off', spellcheck: 'false', 'aria-label': 'New pattern' });
  const addOne = () => {
    const t = input.value.trim();
    if (!t) return;
    if (!rows.some(r => r.text === t)) { row(t); sync(); changed() }
    input.value = '';
    input.focus();
  };
  input.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addOne() } });
  sync();
  return {
    node: el('div.pats', null, none, list, readOnly ? null : el('div.pat-add', null, input, el('button.btn.sm', { type: 'button', onclick: addOne }, svg(ICON.plus), 'Add'))),
    values: () => rows.map(r => r.text),
  };
}

// --- GitHub ----------------------------------------------------------------------

function githubSection(c) {
  const g = c.d.github, ed = c.ed;
  const install = githubLink(g.install_url);
  if (g.state === 'app' || g.state === 'file') {
    const name = g.slug || 'App ' + g.app_id;
    return setSection('GitHub', 'Agents clone and push through the broker with this App\'s short-lived tokens, one repository at a time.', null,
      el('div.set-list', null, el('div.set-row.static', null, svg(ICON.git, 'set-ic'),
        el('span.set-row-main', null, el('span.set-row-title', null, el('b.mono', null, name)),
          el('span.set-row-sub', null, [g.owner ? 'owned by ' + g.owner : '', 'App ID ' + g.app_id, g.state === 'file' ? 'from secrets.yaml' : 'created here'].filter(Boolean).join(' · ')),
          g.error ? el('span.set-row-sub.bad', null, g.error) : null),
        install ? el('a.btn.sm', { href: install, target: '_blank', rel: 'noopener noreferrer' }, svg(ICON.external), 'Install on repositories') : null)),
      githubLacking(g.lacking));
  }
  if (!g.can_create) {
    return setSection('GitHub', null, null, el('div.set-empty', null, g.error || 'A secret named github_app in the secrets file isn\'t a GitHub App, so one can\'t be created here.'));
  }
  const org = el('input.np-input', { type: 'text', placeholder: 'organization', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-label': 'Organization', disabled: true });
  const mine = el('input', { type: 'radio', name: 'gh-owner', value: 'me', checked: true, onchange: () => { org.disabled = true } });
  const theirs = el('input', { type: 'radio', name: 'gh-owner', value: 'org', onchange: () => { org.disabled = false; org.focus() } });
  const create = el('button.btn.primary', { type: 'submit', disabled: !ed }, svg(ICON.git), 'Create GitHub App');
  const form = el('form.set-card.set-gh', {
    onsubmit: async e => {
      e.preventDefault();
      create.disabled = true;
      try {
        const m = await api('POST', '/api/github-app/manifest', { org: theirs.checked ? org.value.trim() : '' });
        if (!githubLink(m.action)) throw new Error('unexpected address for GitHub');
        // GitHub's manifest flow takes a form post; it creates the App when
        // you confirm and sends you back here.
        const post = el('form', { method: 'post', action: m.action, hidden: true }, el('input', { type: 'hidden', name: 'manifest', value: m.manifest }));
        document.body.append(post);
        post.submit();
      } catch (err) { toast(err.message, 'error'); create.disabled = false }
    },
  },
    el('fieldset.np-group', null, el('legend', null, 'Owned by'),
      el('label.np-check', null, mine, el('span.np-check-text', null, el('b', null, 'My account'))),
      el('label.np-check', null, theirs, el('span.np-check-text', null, el('b', null, 'An organization'), org))),
    el('div', null, create));
  return setSection('GitHub', 'Agents reach GitHub only through the broker, with a GitHub App\'s short-lived tokens. GitHub opens with the App filled in: read and write to contents, pull requests and issues, read metadata, Actions, checks and commit statuses, no webhook. Confirm there and you come back here to install it.', null, form);
}

// githubLacking says what the App can't yet do that agents need, such as
// read CI, and where to fix it: the App's permissions on GitHub, then each
// installation, whose owner accepts them.
function githubLacking(l) {
  if (!l) return null;
  const link = (url, text) => githubLink(url) ? el('a.btn.sm', { href: url, target: '_blank', rel: 'noopener noreferrer' }, svg(ICON.external), text) : null;
  const rows = [];
  if (l.app?.length) {
    rows.push(el('div.set-lack-row', null,
      el('span', null, 'The App lacks ', el('span.mono', null, l.app.join(', ')), '. Add them to its permissions on GitHub, then accept them where it\'s installed.'),
      link(l.permissions_url, 'Change permissions')));
  }
  for (const i of l.installations || []) {
    rows.push(el('div.set-lack-row', null,
      el('span', null, 'Its installation on ', el('b', null, i.account), ' hasn\'t accepted ', el('span.mono', null, i.lacks.join(', ')), ' yet.'),
      link(i.url, 'Review and accept')));
  }
  return el('div.set-lack', { role: 'note' }, el('b', null, 'Agents can\'t do everything they need on GitHub'),
    el('p.np-hint', null, 'Without these, agents can\'t read CI runs, their logs or pull request checks, and re-run CI themselves to find out why a pull request failed.'),
    rows);
}

// --- secrets -----------------------------------------------------------------------

function secretsSection(c) {
  const add = c.ed ? el('button.btn.sm', { type: 'button', onclick: () => secretSheet(c) }, svg(ICON.plus), 'Add secret') : null;
  const rows = c.d.secrets.map(s => el('button.set-row', { type: 'button', onclick: () => secretSheet(c, s) },
    svg(ICON.key, 'set-ic'),
    el('span.set-row-main', null,
      el('span.set-row-title', null, el('b.mono', null, s.name), s.source !== 'app' ? fileTag() : null),
      el('span.set-row-sub.mono', null, s.hosts.join(', '))),
    el('span.set-row-side', null, s.used_by.length ? s.used_by.join(', ') : 'Not used'),
    svg(ICON.chevron, 'chev')));
  return setSection('Secrets', 'API keys and tokens the broker adds to requests. Each goes only to the hosts listed with it, and is never shown again: not here, not to agents.', add,
    setList(rows, 'No secrets yet.', 'secrets'));
}

// secretSheet shows a secret and replaces or deletes it, or adds one. Its
// hosts change only with the value: sending a secret somewhere new needs
// the secret itself.
function secretSheet(c, old) {
  const field = (label, input, hint) => el('label.np-field', null, el('span.np-label', null, label), input, hint ? el('span.np-hint', null, hint) : null);
  const mine = !old || old.source === 'app';
  const details = old ? el('dl.set-dl', null,
    el('dt', null, 'Sent to'), el('dd.mono', null, old.hosts.join(', ')),
    el('dt', null, 'Used by'), el('dd', null, old.used_by.length ? old.used_by.join(', ') : 'Nothing yet'),
    old.fingerprint ? [el('dt', null, 'Fingerprint'), el('dd.mono', null, old.fingerprint)] : null,
    old.added_by ? [el('dt', null, 'Added'), el('dd', null, `by ${old.added_by} ${ago(old.added)}`)] : null,
    el('dt', null, 'Last used'), el('dd', null, old.last_used ? ago(old.last_used) : 'Not since Skybreaker started')) : null;
  if (old && (!mine || !c.ed)) {
    settingsSheet(old.name, null, details, el('p.np-hint', null, mine ? 'Only admins can change it.' : 'It\'s in the secrets file; change it there.'));
    return;
  }
  const name = el('input.np-input', { type: 'text', placeholder: 'openai', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', required: true, pattern: '[a-z][a-z0-9_\\-]{0,47}' });
  const value = el('input.np-input', { type: 'password', autocomplete: 'new-password', spellcheck: 'false', required: true, placeholder: old ? 'Paste the new value' : 'Paste the value' });
  const hosts = el('input.np-input', { type: 'text', placeholder: 'api.example.com', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', required: true, value: old ? old.hosts.join(', ') : null });
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const form = el('form.np-form', {
    onsubmit: async e => {
      e.preventDefault();
      error.hidden = true;
      try {
        const body = { value: value.value, hosts: hostsFromText(hosts.value) };
        if (old) await api('PUT', '/api/secrets/' + encodeURIComponent(old.name), body);
        else await api('POST', '/api/secrets', { name: name.value.trim(), ...body });
        value.value = '';
        closeSheet();
        saved();
      } catch (err) { error.textContent = err.message; error.hidden = false }
    },
  },
    old ? el('div.set-sheet-label', null, 'Replace its value') : field('Name', name, 'Bundles name it to send it: lowercase letters, digits, dashes and underscores.'),
    field(old ? 'New value' : 'Value', value, 'It isn\'t shown again.'),
    field('Hosts', hosts, old ? 'Where it may be sent. To change these, paste the value again.' : 'Where it may be sent, exactly: api.example.com, separated by commas.'),
    error,
    el('div.set-acts', null,
      old ? el('button.btn.ghost.danger-text', { type: 'button', onclick: () => deleteSecret(old) }, svg(ICON.trash), 'Delete') : null,
      el('span.grow'), el('button.btn.primary', { type: 'submit' }, old ? 'Replace' : 'Add secret')));
  settingsSheet(old ? old.name : 'Add a secret', null, details, form);
  (old ? value : name).focus();
}

async function deleteSecret(s) {
  if (!confirm(`Delete ${s.name}? Anything that sends it stops working.`)) return;
  try { await api('DELETE', '/api/secrets/' + encodeURIComponent(s.name)); closeSheet(); saved() } catch (e) { toast(e.message, 'error') }
}

// --- bundles -------------------------------------------------------------------------

const hostList = entries => {
  const hosts = entries.map(e => e.host || e.route);
  return hosts.length > 3 ? hosts.slice(0, 3).join(', ') + ` and ${hosts.length - 3} more` : hosts.join(', ');
};
const projectDots = (names, tints) => names.map(n => el('span.proj.tint-' + (tints[n] ?? 0), null, el('span.pdot'), n));

function bundlesSection(c) {
  const add = c.ed ? el('a.btn.sm', { href: '/settings/bundles/new', 'data-link': true }, svg(ICON.plus), 'New bundle') : null;
  const rows = c.d.bundles.map(b => el('a.set-row', { href: '/settings/bundles/' + encodeURIComponent(b.name), 'data-link': true },
    svg(ICON.box, 'set-ic'),
    el('span.set-row-main', null,
      el('span.set-row-title', null, el('b.mono', null, b.name), b.source !== 'app' ? fileTag() : null),
      el('span.set-row-sub.mono', null, hostList(b.entries))),
    el('span.set-row-side', null, b.used_by.length ? projectDots(b.used_by, c.tints) : 'No project uses it'),
    svg(ICON.chevron, 'chev')));
  return setSection('Bundles', 'Named sets of hosts, and how each is reached, that projects take: github, openai, your registry.', add,
    setList(rows, 'No bundles yet.', 'bundles'));
}

// bundlePage edits a bundle, or makes one: its hosts, then a review of what
// the change does to each project's reach before it's saved.
function bundlePage(c, name) {
  const fresh = name === 'new';
  const b = fresh ? null : c.d.bundles.find(x => x.name === name);
  if (!fresh && !b) return el('div.set-empty', null, 'There\'s no bundle called ' + name + '.');
  const readOnly = !c.ed || (b && b.source !== 'app');
  const nameInput = fresh ? el('input.np-input', { type: 'text', placeholder: 'openai', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-label': 'Name', oninput: () => changed() }) : null;
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const hosts = hostEditor(b ? b.entries : [], c.d.secrets, readOnly, () => changed());
  const changed = () => fresh
    ? bar.set(!!nameInput.value.trim() || hosts.typed(), !!nameInput.value.trim(), true)
    : bar.set(hosts.dirty());
  const bundleName = () => fresh ? nameInput.value.trim() : b.name;
  const bar = saveBar(async () => {
    error.hidden = true;
    const list = hosts.read();
    if (list.error) { error.textContent = list.error; error.hidden = false; return }
    const path = '/api/bundles/' + encodeURIComponent(bundleName());
    try {
      const r = await api('PUT', path, { entries: list.entries, dry_run: true });
      reviewSheet('Bundle ' + bundleName(), r, 'No project uses this bundle yet. Give it to one in Project access.',
        () => api('PUT', path, { entries: list.entries }), () => { bar.set(false); navigate('/settings/bundles/' + encodeURIComponent(bundleName())) });
    } catch (err) { error.textContent = err.message; error.hidden = false }
  }, () => { bar.set(false); fresh ? navigate('/settings/bundles') : go() }, fresh ? 'Review' : 'Review changes');
  const del = async () => {
    if (!confirm(`Delete the ${b.name} bundle?`)) return;
    try { await api('DELETE', '/api/bundles/' + encodeURIComponent(b.name)); bar.set(false); toast('Deleted. It applies when Skybreaker restarts, once no agent is working.'); navigate('/settings/bundles') } catch (e) { toast(e.message, 'error') }
  };
  if (fresh) { hosts.add({ mode: 'mitm' }); changed() }
  return setSection(fresh ? 'New bundle' : b.name,
    readOnly && b ? 'From the config file; change it there.' : 'The hosts this bundle lets a project reach, and how.',
    !fresh && !readOnly ? el('button.btn.sm.ghost.danger-text', { type: 'button', onclick: del }, svg(ICON.trash), 'Delete') : null,
    fresh ? el('label.set-field', null, el('span.np-label', null, 'Name'), nameInput, el('span.set-hint', null, 'Lowercase letters, digits, dashes and underscores. Projects take it by this name.')) : null,
    b ? el('p.set-hint', null, b.used_by.length ? ['Used by ', projectDots(b.used_by, c.tints)] : 'No project uses it yet.') : null,
    hosts.node, error, readOnly ? null : bar.node);
}

// --- project access -----------------------------------------------------------------

function accessSection(c) {
  const rows = c.d.projects.map(p => {
    const caps = [...p.file.capabilities || [], ...p.app.capabilities || []];
    return el('a.set-row', { href: '/settings/access/' + encodeURIComponent(p.name), 'data-link': true },
      el('span.proj.tint-' + (c.tints[p.name] ?? 0), null, el('span.pdot')),
      el('span.set-row-main', null,
        el('span.set-row-title', null, el('b', null, p.name), p.added ? el('span.tag', null, 'Made here') : null),
        el('span.set-row-sub', null, caps.length ? caps.join(', ') : 'No bundles')),
      el('span.set-row-side', null, plural(p.reaches.length, 'host')),
      svg(ICON.chevron, 'chev'));
  });
  return setSection('Project access', 'What each project\'s sandbox may reach. Anything else is denied.', null,
    setList(rows, 'No projects yet.', 'projects'));
}

// accessPage edits what one project may reach: the bundles it takes and
// hosts of its own, beside what the config file gives it.
function accessPage(c, name) {
  const p = c.d.projects.find(x => x.name === name);
  if (!p) return el('div.set-empty', null, 'There\'s no project called ' + name + '.');
  const ed = c.ed;
  const fileCaps = p.file.capabilities || [];
  const appCaps = p.app.capabilities || [];
  const start = JSON.stringify(appCaps.slice().sort());
  const checks = c.d.bundles.map(b => {
    const locked = fileCaps.includes(b.name);
    const box = el('input', { type: 'checkbox', value: b.name, checked: locked || appCaps.includes(b.name) || null, disabled: locked || !ed || null, onchange: () => changed() });
    return { box, locked, node: el('label.check-row' + (locked ? '.locked' : ''), null, box,
      el('span.set-row-main', null, el('span.set-row-title', null, el('b.mono', null, b.name), locked ? fileTag() : null),
        el('span.set-row-sub.mono', null, hostList(b.entries)))) };
  });
  const chosen = () => checks.filter(x => !x.locked && x.box.checked).map(x => x.box.value).sort();
  const hosts = hostEditor(p.app.allow || [], c.d.secrets, !ed, () => changed());
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const changed = () => bar.set(JSON.stringify(chosen()) !== start || hosts.dirty());
  const path = `/api/projects/${encodeURIComponent(p.name)}/access`;
  const bar = saveBar(async () => {
    error.hidden = true;
    const list = hosts.read();
    if (list.error) { error.textContent = list.error; error.hidden = false; return }
    const body = { capabilities: chosen(), allow: list.entries };
    try {
      const r = await api('PUT', path, { ...body, dry_run: true });
      reviewSheet(p.name, r, 'Nothing changes.', () => api('PUT', path, body), () => { bar.set(false); go() });
    } catch (err) { error.textContent = err.message; error.hidden = false }
  }, () => { bar.set(false); go() }, 'Review changes');
  const fileHosts = p.file.allow || [];
  return setSection(el('span.set-proj-title.tint-' + (c.tints[p.name] ?? 0), null, el('span.pdot'), p.name), 'What this project\'s sandbox may reach. Anything else is denied.', null,
    el('div.set-field', null, el('span.np-label', null, 'Bundles'),
      checks.length ? el('div.set-list', null, checks.map(x => x.node)) : el('div.set-empty', null, 'No bundles yet.')),
    fileHosts.length ? el('div.set-field', null, el('span.np-label', null, 'Hosts from the config file'),
      el('div.set-list', null, fileHosts.map(e => el('div.set-row.static', null, svg(ICON.globe, 'set-ic'),
        el('span.set-row-main', null, el('b.mono', null, e.host || e.route), el('span.set-row-sub', null, e.mode === 'tunnel' ? 'Passed through unread' : 'Inspected')), fileTag())))) : null,
    el('div.set-field', null, el('span.np-label', null, 'Hosts of its own'), hosts.node),
    p.reaches.length ? el('details.set-reach', null, el('summary', null, `What it reaches now: ${plural(p.reaches.length, 'host')}`),
      el('div.rv', null, p.reaches.map(x => reach(x, 'now')))) : null,
    error, ed ? bar.node : null);
}

// --- the host editor ---------------------------------------------------------------------

const RULE_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', '*'];
const RULE_ACTIONS = [['auto', 'Allow'], ['ask', 'Ask'], ['deny', 'Deny']];

// hostEditor edits a list of hosts: each passed through unread or
// inspected, with rules for its requests and a secret to send. The app adds
// hosts only; routes stay in the config file. A rule keeps what its row
// can't show (pushed refs, GraphQL operations, redirects) as it was.
function hostEditor(initial, secrets, readOnly, changed) {
  const list = el('div.hosts');
  const cards = [];
  let removed = false;
  const add = (e, focus) => {
    let mode = e.mode === 'tunnel' ? 'tunnel' : 'mitm';
    const host = el('input.np-input.host-name', { type: 'text', placeholder: 'api.example.com', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', value: e.host || null, 'aria-label': 'Host', disabled: readOnly || null });
    const hint = el('p.host-hint');
    const body = el('div.host-body');
    const modeSeg = seg([['mitm', 'Inspect'], ['tunnel', 'Pass through']], mode, v => { mode = v; sync(); changed() }, { disabled: readOnly, cls: '.set-seg' });

    // Rules, one row each: method, path, and what happens.
    const rules = el('div.rules');
    const rows = [];
    let rulesTouched = false;
    const touch = () => { rulesTouched = true; changed() };
    const ruleRow = r => {
      const m = /^(\S+)\s+(\S.*)$/.exec(r.match || '') || [null, 'GET', '/**'];
      const method = el('select.np-input.rule-method', { 'aria-label': 'Method', disabled: readOnly || null, onchange: touch },
        [...new Set([...RULE_METHODS, m[1]])].map(x => el('option', { value: x, selected: x === m[1] || null }, x === '*' ? 'Any' : x)));
      const path = el('input.np-input.rule-path', { type: 'text', value: m[2], placeholder: '/v1/**', autocomplete: 'off', spellcheck: 'false', 'aria-label': 'Path', disabled: readOnly || null, oninput: touch });
      let action = ['auto', 'ask', 'deny'].includes(r.action) ? r.action : 'auto';
      const extra = [r.refs?.length ? 'refs' : '', r.graphql ? 'GraphQL' : '', r.follow_redirects?.length ? 'redirects' : ''].filter(Boolean);
      const row = { r, method, path, action: () => action };
      row.node = el('div.rule', null, method, path,
        seg(RULE_ACTIONS, action, v => { action = v; touch() }, { disabled: readOnly, cls: '.set-seg' }),
        extra.length ? el('span.tag.rule-extra', { title: 'Also limits ' + extra.join(', ') + '; kept as it is' }, '+' + extra.join(', ')) : null,
        readOnly ? null : el('button.icon-btn.bare.sm', { type: 'button', 'aria-label': 'Remove rule', title: 'Remove', onclick: () => { row.node.remove(); rows.splice(rows.indexOf(row), 1); touch() } }, svg(ICON.x)));
      rows.push(row);
      rules.append(row.node);
      return row;
    };
    const startRules = e.methods?.length ? e.methods.map(x => ({ match: x + ' /**', action: 'auto' })) : e.rules?.length ? e.rules : [{ match: 'GET /**', action: 'auto' }];
    startRules.forEach(ruleRow);

    // The secret it sends, and how.
    const inj = e.inject || {};
    const secretName = inj.bearer || inj.header?.secret || inj.basic_auth?.secret || '';
    const secret = el('select.np-input', { 'aria-label': 'Secret to send', disabled: readOnly || null },
      el('option', { value: '' }, 'No secret'),
      secrets.filter(s => s.kind === 'static' || s.name === secretName).map(s => el('option', { value: s.name, selected: s.name === secretName || null }, s.name)));
    const how = el('select.np-input', { 'aria-label': 'Send it as', disabled: readOnly || null },
      el('option', { value: 'bearer', selected: !!inj.bearer || null }, 'as a Bearer token'),
      el('option', { value: 'header', selected: !!inj.header || null }, 'in a header'),
      el('option', { value: 'basic', selected: !!inj.basic_auth || null }, 'as basic auth'));
    const header = el('input.np-input', { type: 'text', placeholder: 'X-Api-Key', autocomplete: 'off', spellcheck: 'false', value: inj.header?.name || null, 'aria-label': 'Header name', disabled: readOnly || null });
    const user = el('input.np-input', { type: 'text', placeholder: 'user', autocomplete: 'off', spellcheck: 'false', value: inj.basic_auth?.user || null, 'aria-label': 'User', disabled: readOnly || null });
    const ports = el('input.np-input.host-ports', { type: 'text', placeholder: '443, the default', inputmode: 'numeric', value: (e.ports || []).join(', ') || null, 'aria-label': 'Ports', disabled: readOnly || null });

    body.append(
      el('div.host-label', null, 'Requests'),
      rules,
      readOnly ? null : el('button.btn.sm.ghost.host-add', { type: 'button', onclick: () => { ruleRow({ match: 'GET /**', action: 'auto' }).path.focus(); touch() } }, svg(ICON.plus), 'Add rule'),
      el('div.host-label', null, 'Credential'),
      el('div.cred', null, secret, how, header, user));
    const sync = () => {
      hint.textContent = mode === 'tunnel' ? 'Passed through unread: no rules, and no secret added.' : 'Each request is read: the first rule that matches decides, and anything else is denied.';
      body.hidden = mode === 'tunnel';
      how.hidden = !secret.value;
      header.hidden = !secret.value || how.value !== 'header';
      user.hidden = !secret.value || how.value !== 'basic';
    };
    [secret, how].forEach(s => s.addEventListener('change', sync));
    const card = el('div.host-card', null,
      el('div.host-head', null, host, modeSeg,
        readOnly ? null : el('button.icon-btn.bare', { type: 'button', 'aria-label': 'Remove host', title: 'Remove host', onclick: () => { card.remove(); cards.splice(cards.indexOf(c), 1); removed = true; changed() } }, svg(ICON.trash))),
      hint, body,
      el('label.host-foot', null, el('span', null, 'Ports'), ports));
    // Typing anywhere in the card is a change.
    card.addEventListener('input', () => changed());
    card.addEventListener('change', () => changed());
    const snapshot = () => JSON.stringify([host.value, mode, ports.value, secret.value, how.value, header.value, user.value, rulesTouched]);
    const c = {
      card, start: e.host ? snapshot() : null,
      dirty: () => c.start === null || snapshot() !== c.start,
      read() {
        const out = { host: host.value.trim().toLowerCase(), mode };
        if (!out.host) return { error: 'a host is empty' };
        const ps = ports.value.split(/[\s,]+/).filter(Boolean).map(Number);
        if (ps.some(n => !Number.isInteger(n) || n < 1 || n > 65535)) return { error: `${out.host}: ports are numbers from 1 to 65535` };
        if (ps.length) out.ports = ps;
        if (mode === 'tunnel') return { entry: out };
        // Rules nobody touched stay exactly as they were.
        if (!rulesTouched && (e.rules || e.methods)) {
          if (e.methods) out.methods = e.methods; else out.rules = e.rules;
        } else {
          if (!rows.length) return { error: `${out.host}: add at least one rule, such as GET /** Allow` };
          out.rules = [];
          for (const row of rows) {
            const p = row.path.value.trim();
            if (!p.startsWith('/')) return { error: `${out.host}: a path starts with /, such as /v1/**` };
            const { match, action, ...keep } = row.r;
            out.rules.push({ ...keep, match: row.method.value + ' ' + p, action: row.action() });
          }
        }
        if (secret.value) {
          if (how.value === 'bearer') out.inject = { bearer: secret.value };
          if (how.value === 'header') out.inject = { header: { name: header.value.trim(), secret: secret.value } };
          if (how.value === 'basic') out.inject = { basic_auth: { user: user.value.trim(), secret: secret.value } };
        }
        return { entry: out };
      },
    };
    sync();
    cards.push(c);
    list.append(card);
    if (focus) host.focus();
  };
  initial.forEach(e => add(e));
  const node = el('div.host-editor', null, list,
    readOnly ? (initial.length ? null : el('div.set-empty', null, 'None.'))
      : el('button.btn.sm.host-new', { type: 'button', onclick: () => { add({ mode: 'mitm' }, true); changed() } }, svg(ICON.plus), 'Add a host'));
  return {
    node, add: e => add(e),
    typed: () => cards.some(c => c.card.querySelector('.host-name').value.trim()),
    dirty: () => removed || cards.length !== initial.length || cards.some(c => c.dirty()),
    read() {
      const entries = [];
      for (const c of cards) {
        const r = c.read();
        if (r.error) return r;
        entries.push(r.entry);
      }
      return { entries };
    },
  };
}

// --- review and save -----------------------------------------------------------------------

function reach(r, tone) {
  return el('div.rv-item.' + tone, null,
    el('div.rv-target', null, el('b.mono', null, r.target), r.bundle ? el('span.tag', null, r.bundle) : null),
    el('div.rv-how', null, r.how),
    (r.rules || []).length ? el('ul.rv-rules', null, r.rules.map(x => el('li.mono', null, x))) : null);
}

// reviewSheet shows what a change does to each project's reach, as the
// broker compiles it, before it's saved.
function reviewSheet(title, r, none, save, done) {
  const ok = el('button.btn.primary', {
    type: 'button', onclick: async () => {
      ok.disabled = true;
      try { await save(); closeSheet(); toast('Saved. It applies when Skybreaker restarts, once no agent is working.'); done() } catch (e) { toast(e.message, 'error'); ok.disabled = false }
    },
  }, 'Save');
  const body = el('div.rv', null,
    r.review.length ? r.review.map(p => el('div.rv-project', null,
      p.added.length ? [el('p.rv-head', null, el('b', null, p.project), ' can now reach'), p.added.map(x => reach(x, 'add'))] : null,
      p.removed.length ? [el('p.rv-head', null, el('b', null, p.project), ' no longer reaches'), p.removed.map(x => reach(x, 'del'))] : null))
      : el('p.np-hint', null, none),
    el('div.set-acts', null, el('span.grow'), el('button.btn.ghost', { type: 'button', onclick: closeSheet }, 'Back'), ok));
  settingsSheet(title, 'Review', body);
}

function settingsSheet(title, sub, ...body) {
  $('#sheet-root').replaceChildren(
    el('div.backdrop', { onclick: closeSheet }),
    el('div.picker.set-sheet', { role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
      el('div.picker-grip'),
      el('div.set-sheet-head', null, el('div', null, el('h2', null, title), sub ? el('span', null, sub) : null),
        el('button.icon-btn.bare', { type: 'button', 'aria-label': 'Close', title: 'Close', onclick: closeSheet }, svg(ICON.x))),
      ...body));
}

// saved redraws the page, which says when the change applies.
function saved() {
  toast('Saved. It applies when Skybreaker restarts, once no agent is working.');
  go();
}
