// This file contains shellout functionality that is specific to UNIX-like
// operating systems.

// +build !windows

package util

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

func (s *ShellOut) cmd() (*exec.Cmd, error) {
	if s.Timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
		s.cancelFunc = cancel
		return exec.CommandContext(ctx, "/bin/sh", "-c", s.Command), nil
	}
	return exec.Command("/bin/sh", "-c", s.Command), nil
}

// noQuotesNeeded is a regexp for detecting which variable values do not require
// escaping and quote-wrapping in escapeVarValue()
var noQuotesNeeded = regexp.MustCompile(`^[\w/@%=:.,+-]*$`)

// escapeVarValue takes a string, and wraps it in single-quotes so that it will
// be interpretted as a single arg in a shell-out command line. If the value
// already contained any single-quotes, they will be escaped in a way that will
// cause /bin/sh -c to still interpret them as part of a single arg.
func escapeVarValue(value string) string {
	if noQuotesNeeded.MatchString(value) {
		return value
	}
	return fmt.Sprintf("'%s'", strings.ReplaceAll(value, "'", `'"'"'`))
}
