#!/bin/sh
# Fake `codex` for tests. Chosen by FAKE_CODEX_MODE; records argv and stdin.
# FAKE_CODEX_DIR locates the fixtures when the script runs from a copy.
here=${FAKE_CODEX_DIR:-$(cd "$(dirname "$0")" && pwd)}
[ -n "$FAKE_CODEX_ARGS_FILE" ] && printf '%s\n' "$@" > "$FAKE_CODEX_ARGS_FILE"
[ -n "$FAKE_CODEX_STDIN_FILE" ] && cat > "$FAKE_CODEX_STDIN_FILE"
[ -n "$FAKE_CODEX_ENV_FILE" ] && env > "$FAKE_CODEX_ENV_FILE"
case "$FAKE_CODEX_MODE" in
  fixture)    cat "$FAKE_CODEX_FIXTURE" ;;
  success)    cat "$here/exec_success.jsonl" ;;
  error)      cat "$here/exec_error.jsonl"; exit 1 ;;
  needsinput) cat "$here/exec_needsinput.jsonl" ;;
  hang)       head -1 "$here/exec_success.jsonl"; sleep 60 ;;
  manysteps)
    head -1 "$here/exec_success.jsonl"
    i=0
    while [ $i -lt 50 ]; do
      printf '{"type":"item.started","item":{"id":"item_%s","type":"command_execution","command":"bash -lc ls","aggregated_output":"","exit_code":null,"status":"in_progress"}}\n' "$i"
      i=$((i+1))
      sleep 0.02
    done
    sleep 60 ;;
  plan)
    # --output-schema constrains the final message to the schema, so the
    # last agent_message is the JSON document itself.
    printf '{"type":"thread.started","thread_id":"p"}\n'
    printf '{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"{\\"files\\":[\\"a.go\\",\\"b.go\\"]}"}}\n'
    printf '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}\n' ;;
  crash)      echo "segfault" >&2; exit 139 ;;
  *)          echo "unknown FAKE_CODEX_MODE=$FAKE_CODEX_MODE" >&2; exit 2 ;;
esac
