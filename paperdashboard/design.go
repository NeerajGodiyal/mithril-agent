package paperdashboard

// dashboardCSS is a single, local design system derived from the supplied
// Finbro reference. It keeps the dashboard private with no remote runtime assets.
const dashboardCSS = `@font-face {
  font-family: "Space Grotesk";
  src: url("/vendor/space-grotesk-latin.woff2") format("woff2");
  font-style: normal;
  font-weight: 400 700;
  font-display: swap;
}

:root {
  --canvas: #000;
  --surface: #0d0d0d;
  --surface-raised: #141414;
  --surface-hover: #1a1a1a;
  --line: #242424;
  --line-strong: #353535;
  --text: #e7e7e7;
  --secondary: #b0b0b0;
  --muted: #919191;
  --subtle: #7f7f7f;
  --green: #86efac;
  --green-strong: #5fffaf;
  --green-soft: rgba(134, 239, 172, .10);
  --red: #ff6b74;
  --red-soft: rgba(255, 107, 116, .10);
  --amber: #f5b84c;
  --amber-soft: rgba(245, 184, 76, .10);
  --blue: #86aefb;
  --blue-soft: rgba(134, 174, 251, .10);
  --violet: #b09afb;
  --violet-soft: rgba(176, 154, 251, .10);
  --radius: 16px;
  --radius-small: 12px;
  --ease-out: cubic-bezier(.23, .88, .26, .92);
  --font: "Space Grotesk", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}

* { box-sizing: border-box; }
html { min-width: 320px; color-scheme: dark; background: var(--canvas); }
body {
  min-width: 320px;
  min-height: 100vh;
  margin: 0;
  color: var(--text);
  background: var(--canvas);
  font: 400 16px/1.45 var(--font);
  text-rendering: optimizeLegibility;
}
button, select, input { font: inherit; }
button, select, input, summary, a { -webkit-tap-highlight-color: transparent; }
button { color: inherit; }
a { color: var(--green); }
[hidden] { display: none !important; }
.shell { width: auto; }
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
.skip {
  position: fixed;
  z-index: 100;
  top: -60px;
  left: 16px;
  padding: 10px 14px;
  border-radius: 8px;
  color: #06120b;
  background: var(--green);
}
.skip:focus { top: 16px; }

.app-header {
  position: fixed;
  z-index: 30;
  top: 0;
  right: 0;
  left: 0;
  height: 72px;
  background: rgba(0, 0, 0, .34);
  backdrop-filter: blur(40px);
}
.topbar {
  display: flex;
  height: 72px;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  padding: 0 24px;
}
.brand {
  display: flex;
  min-width: 224px;
  align-items: center;
  gap: 12px;
}
.brand-logo { display: block; width: 30px; height: 34px; object-fit: contain; }
.eyebrow {
  margin: 0 0 3px;
  color: var(--muted);
  font-size: .67rem;
  font-weight: 600;
  letter-spacing: .13em;
  text-transform: uppercase;
}
.brand .eyebrow { color: var(--green); font-size: .58rem; }
.brand h1 { margin: 0; font-size: 1rem; font-weight: 650; letter-spacing: -.025em; }
.header-state { display: flex; align-items: center; gap: 18px; }
.checked,
.trust strong {
  display: inline-flex;
  min-height: 30px;
  align-items: center;
  gap: 9px;
  padding: 0;
  color: var(--muted);
  font-size: .69rem;
  font-weight: 550;
  white-space: nowrap;
}
.trust { margin: 0; }
.trust-inner { display: flex; align-items: center; }
.trust strong { color: var(--green); }
.trust strong::before {
  content: "";
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}
.trust span { display: none; }
.dot,
.live-dot {
  width: 7px;
  height: 7px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: var(--amber);
}
.dot.ok, .live-dot.green { background: var(--green); box-shadow: 0 0 0 3px rgba(134, 239, 172, .10); }
.dot.bad, .live-dot.red { background: var(--red); }
.live-dot.amber { background: var(--amber); }

.tabs {
  position: fixed;
  z-index: 25;
  top: 96px;
  bottom: 24px;
  left: 24px;
  display: flex;
  width: 224px;
  height: calc(100vh - 120px);
  flex-direction: column;
  gap: 28px;
  padding: 32px;
  border-radius: var(--radius);
  background: var(--surface);
}
.tab {
  display: grid;
  grid-template-columns: 26px minmax(0, 1fr);
  min-height: 28px;
  align-items: center;
  gap: 10px;
  padding: 0;
  border: 0;
  color: var(--muted);
  background: transparent;
  text-align: left;
  cursor: pointer;
  font-size: .75rem;
  font-weight: 550;
  letter-spacing: .07em;
  text-transform: uppercase;
  transition: color 150ms ease, transform 150ms ease;
}
.tab:hover, .tab.active { color: var(--text); background: transparent; }
.tab:active { transform: scale(.985); }
.nav-icon {
  display: grid;
  width: 26px;
  height: 26px;
  place-items: center;
  color: var(--subtle);
}
.nav-icon svg { width: 18px; height: 18px; fill: none; stroke: currentColor; stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round; }
.tab.active .nav-icon { color: var(--green); }

main.shell {
  max-width: 1720px;
  min-height: calc(100vh - 72px);
  margin-left: 272px;
  padding: 96px 24px 48px 0;
}
footer.shell {
  margin-left: 272px;
  padding: 0 24px 28px 0;
  color: var(--subtle);
  font-size: .68rem;
}
.panel { animation: view-enter 180ms ease-out; }
.panel:focus-visible { outline: 2px solid var(--green); outline-offset: 8px; }
.notice { min-height: 0; }
.notice:not(:empty) {
  margin-bottom: 16px;
  padding: 12px 14px;
  border-radius: 10px;
  color: #f6dba7;
  background: var(--amber-soft);
}

.section-title {
  display: flex;
  min-height: 72px;
  align-items: flex-end;
  justify-content: space-between;
  gap: 24px;
  margin: 0 0 24px;
}
.workspace-title { min-height: 72px; }
.section-title.compact { min-height: auto; margin: 34px 0 16px; }
.section-title h2 {
  margin: 3px 0 0;
  font-size: clamp(1.4rem, 2vw, 1.75rem);
  font-weight: 650;
  line-height: 1.12;
  letter-spacing: -.045em;
}
.section-title.compact h2 { font-size: 1.12rem; letter-spacing: -.025em; }
.section-title p:not(.eyebrow) {
  max-width: 700px;
  margin: 7px 0 0;
  color: var(--muted);
  font-size: .79rem;
}
.controls { display: flex; align-items: center; gap: 14px; }
.button,
.filter select,
.instruction-controls input,
.instruction-controls select {
  min-height: 42px;
  border: 1px solid var(--line-strong);
  border-radius: 9px;
  color: var(--text);
  background: var(--surface);
}
.button {
  position: relative;
  padding: 0 15px;
  font-size: .74rem;
  font-weight: 600;
  cursor: pointer;
  transition: border-color 150ms ease, background-color 150ms ease, transform 150ms ease;
}
.button:hover { border-color: #4a4a4a; background: var(--surface-hover); }
.button:active { transform: scale(.98); }
.button.quiet { color: var(--muted); }
.header-state .button {
  min-height: 30px;
  padding: 0;
  border: 0;
  border-radius: 0;
  color: var(--muted);
  background: transparent;
  font-size: .69rem;
}
.header-state .button:hover { color: var(--text); background: transparent; }
.header-state #refresh { color: var(--text); }
.button:disabled { cursor: wait; opacity: .66; }
.button.loading { padding-left: 35px; }
.button.loading::before {
  content: "";
  position: absolute;
  left: 14px;
  width: 12px;
  height: 12px;
  border: 2px solid #555;
  border-top-color: var(--green);
  border-radius: 50%;
  animation: spin .7s linear infinite;
}
.text-button {
  min-height: 36px;
  padding: 0;
  border: 0;
  color: var(--green);
  background: transparent;
  font-size: .75rem;
  cursor: pointer;
}
.text-button:hover { color: #b7f8ca; }

.agent-now {
  margin-bottom: 12px;
  padding: 20px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: linear-gradient(135deg, #111 0%, #0d0d0d 70%);
}
.agent-now-head,
.agent-now-card > header,
.agent-now-card > footer,
.agent-now-decision { display: flex; align-items: center; }
.agent-now-head { justify-content: space-between; gap: 18px; margin-bottom: 16px; }
.agent-now-head > div { display: grid; gap: 4px; }
.agent-now-head strong { font-size: .9rem; font-weight: 590; letter-spacing: -.02em; }
.paper-boundary {
  padding: 7px 10px;
  border: 1px solid rgba(95, 255, 175, .24);
  border-radius: 999px;
  color: var(--green);
  background: var(--green-soft);
  font-size: .63rem;
  font-weight: 650;
  letter-spacing: .06em;
  text-transform: uppercase;
  white-space: nowrap;
}
.agent-now-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.agent-now-card {
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--line);
  border-radius: var(--radius-small);
  background: rgba(0, 0, 0, .24);
}
.agent-now-card > header { gap: 10px; }
.agent-now-card > header > div { min-width: 0; flex: 1; }
.agent-now-card h2 { margin: 0; font-size: .82rem; font-weight: 620; letter-spacing: -.02em; }
.agent-now-card header small,
.agent-now-decision small { color: var(--subtle); font-size: .64rem; }
.agent-now-decision { gap: 10px; margin-top: 14px; }
.agent-now-icon { display: grid; width: 34px; height: 34px; flex: 0 0 auto; place-items: center; border-radius: 50%; color: var(--green); background: var(--green-soft); }
.agent-now-icon .ui-icon { width: 17px; height: 17px; }
.agent-now-decision > div { display: grid; min-width: 0; gap: 3px; }
.agent-now-decision strong { font-size: .78rem; font-weight: 560; line-height: 1.35; }
.agent-now-card > footer { justify-content: space-between; gap: 16px; margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--line); color: var(--muted); font-size: .68rem; }
.agent-now-card > footer strong { font-size: .7rem; font-variant-numeric: tabular-nums; white-space: nowrap; }

.metrics {
  display: grid;
  grid-template-columns: minmax(250px, 1.35fr) repeat(4, minmax(150px, 1fr));
  gap: 12px;
  align-items: stretch;
}
.metric {
  position: relative;
  display: flex;
  min-height: 148px;
  min-width: 0;
  flex-direction: column;
  justify-content: center;
  padding: 20px;
  overflow: hidden;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--surface);
  box-shadow: inset 0 1px rgba(255, 255, 255, .018);
  transition: border-color 180ms var(--ease-out), background-color 180ms var(--ease-out), transform 180ms var(--ease-out);
}
.metric:hover { border-color: var(--line-strong); background: #101010; transform: translateY(-2px); }
.metric:first-child { background: linear-gradient(145deg, #121212, #0d0d0d 68%); }
.metric-label {
  display: flex;
  align-items: center;
  gap: 7px;
  color: var(--muted);
  font-size: .72rem;
  font-weight: 540;
}
.metric:first-child .metric-label { font-size: .78rem; }
.metric-main { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: 7px 10px; margin: 12px 0 7px; }
.metric-value {
  display: block;
  min-width: 0;
  margin: 0;
  font-size: clamp(1.12rem, 1.65vw, 1.42rem);
  font-weight: 600;
  line-height: 1.18;
  letter-spacing: -.045em;
  font-variant-numeric: tabular-nums;
}
.metric:first-child .metric-value {
  font-size: clamp(2rem, 2.8vw, 2.85rem);
  font-weight: 600;
  letter-spacing: -.06em;
  white-space: nowrap;
}
.metric-trend {
  display: grid;
  width: 30px;
  height: 30px;
  flex: 0 0 auto;
  border: 1px solid currentColor;
  border-radius: 50%;
  place-items: center;
  opacity: .9;
}
.metric-trend .ui-icon { width: 15px; height: 15px; }
.metric-trend.positive { background: var(--green-soft); }
.metric-trend.negative { background: var(--red-soft); }
.metric-trend.neutral { color: var(--muted); background: rgba(145, 145, 145, .08); }
.metric-percent { color: inherit; font-size: .72em; font-weight: 520; letter-spacing: -.02em; }
.metric-foot {
  color: var(--subtle);
  font-size: .68rem;
  line-height: 1.4;
}
.positive { color: var(--green) !important; }
.negative { color: var(--red) !important; }
.neutral { color: var(--text); }

.help {
  display: grid;
  width: 32px;
  height: 32px;
  margin: -7px;
  padding: 0;
  border: 0;
  place-items: center;
  color: var(--subtle);
  background: transparent;
  cursor: help;
}
.help .ui-icon { width: 14px; height: 14px; }
.help:hover { color: var(--text); }
.help-dialog {
  width: min(520px, calc(100% - 32px));
  max-height: calc(100dvh - 32px);
  padding: 0;
  overflow: auto;
  border: 1px solid var(--line-strong);
  border-radius: var(--radius);
  color: var(--secondary);
  background: var(--surface-raised);
  box-shadow: 0 28px 80px rgba(0, 0, 0, .68);
}
.help-dialog[open] { animation: dialog-in 280ms cubic-bezier(.23, .88, .26, .92); }
.help-dialog.plan { width: min(780px, calc(100% - 32px)); }
.help-dialog::backdrop { background: rgba(0, 0, 0, .76); backdrop-filter: blur(5px); animation: backdrop-in 180ms ease-out; }
.help-dialog-panel { padding: 0; }
.help-dialog-head { display: flex; min-height: 56px; align-items: center; justify-content: space-between; gap: 16px; padding: 0 20px; border-bottom: 1px solid var(--line); }
.help-dialog-head > span { color: var(--green); font-size: .65rem; font-weight: 600; letter-spacing: .1em; text-transform: uppercase; }
.help-dialog-close { display: grid; width: 40px; height: 40px; padding: 0; border: 1px solid var(--line-strong); border-radius: 50%; place-items: center; color: var(--muted); background: transparent; font-size: 1.15rem; line-height: 1; cursor: pointer; transition: color 160ms ease, background-color 160ms ease, transform 160ms ease; }
.help-dialog-close:hover { color: var(--text); background: var(--surface-hover); }
.help-dialog-close:active { transform: scale(.94); }
.help-dialog-content { display: grid; grid-template-columns: 180px minmax(0, 1fr); gap: 24px; align-items: center; padding: 24px; }
.help-dialog.plan .help-dialog-content { grid-template-columns: 1fr; gap: 16px; }
.help-dialog-visual { display: grid; min-height: 112px; place-items: center; overflow: hidden; border: 1px solid var(--line); border-radius: var(--radius-small); background: #0a0a0a; }
.help-dialog.plan .help-dialog-visual { min-height: 0; overflow: visible; border: 0; background: transparent; }
.help-dialog h2 { margin: 0 0 8px; color: var(--text); font-size: 1.22rem; font-weight: 600; letter-spacing: -.035em; }
.help-dialog h2:focus { outline: none; }
.help-dialog-copy p { max-width: 44ch; margin: 0; color: var(--muted); font-size: .78rem; line-height: 1.6; }
.explain-svg { display: block; width: 100%; height: 104px; }
.explain-svg path, .explain-svg circle, .explain-svg rect { fill: none; stroke-linecap: round; stroke-linejoin: round; stroke-width: 2; }
.explain-svg .visual-grid { stroke: #262626; stroke-width: 1; }
.explain-svg .visual-primary { stroke: var(--green); }
.explain-svg .visual-muted { stroke: var(--muted); }
.explain-svg .visual-dash { stroke: var(--subtle); stroke-dasharray: 4 6; stroke-width: 1; }
.explain-svg .visual-dot { fill: var(--green); stroke: #07120b; }
.explain-svg .visual-dot.muted { fill: var(--muted); }
.explain-svg .visual-node { fill: #111; stroke: var(--line-strong); }
.explain-svg .visual-node.active { fill: var(--green-soft); stroke: var(--green); }
.ui-icon { width: 20px; height: 20px; fill: none; stroke: currentColor; stroke-width: 1.7; stroke-linecap: round; stroke-linejoin: round; }

.plan-loop { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; padding: 0; margin: 0; list-style: none; }
.plan-node { position: relative; display: flex; min-width: 0; min-height: 68px; align-items: center; gap: 10px; padding: 12px; border: 1px solid var(--line); border-radius: var(--radius-small); color: var(--subtle); background: #0b0b0b; animation: plan-node-in 320ms cubic-bezier(.23, .88, .26, .92) backwards; transition: border-color 160ms ease, color 160ms ease, transform 160ms ease; }
.plan-node:nth-child(2) { animation-delay: 40ms; }
.plan-node:nth-child(3) { animation-delay: 80ms; }
.plan-node:nth-child(4) { animation-delay: 120ms; }
.plan-node:hover { color: var(--secondary); border-color: var(--line-strong); transform: translateY(-2px); }
.plan-node.active { color: var(--green); border-color: rgba(134, 239, 172, .42); background: var(--green-soft); box-shadow: inset 0 0 24px rgba(134, 239, 172, .035); }
.plan-node.active::after { content: ""; position: absolute; top: 9px; right: 9px; width: 5px; height: 5px; border-radius: 50%; background: var(--green); box-shadow: 0 0 0 5px rgba(134, 239, 172, .08); animation: plan-pulse 1.8s ease-in-out infinite; }
.plan-node .ui-icon { flex: 0 0 auto; }
.plan-node span { min-width: 0; }
.plan-node strong, .plan-node small { display: block; }
.plan-node strong { color: var(--text); font-size: .75rem; }
.plan-node small { margin-top: 2px; color: currentColor; font-size: .61rem; }
.plan-snapshot { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; margin: 0 24px; overflow: hidden; border: 1px solid var(--line); border-radius: var(--radius-small); background: var(--line); }
.plan-snapshot article { position: relative; min-width: 0; min-height: 116px; padding: 16px; background: #101010; }
.plan-snapshot .ui-icon { margin-bottom: 18px; color: var(--green); }
.plan-snapshot span, .plan-snapshot strong, .plan-snapshot small { display: block; }
.plan-snapshot span { color: var(--muted); font-size: .62rem; }
.plan-snapshot strong { margin-top: 5px; overflow: hidden; color: var(--text); font-size: .95rem; font-variant-numeric: tabular-nums; text-overflow: ellipsis; }
.plan-snapshot small { margin-top: 3px; color: var(--subtle); font-size: .59rem; line-height: 1.35; }
.plan-allocation { margin: 20px 24px 0; }
.plan-allocation > div:first-child { display: flex; align-items: center; justify-content: space-between; gap: 16px; color: var(--muted); font-size: .67rem; }
.plan-allocation strong { color: var(--text); font-variant-numeric: tabular-nums; }
.plan-meter { display: block; width: 100%; height: 7px; margin-top: 10px; overflow: hidden; border: 0; border-radius: 99px; appearance: none; background: #242424; }
.plan-meter::-webkit-progress-bar { border-radius: inherit; background: #242424; }
.plan-meter::-webkit-progress-value { border-radius: inherit; background: linear-gradient(90deg, #2d8d61, var(--green)); transform-origin: left; animation: meter-in 520ms cubic-bezier(.23, .88, .26, .92) both; }
.plan-meter::-moz-progress-bar { border-radius: inherit; background: linear-gradient(90deg, #2d8d61, var(--green)); }
.plan-more { margin: 18px 24px 0; border-top: 1px solid var(--line); }
.plan-more summary { width: max-content; min-height: 42px; padding: 12px 0 6px; color: var(--muted); font-size: .67rem; cursor: pointer; }
.plan-more .limit-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.plan-footnote { max-width: 62ch; margin: 12px 0 0 !important; color: var(--subtle) !important; font-size: .65rem !important; line-height: 1.5 !important; }
.plan-reason { display: flex; align-items: center; gap: 12px; margin: 20px 24px 24px !important; padding: 14px; border-radius: var(--radius-small); color: var(--secondary) !important; background: var(--green-soft); }
.plan-reason > span:first-child { display: grid; width: 34px; height: 34px; flex: 0 0 auto; border-radius: 50%; place-items: center; color: var(--green); background: rgba(134, 239, 172, .08); }
.plan-reason > span:last-child, .plan-reason strong { display: block; }
.plan-reason strong { margin-bottom: 2px; color: var(--text); font-size: .67rem; }
.plan-reason > span:last-child { color: var(--muted); font-size: .68rem; line-height: 1.45; }

.market-grid { margin-top: 24px; }
.market {
  overflow: hidden;
  padding: 24px;
  border-radius: var(--radius);
  background: var(--surface);
}
.market-head {
  display: flex;
  min-height: 44px;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
}
.qualification-strip { display: grid; grid-template-columns: auto repeat(3, minmax(0, 1fr)) auto; gap: 18px; align-items: center; margin-top: 18px; padding: 14px 16px; border: 1px solid var(--line); border-radius: var(--radius-small); background: #0b0b0b; }
.qualification-strip > span { min-width: 0; }
.qualification-strip small, .qualification-strip strong { display: block; }
.qualification-strip small { color: var(--subtle); font-size: .59rem; }
.qualification-strip strong { margin-top: 3px; overflow: hidden; color: var(--secondary); font-size: .7rem; text-overflow: ellipsis; white-space: nowrap; }
.qualification-symbol { display: grid; width: 34px; height: 34px; border-radius: 50%; place-items: center; color: var(--green); background: var(--green-soft); }
.qualification-symbol .ui-icon { width: 17px; height: 17px; }
.performance-title { display: flex; min-width: 0; align-items: center; gap: 12px; }
.performance-title h3,
.market-head > h3 { margin: 0; font-size: 1.2rem; font-weight: 550; letter-spacing: -.03em; }
.asset-chip {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  gap: 8px;
  padding: 0 10px 0 5px;
  border-radius: 999px;
  color: var(--secondary);
  background: var(--surface-hover);
  border: 1px solid var(--line-strong);
  font-size: .69rem;
}
.asset-chip > span {
  display: grid;
  width: 20px;
  height: 20px;
  place-items: center;
  border-radius: 50%;
  color: #062012;
  background: var(--green);
  font-size: .6rem;
  font-weight: 750;
}
.market-status { display: flex; align-items: center; gap: 9px; }
.updated { color: var(--subtle); font-size: .66rem; white-space: nowrap; }
.badge {
  display: inline-flex;
  width: max-content;
  min-height: 20px;
  align-items: center;
  gap: 6px;
  font-size: .65rem;
  font-weight: 600;
  white-space: nowrap;
}
.badge::before {
  content: "";
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}
.badge.green { color: var(--green); }
.badge.red { color: var(--red); }
.badge.amber { color: var(--amber); }
.badge.blue { color: var(--blue); }
.badge.violet { color: var(--violet); }
.badge.neutral { color: var(--muted); }

.market-chart-stage { position: relative; min-width: 0; }
.balance-strip {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 1px;
  overflow: hidden;
  margin-top: 18px;
  border: 1px solid var(--line);
  border-radius: var(--radius-small);
  background: var(--line);
}
.balance-strip > span { display: grid; min-width: 0; gap: 4px; padding: 14px 16px; background: #0b0b0b; }
.balance-strip small { color: var(--subtle); font-size: .6rem; }
.balance-strip strong { color: var(--secondary); font-size: .73rem; font-weight: 570; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.balance-note { grid-column: 1 / -1; margin: 0; padding: 10px 16px; color: var(--muted); background: #0b0b0b; font-size: .65rem; }
.balance-strip.unavailable { display: flex; align-items: center; gap: 12px; padding: 14px 16px; color: var(--muted); background: #0b0b0b; }
.balance-strip.unavailable > span { display: grid; width: 34px; height: 34px; flex: 0 0 auto; padding: 0; place-items: center; border-radius: 50%; color: var(--subtle); background: var(--surface-hover); }
.balance-strip.unavailable p { display: grid; gap: 3px; margin: 0; }
.balance-strip.unavailable strong { color: var(--secondary); }
.chart-switch {
  position: absolute;
  z-index: 3;
  top: -44px;
  right: 0;
  display: inline-flex;
  padding: 3px;
  border-radius: 9px;
  background: var(--surface-hover);
}
.chart-toggle {
  min-height: 30px;
  padding: 0 11px;
  border: 0;
  border-radius: 7px;
  color: var(--subtle);
  background: transparent;
  font-size: .66rem;
  cursor: pointer;
}
.chart-toggle:hover { color: var(--text); }
.chart-toggle.active { color: var(--text); background: #2b2b2b; }
.chart { padding-top: 24px; }
.chart-head {
  display: flex;
  min-height: 32px;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.chart-title { display: block; color: var(--secondary); font-size: .74rem; font-weight: 550; }
.chart-title-row { display: flex; align-items: center; gap: 5px; }
.chart-subtitle { display: block; margin-top: 2px; color: var(--subtle); font-size: .62rem; }
.chart-tools { display: flex; gap: 5px; }
.chart-tools button {
  min-width: 36px;
  min-height: 36px;
  padding: 0 8px;
  border: 1px solid var(--line);
  border-radius: 7px;
  color: var(--muted);
  background: transparent;
  font-size: .65rem;
  cursor: pointer;
}
.chart-tools button:hover { color: var(--text); background: var(--surface-hover); }
.chart-readout {
  display: flex;
  min-height: 31px;
  align-items: center;
  gap: 20px;
  padding-top: 10px;
  color: var(--muted);
  font-size: .72rem;
}
.chart-readout span { display: inline-flex; align-items: center; gap: 7px; }
.chart-readout strong { color: var(--text); font-weight: 600; font-variant-numeric: tabular-nums; }
.chart-readout time { margin-left: auto; color: var(--subtle); }
.legend-line { width: 18px; height: 2px; flex: 0 0 18px; border-radius: 2px; background: var(--green); }
.legend-line.green { background: #14f195; }
.legend-line.muted { background: #787878; }
.chart-comparison { font-weight: 550; }
.chart-canvas { width: 100%; height: 390px; outline: none; }
.chart-canvas:focus-visible { border-radius: 8px; box-shadow: inset 0 0 0 2px var(--green); }
.chart-empty {
  display: grid;
  height: 390px;
  place-items: center;
  color: var(--subtle);
  font-size: .75rem;
}
.chart-data { margin-top: 8px; }
.chart-data > summary {
  width: max-content;
  padding: 8px 0;
  color: var(--subtle);
  font-size: .67rem;
  cursor: pointer;
}
.chart-table-scroll { max-height: 260px; overflow: auto; border: 1px solid var(--line); border-radius: 10px; }
.chart-data table { width: 100%; border-collapse: collapse; font-size: .7rem; }
.chart-data th, .chart-data td { padding: 9px 11px; border-bottom: 1px solid var(--line); text-align: right; white-space: nowrap; }
.chart-data th:first-child { text-align: left; }
.chart-data thead th { position: sticky; top: 0; color: var(--muted); background: var(--surface-raised); font-weight: 550; }

.market-switcher {
  display: block;
  margin-top: 24px;
  padding: 24px;
  overflow-x: auto;
  border-radius: var(--radius);
  background: var(--surface);
}

@media (min-width: 1280px) {
  .overview-workspace {
    display: grid;
    grid-template-columns: minmax(0, 3fr) minmax(330px, 2fr);
    gap: 24px;
    align-items: stretch;
    margin-top: 24px;
  }
  .overview-workspace .market-grid,
  .overview-workspace .market-switcher { min-width: 0; margin-top: 0; }
  .overview-workspace .market { height: 100%; }
  .overview-workspace .market-list-head,
  .overview-workspace .market-choice {
    min-width: 0;
    grid-template-columns: minmax(110px, 1fr) 82px max-content;
    gap: 12px;
  }
  .overview-workspace .market-list-head > :nth-child(n+3):nth-child(-n+6),
  .overview-workspace .market-choice > :nth-child(n+3):nth-child(-n+6) { display: none; }
}
.market-switcher::before {
  content: "Live spot markets";
  display: block;
  margin: 0 12px 18px;
  font-size: 1rem;
  font-weight: 600;
  letter-spacing: -.025em;
}
.perps-research { margin-top: 28px; }
.perps-research-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
.perps-research-card {
  min-width: 0;
  padding: 18px;
  border: 1px solid var(--line);
  border-radius: var(--radius-small);
  background: var(--surface);
}
.perps-research-card > header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.perps-outcome { display: grid; grid-template-columns: 28px minmax(0, 1fr); gap: 10px; align-items: center; margin-top: 18px; padding-top: 16px; border-top: 1px solid var(--line); }
.perps-outcome > span { display: grid; width: 28px; height: 28px; place-items: center; border-radius: 50%; color: var(--amber); background: var(--amber-soft); }
.perps-outcome > span .ui-icon { width: 15px; height: 15px; }
.perps-outcome small, .perps-outcome strong { display: block; }
.perps-outcome small { color: var(--subtle); font-size: .75rem; }
.perps-outcome strong { margin-top: 3px; font-size: .75rem; }
.perps-outcome .plan-trigger { grid-column: 2; justify-self: start; }
.attempt-grid { display: grid; gap: 7px; margin-top: 14px; }
.attempt-card { padding: 12px; border-radius: 10px; background: var(--surface-raised); }
.attempt-head, .attempt-result { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.attempt-head strong, .attempt-head small { display: block; }
.attempt-head strong { font-size: .75rem; }
.attempt-head small { margin-top: 2px; color: var(--subtle); font-size: .75rem; }
.attempt-result { margin-top: 10px; font-size: .75rem; }
.attempt-result > span { color: var(--muted); }
.attempt-result > strong { font-size: .75rem; font-variant-numeric: tabular-nums; }
.attempt-meter { display: block; width: 100%; height: 5px; margin-top: 8px; border: 0; border-radius: 999px; overflow: hidden; background: var(--line); }
.attempt-meter::-webkit-progress-bar { background: var(--line); }
.attempt-meter::-webkit-progress-value { border-radius: 999px; background: var(--muted); }
.attempt-meter.positive::-webkit-progress-value { background: var(--green); }
.attempt-meter.negative::-webkit-progress-value { background: var(--red); }
.attempt-meter::-moz-progress-bar { border-radius: 999px; background: var(--muted); }
.attempt-meter.positive::-moz-progress-bar { background: var(--green); }
.attempt-meter.negative::-moz-progress-bar { background: var(--red); }
.attempt-card p, .perps-empty { margin: 8px 0 0; color: var(--subtle); font-size: .75rem; line-height: 1.5; }
.perps-current-evidence { margin-top: 14px; }
.perps-progress-head { margin-top: 14px; }
.perps-current-reason { margin: 11px 0 0; color: var(--muted); font-size: .7rem; line-height: 1.5; }
.attempt-kicker { display: flex; min-height: 26px; align-items: center; justify-content: space-between; gap: 10px; color: var(--muted); font-size: .75rem; font-weight: 600; }
.attempt-kicker .help { flex: 0 0 auto; }
.attempt-more { margin-top: 2px; }
.attempt-more > summary { min-height: 36px; cursor: pointer; color: var(--muted); font-size: .75rem; font-weight: 600; }
.attempt-more[open] > summary { margin-bottom: 7px; }
.market-list-head,
.market-choice {
  display: grid;
  min-width: 880px;
  grid-template-columns: minmax(180px, 1.35fr) minmax(110px, .8fr) 70px minmax(100px, .8fr) minmax(110px, .8fr) minmax(110px, .8fr) minmax(145px, 1fr);
  gap: 18px;
  align-items: center;
}
.market-list-head {
  min-height: 36px;
  padding: 0 12px 8px;
  color: var(--muted);
  font-size: .78rem;
}
.market-list-head span:nth-child(n+3) { text-align: right; }
.market-choice {
  position: relative;
  width: 100%;
  min-height: 72px;
  padding: 12px;
  border: 0;
  border-radius: var(--radius-small);
  color: var(--text);
  background: transparent;
  text-align: left;
  cursor: pointer;
  transition: background-color 150ms ease, transform 150ms ease;
}
.market-choice:hover { background: #151515; }
.market-choice.active { background: var(--surface-hover); }
.market-choice:active { transform: scale(.997); }
.market-choice.active::before {
  content: "";
  position: absolute;
  top: 18px;
  bottom: 18px;
  left: 0;
  width: 3px;
  border-radius: 0 4px 4px 0;
  background: var(--green);
}
.market-choice-name { display: block; min-width: 0; }
.market-choice-name strong { display: block; font-size: .88rem; font-weight: 600; }
.market-choice-name small {
  display: block;
  margin-top: 4px;
  overflow: hidden;
  color: var(--muted);
  font-size: .69rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.market-choice-name small.green { color: var(--green); }
.market-choice-name small.amber { color: var(--amber); }
.market-choice-name small.red { color: var(--red); }
.market-sparkline { display: block; width: 100%; height: 40px; }
.market-choice-value {
  overflow: hidden;
  color: var(--secondary);
  font-size: .8rem;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.return-arrow { margin-right: 4px; }
.daily-note {
  margin: 14px 0 0;
  color: var(--subtle);
  font-size: .68rem;
}
.daily-note summary { width: max-content; min-height: 36px; padding: 9px 0; cursor: pointer; }
.daily-note p { max-width: 760px; margin: 3px 0 0; line-height: 1.55; }

.card,
.automation-grid,
.system-list {
  border-radius: var(--radius);
  background: var(--surface);
}
.card { padding: 24px; }
.automation-grid,
.system-list { padding: 24px; overflow: hidden; }
.activity-table {
  max-height: min(720px, calc(100dvh - 220px));
  overflow: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
}

.filter { display: flex; align-items: center; gap: 12px; color: var(--muted); font-size: .71rem; }
.filter select { min-width: 190px; padding: 0 12px; outline: none; }
.filter select:focus { border-color: var(--green); }

.activity-list-head,
.activity-item {
  display: grid;
  grid-template-columns: minmax(190px, .8fr) minmax(280px, 1.45fr) minmax(180px, .8fr) 110px;
  gap: 20px;
  align-items: center;
}
.activity-list-head,
.strategy-list-head,
.automation-list-head {
  min-height: 36px;
  padding: 0 12px 8px;
  color: var(--muted);
  font-size: .7rem;
}
.activity-list-head span:last-child,
.activity-time { text-align: right; }
.activity-table .activity-list-head {
  position: sticky;
  z-index: 3;
  top: 0;
  min-height: 42px;
  padding: 0 16px 9px;
  background: var(--canvas);
}
.activity-list { display: grid; gap: 9px; padding-bottom: 1px; }
.activity-item {
  min-height: 82px;
  padding: 15px 16px;
  border: 1px solid var(--line);
  border-radius: var(--radius-small);
  background: var(--surface);
  transition: border-color 180ms var(--ease-out), background-color 180ms var(--ease-out), transform 180ms var(--ease-out);
}
.activity-item:hover, .activity-item:focus-within { border-color: var(--line-strong); background: #101010; transform: translateY(-1px); }
.activity-event { display: grid; grid-template-columns: 34px minmax(0, 1fr); gap: 12px; align-items: center; min-width: 0; }
.event-mark { display: grid; width: 34px; height: 34px; border: 1px solid var(--line-strong); border-radius: 10px; place-items: center; color: var(--muted); background: #0a0a0a; }
.event-mark .ui-icon { width: 17px; height: 17px; }
.event-mark.order_opened { color: var(--amber); }
.event-mark.order_filled { color: var(--green); }
.event-mark.risk_halted, .event-mark.data_unavailable { color: var(--red); }
.activity-event span:not(.event-mark) { display: block; margin-bottom: 3px; color: var(--muted); font-size: .64rem; }
.activity-event h3 { margin: 0; font-size: .82rem; font-weight: 600; line-height: 1.3; }
.activity-copy p, .activity-result, .activity-time { margin: 0; font-size: .77rem; line-height: 1.5; }
.activity-copy p { color: var(--secondary); }
.activity-result { display: grid; gap: 3px; color: var(--secondary); font-weight: 550; }
.activity-result-label { color: var(--subtle); font-size: .61rem; font-weight: 500; }
.activity-result-value { display: inline-flex; align-items: center; gap: 6px; }
.activity-result-value .ui-icon { width: 15px; height: 15px; }
.activity-time { color: var(--subtle); font-variant-numeric: tabular-nums; }
.activity-more { margin-top: 6px; color: var(--muted); }
.activity-more summary { width: max-content; min-height: 36px; padding: 9px 0; font-size: .72rem; cursor: pointer; }
.activity-more p { padding: 2px 0 6px; white-space: pre-line; }

.strategy-layout { display: grid; grid-template-columns: minmax(0, 1.45fr) minmax(280px, .55fr); gap: 24px; }
.strategy-layout .card { min-height: 174px; }
.strategy-brief {
  display: grid;
  grid-template-columns: minmax(230px, .8fr) minmax(0, 1.4fr);
  gap: 32px;
  align-items: center;
}
.strategy-brief h3 { max-width: 310px; margin: 16px 0 0; font-size: 1.42rem; font-weight: 600; line-height: 1.22; letter-spacing: -.04em; }
.strategy-flow { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; padding: 0; margin: 0; list-style: none; }
.strategy-flow li { display: flex; min-width: 0; align-items: center; gap: 10px; padding: 12px; border: 1px solid var(--line); border-radius: var(--radius-small); background: #0b0b0b; transition: border-color 160ms ease, transform 160ms ease; }
.strategy-flow li:hover { border-color: var(--line-strong); transform: translateY(-2px); }
.flow-icon { display: grid; width: 32px; height: 32px; flex: 0 0 auto; border-radius: 50%; place-items: center; color: var(--green); background: var(--green-soft); }
.flow-icon svg { width: 17px; height: 17px; fill: none; stroke: currentColor; stroke-width: 1.7; stroke-linecap: round; stroke-linejoin: round; }
.strategy-flow strong, .strategy-flow small { display: block; }
.strategy-flow strong { font-size: .7rem; }
.strategy-flow small { margin-top: 2px; color: var(--subtle); font-size: .58rem; line-height: 1.35; }
.guardrails { display: flex; flex-direction: column; justify-content: center; }
.guardrails h3 { margin: 16px 0 8px; font-size: 1rem; }
.guardrails p { margin: 0; color: var(--muted); font-size: .74rem; line-height: 1.6; }

.market-grid.small { display: grid; gap: 9px; margin-top: 24px; }
.strategy-market-group { display: grid; gap: 9px; }
.strategy-market-group + .strategy-market-group { margin-top: 24px; padding-top: 24px; border-top: 1px solid var(--line); }
.strategy-market-group .subsection-head { margin-bottom: 4px; }
.strategy-market-group .subsection-head h3 { margin: 0; font-size: .9rem; }
.strategy-market-group .subsection-head p { margin: 4px 0 0; color: var(--muted); font-size: .67rem; }
.strategy-list-head,
.strategy-market-row {
  display: grid;
  grid-template-columns: minmax(180px, .72fr) minmax(300px, 1.55fr) minmax(190px, .7fr);
  gap: 24px;
  align-items: center;
}
.strategy-list-head { padding-bottom: 10px; }
.strategy-list-head span:last-child { text-align: right; }
.strategy-market-row {
  min-height: 82px;
  padding: 15px 16px;
  border: 1px solid var(--line);
  border-radius: var(--radius-small);
  background: var(--surface);
  transition: border-color 180ms var(--ease-out), background-color 180ms var(--ease-out), transform 180ms var(--ease-out);
}
.strategy-market-row:hover, .strategy-market-row:focus-within { border-color: var(--line-strong); background: #101010; transform: translateY(-1px); }
.strategy-market-name { display: flex; min-width: 0; align-items: center; gap: 12px; }
.asset-orb {
  display: grid;
  width: 34px;
  height: 34px;
  flex: 0 0 auto;
  place-items: center;
  border-radius: 50%;
  color: var(--green);
  background: var(--green-soft);
  border: 1px solid rgba(134, 239, 172, .24);
  font-size: .7rem;
  font-weight: 750;
}
.strategy-market-name h3 { margin: 0; font-size: .86rem; }
.strategy-market-name span:not(.asset-orb) { color: var(--muted); font-size: .64rem; }
.strategy-next { margin: 0; color: var(--secondary); font-size: .75rem; line-height: 1.45; }
.strategy-next small { display: block; margin-top: 4px; color: var(--subtle); font-size: .64rem; }
.strategy-plan { display: flex; min-width: 0; align-items: flex-end; justify-self: end; flex-direction: column; gap: 8px; }
.plan-trigger { display: inline-flex; min-height: 36px; align-items: center; gap: 7px; padding: 0; border: 0; color: var(--muted); background: transparent; font-size: .7rem; cursor: pointer; transition: color 160ms ease; }
.plan-trigger:hover { color: var(--green); }
.plan-trigger .ui-icon { width: 14px; height: 14px; transition: transform 180ms cubic-bezier(.23, .88, .26, .92); }
.plan-trigger:hover .ui-icon { transform: translateX(3px); }
.limit-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 1px;
  margin-top: 10px;
  overflow: hidden;
  border: 1px solid var(--line);
  border-radius: var(--radius-small);
  background: var(--line);
}
.limit-grid > div { min-width: 0; padding: 14px; background: var(--surface-raised); }
.limit-grid dt { color: var(--muted); font-size: .64rem; }
.limit-grid dd { margin: 6px 0 0; color: var(--text); font-size: .77rem; font-variant-numeric: tabular-nums; }
.limit-note { max-width: 900px; margin: 10px 0 0; color: var(--subtle); font-size: .67rem; line-height: 1.55; }

.experiment-card { margin-top: 24px; padding: 0; overflow: hidden; }
.experiment-card > .instruction-copy,
.experiment-card > .active-limits,
.experiment-card > .instruction-controls { padding: 24px; }
.instruction-copy h3 { margin: 16px 0 8px; font-size: 1.08rem; }
.instruction-copy p { max-width: 720px; margin: 0; color: var(--muted); font-size: .75rem; }
.active-limits { border-top: 1px solid var(--line); }
.subsection-head { display: flex; align-items: center; gap: 12px; }
.subsection-head h4 { margin: 0; font-size: .86rem; }
.active-limit-list { display: grid; gap: 1px; margin-top: 16px; overflow: hidden; border-radius: 10px; background: var(--line); }
.active-limit { display: grid; grid-template-columns: 130px minmax(180px, .7fr) minmax(280px, 1.3fr); gap: 16px; align-items: center; padding: 13px 14px; background: var(--surface-raised); }
.active-limit strong { font-size: .76rem; }
.active-limit span { color: var(--secondary); font-size: .7rem; }
.active-limit small { color: var(--muted); font-size: .65rem; line-height: 1.45; }
.instruction-controls {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  border-top: 1px solid var(--line);
  background: #101010;
}
.instruction-group { display: grid; min-width: 0; align-content: start; gap: 14px; padding: 16px; border: 1px solid var(--line); border-radius: var(--radius-small); background: var(--surface); }
.instruction-group legend { padding: 0 7px; color: var(--text); font-size: .74rem; font-weight: 600; }
.instruction-controls label { display: grid; gap: 7px; color: var(--muted); font-size: .72rem; }
.instruction-controls input,
.instruction-controls select { width: 100%; min-width: 0; padding: 0 11px; outline: none; }
.instruction-controls input { width: 100%; min-width: 0; padding: 0 11px; outline: none; }
.instruction-controls input:focus,
.instruction-controls select:focus { border-color: var(--green); }
.instruction-controls small { color: var(--subtle); font-size: .68rem; line-height: 1.45; }
.locked-setting { display: grid; gap: 5px; padding: 11px 12px; border: 1px solid var(--line); border-radius: 9px; background: var(--surface-raised); }
.locked-setting > span { color: var(--muted); font-size: .68rem; }
.locked-setting > strong { color: var(--text); font-size: .86rem; font-weight: 600; }
.money-input,
.percent-input {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  border: 1px solid var(--line-strong);
  border-radius: 9px;
  background: var(--surface);
}
.money-input > span, .percent-input > span { padding: 0 0 0 11px; color: var(--subtle); }
.money-input input, .percent-input input { border: 0; background: transparent; }
.percent-input { grid-template-columns: minmax(0, 1fr) auto; }
.percent-input > span { padding: 0 11px 0 0; }
.instruction-warning { grid-column: 1 / -1; min-height: 0; color: var(--amber); font-size: .68rem; }
.instruction-controls .button { justify-self: start; background: var(--green); color: #062012; border-color: var(--green); }
#instruction-status { align-self: center; color: var(--muted); font-size: .67rem; }
.instruction-boundary { margin: 0; padding: 16px 24px 22px; color: var(--subtle); font-size: .66rem; line-height: 1.55; }

.automation-grid { display: block; }
.automation-list-head,
.automation-card {
  display: grid;
  grid-template-columns: minmax(210px, .75fr) minmax(320px, 1.7fr) minmax(150px, .55fr);
  gap: 24px;
  align-items: center;
}
.automation-list-head { padding-bottom: 10px; }
.automation-list-head span:last-child { text-align: right; }
.automation-card {
  min-height: 78px;
  padding: 14px 12px;
  border-radius: var(--radius-small);
  transition: background-color 150ms ease;
}
.automation-card:hover { background: var(--surface-hover); }
.automation-name { display: flex; min-width: 0; align-items: center; gap: 12px; }
.role-symbol {
  display: grid;
  width: 28px;
  height: 28px;
  flex: 0 0 auto;
  place-items: center;
  color: var(--green);
  background: transparent;
  font-size: .63rem;
  font-weight: 700;
}
.automation-card h3 { margin: 0; font-size: .88rem; font-weight: 600; }
.automation-card p { margin: 0; color: var(--muted); font-size: .76rem; line-height: 1.5; }
.automation-card > .badge { justify-self: end; }

.market-research-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
.market-research-card {
  min-width: 0;
  padding: 20px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: linear-gradient(145deg, #101010, #0b0b0b);
}
.research-market-head,
.research-progress-head { display: flex; align-items: center; justify-content: space-between; gap: 14px; }
.research-market-name { display: flex; min-width: 0; align-items: center; gap: 11px; }
.research-market-name > span {
  display: grid;
  width: 34px;
  height: 34px;
  flex: 0 0 auto;
  border: 1px solid var(--line-strong);
  border-radius: 50%;
  place-items: center;
  color: var(--green);
  background: var(--green-soft);
  font-size: .62rem;
  font-weight: 700;
}
.research-market-name h3 { margin: 0; font-size: .86rem; font-weight: 620; }
.research-market-name small { display: block; margin-top: 2px; color: var(--subtle); font-size: .7rem; }
.research-progress-head { margin-top: 22px; color: var(--muted); font-size: .72rem; }
.research-progress-head strong { color: var(--secondary); font-size: .76rem; font-variant-numeric: tabular-nums; }
.research-progress { display: block; width: 100%; height: 6px; margin-top: 9px; overflow: hidden; border: 0; border-radius: 999px; appearance: none; background: #222; }
.research-progress::-webkit-progress-bar { border-radius: inherit; background: #222; }
.research-progress::-webkit-progress-value { border-radius: inherit; background: linear-gradient(90deg, #2d8d61, var(--green)); transition: width 400ms var(--ease-out); }
.research-progress::-moz-progress-bar { border-radius: inherit; background: linear-gradient(90deg, #2d8d61, var(--green)); }
.research-stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; margin-top: 18px; }
.research-stats > span { min-width: 0; padding: 10px; border-radius: 10px; background: var(--surface-raised); }
.research-stats small,
.research-stats strong,
.research-stats em { display: block; }
.research-stats small { color: var(--subtle); font-size: .7rem; line-height: 1.35; }
.research-stats strong { margin-top: 5px; color: var(--text); font-size: .82rem; font-variant-numeric: tabular-nums; }
.research-stats em { margin-top: 3px; color: var(--muted); font-size: .7rem; font-style: normal; line-height: 1.35; }
.research-check {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 1px;
  margin-top: 14px;
  overflow: hidden;
  border: 1px solid var(--line);
  border-radius: 10px;
  background: var(--line);
}
.research-check > span { min-width: 0; padding: 10px; background: var(--surface-raised); }
.research-check small,
.research-check strong { display: block; }
.research-check small { color: var(--subtle); font-size: .7rem; line-height: 1.35; }
.research-check strong { margin-top: 5px; color: var(--text); font-size: .78rem; font-variant-numeric: tabular-nums; }
.research-check strong.positive { color: var(--green); }
.research-check strong.negative { color: var(--red); }
.market-research-card > p { min-height: 2.8em; margin: 16px 0 0; color: var(--muted); font-size: .72rem; line-height: 1.45; }
.market-research-card > p.research-history { min-height: 0; margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--line); color: var(--subtle); }
.market-research-empty { grid-column: 1 / -1; padding: 24px; border-radius: var(--radius); color: var(--muted); background: var(--surface); font-size: .74rem; }
.market-research-empty.error { color: var(--red); }

.system-list { display: grid; gap: 0; }
.system-row {
  display: grid;
  grid-template-columns: minmax(150px, .55fr) minmax(280px, 1.7fr) minmax(110px, .4fr);
  gap: 20px;
  min-height: 70px;
  align-items: center;
  padding: 12px;
  border-radius: var(--radius-small);
  transition: background-color 150ms ease;
}
.system-row:hover { background: var(--surface-hover); }
.system-row p { margin: 0; font-size: .8rem; }
.system-row .description { color: var(--muted); font-size: .75rem; }
.system-row .badge { justify-self: end; }
.detail-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.detail-card { min-height: 160px; }
.detail-card h3 { margin: 16px 0 8px; font-size: .92rem; }
.detail-card p { margin: 0; color: var(--muted); font-size: .72rem; line-height: 1.62; }
.detail-card .text-button { margin-top: 12px; }
.access { display: grid; grid-template-columns: minmax(180px, .55fr) minmax(320px, 1.45fr); gap: 28px; align-items: center; margin-top: 16px; }
.access h3 { margin: 14px 0 0; font-size: .94rem; }
.access p { margin: 0; color: var(--muted); font-size: .72rem; line-height: 1.6; }
.empty { display: grid; min-height: 120px; place-items: center; color: var(--muted); font-size: .75rem; }

:focus-visible { outline: 2px solid var(--green); outline-offset: 2px; }
@keyframes spin { to { transform: rotate(360deg); } }
@keyframes view-enter { from { opacity: .65; transform: translateY(5px); } to { opacity: 1; transform: none; } }
@keyframes dialog-in { from { opacity: 0; transform: translateY(14px) scale(.975); } to { opacity: 1; transform: none; } }
@keyframes backdrop-in { from { opacity: 0; } to { opacity: 1; } }
@keyframes plan-node-in { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: none; } }
@keyframes meter-in { from { transform: scaleX(0); } to { transform: scaleX(1); } }
@keyframes plan-pulse { 0%, 100% { box-shadow: 0 0 0 3px rgba(134, 239, 172, .06); } 50% { box-shadow: 0 0 0 7px rgba(134, 239, 172, .12); } }

@media (max-width: 1399px) {
  .market-research-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}

@media (max-width: 1279px) {
  .metrics { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .metric:first-child { grid-column: span 2; }
  .activity-list-head, .activity-item { grid-template-columns: minmax(170px, .8fr) minmax(230px, 1.3fr) minmax(150px, .7fr) 98px; gap: 16px; }
  .strategy-layout { grid-template-columns: minmax(0, 1.25fr) minmax(250px, .75fr); }
  .strategy-brief { grid-template-columns: 1fr; gap: 22px; }
  .limit-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .instruction-controls { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}

@media (max-width: 1023px) {
  .app-header, .topbar { height: 64px; }
  .app-header { background: rgba(0, 0, 0, .88); }
  .topbar { padding: 0 16px; }
  .brand { min-width: 0; }
  .tabs {
    top: 72px;
    right: 16px;
    bottom: auto;
    left: 16px;
    width: auto;
    height: 56px;
    flex-direction: row;
    gap: 24px;
    padding: 14px 24px;
    overflow-x: auto;
    border-radius: 12px;
  }
  .tab { min-width: max-content; flex: 1 0 auto; grid-template-columns: 24px auto; min-height: 28px; padding: 0; }
  main.shell { margin-left: 0; padding: 148px 16px 44px; }
  footer.shell { margin-left: 0; padding: 0 16px 24px; }
  .metrics { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .metric:first-child { grid-column: span 2; }
  .activity-list-head { display: none; }
  .activity-item { grid-template-columns: minmax(170px, .8fr) minmax(0, 1.2fr) minmax(145px, .7fr) 100px; }
  .strategy-layout { grid-template-columns: 1fr; }
  .perps-research-grid { grid-template-columns: 1fr; }
  .perps-research-card .attempt-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .strategy-brief { grid-template-columns: minmax(200px, .7fr) minmax(0, 1.3fr); }
  .strategy-list-head, .strategy-market-row { grid-template-columns: minmax(155px, .65fr) minmax(220px, 1.35fr) minmax(160px, .65fr); gap: 16px; }
  .automation-list-head, .automation-card { grid-template-columns: minmax(180px, .7fr) minmax(250px, 1.4fr) 140px; gap: 16px; }
  .instruction-controls { grid-template-columns: 1fr; }
  .market-research-grid { grid-template-columns: 1fr; }
}

@media (max-width: 767px) {
  .header-state .trust { display: none; }
  .header-state #live { display: none; }
  .section-title { min-height: auto; align-items: stretch; flex-direction: column; gap: 16px; }
  .filter { width: 100%; justify-content: space-between; }
  .filter select { flex: 1; }
  .tabs { gap: 12px; padding-right: 16px; padding-left: 16px; }
  .metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
	.agent-now-grid { grid-template-columns: 1fr; }
  .metric:first-child { grid-column: 1 / -1; }
  .market { padding: 18px; }
	.balance-strip { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .market-head { min-height: 84px; }
  .market-status { align-items: flex-end; flex-direction: column-reverse; }
  .chart-switch { position: static; width: max-content; margin: 0 0 6px auto; }
  .chart { padding-top: 16px; }
  .chart-canvas, .chart-empty { height: 320px; }
  .chart-readout { flex-wrap: wrap; }
  .chart-readout time { width: 100%; margin-left: 0; }
	.qualification-strip { grid-template-columns: auto 1fr 1fr; gap: 12px; }
	.qualification-strip > span:nth-child(4) { grid-column: 2 / -1; }
	.qualification-strip > .badge { grid-column: 3; grid-row: 1; justify-self: end; }
  .automation-grid, .system-list, .market-switcher { padding: 16px; }
  .activity-item { grid-template-columns: minmax(0, 1fr) auto; gap: 8px 14px; }
  .activity-copy { grid-column: 1 / -1; padding-left: 46px; }
  .activity-result { grid-column: 1 / -1; padding-left: 46px; }
  .activity-time { grid-column: 2; grid-row: 1; }
  .strategy-brief { grid-template-columns: 1fr; }
  .perps-research-card .attempt-grid { grid-template-columns: 1fr; }
  .strategy-flow { gap: 6px; }
  .strategy-list-head, .automation-list-head { display: none; }
  .strategy-market-row { grid-template-columns: minmax(0, 1fr) auto; gap: 10px 14px; }
  .strategy-next { grid-column: 1 / -1; padding-left: 46px; }
  .strategy-plan { grid-column: 2; grid-row: 1; }
  .help-dialog-content { grid-template-columns: 150px minmax(0, 1fr); }
  .plan-loop, .plan-snapshot { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .plan-more .limit-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .limit-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .automation-card { grid-template-columns: minmax(0, 1fr) auto; gap: 10px 14px; }
  .automation-card p { grid-column: 1 / -1; padding-left: 46px; }
  .automation-card > .badge { grid-column: 2; grid-row: 1; }
  .system-row { grid-template-columns: minmax(0, 1fr) auto; gap: 7px 14px; }
  .system-row .description { grid-column: 1 / -1; }
  .system-row .badge { grid-column: 2; grid-row: 1; }
  .active-limit { grid-template-columns: 1fr; gap: 5px; }
}

@media (max-width: 639px) {
  .brand .eyebrow { display: none; }
  .brand h1 { font-size: .9rem; }
  .checked { max-width: 190px; overflow: hidden; text-overflow: ellipsis; }
  .section-title h2 { font-size: 1.35rem; }
  .metrics { grid-template-columns: 1fr; }
  .metric:first-child { grid-column: auto; }
  .metrics { gap: 10px; }
  .market-research-card { padding: 17px; }
  .research-stats { grid-template-columns: 1fr; }
	.research-check { grid-template-columns: 1fr; }
  .research-stats > span { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 3px 10px; }
  .research-stats strong { grid-column: 2; grid-row: 1; margin-top: 0; }
  .research-stats em { grid-column: 1 / -1; }
  .metric { min-height: 126px; padding: 18px; }
  .metric:first-child .metric-value { font-size: 2.2rem; }
  .performance-title { align-items: flex-start; flex-direction: column; gap: 7px; }
  .market-status .updated { display: none; }
  .chart-tools button { min-width: 40px; min-height: 40px; }
  .strategy-flow { grid-template-columns: 1fr; }
  .strategy-plan { grid-column: 1 / -1; grid-row: auto; width: 100%; align-items: stretch; }
  .plan-trigger { min-height: 44px; justify-content: center; border-top: 1px solid var(--line); }
  .help-dialog-content { grid-template-columns: 1fr; }
  .help-dialog-visual { min-height: 96px; }
  .help-dialog.plan .help-dialog-copy p { max-width: none; }
  .plan-snapshot { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .plan-more .limit-grid { grid-template-columns: 1fr; }
  .limit-grid { grid-template-columns: 1fr; }
  .instruction-controls { grid-template-columns: 1fr; }
  .instruction-warning { grid-column: 1; }
  .detail-grid { grid-template-columns: 1fr; }
  .access { grid-template-columns: 1fr; gap: 16px; }
}

@media (max-width: 430px) {
  .topbar { padding: 0 12px; }
  .brand-logo { width: 24px; height: 28px; }
  .checked { max-width: 142px; font-size: .62rem; }
	.checked #checked { display: block; max-width: 86px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
	.agent-now { padding: 14px; }
	.agent-now-head { align-items: flex-start; flex-direction: column; gap: 10px; }
	.agent-now-card > footer { align-items: flex-start; flex-direction: column; gap: 6px; }
  .tabs { right: 10px; left: 10px; display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 4px; padding: 8px; overflow: hidden; }
  .tab { min-width: 0; min-height: 40px; grid-template-columns: minmax(0, auto); justify-content: center; padding: 0; font-size: .625rem; }
  .nav-icon { display: none; }
  main.shell { padding-right: 10px; padding-left: 10px; }
  .market { padding: 16px 13px; }
	.balance-strip { grid-template-columns: 1fr; }
  .chart-switch { max-width: 100%; }
  .chart-toggle { padding: 0 9px; font-size: .61rem; }
  .chart-canvas, .chart-empty { height: 280px; }
	.qualification-strip { grid-template-columns: auto 1fr; }
	.qualification-strip > span:nth-child(3), .qualification-strip > span:nth-child(4) { grid-column: 2; }
	.qualification-strip > .badge { grid-column: 1 / -1; grid-row: auto; justify-self: start; }
  .filter { width: 100%; justify-content: space-between; }
  .filter select { min-width: 0; flex: 1; }
  .activity-time { font-size: .62rem; }
  .help-dialog { width: calc(100% - 20px); max-height: calc(100dvh - 20px); }
  .help-dialog-head { padding: 0 16px; }
  .help-dialog-content { padding: 18px 16px; }
  .plan-loop { grid-template-columns: 1fr 1fr; gap: 8px; }
  .plan-node { min-height: 60px; padding: 10px; }
  .plan-snapshot, .plan-allocation, .plan-more { margin-right: 16px; margin-left: 16px; }
  .plan-snapshot article { min-height: 108px; padding: 14px; }
  .plan-reason { margin-right: 16px !important; margin-left: 16px !important; }
}

@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { scroll-behavior: auto !important; animation-duration: .001ms !important; animation-iteration-count: 1 !important; transition-duration: .001ms !important; }
}
`
