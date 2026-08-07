# Benchmark Artifacts

This directory is intended for local benchmark captures.

Do not commit ad-hoc benchmark outputs unless they are part of a release
baseline or a documented comparison.

## Generate local baseline

```bash
make bench-base
```

This writes `bench/base.txt`.

## Run protected hot-path benchmarks

```bash
make bench-hot
```

The protected benchmark list is stored in `hotpath.txt`.

## Rules

- Benchmark numbers in README/BENCH.md must come from a reproducible
  environment, preferably CI or a dedicated benchmark host.
- Laptop numbers may be used for exploration but must not be presented as
  release-level evidence unless the methodology is documented.
