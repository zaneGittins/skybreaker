// Puts what Skybreaker provides into pi's own config before pi starts: its
// MCP server (the board, show and preview tools), and the model servers
// behind broker routes from /etc/skybreaker/pi-models.json. Providers it
// added before are replaced; anything else in pi's config is left alone.
// Usage: node pi-config.mjs [--skybreaker-mcp <mcp.json>]
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const etc = process.env.SKYBREAKER_ETC || '/etc/skybreaker';
const agent = process.env.PI_CODING_AGENT_DIR || path.join(os.homedir(), '.pi', 'agent');
const args = process.argv.slice(2);
const mcpFile = args[0] === '--skybreaker-mcp' && args[1] ? args[1] : path.join(etc, 'mcp.json');

const read = f => {
  try { return JSON.parse(fs.readFileSync(f, 'utf8')) } catch { return null }
};
const write = (f, v) => {
  const tmp = `${f}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(v, null, 2) + '\n', { mode: 0o600 });
  fs.renameSync(tmp, f);
};
const object = v => v && typeof v === 'object' && !Array.isArray(v) ? v : {};

fs.mkdirSync(agent, { recursive: true, mode: 0o700 });

// MCP: Skybreaker's own server, under its name.
const ours = object(object(read(mcpFile)).mcpServers).skybreaker;
if (ours) {
  const f = path.join(agent, 'mcp.json');
  const mcp = object(read(f));
  mcp.mcpServers = { ...object(mcp.mcpServers), skybreaker: ours };
  write(f, mcp);
}

// Models: the providers Skybreaker lists now replace the ones it listed
// last time, which a note beside pi's config remembers.
const noteFile = path.join(agent, 'skybreaker-providers.json');
const before = Array.isArray(read(noteFile)) ? read(noteFile) : [];
const given = object(object(read(path.join(etc, 'pi-models.json'))).providers);
if (before.length || Object.keys(given).length) {
  const f = path.join(agent, 'models.json');
  const models = object(read(f));
  const providers = object(models.providers);
  for (const name of before) delete providers[name];
  Object.assign(providers, given);
  models.providers = providers;
  write(f, models);
  write(noteFile, Object.keys(given));
}
