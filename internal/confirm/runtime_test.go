//go:build windows || darwin

package confirm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDialogHelperProcess(t *testing.T) {
	if os.Getenv("ORACLE_CONFIRM_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	marker := 0
	for i, arg := range args {
		if arg == "--" {
			marker = i
			break
		}
	}
	mode, dir := args[marker+1], args[marker+2]
	if mode == "directory" {
		created, err := newDialogDir()
		if err != nil {
			os.Exit(8)
		}
		os.WriteFile(filepath.Join(created, "result.txt"), []byte(dir), 0600)
		fmt.Print(created)
		os.Exit(0)
	}
	os.WriteFile(filepath.Join(dir, "ui-stages.log"), []byte("PROCESS_READY 1\nUI_SHOWN 5\nUI_HEARTBEAT 10\nCLICK_CANCEL 20\nUI_CLOSED 21\n"), 0600)
	switch mode {
	case "cancel":
		os.WriteFile(filepath.Join(dir, "result.txt"), []byte("0"), 0600)
	case "approve":
		os.WriteFile(filepath.Join(dir, "result.txt"), []byte("1"), 0600)
	case "invalid":
		os.WriteFile(filepath.Join(dir, "result.txt"), []byte("garbage"), 0600)
	case "fail":
		os.WriteFile(filepath.Join(dir, "result.txt"), []byte("1"), 0600)
		fmt.Fprint(os.Stderr, "synthetic dialog failure")
		os.Exit(7)
	}
	os.Exit(0)
}

func helperCommand(ctx context.Context, mode, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDialogHelperProcess$", "--", mode, dir)
	cmd.Env = append(os.Environ(), "ORACLE_CONFIRM_TEST_HELPER=1")
	return cmd
}

func TestDialogFilesIsolatedAcrossProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type response struct {
		dir string
		err error
	}
	results := make(chan response, 2)
	for _, value := range []string{"approval-A", "cancellation-B"} {
		go func(value string) {
			out, err := helperCommand(ctx, "directory", value).Output()
			results <- response{string(out), err}
		}(value)
	}
	paths := []string{}
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if filepath.Dir(result.dir) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(result.dir), "oracle-mcp-confirm-") {
			t.Fatalf("unexpected generated path %q", result.dir)
		}
		paths = append(paths, result.dir)
		t.Cleanup(func() { os.RemoveAll(result.dir) })
	}
	if paths[0] == paths[1] {
		t.Fatal("processes share a dialog directory")
	}
	first, err := os.ReadFile(filepath.Join(paths[0], "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(paths[1], "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) {
		t.Fatal("dialog results collided")
	}
	os.RemoveAll(paths[0])
	if _, err := os.Stat(filepath.Join(paths[1], "result.txt")); err != nil {
		t.Fatal("cleanup removed another process's files")
	}
}

func TestDialogFailureIsNotCancellationAndStagesExcludeSQL(t *testing.T) {
	for _, mode := range []string{"cancel", "approve", "fail", "missing", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var stages []string
			req := &ConfirmRequest{SQL: "SECRET_SQL_MUST_NOT_APPEAR", LogStage: func(stage, detail string) { stages = append(stages, stage+" "+detail) }}
			_, err := runDialog(req, helperCommand(ctx, mode, dir), filepath.Join(dir, "ui-stages.log"))
			if mode == "fail" {
				if err == nil {
					t.Fatal("process failure was treated as cancellation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				result, err := readDialogResult(filepath.Join(dir, "result.txt"))
				if mode == "missing" || mode == "invalid" {
					if err == nil {
						t.Fatal("missing/invalid result treated as cancellation")
					}
				} else if err != nil || result.Approved != (mode == "approve") {
					t.Fatalf("result %v %v", result, err)
				}
			}
			joined := strings.Join(stages, "\n")
			if strings.Contains(joined, req.SQL) || !strings.Contains(joined, "CONFIRM_PROCESS_STARTED") || !strings.Contains(joined, "CONFIRM_PROCESS_EXIT") || !strings.Contains(joined, "UI_SHOWN@5ms") {
				t.Fatalf("incorrect diagnostic stages: %s", joined)
			}
		})
	}
}
