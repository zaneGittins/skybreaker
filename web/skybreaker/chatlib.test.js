'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { mdBlocks, mdInline, safeHref, previewURL, parseCommand, commandHints, promptText, CHAT_COLORS, diffFiles, newChat, foldChat, sessionTone, inboxGroups, resting, closable, openCount, currentModel, currentEffort, effortChoice, effortName, modelLine, contextUse, fmtTokens, ATTACH, scalable, attachProblem, readProblem, thumbSize, fitSize, fmtSize, composerAction, backTo } = require('./chatlib.js');

test('mdBlocks: headings, paragraphs, rules, quotes', () => {
  const b = mdBlocks('# Title\n\nOne line\nnext line\n\n---\n> quoted\n> more');
  assert.deepEqual(b.map(x => x.t), ['h', 'p', 'hr', 'quote']);
  assert.equal(b[0].level, 1);
  assert.equal(b[0].text, 'Title');
  assert.equal(b[1].text, 'One line\nnext line');
  assert.equal(b[3].blocks[0].text, 'quoted\nmore');
});

test('mdBlocks: fenced code keeps its text exactly', () => {
  const b = mdBlocks('Run:\n```bash\nmake test\n  echo "**not bold**"\n```\nafter');
  assert.deepEqual(b.map(x => x.t), ['p', 'code', 'p']);
  assert.equal(b[1].lang, 'bash');
  assert.equal(b[1].text, 'make test\n  echo "**not bold**"');
  // An unclosed fence runs to the end, as it does while streaming.
  const open = mdBlocks('```go\nfunc main() {');
  assert.equal(open[0].t, 'code');
  assert.equal(open[0].text, 'func main() {');
});

test('mdBlocks: lists, nesting, tasks and numbering', () => {
  const b = mdBlocks('- one\n- two\n  - nested\n  - more\n- [x] done\n- [ ] todo\n\n3. three\n4. four');
  assert.equal(b[0].t, 'ul');
  assert.equal(b[0].items.length, 4);
  assert.equal(b[0].items[1].text, 'two');
  assert.equal(b[0].items[1].children[0].t, 'ul');
  assert.equal(b[0].items[1].children[0].items[1].text, 'more');
  assert.equal(b[0].items[2].check, true);
  assert.equal(b[0].items[3].check, false);
  assert.equal(b[1].t, 'ol');
  assert.equal(b[1].start, 3);
  // A code block inside a list item stays code.
  const c = mdBlocks('1. Do this:\n   ```\n   x = 1\n   ```\n2. Then');
  assert.equal(c[0].items[0].children[0].t, 'code');
  assert.equal(c[0].items[0].children[0].text, 'x = 1');
  assert.equal(c[0].items.length, 2);
});

test('mdBlocks: tables', () => {
  const b = mdBlocks('| File | Lines |\n|:-----|------:|\n| a.go | 12 |\n| b\\|c.go | 3 |');
  assert.equal(b[0].t, 'table');
  assert.deepEqual(b[0].head, ['File', 'Lines']);
  assert.deepEqual(b[0].align, ['left', 'right']);
  assert.deepEqual(b[0].rows, [['a.go', '12'], ['b|c.go', '3']]);
});

test('mdInline: emphasis, code, links', () => {
  const r = mdInline('Use **bold**, *it*, `x*y*`, ~~old~~ and [docs](https://example.com/a?b=1).');
  const kinds = r.map(x => x.t);
  assert.deepEqual(kinds, ['text', 'b', 'text', 'i', 'text', 'code', 'text', 's', 'text', 'a', 'text']);
  assert.equal(r[5].text, 'x*y*');
  assert.equal(r[9].href, 'https://example.com/a?b=1');
  assert.equal(r[9].children[0].text, 'docs');
});

test('mdInline: snake_case and lone stars stay text', () => {
  const r = mdInline('call my_func_name with 2 * 3 * 4');
  assert.deepEqual(r, [{ t: 'text', text: 'call my_func_name with 2 * 3 * 4' }]);
});

test('mdInline: bare URLs link, unsafe ones never do', () => {
  const r = mdInline('see https://github.com/a/b/pull/7. ok');
  assert.equal(r[1].t, 'a');
  assert.equal(r[1].href, 'https://github.com/a/b/pull/7');
  assert.equal(r[2].text, '. ok');
  for (const bad of ['[x](javascript:alert(1))', '[x](data:text/html,hi)', '[x](//evil.com)', '[x](vbscript:x)']) {
    assert.ok(!mdInline(bad).some(n => n.t === 'a'), bad);
  }
  assert.equal(safeHref(' JAVASCRIPT:alert(1)'), null);
  assert.equal(safeHref('mailto:a@b.c'), 'mailto:a@b.c');
});

test('mdInline: newlines and escapes', () => {
  const r = mdInline('a\nb \\*not\\*');
  assert.deepEqual(r.map(x => x.t), ['text', 'br', 'text']);
  assert.equal(r[2].text, 'b *not*');
});

test('mdInline: long text stays fast', () => {
  const s = 'word '.repeat(40000) + '*';
  const t0 = Date.now();
  mdInline(s);
  mdBlocks(('line with `code` and **bold**\n').repeat(5000));
  assert.ok(Date.now() - t0 < 1500, 'took ' + (Date.now() - t0) + 'ms');
});

