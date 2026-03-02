package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

//go:embed help.txt
var helpText string

var version = "dev"

// State persisted between CLI invocations
type State struct {
	DebugURL    string `json:"debug_url"`
	ChromePID   int    `json:"chrome_pid"`
	ActivePage  int    `json:"active_page"`  // index into pages list
	DataDir     string `json:"data_dir"`
	ProxyPID    int    `json:"proxy_pid,omitempty"`  // PID of auth proxy helper
	ProxyPort   int    `json:"proxy_port,omitempty"` // local port of auth proxy
	ReactHook   bool   `json:"react_hook,omitempty"` // true if React DevTools hook should be injected
}

func stateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rodney")
}

func statePath() string {
	return filepath.Join(stateDir(), "state.json")
}

func loadState() (*State, error) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return nil, fmt.Errorf("no browser session (run 'rodney start' first)")
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("corrupt state file: %w", err)
	}
	return &s, nil
}

func saveState(s *State) error {
	if err := os.MkdirAll(stateDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(), data, 0644)
}

func removeState() {
	os.Remove(statePath())
}

// connectBrowser connects to the running Chrome instance
func connectBrowser(s *State) (*rod.Browser, error) {
	browser := rod.New().ControlURL(s.DebugURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to browser (is it still running?): %w", err)
	}
	return browser, nil
}

// getActivePage returns the currently active page, creating a blank one if none exist
func getActivePage(browser *rod.Browser, s *State) (*rod.Page, error) {
	pages, err := browser.Pages()
	if err != nil {
		return nil, fmt.Errorf("failed to list pages: %w", err)
	}
	if len(pages) == 0 {
		// Auto-create a blank page so commands don't fail after 'rodney start'
		page, err := browser.Page(proto.TargetCreateTarget{URL: ""})
		if err != nil {
			return nil, fmt.Errorf("failed to create initial page: %w", err)
		}
		s.ActivePage = 0
		saveState(s)
		return page, nil
	}
	idx := s.ActivePage
	if idx < 0 || idx >= len(pages) {
		idx = 0
	}
	return pages[idx], nil
}

func printUsage() {
	fmt.Print(helpText)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	if cmd == "--version" {
		fmt.Println(version)
		os.Exit(0)
	}

	switch cmd {
	case "_proxy":
		cmdInternalProxy(args) // hidden: runs the auth proxy helper
	case "start":
		cmdStart(args)
	case "stop":
		cmdStop(args)
	case "status":
		cmdStatus(args)
	case "open":
		cmdOpen(args)
	case "back":
		cmdBack(args)
	case "forward":
		cmdForward(args)
	case "reload":
		cmdReload(args)
	case "url":
		cmdURL(args)
	case "title":
		cmdTitle(args)
	case "html":
		cmdHTML(args)
	case "text":
		cmdText(args)
	case "attr":
		cmdAttr(args)
	case "pdf":
		cmdPDF(args)
	case "js":
		cmdJS(args)
	case "click":
		cmdClick(args)
	case "input":
		cmdInput(args)
	case "clear":
		cmdClear(args)
	case "select":
		cmdSelect(args)
	case "submit":
		cmdSubmit(args)
	case "hover":
		cmdHover(args)
	case "file":
		cmdFile(args)
	case "download":
		cmdDownload(args)
	case "focus":
		cmdFocus(args)
	case "wait":
		cmdWait(args)
	case "waitload":
		cmdWaitLoad(args)
	case "waitstable":
		cmdWaitStable(args)
	case "waitidle":
		cmdWaitIdle(args)
	case "sleep":
		cmdSleep(args)
	case "screenshot":
		cmdScreenshot(args)
	case "screenshot-el":
		cmdScreenshotEl(args)
	case "pages":
		cmdPages(args)
	case "page":
		cmdPage(args)
	case "newpage":
		cmdNewPage(args)
	case "closepage":
		cmdClosePage(args)
	case "exists":
		cmdExists(args)
	case "count":
		cmdCount(args)
	case "visible":
		cmdVisible(args)
	case "ax-tree":
		cmdAXTree(args)
	case "ax-find":
		cmdAXFind(args)
	case "ax-node":
		cmdAXNode(args)
	case "scroll":
		cmdScroll(args)
	case "key":
		cmdKey(args)
	case "waitfor":
		cmdWaitFor(args)
	case "perf":
		cmdPerf(args)
	case "console":
		cmdConsole(args)
	case "network":
		cmdNetwork(args)
	case "cookies":
		cmdCookies(args)
	case "storage":
		cmdStorage(args)
	case "react":
		cmdReact(args)
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

// Default timeout for element queries (seconds)
var defaultTimeout = 30 * time.Second

func init() {
	if t := os.Getenv("ROD_TIMEOUT"); t != "" {
		if secs, err := strconv.ParseFloat(t, 64); err == nil {
			defaultTimeout = time.Duration(secs * float64(time.Second))
		}
	}
}

// withPage loads state, connects, and returns the active page.
// Caller should NOT close the browser (we just disconnect).
func withPage() (*State, *rod.Browser, *rod.Page) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	page, err := getActivePage(browser, s)
	if err != nil {
		fatal("%v", err)
	}
	// Re-inject React hook on every connection — the registration is per-CDP-session
	// and lost when the previous rodney process exited
	if s.ReactHook {
		injectReactHook(page)
	}
	// Apply default timeout so element queries don't hang forever
	page = page.Timeout(defaultTimeout)
	return s, browser, page
}

// injectReactHook registers the React DevTools hook on a page so it runs
// before any JS on new document loads, and also evals it into the current page.
func injectReactHook(page *rod.Page) {
	proto.PageAddScriptToEvaluateOnNewDocument{Source: reactHookJS}.Call(page)
	page.Eval(`() => {
		if (window.__REACT_DEVTOOLS_GLOBAL_HOOK__) return;
		` + reactHookJS + `
	}`)
}

// --- Commands ---

func cmdStart(args []string) {
	// Check if already running
	if s, err := loadState(); err == nil {
		// Try connecting
		if b, err := connectBrowser(s); err == nil {
			b.MustClose()
			// It was actually running, warn
			removeState()
		}
	}

	dataDir := filepath.Join(stateDir(), "chrome-data")
	os.MkdirAll(dataDir, 0755)

	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Headless(true).
		Leakless(false). // Keep Chrome alive after CLI exits
		UserDataDir(dataDir)

	// --single-process merges browser+renderer into one OS process.
	// Required for screenshots in gVisor/container environments, but
	// causes crashes on memory-heavy pages (e.g. React dev builds).
	// Enable with ROD_SINGLE_PROCESS=1 when running inside gVisor.
	if os.Getenv("ROD_SINGLE_PROCESS") == "1" {
		l = l.Set("single-process")
	}

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	// Detect authenticated proxy and launch helper if needed
	var proxyPID, proxyPort int
	if server, user, pass, needed := detectProxy(); needed {
		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

		// Find a free port for the local proxy
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal("failed to find free port for proxy: %v", err)
		}
		proxyPort = ln.Addr().(*net.TCPAddr).Port
		ln.Close()

		// Launch ourselves as the proxy helper in the background
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "_proxy",
			strconv.Itoa(proxyPort), server, authHeader)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			fatal("failed to start proxy helper: %v", err)
		}
		proxyPID = cmd.Process.Pid
		// Detach so it survives after we exit
		cmd.Process.Release()

		// Wait for the proxy to be ready
		time.Sleep(500 * time.Millisecond)

		l.Set("proxy-server", fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
		l.Set("ignore-certificate-errors")
		fmt.Printf("Auth proxy started (PID %d, port %d) -> %s\n", proxyPID, proxyPort, server)
	}

	debugURL := l.MustLaunch()

	// Get Chrome PID from the launcher
	pid := l.PID()

	state := &State{
		DebugURL:   debugURL,
		ChromePID:  pid,
		ActivePage: 0,
		DataDir:    dataDir,
		ProxyPID:   proxyPID,
		ProxyPort:  proxyPort,
	}

	if err := saveState(state); err != nil {
		fatal("failed to save state: %v", err)
	}

	fmt.Printf("Chrome started (PID %d)\n", pid)
	fmt.Printf("Debug URL: %s\n", debugURL)
}

func cmdStop(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		// Try to kill by PID
		if s.ChromePID > 0 {
			proc, err := os.FindProcess(s.ChromePID)
			if err == nil {
				proc.Signal(syscall.SIGTERM)
			}
		}
	} else {
		browser.MustClose()
	}
	// Also kill the proxy helper if running
	if s.ProxyPID > 0 {
		if proc, err := os.FindProcess(s.ProxyPID); err == nil {
			proc.Signal(syscall.SIGTERM)
		}
	}
	removeState()
	fmt.Println("Chrome stopped")
}

func cmdStatus(args []string) {
	s, err := loadState()
	if err != nil {
		fmt.Println("No active browser session")
		return
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fmt.Printf("Browser not responding (PID %d, state may be stale)\n", s.ChromePID)
		return
	}
	pages, _ := browser.Pages()
	fmt.Printf("Browser running (PID %d)\n", s.ChromePID)
	fmt.Printf("Debug URL: %s\n", s.DebugURL)
	fmt.Printf("Pages: %d\n", len(pages))
	fmt.Printf("Active page: %d\n", s.ActivePage)
	if page, err := getActivePage(browser, s); err == nil {
		info, _ := page.Info()
		if info != nil {
			fmt.Printf("Current: %s - %s\n", info.Title, info.URL)
		}
	}
}

func cmdOpen(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney open <url>")
	}
	url := args[0]
	// Add scheme if missing
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	// If no pages exist, create one
	pages, _ := browser.Pages()
	var page *rod.Page
	if len(pages) == 0 {
		page = browser.MustPage(url)
		s.ActivePage = 0
		saveState(s)
	} else {
		page, err = getActivePage(browser, s)
		if err != nil {
			fatal("%v", err)
		}
		// Re-inject React hook before navigation so it's present when React initializes
		if s.ReactHook {
			injectReactHook(page)
		}
		if err := page.Navigate(url); err != nil {
			fatal("navigation failed: %v", err)
		}
	}
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.Title)
	}
}

