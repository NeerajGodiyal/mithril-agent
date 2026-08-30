#!/bin/sh
set -eu

toolsets='web,solana_docs'

if [ -f /var/lib/mithril-agent-research/champion/active.json ]; then
  toolsets="$toolsets,mithril_paper"
fi

if [ -f /var/lib/mithril-agent-research/index/events.jsonl ] &&
  /usr/sbin/runuser -u mithril-agent-research -- \
    /usr/local/libexec/mithril-agent/mithril-agent index doctor \
      --dir /var/lib/mithril-agent-research/index >/dev/null; then
  toolsets="$toolsets,mithril_index"
fi

export MITHRIL_HERMES_TOOLSETS="$toolsets"
cd /opt/mithril-hermes-research
exec /usr/bin/docker compose run --rm --no-TTY hermes-research
