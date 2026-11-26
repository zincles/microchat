// Package logx provides the backend's structured text logger.
package logx

import (
	"fmt"
	"os"
	"time"
)

const timestampLayout = "2006-01-02T15:04:05.000-07:00"

func Debugf(format string, args ...any) { write("DEBUG", format, args...) }
func Infof(format string, args ...any)  { write("INFO", format, args...) }
func Warnf(format string, args ...any)  { write("WARN", format, args...) }
func Errorf(format string, args ...any) { write("ERROR", format, args...) }
func Fatalf(format string, args ...any) { write("FATAL", format, args...) }

func write(level, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(os.Stderr, "%s [%s] %s\n", time.Now().In(time.Local).Format(timestampLayout), level, message)
}
