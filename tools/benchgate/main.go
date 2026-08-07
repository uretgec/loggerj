// Command benchgate compares two Go benchmark outputs and fails if
// protected benchmarks regress beyond the configured threshold.
// make bench-base
// mv bench/base.txt bench/base-local.txt

// # Herhangi bir değişiklik sonrası:
// make bench-hot > bench/pr-local.txt 2>/dev/null || true

//	go run ./tools/benchgate \
//	  -baseline=bench/base-local.txt \
//	  -candidate=bench/pr-local.txt \
//	  -threshold=10 \
//	  -benchmarks=hotpath.txt
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type sample struct {
	ns     float64
	bytes  float64
	allocs float64
}

type aggregate struct {
	ns     float64
	bytes  float64
	allocs float64
}

const epsilon = 0.0001

func main() {
	baselinePath := flag.String("baseline", "", "baseline benchmark output file")
	candidatePath := flag.String("candidate", "", "candidate benchmark output file")
	threshold := flag.Float64("threshold", 10.0, "maximum allowed ns/op regression percentage")
	benchmarksPath := flag.String("benchmarks", "hotpath.txt", "file containing protected benchmark names")
	markdownPath := flag.String("markdown", "", "optional path to write markdown report")
	flag.Parse()

	if *baselinePath == "" || *candidatePath == "" {
		fmt.Fprintln(os.Stderr, "benchgate: baseline and candidate are required")
		os.Exit(2)
	}

	names, err := readBenchmarkList(*benchmarksPath)
	if err != nil {
		fatal(err)
	}
	if len(names) == 0 {
		fatal(fmt.Errorf("no protected benchmarks found in %s", *benchmarksPath))
	}

	baseline, err := parseBenchmarkFile(*baselinePath)
	if err != nil {
		fatal(err)
	}

	candidate, err := parseBenchmarkFile(*candidatePath)
	if err != nil {
		fatal(err)
	}

	var report strings.Builder
	report.WriteString("## Benchmark gate report\n\n")
	report.WriteString(fmt.Sprintf("Threshold: %.1f%% ns/op regression. Any allocs/op increase fails.\n\n", *threshold))
	report.WriteString("| Benchmark | base ns/op | PR ns/op | delta | base allocs/op | PR allocs/op | result |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|---|\n")

	failed := false

	for _, name := range names {
		baseSamples, baseOK := baseline[name]
		candSamples, candOK := candidate[name]

		if !baseOK || !candOK {
			failed = true

			missing := make([]string, 0, 2)
			if !baseOK {
				missing = append(missing, "baseline")
			}
			if !candOK {
				missing = append(missing, "candidate")
			}

			report.WriteString(fmt.Sprintf(
				"| `%s` | - | - | - | - | - | FAIL missing in %s |\n",
				name,
				strings.Join(missing, ", "),
			))
			continue
		}

		base := aggregateSamples(baseSamples)
		cand := aggregateSamples(candSamples)

		if base.ns <= 0 {
			failed = true
			report.WriteString(fmt.Sprintf(
				"| `%s` | %.3f | %.3f | - | %.0f | %.0f | FAIL invalid baseline ns/op |\n",
				name,
				base.ns,
				cand.ns,
				base.allocs,
				cand.allocs,
			))
			continue
		}

		deltaPct := (cand.ns/base.ns - 1.0) * 100.0

		result := "OK"

		if deltaPct > *threshold {
			result = fmt.Sprintf("FAIL >%.1f%%", *threshold)
			failed = true
		}

		if cand.allocs > base.allocs+epsilon {
			if result == "OK" {
				result = "FAIL alloc increase"
			} else {
				result += ", alloc increase"
			}
			failed = true
		}

		report.WriteString(fmt.Sprintf(
			"| `%s` | %.3f | %.3f | %+.2f%% | %.0f | %.0f | %s |\n",
			name,
			base.ns,
			cand.ns,
			deltaPct,
			base.allocs,
			cand.allocs,
			result,
		))
	}

	report.WriteString("\n")
	if failed {
		report.WriteString("Result: **FAIL**\n")
	} else {
		report.WriteString("Result: **PASS**\n")
	}

	out := report.String()
	fmt.Print(out)

	if *markdownPath != "" {
		if err := os.WriteFile(*markdownPath, []byte(out), 0o644); err != nil {
			fatal(err)
		}
	}

	if failed {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "benchgate: %v\n", err)
	os.Exit(2)
}

func readBenchmarkList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var names []string
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !seen[line] {
			seen[line] = true
			names = append(names, line)
		}
	}

	return names, scanner.Err()
}

func parseBenchmarkFile(path string) (map[string][]sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := make(map[string][]sample)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		name := stripProcsSuffix(fields[0])

		var s sample
		haveNS := false

		for i := 1; i < len(fields); i++ {
			switch fields[i] {
			case "ns/op":
				if v, err := strconv.ParseFloat(fields[i-1], 64); err == nil {
					s.ns = v
					haveNS = true
				}
			case "B/op":
				if v, err := strconv.ParseFloat(fields[i-1], 64); err == nil {
					s.bytes = v
				}
			case "allocs/op":
				if v, err := strconv.ParseFloat(fields[i-1], 64); err == nil {
					s.allocs = v
				}
			}
		}

		if haveNS {
			m[name] = append(m[name], s)
		}
	}

	return m, scanner.Err()
}

func stripProcsSuffix(name string) string {
	idx := strings.LastIndex(name, "-")
	if idx <= 0 || idx == len(name)-1 {
		return name
	}

	suffix := name[idx+1:]
	if _, err := strconv.Atoi(suffix); err == nil {
		return name[:idx]
	}

	return name
}

func aggregateSamples(samples []sample) aggregate {
	ns := make([]float64, len(samples))
	bytes := make([]float64, len(samples))
	allocs := make([]float64, len(samples))

	for i, s := range samples {
		ns[i] = s.ns
		bytes[i] = s.bytes
		allocs[i] = s.allocs
	}

	return aggregate{
		ns:     median(ns),
		bytes:  median(bytes),
		allocs: median(allocs),
	}
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	sort.Float64s(values)

	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}

	return (values[mid-1] + values[mid]) / 2.0
}