test('diffFiles: files, hunks, numbers', () => {
  const d = [
    'diff --git a/a.go b/a.go', 'index 1..2 100644', '--- a/a.go', '+++ b/a.go',
    '@@ -1,3 +1,3 @@ func x', ' keep', '--- removed line that looks like a header', '+added', ' end',
    'diff --git a/new.txt b/new.txt', 'new file mode 100644', '--- /dev/null', '+++ b/new.txt', '@@ -0,0 +1,2 @@', '+one', '+two',
  ].join('\n');
  const f = diffFiles(d);
  assert.equal(f.length, 2);
  assert.equal(f[0].path, 'a.go');
  assert.equal(f[0].added, 1);
  assert.equal(f[0].removed, 1);
  const lines = f[0].hunks[0].lines;
  assert.deepEqual(lines.map(l => l.k), ['ctx', 'del', 'add', 'ctx']);
  assert.equal(lines[1].text, '-- removed line that looks like a header');
  assert.equal(lines[2].n, 2);
  assert.equal(lines[3].o, 3);
  assert.equal(f[1].path, 'new.txt');
  assert.equal(f[1].isNew, true);
  assert.equal(f[1].added, 2);
  // Skybreaker's own per-file diffs have no "diff --git" line.
  const g = diffFiles('--- a/x.go\n+++ b/x.go\n@@ -1,1 +1,1 @@\n-a\n+b\n--- a/y.go\n+++ b/y.go\n@@ -1,1 +1,1 @@\n-c\n+d\n');
  assert.deepEqual(g.map(x => x.path), ['x.go', 'y.go']);
});

test('foldChat: a turn streams, then settles', () => {
  const c = newChat();
  foldChat(c, { seq: 1, kind: 'user', id: 'u1', text: 'hi' });
  foldChat(c, { seq: 2, kind: 'turn', turn: { state: 'working' } });
  assert.equal(c.working, true);
  foldChat(c, { kind: 'delta', id: 'm:0', text: 'Hel' });
  const r = foldChat(c, { kind: 'delta', id: 'm:0', text: 'lo' });
  assert.equal(r.changed.text, 'Hello');
  assert.equal(r.changed.streaming, true);
  foldChat(c, { seq: 3, kind: 'text', id: 'm:0', text: 'Hello!' });
  assert.equal(c.byId.get('m:0').text, 'Hello!');
  assert.equal(c.byId.get('m:0').streaming, false);
  foldChat(c, { seq: 4, kind: 'turn', turn: { state: 'done' } });
  assert.equal(c.working, false);
  assert.deepEqual(c.items.map(i => i.type), ['user', 'text']);
  // A replayed event is ignored.
  assert.equal(foldChat(c, { seq: 3, kind: 'text', id: 'x', text: 'dup' }), null);
  assert.equal(c.lastUser, 'hi');
});

test('foldChat: tools update in place and nest under their agent', () => {
  const c = newChat();
  foldChat(c, { seq: 1, kind: 'tool', id: 't1', tool: { name: 'Task', kind: 'agent', title: 'explore', status: 'running' } });
  foldChat(c, { seq: 2, kind: 'tool', id: 't2', tool: { name: 'Grep', kind: 'search', title: 'x', status: 'running', parent: 't1' } });
  const r = foldChat(c, { seq: 3, kind: 'tool', id: 't2', tool: { name: 'Grep', kind: 'search', title: 'x', status: 'done', parent: 't1' } });
  assert.equal(c.items.length, 1);
  assert.equal(r.changed.id, 't1');
  assert.equal(c.items[0].children.length, 1);
  assert.equal(c.items[0].children[0].tool.status, 'done');
});

test('foldChat: asks open and close; failures and logins show', () => {
  const c = newChat();
  foldChat(c, { seq: 1, kind: 'ask', id: 'r1', ask: { kind: 'permission', title: 'Run make' } });
  assert.equal(c.asks.size, 1);
  foldChat(c, { seq: 2, kind: 'answered', id: 'r1', text: 'allowed' });
  assert.equal(c.asks.size, 0);
  assert.equal(c.byId.get('r1').state, 'allowed');
  foldChat(c, { seq: 3, kind: 'login', login: { needed: true, terminal: true } });
  assert.equal(c.login.needed, true);
  foldChat(c, { seq: 4, kind: 'turn', turn: { state: 'failed', error: 'Not logged in' } });
  assert.equal(c.items.at(-1).type, 'error');
  foldChat(c, { seq: 5, kind: 'login', login: { done: true } });
  assert.equal(c.login, null);
  assert.equal(c.items.at(-1).type, 'notice');
  foldChat(c, { seq: 6, kind: 'info', info: { model: 'm1', mode: 'ask' } });
  assert.equal(c.info.model, 'm1');
});

