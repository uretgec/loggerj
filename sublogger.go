package loggerj

// SubLogger is a handle to a registered SubProfile. It logs under a fixed log
// type, so a typo in the log type cannot silently lose the profile's rate
// limit, sampling and static fields.
//
// The handle skips the profile lookup, but that lookup is cheap (a linear scan
// for up to 8 profiles), so expect no speed difference in typical services;
// the benefit is safety, not speed (see BENCH.md).
//
//	http := logger.Sub("HTTP",
//	    loggerj.WithRateLimit(1000, time.Second),
//	    loggerj.WithFields("service", "gateway"),
//	)
//	http.InfoString("request received", "method", "GET")
//
// A SubLogger is safe for concurrent use. It keeps the profile it was created
// with: registering the same log type again with RegisterSub or Sub replaces
// the profile for the Logger methods but does not affect existing handles.
type SubLogger struct {
	l *Logger
	p *SubProfile
}

// Sub returns a SubLogger for logType.
//
// With options it registers (or replaces) the profile like RegisterSub. Without
// options it reuses the profile already registered for logType and only
// registers an empty profile when there is none, so Sub("HTTP") never resets
// an existing rate limit.
//
// COLD PATH: call Sub during initialization, not inside handlers or hot loops.
func (l *Logger) Sub(logType string, opts ...SubOption) *SubLogger {
	if len(opts) > 0 || l.getProfile(logType) == l.defaultProfile {
		l.RegisterSub(logType, opts...)
	}
	return &SubLogger{l: l, p: l.getProfile(logType)}
}

// Name returns the log type of the handle.
func (s *SubLogger) Name() string { return s.p.Name }

// Debug logs a message at LevelDebug.
func (s *SubLogger) Debug(msg []byte, fields ...string) {
	s.l.logProfile(LevelDebug, s.p, msg, 2, fields...)
}

// Info logs a message at LevelInfo.
func (s *SubLogger) Info(msg []byte, fields ...string) {
	s.l.logProfile(LevelInfo, s.p, msg, 2, fields...)
}

// Warn logs a message at LevelWarn.
func (s *SubLogger) Warn(msg []byte, fields ...string) {
	s.l.logProfile(LevelWarn, s.p, msg, 2, fields...)
}

// Error logs a message at LevelError.
func (s *SubLogger) Error(msg []byte, fields ...string) {
	s.l.logProfile(LevelError, s.p, msg, 2, fields...)
}

// DebugString logs a string message at LevelDebug.
func (s *SubLogger) DebugString(msg string, fields ...string) {
	s.l.logProfile(LevelDebug, s.p, unsafeStringToBytes(msg), 2, fields...)
}

// InfoString logs a string message at LevelInfo.
func (s *SubLogger) InfoString(msg string, fields ...string) {
	s.l.logProfile(LevelInfo, s.p, unsafeStringToBytes(msg), 2, fields...)
}

// WarnString logs a string message at LevelWarn.
func (s *SubLogger) WarnString(msg string, fields ...string) {
	s.l.logProfile(LevelWarn, s.p, unsafeStringToBytes(msg), 2, fields...)
}

// ErrorString logs a string message at LevelError.
func (s *SubLogger) ErrorString(msg string, fields ...string) {
	s.l.logProfile(LevelError, s.p, unsafeStringToBytes(msg), 2, fields...)
}

// DebugFields logs a message at LevelDebug with typed fields.
func (s *SubLogger) DebugFields(msg []byte, fields ...Field) {
	s.l.logTypedProfile(LevelDebug, s.p, msg, 2, fields...)
}

// InfoFields logs a message at LevelInfo with typed fields.
func (s *SubLogger) InfoFields(msg []byte, fields ...Field) {
	s.l.logTypedProfile(LevelInfo, s.p, msg, 2, fields...)
}

// WarnFields logs a message at LevelWarn with typed fields.
func (s *SubLogger) WarnFields(msg []byte, fields ...Field) {
	s.l.logTypedProfile(LevelWarn, s.p, msg, 2, fields...)
}

// ErrorFields logs a message at LevelError with typed fields.
func (s *SubLogger) ErrorFields(msg []byte, fields ...Field) {
	s.l.logTypedProfile(LevelError, s.p, msg, 2, fields...)
}

// passGates runs the sampling and rate-limit gates for a profile that is
// already resolved. It must stay in sync with checkGatesAfterLevel (same
// order: sampling first, then the rate limit); TestSub_GatesMatchLogType
// guards this.
func (l *Logger) passGates(p *SubProfile) bool {
	if p.sampleRate > 0 {
		if p.sampleCount.Add(1)%p.sampleRate != 0 {
			return false
		}
	}
	if p.rlLimit > 0 {
		if !l.checkAtomicRateLimit(p) {
			return false
		}
	}
	return true
}

// logProfile is log() for a resolved profile: the same filtering, gating,
// caller capture and dispatch, without the profile lookup. skip has the same
// meaning as for log: 2 when called from a public method.
func (l *Logger) logProfile(level Level, p *SubProfile, msg []byte, skip int, fields ...string) {
	if level < Level(l.currentLevel.Load()) {
		return
	}
	if !l.passGates(p) {
		return
	}

	file, line := l.captureCaller(skip)

	if l.cfg.SyncMode {
		l.syncWrite(level, p.Name, msg, p, nil, fields, file, line)
		return
	}

	e := l.pool.Get().(*Entry)
	e.Level = level
	e.Type = p.Name
	e.Msg = append(e.Msg[:0], msg...)
	e.File = file
	e.Line = line
	e.Fields = append(e.Fields[:0], fields...)
	e.Profile = p

	l.dispatch(e)
}

// logTypedProfile is logTyped() for a resolved profile.
func (l *Logger) logTypedProfile(level Level, p *SubProfile, msg []byte, skip int, fields ...Field) {
	if level < Level(l.currentLevel.Load()) {
		return
	}
	if !l.passGates(p) {
		return
	}

	file, line := l.captureCaller(skip)

	if l.cfg.SyncMode {
		l.syncWrite(level, p.Name, msg, p, fields, nil, file, line)
		return
	}

	e := l.pool.Get().(*Entry)
	e.Level = level
	e.Type = p.Name
	e.Msg = append(e.Msg[:0], msg...)
	e.File = file
	e.Line = line
	e.FieldsV = append(e.FieldsV[:0], fields...)
	e.Profile = p

	l.dispatch(e)
}
