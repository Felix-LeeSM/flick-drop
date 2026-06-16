#!/usr/bin/env bash
set -euo pipefail

failed=0

fail() {
  echo "review-gate: $1" >&2
  failed=1
}

require_env() {
  local name="$1"

  if [ -z "${!name:-}" ]; then
    echo "review-gate: missing environment variable $name" >&2
    exit 1
  fi
}

require_env GH_TOKEN
require_env GH_REPO
require_env PR_NUMBER

pr_json="$(gh api "repos/$GH_REPO/pulls/$PR_NUMBER")"
issue_json="$(gh api "repos/$GH_REPO/issues/$PR_NUMBER")"
files="$(
  gh api --paginate "repos/$GH_REPO/pulls/$PR_NUMBER/files?per_page=100" \
    --jq '.[].filename'
)"
labels="$(printf '%s' "$issue_json" | jq -r '.labels[].name')"
body="$(printf '%s' "$pr_json" | jq -r '.body // ""')"

has_label() {
  local wanted="$1"

  printf '%s\n' "$labels" | grep -Fxq "$wanted"
}

count_prefix() {
  local prefix="$1"

  printf '%s\n' "$labels" | awk -v prefix="$prefix" 'index($0, prefix) == 1 { count++ } END { print count + 0 }'
}

require_label() {
  local label="$1"
  local reason="$2"

  if ! has_label "$label"; then
    fail "missing label $label ($reason)"
  fi
}

require_one_of_labels() {
  local reason="$1"
  shift

  local label
  for label in "$@"; do
    if has_label "$label"; then
      return 0
    fi
  done

  fail "missing one of labels: $* ($reason)"
}

if [ "$(printf '%s' "$pr_json" | jq -r '.draft')" = "true" ]; then
  fail "draft pull requests cannot pass review gate"
fi

if [ "$(printf '%s' "$pr_json" | jq -r '.base.ref')" != "main" ]; then
  fail "pull request base must be main"
fi

if [ "$(printf '%s' "$issue_json" | jq -r '.milestone.title // empty')" = "" ]; then
  fail "pull request must have a milestone"
fi

if ! printf '%s' "$body" | grep -Eiq '(close[sd]?|fix(e[sd])?|resolve[sd]?) #[0-9]+'; then
  fail "pull request body must link an issue with Closes/Fixes/Resolves #number"
fi

for heading in \
  "## Summary" \
  "## Linked Issue" \
  "## Acceptance Criteria" \
  "## Test Plan" \
  "## Review Notes"; do
  if ! printf '%s' "$body" | grep -Fq "$heading"; then
    fail "pull request body is missing section: $heading"
  fi
done

if printf '%s' "$body" | grep -Eq '^- \[ \]'; then
  fail "pull request body contains unchecked checklist items"
fi

if [ "$(count_prefix "type:")" -ne 1 ]; then
  fail "pull request must have exactly one type:* label"
fi

if [ "$(count_prefix "risk:")" -ne 1 ]; then
  fail "pull request must have exactly one risk:* label"
fi

if [ "$(count_prefix "area:")" -lt 1 ]; then
  fail "pull request must have at least one area:* label"
fi

if has_label "review:changes-requested"; then
  fail "review:changes-requested blocks merge"
fi

if ! has_label "review:approved"; then
  fail "review:approved label is required"
else
  head_sha="$(printf '%s' "$pr_json" | jq -r '.head.sha')"
  head_commit_date="$(gh api "repos/$GH_REPO/commits/$head_sha" --jq '.commit.committer.date')"
  approved_at="$(
    gh api --paginate "repos/$GH_REPO/issues/$PR_NUMBER/events?per_page=100" \
      --jq '.[] | select(.event == "labeled" and .label.name == "review:approved") | .created_at' |
      tail -n 1
  )"

  if [ -z "$approved_at" ]; then
    fail "review:approved label event was not found"
  elif [[ "$approved_at" < "$head_commit_date" ]]; then
    fail "review:approved label must be applied after the latest commit"
  fi
fi

head_sha="$(printf '%s' "$pr_json" | jq -r '.head.sha')"
head_commit_date="$(gh api "repos/$GH_REPO/commits/$head_sha" --jq '.commit.committer.date')"
subagent_review_at="$(
  gh api --paginate "repos/$GH_REPO/issues/$PR_NUMBER/comments?per_page=100" \
    --jq '.[] | select((.body // "") | contains("## BurnLink Subagent Review")) | select((.body // "") | contains("Decision: approve")) | .created_at' |
    tail -n 1
)"

if [ -z "$subagent_review_at" ]; then
  fail "a PR comment containing '## BurnLink Subagent Review' and 'Decision: approve' is required"
elif [[ "$subagent_review_at" < "$head_commit_date" ]]; then
  fail "subagent review comment must be created after the latest commit"
fi

requires_medium_or_high_risk=0
while IFS= read -r file; do
  case "$file" in
    .github/*)
      require_label "area:github" "$file changed"
      ;;
  esac

  case "$file" in
    .github/workflows/* | scripts/ci/*)
      require_label "area:ci" "$file changed"
      requires_medium_or_high_risk=1
      ;;
    web/*)
      require_label "area:web" "$file changed"
      ;;
    cmd/burnlink-api/* | internal/httpapi/*)
      require_label "area:api" "$file changed"
      ;;
    cmd/burnlink-worker/* | internal/worker/*)
      require_label "area:worker" "$file changed"
      ;;
    internal/storage/*)
      require_label "area:storage" "$file changed"
      requires_medium_or_high_risk=1
      ;;
    contracts/*)
      require_label "area:contracts" "$file changed"
      requires_medium_or_high_risk=1
      ;;
    deploy/*)
      require_label "area:deploy" "$file changed"
      requires_medium_or_high_risk=1
      ;;
    docs/*)
      require_label "area:docs" "$file changed"
      ;;
  esac

  case "$file" in
    docs/architecture/security-model.md | internal/secrets/* | web/src/lib/crypto/*)
      require_label "area:security" "$file changed"
      requires_medium_or_high_risk=1
      ;;
  esac
done <<EOF
$files
EOF

if [ "$requires_medium_or_high_risk" -eq 1 ]; then
  require_one_of_labels "sensitive path changed" "risk:medium" "risk:high"
fi

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "review-gate: ok"
