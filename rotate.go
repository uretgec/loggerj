package loggerj

import (
	"bufio"
	"fmt"
	"os"
)

// -----------------------------------------------------------------------------
// File Rotation & I/O
// -----------------------------------------------------------------------------

// openLogFile opens (or reopens) the configured log file and initializes
// the buffered writer. Called during NewLogger and after rotation.
func (l *Logger) openLogFile() error {
	l.rotationMu.Lock()
	defer l.rotationMu.Unlock()

	if l.currentFile != nil {
		if err := l.currentWriter.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "loggerj: flush error on reopen: %v\n", err)
		}
		l.currentFile.Close()
	}

	f, err := os.OpenFile(l.outputFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, l.cfg.FileMode)
	if err != nil {
		return err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	l.currentFile = f
	l.currentWriter = bufio.NewWriterSize(f, l.cfg.WriterBufferSize)
	l.currentSize = stat.Size()

	l.globalWriterMu.Lock()
	l.globalWriter = l.currentWriter
	l.globalWriterMu.Unlock()
	return nil
}

// rotateIfNeeded rotates the log file when it exceeds MaxFileSize and returns
// the bufio.Writer the worker must use afterwards. Errors are reported to
// stderr and counted in Stats().RotationErrors.
func (l *Logger) rotateIfNeeded(bw *bufio.Writer) *bufio.Writer {
	if l.cfg.OutputFile == "" {
		return bw
	}
	rotated, err := l.rotateLogFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loggerj: rotation error: %v\n", err)
	}
	if rotated {
		return l.currentWriter
	}
	return bw
}

// reopenAppendLocked reopens the log file in append mode WITHOUT truncating
// it. Callers must hold rotationMu.
func (l *Logger) reopenAppendLocked() error {
	f, err := os.OpenFile(l.outputFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, l.cfg.FileMode)
	if err != nil {
		return err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	l.currentFile = f
	l.currentWriter = bufio.NewWriterSize(f, l.cfg.WriterBufferSize)
	l.currentSize = stat.Size()

	l.globalWriterMu.Lock()
	l.globalWriter = l.currentWriter
	l.globalWriterMu.Unlock()
	return nil
}

// rotateLogFile performs size-based log rotation. It returns (true, nil) if
// rotation occurred, (false, nil) if no rotation was needed. It returns
// rotated=true together with a non-nil error when rotation was skipped but the
// log file was reopened (the caller must refresh its bufio.Writer in every
// rotated=true case). It returns (false, err) on failures after which no
// usable writer exists.
//
// Steps: (1) move the live log to OutputFile.rotating; if this fails nothing
// else is touched and the live log keeps being appended to (it is never
// truncated); (2) shift backups .N-1 -> .N ... .1 -> .2; (3) move
// OutputFile.rotating to OutputFile.1; (4) create a fresh log file.
// MaxBackupFiles=N keeps N backups (.1 ... .N); 0 keeps a single backup (.1).
//
// HONEST SCOPE DISCLOSURE:
//   - Size-based rotation ONLY. No time-based (daily/hourly) rotation.
//   - No compression (gzip). Backup files are plain text copies.
//   - Async mode only; SyncMode does not rotate.
//   - This is a SUBSET of lumberjack's functionality, traded for zero
//     external dependencies.
//   - Rotation failures are counted in Stats().RotationErrors and logged
//     to stderr. A persistent permission error (e.g., read-only disk)
//     keeps the log growing without rotation and reports an error on every
//     flush attempt; monitor RotationErrors.
//   - For production systems requiring daily rotation, compression, or
//     remote shipping, use lumberjack or a dedicated log shipper as the
//     underlying io.Writer via StartWithWriter().
func (l *Logger) rotateLogFile() (rotated bool, err error) {
	if l.cfg.MaxFileSize <= 0 || l.currentSize < l.cfg.MaxFileSize {
		return false, nil
	}

	l.rotationMu.Lock()
	defer l.rotationMu.Unlock()

	if l.currentWriter != nil {
		if err := l.currentWriter.Flush(); err != nil {
			l.rotationErrors.Add(1)
			fmt.Fprintf(os.Stderr, "loggerj: flush before rotation failed: %v\n", err)
		}
	}
	if l.currentFile != nil {
		if err := l.currentFile.Close(); err != nil {
			l.rotationErrors.Add(1)
			fmt.Fprintf(os.Stderr, "loggerj: close before rotation failed: %v\n", err)
		}
	}

	// Step 1: move the live log aside. If this fails, keep appending to the
	// existing file instead of truncating it (which would destroy the log).
	rotating := l.outputFilePath + ".rotating"
	if err := os.Rename(l.outputFilePath, rotating); err != nil {
		l.rotationErrors.Add(1)
		if rerr := l.reopenAppendLocked(); rerr != nil {
			return false, fmt.Errorf("rename current log failed (%v) and reopen failed: %w", err, rerr)
		}
		return true, fmt.Errorf("rename current log: %w (rotation skipped, still appending to the existing file)", err)
	}

	// Step 2: shift existing backups: .4 -> .5, .3 -> .4, .2 -> .3, .1 -> .2.
	// os.Rename replaces the destination, so the oldest backup (.N) is
	// overwritten by .N-1 and needs no separate removal.
	for i := l.cfg.MaxBackupFiles - 1; i > 0; i-- {
		src := fmt.Sprintf("%s.%d", l.outputFilePath, i)
		dst := fmt.Sprintf("%s.%d", l.outputFilePath, i+1)
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			l.rotationErrors.Add(1)
			fmt.Fprintf(os.Stderr, "loggerj: failed to rotate backup %d -> %d: %v\n", i, i+1, err)
		}
	}

	// Step 3: the rotated log becomes backup .1.
	if err := os.Rename(rotating, l.outputFilePath+".1"); err != nil {
		l.rotationErrors.Add(1)
		fmt.Fprintf(os.Stderr, "loggerj: failed to move %s to backup .1 (left in place): %v\n", rotating, err)
	}

	// Step 4: create a fresh log file.
	f, err := os.OpenFile(l.outputFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, l.cfg.FileMode)
	if err != nil {
		return false, fmt.Errorf("failed to open new log file: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return false, fmt.Errorf("failed to stat new log file: %w", err)
	}

	l.currentFile = f
	l.currentWriter = bufio.NewWriterSize(f, l.cfg.WriterBufferSize)
	l.currentSize = stat.Size()

	l.globalWriterMu.Lock()
	l.globalWriter = l.currentWriter
	l.globalWriterMu.Unlock()

	return true, nil
}
