#!/bin/sh
set -eu

allocations=/var/lib/mithril-agent-research/allocations
outcomes=/var/lib/mithril-agent-research/outcomes
selector=/etc/mithril-agent/paper-active
exec 9<"$allocations"
/usr/bin/flock -s 9
generation=$(/usr/bin/readlink -e -- "$selector")

[ -d "$generation" ] && [ "$(/usr/bin/dirname -- "$generation")" = "$allocations" ] || {
  echo "paper-active must resolve to one direct allocation generation" >&2
  exit 1
}
action=${1-}
market=${2-}
case "$market" in
sol|jup) ;;
*) echo "usage: $0 {observe|bootstrap|auto-select|status-handoff} {sol|jup} [ROLE]" >&2; exit 2 ;;
esac

policy="$generation/$market-policy.json"
portfolio="$generation/portfolio.json"
runs="$generation/runs/$market"
selection="$generation/selection/$market"
status="$generation/status/$market"

case "$action" in
observe)
  role=${3-}
  case "$role" in
  base)
    exec /usr/local/libexec/mithril-agent/mithril-agent shadow run \
      --policy "$policy" --dir "$runs/base" \
      --portfolio "$portfolio" --portfolio-book "$market"
    ;;
  pre-champion)
    exec /usr/local/libexec/mithril-agent/mithril-agent shadow run \
      --policy "$policy" --dir "$runs/pre-champion" \
      --portfolio "$portfolio" --portfolio-book "$market" \
      --alert-status "$status/alerts.json"
    ;;
  champion|challenger)
    set -- /usr/local/libexec/mithril-agent/mithril-agent shadow run \
      --policy "$policy" --dir "$runs/$role" \
      --candidate-pointer "$selection/$role/active.json" \
      --portfolio "$portfolio" --portfolio-book "$market"
    [ "$role" = champion ] && set -- "$@" --alert-status "$status/alerts.json"
    exec "$@"
    ;;
  *) echo "observe requires base, pre-champion, champion, or challenger" >&2; exit 2 ;;
  esac
  ;;
bootstrap)
	  exec /opt/mithril-hermes-research/bootstrap-first-champion.sh \
	    "$policy" "$runs/base" "$selection/champion" "$selection/challenger" \
	    "$generation/instruction.json"
  ;;
auto-select)
  [ -d "$outcomes" ] && [ ! -L "$outcomes" ] || {
    echo "paper outcome directory is unavailable" >&2
    exit 1
  }
  exec /usr/local/libexec/mithril-agent/mithril-agent shadow auto-select \
    --policy "$policy" \
    --champion-pointer "$selection/champion/active.json" \
    --challenger-pointer "$selection/challenger/active.json" \
    --champion-dir "$runs/champion" \
    --challenger-dir "$runs/challenger" --days 7 \
    --rollback-pointer "$selection/champion/previous.json" \
    --lifecycle-lock "$selection/challenger/lifecycle.lock" \
    --outcome-journal "$outcomes/$market.jsonl"
  ;;
status-handoff)
  [ ! -e "$status/champion-owned" ] || exit 0
  if [ -e "$status/alerts.json" ]; then
    /usr/bin/mv -- "$status/alerts.json" "$status/pre-champion-alerts.json"
  fi
  /usr/bin/touch -- "$status/champion-owned"
  ;;
*) echo "usage: $0 {observe|bootstrap|auto-select|status-handoff} {sol|jup} [ROLE]" >&2; exit 2 ;;
esac
