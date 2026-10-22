#!/usr/bin/env bash
set -euo pipefail

# CI runs on Linux amd64. Pin both the release and its official asset digest.
if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
  printf '%s\n' 'Secret scanning requires Linux amd64.' >&2
  exit 1
fi

readonly gitleaks_version=8.30.1
readonly gitleaks_sha256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
scan_dir=$(mktemp -d)
trap 'rm -rf -- "$scan_dir"' EXIT

curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 \
  --output "$scan_dir/gitleaks.tar.gz" \
  "https://github.com/gitleaks/gitleaks/releases/download/v${gitleaks_version}/gitleaks_${gitleaks_version}_linux_x64.tar.gz"
printf '%s  %s\n' "$gitleaks_sha256" "$scan_dir/gitleaks.tar.gz" | sha256sum --check --status
tar -xzf "$scan_dir/gitleaks.tar.gz" -C "$scan_dir" gitleaks

# Scan local files as well as every fetched history ref. Always redact output;
# inline suppression comments cannot hide new findings from the release gate.
"$scan_dir/gitleaks" dir --redact --no-banner --ignore-gitleaks-allow "$repo_dir"
"$scan_dir/gitleaks" git --redact --no-banner --ignore-gitleaks-allow --log-opts=--all "$repo_dir"
