#!/bin/sh
# Checks that OpenCode's published plugin API still has what
# internal/hooks/opencode.js uses. Run it before an OpenCode upgrade, or when
# the plugin stops reporting events:
#   scripts/check-opencode-api.sh [VERSION]   (default: latest)
set -eu

version=${1:-latest}
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
(cd "$dir" && npm pack --silent "@opencode/plugin@$version" >/dev/null && tar xzf ./*.tgz)
api=$dir/package/dist/promise
echo "@opencode/plugin $(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$dir/package/package.json")"

fail=0
need() { # need FILE PATTERN WHAT
  if ! grep -Eq "$2" "$api/$1"; then
    echo "missing: $3 ($1)"
    fail=1
  fi
}
for h in prompt context compaction '"experimental.idle"'; do
  need session.d.ts "readonly $h: Session" "session hook $h"
done
need session.d.ts 'readonly messageID: SessionMessage.ID' 'prompt event messageID'
need session.d.ts 'prompt: Types.DeepMutable<PromptInput.Prompt>' 'prompt event prompt'
need session.d.ts 'system: Array<SystemPart>' 'request system parts'
need session.d.ts 'messages: Array<Message>' 'request messages'
need session.d.ts '"get"' 'session.get'
need session.d.ts '"context"' 'session.context'
need session.d.ts 'readonly hook: ModelHooks<SessionHooks>' 'session.hook'
need plugin.d.ts 'readonly location: Location.Info' 'ctx.location'
need plugin.d.ts 'readonly session: SessionDomain' 'ctx.session'
[ "$fail" = 0 ] && echo "ok: the plugin's hooks and fields are all there"
exit "$fail"
