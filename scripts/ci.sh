#!/usr/bin/env bash
# Local CI: formatting, vet, race tests, vulnerability scan, and (with --full) the container image,
# a trivy scan of it and the podman end-to-end suite.
#
#   scripts/ci.sh           quick: gofmt, vet, go test -race, govulncheck, Go patch-level check
#   scripts/ci.sh --full    also: podman build, trivy image scan, tests/e2e/run.sh (needs network and podman)
#   scripts/ci.sh --fuzz    also: 20 s of every fuzzer
#
# Exit status is non-zero when any step fails. Nothing is piped, so a failing command is never masked.
set -euo pipefail

cd "$(dirname "$0")/.."

full=0 fuzz=0
for a in "$@"; do
	case "$a" in
	--full) full=1 ;;
	--fuzz) fuzz=1 ;;
	-h | --help)
		sed -n '2,10p' "$0"
		exit 0
		;;
	*)
		echo "unknown option: $a" >&2
		exit 2
		;;
	esac
done

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
failed=()
run() { # run NAME CMD... : keep going after a failure, report at the end
	local name=$1
	shift
	step "$name"
	if "$@"; then echo "ok: $name"; else
		echo "FAILED: $name" >&2
		failed+=("$name")
	fi
}

gofmt_check() {
	local out
	out=$(gofmt -l .)
	if [ -n "$out" ]; then
		echo "not gofmt-formatted:" >&2
		echo "$out" >&2
		return 1
	fi
}

govulncheck_bin() {
	if command -v govulncheck >/dev/null; then command -v govulncheck; else echo "$(go env GOPATH)/bin/govulncheck"; fi
}

vulncheck() {
	local bin
	bin=$(govulncheck_bin)
	if [ ! -x "$bin" ]; then
		echo "govulncheck is missing: go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
		return 1
	fi
	"$bin" ./...
}

# Warn (not fail) when a newer patch release of the pinned Go minor version exists.
go_patch_level() {
	local want latest
	want=$(go env GOVERSION | sed 's/^go//; s/-.*//')
	latest=$(curl -fsS --max-time 10 'https://go.dev/dl/?mode=json' |
		python3 -I -c "
import json, sys
minor = '.'.join('$want'.split('.')[:2])
v = [r['version'][2:] for r in json.load(sys.stdin) if r['version'][2:].startswith(minor + '.')]
print(v[0] if v else '')
") || {
		echo "warning: could not reach go.dev, patch level not checked" >&2
		return 0
	}
	if [ -n "$latest" ] && [ "$latest" != "$want" ]; then
		echo "warning: Go $want is in use, but $latest is released: update go.mod and the Containerfile" >&2
	else
		echo "Go $want is the latest patch of its minor version"
	fi
}

fuzzers() {
	local spec pkg name
	for spec in \
		"./internal/upstream FuzzParseProgram" \
		"./internal/images FuzzSanitize" \
		"./internal/store FuzzSearchTerm"; do
		read -r pkg name <<<"$spec"
		go test -run '^$' -fuzz="^${name}\$" -fuzztime=20s "$pkg" || return 1
	done
}

trivy_image() {
	local dir cache
	dir=$(mktemp -d)
	cache="${XDG_CACHE_HOME:-$HOME/.cache}/trivy"
	mkdir -p "$cache"
	podman save -o "$dir/m3.tar" localhost/m3:dev
	# Fail on anything HIGH or CRITICAL that has a fix.
	podman run --rm -v "$dir":/scan:Z -v "$cache":/root/.cache:Z docker.io/aquasec/trivy:latest \
		image --input /scan/m3.tar --scanners vuln,secret,misconfig \
		--severity HIGH,CRITICAL --ignore-unfixed --exit-code 1
	local rc=$?
	rm -rf "$dir"
	return $rc
}

run "gofmt" gofmt_check
run "go vet" go vet ./...
run "go test -race" go test -race -count=1 ./...
run "govulncheck" vulncheck
run "Go patch level" go_patch_level

if [ "$fuzz" = 1 ]; then
	run "fuzzers (20 s each)" fuzzers
fi

if [ "$full" = 1 ]; then
	run "podman build (m3)" podman build --target m3 -t localhost/m3:dev .
	run "podman build (e2e-proxy)" podman build --target e2e-proxy -t localhost/m3-e2e-proxy:dev .
	run "trivy image scan" trivy_image
	run "e2e (podman, real upstream)" tests/e2e/run.sh
fi

step "summary"
if [ ${#failed[@]} -gt 0 ]; then
	printf 'FAILED: %s\n' "${failed[@]}" >&2
	exit 1
fi
echo "all steps passed"
