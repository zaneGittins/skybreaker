// Run with: node --test web/shared/   (go test ./web also runs it when node is installed)
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { agentCursor, ago, until, route, samePage, toolSummary, toolInput, reveal, duration, messageBytes, termFont, sgrRuns } = require('./lib.js');

test('ago', () => {
  const now = Date.parse('2026-10-17T12:00:00Z');
  assert.equal(ago('2026-10-17T11:59:50Z', now), 'just now');
  assert.equal(ago('2026-10-17T11:50:00Z', now), '10m ago');
  assert.equal(ago('2026-10-17T09:00:00Z', now), '3h ago');
  assert.equal(ago('2026-10-15T12:00:00Z', now), '2d ago');
  assert.equal(ago('', now), '');
});

test('route', () => {
  assert.deepEqual(route('/'), { name: 'home' });
  assert.deepEqual(route('/p/orbit'), { name: 'project', project: 'orbit', tab: 'chats' });
  assert.deepEqual(route('/s/sabc123'), { name: 'session', id: 'sabc123' });
  assert.deepEqual(route('/r/r20261017020000abcd'), { name: 'run', id: 'r20261017020000abcd' });
  assert.deepEqual(route('/activity'), { name: 'activity' });
  assert.deepEqual(route('/settings'), { name: 'settings' });
  assert.deepEqual(route('/settings/prompts'), { name: 'settings', section: 'prompts' });
  assert.deepEqual(route('/settings/bundles/openai'), { name: 'settings', section: 'bundles', item: 'openai' });
  assert.deepEqual(route('/settings/bundles/new'), { name: 'settings', section: 'bundles', item: 'new' });
  assert.deepEqual(route('/settings/access/orbit'), { name: 'settings', section: 'access', item: 'orbit' });
  assert.deepEqual(route('/settings/nope'), { name: 'notfound' });
  assert.deepEqual(route('/settings/prompts/x'), { name: 'notfound' });
  assert.deepEqual(route('/settings/access/Bad%20Name'), { name: 'notfound' });
  assert.deepEqual(route('/activity/x'), { name: 'notfound' });
  assert.deepEqual(route('/p/Bad%20Name'), { name: 'notfound' });
  assert.deepEqual(route('/s/../../etc'), { name: 'notfound' });
  assert.deepEqual(route('/nope'), { name: 'notfound' });
  assert.deepEqual(route('/p/%E0%A4%A'), { name: 'notfound' }); // malformed escape must not throw
  assert.deepEqual(route('/s/%'), { name: 'notfound' });
  // An assistant's pages.
  assert.deepEqual(route('/a/pip'), { name: 'assistant', project: 'pip', tab: 'today' });
  for (const tab of ['memory', 'routines', 'look', 'browser', 'takeover']) assert.deepEqual(route('/a/pip/' + tab), { name: 'assistant', project: 'pip', tab });
  // Its one thread, where an old link to its chats lands too.
  assert.deepEqual(route('/a/pip/chat'), { name: 'thread', project: 'pip' });
  assert.deepEqual(route('/a/pip/chats'), { name: 'thread', project: 'pip' });
  assert.deepEqual(route('/a/pip/today'), { name: 'notfound' });
  assert.deepEqual(route('/a/pip/board'), { name: 'notfound' });
  assert.deepEqual(route('/a/Bad%20Name'), { name: 'notfound' });
  assert.deepEqual(route('/a/%'), { name: 'notfound' });
});

test('toolSummary', () => {
  assert.equal(toolSummary('Bash', { command: 'make test' }), 'make test');
  assert.equal(toolSummary('Edit', { file_path: '/workspace/a.go', old_string: 'x' }), '/workspace/a.go');
  assert.equal(toolSummary('WebFetch', { url: 'https://example.com' }), 'https://example.com');
  const long = toolSummary('Other', { blob: 'x'.repeat(500) });
  assert.ok(long.length <= 241 && long.endsWith('…'));
  assert.equal(toolSummary('Bash', null), '');
  assert.equal(toolSummary('Bash', { command: 'cat \u202egpj.exe' }), 'cat <U+202E>gpj.exe');
});

test('reveal shows characters that hide or reorder text', () => {
  assert.equal(reveal('a\u202eb\u200b\n\tc\r\x1b'), 'a<U+202E>b<U+200B>\n\tc<U+000D><U+001B>');
  assert.equal(reveal('ok\u{E0041}\ufeff\u2028'), 'ok<U+E0041><U+FEFF><U+2028>');
  assert.equal(reveal('\ud800x'), '<U+D800>x'); // a lone surrogate
  assert.equal(reveal('café 🙂'), 'café 🙂');
  assert.equal(reveal(undefined), '');
});