test('sessionTone and inboxGroups', () => {
  const t0 = '2026-10-18T10:00:00Z', t1 = '2026-10-18T11:00:00Z';
  const s = [
    { id: 'a', mode: 'chat', status: 'running', state: 'waiting', note: 'Run make', updated: t0 },
    { id: 'b', mode: 'chat', status: 'running', state: 'working', updated: t1 },
    { id: 'c', mode: 'chat', status: 'running', state: 'idle', updated: t1, seen: t0 },
    { id: 'd', mode: 'chat', status: 'running', state: 'idle', updated: t0, seen: t1 },
    { id: 'e', status: 'running', created: t0 },
    { id: 'f', status: 'killed', created: t0, ended: t1 },
    { id: 'g', mode: 'chat', status: 'running', state: 'failed', note: 'boom', updated: t0 },
  ];
  assert.deepEqual(sessionTone(s[0]), { tone: 'wait', label: 'Run make' });
  assert.equal(sessionTone(s[2]).label, 'Finished');
  assert.equal(sessionTone(s[3]).tone, 'idle');
  assert.equal(sessionTone(s[4]).tone, 'live');
  assert.equal(sessionTone(s[5]).tone, 'ended');
  const g = inboxGroups(s, [{ session: 'e' }], Date.parse(t1));
  // Waiting on you comes first, then problems, then finished work; newest first within each.
  assert.deepEqual(g.forYou.map(x => x.id), ['a', 'e', 'g', 'c']);
  assert.deepEqual(g.working.map(x => x.id), ['b']);
  assert.deepEqual(g.recent.map(x => x.id), ['d']);
  assert.deepEqual(g.ended.map(x => x.id), ['f']);
  // A day on, the idle chat you've seen rests with the ended ones; the rest stay.
  const later = inboxGroups(s, [{ session: 'e' }], Date.parse(t0) + 25 * 3600e3);
  assert.deepEqual(later.recent.map(x => x.id), []);
  assert.deepEqual(later.ended.map(x => x.id), ['f', 'd']);
  assert.deepEqual(later.forYou.map(x => x.id), ['a', 'e', 'g', 'c']);
});

test('resting and closable', () => {
  const day = 24 * 3600e3, t0 = '2026-10-18T10:00:00Z', now = Date.parse(t0) + day + 1;
  const idle = { mode: 'chat', status: 'running', state: 'idle', updated: t0, seen: t0 };
  assert.equal(resting(idle, now), true);
  assert.equal(resting(idle, now - 2), false);
  assert.equal(resting({ ...idle, seen: '2026-10-18T09:00:00Z' }, now), false, 'unseen finished work');
  assert.equal(resting({ status: 'running', created: t0 }, now), false, 'a terminal');
  assert.equal(resting({ ...idle, status: 'killed' }, now), false);
  assert.equal(resting(idle, now, new Set(['c'])), true);
  assert.equal(resting({ ...idle, id: 'c' }, now, new Set(['c'])), false, 'an approval waits on it');
  const ss = [idle, { ...idle, updated: '2026-10-19T10:00:00Z' }, { ...idle, project: 'b' }, { ...idle, status: 'killed' }].map(x => ({ project: 'a', ...x }));
  ss[2].project = 'b';
  assert.equal(openCount(ss, 'a', now), 1, 'the resting and the ended aren\'t counted');
  assert.equal(openCount(ss, 'b', now - 2), 1);
  for (const st of ['idle', 'failed']) assert.equal(closable({ ...idle, state: st }), true, st);
  for (const st of ['working', 'waiting', 'login']) assert.equal(closable({ ...idle, state: st }), false, st);
  assert.equal(closable({ ...idle, status: 'killed' }), false);
  assert.equal(closable({ status: 'running', created: t0 }), false, 'a terminal');
});

test('currentModel: chosen, then reported, then the default', () => {
  const ms = [
    { id: 'default', name: 'Default (recommended)', resolved: 'claude-opus-5-5', default: true },
    { id: 'opus', name: 'Opus 5.5', resolved: 'claude-opus-5-5' },
    { id: 'sonnet', name: 'Sonnet 5.5', resolved: 'claude-sonnet-5-5' },
    { id: 'gpt-daybreak-blue-latest', name: 'Daybreak Blue' },
  ];
  assert.equal(currentModel(ms, 'sonnet', 'claude-opus-5-5').name, 'Sonnet 5.5');
  assert.equal(currentModel(ms, '', 'claude-opus-5-5').id, 'default');
  assert.equal(currentModel(ms, '', 'claude-sonnet-5-5').id, 'sonnet');
  assert.equal(currentModel(ms, '', 'gpt-daybreak-blue-latest').name, 'Daybreak Blue');
  assert.equal(currentModel(ms).id, 'default');
  assert.deepEqual(currentModel(ms, '', 'mystery-1'), { id: 'mystery-1', name: 'mystery-1' });
  assert.deepEqual(currentModel([], 'gpt-x'), { id: 'gpt-x', name: 'gpt-x' });
  assert.equal(currentModel([], '', ''), null);
  assert.equal(currentModel(undefined, '', 'm').name, 'm');
});

test('foldChat: a shown file is its own item, even shown twice', () => {
  const c = newChat();
  const sh = { path: 'shot.png', kind: 'image', size: 4 };
  const a = foldChat(c, { seq: 1, kind: 'show', id: 'abc.png', show: sh });
  const b = foldChat(c, { seq: 2, kind: 'show', id: 'abc.png', show: sh });
  assert.equal(a.added.type, 'show');
  assert.equal(a.added.file, 'abc.png');
  assert.deepEqual(a.added.show, sh);
  assert.notEqual(a.added.id, b.added.id);
  assert.equal(c.items.length, 2);
});

