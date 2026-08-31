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
  <header class="shell topbar">
    <div>
      <p class="eyebrow">Mithril</p>
      <h1>Paper trading</h1>
    </div>
    <div class="checked"><span id="connection-dot" class="dot" aria-hidden="true"></span><span id="checked">Connecting…</span></div>
  </header>
  <div class="trust" role="note">
    <div class="shell trust-inner">
      <strong>Simulation only</strong>
      <span>Paper money</span>
      <span>No real orders</span>
    </div>
  </div>
  <nav class="shell tabs" aria-label="Dashboard sections" role="tablist">
    <button id="tab-overview" class="tab active" data-tab="overview" role="tab" aria-selected="true" aria-controls="overview">Overview</button>
    <button id="tab-activity" class="tab" data-tab="activity" role="tab" aria-selected="false" aria-controls="activity" tabindex="-1">Activity</button>
    <button id="tab-strategy" class="tab" data-tab="strategy" role="tab" aria-selected="false" aria-controls="strategy" tabindex="-1">Strategy</button>
    <button id="tab-system" class="tab" data-tab="system" role="tab" aria-selected="false" aria-controls="system" tabindex="-1">Automation</button>
  </nav>
  <main id="main" class="shell">
    <div id="notice" class="notice" role="status" aria-live="polite"></div>
    <section id="overview" class="panel active" role="tabpanel" aria-labelledby="tab-overview" tabindex="0">
      <div class="section-title">
        <h2 id="overview-title">Paper account</h2>
        <div class="controls">
          <button id="live" class="button quiet" type="button" aria-pressed="true">Live updates: On</button>
          <button id="refresh" class="button" type="button">Refresh</button>
          <span id="refresh-status" class="sr-only" role="status" aria-live="polite"></span>
        </div>
      </div>
      <div id="metrics" class="metrics" aria-label="Paper account summary"></div>
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
    </section>
    <section id="system" class="panel" role="tabpanel" aria-labelledby="tab-system" tabindex="0" hidden>
      <div class="section-title"><div><p class="eyebrow">Who does what</p><h2 id="system-title">Automation setup</h2><p>Configured roles, permissions, and current market observers.</p></div></div>
      <div id="automation" class="automation-grid" aria-label="Automation roles"></div>
      <div class="section-title compact"><div><p class="eyebrow">Live status</p><h2>Market observers</h2></div></div>
      <div id="system-list" class="system-list"></div>
      <div class="section-title compact"><div><p class="eyebrow">Trust boundary</p><h2>Access and evidence</h2></div></div>
      <div class="detail-grid">
        <article class="card detail-card"><span class="badge green">Paper only</span><h3>Permissions</h3><p>No wallet key, signing, real funds, Mainnet submission, margin, leverage, short position, or liquidation authority.</p></article>
        <article class="card detail-card"><span class="badge blue">Independent checks</span><h3>Market sources</h3><p>Pyth on-chain prices are checked against Kraken, and Jupiter supplies route quotes. The separate Hermes profile is configured for official web, Solana, and bounded Mithril research tools.</p></article>
        <article class="card detail-card"><span class="badge amber">Not enabled</span><h3>Futures and perps</h3><p>This is spot-like paper trading. Perpetual positions need separate margin, funding, liquidation, oracle, and venue accounting before they can be shown truthfully.</p></article>
        <article class="card detail-card"><span class="badge blue">Evidence retained</span><h3>Recent order activity</h3><p>The dashboard keeps a bounded recent list. Older events remain in the local evidence journals. There are no on-chain signatures because no transaction is submitted.</p><button id="open-order-history" class="text-button" type="button">View recent paper orders</button></article>
      </div>
      <article class="card access">
        <div><span class="badge green">Private access</span><h3>Dashboard stays on the server</h3></div>
        <p>Open it through an SSH tunnel. It is not published to the internet and has no trading controls.</p>
      </article>
    </section>
  </main>
  <footer class="shell">Read-only paper status · Values are simulated, not financial results.</footer>
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