func cmdBack(args []string) {
	_, _, page := withPage()
	page.MustNavigateBack()
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdForward(args []string) {
	_, _, page := withPage()
	page.MustNavigateForward()
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdReload(args []string) {
	hard := false
	for _, arg := range args {
		if arg == "--hard" {
			hard = true
		} else {
			fatal("unknown flag: %s\nusage: rodney reload [--hard]", arg)
		}
	}

	s, _, page := withPage()

	// Re-inject React hook before reload so it's present when React re-initializes
	if s.ReactHook {
		injectReactHook(page)
	}

	if hard {
		err := proto.PageReload{IgnoreCache: true}.Call(page)
		if err != nil {
			fatal("reload failed: %v", err)
		}
	} else {
		page.MustReload()
	}
	page.MustWaitLoad()

	if hard {
		fmt.Println("Hard reloaded (cache bypassed)")
	} else {
		fmt.Println("Reloaded")
	}
}

func cmdURL(args []string) {
	_, _, page := withPage()
	info, err := page.Info()
	if err != nil {
		fatal("failed to get page info: %v", err)
	}
	fmt.Println(info.URL)
}

func cmdTitle(args []string) {
	_, _, page := withPage()
	info, err := page.Info()
	if err != nil {
		fatal("failed to get page info: %v", err)
	}
	fmt.Println(info.Title)
}

func cmdHTML(args []string) {
	_, _, page := withPage()
	if len(args) > 0 {
		el, err := page.Element(args[0])
		if err != nil {
			fatal("element not found: %v", err)
		}
		html, err := el.HTML()
		if err != nil {
			fatal("failed to get HTML: %v", err)
		}
		fmt.Println(html)
	} else {
		html := page.MustEval(`() => document.documentElement.outerHTML`).Str()
		fmt.Println(html)
	}
}

func cmdText(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney text <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	text, err := el.Text()
	if err != nil {
		fatal("failed to get text: %v", err)
	}
	fmt.Println(text)
}

func cmdAttr(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney attr <selector> <attribute>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	val := el.MustAttribute(args[1])
	if val == nil {
		fatal("attribute %q not found", args[1])
	}
	fmt.Println(*val)
}

func cmdPDF(args []string) {
	file := "page.pdf"
	if len(args) > 0 {
		file = args[0]
	}
	_, _, page := withPage()
	req := proto.PagePrintToPDF{}
	r, err := page.PDF(&req)
	if err != nil {
		fatal("failed to generate PDF: %v", err)
	}
	buf := make([]byte, 0)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	if err := os.WriteFile(file, buf, 0644); err != nil {
		fatal("failed to write PDF: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", file, len(buf))
}

func cmdJS(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney js <expression>")
	}
	expr := strings.Join(args, " ")
	_, _, page := withPage()

	// Wrap bare expressions in a function
	js := fmt.Sprintf(`() => { return (%s); }`, expr)
	result, err := page.Eval(js)
	if err != nil {
		fatal("JS error: %v", err)
	}
	// Print the value based on its JSON type
	v := result.Value
	raw := v.JSON("", "")
	// For simple types, print cleanly; for objects/arrays, pretty-print
	switch {
	case raw == "null" || raw == "undefined":
		fmt.Println(raw)
	case raw == "true" || raw == "false":
		fmt.Println(raw)
	case len(raw) > 0 && raw[0] == '"':
		// String value - print unquoted
		fmt.Println(v.Str())
	case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
		// Object or array - pretty print
		fmt.Println(v.JSON("", "  "))
	default:
		// Numbers and other primitives
		fmt.Println(raw)
	}
}

func cmdClick(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney click <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		fatal("click failed: %v", err)
	}
	// Brief pause for click handlers to execute
	time.Sleep(100 * time.Millisecond)
	fmt.Println("Clicked")
}

func cmdInput(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney input <selector> <text>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	text := strings.Join(args[1:], " ")
	el.MustSelectAllText().MustInput(text)
	fmt.Printf("Typed: %s\n", text)
}

func cmdClear(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney clear <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustSelectAllText().MustInput("")
	fmt.Println("Cleared")
}

func cmdFile(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney file <selector> <path|->")
	}
	selector := args[0]
	filePath := args[1]

	_, _, page := withPage()
	el, err := page.Element(selector)
	if err != nil {
		fatal("element not found: %v", err)
	}

	if filePath == "-" {
		// Read from stdin to a temp file
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal("failed to read stdin: %v", err)
		}
		tmp, err := os.CreateTemp("", "rodney-upload-*")
		if err != nil {
			fatal("failed to create temp file: %v", err)
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			fatal("failed to write temp file: %v", err)
		}
		tmp.Close()
		filePath = tmp.Name()
	} else {
		if _, err := os.Stat(filePath); err != nil {
			fatal("file not found: %v", err)
		}
	}

	if err := el.SetFiles([]string{filePath}); err != nil {
		fatal("failed to set file: %v", err)
	}
	fmt.Printf("Set file: %s\n", args[1])
}

func cmdDownload(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney download <selector> [file|-]")
	}
	selector := args[0]
	outFile := ""
	if len(args) > 1 {
		outFile = args[1]
	}

	_, _, page := withPage()
	el, err := page.Element(selector)
	if err != nil {
		fatal("element not found: %v", err)
	}

	// Get the URL from the element's href or src attribute
	urlStr := ""
	if v := el.MustAttribute("href"); v != nil {
		urlStr = *v
	} else if v := el.MustAttribute("src"); v != nil {
		urlStr = *v
	} else {
		fatal("element has no href or src attribute")
	}

	var data []byte

	if strings.HasPrefix(urlStr, "data:") {
		data, err = decodeDataURL(urlStr)
		if err != nil {
			fatal("failed to decode data URL: %v", err)
		}
	} else {
		// Use fetch() in the page context so it has cookies/session
		// Also resolves relative URLs automatically
		js := fmt.Sprintf(`async () => {
			const resp = await fetch(%q);
			if (!resp.ok) throw new Error('HTTP ' + resp.status);
			const buf = await resp.arrayBuffer();
			const bytes = new Uint8Array(buf);
			let binary = '';
			for (let i = 0; i < bytes.length; i++) {
				binary += String.fromCharCode(bytes[i]);
			}
			return btoa(binary);
		}`, urlStr)
		result, err := page.Eval(js)
		if err != nil {
			fatal("download failed: %v", err)
		}
		data, err = base64.StdEncoding.DecodeString(result.Value.Str())
		if err != nil {
			fatal("failed to decode response: %v", err)
		}
	}

	if outFile == "-" {
		os.Stdout.Write(data)
		return
	}

	if outFile == "" {
		outFile = inferDownloadFilename(urlStr)
	}

	if err := os.WriteFile(outFile, data, 0644); err != nil {
		fatal("failed to write file: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", outFile, len(data))
}

// decodeDataURL decodes a data:[<mediatype>][;base64],<data> URL.
func decodeDataURL(dataURL string) ([]byte, error) {
	// Find the comma separating metadata from data
	commaIdx := strings.Index(dataURL, ",")
	if commaIdx < 0 {
		return nil, fmt.Errorf("invalid data URL: no comma found")
	}
	meta := dataURL[5:commaIdx] // skip "data:"
	encoded := dataURL[commaIdx+1:]

	if strings.HasSuffix(meta, ";base64") {
		return base64.StdEncoding.DecodeString(encoded)
	}
	// URL-encoded text
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return nil, err
	}
	return []byte(decoded), nil
}

// inferDownloadFilename tries to extract a reasonable filename from a URL.
func inferDownloadFilename(urlStr string) string {
	if strings.HasPrefix(urlStr, "data:") {
		// Extract MIME type for extension
		commaIdx := strings.Index(urlStr, ",")
		if commaIdx > 0 {
			meta := urlStr[5:commaIdx]
			meta = strings.TrimSuffix(meta, ";base64")
			ext := mimeToExt(meta)
			return nextAvailableFile("download", ext)
		}
		return nextAvailableFile("download", "")
	}

	parsed, err := url.Parse(urlStr)
	if err == nil && parsed.Path != "" && parsed.Path != "/" {
		base := filepath.Base(parsed.Path)
		if base != "." && base != "/" {
			return nextAvailableFile(
				strings.TrimSuffix(base, filepath.Ext(base)),
				filepath.Ext(base),
			)
		}
	}
	return nextAvailableFile("download", "")
}

// mimeToExt returns a file extension for common MIME types.
func mimeToExt(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/css":
		return ".css"
	case "application/json":
		return ".json"
	case "application/javascript":
		return ".js"
	case "application/octet-stream":
		return ".bin"
	default:
		return ""
	}
}

func cmdSelect(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney select <selector> <value>")
	}
	_, _, page := withPage()
	// Use JavaScript to set the value, as rod's Select matches by text
	js := fmt.Sprintf(`() => {
		const el = document.querySelector(%q);
		if (!el) throw new Error('element not found');
		el.value = %q;
		el.dispatchEvent(new Event('change', {bubbles: true}));
		return el.value;
	}`, args[0], args[1])
	result, err := page.Eval(js)
	if err != nil {
		fatal("select failed: %v", err)
	}
	fmt.Printf("Selected: %s\n", result.Value.Str())
}

func cmdSubmit(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney submit <selector>")
	}
	_, _, page := withPage()
	_, err := page.Element(args[0])
	if err != nil {
		fatal("form not found: %v", err)
	}
	page.MustEval(fmt.Sprintf(`() => document.querySelector(%q).submit()`, args[0]))
	fmt.Println("Submitted")
}

func cmdHover(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney hover <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustHover()
	fmt.Println("Hovered")
}

func cmdFocus(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney focus <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustFocus()
	fmt.Println("Focused")
}

func cmdWait(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney wait <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustWaitVisible()
	fmt.Println("Element visible")
}

func cmdWaitLoad(args []string) {
	_, _, page := withPage()
	page.MustWaitLoad()
	fmt.Println("Page loaded")
}

func cmdWaitStable(args []string) {
	_, _, page := withPage()
	page.MustWaitStable()
	fmt.Println("DOM stable")
}

func cmdWaitIdle(args []string) {
	_, _, page := withPage()
	page.MustWaitIdle()
	fmt.Println("Network idle")
}

func cmdSleep(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney sleep <seconds>")
	}
	secs, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		fatal("invalid seconds: %v", err)
	}
	time.Sleep(time.Duration(secs * float64(time.Second)))
}

// nextAvailableFile returns "base+ext" if it doesn't exist,
// otherwise "base-2+ext", "base-3+ext", etc.
func nextAvailableFile(base, ext string) string {
	name := base + ext
	if _, err := os.Stat(name); os.IsNotExist(err) {
		return name
	}
	for i := 2; ; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return name
		}
	}
}

func cmdScreenshot(args []string) {
	var file string
	width := 1280
	height := 0
	fullPage := true

	// Parse flags and positional args
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-w", "--width":
			i++
			if i >= len(args) {
				fatal("missing value for %s", args[i-1])
			}
			v, err := strconv.Atoi(args[i])
			if err != nil {
				fatal("invalid width: %v", err)
			}
			width = v
		case "-h", "--height":
			i++
			if i >= len(args) {
				fatal("missing value for %s", args[i-1])
			}
			v, err := strconv.Atoi(args[i])
			if err != nil {
				fatal("invalid height: %v", err)
			}
			height = v
			fullPage = false
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) > 0 {
		file = positional[0]
	} else {
		file = nextAvailableFile("screenshot", ".png")
	}

	_, _, page := withPage()

	// Set viewport size
	viewportHeight := height
	if viewportHeight == 0 {
		viewportHeight = 720
	}
	err := proto.EmulationSetDeviceMetricsOverride{
		Width:             width,
		Height:            viewportHeight,
		DeviceScaleFactor: 1,
	}.Call(page)
	if err != nil {
		fatal("failed to set viewport: %v", err)
	}

	data, err := page.Screenshot(fullPage, nil)
	if err != nil {
		fatal("screenshot failed: %v", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write screenshot: %v", err)
	}
	fmt.Println(file)
}

