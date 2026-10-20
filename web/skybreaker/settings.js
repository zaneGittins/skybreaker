'use strict';

// Settings: the GitHub App, secrets, bundles and what each project may
// reach. The config file stays the base and is shown read-only; what's
// changed here is saved in the data directory and applies when Skybreaker restarts,
// once no agent is working. Secret values go in and never come back out.

Object.assign(ICON, {
  gear: ['M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z'],
  key: ['M15 7a4 4 0 1 1-3.5 6L5 19.5V22H2v-3l6.5-6.5A4 4 0 0 1 15 7z'],
  box: ['M21 8l-9-5-9 5 9 5 9-5z', 'M3 8v8l9 5 9-5V8', 'M12 13v8'],
  git: ['M6 3v12', 'M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M18 9a9 9 0 0 1-9 9'],
  trash: ['M4 7h16', 'M10 11v6', 'M14 11v6', 'M6 7l1 13h10l1-13', 'M9 7V4h6v3'],
});

const githubLink = u => typeof u === 'string' && /^https:\/\/github\.com\//.test(u) ? u : null;

async function renderSettings(seq) {
  const d = await api('GET', '/api/settings');
  if (seq !== state.seq) return;
  crumb(null);
  // GitHub sends you back here after creating the App.
  const q = new URLSearchParams(location.search);
  if (q.has('github')) toast('GitHub App created. Next, install it on your repositories.');
  if (q.has('github_error')) toast(q.get('github_error'), 'error');
  if (q.has('github') || q.has('github_error')) history.replaceState(null, '', '/settings');

  const ed = d.editable;
  const head = el('section.inbox-head', null, el('div', null, el('h1', null, 'Settings'),
    el('p', null, 'What your agents can reach, and with which credentials. Your config file is the base, shown here read-only; changes made here add to it.')));
  const banners = [
    d.pending ? el('p.set-banner', null, el('span.spinner'), 'Saved. Skybreaker restarts to apply it once no agent is working.') : null,
    !d.admin ? el('p.set-banner', null, 'Only admins can change settings.')
      : !ed ? el('p.set-banner', null, 'Changes here are off: the config file sets app_settings: false.') : null,
  ];
  $('#app').replaceChildren(el('div.wrap.activity.settings', null, head, banners,
    githubSection(d.github, ed), secretsSection(d.secrets, ed), bundlesSection(d.bundles, ed, d),
    accessSection(d.projects, ed, d)));
}

function setSection(title, note, action, body) {
  return el('section.inbox-sec.set-sec', null,
    el('div.sec-head', null, el('h2', null, title), action),
    note ? el('p.note', null, note) : null, body);
}

const sourceTag = src => el('span.tag' + (src === 'app' ? '.ok' : ''), null, src === 'app' ? 'Added here' : 'Config file');

// --- GitHub ----------------------------------------------------------------------

