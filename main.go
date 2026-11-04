package main

import (
	"io"
	"net/http"
	"os"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/go-kit/log/term"
	"github.com/prometheus/common/version"
)

func main() {
	app := kingpin.New("loki-label-proxy", "Proxy to enforce LogQL stream labels").Version(version.Print("loki-label-proxy"))
	app.HelpFlag.Short('h')
	listen := app.Flag("listen", "HTTP listen address, for example :8080 or 127.0.0.1:8080.").Default(":8080").String()
	addr := app.Flag("addr", "Server address. Can also be set using LOKI_ADDR env var.").Default("http://localhost:3100").Envar("LOKI_ADDR").String()
	lokiUser := app.Flag("loki-user", "Username for connection to Loki. Can also be set using LOKI_USERNAME env var.").Default("").Envar("LOKI_USERNAME").String()
	lokiPass := app.Flag("loki-pass", "Password for connection to Loki. Can also be set using LOKI_PASSWORD env var.").Default("").Envar("LOKI_PASSWORD").String()
	authUser := app.Flag("auth-user", "Username for HTTP basic auth. (Enables auth to proxy itself)").Default("").String()
	authPassSha := app.Flag("auth-pass-sha256", "sha256 of password for HTTP basic auth.").Default("").String()
	config := app.Flag("config", "Path to config files/dirs (repeated).").Default("/configs").Strings()
	logLevel := app.Flag("log", "Log filtering level (info, debug)").Default("info").Enum("error", "warn", "info", "debug")
	kingpin.MustParse(app.Parse(os.Args[1:]))

	logger := newLogger(os.Stdout, *logLevel)

	cfg := newConfig(config, logger)
	enforcer := newEnforcer(addr, lokiUser, lokiPass, authUser, authPassSha, cfg, logger)

	level.Info(logger).Log("msg", "Listening on "+*listen)
	level.Info(logger).Log(http.ListenAndServe(*listen, newHTTPHandler(enforcer)))
}

func newHTTPHandler(enforcer *Enforcer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", enforcer.NotFound)
	mux.HandleFunc("/healthz", enforcer.Health)
	// https://grafana.com/docs/loki/latest/api/
	mux.HandleFunc("/loki/api/v1/status/buildinfo", enforcer.Pass)
	mux.HandleFunc("/loki/api/v1/format_query", enforcer.Pass)
	mux.HandleFunc("/loki/api/v1/label", enforcer.Labels)  // grafana v8.5
	mux.HandleFunc("/loki/api/v1/labels", enforcer.Labels) // grafana v9
	mux.HandleFunc("/loki/api/v1/label/", enforcer.Labels)
	mux.HandleFunc("/loki/api/v1/query", enforcer.Query)
	mux.HandleFunc("/loki/api/v1/query_range", enforcer.Query)
	mux.HandleFunc("/loki/api/v1/series", enforcer.Series)
	mux.HandleFunc("/loki/api/v1/tail", enforcer.Query)
	mux.HandleFunc("/loki/api/v1/index/stats", enforcer.Query)
	mux.HandleFunc("/loki/api/v1/index/volume", enforcer.Query)
	mux.HandleFunc("/loki/api/v1/index/volume_range", enforcer.Query)
	return mux
}

// newLogger 为所有应用日志增加本地时间和调用位置；终端和日志文件均保留颜色。
func newLogger(output io.Writer, logLevel string) log.Logger {
	logger := term.NewColorLogger(output, log.NewLogfmtLogger, logColors)
	logger = log.NewSyncLogger(logger)
	logger = level.NewFilter(logger, level.Allow(level.ParseDefault(logLevel, level.InfoValue())))
	// 时间格式示例：11-04T18:30:25.123；caller 指向实际调用 Log 的文件和行号。
	return log.With(logger, "time", log.TimestampFormat(time.Now, "01-02T15:04:05.000"), "caller", log.DefaultCaller)
}

// logColors 按级别选择前景色；重定向到文件时也写入 ANSI 颜色控制符。
func logColors(keyvals ...interface{}) term.FgBgColor {
	for i := 0; i+1 < len(keyvals); i += 2 {
		if keyvals[i] != "level" {
			continue
		}
		switch keyvals[i+1] {
		case level.InfoValue():
			return term.FgBgColor{Fg: term.Green}
		case level.WarnValue():
			return term.FgBgColor{Fg: term.Yellow}
		case level.ErrorValue():
			return term.FgBgColor{Fg: term.Red}
		case level.DebugValue():
			return term.FgBgColor{Fg: term.Gray}
		}
	}
	return term.FgBgColor{}
}
