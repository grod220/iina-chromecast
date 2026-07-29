package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Verbosity levels. -v selects the highest level that gets printed.
const (
	lvlError = iota
	lvlWarn
	lvlInfo
	lvlDebug
	lvlTrace
)

var levelNames = map[int]string{
	lvlError: "ERROR",
	lvlWarn:  "WARN ",
	lvlInfo:  "INFO ",
	lvlDebug: "DEBUG",
	lvlTrace: "TRACE",
}

var (
	logger         = log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds|log.Lshortfile)
	logLevel int32 = lvlDebug
)

// setupLogging points every log line at stderr and (if possible) a file, so the
// output survives IINA swallowing the helper's stderr. stdout stays clean —
// the plugin reads it looking for "READY".
func setupLogging(path string, level int) {
	atomic.StoreInt32(&logLevel, int32(level))

	w := io.Writer(os.Stderr)
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			logger.Printf("[ERROR] cannot open log file %s: %v (stderr only)", path, err)
		} else {
			w = io.MultiWriter(os.Stderr, f)
		}
	}
	logger.SetOutput(w)

	// The stdlib logger is used by net/http for its own errors; send it to the
	// same place.
	log.SetOutput(w)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

func defaultLogPath() string {
	return filepath.Join(os.TempDir(), "casthelper.log")
}

func logAt(level int, format string, args ...any) {
	if int(atomic.LoadInt32(&logLevel)) < level {
		return
	}
	// calldepth 3: logAt -> logXxx -> caller
	_ = logger.Output(3, "["+levelNames[level]+"] "+fmt.Sprintf(format, args...))
}

func logError(format string, args ...any) { logAt(lvlError, format, args...) }
func logWarn(format string, args ...any)  { logAt(lvlWarn, format, args...) }
func logInfo(format string, args ...any)  { logAt(lvlInfo, format, args...) }
func logDebug(format string, args ...any) { logAt(lvlDebug, format, args...) }
func logTrace(format string, args ...any) { logAt(lvlTrace, format, args...) }

func traceEnabled() bool { return int(atomic.LoadInt32(&logLevel)) >= lvlTrace }

// truncate keeps long JSON payloads (device status blobs run to several KB)
// from drowning the log.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(%d bytes total)", len(s))
}

// hexdump renders wire bytes for the protobuf-level trace logging.
func hexdump(b []byte, max int) string {
	if len(b) > max {
		return hex.EncodeToString(b[:max]) + fmt.Sprintf("...(%d bytes total)", len(b))
	}
	return hex.EncodeToString(b)
}
