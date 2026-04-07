//go:build windows

package main

import (
	"os"
	"os/exec"
)

func processExists(pid int) bool {
	return pid > 0
}

func configureBackgroundCommand(cmd *exec.Cmd) {
	cmd.Stdin = nil
}

func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
