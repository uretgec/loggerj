# loggerj - Usage Examples

Comprehensive examples for all `loggerj` features, configurations, and the Pre-Compiled SubProfile architecture.

## Table of Contents

- [loggerj - Usage Examples](#loggerj---usage-examples)
  - [Table of Contents](#table-of-contents)
  - [1. The SubProfile Paradigm (CRITICAL)](#1-the-subprofile-paradigm-critical)
  - [2. Basic Usage](#2-basic-usage)
  - [3. JSON \& Text Output](#3-json--text-output)
    - [JSON Output (Recommended for Production)](#json-output-recommended-for-production)
    - [Text Output (Recommended for Development)](#text-output-recommended-for-development)
  - [4. Typed Field API (Zero-Allocation)](#4-typed-field-api-zero-allocation)
    - [Dynamic Values (Variables)](#dynamic-values-variables)
    - [String Literals](#string-literals)
    - [Error Handling](#error-handling)
    - [HTTP Middleware Example](#http-middleware-example)
  - [5. Multiple Loggers](#5-multiple-loggers)
  - [6. File Logging \& Rotation](#6-file-logging--rotation)
  - [7. Sampling \& Rate Limiting](#7-sampling--rate-limiting)
    - [Rate Limiting](#rate-limiting)
    - [Sampling](#sampling)
  - [8. Runtime Level Change](#8-runtime-level-change)
  - [9. Concurrent Usage \& Graceful Shutdown](#9-concurrent-usage--graceful-shutdown)
  - [10. Sync Mode (Audit Trails)](#10-sync-mode-audit-trails)
    - [OSBuffered (Default)](#osbuffered-default)
    - [Direct (Per-Log Guarantee)](#direct-per-log-guarantee)
    - [FsyncEveryN (Batch Durability)](#fsynceveryn-batch-durability)
    - [FsyncEveryWrite (Maximum Durability)](#fsynceverywrite-maximum-durability)
    - [Durability Tier Decision Matrix](#durability-tier-decision-matrix)
  - [11. HTTP \& Middleware Integration](#11-http--middleware-integration)
    - [Standard HTTP Middleware](#standard-http-middleware)
    - [Go Fiber Middleware](#go-fiber-middleware)
  - [12. Error Context \& Structured Fields](#12-error-context--structured-fields)
  - [13. Custom Writer \& Test Helper](#13-custom-writer--test-helper)
    - [Custom Writer (e.g., Kafka)](#custom-writer-eg-kafka)
    - [Test Helper (Deterministic, No Sleeps)](#test-helper-deterministic-no-sleeps)
  - [14. Production Configurations](#14-production-configurations)
    - [Standard Production Config](#standard-production-config)
    - [Low-Resource Config (e.g., 512MB RAM, 1 CPU)](#low-resource-config-eg-512mb-ram-1-cpu)
    - [High-Throughput Config](#high-throughput-config)
    - [Audit Trail Config (Sync Mode)](#audit-trail-config-sync-mode)
  - [15. Advanced Integrations](#15-advanced-integrations)
    - [Metrics Integration (Prometheus)](#metrics-integration-prometheus)
    - [Worker Pool Integration](#worker-pool-integration)
    - [Standard Library Integration (Intercepting `std log`)](#standard-library-integration-intercepting-std-log)
      - [Output Example](#output-example)
    - [Performance Note](#performance-note)
  - [16. Observability](#16-observability)
    - [Caller Info (Debug Only)](#caller-info-debug-only)
    - [Drop Monitoring (Polling)](#drop-monitoring-polling)
    - [Drop Monitoring (Callback — Real-Time)](#drop-monitoring-callback--real-time)
    - [Flush on Signal](#flush-on-signal)
    - [Sync Mode Write Error Monitoring (v1.3.1)](#sync-mode-write-error-monitoring-v131)
  - [17. Buffer Tuning](#17-buffer-tuning)
  - [18. Context Integration](#18-context-integration)
    - [Available Context Keys](#available-context-keys)
    - [Basic Usage](#basic-usage)
    - [Nil Context Safety](#nil-context-safety)
    - [Custom Context Keys](#custom-context-keys)
    - [Performance](#performance)
  - [19. slog.Handler Adapter (Go 1.21+ Ecosystem Integration)](#19-sloghandler-adapter-go-121-ecosystem-integration)
    - [Basic Usage](#basic-usage-1)
    - [WithAttrs and WithGroup](#withattrs-and-withgroup)
    - [Setting as Default slog Logger](#setting-as-default-slog-logger)
    - [Performance Note](#performance-note-1)
    - [Limitations](#limitations)
  - [20. Rate Limit Exact Counting Cap (v1.4.0)](#20-rate-limit-exact-counting-cap-v140)
  - [21. slog.Group Multi-Key Flattening (v1.4.0)](#21-sloggroup-multi-key-flattening-v140)
    - [Nested Groups](#nested-groups)
    - [Why Dotted Keys Instead of Nested JSON?](#why-dotted-keys-instead-of-nested-json)
  - [Summary](#summary)

---

## 1. The SubProfile Paradigm (CRITICAL)

In `loggerj`, rate limiting, sampling, and static fields are no longer passed during the log call. Instead, they are defined once at startup using `RegisterSub`. This "Pre-Compiled" approach is the secret to our lock-free, zero-allocation hot path.

```go
package main

import (
    "context"
    "time"

    "github.com/uretgec/loggerj"
)

func main() {
    // 1. Create logger
    logger := loggerj.NewLogger(loggerj.Config{
        JSONOutput:   true,
        FlushTimeout: 50 * time.Millisecond,
    })

    // 2. Register SubProfiles (COLD PATH: Do this ONCE at startup)
    // Rules are baked into memory. Zero hot-path overhead.
    logger.RegisterSub("HTTP",
        loggerj.WithRateLimit(1000, time.Second),
        loggerj.WithFields("env", "prod", "service", "gateway"), // Pre-baked prefix!
    )
    logger.RegisterSub("DB",
        loggerj.WithSampleRate(100), // Log 1 out of 100 entries
    )

    // 3. Start the async worker
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go logger.Start(ctx)
    defer logger.Close()

    // 4. Log messages (HOT PATH: Ultra-fast, zero allocation)
    // The "HTTP" type automatically gets the rate limit and prefix fields applied!
    logger.InfoString("HTTP", "request received", "method", "GET", "path", "/api/v1/users")
    logger.ErrorString("DB", "connection timeout", "host", "localhost", "err", "dial tcp: i/o timeout")

    // 5. Ensure all logs are written before exit
    logger.Flush()
}
```

---

## 2. Basic Usage

Minimal example using the recommended `String` API.

```go
logger := loggerj.NewLogger(loggerj.Config{
    FlushTimeout: 50 * time.Millisecond,
})

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go logger.Start(ctx)
defer logger.Close()

// String API (Recommended for 99% of use cases)
logger.InfoString("STARTUP", "Server initialized")
logger.WarnString("DB", "connection slow", "latency_ms", "500")
logger.ErrorString("DB", "connection failed", "host", "localhost", "err", "timeout")

// []byte API (For extreme hot-paths to avoid string->[]byte conversion)
logger.Info("STARTUP", []byte("Server initialized"))
```

---

## 3. JSON & Text Output

### JSON Output (Recommended for Production)

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:   true,
    FlushTimeout: 50 * time.Millisecond,
})
// ... start worker ...

logger.InfoString("HTTP", "request received",
    "method", "GET",
    "path", "/api/v1/users",
    "status", "200",
    "duration_ms", "45")
```

Output:

```json
{"ts":1704067200123,"level":"INFO","type":"HTTP","msg":"request received","fields":{"method":"GET","path":"/api/v1/users","status":"200","duration_ms":"45"}}
```

### Text Output (Recommended for Development)

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:   false,  // Text format
    FlushTimeout: 50 * time.Millisecond,
})
// ... start worker ...

logger.InfoString("CONN", "new connection", "addr", "192.168.1.1:54321")
```

Output:

```txt
[1704067200123] INFO [CONN] new connection addr=192.168.1.1:54321
```

---

## 4. Typed Field API (Zero-Allocation)

`loggerj` provides a typed field API similar to `zap.Field`, but with **zero caller-side allocations** even for dynamic values. The `Field` struct is 48 bytes, passed by value, with `Num uint64` holding int64/uint64/float64-bits/duration-ns/bool as a tagged union.

### Dynamic Values (Variables)

When logging dynamic values (variables, not string literals), typed fields avoid `strconv.Itoa` / `fmt.Sprintf` allocations:

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:   true,
    FlushTimeout: 50 * time.Millisecond,
})
// ... start worker ...

// Dynamic values (variables)
status := 200
latency := 150 * time.Millisecond
cached := true
userID := int64(12345)

logger.InfoFields("HTTP", []byte("request completed"),
    loggerj.Int("status", status),           // 0 allocs
    loggerj.Dur("latency", latency),         // 0 allocs
    loggerj.Bool("cached", cached),          // 0 allocs
    loggerj.Int64("user_id", userID),        // 0 allocs
    loggerj.Str("method", "GET"),            // 0 allocs
)
```

Output:

```json
{"ts":1704067200123,"level":"INFO","type":"HTTP","msg":"request completed","fields":{"status":200,"latency":"150ms","cached":true,"user_id":12345,"method":"GET"}}
```

**Benchmark: 0 allocs/op for dynamic fields*

```txt
BenchmarkTypedFields_Dynamic-10    15,700,000    72 ns/op    0 B/op    0 allocs/op
```

### String Literals

For string literals (constants), use the `Str` constructor:

```go
logger.InfoFields("HTTP", []byte("request"),
    loggerj.Str("method", "GET"),
    loggerj.Str("path", "/api/v1/users"),
)
```

### Error Handling

The `Err` constructor returns a zero-value `Field` (skipped by encoder) if the error is nil:

```go
func processOrder(logger *loggerj.Logger, orderID string) error {
    order, err := db.GetOrder(orderID)
    if err != nil {
        logger.ErrorFields("ORDER", []byte("failed to get order"),
            loggerj.Str("order_id", orderID),
            loggerj.Err(err),  // Automatically skipped if err is nil
        )
        return err
    }

    logger.InfoFields("ORDER", []byte("order processed"),
        loggerj.Str("order_id", orderID),
        loggerj.Err(nil),  // Skipped — no "error" field in output
    )
    return nil
}
```

### HTTP Middleware Example

Complete HTTP middleware using typed fields:

```go
func loggingMiddleware(logger *loggerj.Logger, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        rw := &responseWriter{ResponseWriter: w, statusCode: 200}

        next.ServeHTTP(rw, r)

        latency := time.Since(start)

        logger.InfoFields("HTTP", []byte("request completed"),
            loggerj.Str("method", r.Method),
            loggerj.Str("path", r.URL.Path),
            loggerj.Int("status", rw.statusCode),
            loggerj.Dur("latency", latency),
            loggerj.Str("remote_addr", r.RemoteAddr),
            loggerj.Int64("bytes_written", int64(rw.bytesWritten)),
        )
    })
}
```

---

## 5. Multiple Loggers

Different loggers for different purposes, running independently.

```go
// Application logger
appLogger := loggerj.NewLogger(loggerj.Config{
    OutputFile:   "/var/log/app.log",
    JSONOutput:   true,
    FlushTimeout: 100 * time.Millisecond,
})

// Audit logger (critical, fast flush)
auditLogger := loggerj.NewLogger(loggerj.Config{
    OutputFile:   "/var/log/audit.log",
    JSONOutput:   true,
    FlushTimeout: 10 * time.Millisecond,
})

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

go appLogger.Start(ctx)
go auditLogger.Start(ctx)

defer appLogger.Close()
defer auditLogger.Close()

appLogger.InfoString("HTTP", "request received")
auditLogger.InfoString("AUTH", "user login", "user_id", "12345")
```

---

## 6. File Logging & Rotation

Automatic log file rotation based on size, with no external dependencies.

```go
logger := loggerj.NewLogger(loggerj.Config{
    OutputFile:     "/var/log/myapp.log",
    MaxFileSize:    100 * 1024 * 1024,  // 100 MB
    MaxBackupFiles: 5,                   // Keep 5 backups
    JSONOutput:     true,
    FlushTimeout:   50 * time.Millisecond,
})

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go logger.Start(ctx)
defer logger.Close()

// Logs will rotate automatically:
// myapp.log → myapp.log.1 → myapp.log.2 → ... → myapp.log.5 → (deleted)
```

---

## 7. Sampling & Rate Limiting

These are handled exclusively via `RegisterSub` to ensure lock-free performance.

### Rate Limiting

```go
logger := loggerj.NewLogger(loggerj.Config{
    FlushTimeout: 50 * time.Millisecond,
})

// Define the rule ONCE at startup
logger.RegisterSub("INVALID_CMD", loggerj.WithRateLimit(10, time.Second))

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go logger.Start(ctx)

// Hot path: Clean API, lock-free enforcement
for i := 0; i < 1000; i++ {
    logger.WarnString("INVALID_CMD", "bad command received",
        "cmd", "DROP TABLE",
        "ip", "10.0.0.5")
}
// Only ~10 logs will be written per second. The rest are dropped in ~41ns with 0 allocs.
```

### Sampling

```go
logger := loggerj.NewLogger(loggerj.Config{
    FlushTimeout: 50 * time.Millisecond,
})

// Log only 1 out of 100 entries for this specific type
logger.RegisterSub("HTTP_REQUEST", loggerj.WithSampleRate(100))

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go logger.Start(ctx)

for i := 0; i < 10000; i++ {
    logger.InfoString("HTTP_REQUEST", "request received", "path", "/api/v1/users")
}
// Only ~100 logs will be written.
```

---

## 8. Runtime Level Change

Change log level at runtime without restarting the application.

```go
logger := loggerj.NewLogger(loggerj.Config{
    FlushTimeout: 50 * time.Millisecond,
})

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go logger.Start(ctx)

// Start with ERROR level
logger.SetLevelValue(loggerj.LevelError)

// Debug logs are filtered instantly (~2 ns/op, zero CPU cost)
logger.DebugString("SQL", "query plan", "sql", "SELECT ...")  // Dropped

// Change level at runtime (e.g., via HTTP admin endpoint)
logger.SetLevelValue(loggerj.LevelDebug)  // Now DEBUG logs appear
logger.DebugString("SQL", "query plan", "sql", "SELECT ...")  // Written
```

---

## 9. Concurrent Usage & Graceful Shutdown

`loggerj` is fully thread-safe and designed for high concurrency.

```go
func main() {
    logger := loggerj.NewLogger(loggerj.Config{
        OutputFile:   "/var/log/app.log",
        FlushTimeout: 50 * time.Millisecond,
    })

    ctx, cancel := context.WithCancel(context.Background())
    go logger.Start(ctx)

    // Handle shutdown signals
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

    // Concurrent logging from 100 goroutines
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            for j := 0; j < 1000; j++ {
                logger.InfoString("WORKER", "processing",
                    "worker_id", fmt.Sprintf("%d", id),
                    "iteration", fmt.Sprintf("%d", j))
            }
        }(i)
    }

    wg.Wait()

    // Wait for signal
    <-sigCh
    logger.InfoString("SHUTDOWN", "Received shutdown signal")

    // Graceful shutdown sequence
    logger.Flush()   // 1. Flush pending logs
    cancel()         // 2. Stop worker
    logger.Close()   // 3. Close file handles
}
```

---

## 10. Sync Mode (Audit Trails)

For scenarios requiring **per-log write guarantees** (audit trails, financial logs), `loggerj` offers a sync mode that bypasses the async pipeline. Sync mode is **fixed at creation time** and cannot be toggled at runtime.

### OSBuffered (Default)

Buffered writes with periodic flush (~10ms). **2.7x faster than zerolog sync** (~112 ns/op vs ~300 ns/op).

```go
logger := loggerj.NewLogger(loggerj.Config{
    SyncMode:       true,
    OutputFile:     "/var/log/audit.log",
    DurabilityTier: loggerj.OSBuffered,  // Default
    JSONOutput:     true,
})

// No Start() needed — sync mode writes directly
logger.InfoFields("AUDIT", []byte("user login"),
    loggerj.Str("user_id", "12345"),
    loggerj.Str("ip", "192.168.1.50"),
    loggerj.Dur("latency", 50*time.Millisecond),
)

// Graceful shutdown: Flush() is a no-op (already sync), but Close() persists all buffered data
logger.Close()
```

**Survives:** Process crash  
**Does NOT survive:** OS crash / power loss (data may still be in page cache)

### Direct (Per-Log Guarantee)

Each log is written with a single `write()` syscall. No buffering.

```go
logger := loggerj.NewLogger(loggerj.Config{
    SyncMode:       true,
    OutputFile:     "/var/log/audit.log",
    DurabilityTier: loggerj.Direct,
    JSONOutput:     true,
})

logger.InfoString("AUDIT", "user login", "user_id", "12345")
logger.Close()
```

**Survives:** Process crash  
**Does NOT survive:** OS crash / power loss  
**Throughput:** ~1550 ns/op (syscall overhead)

### FsyncEveryN (Batch Durability)

Calls `fsync()` after every N writes. Survives OS crash / power loss for committed entries.

```go
logger := loggerj.NewLogger(loggerj.Config{
    SyncMode:         true,
    OutputFile:       "/var/log/audit.log",
    DurabilityTier:   loggerj.FsyncEveryN,
    FsyncEveryNCount: 100,  // fsync every 100 logs
    JSONOutput:       true,
})

for i := 0; i < 1000; i++ {
    logger.InfoString("AUDIT", fmt.Sprintf("event %d", i))
}
// fsync() called 10 times (at logs 100, 200, ..., 1000)
logger.Close()
```

**Survives:** Process crash + OS crash / power loss (for committed entries)  
**Throughput:** ~5000 ns/op (fsync latency amortized)

### FsyncEveryWrite (Maximum Durability)

Calls `fsync()` after every single write. Maximum durability, minimum throughput.

```go
logger := loggerj.NewLogger(loggerj.Config{
    SyncMode:       true,
    OutputFile:     "/var/log/audit.log",
    DurabilityTier: loggerj.FsyncEveryWrite,
    JSONOutput:     true,
})

logger.InfoString("AUDIT", "critical event", "event_id", "12345")
// fsync() called immediately — data is on disk before this returns
logger.Close()
```

**Survives:** Process crash + OS crash / power loss (every log)  
**Throughput:** ~4-5 ms/op (fsync on every log)  
**Use case:** Audit trails, financial records, compliance logs

### Durability Tier Decision Matrix

| Scenario | Recommended Tier | Why |
|----------|------------------|-----|
| High-volume application logs | **Async mode** (default) | Maximum throughput, acceptable if some logs lost on crash |
| Audit trails (low volume) | **FsyncEveryWrite** | Every log must survive power loss |
| Financial transactions | **FsyncEveryN** (N=10-100) | Balance durability and throughput |
| General logging with sync guarantee | **OSBuffered** | Fast, survives process crash |
| Debug logs with immediate visibility | **Direct** | No buffering delay |

---

## 11. HTTP & Middleware Integration

### Standard HTTP Middleware

```go
func loggingMiddleware(logger *loggerj.Logger, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        rw := &responseWriter{ResponseWriter: w, statusCode: 200}

        next.ServeHTTP(rw, r)

        logger.InfoString("HTTP", "request completed",
            "method", r.Method,
            "path", r.URL.Path,
            "status", fmt.Sprintf("%d", rw.statusCode),
            "duration_ms", fmt.Sprintf("%d", time.Since(start).Milliseconds()),
            "remote_addr", r.RemoteAddr)
    })
}
```

### Go Fiber Middleware

```go
func LoggerMiddleware(logger *loggerj.Logger) fiber.Handler {
    return func(c *fiber.Ctx) error {
        start := time.Now()
        err := c.Next()

        logger.InfoString("HTTP", "request",
            "method", c.Method(),
            "path", c.Path(),
            "status", fmt.Sprintf("%d", c.Response().StatusCode()),
            "duration_ms", fmt.Sprintf("%d", time.Since(start).Milliseconds()),
            "ip", c.IP())

        return err
    }
}
```

---

## 12. Error Context & Structured Fields

Rich structured logging with full error context.

```go
func processOrder(logger *loggerj.Logger, orderID string, userID string) error {
    order, err := db.GetOrder(orderID)
    if err != nil {
        logger.ErrorString("ORDER", "failed to get order",
            "order_id", orderID,
            "user_id", userID,
            "err", err.Error())
        return fmt.Errorf("get order: %w", err)
    }

    logger.InfoString("ORDER", "order processed",
        "order_id", orderID,
        "user_id", userID,
        "total", fmt.Sprintf("%.2f", order.Total))

    return nil
}
```

---

## 13. Custom Writer & Test Helper

### Custom Writer (e.g., Kafka)

```go
type KafkaWriter struct {
    producer *kafka.Producer
    topic    string
}

func (w *KafkaWriter) Write(p []byte) (n int, err error) {
    msg := &kafka.Message{
        TopicPartition: kafka.TopicPartition{Topic: &w.topic},
        Value:          p,
    }
    return w.producer.Produce(msg, nil)
}

// Usage:
// kafkaWriter := &KafkaWriter{producer: p, topic: "app-logs"}
// go logger.StartWithWriter(ctx, kafkaWriter)
```

### Test Helper (Deterministic, No Sleeps)

```go
package testutil

import (
    "bytes"
    "context"
    "strings"
    "time"

    "github.com/uretgec/loggerj"
)

type TestLogger struct {
    Logger *loggerj.Logger
    Buffer *bytes.Buffer
    cancel context.CancelFunc
    done   chan struct{}
}

func NewTestLogger() *TestLogger {
    buf := &bytes.Buffer{}
    logger := loggerj.NewLogger(loggerj.Config{
        JSONOutput:   true,
        FlushTimeout: 10 * time.Millisecond,
        ChannelSize:  100,
    })

    ctx, cancel := context.WithCancel(context.Background())
    go logger.StartWithWriter(ctx, buf)

    // Block until the worker is ready — no time.Sleep needed
    <-logger.started

    return &TestLogger{
        Logger: logger,
        Buffer: buf,
        cancel: cancel,
        done:   logger.workerDone,
    }
}

func (t *TestLogger) Close() {
    t.Logger.Flush()       // 1. Write all pending entries
    t.cancel()             // 2. Signal worker to stop
    <-t.done               // 3. Wait for worker to exit (deterministic)
    t.Logger.Close()       // 4. Release file handles
}

func (t *TestLogger) Contains(s string) bool {
    t.Logger.Flush()
    return strings.Contains(t.Buffer.String(), s)
}
```

---

## 14. Production Configurations

### Standard Production Config

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:     true,   // Structured for log aggregation
    OutputFile:     "/var/log/myapp/app.log",
    MaxFileSize:    100 * 1024 * 1024,  // 100 MB
    MaxBackupFiles: 10,
    FlushTimeout:   100 * time.Millisecond,
    ChannelSize:    8192,
    IncludeCaller:  false, // CRITICAL: Saves ~460ns and 2 allocs per log
})
```

### Low-Resource Config (e.g., 512MB RAM, 1 CPU)

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:       false,  // Text is faster and smaller
    OutputFile:       "/var/log/myapp/app.log",
    FlushTimeout:     100 * time.Millisecond,
    ChannelSize:      1024,
    WorkerBufferSize: 2048,
    FlushThreshold:   2048,
    WriterBufferSize: 4096,
    IncludeCaller:    false,
})
```

### High-Throughput Config

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:       true,
    OutputFile:       "/var/log/myapp/app.log",
    FlushTimeout:     500 * time.Millisecond,  // Batch more
    ChannelSize:      65536,                   // Huge buffer for spikes
    WorkerBufferSize: 16384,
    FlushThreshold:   16384,
    WriterBufferSize: 32768,
    IncludeCaller:    false,
})
```

### Audit Trail Config (Sync Mode)

```go
logger := loggerj.NewLogger(loggerj.Config{
    SyncMode:         true,
    OutputFile:       "/var/log/audit.log",
    DurabilityTier:   loggerj.FsyncEveryN,
    FsyncEveryNCount: 10,  // fsync every 10 logs
    JSONOutput:       true,
    MaxFileSize:      50 * 1024 * 1024,  // 50 MB
    MaxBackupFiles:   30,                // Keep 30 days
})

// No Start() needed — sync mode writes directly
logger.InfoFields("AUDIT", []byte("user login"),
    loggerj.Str("user_id", "12345"),
    loggerj.Str("ip", "192.168.1.50"),
)
logger.Close()  // Flushes all buffered data before closing
```

---

## 15. Advanced Integrations

### Metrics Integration (Prometheus)

```go
import "github.com/prometheus/client_golang/prometheus"

var logsDropped = prometheus.NewCounter(
    prometheus.CounterOpts{
        Name: "logger_logs_dropped_total",
        Help: "Total number of dropped logs",
    },
)

func collectLoggerMetrics(logger *loggerj.Logger) {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        stats := logger.Stats()
        logsDropped.Add(float64(stats.Drops))  // Stats is now a struct, not a map
    }
}
```

### Worker Pool Integration

```go
func (p *WorkerPool) Start(ctx context.Context) {
    for i := 0; i < p.workers; i++ {
        go func(workerID int) {
            for {
                select {
                case <-ctx.Done():
                    return
                case task := <-p.tasks:
                    p.logger.InfoString("WORKER", "processing task",
                        "worker_id", fmt.Sprintf("%d", workerID),
                        "task_id", fmt.Sprintf("%d", task.ID))
                    // ... process task ...
                }
            }
        }(i)
    }
}
```

### Standard Library Integration (Intercepting `std log`)

`loggerj` provides an `io.Writer` adapter that allows you to intercept logs from Go's standard `log` package and third-party libraries that rely on it. This ensures that all application logs flow through `loggerj`'s high-performance, asynchronous pipeline, benefiting from log rotation, structured formatting, and drop monitoring.

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/uretgec/loggerj"
)

func main() {
    // 1. Initialize loggerj
    logger := loggerj.NewLogger(loggerj.Config{
        JSONOutput:   true,
        FlushTimeout: 50 * time.Millisecond,
    })

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go logger.Start(ctx)
    defer logger.Close()

    // 2. Redirect standard log to loggerj
    // CRITICAL: Disable std log's default flags (date/time) to avoid duplicate
    // timestamps, as loggerj already adds its own high-precision timestamp.
    log.SetFlags(0)
    log.SetOutput(logger.AsWriter(loggerj.LevelInfo, "STDLIB"))

    // 3. Usage
    // These messages will now be formatted as JSON and processed asynchronously.
    log.Println("This is a standard log message intercepted by loggerj.")
    log.Printf("User %s logged in from %s", "admin", "192.168.1.50")

    // You can still use loggerj's native API alongside std log
    logger.InfoString("APP", "Application logic running")

    logger.Flush()
}
```

#### Output Example

When using `JSONOutput: true`, intercepted standard logs will appear as:

```json
{"ts":1704067200123,"level":"INFO","type":"STDLIB","msg":"This is a standard log message intercepted by loggerj."}
{"ts":1704067200456,"level":"INFO","type":"STDLIB","msg":"User admin logged in from 192.168.1.50"}
{"ts":1704067200789,"level":"INFO","type":"APP","msg":"Application logic running"}
```

### Performance Note

`loggerj` 's  `AsWriter`  adapter performs **zero heap allocations** per write. The implementation uses  `bytes.TrimRight(p, "\n")`  instead of  `strings.TrimRight(string(p), "\n")` , avoiding the  `string(p)`  allocation. This is safe because  `Log()`  immediately copies the message via  `append(e.Msg[:0], msg...)` , satisfying the  `io.Writer`  contract (not retaining  `p`  after  `Write`  returns).

For maximum performance, prefer  `logger.InfoString`  or  `logger.Info`  directly over  `AsWriter` , as the adapter still incurs function call overhead. However,  `AsWriter`  is now allocation-free and suitable for intercepting high-volume legacy or third-party logs.

---

## 16. Observability

### Caller Info (Debug Only)

```go
logger := loggerj.NewLogger(loggerj.Config{
    IncludeCaller: true,  // Adds file:line to logs
    FlushTimeout:  50 * time.Millisecond,
})

// Output: [1704067200123] INFO [DEBUG] main.go:42 detailed info
// WARNING: Adds ~460ns and 2 allocations per log. Disable in production.
```

### Drop Monitoring (Polling)

```go
go func() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        drops := logger.Drops()
        if drops > 0 {
            fmt.Fprintf(os.Stderr, "WARNING: %d logs dropped in last 5s\n", drops)
            logger.ResetDrops()
        }
    }
}()
```

### Drop Monitoring (Callback — Real-Time)

```go
// SetOnDrop registers a callback invoked on every drop event.
// WARNING: Called from the hot path — keep it fast (atomic ops only).
logger.SetOnDrop(func(totalDropped uint64) {
    // Example: increment a Prometheus counter
    logsDropped.Inc()

    // Example: log to stderr every 1000 drops
    if totalDropped%1000 == 0 {
        fmt.Fprintf(os.Stderr, "WARNING: %d total logs dropped\n", totalDropped)
    }
})
```

### Flush on Signal

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGUSR1)

go func() {
    for range sigCh {
        logger.InfoString("SIGNAL", "Received SIGUSR1, flushing logs")
        logger.Flush()
    }
}()

// Usage: kill -USR1 <pid>
```

### Sync Mode Write Error Monitoring (v1.3.1)

When using **Sync Mode** for audit trails, failed `write(2)` syscalls indicate
log loss. `loggerj` v1.3.1 exposes these failures via `Stats.SyncWriteErrors`:

```go
logger := loggerj.NewLogger(loggerj.Config{
    SyncMode:       true,
    OutputFile:     "/var/log/audit.log",
    DurabilityTier: loggerj.FsyncEveryWrite,
})

// ... application runs, logs are written synchronously ...

// Monitor sync write failures (e.g., disk full, file handle closed)
stats := logger.Stats()
if stats.SyncWriteErrors > 0 {
    log.Printf("CRITICAL: %d sync writes failed — audit logs may be lost",
        stats.SyncWriteErrors)
    // Alert, investigate, remediate
}
```

**When `SyncWriteErrors` increments:**

- Disk is full or read-only
- File handle was closed externally
- OS-level I/O error (e.g., NFS mount lost)
- `fsync()` failed (hardware issue)

Every failed write is also logged to `stderr` with tier information:

```txt
loggerj: sync write error (tier=FsyncEveryWrite): write /var/log/audit.log: no space left on device
```

**Best practice:** Poll `Stats().SyncWriteErrors` in your observability loop
(e.g., Prometheus exporter) and alert when non-zero. For audit-critical
systems, a non-zero counter should trigger immediate investigation.

---

## 17. Buffer Tuning

Fine-tune buffer sizes for your specific workload:

| Parameter | Low Memory | Balanced (Default) | High Performance |
|-----------|-----------|-------------------|-----------------|
| ChannelSize | 512-1024 | 4096 | 16384-65536 |
| WorkerBufferSize | 1024-2048 | 4096 | 8192-16384 |
| FlushThreshold | 1024-2048 | 4096 | 8192-16384 |
| WriterBufferSize | 2048-4096 | 8192 | 16384-32768 |
| FlushTimeout | 100ms | 50ms | 10-25ms |

---

## 18. Context Integration

`loggerj` provides opt-in context-aware logging methods (`InfoCtx`, `DebugCtx`, `WarnCtx`, `ErrorCtx`) that extract known keys from `context.Context`. When unused, there is **zero overhead** — the methods behave identically to their non-context counterparts.

### Available Context Keys

| Key | Field Name | Use Case |
|-----|-----------|----------|
| `loggerj.TraceIDKey` | `trace_id` | Distributed tracing (Jaeger, Zipkin, OTel) |
| `loggerj.RequestIDKey` | `request_id` | HTTP request correlation |
| `loggerj.SpanIDKey` | `span_id` | Span-level tracing |

### Basic Usage

```go
// In your HTTP middleware, inject trace/request IDs into the context:
func tracingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()
        ctx = context.WithValue(ctx, loggerj.TraceIDKey, r.Header.Get("X-Trace-ID"))
        ctx = context.WithValue(ctx, loggerj.RequestIDKey, r.Header.Get("X-Request-ID"))
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// In your handlers, use the Ctx methods:
func handleRequest(logger *loggerj.Logger, ctx context.Context) {
    logger.InfoCtx(ctx, "HTTP", "processing request", "method", "GET", "path", "/api")
    // Output includes: "trace_id":"abc-123","request_id":"req-456"
}
```

### Nil Context Safety

```go
// Passing nil is safe — behaves exactly like InfoString
logger.InfoCtx(nil, "HTTP", "no context available")
// Output: {"ts":...,"level":"INFO","type":"HTTP","msg":"no context available"}
// No trace_id, request_id, or span_id fields added.
```

### Custom Context Keys

For application-specific context values, extract them in your middleware and pass as standard fields:

```go
// Extract custom values in middleware, pass as fields (zero-cost)
userID := ctx.Value("user_id").(string)
logger.InfoString("HTTP", "request", "user_id", userID, "method", "GET")
```

### Performance

| Method | ns/op | allocs/op | Notes |
|--------|-------|-----------|-------|
| `InfoString` (no ctx) | ~63 | 0 | Baseline |
| `InfoCtx` (nil ctx) | ~63 | 0 | Identical to InfoString |
| `InfoCtx` (with values) | ~63 + N×ctx.Value | 0-1 | N = number of known keys found |

---

## 19. slog.Handler Adapter (Go 1.21+ Ecosystem Integration)

`loggerj` v1.3.1 provides a native `slog.Handler` implementation that routes all
`slog` calls through loggerj's zero-allocation async pipeline. This lets
applications adopted to the `log/slog` standard benefit from loggerj's
throughput without changing call sites.

### Basic Usage

```go
package main

import (
    "context"
    "log/slog"
    "time"

    "github.com/uretgec/loggerj"
)

func main() {
    // 1. Create loggerj logger (async mode)
    logger := loggerj.NewLogger(loggerj.Config{
        JSONOutput:   true,
        FlushTimeout: 50 * time.Millisecond,
    })

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go logger.Start(ctx)
    defer logger.Close()

    // 2. Create slog.Handler wrapping loggerj
    handler := loggerj.NewSlogHandler(logger, "APP")
    slogger := slog.New(handler)

    // 3. Use slog API — all calls flow through loggerj's async pipeline
    slogger.Info("request received",
        "method", "GET",
        "path", "/api/v1/users",
        "status", 200,
    )
    // Output: {"ts":...,"level":"INFO","type":"APP","msg":"request received","fields":{"method":"GET","path":"/api/v1/users","status":200}}
}
```

### WithAttrs and WithGroup

```go
handler := loggerj.NewSlogHandler(logger, "HTTP")
slogger := slog.New(handler)

// WithAttrs: pre-attributes prepended to every record (cold path)
child := slogger.With("env", "prod", "service", "gateway")
child.Info("request")
// Output includes: "env":"prod","service":"gateway"

// WithGroup: nested groups flatten into dotted keys
grouped := slogger.WithGroup("http")
grouped.Info("request", "method", "GET")
// Output: "http.method":"GET" (flattened, not nested object)
```

### Setting as Default slog Logger

```go
handler := loggerj.NewSlogHandler(logger, "APP")
slog.SetDefault(slog.New(handler))

// Now all slog.Info/Warn/Error calls in your application
// (and third-party libraries using slog) flow through loggerj
slog.Info("application started")
```

### Performance Note

```txt
BenchmarkSlogHandler_Handle-10    8,600,000    143 ns/op    0 B/op    0 allocs/op
```

The handler's `Handle()` method is allocation-free once warm. The `slog.Attr`
→ `Field` conversion is boxing-free because `slog.Value` is already a tagged
union that maps directly to loggerj's `Field` struct.

### Limitations

- **Nested groups flatten to dotted keys** — loggerj's `Field` API does not
  support nested JSON objects. `WithGroup("http").WithGroup("request")` produces
  `"http.request.method"` as a single key, not `{"http":{"request":{"method":...}}}`.
- **Level mapping is bucket-based** — slog's custom intermediate levels (e.g.,
  `slog.Level(2)`) map to the nearest loggerj level (`LevelInfo`).
- **Time values render as RFC3339 strings** — for stable JSON output. If you
  need Unix timestamps, use `Int64("ts", time.Now().Unix())` instead.

---

## 20. Rate Limit Exact Counting Cap (v1.4.0)

v1.4.0 uses a packed atomic state (40-bit window index + 24-bit counter) for
lock-free rate limiting. This design supports exact per-window counting up to
**16,777,215** logs per window. Larger configured limits are silently capped.

```go
logger := loggerj.NewLogger(loggerj.Config{
    FlushTimeout: 50 * time.Millisecond,
})

// This limit is within the exact counting range
logger.RegisterSub("API", loggerj.WithRateLimit(1_000_000, time.Second))
// Exact: exactly 1,000,000 logs per second

// This limit exceeds the exact counting range
logger.RegisterSub("HIGH_VOLUME", loggerj.WithRateLimit(20_000_000, time.Second))
// Capped: effectively 16,777,215 logs per second

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go logger.Start(ctx)
defer logger.Close()

// Both profiles work correctly, but HIGH_VOLUME is capped at 16,777,215
for i := 0; i < 25_000_000; i++ {
    logger.InfoString("HIGH_VOLUME", "event", "id", fmt.Sprintf("%d", i))
}
// ~16,777,215 logs written, rest dropped
```

**When to use limits >16M:**

If you need rate limits exceeding 16,777,215 per window, consider:

- Using a longer window (e.g., `WithRateLimit(20_000_000, 2*time.Second)` stays within cap)
- External rate limiting (e.g., `golang.org/x/time/rate`) for exact counts above the cap
- Accepting the cap if approximate limiting is acceptable

**Why this cap exists:**

The packed atomic state uses 24 bits for the in-window counter to keep the
entire state in a single `atomic.Uint64`. This enables lock-free CAS operations
without mutex contention. The trade-off is a hard ceiling on exact counting.

---

## 21. slog.Group Multi-Key Flattening (v1.4.0)

v1.4.0 fixed the `slog.Group` multi-key attribute handling. Previously, groups
with multiple key-value pairs fell back to string representation. Now all groups
are correctly flattened into dotted keys, aligning with standard observability
pipeline expectations (Loki, Elasticsearch, Datadog).

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:   true,
    FlushTimeout: 50 * time.Millisecond,
})

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go logger.Start(ctx)
defer logger.Close()

handler := loggerj.NewSlogHandler(logger, "APP")
slogger := slog.New(handler)

// Multi-key slog.Group now produces dotted keys
slogger.Info("request",
    slog.Group("http",
        "method", "GET",
        "status", 200,
        "path", "/api/v1/users",
    ),
)
```

**Output (v1.4.0):**

```json
{"ts":1704067200123,"level":"INFO","type":"APP","msg":"request","fields":{"http.method":"GET","http.status":200,"http.path":"/api/v1/users"}}
```

**Output (v1.3.x, old behavior):**

```json
{"ts":1704067200123,"level":"INFO","type":"APP","msg":"request","fields":{"http":"method=GET status=200 path=/api/v1/users"}}
```

### Nested Groups

Nested `slog.Group` values are recursively flattened:

```go
slogger.Info("request",
    slog.Group("http",
        slog.Group("request",
            "method", "GET",
            "path", "/api",
        ),
        slog.Group("response",
            "status", 200,
            "bytes", 1024,
        ),
    ),
)
```

**Output:**

```json
{"ts":1704067200123,"level":"INFO","type":"APP","msg":"request","fields":{"http.request.method":"GET","http.request.path":"/api","http.response.status":200,"http.response.bytes":1024}}
```

### Why Dotted Keys Instead of Nested JSON?

loggerj's `Field` API is designed for zero-allocation hot-path performance.
True nested JSON objects would require either:

- Variadic slices (heap allocation)
- Inline arrays (memory bandwidth bloat)
- Interface boxing (allocation)

Dotted keys preserve the zero-allocation guarantee while working well with
modern log aggregation pipelines that treat dotted keys as equivalent to
nested objects for indexing and querying.

---

## Summary

These examples demonstrate the core philosophy of `loggerj`:

1. **Define rules once** using `RegisterSub` (Cold Path).
2. **Log cleanly and rapidly** using `InfoString`/`ErrorString` (Hot Path).
3. **Use typed fields** (`InfoFields` with `Int`, `Bool`, `Dur`) for zero-allocation dynamic values.
4. **Use context-aware methods** (`InfoCtx`/`ErrorCtx`) for distributed tracing (Opt-in).
5. **Choose async or sync mode** based on your durability requirements.
6. **Use slog.Handler** if your application is committed to the `log/slog` standard (v1.3.1).
7. **Monitor drops** via `SetOnDrop` callback or `Drops()` polling.
8. **Monitor sync write errors** via `Stats().SyncWriteErrors` for audit trails (v1.3.1).
9. **Achieve true zero-allocation** and lock-free concurrency.

For detailed performance metrics, see [BENCH.md](BENCH.md). For API reference, see [README.md](README.md). For competitor comparison, see [COMPARISON.md](COMPARISON.md).
