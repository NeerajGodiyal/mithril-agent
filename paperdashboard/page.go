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
      <p class="eyebrow">Mithril agent</p>
      <h1>Paper trading</h1>
    </div>
    <div class="checked"><span id="connection-dot" class="dot" aria-hidden="true"></span><span id="checked">Connecting…</span></div>
  </header>
  <div class="trust" role="note">
    <div class="shell trust-inner">
      <strong>Simulation only</strong>
      <span>No funds move</span>
      <span>No wallet keys loaded</span>
      <span>No live orders</span>
    </div>
  </div>
  <nav class="shell tabs" aria-label="Dashboard sections" role="tablist">
    <button id="tab-overview" class="tab active" data-tab="overview" role="tab" aria-selected="true" aria-controls="overview">Overview</button>
    <button id="tab-activity" class="tab" data-tab="activity" role="tab" aria-selected="false" aria-controls="activity" tabindex="-1">Activity</button>
    <button id="tab-strategy" class="tab" data-tab="strategy" role="tab" aria-selected="false" aria-controls="strategy" tabindex="-1">Strategy</button>
    <button id="tab-system" class="tab" data-tab="system" role="tab" aria-selected="false" aria-controls="system" tabindex="-1">System</button>
  </nav>
  <main id="main" class="shell">
    <div id="notice" class="notice" role="status" aria-live="polite"></div>
    <section id="overview" class="panel active" role="tabpanel" aria-labelledby="tab-overview">
      <div class="section-title">
        <div><p class="eyebrow">Current session</p><h2 id="overview-title">Overview</h2></div>
        <button id="refresh" class="button" aria-live="polite">Refresh</button>
      </div>
      <div id="metrics" class="metrics" aria-label="Portfolio summary"></div>
      <div class="section-title compact"><h2>Markets</h2><p>Each market runs independently.</p></div>
      <div id="markets" class="market-grid"></div>
    </section>
    <section id="activity" class="panel" role="tabpanel" aria-labelledby="tab-activity" hidden>
      <div class="section-title">
        <div><p class="eyebrow">What happened</p><h2 id="activity-title">Activity</h2></div>
        <label class="filter">Show
          <select id="activity-filter">
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
    <section id="strategy" class="panel" role="tabpanel" aria-labelledby="tab-strategy" hidden>
      <div class="section-title"><div><p class="eyebrow">How decisions are made</p><h2 id="strategy-title">Strategy</h2></div></div>
      <div class="strategy-layout">
        <article class="card feature">
          <span class="badge blue">Paper strategies</span>
          <h3>Each market follows its saved plan.</h3>
          <p>The system measures current prices, applies each market's saved decision rules, waits when evidence is not good enough, and pauses at its safety limit.</p>
          <dl class="facts">
            <div><dt>Can change</dt><dd>Whether the paper account waits, buys, or sells</dd></div>
            <div><dt>Cannot change</dt><dd>Wallet access, live-trading mode, or safety boundaries</dd></div>
            <div><dt>Updates</dt><dd>Evaluated changes apply at the next UTC trading day without restarting the observer</dd></div>
          </dl>
        </article>
        <aside class="card guardrails">
          <h3>Safety boundary</h3>
          <ul>
            <li>Paper balances only</li>
            <li>No LLM controls execution</li>
            <li>News cannot directly trigger a trade</li>
            <li>Every decision remains in the evidence journal</li>
          </ul>
        </aside>
      </div>
      <div id="strategy-markets" class="market-grid small"></div>
    </section>
    <section id="system" class="panel" role="tabpanel" aria-labelledby="tab-system" hidden>
      <div class="section-title"><div><p class="eyebrow">Operational state</p><h2 id="system-title">System</h2></div></div>
      <div id="system-list" class="system-list"></div>
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
  --bg:#090b10;--surface:#11151d;--raised:#171c26;--line:#252c38;--text:#f4f6f8;
  --muted:#98a2b3;--green:#65d89a;--green-bg:#11271d;--blue:#7bb7ff;--blue-bg:#102238;
  --amber:#f4bf67;--amber-bg:#2b2111;--red:#ff8c8c;--red-bg:#301719;--radius:18px;
}
.metric-value{overflow-wrap:anywhere}.market-state{flex-wrap:wrap}.topbar>div,.checked,.market,.market-head>*,.activity-copy,.system-row>*{min-width:0}.activity-copy h3,.activity-copy p,.market h3,.system-row p{overflow-wrap:anywhere}
*{box-sizing:border-box}html{background:var(--bg);color:var(--text);font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:16px}body{margin:0;min-width:320px;background:radial-gradient(circle at 80% -20%,#192236 0,transparent 34rem),var(--bg)}button,select{font:inherit}.shell{width:min(100% - 32px,1380px);margin-inline:auto}.skip{position:fixed;left:12px;top:-60px;z-index:10;background:#fff;color:#000;padding:10px 14px;border-radius:8px}.skip:focus{top:12px}.topbar{min-height:108px;display:flex;align-items:center;justify-content:space-between;gap:24px}.eyebrow{margin:0 0 5px;color:var(--blue);font-size:.72rem;font-weight:750;letter-spacing:.14em;text-transform:uppercase}.topbar h1{font-size:clamp(1.6rem,4vw,2.25rem);letter-spacing:-.04em;margin:0}.checked{display:flex;align-items:center;gap:9px;color:var(--muted);font-size:.88rem;font-variant-numeric:tabular-nums}.dot{width:9px;height:9px;border-radius:50%;background:var(--amber);box-shadow:0 0 0 5px rgba(244,191,103,.1)}.dot.ok{background:var(--green);box-shadow:0 0 0 5px rgba(101,216,154,.1)}.dot.bad{background:var(--red);box-shadow:0 0 0 5px rgba(255,140,140,.1)}.trust{border-block:1px solid #22402e;background:rgba(17,39,29,.78)}.trust-inner{display:flex;align-items:center;gap:28px;min-height:48px;color:#c1cec6;font-size:.84rem}.trust strong{color:var(--green);font-size:.76rem;letter-spacing:.08em;text-transform:uppercase}.trust span{display:flex;align-items:center;gap:8px}.trust span:before{content:"✓";color:var(--green);font-weight:800}.tabs{display:flex;gap:6px;padding-block:24px 18px}.tab,.button{min-height:44px;border:1px solid transparent;border-radius:12px;background:transparent;color:var(--muted);padding:0 16px;cursor:pointer}.tab:hover,.tab:focus-visible,.button:hover,.button:focus-visible{color:var(--text);border-color:#495369;outline:none}.tab.active{background:var(--raised);color:var(--text);border-color:var(--line)}.button{min-width:118px;border-color:var(--line);background:var(--surface);color:var(--text)}.button.loading:before{content:"";display:inline-block;width:12px;height:12px;margin:0 8px -2px 0;border:2px solid var(--muted);border-top-color:transparent;border-radius:50%;animation:spin .7s linear infinite}main{min-height:610px}.panel{animation:enter .18s ease}.panel[hidden]{display:none}.notice{min-height:0;margin-bottom:14px}.notice:not(:empty){padding:13px 15px;border:1px solid #62451d;border-radius:12px;background:var(--amber-bg);color:#f5d69f}.section-title{display:flex;align-items:end;justify-content:space-between;gap:20px;margin:18px 0}.section-title.compact{margin-top:36px}.section-title h2{margin:0;font-size:1.25rem;letter-spacing:-.02em}.section-title p{margin:0;color:var(--muted);font-size:.88rem}.metrics{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px}.metric,.card,.market,.activity-item,.system-row{background:linear-gradient(155deg,rgba(24,30,41,.97),rgba(15,19,26,.97));border:1px solid var(--line);border-radius:var(--radius)}.metric{padding:22px;min-height:130px;display:flex;flex-direction:column;justify-content:space-between}.metric-label{position:relative;display:flex;align-items:center;justify-content:space-between;gap:8px;color:var(--muted);font-size:.79rem}.help{display:inline-grid;place-items:center;flex:0 0 auto;width:32px;height:32px;padding:0;border:1px solid var(--line);border-radius:50%;background:var(--surface);color:var(--blue);cursor:pointer}.help-tip{display:none;position:absolute;z-index:4;top:calc(100% + 8px);right:0;width:min(245px,calc(100vw - 48px));padding:10px 12px;border:1px solid #495369;border-radius:10px;background:#202735;color:var(--text);font-size:.75rem;font-weight:400;line-height:1.45;text-align:left;box-shadow:0 12px 30px rgba(0,0,0,.35)}.metric:nth-child(odd) .help-tip{right:auto;left:0}.help:hover .help-tip,.help:focus-visible .help-tip,.help[aria-expanded="true"] .help-tip{display:block}.metric-value{font-size:clamp(1.35rem,2.2vw,2rem);font-weight:720;letter-spacing:-.04em;font-variant-numeric:tabular-nums}.metric-foot{color:var(--muted);font-size:.75rem}.positive{color:var(--green)!important}.negative{color:var(--red)!important}.neutral{color:var(--text)!important}.market-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.market-grid.small{margin-top:14px}.market{padding:22px}.market-head,.market-state{display:flex;align-items:center;justify-content:space-between;gap:16px}.market h3{margin:0;font-size:1.08rem}.badge{display:inline-flex;align-items:center;min-height:26px;padding:0 9px;border-radius:99px;font-size:.7rem;font-weight:750;letter-spacing:.03em}.badge.green{color:var(--green);background:var(--green-bg)}.badge.blue{color:var(--blue);background:var(--blue-bg)}.badge.amber{color:var(--amber);background:var(--amber-bg)}.badge.red{color:var(--red);background:var(--red-bg)}.price{margin:20px 0 6px;font-size:clamp(1.7rem,4vw,2.7rem);font-weight:750;letter-spacing:-.055em;font-variant-numeric:tabular-nums}.market-state{color:var(--muted);font-size:.83rem}.market-state strong{color:var(--text);font-weight:620}.market-metrics{display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin-top:22px;padding-top:18px;border-top:1px solid var(--line)}.market-metrics div{min-width:0}.market-metrics span{display:block;color:var(--muted);font-size:.72rem;margin-bottom:6px}.market-metrics strong{display:block;font-size:.93rem;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}.empty{padding:34px;border:1px dashed #394151;border-radius:var(--radius);color:var(--muted);text-align:center}.filter{display:flex;align-items:center;gap:10px;color:var(--muted);font-size:.82rem}.filter select{min-height:44px;color:var(--text);background:var(--surface);border:1px solid var(--line);border-radius:12px;padding:0 32px 0 12px}.activity-list{display:grid;gap:10px}.activity-item{display:grid;grid-template-columns:10px minmax(0,1fr) auto;gap:15px;padding:17px 18px}.event-mark{width:9px;height:9px;border-radius:50%;margin-top:6px;background:var(--blue)}.event-mark.order_filled{background:var(--green)}.event-mark.risk_halted,.event-mark.data_unavailable{background:var(--red)}.event-mark.order_refused,.event-mark.order_missed{background:var(--muted)}.activity-copy h3{margin:0 0 5px;font-size:.95rem}.activity-copy p{white-space:pre-line;margin:0;color:var(--muted);font-size:.84rem;line-height:1.48}.activity-time{text-align:right;color:var(--muted);font-size:.75rem;font-variant-numeric:tabular-nums}.strategy-layout{display:grid;grid-template-columns:minmax(0,1.5fr) minmax(280px,.7fr);gap:14px}.card{padding:24px}.feature h3{margin:18px 0 8px;font-size:1.45rem;letter-spacing:-.03em}.feature>p,.access p{color:var(--muted);line-height:1.58}.facts{display:grid;gap:0;margin:22px 0 0}.facts div{padding:14px 0;border-top:1px solid var(--line)}.facts dt{color:var(--muted);font-size:.74rem;margin-bottom:5px}.facts dd{margin:0;font-size:.88rem}.guardrails h3,.access h3{margin:0 0 14px}.guardrails ul{margin:0;padding-left:20px;color:#cbd2db;line-height:2}.system-list{display:grid;gap:10px}.system-row{display:grid;grid-template-columns:minmax(150px,1fr) minmax(0,2fr) auto;align-items:center;gap:18px;padding:17px 19px}.system-row p{margin:0}.system-row .description{color:var(--muted);font-size:.84rem}.access{display:flex;justify-content:space-between;gap:25px;align-items:center;margin-top:14px}.access p{max-width:580px;margin:0}footer{padding-block:46px;color:#6f7989;font-size:.75rem}button:focus-visible,select:focus-visible,a:focus-visible{outline:3px solid var(--blue);outline-offset:3px}@keyframes enter{from{opacity:.3;transform:translateY(4px)}to{opacity:1;transform:none}}@keyframes spin{to{transform:rotate(360deg)}}@media (prefers-reduced-motion:reduce){*,*:before,*:after{animation-duration:.01ms!important;animation-iteration-count:1!important;scroll-behavior:auto!important}}@media(max-width:900px){.metrics{grid-template-columns:repeat(2,1fr)}.strategy-layout{grid-template-columns:1fr}.trust-inner{gap:16px;flex-wrap:wrap;padding-block:11px}.market-grid{grid-template-columns:1fr}}@media(max-width:600px){.shell{width:min(100% - 22px,1380px)}.topbar{min-height:88px}.checked{max-width:135px;text-align:right}.trust-inner{display:grid;grid-template-columns:1fr 1fr;gap:9px 15px}.trust strong{grid-column:1/-1}.tabs{overflow-x:auto;padding-block:17px 13px}.tab{flex:1;padding-inline:12px}.section-title{align-items:center}.section-title.compact{display:block}.section-title.compact p{margin-top:5px}.metrics{gap:8px}.metric{padding:16px;min-height:112px}.metric-value{font-size:1.3rem}.market{padding:18px}.market-metrics{grid-template-columns:1fr 1fr}.activity-item{grid-template-columns:9px minmax(0,1fr)}.activity-time{grid-column:2;text-align:left}.filter{display:block}.filter select{display:block;margin-top:6px;max-width:170px}.system-row{grid-template-columns:1fr auto;gap:8px}.system-row .description{grid-column:1/-1;grid-row:2}.access{display:block}.access p{margin-top:12px}}`

const mobileCSS = `.market-metrics{grid-template-columns:repeat(4,minmax(0,1fr))}.chart{margin-top:20px;padding-top:17px;border-top:1px solid var(--line)}.chart-head,.chart-legend{display:flex;justify-content:space-between;gap:12px;color:var(--muted);font-size:.72rem}.chart svg{display:block;width:100%;height:104px;margin:11px 0 8px;overflow:visible}.chart-grid{stroke:#303846;stroke-width:.6}.chart-paper,.chart-hold{fill:none;stroke-width:2;vector-effect:non-scaling-stroke}.chart-paper{stroke:var(--green)}.chart-hold{stroke:var(--blue);stroke-dasharray:4 3}.chart-legend{justify-content:flex-start;gap:18px}.chart-legend span:before{content:"";display:inline-block;width:14px;height:2px;margin:0 6px 3px 0;background:var(--green)}.chart-legend span:last-child:before{background:repeating-linear-gradient(90deg,var(--blue) 0 4px,transparent 4px 7px)}.chart-empty{min-height:104px;display:grid;place-items:center;color:var(--muted);font-size:.8rem}.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}@media(max-width:600px){.tabs{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));overflow:visible}.tab{min-width:0;padding-inline:4px;font-size:.88rem}.market-metrics{grid-template-columns:1fr 1fr}}`

const appJS = `const $=id=>document.getElementById(id);
let current=null;
let refreshReset;
let requestSequence=0;
const safe=value=>String(value??'').replace(/[&<>'"]/g,char=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[char]));
const integer=value=>{try{return BigInt(value||0);}catch{return 0n;}};
const decimal=(value,min,max)=>{let amount=integer(value),sign='';if(amount<0n){sign='-';amount=-amount;}const shift=10n**BigInt(6-max);amount=(amount+shift/2n)/shift;const base=10n**BigInt(max);const whole=amount/base;let fraction=(amount%base).toString().padStart(max,'0');while(fraction.length>min&&fraction.endsWith('0'))fraction=fraction.slice(0,-1);return sign+whole.toLocaleString()+(fraction?'.'+fraction:'');};
const money=micros=>'$'+decimal(micros,2,2);
const price=micros=>'$'+decimal(micros,2,6);
const paperValue=(micros,unit)=>unit==='USD'?money(micros):decimal(micros,2,6)+' '+(unit||'units');
const deltaValue=(micros,unit)=>unit==='USD'?'$'+decimal(micros,2,6):paperValue(micros,unit);
const resultDelta=(from,to,unit)=>{const value=integer(to)-integer(from);if(value===0n)return 'Unchanged';return (value>0n?'Up ':'Down ')+deltaValue(value>0n?value:-value,unit);};
const comparisonDelta=(from,to,unit)=>{const value=integer(to)-integer(from);if(value===0n)return 'The same';return deltaValue(value>0n?value:-value,unit)+(value>0n?' better':' worse');};
const tone=value=>value>0n?'positive':value<0n?'negative':'neutral';
const time=value=>value?new Date(value).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',timeZone:'UTC'})+' UTC':'Not available';
const eventTime=value=>value?new Date(value).toLocaleString([],{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit',timeZone:'UTC'})+' UTC':'Not available';
const state=value=>({warming:'Learning recent prices',uptrend:'Market rising',downtrend:'Market falling',range:'Market moving sideways',volatile:'Waiting for calmer prices','order pending':'Paper order being checked','waiting for data':'Price data delayed',paused:'Paused by safety limit',watching:'Watching market'}[value]||'Watching market');
const eventGroup=kind=>kind.startsWith('order_')?'orders':kind.startsWith('strategy_')?'strategy':kind==='risk_halted'?'safety':kind.startsWith('data_')?'data':'other';

function chartPaths(points,key,minValue,span,start,end){
  const shapes=[];let segment=[];
  const finish=()=>{if(segment.length>1)shapes.push('<polyline class="chart-'+key+'" points="'+segment.join(' ')+'"></polyline>');else if(segment.length===1){const [x,y]=segment[0].split(',');shapes.push('<circle class="chart-'+key+'" cx="'+x+'" cy="'+y+'" r="1.7"></circle>');}segment=[];};
  points.forEach(point=>{if(point.unavailable){finish();return;}const at=Date.parse(point.at);if(!Number.isFinite(at))return;const x=end===start?50:(at-start)*100/(end-start);const y=50-Number((integer(point[key])-minValue)*4400n/span)/100;segment.push(x.toFixed(2)+','+y.toFixed(2));});
  finish();return shapes.join('');
}
function performanceChart(m){
  const points=m.history||[],available=points.filter(point=>!point.unavailable);
  const period=m.fresh?'Today':'Last recorded session';
  if(available.length<2)return '<div class="chart"><div class="chart-head"><span>'+period+'</span><span>Biggest drop '+paperValue(m.max_drawdown_micros,m.value_unit)+'</span></div><div class="chart-empty">Building the performance chart…</div></div>';
  const values=available.flatMap(point=>[integer(point.equity_micros),integer(point.hold_benchmark_micros)]);
  let minimum=values[0],maximum=values[0];values.forEach(value=>{if(value<minimum)minimum=value;if(value>maximum)maximum=value;});
  const span=maximum===minimum?1n:maximum-minimum,start=Date.parse(points[0].at),end=Date.parse(points[points.length-1].at);
  const paper=chartPaths(points,'equity_micros',minimum,span,start,end).replaceAll('chart-equity_micros','chart-paper');
  const hold=chartPaths(points,'hold_benchmark_micros',minimum,span,start,end).replaceAll('chart-hold_benchmark_micros','chart-hold');
  if(!paper&&!hold)return '<div class="chart"><div class="chart-empty">Price-data gaps separate today’s observations.</div></div>';
  const first=available[0],last=available[available.length-1],gaps=points.filter(point=>point.unavailable).length;
  const summary=m.name+' practice account moved from '+paperValue(first.equity_micros,m.value_unit)+' to '+paperValue(last.equity_micros,m.value_unit)+' ('+resultDelta(first.equity_micros,last.equity_micros,m.value_unit)+'). No-trading comparison moved from '+paperValue(first.hold_benchmark_micros,m.value_unit)+' to '+paperValue(last.hold_benchmark_micros,m.value_unit)+' ('+resultDelta(first.hold_benchmark_micros,last.hold_benchmark_micros,m.value_unit)+'). '+gaps+' unavailable interval'+(gaps===1?'':'s')+'.';
  return '<div class="chart"><div class="chart-head"><span>'+period+'</span><span>Biggest drop '+paperValue(m.max_drawdown_micros,m.value_unit)+'</span></div><p class="sr-only">'+safe(summary)+'</p><svg viewBox="0 0 100 56" preserveAspectRatio="none" aria-hidden="true" focusable="false"><line class="chart-grid" x1="0" y1="6" x2="100" y2="6"></line><line class="chart-grid" x1="0" y1="28" x2="100" y2="28"></line><line class="chart-grid" x1="0" y1="50" x2="100" y2="50"></line>'+hold+paper+'</svg><div class="chart-legend"><span>Strategy</span><span>No trading</span></div></div>';
}

const tabs=[...document.querySelectorAll('.tab')];
function selectTab(button,focus=false){
  tabs.forEach(item=>{const active=item===button;item.classList.toggle('active',active);item.setAttribute('aria-selected',String(active));item.tabIndex=active?0:-1;});
  document.querySelectorAll('.panel').forEach(panel=>{const active=panel.id===button.dataset.tab;panel.hidden=!active;panel.classList.toggle('active',active);});
  if(focus)button.focus();
}
tabs.forEach((button,index)=>{button.addEventListener('click',()=>selectTab(button));button.addEventListener('keydown',event=>{let next;if(event.key==='ArrowRight')next=(index+1)%tabs.length;else if(event.key==='ArrowLeft')next=(index+tabs.length-1)%tabs.length;else if(event.key==='Home')next=0;else if(event.key==='End')next=tabs.length-1;else return;event.preventDefault();selectTab(tabs[next],true);});});
$('refresh').addEventListener('click',()=>load(true));
$('activity-filter').addEventListener('change',renderActivity);
document.addEventListener('click',event=>{const selected=event.target.closest('.help');document.querySelectorAll('.help[aria-expanded="true"]').forEach(button=>{if(button!==selected)button.setAttribute('aria-expanded','false');});if(selected)selected.setAttribute('aria-expanded',String(selected.getAttribute('aria-expanded')!=='true'));else if(document.activeElement?.classList.contains('help'))document.activeElement.blur();});
document.addEventListener('keydown',event=>{if(event.key!=='Escape')return;document.querySelectorAll('.help[aria-expanded="true"]').forEach(button=>button.setAttribute('aria-expanded','false'));if(document.activeElement?.classList.contains('help'))document.activeElement.blur();});

function help(label,text){const id='help-'+label.toLowerCase().replace(/[^a-z0-9]+/g,'-');return '<button type="button" class="help" aria-label="Explain '+safe(label)+'" aria-describedby="'+id+'" aria-expanded="false">?<span id="'+id+'" class="help-tip" role="tooltip">'+safe(text)+'</span></button>';}
function metric(label,value,foot,klass='',explanation=''){
  return '<article class="metric"><span class="metric-label"><span>'+safe(label)+'</span>'+help(label,explanation)+'</span><strong class="metric-value '+klass+'">'+safe(value)+'</strong><span class="metric-foot">'+safe(foot)+'</span></article>';
}
function renderMetrics(){
  if(!current.complete){$('metrics').innerHTML=metric('Practice account','—','Waiting for all markets','','Estimated value of the simulated assets. No real money moves.')+metric("Today's result",'—','Waiting for current data','','How much the practice account gained or lost since 00:00 UTC.')+metric('Versus no trading','—','Waiting for current data','','Difference from leaving the starting assets unchanged.')+metric('Completed trades','—','Waiting for current data','','Simulated buys or sells that finished. More trades do not necessarily mean more profit.');return;}
  const o=current.overview||{};
  const pnl=integer(o.equity_micros)-integer(o.opening_equity_micros);
  const hold=integer(o.equity_micros)-integer(o.hold_benchmark_micros);
  const coverage=o.coverage_ready?(Number(o.coverage_bps)<10000?' · price updates '+(Number(o.coverage_bps)/100).toFixed(2)+'%':''):' · learning price updates';
  $('metrics').innerHTML=metric('Practice account',paperValue(o.equity_micros,o.value_unit),'Across available markets','neutral','Estimated value of the simulated assets. No real money moves.')+
    metric("Today's result",resultDelta(o.opening_equity_micros,o.equity_micros,o.value_unit),'Since 00:00 UTC',tone(pnl),'How much the practice account gained or lost since 00:00 UTC.')+
    metric('Versus no trading',comparisonDelta(o.hold_benchmark_micros,o.equity_micros,o.value_unit),'Compared with leaving the starting assets unchanged',tone(hold),'Difference from leaving the starting assets unchanged.')+
    metric('Completed trades',String(o.trades||0),'Plan reacted '+String(o.signals||0)+' times'+coverage,'neutral','Simulated buys or sells that finished. A reaction means the plan noticed trade conditions; repeats can belong to the same trade. More trades do not necessarily mean more profit.');
}
function marketCard(m){
  if(!m.available)return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge red">Unavailable</span></div><p class="price">—</p><p class="market-state">Status source could not be read. Other markets are unaffected.</p></article>';
  if(!m.ready)return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge amber">Updating</span></div><p class="price">—</p><p class="market-state">Waiting for the first complete paper status.</p></article>';
  const badge=m.risk_halted?'<span class="badge red">Safety paused</span>':m.fresh?'<span class="badge green">Running</span>':'<span class="badge amber">Stale</span>';
  const pnl=integer(m.equity_micros)-integer(m.opening_equity_micros);
  const holding=integer(m.equity_micros)-integer(m.hold_benchmark_micros);
  const action=m.risk_halted?'No new orders':!m.fresh?'Waiting for fresh prices':m.next_action?'Ready to '+safe(m.next_action)+' if a good opportunity appears':state(m.state);
  const marketPrice=m.price_micros?(!m.fresh?'Last recorded price '+price(m.price_micros):m.value_unit==='devUSDC'?'Reference '+price(m.price_micros):price(m.price_micros)):'Learning prices';
  const resultLabel=m.fresh?'Today’s result':'Last recorded result';
  const comparisonLabel=m.fresh?'Versus no trading':'Last result vs no trading';
  const marketState=m.fresh?state(m.state):'Last recorded: '+state(m.state);
  return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3>'+badge+'</div><p class="price">'+marketPrice+'</p><div class="market-state"><strong>'+safe(marketState)+'</strong><span>'+safe(action)+'</span></div><div class="market-metrics"><div><span>Practice account</span><strong>'+paperValue(m.equity_micros,m.value_unit)+'</strong></div><div><span>'+resultLabel+'</span><strong class="'+tone(pnl)+'">'+resultDelta(m.opening_equity_micros,m.equity_micros,m.value_unit)+'</strong></div><div><span>'+comparisonLabel+'</span><strong class="'+tone(holding)+'">'+comparisonDelta(m.hold_benchmark_micros,m.equity_micros,m.value_unit)+'</strong></div><div><span>Activity</span><strong>'+safe(String(m.trades||0))+' completed · plan reacted '+safe(String(m.signals||0))+' times</strong></div></div>'+performanceChart(m)+'</article>';
}
function strategyCard(m){
  const unavailable=!m.available,label=unavailable?'Unavailable':m.ready?(m.strategy==='adaptive'?'Market-responsive paper plan':m.strategy||'Saved plan'):'Updating';
  const next=unavailable?'Status source unavailable':!m.ready?'Waiting for status':!m.fresh?'Waiting for fresh prices':m.risk_halted?'No new orders while the safety pause is active':m.next_action?'If a good opportunity appears: '+m.next_action:'No next side yet';
  const status=unavailable?'Unavailable':m.ready?state(m.state):'Status updating';
  const trades=unavailable||!m.ready?'—':(m.trades||0)+' completed trades · plan reacted '+(m.signals||0)+' times '+(m.fresh?'today':'in the last recorded session');
  const note=m.strategy==='adaptive'?'<p class="metric-foot">Decisions respond to market prices. The plan does not retrain itself live.</p>':'';
  return '<article class="market"><div class="market-head"><h3>'+safe(m.name)+'</h3><span class="badge '+(unavailable?'red':'blue')+'">'+safe(label)+'</span></div><p class="price">'+safe(next)+'</p><div class="market-state"><strong>'+safe(status)+'</strong><span>'+safe(trades)+'</span></div>'+note+'</article>';
}
function renderMarkets(){
  $('markets').innerHTML=current.markets.length?current.markets.map(marketCard).join(''):'<div class="empty">No paper markets configured.</div>';
  $('strategy-markets').innerHTML=current.markets.map(strategyCard).join('');
}
function renderActivity(){
  if(!current)return;
  const filter=$('activity-filter').value;
  const items=current.activity.filter(item=>filter==='all'||eventGroup(item.kind)===filter);
  $('activity-list').innerHTML=items.length?items.map(item=>{
    const lines=String(item.message||'').split('\n');
    const title=(lines.shift()||item.kind).replace(/^PAPER · /,'');
    return '<article class="activity-item"><span class="event-mark '+safe(item.kind)+'" aria-hidden="true"></span><div class="activity-copy"><h3>'+safe(item.market)+' · '+safe(title)+'</h3><p>'+safe(lines.join('\n'))+'</p></div><time class="activity-time" datetime="'+safe(item.at)+'">'+safe(eventTime(item.at))+'</time></article>';
  }).join(''):'<div class="empty">No matching activity yet.</div>';
}
function renderSystem(){
  $('system-list').innerHTML=current.markets.map(m=>{const healthy=m.available&&m.ready&&m.fresh;const updating=m.available&&!m.ready;const description=healthy?'Paper observer and bounded status are current.':updating?'Waiting for the first complete paper status.':m.available?'Observer status is older than expected.':'Status source could not be read. Other markets continue independently.';const label=healthy?'Healthy':updating?'Updating':m.available?'Stale':'Unavailable';return '<article class="system-row"><p><strong>'+safe(m.name)+'</strong></p><p class="description">'+description+'</p><span class="badge '+(healthy?'green':updating||m.available?'amber':'red')+'">'+label+'</span></article>';}).join('');
}
function render(){
  renderMetrics();renderMarkets();renderActivity();renderSystem();
  $('notice').textContent=current.complete?'':'Some market status is delayed. Available markets remain visible.';
  $('checked').textContent=current.observed_at?'Checked '+time(current.observed_at):'No current data';
  $('connection-dot').className='dot '+(current.complete?'ok':'bad');
}
async function load(manual=false){
  const button=$('refresh');
  const request=++requestSequence;
  const previous=current?.observed_at;
  if(manual){clearTimeout(refreshReset);button.disabled=true;button.classList.add('loading');button.textContent='Refreshing…';}
  try{const response=await fetch('/api/v1/status'+(manual?'?fresh=1':''),{cache:'no-store'});if(!response.ok)throw new Error('status unavailable');const next=await response.json();if(request!==requestSequence)return;current=next;render();if(manual){button.classList.remove('loading');button.textContent=!current.complete?'Data delayed':previous&&previous===current.observed_at?'Checked ✓':'Updated ✓';refreshReset=setTimeout(()=>button.textContent='Refresh',1500);}else if(button.textContent==='Try again')button.textContent='Refresh';}
  catch(error){if(request!==requestSequence)return;if(current){current.complete=false;current.markets.forEach(m=>m.fresh=false);render();}$('notice').textContent='Dashboard status is unavailable. It will retry automatically.';$('checked').textContent='Connection lost';$('connection-dot').className='dot bad';}
  finally{if(manual&&request===requestSequence){button.disabled=false;button.classList.remove('loading');if(button.textContent==='Refreshing…'){button.textContent='Try again';refreshReset=setTimeout(()=>button.textContent='Refresh',3000);}}}
}
load();setInterval(()=>{if(!document.hidden&&!$('refresh').disabled&&!document.activeElement?.closest('.activity-list,.help'))load();},10000);`
