#!/usr/bin/env bash
# Runs inside the container (see profile-linux.sh) or on a GitHub runner (see
# .github/workflows/linux-profile.yml). Builds the test binary once,
# then runs the benchmarks and/or the CPU and mutex profiles from it.
set -euo pipefail

# SRC_DIR defaults to the Docker mount point; the GitHub workflow sets it to
# the checked-out workspace.
SRC="${SRC_DIR:-/src}"
cd "$SRC"
OUT="$SRC/${OUT_DIR:-profiles/linux}"
COUNT="${COUNT:-6}"
BENCHTIME="${BENCHTIME:-5s}"
MODE="${1:-all}"

mkdir -p "$OUT"

env_info() {
	{
		go version
		uname -a
		echo "nproc=$(nproc)"
		echo "GOMAXPROCS=${GOMAXPROCS:-default}"
		grep -m1 'model name' /proc/cpuinfo || true
		echo "cgroup cpu.max: $(cat /sys/fs/cgroup/cpu.max 2>/dev/null || echo n/a)"
		date -u +%Y-%m-%dT%H:%M:%SZ
	} | tee "$OUT/env.txt"
}

build_test_binary() {
	go test -c -o "$OUT/loggerj.test" .
}

run_bench() {
	"$OUT/loggerj.test" \
		-test.run='^$' \
		-test.bench='Enqueue_|Log_Filtered$|Log_RateLimited$|MonoMillis|TimeNowUnixMilli|E2E_' \
		-test.benchmem \
		-test.count="$COUNT" | tee "$OUT/bench.txt"
}

# profile_one NAME BENCH_REGEX
profile_one() {
	local name="$1" pattern="$2"
	local bin="$OUT/loggerj.test"

	"$bin" \
		-test.run='^$' \
		-test.bench="$pattern" \
		-test.benchtime="$BENCHTIME" \
		-test.benchmem \
		-test.cpuprofile="$OUT/cpu_${name}.out" \
		-test.mutexprofile="$OUT/mutex_${name}.out" | tee "$OUT/profile_${name}_bench.txt"

	go tool pprof -top -cum -nodecount=40 "$bin" "$OUT/cpu_${name}.out" >"$OUT/cpu_${name}_top_cum.txt" 2>&1 || true
	go tool pprof -top -nodecount=40 "$bin" "$OUT/cpu_${name}.out" >"$OUT/cpu_${name}_top_flat.txt" 2>&1 || true
	go tool pprof -top -cum -nodecount=40 -focus='runWorker|StartWithWriter' "$bin" "$OUT/cpu_${name}.out" >"$OUT/cpu_${name}_worker.txt" 2>&1 || true
	go tool pprof -top -cum -nodecount=40 -focus='benchmarkE2E' "$bin" "$OUT/cpu_${name}.out" >"$OUT/cpu_${name}_producer.txt" 2>&1 || true
	go tool pprof -top -nodecount=30 "$bin" "$OUT/mutex_${name}.out" >"$OUT/mutex_${name}_top.txt" 2>&1 || true
}

run_profiles() {
	profile_one e2e_nofields 'E2E_NoFields_Text$'
	profile_one e2e_typed_json 'E2E_TypedFields_JSON$'
}

case "$MODE" in
bench)
	env_info
	build_test_binary
	run_bench
	;;
profile)
	env_info
	build_test_binary
	run_profiles
	;;
all)
	env_info
	build_test_binary
	run_bench
	run_profiles
	;;
*)
	echo "unknown mode: $MODE (bench | profile | all)" >&2
	exit 2
	;;
esac

echo
echo "Results are in ${OUT_DIR:-profiles/linux}/"