func cmdScreenshotEl(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney screenshot-el <selector> [file]")
	}
	file := "element.png"
	if len(args) > 1 {
		file = args[1]
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	if err != nil {
		fatal("screenshot failed: %v", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write screenshot: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", file, len(data))
}

func cmdPages(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	for i, p := range pages {
		marker := " "
		if i == s.ActivePage {
			marker = "*"
		}
		info, _ := p.Info()
		if info != nil {
			fmt.Printf("%s [%d] %s - %s\n", marker, i, info.Title, info.URL)
		} else {
			fmt.Printf("%s [%d] (unknown)\n", marker, i)
		}
	}
}

func cmdPage(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney page <index>")
	}
	idx, err := strconv.Atoi(args[0])
	if err != nil {
		fatal("invalid index: %v", err)
	}
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	if idx < 0 || idx >= len(pages) {
		fatal("page index %d out of range (0-%d)", idx, len(pages)-1)
	}
	s.ActivePage = idx
	if err := saveState(s); err != nil {
		fatal("failed to save state: %v", err)
	}
	info, _ := pages[idx].Info()
	if info != nil {
		fmt.Printf("Switched to [%d] %s - %s\n", idx, info.Title, info.URL)
	}
}

func cmdNewPage(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	url := ""
	if len(args) > 0 {
		url = args[0]
		if url != "about:blank" && !strings.Contains(url, "://") {
			url = "http://" + url
		}
	}

	var page *rod.Page
	if url == "" || url == "about:blank" {
		// Use TargetCreateTarget for blank pages — MustPage panics on about:blank
		p, err := browser.Page(proto.TargetCreateTarget{URL: ""})
		if err != nil {
			fatal("failed to create new page: %v", err)
		}
		page = p
	} else {
		page = browser.MustPage(url)
		page.MustWaitLoad()
	}

	// Switch active to the new page
	pages, _ := browser.Pages()
	for i, p := range pages {
		if p.TargetID == page.TargetID {
			s.ActivePage = i
			break
		}
	}
	saveState(s)

	info, _ := page.Info()
	if info != nil {
		fmt.Printf("Opened [%d] %s\n", s.ActivePage, info.URL)
	}
}

func cmdClosePage(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	if len(pages) <= 1 {
		fatal("cannot close the last page")
	}

	idx := s.ActivePage
	if len(args) > 0 {
		idx, err = strconv.Atoi(args[0])
		if err != nil {
			fatal("invalid index: %v", err)
		}
	}
	if idx < 0 || idx >= len(pages) {
		fatal("page index %d out of range", idx)
	}

	pages[idx].MustClose()

	// Adjust active page
	if s.ActivePage >= len(pages)-1 {
		s.ActivePage = len(pages) - 2
	}
	if s.ActivePage < 0 {
		s.ActivePage = 0
	}
	saveState(s)
	fmt.Printf("Closed page %d\n", idx)
}

func cmdExists(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney exists <selector>")
	}
	_, _, page := withPage()
	has, _, err := page.Has(args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}
	if has {
		fmt.Println("true")
		os.Exit(0)
	} else {
		fmt.Println("false")
		os.Exit(1)
	}
}

func cmdCount(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney count <selector>")
	}
	_, _, page := withPage()
	els, err := page.Elements(args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}
	fmt.Println(len(els))
}

func cmdVisible(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney visible <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fmt.Println("false")
		os.Exit(1)
	}
	visible, err := el.Visible()
	if err != nil {
		fmt.Println("false")
		os.Exit(1)
	}
	if visible {
		fmt.Println("true")
		os.Exit(0)
	} else {
		fmt.Println("false")
		os.Exit(1)
	}
}

// Ignore SIGPIPE for piped output
func init() {
	signal.Ignore(syscall.SIGPIPE)
}

// --- Accessibility commands ---

func cmdAXTree(args []string) {
	var depth *int
	jsonOutput := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--depth":
			i++
			if i >= len(args) {
				fatal("missing value for --depth")
			}
			v, err := strconv.Atoi(args[i])
			if err != nil {
				fatal("invalid depth: %v", err)
			}
			depth = &v
		case "--json":
			jsonOutput = true
		default:
			fatal("unknown flag: %s\nusage: rodney ax-tree [--depth N] [--json]", args[i])
		}
	}

	_, _, page := withPage()
	result, err := proto.AccessibilityGetFullAXTree{Depth: depth}.Call(page)
	if err != nil {
		fatal("failed to get accessibility tree: %v", err)
	}

	if jsonOutput {
		fmt.Println(formatAXTreeJSON(result.Nodes))
	} else {
		fmt.Print(formatAXTree(result.Nodes))
	}
}

func cmdAXFind(args []string) {
	var name, role string
	jsonOutput := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			if i >= len(args) {
				fatal("missing value for --name")
			}
			name = args[i]
		case "--role":
			i++
			if i >= len(args) {
				fatal("missing value for --role")
			}
			role = args[i]
		case "--json":
			jsonOutput = true
		default:
			fatal("unknown flag: %s\nusage: rodney ax-find [--name N] [--role R] [--json]", args[i])
		}
	}

	_, _, page := withPage()
	nodes, err := queryAXNodes(page, name, role)
	if err != nil {
		fatal("query failed: %v", err)
	}

	if len(nodes) == 0 {
		fmt.Fprintln(os.Stderr, "No matching nodes")
		os.Exit(1)
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(nodes, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Print(formatAXNodeList(nodes))
	}
}

func cmdAXNode(args []string) {
	jsonOutput := false
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 1 {
		fatal("usage: rodney ax-node <selector> [--json]")
	}
	selector := positional[0]

	_, _, page := withPage()
	node, err := getAXNode(page, selector)
	if err != nil {
		fatal("%v", err)
	}

	if jsonOutput {
		fmt.Println(formatAXNodeDetailJSON(node))
	} else {
		fmt.Print(formatAXNodeDetail(node))
	}
}

// queryAXNodes uses Accessibility.queryAXTree to find nodes by name and/or role.
func queryAXNodes(page *rod.Page, name, role string) ([]*proto.AccessibilityAXNode, error) {
	// Get the document node to use as query root
	zero := 0
	doc, err := proto.DOMGetDocument{Depth: &zero}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to get document: %w", err)
	}

	result, err := proto.AccessibilityQueryAXTree{
		BackendNodeID: doc.Root.BackendNodeID,
		AccessibleName: name,
		Role:           role,
	}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("accessibility query failed: %w", err)
	}

	return result.Nodes, nil
}

// getAXNode gets the accessibility node for a DOM element identified by CSS selector.
func getAXNode(page *rod.Page, selector string) (*proto.AccessibilityAXNode, error) {
	el, err := page.Element(selector)
	if err != nil {
		return nil, fmt.Errorf("element not found: %w", err)
	}

	// Describe the DOM node to get its backend node ID
	node, err := proto.DOMDescribeNode{ObjectID: el.Object.ObjectID}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to describe DOM node: %w", err)
	}

	result, err := proto.AccessibilityGetPartialAXTree{
		BackendNodeID:  node.Node.BackendNodeID,
		FetchRelatives: false,
	}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to get accessibility info: %w", err)
	}

	// Find the non-ignored node (the first non-ignored node is typically our target)
	for _, n := range result.Nodes {
		if !n.Ignored {
			return n, nil
		}
	}

	// Fall back to first node if all are ignored
	if len(result.Nodes) > 0 {
		return result.Nodes[0], nil
	}

	return nil, fmt.Errorf("no accessibility node found for selector %q", selector)
}

// axValueStr extracts a printable string from an AccessibilityAXValue.
func axValueStr(v *proto.AccessibilityAXValue) string {
	if v == nil {
		return ""
	}
	raw := v.Value.JSON("", "")
	// Unquote JSON strings
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			return s
		}
	}
	return raw
}

// formatAXTree formats a flat list of AX nodes as an indented text tree.
// Ignored nodes are skipped.
func formatAXTree(nodes []*proto.AccessibilityAXNode) string {
	if len(nodes) == 0 {
		return ""
	}

	// Build lookup maps
	nodeByID := make(map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode)
	for _, n := range nodes {
		nodeByID[n.NodeID] = n
	}

	// Find root (node with no parent or first node)
	var rootID proto.AccessibilityAXNodeID
	for _, n := range nodes {
		if n.ParentID == "" {
			rootID = n.NodeID
			break
		}
	}
	if rootID == "" && len(nodes) > 0 {
		rootID = nodes[0].NodeID
	}

	var sb strings.Builder
	var walk func(id proto.AccessibilityAXNodeID, depth int)
	walk = func(id proto.AccessibilityAXNodeID, depth int) {
		node, ok := nodeByID[id]
		if !ok {
			return
		}
		// Skip ignored nodes but still recurse into their children
		if !node.Ignored {
			indent := strings.Repeat("  ", depth)
			role := axValueStr(node.Role)
			name := axValueStr(node.Name)

			line := fmt.Sprintf("%s[%s]", indent, role)
			if name != "" {
				line += fmt.Sprintf(" %q", name)
			}

			// Append interesting properties
			props := formatProperties(node.Properties)
			if props != "" {
				line += " (" + props + ")"
			}

			sb.WriteString(line + "\n")
			// Children at depth+1
			for _, childID := range node.ChildIDs {
				walk(childID, depth+1)
			}
		} else {
			// Ignored node: pass through to children at same depth
			for _, childID := range node.ChildIDs {
				walk(childID, depth)
			}
		}
	}

	walk(rootID, 0)
	return sb.String()
}