test('activityLabel: what the agent is doing right now', () => {
  const { activityLabel } = require('./chatlib.js');
  const c = newChat();
  assert.equal(activityLabel(c), 'Working');
  foldChat(c, { seq: 1, kind: 'thinking', id: 'th', text: 'hmm' });
  c.byId.get('th').streaming = true;
  assert.equal(activityLabel(c), 'Thinking');
  const tool = (seq, id, t, parent) => foldChat(c, { seq, kind: 'tool', id, tool: { status: 'running', parent, ...t } });
  tool(2, 't1', { kind: 'command', title: 'npm test -- --run web' });
  assert.equal(activityLabel(c), 'Running npm test -- --run web');
  tool(3, 't1', { kind: 'command', title: 'npm test', status: 'done' });
  foldChat(c, { seq: 4, kind: 'delta', id: 'm', text: 'All' });
  assert.equal(activityLabel(c), 'Writing');
  tool(5, 't2', { kind: 'edit', title: 'web/skybreaker/chat.js' });
  assert.equal(activityLabel(c), 'Editing chat.js');
  tool(6, 't3', { kind: 'read', title: '/workspace/go.mod' });
  assert.equal(activityLabel(c), 'Reading go.mod');
  // The newest running tool wins, a subagent's step included.
  tool(7, 'a1', { kind: 'agent', title: 'explore the parser' });
  tool(8, 'a2', { kind: 'search', title: 'parseQuery' }, 'a1');
  assert.equal(activityLabel(c), 'Searching for parseQuery');
  tool(9, 'w1', { kind: 'web', title: 'https://go.dev/doc' });
  assert.equal(activityLabel(c), 'Browsing go.dev');
  tool(10, 'p1', { kind: 'mcp', title: 'skybreaker · list_items' });
  assert.equal(activityLabel(c), 'Using skybreaker · list_items');
  // Long commands are cut, and only the first line shows.
  tool(11, 'c2', { kind: 'command', title: 'go test ./... -run TestSomethingExtremelyLongNamed -count=1 -v\nsecond' });
  assert.ok(activityLabel(c).length <= 48 && !activityLabel(c).includes('second'));
});

test('foldChat: a preview is its own item', () => {
  const c = newChat();
  const r = foldChat(c, { seq: 1, kind: 'preview', preview: { port: 5173, url: 'https://sky.example.ts.net:8401', title: 'Settings' } });
  assert.equal(r.added.type, 'preview');
  assert.equal(r.added.preview.port, 5173);
});

test('previewURL: only a bare http(s) origin becomes a link', () => {
  assert.equal(previewURL('https://sky.example.ts.net:8401'), 'https://sky.example.ts.net:8401');
  assert.equal(previewURL('http://127.0.0.1:8402'), 'http://127.0.0.1:8402');
  for (const bad of ['javascript:alert(1)', 'https://sky.example.ts.net:8401/x', 'https://u:p@sky.example.ts.net:8401',
    'data:text/html,x', '//evil.example', 'https://evil.example:8401?x', ' https://a.b:1', '', null, 42]) {
    assert.equal(previewURL(bad), null, String(bad));
  }
});

test('parseCommand: only Skybreaker\'s own commands, everything else goes to the agent', () => {
  assert.deepEqual(parseCommand('/rename Login flow'), { cmd: 'rename', arg: 'Login flow' });
  assert.deepEqual(parseCommand('  /color stormlight  '), { cmd: 'color', arg: 'stormlight' });
  assert.deepEqual(parseCommand('/color'), { cmd: 'color', arg: '' });
  assert.deepEqual(parseCommand('/goal Ship the fix\nwith tests'), { cmd: 'goal', arg: 'Ship the fix\nwith tests' });
  assert.deepEqual(parseCommand('/goal'), { cmd: 'goal', arg: '' });
  assert.deepEqual(parseCommand('/prompt review focus on auth'), { cmd: 'prompt', arg: 'review focus on auth' });
  assert.deepEqual(parseCommand('/prompt'), { cmd: 'prompt', arg: '' });
  for (const c of ['clear', 'fork', 'compact']) {
    assert.deepEqual(parseCommand('/' + c), { cmd: c, arg: '' });
    assert.deepEqual(parseCommand(' /' + c + ' '), { cmd: c, arg: '' });
  }
  for (const s of ['/model opus', '/renamed x', '/Rename x', 'please /rename x', '/clearly', '/forks', '/prompts:review', '/prompts', '/', 'hello', '']) {
    assert.equal(parseCommand(s), null, s);
  }
});

test('commandHints: what to suggest as you type', () => {
  assert.deepEqual(commandHints('/').map(h => h.cmd), ['rename', 'color', 'goal', 'clear', 'fork', 'compact', 'prompt']);
  assert.deepEqual(commandHints('/co').map(h => h.cmd), ['color', 'compact']);
  assert.deepEqual(commandHints('/f').map(h => h.cmd), ['fork']);
  assert.deepEqual(commandHints('/color ').map(h => h.color), CHAT_COLORS);
  assert.deepEqual(commandHints('/color s').map(h => h.color), ['sun', 'stormlight']);
  assert.deepEqual(commandHints('/rename x'), []);
  assert.deepEqual(commandHints('/model'), []);
  assert.deepEqual(commandHints('hello'), []);
});

const PROMPTS = [
  { name: 'review', text: 'Review the diff on this branch for bugs, and list them most severe first.\nThen stop.' },
  { name: 'release', text: 'Cut a release.' },
  { name: 'tests', text: 'Run the tests.' },
];

test('commandHints: /prompt offers the saved prompts, like /color offers colours', () => {
  assert.deepEqual(commandHints('/pr', PROMPTS).map(h => h.cmd), ['prompt']);
  assert.deepEqual(commandHints('/prompt ', PROMPTS).map(h => h.prompt), ['review', 'release', 'tests']);
  assert.deepEqual(commandHints('/prompt re', PROMPTS).map(h => h.prompt), ['review', 'release']);
  assert.deepEqual(commandHints('/prompt rev', PROMPTS)[0], { cmd: 'prompt', prompt: 'review', desc: 'Review the diff on this branch for bugs, and li…' });
  assert.deepEqual(commandHints('/prompt x', PROMPTS), []);
  assert.deepEqual(commandHints('/prompt ', []), []);
  // Once you add to it, the hints go.
  assert.deepEqual(commandHints('/prompt review and ', PROMPTS), []);
});

