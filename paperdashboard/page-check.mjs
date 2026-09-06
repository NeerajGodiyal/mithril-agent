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
  'Paper run #6', 'Not an active order', 'does not confirm a test result or a strategy change', 'Last attempt · 2h ago']) {
  assert(proposalHTML.includes(expected), `proposal omitted ${expected}`);
}
assert.doesNotMatch(proposalHTML, /Running|Selected|Profit|Learning complete/);
const existingHTML = proposals({markets: [{symbol:'SOL', status:'already_saved', strategy:'breakout',
  risk_arm:'balanced', target_episode:'6', frozen_at:'2026-09-05T20:00:00Z'}],
  finished_at:'2026-09-05T21:45:52Z'}, false);
for (const expected of ['Already saved', 'Last checked', 'Saved earlier', 'No new model call',
  'Paper run #6', 'Research was not repeated']) assert(existingHTML.includes(expected), expected);
assert.doesNotMatch(existingHTML, /Data reviewed|recorded runs|earlier results|undefined|Selected/);
for (const status of ['retained_baseline', 'already_retained']) {
  const html = proposals({ markets: [{ symbol: 'ETH', status, target_episode: '7',
    reviewed_at: '2026-09-06T12:00:00Z' }] }, false);
  for (const expected of ['Reviewed · 2h ago', 'No strategy change', 'No challenger created',
    'Paper run #7', 'Not a trade or an order']) assert(html.includes(expected), expected);
  assert.doesNotMatch(html, /Suggested plan|recorded runs|earlier results|undefined|Profit|Selected/);
  assert.equal(html.includes('No new model call'), status === 'already_retained');
}
for (const status of ['unavailable', 'cleanup_required', 'interrupted']) {
  const html = proposals({ markets: [{ symbol: 'BTC', status }] }, false);
  assert.match(html, /A saved proposal, if any, has not been confirmed here/);
  assert.doesNotMatch(html, /Paper run #|Suggested plan|recorded runs/);
}
assert(!proposals({ markets: [{ ...savedProposal, symbol: '<script>' }] }, false).includes('<script>'));
console.log('Hermes proposals: missing, invalid, saved, repeated, stale, failure and escaping cases passed.');
const lifecycleStart = source.indexOf('function hermesLifecycleCards(');
const lifecycleEnd = source.indexOf('function renderSystem(', lifecycleStart);
const moneyFormatters = ['integer','decimal','money','paperValue','deltaValue','signedAmount','tone']
  .map(name => {const line=source.match(new RegExp('^const '+name+'=.*;$','m'))?.[0];assert(line,name);return line;}).join('\n');
const lifecycleCards = runInNewContext(moneyFormatters + '\n' + source.slice(lifecycleStart, lifecycleEnd) + '\nhermesLifecycleCards', {
  safe: value => String(value).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;'),
  age: () => 'Updated 2h ago',
});
assert.match(lifecycleCards(null,false), /not connected yet/);
assert.match(lifecycleCards({lifecycle_error:true},false), /could not be verified/);
const lifecycle = {as_of:'2026-09-05T21:00:00Z',selection_enabled:false,
  markets:[{symbol:'SOL',recorded_proposals:4,manual_reconciliation_required:true}],
  proposals:[{symbol:'SOL',proposal_sha256:'a'.repeat(64),target_episode:'27',frozen_at:'2026-09-05T20:00:00Z',evaluation_status:'evaluated',selection_status:'paused'},
    {symbol:'SOL',target_episode:'28',frozen_at:'2026-09-05T20:00:00Z',evaluation_status:'pending',selection_status:'paused'}]};
const lifecycleHTML = lifecycleCards({lifecycle},false);
for(const text of ['Automatic selection paused','Test complete','Awaiting result','Selection paused',
  'previous selection needs review','not be retried automatically','4 recorded · showing 2','not an approval or a live order']) {
  assert(lifecycleHTML.includes(text),text);
}
assert.doesNotMatch(lifecycleHTML,/Approved|Active plan|undefined/);
const historicalHTML = lifecycleCards({lifecycle:{...lifecycle,proposals:[{...lifecycle.proposals[0],selection_status:'selected_previously'}]}},false);
assert.match(historicalHTML,/Selected previously/);
assert.match(historicalHTML,/not confirmation of the plan running now/);
console.log('Proposal lifecycle: missing, unavailable, paused, historical and older-warning cases passed.');
const score = {filled_orders:'1',closed_positions:'1',net_pnl_micros:'-123456',fees_paid_micros:'1200'};
const compared = comparison => lifecycleCards({lifecycle:{...lifecycle,proposals:[{...lifecycle.proposals[0],comparison}]}},false);
const comparedHTML = compared({proposed:{...score,filled_orders:'0',closed_positions:'0',net_pnl_micros:'0',fees_paid_micros:'0'},
  baseline:score,proposed_stress:null,baseline_stress:{...score,net_pnl_micros:'-999'}});
for(const text of ['Proposed plan','Previous plan','$0.00','−$0.12','−&lt;$0.01','No positions opened',
  '1 opened · 1 closed','Not scored','after costs','Not your account balance','Higher-fee test','2× modeled entry / exit fees']) {
  assert(comparedHTML.includes(text),text);
}
assert.match(comparedHTML,/<details class="proposal-stress" data-detail="[^"]*"><summary>/);
assert.doesNotMatch(comparedHTML,/Approved|Qualified|2× costs|undefined/);
assert.match(compared(undefined),/Result details unavailable/);
assert.doesNotMatch(compared(undefined),/\$0\.00|No positions opened/);
assert.doesNotMatch(compared({proposed:null,baseline:null,proposed_stress:null,baseline_stress:null}),/\$0\.00|No positions opened/);
assert.match(compared({proposed:{...score,net_pnl_micros:'123456'}}),/\+\$0\.12/);
assert.match(compared({proposed:{...score,net_pnl_micros:'9223372036854775807'}}),/9,223,372,036,854\.78/);
console.log('Proposal comparison: actual zero, loss, missing, unscored, sub-cent, large and higher-fee results passed.');
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
