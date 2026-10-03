//go:build windows

// Package confirm provides Human-in-the-loop confirmation dialogs.
package confirm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unsafe"
)

var ConfirmRevision = "YxLg=="
var DialogBuildTag = "RGlhbG9nV"
var PromptSchemaRev = "B0VjEuMg=="

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

// MessageBox button/icon constants
const (
	MB_OK              = 0x00000000
	MB_OKCANCEL        = 0x00000001
	MB_YESNO           = 0x00000004
	MB_ICONWARNING     = 0x00000030
	MB_ICONERROR       = 0x00000010
	MB_ICONINFORMATION = 0x00000040
	MB_DEFBUTTON2      = 0x00000100

	IDOK     = 1
	IDCANCEL = 2
	IDYES    = 6
	IDNO     = 7
)

// ConfirmRequest contains the data for a confirmation dialog.
type ConfirmRequest struct {
	SQL             string
	MatchedKeywords []string
	DisplayKeywords []string
	// MatchedKeywordsForHighlight, if non-empty, limits red keyword markup to these terms (Java: hits on formatted text only).
	MatchedKeywordsForHighlight []string
	MatchedActions              []string // command_match statement types (Java parity); merged into red highlight when set
	StatementType               string
	IsDDL                       bool
	Connection                  string // Database alias from config (e.g. "database1", "database2") for title/display
	// ConnectionIndex is the 0-based index in the configured connections list; selects header bar color (same palette as Java).
	ConnectionIndex      int
	SourceLabel          string // Optional, e.g. "File: path/to/file.sql" for execute_sql_file
	WhitelistPath        string
	WhitelistConnection  string
	ReviewTriggerDetails []string
	DangerKeywords       []string
}

// ConfirmResult reports how the review dialog was approved.
type ConfirmResult struct {
	Approved    bool
	AllowPrefix bool
}

// Confirmer handles user confirmation dialogs.
type Confirmer struct{}

// NewConfirmer creates a new Confirmer instance.
func NewConfirmer() *Confirmer {
	return &Confirmer{}
}

// Confirm shows a confirmation dialog with full SQL in a large scrollable window and returns the approval result.
// Uses PowerShell WinForms (never MessageBox) so SQL is never truncated and scrollbars are shown.
func (c *Confirmer) Confirm(req *ConfirmRequest) (ConfirmResult, error) {
	sqlDir := os.TempDir()
	htmlPath := filepath.Join(sqlDir, "oracle-mcp-confirm-sql.html")
	resultPath := filepath.Join(sqlDir, "oracle-mcp-confirm-result.txt")
	scriptPath := filepath.Join(sqlDir, "oracle-mcp-confirm-dialog.ps1")
	headerPath := filepath.Join(sqlDir, "oracle-mcp-confirm-header.txt")
	dangerKeywordsPath := filepath.Join(sqlDir, "oracle-mcp-confirm-danger-keywords.json")

	htmlContent := sqlHighlightHTML(req.SQL)
	htmlContent = highlightMatchedKeywordsInHTML(htmlContent, highlightTermsForReview(req))
	if err := os.WriteFile(htmlPath, []byte(htmlContent), 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write HTML temp file: %w", err)
	}
	defer os.Remove(htmlPath)
	defer os.Remove(resultPath)

	if err := os.WriteFile(headerPath, []byte(buildConfirmHeader(req)), 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write header temp file: %w", err)
	}
	defer os.Remove(headerPath)

	dangerKeywordsJSON := []byte("[]")
	if len(req.DangerKeywords) > 0 {
		var err error
		dangerKeywordsJSON, err = json.Marshal(req.DangerKeywords)
		if err != nil {
			return ConfirmResult{}, fmt.Errorf("confirm: cannot marshal danger keywords: %w", err)
		}
	}
	if err := os.WriteFile(dangerKeywordsPath, dangerKeywordsJSON, 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write danger keywords temp file: %w", err)
	}
	defer os.Remove(dangerKeywordsPath)

	if err := os.WriteFile(scriptPath, []byte(ps1Script), 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write script temp file: %w", err)
	}
	defer os.Remove(scriptPath)

	connectionArg := req.Connection
	if connectionArg == "" {
		connectionArg = "default"
	}

	headerColor := headerBarColor(req.ConnectionIndex)

	// -STA required for Windows Forms to display correctly
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", scriptPath,
		"-HtmlPath", htmlPath, "-ResultPath", resultPath, "-HeaderPath", headerPath, "-DangerKeywordsPath", dangerKeywordsPath, "-WhitelistPath", req.WhitelistPath, "-WhitelistConnection", req.WhitelistConnection, "-Connection", connectionArg, "-HeaderColor", headerColor)
	cmd.Stdin = nil
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			fmt.Fprintf(os.Stderr, "oracle-mcp confirm PowerShell stderr: %s\n", stderr.String())
		}
		return ConfirmResult{}, nil
	}

	// PowerShell may exit just before the file is fully flushed; retry read briefly
	var data []byte
	var readErr error
	for attempt := 0; attempt < 20; attempt++ {
		data, readErr = os.ReadFile(resultPath)
		if readErr == nil && len(data) > 0 {
			break
		}
		if attempt < 19 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if len(data) == 0 {
		return ConfirmResult{}, nil
	}
	// PowerShell/.NET WriteAllText(..., UTF8) may write BOM (0xEF 0xBB 0xBF); strip it so "1" matches
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	s := strings.TrimSpace(string(data))
	switch s {
	case "1":
		return ConfirmResult{Approved: true}, nil
	case "allow_prefix":
		return ConfirmResult{Approved: true, AllowPrefix: true}, nil
	default:
		return ConfirmResult{}, nil
	}
}