test('commandHints: /effort offers the levels the chat\'s model has', () => {
  const levels = { list: ['low', 'medium', 'high', 'max'], on: 'high' };
  assert.deepEqual(commandHints('/ef', [], levels).map(h => h.cmd), ['effort']);
  // Without levels to choose from there's no /effort to offer.
  assert.deepEqual(commandHints('/ef'), []);
  assert.deepEqual(commandHints('/effort x y', [], levels), []);
  assert.ok(commandHints('/effort ', [], { list: levels.list, on: '' }).every(h => !h.on));
  assert.deepEqual(commandHints('/effort ', [], levels).map(h => h.effort), ['low', 'medium', 'high', 'max']);
  assert.deepEqual(commandHints('/effort m', [], levels), [
    { cmd: 'effort', effort: 'medium', on: false }, { cmd: 'effort', effort: 'max', on: false }]);
  assert.equal(commandHints('/effort h', [], levels)[0].on, true);
  assert.deepEqual(commandHints('/effort ', []), []);
  assert.deepEqual(parseCommand('/effort  max '), { cmd: 'effort', arg: 'max' });
});

test('currentEffort: chosen, then reported, then the model\'s default, among its levels', () => {
  const m = { id: 'gpt-6-sol', efforts: ['low', 'medium', 'high', 'ultra'], default_effort: 'medium' };
  assert.equal(currentEffort(m, 'ultra', 'low'), 'ultra');
  assert.equal(currentEffort(m, '', 'low'), 'low');
  assert.equal(currentEffort(m, '', ''), 'medium');
  // A level the model lacks is passed over.
  assert.equal(currentEffort(m, 'max', 'xhigh'), 'medium');
  assert.equal(currentEffort({ id: 'x', efforts: ['low', 'high'] }, '', ''), '');
  // One level, or none, is nothing to choose.
  assert.equal(currentEffort({ id: 'plain', efforts: ['off'] }, '', 'off'), '');
  assert.equal(currentEffort({ id: 'old' }, 'high', 'high'), '');
  assert.equal(currentEffort(null, 'high'), '');
});

test('effortName: short for the picker, whole for the header', () => {
  assert.equal(effortName('high'), 'High');
  assert.equal(effortName('xhigh'), 'Extra high');
  assert.equal(effortName('xhigh', true), 'XHigh');
  assert.equal(effortName('medium', true), 'Med');
  assert.equal(effortName('medium'), 'Medium');
  assert.equal(effortName('ultra', true), 'Ultra');
  assert.equal(effortName(''), '');
  assert.equal(effortName('minimal', true), 'Min');
  assert.equal(effortName('off'), 'Off');
  assert.equal(effortName('constructor'), 'Constructor');
});

test('effortChoice: the levels to offer and the one on, the same for picker, hints and /effort', () => {
  const m = { id: 'gpt-6-sol', efforts: ['low', 'medium', 'high'] };
  assert.deepEqual(effortChoice(m, true, 'high', 'low'), { list: ['low', 'medium', 'high'], on: 'high' });
  // Levels with none known to be on are still offered, none marked.
  assert.deepEqual(effortChoice(m, true, '', ''), { list: ['low', 'medium', 'high'], on: '' });
  assert.deepEqual(effortChoice({ ...m, default_effort: 'ultra' }, true, '', ''), { list: ['low', 'medium', 'high'], on: '' });
  assert.deepEqual(effortChoice(m, false, 'high'), { list: [], on: '' });
  assert.deepEqual(effortChoice({ id: 'plain', efforts: ['off'] }, true, '', 'off'), { list: [], on: '' });
  assert.deepEqual(effortChoice(null, true), { list: [], on: '' });
});

test('modelLine: the model, and its level when there is one', () => {
  const m = { id: 'opus', name: 'Opus 5.5' };
  assert.equal(modelLine(m, 'xhigh'), 'Opus 5.5 · Extra high');
  assert.equal(modelLine(m, ''), 'Opus 5.5');
  assert.equal(modelLine(null, 'high'), '');
});

test('promptText: the saved prompt, with anything after its name added', () => {
  assert.equal(promptText('tests', PROMPTS), 'Run the tests.');
  assert.equal(promptText('  tests  ', PROMPTS), 'Run the tests.');
  assert.equal(promptText('tests only the api\npackage ', PROMPTS), 'Run the tests.\n\nonly the api\npackage');
  for (const bad of ['', 'test', 'Tests', 'nope tests']) assert.equal(promptText(bad, PROMPTS), null, bad);
  assert.equal(promptText('tests', []), null);
});

test('foldChat: context usage keeps the last window it was told', () => {
  const c = newChat();
  foldChat(c, { seq: 1, kind: 'info', info: { context: { used: 5 } } });
  foldChat(c, { seq: 2, kind: 'info', info: { context: { used: 10, window: 100 } } });
  foldChat(c, { seq: 3, kind: 'info', info: { model: 'm' } });
  foldChat(c, { seq: 4, kind: 'info', info: { context: { used: 3 } } });
  assert.deepEqual(c.info.context, { used: 3, window: 100 });
  assert.equal(c.info.model, 'm');
});

