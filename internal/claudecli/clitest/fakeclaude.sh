#!/bin/sh
# Fake `claude` for tests. Chosen by FAKE_CLAUDE_MODE; records argv and stdin.
here=$(cd "$(dirname "$0")" && pwd)
[ -n "$FAKE_CLAUDE_ARGS_FILE" ] && printf '%s\n' "$@" > "$FAKE_CLAUDE_ARGS_FILE"
[ -n "$FAKE_CLAUDE_STDIN_FILE" ] && cat > "$FAKE_CLAUDE_STDIN_FILE"
case "$FAKE_CLAUDE_MODE" in
  fixture)    cat "$FAKE_CLAUDE_FIXTURE" ;;
  success)    cat "$here/stream_success.jsonl" ;;
  error)      cat "$here/stream_error.jsonl"; exit 1 ;;
  needsinput) cat "$here/stream_needsinput.jsonl" ;;
  hang)       head -1 "$here/stream_success.jsonl"; sleep 60 ;;
  manysteps)
    head -1 "$here/stream_success.jsonl"
    i=0
    while [ $i -lt 50 ]; do
      printf '{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"t%s","name":"Read","input":{"file_path":"/x"}}]}}\n' "$i"
      i=$((i+1))
      sleep 0.02
    done
    sleep 60 ;;
  plan)       printf '{"type":"result","subtype":"success","is_error":false,"result":"{\\"files\\":[\\"a.go\\"]}","structured_output":{"files":["a.go","b.go"]},"session_id":"p"}\n' ;;
  crash)      echo "segfault" >&2; exit 139 ;;
  *)          echo "unknown FAKE_CLAUDE_MODE=$FAKE_CLAUDE_MODE" >&2; exit 2 ;;
esac
