package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
)

// 风险：时间格式或调用位置不正确，重定向到文件后颜色丢失。
func TestLoggerMetadataAndForcedColor(t *testing.T) {
	var output bytes.Buffer
	logger := newLogger(&output, "info")
	_, file, line, _ := runtime.Caller(0)
	err := level.Info(logger).Log("msg", "request completed")
	if err != nil {
		t.Fatal(err)
	}
	got := output.String()
	pattern := regexp.MustCompile(`(?:^| )time=(\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3})(?: |$)`)
	match := pattern.FindStringSubmatch(got)
	if len(match) != 2 {
		t.Fatalf("time must use MM-DDTHH:mm:ss.SSS: %q", got)
	}
	if _, err := time.Parse("01-02T15:04:05.000", match[1]); err != nil {
		t.Fatal(err)
	}
	wantCaller := filepath.Base(file) + ":" + strconv.Itoa(line+1)
	if !strings.Contains(got, "caller="+wantCaller+" ") {
		t.Fatalf("caller must identify the actual log statement %s: %q", wantCaller, got)
	}
	if !strings.HasPrefix(got, "level=\x1b[32;1minfo\x1b[0m ") {
		t.Fatalf("only the info word should be colored, including file output: %q", got)
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(got, "")
	if !strings.Contains(plain, "level=info") || !strings.Contains(plain, `msg="request completed"`) {
		t.Fatalf("log level or message lost: %q", got)
	}
}

// 风险：增加格式包装后，原有 --log 级别过滤不再生效。
func TestLoggerLevelFilter(t *testing.T) {
	var output bytes.Buffer
	logger := newLogger(&output, "warn")
	_ = level.Info(logger).Log("msg", "hidden")
	if output.Len() != 0 {
		t.Fatalf("info must be filtered at warn level: %q", output.String())
	}
	_ = level.Error(logger).Log("msg", "visible")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, "level=error") || strings.Contains(plain, "hidden") {
		t.Fatalf("error missing or filtered info leaked: %q", output.String())
	}
}

// 风险：无法识别实际日志级别，导致终端没有颜色或错误使用其他级别的颜色。
func TestLoggerSeverityColors(t *testing.T) {
	cases := []struct {
		name   string
		wrap   func(log.Logger) log.Logger
		prefix string
		value  string
	}{
		{"info green", level.Info, "\x1b[32;1m", "info"},
		{"warn yellow", level.Warn, "\x1b[33;1m", "warn"},
		{"error red", level.Error, "\x1b[31;1m", "error"},
		{"debug gray", level.Debug, "\x1b[37m", "debug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := newLogger(&output, "debug")
			if err := tc.wrap(logger).Log("msg", "color check info warn error debug level=error", "body", "level=info payload"); err != nil {
				t.Fatal(err)
			}
			wantPrefix := "level=" + tc.prefix + tc.value + "\x1b[0m "
			if !strings.HasPrefix(output.String(), wantPrefix) {
				t.Fatalf("only the severity word should be colored: %q", output.String())
			}
			if strings.Contains(strings.TrimPrefix(output.String(), wantPrefix), "\x1b[") {
				t.Fatalf("color extends to metadata, message or body: %q", output.String())
			}
			if !strings.Contains(output.String(), "color check info warn error debug level=error") ||
				!strings.Contains(output.String(), `body="level=info payload"`) {
				t.Fatalf("message or body changed while coloring severity: %q", output.String())
			}
		})
	}
}

type loggerTestWriter func([]byte) (int, error)

func (w loggerTestWriter) Write(p []byte) (int, error) {
	return w(p)
}

// 风险：着色输出层吞掉写入错误或短写，使日志丢失却被报告为成功。
func TestLoggerWriteErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	cases := []struct {
		name   string
		writer loggerTestWriter
		want   error
	}{
		{"write error", func([]byte) (int, error) { return 0, writeErr }, writeErr},
		{"short write", func(p []byte) (int, error) { return len(p) - 1, nil }, io.ErrShortWrite},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := level.Info(newLogger(tc.writer, "info")).Log("msg", "write check")
			if !errors.Is(err, tc.want) {
				t.Fatalf("write failure lost: got=%v want=%v", err, tc.want)
			}
		})
	}
}
