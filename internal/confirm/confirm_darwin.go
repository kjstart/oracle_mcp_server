//go:build darwin

// Package confirm provides Human-in-the-loop confirmation dialogs for macOS.
package confirm

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ConfirmRequest contains the data for a confirmation dialog.
type ConfirmRequest struct {
	SQL                         string
	MatchedKeywords             []string
	DisplayKeywords             []string
	MatchedKeywordsForHighlight []string
	MatchedActions              []string
	StatementType               string
	IsDDL                       bool
	Connection                  string
	ConnectionIndex             int
	SourceLabel                 string
	WhitelistPath               string
	WhitelistConnection         string
	ReviewTriggerDetails        []string
	DangerKeywords              []string
}

// ConfirmResult reports how the review dialog was approved.
type ConfirmResult struct {
	Approved    bool
	AllowPrefix bool
}

// Confirmer handles user confirmation dialogs on macOS.
type Confirmer struct{}

// NewConfirmer creates a new Confirmer instance.
func NewConfirmer() *Confirmer {
	return &Confirmer{}
}

// Confirm shows a confirmation dialog using JXA and returns the approval result.
func (c *Confirmer) Confirm(req *ConfirmRequest) (ConfirmResult, error) {
	sqlDir := os.TempDir()
	resultPath := filepath.Join(sqlDir, "oracle-mcp-confirm-result.txt")
	scriptPath := filepath.Join(sqlDir, "oracle-mcp-confirm-dialog.js")
	headerPath := filepath.Join(sqlDir, "oracle-mcp-confirm-header.txt")
	sqlPath := filepath.Join(sqlDir, "oracle-mcp-confirm-sql.txt")
	dangerKeywordsPath := filepath.Join(sqlDir, "oracle-mcp-confirm-danger-keywords.json")
	highlightTermsPath := filepath.Join(sqlDir, "oracle-mcp-confirm-highlight-terms.json")

	if err := os.WriteFile(headerPath, []byte(buildConfirmHeader(req)), 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write header temp file: %w", err)
	}
	defer os.Remove(headerPath)
	defer os.Remove(resultPath)

	if err := os.WriteFile(sqlPath, []byte(req.SQL), 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write sql temp file: %w", err)
	}
	defer os.Remove(sqlPath)

	dangerKeywordsJSON := "[]"
	if len(req.DangerKeywords) > 0 {
		quoted := make([]string, 0, len(req.DangerKeywords))
		for _, keyword := range req.DangerKeywords {
			escaped := strings.ReplaceAll(keyword, `\`, `\\`)
			escaped = strings.ReplaceAll(escaped, `"`, `\"`)
			quoted = append(quoted, `"`+escaped+`"`)
		}
		dangerKeywordsJSON = "[" + strings.Join(quoted, ",") + "]"
	}
	if err := os.WriteFile(dangerKeywordsPath, []byte(dangerKeywordsJSON), 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write danger keywords temp file: %w", err)
	}
	defer os.Remove(dangerKeywordsPath)

	highlightTerms := highlightTermsForReview(req)
	if highlightTerms == nil {
		highlightTerms = []string{}
	}
	highlightTermsJSON, err := json.Marshal(highlightTerms)
	if err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot marshal highlight terms: %w", err)
	}
	if err := os.WriteFile(highlightTermsPath, highlightTermsJSON, 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write highlight terms temp file: %w", err)
	}
	defer os.Remove(highlightTermsPath)

	if err := os.WriteFile(scriptPath, []byte(jxaScript), 0600); err != nil {
		return ConfirmResult{}, fmt.Errorf("confirm: cannot write script temp file: %w", err)
	}
	defer os.Remove(scriptPath)

	connectionArg := req.Connection
	if connectionArg == "" {
		connectionArg = "default"
	}
	headerColor := headerBarColor(req.ConnectionIndex)

	cmd := exec.Command("osascript", "-l", "JavaScript", scriptPath,
		headerPath, sqlPath, resultPath, req.WhitelistPath, req.WhitelistConnection, dangerKeywordsPath, connectionArg, headerColor, highlightTermsPath)
	output, err := cmd.Output()
	if result, ok := readConfirmResultFile(resultPath); ok {
		return result, nil
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 || strings.Contains(string(exitErr.Stderr), "-128") {
				return ConfirmResult{}, nil
			}
		}
		return ConfirmResult{}, fmt.Errorf("dialog error: %w", err)
	}

	if result, ok := parseConfirmResult(strings.TrimSpace(string(output))); ok {
		return result, nil
	}
	return ConfirmResult{}, nil
}