test('contextUse: how full the window is, once the harness has said', () => {
  assert.equal(contextUse({}), null);
  assert.equal(contextUse({ context: { used: 50 } }), null);
  assert.deepEqual(contextUse({ context: { used: 250000, window: 1000000 } }), { used: 250000, window: 1000000, frac: 0.25, pct: 25 });
  assert.equal(contextUse({ context: { used: 2000, window: 1000 } }).frac, 1);
  assert.equal(contextUse({ context: { used: 0, window: 1000 } }).pct, 0);
  // A sliver still shows as 1%, not nothing.
  assert.equal(contextUse({ context: { used: 1, window: 1000000 } }).pct, 1);
  assert.equal(contextUse({ context: { used: -5, window: 100 } }).frac, 0);
});

test('fmtTokens: short counts', () => {
  assert.equal(fmtTokens(950), '950');
  assert.equal(fmtTokens(1500), '1.5k');
  assert.equal(fmtTokens(24181), '24k');
  assert.equal(fmtTokens(258400), '258k');
  assert.equal(fmtTokens(1000000), '1M');
  assert.equal(fmtTokens(1250000), '1.3M');
});

test('mdInline is fast on unmatched markers', () => {
  // A hostile agent's text: every unmatched ** or * rescanned to the end.
  for (const s of ['**a '.repeat(16000), '*a **b '.repeat(6000), '`a ``b '.repeat(6000), '~~a '.repeat(16000)]) {
    const t0 = Date.now();
    mdInline(s);
    assert.ok(Date.now() - t0 < 500, `${s.slice(0, 8)}… took ${Date.now() - t0}ms`);
  }
  // Skipping what can't close changes no parse: these are as before.
  const golden = "{\"** a **b**\": [{\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"* a \"}, {\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"*b\"}]}]}], \"* a *b*\": [{\"t\": \"text\", \"text\": \"* a \"}, {\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"b\"}]}], \"**x **y**\": [{\"t\": \"b\", \"children\": [{\"t\": \"text\", \"text\": \"x **y\"}]}], \"`a ``b `c`\": [{\"t\": \"code\", \"text\": \"a ``b \"}, {\"t\": \"text\", \"text\": \"c`\"}], \"a * b * c\": [{\"t\": \"text\", \"text\": \"a * b * c\"}], \"**bold** and *em* and __u__ and _e_\": [{\"t\": \"b\", \"children\": [{\"t\": \"text\", \"text\": \"bold\"}]}, {\"t\": \"text\", \"text\": \" and \"}, {\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"em\"}]}, {\"t\": \"text\", \"text\": \" and \"}, {\"t\": \"b\", \"children\": [{\"t\": \"text\", \"text\": \"u\"}]}, {\"t\": \"text\", \"text\": \" and \"}, {\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"e\"}]}], \"~~ x ~~y~~\": [{\"t\": \"text\", \"text\": \"~~ x \"}, {\"t\": \"s\", \"children\": [{\"t\": \"text\", \"text\": \"y\"}]}], \"snake_case_name *x*\": [{\"t\": \"text\", \"text\": \"snake_case_name \"}, {\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"x\"}]}], \"``` x ``\": [{\"t\": \"code\", \"text\": \"` x \"}], \"**a *b* c**\": [{\"t\": \"b\", \"children\": [{\"t\": \"text\", \"text\": \"a \"}, {\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"b\"}]}, {\"t\": \"text\", \"text\": \" c\"}]}], \"*a **b** c*\": [{\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"a \"}, {\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"*b\"}]}]}, {\"t\": \"text\", \"text\": \" c*\"}], \"x**y**z\": [{\"t\": \"text\", \"text\": \"x\"}, {\"t\": \"b\", \"children\": [{\"t\": \"text\", \"text\": \"y\"}]}, {\"t\": \"text\", \"text\": \"z\"}], \"_a_b_\": [{\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"a_b\"}]}], \"**\\n**\": [{\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"*\"}, {\"t\": \"br\"}, {\"t\": \"text\", \"text\": \"*\"}]}], \"* *\": [{\"t\": \"text\", \"text\": \"* *\"}], \"****\": [{\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"**\"}]}], \"*a`*`b*\": [{\"t\": \"i\", \"children\": [{\"t\": \"text\", \"text\": \"a`\"}]}, {\"t\": \"text\", \"text\": \"`b*\"}]}";
  for (const [src, want] of Object.entries(JSON.parse(golden))) assert.deepEqual(mdInline(src), want, JSON.stringify(src));
});

test('signInHost', () => {
  const { signInHost } = require('./chatlib.js');
  assert.deepEqual(signInHost('https://auth.openai.com/codex/device'), { host: 'auth.openai.com', known: true });
  assert.deepEqual(signInHost('https://claude.ai/oauth/x'), { host: 'claude.ai', known: true });
  // The link comes from the sandbox's harness: an unknown host is shown as one.
  assert.deepEqual(signInHost('https://auth.openai.com.evil.example/x'), { host: 'auth.openai.com.evil.example', known: false });
  assert.deepEqual(signInHost('https://user@auth.openai.com@evil.example/'), { host: 'evil.example', known: false });
  assert.equal(signInHost('javascript:alert(1)'), null);
});