test('toolInput is the whole input, so the card never hides what is approved', () => {
  const input = { file_path: '/workspace/a.go', content: 'x'.repeat(500) + '\u2066' };
  const s = toolInput(input);
  assert.ok(s.includes('x'.repeat(500)), 'content was cut');
  assert.ok(s.endsWith('<U+2066>"\n}'), s.slice(-20));
  assert.equal(toolInput(null), '');
});

test('duration', () => {
  assert.equal(duration('2026-10-17T02:00:00Z', '2026-10-17T02:03:30Z'), '3m 30s');
  assert.equal(duration('2026-10-17T02:00:00Z', '2026-10-17T03:10:00Z'), '1h 10m');
  assert.equal(duration('2026-10-17T02:00:00Z', ''), '');
});

test('until', () => {
  const now = Date.parse('2026-10-17T12:00:00Z');
  assert.equal(until('2026-10-17T12:04:00Z', now), 'in 4m');
  assert.equal(until('2026-10-17T12:00:20Z', now), 'in under a minute');
  assert.equal(until('2026-10-17T14:00:00Z', now), 'in 2h');
  assert.equal(until('2026-10-17T11:00:00Z', now), 'now');
});

test('messageBytes sends one line as typed and wraps several in a bracketed paste', () => {
  assert.equal(messageBytes('fix the build'), 'fix the build');
  assert.equal(messageBytes('one\ntwo'), '\x1b[200~one\ntwo\x1b[201~');
  assert.equal(messageBytes('one\r\ntwo\r'), '\x1b[200~one\ntwo\n\x1b[201~');
  // The text can't end the paste early and have the rest typed as keys.
  assert.equal(messageBytes('a\nb\x1b[201~\x03'), '\x1b[200~a\nb\x03\x1b[201~');
  assert.equal(messageBytes(''), '');
});

test('termFont keeps a saved size in range', () => {
  assert.equal(termFont(null), 14);
  assert.equal(termFont('17'), 17);
  assert.equal(termFont('3'), 10);
  assert.equal(termFont('99'), 24);
  assert.equal(termFont('junk'), 14);
});


test('sgrRuns splits a row into styled runs', () => {
  assert.deepEqual(sgrRuns('plain'), [{ text: 'plain', style: {} }]);
  assert.deepEqual(sgrRuns(''), []);
  assert.deepEqual(sgrRuns('\x1b[1;31mred\x1b[0m ok'), [
    { text: 'red', style: { bold: true, fg: '#cc0000' } },
    { text: ' ok', style: {} },
  ]);
  // 256 colours, truecolour, background, bright, resets of one attribute.
  assert.deepEqual(sgrRuns('\x1b[38;5;220mA\x1b[38;2;1;2;3mB\x1b[48;5;244;94mC\x1b[39;49mD'), [
    { text: 'A', style: { fg: '#ffd700' } },
    { text: 'B', style: { fg: 'rgb(1,2,3)' } },
    { text: 'C', style: { fg: '#729fcf', bg: '#808080' } },
    { text: 'D', style: {} },
  ]);
  assert.deepEqual(sgrRuns('\x1b[2;3;4;7mx\x1b[22;23;24;27my'), [
    { text: 'x', style: { dim: true, italic: true, underline: true, inverse: true } },
    { text: 'y', style: {} },
  ]);
  // Other escape sequences and controls are dropped, never shown.
  assert.deepEqual(sgrRuns('a\x1b[2Kb\x1b]0;title\x07c\x07d'), [{ text: 'abcd', style: {} }]);
});

test('auditRow', () => {
  const { auditRow } = require('./lib.js');
  assert.deepEqual(auditRow({ decision: 'allow', mode: 'tunnel', method: 'CONNECT', host: 'registry.npmjs.org' }),
    { tone: 'ok', verb: 'Allowed', target: 'registry.npmjs.org' });
  assert.deepEqual(auditRow({ decision: 'deny', method: 'POST', host: 'api.github.com', path: '/repos/x/y/pulls', reason: 'r' }),
    { tone: 'bad', verb: 'Blocked', target: 'POST api.github.com/repos/x/y/pulls' });
  assert.deepEqual(auditRow({ decision: 'ask', method: 'PUT', host: 'api.github.com', path: '/m' }).tone, 'wait');
  assert.deepEqual(auditRow({ decision: 'approve', method: 'PUT', host: 'h', path: '/m', by: 'z' }).verb, 'Approved');
  // You refusing a card is told apart from the broker blocking a request.
  assert.deepEqual(auditRow({ decision: 'reject', method: 'PUT', host: 'h', path: '/m', by: 'z' }).verb, 'Denied');
  // Token changes have no host: the reason says what happened.
  assert.deepEqual(auditRow({ decision: 'mint', project: 'orbit', reason: 'session abc' }), { tone: 'info', verb: 'Token issued', target: 'session abc' });
  assert.deepEqual(auditRow({ decision: 'revoke', reason: 'kill switch: 2 sessions' }), { tone: 'bad', verb: 'Revoked', target: 'kill switch: 2 sessions' });
  assert.equal(auditRow({ decision: 'mystery' }).verb, 'mystery');
  // Hidden characters in a path are shown, not obeyed.
  assert.equal(auditRow({ decision: 'allow', method: 'GET', host: 'h', path: '/a\u202eb' }).target, 'GET h/a<U+202E>b');
});

