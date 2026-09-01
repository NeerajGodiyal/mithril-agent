#!/bin/sh
set -eu
umask 077

toolsets='web,solana_docs'

if [ -f /var/lib/mithril-agent-research/champion/active.json ] &&
  /usr/bin/systemctl is-active --quiet mithril-agent-paper-base.service &&
  /usr/bin/systemctl is-active --quiet mithril-agent-paper-champion.service; then
  toolsets="$toolsets,mithril_paper"
fi

if [ -f /var/lib/mithril-agent-research/index/events.jsonl ] &&
  /usr/sbin/runuser -u mithril-agent-research -- \
    /usr/local/libexec/mithril-agent/mithril-agent index doctor \
      --dir /var/lib/mithril-agent-research/index \
      --max-record-age 15m >/dev/null; then
  toolsets="$toolsets,mithril_index"
fi

export MITHRIL_HERMES_TOOLSETS="$toolsets"
base_query=/opt/mithril-hermes-research/prompts/market-scout.md
instruction=/var/lib/mithril-agent-dashboard/instruction.json
query_file=/run/mithril-hermes-research/market-scout.md
/usr/bin/cp "$base_query" "$query_file"
/usr/bin/printf '\n\nTrusted run-time anchor: %s. Use this exact value for `created_at`; do not invent or round a timestamp. Set `valid_until` no more than 12 hours after this anchor.\n' \
  "$(/usr/bin/date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$query_file"
if [ -f "$instruction" ]; then
  rendered=$(/usr/sbin/runuser -u mithril-agent-dashboard -- \
    /usr/local/libexec/mithril-agent/mithril-agent-paper-dashboard \
      --render-instruction "$instruction")
  /usr/bin/printf '%s' "$rendered" >>"$query_file"
fi
/usr/bin/chmod 0644 "$query_file"
export MITHRIL_HERMES_QUERY_FILE="$query_file"

packet=/run/mithril-hermes-research/packet.raw
dashboard_packet=/run/mithril-hermes-research/packet-dashboard.raw
latest=/var/lib/mithril-agent-research/latest-research.json
projection=/var/lib/mithril-agent-dashboard/research.json
cleanup() {
  /usr/bin/rm -f "$packet" "$dashboard_packet"
}
trap cleanup EXIT HUP INT TERM

# The model output is advisory and bounded before deterministic code accepts it.
# POSIX ulimit -f uses 512-byte blocks, matching the packet's 64 KiB ceiling.
ulimit -f 128
cd /opt/mithril-hermes-research
/usr/bin/docker compose run --rm --no-TTY hermes-research >"$packet"
/usr/bin/chmod 0600 "$packet"
/usr/bin/chown mithril-agent-research:mithril-agent-research "$packet"
/usr/sbin/runuser -u mithril-agent-research -- \
  /usr/local/libexec/mithril-agent/mithril-agent research packet-record \
    --in "$packet" \
    --archive-dir /var/lib/mithril-agent-research/reports \
    --latest "$latest"

# Validate the dashboard projection again as the unprivileged dashboard user.
# Root never follows a name from the dashboard-owned state directory.
/usr/bin/install -o mithril-agent-dashboard -g mithril-agent-dashboard -m 0600 \
  "$packet" "$dashboard_packet"
/usr/sbin/runuser -u mithril-agent-dashboard -- \
  /usr/local/libexec/mithril-agent/mithril-agent research packet-record \
    --in "$dashboard_packet" --latest "$projection"
