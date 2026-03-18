package api

import (
	"context"
	"log"
	"time"

	"github.com/fsnotify/fsnotify"
)

// logFileWatcher wraps fsnotify for watching a session log file.
// On creation it tries to set up inotify; if that fails, or if the
// watched file is renamed/removed (log rotation), it falls back to
// polling at outputStreamPollInterval.
type logFileWatcher struct {
	watcher      *fsnotify.Watcher
	fallbackPoll *time.Ticker
	logPath      string
	// onReset is called when the watcher switches to polling due to
	// file rename/remove. Callers should reset their cached file state
	// (size, cursor) so the next read doesn't skip the new file.
	onReset func()
}

// newLogFileWatcher creates a watcher for logPath. If fsnotify is
// unavailable or the file cannot be watched, it falls back to polling.
func newLogFileWatcher(logPath string) *logFileWatcher {
	lw := &logFileWatcher{logPath: logPath}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		lw.fallbackPoll = time.NewTicker(outputStreamPollInterval)
		log.Printf("session stream: fsnotify unavailable for %s, falling back to polling", logPath)
		return lw
	}
	if addErr := watcher.Add(logPath); addErr != nil {
		_ = watcher.Close()
		lw.fallbackPoll = time.NewTicker(outputStreamPollInterval)
		log.Printf("session stream: fsnotify watch failed for %s, falling back to polling", logPath)
		return lw
	}
	lw.watcher = watcher
	return lw
}

// Close releases watcher or ticker resources.
func (lw *logFileWatcher) Close() {
	if lw.watcher != nil {
		lw.watcher.Close() //nolint:errcheck
	}
	if lw.fallbackPoll != nil {
		lw.fallbackPoll.Stop()
	}
}

// switchToPolling closes the fsnotify watcher and starts polling instead.
// Calls onReset if set so callers can invalidate cached file state.
func (lw *logFileWatcher) switchToPolling(reason string) {
	if lw.watcher != nil {
		lw.watcher.Close() //nolint:errcheck
		lw.watcher = nil
	}
	if lw.fallbackPoll == nil {
		lw.fallbackPoll = time.NewTicker(outputStreamPollInterval)
		log.Printf("session stream: %s for %s, switching to polling", reason, lw.logPath)
	}
	if lw.onReset != nil {
		lw.onReset()
	}
}

// Run executes the main event loop. It calls readAndEmit on file changes
// and writeKeepalive on keepalive ticks. Blocks until ctx is canceled.
func (lw *logFileWatcher) Run(ctx context.Context, readAndEmit func(), writeKeepalive func()) {
	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()

	// Emit initial state immediately.
	readAndEmit()

	for {
		if lw.watcher != nil {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-lw.watcher.Events:
				if !ok {
					return
				}
				if ev.Has(fsnotify.Write) {
					readAndEmit()
				}
				if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
					lw.switchToPolling("file removed/renamed")
					readAndEmit()
				}
			case err, ok := <-lw.watcher.Errors:
				if !ok {
					return
				}
				lw.switchToPolling("watcher error: " + err.Error())
			case <-keepalive.C:
				writeKeepalive()
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case <-lw.fallbackPoll.C:
				readAndEmit()
			case <-keepalive.C:
				writeKeepalive()
			}
		}
	}
}
