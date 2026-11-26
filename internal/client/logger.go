package client

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

type logEvent struct {
	Time   string `json:"ts"`
	Level  string `json:"level"`
	Module string `json:"module"`
	Msg    string `json:"msg"`
}

var (
	logColors = map[string]string{
		"INFO":  "\033[36m",
		"WARN":  "\033[33m",
		"ERROR": "\033[31m",
	}
	logLevelToInt = map[string]int{
		"":      -1,
		"DEBUG": 0,
		"INFO":  1,
		"WARN":  2,
		"ERROR": 3,
	}
)

type stderrLogger struct {
	out   io.Writer
	mod   string
	color bool
	min   int
}

func NewStderrLogger(module string, level string, colorful bool) waLog.Logger {
	return &stderrLogger{
		out:   os.Stderr,
		mod:   module,
		color: colorful,
		min:   logLevelToInt[strings.ToUpper(level)],
	}
}

func (s *stderrLogger) outputf(level, msg string, args ...interface{}) {
	if logLevelToInt[level] < s.min {
		return
	}
	var colorStart, colorReset string
	if s.color {
		colorStart = logColors[level]
		if colorStart != "" {
			colorReset = "\033[0m"
		}
	}
	fmt.Fprintf(s.out, "%s%s [%s %s] %s%s\n",
		time.Now().Format("15:04:05.000"),
		colorStart, s.mod, level, fmt.Sprintf(msg, args...), colorReset,
	)
}

func (s *stderrLogger) Errorf(msg string, args ...interface{}) { s.outputf("ERROR", msg, args...) }
func (s *stderrLogger) Warnf(msg string, args ...interface{})  { s.outputf("WARN", msg, args...) }
func (s *stderrLogger) Infof(msg string, args ...interface{})  { s.outputf("INFO", msg, args...) }
func (s *stderrLogger) Debugf(msg string, args ...interface{}) { s.outputf("DEBUG", msg, args...) }

func (s *stderrLogger) Sub(mod string) waLog.Logger {
	return &stderrLogger{
		out:   s.out,
		mod:   fmt.Sprintf("%s/%s", s.mod, mod),
		color: s.color,
		min:   s.min,
	}
}

type jsonLogger struct {
	mod string
	min int
	enc *json.Encoder
}

func newJSONLogger(module string, level string) waLog.Logger {
	return &jsonLogger{
		mod: module,
		min: logLevelToInt[strings.ToUpper(level)],
		enc: json.NewEncoder(os.Stderr),
	}
}

func (j *jsonLogger) output(level, msg string) {
	if logLevelToInt[level] < j.min {
		return
	}
	_ = j.enc.Encode(logEvent{
		Time:   time.Now().Format(time.RFC3339Nano),
		Level:  level,
		Module: j.mod,
		Msg:    msg,
	})
}

func (j *jsonLogger) Errorf(msg string, args ...interface{}) {
	j.output("ERROR", fmt.Sprintf(msg, args...))
}
func (j *jsonLogger) Warnf(msg string, args ...interface{}) {
	j.output("WARN", fmt.Sprintf(msg, args...))
}
func (j *jsonLogger) Infof(msg string, args ...interface{}) {
	j.output("INFO", fmt.Sprintf(msg, args...))
}
func (j *jsonLogger) Debugf(msg string, args ...interface{}) {
	j.output("DEBUG", fmt.Sprintf(msg, args...))
}

func (j *jsonLogger) Sub(mod string) waLog.Logger {
	return &jsonLogger{
		mod: fmt.Sprintf("%s/%s", j.mod, mod),
		min: j.min,
		enc: j.enc,
	}
}

func NewJSONLogger(module string, level string) waLog.Logger {
	return newJSONLogger(module, level)
}
