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

const researchStart = source.indexOf('function researchView(');
const researchEnd = source.indexOf('function mithrilEvidenceView(', researchStart);
assert(researchStart > 0 && researchEnd > researchStart, 'research renderer boundaries missing');
const research = packet => runInNewContext(source.slice(researchStart, researchEnd) + '\nresearchView()', {
  current: { research_enabled: true, research: packet }, age: () => 'just now',
});
const web = { market: 'SOL/USDC', current: true, actionable: true, disposition: 'candidate', risk_decision: 'pass',
  risk_reason: 'Paper only.', sources_checked: 0, retrieved_pages: 0, successful_web_searches: 0,
  two_source_claims: 0, single_source_facts: 0, contradicted_facts: 0, unverified_facts: 0 };
const legacyResearch = research(web);
const webResearch = research({ ...web, evidence_basis: 'web_sources', retrospective_screening: false });
assert.equal(JSON.stringify(legacyResearch), JSON.stringify(webResearch), 'v1 rendering changed');
assert.doesNotMatch(webResearch.detail, /Uses recorded|Still needs testing/);
for (const current of [true, false]) {
  const recorded = research({ ...web, current, evidence_basis: 'recorded_paper_observations',
    observation_day: '2026-09-04', observation_metric_ids: ['signals', 'fills'], retrospective_screening: true });
  assert.match(recorded.detail, /Uses recorded paper data from 2026-09-04/);
  assert.match(recorded.detail, /Still needs testing on new market data/);
  assert.match(recorded.description, /0 unique sources/);
  assert.match(recorded.description, /0 two-source facts/);
  assert.equal(recorded.label, current ? 'Proposal ready' : 'Expired');
}
assert.doesNotMatch(research({ ...web, evidence_basis: 'future_unknown', retrospective_screening: true }).detail, /Uses recorded/);
console.log('Research renderer: legacy, web, recorded, expired and unknown evidence bases passed.');

const proposalStart = source.indexOf('function hermesPerpsCards(');
const proposalEnd = source.indexOf('function renderSystem(', proposalStart);
assert(proposalStart > 0 && proposalEnd > proposalStart, 'proposal renderer boundaries missing');
const proposals = runInNewContext(source.slice(proposalStart, proposalEnd) + '\nhermesPerpsCards', {
  safe: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'),
  age: () => 'Updated 2h ago',
});
assert.match(proposals(null, false), /No Hermes perps proposal summary is connected/);
assert.match(proposals(null, true), /could not be verified/);
const savedProposal = { symbol: 'SOL', status: 'pending_advisory', strategy: 'breakout', risk_arm: 'balanced',
  training_tapes: 8, resolved_outcomes: 0, target_episode: '6' };
const proposalHTML = proposals({ markets: [savedProposal], finished_at: '2026-09-05T21:45:52Z' }, false);
for (const expected of ['Proposal saved', 'Breakout', 'Balanced risk', '8 recorded runs', '0 earlier results',
  'Paper run #6', 'Not an active order', 'does not confirm a test result or a strategy change', 'Updated 2h ago']) {
  assert(proposalHTML.includes(expected), `proposal omitted ${expected}`);
}
assert.doesNotMatch(proposalHTML, /Running|Selected|Profit|Learning complete/);
for (const status of ['unavailable', 'cleanup_required', 'interrupted']) {
  const html = proposals({ markets: [{ symbol: 'BTC', status }] }, false);
  assert.match(html, /A saved proposal, if any, has not been confirmed here/);
  assert.doesNotMatch(html, /Paper run #|Suggested plan|recorded runs/);
}
assert(!proposals({ markets: [{ ...savedProposal, symbol: '<script>' }] }, false).includes('<script>'));
console.log('Hermes proposals: missing, invalid, saved, stale, failure and escaping cases passed.');
const recordingFailure = proposals({ markets: [{ symbol: 'SOL', status: 'unavailable', phase: 'record_invocation' }] }, false);
assert.doesNotMatch(recordingFailure, /No usable proposal|No proposal was saved/);
assert.match(recordingFailure, /has not been confirmed here/);

const planStart = source.indexOf('function perpsPlanSource(');
const planEnd = source.indexOf('function perpsLaterOutcome(', planStart);
assert(planStart > 0 && planEnd > planStart);
const planSource = runInNewContext(source.slice(planStart, planEnd) + '\nperpsPlanSource');
assert.equal(planSource({ decision_source: 'selected_paper_plan', proposal_source: 'frozen_proposal' }), 'Selected paper plan from an evaluated proposal');
assert.equal(planSource({ decision_source: 'selected_paper_plan', proposal_source: 'deterministic_search' }), 'Selected paper plan proposed by deterministic search');
assert.equal(planSource({ decision_source: 'legacy_fixed_policy', proposal_source: 'built_in' }), 'Built-in fixed paper plan');
assert.equal(planSource({ decision_source: 'legacy_fixed_policy', proposal_source: 'frozen_proposal' }), 'Paper plan source unavailable');
console.log('Paper plan source labels preserve provenance and legacy behavior.');