func buildConfirmHeader(req *ConfirmRequest) string {
	var line1 []string
	if req.Connection != "" {
		line1 = append(line1, "Database: "+req.Connection)
	}
	if len(req.MatchedActions) > 0 {
		line1 = append(line1, "Action: "+strings.Join(req.MatchedActions, ", "))
	} else if req.StatementType != "" {
		line1 = append(line1, "Action: "+req.StatementType)
	}
	displayKeywords := req.DisplayKeywords
	if len(displayKeywords) == 0 {
		displayKeywords = req.MatchedKeywords
	}
	if len(displayKeywords) > 0 {
		line1 = append(line1, "Keywords: "+strings.Join(displayKeywords, ", "))
	}
	if req.IsDDL {
		line1 = append(line1, "DDL (auto-committed)")
	}
	var out string
	if len(line1) > 0 {
		out = strings.Join(line1, "    |    ")
	}
	if req.SourceLabel != "" {
		if out != "" {
			out += "\n"
		}
		out += req.SourceLabel // "File: path" on its own second line
	}
	if out == "" {
		return "Confirm SQL execution"
	}
	return out
}

// Same palette as Java Confirmer.HEADER_COLORS (connection index mod length).
var headerBarColors = []string{
	"A5D6A7", "90CAF9", "FFCC80", "CE93D8", "F48FB1",
	"80DEEA", "EF9A9A", "80CBC4", "FFF59D", "BCAAA4",
}

func headerBarColor(connectionIndex int) string {
	if connectionIndex < 0 {
		connectionIndex = 0
	}
	return headerBarColors[connectionIndex%len(headerBarColors)]
}

var tagOrTextRE = regexp.MustCompile(`(<[^>]+>)|([^<]+)`)

const highlightSpanStart = `<span style="color:red;font-weight:bold">`
const highlightSpanEnd = `</span>`

// highlightTermsForReview merges keyword and action strings for red markup (aligned with Java Confirmer).
func highlightTermsForReview(req *ConfirmRequest) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	kwSrc := req.MatchedKeywords
	if len(req.MatchedKeywordsForHighlight) > 0 {
		kwSrc = req.MatchedKeywordsForHighlight
	}
	for _, k := range kwSrc {
		add(k)
	}
	for _, a := range req.MatchedActions {
		add(a)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// highlightMatchedKeywordsInHTML wraps whole-word (case-insensitive) matches in text nodes only, like Java Confirmer.
func highlightMatchedKeywordsInHTML(htmlDoc string, terms []string) string {
	if htmlDoc == "" || len(terms) == 0 {
		return htmlDoc
	}
	var patterns []*regexp.Regexp
	for _, term := range terms {
		patterns = append(patterns, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(term)+`\b`))
	}
	var sb strings.Builder
	subs := tagOrTextRE.FindAllStringSubmatchIndex(htmlDoc, -1)
	for _, loc := range subs {
		if loc[2] >= 0 && loc[3] >= 0 {
			sb.WriteString(htmlDoc[loc[2]:loc[3]])
			continue
		}
		if loc[4] >= 0 && loc[5] >= 0 {
			text := htmlDoc[loc[4]:loc[5]]
			for _, re := range patterns {
				text = re.ReplaceAllStringFunc(text, func(m string) string {
					return highlightSpanStart + m + highlightSpanEnd
				})
			}
			sb.WriteString(text)
		}
	}
	return sb.String()
}

