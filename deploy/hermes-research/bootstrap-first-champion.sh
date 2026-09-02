#!/bin/sh
set -eu

case "$#" in
0)
  policy=/var/lib/mithril-agent-research/policy/policy.json
  journals=/var/lib/mithril-agent-research/journals
  champion=/var/lib/mithril-agent-research/champion
  challenger=/var/lib/mithril-agent-research/challenger
  instruction=
  ;;
5)
  policy=$1
  journals=$2
  champion=$3
  challenger=$4
  instruction=$5
  ;;
*)
  echo "usage: $0 [POLICY JOURNAL_DIR CHAMPION_DIR CHALLENGER_DIR INSTRUCTION]" >&2
  exit 2
  ;;
esac

pointer="$champion/active.json"
lock="$challenger/lifecycle.lock"

[ ! -e "$pointer" ] || exit 0

validation_day=$(/bin/date -u -d 'yesterday' +%F)
train_day=$(/bin/date -u -d '2 days ago' +%F)
candidate="$champion/initial-$train_day-$validation_day.json"

[ -f "$journals/shadow-$train_day.jsonl" ] || exit 0
[ -f "$journals/shadow-$validation_day.jsonl" ] || exit 0

if [ ! -e "$candidate" ]; then
  set -- /usr/local/libexec/mithril-agent/mithril-agent shadow search \
    --policy "$policy" \
    --dir "$journals" \
    --train-day "$train_day" \
    --validation-day "$validation_day" \
    --candidate-out "$candidate"
  [ -z "$instruction" ] || set -- "$@" --instruction "$instruction"
  "$@"
fi

/usr/local/libexec/mithril-agent/mithril-agent shadow select \
  --policy "$policy" \
  --candidate "$candidate" \
  --pointer "$pointer" \
  --lifecycle-lock "$lock" \
  --initial \
  --evidence-dir "$journals"