test('runningAgents: subagents at work, past the turn that started them', () => {
  const { runningAgents, agentActivity } = require('./chatlib.js');
  const c = newChat();
  const tool = (seq, id, t, time) => foldChat(c, { seq, time, kind: 'tool', id, tool: { status: 'running', ...t } });
  foldChat(c, { seq: 1, kind: 'turn', turn: { state: 'working' } });
  tool(2, 'a1', { kind: 'agent', title: 'Nap A' }, '2026-10-20T10:00:00Z');
  tool(3, 'a2', { kind: 'agent', title: 'Nap B' }, '2026-10-20T10:00:05Z');
  foldChat(c, { seq: 4, kind: 'turn', turn: { state: 'done' } });
  assert.equal(c.working, false);
  assert.deepEqual(runningAgents(c).map(x => x.id), ['a1', 'a2']);
  assert.equal(agentActivity(c.byId.get('a1')), 'Starting');
  tool(5, 's1', { kind: 'command', title: 'sleep 12', parent: 'a1' });
  assert.equal(agentActivity(c.byId.get('a1')), 'Running sleep 12');
  tool(6, 's1', { kind: 'command', title: 'sleep 12', parent: 'a1', status: 'done' });
  assert.equal(agentActivity(c.byId.get('a1')), 'Thinking');
  tool(7, 'a1', { kind: 'agent', title: 'Nap A', status: 'done', output: 'a' });
  assert.deepEqual(runningAgents(c).map(x => x.id), ['a2']);
  // A subagent of a subagent counts too.
  tool(8, 'a3', { kind: 'agent', title: 'Deeper', parent: 'a2' }, '2026-10-20T10:00:09Z');
  assert.deepEqual(runningAgents(c).map(x => x.id), ['a2', 'a3']);
  // Given more to do, a finished one runs again, timed from then.
  tool(9, 'a1', { kind: 'agent', title: 'Nap A' }, '2026-10-20T10:01:00Z');
  assert.equal(c.byId.get('a1').since, '2026-10-20T10:01:00Z');
  assert.equal(sessionTone({ mode: 'chat', status: 'running', state: 'working', note: '2 agents working' }).label, '2 agents working');
});

test('modelName: model IDs the way people say them', () => {
  const { modelName } = require('./chatlib.js');
  assert.equal(modelName('claude-opus-5-5'), 'Opus 5.5');
  assert.equal(modelName('claude-sonnet-4-5-20250929'), 'Sonnet 4.5');
  assert.equal(modelName('claude-haiku-4'), 'Haiku 4');
  assert.equal(modelName('claude-opus-5-5[1m]'), 'Opus 5.5');
  assert.equal(modelName('gpt-6-sol'), 'GPT-6 Sol');
  assert.equal(modelName('gpt-5.6-luna'), 'GPT-5.6 Luna');
  assert.equal(modelName('qwen3-coder:30b'), 'qwen3-coder:30b');
  assert.equal(modelName('Opus 5.5'), 'Opus 5.5');
});

test('modelLabel: a default says which model it stands for', () => {
  const { modelLabel } = require('./chatlib.js');
  assert.equal(modelLabel({ id: 'default', name: 'Default (recommended)', description: 'Opus 5.5 · Best for everyday, complex tasks', resolved: 'claude-opus-5-5', default: true }), 'Opus 5.5');
  assert.equal(modelLabel({ id: 'default', name: 'Default (recommended)', description: 'Use the default model.', resolved: 'claude-opus-5-5', default: true }), 'Opus 5.5');
  assert.equal(modelLabel({ id: 'gpt-6-sol', name: 'gpt-6-sol', default: true }), 'GPT-6 Sol');
  assert.equal(modelLabel({ id: 'sonnet', name: 'Sonnet 5.5' }), 'Sonnet 5.5');
  assert.equal(modelLabel(null), '');
});

test('splitTarget: a long target keeps its end', () => {
  const { splitTarget } = require('./chatlib.js');
  assert.deepEqual(splitTarget('PUT api.github.com/repos/zaneGittins/skybreaker/pulls/41/merge'), ['PUT api.github.com/repos/zaneGittins/skybreaker', '/pulls/41/merge']);
  assert.deepEqual(splitTarget('GET example.com/a'), ['GET example.com/a', '']);
  const [head, tail] = splitTarget('POST api.example.com/v1/projects/very-long-project-name-here/deployments');
  assert.equal(head + tail, 'POST api.example.com/v1/projects/very-long-project-name-here/deployments');
  assert.ok(tail.length <= 28 && tail.startsWith('/'));
});

test('foldChat: a tool waits on its approval, then carries on', () => {
  const c = newChat();
  foldChat(c, { seq: 1, kind: 'tool', id: 't1', tool: { kind: 'command', title: 'npm test', status: 'running' } });
  const r = foldChat(c, { seq: 2, kind: 'ask', id: 'a1', ask: { kind: 'permission', title: 'Run npm test', tool_id: 't1' } });
  assert.equal(r.added.type, 'ask');
  assert.equal(r.changed.id, 't1');
  assert.equal(c.byId.get('t1').waiting, 'a1');
  const done = foldChat(c, { seq: 3, kind: 'answered', id: 'a1', text: 'allowed' });
  assert.equal(done.changed.id, 'a1');
  assert.equal(done.also.id, 't1');
  assert.equal(c.byId.get('t1').waiting, undefined);
  // An ask for no known tool changes nothing else.
  assert.equal(foldChat(c, { seq: 4, kind: 'ask', id: 'a2', ask: { kind: 'question', title: 'A question for you' } }).changed, undefined);
});

