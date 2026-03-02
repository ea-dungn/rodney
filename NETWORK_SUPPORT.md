# Network Tab Support

This document describes the network inspection functionality added to Rodney.

## Overview

Network tab support allows you to inspect HTTP requests made by web pages. This is useful for:
- Debugging API calls
- Analyzing page performance
- Monitoring resource loading
- Tracking XHR/fetch requests

## Implementation

The network commands use the browser's built-in [Performance API](https://developer.mozilla.org/en-US/docs/Web/API/Performance_API) to capture network activity. This API is always available and doesn't require explicit enabling.

### Data Captured

For each network request, the following information is captured:
- **URL**: The full request URL
- **Method**: HTTP method (GET, POST, etc.)
- **Type**: Resource type (fetch, xmlhttprequest, script, stylesheet, etc.)
- **Start Time**: When the request started (milliseconds since page load)
- **Duration**: How long the request took (milliseconds)
- **Transfer Size**: Number of bytes transferred (including headers)
- **Encoded Body Size**: Size of the response body (compressed)
- **Decoded Body Size**: Size of the response body (uncompressed)
- **Response Status**: HTTP status code (if available)
- **Protocol**: HTTP protocol used (h2, http/1.1, etc.)

## Commands

### `rodney network list`

List all captured network requests.

```bash
# Basic usage - shows table format
rodney network list

# JSON output
rodney network list --json

# Filter by URL pattern
rodney network list --filter "api"
rodney network list --filter ".js"
```

**Output (table format):**
```
METHOD     URL                                                          TYPE         SIZE       DURATION
------------------------------------------------------------------------------------------------------------------------
GET        https://example.com/                                         navigation   1.2KB      150ms
GET        https://example.com/style.css                                stylesheet   5.4KB      45ms
GET        https://example.com/script.js                                script       12.3KB     78ms

Total requests: 3
```

**Output (JSON format):**
```json
[
  {
    "url": "https://example.com/",
    "method": "GET",
    "type": "navigation",
    "startTime": 0,
    "duration": 150.5,
    "transferSize": 1234,
    "responseStatus": 200
  }
]
```

### `rodney network filter`

Filter network requests by response body content.

```bash
# Search for GraphQL operation names
rodney network filter "getUserProfile"

# Search for specific data in responses
rodney network filter "error"
rodney network filter "token"
```

This command searches through response bodies of all resources and displays matches with context. This is particularly useful for:
- **GraphQL debugging**: Find requests containing specific operation names or response data
- **API debugging**: Search for specific fields or error messages in JSON responses
- **Content verification**: Check if specific data appears in any loaded resources

**Output:**
```
Found 2 request(s) matching pattern: getUserProfile

[1] https://api.example.com/graphql
    Content-Type: application/json
    Match: ...{"data":{"getUserProfile":{"id":"123","name":"John"}}...

[2] https://api.example.com/graphql
    Content-Type: application/json
    Match: ...operationName":"getUserProfile","variables":{}...
```

**Note:** This command re-fetches resources to search their content, so:
- CORS restrictions apply (cross-origin resources may not be searchable)
- Only works for GET requests and publicly accessible resources
- POST request bodies cannot be searched retroactively
- Cached responses are used when available

For more comprehensive request/response body inspection (including POST bodies and real-time capture), consider using Chrome DevTools or a proxy tool.

### `rodney network clear`

Clear the network request log.

```bash
rodney network clear
```

This clears the Performance API buffer, removing all captured network requests.

### `rodney network save`

Save the network log to a JSON file.

```bash
rodney network save network.json
```

The output format is based on the HAR (HTTP Archive) specification:

```json
{
  "log": {
    "version": "1.2",
    "creator": {
      "name": "rodney",
      "version": "dev"
    },
    "entries": [
      {
        "url": "https://example.com/",
        "method": "GET",
        "type": "navigation",
        "startTime": 0,
        "duration": 150.5,
        "transferSize": 1234,
        "encodedBodySize": 1000,
        "decodedBodySize": 1000,
        "responseStatus": 200,
        "protocol": "h2"
      }
    ]
  }
}
```

## Usage Examples

### Debug API calls
```bash
rodney start
rodney open https://api.example.com
rodney waitload
rodney network list --filter "/api/"
```

### Performance analysis
```bash
rodney open https://example.com
rodney waitload
rodney network save performance.json
# Analyze the JSON file to identify slow requests
```

### CI/CD integration
```bash
# Capture network activity during tests
rodney open https://app.example.com
rodney waitload
rodney network save network.json

# Check for failed requests
rodney network list --json | jq 'map(select(.responseStatus >= 400))'
```

### GraphQL debugging
```bash
# Navigate to a GraphQL-powered app
rodney start
rodney open https://app.example.com
rodney waitload

# Find all requests with a specific operation
rodney network filter "getUserProfile"

# Search for errors in GraphQL responses
rodney network filter '"errors"'

# Find requests containing specific field data
rodney network filter "email"
```

**GraphQL-specific tips:**
- Search for operation names to find specific queries/mutations
- Search for `"errors"` to find failed GraphQL requests
- Search for field names to see which operations return specific data
- The filter searches response bodies, so you'll see the returned data

## Limitations

1. **Performance API limitations**: The Performance API has a buffer size limit (typically 150 entries). Use `network-clear` to reset the buffer if needed.

2. **Method detection**: The Performance API doesn't always expose the HTTP method. For most requests, "GET" is assumed. For more accurate method tracking, consider using Chrome DevTools Protocol's Network domain directly.

3. **Request/Response bodies**: The Performance API doesn't capture request/response bodies. Only metadata and timing information is available.

4. **Cross-origin resources**: Information may be limited for cross-origin resources depending on CORS headers.

## Technical Details

- **API Used**: [PerformanceResourceTiming](https://developer.mozilla.org/en-US/docs/Web/API/PerformanceResourceTiming)
- **Data Source**: `performance.getEntries()` filtered by entry type
- **Format**: Compatible with HAR (HTTP Archive) specification
- **No background process**: Unlike other CDP-based approaches, this uses JavaScript evaluation and doesn't require a persistent connection

## Future Enhancements

Lower priority items mentioned by the user:
- Memory inspection
- React component rendering inspection

Potential improvements:
- Real-time request capturing using CDP's Network domain
- Request/response body inspection
- WebSocket tracking
- More detailed timing information (DNS, TCP, SSL)
- HAR export with full request/response data