// formatProperties formats the interesting AX properties into a comma-separated string.
func formatProperties(props []*proto.AccessibilityAXProperty) string {
	if len(props) == 0 {
		return ""
	}
	var parts []string
	for _, p := range props {
		val := axValueStr(p.Value)
		switch string(p.Name) {
		case "focusable", "disabled", "editable", "hidden", "required",
			"checked", "expanded", "selected", "modal", "multiline",
			"multiselectable", "readonly", "focused", "settable":
			// Boolean-ish properties: only show if true
			if val == "true" {
				parts = append(parts, string(p.Name))
			}
		case "level":
			parts = append(parts, fmt.Sprintf("level=%s", val))
		case "autocomplete", "hasPopup", "orientation", "live",
			"relevant", "valuemin", "valuemax", "valuetext",
			"roledescription", "keyshortcuts":
			if val != "" {
				parts = append(parts, fmt.Sprintf("%s=%s", p.Name, val))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// formatAXTreeJSON formats nodes as a JSON array.
func formatAXTreeJSON(nodes []*proto.AccessibilityAXNode) string {
	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(data)
}

// formatAXNodeList formats a list of nodes as single-line summaries.
func formatAXNodeList(nodes []*proto.AccessibilityAXNode) string {
	var sb strings.Builder
	for _, node := range nodes {
		role := axValueStr(node.Role)
		name := axValueStr(node.Name)
		line := fmt.Sprintf("[%s]", role)
		if name != "" {
			line += fmt.Sprintf(" %q", name)
		}
		if node.BackendDOMNodeID != 0 {
			line += fmt.Sprintf(" backendNodeId=%d", node.BackendDOMNodeID)
		}
		props := formatProperties(node.Properties)
		if props != "" {
			line += " (" + props + ")"
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// formatAXNodeDetail formats a single node with all its properties in key: value format.
func formatAXNodeDetail(node *proto.AccessibilityAXNode) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("role: %s\n", axValueStr(node.Role)))
	if name := axValueStr(node.Name); name != "" {
		sb.WriteString(fmt.Sprintf("name: %s\n", name))
	}
	if desc := axValueStr(node.Description); desc != "" {
		sb.WriteString(fmt.Sprintf("description: %s\n", desc))
	}
	if val := axValueStr(node.Value); val != "" {
		sb.WriteString(fmt.Sprintf("value: %s\n", val))
	}
	for _, p := range node.Properties {
		val := axValueStr(p.Value)
		sb.WriteString(fmt.Sprintf("%s: %s\n", p.Name, val))
	}
	return sb.String()
}

// formatAXNodeDetailJSON formats a single node as JSON.
func formatAXNodeDetailJSON(node *proto.AccessibilityAXNode) string {
	data, err := json.MarshalIndent(node, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// --- Network tracking commands ---

func cmdNetwork(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rodney network <subcommand>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  list [--json] [--filter <pattern>]  List network requests")
		fmt.Fprintln(os.Stderr, "  filter <pattern>                    Filter requests by body content")
		fmt.Fprintln(os.Stderr, "  clear                               Clear network log")
		fmt.Fprintln(os.Stderr, "  save <file.json>                    Save network log to file")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "list":
		cmdNetworkList(subargs)
	case "filter":
		cmdNetworkFilter(subargs)
	case "clear":
		cmdNetworkClear(subargs)
	case "save":
		cmdNetworkSave(subargs)
	default:
		fmt.Fprintf(os.Stderr, "unknown network subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func cmdNetworkList(args []string) {
	jsonOutput := false
	filterURL := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--filter":
			i++
			if i >= len(args) {
				fatal("missing value for --filter")
			}
			filterURL = args[i]
		default:
			fatal("unknown flag: %s\nusage: rodney network list [--json] [--filter <pattern>]", args[i])
		}
	}

	// Use JS to extract network info from the browser's performance API
	_, _, page := withPage()

	js := `() => {
		const entries = performance.getEntries()
			.filter(e => e.entryType === 'resource' || e.entryType === 'navigation')
			.map(e => ({
				url: e.name,
				method: 'GET',
				type: e.initiatorType || e.entryType,
				startTime: e.startTime,
				duration: e.duration,
				transferSize: e.transferSize,
				responseStatus: e.responseStatus || 0
			}));
		return entries;
	}`

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to get network info: %v", err)
	}

	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(result.Value.JSON("", "")), &entries); err != nil {
		fatal("failed to parse network data: %v", err)
	}

	// Filter if needed
	var filtered []map[string]interface{}
	for _, entry := range entries {
		url := entry["url"].(string)
		if filterURL == "" || strings.Contains(url, filterURL) {
			filtered = append(filtered, entry)
		}
	}

	if len(filtered) == 0 {
		fmt.Fprintln(os.Stderr, "No network requests found")
		os.Exit(1)
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(filtered, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("%-10s %-60s %-12s %-10s %s\n", "METHOD", "URL", "TYPE", "SIZE", "DURATION")
		fmt.Println(strings.Repeat("-", 120))
		for _, entry := range filtered {
			method := "GET"
			if m, ok := entry["method"].(string); ok {
				method = m
			}

			url := entry["url"].(string)
			if len(url) > 60 {
				url = url[:57] + "..."
			}

			resourceType := "-"
			if t, ok := entry["type"].(string); ok {
				resourceType = t
			}

			size := "-"
			if s, ok := entry["transferSize"].(float64); ok && s > 0 {
				if s < 1024 {
					size = fmt.Sprintf("%.0fB", s)
				} else if s < 1024*1024 {
					size = fmt.Sprintf("%.1fKB", s/1024)
				} else {
					size = fmt.Sprintf("%.1fMB", s/1024/1024)
				}
			}

			duration := "-"
			if d, ok := entry["duration"].(float64); ok && d > 0 {
				duration = fmt.Sprintf("%.0fms", d)
			}

			fmt.Printf("%-10s %-60s %-12s %-10s %s\n",
				method, url, resourceType, size, duration)
		}
		fmt.Printf("\nTotal requests: %d\n", len(filtered))
	}
}

func cmdNetworkFilter(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney network filter <pattern>")
	}
	pattern := args[0]

	_, _, page := withPage()

	// Enable network tracking to get request IDs
	err := proto.NetworkEnable{}.Call(page)
	if err != nil {
		fatal("failed to enable network tracking: %v", err)
	}

	// Get all performance entries to find resource requests
	js := `() => {
		return performance.getEntries()
			.filter(e => e.entryType === 'resource' || e.entryType === 'navigation')
			.map(e => ({
				url: e.name,
				type: e.initiatorType || e.entryType
			}));
	}`

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to get network info: %v", err)
	}

	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(result.Value.JSON("", "")), &entries); err != nil {
		fatal("failed to parse network data: %v", err)
	}

	// Use CDP to intercept and search through network traffic
	// We'll search through request/response bodies
	matches := []map[string]interface{}{}

	// Get network requests via CDP - this is a workaround to search bodies
	// We'll use JS to fetch resources again and search their content
	for _, entry := range entries {
		urlStr := entry["url"].(string)

		// Try to fetch and search the response body
		searchJS := fmt.Sprintf(`async () => {
			try {
				const response = await fetch(%q);
				const text = await response.text();
				if (text.includes(%q)) {
					return {
						url: %q,
						type: %q,
						matched: true,
						contentType: response.headers.get('content-type'),
						// Extract context around match
						snippet: text.substring(
							Math.max(0, text.indexOf(%q) - 100),
							Math.min(text.length, text.indexOf(%q) + 100)
						)
					};
				}
				return null;
			} catch (e) {
				return null;
			}
		}`, urlStr, pattern, urlStr, entry["type"], pattern, pattern)

		matchResult, err := page.Eval(searchJS)
		if err != nil {
			continue // Skip errors (CORS, etc.)
		}

		matchJSON := matchResult.Value.JSON("", "")
		if matchJSON != "null" {
			var match map[string]interface{}
			if err := json.Unmarshal([]byte(matchJSON), &match); err == nil {
				if match != nil && match["matched"] == true {
					matches = append(matches, match)
				}
			}
		}
	}

	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "No requests found matching pattern: %s\n", pattern)
		fmt.Fprintln(os.Stderr, "Note: Only accessible resources can be searched (CORS restrictions apply)")
		os.Exit(1)
	}

	// Display matches
	fmt.Printf("Found %d request(s) matching pattern: %s\n\n", len(matches), pattern)
	for i, match := range matches {
		fmt.Printf("[%d] %s\n", i+1, match["url"])
		if ct, ok := match["contentType"].(string); ok {
			fmt.Printf("    Content-Type: %s\n", ct)
		}
		if snippet, ok := match["snippet"].(string); ok {
			// Clean up and display snippet
			snippet = strings.ReplaceAll(snippet, "\n", " ")
			snippet = strings.ReplaceAll(snippet, "\r", "")
			if len(snippet) > 200 {
				snippet = snippet[:197] + "..."
			}
			fmt.Printf("    Match: ...%s...\n", snippet)
		}
		fmt.Println()
	}
}

func cmdNetworkClear(args []string) {
	_, _, page := withPage()

	// Clear performance entries
	page.MustEval(`() => { performance.clearResourceTimings(); performance.clearMarks(); performance.clearMeasures(); }`)

	fmt.Println("Network log cleared")
}

func cmdNetworkSave(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney network save <file.json>")
	}
	file := args[0]

	_, _, page := withPage()

	// Get all network entries from Performance API
	js := `() => {
		const entries = performance.getEntries()
			.filter(e => e.entryType === 'resource' || e.entryType === 'navigation')
			.map(e => ({
				url: e.name,
				method: 'GET',
				type: e.initiatorType || e.entryType,
				startTime: e.startTime,
				duration: e.duration,
				transferSize: e.transferSize,
				encodedBodySize: e.encodedBodySize,
				decodedBodySize: e.decodedBodySize,
				responseStatus: e.responseStatus || 0,
				protocol: e.nextHopProtocol
			}));
		return entries;
	}`

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to get network info: %v", err)
	}

	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(result.Value.JSON("", "")), &entries); err != nil {
		fatal("failed to parse network data: %v", err)
	}

	if len(entries) == 0 {
		fatal("no network requests to save")
	}

	// Format as HAR-like structure
	data, err := json.MarshalIndent(map[string]interface{}{
		"log": map[string]interface{}{
			"version": "1.2",
			"creator": map[string]string{
				"name":    "rodney",
				"version": version,
			},
			"entries": entries,
		},
	}, "", "  ")
	if err != nil {
		fatal("failed to marshal network log: %v", err)
	}

	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write file: %v", err)
	}

	fmt.Printf("Saved %d network requests to %s\n", len(entries), file)
}

// --- Scroll command ---

func cmdScroll(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney scroll <up|down|left|right|top|bottom|selector> [pixels]")
	}

	_, _, page := withPage()
	direction := args[0]
	pixels := 300.0

	if len(args) > 1 {
		v, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			fatal("invalid pixel value: %v", err)
		}
		pixels = v
	}

	switch direction {
	case "up":
		if err := page.Mouse.Scroll(0, -pixels, 1); err != nil {
			fatal("scroll failed: %v", err)
		}
		fmt.Printf("Scrolled up %.0fpx\n", pixels)
	case "down":
		if err := page.Mouse.Scroll(0, pixels, 1); err != nil {
			fatal("scroll failed: %v", err)
		}
		fmt.Printf("Scrolled down %.0fpx\n", pixels)
	case "left":
		if err := page.Mouse.Scroll(-pixels, 0, 1); err != nil {
			fatal("scroll failed: %v", err)
		}
		fmt.Printf("Scrolled left %.0fpx\n", pixels)
	case "right":
		if err := page.Mouse.Scroll(pixels, 0, 1); err != nil {
			fatal("scroll failed: %v", err)
		}
		fmt.Printf("Scrolled right %.0fpx\n", pixels)
	case "top":
		page.MustEval(`() => window.scrollTo(0, 0)`)
		fmt.Println("Scrolled to top")
	case "bottom":
		page.MustEval(`() => window.scrollTo(0, document.body.scrollHeight)`)
		fmt.Println("Scrolled to bottom")
	default:
		// Treat as CSS selector
		el, err := page.Element(direction)
		if err != nil {
			fatal("element not found: %v", err)
		}
		if err := el.ScrollIntoView(); err != nil {
			fatal("scroll into view failed: %v", err)
		}
		fmt.Println("Scrolled into view")
	}
}

