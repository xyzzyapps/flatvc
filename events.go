package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// RunEvent checks if an event trigger exists under .flatvc_db/events/ and executes it.
// If the event script returns a non-zero exit code, it returns an error.
func RunEvent(repoRoot, eventName string, args []string) error {
	eventsDir := filepath.Join(repoRoot, FlatvcDir, EventsDir)

	var scriptPath string
	var cmdName string
	var cmdArgs []string

	if runtime.GOOS == "windows" {
		// Extensions to check on Windows in order of preference
		exts := []string{"", ".exe", ".bat", ".cmd", ".ps1"}
		for _, ext := range exts {
			testPath := filepath.Join(eventsDir, eventName+ext)
			if fi, err := os.Stat(testPath); err == nil && !fi.IsDir() {
				scriptPath = testPath
				if ext == ".ps1" {
					cmdName = "powershell"
					cmdArgs = append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath}, args...)
				} else if ext == ".bat" || ext == ".cmd" {
					cmdName = "cmd"
					cmdArgs = append([]string{"/C", scriptPath}, args...)
				} else {
					cmdName = scriptPath
					cmdArgs = args
				}
				break
			}
		}
	} else {
		// On Unix/macOS, check if the base script name exists
		testPath := filepath.Join(eventsDir, eventName)
		if fi, err := os.Stat(testPath); err == nil && !fi.IsDir() {
			scriptPath = testPath
			cmdName = scriptPath
			cmdArgs = args
		}
	}

	// If no script matches, it's a no-op (succeeds silently)
	if scriptPath == "" {
		return nil
	}

	cmd := exec.Command(cmdName, cmdArgs...)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("event script '%s' failed: %w", eventName, err)
	}

	return nil
}