const finalCSS = `.help:focus-visible:not([aria-expanded="true"]) .help-tip{display:none}@media(max-width:600px){.metric:last-child{min-height:82px;display:grid;grid-template-columns:1fr auto;align-items:center;gap:4px 12px}.metric:last-child .metric-value{grid-column:2;grid-row:1}.metric:last-child .metric-foot{grid-column:1/-1}}@media(max-width:360px){.metric:last-child{display:flex;min-height:104px}}`

const appJS = `const $=id=>document.getElementById(id);
let current=null;
let refreshReset;
let requestSequence=0;
let liveUpdates=true;
const safe=value=>String(value??'').replace(/[&<>'"]/g,char=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[char]));
const integer=value=>{try{return BigInt(value||0);}catch{return 0n;}};
const decimal=(value,min,max)=>{let amount=integer(value),sign='';if(amount<0n){sign='-';amount=-amount;}const shift=10n**BigInt(6-max);amount=(amount+shift/2n)/shift;const base=10n**BigInt(max);const whole=amount/base;let fraction=(amount%base).toString().padStart(max,'0');while(fraction.length>min&&fraction.endsWith('0'))fraction=fraction.slice(0,-1);return sign+whole.toLocaleString()+(fraction?'.'+fraction:'');};
const money=micros=>integer(micros)>0n&&integer(micros)<10000n?'<$0.01':'$'+decimal(micros,2,2);
const price=micros=>{const amount=integer(micros),places=amount>=1000000n?2:amount>=10000n?4:6;return '$'+decimal(amount,2,places);};
const paperValue=(micros,unit)=>unit==='USD'?money(micros):decimal(micros,2,6)+' '+(unit||'units');
const deltaValue=(micros,unit)=>unit==='USD'&&integer(micros)<10000n?'<$0.01':unit==='USD'?money(micros):paperValue(micros,unit);
const resultDelta=(from,to,unit)=>{const value=integer(to)-integer(from);if(value===0n)return 'Unchanged';return (value>0n?'Up ':'Down ')+deltaValue(value>0n?value:-value,unit);};
const comparisonDelta=(from,to,unit)=>{const value=integer(to)-integer(from);if(value===0n)return 'The same';return deltaValue(value>0n?value:-value,unit)+(value>0n?' better':' worse');};
const attempts=value=>Number(value||0)===1?'Plan tried to trade once':'Plan tried to trade '+Number(value||0)+' times';
const tone=value=>value>0n?'positive':value<0n?'negative':'neutral';
const age=value=>{if(!value)return 'Not updated';const seconds=Math.max(0,Math.round((Date.now()-Date.parse(value))/1000));if(seconds<10)return 'Updated just now';if(seconds<60)return 'Updated '+seconds+'s ago';const minutes=Math.round(seconds/60);if(minutes<60)return 'Updated '+minutes+'m ago';const hours=Math.round(minutes/60);if(hours<24)return 'Updated '+hours+'h ago';return 'Updated '+Math.round(hours/24)+'d ago';};
const eventTime=value=>value?new Date(value).toLocaleString([],{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'}):'Not available';
const state=value=>({warming:'Learning recent prices',uptrend:'Market rising',downtrend:'Market falling',range:'Market moving sideways',volatile:'Waiting for calmer prices','order pending':'Paper order being checked','waiting for data':'Price data delayed',paused:'Paused by safety limit',watching:'Watching market'}[value]||'Watching market');
const eventGroup=kind=>kind.startsWith('order_')?'orders':kind.startsWith('strategy_')?'strategy':kind==='risk_halted'?'safety':kind.startsWith('data_')?'data':'other';

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
function performanceChart(m){
  const points=m.history||[],available=points.filter(point=>!point.unavailable);
  const period=m.fresh?'Today':'Last recorded session';
  if(available.length<2)return '<div class="chart"><div class="chart-head"><span>'+period+'</span><span>Max drawdown '+paperValue(m.max_drawdown_micros,m.value_unit)+'</span></div><div class="chart-empty">Building the performance chart…</div></div>';
  const values=available.flatMap(point=>[integer(point.equity_micros),integer(point.hold_benchmark_micros)]);
  let minimum=values[0],maximum=values[0];values.forEach(value=>{if(value<minimum)minimum=value;if(value>maximum)maximum=value;});
  const span=maximum===minimum?1n:maximum-minimum,start=Date.parse(points[0].at),end=Date.parse(points[points.length-1].at);
  const paper=chartPaths(points,'equity_micros',minimum,span,start,end).replaceAll('chart-equity_micros','chart-paper');
  const hold=chartPaths(points,'hold_benchmark_micros',minimum,span,start,end).replaceAll('chart-hold_benchmark_micros','chart-hold');
  if(!paper&&!hold)return '<div class="chart"><div class="chart-empty">Price-data gaps separate today’s observations.</div></div>';
  const first=available[0],last=available[available.length-1],gaps=points.filter(point=>point.unavailable).length;
  const summary=m.name+' estimated paper account value moved from '+paperValue(first.equity_micros,m.value_unit)+' to '+paperValue(last.equity_micros,m.value_unit)+' ('+resultDelta(first.equity_micros,last.equity_micros,m.value_unit)+'). Holding comparison moved from '+paperValue(first.hold_benchmark_micros,m.value_unit)+' to '+paperValue(last.hold_benchmark_micros,m.value_unit)+' ('+resultDelta(first.hold_benchmark_micros,last.hold_benchmark_micros,m.value_unit)+'). '+gaps+' unavailable interval'+(gaps===1?'':'s')+'.';
  const dots=chartDots(points,minimum,span,start,end,m.value_unit);
  return '<div class="chart"><div class="chart-head"><span>'+period+'</span><span>Max drawdown '+paperValue(m.max_drawdown_micros,m.value_unit)+'</span></div><svg viewBox="0 0 100 56" preserveAspectRatio="none" role="img" aria-label="'+safe(summary)+'"><line class="chart-grid" x1="0" y1="6" x2="100" y2="6"></line><line class="chart-grid" x1="0" y1="28" x2="100" y2="28"></line><line class="chart-grid" x1="0" y1="50" x2="100" y2="50"></line>'+hold+paper+dots+'</svg><div class="chart-legend"><span>Strategy '+paperValue(last.equity_micros,m.value_unit)+'</span><span>Holding '+paperValue(last.hold_benchmark_micros,m.value_unit)+'</span></div></div>';
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
document.addEventListener('click',event=>{const selected=event.target.closest('.help');document.querySelectorAll('.help[aria-expanded="true"]').forEach(button=>{if(button!==selected)button.setAttribute('aria-expanded','false');});if(selected)selected.setAttribute('aria-expanded',String(selected.getAttribute('aria-expanded')!=='true'));else if(document.activeElement?.classList.contains('help'))document.activeElement.blur();});
document.addEventListener('keydown',event=>{if(event.key!=='Escape')return;document.querySelectorAll('.help[aria-expanded="true"]').forEach(button=>button.setAttribute('aria-expanded','false'));});

function help(label,text){const id='help-'+label.toLowerCase().replace(/[^a-z0-9]+/g,'-');return '<button type="button" class="help" aria-label="Explain '+safe(label)+'" aria-describedby="'+id+'" aria-expanded="false">?<span id="'+id+'" class="help-tip" role="tooltip">'+safe(text)+'</span></button>';}
function metric(label,value,foot,klass='',explanation=''){
  return '<article class="metric"><span class="metric-label"><span>'+safe(label)+'</span>'+help(label,explanation)+'</span><strong class="metric-value '+klass+'">'+safe(value)+'</strong><span class="metric-foot">'+safe(foot)+'</span></article>';
}
function renderMetrics(){
  if(!current.complete){$('metrics').innerHTML=metric('Total paper value','—','Waiting for all markets','','Estimated value of all simulated cash and coins at current prices. It is not a real wallet balance.')+metric("Today's paper result",'—','Waiting for current data','','Change across every paper market since today started. It includes coins that have not been sold.')+metric('Compared with holding','—','Waiting for current data','','Difference from leaving the starting assets unchanged.')+metric('Filled paper orders','—','Waiting for current data','','Simulated buy or sell orders that finished. More filled orders do not necessarily mean more profit.');return;}
  const o=current.overview||{};
  const pnl=integer(o.equity_micros)-integer(o.opening_equity_micros);
  const hold=integer(o.equity_micros)-integer(o.hold_benchmark_micros);
  const coverage=o.coverage_ready?'Price data '+(Number(o.coverage_bps||0)/100).toFixed(2)+'%':'Price data warming';
  $('metrics').innerHTML=metric('Total paper value',paperValue(o.equity_micros,o.value_unit),'Simulated cash + coins','neutral','Estimated value of all simulated cash and coins at current prices. It is not a real wallet balance.')+
    metric("Today's paper result",resultDelta(o.opening_equity_micros,o.equity_micros,o.value_unit),'All markets · includes unsold coins',tone(pnl),'Change across every paper market since today started. It includes coins that have not been sold.')+
    metric('Compared with holding',comparisonDelta(o.hold_benchmark_micros,o.equity_micros,o.value_unit),'Starting assets left unchanged',tone(hold),'Difference from leaving the starting assets unchanged.')+
    metric('Filled paper orders',String(o.trades||0),coverage+' · '+attempts(o.signals),'neutral','The plan checked '+Number(o.signals||0)+' possible trades; only filled orders changed the paper account. Price-data coverage shows how much of today had usable independent prices. More filled orders do not necessarily mean more profit.');
}
function marketCard(m){
  if(!m.available)return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge red">Unavailable</span></div><p class="strategy-next">Status is unavailable</p><p class="market-context">Other markets are unaffected.</p></article>';
  if(!m.ready)return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge amber">Updating</span></div><p class="strategy-next">Waiting for market data</p></article>';
  const badge=m.risk_halted?'<span class="badge red">New buys paused</span>':m.fresh?'<span class="badge green">Running</span>':'<span class="badge amber">Stale</span>';
  const pnl=integer(m.equity_micros)-integer(m.opening_equity_micros);
  const holding=integer(m.equity_micros)-integer(m.hold_benchmark_micros);
  const action=m.risk_halted?'New buys paused':!m.fresh?'Waiting for fresh prices':m.next_action?'Looking to '+safe(m.next_action):'Watching';
  const actionNote=m.risk_halted?'Sells can still reduce risk':!m.fresh?'The plan waits until prices recover':m.next_action?'Only if the opportunity is strong enough':state(m.state);
  const marketPrice=m.price_micros?price(m.price_micros):'Learning';
  const priceLabel=m.fresh?'Market price':'Last market price';
  const resultLabel=m.fresh?"Today's result":'Last result';
  const comparisonLabel=m.fresh?'Compared with holding':'Last compared with holding';
  const coverage=m.coverage_ready?'Price data '+(Number(m.coverage_bps||0)/100).toFixed(2)+'%':'Price data warming';
  return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><div class="market-status"><span class="updated">'+safe(age(m.observed_at))+'</span>'+badge+'</div></div><div class="market-overview"><div><span class="market-label">'+priceLabel+'</span><strong class="market-price">'+marketPrice+'</strong></div><div><span class="market-label">Current plan</span><strong class="market-plan">'+action+'</strong><span class="market-context">'+safe(actionNote)+'</span></div></div><div class="market-metrics"><div><span>This market value</span><strong>'+paperValue(m.equity_micros,m.value_unit)+'</strong></div><div><span>'+resultLabel+'</span><strong class="'+tone(pnl)+'">'+resultDelta(m.opening_equity_micros,m.equity_micros,m.value_unit)+'</strong></div><div><span>'+comparisonLabel+'</span><strong class="'+tone(holding)+'">'+comparisonDelta(m.hold_benchmark_micros,m.equity_micros,m.value_unit)+'</strong></div><div><span>Filled orders</span><strong>'+safe(String(m.trades||0))+'</strong><span class="market-detail">'+safe(coverage+' · '+attempts(m.signals))+'</span></div></div>'+performanceChart(m)+'</article>';
}
function strategyCard(m){
  const unavailable=!m.available,label=unavailable?'Unavailable':m.ready?(m.strategy==='adaptive'?'Market-responsive paper plan':m.strategy||'Saved plan'):'Updating';
  const next=unavailable?'Status source unavailable':!m.ready?'Waiting for status':!m.fresh?'Waiting for fresh prices':m.risk_halted?'New buys paused; sells can still reduce risk':m.next_action?'If a good opportunity appears: '+m.next_action:'No next side yet';
  const status=unavailable?'Unavailable':m.ready?state(m.state):'Status updating';
  const trades=unavailable||!m.ready?'—':(m.trades||0)+' filled paper orders · '+attempts(m.signals).toLowerCase()+' '+(m.fresh?'today':'in the last recorded session');
  const note=m.strategy==='adaptive'?'<p class="metric-foot">Prices change decisions now. New parameter versions must beat the current plan in a separate forward paper test before selection.</p>':'';
  return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge '+(unavailable?'red':'blue')+'">'+safe(label)+'</span></div><p class="strategy-next">'+safe(next)+'</p><div class="market-state"><strong>'+safe(status)+'</strong><span>'+safe(trades)+'</span></div>'+note+'</article>';
}
function renderMarkets(){
  $('markets').innerHTML=current.markets.length?current.markets.map(marketCard).join(''):'<div class="empty">No paper markets configured.</div>';
  $('strategy-markets').innerHTML=current.markets.map(strategyCard).join('');
}
function activityUSD(amount){
  const value=Number(amount);
  if(!Number.isFinite(value))return '$'+amount;
  return value>0&&value<.005?'<$0.01':'$'+value.toFixed(2);
}
function readableActivityResult(line){
  const value=line.match(/^This market value: \$([0-9]+(?:\.[0-9]+)?)$/);
  if(value)return 'This market value: '+activityUSD(value[1]);
  const result=line.match(/^(This market(?:'s result)? (?:today|gain\/loss):) (up|down) \$([0-9]+(?:\.[0-9]+)?)(.*)$/);
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
function renderSystem(){
  const healthy=current.markets.filter(m=>m.available&&m.ready&&m.fresh).length,total=current.markets.length;
  const marketNames=current.markets.map(m=>m.name).join(', ')||'none configured';
  $('automation').innerHTML=
    automationCard('engines','BOT','Paper engines',healthy===total&&total?'Running':'Needs attention',healthy===total&&total?'green':'amber',healthy+' of '+total+' market observers are current: '+marketNames+'. They make the current simulated decisions.')+
    automationCard('hermes','H','Nous Hermes','Configured','violet','The isolated research profile is configured for scheduled source checks and bounded paper proposals. This dashboard does not yet prove its timer is currently healthy.')+
    automationCard('strategy','AD','Versioned learning','Gate required','blue','Market rules adapt on current prices. A new parameter version can replace a paper plan only after independent forward evidence; configuration alone does not mean it passed.')+
    automationCard('alerts','TG','Telegram alerts','Open + filled','amber','Sends concise open-order, filled-order, safety, data, and daily-result messages. Unfilled attempts appear in Recent activity instead of creating Telegram noise.');
  $('system-list').innerHTML=current.markets.map(m=>{const healthy=m.available&&m.ready&&m.fresh;const updating=m.available&&!m.ready;const description=healthy?'Paper observer and bounded status are current.':updating?'Waiting for the first complete paper status.':m.available?'Observer status is older than expected.':'Status source could not be read. Other markets continue independently.';const label=healthy?'Healthy':updating?'Updating':m.available?'Stale':'Unavailable';return '<article class="system-row"><p><strong>'+safe(m.name)+'</strong></p><p class="description">'+description+'</p><span class="badge '+(healthy?'green':updating||m.available?'amber':'red')+'">'+label+'</span></article>';}).join('');
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
