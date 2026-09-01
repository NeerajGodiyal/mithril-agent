package paperdashboard

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="color-scheme" content="dark">
  <title>Mithril Paper Trading</title>
  <link rel="stylesheet" href="/app.css">
</head>
<body>
  <a class="skip" href="#main">Skip to content</a>
  <header class="app-header">
    <div class="shell topbar">
      <div class="brand">
        <p class="eyebrow">Mithril</p>
        <h1>Paper trading</h1>
      </div>
      <nav class="tabs" aria-label="Dashboard sections" role="tablist">
        <button id="tab-overview" class="tab active" data-tab="overview" role="tab" aria-selected="true" aria-controls="overview">Overview</button>
        <button id="tab-activity" class="tab" data-tab="activity" role="tab" aria-selected="false" aria-controls="activity" tabindex="-1">Activity</button>
        <button id="tab-strategy" class="tab" data-tab="strategy" role="tab" aria-selected="false" aria-controls="strategy" tabindex="-1">Strategy</button>
        <button id="tab-system" class="tab" data-tab="system" role="tab" aria-selected="false" aria-controls="system" tabindex="-1">Automation</button>
      </nav>
      <div class="header-state">
        <div class="checked"><span id="connection-dot" class="dot" aria-hidden="true"></span><span id="checked">Connecting…</span></div>
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
      <div class="section-title">
        <h2 id="overview-title">Today's paper account</h2>
        <div class="controls">
          <button id="live" class="button quiet" type="button" aria-pressed="true">Live updates: On</button>
          <button id="refresh" class="button" type="button">Refresh</button>
          <span id="refresh-status" class="sr-only" role="status" aria-live="polite"></span>
        </div>
      </div>
      <div id="metrics" class="metrics" aria-label="Paper account summary"></div>
      <p class="daily-note"><strong>Daily paper test:</strong> the simulated balance restarts each day. “Started today” and “Now” are this UTC day's values, not a continuously compounded wallet.</p>
      <div class="section-title compact"><h2>Markets</h2><p>Price, current plan, and results.</p></div>
      <div id="markets" class="market-grid"></div>
    </section>
    <section id="activity" class="panel" role="tabpanel" aria-labelledby="tab-activity" tabindex="0" hidden>
      <div class="section-title">
        <div><p class="eyebrow">What happened</p><h2 id="activity-title">Recent activity</h2><p id="activity-summary">Loading recent paper activity…</p></div>
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
      <div id="activity-list" class="activity-list"></div>
    </section>
    <section id="strategy" class="panel" role="tabpanel" aria-labelledby="tab-strategy" tabindex="0" hidden>
      <div class="section-title"><div><p class="eyebrow">How decisions are made</p><h2 id="strategy-title">Strategy</h2></div></div>
      <div class="strategy-layout">
        <article class="card feature">
          <span class="badge blue">Paper strategies</span>
          <h3>Each market follows its saved plan.</h3>
          <p>The system measures current prices, applies each market's saved decision rules, waits when evidence is not good enough, and pauses at its safety limit.</p>
          <dl class="facts">
            <div><dt>Responds now</dt><dd>Market direction, volatility, drawdown, costs, and whether to wait, buy, or sell</dd></div>
            <div><dt>Learns carefully</dt><dd>Tested parameter challengers can replace the current paper plan only after a forward paper gate</dd></div>
            <div><dt>Cannot change</dt><dd>Wallet access, real-trading mode, leverage, or safety boundaries</dd></div>
          </dl>
        </article>
        <aside class="card guardrails">
          <h3>Safety boundary</h3>
          <ul>
            <li>Paper balances only</li>
            <li>No LLM controls execution</li>
            <li>News cannot directly trigger a trade</li>
            <li>Every decision remains in the evidence journal</li>
            <li>No live self-retraining from one win or loss</li>
          </ul>
        </aside>
      </div>
      <div id="strategy-markets" class="market-grid small"></div>
      <article id="research-instruction" class="card instruction-card experiment-card" hidden>
        <div class="instruction-copy">
          <span class="badge violet">Nous Hermes research</span>
          <h3>Plan the next paper experiment</h3>
          <p>Set the simulated budget and risk envelope Hermes should research. Saving updates the next research run without restarting the dashboard.</p>
        </div>
        <section class="active-limits" aria-labelledby="active-limits-title">
          <div class="subsection-head"><span class="badge green">Active now</span><h4 id="active-limits-title">Current paper plans</h4></div>
          <div id="active-limit-list" class="active-limit-list">Waiting for current limits…</div>
        </section>
        <div class="instruction-controls" aria-label="Next paper experiment request">
          <label>Market
            <select id="instruction-market"><option value="all">All paper markets</option></select>
          </label>
          <label>Research goal
            <select id="instruction-preference">
              <option value="balanced">Keep it balanced</option>
              <option value="more-opportunities">Look for more opportunities</option>
              <option value="more-selective">Be more selective</option>
            </select>
          </label>
          <label>Paper capital
            <span class="money-input"><span aria-hidden="true">$</span><input id="instruction-capital" type="number" min="10" max="1000000" step="0.01" inputmode="decimal"></span>
            <small>Requested simulated money for the next experiment</small>
          </label>
          <label>Smallest order
            <span class="money-input"><span aria-hidden="true">$</span><input id="instruction-minimum" type="number" min="1" max="1000000" step="0.01" inputmode="decimal"></span>
            <small>Request to skip smaller paper trades</small>
          </label>
          <label>Largest order
            <span class="money-input"><span aria-hidden="true">$</span><input id="instruction-maximum" type="number" min="1" max="1000000" step="0.01" inputmode="decimal"></span>
            <small>Requested cap for the next experiment</small>
          </label>
          <label>Price-check speed
            <select id="instruction-cadence">
              <option value="5">Every 5 seconds</option>
              <option value="15">Every 15 seconds</option>
              <option value="30">Every 30 seconds</option>
              <option value="60">Every minute</option>
              <option value="300">Every 5 minutes</option>
            </select>
            <small>Faster checks do not force more trades</small>
          </label>
          <label>Paper loss stop
            <span class="percent-input"><input id="instruction-drawdown" type="number" min="0.1" max="50" step="0.1" inputmode="decimal"><span aria-hidden="true">%</span></span>
            <small>Pause new buys after this drawdown</small>
          </label>
          <div id="instruction-warning" class="instruction-warning" role="status" aria-live="polite"></div>
          <button id="save-instruction" class="button" type="button">Save experiment request</button>
          <span id="instruction-status" role="status" aria-live="polite">No preference saved yet.</span>
        </div>
        <p class="instruction-boundary"><strong>Activation:</strong> this changes the next Hermes research brief immediately. It does not resize an order already running. A requested plan becomes active only as a new paper-policy version after deterministic checks and a clean experiment boundary.</p>
      </article>
    </section>
    <section id="system" class="panel" role="tabpanel" aria-labelledby="tab-system" tabindex="0" hidden>
      <div class="section-title"><div><p class="eyebrow">Who does what</p><h2 id="system-title">Automation setup</h2><p>Configured roles, permissions, and current market observers.</p></div></div>
      <div id="automation" class="automation-grid" aria-label="Automation roles"></div>
      <div class="section-title compact"><div><p class="eyebrow">Live status</p><h2>Market observers</h2></div></div>
      <div id="system-list" class="system-list"></div>
      <div class="section-title compact"><div><p class="eyebrow">Trust boundary</p><h2>Access and evidence</h2></div></div>
      <div class="detail-grid">
        <article class="card detail-card"><span class="badge green">Paper only</span><h3>Permissions</h3><p>No wallet key, signing, real funds, Mainnet submission, margin, leverage, short position, or liquidation authority.</p></article>
        <article id="research-evidence" class="card detail-card"><span class="badge blue">Research status</span><h3>Latest Hermes research</h3><p>Waiting for a validated research packet.</p></article>
        <article class="card detail-card"><span class="badge amber">Reviewed scope</span><h3>Markets</h3><p>SOL and JUP are active paper markets. WIF, JTO, and PYTH are review candidates: a 7-day checkpoint can find early data problems, while 30 complete collector days remain required before paper admission. Perps and non-Solana venues still need their own margin, funding, liquidation, and custody boundaries.</p></article>
        <article class="card detail-card"><span class="badge blue">Evidence retained</span><h3>Recent order activity</h3><p>The dashboard keeps a bounded recent list. Older events remain in the local evidence journals. There are no on-chain signatures because no transaction is submitted.</p><button id="open-order-history" class="text-button" type="button">View recent paper orders</button></article>
      </div>
      <article class="card access">
        <div><span class="badge green">Private access</span><h3>Dashboard stays on the server</h3></div>
        <p>Open it through an SSH tunnel. It is not published to the internet. Its only write action is the bounded paper-research preference above.</p>
      </article>
    </section>
  </main>
  <footer class="shell">Private paper dashboard · Values are simulated, not financial results.</footer>
  <script src="/app.js" defer></script>
