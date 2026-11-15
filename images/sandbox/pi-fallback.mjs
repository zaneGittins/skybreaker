// <provider>/auto: one model that answers with the first of a provider's
// fallback list (from Skybreaker's config, in the sandbox's pi-models.json)
// that has quota left. A hosted model server can cap each model's tokens a
// day; when one is out, the retry goes to the next and that model rests,
// rather than the turn failing. A turn stays on the model it started with
// (its prompt cache and thinking stay valid), and a new message from you
// goes back to the first model whose rest is over.
import fs from 'node:fs';
import path from 'node:path';

// rest is how long a model out of its daily quota is left alone; quotas
// refill by the minute, so it's tried again after this.
const rest = 30 * 60 * 1000;

// outOfQuota is a provider's daily token cap, as pi reports the failure:
// DigitalOcean's is `429: {"message":"Rate limit exceeded: tokens_per_day."}`.
// Other failures, a per-minute limit among them, are pi's own retries'.
const outOfQuota = (msg) => typeof msg === 'string' && /\b429\b/.test(msg) && /tokens?[ _-]per[ _-]day/i.test(msg);

const object = (v) => (v && typeof v === 'object' && !Array.isArray(v) ? v : {});

function piModels() {
  const etc = process.env.SKYBREAKER_ETC || '/etc/skybreaker';
  try {
    return object(JSON.parse(fs.readFileSync(path.join(etc, 'pi-models.json'), 'utf8')));
  } catch {
    return {};
  }
}

export default function (pi) {
  const config = piModels();
  for (const [provider, ids] of Object.entries(object(config.fallbacks))) {
    if (!Array.isArray(ids) || ids.length < 2 || !ids.every((id) => typeof id === 'string')) continue;
    // Until a model answers, pi shows the first one's limits.
    const models = object(object(config.providers)[provider]).models;
    const lead = object((Array.isArray(models) ? models : []).find((m) => m?.id === ids[0]));
    pi.registerVirtualModel({
      provider,
      id: 'auto',
      name: `Auto (${ids.join(', then ')})`,
      thinkingLevels: ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'],
      ...(typeof lead.contextWindow === 'number' && { contextWindow: lead.contextWindow }),
      ...(typeof lead.maxTokens === 'number' && { maxTokens: lead.maxTokens }),
      input: Array.isArray(lead.input) ? lead.input : ['text'],
      route(request, ctx) {
        const now = Date.now();
        const resting = { ...(request.state?.resting ?? {}) };
        const find = (id) => ctx.modelRegistry.find(provider, id);
        const first = (except) => ids.find((id) => id !== except && !(resting[id] > now) && find(id));
        const level = request.thinkingLevel;
        const failed = request.failed;
        if (request.reason === 'retry' && failed) {
          if (!outOfQuota(failed.message?.errorMessage)) {
            return { model: failed.model, thinkingLevel: failed.thinkingLevel ?? level };
          }
          resting[failed.model.id] = now + rest;
          const next = first(failed.model.id);
          // With every model out, pi's retries wait on the one that failed.
          return next
            ? { model: find(next), thinkingLevel: level, state: { resting } }
            : { model: failed.model, thinkingLevel: failed.thinkingLevel ?? level, state: { resting } };
        }
        if (request.reason === 'continuation' && request.previous) {
          return { model: request.previous.model, thinkingLevel: request.previous.thinkingLevel ?? level };
        }
        const id = first() ?? ids.find(find);
        if (!id) throw new Error(`${provider}/auto: none of ${ids.join(', ')} is available`);
        return { model: find(id), thinkingLevel: level };
      },
    });
  }
}