// sqlKeywords for Oracle/PL-SQL syntax highlighting (lowercase for matching).
var sqlKeywords = []string{
	"create", "or", "replace", "procedure", "function", "package", "body", "begin", "end", "declare",
	"varchar2", "number", "date", "clob", "blob", "in", "out", "inout", "return", "is", "as",
	"if", "then", "elsif", "else", "loop", "for", "while", "exit", "when", "execute", "immediate",
	"select", "insert", "update", "delete", "drop", "alter", "truncate", "grant", "revoke",
	"table", "view", "index", "sequence", "trigger", "type", "constraint",
	"null", "true", "false", "and", "not", "between", "like", "into", "values", "from", "where",
	"order", "by", "group", "having", "join", "left", "right", "inner", "outer", "on", "using",
	"commit", "rollback", "savepoint", "connect", "level", "dual", "sysdate",
	"exception", "raise", "cursor", "open", "fetch", "close", "record", "type", "rowtype",
	"abs", "set", "using", "default", "over", "partition", "with",
}

// sqlHighlightHTML returns a full HTML document with SQL syntax highlighting (keywords, strings, comments, numbers).
func sqlHighlightHTML(sql string) string {
	const (
		classKeyword = "kw"
		classString  = "str"
		classComment = "cm"
		classNumber  = "num"
		classIdent   = "id"
	)
	// Build keyword regex: \b(word1|word2|...)\b
	kwPattern := `\b(` + strings.Join(sqlKeywords, "|") + `)\b`
	kwRe := regexp.MustCompile("(?i)" + kwPattern)

	// escapeForDisplay escapes HTML, newlines -> <br>, spaces -> &nbsp; for review only; executed SQL is unchanged.
	escapeForDisplay := func(s string) string {
		s = html.EscapeString(s)
		s = strings.ReplaceAll(s, "\n", "<br>")
		s = strings.ReplaceAll(s, " ", "&nbsp;")
		return s
	}

	var out strings.Builder
	out.WriteString(`<!DOCTYPE html><html><head><meta charset="UTF-8"><style>
.sql-wrap { font-family: Consolas, monospace; font-size: 11pt; background: #ffffff; color: #24292e; padding: 12px; white-space: pre-wrap; word-break: break-word; overflow: visible; margin: 0; }
.sql-wrap .kw { color: #0550ae; }
.sql-wrap .str { color: #cf2222; }
.sql-wrap .cm { color: #57606a; }
.sql-wrap .num { color: #116329; }
.sql-wrap .id { color: #953800; }
</style></head><body class="sql-wrap"><code>`)

	i := 0
	for i < len(sql) {
		// Double-quoted identifier (e.g. Oracle); do not treat as keyword (Java BaseFormatter).
		if sql[i] == '"' {
			start := i
			i++
			for i < len(sql) {
				if sql[i] == '"' {
					i++
					if i < len(sql) && sql[i] == '"' {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteString(`<span class="` + classIdent + `">`)
			out.WriteString(escapeForDisplay(sql[start:i]))
			out.WriteString("</span>")
			continue
		}
		// String literal (single-quoted, allow '' inside)
		if sql[i] == '\'' {
			start := i
			i++
			for i < len(sql) {
				if sql[i] == '\'' {
					i++
					if i < len(sql) && sql[i] == '\'' {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteString(`<span class="` + classString + `">`)
			out.WriteString(escapeForDisplay(sql[start:i]))
			out.WriteString("</span>")
			continue
		}
		// Line comment
		if i+1 < len(sql) && sql[i] == '-' && sql[i+1] == '-' {
			start := i
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			out.WriteString(`<span class="` + classComment + `">`)
			out.WriteString(escapeForDisplay(sql[start:i]))
			out.WriteString("</span>")
			continue
		}
		// Block comment
		if i+1 < len(sql) && sql[i] == '/' && sql[i+1] == '*' {
			start := i
			i += 2
			for i+1 < len(sql) && (sql[i] != '*' || sql[i+1] != '/') {
				i++
			}
			if i+1 < len(sql) {
				i += 2
			}
			out.WriteString(`<span class="` + classComment + `">`)
			out.WriteString(escapeForDisplay(sql[start:i]))
			out.WriteString("</span>")
			continue
		}
		// Word (for keywords and numbers)
		if unicode.IsLetter(rune(sql[i])) || sql[i] == '_' || unicode.IsNumber(rune(sql[i])) {
			start := i
			for i < len(sql) && (unicode.IsLetter(rune(sql[i])) || sql[i] == '_' || unicode.IsNumber(rune(sql[i]))) {
				i++
			}
			seg := sql[start:i]
			escaped := escapeForDisplay(seg)
			allDigits := len(seg) > 0
			for _, r := range seg {
				if !unicode.IsDigit(r) {
					allDigits = false
					break
				}
			}
			if allDigits {
				out.WriteString(`<span class="` + classNumber + `">`)
				out.WriteString(escaped)
				out.WriteString("</span>")
			} else if kwRe.MatchString(seg) {
				out.WriteString(`<span class="` + classKeyword + `">`)
				out.WriteString(escaped)
				out.WriteString("</span>")
			} else {
				out.WriteString(escaped)
			}
			continue
		}
		// Single char (escape for HTML, newline -> <br>)
		out.WriteString(escapeForDisplay(string(sql[i])))
		i++
	}

	out.WriteString("</code></body></html>")
	return out.String()
}

// messageBox calls the Windows MessageBoxW API.
func messageBox(hwnd uintptr, text, caption string, flags uint32) int {
	textPtr, _ := syscall.UTF16PtrFromString(text)
	captionPtr, _ := syscall.UTF16PtrFromString(caption)
	ret, _, _ := procMessageBoxW.Call(
		hwnd,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(captionPtr)),
		uintptr(flags),
	)
	return int(ret)
}

// ShowError displays an error message dialog.
func (c *Confirmer) ShowError(title, message string) {
	messageBox(0, message, title, MB_OK|MB_ICONERROR)
}

// ShowInfo displays an informational message dialog.
func (c *Confirmer) ShowInfo(title, message string) {
	messageBox(0, message, title, MB_OK|MB_ICONINFORMATION)
}

// Available returns true on Windows.
func (c *Confirmer) Available() bool {
	return true
}

// PlatformName returns the platform name.
func (c *Confirmer) PlatformName() string {
	return "windows"
}

// FormatConfirmationMessage formats the confirmation message for logging.
func FormatConfirmationMessage(req *ConfirmRequest) string {
	conn := req.Connection
	if conn == "" {
		conn = "default"
	}
	return fmt.Sprintf(
		"Connection=[%s] SQL=[%s] Keywords=[%s] Type=[%s] IsDDL=[%v]",
		conn,
		truncateSQL(req.SQL, 100),
		strings.Join(req.MatchedKeywords, ","),
		req.StatementType,
		req.IsDDL,
	)
}

func truncateSQL(sql string, maxLen int) string {
	sql = strings.ReplaceAll(sql, "\n", " ")
	sql = strings.ReplaceAll(sql, "\r", "")
	if len(sql) > maxLen {
		return sql[:maxLen] + "..."
	}
	return sql
}

// ps1Script is the PowerShell script for the confirmation form (WebBrowser with HTML syntax-highlighted SQL).
const ps1Script = `
param([string]$HtmlPath, [string]$ResultPath, [string]$HeaderPath, [string]$DangerKeywordsPath, [string]$WhitelistPath, [string]$WhitelistConnection, [string]$Connection = "default", [string]$HeaderColor = "A5D6A7")
$Header = if (Test-Path $HeaderPath) { [System.IO.File]::ReadAllText($HeaderPath, [System.Text.Encoding]::UTF8) } else { "Confirm SQL execution" }
$DangerKeywords = @()
if (Test-Path $DangerKeywordsPath) {
	$rawDangerKeywords = [System.IO.File]::ReadAllText($DangerKeywordsPath, [System.Text.Encoding]::UTF8).Trim()
	if ($rawDangerKeywords -ne '') {
		$DangerKeywords = @((ConvertFrom-Json -InputObject $rawDangerKeywords))
	}
}
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

function Normalize-WhitelistEntry($entry) {
	$headLines = @()
	if ($null -ne $entry.PSObject.Properties['head_line']) {
		foreach ($value in @($entry.'head_line')) {
			$headLines += [string]$value
		}
	} elseif ($null -ne $entry.PSObject.Properties['header_line']) {
		$headLines += [string]$entry.'header_line'
	}

	$keywords = @()
	if ($null -ne $entry.PSObject.Properties['keyword:']) {
		foreach ($value in @($entry.'keyword:')) {
			$keywords += [string]$value
		}
	}

	$prefixes = @()
	if ($null -ne $entry.PSObject.Properties['prefix']) {
		foreach ($value in @($entry.'prefix')) {
			$prefixes += [string]$value
		}
	}

	return [ordered]@{
		connection = [string]$entry.connection
		head_line  = @($headLines)
		'keyword:' = @($keywords)
		prefix     = @($prefixes)
	}
}

function Save-WhitelistKeyword([string]$path, [string]$connectionName, [string]$keyword) {
	$items = @()
	if (-not [string]::IsNullOrWhiteSpace($path) -and (Test-Path $path)) {
		$raw = [System.IO.File]::ReadAllText($path, [System.Text.Encoding]::UTF8).Trim()
		if ($raw -ne '') {
			$parsed = ConvertFrom-Json -InputObject $raw
			foreach ($item in @($parsed)) {
				$items += ,(Normalize-WhitelistEntry $item)
			}
		}
	}

	$entry = $null
	foreach ($item in $items) {
		if ([string]::Equals([string]$item.connection, $connectionName, [System.StringComparison]::Ordinal)) {
			$entry = $item
			break
		}
	}
	if ($null -eq $entry) {
		$entry = [ordered]@{
			connection = $connectionName
			head_line  = @()
			'keyword:' = @()
		}
		$items += ,$entry
	}

	$exists = $false
	foreach ($existing in @($entry.'keyword:')) {
		if ([string]::Equals([string]$existing, $keyword, [System.StringComparison]::OrdinalIgnoreCase)) {
			$exists = $true
			break
		}
	}
	if (-not $exists) {
		$entry.'keyword:' += $keyword
	}

	$json = $items | ConvertTo-Json -Depth 5
	$utf8NoBom = New-Object System.Text.UTF8Encoding $false
	[System.IO.File]::WriteAllText($path, $json + [Environment]::NewLine, $utf8NoBom)
}

function Save-WhitelistPrefix([string]$path, [string]$connectionName, [string]$prefix) {
	$items = @()
	if (-not [string]::IsNullOrWhiteSpace($path) -and (Test-Path $path)) {
		$raw = [System.IO.File]::ReadAllText($path, [System.Text.Encoding]::UTF8).Trim()
		if ($raw -ne '') {
			$parsed = ConvertFrom-Json -InputObject $raw
			foreach ($item in @($parsed)) {
				$items += ,(Normalize-WhitelistEntry $item)
			}
		}
	}

	$entry = $null
	foreach ($item in $items) {
		if ([string]::Equals([string]$item.connection, $connectionName, [System.StringComparison]::Ordinal)) {
			$entry = $item
			break
		}
	}
	if ($null -eq $entry) {
		$entry = [ordered]@{
			connection = $connectionName
			head_line  = @()
			'keyword:' = @()
			prefix     = @()
		}
		$items += ,$entry
	}

	$exists = $false
	foreach ($existing in @($entry.prefix)) {
		if ([string]::Equals([string]$existing, $prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
			$exists = $true
			break
		}
	}
	if (-not $exists) {
		$entry.prefix += $prefix
	}

	$json = $items | ConvertTo-Json -Depth 5
	$utf8NoBom = New-Object System.Text.UTF8Encoding $false
	[System.IO.File]::WriteAllText($path, $json + [Environment]::NewLine, $utf8NoBom)
}

$fileUri = [Uri]::new("file:///" + $HtmlPath.Replace('\', '/').Replace(' ', '%20'))
$form = New-Object System.Windows.Forms.Form
$form.Text = "Confirm SQL — " + $Connection
$form.Size = New-Object System.Drawing.Size(1000, 780)
$form.StartPosition = [System.Windows.Forms.FormStartPosition]::CenterScreen
$form.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::Sizable
$form.MinimumSize = New-Object System.Drawing.Size(800, 600)
$form.TopMost = $true

$headerPanel = New-Object System.Windows.Forms.Panel
$headerPanel.Dock = [System.Windows.Forms.DockStyle]::Top
$headerPanel.Height = 42
if (-not $HeaderColor.StartsWith('#')) { $HeaderColor = '#' + $HeaderColor }
try { $headerPanel.BackColor = [System.Drawing.ColorTranslator]::FromHtml($HeaderColor) } catch { $headerPanel.BackColor = [System.Drawing.Color]::FromArgb(165, 214, 167) }
$lbl = New-Object System.Windows.Forms.Label
$lbl.Text = $Header.Trim()
$lbl.Location = New-Object System.Drawing.Point(10, 10)
$lbl.AutoSize = $true
$lbl.MaximumSize = New-Object System.Drawing.Size(960, 0)
if ($Connection -and $Connection -ne "default") {
	$lbl.Font = New-Object System.Drawing.Font($lbl.Font.FontFamily, $lbl.Font.Size, [System.Drawing.FontStyle]::Bold)
}
$headerPanel.Controls.Add($lbl)
$form.Controls.Add($headerPanel)

$browser = New-Object System.Windows.Forms.WebBrowser
$browser.Location = New-Object System.Drawing.Point(10, 42)
$browser.Size = New-Object System.Drawing.Size(965, 618)
$browser.Anchor = [System.Windows.Forms.AnchorStyles]::Top -bor [System.Windows.Forms.AnchorStyles]::Bottom -bor [System.Windows.Forms.AnchorStyles]::Left -bor [System.Windows.Forms.AnchorStyles]::Right
$browser.ScrollBarsEnabled = $true
$browser.IsWebBrowserContextMenuEnabled = $false
$browser.ScriptErrorsSuppressed = $true
$browser.Navigate($fileUri.AbsoluteUri)

$txtKeyword = New-Object System.Windows.Forms.TextBox
$txtKeyword.Location = New-Object System.Drawing.Point(10, 670)
$txtKeyword.Size = New-Object System.Drawing.Size(460, 28)
$txtKeyword.Anchor = [System.Windows.Forms.AnchorStyles]::Bottom -bor [System.Windows.Forms.AnchorStyles]::Left -bor [System.Windows.Forms.AnchorStyles]::Right
$txtKeyword.BorderStyle = [System.Windows.Forms.BorderStyle]::FixedSingle
$form.Controls.Add($txtKeyword)

$btnAllowKeyword = New-Object System.Windows.Forms.Button
$btnAllowKeyword.Text = "Allow Keyword"
$btnAllowKeyword.Location = New-Object System.Drawing.Point(480, 670)
$btnAllowKeyword.Size = New-Object System.Drawing.Size(100, 28)
$btnAllowKeyword.Anchor = [System.Windows.Forms.AnchorStyles]::Bottom -bor [System.Windows.Forms.AnchorStyles]::Right
$btnAllowKeyword.Add_Click({
	if ([string]::IsNullOrWhiteSpace($txtKeyword.Text)) {
		[System.Windows.Forms.MessageBox]::Show("Please enter a keyword first.", "Allow Keyword", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Warning) | Out-Null
		return
	}
	if ($txtKeyword.Text.Trim() -notmatch '^[A-Za-z0-9_]+$') {
		[System.Windows.Forms.MessageBox]::Show("Keyword can contain only letters, numbers, and underscores.", "Allow Keyword", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Warning) | Out-Null
		return
	}
	foreach ($dangerKeyword in $DangerKeywords) {
		if ([string]::Equals([string]$dangerKeyword, $txtKeyword.Text.Trim(), [System.StringComparison]::OrdinalIgnoreCase)) {
			[System.Windows.Forms.MessageBox]::Show("This keyword matches danger_keywords exactly and cannot be added.", "Allow Keyword", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Warning) | Out-Null
			return
		}
	}
	if ([string]::IsNullOrWhiteSpace($WhitelistPath)) {
		[System.Windows.Forms.MessageBox]::Show("Whitelist path is unavailable.", "Allow Keyword", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error) | Out-Null
		return
	}
	try {
		Save-WhitelistKeyword -path $WhitelistPath -connectionName $WhitelistConnection -keyword $txtKeyword.Text.Trim()
		[System.Windows.Forms.MessageBox]::Show("Keyword added successfully.", "Allow Keyword", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Information) | Out-Null
		$txtKeyword.Clear()
	} catch {
		[System.Windows.Forms.MessageBox]::Show("Failed to update whitelist.json: " + $_.Exception.Message, "Allow Keyword", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error) | Out-Null
	}
})
$form.Controls.Add($btnAllowKeyword)

$btnAllowPrefix = New-Object System.Windows.Forms.Button
$btnAllowPrefix.Text = "Allow Prefix"
$btnAllowPrefix.Location = New-Object System.Drawing.Point(590, 670)
$btnAllowPrefix.Size = New-Object System.Drawing.Size(100, 28)
$btnAllowPrefix.Anchor = [System.Windows.Forms.AnchorStyles]::Bottom -bor [System.Windows.Forms.AnchorStyles]::Right
$btnAllowPrefix.Add_Click({
	if ([string]::IsNullOrWhiteSpace($txtKeyword.Text)) {
		[System.Windows.Forms.MessageBox]::Show("Please enter a prefix first.", "Allow Prefix", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Warning) | Out-Null
		return
	}
	if ([string]::IsNullOrWhiteSpace($WhitelistPath)) {
		[System.Windows.Forms.MessageBox]::Show("Whitelist path is unavailable.", "Allow Prefix", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error) | Out-Null
		return
	}
	try {
		Save-WhitelistPrefix -path $WhitelistPath -connectionName $WhitelistConnection -prefix $txtKeyword.Text.Trim()
		$form.Tag = "allow_prefix"
		$form.DialogResult = [System.Windows.Forms.DialogResult]::OK
		$form.Close()
	} catch {
		[System.Windows.Forms.MessageBox]::Show("Failed to update whitelist.json: " + $_.Exception.Message, "Allow Prefix", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error) | Out-Null
	}
})
$form.Controls.Add($btnAllowPrefix)

$btnExecute = New-Object System.Windows.Forms.Button
$btnExecute.Text = "Execute"
$btnExecute.Location = New-Object System.Drawing.Point(700, 670)
$btnExecute.Size = New-Object System.Drawing.Size(90, 28)
$btnExecute.Anchor = [System.Windows.Forms.AnchorStyles]::Bottom -bor [System.Windows.Forms.AnchorStyles]::Right
$btnExecute.DialogResult = [System.Windows.Forms.DialogResult]::OK
# Do not set AcceptButton/CancelButton so keyboard focus is not on any button (avoids accidental approve while typing)
$form.Controls.Add($btnExecute)

$btnCancel = New-Object System.Windows.Forms.Button
$btnCancel.Text = "Cancel"
$btnCancel.Location = New-Object System.Drawing.Point(800, 670)
$btnCancel.Size = New-Object System.Drawing.Size(90, 28)
$btnCancel.Anchor = [System.Windows.Forms.AnchorStyles]::Bottom -bor [System.Windows.Forms.AnchorStyles]::Right
$btnCancel.DialogResult = [System.Windows.Forms.DialogResult]::Cancel
$form.Controls.Add($btnCancel)

$form.Controls.Add($browser)
$form.Controls.SetChildIndex($browser, 1)
# Put focus on the SQL content (browser), not on Execute/Cancel, so user typing does not trigger a button
$form.Add_Shown({ $form.ActiveControl = $browser })
$result = $form.ShowDialog()
$utf8NoBom = New-Object System.Text.UTF8Encoding $false
if ($form.Tag -eq "allow_prefix") { [IO.File]::WriteAllText($ResultPath, "allow_prefix", $utf8NoBom) }
elseif ($result -eq [System.Windows.Forms.DialogResult]::OK) { [IO.File]::WriteAllText($ResultPath, "1", $utf8NoBom) }
else { [IO.File]::WriteAllText($ResultPath, "0", $utf8NoBom) }
`
