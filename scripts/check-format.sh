#!/usr/bin/env bash
set -euo pipefail
git rev-parse --show-toplevel >/dev/null
failed=0
while IFS= read -r -d '' file; do
  [[ -e "$file" ]] || continue
  if diff=$(gofmt -d "$file"); then
    :
  else
    printf 'gofmt failed for: %s\n' "$file" >&2
    failed=1
  fi
  if [[ -n "$diff" ]]; then
    printf 'Unformatted Go file: %s\n%s\n' "$file" "$diff" >&2
    failed=1
  fi
done < <(git ls-files --cached --others --exclude-standard --deduplicate -z -- '*.go')
exit "$failed"
