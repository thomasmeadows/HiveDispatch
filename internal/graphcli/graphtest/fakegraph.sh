#!/bin/sh
# A stand-in for hivegraph: replays events by mode.
cat > /dev/null
case "$FAKEGRAPH_MODE" in
ok)
  echo '{"type":"step","kind":"tool","name":"Bash","input":"{}","output":"ok","is_error":false,"start":"2026-10-01T12:00:00Z","end":"2026-10-01T12:00:01Z"}'
  echo '{"type":"result","status":"completed","summary":"done","resume_token":"th-1","changed_files":[]}'
  ;;
noresult)
  echo 'boom' >&2
  exit 3
  ;;
steps)
  i=0
  while [ $i -lt 10 ]; do
    echo '{"type":"step","kind":"tool","name":"Bash","start":"2026-10-01T12:00:00Z","end":"2026-10-01T12:00:01Z"}'
    i=$((i+1))
  done
  sleep 30
  ;;
trap)
  trap 'echo "{\"type\":\"node\",\"name\":\"got-term\"}"; exit 0' TERM
  echo '{"type":"node","name":"started"}'
  while :; do sleep 0.05; done
  ;;
ignore)
  trap '' TERM
  echo '{"type":"node","name":"started"}'
  while :; do sleep 0.05; done
  ;;
esac
