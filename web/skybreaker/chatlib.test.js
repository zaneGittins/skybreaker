'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { mdBlocks, mdInline, safeHref, previewURL, parseCommand, commandHints, CHAT_COLORS, diffFiles, newChat, foldChat, sessionTone, inboxGroups, currentModel, contextUse, fmtTokens } = require('./chatlib.js');

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
  const g = inboxGroups(s, [{ session: 'e' }]);
  // Waiting on you comes first, then problems, then finished work; newest first within each.
  assert.deepEqual(g.forYou.map(x => x.id), ['a', 'e', 'g', 'c']);
  assert.deepEqual(g.working.map(x => x.id), ['b']);
  assert.deepEqual(g.recent.map(x => x.id), ['d']);
  assert.deepEqual(g.ended.map(x => x.id), ['f']);
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
  assert.deepEqual(parseCommand('  /color blue  '), { cmd: 'color', arg: 'blue' });
  assert.deepEqual(parseCommand('/color'), { cmd: 'color', arg: '' });
  assert.deepEqual(parseCommand('/goal Ship the fix\nwith tests'), { cmd: 'goal', arg: 'Ship the fix\nwith tests' });
  assert.deepEqual(parseCommand('/goal'), { cmd: 'goal', arg: '' });
  for (const c of ['clear', 'fork', 'compact']) {
    assert.deepEqual(parseCommand('/' + c), { cmd: c, arg: '' });
    assert.deepEqual(parseCommand(' /' + c + ' '), { cmd: c, arg: '' });
  }
  for (const s of ['/model opus', '/renamed x', '/Rename x', 'please /rename x', '/clearly', '/forks', '/', 'hello', '']) {
    assert.equal(parseCommand(s), null, s);
  }
});

test('commandHints: what to suggest as you type', () => {
  assert.deepEqual(commandHints('/').map(h => h.cmd), ['rename', 'color', 'goal', 'clear', 'fork', 'compact']);
  assert.deepEqual(commandHints('/co').map(h => h.cmd), ['color', 'compact']);
  assert.deepEqual(commandHints('/f').map(h => h.cmd), ['fork']);
  assert.deepEqual(commandHints('/color ').map(h => h.color), CHAT_COLORS);
  assert.deepEqual(commandHints('/color b').map(h => h.color), ['blue']);
  assert.deepEqual(commandHints('/rename x'), []);
  assert.deepEqual(commandHints('/model'), []);
  assert.deepEqual(commandHints('hello'), []);
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
