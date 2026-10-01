#!/bin/sh
# A stand-in for `deepcode -x`: records its argv, writes a session the way
# DeepCode does under $HOME/.deepcode/projects/<code>/, and exits by mode.
printf '%s\n' "$@" > "$FAKE_DEEPCODE_ARGS_FILE"
id=${FAKE_DEEPCODE_SESSION:-11111111-2222-4333-8444-555555555555}
prev=""
while [ $# -gt 0 ]; do
  case "$1" in -r) id=$2; shift ;; esac
  shift
done
dir="$HOME/.deepcode/projects/fake-$(basename "$PWD")"
mkdir -p "$dir"
f="$dir/$id.jsonl"
sys() { printf '{"sessionId":"%s","role":"system","content":"# Local Workspace Environment\\n\\n```json\\n{\\n  \\"root path\\": \\"%s\\"\\n}\\n```","createTime":"2026-10-01T07:00:00.000Z"}\n' "$id" "$PWD" >> "$f"; }
tool() { # tool <n> <name> <args-json-escaped>
  printf '{"sessionId":"%s","role":"assistant","content":"","messageParams":{"tool_calls":[{"id":"c%s","type":"function","function":{"name":"%s","arguments":"%s"}}]},"createTime":"2026-10-01T07:00:0%s.000Z"}\n' "$id" "$1" "$2" "$3" "$1" >> "$f"
  printf '{"sessionId":"%s","role":"tool","content":"{\\"ok\\": true, \\"name\\": \\"%s\\", \\"output\\": \\"done\\"}","messageParams":{"tool_call_id":"c%s"},"createTime":"2026-10-01T07:00:0%s.500Z"}\n' "$id" "$2" "$1" "$1" >> "$f"
}
index() { # index <status> <failReason or empty> <reply>
  printf '{"version":1,"entries":[{"id":"%s","status":"%s","failReason":%s,"assistantReply":"%s","usage":{"prompt_tokens":100,"completion_tokens":20},"usagePerModel":{"deepseek-flash":{}}}]}\n' "$id" "$1" "$2" "$3" > "$dir/sessions-index.json"
}
[ -f "$f" ] || sys
case "$FAKE_DEEPCODE_MODE" in
ok)
  tool 1 read "{\\\"file_path\\\": \\\"$PWD/main.go\\\"}"
  tool 2 edit "{\\\"file_path\\\": \\\"$PWD/main.go\\\"}"
  index completed null "Added the function."
  echo "Added the function."
  ;;
needsinput)
  index completed null "HIVE_NEEDS_INPUT: Which DB?"
  echo "HIVE_NEEDS_INPUT: Which DB?"
  ;;
permission)
  tool 1 bash "{\\\"command\\\": \\\"rm -rf /\\\"}"
  index ask_permission null ""
  echo "Execution requires permission confirmation, which is unavailable in --exec mode." >&2
  exit 1
  ;;
ratelimit)
  index failed '"429 Too Many Requests"' ""
  echo "Execution failed: 429 Too Many Requests" >&2
  exit 1
  ;;
nosession)
  rm -f "$f"
  echo "Execution failed: API key not found" >&2
  exit 1
  ;;
steps)
  i=1
  while [ $i -le 9 ]; do tool $i read "{}"; i=$((i+1)); done
  sleep 30
  ;;
esac