func readConfirmResultFile(path string) (ConfirmResult, bool) {
	for attempt := 0; attempt < 20; attempt++ {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return parseConfirmResult(strings.TrimSpace(string(data)))
		}
		if attempt < 19 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return ConfirmResult{}, false
}

func parseConfirmResult(value string) (ConfirmResult, bool) {
	switch value {
	case "1":
		return ConfirmResult{Approved: true}, true
	case "allow_prefix":
		return ConfirmResult{Approved: true, AllowPrefix: true}, true
	case "0":
		return ConfirmResult{}, true
	default:
		return ConfirmResult{}, false
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
		out += req.SourceLabel
	}
	if out == "" {
		return "Confirm SQL execution"
	}
	return out
}

// Same palette as Windows Confirmer.HEADER_COLORS (connection index mod length).
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

// highlightTermsForReview merges keyword and action strings for red markup (aligned with Windows).
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

// ShowError displays an error message dialog on macOS.
func (c *Confirmer) ShowError(title, message string) {
	script := fmt.Sprintf(`
		display dialog %q with title %q buttons {"OK"} default button "OK" with icon stop
	`, message, title)
	exec.Command("osascript", "-e", script).Run()
}

// ShowInfo displays an informational message dialog on macOS.
func (c *Confirmer) ShowInfo(title, message string) {
	script := fmt.Sprintf(`
		display dialog %q with title %q buttons {"OK"} default button "OK" with icon note
	`, message, title)
	exec.Command("osascript", "-e", script).Run()
}

// Available returns true on macOS.
func (c *Confirmer) Available() bool {
	return true
}

