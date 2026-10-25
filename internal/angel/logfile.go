package angel

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"time"
)

// Log levels of the angel log, lowest first.
const (
	LevelDebug = "DEBUG"
	LevelInfo  = "INFO"
	LevelWarn  = "WARN"
	LevelError = "ERROR"
)

// LogEntry is one line of run/netivri.log.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

const (
	// logTailBytes is how much of the end of the log a read looks at, so a huge
	// file never costs more than this.
	logTailBytes = 4 << 20
	// MaxLogBytes is the size above which the log is moved aside at spawn.
	MaxLogBytes = 8 << 20
	// legacyLogPrefix and legacyLogLayout describe the lines of the plain
	// log.Logger the angel wrote before the leveled format.
	legacyLogPrefix = "netivri-daemon "
	legacyLogLayout = "2006/01/02 15:04:05"
	// FollowEvery is how often a followed log is looked at.
	FollowEvery = 500 * time.Millisecond
)

// LevelRank orders the levels; an unknown level is -1.
func LevelRank(level string) int {
	switch level {
	case LevelDebug:
		return 0
	case LevelInfo:
		return 1
	case LevelWarn:
		return 2
	case LevelError:
		return 3
	}
	return -1
}

// NormalizeLevel maps a user-typed level (any case, "warning") to a level
// constant.
func NormalizeLevel(s string) (string, bool) {
	l := strings.ToUpper(strings.TrimSpace(s))
	if l == "WARNING" {
		l = LevelWarn
	}
	return l, LevelRank(l) >= 0
}

// FormatLogLine renders one log line without the trailing newline. A message
// is kept on one line.
func FormatLogLine(t time.Time, level, msg string) string {
	msg = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(msg)
	return t.UTC().Format(time.RFC3339) + " " + level + " " + msg
}

// ParseLogLine reads a line of the leveled format, or of the legacy format
// (taken as INFO). Any other line (a panic trace, say) is not an entry.
func ParseLogLine(line string) (LogEntry, bool) {
	line = strings.TrimRight(line, "\r\n")
	if rest, ok := strings.CutPrefix(line, legacyLogPrefix); ok {
		if len(rest) > len(legacyLogLayout) {
			if t, err := time.ParseInLocation(legacyLogLayout, rest[:len(legacyLogLayout)], time.UTC); err == nil {
				return LogEntry{Time: t, Level: LevelInfo, Message: strings.TrimPrefix(rest[len(legacyLogLayout):], " ")}, true
			}
		}
		return LogEntry{}, false
	}
	stamp, rest, ok := strings.Cut(line, " ")
	if !ok {
		return LogEntry{}, false
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return LogEntry{}, false
	}
	level, msg, _ := strings.Cut(rest, " ")
	if LevelRank(level) < 0 {
		return LogEntry{}, false
	}
	return LogEntry{Time: t.UTC(), Level: level, Message: msg}, true
}

// LogQuery selects entries: at least MinLevel (empty = all), not before Since
// (zero = all) and, when Tail > 0, only the last Tail of them.
type LogQuery struct {
	MinLevel string
	Since    time.Time
	Tail     int
}

func (q LogQuery) match(e LogEntry) bool {
	if q.MinLevel != "" && LevelRank(e.Level) < LevelRank(q.MinLevel) {
		return false
	}
	return q.Since.IsZero() || (!e.Time.IsZero() && !e.Time.Before(q.Since))
}

// logParser turns lines into entries. A line that is not an entry belongs to
// the entry before it and takes its time and level, so a trace after an error
// line stays an error.
type logParser struct{ prev LogEntry }

func (p *logParser) entry(line string) (LogEntry, bool) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return LogEntry{}, false
	}
	if e, ok := ParseLogLine(line); ok {
		p.prev = e
		return e, true
	}
	level := p.prev.Level
	if level == "" {
		level = LevelInfo
	}
	return LogEntry{Time: p.prev.Time, Level: level, Message: line}, true
}

// ReadLog returns the entries of the angel log that match q and the offset
// where the read stopped, to continue a follow from. A missing log is
// os.ErrNotExist.
func ReadLog(home string, q LogQuery) ([]LogEntry, int64, error) {
	data, end, err := readLogRange(LogPath(home), -1)
	if err != nil {
		return nil, 0, err
	}
	var p logParser
	var out []LogEntry
	for _, line := range strings.Split(string(data), "\n") {
		if e, ok := p.entry(line); ok && q.match(e) {
			out = append(out, e)
		}
	}
	if q.Tail > 0 && len(out) > q.Tail {
		out = out[len(out)-q.Tail:]
	}
	return out, end, nil
}

// readLogRange reads the log from offset from (-1 = the last logTailBytes)
// up to the last complete line. It returns the data and the offset after it.
// A file smaller than from restarts at 0, as after a rotation.
func readLogRange(path string, from int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := info.Size()
	start := from
	switch {
	case from < 0:
		start = max(size-logTailBytes, 0)
	case from > size:
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, 0, err
	}
	data := make([]byte, size-start)
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, 0, err
	}
	if from < 0 && start > 0 {
		// The first line is cut by the tail window.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
			start += int64(i + 1)
		}
	}
	// A follow takes complete lines only: the angel may be in the middle of a
	// write. A one-shot read also takes a last line without its newline.
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 && from >= 0 {
		data = data[:i+1]
	} else if i < 0 && from >= 0 {
		data = nil
	}
	return data, start + int64(len(data)), nil
}

// FollowLog calls emit for every new entry that matches q, polling the log
// until ctx ends, starting at offset from (see ReadLog). A log that shrinks
// (rotation) is read again from its start; a missing log is waited for.
func FollowLog(ctx context.Context, home string, from int64, q LogQuery, emit func(LogEntry)) {
	var p logParser
	offset := from
	tick := time.NewTicker(FollowEvery)
	defer tick.Stop()
	for {
		data, end, err := readLogRange(LogPath(home), offset)
		if err == nil {
			offset = end
			for _, line := range strings.Split(string(data), "\n") {
				if e, ok := p.entry(line); ok && q.match(e) {
					emit(e)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// RotateLog moves run/netivri.log to netivri.log.1 (replacing an older one)
// when it is bigger than maxBytes, so the log of a long-lived install stays
// bounded. A missing log is not an error.
func RotateLog(home string, maxBytes int64) error {
	info, err := os.Stat(LogPath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() <= maxBytes {
		return nil
	}
	old := LogPath(home) + ".1"
	_ = os.Remove(old)
	return os.Rename(LogPath(home), old)
}