test('foldChat: a message keeps its attachments, and Try again skips it', () => {
  const c = newChat();
  const att = { name: 'shot.png', kind: 'image', size: 10, kept: 'ab.png', path: '/tmp/attachments/s/ab-shot.png' };
  foldChat(c, { seq: 1, kind: 'user', id: 'u1', text: '', attach: [att, null, { name: 'no kept copy' }, { kept: 'b.png', thumb: 7 }] });
  assert.deepEqual(c.items[0].attach, [{ ...att, thumb: '' }, { kept: 'b.png', thumb: '' }]);
  assert.equal(c.items[0].text, '');
  assert.equal(c.lastUser, '');
  foldChat(c, { seq: 2, kind: 'user', id: 'u2', text: 'plain' });
  assert.deepEqual(c.items[1].attach, []);
  assert.equal(c.lastUser, 'plain');
});

test('attachProblem: count, size and total limits', () => {
  const f = (name, size) => ({ name, size });
  assert.equal(attachProblem([], [f('a', 1)]), '');
  assert.match(attachProblem(Array(ATTACH.files).fill(f('a', 1)), [f('b', 1)]), /at most 10/);
  assert.match(attachProblem([], [f('big.zip', ATTACH.file + 1)]), /big\.zip is over 10 MB/);
  assert.match(attachProblem([], [f('none.txt', 0)]), /none\.txt is empty/);
  assert.match(attachProblem([f('a', 9 << 20), f('b', 9 << 20)], [f('c', 9 << 20)]), /over 25 MB/);
  // A file still being read counts toward the number, not the size.
  assert.match(attachProblem([{ name: 'reading', busy: true }, f('a', 20 << 20)], [f('b', 9 << 20)]), /over 25 MB/);
});

test('readProblem: refuses before reading, letting large images be scaled', () => {
  const f = (name, size, type = '') => ({ name, size, type });
  assert.equal(readProblem([], [f('photo.heic', 30 << 20, 'image/heic')]), '');
  assert.match(readProblem([], [f('movie.mp4', 1 << 30, 'video/mp4')]), /movie\.mp4 is over 10 MB/);
  assert.match(readProblem([], [f('huge.png', 100 << 20, 'image/png')]), /over 64 MB/);
  assert.match(readProblem([], [f('anim.gif', 11 << 20, 'image/gif')]), /over 10 MB/);
  assert.match(readProblem(Array(9).fill(f('a', 1)), [f('b', 1), f('c', 1)]), /at most 10/);
  assert.match(readProblem([], [f('', 0)]), /That file is empty/);
});

test('scalable: raster images only', () => {
  for (const t of ['image/png', 'image/jpeg', 'image/webp', 'image/heic']) assert.equal(scalable(t), true, t);
  for (const t of ['image/gif', 'image/svg+xml', 'text/plain', '', undefined]) assert.equal(scalable(t), false, String(t));
});

test('fitSize and fmtSize', () => {
  assert.deepEqual(fitSize(4032, 3024, 2000), { w: 2000, h: 1500 });
  assert.deepEqual(fitSize(1170, 2532, 2000), { w: 924, h: 2000 });
  assert.deepEqual(fitSize(800, 600, 2000), { w: 800, h: 600 });
  assert.deepEqual(fitSize(10000, 1, 2000), { w: 2000, h: 1 });
  assert.equal(fmtSize(512), '512 B');
  assert.equal(fmtSize(1536), '1.5 KB');
  assert.equal(fmtSize(250 * 1024), '250 KB');
  assert.equal(fmtSize(3.4 * 1024 * 1024), '3.4 MB');
  assert.equal(ATTACH.imageBytes, 3932160); // the server's maxImageInput
});

test('thumbSize: fills a square tile, within bounds', () => {
  assert.deepEqual(thumbSize(2000, 1333), { w: 360, h: 240 });
  assert.deepEqual(thumbSize(924, 2000), { w: 240, h: 519 });
  assert.deepEqual(thumbSize(100, 80), { w: 100, h: 80 });
  assert.deepEqual(thumbSize(10000, 100), { w: 960, h: 10 });
});

test('composerAction: one button that stops, attaches or sends', () => {
  const a = (o = {}) => composerAction({ working: false, text: '', files: 0, ask: false, ...o });
  assert.equal(a(), 'attach');
  assert.equal(a({ text: 'hi' }), 'send');
  assert.equal(a({ files: 1 }), 'send');
  assert.equal(a({ working: true }), 'stop');
  assert.equal(a({ working: true, text: 'more' }), 'send');
  assert.equal(a({ working: true, files: 1 }), 'send');
  // While the agent waits on an answer, files can't go: the button sends words.
  assert.equal(a({ ask: true }), 'send');
  assert.equal(a({ ask: true, working: true }), 'send');
});

test('backTo: steps back only when that lands somewhere sensible', () => {
  // Chat, then its terminal: Back returns to the chat already in history.
  assert.equal(backTo('/s/c1', '/s/c1', '/p/web'), null);
  // A terminal opened directly: the chat replaces it, so no loop builds up.
  assert.equal(backTo('/', '/s/c1', '/p/web'), '/s/c1');
  assert.equal(backTo('', '/s/c1', '/p/web'), '/s/c1');
  // A chat reached in the app goes back the way it came.
  assert.equal(backTo('/p/web', '', '/p/web'), null);
  // A chat opened from a link goes up to its project.
  assert.equal(backTo('', '', '/p/web'), '/p/web');
});
