#!/usr/bin/env bash
# Linux benchmarks and CPU/mutex profiles for loggerj, run in Docker.
#
#   ./profile-linux.sh build            build the image
#   ./profile-linux.sh bench            benchmarks only  -> profiles/linux/bench.txt
#   ./profile-linux.sh profile          CPU + mutex profiles of the E2E benchmarks
#   ./profile-linux.sh all              bench + profile (default)
#   ./profile-linux.sh web NAME         pprof web UI for a profile, e.g. e2e_nofields
#   ./profile-linux.sh shell            interactive shell in the container
#
# Environment (all optional):
#   COUNT=6        benchmark repetitions for `bench`
#   BENCHTIME=5s   run time of each profiled benchmark
#   CPUS=4         limit the container to N CPUs (default: all CPUs of the Docker host/VM)
#   OUT_DIR=profiles/linux
#   PORT=8080      host port for `web`
#   IMAGE=loggerj-profile
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
REPO_DIR="$(pwd)"
IMAGE="${IMAGE:-loggerj-profile}"
OUT_DIR="${OUT_DIR:-profiles/linux}"
COUNT="${COUNT:-6}"
BENCHTIME="${BENCHTIME:-5s}"
CPUS="${CPUS:-}"
PORT="${PORT:-8080}"

cmd="${1:-all}"

docker_run() {
	local args=(
		--rm
		-v "$REPO_DIR":/src
		-v loggerj-gocache:/root/.cache/go-build
		-v loggerj-gomod:/go/pkg/mod
		-e OUT_DIR="$OUT_DIR"
		-e COUNT="$COUNT"
		-e BENCHTIME="$BENCHTIME"
	)
	if [[ -n "$CPUS" ]]; then
		args+=(--cpus "$CPUS")
	fi
	if [[ -n "${GOMAXPROCS:-}" ]]; then
		args+=(-e GOMAXPROCS="$GOMAXPROCS")
	fi
	docker run "${args[@]}" "$@"
}

ensure_image() {
	if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
		docker build -f Dockerfile.profile -t "$IMAGE" .
	fi
}

case "$cmd" in
build)
	docker build -f Dockerfile.profile -t "$IMAGE" .
	;;
bench | profile | all)
	ensure_image
	docker_run "$IMAGE" bash /src/profile-inner.sh "$cmd"
	;;
web)
	name="${2:?usage: $0 web NAME (e.g. e2e_nofields or e2e_typed_json)}"
	ensure_image
	echo "Open http://localhost:${PORT}  (Ctrl-C to stop)"
	docker_run -p "${PORT}:8080" "$IMAGE" \
		go tool pprof -http=0.0.0.0:8080 -no_browser \
		"/src/${OUT_DIR}/loggerj.test" "/src/${OUT_DIR}/cpu_${name}.out"
	;;
shell)
	ensure_image
	docker_run -it "$IMAGE" bash
	;;
*)
	echo "unknown command: $cmd" >&2
	sed -n '2,13p' "$0" >&2
	exit 2
	;;
esac
