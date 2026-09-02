#!/bin/sh
set -eu

[ "$(/usr/bin/id -u)" -eq 0 ] || {
  echo "paper instruction activation must run as root" >&2
  exit 1
}

agent=/usr/local/libexec/mithril-agent/mithril-agent
dashboard=/usr/local/libexec/mithril-agent/mithril-agent-paper-dashboard
source=/var/lib/mithril-agent-dashboard/instruction.json
runtime=/run/mithril-agent-paper-instruction
instruction="$runtime/instruction.json"
receipt="$runtime/allocation.json"
allocations=/var/lib/mithril-agent-research/allocations
selector=/etc/mithril-agent/paper-active
target=mithril-agent-paper-generation.target
legacy=/var/lib/mithril-agent-research/policy/portfolio.json
old=
next=
temporary="$selector.next.$$"
stopped=false
switched=false
complete=false

generation_is_active() {
  generation=$1
  /usr/bin/systemctl is-active --quiet "$target" || return 1
  for generation_market in sol jup; do
    [ -f "$generation/$generation_market-policy.json" ] || continue
    case "$generation_market" in
    sol)
      generation_base=mithril-agent-paper-base.service
      generation_pre=mithril-agent-paper-pre-champion.service
      generation_champion=mithril-agent-paper-champion.service
      ;;
    jup)
      generation_base=mithril-agent-paper-jup.service
      generation_pre=mithril-agent-paper-jup-pre-champion.service
      generation_champion=mithril-agent-paper-jup-champion.service
      ;;
    esac
    /usr/bin/systemctl is-active --quiet "$generation_base" || return 1
    if [ -f "$generation/selection/$generation_market/champion/active.json" ]; then
      /usr/bin/systemctl is-active --quiet "$generation_champion" || return 1
    else
      /usr/bin/systemctl is-active --quiet "$generation_pre" || return 1
    fi
  done
}

start_generation() {
  generation=$1
  /usr/bin/systemctl start "$target" || true
  for generation_market in sol jup; do
    [ -f "$generation/$generation_market-policy.json" ] || continue
    case "$generation_market" in
    sol)
      generation_base=mithril-agent-paper-base.service
      generation_pre=mithril-agent-paper-pre-champion.service
      generation_champion=mithril-agent-paper-champion.service
      ;;
    jup)
      generation_base=mithril-agent-paper-jup.service
      generation_pre=mithril-agent-paper-jup-pre-champion.service
      generation_champion=mithril-agent-paper-jup-champion.service
      ;;
    esac
    /usr/bin/systemctl start "$generation_base" || true
    if [ -f "$generation/selection/$generation_market/champion/active.json" ]; then
      /usr/bin/systemctl start "$generation_champion" || true
    else
      /usr/bin/systemctl start "$generation_pre" || true
    fi
  done
}

wait_for_generation() {
  generation_wait=0
  generation_stable=0
  while [ "$generation_wait" -lt 160 ]; do
    if generation_is_active "$1"; then
      generation_stable=$((generation_stable + 1))
      [ "$generation_stable" -lt 9 ] || return 0
    else
      generation_stable=0
    fi
    generation_wait=$((generation_wait + 1))
    /usr/bin/sleep 0.25
  done
  return 1
}

restore_selector() {
  if [ -n "$old" ]; then
    /usr/bin/ln -s -- "$old" "$temporary"
    /usr/bin/chown -h root:root "$temporary"
    /usr/bin/mv -Tf -- "$temporary" "$selector"
  else
    /usr/bin/rm -f -- "$selector"
  fi
}

