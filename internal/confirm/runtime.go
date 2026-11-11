//go:build windows || darwin

package confirm

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func newDialogDir() (string, error) {
	return os.MkdirTemp("", fmt.Sprintf("oracle-mcp-confirm-%d-", os.Getpid()))
}

func (r *ConfirmRequest) trace(stage, detail string) {
	if r.LogStage != nil {
		r.LogStage(stage, detail)
	}
}

// Waiting for approval is intentionally not time-limited. Periodic diagnostics
// distinguish a waiting process from a UI whose message loop stopped responding.
// SQL stays in private files and is never included in these diagnostic messages.
func runDialog(r *ConfirmRequest, cmd *exec.Cmd, signalPath string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start confirmation process: %w", err)
	}
	r.trace("CONFIRM_PROCESS_STARTED", fmt.Sprintf("child_pid=%d", cmd.Process.Pid))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	lastSignal := ""
	observe := func(wait bool) {
		stage, age := readDialogSignal(signalPath)
		if stage != "" && stage != lastSignal {
			r.trace("CONFIRM_UI_STAGE", fmt.Sprintf("child_pid=%d ui_stage=%s", cmd.Process.Pid, stage))
			lastSignal = stage
		}
		if wait {
			r.trace("CONFIRM_WAIT", fmt.Sprintf("child_pid=%d elapsed_ms=%d ui_stage=%s ui_signal_age_ms=%d", cmd.Process.Pid, time.Since(started).Milliseconds(), stage, age.Milliseconds()))
		}
	}
	for {
		select {
		case err := <-done:
			observe(false)
			r.trace("CONFIRM_PROCESS_EXIT", fmt.Sprintf("child_pid=%d exit_code=%d elapsed_ms=%d", cmd.Process.Pid, cmd.ProcessState.ExitCode(), time.Since(started).Milliseconds()))
			if err != nil {
				detail := strings.TrimSpace(stderr.String())
				if len(detail) > 4096 {
					detail = detail[:4096] + "..."
				}
				return nil, fmt.Errorf("confirmation process failed: %w; stderr=%q", err, detail)
			}
			return stdout.Bytes(), nil
		case <-ticker.C:
			observe(true)
		}
	}
}

func readDialogSignal(path string) (string, time.Duration) {
	if path == "" {
		return "unknown", -time.Millisecond
	}
	f, err := os.Open(path)
	if err != nil {
		return "unknown", -time.Millisecond
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "unknown", -time.Millisecond
	}
	start := info.Size() - 4096
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return "unknown", -time.Millisecond
	}
	data := make([]byte, 4096)
	n, _ := f.Read(data)
	var stages []string
	for _, line := range strings.Split(string(data[:n]), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		stage := fields[0]
		switch stage {
		case "PROCESS_READY", "UI_CREATED", "UI_SHOWN", "CONTENT_READY", "UI_HEARTBEAT", "CLICK_EXECUTE", "CLICK_CANCEL", "CLICK_ALLOW_KEYWORD", "CLICK_ALLOW_PREFIX", "UI_CLOSED":
			if stage != "UI_HEARTBEAT" {
				if len(fields) > 1 {
					if ms, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						stage += fmt.Sprintf("@%dms", ms)
					}
				}
				stages = append(stages, stage)
			}
		}
	}
	// Keep non-heartbeat milestones visible even when periodic heartbeats follow.
	last := "unknown"
	if len(stages) > 0 {
		last = strings.Join(stages, ",")
	}
	return last, time.Since(info.ModTime())
}

func readDialogResult(path string) (ConfirmResult, error) {
	for i := 0; i < 20; i++ {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			value := strings.TrimSpace(string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
			switch value {
			case "0":
				return ConfirmResult{}, nil
			case "1":
				return ConfirmResult{Approved: true}, nil
			case "allow_prefix":
				return ConfirmResult{Approved: true, AllowPrefix: true}, nil
			}
			return ConfirmResult{}, fmt.Errorf("confirmation process returned an invalid result")
		}
		if i < 19 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return ConfirmResult{}, fmt.Errorf("confirmation process exited without a result file (%s)", filepath.Base(path))
}
