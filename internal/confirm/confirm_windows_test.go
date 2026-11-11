//go:build windows

package confirm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLargeSQLUsesFullNativeTextWithoutHTML(t *testing.T) {
	dir := t.TempDir()
	sql := strings.Repeat("select '大 SQL' from dual;\n", 16000)
	mode, err := writeReviewContent(&ConfirmRequest{SQL: sql}, filepath.Join(dir, "sql.html"), filepath.Join(dir, "sql.txt"))
	if err != nil || mode != "plain" {
		t.Fatalf("mode=%s error=%v", mode, err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "sql.txt"))
	if err != nil || string(content) != sql {
		t.Fatal("large SQL was truncated or changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "sql.html")); !os.IsNotExist(err) {
		t.Fatal("large SQL still produced expensive HTML")
	}
	mode, err = writeReviewContent(&ConfirmRequest{SQL: "delete from demo"}, filepath.Join(dir, "small.html"), filepath.Join(dir, "small.txt"))
	if err != nil || mode != "html" {
		t.Fatal("small SQL lost highlighted rendering")
	}
}

// Opt-in integration test runs only our own form off-screen, with no database
// client and no user SQL. A UI timer must fire while the large text is displayed.
func TestLargeSQLNativeDialogResponds(t *testing.T) {
	if os.Getenv("ORACLE_CONFIRM_GUI_TEST") != "1" {
		t.Skip("set ORACLE_CONFIRM_GUI_TEST=1 to test an off-screen WinForms dialog")
	}
	dir := t.TempDir()
	sql := strings.Repeat("select 'large SQL payload' from dual;\r\n", 10000)
	sqlPath := filepath.Join(dir, "sql.txt")
	os.WriteFile(sqlPath, []byte(sql), 0600)
	headerPath := filepath.Join(dir, "header.txt")
	os.WriteFile(headerPath, []byte("Synthetic SQL rendering test"), 0600)
	keywordsPath := filepath.Join(dir, "keywords.json")
	os.WriteFile(keywordsPath, []byte("[]"), 0600)
	testSetup := `$form.StartPosition = [Windows.Forms.FormStartPosition]::Manual
$form.Location = New-Object Drawing.Point(-32000, -32000)
$testTimer = New-Object Windows.Forms.Timer
$testTimer.Interval = 250
$testTimer.Add_Tick({
  $testTimer.Stop()
  if ($browser -isnot [Windows.Forms.RichTextBox] -or $browser.Text.Length -lt 300000 -or -not $btnExecute.Enabled) { [Environment]::Exit(9) }
  Write-DialogStage 'UI_HEARTBEAT'
  $btnCancel.PerformClick()
})
$form.Add_Shown({ $testTimer.Start() })
`
	script := strings.Replace(ps1Script, "try { $result = $form.ShowDialog() }", testSetup+"\ntry { $result = $form.ShowDialog() }", 1)
	scriptPath := filepath.Join(dir, "dialog.ps1")
	os.WriteFile(scriptPath, []byte(script), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", scriptPath, "-RenderMode", "plain", "-SqlPath", sqlPath, "-HeaderPath", headerPath, "-DangerKeywordsPath", keywordsPath, "-SignalPath", filepath.Join(dir, "ui.log"), "-ResultPath", filepath.Join(dir, "result.txt"))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native dialog test: %v: %s", err, output)
	}
	result, err := readDialogResult(filepath.Join(dir, "result.txt"))
	if err != nil || result.Approved {
		t.Fatalf("cancel response: %+v %v", result, err)
	}
	signals, err := os.ReadFile(filepath.Join(dir, "ui.log"))
	if err != nil || !strings.Contains(string(signals), "CLICK_CANCEL") {
		t.Fatal("native form did not process timer/cancel events")
	}
}
