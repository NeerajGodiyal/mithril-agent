// Run with: node paperdashboard/page-check.mjs
// Exercise the embedded renderer directly without installing a frontend stack.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const source = readFileSync(new URL('./page.go', import.meta.url), 'utf8');
const start = source.indexOf('function paperCheckReason(');
const end = source.indexOf('function paperCheckGateReason(', start);
assert(start > 0 && end > start, 'paper-check renderer boundaries missing');
const percent = source.match(/^const percent=.*;$/m)?.[0];
assert(percent, 'shared percentage formatter missing');
const view = runInNewContext(percent + '\n' + source.slice(start, end) + '\npaperCheckView');

const old = {
  outcome: 'no_training_candidate', candidates_evaluated: 72,
  training_rejections: { no_round_trip: 72 },
  reasons: ['no_qualified_training_candidate'],
};
const activity = { version: 1, base_minimum_signal_bps: 90, candidates_without_entry_signal: 72 };
assert.equal(view({}), null);
assert.equal(view({ paper_check: old }).label, 'No suitable plan');
const noEntries = view({ paper_check: { ...old, training_activity: activity } });
assert.equal(noEntries.label, 'No entry signal');
assert.match(noEntries.note, /None of the 72 tested plans generated an entry signal/);
assert.match(noEntries.note, /0\.90%; this search never lowers it/);
assert.doesNotMatch(noEntries.note, /price.*(below|too small)/i);
for (const changed of [{ ...activity, candidates_without_entry_signal: 71 }, { ...activity, version: 9 }]) {
  const result = view({ paper_check: { ...old, training_activity: changed } });
  assert.equal(result.label, 'No suitable plan');
  assert.doesNotMatch(result.note, /None of the 72/);
}
assert.equal(view({ paper_check: { ...old, outcome: 'insufficient_evidence', candidates_evaluated: 0, training_activity: activity } }).label, 'Not enough data');
console.log('Paper-check renderer: legacy, no-entry, mixed, unknown and incomplete cases passed.');
