#!/bin/sh
set -eu

root=/var/lib/mithril-agent-research
policy="$root/policy/policy.json"
champion="$root/champion"
pointer="$champion/active.json"
lock="$root/challenger/lifecycle.lock"

[ ! -e "$pointer" ] || exit 0

validation_day=$(/bin/date -u -d 'yesterday' +%F)
train_day=$(/bin/date -u -d '2 days ago' +%F)
candidate="$champion/initial-$train_day-$validation_day.json"

[ -f "$root/journals/shadow-$train_day.jsonl" ] || exit 0
[ -f "$root/journals/shadow-$validation_day.jsonl" ] || exit 0

if [ ! -e "$candidate" ]; then
  /usr/local/libexec/mithril-agent/mithril-agent shadow search \
    --policy "$policy" \
    --dir "$root/journals" \
    --train-day "$train_day" \
    --validation-day "$validation_day" \
    --candidate-out "$candidate"
fi

/usr/local/libexec/mithril-agent/mithril-agent shadow select \
  --policy "$policy" \
  --candidate "$candidate" \
  --pointer "$pointer" \
  --lifecycle-lock "$lock" \
  --initial