// --- Key command ---

var keyNameMap = map[string]input.Key{
	"enter":     input.Enter,
	"tab":       input.Tab,
	"escape":    input.Escape,
	"esc":       input.Escape,
	"backspace": input.Backspace,
	"delete":    input.Delete,
	"space":     input.Space,
	"arrowup":   input.ArrowUp,
	"arrowdown": input.ArrowDown,
	"arrowleft": input.ArrowLeft,
	"arrowright":input.ArrowRight,
	"up":        input.ArrowUp,
	"down":      input.ArrowDown,
	"left":      input.ArrowLeft,
	"right":     input.ArrowRight,
	"home":      input.Home,
	"end":       input.End,
	"pageup":    input.PageUp,
	"pagedown":  input.PageDown,
	"f1":        input.F1,
	"f2":        input.F2,
	"f3":        input.F3,
	"f4":        input.F4,
	"f5":        input.F5,
	"f6":        input.F6,
	"f7":        input.F7,
	"f8":        input.F8,
	"f9":        input.F9,
	"f10":       input.F10,
	"f11":       input.F11,
	"f12":       input.F12,
}

var modifierMap = map[string]input.Key{
	"ctrl":    input.ControlLeft,
	"control": input.ControlLeft,
	"shift":   input.ShiftLeft,
	"alt":     input.AltLeft,
	"meta":    input.MetaLeft,
	"cmd":     input.MetaLeft,
}

func resolveKey(name string) (input.Key, bool) {
	lower := strings.ToLower(name)
	if k, ok := keyNameMap[lower]; ok {
		return k, true
	}
	if k, ok := modifierMap[lower]; ok {
		return k, true
	}
	// Single character
	if len(name) == 1 {
		return input.Key(rune(name[0])), true
	}
	return 0, false
}

func cmdKey(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney key <Enter|ctrl+a|text>")
	}
	_, _, page := withPage()

	arg := strings.Join(args, " ")

	// Key combo: contains + and no spaces (e.g. ctrl+a, shift+Tab)
	if strings.Contains(arg, "+") && !strings.Contains(arg, " ") {
		parts := strings.Split(arg, "+")
		if len(parts) < 2 {
			fatal("invalid key combo: %s", arg)
		}

		// All but the last are modifiers, last is the key
		ka := page.KeyActions()
		for _, modName := range parts[:len(parts)-1] {
			mod, ok := modifierMap[strings.ToLower(modName)]
			if !ok {
				fatal("unknown modifier: %s", modName)
			}
			ka = ka.Press(mod)
		}

		keyName := parts[len(parts)-1]
		key, ok := resolveKey(keyName)
		if !ok {
			fatal("unknown key: %s", keyName)
		}
		ka = ka.Type(key)

		// Release modifiers in reverse
		for i := len(parts) - 2; i >= 0; i-- {
			mod := modifierMap[strings.ToLower(parts[i])]
			ka = ka.Release(mod)
		}

		if err := ka.Do(); err != nil {
			fatal("key combo failed: %v", err)
		}
		fmt.Printf("Pressed %s\n", arg)
		return
	}

	// Named key (e.g. Enter, Tab, Escape)
	if key, ok := resolveKey(arg); ok && (len(arg) > 1 || strings.ToLower(arg) != arg) {
		// Only treat as named key if it's multi-char or a single uppercase letter
		// This avoids treating "a" as a named key when the user wants to type "a"
		if len(arg) > 1 {
			if err := page.Keyboard.Type(key); err != nil {
				fatal("key press failed: %v", err)
			}
			fmt.Printf("Pressed %s\n", arg)
			return
		}
	}

	// Type as text
	keys := make([]input.Key, 0, len(arg))
	for _, r := range arg {
		keys = append(keys, input.Key(r))
	}
	if err := page.Keyboard.Type(keys...); err != nil {
		fatal("type failed: %v", err)
	}
	fmt.Printf("Typed: %s\n", arg)
}

// --- Waitfor command ---

func cmdWaitFor(args []string) {
	timeout := defaultTimeout
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--timeout":
			i++
			if i >= len(args) {
				fatal("missing value for --timeout")
			}
			secs, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				fatal("invalid timeout: %v", err)
			}
			timeout = time.Duration(secs * float64(time.Second))
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 1 {
		fatal("usage: rodney waitfor [--timeout N] <js-expression>")
	}

	expr := strings.Join(positional, " ")
	_, _, page := withPage()

	// Override the page timeout for this wait
	page = page.Timeout(timeout)

	js := fmt.Sprintf(`() => !!(%s)`, expr)
	if err := page.Wait(rod.Eval(js)); err != nil {
		fatal("wait failed: %v", err)
	}
	fmt.Println("Condition met")
}

// --- Perf command group ---

func cmdPerf(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rodney perf <subcommand>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  metrics [--json]    Show runtime performance metrics")
		fmt.Fprintln(os.Stderr, "  vitals [--json]     Show Core Web Vitals (LCP, CLS, TTFB)")
		fmt.Fprintln(os.Stderr, "  timing [--json]     Show navigation timing breakdown")
		fmt.Fprintln(os.Stderr, "  profile <secs> [file]  Record CPU profile for N seconds")
		fmt.Fprintln(os.Stderr, "  trace <secs> [file]    Record browser trace for N seconds")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "metrics":
		cmdPerfMetrics(subargs)
	case "vitals":
		cmdPerfVitals(subargs)
	case "timing":
		cmdPerfTiming(subargs)
	case "profile":
		cmdPerfProfile(subargs)
	case "trace":
		cmdPerfTrace(subargs)
	default:
		fmt.Fprintf(os.Stderr, "unknown perf subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func cmdPerfMetrics(args []string) {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else {
			fatal("unknown flag: %s\nusage: rodney perf metrics [--json]", arg)
		}
	}

	_, _, page := withPage()

	err := proto.PerformanceEnable{}.Call(page)
	if err != nil {
		fatal("failed to enable performance: %v", err)
	}

	result, err := proto.PerformanceGetMetrics{}.Call(page)
	if err != nil {
		fatal("failed to get metrics: %v", err)
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(result.Metrics, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("%-40s %s\n", "METRIC", "VALUE")
		fmt.Println(strings.Repeat("-", 60))
		for _, m := range result.Metrics {
			fmt.Printf("%-40s %.4f\n", m.Name, m.Value)
		}
	}
}

func cmdPerfVitals(args []string) {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else {
			fatal("unknown flag: %s\nusage: rodney perf vitals [--json]", arg)
		}
	}

	_, _, page := withPage()

	js := `() => {
		const result = {};

		// LCP
		const lcpEntries = performance.getEntriesByType('largest-contentful-paint');
		if (lcpEntries.length > 0) {
			result.lcp = lcpEntries[lcpEntries.length - 1].startTime;
		}

		// CLS
		let cls = 0;
		const layoutShifts = performance.getEntriesByType('layout-shift');
		for (const entry of layoutShifts) {
			if (!entry.hadRecentInput) {
				cls += entry.value;
			}
		}
		result.cls = cls;

		// TTFB
		const navEntries = performance.getEntriesByType('navigation');
		if (navEntries.length > 0) {
			result.ttfb = navEntries[0].responseStart;
		}

		return result;
	}`

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to get vitals: %v", err)
	}

	if jsonOutput {
		fmt.Println(result.Value.JSON("", "  "))
	} else {
		data := result.Value
		fmt.Println("Core Web Vitals:")
		fmt.Println(strings.Repeat("-", 40))
		if lcp := data.Get("lcp"); lcp.Num() > 0 || lcp.JSON("", "") != "" {
			fmt.Printf("  LCP  (Largest Contentful Paint): %.1fms\n", lcp.Num())
		} else {
			fmt.Println("  LCP  (Largest Contentful Paint): n/a")
		}
		fmt.Printf("  CLS  (Cumulative Layout Shift):  %.4f\n", data.Get("cls").Num())
		if ttfb := data.Get("ttfb"); ttfb.Num() > 0 || ttfb.JSON("", "") != "" {
			fmt.Printf("  TTFB (Time to First Byte):       %.1fms\n", ttfb.Num())
		} else {
			fmt.Println("  TTFB (Time to First Byte):       n/a")
		}
	}
}

func cmdPerfTiming(args []string) {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else {
			fatal("unknown flag: %s\nusage: rodney perf timing [--json]", arg)
		}
	}

	_, _, page := withPage()

	js := `() => {
		const nav = performance.getEntriesByType('navigation')[0];
		if (!nav) return null;
		return {
			redirect:        nav.redirectEnd - nav.redirectStart,
			dns:             nav.domainLookupEnd - nav.domainLookupStart,
			tcp:             nav.connectEnd - nav.connectStart,
			tls:             nav.secureConnectionStart > 0 ? nav.connectEnd - nav.secureConnectionStart : 0,
			ttfb:            nav.responseStart - nav.requestStart,
			contentDownload: nav.responseEnd - nav.responseStart,
			domInteractive:  nav.domInteractive - nav.responseEnd,
			domComplete:     nav.domComplete - nav.domInteractive,
			loadEvent:       nav.loadEventEnd - nav.loadEventStart,
			total:           nav.duration
		};
	}`

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to get timing: %v", err)
	}

	raw := result.Value.JSON("", "")
	if raw == "null" {
		fatal("no navigation timing data available")
	}

	if jsonOutput {
		fmt.Println(result.Value.JSON("", "  "))
	} else {
		data := result.Value
		fmt.Println("Navigation Timing:")
		fmt.Println(strings.Repeat("-", 40))
		timings := []struct{ label, key string }{
			{"Redirect", "redirect"},
			{"DNS Lookup", "dns"},
			{"TCP Connect", "tcp"},
			{"TLS Handshake", "tls"},
			{"TTFB", "ttfb"},
			{"Content Download", "contentDownload"},
			{"DOM Interactive", "domInteractive"},
			{"DOM Complete", "domComplete"},
			{"Load Event", "loadEvent"},
			{"Total", "total"},
		}
		for _, t := range timings {
			fmt.Printf("  %-20s %8.1fms\n", t.label, data.Get(t.key).Num())
		}
	}
}

// --- CPU profiling ---

