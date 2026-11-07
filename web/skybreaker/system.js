'use strict';

const systemState = { snapshot: null, error: '', storage: new Map() };

function systemBytes(n) {
  if (!Number.isFinite(n)) return 'Unavailable';
  if (n === 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const i = Math.min(units.length - 1, Math.floor(Math.log(Math.max(1, n)) / Math.log(1024)));
  return (n / 1024 ** i).toLocaleString(undefined, { maximumFractionDigits: i > 1 ? 1 : 0 }) + ' ' + units[i];
}

function systemResources(d) {
  const capacity = (key, label, detail) => {
    const c = d?.[key];
    return { key, label, value: c ? systemBytes(c.used) : 'Unavailable',
      percent: c?.total ? 100 * c.used / c.total : null, detail: c ? detail(c) : 'Waiting for a measurement' };
  };
  return [
    { key: 'cpu', label: 'CPU', value: d?.cpu == null ? 'Sampling' : Math.round(d.cpu) + '%', percent: d?.cpu,
      detail: 'Across all host cores' },
    capacity('memory', 'Memory', c => systemBytes(c.available) + ' available of ' + systemBytes(c.total)),
    capacity('disk', 'Disk', c => systemBytes(c.available) + ' available of ' + systemBytes(c.total)),
  ];
}

function systemMeter(r) {
  return el('progress.system-meter.' + r.key + (r.percent >= 90 ? '.high' : ''), {
    max: 100, value: r.percent == null ? null : Math.max(0, Math.min(100, r.percent)), 'aria-label': r.label + ' usage',
  });
}

function systemSummary(phone = false) {
  if (!state.meta?.admin) return null;
  const link = el('a.system-summary' + (phone ? '.phone-only' : ''), {
    href: '/settings/system', 'data-link': true, 'data-system-summary': true, 'aria-label': 'Host resources. Open System settings',
  });
  paintSystemSummary(link);
  return link;
}

function paintSystemSummary(link) {
  if (systemState.error) {
    link.replaceChildren(el('span.system-summary-unavailable', null, 'System', el('span', null, 'Usage unavailable')));
    return;
  }
  link.replaceChildren(...systemResources(systemState.snapshot).map(r => el('span.system-mini', null,
    el('span.system-mini-head', null, el('span', null, r.label === 'Memory' ? 'RAM' : r.label),
      el('b', null, r.percent == null ? '···' : Math.round(r.percent) + '%')), systemMeter(r))));
}

function paintSystemMetrics(root) {
  const d = systemState.snapshot;
  root.replaceChildren(...[el('div.system-cards', null, systemResources(d).map(r =>
    el('div.system-card.' + r.key, null, el('span.system-card-label', null, r.label),
      el('strong.system-card-value', null, r.value), systemMeter(r), el('span.system-card-detail', null, r.detail)))),
    el('p.system-sampling', null, systemState.error || (d ? 'Updated ' + ago(d.sampled_at) + '. Host CPU and memory include all processes; disk is the filesystem holding Skybreaker data.' : 'Measuring host resources…')),
    d?.errors?.length ? el('p.system-warning', { role: 'status' }, d.errors.join('. ')) : null].filter(Boolean));
}

async function pollSystem() {
  if (!document.hidden) {
    try {
      systemState.snapshot = await api('GET', '/api/system');
      systemState.error = '';
    } catch (e) {
      systemState.snapshot = null;
      systemState.error = 'Resource usage is unavailable. Retrying shortly.';
    }
    document.querySelectorAll('[data-system-summary]').forEach(paintSystemSummary);
    document.querySelectorAll('[data-system-metrics]').forEach(paintSystemMetrics);
  }
  setTimeout(pollSystem, 5000);
}

function startSystemMonitor() {
  if (!state.meta?.admin) return;
  $('#side-system').append(systemSummary());
  pollSystem();
}

function systemSection(c, projects) {
  if (!state.meta?.admin) return setSection('System', 'Only admins can view host resources.');
  const metrics = el('div', { 'data-system-metrics': true });
  paintSystemMetrics(metrics);
  const rows = projects.map(p => systemStorageRow(p, c.ed));
  const list = el('div.system-projects', null, rows.map(r => r.node));
  const scan = el('button.btn.sm', { type: 'button' }, svg(ICON.find), 'Scan storage');
  const note = el('p.system-storage-note', { role: 'status' }, 'Storage is measured inside running sandboxes. Stopped sandboxes are not started to scan them.');
  const load = async () => {
    scan.disabled = true;
    scan.textContent = 'Scanning…';
    const queue = rows.filter(r => r.running);
    let completed = 0;
    const worker = async () => {
      while (queue.length && list.isConnected) {
        const row = queue.shift();
        await row.scan();
        completed++;
        note.textContent = 'Scanned ' + completed + ' of ' + rows.filter(r => r.running).length + ' running sandboxes. Measurements are cached for one minute.';
      }
    };
    await Promise.all([worker(), worker()]);
    scan.disabled = false;
    scan.textContent = 'Scan storage';
    if (!completed) note.textContent = 'No running sandboxes to scan. Start a project to inspect its storage.';
  };
  scan.addEventListener('click', load);
  // Let the page mount before the bounded scan queue begins.
  requestAnimationFrame(() => { if (list.isConnected) load() });
  return setSection('System', 'Keep an eye on your server and clear the build caches agents leave behind.', null,
    metrics,
    el('div.system-storage-head', null, el('div', null, el('h3', null, 'Storage by project'), el('p', null, 'Find where the space went.')), scan),
    rows.length ? list : el('div.set-empty', null, 'Projects will appear here once you add one.'), note,
    el('div.system-policy', null, svg(ICON.shield), el('div', null, el('b', null, 'Cleanup waits for a quiet project'),
      el('p', null, 'No active terminal, SSH connection, setup or scheduled job, and chats quiet for 15 minutes. Workspaces, worktrees, volumes, module downloads and agent history are kept. Builds may take longer afterward.'))));
}

function systemStorageRow(project, editable) {
  let data = systemState.storage.get(project.name);
  const total = el('b.system-project-total', null, data ? systemBytes(data.total) : project.running ? 'Not scanned' : 'Stopped');
  const status = el('span.system-project-status', null, project.running ? 'Running' : 'Start the sandbox to scan');
  const breakdown = el('div.system-breakdown');
  const button = el('button.btn.sm', { type: 'button', disabled: !editable || !project.running || !data,
    onclick: () => reviewSystemCleanup(project, data, async result => {
      if (result.after) { data = result.after; systemState.storage.set(project.name, data); paint() }
      else { data = null; systemState.storage.delete(project.name); paint() }
    }) }, 'Review cleanup');
  const paint = () => {
    total.textContent = data ? systemBytes(data.total) : project.running ? 'Unavailable' : 'Stopped';
    button.disabled = !editable || !project.running || !data;
    if (!data) { breakdown.replaceChildren(); return }
    const categories = [['Go build cache', 'go_cache'], ['Docker', 'docker'], ['Workspace', 'workspace'], ['Worktrees', 'worktrees'], ['Other home files', 'home']];
    breakdown.replaceChildren(...categories.map(([label, key]) => el('div.system-category.' + key, null,
      el('span', null, label), el('progress', { max: Math.max(1, data.total), value: data[key], 'aria-label': label + ' storage' }), el('b', null, systemBytes(data[key])))),
      el('p.system-scan-time', null, 'Scanned ' + ago(data.scanned_at) + '. Docker includes images, build cache and volumes; only eligible caches are cleaned.'));
  };
  const node = el('details.system-project', null,
    el('summary', null, el('span.proj.tint-' + project.tint, null, el('span.pdot'), el('b', null, project.name)), total, svg(ICON.chevron, 'chev')),
    el('div.system-project-body', null, breakdown, el('div.system-project-actions', null, status, button)));
  paint();
  return { node, running: project.running, scan: async () => {
    status.textContent = 'Measuring storage…';
    total.textContent = 'Scanning…';
    try {
      data = await api('GET', '/api/projects/' + project.name + '/storage');
      systemState.storage.set(project.name, data);
      status.textContent = 'Ready to review';
    } catch (e) {
      data = null;
      systemState.storage.delete(project.name);
      status.textContent = e.message;
    }
    paint();
  } };
}

function reviewSystemCleanup(project, data, done) {
  if (!data) return;
  const goCache = el('input', { type: 'checkbox', checked: data.go_cache > 0 });
  const docker = el('input', { type: 'checkbox', checked: true });
  const days = el('input.system-days', { type: 'number', min: 1, max: 365, value: 7, required: true, 'aria-label': 'Minimum Docker cache age in days' });
  const status = el('p.system-clean-status', { role: 'status' });
  const submit = el('button.btn.primary', { type: 'submit' }, 'Clean selected caches');
  const form = el('form.system-clean-form', { onsubmit: async e => {
    e.preventDefault();
    if (submit.disabled) return;
    if (!goCache.checked && !docker.checked) { status.textContent = 'Choose at least one cache to clean.'; return }
    submit.disabled = true;
    goCache.disabled = docker.disabled = days.disabled = true;
    status.textContent = 'Checking the project and clearing eligible caches…';
    try {
      const result = await api('POST', '/api/projects/' + project.name + '/cleanup', { go: goCache.checked, docker: docker.checked, older_than_days: Number(days.value) });
      done(result);
      const message = result.after ? systemBytes(result.recovered) + ' recovered.' : 'Recovered space could not be measured.';
      status.textContent = message + (result.error ? ' ' + result.error : ' Your next build will regenerate any caches it needs.');
      if (!form.isConnected) toast(project.name + ': ' + message + (result.error ? ' ' + result.error : ''), result.error ? 'error' : '');
      submit.textContent = 'Done';
      submit.type = 'button';
      submit.addEventListener('click', closeSheet, { once: true });
    } catch (e) {
      status.textContent = e.message;
      goCache.disabled = docker.disabled = days.disabled = false;
    } finally { submit.disabled = false }
  } },
    el('p.system-clean-intro', null, 'Clear regenerable caches in ', el('b', null, project.name), '. The server checks again that the project is quiet before it starts.'),
    el('label.system-clean-choice', null, goCache, el('span', null, el('b', null, 'Go build cache'), el('span', null, 'About ' + systemBytes(data.go_cache) + '. Module downloads are kept.'))),
    el('label.system-clean-choice', null, docker, el('span', null, el('b', null, 'Docker build cache and dangling images'), el('span', null, 'Eligible items only. Reclaimable space is known after cleanup; volumes and containers are kept.'))),
    el('label.system-age', null, 'Docker items older than', days, 'days'),
    el('p.system-clean-note', null, 'Source files and agent history are kept. Builds may take longer afterward.'), status,
    el('div.system-clean-actions', null, el('button.btn', { type: 'button', onclick: closeSheet }, 'Cancel'), submit));
  settingsSheet('Review cleanup', project.name, form);
  goCache.focus();
}
