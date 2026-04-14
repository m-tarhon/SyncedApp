package main

import "log"

// Logger encapsulates the three independent logging channels.
type Logger struct {
	Cache      bool
	Timeseries bool
	Splitting  bool
}

// NewLogger creates a logger with the given toggles.
func NewLogger(cache, timeseries, splitting bool) *Logger {
	return &Logger{
		Cache:      cache,
		Timeseries: timeseries,
		Splitting:  splitting,
	}
}

// LogCache logs cache operations (HITs, MISSes, SETs).
func (l *Logger) LogCache(format string, args ...interface{}) {
	if l == nil || !l.Cache {
		return
	}
	log.Printf("[cache] "+format, args...)
}

// LogTimeseries logs timeseries computation and Prometheus response parsing.
func (l *Logger) LogTimeseries(format string, args ...interface{}) {
	if l == nil || !l.Timeseries {
		return
	}
	log.Printf("[timeseries] "+format, args...)
}

// LogSplitting logs query splitting and job routing decisions.
func (l *Logger) LogSplitting(format string, args ...interface{}) {
	if l == nil || !l.Splitting {
		return
	}
	log.Printf("[splitting] "+format, args...)
}
