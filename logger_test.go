package main

import (
	"bytes"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/go-kit/log/term"
)

// 风险：时间格式不符合约定，行号指向日志封装层，或文件中写入颜色控制符。
func TestLoggerMetadataAndPlainOutput(t *testing.T) {
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
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("file output contains terminal colors: %q", got)
	}
	if !strings.Contains(got, "level=info") || !strings.Contains(got, `msg="request completed"`) {
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
	if !strings.Contains(output.String(), "level=error") || strings.Contains(output.String(), "hidden") {
		t.Fatalf("error missing or filtered info leaked: %q", output.String())
	}
}

// 风险：无法识别实际日志级别，导致终端没有颜色或错误使用其他级别的颜色。
func TestLoggerSeverityColors(t *testing.T) {
	cases := []struct {
		name   string
		wrap   func(log.Logger) log.Logger
		prefix string
	}{
		{"info green", level.Info, "\x1b[32;1m"},
		{"warn yellow", level.Warn, "\x1b[33;1m"},
		{"error red", level.Error, "\x1b[31;1m"},
		{"debug gray", level.Debug, "\x1b[37m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := term.NewColorLogger(&output, log.NewLogfmtLogger, logColors)
			if err := tc.wrap(logger).Log("msg", "color check"); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(output.String(), tc.prefix) {
				t.Fatalf("incorrect color: %q", output.String())
			}
			if !strings.Contains(output.String(), "color check") {
				t.Fatalf("colored log message lost: %q", output.String())
			}
		})
	}
}