test('board route', () => {
  assert.deepEqual(route('/p/orbit/board'), { name: 'project', project: 'orbit', tab: 'board' });
  assert.deepEqual(route('/p/orbit/runs'), { name: 'project', project: 'orbit', tab: 'runs' });
  assert.deepEqual(route('/p/Bad%20Name/board'), { name: 'notfound' });
  assert.deepEqual(route('/p/orbit/other'), { name: 'notfound' });
});

test('dropPosition', () => {
  const { dropPosition } = require('./lib.js');
  assert.equal(dropPosition(undefined, undefined), 0);
  assert.equal(dropPosition(undefined, 4), 3);   // top of the column
  assert.equal(dropPosition(4, undefined), 5);   // bottom
  assert.equal(dropPosition(1, 2), 1.5);         // between
  assert.equal(dropPosition(-1, -1), -1);        // equal neighbours: the server respaces
});

test('columnPage', () => {
  const { columnPage } = require('./lib.js');
  const items = Array.from({ length: 30 }, (_, i) => ({ ref: 'A-' + i, position: i }));
  let p = columnPage('backlog', items, 30, false);
  assert.equal(p.shown.length, 20);
  assert.equal(p.more, 'Show 10 more');
  assert.equal(p.below, 20); // a drop at the foot lands above the first hidden card
  p = columnPage('backlog', items, 30, true);
  assert.equal(p.shown.length, 30);
  assert.equal(p.more, null);
  assert.equal(p.below, undefined);
  // Done shows what was loaded and offers older ones, fifty at a time.
  p = columnPage('done', items.slice(0, 20), 140, false);
  assert.equal(p.shown.length, 20);
  assert.equal(p.more, 'Show 50 older');
  assert.equal(columnPage('done', items.slice(0, 20), 32, false).more, 'Show 12 older');
  assert.equal(columnPage('todo', items.slice(0, 3), 3, false).more, null);
});

test('hosts from text', () => {
  const { hostsFromText } = require('./lib.js');
  assert.deepEqual(hostsFromText(' api.openai.com,  eu.api.openai.com\nx.example '), ['api.openai.com', 'eu.api.openai.com', 'x.example']);
  assert.deepEqual(hostsFromText(''), []);
});

test('settings audit rows', () => {
  const { auditRow } = require('./lib.js');
  assert.deepEqual(auditRow({ decision: 'config', by: 'z', reason: 'added secret openai for api.openai.com' }),
    { tone: 'info', verb: 'Settings', target: 'added secret openai for api.openai.com' });
});

test('boardSeries', () => {
  const { boardSeries } = require('./lib.js');
  const at = (d, h = 12) => new Date(2026, 9, d, h).toISOString();
  const now = new Date(2026, 9, 19, 15);
  const h = { since: at(4), open_at_start: 3, created: [at(10), at(18, 23), at(19, 9)], done: [at(12), at(19, 10), at(19, 11)] };
  const s = boardSeries(h, now);
  assert.equal(s.length, 14);
  assert.equal(s[13].day.getDate(), 19);
  assert.equal(s[0].day.getDate(), 6);
  assert.deepEqual(s.map(x => x.done), [0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 2]);
  // Open at each day's end: 3, +1 on the 10th, -1 on the 12th, +1 late on
  // the 18th, then today +1 -2.
  assert.deepEqual(s.map(x => x.open), [3, 3, 3, 3, 4, 4, 3, 3, 3, 3, 3, 3, 4, 3]);
  assert.equal(boardSeries({ since: at(4), open_at_start: 0, created: [], done: [] }, now).every(x => !x.done && !x.open), true);
});

test('activitySeries', () => {
  const { activitySeries } = require('./lib.js');
  const now = new Date(2026, 9, 19, 15);
  const hr = (d, h) => new Date(2026, 9, d, h).toISOString();
  const s = activitySeries([
    { t: hr(4, 10), allowed: 50, asked: 0, blocked: 0 }, // before the window
    { t: hr(18, 1), allowed: 3, asked: 1, blocked: 0 },
    { t: hr(18, 23), allowed: 2, asked: 0, blocked: 4 },
    { t: hr(19, 9), allowed: 7, asked: 0, blocked: 1 },
  ], now);
  assert.equal(s.length, 14);
  assert.deepEqual(s[12], { day: new Date(2026, 9, 18), allowed: 5, asked: 1, blocked: 4 });
  assert.deepEqual(s[13], { day: new Date(2026, 9, 19), allowed: 7, asked: 0, blocked: 1 });
  assert.equal(s.slice(0, 12).every(x => !x.allowed && !x.asked && !x.blocked), true);
});

