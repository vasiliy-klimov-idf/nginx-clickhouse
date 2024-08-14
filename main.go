package main

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mintance/nginx-clickhouse/clickhouse"
	configParser "github.com/mintance/nginx-clickhouse/config"
	"github.com/mintance/nginx-clickhouse/nginx"
	"github.com/papertrail/go-tail/follower"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

var (
	locker sync.Mutex
	logs   []string
)

var (
	linesProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "nginx_clickhouse_lines_processed_total",
		Help: "The total number of processed log lines",
	})
	linesNotProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "nginx_clickhouse_lines_not_processed_total",
		Help: "The total number of log lines which was not processed",
	})
)

func main() {
	// Read config & incoming flags
	config := configParser.Read()

	// Update config with environment variables if exist
	config.SetEnvVariables()

	nginxParser, err := nginx.GetParser(config)

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.ListenAndServe(":2112", nil)
	}()

	if err != nil {
		logrus.Fatal("Can`t parse nginx log format: ", err)
	}

	logs = []string{}

	logrus.Info("Trying to open logfile: " + config.Settings.LogPath)

	whenceSeek := io.SeekStart
	if config.Settings.SeekFromEnd {
		whenceSeek = io.SeekEnd
	}

	t, err := follower.New(config.Settings.LogPath, follower.Config{
		Whence: whenceSeek,
		Offset: 0,
		Reopen: true,
	})

	if err != nil {
		logrus.Fatal("Can`t tail logfile: ", err)
	}

	for {
		time.Sleep(time.Second * time.Duration(config.Settings.Interval))

		locker.Lock()
		if len(logs) > 0 {
			logrus.Info("Preparing to save ", len(logs), " new log entries.")
			parsedLogs := nginx.ParseLogs(nginxParser, logs)
			logs = []string{} // очищаем логи сразу после парсинга
			locker.Unlock()

			err := clickhouse.Save(config, parsedLogs)
			if err != nil {
				logrus.Error("Can't save logs: ", err)
				linesNotProcessed.Add(float64(len(parsedLogs)))
			} else {
				logrus.Info("Saved ", len(parsedLogs), " new logs.")
				linesProcessed.Add(float64(len(parsedLogs)))
			}
		} else {
			locker.Unlock()
		}

		// Чтение новой строки логов
		line, ok := <-t.Lines()
		if !ok {
			break // Завершить цикл, если канал закрыт
		}

		trimmedLine := strings.TrimSpace(line.String())
		if trimmedLine != "" {
			locker.Lock()
			logrus.Info("Read new log line: ", trimmedLine)
			logs = append(logs, trimmedLine)
			locker.Unlock()
		}
	}
}
