# Rodney: Chrome automation from the command line

[![PyPI](https://img.shields.io/pypi/v/rodney.svg)](https://pypi.org/project/rodney/)
[![Changelog](https://img.shields.io/github/v/release/simonw/rodney?include_prereleases&label=changelog)](https://github.com/simonw/rodney/releases)
[![Tests](https://github.com/simonw/rodney/actions/workflows/test.yml/badge.svg)](https://github.com/simonw/rodney/actions/workflows/test.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](https://github.com/simonw/rodney/blob/main/LICENSE)

A Go CLI tool that drives a persistent headless Chrome instance using the [rod](https://github.com/go-rod/rod) browser automation library. Each command connects to the same long-running Chrome process, making it easy to script multi-step browser interactions from shell scripts or interactive use.

## Architecture

```
rodney start     →  launches Chrome (headless, persists after CLI exits)
                     saves WebSocket debug URL to ~/.rodney/state.json

rodney open URL  →  connects to running Chrome via WebSocket
                     navigates the active tab, disconnects

rodney js EXPR   →  connects, evaluates JS, prints result, disconnects

rodney stop      →  connects and shuts down Chrome, cleans up state
```

Each CLI invocation is a short-lived process. Chrome runs independently and tabs persist between commands.

## Building

```bash
go build -o rodney .
```

Requires:
- Go 1.21+
- Google Chrome or Chromium installed (or set `ROD_CHROME_BIN=/path/to/chrome`)

## Usage

### Start/stop the browser

```bash
rodney start          # Launch headless Chrome
rodney status         # Show browser info and active page
rodney stop           # Shut down Chrome
```

### Navigate

```bash
rodney open https://example.com    # Navigate to URL
rodney open example.com            # http:// prefix added automatically
rodney back                        # Go back
rodney forward                     # Go forward
rodney reload                      # Reload page
rodney reload --hard               # Reload bypassing cache
```

### Extract information

```bash
rodney url                    # Print current URL
rodney title                  # Print page title
rodney text "h1"              # Print text content of element
rodney html "div.content"     # Print outer HTML of element
rodney html                   # Print full page HTML
rodney attr "a#link" href     # Print attribute value
rodney pdf output.pdf         # Save page as PDF
```

### Run JavaScript

```bash
rodney js document.title                        # Evaluate expression
rodney js "1 + 2"                               # Math
rodney js 'document.querySelector("h1").textContent'  # DOM queries
rodney js '[1,2,3].map(x => x * 2)'            # Returns pretty-printed JSON
rodney js 'document.querySelectorAll("a").length'     # Count elements
```

The expression is automatically wrapped in `() => { return (expr); }`.

### Interact with elements

```bash
rodney click "button#submit"       # Click element
rodney input "#search" "query"     # Type into input field
rodney clear "#search"             # Clear input field
rodney file "#upload" photo.jpg    # Set file on a file input
rodney file "#upload" -            # Set file from stdin
rodney download "a#link" out.pdf   # Download href/src target
rodney download "img.hero" -       # Download to stdout
rodney select "#dropdown" "value"  # Select dropdown by value
rodney submit "form#login"         # Submit a form
rodney hover ".menu-item"          # Hover over element
rodney focus "#email"              # Focus element
```

### Scroll

```bash
rodney scroll ".section"           # Scroll element into view
rodney scroll down                 # Scroll down 300px (default)
rodney scroll down 500             # Scroll down 500px
rodney scroll up 200               # Scroll up 200px
rodney scroll left                 # Scroll left
rodney scroll right                # Scroll right
rodney scroll top                  # Scroll to page top
rodney scroll bottom               # Scroll to page bottom
```

### Keyboard

```bash
rodney key Enter                   # Press Enter
rodney key Tab                     # Press Tab
rodney key Escape                  # Press Escape
rodney key ctrl+a                  # Select all
rodney key shift+Tab               # Shift+Tab
rodney key "hello world"           # Type text character by character
```

### Wait for conditions

```bash
rodney wait ".loaded"       # Wait for element to appear and be visible
rodney waitload             # Wait for page load event
rodney waitstable           # Wait for DOM to stop changing
rodney waitidle             # Wait for network to be idle
rodney waitfor 'document.querySelector(".done")'  # Wait until JS expression is truthy
rodney waitfor --timeout 10 'window.ready'        # Custom timeout (seconds)
rodney sleep 2.5            # Sleep for N seconds
```

### Screenshots

```bash
rodney screenshot                      # Save as screenshot.png
rodney screenshot page.png             # Save to specific file
rodney screenshot -w 1280 -h 720 out.png  # Set viewport size
rodney screenshot-el ".chart" chart.png   # Screenshot specific element
```

### Manage tabs

```bash
rodney pages                    # List all tabs (* marks active)
rodney newpage https://...      # Open URL in new tab
rodney page 1                   # Switch to tab by index
rodney closepage 1              # Close tab by index
rodney closepage                # Close active tab
```

### Query elements

```bash
rodney exists ".loading"    # Exit 0 if exists, exit 1 if not
rodney count "li.item"      # Print number of matching elements
rodney visible "#modal"     # Exit 0 if visible, exit 1 if not
```

### Accessibility testing

```bash
rodney ax-tree                           # Dump full accessibility tree
rodney ax-tree --depth 3                 # Limit tree depth
rodney ax-tree --json                    # Output as JSON

rodney ax-find --role button             # Find all buttons
rodney ax-find --name "Submit"           # Find by accessible name
rodney ax-find --role link --name "Home" # Combine filters
rodney ax-find --role button --json      # Output as JSON

rodney ax-node "#submit-btn"             # Inspect element's a11y properties
rodney ax-node "h1" --json               # Output as JSON
```

These commands use Chrome's [Accessibility CDP domain](https://chromedevtools.github.io/devtools-protocol/tot/Accessibility/) to expose what assistive technologies see. `ax-tree` uses `getFullAXTree`, `ax-find` uses `queryAXTree`, and `ax-node` uses `getPartialAXTree`.

```bash
# CI check: verify all buttons have accessible names
rodney ax-find --role button --json | python3 -c "
import json, sys
buttons = json.load(sys.stdin)
unnamed = [b for b in buttons if not b.get('name', {}).get('value')]
if unnamed:
    print(f'FAIL: {len(unnamed)} button(s) missing accessible name')
    sys.exit(1)
print(f'PASS: all {len(buttons)} buttons have accessible names')
"
```

### Console

```bash
rodney console                     # Read captured console messages
rodney console --errors            # Show only errors
rodney console --json              # Output as JSON
rodney console --follow            # Stream console output (Ctrl+C to stop)
rodney console --follow --errors   # Stream only errors
rodney console --clear             # Clear captured messages
```

### Cookies

```bash
rodney cookies list                # List all cookies for current page
rodney cookies list --json         # Output as JSON
rodney cookies get session_id      # Get a cookie value by name
rodney cookies set name value      # Set a cookie
rodney cookies set name value --domain .example.com --secure --httponly
rodney cookies delete session_id   # Delete a cookie
rodney cookies clear               # Clear all cookies
```

### Storage

```bash
rodney storage list                # List localStorage items
rodney storage list --json         # Output as JSON
rodney storage list --session      # List sessionStorage instead
rodney storage get theme           # Get a localStorage value
rodney storage set theme dark      # Set a value
rodney storage delete theme        # Delete a key
rodney storage clear               # Clear all localStorage
rodney storage clear --session     # Clear sessionStorage
```

### Network inspection

```bash
rodney network list                      # List all network requests
rodney network list --json               # Output as JSON
rodney network list --filter "api"       # Filter by URL pattern
rodney network filter "operationName"    # Filter by response body content (e.g., GraphQL)
rodney network clear                     # Clear network log
rodney network save network.json         # Save network log to file
```

Network commands use the browser's [Performance API](https://developer.mozilla.org/en-US/docs/Web/API/Performance_API) to capture network activity. The log includes URLs, methods, resource types, transfer sizes, and timing information.

The `filter` subcommand searches through response body content, which is particularly useful for debugging GraphQL queries by searching for operation names, field names, or response data.

### Performance profiling

```bash
# Quick metrics
rodney perf metrics                # Runtime performance metrics
rodney perf vitals                 # Core Web Vitals (LCP, CLS, TTFB)
rodney perf timing                 # Navigation timing breakdown
rodney perf metrics --json         # Any subcommand supports --json

# CPU profiling (outputs .cpuprofile for speedscope)
# Tip: set up the action BEFORE profiling
rodney js "setInterval(() => scrollBy(0, 100), 50)"
rodney perf profile 3 scroll.cpuprofile
npx speedscope scroll.cpuprofile        # Visualize

# Full browser trace (outputs trace JSON for Perfetto)
rodney perf trace 5 trace.json
# Open https://ui.perfetto.dev and load trace.json
```

### React component profiling

```bash
# 1. Install the DevTools hook BEFORE navigating
rodney react hook

# 2. Navigate to your React app
rodney open http://localhost:3000

# 3. Interact with the app
rodney click "#tab-profile"
rodney waitstable

# 4. Inspect what rendered
rodney react tree                        # Component hierarchy
rodney react renders                     # Per-component render timing
rodney react flamegraph renders.json     # Export as flamegraph
npx speedscope renders.json              # Visualize in browser
```

The React hook intercepts React's internal `onCommitFiberRoot` callback to capture render data. It works with React 16+, in both development and production builds. Component timing (`actualDuration`) is only available in profiling-enabled builds (dev builds, or `react-scripts build --profile`).

### Shell scripting examples

```bash
# Wait for page to load and extract data
rodney start
rodney open https://example.com
rodney waitstable
title=$(rodney title)
echo "Page: $title"

# Conditional logic based on element presence
if rodney exists ".error-message"; then
    rodney text ".error-message"
fi

# Loop through pages
for url in page1 page2 page3; do
    rodney open "https://example.com/$url"
    rodney waitstable
    rodney screenshot "${url}.png"
done

rodney stop
```

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `ROD_CHROME_BIN` | `/usr/bin/google-chrome` | Path to Chrome/Chromium binary |
| `ROD_TIMEOUT` | `30` | Default timeout in seconds for element queries |
| `HTTPS_PROXY` / `HTTP_PROXY` | (none) | Authenticated proxy auto-detected on start |

State is stored in `~/.rodney/state.json`. Chrome user data is stored in `~/.rodney/chrome-data/`.

## Proxy support

In environments with authenticated HTTP proxies (e.g., `HTTPS_PROXY=http://user:pass@host:port`), `rodney start` automatically:

1. Detects the proxy credentials from environment variables
2. Launches a local forwarding proxy that injects `Proxy-Authorization` headers into CONNECT requests
3. Configures Chrome to use the local proxy

This is necessary because Chrome cannot natively authenticate to proxies during HTTPS tunnel (CONNECT) establishment. The local proxy runs as a background process and is automatically cleaned up by `rodney stop`.

See [claude-code-chrome-proxy.md](claude-code-chrome-proxy.md) for detailed technical notes.

## How it works

The tool uses the [rod](https://github.com/go-rod/rod) Go library which communicates with Chrome via the DevTools Protocol (CDP) over WebSocket. Key implementation details:

- **`start`** uses rod's `launcher` package to start Chrome with `Leakless(false)` so Chrome survives after the CLI exits
- **Proxy auth** handled via a local forwarding proxy that bridges Chrome to authenticated upstream proxies
- **State persistence** via a JSON file containing the WebSocket debug URL and Chrome PID
- **Each command** creates a new rod `Browser` connection to the same Chrome instance, executes the operation, and disconnects
- **Element queries** use rod's built-in auto-wait with a configurable timeout (default 30s)
- **JS evaluation** wraps user expressions in arrow functions as required by rod's `Eval`
- **Accessibility commands** call CDP's Accessibility domain directly via rod's `proto` package (`getFullAXTree`, `queryAXTree`, `getPartialAXTree`)

## Dependencies

- [github.com/go-rod/rod](https://github.com/go-rod/rod) v0.116.2 - Chrome DevTools Protocol automation

## Commands reference

| Command | Arguments | Description |
|---|---|---|
| `start` | | Launch headless Chrome |
| `stop` | | Shut down Chrome |
| `status` | | Show browser status |
| `open` | `<url>` | Navigate to URL |
| `back` | | Go back in history |
| `forward` | | Go forward in history |
| `reload` | `[--hard]` | Reload current page |
| `url` | | Print current URL |
| `title` | | Print page title |
| `html` | `[selector]` | Print HTML (page or element) |
| `text` | `<selector>` | Print element text content |
| `attr` | `<selector> <name>` | Print attribute value |
| `pdf` | `[file]` | Save page as PDF |
| `js` | `<expression>` | Evaluate JavaScript |
| `click` | `<selector>` | Click element |
| `input` | `<selector> <text>` | Type into input |
| `clear` | `<selector>` | Clear input |
| `file` | `<selector> <path\|->` | Set file on file input |
| `download` | `<selector> [file\|-]` | Download href/src target |
| `select` | `<selector> <value>` | Select dropdown value |
| `submit` | `<selector>` | Submit form |
| `hover` | `<selector>` | Hover over element |
| `focus` | `<selector>` | Focus element |
| `scroll` | `<selector>` | Scroll element into view |
| `scroll` | `up\|down\|left\|right [px]` | Scroll direction |
| `scroll` | `top\|bottom` | Scroll to page edge |
| `key` | `<key\|combo\|text>` | Press key, combo, or type text |
| `wait` | `<selector>` | Wait for element to appear |
| `waitload` | | Wait for page load |
| `waitstable` | | Wait for DOM stability |
| `waitidle` | | Wait for network idle |
| `waitfor` | `[--timeout N] <js-expr>` | Wait until JS expression is truthy |
| `sleep` | `<seconds>` | Sleep N seconds |
| `screenshot` | `[-w N] [-h N] [file]` | Page screenshot |
| `screenshot-el` | `<selector> [file]` | Element screenshot |
| `pages` | | List tabs |
| `page` | `<index>` | Switch tab |
| `newpage` | `[url]` | Open new tab |
| `closepage` | `[index]` | Close tab |
| `exists` | `<selector>` | Check element exists (exit code) |
| `count` | `<selector>` | Count matching elements |
| `visible` | `<selector>` | Check element visible (exit code) |
| `ax-tree` | `[--depth N] [--json]` | Dump accessibility tree |
| `ax-find` | `[--name N] [--role R] [--json]` | Find accessible nodes |
| `ax-node` | `<selector> [--json]` | Show element accessibility info |
| `console` | `[--errors] [--json]` | Read console messages |
| `console` | `--follow [--errors]` | Stream console output |
| `console` | `--clear` | Clear console messages |
| `cookies list` | `[--json]` | List cookies |
| `cookies get` | `<name>` | Get cookie value |
| `cookies set` | `<name> <value> [flags]` | Set a cookie |
| `cookies delete` | `<name> [--domain D]` | Delete a cookie |
| `cookies clear` | | Clear all cookies |
| `storage list` | `[--session] [--json]` | List storage items |
| `storage get` | `<key> [--session]` | Get storage value |
| `storage set` | `<key> <value> [--session]` | Set storage value |
| `storage delete` | `<key> [--session]` | Delete storage key |
| `storage clear` | `[--session]` | Clear all storage |
| `network list` | `[--json] [--filter <pattern>]` | List network requests |
| `network filter` | `<pattern>` | Filter requests by body content |
| `network clear` | | Clear network request log |
| `network save` | `<file.json>` | Save network log to file |
| `perf metrics` | `[--json]` | Runtime performance metrics |
| `perf vitals` | `[--json]` | Core Web Vitals |
| `perf timing` | `[--json]` | Navigation timing breakdown |
| `perf profile` | `<seconds> [file]` | Record CPU profile |
| `perf trace` | `<seconds> [file]` | Record browser trace |
| `react hook` | | Install React DevTools hook |
| `react tree` | `[--json]` | Show component tree |
| `react renders` | `[--json]` | Show render commits with timing |
| `react flamegraph` | `<file>` | Export as speedscope flamegraph |