// PlatformName returns the platform name.
func (c *Confirmer) PlatformName() string {
	return "darwin"
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

const jxaScript = `
	ObjC.import('AppKit');
	ObjC.import('Foundation');

	let modalAction = -1;
	ObjC.registerSubclass({
	  name: "OracleMCPConfirmDialogHandler",
	  protocols: ["NSWindowDelegate"],
	  methods: {
	    "buttonClicked:": {
	      types: ["void", ["id"]],
	      implementation: function(sender) {
	        modalAction = Number(sender.tag);
	        $.NSApp.stopModalWithCode(modalAction);
	      }
	    },
	    "windowShouldClose:": {
	      types: ["bool", ["id"]],
	      implementation: function(sender) {
	        modalAction = 0;
	        $.NSApp.stopModalWithCode(0);
	        return true;
	      }
	    }
	  }
	});

	ObjC.registerSubclass({
	  name: "OracleMCPConfirmWindow",
	  superclass: "NSWindow",
	  methods: {
	    "performKeyEquivalent:": {
	      types: ["bool", ["id"]],
	      implementation: function(event) {
	        return handleWindowShortcut(this, event);
	      }
	    }
	  }
	});
	
	function run(argv) {
  const headerPath = argv[0];
  const sqlPath = argv[1];
  const resultPath = argv[2];
  const whitelistPath = argv[3];
	  const whitelistConnection = argv[4] || "default";
	  const dangerKeywordsPath = argv[5];
	  const connectionName = argv[6] || "default";
	  const headerColor = argv[7] || "A5D6A7";
	  const highlightTermsPath = argv[8];

  const app = Application.currentApplication();
  app.includeStandardAdditions = true;

	  const header = readTextFile(headerPath) || "Confirm SQL execution";
	  const sqlText = readTextFile(sqlPath) || "";
	  const dangerKeywords = readJSONFile(dangerKeywordsPath, []);
	  const highlightTerms = readJSONFile(highlightTermsPath, []);
	  let keywordText = "";
	  activateDialogApp();

	  while (true) {
	    const ui = buildDialog(connectionName, header, sqlText, keywordText, headerColor, highlightTerms);
	    const response = showDialog(ui);
	    keywordText = ObjC.unwrap(ui.keywordField.stringValue);

	    if (response === 1) {
	      writeResult(resultPath, "1");
	      return "1";
	    }
	    if (response === 0) {
	      writeResult(resultPath, "0");
	      return "0";
	    }
	    if (response === 2) {
	      const trimmed = keywordText.trim();
	      const validationError = validateKeyword(trimmed, dangerKeywords);
	      if (validationError) {
        app.displayAlert("Allow Keyword", {message: validationError});
        continue;
      }
      if (!whitelistPath) {
        app.displayAlert("Allow Keyword", {message: "Whitelist path is unavailable."});
        continue;
      }
      try {
        saveWhitelistKeyword(whitelistPath, whitelistConnection, trimmed);
        app.displayAlert("Allow Keyword", {message: "Keyword added successfully."});
        keywordText = "";
      } catch (error) {
        app.displayAlert("Allow Keyword", {message: "Failed to update whitelist.json: " + error});
	      }
	      continue;
	    }
	    if (response === 3) {
	      const trimmed = keywordText.trim();
	      if (!trimmed) {
	        app.displayAlert("Allow Prefix", {message: "Please enter a prefix first."});
	        continue;
	      }
	      if (!whitelistPath) {
	        app.displayAlert("Allow Prefix", {message: "Whitelist path is unavailable."});
	        continue;
	      }
	      try {
	        saveWhitelistPrefix(whitelistPath, whitelistConnection, trimmed);
	        writeResult(resultPath, "allow_prefix");
	        return "allow_prefix";
	      } catch (error) {
	        app.displayAlert("Allow Prefix", {message: "Failed to update whitelist.json: " + error});
	        continue;
	      }
	    }

    writeResult(resultPath, "0");
    return "0";
	  }
	}
	
	function buildDialog(connectionName, header, sqlText, keywordText, headerColor, highlightTerms) {
	  const windowWidth = 820;
	  const windowHeight = 620;
	  const handler = $.OracleMCPConfirmDialogHandler.alloc.init;
	  const window = $.OracleMCPConfirmWindow.alloc.initWithContentRectStyleMaskBackingDefer(
	    $.NSMakeRect(0, 0, windowWidth, windowHeight),
	    $.NSTitledWindowMask | $.NSClosableWindowMask | $.NSMiniaturizableWindowMask,
	    $.NSBackingStoreBuffered,
	    false
	  );
	  window.title = "Confirm SQL — " + connectionName;
	  window.setDelegate(handler);
	
	  const container = $.NSView.alloc.initWithFrame($.NSMakeRect(0, 0, windowWidth, windowHeight));
	  window.setContentView(container);
	
	  addHeaderBanner(container, header, headerColor, windowWidth, windowHeight);
	
	  const scrollView = $.NSScrollView.alloc.initWithFrame($.NSMakeRect(20, 120, 780, 440));
	  scrollView.hasVerticalScroller = true;
	  scrollView.hasHorizontalScroller = false;
	  scrollView.borderType = $.NSBezelBorder;

	  const textView = $.NSTextView.alloc.initWithFrame($.NSMakeRect(0, 0, 760, 440));
	  textView.setEditable(false);
	  textView.setSelectable(true);
	  setSQLText(textView, sqlText, highlightTerms);
	  scrollView.documentView = textView;
	  container.addSubview(scrollView);

	  const label = $.NSTextField.labelWithString("Keyword:");
	  label.setFrame($.NSMakeRect(20, 76, 70, 24));
	  container.addSubview(label);

	  const keywordField = $.NSTextField.alloc.initWithFrame($.NSMakeRect(90, 72, 710, 28));
	  keywordField.setStringValue($(keywordText || ""));
	  container.addSubview(keywordField);

	  addButton(container, handler, "Allow Keyword", 2, 374, 24, 112);
	  addButton(container, handler, "Allow Prefix", 3, 496, 24, 104);
	  addButton(container, handler, "Execute", 1, 610, 24, 90);
	  addButton(container, handler, "Cancel", 0, 710, 24, 90);
	
	  return {window: window, keywordField: keywordField, textView: textView, handler: handler};
	}
	
	function activateDialogApp() {
  const nsApp = $.NSApplication.sharedApplication;
  nsApp.setActivationPolicy($.NSApplicationActivationPolicyAccessory);
	  nsApp.activateIgnoringOtherApps(true);
	}
	
	function showDialog(ui) {
	  modalAction = -1;
	  activateDialogApp();
	
	  const window = ui.window;
	  if (window) {
	    window.setLevel($.NSModalPanelWindowLevel);
	    window.center;
	    window.makeKeyAndOrderFront(null);
	    window.orderFrontRegardless;
	    if (ui.textView) {
	      window.makeFirstResponder(ui.textView);
	    }
	  }
	
	  let response = 0;
	  try {
	    response = $.NSApp.runModalForWindow(window);
	  } finally {
	    window.orderOut(null);
	  }
	  if (modalAction >= 0) {
	    return modalAction;
	  }
	  return Number(response);
	}

	function handleWindowShortcut(window, event) {
	  if (!window || !event) {
	    return false;
	  }
	  const flags = Number(event.modifierFlags);
	  if ((flags & Number($.NSEventModifierFlagCommand)) === 0) {
	    return false;
	  }
	  const key = String(ObjC.unwrap(event.charactersIgnoringModifiers) || "").toLowerCase();
	  if (key.length !== 1) {
	    return false;
	  }

	  const target = shortcutTargetForResponder(window.firstResponder);
	  if (!target) {
	    return false;
	  }
	  if (key === "c" && target.respondsToSelector($.NSSelectorFromString("copy:"))) {
	    target.copy(target);
	    return true;
	  }
	  if (key === "v" && target.respondsToSelector($.NSSelectorFromString("paste:"))) {
	    target.paste(target);
	    return true;
	  }
	  if (key === "a" && target.respondsToSelector($.NSSelectorFromString("selectAll:"))) {
	    target.selectAll(target);
	    return true;
	  }
	  if (key === "x" && target.respondsToSelector($.NSSelectorFromString("cut:"))) {
	    target.cut(target);
	    return true;
	  }
	  return false;
	}

	function shortcutTargetForResponder(responder) {
	  if (!responder) {
	    return null;
	  }
	  if (
	    responder.respondsToSelector($.NSSelectorFromString("copy:")) ||
	    responder.respondsToSelector($.NSSelectorFromString("paste:")) ||
	    responder.respondsToSelector($.NSSelectorFromString("selectAll:"))
	  ) {
	    return responder;
	  }
	  if (responder.respondsToSelector($.NSSelectorFromString("currentEditor"))) {
	    const editor = responder.currentEditor;
	    if (
	      editor &&
	      (
	        editor.respondsToSelector($.NSSelectorFromString("copy:")) ||
	        editor.respondsToSelector($.NSSelectorFromString("paste:")) ||
	        editor.respondsToSelector($.NSSelectorFromString("selectAll:"))
	      )
	    ) {
	      return editor;
	    }
	  }
	  return null;
	}
	
	function makeLabel(text, frame) {
	  const label = $.NSTextField.alloc.initWithFrame(frame);
	  label.setStringValue($(text));
	  label.setBezeled(false);
	  label.setDrawsBackground(false);
	  label.setEditable(false);
	  label.setSelectable(false);
	  return label;
	}

	function addHeaderBanner(container, header, headerColor, windowWidth, windowHeight) {
	  const bannerHeight = 42;
	  const banner = $.NSTextField.alloc.initWithFrame($.NSMakeRect(0, windowHeight - bannerHeight, windowWidth, bannerHeight));
	  banner.setStringValue($(""));
	  banner.setBezeled(false);
	  banner.setDrawsBackground(true);
	  banner.setEditable(false);
	  banner.setSelectable(false);
	  banner.setBackgroundColor(nsColorFromHex(headerColor));
	  container.addSubview(banner);
	
	  const label = makeLabel(String(header || "").trim(), $.NSMakeRect(12, windowHeight - bannerHeight + 8, windowWidth - 24, 24));
	  label.setFont($.NSFont.boldSystemFontOfSize(13));
	  label.setTextColor($.NSColor.blackColor);
	  label.setSelectable(true);
	  container.addSubview(label);
	}

	function nsColorFromHex(hex) {
	  let value = String(hex || "A5D6A7").replace(/^#/, "");
	  if (!/^[0-9A-Fa-f]{6}$/.test(value)) {
	    value = "A5D6A7";
	  }
	  const red = parseInt(value.slice(0, 2), 16) / 255.0;
	  const green = parseInt(value.slice(2, 4), 16) / 255.0;
	  const blue = parseInt(value.slice(4, 6), 16) / 255.0;
	  return $.NSColor.colorWithCalibratedRedGreenBlueAlpha(red, green, blue, 1.0);
	}

	function setSQLText(textView, sqlText, highlightTerms) {
	  const baseFont = $.NSFont.userFixedPitchFontOfSize(12);
	  textView.setFont(baseFont);
	  textView.setString($(sqlText));
	
	  const storage = textView.textStorage;
	  const fullRange = $.NSMakeRange(0, storage.length);
	  if (storage.length > 0) {
	    storage.addAttributeValueRange($.NSFontAttributeName, baseFont, fullRange);
	    storage.addAttributeValueRange($.NSForegroundColorAttributeName, $.NSColor.textColor, fullRange);
	  }
	  applyHighlightTerms(storage, sqlText, highlightTerms, baseFont);
	}

	function applyHighlightTerms(storage, sqlText, highlightTerms, baseFont) {
	  if (!Array.isArray(highlightTerms) || !sqlText) {
	    return;
	  }
	  const red = $.NSColor.redColor;
	  const boldFont = $.NSFontManager.sharedFontManager.convertFontToHaveTrait(baseFont, $.NSBoldFontMask);
	  for (let i = 0; i < highlightTerms.length; i++) {
	    const term = String(highlightTerms[i] || "").trim();
	    if (!term) {
	      continue;
	    }
	    const re = new RegExp("\\b" + escapeRegExp(term) + "\\b", "gi");
	    let match;
	    while ((match = re.exec(sqlText)) !== null) {
	      if (!match[0]) {
	        re.lastIndex++;
	        continue;
	      }
	      const range = $.NSMakeRange(match.index, match[0].length);
	      storage.addAttributeValueRange($.NSForegroundColorAttributeName, red, range);
	      storage.addAttributeValueRange($.NSFontAttributeName, boldFont, range);
	    }
	  }
	}

	function escapeRegExp(text) {
	  return String(text).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
	}
	
	function addButton(container, handler, title, tag, x, y, width) {
	  const button = $.NSButton.alloc.initWithFrame($.NSMakeRect(x, y, width, 30));
	  button.title = title;
	  button.setBezelStyle($.NSBezelStyleRounded);
	  button.setKeyEquivalent("");
	  button.setTarget(handler);
	  button.setAction("buttonClicked:");
	  button.setTag(tag);
	  container.addSubview(button);
	  return button;
	}
	
	function validateKeyword(keyword, dangerKeywords) {
  if (!keyword) {
    return "Please enter a keyword first.";
  }
  if (!/^[A-Za-z0-9_]+$/.test(keyword)) {
    return "Keyword can contain only letters, numbers, and underscores.";
  }
  for (let i = 0; i < dangerKeywords.length; i++) {
    if (String(dangerKeywords[i]).toLowerCase() === keyword.toLowerCase()) {
      return "This keyword matches danger_keywords exactly and cannot be added.";
    }
  }
  return "";
}

function readTextFile(path) {
  if (!path) return "";
  const str = $.NSString.stringWithContentsOfFileEncodingError($(path), $.NSUTF8StringEncoding, null);
  const text = str ? ObjC.unwrap(str) : "";
  return text == null ? "" : String(text);
}

function readJSONFile(path, fallbackValue) {
  const text = readTextFile(path).trim();
  if (!text) return fallbackValue;
  return JSON.parse(text);
}

function writeResult(path, value) {
  $(value).writeToFileAtomicallyEncodingError($(path), true, $.NSUTF8StringEncoding, null);
}

function saveWhitelistKeyword(path, connectionName, keyword) {
  let items = [];
  const raw = readTextFile(path).trim();
  if (raw) {
    const parsed = JSON.parse(raw);
    items = parsed.map(normalizeWhitelistEntry);
  }

  let entry = null;
  for (let i = 0; i < items.length; i++) {
    if (String(items[i].connection) === String(connectionName)) {
      entry = items[i];
      break;
    }
  }
  if (!entry) {
    entry = {connection: String(connectionName), head_line: [], "keyword:": [], prefix: []};
    items.push(entry);
  }

  const exists = entry["keyword:"].some(function (item) {
    return String(item).toLowerCase() === String(keyword).toLowerCase();
  });
  if (!exists) {
    entry["keyword:"].push(String(keyword));
  }

  const jsonText = JSON.stringify(items, null, 2) + "\n";
  ensureParentDirectory(path);
  const wrote = $(jsonText).writeToFileAtomicallyEncodingError($(path), true, $.NSUTF8StringEncoding, null);
  if (!wrote) {
    throw "writeToFile failed for " + path;
  }
}

function saveWhitelistPrefix(path, connectionName, prefix) {
  let items = [];
  const raw = readTextFile(path).trim();
  if (raw) {
    const parsed = JSON.parse(raw);
    items = parsed.map(normalizeWhitelistEntry);
  }

  let entry = null;
  for (let i = 0; i < items.length; i++) {
    if (String(items[i].connection) === String(connectionName)) {
      entry = items[i];
      break;
    }
  }
  if (!entry) {
    entry = {connection: String(connectionName), head_line: [], "keyword:": [], prefix: []};
    items.push(entry);
  }

  const exists = entry.prefix.some(function (item) {
    return String(item).toLowerCase() === String(prefix).toLowerCase();
  });
  if (!exists) {
    entry.prefix.push(String(prefix));
  }

  const jsonText = JSON.stringify(items, null, 2) + "\n";
  ensureParentDirectory(path);
  const wrote = $(jsonText).writeToFileAtomicallyEncodingError($(path), true, $.NSUTF8StringEncoding, null);
  if (!wrote) {
    throw "writeToFile failed for " + path;
  }
}

function ensureParentDirectory(path) {
  const parent = $(path).stringByDeletingLastPathComponent;
  const parentText = ObjC.unwrap(parent);
  if (!parentText || parentText === "." || parentText === String(path)) {
    return;
  }
  const created = $.NSFileManager.defaultManager.createDirectoryAtPathWithIntermediateDirectoriesAttributesError(parent, true, $.NSDictionary.dictionary, null);
  if (!created) {
    throw "could not create directory " + parentText;
  }
}

function normalizeWhitelistEntry(entry) {
  const normalized = {
    connection: String(entry.connection || ""),
    head_line: [],
    "keyword:": [],
    prefix: []
  };

  if (Array.isArray(entry.head_line)) {
    normalized.head_line = entry.head_line.map(String);
  } else if (typeof entry.header_line === "string") {
    normalized.head_line = [String(entry.header_line)];
  }

  if (Array.isArray(entry["keyword:"])) {
    normalized["keyword:"] = entry["keyword:"].map(String);
  }

  if (Array.isArray(entry.prefix)) {
    normalized.prefix = entry.prefix.map(String);
  }

  return normalized;
}
`
