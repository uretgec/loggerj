# Linux benchmark and profiling image for loggerj.
#
# Build:  ./profile-linux.sh build
# The repository is mounted at /src at run time; nothing is copied into the image.
FROM golang:1.24-bookworm

# graphviz is only needed for the graph view of the pprof web UI.
RUN apt-get update \
 && apt-get install -y --no-install-recommends graphviz \
 && rm -rf /var/lib/apt/lists/*

RUN go install golang.org/x/perf/cmd/benchstat@latest

# The mounted repository may not be owned by the container user, and VCS
# stamping would fail on it.
ENV GOFLAGS=-buildvcs=false
RUN git config --global --add safe.directory '*'

WORKDIR /src
CMD ["bash"]