test('until in days', () => {
  const now = Date.parse('2026-10-17T12:00:00Z');
  assert.equal(until('2026-10-18T10:00:00Z', now), 'in 22h');
  assert.equal(until('2026-10-20T12:00:00Z', now), 'in 3d');
});

test('usageSeries', () => {
  const { usageSeries } = require('./lib.js');
  const now = new Date(2026, 9, 19, 15);
  const hr = (d, h) => new Date(2026, 9, d, h).toISOString();
  const { days, harnesses } = usageSeries([
    { t: hr(1, 10), harness: 'claude', input: 9, cached: 9, output: 9 }, // before the window
    { t: hr(18, 1), harness: 'codex', input: 10, cached: 80, output: 10 },
    { t: hr(18, 3), harness: 'claude', input: 1, cached: 2, output: 3 },
    { t: hr(19, 9), harness: 'claude', input: 100, cached: 0, output: 0 },
    { t: hr(19, 10), harness: 'pi', input: 1, cached: 0, output: 0 },
  ], now);
  assert.deepEqual(harnesses, ['claude', 'codex', 'pi']);
  assert.equal(days.length, 14);
  assert.deepEqual(days[12], { day: new Date(2026, 9, 18), claude: 6, codex: 100, pi: 0 });
  assert.deepEqual(days[13], { day: new Date(2026, 9, 19), claude: 100, codex: 0, pi: 1 });
  assert.equal(usageSeries([], now).harnesses.length, 0);
});

test('compactCount', () => {
  const { compactCount } = require('./lib.js');
  assert.equal(compactCount(950), '950');
  assert.equal(compactCount(12400), '12.4K');
  assert.equal(compactCount(3_250_000), '3.3M');
  assert.equal(compactCount(1_200_000_000), '1.2B');
});

test('limitLabel', () => {
  const { limitLabel } = require('./lib.js');
  assert.equal(limitLabel({ name: 'five_hour', minutes: 300 }), '5-hour');
  assert.equal(limitLabel({ name: 'seven_day', minutes: 10080 }), 'Weekly');
  assert.equal(limitLabel({ name: 'primary', minutes: 1440 }), 'Daily');
  assert.equal(limitLabel({ name: 'seven_day_opus' }), 'Seven day opus');
});

test('samePage: a tab or a settings section is the same page, so it redraws without a fade', () => {
  const same = (a, b) => samePage(route(a), route(b));
  assert.equal(same('/a/pip', '/a/pip/memory'), true);
  assert.equal(same('/p/orbit', '/p/orbit/board'), true);
  assert.equal(same('/settings/rules', '/settings/access/pip'), true);
  assert.equal(same('/a/pip', '/a/iona'), false);
  assert.equal(same('/p/orbit', '/a/orbit'), false);
  assert.equal(same('/s/abc', '/s/def'), false);
  assert.equal(same('/', '/activity'), false);
  assert.equal(samePage(undefined, route('/')), false);
});

test('agentCursor follows where the agent acts on the page', () => {
  const size = { w: 1280, h: 900 };
  let c = agentCursor(null, { act: 'click', x: 640, y: 225 }, size, 1000);
  assert.deepEqual(c, { left: 50, top: 25, act: 'click', at: 1000, hideAt: null });
  // Typing moves it too; a click elsewhere moves it on.
  c = agentCursor(c, { act: 'type', x: 320, y: 450 }, size, 2000);
  assert.deepEqual([c.left, c.top, c.act, c.hideAt], [25, 50, 'type', null]);
  // A page loaded right after a click lets the press show before it goes.
  assert.equal(agentCursor(c, { act: 'page' }, size, 2100).hideAt, 3200);
  assert.equal(agentCursor(c, { act: 'page' }, size, 5000).hideAt, 5000);
  // Scrolling or a take-over leaves it pointing at nothing: it goes now.
  for (const act of ['scroll', 'taken']) assert.equal(agentCursor(c, { act }, size, 3000).hideAt, 3000);
  // Nothing shown yet stays nothing; an unknown act changes nothing.
  assert.equal(agentCursor(null, { act: 'page' }, size, 1), null);
  assert.equal(agentCursor(c, { act: 'free' }, size, 1), c);
  // A phone-sized page maps by its own size.
  assert.deepEqual([agentCursor(null, { act: 'click', x: 195, y: 700 }, { w: 390, h: 700 }, 0).left, agentCursor(null, { act: 'click', x: 195, y: 700 }, { w: 390, h: 700 }, 0).top], [50, 100]);
});