func cmdPerfProfile(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rodney perf profile <seconds> [file.cpuprofile]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Records a CPU profile for the given duration.")
		fmt.Fprintln(os.Stderr, "  Open with: npx speedscope <file>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Tip: set up scrolling/actions BEFORE profiling via rodney js:")
		fmt.Fprintln(os.Stderr, `    rodney js "setInterval(() => scrollBy(0,100), 50)"`)
		fmt.Fprintln(os.Stderr, "    rodney perf profile 3 scroll.cpuprofile")
		os.Exit(1)
	}

	duration, err := strconv.ParseFloat(args[0], 64)
	if err != nil || duration <= 0 {
		fatal("invalid duration: %s (must be a positive number of seconds)", args[0])
	}

	file := "profile.cpuprofile"
	if len(args) > 1 {
		file = args[1]
	}

	_, _, page := withPage()

	err = proto.ProfilerEnable{}.Call(page)
	if err != nil {
		fatal("failed to enable profiler: %v", err)
	}

	err = proto.ProfilerStart{}.Call(page)
	if err != nil {
		fatal("failed to start profiling: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Profiling for %.1fs...\n", duration)
	time.Sleep(time.Duration(duration * float64(time.Second)))

	result, err := proto.ProfilerStop{}.Call(page)
	if err != nil {
		fatal("failed to stop profiling: %v", err)
	}

	data, err := json.MarshalIndent(result.Profile, "", "  ")
	if err != nil {
		fatal("failed to marshal profile: %v", err)
	}

	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write profile: %v", err)
	}

	fmt.Printf("Saved CPU profile to %s (%d bytes)\n", file, len(data))
	fmt.Println("Open with: npx speedscope " + file)
}

// --- Browser tracing ---

func cmdPerfTrace(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rodney perf trace <seconds> [file.json]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Records a full browser trace for the given duration.")
		fmt.Fprintln(os.Stderr, "  Open with: https://ui.perfetto.dev")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Tip: set up scrolling/actions BEFORE tracing via rodney js:")
		fmt.Fprintln(os.Stderr, `    rodney js "setInterval(() => scrollBy(0,100), 50)"`)
		fmt.Fprintln(os.Stderr, "    rodney perf trace 3 scroll-trace.json")
		os.Exit(1)
	}

	duration, err := strconv.ParseFloat(args[0], 64)
	if err != nil || duration <= 0 {
		fatal("invalid duration: %s (must be a positive number of seconds)", args[0])
	}

	file := "trace.json"
	if len(args) > 1 {
		file = args[1]
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	// Collect trace data chunks via events
	var chunks []json.RawMessage
	done := make(chan struct{})

	go browser.EachEvent(
		func(e *proto.TracingDataCollected) {
			for _, item := range e.Value {
				data, err := json.Marshal(item)
				if err == nil {
					chunks = append(chunks, data)
				}
			}
		},
		func(e *proto.TracingTracingComplete) {
			close(done)
		},
	)()

	// Start tracing
	err = proto.TracingStart{
		Categories:   "-*,devtools.timeline,v8.execute,disabled-by-default-devtools.timeline,disabled-by-default-devtools.timeline.frame,disabled-by-default-v8.cpu_profiler",
		TransferMode: proto.TracingStartTransferModeReportEvents,
	}.Call(browser)
	if err != nil {
		fatal("failed to start tracing: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Tracing for %.1fs...\n", duration)
	time.Sleep(time.Duration(duration * float64(time.Second)))

	// End tracing
	err = proto.TracingEnd{}.Call(browser)
	if err != nil {
		fatal("failed to end tracing: %v", err)
	}

	// Wait for all data to arrive
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		fatal("timeout waiting for trace data")
	}

	// Write Chrome Trace Event format
	f, err := os.Create(file)
	if err != nil {
		fatal("failed to create file: %v", err)
	}
	defer f.Close()

	f.WriteString("[")
	for i, chunk := range chunks {
		if i > 0 {
			f.WriteString(",")
		}
		f.Write(chunk)
	}
	f.WriteString("]")

	fmt.Printf("Saved browser trace to %s\n", file)
	fmt.Println("Open with: https://ui.perfetto.dev or Chrome DevTools Performance tab")
}

// --- React profiling ---

// The JS hook injected before React loads. It intercepts React's
// renderer registration and captures commit data with component timing.
const reactHookJS = `
window.__RODNEY_REACT = {
	commits: [],
	renderers: new Map(),
	fiberRoots: new Set()
};

window.__REACT_DEVTOOLS_GLOBAL_HOOK__ = {
	renderers: new Map(),
	supportsFiber: true,
	inject: function(renderer) {
		var id = this.renderers.size + 1;
		this.renderers.set(id, renderer);
		window.__RODNEY_REACT.renderers.set(id, renderer);
		return id;
	},
	onCommitFiberRoot: function(rendererID, root, priorityLevel) {
		window.__RODNEY_REACT.fiberRoots.add(root);
		var commit = {
			timestamp: Date.now(),
			components: []
		};
		function walk(fiber, depth) {
			if (!fiber) return;
			// tag 0 = FunctionComponent, 1 = ClassComponent, 11 = ForwardRef, 15 = SimpleMemoComponent
			if (fiber.tag === 0 || fiber.tag === 1 || fiber.tag === 11 || fiber.tag === 15) {
				var name = 'Anonymous';
				if (fiber.type) {
					name = fiber.type.displayName || fiber.type.name || 'Anonymous';
				}
				commit.components.push({
					name: name,
					depth: depth,
					actualDuration: fiber.actualDuration || 0,
					selfBaseDuration: fiber.selfBaseDuration || 0,
					flags: fiber.flags || 0
				});
			}
			walk(fiber.child, depth + 1);
			walk(fiber.sibling, depth);
		}
		if (root && root.current) {
			walk(root.current, 0);
		}
		if (commit.components.length > 0) {
			window.__RODNEY_REACT.commits.push(commit);
		}
	},
	onCommitFiberUnmount: function() {},
	onPostCommitFiberRoot: function() {},
	isDisabled: false,
	checkDCE: function() {}
};
`

func cmdReact(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rodney react <subcommand>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  hook                Install React DevTools hook (run BEFORE open)")
		fmt.Fprintln(os.Stderr, "  tree [--json]       Show component tree")
		fmt.Fprintln(os.Stderr, "  renders [--json]    Show render commits with timing")
		fmt.Fprintln(os.Stderr, "  flamegraph <file>   Export renders as speedscope JSON")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "hook":
		cmdReactHook(subargs)
	case "tree":
		cmdReactTree(subargs)
	case "renders":
		cmdReactRenders(subargs)
	case "flamegraph":
		cmdReactFlamegraph(subargs)
	default:
		fmt.Fprintf(os.Stderr, "unknown react subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func cmdReactHook(args []string) {
	s, _, page := withPage()

	injectReactHook(page)

	// Persist so future open/reload commands re-inject automatically
	s.ReactHook = true
	saveState(s)

	fmt.Println("React DevTools hook installed (persists across navigations)")
	fmt.Println("Now run 'rodney open <url>' to navigate to a React app")
	fmt.Println("Then use 'rodney react renders' or 'rodney react tree' to inspect")
}

func cmdReactTree(args []string) {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else {
			fatal("unknown flag: %s\nusage: rodney react tree [--json]", arg)
		}
	}

	_, _, page := withPage()

	js := `() => {
		// Find React fiber root from DOM
		function findFiberRoot() {
			// Check for rodney hook data first
			if (window.__RODNEY_REACT && window.__RODNEY_REACT.fiberRoots.size > 0) {
				return Array.from(window.__RODNEY_REACT.fiberRoots)[0];
			}
			// Fall back to walking DOM to find fiber nodes
			const rootEl = document.getElementById('root') || document.getElementById('app') ||
				document.querySelector('[data-reactroot]') ||
				document.querySelector('#__next') || document.querySelector('#__nuxt');
			if (!rootEl) return null;
			const fiberKey = Object.keys(rootEl).find(k =>
				k.startsWith('__reactFiber$') || k.startsWith('__reactInternalInstance$')
			);
			if (!fiberKey) return null;
			// Walk up to find the root
			let fiber = rootEl[fiberKey];
			while (fiber.return) fiber = fiber.return;
			return { current: fiber };
		}

		const root = findFiberRoot();
		if (!root || !root.current) return null;

		const tree = [];
		function walk(fiber, depth) {
			if (!fiber) return;
			if (fiber.tag === 0 || fiber.tag === 1 || fiber.tag === 11 || fiber.tag === 15) {
				let name = 'Anonymous';
				if (fiber.type) {
					name = fiber.type.displayName || fiber.type.name || 'Anonymous';
				}
				const node = {
					name: name,
					depth: depth,
					actualDuration: fiber.actualDuration || 0,
					selfBaseDuration: fiber.selfBaseDuration || 0
				};
				// Get props keys (not values, to avoid circular refs)
				if (fiber.memoizedProps && typeof fiber.memoizedProps === 'object') {
					node.props = Object.keys(fiber.memoizedProps).filter(k => k !== 'children');
				}
				// Get state keys for class components
				if (fiber.memoizedState && typeof fiber.memoizedState === 'object' && !fiber.memoizedState.memoizedState) {
					node.stateKeys = Object.keys(fiber.memoizedState);
				}
				tree.push(node);
			}
			walk(fiber.child, depth + 1);
			walk(fiber.sibling, depth);
		}
		walk(root.current, 0);
		return tree;
	}`

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to get React tree: %v", err)
	}

	raw := result.Value.JSON("", "")
	if raw == "null" || raw == "[]" {
		fmt.Fprintln(os.Stderr, "No React components found")
		fmt.Fprintln(os.Stderr, "Make sure:")
		fmt.Fprintln(os.Stderr, "  1. The page uses React")
		fmt.Fprintln(os.Stderr, "  2. Run 'rodney react hook' before 'rodney open' for best results")
		os.Exit(1)
	}

	if jsonOutput {
		fmt.Println(result.Value.JSON("", "  "))
		return
	}

	var components []struct {
		Name             string   `json:"name"`
		Depth            int      `json:"depth"`
		ActualDuration   float64  `json:"actualDuration"`
		SelfBaseDuration float64  `json:"selfBaseDuration"`
		Props            []string `json:"props"`
	}
	json.Unmarshal([]byte(raw), &components)

	for _, c := range components {
		indent := strings.Repeat("  ", c.Depth)
		line := fmt.Sprintf("%s<%s>", indent, c.Name)
		if c.ActualDuration > 0 {
			line += fmt.Sprintf(" (%.1fms)", c.ActualDuration)
		}
		if len(c.Props) > 0 && len(c.Props) <= 5 {
			line += fmt.Sprintf(" props=[%s]", strings.Join(c.Props, ", "))
		} else if len(c.Props) > 5 {
			line += fmt.Sprintf(" props=[%s, ...+%d]", strings.Join(c.Props[:5], ", "), len(c.Props)-5)
		}
		fmt.Println(line)
	}
}

