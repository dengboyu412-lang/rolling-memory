// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

//go:build !windows

package rollingmemory

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// startDetached runs a helper in a new session so it survives the console
// process whose restart it is about to perform.
func startDetached(logPath, command string, args ...string) error {
	if dir := filepath.Dir(logPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(command, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
