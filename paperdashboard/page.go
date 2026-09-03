package paperdashboard

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="color-scheme" content="dark">
  <title>Mithril Paper Trading</title>
  <link rel="icon" href="/vendor/overclock.svg" type="image/svg+xml">
  <link rel="stylesheet" href="/app.css">
</head>
<body>
  <a class="skip" href="#main">Skip to content</a>
  <header class="app-header">
    <div class="shell topbar">
      <div class="brand">
        <img class="brand-logo" src="/vendor/overclock.svg" alt="">
        <div><p class="eyebrow">Overclock</p><h1>Mithril</h1></div>
      </div>
      <nav class="tabs" aria-label="Dashboard sections" role="tablist">
        <button id="tab-overview" class="tab active" data-tab="overview" role="tab" aria-selected="true" aria-controls="overview"><span class="nav-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><rect x="4" y="4" width="6" height="6" rx="1"/><rect x="14" y="4" width="6" height="6" rx="1"/><rect x="4" y="14" width="6" height="6" rx="1"/><rect x="14" y="14" width="6" height="6" rx="1"/></svg></span>Dashboard</button>
        <button id="tab-activity" class="tab" data-tab="activity" role="tab" aria-selected="false" aria-controls="activity" tabindex="-1"><span class="nav-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M5 17l4-5 4 2 6-8"/><path d="M14 6h5v5"/></svg></span>Activity</button>
        <button id="tab-strategy" class="tab" data-tab="strategy" role="tab" aria-selected="false" aria-controls="strategy" tabindex="-1"><span class="nav-icon" aria-hidden="true"><circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/><path d="M12 4v3m0 10v3M4 12h3m10 0h3"/></svg></span>Strategy</button>
        <button id="tab-system" class="tab" data-tab="system" role="tab" aria-selected="false" aria-controls="system" tabindex="-1"><span class="nav-icon" aria-hidden="true"><path d="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3z"/><path d="M8 12h8m-4-4v8"/></svg></span>Automation</button>
      </nav>
      <div class="header-state">
        <div class="checked"><span id="connection-dot" class="dot" aria-hidden="true"></span><span id="checked">Connecting…</span></div>
        <div class="controls">
          <button id="live" class="button quiet" type="button" aria-pressed="true">Live updates: On</button>
          <button id="refresh" class="button" type="button">Refresh</button>
          <span id="refresh-status" class="sr-only" role="status" aria-live="polite"></span>
        </div>
        <div class="trust" role="note">
          <div class="trust-inner">
            <strong>Simulation only</strong>
            <span>Paper money · No real orders</span>
          </div>
        </div>
      </div>
    </div>
  </header>
  <main id="main" class="shell">
    <div id="notice" class="notice" role="status" aria-live="polite"></div>
    <section id="overview" class="panel active" role="tabpanel" aria-labelledby="tab-overview" tabindex="0">
      <div id="metrics" class="metrics" aria-label="Paper account summary"></div>
      <div class="overview-workspace">
        <div id="markets" class="market-grid" role="tabpanel" aria-label="Selected paper market performance"></div>
        <div id="market-switcher" class="market-switcher" role="tablist" aria-label="Paper markets" aria-orientation="vertical"></div>
      </div>
      <details class="daily-note"><summary>About this paper account</summary><p>“Started” compares each active simulation with its own opening balance. Spot runs reset on the bot's UTC day; short perps experiments can begin later. The combined result is simulated and is not a continuously compounded wallet.</p></details>
    </section>
    <section id="activity" class="panel" role="tabpanel" aria-labelledby="tab-activity" tabindex="0" hidden>
      <div class="section-title">
        <div><p class="eyebrow">Order history</p><h2 id="activity-title">Recent activity</h2><p id="activity-summary">Loading recent paper activity…</p></div>
        <label class="filter">Show
          <select id="activity-filter">
            <option value="important">Important activity</option>
            <option value="all">All activity</option>
            <option value="orders">Paper order activity</option>
            <option value="strategy">Strategy changes</option>
            <option value="safety">Safety events</option>
            <option value="data">Data issues</option>
          </select>
        </label>
      </div>
      <div class="activity-table">
        <div class="activity-list-head" aria-hidden="true"><span>Event</span><span>Summary</span><span>Result</span><span>When</span></div>
        <div id="activity-list" class="activity-list"></div>
      </div>
    </section>
    <section id="strategy" class="panel" role="tabpanel" aria-labelledby="tab-strategy" tabindex="0" hidden>
      <div class="section-title"><div><p class="eyebrow">How decisions are made</p><h2 id="strategy-title">Strategy</h2></div></div>
      <div class="strategy-layout">
        <article class="card feature strategy-brief">
          <div><span class="badge blue">Paper strategies</span><h3>Watch. Decide. Verify.</h3></div>
          <ol class="strategy-flow" aria-label="Strategy decision flow">
            <li><span class="flow-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M3 16l4-5 4 3 5-8 5 4"/><path d="M3 20h18"/></svg></span><span><strong>Observe</strong><small>Price · movement · costs</small></span></li>
            <li><span class="flow-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="7"/><circle cx="12" cy="12" r="2"/><path d="M12 2v3m0 14v3M2 12h3m14 0h3"/></svg></span><span><strong>Decide</strong><small>Wait · buy · sell</small></span></li>
            <li><span class="flow-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3l7 3v5c0 4.4-2.8 8-7 10-4.2-2-7-5.6-7-10V6l7-3z"/><path d="M9 12l2 2 4-4"/></svg></span><span><strong>Verify</strong><small>Forward paper gate</small></span></li>
          </ol>
        </article>
        <aside class="card guardrails">
          <span class="badge green">Protected</span>
          <h3>Simulation boundary</h3>
          <p>Paper balances only. Hermes and news can propose research, but neither can sign, submit, or bypass deterministic gates. Perps can simulate bounded leverage and shorts without a wallet or real order.</p>
        </aside>
      </div>
      <div id="strategy-markets" class="market-grid small"></div>
      <article id="research-instruction" class="card instruction-card experiment-card" hidden>
        <div class="instruction-copy">
          <span class="badge violet">Nous Hermes research</span>
          <h3>Plan the next paper experiment</h3>
          <p>Set exact safety requirements for the next candidate. Saving updates the next research run without restarting the dashboard.</p>
        </div>
        <section class="active-limits" aria-labelledby="active-limits-title">
          <div class="subsection-head"><span class="badge green">Active now</span><h4 id="active-limits-title">Current paper plans</h4></div>
          <div id="active-limit-list" class="active-limit-list">Waiting for current limits…</div>
        </section>
        <div class="instruction-controls" aria-label="Next paper experiment request">
          <fieldset class="instruction-group">
            <legend>Scope</legend>
	            <p>Applies to the configurable spot markets bound to the reviewed research generation.</p>
            <label>Research goal
              <select id="instruction-preference">
                <option value="balanced">Keep it balanced</option>
                <option value="more-opportunities">Look for more opportunities</option>
                <option value="more-selective">Be more selective</option>
              </select>
            </label>
          </fieldset>
          <fieldset class="instruction-group">
            <legend>Capital and order size</legend>
            <p>Paper money only. A validated allocation starts with the next paper plan; risk exits may close more than the order cap.</p>
            <label>Total paper budget
              <input id="instruction-capital" type="number" min="10" max="1000000" step="1" inputmode="decimal" aria-describedby="instruction-capital-help">
              <small id="instruction-capital-help">Cash and simulated holdings across the configurable spot markets</small>
            </label>
            <label>Smallest order
              <input id="instruction-minimum-order" type="number" min="1" max="1000000" step="1" inputmode="decimal">
            </label>
            <label>Largest order
              <input id="instruction-maximum-order" type="number" min="1" max="1000000" step="1" inputmode="decimal">
            </label>
          </fieldset>
          <fieldset class="instruction-group">
            <legend>Safety and speed</legend>
            <label>Price-check speed
              <select id="instruction-cadence">
                <option value="5">Every 5 seconds</option>
                <option value="15">Every 15 seconds</option>
                <option value="30">Every 30 seconds</option>
                <option value="60">Every minute</option>
                <option value="300">Every 5 minutes</option>
              </select>
              <small>How often the paper plan checks prices; this does not force a trade</small>
            </label>
            <label>Paper loss stop
              <input id="instruction-drawdown" type="number" min="0.1" max="50" step="0.1" inputmode="decimal">
              <small>Pause new buys after this percentage fall from the session high</small>
            </label>
          </fieldset>
          <div id="instruction-warning" class="instruction-warning" role="status" aria-live="polite"></div>
          <button id="save-instruction" class="button" type="button">Save experiment request</button>
          <span id="instruction-status" role="status" aria-live="polite">No preference saved yet.</span>
        </div>
        <p class="instruction-boundary"><strong>Activation:</strong> saving never restarts Mithril or places an order. The paper allocator validates a fresh, isolated plan before switching the configurable spot markets together. Perps experiments remain isolated.</p>
      </article>
    </section>
    <section id="system" class="panel" role="tabpanel" aria-labelledby="tab-system" tabindex="0" hidden>
      <div class="section-title"><div><p class="eyebrow">Agent workspace</p><h2 id="system-title">Automation setup</h2><p>Every service, its current role, and the boundary it cannot cross.</p></div></div>
      <div id="automation" class="automation-grid" aria-label="Automation roles"></div>
      <div class="section-title compact"><div><p class="eyebrow">Live status</p><h2>Market observers</h2></div></div>
      <div id="system-list" class="system-list"></div>
      <div class="section-title compact"><div><p class="eyebrow">Trust boundary</p><h2>Access and evidence</h2></div></div>
      <div class="detail-grid">
        <article class="card detail-card"><span class="badge green">Paper only</span><h3>Permissions</h3><p>No wallet key, signing, real funds, or Mainnet submission. Margin, leverage, short positions, funding, and liquidation exist only inside the perps simulation.</p></article>
        <article id="research-evidence" class="card detail-card"><span class="badge blue">Research status</span><h3>Latest Hermes research</h3><p>Waiting for a validated research packet.</p></article>
	        <article class="card detail-card"><span class="badge amber">Reviewed scope</span><h3>Markets</h3><p>SOL and JUP spot simulations run beside isolated SOL, BTC, and ETH perps experiments. Recorded replay and cost stress checks run in minutes; short live checkpoints exercise current data and status plumbing. None proves profitability. WIF, JTO, and PYTH remain research-only until their route and liquidity evidence passes admission.</p></article>
        <article class="card detail-card"><span class="badge blue">Evidence retained</span><h3>Recent order activity</h3><p>The dashboard keeps a bounded recent list. Older events remain in the local evidence journals. There are no on-chain signatures because no transaction is submitted.</p><button id="open-order-history" class="text-button" type="button">View recent paper orders</button></article>
      </div>
      <article class="card access">
        <div><span class="badge green">Private access</span><h3>Dashboard stays on the server</h3></div>
        <p>Open it through an SSH tunnel. It is not published to the internet. Its only write action is the bounded paper-research preference above.</p>
      </article>
    </section>
  </main>
  <dialog id="help-dialog" class="help-dialog" aria-labelledby="help-dialog-title" aria-describedby="help-dialog-copy">
    <div class="help-dialog-panel">
      <div class="help-dialog-head"><span id="help-dialog-kicker">Quick explanation</span><form method="dialog"><button class="help-dialog-close" type="submit" aria-label="Close dialog"><span aria-hidden="true">×</span></button></form></div>
      <div class="help-dialog-content">
        <div id="help-dialog-visual" class="help-dialog-visual" aria-hidden="true"></div>
        <div class="help-dialog-copy"><h2 id="help-dialog-title" tabindex="-1"></h2><p id="help-dialog-copy"></p></div>
      </div>
      <div id="help-dialog-extra"></div>
    </div>
  </dialog>
  <footer class="shell">Private paper dashboard · Values are simulated, not financial results. TradingView Lightweight Charts™ · Copyright © 2025 TradingView, Inc. · <a href="https://www.tradingview.com/" rel="noreferrer">TradingView</a>.</footer>
  <script src="/vendor/lightweight-charts-5.2.1.js" defer></script>
  <script src="/app.js" defer></script>