cleanup() {
  status=$?
  if [ "$complete" != true ]; then
    if [ "$switched" = true ]; then
      /usr/bin/systemctl stop "$target" || true
      restore_selector || true
    fi
    if [ "$stopped" = true ] && [ -n "$old" ]; then
      start_generation "$old" || true
    fi
    if [ -n "$next" ] && [ -d "$next" ] && [ ! -L "$next" ] &&
      [ "$(/usr/bin/dirname -- "$next")" = "$allocations" ]; then
      /usr/bin/find "$next" -xdev -depth -delete
    fi
  fi
  /usr/bin/rm -f -- "$temporary"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

/usr/bin/install -d -o root -g root -m 0711 /etc/mithril-agent
[ -d /etc/mithril-agent ] && [ ! -L /etc/mithril-agent ] &&
  [ "$(/usr/bin/stat -c %u /etc/mithril-agent)" -eq 0 ] &&
  [ "$((0$(/usr/bin/stat -c %a /etc/mithril-agent) & 0022))" -eq 0 ] || {
  echo "/etc/mithril-agent must be a root-owned real directory" >&2
  exit 1
}
[ -e "$allocations" ] || /usr/bin/install -d -o root -g root -m 0755 "$allocations"
[ -d "$allocations" ] && [ ! -L "$allocations" ] &&
  [ "$(/usr/bin/stat -c %u "$allocations")" -eq 0 ] &&
  [ "$((0$(/usr/bin/stat -c %a "$allocations") & 0022))" -eq 0 ] || {
  echo "paper allocations must be a root-owned directory" >&2
  exit 1
}
/usr/bin/install -d -o root -g mithril-agent-research -m 0710 "$runtime"
exec 8<"$runtime"
/usr/bin/flock -n 8 || {
  echo "another paper instruction activation is already running" >&2
  exit 1
}
/usr/sbin/runuser -u mithril-agent-dashboard -- "$dashboard" \
  --export-instruction "$source" >"$instruction"
/usr/bin/chown mithril-agent-research:mithril-agent-research "$instruction"
/usr/bin/chmod 0400 "$instruction"

current=$legacy
if [ -e "$selector" ] || [ -L "$selector" ]; then
  [ -L "$selector" ] || { echo "paper-active is not a symlink" >&2; exit 1; }
  old=$(/usr/bin/readlink -e -- "$selector")
  [ -d "$old" ] && [ "$(/usr/bin/dirname -- "$old")" = "$allocations" ] || {
    echo "paper-active does not name one direct allocation generation" >&2
    exit 1
  }
  current="$old/portfolio.json"
  if [ -f "$old/instruction.json" ] &&
    /usr/bin/cmp -s "$instruction" "$old/instruction.json"; then
    if ! generation_is_active "$old"; then
      stopped=true
      /usr/bin/systemctl stop "$target"
      start_generation "$old"
      wait_for_generation "$old"
      stopped=false
    fi
    complete=true
    exit 0
  fi
fi
[ -f "$current" ] || { echo "current paper portfolio is unavailable" >&2; exit 1; }

digest=$(/usr/bin/sha256sum "$instruction")
digest=${digest%% *}
next="$allocations/$digest-$(/usr/bin/date -u +%Y%m%dT%H%M%SZ)-$$"
[ ! -e "$next" ] || { echo "paper allocation generation already exists" >&2; exit 1; }
/usr/bin/install -d -o mithril-agent-research -g mithril-agent-research -m 0700 "$next"

/usr/sbin/runuser -u mithril-agent-research -- "$agent" shadow allocation \
  --portfolio "$current" --instruction "$instruction" --out-dir "$next" >"$receipt"
[ ! -f "$next/sol-policy.json" ] || /usr/bin/ln -- "$next/sol-policy.json" "$next/policy.json"
/usr/bin/install -o mithril-agent-research -g mithril-agent-research -m 0600 \
  "$instruction" "$next/instruction.json"
for file in "$next"/*-policy.json "$next/portfolio.json"; do
  [ -f "$file" ] || { echo "paper allocation output is incomplete" >&2; exit 1; }
  /usr/bin/chown mithril-agent-research:mithril-agent-research "$file"
  /usr/bin/chmod 0600 "$file"
done
/usr/bin/chown root:mithril-agent-research "$next"
/usr/bin/chmod 0750 "$next"
for market in sol jup; do
  for role in base pre-champion champion challenger; do
    /usr/bin/install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
      "$next/runs/$market/$role"
  done
  /usr/bin/install -d -o mithril-agent-research -g mithril-agent-research -m 0700 \
    "$next/selection/$market/champion" "$next/selection/$market/challenger" \
    "$next/status/$market"
	if [ ! -f "$next/$market-policy.json" ]; then
		stamp=$(/usr/bin/date -u +%Y-%m-%dT%H:%M:%SZ)
		placeholder="$runtime/$market-not-enabled.json"
		/usr/bin/printf '{"version":4,"observed_at":"%s","dropped_events":0,"events":[],"current":"PAPER · NOT ENABLED"}\n' \
		  "$stamp" >"$placeholder"
		/usr/bin/install -o mithril-agent-research -g mithril-agent-research -m 0600 \
		  "$placeholder" "$next/status/$market/alerts.json"
	fi
done

stopped=true
/usr/bin/systemctl stop "$target"
exec 9<"$allocations"
/usr/bin/flock -x 9
/usr/bin/ln -s -- "$next" "$temporary"
/usr/bin/chown -h root:root "$temporary"
switched=true
/usr/bin/mv -Tf -- "$temporary" "$selector"
start_generation "$next"
wait_for_generation "$next"
complete=true