function githubSection(g, ed) {
  const install = githubLink(g.install_url);
  if (g.state === 'app' || g.state === 'file') {
    const name = g.slug || 'App ' + g.app_id;
    return setSection('GitHub', 'Agents clone and push through the broker with this App\'s short-lived tokens, one repository at a time.', null,
      el('div.row-list', null, el('div.arow.set-row', null, svg(ICON.git, 'set-ic'),
        el('span.set-main', null, el('b.mono', null, name),
          el('span.set-sub', null, [g.owner ? 'owned by ' + g.owner : '', 'App ID ' + g.app_id, g.state === 'file' ? 'from secrets.yaml' : 'created here'].filter(Boolean).join(' · ')),
          g.error ? el('span.set-sub.bad', null, g.error) : null),
        install ? el('a.btn.sm', { href: install, target: '_blank', rel: 'noopener noreferrer' }, svg(ICON.external), 'Install on repositories') : null)));
  }
  if (!g.can_create) {
    return setSection('GitHub', null, null, el('p.note', null, g.error || 'A secret named github_app in the secrets file isn\'t a GitHub App, so one can\'t be created here.'));
  }
  const org = el('input.np-input', { type: 'text', placeholder: 'organization', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-label': 'Organization', disabled: true });
  const mine = el('input', { type: 'radio', name: 'gh-owner', value: 'me', checked: true, onchange: () => { org.disabled = true } });
  const theirs = el('input', { type: 'radio', name: 'gh-owner', value: 'org', onchange: () => { org.disabled = false; org.focus() } });
  const create = el('button.btn.primary', { type: 'submit', disabled: !ed }, svg(ICON.git), 'Create GitHub App');
  const form = el('form.np-form.set-gh', {
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
    el('div.np-group', null,
      el('label.np-check', null, mine, el('span.np-check-text', null, el('b', null, 'My account'))),
      el('label.np-check', null, theirs, el('span.np-check-text', null, el('b', null, 'An organization'), org))),
    el('div', null, create));
  return setSection('GitHub', 'Agents reach GitHub only through the broker, with a GitHub App\'s short-lived tokens. GitHub opens with the App filled in: read and write to contents, pull requests and issues, read metadata, no webhook. Confirm there and you come back here to install it.', null, form);
}

// --- secrets -----------------------------------------------------------------------

function secretsSection(list, ed) {
  const add = ed ? el('button.btn.sm', { type: 'button', onclick: () => secretSheet() }, svg(ICON.plus), 'Add secret') : null;
  const used = s => s.last_used ? 'used ' + ago(s.last_used) : 'not used since Skybreaker started';
  const rows = list.map(s => el('div.arow.set-row', null, svg(ICON.key, 'set-ic'),
    el('span.set-main', null,
      el('span.set-title', null, el('b.mono', null, s.name), sourceTag(s.source)),
      el('span.set-sub.mono', null, 'to ' + s.hosts.join(', ')),
      el('span.set-sub', null, [s.fingerprint ? 'fingerprint ' + s.fingerprint : '', s.added_by ? 'added by ' + s.added_by + ' ' + ago(s.added) : '', used(s)].filter(Boolean).join(' · ')),
      s.used_by.length ? el('span.set-sub', null, 'sent by ' + s.used_by.join(', ')) : null),
    ed && s.source === 'app' ? el('span.set-acts', null,
      el('button.btn.sm.ghost', { type: 'button', onclick: () => secretSheet(s) }, 'Replace'),
      el('button.icon-btn.bare', { type: 'button', 'aria-label': 'Delete ' + s.name, title: 'Delete', onclick: () => deleteSecret(s) }, svg(ICON.trash))) : null));
  return setSection('Secrets', 'API keys and tokens the broker adds to requests. Each goes only to the hosts listed with it, and is never shown again: not here, not to agents.', add,
    rows.length ? el('div.row-list', null, rows) : el('div.empty-note', null, 'No secrets yet.'));
}

async function deleteSecret(s) {
  if (!confirm(`Delete ${s.name}? Anything that sends it stops working.`)) return;
  try { await api('DELETE', '/api/secrets/' + encodeURIComponent(s.name)); saved() } catch (e) { toast(e.message, 'error') }
}

// secretSheet adds a secret, or replaces one's value. Its hosts change only
// with the value: sending a secret somewhere new needs the secret itself.
function secretSheet(old) {
  const field = (label, input, hint) => el('label.np-field', null, el('span.np-label', null, label), input, hint ? el('span.np-hint', null, hint) : null);
  const name = el('input.np-input', { type: 'text', placeholder: 'openai', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', required: true, pattern: '[a-z][a-z0-9_\\-]{0,47}' });
  const value = el('input.np-input', { type: 'password', autocomplete: 'new-password', spellcheck: 'false', required: true, placeholder: old ? 'Paste the new value' : 'Paste the value' });
  const hosts = el('input.np-input', { type: 'text', placeholder: 'api.example.com', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', required: true, value: old ? old.hosts.join(', ') : null });
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const save = el('button.btn.primary', { type: 'submit' }, old ? 'Replace' : 'Add secret');
  const form = el('form.np-form', {
    onsubmit: async e => {
      e.preventDefault();
      error.hidden = true;
      save.disabled = true;
      try {
        const body = { value: value.value, hosts: hostsFromText(hosts.value) };
        if (old) await api('PUT', '/api/secrets/' + encodeURIComponent(old.name), body);
        else await api('POST', '/api/secrets', { name: name.value.trim(), ...body });
        value.value = '';
        closeSheet();
        saved();
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
        save.disabled = false;
      }
    },
  },
    old ? null : field('Name', name, 'Bundles name it to send it: lowercase letters, digits, dashes and underscores.'),
    field('Value', value, old ? 'The old value is replaced. It isn\'t shown again.' : 'It isn\'t shown again.'),
    field('Hosts', hosts, old ? 'Where it may be sent. To change these, paste the value again.' : 'Where it may be sent, exactly: api.example.com, separated by commas.'),
    error, save);
  settingsSheet(old ? 'Replace ' + old.name : 'Add a secret', null, form);
  (old ? value : name).focus();
}

// --- bundles -------------------------------------------------------------------------

function bundlesSection(list, ed, d) {
  const add = ed ? el('button.btn.sm', { type: 'button', onclick: () => bundleSheet(null, d) }, svg(ICON.plus), 'New bundle') : null;
  const rows = list.map(b => el('div.arow.set-row', null, svg(ICON.box, 'set-ic'),
    el('span.set-main', null,
      el('span.set-title', null, el('b.mono', null, b.name), sourceTag(b.source)),
      el('span.set-sub.mono', null, b.entries.map(e => e.host || e.route).join(', ')),
      el('span.set-sub', null, b.used_by.length ? 'used by ' + b.used_by.join(', ') : 'no project uses it')),
    ed && b.source === 'app' ? el('span.set-acts', null,
      el('button.btn.sm.ghost', { type: 'button', onclick: () => bundleSheet(b, d) }, 'Edit'),
      el('button.icon-btn.bare', { type: 'button', 'aria-label': 'Delete ' + b.name, title: 'Delete', onclick: () => deleteBundle(b) }, svg(ICON.trash))) : null));
  return setSection('Bundles', 'Named sets of hosts that projects take, like github or claude-login.', add,
    rows.length ? el('div.row-list', null, rows) : el('div.empty-note', null, 'No bundles yet.'));
}

async function deleteBundle(b) {
  if (!confirm(`Delete the ${b.name} bundle?`)) return;
  try { await api('DELETE', '/api/bundles/' + encodeURIComponent(b.name)); saved() } catch (e) { toast(e.message, 'error') }
}

function bundleSheet(old, d) {
  const name = el('input.np-input', { type: 'text', placeholder: 'openai', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', required: true });
  const entries = entriesEditor(old ? old.entries : [], d.secrets);
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const review = el('button.btn.primary', { type: 'submit' }, 'Review');
  const bundleName = () => old ? old.name : name.value.trim();
  const form = el('form.np-form', {
    onsubmit: async e => {
      e.preventDefault();
      error.hidden = true;
      const list = entries.read();
      if (list.error) { error.textContent = list.error; error.hidden = false; return }
      const path = '/api/bundles/' + encodeURIComponent(bundleName());
      try {
        const r = await api('PUT', path, { entries: list.entries, dry_run: true });
        reviewSheet('Bundle ' + bundleName(), r, 'No project uses this bundle yet. Give it to one in its access, below.',
          () => api('PUT', path, { entries: list.entries }), () => settingsSheet(old ? 'Edit ' + old.name : 'New bundle', null, form));
      } catch (err) { error.textContent = err.message; error.hidden = false }
    },
  },
    old ? null : el('label.np-field', null, el('span.np-label', null, 'Name'), name, el('span.np-hint', null, 'Lowercase letters, digits, dashes and underscores.')),
    el('div.np-group', null, el('span.np-label', null, 'Hosts'), entries.node),
    error, review);
  settingsSheet(old ? 'Edit ' + old.name : 'New bundle', null, form);
}

// --- project access -----------------------------------------------------------------

function accessSection(list, ed, d) {
  const rows = list.map(p => {
    const project = (state.inbox?.projects || []).find(x => x.name === p.name);
    const own = [...p.file.capabilities || [], ...p.app.capabilities || []];
    const extra = (p.app.capabilities || []).length + (p.app.allow || []).length;
    return el('div.arow.set-row', null,
      el('span.proj.tint-' + (project?.tint ?? 0), null, el('span.pdot')),
      el('span.set-main', null,
        el('span.set-title', null, el('b', null, p.name), p.added ? el('span.tag', null, 'Made here') : null),
        el('span.set-sub', null, plural(p.reaches.length, 'host') + (own.length ? ' · ' + own.join(', ') : '')),
        extra ? el('span.set-sub', null, plural(extra, 'grant') + ' added here') : null),
      el('span.set-acts', null, el('button.btn.sm.ghost', { type: 'button', onclick: () => accessSheet(p, d) }, ed ? 'Edit' : 'View')));
  });
  return setSection('Project access', 'What each project\'s sandbox may reach. Anything else is denied.', null, el('div.row-list', null, rows));
}

function accessSheet(p, d) {
  const ed = d.editable;
  const fileCaps = p.file.capabilities || [];
  const appCaps = p.app.capabilities || [];
  const boxes = d.bundles.filter(b => !fileCaps.includes(b.name)).map(b => el('label.np-check', null,
    el('input', { type: 'checkbox', value: b.name, checked: appCaps.includes(b.name) || null, disabled: !ed || null }),
    el('span.np-check-text', null, el('b', null, b.name), el('span.np-reach', null, b.entries.map(e => e.host || e.route).join(', ')))));
  const entries = entriesEditor(p.app.allow || [], d.secrets, !ed);
  const error = el('p.np-error', { role: 'alert', hidden: true });
  const path = `/api/projects/${encodeURIComponent(p.name)}/access`;
  const body = () => {
    const list = entries.read();
    if (list.error) return list;
    return { capabilities: boxes.map(b => b.querySelector('input')).filter(i => i.checked).map(i => i.value), allow: list.entries };
  };
  const form = el('form.np-form', {
    onsubmit: async e => {
      e.preventDefault();
      error.hidden = true;
      const b = body();
      if (b.error) { error.textContent = b.error; error.hidden = false; return }
      try {
        const r = await api('PUT', path, { ...b, dry_run: true });
        reviewSheet(p.name, r, 'Nothing changes.', () => api('PUT', path, b), () => settingsSheet(p.name, null, form));
      } catch (err) { error.textContent = err.message; error.hidden = false }
    },
  },
    fileCaps.length || (p.file.allow || []).length ? el('div.set-file', null,
      el('span.np-label', null, 'From the config file'),
      fileCaps.length ? el('p.np-hint', null, 'Bundles: ' + fileCaps.join(', ')) : null,
      (p.file.allow || []).length ? el('p.np-hint.mono', null, 'Hosts: ' + p.file.allow.map(e => e.host || e.route).join(', ')) : null) : null,
    el('fieldset.np-group', null, el('legend', null, ed ? 'Bundles added here' : 'Bundles'), boxes.length ? boxes : el('p.np-hint', null, 'Every bundle is already given in the config file.')),
    el('div.np-group', null, el('span.np-label', null, 'Hosts added here'), entries.node),
    error, ed ? el('button.btn.primary', { type: 'submit' }, 'Review') : null);
  settingsSheet(p.name, 'Access', form);
}

// --- the host editor ---------------------------------------------------------------------

// entriesEditor edits a list of hosts: each passed through unread (tunnel)
// or inspected, with rules and a secret to send. The app adds hosts only;
// routes stay in the config file.
function entriesEditor(initial, secrets, readOnly) {
  const list = el('div.ent-list');
  const cards = [];
  const add = e => {
    const host = el('input.np-input', { type: 'text', placeholder: 'api.example.com', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', value: e.host || null, 'aria-label': 'Host', disabled: readOnly || null });
    const mode = el('select.np-input', { 'aria-label': 'How', disabled: readOnly || null },
      el('option', { value: 'mitm', selected: e.mode !== 'tunnel' || null }, 'Inspected: rules apply'),
      el('option', { value: 'tunnel', selected: e.mode === 'tunnel' || null }, 'Tunnel: passed through unread'));
    const text = rulesToText(e);
    const rules = el('textarea.textarea.ent-rules', { rows: 3, spellcheck: 'false', placeholder: 'GET /** auto\nPOST /v1/** ask', 'aria-label': 'Rules', disabled: readOnly || null });
    rules.value = text || 'GET /** auto';
    const inj = e.inject || {};
    const secretName = inj.bearer || inj.header?.secret || inj.basic_auth?.secret || '';
    const secret = el('select.np-input', { 'aria-label': 'Secret to send', disabled: readOnly || null },
      el('option', { value: '' }, 'No secret'),
      secrets.filter(s => s.kind === 'static' || s.name === secretName).map(s => el('option', { value: s.name, selected: s.name === secretName || null }, s.name + ' (to ' + s.hosts.join(', ') + ')')));
    const how = el('select.np-input', { 'aria-label': 'Send it as', disabled: readOnly || null },
      el('option', { value: 'bearer', selected: !!inj.bearer || null }, 'Authorization: Bearer'),
      el('option', { value: 'header', selected: !!inj.header || null }, 'A header'),
      el('option', { value: 'basic', selected: !!inj.basic_auth || null }, 'Basic auth'));
    const header = el('input.np-input', { type: 'text', placeholder: 'X-Api-Key', autocomplete: 'off', spellcheck: 'false', value: inj.header?.name || null, 'aria-label': 'Header name', disabled: readOnly || null });
    const user = el('input.np-input', { type: 'text', placeholder: 'user', autocomplete: 'off', spellcheck: 'false', value: inj.basic_auth?.user || null, 'aria-label': 'User', disabled: readOnly || null });
    const ports = el('input.np-input.ent-ports', { type: 'text', placeholder: '443', inputmode: 'numeric', value: (e.ports || []).join(', ') || null, 'aria-label': 'Ports', disabled: readOnly || null });
    const inspected = el('div.ent-inspected', null,
      el('label.np-field', null, el('span.np-hint', null, 'Rules, one per line: METHOD /path auto, ask or deny. {repo} is the project\'s repo.'), rules),
      el('div.ent-row', null, secret, how, header, user));
    const sync = () => {
      inspected.hidden = mode.value === 'tunnel';
      how.hidden = !secret.value;
      header.hidden = !secret.value || how.value !== 'header';
      user.hidden = !secret.value || how.value !== 'basic';
    };
    [mode, secret, how].forEach(s => s.addEventListener('change', sync));
    const card = el('div.ent-card', null,
      el('div.ent-row', null, host, mode, ports,
        readOnly ? null : el('button.icon-btn.bare', { type: 'button', 'aria-label': 'Remove host', title: 'Remove', onclick: () => { card.remove(); cards.splice(cards.indexOf(c), 1) } }, svg(ICON.x))),
      inspected);
    const c = {
      card, read() {
        const out = { host: host.value.trim().toLowerCase(), mode: mode.value };
        if (!out.host) return { error: 'a host is empty' };
        const ps = ports.value.split(/[\s,]+/).filter(Boolean).map(Number);
        if (ps.some(n => !Number.isInteger(n) || n < 1 || n > 65535)) return { error: `${out.host}: ports are numbers from 1 to 65535` };
        if (ps.length) out.ports = ps;
        if (mode.value === 'tunnel') return { entry: out };
        // Unchanged rules keep what lines can't say (pushed refs, GraphQL).
        if (rules.value === text && (e.rules || e.methods)) {
          if (e.methods) out.methods = e.methods; else out.rules = e.rules;
        } else {
          const r = rulesFromText(rules.value);
          if (r.error) return { error: `${out.host}: ${r.error}` };
          out.rules = r.rules;
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
  };
  initial.forEach(add);
  const node = el('div.ent', null, list,
    readOnly ? (initial.length ? null : el('p.np-hint', null, 'None.'))
      : el('button.btn.sm.ghost.ent-add', { type: 'button', onclick: () => add({ mode: 'mitm' }) }, svg(ICON.plus), 'Add a host'));
  return {
    node, read() {
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
function reviewSheet(title, r, none, save, back) {
  const done = el('button.btn.primary', {
    type: 'button', onclick: async () => {
      done.disabled = true;
      try { await save(); closeSheet(); saved() } catch (e) { toast(e.message, 'error'); done.disabled = false }
    },
  }, 'Save');
  const body = el('div.rv', null,
    r.review.length ? r.review.map(p => el('div.rv-project', null,
      p.added.length ? [el('p.rv-head', null, el('b', null, p.project), ' can now reach'), p.added.map(x => reach(x, 'add'))] : null,
      p.removed.length ? [el('p.rv-head', null, el('b', null, p.project), ' no longer reaches'), p.removed.map(x => reach(x, 'del'))] : null))
      : el('p.np-hint', null, none),
    el('div.rv-acts', null, el('button.btn.ghost', { type: 'button', onclick: back }, 'Back'), done));
  settingsSheet(title, 'Review', body);
}

function settingsSheet(title, sub, ...body) {
  $('#sheet-root').replaceChildren(
    el('div.backdrop', { onclick: closeSheet }),
    el('div.picker.set-sheet', { role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
      el('div.picker-grip'),
      el('div.pv-head', null, el('b', null, title), sub ? el('span', null, sub) : null),
      ...body,
      el('button.btn.ghost.picker-cancel', { type: 'button', onclick: closeSheet }, 'Close')));
}

// saved redraws the page, which says when the change applies.
function saved() {
  toast('Saved. It applies when Skybreaker restarts, once no agent is working.');
  go();
}