func cmdReactRenders(args []string) {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else {
			fatal("unknown flag: %s\nusage: rodney react renders [--json]", arg)
		}
	}

	_, _, page := withPage()

	js := `() => {
		if (!window.__RODNEY_REACT || !window.__RODNEY_REACT.commits) return null;
		return window.__RODNEY_REACT.commits;
	}`

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to get React renders: %v", err)
	}

	raw := result.Value.JSON("", "")
	if raw == "null" || raw == "[]" {
		fmt.Fprintln(os.Stderr, "No React render commits captured")
		fmt.Fprintln(os.Stderr, "Make sure you ran 'rodney react hook' before 'rodney open <url>'")
		os.Exit(1)
	}

	if jsonOutput {
		fmt.Println(result.Value.JSON("", "  "))
		return
	}

	var commits []struct {
		Timestamp  int64 `json:"timestamp"`
		Components []struct {
			Name             string  `json:"name"`
			Depth            int     `json:"depth"`
			ActualDuration   float64 `json:"actualDuration"`
			SelfBaseDuration float64 `json:"selfBaseDuration"`
		} `json:"components"`
	}
	json.Unmarshal([]byte(raw), &commits)

	for i, commit := range commits {
		ts := time.UnixMilli(commit.Timestamp).Format("15:04:05.000")
		fmt.Printf("Commit #%d [%s] (%d components)\n", i+1, ts, len(commit.Components))

		// Sort by duration desc to show slowest first
		for _, c := range commit.Components {
			indent := strings.Repeat("  ", c.Depth+1)
			if c.ActualDuration > 0 {
				fmt.Printf("%s%-30s %8.1fms (self: %.1fms)\n",
					indent, c.Name, c.ActualDuration, c.SelfBaseDuration)
			} else {
				fmt.Printf("%s%s\n", indent, c.Name)
			}
		}
		fmt.Println()
	}
	fmt.Printf("Total commits: %d\n", len(commits))
}

func cmdReactFlamegraph(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney react flamegraph <file.json>")
	}
	file := args[0]

	_, _, page := withPage()

	js := `() => {
		if (!window.__RODNEY_REACT || !window.__RODNEY_REACT.commits) return null;
		return window.__RODNEY_REACT.commits;
	}`

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to get React renders: %v", err)
	}

	raw := result.Value.JSON("", "")
	if raw == "null" || raw == "[]" {
		fmt.Fprintln(os.Stderr, "No React render commits captured")
		fmt.Fprintln(os.Stderr, "Make sure you ran 'rodney react hook' before 'rodney open <url>'")
		os.Exit(1)
	}

	var commits []struct {
		Timestamp  int64 `json:"timestamp"`
		Components []struct {
			Name             string  `json:"name"`
			Depth            int     `json:"depth"`
			ActualDuration   float64 `json:"actualDuration"`
			SelfBaseDuration float64 `json:"selfBaseDuration"`
		} `json:"components"`
	}
	json.Unmarshal([]byte(raw), &commits)

	// Build speedscope format
	// Collect unique frame names
	frameIndex := map[string]int{}
	var frames []map[string]string

	getFrame := func(name string) int {
		if idx, ok := frameIndex[name]; ok {
			return idx
		}
		idx := len(frames)
		frameIndex[name] = idx
		frames = append(frames, map[string]string{"name": name})
		return idx
	}

	// Build profiles - one per commit
	var profiles []interface{}

	for i, commit := range commits {
		// Build evented profile from component tree
		// Components are walked depth-first, so we can reconstruct the call stack
		var events []map[string]interface{}
		var at float64

		// Track open frames via depth stack
		stack := []int{} // frame indices

		for _, comp := range commit.Components {
			frameIdx := getFrame(comp.Name)
			dur := comp.SelfBaseDuration
			if dur <= 0 {
				dur = comp.ActualDuration
			}
			if dur <= 0 {
				dur = 0.1 // minimal duration for visibility
			}

			// Close frames deeper than current depth
			for len(stack) > comp.Depth {
				events = append(events, map[string]interface{}{
					"type": "C", "at": at, "frame": stack[len(stack)-1],
				})
				stack = stack[:len(stack)-1]
			}

			// Open this frame
			events = append(events, map[string]interface{}{
				"type": "O", "at": at, "frame": frameIdx,
			})
			stack = append(stack, frameIdx)
			at += dur
		}

		// Close remaining open frames
		for len(stack) > 0 {
			events = append(events, map[string]interface{}{
				"type": "C", "at": at, "frame": stack[len(stack)-1],
			})
			stack = stack[:len(stack)-1]
		}

		profiles = append(profiles, map[string]interface{}{
			"type":       "evented",
			"name":       fmt.Sprintf("Commit #%d", i+1),
			"unit":       "milliseconds",
			"startValue": 0,
			"endValue":   at,
			"events":     events,
		})
	}

	speedscope := map[string]interface{}{
		"$schema":        "https://www.speedscope.app/file-format-schema.json",
		"shared":         map[string]interface{}{"frames": frames},
		"profiles":       profiles,
		"name":           "React Renders",
		"activeProfileIndex": 0,
		"exporter":       "rodney " + version,
	}

	data, err := json.MarshalIndent(speedscope, "", "  ")
	if err != nil {
		fatal("failed to marshal flamegraph: %v", err)
	}

	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write file: %v", err)
	}

	fmt.Printf("Saved React flamegraph to %s (%d commits, %d components)\n",
		file, len(commits), len(frames))
	fmt.Println("Open with: npx speedscope " + file)
}

// --- Console command ---

func cmdConsole(args []string) {
	follow := false
	errorsOnly := false
	jsonOutput := false
	clearLog := false

	for _, arg := range args {
		switch arg {
		case "--follow":
			follow = true
		case "--errors":
			errorsOnly = true
		case "--json":
			jsonOutput = true
		case "--clear":
			clearLog = true
		default:
			fatal("unknown flag: %s\nusage: rodney console [--follow] [--errors] [--json] [--clear]", arg)
		}
	}

	_, _, page := withPage()

	if clearLog {
		page.MustEval(`() => { window.__rodney_console = []; }`)
		fmt.Println("Console log cleared")
		return
	}

	if follow {
		cmdConsoleFollow(page, errorsOnly, jsonOutput)
		return
	}

	// Snapshot mode: install monkey-patch if not already done, then read
	installed := page.MustEval(`() => !!window.__rodney_console`).Bool()
	if !installed {
		page.MustEval(`() => {
			window.__rodney_console = [];
			const orig = {};
			['log', 'warn', 'error', 'info', 'debug'].forEach(level => {
				orig[level] = console[level];
				console[level] = function(...args) {
					window.__rodney_console.push({
						type: level,
						text: args.map(a => typeof a === 'object' ? JSON.stringify(a) : String(a)).join(' '),
						timestamp: Date.now()
					});
					orig[level].apply(console, args);
				};
			});
		}`)
		fmt.Fprintln(os.Stderr, "Console hook installed. Run JS that logs, then call 'rodney console' again to read.")
		return
	}

	// Read captured messages
	result, err := page.Eval(`() => window.__rodney_console || []`)
	if err != nil {
		fatal("failed to read console: %v", err)
	}

	var messages []map[string]interface{}
	if err := json.Unmarshal([]byte(result.Value.JSON("", "")), &messages); err != nil {
		fatal("failed to parse console data: %v", err)
	}

	if errorsOnly {
		var filtered []map[string]interface{}
		for _, m := range messages {
			t := m["type"].(string)
			if t == "error" || t == "warn" {
				filtered = append(filtered, m)
			}
		}
		messages = filtered
	}

	if len(messages) == 0 {
		fmt.Fprintln(os.Stderr, "No console messages captured")
		return
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(messages, "", "  ")
		fmt.Println(string(data))
	} else {
		for _, m := range messages {
			level := strings.ToUpper(m["type"].(string))
			text := m["text"].(string)
			fmt.Printf("[%s] %s\n", level, text)
		}
	}
}

func cmdConsoleFollow(page *rod.Page, errorsOnly, jsonOutput bool) {
	err := proto.RuntimeEnable{}.Call(page)
	if err != nil {
		fatal("failed to enable runtime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	fmt.Fprintln(os.Stderr, "Streaming console output (Ctrl+C to stop)...")

	page = page.Context(ctx)
	wait := page.EachEvent(func(e *proto.RuntimeConsoleAPICalled) bool {
		level := string(e.Type)

		if errorsOnly && level != "error" && level != "warning" {
			return false
		}

		var parts []string
		for _, arg := range e.Args {
			if arg.Value.JSON("", "") != "" {
				raw := arg.Value.JSON("", "")
				if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
					var s string
					if err := json.Unmarshal([]byte(raw), &s); err == nil {
						parts = append(parts, s)
						continue
					}
				}
				parts = append(parts, raw)
			} else if arg.Description != "" {
				parts = append(parts, arg.Description)
			}
		}

		text := strings.Join(parts, " ")

		if jsonOutput {
			entry := map[string]string{"type": level, "text": text}
			data, _ := json.Marshal(entry)
			fmt.Println(string(data))
		} else {
			fmt.Printf("[%s] %s\n", strings.ToUpper(level), text)
		}
		return false // keep listening
	})

	wait()
}

// --- Cookies command group ---

func cmdCookies(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rodney cookies <subcommand>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  list [--json]              List all cookies for current page")
		fmt.Fprintln(os.Stderr, "  get <name>                 Get a cookie value by name")
		fmt.Fprintln(os.Stderr, "  set <name> <value> [flags] Set a cookie")
		fmt.Fprintln(os.Stderr, "  delete <name> [--domain D] Delete a cookie by name")
		fmt.Fprintln(os.Stderr, "  clear                      Clear all cookies")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "list":
		cmdCookiesList(subargs)
	case "get":
		cmdCookiesGet(subargs)
	case "set":
		cmdCookiesSet(subargs)
	case "delete":
		cmdCookiesDelete(subargs)
	case "clear":
		cmdCookiesClear(subargs)
	default:
		fmt.Fprintf(os.Stderr, "unknown cookies subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func cmdCookiesList(args []string) {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else {
			fatal("unknown flag: %s\nusage: rodney cookies list [--json]", arg)
		}
	}

	_, _, page := withPage()
	cookies, err := page.Cookies(nil)
	if err != nil {
		fatal("failed to get cookies: %v", err)
	}

	if len(cookies) == 0 {
		fmt.Fprintln(os.Stderr, "No cookies found")
		return
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(cookies, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("%-20s %-40s %-20s %-8s %s\n", "NAME", "VALUE", "DOMAIN", "PATH", "EXPIRES")
		fmt.Println(strings.Repeat("-", 110))
		for _, c := range cookies {
			val := c.Value
			if len(val) > 40 {
				val = val[:37] + "..."
			}
			expires := "Session"
			if c.Expires > 0 {
				t := time.Unix(int64(c.Expires), 0)
				expires = t.Format("2006-01-02 15:04")
			}
			fmt.Printf("%-20s %-40s %-20s %-8s %s\n", c.Name, val, c.Domain, c.Path, expires)
		}
		fmt.Printf("\nTotal cookies: %d\n", len(cookies))
	}
}

func cmdCookiesGet(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney cookies get <name>")
	}
	name := args[0]

	_, _, page := withPage()
	cookies, err := page.Cookies(nil)
	if err != nil {
		fatal("failed to get cookies: %v", err)
	}

	for _, c := range cookies {
		if c.Name == name {
			fmt.Println(c.Value)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "cookie %q not found\n", name)
	os.Exit(1)
}

func cmdCookiesSet(args []string) {
	var domain, path string
	var secure, httpOnly bool
	var expires float64
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--domain":
			i++
			if i >= len(args) {
				fatal("missing value for --domain")
			}
			domain = args[i]
		case "--path":
			i++
			if i >= len(args) {
				fatal("missing value for --path")
			}
			path = args[i]
		case "--secure":
			secure = true
		case "--httponly":
			httpOnly = true
		case "--expires":
			i++
			if i >= len(args) {
				fatal("missing value for --expires")
			}
			v, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				fatal("invalid expires: %v", err)
			}
			expires = v
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 2 {
		fatal("usage: rodney cookies set <name> <value> [--domain D] [--path P] [--secure] [--httponly] [--expires UNIX]")
	}

	name := positional[0]
	value := positional[1]
	_, _, page := withPage()

	param := &proto.NetworkCookieParam{
		Name:     name,
		Value:    value,
		Secure:   secure,
		HTTPOnly: httpOnly,
	}

	if domain != "" {
		param.Domain = domain
	}
	if path != "" {
		param.Path = path
	}
	if expires > 0 {
		param.Expires = proto.TimeSinceEpoch(expires)
	}

	// If no domain given, scope to current page URL
	if domain == "" {
		info, err := page.Info()
		if err == nil && info != nil {
			param.URL = info.URL
		}
	}

	if err := page.SetCookies([]*proto.NetworkCookieParam{param}); err != nil {
		fatal("failed to set cookie: %v", err)
	}
	fmt.Printf("Set cookie: %s=%s\n", name, value)
}

