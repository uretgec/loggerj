# loggerj - Usage Examples

Comprehensive examples for all `loggerj` features, configurations, and the new **Pre-Compiled SubProfile** architecture.

---

## Table of Contents

- [loggerj - Usage Examples](#loggerj---usage-examples)
  - [Table of Contents](#table-of-contents)
  - [1. The SubProfile Paradigm (CRITICAL)](#1-the-subprofile-paradigm-critical)
  - [2. Basic Usage](#2-basic-usage)
  - [3. JSON \& Text Output](#3-json--text-output)
    - [JSON Output (Recommended for Production)](#json-output-recommended-for-production)
    - [Text Output (Recommended for Development)](#text-output-recommended-for-development)
  - [4. Multiple Loggers](#4-multiple-loggers)
  - [5. File Logging \& Rotation](#5-file-logging--rotation)
  - [6. Sampling \& Rate Limiting](#6-sampling--rate-limiting)
    - [Rate Limiting](#rate-limiting)
    - [Sampling](#sampling)
  - [7. Runtime Level Change](#7-runtime-level-change)
  - [8. Concurrent Usage \& Graceful Shutdown](#8-concurrent-usage--graceful-shutdown)
  - [9. HTTP \& Middleware Integration](#9-http--middleware-integration)
    - [Standard HTTP Middleware](#standard-http-middleware)
    - [Go Fiber Middleware](#go-fiber-middleware)
  - [10. Error Context \& Structured Fields](#10-error-context--structured-fields)
  - [11. Custom Writer \& Test Helper](#11-custom-writer--test-helper)
    - [Custom Writer (e.g., Kafka)](#custom-writer-eg-kafka)
    - [Test Helper](#test-helper)
  - [12. Production Configurations](#12-production-configurations)
    - [Standard Production Config](#standard-production-config)
    - [Low-Resource Config (e.g., 512MB RAM, 1 CPU)](#low-resource-config-eg-512mb-ram-1-cpu)
    - [High-Throughput Config](#high-throughput-config)
  - [13. Advanced Integrations](#13-advanced-integrations)
    - [Metrics Integration (Prometheus)](#metrics-integration-prometheus)
    - [Worker Pool Integration](#worker-pool-integration)
    - [Standard Library Integration (Intercepting `std log`)](#standard-library-integration-intercepting-std-log)
  - [14. Observability](#14-observability)
    - [Caller Info (Debug Only)](#caller-info-debug-only)
    - [Drop Monitoring](#drop-monitoring)
    - [Flush on Signal](#flush-on-signal)
  - [15. Buffer Tuning](#15-buffer-tuning)
  - [Summary](#summary)

---

## 1. The SubProfile Paradigm (CRITICAL)

In `loggerj` v2, rate limiting, sampling, and static fields are **no longer passed during the log call**. Instead, they are defined once at startup using `RegisterSub`. This "Pre-Compiled" approach is the secret to our lock-free, zero-allocation hot path.

```go
package main

import (
    "context"
    "time"
    "uretgec/internal/loggerj"
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

**Output:**

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

**Output:**

```txt
[1704067200123] INFO [CONN] new connection addr=192.168.1.1:54321
```

---

## 4. Multiple Loggers

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

## 5. File Logging & Rotation

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

## 6. Sampling & Rate Limiting

In v2, these are handled exclusively via `RegisterSub` to ensure lock-free performance.

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
// Only ~10 logs will be written per second. The rest are dropped in ~43ns with 0 allocs.
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

## 7. Runtime Level Change

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

## 8. Concurrent Usage & Graceful Shutdown

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

## 9. HTTP & Middleware Integration

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

## 10. Error Context & Structured Fields

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

## 11. Custom Writer & Test Helper

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

### Test Helper

```go
package testutil

import (
    "bytes"
    "context"
    "strings"
    "time"
    "uretgec/internal/loggerj"
)

type TestLogger struct {
    Logger *loggerj.Logger
    Buffer *bytes.Buffer
    cancel context.CancelFunc
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
    
    return &TestLogger{Logger: logger, Buffer: buf, cancel: cancel}
}

func (t *TestLogger) Close() {
    t.cancel()
    time.Sleep(20 * time.Millisecond)
    t.Logger.Close()
}

func (t *TestLogger) Contains(s string) bool {
    t.Logger.Flush()
    return strings.Contains(t.Buffer.String(), s)
}
```

---

## 12. Production Configurations

### Standard Production Config

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:   true,   // Structured for log aggregation
    OutputFile:   "/var/log/myapp/app.log",
    MaxFileSize:  100 * 1024 * 1024,  // 100 MB
    MaxBackupFiles: 10,
    FlushTimeout: 100 * time.Millisecond,
    ChannelSize:  8192,
    IncludeCaller: false, // CRITICAL: Saves ~380ns and 2 allocs per log
})
```

### Low-Resource Config (e.g., 512MB RAM, 1 CPU)

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:   false,  // Text is faster and smaller
    OutputFile:   "/var/log/myapp/app.log",
    FlushTimeout: 100 * time.Millisecond,
    ChannelSize:  1024,
    WorkerBufferSize: 2048,
    FlushThreshold:   2048,
    WriterBufferSize: 4096,
    IncludeCaller: false,
})
```

### High-Throughput Config

```go
logger := loggerj.NewLogger(loggerj.Config{
    JSONOutput:   true,
    OutputFile:   "/var/log/myapp/app.log",
    FlushTimeout:     500 * time.Millisecond,  // Batch more
    ChannelSize:      65536,                   // Huge buffer for spikes
    WorkerBufferSize: 16384,
    FlushThreshold:   16384,
    WriterBufferSize: 32768,
    IncludeCaller: false,
})
```

---

## 13. Advanced Integrations

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
        logsDropped.Add(float64(stats["drops"]))
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

`loggerj` provides an `io.Writer` adapter that allows you to intercept logs from Go's standard `log` package and third-party libraries that rely on it. This ensures that *all* application logs flow through `loggerj`'s high-performance, asynchronous pipeline, benefiting from log rotation, structured formatting, and drop monitoring.

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
 // CRITICAL: Disable std log's default flags (date/time) to avoid duplicate timestamps,
 // as loggerj already adds its own high-precision timestamp.
 log.SetFlags(0)
 log.SetOutput(logger.AsWriter(loggerj.LevelInfo, "STDLIB"))

 // 3. Usage
 // These messages will now be formatted as JSON and processed asynchronously by loggerj.
 log.Println("This is a standard log message intercepted by loggerj.")
 log.Printf("User %s logged in from %s", "admin", "192.168.1.50")

 // You can still use loggerj's native API alongside std log
 logger.InfoString("APP", "Application logic running")

 logger.Flush()
}
```

-> Output Example

When using `JSONOutput: true`, intercepted standard logs will appear as:

```json
{"ts":1704067200123,"level":"INFO","type":"STDLIB","msg":"This is a standard log message intercepted by loggerj."}
{"ts":1704067200456,"level":"INFO","type":"STDLIB","msg":"User admin logged in from 192.168.1.50"}
{"ts":1704067200789,"level":"INFO","type":"APP","msg":"Application logic running"}
```

-> Performance Note

While `loggerj`'s core hot path is strictly zero-allocation, the `AsWriter` adapter incurs a minor allocation (`string(p)`) to convert the `[]byte` input from `std log` into a string. This is acceptable for intercepting legacy or third-party logs but should not be used for the application's primary high-throughput logging path. For maximum performance, prefer `logger.InfoString` or `logger.Info` directly.

---

## 14. Observability

### Caller Info (Debug Only)

```go
logger := loggerj.NewLogger(loggerj.Config{
    IncludeCaller: true,  // Adds file:line to logs
    FlushTimeout:  50 * time.Millisecond,
})
// Output: [1704067200123] INFO [DEBUG] main.go:42 detailed info
// WARNING: Adds ~500ns and 2 allocations per log. Disable in production.
```

### Drop Monitoring

```go
go func() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        drops := logger.Drops()
        if drops > 0 {
            fmt.Fprintf(os.Stderr, "⚠️ WARNING: %d logs dropped in last 5s\n", drops)
            logger.ResetDrops()
        }
    }
}()
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

---

## 15. Buffer Tuning

Fine-tune buffer sizes for your specific workload:

| Parameter | Low Memory | Balanced (Default) | High Performance |
|-----------|------------|--------------------|------------------|
| `ChannelSize` | 512-1024 | 4096 | 16384-65536 |
| `WorkerBufferSize` | 1024-2048 | 4096 | 8192-16384 |
| `FlushThreshold` | 1024-2048 | 4096 | 8192-16384 |
| `WriterBufferSize` | 2048-4096 | 8192 | 16384-32768 |
| `FlushTimeout` | 100ms | 50ms | 10-25ms |

---

## Summary

These examples demonstrate the core philosophy of `loggerj` v2:

1. **Define rules once** using `RegisterSub` (Cold Path).
2. **Log cleanly and rapidly** using `InfoString`/`ErrorString` (Hot Path).
3. **Achieve true zero-allocation** and lock-free concurrency.

For detailed performance metrics, see [BENCH.md](BENCH.md). For API reference, see [README.md](README.md).
