package logger

import (
	"fmt"
	"log/slog"
	"os"
)

func parseLevel(level string) (slog.Level, error) {
	var lvl slog.Level

	err := lvl.UnmarshalText([]byte(level))

	return lvl, err
}

func Setup(logLevel string) error {
	lvl, err := parseLevel(logLevel)
	if err != nil {
		return err
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
	})
	slog.SetDefault(slog.New(handler))

	slog.Info(fmt.Sprintf("Logger initialized with level %s", lvl.String()))

	return nil
}