func cmdCookiesDelete(args []string) {
	var domain string
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--domain":
			i++
			if i >= len(args) {
				fatal("missing value for --domain")
			}
			domain = args[i]
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 1 {
		fatal("usage: rodney cookies delete <name> [--domain D]")
	}

	name := positional[0]
	_, _, page := withPage()

	req := proto.NetworkDeleteCookies{Name: name}
	if domain != "" {
		req.Domain = domain
	} else {
		info, err := page.Info()
		if err == nil && info != nil {
			req.URL = info.URL
		}
	}

	if err := req.Call(page); err != nil {
		fatal("failed to delete cookie: %v", err)
	}
	fmt.Printf("Deleted cookie: %s\n", name)
}

func cmdCookiesClear(args []string) {
	_, _, page := withPage()
	if err := page.SetCookies(nil); err != nil {
		fatal("failed to clear cookies: %v", err)
	}
	fmt.Println("All cookies cleared")
}

// --- Storage command group ---

func storageType(session bool) string {
	if session {
		return "sessionStorage"
	}
	return "localStorage"
}

func cmdStorage(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rodney storage <subcommand>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  list [--session] [--json]     List storage items")
		fmt.Fprintln(os.Stderr, "  get <key> [--session]         Get a storage value")
		fmt.Fprintln(os.Stderr, "  set <key> <value> [--session] Set a storage value")
		fmt.Fprintln(os.Stderr, "  delete <key> [--session]      Delete a storage key")
		fmt.Fprintln(os.Stderr, "  clear [--session]             Clear all storage")
		os.Exit(1)
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "list":
		cmdStorageList(subargs)
	case "get":
		cmdStorageGet(subargs)
	case "set":
		cmdStorageSet(subargs)
	case "delete":
		cmdStorageDelete(subargs)
	case "clear":
		cmdStorageClear(subargs)
	default:
		fmt.Fprintf(os.Stderr, "unknown storage subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func cmdStorageList(args []string) {
	session := false
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--session":
			session = true
		case "--json":
			jsonOutput = true
		default:
			fatal("unknown flag: %s\nusage: rodney storage list [--session] [--json]", arg)
		}
	}

	_, _, page := withPage()
	st := storageType(session)

	js := fmt.Sprintf(`() => {
		const s = %s;
		const result = {};
		for (let i = 0; i < s.length; i++) {
			const key = s.key(i);
			result[key] = s.getItem(key);
		}
		return result;
	}`, st)

	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to read %s: %v", st, err)
	}

	raw := result.Value.JSON("", "")
	if raw == "{}" || raw == "null" {
		fmt.Fprintf(os.Stderr, "No items in %s\n", st)
		return
	}

	if jsonOutput {
		fmt.Println(result.Value.JSON("", "  "))
	} else {
		var items map[string]string
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			fatal("failed to parse storage data: %v", err)
		}
		fmt.Printf("%-30s %s\n", "KEY", "VALUE")
		fmt.Println(strings.Repeat("-", 80))
		for k, v := range items {
			if len(v) > 50 {
				v = v[:47] + "..."
			}
			fmt.Printf("%-30s %s\n", k, v)
		}
		fmt.Printf("\nTotal items: %d\n", len(items))
	}
}

func cmdStorageGet(args []string) {
	session := false
	var positional []string

	for i := 0; i < len(args); i++ {
		if args[i] == "--session" {
			session = true
		} else {
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 1 {
		fatal("usage: rodney storage get <key> [--session]")
	}

	key := positional[0]
	_, _, page := withPage()
	st := storageType(session)

	js := fmt.Sprintf(`() => %s.getItem(%q)`, st, key)
	result, err := page.Eval(js)
	if err != nil {
		fatal("failed to read %s: %v", st, err)
	}

	raw := result.Value.JSON("", "")
	if raw == "null" {
		fmt.Fprintf(os.Stderr, "key %q not found in %s\n", key, st)
		os.Exit(1)
	}
	fmt.Println(result.Value.Str())
}

func cmdStorageSet(args []string) {
	session := false
	var positional []string

	for i := 0; i < len(args); i++ {
		if args[i] == "--session" {
			session = true
		} else {
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 2 {
		fatal("usage: rodney storage set <key> <value> [--session]")
	}

	key := positional[0]
	value := positional[1]
	_, _, page := withPage()
	st := storageType(session)

	js := fmt.Sprintf(`() => %s.setItem(%q, %q)`, st, key, value)
	_, err := page.Eval(js)
	if err != nil {
		fatal("failed to write %s: %v", st, err)
	}
	fmt.Printf("Set %s: %s=%s\n", st, key, value)
}

func cmdStorageDelete(args []string) {
	session := false
	var positional []string

	for i := 0; i < len(args); i++ {
		if args[i] == "--session" {
			session = true
		} else {
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 1 {
		fatal("usage: rodney storage delete <key> [--session]")
	}

	key := positional[0]
	_, _, page := withPage()
	st := storageType(session)

	js := fmt.Sprintf(`() => %s.removeItem(%q)`, st, key)
	_, err := page.Eval(js)
	if err != nil {
		fatal("failed to delete from %s: %v", st, err)
	}
	fmt.Printf("Deleted %s key: %s\n", st, key)
}

func cmdStorageClear(args []string) {
	session := false
	for _, arg := range args {
		if arg == "--session" {
			session = true
		} else {
			fatal("unknown flag: %s\nusage: rodney storage clear [--session]", arg)
		}
	}

	_, _, page := withPage()
	st := storageType(session)

	js := fmt.Sprintf(`() => %s.clear()`, st)
	_, err := page.Eval(js)
	if err != nil {
		fatal("failed to clear %s: %v", st, err)
	}
	fmt.Printf("Cleared %s\n", st)
}

// --- Auth proxy for environments with authenticated HTTP proxies ---

// detectProxy checks for HTTPS_PROXY/HTTP_PROXY with credentials.
// Returns (proxyServer, username, password, true) if auth proxy is needed.
func detectProxy() (server, user, pass string, needed bool) {
	proxyEnv := os.Getenv("HTTPS_PROXY")
	if proxyEnv == "" {
		proxyEnv = os.Getenv("https_proxy")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("HTTP_PROXY")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("http_proxy")
	}
	if proxyEnv == "" {
		return "", "", "", false
	}
	parsed, err := url.Parse(proxyEnv)
	if err != nil || parsed.User == nil {
		return "", "", "", false
	}
	user = parsed.User.Username()
	pass, _ = parsed.User.Password()
	if user == "" {
		return "", "", "", false
	}
	server = parsed.Hostname() + ":" + parsed.Port()
	return server, user, pass, true
}

// cmdInternalProxy is a hidden subcommand: rodney _proxy <port> <upstream> <authHeader>
// It runs a local auth proxy that forwards to the upstream proxy with credentials.
func cmdInternalProxy(args []string) {
	if len(args) < 3 {
		fatal("usage: rodney _proxy <port> <upstream> <authHeader>")
	}
	port := args[0]
	upstream := args[1]
	authHeader := args[2]

	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fatal("proxy listen failed: %v", err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				proxyConnect(w, r, upstream, authHeader)
			} else {
				proxyHTTP(w, r, upstream, authHeader)
			}
		}),
	}
	server.Serve(listener) // blocks forever
}

func proxyConnect(w http.ResponseWriter, r *http.Request, upstream, authHeader string) {
	upstreamConn, err := net.DialTimeout("tcp", upstream, 30*time.Second)
	if err != nil {
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		r.Host, r.Host, authHeader)
	if _, err := upstreamConn.Write([]byte(connectReq)); err != nil {
		upstreamConn.Close()
		http.Error(w, "upstream write failed", http.StatusBadGateway)
		return
	}

	buf := make([]byte, 4096)
	n, err := upstreamConn.Read(buf)
	if err != nil {
		upstreamConn.Close()
		http.Error(w, "upstream read failed", http.StatusBadGateway)
		return
	}
	response := string(buf[:n])
	if len(response) < 12 || response[9:12] != "200" {
		upstreamConn.Close()
		http.Error(w, "upstream rejected CONNECT", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstreamConn.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		upstreamConn.Close()
		return
	}

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go func() {
		io.Copy(upstreamConn, clientConn)
		upstreamConn.Close()
	}()
	go func() {
		io.Copy(clientConn, upstreamConn)
		clientConn.Close()
	}()
}

func proxyHTTP(w http.ResponseWriter, r *http.Request, upstream, authHeader string) {
	proxyURL, _ := url.Parse("http://" + upstream)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		ProxyConnectHeader: http.Header{
			"Proxy-Authorization": {authHeader},
		},
	}
	r.Header.Set("Proxy-Authorization", authHeader)

	resp, err := transport.RoundTrip(r)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