</body>
</html>`

const appJS = `const $=id=>document.getElementById(id);
let current=null;
let refreshReset;
let requestSequence=0;
let liveUpdates=true;
let instructionDirty=false;
let marketChartViews={};
let selectedMarketName='';
let activeChart=null;
let activeChartKey='';
let chartRanges={};
let marketSparklines=[];
let helpReturn=null;
const safe=value=>String(value??'').replace(/[&<>'"]/g,char=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[char]));
const integer=value=>{try{return BigInt(value||0);}catch{return 0n;}};
const isPerps=market=>market?.instrument==='perpetual'||String(market?.name||'').endsWith('-PERP');
const decimal=(value,min,max)=>{let amount=integer(value),sign='';if(amount<0n){sign='-';amount=-amount;}const shift=10n**BigInt(6-max);amount=(amount+shift/2n)/shift;const base=10n**BigInt(max);const whole=amount/base;let fraction=(amount%base).toString().padStart(max,'0');while(fraction.length>min&&fraction.endsWith('0'))fraction=fraction.slice(0,-1);return sign+whole.toLocaleString()+(fraction?'.'+fraction:'');};
const money=micros=>integer(micros)>0n&&integer(micros)<10000n?'<$0.01':'$'+decimal(micros,2,2);
const price=micros=>{const amount=integer(micros),places=amount>=1000000n?2:amount>=10000n?4:6;return '$'+decimal(amount,2,places);};
const paperValue=(micros,unit)=>unit==='USD'?money(micros):decimal(micros,2,6)+' '+(unit||'units');
const assetAmount=(units,places,asset)=>{const amount=integer(units),digits=Math.max(0,Math.min(18,Number(places||0))),base=10n**BigInt(digits),whole=amount/base;let fraction=(amount%base).toString().padStart(digits,'0').replace(/0+$/,'');if(fraction.length>9)fraction=fraction.slice(0,9).replace(/0+$/,'');return whole.toLocaleString()+(fraction?'.'+fraction:'')+' '+safe(asset||'units');};
const unitsAsMicros=(units,places)=>{const digits=Math.max(0,Math.min(18,Number(places||0))),amount=integer(units);return digits>=6?amount/(10n**BigInt(digits-6)):amount*(10n**BigInt(6-digits));};
const initialLotValue=m=>{const asset=String(m.initial_lot_asset||'').toUpperCase(),base=String(m.name||'').split('/')[0].toUpperCase();if(asset==='USD'||asset.endsWith('USDC'))return unitsAsMicros(m.initial_lot_units,m.initial_lot_decimals);if(asset===base&&integer(m.price_micros)>0n)return integer(m.initial_lot_units)*integer(m.price_micros)/(10n**BigInt(Number(m.initial_lot_decimals||0)));return 0n;};
const exposure=(lot,capital)=>integer(capital)>0n?(Number(integer(lot)*10000n/integer(capital))/100).toFixed(1)+'%':'—';
const percent=bps=>(Number(bps||0)/100).toFixed(2).replace(/\.00$/,'')+'%';
const duration=seconds=>Number(seconds||0)>=60&&Number(seconds||0)%60===0?(Number(seconds)/60)+' min':Number(seconds||0)+' sec';
const deltaValue=(micros,unit)=>unit==='USD'&&integer(micros)<10000n?'<$0.01':unit==='USD'?money(micros):paperValue(micros,unit);
const resultDelta=(from,to,unit)=>{const value=integer(to)-integer(from);if(value===0n)return 'Unchanged';return (value>0n?'Up ':'Down ')+deltaValue(value>0n?value:-value,unit);};
const signedResult=(value,unit)=>{value=integer(value);if(value===0n)return 'Unchanged';return (value>0n?'Up ':'Down ')+deltaValue(value>0n?value:-value,unit);};
const signedAmount=(value,unit)=>{value=integer(value);if(value===0n)return 'No change';return (value>0n?'+':'−')+deltaValue(value>0n?value:-value,unit);};
const versusHolding=(strategy,hold,unit)=>{const value=integer(strategy)-integer(hold);if(value===0n)return 'Same as holding';return (value>0n?'Ahead by ':'Behind by ')+deltaValue(value>0n?value:-value,unit);};
const changePercent=(from,to)=>{from=integer(from);to=integer(to);if(from===0n)return '';const change=to-from;if(change===0n)return '0.00%';const hundredths=change*10000n/from,absolute=hundredths<0n?-hundredths:hundredths;return (change>0n?'+':'-')+(Number(absolute)/100).toFixed(2)+'%';};
const resultWithPercent=(from,to,unit)=>{const percentage=changePercent(from,to);return signedAmount(integer(to)-integer(from),unit)+(percentage?' · '+percentage:'');};
const effectiveEquity=value=>integer(value.equity_micros)-integer(value.deficit_micros);
const resultWithDeficit=value=>{const effective=effectiveEquity(value),percentage=changePercent(value.opening_equity_micros,effective);return signedAmount(effective-integer(value.opening_equity_micros),value.value_unit)+(percentage?' · '+percentage:'');};
const attempts=value=>Number(value||0)===1?'Plan tried to trade once':'Plan tried to trade '+Number(value||0)+' times';
const tone=value=>value>0n?'positive':value<0n?'negative':'neutral';
const age=value=>{if(!value)return 'Not updated';const seconds=Math.max(0,Math.round((Date.now()-Date.parse(value))/1000));if(seconds<10)return 'Updated just now';if(seconds<60)return 'Updated '+seconds+'s ago';const minutes=Math.round(seconds/60);if(minutes<60)return 'Updated '+minutes+'m ago';const hours=Math.round(minutes/60);if(hours<24)return 'Updated '+hours+'h ago';return 'Updated '+Math.round(hours/24)+'d ago';};
const eventTime=value=>value?new Date(value).toLocaleString([],{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}):'Not available';
const state=value=>({warming:'Learning recent prices',uptrend:'Market rising',downtrend:'Market falling',range:'Market moving sideways',volatile:'Waiting for calmer prices','order pending':'Paper order being checked','waiting for data':'Price data delayed',paused:'Paused by safety limit',watching:'Watching market'}[value]||'Watching market');
const decisionReason=value=>({'watching':'Watching the next price update','collecting_history':'Still learning recent prices','drawdown_limit':'Reducing risk after the loss limit','risk_halt':'New buys are paused by the loss limit','drawdown_halt':'New buys are paused by the loss limit','volatility_limit':'The market is moving too quickly','cooldown':'Taking a short break after a fill','trend_aligned_buy':'The trend supported a buy','sell_leg_waiting':'Waiting for a better sell move','trend_aligned_sell':'The trend supported a sell','buy_leg_waiting':'Waiting for a better buy move','range_high_sell':'Price reached the plan’s sell range','range_low_buy':'Price reached the plan’s buy range','signal_below_cost_hurdle':'The move is too small after costs','data_unavailable':'Fresh prices are unavailable','fee_budget_used':'This run’s simulated fee budget is used up','route_cost_limit':'The route was too expensive','order_pending':'A paper order is waiting to fill','order_filled':'The latest paper order filled','fill_limit':'Price moved beyond the fill limit','trade_unavailable':'The paper trade could not be priced or funded'}[value]||'Watching the market');
const eventGroup=kind=>kind.startsWith('order_')?'orders':kind.startsWith('strategy_')?'strategy':kind==='risk_halted'?'safety':kind.startsWith('data_')?'data':'other';
const marketStatus=(m,feeBudgetUsed)=>!m.fresh?{label:m.optional?'Not active':'Waiting for data',tone:'amber'}:feeBudgetUsed?{label:'Orders paused',tone:'amber'}:m.risk_halted?{label:isPerps(m)?'Simulation paused':'New buys paused',tone:'red'}:!m.coverage_ready?{label:'Checking data quality',tone:'amber'}:Number(m.coverage_bps||0)<9900?{label:'Limited price data',tone:'amber'}:{label:'Running',tone:'green'};
const priceCoverage=m=>m.coverage_ready?(Number(m.coverage_bps||0)/100).toFixed(1).replace(/\.0$/,'')+'%':'updating';
const marketDataHealthy=m=>m.available&&m.ready&&m.fresh&&m.coverage_ready&&Number(m.coverage_bps||0)>=9900;
const ratio=(value,total)=>integer(total)>0n?Math.max(0,Math.min(100,Number(integer(value)*10000n/integer(total))/100)):0;
const uiIcon=name=>'<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true">'+({
  watch:'<path d="M3 16l4-5 4 3 5-8 5 4"/><path d="M3 20h18"/>',
  score:'<circle cx="12" cy="12" r="7"/><circle cx="12" cy="12" r="2"/><path d="M12 2v3m0 14v3M2 12h3m14 0h3"/>',
  decide:'<path d="M5 5h14v14H5z"/><path d="M8 12h8m-3-3 3 3-3 3"/>',
  protect:'<path d="M12 3l7 3v5c0 4.4-2.8 8-7 10-4.2-2-7-5.6-7-10V6l7-3z"/><path d="M9 12l2 2 4-4"/>',
  order:'<path d="M5 4h14v16H5z"/><path d="M8 8h8m-8 4h8m-8 4h5"/>',
  clock:'<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  wallet:'<path d="M4 7h15v12H4z"/><path d="M4 7l11-3v3m1 5h5v4h-5a2 2 0 010-4z"/>',
  gauge:'<path d="M4 17a8 8 0 1116 0"/><path d="M12 17l4-5"/>',
  up:'<path d="M5 15l6-6 4 4 4-6"/><path d="M14 7h5v5"/>',
  down:'<path d="M5 9l6 6 4-4 4 6"/><path d="M14 17h5v-5"/>',
  flat:'<path d="M5 12h14"/>',
  filled:'<circle cx="12" cy="12" r="8"/><path d="M8.5 12l2.2 2.2 4.8-5"/>',
  pending:'<circle cx="12" cy="12" r="8"/><path d="M12 7v5l3 2"/>',
  strategy:'<path d="M4 17l4-5 4 2 4-7 4 3"/><path d="M4 20h16"/>',
  data:'<path d="M5 7c3-3 11-3 14 0M7.5 10c2-2 7-2 9 0M10 13c1-1 3-1 4 0"/><circle cx="12" cy="17" r="1"/>',
  info:'<circle cx="12" cy="12" r="9"/><path d="M12 11v6"/><path d="M12 7h.01"/>',
  arrow:'<path d="M5 12h14m-5-5 5 5-5 5"/>'
}[name]||'')+'</svg>';
const activityIcon=kind=>uiIcon(kind==='order_filled'?'filled':kind==='order_opened'?'pending':kind.startsWith('strategy_')?'strategy':kind.startsWith('data_')?'data':kind==='risk_halted'?'protect':'order');

function helpVisual(label){
  const key=String(label||'').toLowerCase();
  if(key.includes('holding'))return '<svg class="explain-svg" viewBox="0 0 180 104"><path class="visual-grid" d="M18 84H164M18 56H164M18 28H164"/><path class="visual-primary" d="M18 78L48 65 76 69 108 43 134 48 164 24"/><path class="visual-muted" d="M18 78L48 70 76 58 108 61 134 53 164 51"/><circle class="visual-dot" cx="164" cy="24" r="4"/><circle class="visual-dot muted" cx="164" cy="51" r="4"/></svg>';
  if(key.includes('order'))return '<svg class="explain-svg" viewBox="0 0 180 104"><path class="visual-grid" d="M46 52H76M104 52H134"/><circle class="visual-node" cx="30" cy="52" r="15"/><circle class="visual-node active" cx="90" cy="52" r="15"/><circle class="visual-node" cx="150" cy="52" r="15"/><path class="visual-primary" d="M24 52h12m-6-6v12M84 52l4 4 8-9M144 52h12"/></svg>';
  if(key.includes('profit')||key.includes('loss'))return '<svg class="explain-svg" viewBox="0 0 180 104"><path class="visual-grid" d="M18 70H162"/><path class="visual-primary" d="M22 70L56 70 82 56 111 63 158 31"/><path class="visual-dash" d="M22 70H158"/><circle class="visual-dot" cx="158" cy="31" r="4"/></svg>';
  return '<svg class="explain-svg" viewBox="0 0 180 104"><rect class="visual-node" x="28" y="27" width="96" height="58" rx="10"/><path class="visual-muted" d="M42 43h45M42 55h67M42 67h32"/><path class="visual-primary" d="M135 66l12-12 9 7 10-18"/><circle class="visual-dot" cx="166" cy="43" r="4"/></svg>';
}

function chartHistory(m){
  const points=(m.history||[]).filter(point=>Number.isFinite(Date.parse(point.at))).map(point=>({...point}));
  const observed=Date.parse(m.observed_at),currentPrice=integer(m.price_micros),last=points[points.length-1];
  if(currentPrice>0n&&Number.isFinite(observed)){
    const unavailable=m.state==='waiting for data';
    if(last&&Date.parse(last.at)===observed){last.price_micros=String(currentPrice);last.unavailable=unavailable;}
    else points.push({at:m.observed_at,price_micros:String(currentPrice),unavailable});
  }
  return points;
}
const chartTime=value=>Math.floor(Date.parse(value)/1000);
const chartNumber=value=>Number(integer(value))/1000000;
const chartClock=value=>new Date(value).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
const chartPointAvailable=(point,key)=>!point.unavailable&&(key!=='price_micros'||integer(point[key])>0n);
function chartTable(points,columns,detailKey){
  const head='<tr><th scope="col">Time</th>'+columns.map(column=>'<th scope="col">'+safe(column.label)+'</th>').join('')+'<th scope="col">Price data</th></tr>';
  const rows=points.map(point=>{const available=columns.every(column=>chartPointAvailable(point,column.key));return '<tr><th scope="row"><time datetime="'+safe(point.at)+'">'+safe(chartClock(point.at))+'</time></th>'+columns.map(column=>'<td>'+(available?safe(column.format(point[column.key])):'—')+'</td>').join('')+'<td>'+(available?'Available':'Unavailable')+'</td></tr>';}).join('');
  return '<details class="chart-data" data-detail="'+safe(detailKey)+'"><summary>View exact chart values</summary><div class="chart-table-scroll"><table><thead>'+head+'</thead><tbody>'+rows+'</tbody></table></div></details>';
}
function chartPanel(view,hidden,title,summary,legend,table){
  const explanation=view==='price'?'The green line shows the observed market price. Move over the chart to inspect an exact time and value.':'Green shows the bot’s practice-account value. Gray shows what the same starting assets would be worth if they were simply held.';
	const subtitle=view==='price'?"Current run · observed market data":"Current run · practice account comparison";
  return '<section class="chart '+(view==='price'?'market-price-chart ':'')+'library-chart" data-chart-panel="'+view+'"'+(hidden?' hidden':'')+'><div class="chart-head"><div><div class="chart-title-row"><span class="chart-title">'+safe(title)+'</span>'+help(title,explanation)+'</div><span class="chart-subtitle">'+safe(subtitle)+'</span></div><div class="chart-tools" aria-label="Chart controls"><button type="button" data-chart-action="zoom-out" aria-label="Zoom chart out">−</button><button type="button" data-chart-action="zoom-in" aria-label="Zoom chart in">+</button><button type="button" data-chart-action="reset">Reset</button></div></div><div class="chart-readout" data-chart-readout>'+legend+'</div><div class="chart-canvas" data-chart-canvas tabindex="0" role="group" aria-label="'+safe(summary)+' Use arrow keys to move through time, plus and minus to zoom, or open the exact values table below."></div>'+table+'</section>';
}
function marketPriceChart(m,hidden=false){
  const points=chartHistory(m),available=points.filter(point=>!point.unavailable&&integer(point.price_micros)>0n);
  if(available.length<2)return '<section class="chart market-price-chart" data-chart-panel="price"'+(hidden?' hidden':'')+'><div class="chart-head"><span class="chart-title">Market price</span></div><div class="chart-empty">Building the interactive price chart…</div></section>';
  const first=available[0],last=available[available.length-1],change=integer(last.price_micros)-integer(first.price_micros),summary=m.name+' observed market price moved from '+price(first.price_micros)+' to '+price(last.price_micros)+' ('+changePercent(first.price_micros,last.price_micros)+').';
	const legend='<span><i class="legend-line green" aria-hidden="true"></i>'+safe(m.name)+' price <strong>'+price(last.price_micros)+'</strong></span><span class="'+tone(change)+'">This run '+safe(changePercent(first.price_micros,last.price_micros))+'</span>';
  return chartPanel('price',hidden,m.name+' market price',summary,legend,chartTable(points,[{key:'price_micros',label:'Market price',format:price}],'price-values'));
}
function performanceChart(m,hidden=false){
  const points=m.history||[],available=points.filter(point=>!point.unavailable);
  if(available.length<2)return '<section class="chart" data-chart-panel="performance"'+(hidden?' hidden':'')+'><div class="chart-head"><span class="chart-title">Paper performance</span></div><div class="chart-empty">Building the interactive performance chart…</div></section>';
  const first=available[0],last=available[available.length-1],gaps=points.filter(point=>point.unavailable).length;
  const summary=m.name+' estimated paper account value moved from '+paperValue(first.equity_micros,m.value_unit)+' to '+paperValue(last.equity_micros,m.value_unit)+' ('+resultDelta(first.equity_micros,last.equity_micros,m.value_unit)+'). Holding comparison moved from '+paperValue(first.hold_benchmark_micros,m.value_unit)+' to '+paperValue(last.hold_benchmark_micros,m.value_unit)+' ('+resultDelta(first.hold_benchmark_micros,last.hold_benchmark_micros,m.value_unit)+'). '+gaps+' unavailable interval'+(gaps===1?'':'s')+'.';
  const comparison=integer(last.equity_micros)-integer(last.hold_benchmark_micros);
  const legend='<span><i class="legend-line green" aria-hidden="true"></i>Bot strategy <strong>'+paperValue(last.equity_micros,m.value_unit)+'</strong></span><span><i class="legend-line muted" aria-hidden="true"></i>If simply held <strong>'+paperValue(last.hold_benchmark_micros,m.value_unit)+'</strong></span><span class="chart-comparison '+tone(comparison)+'">'+safe(versusHolding(last.equity_micros,last.hold_benchmark_micros,m.value_unit))+'</span>';
  return chartPanel('performance',hidden,'Bot performance vs holding',summary,legend,chartTable(points,[{key:'equity_micros',label:'Bot strategy',format:value=>paperValue(value,m.value_unit)},{key:'hold_benchmark_micros',label:'If simply held',format:value=>paperValue(value,m.value_unit)}],'performance-values'));
}

function disposeChart(){
  if(!activeChart)return;
  activeChart.remove();activeChart=null;activeChartKey='';
}
function rememberChartRange(){
  const range=activeChart?.timeScale().getVisibleLogicalRange();
  if(range&&activeChartKey)chartRanges[activeChartKey]=range;
}
function chartSegments(points,key){
  const segments=[];let segment=[];
  const flush=()=>{if(segment.length)segments.push(segment);segment=[];};
  points.forEach(point=>{const time=chartTime(point.at);if(!chartPointAvailable(point,key)||!Number.isFinite(time)){flush();return;}segment.push({time,value:chartNumber(point[key])});});
  flush();return segments;
}
function addSegmentSeries(chart,points,key,style,label){
  const segments=chartSegments(points,key),tracked=[];
  segments.forEach((data,index)=>{const series=chart.addSeries(style.area?LightweightCharts.AreaSeries:LightweightCharts.LineSeries,{color:style.color,lineColor:style.color,topColor:style.topColor||'transparent',bottomColor:style.bottomColor||'transparent',lineWidth:2,lastValueVisible:index===segments.length-1,priceLineVisible:index===segments.length-1,crosshairMarkerVisible:true,priceFormat:{type:'price',precision:style.precision,minMove:10**-style.precision}});series.setData(data);tracked.push({series,label,tone:style.tone});});
  return tracked;
}
function chartOptions(){
  return {autoSize:true,layout:{attributionLogo:false,background:{type:LightweightCharts.ColorType.Solid,color:'#0d0d0d'},textColor:'#919191',fontFamily:'"Space Grotesk", ui-sans-serif, system-ui, sans-serif',fontSize:11},grid:{vertLines:{color:'transparent'},horzLines:{color:'#1f1f1f',style:LightweightCharts.LineStyle.Dashed}},crosshair:{mode:LightweightCharts.CrosshairMode.Normal,vertLine:{color:'#484848',labelBackgroundColor:'#1a1a1a'},horzLine:{color:'#484848',labelBackgroundColor:'#1a1a1a'}},rightPriceScale:{borderColor:'transparent',scaleMargins:{top:.12,bottom:.12}},timeScale:{borderColor:'transparent',timeVisible:true,secondsVisible:false,rightOffset:2,barSpacing:8},handleScroll:{mouseWheel:true,pressedMouseMove:true,horzTouchDrag:true,vertTouchDrag:false},handleScale:{axisPressedMouseMove:true,mouseWheel:true,pinch:true}};
}
function updateChartReadout(readout,tracked,param,unit,original){
  if(!param.time){readout.innerHTML=original;return;}
  const values=[];let strategy,hold;
  tracked.forEach(item=>{const datum=param.seriesData.get(item.series);if(!datum||datum.value===undefined)return;const micros=Math.round(datum.value*1000000);if(item.label==='Bot strategy')strategy=micros;if(item.label==='If simply held')hold=micros;values.push('<span><i class="legend-line '+safe(item.tone)+'" aria-hidden="true"></i>'+safe(item.label)+' <strong>'+safe(unit==='price'?price(micros):paperValue(micros,unit))+'</strong></span>');});
  if(strategy!==undefined&&hold!==undefined){const comparison=integer(strategy)-integer(hold);values.push('<span class="chart-comparison '+tone(comparison)+'">'+safe(versusHolding(strategy,hold,unit))+'</span>');}
  const observed='<time>'+safe(new Date(Number(param.time)*1000).toLocaleString([],{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}))+'</time>';
  readout.innerHTML=values.length?values.join('')+observed:'<span class="chart-unavailable">'+(unit==='price'?'Market price':'Paper values')+' unavailable</span>'+observed;
}
function mountMarketChart(m){
  disposeChart();
  const view=marketChartViews[m.name]==='performance'?'performance':'price',panel=document.querySelector('[data-chart-panel="'+view+'"]'),container=panel?.querySelector('[data-chart-canvas]');
  if(!container)return;
  if(!window.LightweightCharts){container.textContent='Interactive chart could not load. Exact values remain available below.';return;}
  const chart=LightweightCharts.createChart(container,chartOptions());activeChart=chart;activeChartKey=m.name+'/'+view;
  const points=view==='price'?chartHistory(m):(m.history||[]),precision=view==='price'?(integer(m.price_micros)>=1000000n?2:integer(m.price_micros)>=10000n?4:6):2;
  let tracked;
  if(view==='price')tracked=addSegmentSeries(chart,points,'price_micros',{area:true,color:'#14f195',topColor:'rgba(20,241,149,.28)',bottomColor:'rgba(20,241,149,0)',precision,tone:'green'},m.name+' price');
  else {const held=addSegmentSeries(chart,points,'hold_benchmark_micros',{color:'#787878',precision:2,tone:'muted'},'If simply held'),strategy=addSegmentSeries(chart,points,'equity_micros',{color:'#14f195',precision:2,tone:'green'},'Bot strategy');tracked=[...strategy,...held];}
  const anchors=points.filter(point=>Number.isFinite(chartTime(point.at))).map(point=>({time:chartTime(point.at)}));
  if(anchors.length){const anchor=chart.addSeries(LightweightCharts.LineSeries,{color:'transparent',lineVisible:false,lastValueVisible:false,priceLineVisible:false,crosshairMarkerVisible:false});anchor.setData(anchors);}
  const saved=chartRanges[activeChartKey];if(saved)chart.timeScale().setVisibleLogicalRange(saved);else chart.timeScale().fitContent();
  let pointerStart;
  container.addEventListener('pointerdown',event=>pointerStart={x:event.clientX,y:event.clientY});
  container.addEventListener('pointerup',event=>{if(pointerStart&&Math.hypot(event.clientX-pointerStart.x,event.clientY-pointerStart.y)>4)rememberChartRange();pointerStart=null;});
  container.addEventListener('wheel',()=>setTimeout(rememberChartRange),{passive:true});
  const readout=panel.querySelector('[data-chart-readout]'),original=readout.innerHTML;chart.subscribeCrosshairMove(param=>updateChartReadout(readout,tracked,param,view==='price'?'price':m.value_unit,original));
}
function adjustChart(action){
  if(!activeChart)return;
  const scale=activeChart.timeScale();
  if(action==='reset'){delete chartRanges[activeChartKey];scale.fitContent();return;}
  const range=scale.getVisibleLogicalRange();if(!range)return;
  const width=range.to-range.from,center=(range.from+range.to)/2,factor=action==='zoom-in'?.8:1.25;
  scale.setVisibleLogicalRange({from:center-width*factor/2,to:center+width*factor/2});
  rememberChartRange();
}
function moveChart(direction){
  if(!activeChart)return;
  const scale=activeChart.timeScale(),range=scale.getVisibleLogicalRange();if(!range)return;
  const offset=(range.to-range.from)*.12*direction;scale.setVisibleLogicalRange({from:range.from+offset,to:range.to+offset});
  rememberChartRange();
}

const tabs=[...document.querySelectorAll('.tab')],tablist=document.querySelector('.tabs'),desktopTabs=matchMedia('(min-width: 1024px)');
function updateTabOrientation(){tablist.setAttribute('aria-orientation',desktopTabs.matches?'vertical':'horizontal');}
updateTabOrientation();desktopTabs.addEventListener('change',updateTabOrientation);
function selectTab(button,focus=false){
  const changed=!button.classList.contains('active');
  tabs.forEach(item=>{const active=item===button;item.classList.toggle('active',active);item.setAttribute('aria-selected',String(active));item.tabIndex=active?0:-1;});
  document.querySelectorAll('.panel').forEach(panel=>{const active=panel.id===button.dataset.tab;panel.hidden=!active;panel.classList.toggle('active',active);});
  if(changed)window.scrollTo(0,0);
  if(focus)button.focus();
}
tabs.forEach((button,index)=>{button.addEventListener('click',()=>selectTab(button));button.addEventListener('keydown',event=>{let next;if(event.key==='ArrowRight'||event.key==='ArrowDown')next=(index+1)%tabs.length;else if(event.key==='ArrowLeft'||event.key==='ArrowUp')next=(index+tabs.length-1)%tabs.length;else if(event.key==='Home')next=0;else if(event.key==='End')next=tabs.length-1;else return;event.preventDefault();selectTab(tabs[next],true);});});
$('refresh').addEventListener('click',()=>load(true));
$('live').addEventListener('click',()=>{liveUpdates=!liveUpdates;$('live').setAttribute('aria-pressed',String(liveUpdates));$('live').textContent='Live updates: '+(liveUpdates?'On':'Paused');$('refresh-status').textContent=liveUpdates?'Live updates turned on':'Live updates paused';if($('notice').textContent.startsWith('Dashboard status is unavailable.'))setNotice('Dashboard status is unavailable. '+(liveUpdates?'It will retry automatically.':'Use Refresh to try again.'));if(liveUpdates&&!$('refresh').disabled)load();});
$('activity-filter').addEventListener('change',renderActivity);
$('open-order-history').addEventListener('click',()=>{$('activity-filter').value='orders';renderActivity();selectTab($('tab-activity'),true);});
$('markets').addEventListener('click',event=>{const action=event.target.closest('[data-chart-action]');if(action){adjustChart(action.dataset.chartAction);return;}const button=event.target.closest('.chart-toggle');if(!button)return;const card=button.closest('.market');marketChartViews[card.dataset.market]=button.dataset.chartView;renderMarkets();});
$('markets').addEventListener('keydown',event=>{if(!event.target.closest('[data-chart-canvas]'))return;if(event.key==='ArrowLeft'||event.key==='ArrowRight'){event.preventDefault();moveChart(event.key==='ArrowLeft'?-1:1);}else if(event.key==='+'||event.key==='='){event.preventDefault();adjustChart('zoom-in');}else if(event.key==='-'){event.preventDefault();adjustChart('zoom-out');}else if(event.key==='Home'){event.preventDefault();adjustChart('reset');}});
$('market-switcher').addEventListener('click',event=>{const button=event.target.closest('.market-choice');if(!button)return;selectedMarketName=button.dataset.market;renderMarkets();});
$('market-switcher').addEventListener('keydown',event=>{const buttons=[...event.currentTarget.querySelectorAll('.market-choice')],index=buttons.indexOf(event.target);if(index<0)return;let next;if(event.key==='ArrowRight'||event.key==='ArrowDown')next=(index+1)%buttons.length;else if(event.key==='ArrowLeft'||event.key==='ArrowUp')next=(index+buttons.length-1)%buttons.length;else if(event.key==='Home')next=0;else if(event.key==='End')next=buttons.length-1;else return;event.preventDefault();selectedMarketName=buttons[next].dataset.market;renderMarkets();$('market-switcher').querySelector('[data-market="'+CSS.escape(selectedMarketName)+'"]').focus();});
const helpDialog=$('help-dialog');
document.addEventListener('click',event=>{
  const planButton=event.target.closest('[data-plan-market]');
  if(planButton){const market=current?.markets?.find(item=>item.name===planButton.dataset.planMarket);if(market){helpReturn={attribute:'data-plan-market',value:market.name};openPlanDialog(market);}return;}
  const button=event.target.closest('[data-help-copy]');
  if(!button)return;
  helpReturn={attribute:'data-help-label',value:button.dataset.helpLabel};
  helpDialog.classList.remove('plan');
  $('help-dialog-kicker').textContent='Quick explanation';
  $('help-dialog-title').textContent=button.dataset.helpLabel;
  $('help-dialog-copy').textContent=button.dataset.helpCopy;
  $('help-dialog-visual').innerHTML=helpVisual(button.dataset.helpLabel);
  $('help-dialog-extra').innerHTML='';
  if(!helpDialog.open)helpDialog.showModal();
  $('help-dialog-title').focus({preventScroll:true});
});
helpDialog.addEventListener('click',event=>{if(event.target===helpDialog)helpDialog.close();});
helpDialog.addEventListener('close',()=>{helpDialog.classList.remove('plan');const target=helpReturn;helpReturn=null;if(target)requestAnimationFrame(()=>document.querySelector('['+target.attribute+'="'+CSS.escape(target.value)+'"]')?.focus());});
$('instruction-preference').addEventListener('change',()=>{instructionDirty=true;renderInstructionWarning();});
['instruction-capital','instruction-minimum-order','instruction-maximum-order','instruction-drawdown'].forEach(id=>$(id).addEventListener('input',()=>{instructionDirty=true;renderInstructionWarning();}));
$('instruction-cadence').addEventListener('change',()=>{instructionDirty=true;renderInstructionWarning();});
$('save-instruction').addEventListener('click',saveInstruction);
function help(label,text){return '<button class="help" type="button" data-help-label="'+safe(label)+'" data-help-copy="'+safe(text)+'" aria-label="Explain '+safe(label)+'">'+uiIcon('info')+'</button>';}
function metric(label,value,foot,klass='',explanation='',trend='',detail=''){
  const movement=trend?'<span class="metric-trend '+trend+'" aria-hidden="true">'+uiIcon(trend==='positive'?'up':trend==='negative'?'down':'flat')+'</span><span class="sr-only">'+(trend==='positive'?'Gain':trend==='negative'?'Loss':'No change')+'</span>':'';
  return '<article class="metric"><div class="metric-label"><span>'+safe(label)+'</span>'+help(label,explanation)+'</div><div class="metric-main">'+movement+'<strong class="metric-value '+klass+'">'+safe(value)+'</strong>'+(detail?'<span class="metric-percent '+klass+'">'+safe(detail)+'</span>':'')+'</div><span class="metric-foot">'+safe(foot)+'</span></article>';
}
function renderMetrics(){
	if(!current.complete){$('metrics').innerHTML=metric('Paper account now','—','Waiting for all markets','','Current value of all simulated cash and coins. It is not a real wallet balance.')+metric('Start of this run','—','Waiting for current data','','Combined simulated value when the active paper runs began.')+metric('Result this run','—','Waiting for current data','','Change across the active paper markets since these runs began.')+metric('Versus holding','—','Waiting for current data','','Compares the strategy with leaving the same starting assets untouched for this run.')+metric('Paper executions','—','Waiting for current data','','Completed simulated fills in this run.');return;}
  const o=current.overview||{};
	  const effective=effectiveEquity(o),pnl=effective-integer(o.opening_equity_micros);
	  const hold=effective-integer(o.hold_benchmark_micros);
	  const deficit=integer(o.deficit_micros),breakdown=o.accounting_tracked?'Booked '+signedAmount(o.realized_micros,o.value_unit)+' · Open '+signedAmount(o.unrealized_micros,o.value_unit)+(deficit?' · Liquidation deficit '+paperValue(deficit,o.value_unit):''):'Booked/open breakdown updating';
  const holdingText=signedAmount(hold,o.value_unit);
  const ready=(current.markets||[]).filter(m=>m.available&&m.ready),largest=ready.reduce((found,m)=>integer(m.opening_equity_micros)>integer(found?.opening_equity_micros||0)?m:found,null);
	  const accountFoot='All active simulated markets'+(largest?' · Largest market '+exposure(largest.opening_equity_micros,o.opening_equity_micros):'')+(deficit?' · '+paperValue(deficit,o.value_unit)+' liquidation deficit shown in the result':'');
  $('metrics').innerHTML=metric('Paper account now',paperValue(o.equity_micros,o.value_unit),accountFoot,'neutral','Current value of all simulated cash and coins. It is not a real wallet balance. Largest market shows how much of the starting paper account was assigned to one market.')+
	  metric('Start of this run',paperValue(o.opening_equity_micros,o.value_unit),'Opening paper value','neutral','Combined simulated value when the active paper runs began.')+
	  metric('Result this run',signedAmount(pnl,o.value_unit),breakdown,tone(pnl),'Change across every active paper market since these runs began, including any liquidation deficit. Booked includes closed trades, modeled fees, and applied funding; open is still marked to market.',tone(pnl),changePercent(o.opening_equity_micros,effective))+
    metric('Versus holding',holdingText,hold===0n?'Same result as holding':'Same starting assets',tone(hold),'Compares the strategy with leaving the same starting assets untouched.',tone(hold))+
	    metric('Paper executions',String(o.trades||0),paperValue(o.turnover_micros,o.value_unit)+' filled · '+paperValue(o.fees_micros,o.value_unit)+' costs','neutral','Completed simulated spot orders and perps fills. Total filled is activity, not profit.');
}
function marketCard(m){
	  if(!m.available){const ended=m.optional;return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge '+(ended?'amber':'red')+'">'+(ended?'Not active':'Unavailable')+'</span></div><p class="strategy-next">'+(ended?'This bounded experiment is not running':'Status is unavailable')+'</p><p class="market-context">'+(ended?'Core markets and their totals are unaffected.':'Combined totals are withheld until this required status returns.')+'</p></article>';}
  if(!m.ready)return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge amber">Updating</span></div><p class="strategy-next">Waiting for market data</p></article>';
  const feeBudgetUsed=m.fresh&&Boolean(m.fee_budget_tracked)&&!Number(m.estimated_fills_remaining||0);
  const status=marketStatus(m,feeBudgetUsed);
  const badge='<span class="badge '+status.tone+'">'+status.label+'</span>';
  const chartView=marketChartViews[m.name]==='performance'?'performance':'price';
  const chartSwitch='<div class="chart-switch" role="group" aria-label="'+safe(m.name)+' chart"><button class="chart-toggle '+(chartView==='price'?'active':'')+'" type="button" data-chart-view="price" aria-pressed="'+String(chartView==='price')+'">Market price</button><button class="chart-toggle '+(chartView==='performance'?'active':'')+'" type="button" data-chart-view="performance" aria-pressed="'+String(chartView==='performance')+'">Paper vs holding</button></div>';
  return '<article class="market" data-market="'+safe(m.name)+'"><div class="market-head"><div class="performance-title"><h3>Performance</h3><span class="asset-chip"><span aria-hidden="true">'+safe(m.name.slice(0,1))+'</span>'+safe(m.name)+'</span>'+badge+'</div></div><div class="market-chart-stage">'+chartSwitch+marketPriceChart(m,chartView!=='price')+performanceChart(m,chartView!=='performance')+'</div></article>';
}
function strategyView(m){
  const unavailable=!m.available;
	  const ended=m.optional&&(unavailable||(m.ready&&!m.fresh));
  const feeBudgetUsed=m.fresh&&Boolean(m.fee_budget_tracked)&&!Number(m.estimated_fills_remaining||0);
  const perps=isPerps(m),position=m.position_direction==='long'?'Price-up position open':m.position_direction==='short'?'Price-down position open':'No position open';
  return {
    unavailable,
    feeBudgetUsed,
	    label:ended?'Perps experiment ended':unavailable?'Unavailable':m.ready?(perps?'Perps paper experiment':m.strategy==='adaptive'?'Market-responsive paper plan':m.strategy||'Saved plan'):'Updating',
	    next:ended?'This bounded experiment is not active':unavailable?'Status source unavailable':!m.ready?'Waiting for status':!m.fresh?(m.optional?'This bounded experiment is not active':'Waiting for fresh prices'):m.risk_halted?(perps?'Simulation paused after liquidation':'New buys paused; sells can still reduce risk'):perps?position:feeBudgetUsed?'Orders paused until tomorrow; the simulated fee budget is used up':m.next_action?'Ready to '+m.next_action+' when the opportunity clears every limit':'Watching for the next opportunity',
	    status:ended?'Not active':unavailable?'Unavailable':m.ready&&!m.fresh&&m.optional?'Experiment ended':m.ready?state(m.state):'Status updating',
	trades:unavailable||!m.ready?'—':(m.trades||0)+' filled · '+attempts(m.signals).toLowerCase()+' '+(m.fresh?'this run':'in the last recorded run')
  };
}
function strategyCard(m){
  const view=strategyView(m);
	  const plan='<div class="strategy-plan"><span class="badge '+(m.optional&&(view.unavailable||!m.fresh)?'amber':view.unavailable?'red':!m.ready?'amber':'blue')+'">'+safe(view.label)+'</span>'+(view.unavailable||!m.ready?'':'<button class="plan-trigger" type="button" data-plan-market="'+safe(m.name)+'" aria-haspopup="dialog">View visual plan'+uiIcon('arrow')+'</button>')+'</div>';
  return '<article class="strategy-market-row"><div class="strategy-market-name"><span class="asset-orb" aria-hidden="true">'+safe(m.name.slice(0,1))+'</span><div><h3>'+safe(m.name)+'</h3><span>'+safe(view.status)+'</span></div></div><p class="strategy-next">'+safe(view.next)+(view.unavailable||!m.ready?'':'<small>'+safe(view.trades)+'</small>')+'</p>'+plan+'</article>';
}
function openPlanDialog(m){
  if(isPerps(m)){openPerpsPlanDialog(m);return;}
  const view=strategyView(m),lotValue=initialLotValue(m),lotShare=ratio(lotValue,m.opening_equity_micros),lotShareText=lotShare.toFixed(1)+'%';
  const firstOrder=lotValue?paperValue(lotValue,m.value_unit):'Updating';
  const lot=m.initial_lot_units?assetAmount(m.initial_lot_units,m.initial_lot_decimals,m.initial_lot_asset):'Updating';
  const reserve=m.fee_reserve_lamports?assetAmount(m.fee_reserve_lamports,9,'SOL'):'None';
  const feeReserveLeft=m.fee_budget_tracked?assetAmount(m.remaining_fee_reserve_lamports||0,9,'SOL'):'Not tracked';
	const feeLeft=m.fee_budget_tracked?(m.estimated_fills_remaining?String(m.estimated_fills_remaining)+' estimated orders':'No more orders this run'):'Not tracked';
  const ordersLeft=m.fee_budget_tracked?String(m.estimated_fills_remaining||0):'—';
	const orderRange=m.minimum_order_value_micros&&m.maximum_order_value_micros?paperValue(m.minimum_order_value_micros,m.value_unit)+'–'+paperValue(m.maximum_order_value_micros,m.value_unit):'Uses simulated proceeds';
  const lossPause=m.max_drawdown_bps?percent(m.max_drawdown_bps):'Not set';
  const cadence=m.tick_seconds?duration(m.tick_seconds):'Updating';
  const reason=String(m.decision_reason||''),protecting=view.feeBudgetUsed||m.risk_halted||['drawdown_limit','risk_halt','drawdown_halt','volatility_limit','route_cost_limit'].includes(reason);
  const watching=!m.fresh||['collecting_history','watching','data_unavailable','cooldown'].includes(reason);
  const deciding=['order_pending','order_filled','trend_aligned_buy','trend_aligned_sell','range_high_sell','range_low_buy'].includes(reason);
  const stage=protecting?3:watching?0:deciding?2:1;
  const steps=[['watch','Watch','Prices'],['score','Score','Costs'],['decide','Decide','Buy / sell'],['protect','Protect','Limits']];
  $('help-dialog-kicker').textContent='Live paper plan';
  $('help-dialog-title').textContent=m.name;
  $('help-dialog-copy').textContent=view.next;
  $('help-dialog-visual').innerHTML='<ol class="plan-loop" aria-label="Current plan stage">'+steps.map((step,index)=>'<li class="plan-node'+(index===stage?' active':'')+'">'+uiIcon(step[0])+'<span><strong>'+step[1]+'</strong><small>'+step[2]+'</small></span></li>').join('')+'</ol>';
  $('help-dialog-extra').innerHTML='<div class="plan-snapshot">'+
    '<article>'+uiIcon('wallet')+'<span>First order</span><strong>'+safe(firstOrder)+'</strong><small>'+safe(lotShareText)+' of this market</small></article>'+
    '<article>'+uiIcon('gauge')+'<span>Loss pause</span><strong>'+safe(lossPause)+'</strong><small>Below the session high</small></article>'+
    '<article>'+uiIcon('clock')+'<span>Checks</span><strong>'+safe(cadence)+'</strong><small>Does not force a trade</small></article>'+
    '<article>'+uiIcon('order')+'<span>Orders left</span><strong>'+safe(ordersLeft)+'</strong><small>Estimated this session</small></article>'+
    '</div><div class="plan-allocation"><div><span>First order compared with market capital</span><strong>'+safe(lotShareText)+'</strong></div><progress class="plan-meter" max="100" value="'+safe(lotShare.toFixed(1))+'" aria-label="First paper order uses '+safe(lotShareText)+' of this market\'s starting paper capital">'+safe(lotShareText)+'</progress></div>'+
    '<details class="plan-more"><summary>All safeguards and timings</summary><dl class="limit-grid">'+
    '<div><dt>Market started with</dt><dd>'+safe(paperValue(m.opening_equity_micros,m.value_unit))+'</dd></div>'+
    '<div><dt>Starting trade lot</dt><dd>'+lot+' · '+safe(firstOrder)+'</dd></div>'+
	'<div><dt>Active order range</dt><dd>'+safe(orderRange)+'</dd></div>'+
    '<div><dt>Total traded this session</dt><dd>'+safe(paperValue(m.turnover_micros,m.value_unit))+'</dd></div>'+
    '<div><dt>Modeled fees this session</dt><dd>'+safe(paperValue(m.fees_micros,m.value_unit))+'</dd></div>'+
    '<div><dt>Fee reserve</dt><dd>'+reserve+'</dd></div>'+
    '<div><dt>Fee budget left</dt><dd>'+feeReserveLeft+'</dd></div>'+
    '<div><dt>Orders left this session</dt><dd>'+safe(feeLeft)+'</dd></div>'+
    '<div><dt>Minimum opportunity</dt><dd>'+safe(m.minimum_signal_bps?percent(m.minimum_signal_bps):'Not set')+'</dd></div>'+
    '<div><dt>After a fill</dt><dd>Wait '+safe(duration(m.cooldown_seconds))+'</dd></div>'+
    '<div><dt>Scoring delay</dt><dd>'+safe(m.settle_seconds?duration(m.settle_seconds):'Updating')+'</dd></div>'+
    '<div><dt>Slippage ceiling</dt><dd>'+safe(m.slippage_bps?percent(m.slippage_bps):'Not set')+'</dd></div>'+
    '<div><dt>Route impact ceiling</dt><dd>'+safe(m.max_quote_impact_bps?percent(m.max_quote_impact_bps):'Not set')+'</dd></div>'+
    '<div><dt>Volatility pause</dt><dd>'+safe(m.max_volatility_bps?percent(m.max_volatility_bps):'Not set')+'</dd></div>'+
    '<div><dt>Price memory</dt><dd>'+safe(m.fast_window&&m.slow_window?m.fast_window+' / '+m.slow_window+' checks':'Not set')+'</dd></div></dl><p class="plan-footnote">More paper capital changes possible gains and losses, not strategy quality.</p></details>'+
    '<p class="plan-reason"><span>'+uiIcon('score')+'</span><span><strong>Why this stage</strong>'+safe(decisionReason(m.decision_reason))+'</span></p>';
  helpDialog.classList.add('plan');
  if(!helpDialog.open)helpDialog.showModal();
  $('help-dialog-title').focus({preventScroll:true});
}
function openPerpsPlanDialog(m){
  const leverage=Number(m.leverage_bps||10000)/10000;
  const profile=String(m.risk_profile||'bounded').replace(/^./,character=>character.toUpperCase());
  const position=m.position_direction==='long'?'Price-up position':m.position_direction==='short'?'Price-down position':'No position';
	  const positionTone=!m.fresh?'Last recorded state':m.position_direction==='flat'?'Waiting for a signal':'Open in this simulation';
	  $('help-dialog-kicker').textContent=m.fresh?'Live perps simulation':'Last perps experiment';
  $('help-dialog-title').textContent=m.name;
  $('help-dialog-copy').textContent=position+'. Public market data only; no wallet or real order is connected.';
	  const stage=m.risk_halted?3:!m.fresh?0:m.position_direction==='flat'?1:3;
  const steps=[['watch','Read','Closed 1m candle'],['score','Choose','Up · down · wait'],['decide','Simulate','Visible book fill'],['protect','Track','Mark · funding · risk']];
  $('help-dialog-visual').innerHTML='<ol class="plan-loop" aria-label="Current perps stage">'+steps.map((step,index)=>'<li class="plan-node'+(index===stage?' active':'')+'">'+uiIcon(step[0])+'<span><strong>'+step[1]+'</strong><small>'+step[2]+'</small></span></li>').join('')+'</ol>';
  $('help-dialog-extra').innerHTML='<div class="plan-snapshot">'+
    '<article>'+uiIcon('wallet')+'<span>Paper collateral</span><strong>'+safe(paperValue(m.opening_equity_micros,m.value_unit))+'</strong><small>This market only</small></article>'+
    '<article>'+uiIcon('gauge')+'<span>Risk setting</span><strong>'+safe(profile)+'</strong><small>Isolated experiment</small></article>'+
    '<article>'+uiIcon('score')+'<span>Simulated leverage</span><strong>'+safe(leverage.toFixed(2).replace(/\.00$/,''))+'×</strong><small>No borrowed real funds</small></article>'+
	    '<article>'+uiIcon('clock')+'<span>New decision</span><strong>Every minute</strong><small>Uses completed candles</small></article>'+
    '</div><details class="plan-more"><summary>Current accounting and boundaries</summary><dl class="limit-grid">'+
    '<div><dt>Position</dt><dd>'+safe(position)+' · '+safe(positionTone)+'</dd></div>'+
    '<div><dt>Paper value now</dt><dd>'+safe(paperValue(m.equity_micros,m.value_unit))+'</dd></div>'+
	    '<div><dt>Result this run</dt><dd>'+safe(signedAmount(effectiveEquity(m)-integer(m.opening_equity_micros),m.value_unit))+'</dd></div>'+
	    (integer(m.deficit_micros)?'<div><dt>Liquidation deficit</dt><dd>'+safe(paperValue(m.deficit_micros,m.value_unit))+'</dd></div>':'')+
    '<div><dt>Open result</dt><dd>'+safe(signedAmount(m.unrealized_micros,m.value_unit))+'</dd></div>'+
    '<div><dt>Funding</dt><dd>'+safe(m.funding_tracked?signedAmount(m.funding_micros,m.value_unit):'Updating')+'</dd></div>'+
    '<div><dt>Modeled fees</dt><dd>'+safe(paperValue(m.fees_micros,m.value_unit))+'</dd></div>'+
	    '<div><dt>Visible-book fills</dt><dd>'+safe(String(m.trades||0))+'</dd></div>'+
    '<div><dt>Real execution</dt><dd>Disabled</dd></div></dl><p class="plan-footnote">A position opens immediately only when the simulated visible book can fill it within the bounded model. There is no separate pending exchange order to report.</p></details>';
  helpDialog.classList.add('plan');
  if(!helpDialog.open)helpDialog.showModal();
  $('help-dialog-title').focus({preventScroll:true});
}
function experimentDefaults(){
  const plans=(current?.markets||[]).filter(m=>m.available&&m.ready&&m.instruction_sha256),first=plans[0];
  const saved=Number(current?.instruction?.version)===4?current.instruction:null;
  const activeCapital=Number(plans.reduce((total,plan)=>total+integer(plan.opening_equity_micros),0n));
  const activeMaximum=Number(plans.reduce((largest,plan)=>{const value=integer(plan.maximum_order_value_micros||0)||initialLotValue(plan);return value>largest?value:largest;},0n));
  const activeMinimum=Number(plans.reduce((smallest,plan)=>{const value=integer(plan.minimum_order_value_micros||0);return value&&(smallest===0n||value<smallest)?value:smallest;},0n));
  const capital=Number(saved?.paper_capital_micros||activeCapital||100000000);
  const maximum=Number(saved?.maximum_order_micros||activeMaximum||Math.min(capital,25000000));
  const minimum=Number(saved?.minimum_order_micros||activeMinimum||Math.min(maximum,Math.max(1000000,Math.floor(maximum/4))));
  return {paper_capital_micros:capital,minimum_order_micros:minimum,maximum_order_micros:maximum,cadence_seconds:Number(saved?.cadence_seconds||first?.tick_seconds||60),max_drawdown_bps:Number(saved?.max_drawdown_bps||first?.max_drawdown_bps||300)};
}
const inputMicros=id=>Math.round(Number($(id).value||0)*1000000);
const inputDollars=micros=>(Number(micros||0)/1000000).toFixed(2).replace(/\.00$/,'');
function instructionRequest(){
  return {market:'all',preference:$('instruction-preference').value,paper_capital_micros:inputMicros('instruction-capital'),minimum_order_micros:inputMicros('instruction-minimum-order'),maximum_order_micros:inputMicros('instruction-maximum-order'),cadence_seconds:Number($('instruction-cadence').value),max_drawdown_bps:Math.round(Number($('instruction-drawdown').value||0)*100)};
}
function validInstructionRequest(request){return ['balanced','more-opportunities','more-selective'].includes(request.preference)&&Number.isSafeInteger(request.paper_capital_micros)&&request.paper_capital_micros>=10000000&&request.paper_capital_micros<=1000000000000&&Number.isSafeInteger(request.minimum_order_micros)&&request.minimum_order_micros>=1000000&&request.minimum_order_micros<=request.maximum_order_micros&&Number.isSafeInteger(request.maximum_order_micros)&&request.maximum_order_micros<=request.paper_capital_micros&&[5,15,30,60,300].includes(request.cadence_seconds)&&Number.isInteger(request.max_drawdown_bps)&&request.max_drawdown_bps>=10&&request.max_drawdown_bps<=5000;}
function renderActiveLimits(){
  const markets=(current?.markets||[]).filter(m=>m.available&&m.ready&&m.instruction_sha256);
  const accountCapital=markets.reduce((total,market)=>total+integer(market.opening_equity_micros),0n);
  $('active-limit-list').innerHTML=markets.length?markets.map(m=>{const lot=initialLotValue(m),marketShare=exposure(m.opening_equity_micros,accountCapital),lotShare=exposure(lot,m.opening_equity_micros),range=m.minimum_order_value_micros&&m.maximum_order_value_micros?paperValue(m.minimum_order_value_micros,m.value_unit)+'–'+paperValue(m.maximum_order_value_micros,m.value_unit):'First leg '+(lot?paperValue(lot,m.value_unit):'updating');return '<div class="active-limit"><strong>'+safe(m.name)+'</strong><span>'+safe(paperValue(m.opening_equity_micros,m.value_unit))+' · '+safe(marketShare)+' of the starting account</span><small>Active order range '+safe(range)+' · first leg '+safe(lot?paperValue(lot,m.value_unit):'updating')+' ('+safe(lotShare)+') · checks '+safe(m.tick_seconds?'every '+duration(m.tick_seconds):'updating')+' · '+safe(paperValue(m.turnover_micros,m.value_unit))+' traded this session</small></div>';}).join(''):'<span class="market-context">Waiting for current paper-plan limits.</span>';
}
function renderInstructionWarning(){
  const warning=$('instruction-warning'),request=instructionRequest();
  const valid=validInstructionRequest(request);$('save-instruction').disabled=!valid;
  if(!valid){warning.textContent='Use at least $10 total, at least $1 per order, and keep the smallest order at or below the largest order and total budget. Choose a loss stop between 0.1% and 50%.';return;}
  warning.textContent='This saves a paper-only request. It becomes active only after a fresh isolated plan passes validation; saving alone never changes a running plan.';
}
function renderInstruction(){
	const card=$('research-instruction');
	card.hidden=!current.instructions_enabled;
	card.style.display=card.hidden?'none':'';
	if(card.hidden)return;
  renderActiveLimits();
  if(instructionDirty){renderInstructionWarning();return;}
  const saved=current.instruction;
  $('instruction-preference').value=saved?.preference||'balanced';
  const limits=experimentDefaults();
  const sized=Number(saved?.version)===4?saved:limits;
  $('instruction-capital').value=inputDollars(sized.paper_capital_micros);
  $('instruction-minimum-order').value=inputDollars(sized.minimum_order_micros);
  $('instruction-maximum-order').value=inputDollars(sized.maximum_order_micros);
  $('instruction-cadence').value=String(sized.cadence_seconds);
  $('instruction-drawdown').value=(Number(sized.max_drawdown_bps||0)/100).toFixed(2).replace(/0+$/,'').replace(/\.$/,'');
  $('save-instruction').disabled=!validInstructionRequest(instructionRequest());
	$('instruction-status').textContent=current.instruction_error?'Saved paper request is unavailable and will not be used.':current.instruction_active?'This paper setup is active in the configurable spot markets. Independent paper markets keep their own test setup.':saved&&Number(saved.version)===4?'Saved. The configurable spot paper services are validating and applying this setup.':saved?'Older research goal loaded. Save once to bind it to a new paper setup.':'No paper request saved yet.';
  renderInstructionWarning();
}
async function saveInstruction(){
  const button=$('save-instruction'),status=$('instruction-status');
  if(!current){status.textContent='Wait for the first dashboard update, then try again.';return;}
  const wanted=instructionRequest();
  if(!validInstructionRequest(wanted)){status.textContent='Nothing was saved. Fix the highlighted limits first.';renderInstructionWarning();return;}
  button.disabled=true;button.textContent='Saving…';status.textContent='Saving the next paper-experiment request…';
  try{
    const response=await fetch('/api/v1/instruction',{method:'POST',headers:{'Content-Type':'application/json','X-Mithril-Paper-Request':'1'},body:JSON.stringify(wanted)});
    if(!response.ok)throw new Error('save failed');
    current.instruction=await response.json();current.instruction_error=false;current.instruction_active=false;instructionDirty=false;renderInstruction();
  }catch(error){status.textContent='Could not save. The active paper plans were not changed.';}
  finally{button.disabled=false;button.textContent='Save experiment request';}
}
function disposeMarketSparklines(){marketSparklines.forEach(chart=>chart.remove());marketSparklines=[];}
function mountMarketSparklines(markets){
  disposeMarketSparklines();
  if(!window.LightweightCharts)return;
  document.querySelectorAll('[data-market-sparkline]').forEach(container=>{
    const market=markets.find(item=>item.name===container.dataset.marketSparkline),segments=market?chartSegments(market.history||[],'equity_micros'):[],data=segments.at(-1)||[];
    if(data.length<2){container.textContent='—';return;}
    const rising=data.at(-1).value>=data[0].value,color=rising?'#5fffaf':'#ff5d68';
    const chart=LightweightCharts.createChart(container,{autoSize:true,height:40,layout:{attributionLogo:false,background:{type:LightweightCharts.ColorType.Solid,color:'transparent'},textColor:'transparent'},grid:{vertLines:{visible:false},horzLines:{visible:false}},leftPriceScale:{visible:false},rightPriceScale:{visible:false},timeScale:{visible:false},crosshair:{mode:LightweightCharts.CrosshairMode.Hidden},handleScroll:false,handleScale:false});
    const series=chart.addSeries(LightweightCharts.AreaSeries,{lineColor:color,topColor:rising?'rgba(95,255,175,.20)':'rgba(255,93,104,.18)',bottomColor:'transparent',lineWidth:2,lastValueVisible:false,priceLineVisible:false,crosshairMarkerVisible:false});
    series.setData(data.slice(-30));chart.timeScale().fitContent();marketSparklines.push(chart);
  });
}
function renderMarkets(){
  const openChartDetail=$('markets').querySelector('.chart-data[open]')?.dataset.detail;
  const focusedChartCanvas=Boolean(document.activeElement?.closest('[data-chart-canvas]'));
  const focusedChoice=document.activeElement?.closest('.market-choice')?.dataset.market;
  const focused=document.activeElement?.closest('.chart-toggle');
  const focusMarket=focused?.closest('.market')?.dataset.market,focusView=focused?.dataset.chartView;
  if(!current.markets.some(market=>market.name===selectedMarketName))selectedMarketName=current.markets.find(market=>market.available&&market.ready)?.name||current.markets[0]?.name||'';
	  const marketHeading='<div class="market-list-head" role="presentation"><span>Market</span><span aria-label="Paper value trend"></span><span>Fills</span><span>Market price</span><span>Started</span><span>Current</span><span>Paper P&L</span></div>';
  $('market-switcher').innerHTML=marketHeading+current.markets.map(market=>{
    const active=market.name===selectedMarketName,ready=market.available&&market.ready;
    const feeBudgetUsed=ready&&market.fresh&&Boolean(market.fee_budget_tracked)&&!Number(market.estimated_fills_remaining||0);
	    const status=!market.available?{label:market.optional?'Not active':'Unavailable',tone:market.optional?'amber':'red'}:!market.ready?{label:'Updating',tone:'amber'}:marketStatus(market,feeBudgetUsed);
	    const pnl=ready?effectiveEquity(market)-integer(market.opening_equity_micros):0n;
    const plan=ready?(isPerps(market)?'Perps paper experiment':market.strategy==='adaptive'?'Adaptive paper plan':market.strategy==='fixed'?'Fixed paper plan':'Paper plan'):'Waiting for data';
    const arrow=!ready?'':pnl===0n?'→':pnl<0n?'↓':'↑';
	    return '<button class="market-choice'+(active?' active':'')+'" type="button" role="tab" data-market="'+safe(market.name)+'" aria-label="Show '+safe(market.name)+' paper market" aria-selected="'+String(active)+'" aria-controls="markets" tabindex="'+(active?'0':'-1')+'"><span class="market-choice-name"><strong>'+safe(market.name)+'</strong><small class="'+status.tone+'">'+safe(status.label)+' · '+safe(plan)+'</small></span><span class="market-sparkline" data-market-sparkline="'+safe(market.name)+'" role="img" aria-label="Recent '+safe(market.name)+' paper-value trend">'+(ready?'':'—')+'</span><span class="market-choice-value">'+safe(ready?String(market.trades||0):'—')+'</span><span class="market-choice-value">'+safe(ready&&market.price_micros?price(market.price_micros):'—')+'</span><span class="market-choice-value">'+safe(ready?paperValue(market.opening_equity_micros,market.value_unit):'—')+'</span><span class="market-choice-value">'+safe(ready?paperValue(market.equity_micros,market.value_unit):'—')+'</span><span class="market-choice-value '+tone(pnl)+'"><span class="return-arrow" aria-hidden="true">'+arrow+'</span>'+safe(ready?resultWithDeficit(market):'—')+'</span></button>';
  }).join('');
  mountMarketSparklines(current.markets);
  const selected=current.markets.find(market=>market.name===selectedMarketName);
  disposeChart();
  $('markets').innerHTML=selected?marketCard(selected):'<div class="empty">No paper markets configured.</div>';
  if(openChartDetail)$('markets').querySelector('.chart-data[data-detail="'+CSS.escape(openChartDetail)+'"]')?.setAttribute('open','');
  if(selected?.available&&selected.ready)mountMarketChart(selected);
  if(focusedChoice)$('market-switcher').querySelector('[data-market="'+CSS.escape(focusedChoice)+'"]')?.focus({preventScroll:true});
  if(focusMarket&&focusView){[...document.querySelectorAll('.market')].find(card=>card.dataset.market===focusMarket)?.querySelector('[data-chart-view="'+focusView+'"]')?.focus({preventScroll:true});}
  if(focusedChartCanvas)$('markets').querySelector('[data-chart-canvas]')?.focus({preventScroll:true});
  $('strategy-markets').innerHTML='<div class="strategy-list-head" aria-hidden="true"><span>Market</span><span>Current decision</span><span>Plan</span></div>'+current.markets.map(strategyCard).join('');
  renderInstruction();
}
function activityUSD(amount){
  const value=Number(amount);
  if(!Number.isFinite(value))return '$'+amount;
  return value>0&&value<.005?'<$0.01':'$'+value.toFixed(2);
}
const compactActivityDollars=text=>text.replace(/\$([0-9]+(?:\.[0-9]+)?)/g,(_,amount)=>activityUSD(amount));
function readableActivityResult(line){
  const value=line.match(/^(?:This market value|Paper value now): \$([0-9]+(?:\.[0-9]+)?)$/);
  if(value)return 'Paper value now: '+activityUSD(value[1]);
  const result=line.match(/^((?:This market(?:'s result)? (?:today|gain\/loss)|Today's result after trade|Paper result this run|Paper gain\/loss(?: today)?):) (up|down) \$([0-9]+(?:\.[0-9]+)?)(.*)$/);
  if(!result)return line;
  const profit=result[2]==='up';
  return result[1]+' '+(profit?'🟢 ▲ ':'🔴 ▼ ')+activityUSD(result[3])+' ('+(profit?'profit':'loss')+')'+compactActivityDollars(result[4]).replaceAll(' better than holding',' ahead of holding').replaceAll(' worse than holding',' behind holding');
}
function readableActivity(message){
  const lines=String(message||'').split('\n');
  lines[0]=(lines[0]||'').replace(/SELL filled/i,'SOLD').replace(/BUY filled/i,'BOUGHT');
  for(let index=1;index<lines.length;index++){
    lines[index]=readableActivityResult(lines[index]
      .replace('Practice account:','This market value:').replace('Total paper account:','This market value:').replace(/^Equity /,'This market value: ')
      .replace(/^Paper value /,'This market value: ').replace(/^Result:/,'This market gain/loss:')
	      .replace('Gain/loss today:',"Plan result at that update:").replace("Today's result:","Plan result at that update:").replace("Today's estimated paper value:","Plan result at that update:").replace('Paper result this run:',"Plan result at that update:")
      .replaceAll('better than no trading','better than holding').replaceAll('worse than no trading','worse than holding').replaceAll('same as no trading','same as holding')
      .replace(/\b1 trade\b/g,'1 filled paper order').replace(/\b(\d+) trades\b/g,'$1 filled paper orders')
      .replace('Versus no trading:','Compared with holding:').replace('Compared with no trading:','Compared with holding:'));
  }
  if(/\b(?:BOUGHT|SOLD)\b/.test(lines[0]))for(let index=1;index<lines.length;index++)lines[index]=lines[index].replace('This market value:','Paper value now:').replace("This market's result today:","Today's result after trade:");
  if(lines[1]?.includes(' → ')){
    const [movement,...rest]=lines[1].split(' · '),[from,to]=movement.split(' → ');
    if(from&&to){const sell=/\bSOLD\b/i.test(lines[0]);lines.splice(1,1,(sell?'Sold ':'Paid ')+from,(sell?'Received ':'Bought ')+to,...rest);}
  }
  return lines.map(line=>compactActivityDollars(line)
    .replace(/^Price data ([0-9.]+)% · some data missing$/,'Not enough price information · $1% available')
    .replace(/^Price data ([0-9.]+)%$/,'Price information available: $1%')
    .replace(/(\d+\.\d{2})\d*(?=\s+(?:USD|USDC)\b)/g,'$1')
    .replace(/(\d+\.\d{4})\d*(?=\s+[A-Z][A-Z0-9]{1,9}\b)/g,'$1')).join('\n');
}
function activityResultMarkup(result,resultTone){
  const split=result.indexOf(': '),rawLabel=split>=0?result.slice(0,split):'Result',label=rawLabel.replace("Today's result after trade",'Market P&L after trade').replace("This market's result today",'Market P&L today').replace('This market gain/loss','Market P&L').replace('Paper gain/loss','Market P&L'),raw=split>=0?result.slice(split+2):result;
  const value=raw.replace(/🟢\s*▲\s*|🔴\s*▼\s*/g,'').replace(/^up\s+/i,'').replace(/^down\s+/i,'');
  const icon=resultTone==='positive'?'up':resultTone==='negative'?'down':'flat';
  return '<span class="activity-result-label">'+safe(label)+'</span><span class="activity-result-value">'+uiIcon(icon)+'<span>'+safe(value)+'</span></span>';
}
function renderActivity(){
  if(!current)return;
  const openDetails=new Set([...document.querySelectorAll('#activity-list .activity-more[open]')].map(detail=>detail.dataset.detail));
  const filter=$('activity-filter').value;
  const important=new Set(['order_opened','order_filled','strategy_active','strategy_changed','risk_halted','data_unavailable','data_restored','period_closed']);
  const items=current.activity.filter(item=>filter==='all'||filter==='important'&&important.has(item.kind)||eventGroup(item.kind)===filter);
  const opened=current.activity.filter(item=>item.kind==='order_opened').length,filled=current.activity.filter(item=>item.kind==='order_filled').length;
  const omitted=Number(current.activity_omitted||0);
  $('activity-summary').textContent=items.length+' shown · Recent retained totals: '+opened+' opened, '+filled+' filled'+(omitted?' · '+omitted+' older events omitted':'');
  $('activity-list').innerHTML=items.length?items.map(item=>{
    const lines=readableActivity(item.message).split('\n');
    const title=(lines.shift()||item.kind).replace(/^PAPER · /,'').replace(/^[^A-Z0-9]+/i,'');
    const resultIndex=lines.findIndex(line=>/(?:result|gain\/loss|profit|loss)/i.test(line));
	    const result=resultIndex>=0?lines.splice(resultIndex,1)[0]:item.kind==='order_opened'?'Waiting to fill':item.kind==='order_filled'?'Paper fill recorded':'—';
    const summary=lines.splice(0,2).join(' · ')||'Recorded by the paper agent';
    const detailKey=item.at+'|'+item.kind+'|'+item.market;
    const more=lines.length?'<details class="activity-more" data-detail="'+safe(detailKey)+'"><summary>More details</summary><p>'+safe(lines.join('\n'))+'</p></details>':'';
    const resultTone=/profit|🟢|▲|\bup\b/i.test(result)?'positive':/loss|🔴|▼|\bdown\b/i.test(result)?'negative':'';
    return '<article class="activity-item"><div class="activity-event"><span class="event-mark '+safe(item.kind)+'" aria-hidden="true">'+activityIcon(item.kind)+'</span><div><span>'+safe(item.market)+'</span><h3>'+safe(title)+'</h3></div></div><div class="activity-copy"><p>'+safe(summary)+'</p>'+more+'</div><p class="activity-result '+resultTone+'">'+activityResultMarkup(result,resultTone)+'</p><time class="activity-time" datetime="'+safe(item.at)+'">'+safe(eventTime(item.at))+'</time></article>';
  }).join(''):'<div class="empty">No matching activity yet.</div>';
  openDetails.forEach(detail=>$('activity-list').querySelector('.activity-more[data-detail="'+CSS.escape(detail)+'"]')?.setAttribute('open',''));
}
function automationCard(klass,symbol,title,label,toneName,description){return '<article class="automation-card '+klass+'"><div class="automation-name"><span class="role-symbol" aria-hidden="true">'+safe(symbol)+'</span><h3>'+safe(title)+'</h3></div><p>'+safe(description)+'</p><span class="badge '+toneName+'">'+safe(label)+'</span></article>';}
function researchView(){
  if(!current.research_enabled)return {label:'Not connected',tone:'amber',description:'No validated Hermes packet path is configured.',detail:'Hermes remains outside the trading and wallet boundary.'};
  if(current.research_error)return {label:'Rejected output',tone:'red',description:'The latest output did not pass the agent packet checks.',detail:'No proposal or policy change was accepted.'};
  const packet=current.research;
  if(!packet)return {label:'No valid run yet',tone:'amber',description:'Waiting for the first validated source-cited packet.',detail:'The paper plans continue without Hermes input.'};
  const checked=packet.sources_checked+' unique source'+(packet.sources_checked===1?'':'s')+' checked';
	const retrieved=packet.retrieved_pages+' page'+(packet.retrieved_pages===1?'':'s')+' retrieved from '+packet.successful_web_searches+' successful search'+(packet.successful_web_searches===1?'':'es');
	  const outcomes=packet.two_source_claims+' two-source Hermes claim'+(packet.two_source_claims===1?'':'s')+' · '+packet.retrieved_citations+' retrieved citation'+(packet.retrieved_citations===1?'':'s')+' · '+packet.single_source_facts+' single-source · '+packet.contradicted_facts+' contradicted · '+packet.unverified_facts+' unverified';
  const evidence=retrieved+'; '+checked+'; '+outcomes;
  if(!packet.current)return {label:'Expired',tone:'amber',description:packet.market+' research expired. '+evidence+'.',detail:'It cannot be used for a new paper experiment.'};
  const passed=packet.risk_decision==='pass';
  const label=packet.disposition==='candidate'&&packet.actionable?'Proposal ready':packet.disposition==='blocked'?'Vetoed':'No change';
  const tone=packet.disposition==='candidate'&&packet.actionable?'blue':packet.disposition==='blocked'?'red':'green';
  const changes=(packet.proposed_changes||[]).map(change=>change.name.replaceAll('_',' ')+' '+change.current+' → '+change.proposed).join(' · ');
	  return {label,tone,description:packet.market+' · '+evidence+' · '+age(packet.created_at)+'.',detail:'Hermes reported its risk decision as '+(passed?'pass':'reject')+': '+packet.risk_reason+(changes?' Proposed only: '+changes+'.':'')+' Deterministic replay gates decide whether any paper plan may change.'};
}
function mithrilEvidenceView(){
  if(!current.mithril_evidence_enabled)return {label:'Not connected',tone:'amber',description:'No host-produced Mithril evidence status is configured.'};
  if(current.mithril_evidence_error)return {label:'Invalid status',tone:'red',description:'The latest Mithril evidence status could not be verified.'};
  const evidence=current.mithril_evidence;
  if(!evidence)return {label:'Not checked yet',tone:'amber',description:'Waiting for the first host-verified Hermes evidence check.'};
  if(evidence.available_at_check)return {label:'Available at check',tone:'green',description:'The rooted index passed the '+duration(evidence.max_record_age_seconds)+' freshness gate when Hermes started · '+age(evidence.checked_at)+'.'};
  return {label:'Withheld',tone:'amber',description:'No rooted index passed the freshness gate when Hermes started · '+age(evidence.checked_at)+'. Hermes could not use Mithril evidence in that run.'};
}
function renderSystem(){
  const required=current.markets.filter(market=>!market.optional),healthy=required.filter(marketDataHealthy).length,total=required.length;
  const experiments=current.markets.filter(market=>market.optional),activeExperiments=experiments.filter(marketDataHealthy).length;
  const marketNames=current.markets.map(m=>m.name).join(', ')||'none configured';
	const research=researchView();
  const mithril=mithrilEvidenceView();
  $('automation').innerHTML='<div class="automation-list-head" aria-hidden="true"><span>Service</span><span>Role and boundary</span><span>Status</span></div>'+
    automationCard('engines','BOT','Paper engines',healthy===total&&total?'Running':'Needs attention',healthy===total&&total?'green':'amber',healthy+' of '+total+' core market observers are current. '+activeExperiments+' of '+experiments.length+' optional perps experiments are active: '+marketNames+'.')+
    automationCard('hermes','H','Nous Hermes',research.label,research.tone,research.description)+
    automationCard('mithril','M','Mithril evidence',mithril.label,mithril.tone,mithril.description)+
    automationCard('strategy','AD','Versioned learning','Gate required','blue','Spot rules adapt on current prices. Perps strategies are compared on causal, after-cost replay. A candidate changes no live plan until later untouched evidence passes.')+
    automationCard('alerts','TG','Telegram alerts','Open + filled','amber','Sends concise open-order, filled-order, safety, data, and daily-result messages. Unfilled attempts appear in Recent activity instead of creating Telegram noise.');
	  $('system-list').innerHTML=current.markets.map(m=>{const healthy=marketDataHealthy(m),ended=m.optional&&(!m.available||!m.ready||!m.fresh),updating=m.available&&!m.ready,checking=m.available&&m.ready&&m.fresh&&!m.coverage_ready,limited=m.available&&m.ready&&m.fresh&&m.coverage_ready&&Number(m.coverage_bps||0)<9900;const description=healthy?'Paper observer and price data are current.':ended?'This bounded experiment is not active. Core spot markets and their totals are unaffected.':updating?'Waiting for the first complete paper status.':checking?'Checking whether enough recent price data is usable.':limited?'Only '+priceCoverage(m)+' of recent price checks were usable. New evidence is still being collected.':m.available?'Observer status is older than expected.':'Status source could not be read. Other markets continue independently.';const label=healthy?'Healthy':ended?'Not active':updating?'Updating':checking?'Checking data':limited?'Limited data':m.available?'Stale':'Unavailable';return '<article class="system-row"><p><strong>'+safe(m.name)+'</strong></p><p class="description">'+description+'</p><span class="badge '+(healthy?'green':ended||m.available?'amber':'red')+'">'+label+'</span></article>';}).join('');
	$('research-evidence').innerHTML='<span class="badge '+research.tone+'">'+safe(research.label)+'</span><h3>Latest Hermes research</h3><p>'+safe(research.description)+'<br>'+safe(research.detail)+'</p>';
}
function captureRenderFocus(){
  const active=document.activeElement;
  if(active?.matches('[data-help-label]'))return {selector:'[data-help-label="'+CSS.escape(active.dataset.helpLabel)+'"]'};
  if(active?.matches('[data-plan-market]'))return {selector:'[data-plan-market="'+CSS.escape(active.dataset.planMarket)+'"]'};
  if(active?.matches('[data-chart-action]')){const view=active.closest('[data-chart-panel]')?.dataset.chartPanel;return {selector:'#markets [data-chart-panel="'+CSS.escape(view||'')+'"] [data-chart-action="'+CSS.escape(active.dataset.chartAction)+'"]'};}
  if(active?.matches('.chart-data summary'))return {selector:'.chart-data[data-detail="'+CSS.escape(active.closest('.chart-data').dataset.detail)+'"] summary'};
  return null;
}
function setNotice(message){if($('notice').textContent!==message)$('notice').textContent=message;}
function render(){
	const focus=captureRenderFocus();
	renderMetrics();renderMarkets();renderActivity();renderSystem();
	if(focus)document.querySelector(focus.selector)?.focus({preventScroll:true});
	setNotice(current.complete?'':'Some market status is delayed. Available markets remain visible.');
  $('checked').textContent=current.observed_at?(current.complete?'Live · ':'Delayed · ')+age(current.observed_at):'Delayed · no current data';
  $('connection-dot').className='dot '+(current.complete?'ok':'bad');
}
async function load(manual=false){
  const button=$('refresh');
  const request=++requestSequence;
  const previous=current?JSON.stringify(current):'';
	if(manual){clearTimeout(refreshReset);$('refresh-status').textContent='';button.disabled=true;button.classList.add('loading');button.textContent='Refreshing…';$('main').setAttribute('aria-busy','true');}
  try{const response=await fetch('/api/v1/status'+(manual?'?fresh=1':''),{cache:'no-store'});if(!response.ok)throw new Error('status unavailable');const next=await response.json();if(request!==requestSequence)return;current=next;render();if(manual){const changed=previous!==JSON.stringify(current);button.classList.remove('loading');button.textContent=!current.complete?'Data delayed':changed?'Updated ✓':'Checked ✓';$('refresh-status').textContent=!current.complete?'Refresh finished, but some market data is delayed':changed?'Dashboard updated':'Checked just now; no newer data';refreshReset=setTimeout(()=>button.textContent='Refresh',1500);}else if(button.textContent==='Try again')button.textContent='Refresh';}
  catch(error){if(request!==requestSequence)return;if(current){current.complete=false;current.markets.forEach(m=>m.fresh=false);render();}setNotice('Dashboard status is unavailable. '+(liveUpdates?'It will retry automatically.':'Use Refresh to try again.'));$('checked').textContent=current?.observed_at?'Offline · '+age(current.observed_at):'Offline · no current data';$('connection-dot').className='dot bad';if(manual)$('refresh-status').textContent='Refresh failed; dashboard status is unavailable';}
  finally{if(manual&&request===requestSequence){button.disabled=false;button.classList.remove('loading');$('main').removeAttribute('aria-busy');if(button.textContent==='Refreshing…'){button.textContent='Try again';refreshReset=setTimeout(()=>button.textContent='Refresh',3000);}}}
}
load();setInterval(()=>{if(liveUpdates&&!document.hidden&&!$('refresh').disabled&&!document.activeElement?.closest('.activity-list,[data-chart-canvas]'))load();},10000);`
