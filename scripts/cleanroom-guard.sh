#!/usr/bin/env bash
set -euo pipefail

# Clean-Room & Secret Leak Guard for Caliper APM.
# Run manually (`./scripts/cleanroom-guard.sh`), via `.git/hooks/pre-commit`, and in CI.
# Put personal/private denylist patterns in gitignored `.cleanroom.denylist.local`.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

FAIL=0

# Collect target files: tracked + untracked non-ignored files if git repo, else find.
FILES=()
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  while IFS= read -r f; do
    [[ -f "$f" ]] && FILES+=("$f")
  done < <(git ls-files --cached --others --exclude-standard)
else
  while IFS= read -r f; do
    FILES+=("${f#./}")
  done < <(find . -type f -not -path './.git/*')
fi

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "[cleanroom-guard] OK (no files to scan)"
  exit 0
fi

scan_regex() {
  local label="$1"
  local pattern="$2"
  shift 2
  local matches=""
  matches="$(grep -InE -- "$pattern" "$@" 2>/dev/null || true)"
  if [[ -n "$matches" ]]; then
    echo "[cleanroom-guard] BLOCKED ($label):"
    echo "$matches"
    FAIL=1
  fi
}

# Filter out this guard script itself and example files from self-matching patterns.
SCAN_FILES=()
for f in "${FILES[@]}"; do
  case "$f" in
    scripts/cleanroom-guard.sh|.cleanroom.denylist.example) continue ;;
    *) SCAN_FILES+=("$f") ;;
  esac
done

if [[ ${#SCAN_FILES[@]} -gt 0 ]]; then
  # 1. Universal secret & credential patterns
  scan_regex "private-key" "-----BEGIN (RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----" "${SCAN_FILES[@]}"
  scan_regex "google-api-key" "AIza[0-9A-Za-z_-]{35}" "${SCAN_FILES[@]}"
  scan_regex "gcp-service-account" '"type"[[:space:]]*:[[:space:]]*"service_account"' "${SCAN_FILES[@]}"
  scan_regex "aws-access-key" "AKIA[0-9A-Z]{16}" "${SCAN_FILES[@]}"
  scan_regex "feishu-webhook-secret" "https://open\.feishu\.cn/open-apis/bot/v2/hook/[a-zA-Z0-9-]{20,}" "${SCAN_FILES[@]}"
  scan_regex "slack-webhook-secret" "https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]+" "${SCAN_FILES[@]}"

  # 2. Personal workstation absolute paths & hardcoded private LAN IPs
  scan_regex "personal-home-path" "(/Users/[A-Za-z0-9._-]+/|/home/[A-Za-z0-9._-]+/|C:\\\\Users\\\\[A-Za-z0-9._-]+\\\\)" "${SCAN_FILES[@]}"
  scan_regex "hardcoded-lan-ip" "https?://(10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[0-1])\.[0-9]{1,3}\.[0-9]{1,3})" "${SCAN_FILES[@]}"

  # 3. Optional local gitignored denylist for private/employer keywords
  if [[ -f ".cleanroom.denylist.local" ]]; then
    while IFS= read -r raw || [[ -n "$raw" ]]; do
      line="${raw%%#*}"
      line="$(echo "$line" | xargs || true)"
      [[ -z "$line" ]] && continue
      scan_regex "local-denylist" "$line" "${SCAN_FILES[@]}"
    done < ".cleanroom.denylist.local"
  fi
fi

if [[ "$FAIL" -ne 0 ]]; then
  echo "[cleanroom-guard] FAILED: remove flagged patterns before committing."
  exit 1
fi

echo "[cleanroom-guard] OK (${#SCAN_FILES[@]} files scanned)"