</body>
</html>`

const appCSS = `:root{
  --bg:#07090d;--surface:#0d131c;--raised:#121a25;--raised-2:#172131;--line:#273546;--line-strong:#51647a;--text:#f2f5f8;
  --muted:#a3aebb;--green:#49d39d;--green-bg:#0d241b;--blue:#76a9fa;--blue-bg:#10213a;
  --amber:#e8b665;--amber-bg:#2a2113;--red:#ff7a88;--red-bg:#2d151b;--violet:#b6a2ff;--violet-bg:#211c3b;--subtle:#7f8b9a;--radius:7px;
}
.metric-value{overflow-wrap:anywhere}.market-state{flex-wrap:wrap}.topbar>div,.checked,.market,.market-head>*,.activity-copy,.system-row>*{min-width:0}.activity-copy h3,.activity-copy p,.market h3,.system-row p{overflow-wrap:anywhere}
*{box-sizing:border-box}html{background:var(--bg);color:var(--text);font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:16px}body{margin:0;min-width:320px;background:var(--bg)}button,select{font:inherit}.shell{width:min(100% - 32px,1380px);margin-inline:auto}.skip{position:fixed;left:12px;top:-60px;z-index:10;background:#fff;color:#000;padding:10px 14px;border-radius:6px}.skip:focus{top:12px}.topbar{min-height:108px;display:flex;align-items:center;justify-content:space-between;gap:24px}.eyebrow{margin:0 0 5px;color:var(--blue);font-size:.72rem;font-weight:750;letter-spacing:.14em;text-transform:uppercase}.topbar h1{font-size:clamp(1.6rem,4vw,2.25rem);letter-spacing:-.04em;margin:0}.checked{display:flex;align-items:center;gap:9px;color:var(--muted);font-size:.88rem;font-variant-numeric:tabular-nums}.dot{width:9px;height:9px;border-radius:2px;background:var(--amber)}.dot.ok{background:var(--green)}.dot.bad{background:var(--red)}.trust{border-block:1px solid #22402e;background:#101d16}.trust-inner{display:flex;align-items:center;gap:28px;min-height:48px;color:#c1cec6;font-size:.84rem}.trust strong{color:var(--green);font-size:.76rem;letter-spacing:.08em;text-transform:uppercase}.trust span{display:flex;align-items:center;gap:8px}.trust span:before{content:"✓";color:var(--green);font-weight:800}.tabs{display:flex;gap:6px;padding-block:24px 18px}.tab,.button{min-height:44px;border:1px solid transparent;border-radius:6px;background:transparent;color:var(--muted);padding:0 16px;cursor:pointer}.tab:hover,.tab:focus-visible,.button:hover,.button:focus-visible{color:var(--text);border-color:#556173;outline:none}.tab.active{background:var(--raised);color:var(--text);border-color:var(--line)}.button{min-width:108px;border-color:var(--line);background:var(--surface);color:var(--text)}.button.quiet{min-width:136px;color:var(--muted)}.controls{display:flex;align-items:center;gap:8px}.button.loading:before{content:"";display:inline-block;width:12px;height:12px;margin:0 8px -2px 0;border:2px solid var(--muted);border-top-color:transparent;border-radius:50%;animation:spin .7s linear infinite}main{min-height:610px}.panel{animation:enter .18s ease}.panel[hidden]{display:none}.notice{min-height:0;margin-bottom:14px}.notice:not(:empty){padding:13px 15px;border:1px solid #62451d;border-radius:6px;background:var(--amber-bg);color:#f5d69f}.section-title{display:flex;align-items:end;justify-content:space-between;gap:20px;margin:18px 0}.section-title.compact{margin-top:36px}.section-title h2{margin:0;font-size:1.25rem;letter-spacing:-.02em}.section-title p{margin:0;color:var(--muted);font-size:.88rem}.metrics{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px}.metric,.card,.market,.activity-item,.system-row{background:var(--surface);border:1px solid var(--line);border-radius:var(--radius)}.metric{padding:22px;min-height:130px;display:flex;flex-direction:column;justify-content:space-between}.metric-label{position:relative;display:flex;align-items:center;justify-content:space-between;gap:8px;color:var(--muted);font-size:.79rem}.help{display:inline-grid;place-items:center;flex:0 0 auto;width:32px;height:32px;padding:0;border:1px solid var(--line);border-radius:4px;background:var(--surface);color:var(--blue);cursor:pointer}.help-tip{display:none;position:absolute;z-index:4;top:calc(100% + 8px);right:0;width:min(245px,calc(100vw - 48px));padding:10px 12px;border:1px solid #556173;border-radius:6px;background:#20252c;color:var(--text);font-size:.75rem;font-weight:400;line-height:1.45;text-align:left;box-shadow:0 12px 30px rgba(0,0,0,.35)}.metric:nth-child(odd) .help-tip{right:auto;left:0}.help:hover .help-tip,.help:focus-visible .help-tip,.help[aria-expanded="true"] .help-tip{display:block}.metric-value{font-size:clamp(1.35rem,2.2vw,2rem);font-weight:720;letter-spacing:-.04em;font-variant-numeric:tabular-nums}.metric-foot{color:var(--muted);font-size:.75rem}.positive{color:var(--green)!important}.negative{color:var(--red)!important}.neutral{color:var(--text)!important}.market-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.market-grid.small{margin-top:14px}.market{padding:22px}.market-head,.market-state{display:flex;align-items:center;justify-content:space-between;gap:16px}.market h3{margin:0;font-size:1.08rem}.market-status{display:flex;align-items:center;gap:10px}.updated{color:var(--muted);font-size:.72rem;font-variant-numeric:tabular-nums}.badge{display:inline-flex;align-items:center;min-height:26px;padding:0 9px;border-radius:4px;font-size:.7rem;font-weight:750;letter-spacing:.03em}.badge.green{color:var(--green);background:var(--green-bg)}.badge.blue{color:var(--blue);background:var(--blue-bg)}.badge.amber{color:var(--amber);background:var(--amber-bg)}.badge.red{color:var(--red);background:var(--red-bg)}.price{margin:20px 0 6px;font-size:clamp(1.7rem,4vw,2.7rem);font-weight:750;letter-spacing:-.055em;font-variant-numeric:tabular-nums}.market-state{color:var(--muted);font-size:.83rem}.market-state strong{color:var(--text);font-weight:620}.market-metrics{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:8px;margin-top:22px;padding-top:18px;border-top:1px solid var(--line)}.market-metrics div{min-width:0}.market-metrics span{display:block;color:var(--muted);font-size:.72rem;margin-bottom:6px}.market-metrics strong{display:block;font-size:.93rem;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}.empty{padding:34px;border:1px dashed #394151;border-radius:var(--radius);color:var(--muted);text-align:center}.filter{display:flex;align-items:center;gap:10px;color:var(--muted);font-size:.82rem}.filter select{min-height:44px;color:var(--text);background:var(--surface);border:1px solid var(--line);border-radius:6px;padding:0 32px 0 12px}.activity-list{display:grid;gap:8px}.activity-item{display:grid;grid-template-columns:10px minmax(0,1fr) auto;gap:15px;padding:17px 18px}.event-mark{width:9px;height:9px;border-radius:2px;margin-top:6px;background:var(--blue)}.event-mark.order_filled{background:var(--green)}.event-mark.risk_halted,.event-mark.data_unavailable{background:var(--red)}.event-mark.order_refused,.event-mark.order_missed{background:var(--muted)}.activity-copy h3{margin:0 0 5px;font-size:.95rem}.activity-copy p{white-space:pre-line;margin:0;color:var(--muted);font-size:.84rem;line-height:1.48}.activity-time{text-align:right;color:var(--muted);font-size:.75rem;font-variant-numeric:tabular-nums}.strategy-layout{display:grid;grid-template-columns:minmax(0,1.5fr) minmax(280px,.7fr);gap:14px}.card{padding:24px}.feature h3{margin:18px 0 8px;font-size:1.45rem;letter-spacing:-.03em}.feature>p,.access p{color:var(--muted);line-height:1.58}.facts{display:grid;gap:0;margin:22px 0 0}.facts div{padding:14px 0;border-top:1px solid var(--line)}.facts dt{color:var(--muted);font-size:.74rem;margin-bottom:5px}.facts dd{margin:0;font-size:.88rem}.guardrails h3,.access h3{margin:0 0 14px}.guardrails ul{margin:0;padding-left:20px;color:#cbd2db;line-height:2}.system-list{display:grid;gap:8px}.system-row{display:grid;grid-template-columns:minmax(150px,1fr) minmax(0,2fr) auto;align-items:center;gap:18px;padding:17px 19px}.system-row p{margin:0}.system-row .description{color:var(--muted);font-size:.84rem}.access{display:flex;justify-content:space-between;gap:25px;align-items:center;margin-top:14px}.access p{max-width:580px;margin:0}footer{padding-block:46px;color:#7b8798;font-size:.75rem}button:focus-visible,select:focus-visible,a:focus-visible{outline:3px solid var(--blue);outline-offset:3px}@keyframes enter{from{opacity:.3;transform:translateY(4px)}to{opacity:1;transform:none}}@keyframes spin{to{transform:rotate(360deg)}}@media (prefers-reduced-motion:reduce){*,*:before,*:after{animation-duration:.01ms!important;animation-iteration-count:1!important;scroll-behavior:auto!important}}@media(max-width:900px){.metrics{grid-template-columns:repeat(2,1fr)}.strategy-layout{grid-template-columns:1fr}.trust-inner{gap:16px;flex-wrap:wrap;padding-block:11px}.market-grid{grid-template-columns:1fr}}@media(max-width:600px){.shell{width:min(100% - 22px,1380px)}.topbar{min-height:88px}.checked{max-width:150px;text-align:right}.trust-inner{display:grid;grid-template-columns:1fr 1fr;gap:9px 15px}.trust strong{grid-column:1/-1}.tabs{overflow-x:auto;padding-block:17px 13px}.tab{flex:1;padding-inline:12px}.section-title{align-items:center}.section-title.compact{display:block}.section-title.compact p{margin-top:5px}.controls{align-items:stretch;flex-direction:column}.button{min-width:0}.metrics{gap:8px}.metric{padding:16px;min-height:112px}.metric-value{font-size:1.3rem}.market{padding:18px}.market-head{align-items:flex-start}.market-status{align-items:flex-end;flex-direction:column;gap:4px}.market-metrics{grid-template-columns:1fr 1fr}.activity-item{grid-template-columns:9px minmax(0,1fr)}.activity-time{grid-column:2;text-align:left}.filter{display:block}.filter select{display:block;margin-top:6px;max-width:190px}.system-row{grid-template-columns:1fr auto;gap:8px}.system-row .description{grid-column:1/-1;grid-row:2}.access{display:block}.access p{margin-top:12px}}`

const mobileCSS = `.market-metrics{grid-template-columns:repeat(4,minmax(0,1fr))}.chart{margin-top:20px;padding-top:17px;border-top:1px solid var(--line)}.chart-head,.chart-legend{display:flex;justify-content:space-between;gap:12px;color:var(--muted);font-size:.72rem}.chart svg{display:block;width:100%;height:104px;margin:11px 0 8px;overflow:visible}.chart-grid{stroke:#303846;stroke-width:.6}.chart-paper,.chart-hold{fill:none;stroke-width:2;vector-effect:non-scaling-stroke}.chart-paper{stroke:var(--green)}.chart-hold{stroke:var(--blue);stroke-dasharray:4 3}.chart-legend{justify-content:flex-start;gap:18px}.chart-legend span:before{content:"";display:inline-block;width:14px;height:2px;margin:0 6px 3px 0;background:var(--green)}.chart-legend span:last-child:before{background:repeating-linear-gradient(90deg,var(--blue) 0 4px,transparent 4px 7px)}.chart-empty{min-height:104px;display:grid;place-items:center;color:var(--muted);font-size:.8rem}.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}@media(max-width:600px){.tabs{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));overflow:visible}.tab{min-width:0;padding-inline:4px;font-size:.88rem}.market-metrics{grid-template-columns:1fr 1fr}}`

const refinedCSS = `.topbar{min-height:86px}.topbar h1{font-size:clamp(1.45rem,3vw,1.8rem)}.eyebrow{font-size:.68rem}.checked{font-size:.8rem}.trust-inner{min-height:42px;gap:24px;font-size:.78rem}.trust strong{font-size:.7rem}.tabs{padding-block:18px 14px}.section-title{margin:16px 0}.section-title.compact{margin-top:30px}.section-title h2{font-size:1.12rem}.section-title p{font-size:.8rem}.metrics{gap:10px}.metric{min-height:110px;padding:17px 18px}.metric-label{font-size:.74rem}.help{width:30px;height:30px}.metric-value{font-size:clamp(1.2rem,1.8vw,1.65rem);font-weight:700}.metric-foot{font-size:.72rem}.market-grid{gap:12px}.market{padding:19px 20px}.market h3{font-size:1rem}.updated{font-size:.7rem}.badge{min-height:25px;padding-inline:8px;font-size:.68rem}.market-overview{display:grid;grid-template-columns:minmax(120px,.7fr) minmax(0,1.3fr);gap:24px;margin-top:18px;padding:16px 0;border-block:1px solid var(--line)}.market-overview>div{min-width:0}.market-label{display:block;margin-bottom:5px;color:var(--muted);font-size:.68rem;letter-spacing:.04em;text-transform:uppercase}.market-price{display:block;font-size:1.18rem;font-weight:700;letter-spacing:-.025em;font-variant-numeric:tabular-nums}.market-plan{display:block;font-size:.9rem;font-weight:650}.market-context{display:block;margin-top:4px;color:var(--muted);font-size:.75rem;line-height:1.35}.strategy-next{margin:18px 0 5px;font-size:1rem;font-weight:650;line-height:1.4}.market-state{font-size:.78rem}.market-metrics{grid-template-columns:repeat(2,minmax(0,1fr));gap:16px 20px;margin-top:18px;padding-top:0;border-top:0}.market-metrics span{font-size:.7rem;margin-bottom:5px}.market-metrics strong{font-size:.87rem}.chart{margin-top:18px;padding-top:15px}.chart svg{height:88px}.feature h3{font-size:1.3rem}footer{padding-block:40px;font-size:.72rem}@media(max-width:900px){.trust-inner{padding-block:9px}}@media(max-width:600px){.topbar{min-height:74px}.trust-inner{display:flex;gap:14px}.trust strong{grid-column:auto}.tabs{padding-block:14px 11px}.metric{min-height:104px;padding:14px}.metric-value{font-size:1.15rem}.market{padding:16px}.market-overview{grid-template-columns:1fr 1.35fr;gap:16px}.market-metrics{gap:14px}}`

const finishingCSS = `.panel{animation:enter .12s cubic-bezier(0,0,.38,.9)}.metric:first-child{background:var(--raised)}.metric:first-child .metric-value{font-size:clamp(1.5rem,2.25vw,1.9rem)}.metric-label,.market-label{font-size:.75rem;font-weight:500}.metric-value,.market-price{font-weight:700}.market-plan{font-weight:600}.market-price{font-size:1.3rem}.market-detail{display:block;margin-top:4px!important;color:var(--muted);font-size:.7rem!important}.help{width:44px;height:44px;border:0;background:transparent;font-size:0}.help:before{content:"?";display:grid;place-items:center;width:28px;height:28px;border:1px solid var(--line);border-radius:4px;background:var(--surface);color:var(--blue);font-size:.75rem}.help-tip{font-size:.75rem}.chart-paper{stroke:var(--blue)}.chart-hold{stroke:#77808d}.chart-legend span:first-child:before{background:var(--blue)}.chart-legend span:last-child:before{background:repeating-linear-gradient(90deg,#77808d 0 4px,transparent 4px 7px)}@keyframes enter{from{opacity:.85;transform:translateY(2px)}to{opacity:1;transform:none}}@media(min-width:1100px){.metrics{grid-template-columns:minmax(240px,1.35fr) repeat(2,minmax(0,1fr));grid-template-rows:repeat(2,minmax(86px,auto))}.metric:first-child{grid-row:1/3}.metric:nth-child(4){grid-column:2/4}.metric:not(:first-child){min-height:86px;padding-block:15px}}@media(min-width:601px) and (max-width:1099px){.metrics{grid-template-columns:repeat(2,minmax(0,1fr))}.metric:first-child,.metric:nth-child(4){grid-column:1/-1}}@media(max-width:600px){#overview>.section-title{align-items:end;flex-wrap:wrap}#overview>.section-title .controls{display:grid;grid-template-columns:1fr 1fr;width:100%}.metric:first-child,.metric:last-child{grid-column:1/-1}.metric:first-child .metric-value{font-size:1.55rem}.metric:last-child{min-height:82px;display:grid;grid-template-columns:1fr auto;align-items:center;gap:4px 12px}.metric:last-child .metric-value{grid-column:2;grid-row:1}.metric:last-child .metric-foot{grid-column:1/-1}.market-label{font-size:.72rem}}@media(max-width:360px){.market-overview{grid-template-columns:1fr}.trust-inner{gap:10px}.trust-inner span:first-of-type{display:none}}`

const narrowCSS = `@media(max-width:360px){.controls .button{padding-inline:6px;font-size:.8rem;white-space:nowrap}}`

const observabilityCSS = `.topbar{border-bottom:1px solid #101b2a}.trust{border-color:#1c4737;background:#0d2119}.tab.active{background:var(--raised-2);border-color:#38506f}.metric,.card,.market,.activity-item,.system-row{box-shadow:inset 0 1px 0 rgba(255,255,255,.025)}.metric:first-child{border-color:#355277}.market{border-top-color:#355277}.chart-grid{stroke:#2b3b51}.chart-hit{fill:transparent;stroke:transparent;stroke-width:8;cursor:crosshair;pointer-events:stroke}.chart-hit:hover{stroke:var(--blue);stroke-width:2;fill:var(--surface)}.chart-legend span:nth-child(2):before{display:inline-block;background:repeating-linear-gradient(90deg,#77808d 0 4px,transparent 4px 7px)}.chart-legend span:last-child{margin-left:auto}.chart-legend span:last-child:before{display:none}#activity-summary{margin-top:5px}.automation-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}.automation-card{min-height:180px;padding:19px;background:var(--surface);border:1px solid var(--line);border-radius:var(--radius);box-shadow:inset 0 1px 0 rgba(255,255,255,.025)}.automation-head{display:flex;align-items:center;justify-content:space-between;gap:12px}.role-symbol{display:grid;place-items:center;width:34px;height:34px;border-radius:6px;background:var(--blue-bg);color:var(--blue);font-size:.72rem;font-weight:800;letter-spacing:.04em}.automation-card.hermes .role-symbol{background:var(--violet-bg);color:var(--violet)}.automation-card.alerts .role-symbol{background:var(--amber-bg);color:var(--amber)}.automation-card h3{margin:20px 0 7px;font-size:1rem}.automation-card p,.detail-card p{margin:0;color:var(--muted);font-size:.8rem;line-height:1.55}.detail-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.detail-card{min-height:175px}.detail-card h3{margin:16px 0 8px;font-size:1rem}.text-button{min-height:44px;margin:12px 0 -8px;padding:0;border:0;background:transparent;color:var(--blue);font:inherit;font-size:.8rem;font-weight:650;cursor:pointer}.text-button:hover{text-decoration:underline}.access{border-color:#29483d}.badge.violet{color:var(--violet);background:var(--violet-bg)}@media(max-width:1050px){.automation-grid{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:700px){.detail-grid{grid-template-columns:1fr}}@media(max-width:600px){.automation-grid{grid-template-columns:1fr 1fr;gap:8px}.automation-card{min-height:168px;padding:16px}.automation-card h3{margin-top:16px}.detail-card{min-height:0}.system-row{background:var(--surface)}}@media(max-width:380px){.automation-grid{grid-template-columns:1fr}.automation-card{min-height:0}}`

const qaCSS = `body{background:var(--bg)}.topbar:after,.metric:before,.market:before,.automation-card:before{display:none}.checked{border-radius:6px;background:var(--surface)}.dot,.dot.ok,.dot.bad{border-radius:2px;box-shadow:none}.trust{border-color:var(--line);background:var(--green-bg)}.tabs{border-radius:var(--radius);background:var(--bg);backdrop-filter:none}.tab.active{border-color:var(--line-strong);background:var(--raised-2);box-shadow:none}.metric,.card,.market,.activity-item,.system-row,.automation-card,.activity-item:hover,.access{background:var(--surface);box-shadow:inset 0 1px 0 rgba(255,255,255,.025)}.metric:first-child{border-color:var(--line-strong);background:var(--raised)}.market{border-top-color:var(--line)}.button,.filter select,.help:before{border-color:var(--line-strong)}.button:disabled{cursor:wait;opacity:.62}.badge{border-color:transparent;border-radius:4px}.badge.green{background:var(--green-bg)!important}.badge.blue{background:var(--blue-bg)!important}.badge.amber{background:var(--amber-bg)!important}.badge.red{background:var(--red-bg)!important}.badge.violet{background:var(--violet-bg)!important}.role-symbol{border-color:var(--line);background:var(--blue-bg)}.automation-card.hermes .role-symbol{background:var(--violet-bg)}.automation-card.alerts .role-symbol{background:var(--amber-bg)}.metric-label,.market-label,.facts dt{color:var(--muted)}.metric-label,.market-label,.facts dt,.metric-foot,.updated,.badge,.chart-head,.chart-legend,.market-metrics span{font-size:.75rem}.market-overview,.market-metrics,.chart{border-color:var(--line)}.market-metrics,.chart{background:var(--bg)}.chart-grid{stroke:var(--line)}.chart-paper{stroke:var(--blue)}.chart-hold{stroke:var(--subtle)}.chart-legend{flex-wrap:wrap}.chart-legend span:first-child:before{background:var(--blue)}.chart-legend span:nth-child(2){margin-left:auto}.chart-legend span:nth-child(2):before,.chart-legend span:last-child:before{display:inline-block;background:repeating-linear-gradient(90deg,var(--subtle) 0 4px,transparent 4px 7px)}.automation-card p,.detail-card p{font-size:.8125rem}footer{color:var(--subtle)}@media(max-width:600px){.metric:last-child{display:flex;min-height:104px}.metric:last-child .metric-value,.metric:last-child .metric-foot{grid-column:auto;grid-row:auto}}@media(max-width:520px){.automation-grid{grid-template-columns:1fr}.automation-card{min-height:0}}@media(max-width:430px){.topbar{align-items:flex-start;flex-direction:column;justify-content:center;gap:9px}.checked{align-self:flex-start;max-width:none}.market-head{align-items:flex-start;flex-direction:column}.market-status{align-items:flex-start;flex-direction:row}}@media(max-width:390px){.tabs{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:360px){.metrics{grid-template-columns:1fr}.metric:first-child,.metric:last-child{grid-column:auto}.trust-inner{display:grid;grid-template-columns:1fr 1fr;gap:6px 12px}.trust strong{grid-column:1/-1}.trust-inner span:first-of-type{display:flex}}`

const finalCSS = `.help:focus-visible:not([aria-expanded="true"]) .help-tip{display:none}.limit-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:1px;margin:18px 0 0;background:var(--line);border:1px solid var(--line)}.limit-grid div{min-width:0;padding:12px;background:var(--bg)}.limit-grid dt{margin:0 0 4px;color:var(--muted);font-size:.7rem}.limit-grid dd{margin:0;font-size:.82rem;font-weight:650;overflow-wrap:anywhere}.limit-note{margin:14px 0 0;color:var(--muted);font-size:.76rem;line-height:1.5}.instruction-card{display:grid;grid-template-columns:minmax(0,1fr) minmax(320px,.9fr);gap:24px;margin-top:14px}.instruction-copy h3{margin:16px 0 7px;font-size:1.1rem}.instruction-copy p,.instruction-boundary{margin:0;color:var(--muted);font-size:.82rem;line-height:1.55}.instruction-controls{display:grid;grid-template-columns:1fr 1fr;gap:12px;align-content:start}.instruction-controls label{display:grid;gap:6px;color:var(--muted);font-size:.74rem}.instruction-controls select{width:100%;min-height:44px;padding:0 34px 0 11px;border:1px solid var(--line-strong);border-radius:6px;background:var(--raised);color:var(--text)}.instruction-controls .button{align-self:end}.instruction-controls .button:disabled{cursor:wait;opacity:.62}#instruction-status{align-self:center;color:var(--muted);font-size:.75rem;line-height:1.35}.instruction-boundary{grid-column:1/-1;padding-top:14px;border-top:1px solid var(--line)}.instruction-boundary strong{color:var(--text)}@media(max-width:820px){.instruction-card{grid-template-columns:1fr}}@media(max-width:600px){.metric:last-child{min-height:82px;display:grid;grid-template-columns:1fr auto;align-items:center;gap:4px 12px}.metric:last-child .metric-value{grid-column:2;grid-row:1}.metric:last-child .metric-foot{grid-column:1/-1}.instruction-controls{grid-template-columns:1fr}.limit-grid{grid-template-columns:1fr 1fr}}@media(max-width:360px){.metric:last-child{display:flex;min-height:104px}.limit-grid{grid-template-columns:1fr}}`

const clarityCSS = `.daily-note{margin:-5px 0 16px;padding:11px 13px;border-left:3px solid var(--blue);background:var(--blue-bg);color:#cad8ea;font-size:.78rem;line-height:1.5}.market-metrics{grid-template-columns:repeat(3,minmax(0,1fr));gap:1px;background:var(--line);border:1px solid var(--line)}.market-metrics>div{padding:12px;background:var(--bg)}.market-result-note{margin:13px 0 0;color:var(--muted);font-size:.75rem;line-height:1.5}.chart.market-price-chart{border-top:0;margin-top:12px;padding:15px;background:var(--bg);border:1px solid var(--line)}.chart.market-price-chart svg{height:72px}.chart-market{fill:none;stroke:var(--amber);stroke-width:2;vector-effect:non-scaling-stroke}.chart-market-dot{fill:var(--amber)}.chart.market-price-chart .chart-legend span:before{background:var(--amber)}.chart.market-price-chart .chart-legend span:last-child:before{display:none}.chart-title{color:var(--text);font-weight:650}.chart-change{font-weight:650}.chart-change.positive{color:var(--green)}.chart-change.negative{color:var(--red)}@media(max-width:700px){.market-metrics{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:380px){.market-metrics{grid-template-columns:1fr}.market-metrics>div{padding:11px}}`

const controlCSS = `.experiment-card{grid-template-columns:minmax(250px,.72fr) minmax(480px,1.28fr);padding:0;overflow:hidden}.experiment-card>.instruction-copy,.active-limits{padding:22px 24px}.experiment-card>.instruction-copy{border-bottom:1px solid var(--line)}.experiment-card>.instruction-controls{grid-column:2;grid-row:1/3;padding:24px;background:var(--raised)}.experiment-card>.instruction-boundary{margin:0;padding:16px 24px;border-top:1px solid var(--line);background:var(--bg)}.active-limits{align-self:stretch}.subsection-head{display:flex;align-items:center;gap:10px;margin-bottom:14px}.subsection-head h4{margin:0;font-size:.86rem}.active-limit-list{display:grid;gap:8px}.active-limit{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:4px 12px;padding:11px 12px;border:1px solid var(--line);background:var(--bg)}.active-limit strong{font-size:.82rem}.active-limit span{color:var(--muted);font-size:.72rem;text-align:right}.active-limit small{grid-column:1/-1;color:var(--muted);font-size:.7rem;line-height:1.4}.instruction-controls{grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.instruction-controls label{align-content:start}.instruction-controls label>small{color:var(--subtle);font-size:.68rem;line-height:1.35}.instruction-controls input,.instruction-controls select{width:100%;min-height:44px;border:1px solid var(--line-strong);border-radius:6px;background:var(--surface);color:var(--text)}.instruction-controls input{padding:0 11px}.money-input,.percent-input{display:grid;grid-template-columns:auto minmax(0,1fr);align-items:center;border:1px solid var(--line-strong);border-radius:6px;background:var(--surface);color:var(--muted)}.money-input>span,.percent-input>span{padding:0 0 0 11px}.money-input input,.percent-input input{border:0;background:transparent}.percent-input{grid-template-columns:minmax(0,1fr) auto}.percent-input>span{padding:0 11px 0 0}.instruction-warning{grid-column:1/-1;min-height:0;color:var(--muted);font-size:.75rem;line-height:1.45}.instruction-warning:not(:empty){padding:10px 12px;border-left:3px solid var(--amber);background:var(--amber-bg);color:#efd3a5}.instruction-controls .button{justify-self:start}.instruction-controls #instruction-status{align-self:center}.metrics .metric.trade-volume{border-color:#3a4656}@media(min-width:1100px){.metrics{grid-template-columns:minmax(240px,1.35fr) repeat(2,minmax(0,1fr));grid-template-rows:repeat(2,minmax(86px,auto))}.metric:first-child{grid-column:1;grid-row:1/3}.metric:nth-child(2){grid-column:2;grid-row:1}.metric:nth-child(3){grid-column:3;grid-row:1}.metric:nth-child(4){grid-column:2;grid-row:2}.metric:nth-child(5){grid-column:3;grid-row:2}.metric:not(:first-child){min-height:86px;padding-block:15px}}@media(max-width:900px){.experiment-card{grid-template-columns:1fr}.experiment-card>.instruction-controls{grid-column:1;grid-row:auto;border-top:1px solid var(--line)}.experiment-card>.instruction-boundary{grid-column:1}.active-limits{border-top:0}}@media(max-width:600px){.instruction-controls{grid-template-columns:1fr}.instruction-warning{grid-column:1}.experiment-card>.instruction-copy,.active-limits,.experiment-card>.instruction-controls{padding:18px}.experiment-card>.instruction-boundary{padding:14px 18px}.active-limit{grid-template-columns:1fr}.active-limit span{text-align:left}.metrics .metric:last-child{grid-column:1/-1}}`

const cockpitCSS = `:root{--bg:#050713;--surface:#090d1d;--raised:#0d1327;--raised-2:#111936;--line:#1c2746;--line-strong:#344b80;--text:#f4f6ff;--muted:#9aa8c7;--blue:#7d8dff;--blue-bg:#111b43;--green:#4ed9a5;--green-bg:#0b281f;--amber:#f2ba63;--amber-bg:#2b1f10;--red:#ff7189;--red-bg:#2d111c;--violet:#b69cff;--violet-bg:#20183d;--subtle:#7180a3;--radius:12px}body{min-height:100vh;background:radial-gradient(circle at 88% 4%,rgba(58,76,184,.16),transparent 32rem),var(--bg)}.shell{width:min(100% - 36px,1280px)}.app-header{position:sticky;top:0;z-index:9;border-bottom:1px solid var(--line);background:rgba(5,7,19,.96);box-shadow:0 16px 42px rgba(0,0,0,.2)}.topbar{display:grid;grid-template-columns:auto minmax(420px,1fr) auto;align-items:center;gap:24px;min-height:72px;border-bottom:0}.topbar h1{font-size:1rem;letter-spacing:-.02em;white-space:nowrap}.topbar .eyebrow{margin-bottom:2px;font-size:.62rem}.header-state{display:flex;align-items:center;gap:10px}.checked{min-height:34px;padding:0 10px;border:1px solid var(--line);background:rgba(9,13,29,.88);white-space:nowrap}.trust{width:auto;margin:0;border:0;background:transparent}.trust-inner{min-height:34px;padding:0;color:var(--muted)}.trust strong{min-height:34px;display:inline-flex;align-items:center;padding:0 10px;border:1px solid var(--line);border-radius:8px;background:var(--blue-bg);color:var(--blue)}.trust span{display:none}.tabs{position:static;z-index:auto;margin:0;padding:4px;border:1px solid var(--line);border-radius:9px;background:var(--bg);box-shadow:none}.tab{min-height:36px;padding-inline:13px;border-radius:6px;font-size:.77rem;transition:color .15s ease,background .15s ease,border-color .15s ease}.tab.active{border-color:#425a96;background:linear-gradient(180deg,#17234b,#101938);color:#fff}.button,.filter select,.instruction-controls input,.instruction-controls select,.money-input,.percent-input{border-radius:8px}.button{background:var(--raised);transition:background .15s ease,border-color .15s ease}.button:hover,.button:focus-visible{background:var(--raised-2);border-color:var(--blue)}.section-title h2{font-size:1.05rem}.section-title .eyebrow{margin-bottom:7px}.metric,.card,.market,.activity-item,.system-row,.automation-card{border-color:var(--line);border-radius:var(--radius);background:linear-gradient(145deg,rgba(13,19,39,.98),rgba(7,11,25,.98));box-shadow:inset 0 1px 0 rgba(255,255,255,.035),0 18px 50px rgba(0,0,0,.12)}.metric:first-child{border-color:#344b80;background:linear-gradient(145deg,#111b3d,#0a1024)}.metric-label,.market-label,.facts dt,.market-metrics span,.limit-grid dt{letter-spacing:.035em}.metric.has-visual{display:grid;grid-template-columns:minmax(0,1fr) auto;grid-template-rows:auto 1fr auto;column-gap:20px}.metric.has-visual .metric-label{grid-column:1/-1}.metric.has-visual .metric-value{align-self:end}.metric.has-visual .metric-foot{grid-column:1/-1}.coverage-ring{grid-column:2;grid-row:2;align-self:center;width:88px;color:var(--text)}.coverage-ring circle{fill:none;stroke-width:3.5}.coverage-ring .ring-track{stroke:#1b2747}.coverage-ring .ring-value{stroke:var(--blue);stroke-linecap:round}.coverage-ring text{fill:currentColor;font-family:Inter,ui-sans-serif,system-ui,sans-serif;text-anchor:middle}.coverage-ring .ring-number{font-size:6px;font-weight:750}.coverage-ring .ring-label{fill:var(--muted);font-size:3.2px;letter-spacing:.02em}.daily-note{border:1px solid var(--line);border-left:3px solid var(--blue);border-radius:0 8px 8px 0;background:rgba(17,27,67,.68)}#overview #markets{grid-template-columns:1fr}#overview #markets>.market{display:grid;grid-template-columns:minmax(0,1.28fr) minmax(330px,.72fr);gap:14px 18px;padding:20px}#overview #markets .market-head{grid-column:1/-1;padding-bottom:14px;border-bottom:1px solid var(--line)}#overview #markets .market-overview{grid-column:2;grid-row:2;margin:0;padding:14px;border:1px solid var(--line);border-radius:9px;background:var(--raised)}#overview #markets .market-metrics{grid-column:2;grid-row:3/5;margin:0;align-self:stretch;border-radius:9px;overflow:hidden}#overview #markets .market-result-note{grid-column:2;grid-row:5;margin:0;padding:12px;border:1px solid var(--line);border-radius:9px;background:var(--bg)}#overview #markets .chart-switch{grid-column:1;grid-row:2;align-self:start;display:flex;gap:4px;padding:4px;border:1px solid var(--line);border-radius:9px;background:var(--bg)}.chart-toggle{min-height:36px;padding:0 12px;border:1px solid transparent;border-radius:6px;background:transparent;color:var(--muted);font:inherit;font-size:.75rem;cursor:pointer}.chart-toggle:hover,.chart-toggle:focus-visible{color:var(--text);border-color:var(--line-strong);outline:none}.chart-toggle.active{border-color:#425a96;background:var(--raised-2);color:var(--text)}#overview #markets .market-price-chart,#overview #markets .chart:not(.market-price-chart){grid-column:1;grid-row:3/6;margin:0;padding:18px;border:1px solid var(--line);border-radius:9px;background:var(--bg)}#overview #markets .chart[hidden]{display:none}#overview #markets .market-price-chart svg,#overview #markets .chart:not(.market-price-chart) svg{height:252px}.chart-grid{stroke:#1d2a4d}.chart-market{stroke:var(--violet)}.chart-market-dot{fill:var(--violet)}.chart-paper{stroke:var(--blue)}.chart-hold{stroke:#596889}.market-overview,.market-metrics,.chart{border-color:var(--line)}.market-metrics>div,.limit-grid div{background:rgba(5,7,19,.76)}.market-status{white-space:nowrap}.activity-list{gap:7px}.activity-item{transition:border-color .15s ease,background .15s ease}.activity-item:hover{border-color:#344b80;background:var(--raised)}.automation-card{min-height:164px}.role-symbol{border-radius:9px}.experiment-card{border-color:#344b80}.experiment-card>.instruction-controls{background:linear-gradient(145deg,#101938,#0b1126)}.active-limit,.limit-grid,.market-metrics{border-radius:8px;overflow:hidden}.instruction-controls input:focus,.instruction-controls select:focus{outline:2px solid var(--blue);outline-offset:2px}.panel{animation:cockpit-enter .2s cubic-bezier(.2,.7,.2,1)}@keyframes cockpit-enter{from{opacity:.65;transform:translateY(5px)}to{opacity:1;transform:none}}@media(min-width:1100px){.metrics{grid-template-columns:minmax(310px,1.35fr) repeat(2,minmax(0,1fr))}.metric:first-child{min-height:190px}}@media(max-width:1100px){.topbar{grid-template-columns:auto 1fr}.header-state{grid-column:2;grid-row:1;justify-self:end}.tabs{grid-column:1/-1;grid-row:2;margin-bottom:10px}}@media(max-width:900px){#overview #markets>.market{display:block}#overview #markets .market-overview,#overview #markets .market-metrics,#overview #markets .market-result-note,#overview #markets .chart-switch,#overview #markets .market-price-chart,#overview #markets .chart:not(.market-price-chart){margin-top:14px}#overview #markets .market-price-chart svg,#overview #markets .chart:not(.market-price-chart) svg{height:150px}}@media(max-width:600px){body{background:var(--bg)}.shell{width:min(100% - 22px,1280px)}.topbar{display:grid;grid-template-columns:1fr auto;align-items:center;min-height:76px;padding-top:8px}.header-state{grid-column:2;grid-row:1}.trust-inner{padding-inline:0}.tabs{position:static;top:auto;margin:0 0 8px;padding:5px}.tab{font-size:.8rem}.checked{padding-inline:9px}.coverage-ring{width:74px}.metric.has-visual{column-gap:10px}.metric.has-visual .metric-value{font-size:1.45rem}.market-status{white-space:normal}.market-metrics{border-radius:8px}.chart-switch{display:grid!important;grid-template-columns:1fr 1fr}.chart-toggle{min-height:44px;padding-inline:8px}}@media(max-width:430px){.topbar{display:grid;grid-template-columns:1fr;align-items:start;gap:8px}.brand{grid-column:1;grid-row:1}.header-state{grid-column:1;grid-row:2;justify-self:start}.tabs{grid-column:1;grid-row:3;width:100%}.checked{align-self:auto}.trust{display:block}}@media(max-width:390px){.coverage-ring{width:66px}.metric.has-visual{grid-template-columns:minmax(0,1fr) 66px}.trust-inner{display:block}.trust strong{grid-column:auto}}`

const cockpitAccessibilityCSS = `.trust span{display:inline;color:var(--muted);font-size:.72rem;white-space:nowrap}.trust span:before{display:none}.chart-toggle:focus-visible{outline:2px solid var(--blue);outline-offset:2px}.coverage-ring .ring-number{font-size:6.8px}.coverage-ring .ring-label{font-size:5.4px}@media(max-width:600px){.header-state{grid-column:1/-1;grid-row:2;justify-self:start;flex-wrap:wrap;min-width:0}.tabs{grid-row:3}.trust span{white-space:normal}.coverage-ring{width:82px}}@media(max-width:390px){.coverage-ring{width:82px}.metric.has-visual{grid-template-columns:minmax(0,1fr) 82px}}`

const cockpitChoreographyCSS = `#overview #markets .market-meta{grid-column:1/-1;grid-row:2;display:grid;grid-template-columns:repeat(4,minmax(0,1fr));margin:0;border:1px solid var(--line);border-radius:9px;background:var(--bg);overflow:hidden}#overview #markets .market-meta>div{padding:11px 13px;border-right:1px solid var(--line)}#overview #markets .market-meta>div:last-child{border-right:0}.market-meta dt,.market-live-facts dt{margin:0 0 5px;color:var(--muted);font-size:.68rem;letter-spacing:.055em;text-transform:uppercase}.market-meta dd,.market-live-facts dd{margin:0;color:var(--text);font-size:.78rem;font-weight:650}.market-meta .badge{min-height:22px;padding-inline:7px}#overview #markets .chart-switch{grid-row:3}#overview #markets .market-overview{grid-row:3}#overview #markets .market-price-chart,#overview #markets .chart:not(.market-price-chart){grid-row:4/7}#overview #markets .market-metrics{grid-row:4/6}#overview #markets .market-result-note{grid-row:6}#overview #markets .market-bottom{grid-column:1/-1;grid-row:7;display:grid;grid-template-columns:minmax(0,.9fr) minmax(0,1.1fr);gap:14px}.market-detail-card{min-width:0;padding:17px;border:1px solid var(--line);border-radius:9px;background:var(--bg)}.market-detail-head{display:flex;align-items:center;justify-content:space-between;gap:12px;padding-bottom:12px;border-bottom:1px solid var(--line)}.market-detail-head h4{display:flex;align-items:center;gap:8px;margin:0;font-size:.88rem}.market-detail-head>span{color:var(--muted);font-size:.69rem}.live-dot{width:8px;height:8px;border-radius:50%;background:var(--blue)}.live-dot.green{background:var(--green)}.live-dot.amber{background:var(--amber)}.live-dot.red{background:var(--red)}.market-live-facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:1px;margin:12px 0 0;background:var(--line)}.market-live-facts>div{min-width:0;padding:10px 11px;background:var(--surface)}.market-live-facts dd{overflow-wrap:anywhere}.decision-sequence{display:grid;gap:0;margin:12px 0 0;padding:0;list-style:none}.decision-sequence li{display:grid;grid-template-columns:74px minmax(0,1fr);gap:11px;padding:9px 0;border-bottom:1px solid var(--line);color:var(--muted);font-size:.75rem;line-height:1.42}.decision-sequence li:last-child{border-bottom:0}.decision-sequence strong{color:var(--blue);font-size:.68rem;letter-spacing:.06em;text-transform:uppercase}@media(max-width:900px){#overview #markets .market-meta,#overview #markets .market-bottom{margin-top:14px}.market-bottom{grid-template-columns:1fr!important}}@media(max-width:600px){#overview #markets .market-meta{grid-template-columns:repeat(2,minmax(0,1fr))}#overview #markets .market-meta>div:nth-child(2){border-right:0}#overview #markets .market-meta>div:nth-child(-n+2){border-bottom:1px solid var(--line)}}@media(max-width:380px){#overview #markets .market-meta,.market-live-facts{grid-template-columns:1fr}#overview #markets .market-meta>div{border-right:0;border-bottom:1px solid var(--line)}#overview #markets .market-meta>div:last-child{border-bottom:0}.decision-sequence li{grid-template-columns:1fr;gap:3px}}`

const appJS = `const $=id=>document.getElementById(id);
let current=null;
let refreshReset;
let requestSequence=0;
let liveUpdates=true;
let instructionDirty=false;
let marketChartViews={};
const safe=value=>String(value??'').replace(/[&<>'"]/g,char=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[char]));
const integer=value=>{try{return BigInt(value||0);}catch{return 0n;}};
const decimal=(value,min,max)=>{let amount=integer(value),sign='';if(amount<0n){sign='-';amount=-amount;}const shift=10n**BigInt(6-max);amount=(amount+shift/2n)/shift;const base=10n**BigInt(max);const whole=amount/base;let fraction=(amount%base).toString().padStart(max,'0');while(fraction.length>min&&fraction.endsWith('0'))fraction=fraction.slice(0,-1);return sign+whole.toLocaleString()+(fraction?'.'+fraction:'');};
const money=micros=>integer(micros)>0n&&integer(micros)<10000n?'<$0.01':'$'+decimal(micros,2,2);
const price=micros=>{const amount=integer(micros),places=amount>=1000000n?2:amount>=10000n?4:6;return '$'+decimal(amount,2,places);};
const paperValue=(micros,unit)=>unit==='USD'?money(micros):decimal(micros,2,6)+' '+(unit||'units');
const assetAmount=(units,places,asset)=>{const amount=integer(units),digits=Math.max(0,Math.min(18,Number(places||0))),base=10n**BigInt(digits),whole=amount/base;let fraction=(amount%base).toString().padStart(digits,'0').replace(/0+$/,'');if(fraction.length>9)fraction=fraction.slice(0,9).replace(/0+$/,'');return whole.toLocaleString()+(fraction?'.'+fraction:'')+' '+safe(asset||'units');};
const unitsAsMicros=(units,places)=>{const digits=Math.max(0,Math.min(18,Number(places||0))),amount=integer(units);return digits>=6?amount/(10n**BigInt(digits-6)):amount*(10n**BigInt(6-digits));};
const initialLotValue=m=>{const asset=String(m.initial_lot_asset||'').toUpperCase(),base=String(m.name||'').split('/')[0].toUpperCase();if(asset==='USD'||asset.endsWith('USDC'))return unitsAsMicros(m.initial_lot_units,m.initial_lot_decimals);if(asset===base&&integer(m.price_micros)>0n)return integer(m.initial_lot_units)*integer(m.price_micros)/(10n**BigInt(Number(m.initial_lot_decimals||0)));return 0n;};
const exposure=(lot,capital)=>integer(capital)>0n?(Number(integer(lot)*10000n/integer(capital))/100).toFixed(1)+'%':'—';
const microsFromInput=id=>Math.round(Number($(id).value)*1000000);
const inputFromMicros=value=>(Number(integer(value))/1000000).toFixed(2);
const percent=bps=>(Number(bps||0)/100).toFixed(2).replace(/\.00$/,'')+'%';
const duration=seconds=>Number(seconds||0)>=60&&Number(seconds||0)%60===0?(Number(seconds)/60)+' min':Number(seconds||0)+' sec';
const deltaValue=(micros,unit)=>unit==='USD'&&integer(micros)<10000n?'<$0.01':unit==='USD'?money(micros):paperValue(micros,unit);
const resultDelta=(from,to,unit)=>{const value=integer(to)-integer(from);if(value===0n)return 'Unchanged';return (value>0n?'Up ':'Down ')+deltaValue(value>0n?value:-value,unit);};
const signedResult=(value,unit)=>{value=integer(value);if(value===0n)return 'Unchanged';return (value>0n?'Up ':'Down ')+deltaValue(value>0n?value:-value,unit);};
const changePercent=(from,to)=>{from=integer(from);to=integer(to);if(from===0n)return '';const change=to-from;if(change===0n)return '0.00%';const hundredths=change*10000n/from,absolute=hundredths<0n?-hundredths:hundredths;return (change>0n?'+':'-')+(Number(absolute)/100).toFixed(2)+'%';};
const resultWithPercent=(from,to,unit)=>{const percentage=changePercent(from,to);return resultDelta(from,to,unit)+(percentage?' ('+percentage+')':'');};
const comparisonDelta=(from,to,unit)=>{const value=integer(to)-integer(from);if(value===0n)return 'The same';return deltaValue(value>0n?value:-value,unit)+(value>0n?' better':' worse');};
const attempts=value=>Number(value||0)===1?'Plan tried to trade once':'Plan tried to trade '+Number(value||0)+' times';
const tone=value=>value>0n?'positive':value<0n?'negative':'neutral';
const age=value=>{if(!value)return 'Not updated';const seconds=Math.max(0,Math.round((Date.now()-Date.parse(value))/1000));if(seconds<10)return 'Updated just now';if(seconds<60)return 'Updated '+seconds+'s ago';const minutes=Math.round(seconds/60);if(minutes<60)return 'Updated '+minutes+'m ago';const hours=Math.round(minutes/60);if(hours<24)return 'Updated '+hours+'h ago';return 'Updated '+Math.round(hours/24)+'d ago';};
const eventTime=value=>value?new Date(value).toLocaleString([],{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}):'Not available';
const state=value=>({warming:'Learning recent prices',uptrend:'Market rising',downtrend:'Market falling',range:'Market moving sideways',volatile:'Waiting for calmer prices','order pending':'Paper order being checked','waiting for data':'Price data delayed',paused:'Paused by safety limit',watching:'Watching market'}[value]||'Watching market');
const decisionReason=value=>({'watching':'Watching the next price update','collecting_history':'Still learning recent prices','drawdown_limit':'Reducing risk after the loss limit','risk_halt':'New buys are paused by the loss limit','drawdown_halt':'New buys are paused by the loss limit','volatility_limit':'The market is moving too quickly','cooldown':'Taking a short break after a fill','trend_aligned_buy':'The trend supported a buy','sell_leg_waiting':'Waiting for a better sell move','trend_aligned_sell':'The trend supported a sell','buy_leg_waiting':'Waiting for a better buy move','range_high_sell':'Price reached the plan’s sell range','range_low_buy':'Price reached the plan’s buy range','signal_below_cost_hurdle':'The move is too small after costs','data_unavailable':'Fresh prices are unavailable','fee_budget_used':'Today’s simulated fee budget is used up','route_cost_limit':'The route was too expensive','order_pending':'A paper order is waiting to fill','order_filled':'The latest paper order filled','fill_limit':'Price moved beyond the fill limit','trade_unavailable':'The paper trade could not be priced or funded'}[value]||'Watching the market');
const eventGroup=kind=>kind.startsWith('order_')?'orders':kind.startsWith('strategy_')?'strategy':kind==='risk_halted'?'safety':kind.startsWith('data_')?'data':'other';
const marketStatus=(m,feeBudgetUsed)=>feeBudgetUsed?{label:'Orders paused',tone:'amber'}:m.risk_halted?{label:'New buys paused',tone:'red'}:m.fresh?{label:'Running',tone:'green'}:{label:'Waiting for data',tone:'amber'};

function chartPaths(points,key,minValue,span,start,end){
  const shapes=[];let segment=[];
  const finish=()=>{if(segment.length>1)shapes.push('<polyline class="chart-'+key+'" points="'+segment.join(' ')+'"></polyline>');else if(segment.length===1){const [x,y]=segment[0].split(',');shapes.push('<circle class="chart-'+key+'" cx="'+x+'" cy="'+y+'" r="1.7"></circle>');}segment=[];};
  points.forEach(point=>{if(point.unavailable){finish();return;}const at=Date.parse(point.at);if(!Number.isFinite(at))return;const x=end===start?50:(at-start)*100/(end-start);const y=50-Number((integer(point[key])-minValue)*4400n/span)/100;segment.push(x.toFixed(2)+','+y.toFixed(2));});
  finish();return shapes.join('');
}
function chartDots(points,minValue,span,start,end,unit){
  const available=points.filter(point=>!point.unavailable&&Number.isFinite(Date.parse(point.at)));
  if(!available.length)return '';
  const indexes=new Set([0,available.length-1]);
  for(let index=1;index<6&&index<available.length-1;index++)indexes.add(Math.round(index*(available.length-1)/6));
  return [...indexes].sort((a,b)=>a-b).map(index=>{const point=available[index],at=Date.parse(point.at),x=end===start?50:(at-start)*100/(end-start),y=50-Number((integer(point.equity_micros)-minValue)*4400n/span)/100;const label=new Date(at).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'})+' · Strategy '+paperValue(point.equity_micros,unit)+' · Holding '+paperValue(point.hold_benchmark_micros,unit);return '<circle class="chart-hit" cx="'+x.toFixed(2)+'" cy="'+y.toFixed(2)+'" r="1.4"><title>'+safe(label)+'</title></circle>';}).join('');
}
function marketPrices(m){
  const points=(m.history||[]).filter(point=>!point.unavailable&&integer(point.price_micros)>0n&&Number.isFinite(Date.parse(point.at))).map(point=>({...point}));
  const observed=Date.parse(m.observed_at),currentPrice=integer(m.price_micros);
  if(currentPrice>0n&&Number.isFinite(observed)){
    const last=points[points.length-1];
    if(last&&Date.parse(last.at)===observed)last.price_micros=String(currentPrice);
    else if(!last||integer(last.price_micros)!==currentPrice)points.push({at:m.observed_at,price_micros:String(currentPrice)});
  }
  return points;
}
function marketPriceDots(points,minValue,span,start,end){
  if(!points.length)return '';
  const indexes=new Set([0,points.length-1]);
  for(let index=1;index<5&&index<points.length-1;index++)indexes.add(Math.round(index*(points.length-1)/5));
  return [...indexes].sort((a,b)=>a-b).map(index=>{const point=points[index],at=Date.parse(point.at),x=end===start?50:(at-start)*100/(end-start),y=50-Number((integer(point.price_micros)-minValue)*4400n/span)/100;const label=new Date(at).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'})+' · '+price(point.price_micros);return '<circle class="chart-market-dot" cx="'+x.toFixed(2)+'" cy="'+y.toFixed(2)+'" r="1.4"><title>'+safe(label)+'</title></circle>';}).join('');
}
function marketPriceChart(m,hidden=false){
  const opening='<div class="chart market-price-chart" data-chart-panel="price"'+(hidden?' hidden':'')+'>';
  const points=marketPrices(m),period=m.fresh?'Market price today':'Last recorded market prices';
  if(points.length<2)return opening+'<div class="chart-head"><span class="chart-title">'+period+'</span><span>Open-to-now change</span></div><div class="chart-empty">Building the market chart…</div></div>';
  const values=points.map(point=>integer(point.price_micros));let minimum=values[0],maximum=values[0];values.forEach(value=>{if(value<minimum)minimum=value;if(value>maximum)maximum=value;});
  const span=maximum===minimum?1n:maximum-minimum,start=Date.parse(points[0].at),end=Date.parse(points[points.length-1].at);
  const line=chartPaths(points,'price_micros',minimum,span,start,end).replaceAll('chart-price_micros','chart-market');
  const first=points[0],last=points[points.length-1],change=integer(last.price_micros)-integer(first.price_micros),summary=m.name+' market price moved from '+price(first.price_micros)+' to '+price(last.price_micros)+' ('+changePercent(first.price_micros,last.price_micros)+').';
  return opening+'<div class="chart-head"><span class="chart-title">'+period+'</span><span class="chart-change '+tone(change)+'">'+safe(changePercent(first.price_micros,last.price_micros))+'</span></div><svg viewBox="0 0 100 56" preserveAspectRatio="none" role="img" aria-label="'+safe(summary)+'"><line class="chart-grid" x1="0" y1="6" x2="100" y2="6"></line><line class="chart-grid" x1="0" y1="28" x2="100" y2="28"></line><line class="chart-grid" x1="0" y1="50" x2="100" y2="50"></line>'+line+marketPriceDots(points,minimum,span,start,end)+'</svg><div class="chart-legend"><span>Started '+price(first.price_micros)+'</span><span>Now '+price(last.price_micros)+'</span></div></div>';
}
function performanceChart(m,hidden=false){
  const opening='<div class="chart" data-chart-panel="performance"'+(hidden?' hidden':'')+'>';
  const points=m.history||[],available=points.filter(point=>!point.unavailable);
  const period=m.fresh?'Our paper account today':'Last recorded paper account';
  if(available.length<2)return opening+'<div class="chart-head"><span>'+period+'</span><span>Max drawdown '+paperValue(m.max_drawdown_micros,m.value_unit)+'</span></div><div class="chart-empty">Building the performance chart…</div></div>';
  const values=available.flatMap(point=>[integer(point.equity_micros),integer(point.hold_benchmark_micros)]);
  let minimum=values[0],maximum=values[0];values.forEach(value=>{if(value<minimum)minimum=value;if(value>maximum)maximum=value;});
  const span=maximum===minimum?1n:maximum-minimum,start=Date.parse(points[0].at),end=Date.parse(points[points.length-1].at);
  const paper=chartPaths(points,'equity_micros',minimum,span,start,end).replaceAll('chart-equity_micros','chart-paper');
  const hold=chartPaths(points,'hold_benchmark_micros',minimum,span,start,end).replaceAll('chart-hold_benchmark_micros','chart-hold');
  if(!paper&&!hold)return opening+'<div class="chart-empty">Price-data gaps separate today’s observations.</div></div>';
  const first=available[0],last=available[available.length-1],gaps=points.filter(point=>point.unavailable).length;
  const summary=m.name+' estimated paper account value moved from '+paperValue(first.equity_micros,m.value_unit)+' to '+paperValue(last.equity_micros,m.value_unit)+' ('+resultDelta(first.equity_micros,last.equity_micros,m.value_unit)+'). Holding comparison moved from '+paperValue(first.hold_benchmark_micros,m.value_unit)+' to '+paperValue(last.hold_benchmark_micros,m.value_unit)+' ('+resultDelta(first.hold_benchmark_micros,last.hold_benchmark_micros,m.value_unit)+'). '+gaps+' unavailable interval'+(gaps===1?'':'s')+'.';
  const dots=chartDots(points,minimum,span,start,end,m.value_unit);
  return opening+'<div class="chart-head"><span class="chart-title">'+period+'</span><span>Largest drop '+paperValue(m.max_drawdown_micros,m.value_unit)+'</span></div><svg viewBox="0 0 100 56" preserveAspectRatio="none" role="img" aria-label="'+safe(summary)+'"><line class="chart-grid" x1="0" y1="6" x2="100" y2="6"></line><line class="chart-grid" x1="0" y1="28" x2="100" y2="28"></line><line class="chart-grid" x1="0" y1="50" x2="100" y2="50"></line>'+hold+paper+dots+'</svg><div class="chart-legend"><span>Our strategy '+paperValue(last.equity_micros,m.value_unit)+'</span><span>If held '+paperValue(last.hold_benchmark_micros,m.value_unit)+'</span></div></div>';
}

const tabs=[...document.querySelectorAll('.tab')];
function selectTab(button,focus=false){
  tabs.forEach(item=>{const active=item===button;item.classList.toggle('active',active);item.setAttribute('aria-selected',String(active));item.tabIndex=active?0:-1;});
  document.querySelectorAll('.panel').forEach(panel=>{const active=panel.id===button.dataset.tab;panel.hidden=!active;panel.classList.toggle('active',active);});
  if(focus)button.focus();
}
tabs.forEach((button,index)=>{button.addEventListener('click',()=>selectTab(button));button.addEventListener('keydown',event=>{let next;if(event.key==='ArrowRight')next=(index+1)%tabs.length;else if(event.key==='ArrowLeft')next=(index+tabs.length-1)%tabs.length;else if(event.key==='Home')next=0;else if(event.key==='End')next=tabs.length-1;else return;event.preventDefault();selectTab(tabs[next],true);});});
$('refresh').addEventListener('click',()=>load(true));
$('live').addEventListener('click',()=>{liveUpdates=!liveUpdates;$('live').setAttribute('aria-pressed',String(liveUpdates));$('live').textContent='Live updates: '+(liveUpdates?'On':'Paused');$('refresh-status').textContent=liveUpdates?'Live updates turned on':'Live updates paused';if($('notice').textContent.startsWith('Dashboard status is unavailable.'))setNotice('Dashboard status is unavailable. '+(liveUpdates?'It will retry automatically.':'Use Refresh to try again.'));if(liveUpdates&&!$('refresh').disabled)load();});
$('activity-filter').addEventListener('change',renderActivity);
$('open-order-history').addEventListener('click',()=>{$('activity-filter').value='orders';renderActivity();selectTab($('tab-activity'),true);});
$('markets').addEventListener('click',event=>{const button=event.target.closest('.chart-toggle');if(!button)return;const card=button.closest('.market'),view=button.dataset.chartView;marketChartViews[card.dataset.market]=view;card.querySelectorAll('.chart-toggle').forEach(item=>{const active=item===button;item.classList.toggle('active',active);item.setAttribute('aria-pressed',String(active));});card.querySelectorAll('[data-chart-panel]').forEach(panel=>panel.hidden=panel.dataset.chartPanel!==view);});
$('instruction-market').addEventListener('change',()=>instructionDirty=true);
$('instruction-preference').addEventListener('change',()=>instructionDirty=true);
['instruction-capital','instruction-minimum','instruction-maximum','instruction-cadence','instruction-drawdown'].forEach(id=>$(id).addEventListener('input',()=>{instructionDirty=true;renderInstructionWarning();}));
$('save-instruction').addEventListener('click',saveInstruction);
document.addEventListener('click',event=>{const selected=event.target.closest('.help');document.querySelectorAll('.help[aria-expanded="true"]').forEach(button=>{if(button!==selected)button.setAttribute('aria-expanded','false');});if(selected)selected.setAttribute('aria-expanded',String(selected.getAttribute('aria-expanded')!=='true'));else if(document.activeElement?.classList.contains('help'))document.activeElement.blur();});
document.addEventListener('keydown',event=>{if(event.key!=='Escape')return;document.querySelectorAll('.help[aria-expanded="true"]').forEach(button=>button.setAttribute('aria-expanded','false'));});

function help(label,text){const id='help-'+label.toLowerCase().replace(/[^a-z0-9]+/g,'-');return '<button type="button" class="help" aria-label="Explain '+safe(label)+'" aria-describedby="'+id+'" aria-expanded="false">?<span id="'+id+'" class="help-tip" role="tooltip">'+safe(text)+'</span></button>';}
function metric(label,value,foot,klass='',explanation='',cardClass='',visual=''){
  return '<article class="metric '+cardClass+(visual?' has-visual':'')+'"><span class="metric-label"><span>'+safe(label)+'</span>'+help(label,explanation)+'</span><strong class="metric-value '+klass+'">'+safe(value)+'</strong>'+visual+'<span class="metric-foot">'+safe(foot)+'</span></article>';
}
function coverageRing(bps){
  const value=Math.max(0,Math.min(100,Number(bps||0)/100)),rest=100-value,label=value.toFixed(1).replace(/\.0$/,'');
  return '<svg class="coverage-ring" viewBox="0 0 42 42" role="img" aria-label="Usable price data '+safe(label)+' percent"><circle class="ring-track" cx="21" cy="21" r="15.9"></circle><circle class="ring-value" cx="21" cy="21" r="15.9" pathLength="100" stroke-dasharray="'+value.toFixed(2)+' '+rest.toFixed(2)+'" transform="rotate(-90 21 21)"></circle><text class="ring-number" x="21" y="20.5">'+safe(label)+'%</text><text class="ring-label" x="21" y="25">price data</text></svg>';
}
function renderMetrics(){
  if(!current.complete){$('metrics').innerHTML=metric('Paper value now','—','Waiting for all markets','','Current value of all simulated cash and coins. It is not a real wallet balance.')+metric('Started today with','—','Waiting for current data','','Value assigned to today’s independent paper test when its books opened.')+metric("Today's result",'—','Waiting for current data','','Change across every paper market since today started. It includes sold and unsold positions.')+metric('Total traded today','—','Waiting for current data','','Sum of every filled buy and sell. This is activity, not profit.','trade-volume')+metric('Filled paper orders','—','Waiting for current data','','Simulated buy or sell orders that finished. More filled orders do not necessarily mean more profit.');return;}
  const o=current.overview||{};
  const pnl=integer(o.equity_micros)-integer(o.opening_equity_micros);
  const hold=integer(o.equity_micros)-integer(o.hold_benchmark_micros);
  const coverage=o.coverage_ready?'Price data '+(Number(o.coverage_bps||0)/100).toFixed(2)+'%':'Price data warming';
  const breakdown=o.accounting_tracked?'Closed '+signedResult(o.realized_micros,o.value_unit)+' · Open '+signedResult(o.unrealized_micros,o.value_unit):'Sold/open breakdown updating';
  const holdingText=hold===0n?'Same as holding':comparisonDelta(o.hold_benchmark_micros,o.equity_micros,o.value_unit)+' than holding';
  $('metrics').innerHTML=metric('Paper value now',paperValue(o.equity_micros,o.value_unit),'Simulated cash + coins','neutral','Current value of all simulated cash and coins. It is not a real wallet balance.','portfolio-card',coverageRing(o.coverage_bps))+
    metric('Started today with',paperValue(o.opening_equity_micros,o.value_unit),'Daily test opening value','neutral','Value assigned to today’s independent paper test when its books opened.')+
    metric("Today's result",resultWithPercent(o.opening_equity_micros,o.equity_micros,o.value_unit),breakdown+' · '+holdingText,tone(pnl),'Change across every paper market since today started. Closed means inventory already sold; open means the mark-to-market result still held. The holding comparison asks what happened if the starting assets were left untouched.')+
    metric('Total traded today',paperValue(o.turnover_micros,o.value_unit),'Across buys and sells · Modeled fees '+paperValue(o.fees_micros,o.value_unit),'neutral','Turnover adds the value of every filled buy and sell. It can be much larger than the paper account because the same money can be reused. It is not profit.','trade-volume')+
    metric('Filled paper orders',String(o.trades||0),coverage+' · '+attempts(o.signals),'neutral','The plan checked '+Number(o.signals||0)+' possible trades; only filled orders changed the paper account. Price-data coverage shows how much of today had usable independent prices. More filled orders do not necessarily mean more profit.');
}
function marketCard(m){
  if(!m.available)return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge red">Unavailable</span></div><p class="strategy-next">Status is unavailable</p><p class="market-context">Other markets are unaffected.</p></article>';
  if(!m.ready)return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge amber">Updating</span></div><p class="strategy-next">Waiting for market data</p></article>';
  const feeBudgetUsed=m.fresh&&Boolean(m.fee_budget_tracked)&&!Number(m.estimated_fills_remaining||0);
  const status=marketStatus(m,feeBudgetUsed);
  const badge='<span class="badge '+status.tone+'">'+status.label+'</span>';
  const pnl=integer(m.equity_micros)-integer(m.opening_equity_micros);
  const holding=integer(m.equity_micros)-integer(m.hold_benchmark_micros);
  const action=feeBudgetUsed?'Orders paused for today':m.risk_halted?'New buys paused':!m.fresh?'Waiting for fresh prices':m.next_action?'Looking to '+safe(m.next_action):'Watching';
  const actionNote=feeBudgetUsed?(m.risk_halted?'Simulated fee budget is used up; new buys are also paused by the loss limit':'Simulated SOL for fees is used up'):m.risk_halted?'Sells can still reduce risk':!m.fresh?'The plan waits until prices recover':m.next_action?'Only if the opportunity is strong enough':state(m.state);
  const marketPrice=m.price_micros?price(m.price_micros):'Learning';
  const priceLabel=m.fresh?'Market price':'Last market price';
  const resultLabel=m.fresh?"Today's result":'Last result';
  const coverage=m.coverage_ready?'Price data '+(Number(m.coverage_bps||0)/100).toFixed(2)+'%':'Price data warming';
  const prices=marketPrices(m),openPrice=prices.length?prices[0].price_micros:0,marketChange=openPrice?changePercent(openPrice,m.price_micros):'Building';
  const closed=m.accounting_tracked?signedResult(m.realized_micros,m.value_unit):'Updating';
  const open=m.accounting_tracked?signedResult(m.unrealized_micros,m.value_unit):'Updating';
  const holdingText=holding===0n?'Same as holding':comparisonDelta(m.hold_benchmark_micros,m.equity_micros,m.value_unit)+' than holding';
  const chartView=marketChartViews[m.name]==='performance'?'performance':'price';
  const adaptive=m.strategy==='adaptive';
  const planName=adaptive?'Market-responsive plan':m.strategy==='fixed'?'Fixed paper plan':'Paper plan';
  const planStatus=status.label;
  const planTone=status.tone;
  const moveThreshold=m.minimum_signal_bps?percent(m.minimum_signal_bps):'the configured minimum';
  const volatilityThreshold=m.max_volatility_bps?percent(m.max_volatility_bps):'the configured limit';
  const lossStop=m.max_drawdown_bps?percent(m.max_drawdown_bps):'the configured';
  const chartSwitch='<div class="chart-switch" role="group" aria-label="'+safe(m.name)+' chart"><button class="chart-toggle '+(chartView==='price'?'active':'')+'" type="button" data-chart-view="price" aria-pressed="'+String(chartView==='price')+'">Market price</button><button class="chart-toggle '+(chartView==='performance'?'active':'')+'" type="button" data-chart-view="performance" aria-pressed="'+String(chartView==='performance')+'">Paper vs holding</button></div>';
  const meta='<dl class="market-meta"><div><dt>Status</dt><dd><span class="badge '+planTone+'">'+safe(planStatus)+'</span></dd></div><div><dt>Paper day</dt><dd>'+safe(m.day?m.day+' UTC':'Not available')+'</dd></div><div><dt>Mode</dt><dd>Paper simulation</dd></div><div><dt>Checks</dt><dd>'+safe(m.tick_seconds?'Every '+duration(m.tick_seconds):'Updating')+'</dd></div></dl>';
  const overview='<div class="market-overview"><div><span class="market-label">'+priceLabel+'</span><strong class="market-price">'+marketPrice+'</strong></div><div><span class="market-label">Current plan</span><strong class="market-plan">'+action+'</strong><span class="market-context">'+safe(actionNote)+'</span></div></div>';
  const facts='<div class="market-metrics"><div><span>Market today</span><strong class="'+tone(openPrice?integer(m.price_micros)-integer(openPrice):0n)+'">'+safe(marketChange)+'</strong><span class="market-detail">'+(openPrice?'Started '+price(openPrice):'Collecting today’s prices')+'</span></div><div><span>'+resultLabel+'</span><strong class="'+tone(pnl)+'">'+resultWithPercent(m.opening_equity_micros,m.equity_micros,m.value_unit)+'</strong><span class="market-detail">Started '+paperValue(m.opening_equity_micros,m.value_unit)+'</span></div><div><span>Paper value now</span><strong>'+paperValue(m.equity_micros,m.value_unit)+'</strong><span class="market-detail">'+safe(holdingText)+'</span></div><div><span>Closed-trade result</span><strong class="'+tone(integer(m.realized_micros))+'">'+closed+'</strong><span class="market-detail">Inventory already sold</span></div><div><span>Open-position result</span><strong class="'+tone(integer(m.unrealized_micros))+'">'+open+'</strong><span class="market-detail">Inventory still held</span></div><div><span>Filled orders</span><strong>'+safe(String(m.trades||0))+'</strong><span class="market-detail">'+safe(coverage+' · '+attempts(m.signals))+'</span></div></div>';
  const note='<p class="market-result-note">Closed-trade result + open-position result = today’s result. A sell does not guarantee profit; its fill price and the account result after it remain in Activity.</p>';
  const live='<section class="market-detail-card"><div class="market-detail-head"><h4><span class="live-dot '+planTone+'" aria-hidden="true"></span>Paper rule status</h4><span>'+safe(planStatus)+'</span></div><dl class="market-live-facts"><div><dt>Price checks</dt><dd>'+safe(String(m.checks||0))+'</dd></div><div><dt>Trade opportunities</dt><dd>'+safe(String(m.signals||0))+'</dd></div><div><dt>Filled orders</dt><dd>'+safe(String(m.trades||0))+'</dd></div><div><dt>Total traded</dt><dd>'+safe(paperValue(m.turnover_micros,m.value_unit))+'</dd></div><div><dt>Modeled fees</dt><dd>'+safe(paperValue(m.fees_micros,m.value_unit))+'</dd></div><div><dt>Current state</dt><dd>'+safe(state(m.state))+'</dd></div><div><dt>Next possible side</dt><dd>'+safe(m.next_action||'Not selected')+'</dd></div><div><dt>Why now</dt><dd>'+safe(decisionReason(m.decision_reason))+'</dd></div></dl></section>';
  const scoreDelay=m.settle_seconds?duration(m.settle_seconds):'the configured delay';
  const impactLimit=m.max_quote_impact_bps?percent(m.max_quote_impact_bps):'the configured limit';
  const initialLot=initialLotValue(m),lotText=initialLot?paperValue(initialLot,m.value_unit):'the configured starting lot';
  const fillsLeft=m.fee_budget_tracked?(Number(m.estimated_fills_remaining||0)?String(m.estimated_fills_remaining)+' estimated fills remain':'fee budget used'):'fee budget not reported';
  const decisionRule=adaptive?'<li><strong>Learn</strong><span>Compare the short and long trend across '+safe(m.fast_window&&m.slow_window?m.fast_window+' and '+m.slow_window:'the configured')+' price checks.</span></li><li><strong>Gate</strong><span>Require at least '+safe(moveThreshold)+' movement; stop above '+safe(volatilityThreshold)+' volatility, '+safe(impactLimit)+' quote impact, or the '+safe(lossStop)+' loss limit.</span></li>':'<li><strong>Match</strong><span>Evaluate the saved fixed paper-price rules for the next side.</span></li><li><strong>Gate</strong><span>Apply every safety and cost limit reported by this plan before a paper order can continue.</span></li>';
  const afterFill=m.cooldown_seconds?'Wait '+duration(m.cooldown_seconds)+' before another filled order.':adaptive?'No post-fill pause is reported.':'Record the result, then evaluate the next saved fixed rule.';
  const sequence='<section class="market-detail-card"><div class="market-detail-head"><h4>Decision sequence</h4><span>'+safe(planName)+'</span></div><ol class="decision-sequence"><li><strong>Observe</strong><span>Check '+safe(m.name)+' every '+safe(m.tick_seconds?duration(m.tick_seconds):'configured interval')+'.</span></li>'+decisionRule+'<li><strong>Score</strong><span>Wait '+safe(scoreDelay)+' before scoring the paper order. The score can still refuse it.</span></li><li><strong>Outcome</strong><span>'+safe(decisionReason(m.decision_reason))+'. Next possible side: '+safe(m.next_action||'not selected')+'.</span></li><li><strong>After fill</strong><span>'+safe(afterFill)+'</span></li><li><strong>Paper bounds</strong><span>Starting lot '+safe(lotText)+'; slippage ceiling '+safe(m.slippage_bps?percent(m.slippage_bps):'configured')+'; '+safe(fillsLeft)+'. No live order is sent.</span></li></ol></section>';
  return '<article class="market" data-market="'+safe(m.name)+'"><div class="market-head"><h3>'+safe(m.name)+'</h3><div class="market-status"><span class="updated">'+safe(age(m.observed_at))+'</span>'+badge+'</div></div>'+meta+chartSwitch+marketPriceChart(m,chartView!=='price')+performanceChart(m,chartView!=='performance')+overview+facts+note+'<div class="market-bottom">'+live+sequence+'</div></article>';
}
function strategyCard(m){
  const unavailable=!m.available,label=unavailable?'Unavailable':m.ready?(m.strategy==='adaptive'?'Market-responsive paper plan':m.strategy||'Saved plan'):'Updating';
	const feeBudgetUsed=m.fresh&&Boolean(m.fee_budget_tracked)&&!Number(m.estimated_fills_remaining||0);
  const next=unavailable?'Status source unavailable':!m.ready?'Waiting for status':feeBudgetUsed?'Orders paused until tomorrow; simulated SOL for fees is used up':!m.fresh?'Waiting for fresh prices':m.risk_halted?'New buys paused; sells can still reduce risk':m.next_action?'If a good opportunity appears: '+m.next_action:'No next side yet';
  const status=unavailable?'Unavailable':m.ready?state(m.state):'Status updating';
  const trades=unavailable||!m.ready?'—':(m.trades||0)+' filled paper orders · '+attempts(m.signals).toLowerCase()+' '+(m.fresh?'today':'in the last recorded session');
  if(unavailable||!m.ready)return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge '+(unavailable?'red':'amber')+'">'+safe(label)+'</span></div><p class="strategy-next">'+safe(next)+'</p><div class="market-state"><strong>'+safe(status)+'</strong><span>'+safe(trades)+'</span></div></article>';
  const lot=m.initial_lot_units?assetAmount(m.initial_lot_units,m.initial_lot_decimals,m.initial_lot_asset):'Updating';
  const lotValue=initialLotValue(m),lotUSD=lotValue?paperValue(lotValue,m.value_unit):'Value updating';
  const reserve=m.fee_reserve_lamports?assetAmount(m.fee_reserve_lamports,9,'SOL'):'None';
  const feeLeft=m.fee_budget_tracked?(m.estimated_fills_remaining?String(m.estimated_fills_remaining)+' estimated orders':'No more orders today'):'Not tracked';
	const feeReserveLeft=m.fee_budget_tracked?assetAmount(m.remaining_fee_reserve_lamports||0,9,'SOL'):'Not tracked';
  const cadence=m.tick_seconds?'Every '+duration(m.tick_seconds):'Updating';
  const settle=m.settle_seconds?duration(m.settle_seconds):'Updating';
	const why=decisionReason(m.decision_reason);
  const limits='<dl class="limit-grid">'+
    '<div><dt>Today\'s starting paper value</dt><dd>'+safe(paperValue(m.opening_equity_micros,m.value_unit))+'</dd></div>'+
    '<div><dt>Starting trade lot</dt><dd>'+lot+' · '+safe(lotUSD)+' ('+safe(exposure(lotValue,m.opening_equity_micros))+' of this market)</dd></div>'+
    '<div><dt>Total traded today</dt><dd>'+safe(paperValue(m.turnover_micros,m.value_unit))+'</dd></div>'+
    '<div><dt>Modeled fees today</dt><dd>'+safe(paperValue(m.fees_micros,m.value_unit))+'</dd></div>'+
    '<div><dt>Fee reserve</dt><dd>'+reserve+'</dd></div>'+
		'<div><dt>Fee budget left</dt><dd>'+feeReserveLeft+'</dd></div>'+
		'<div><dt>Orders left today</dt><dd>'+safe(feeLeft)+'</dd></div>'+
    '<div><dt>Loss pause</dt><dd>'+safe(m.max_drawdown_bps?percent(m.max_drawdown_bps):'Not set')+' below today\'s high</dd></div>'+
    '<div><dt>Minimum opportunity</dt><dd>'+safe(m.minimum_signal_bps?percent(m.minimum_signal_bps):'Not set')+'</dd></div>'+
    '<div><dt>After a fill</dt><dd>Wait '+safe(duration(m.cooldown_seconds))+'</dd></div>'+
    '<div><dt>Price checks</dt><dd>'+safe(cadence)+'</dd></div>'+
    '<div><dt>Order scoring delay</dt><dd>'+safe(settle)+'</dd></div>'+
    '<div><dt>Slippage ceiling</dt><dd>'+safe(m.slippage_bps?percent(m.slippage_bps):'Not set')+'</dd></div>'+
    '<div><dt>Route impact ceiling</dt><dd>'+safe(m.max_quote_impact_bps?percent(m.max_quote_impact_bps):'Not set')+'</dd></div>'+
    '<div><dt>Volatility pause</dt><dd>'+safe(m.max_volatility_bps?percent(m.max_volatility_bps):'Not set')+'</dd></div>'+
    '<div><dt>Price memory</dt><dd>'+safe(m.fast_window&&m.slow_window?m.fast_window+' / '+m.slow_window+' checks':'Not set')+'</dd></div></dl>';
  const note='<p class="limit-note"><strong>Why now:</strong> '+safe(why)+'</p><p class="limit-note">Later orders use the simulated proceeds, so the lot is not a fixed dollar order. Absolute profit is the percentage return multiplied by this paper capital; a larger mandate scales gains and losses, not strategy quality.</p>';
  return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge blue">'+safe(label)+'</span></div><p class="strategy-next">'+safe(next)+'</p><div class="market-state"><strong>'+safe(status)+'</strong><span>'+safe(trades)+'</span></div>'+limits+note+'</article>';
}
function experimentDefaults(){
  let capital=integer(current?.overview?.opening_equity_micros||0);
  if(capital<10000000n)capital=100000000n;
  const minimum=capital/100n>1000000n?capital/100n:1000000n;
  const maximum=capital/4n>minimum?capital/4n:minimum;
  return {paper_capital_micros:capital,minimum_order_micros:minimum,maximum_order_micros:maximum,cadence_seconds:60,max_drawdown_bps:300};
}
function instructionRequest(){
  return {market:$('instruction-market').value,preference:$('instruction-preference').value,paper_capital_micros:microsFromInput('instruction-capital'),minimum_order_micros:microsFromInput('instruction-minimum'),maximum_order_micros:microsFromInput('instruction-maximum'),cadence_seconds:Number($('instruction-cadence').value),max_drawdown_bps:Math.round(Number($('instruction-drawdown').value)*100)};
}
function validInstructionRequest(request){return Number.isSafeInteger(request.paper_capital_micros)&&request.paper_capital_micros>=10000000&&request.paper_capital_micros<=1000000000000&&Number.isSafeInteger(request.minimum_order_micros)&&request.minimum_order_micros>=1000000&&Number.isSafeInteger(request.maximum_order_micros)&&request.maximum_order_micros>=request.minimum_order_micros&&request.maximum_order_micros<=request.paper_capital_micros&&[5,15,30,60,300].includes(request.cadence_seconds)&&Number.isInteger(request.max_drawdown_bps)&&request.max_drawdown_bps>=10&&request.max_drawdown_bps<=5000;}
function renderActiveLimits(){
  const markets=(current?.markets||[]).filter(m=>m.available&&m.ready);
  $('active-limit-list').innerHTML=markets.length?markets.map(m=>{const lot=initialLotValue(m),share=exposure(lot,m.opening_equity_micros);return '<div class="active-limit"><strong>'+safe(m.name)+'</strong><span>'+safe(paperValue(m.opening_equity_micros,m.value_unit))+' paper capital</span><small>First leg '+safe(lot?paperValue(lot,m.value_unit):'updating')+' · '+safe(share)+' concentration · checks '+safe(m.tick_seconds?'every '+duration(m.tick_seconds):'updating')+' · '+safe(paperValue(m.turnover_micros,m.value_unit))+' traded today</small></div>';}).join(''):'<span class="market-context">Waiting for current paper-plan limits.</span>';
}
function renderInstructionWarning(){
  const warning=$('instruction-warning'),request=instructionRequest();
  if(!validInstructionRequest(request)){warning.textContent='Check the limits: $10 or more capital, $1 or more per order, smallest ≤ largest ≤ capital, a listed check speed, and a 0.1%–50% loss stop.';return;}
  const concentration=request.maximum_order_micros*100/request.paper_capital_micros,onePercent=Math.round(request.paper_capital_micros/100);
  warning.textContent=(concentration>50?'High concentration: one order could use '+concentration.toFixed(1)+'% of the paper account. ':'Largest order is '+concentration.toFixed(1)+'% of the paper account. ')+'A 1% net move on this capital is about '+money(onePercent)+' up or down.'+(request.max_drawdown_bps>1000?' A loss stop above 10% is also a high-risk paper test.':'');
}
function renderInstruction(){
	const card=$('research-instruction');
	card.hidden=!current.instructions_enabled;
	card.style.display=card.hidden?'none':'';
	if(card.hidden)return;
  renderActiveLimits();
  const market=$('instruction-market'),selected=market.value||'all';
  const options=[...new Set(['all',...current.markets.map(item=>item.name),...(current.research_markets||[])])];
  if([...market.options].map(option=>option.value).join('|')!==options.join('|'))market.innerHTML=options.map(value=>'<option value="'+safe(value)+'">'+(value==='all'?'All paper markets':safe(value))+'</option>').join('');
  if(instructionDirty){if(options.includes(selected))market.value=selected;renderInstructionWarning();return;}
  const saved=current.instruction;
  market.value=saved&&options.includes(saved.market)?saved.market:'all';
  $('instruction-preference').value=saved?.preference||'balanced';
  const requested=saved&&Number(saved.version)>=2?saved:experimentDefaults();
  $('instruction-capital').value=inputFromMicros(requested.paper_capital_micros);
  $('instruction-minimum').value=inputFromMicros(requested.minimum_order_micros);
  $('instruction-maximum').value=inputFromMicros(requested.maximum_order_micros);
  $('instruction-cadence').value=String(requested.cadence_seconds||60);
  $('instruction-drawdown').value=(Number(requested.max_drawdown_bps||300)/100).toFixed(1);
  $('instruction-status').textContent=current.instruction_error?'Saved request is unavailable and will not be used.':saved&&Number(saved.version)>=2?'Saved for the next validated paper experiment. The active plans above have not changed.':saved?'Older research preference loaded. Add experiment limits to update it.':'No experiment request saved yet.';
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
    current.instruction=await response.json();current.instruction_error=false;instructionDirty=false;renderInstruction();
  }catch(error){status.textContent='Could not save. The active paper plans were not changed.';}
  finally{button.disabled=false;button.textContent='Save experiment request';}
}
function renderMarkets(){
  const focused=document.activeElement?.closest('.chart-toggle');
  const focusMarket=focused?.closest('.market')?.dataset.market,focusView=focused?.dataset.chartView;
  $('markets').innerHTML=current.markets.length?current.markets.map(marketCard).join(''):'<div class="empty">No paper markets configured.</div>';
  if(focusMarket&&focusView){[...document.querySelectorAll('.market')].find(card=>card.dataset.market===focusMarket)?.querySelector('[data-chart-view="'+focusView+'"]')?.focus({preventScroll:true});}
  $('strategy-markets').innerHTML=current.markets.map(strategyCard).join('');
  renderInstruction();
}
function activityUSD(amount){
  const value=Number(amount);
  if(!Number.isFinite(value))return '$'+amount;
  return value>0&&value<.005?'<$0.01':'$'+value.toFixed(2);
}
function readableActivityResult(line){
  const value=line.match(/^(?:This market value|Paper value now): \$([0-9]+(?:\.[0-9]+)?)$/);
  if(value)return 'Paper value now: '+activityUSD(value[1]);
  const result=line.match(/^((?:This market(?:'s result)? (?:today|gain\/loss)|Today's result after trade):) (up|down) \$([0-9]+(?:\.[0-9]+)?)(.*)$/);
  if(!result)return line;
  const profit=result[2]==='up';
  return result[1]+' '+(profit?'🟢 ▲ ':'🔴 ▼ ')+activityUSD(result[3])+' ('+(profit?'profit':'loss')+')'+result[4];
}
function readableActivity(message){
  const lines=String(message||'').split('\n');
  lines[0]=(lines[0]||'').replace(/SELL filled/i,'SOLD').replace(/BUY filled/i,'BOUGHT');
  for(let index=1;index<lines.length;index++){
    lines[index]=readableActivityResult(lines[index]
      .replace('Practice account:','This market value:').replace('Total paper account:','This market value:').replace(/^Equity /,'This market value: ')
      .replace(/^Paper value /,'This market value: ').replace(/^Result:/,'This market gain/loss:')
      .replace('Gain/loss today:',"This market's result today:").replace("Today's result:","This market's result today:").replace("Today's estimated paper value:","This market's result today:")
      .replaceAll('better than no trading','better than holding').replaceAll('worse than no trading','worse than holding').replaceAll('same as no trading','same as holding')
      .replace(/\b1 trade\b/g,'1 filled paper order').replace(/\b(\d+) trades\b/g,'$1 filled paper orders')
      .replace('Versus no trading:','Compared with holding:').replace('Compared with no trading:','Compared with holding:'));
  }
  if(/\b(?:BOUGHT|SOLD)\b/.test(lines[0]))for(let index=1;index<lines.length;index++)lines[index]=lines[index].replace('This market value:','Paper value now:').replace("This market's result today:","Today's result after trade:");
  if(lines[1]?.includes(' → ')){
    const [movement,...rest]=lines[1].split(' · '),[from,to]=movement.split(' → ');
    if(from&&to){const sell=/\bSOLD\b/i.test(lines[0]);lines.splice(1,1,(sell?'Sold ':'Paid ')+from,(sell?'Received ':'Bought ')+to,...rest);}
  }
  return lines.join('\n');
}
function renderActivity(){
  if(!current)return;
  const filter=$('activity-filter').value;
  const important=new Set(['order_opened','order_filled','strategy_active','strategy_changed','risk_halted','data_unavailable','data_restored','period_closed']);
  const items=current.activity.filter(item=>filter==='all'||filter==='important'&&important.has(item.kind)||eventGroup(item.kind)===filter);
  const opened=current.activity.filter(item=>item.kind==='order_opened').length,filled=current.activity.filter(item=>item.kind==='order_filled').length;
  const omitted=Number(current.activity_omitted||0);
  $('activity-summary').textContent=items.length+' shown · Recent retained totals: '+opened+' opened, '+filled+' filled'+(omitted?' · '+omitted+' older events omitted':'');
  $('activity-list').innerHTML=items.length?items.map(item=>{
    const lines=readableActivity(item.message).split('\n');
    const title=(lines.shift()||item.kind).replace(/^PAPER · /,'');
    return '<article class="activity-item"><span class="event-mark '+safe(item.kind)+'" aria-hidden="true"></span><div class="activity-copy"><h3>'+safe(item.market)+' · '+safe(title)+'</h3><p>'+safe(lines.join('\n'))+'</p></div><time class="activity-time" datetime="'+safe(item.at)+'">'+safe(eventTime(item.at))+'</time></article>';
  }).join(''):'<div class="empty">No matching activity yet.</div>';
}
function automationCard(klass,symbol,title,label,toneName,description){return '<article class="automation-card '+klass+'"><div class="automation-head"><span class="role-symbol" aria-hidden="true">'+safe(symbol)+'</span><span class="badge '+toneName+'">'+safe(label)+'</span></div><h3>'+safe(title)+'</h3><p>'+safe(description)+'</p></article>';}
function researchView(){
  if(!current.research_enabled)return {label:'Not connected',tone:'amber',description:'No validated Hermes packet path is configured.',detail:'Hermes remains outside the trading and wallet boundary.'};
  if(current.research_error)return {label:'Rejected output',tone:'red',description:'The latest output did not pass the Mithril packet checks.',detail:'No proposal or policy change was accepted.'};
  const packet=current.research;
  if(!packet)return {label:'No valid run yet',tone:'amber',description:'Waiting for the first validated source-cited packet.',detail:'The paper plans continue without Hermes input.'};
  const evidence=packet.verified_facts+' verified fact'+(packet.verified_facts===1?'':'s')+' from '+packet.sources+' source'+(packet.sources===1?'':'s');
  if(!packet.current)return {label:'Expired',tone:'amber',description:safe(packet.market)+' research expired. '+evidence+'.',detail:'It cannot be used for a new paper experiment.'};
  const passed=packet.risk_decision==='pass';
  const label=packet.disposition==='candidate'&&packet.actionable?'Proposal ready':packet.disposition==='blocked'?'Vetoed':'No change';
  const tone=packet.disposition==='candidate'&&packet.actionable?'blue':packet.disposition==='blocked'?'red':'green';
  const changes=(packet.proposed_changes||[]).map(change=>change.name.replaceAll('_',' ')+' '+change.current+' → '+change.proposed).join(' · ');
	  return {label,tone,description:safe(packet.market)+' · '+evidence+' · '+age(packet.created_at)+'.',detail:'Risk check '+(passed?'passed':'rejected')+': '+safe(packet.risk_reason)+(changes?' Proposed only: '+safe(changes)+'.':'')+' No active plan was changed.'};
}
function renderSystem(){
  const healthy=current.markets.filter(m=>m.available&&m.ready&&m.fresh).length,total=current.markets.length;
  const marketNames=current.markets.map(m=>m.name).join(', ')||'none configured';
	const research=researchView();
  $('automation').innerHTML=
    automationCard('engines','BOT','Paper engines',healthy===total&&total?'Running':'Needs attention',healthy===total&&total?'green':'amber',healthy+' of '+total+' market observers are current: '+marketNames+'. They make the current simulated decisions.')+
    automationCard('hermes','H','Nous Hermes',research.label,research.tone,research.description)+
    automationCard('strategy','AD','Versioned learning','Gate required','blue','Market rules adapt on current prices. A new parameter version can replace a paper plan only after independent forward evidence; configuration alone does not mean it passed.')+
    automationCard('alerts','TG','Telegram alerts','Open + filled','amber','Sends concise open-order, filled-order, safety, data, and daily-result messages. Unfilled attempts appear in Recent activity instead of creating Telegram noise.');
  $('system-list').innerHTML=current.markets.map(m=>{const healthy=m.available&&m.ready&&m.fresh;const updating=m.available&&!m.ready;const description=healthy?'Paper observer and bounded status are current.':updating?'Waiting for the first complete paper status.':m.available?'Observer status is older than expected.':'Status source could not be read. Other markets continue independently.';const label=healthy?'Healthy':updating?'Updating':m.available?'Stale':'Unavailable';return '<article class="system-row"><p><strong>'+safe(m.name)+'</strong></p><p class="description">'+description+'</p><span class="badge '+(healthy?'green':updating||m.available?'amber':'red')+'">'+label+'</span></article>';}).join('');
	$('research-evidence').innerHTML='<span class="badge '+research.tone+'">'+safe(research.label)+'</span><h3>Latest Hermes research</h3><p>'+safe(research.description)+'<br>'+safe(research.detail)+'</p>';
}
function setNotice(message){if($('notice').textContent!==message)$('notice').textContent=message;}
function render(){
	renderMetrics();renderMarkets();renderActivity();renderSystem();
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
load();setInterval(()=>{if(liveUpdates&&!document.hidden&&!$('refresh').disabled&&!document.activeElement?.closest('.activity-list,.help'))load();},10000);`
