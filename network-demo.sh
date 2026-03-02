#!/bin/bash
# Network Tab Demo - demonstrates network inspection features

set -e

echo "=== Rodney Network Inspection Demo ==="
echo

# Start the browser
echo "1. Starting Chrome..."
./rodney start
sleep 1

# Navigate to a website
echo "2. Navigating to example.com..."
./rodney open https://example.com
./rodney waitload
sleep 2

# List network requests
echo "3. Listing network requests..."
./rodney network list
echo

# Show JSON output
echo "4. Showing JSON output..."
./rodney network list --json | head -n 20
echo "..."
echo

# Save network log
echo "5. Saving network log to network.json..."
./rodney network save network.json
echo

# Navigate to another page
echo "6. Navigating to another page..."
./rodney open https://httpbin.org/json
./rodney waitload
sleep 2

# Filter network requests
echo "7. Filtering requests by URL pattern..."
./rodney network list --filter "httpbin"
echo

# Filter by body content
echo "8. Filtering by response body content..."
./rodney network filter "slideshow" || echo "No matches (expected for httpbin)"
echo

# Clear network log
echo "9. Clearing network log..."
./rodney network clear
echo

# Verify log is cleared
echo "10. Verifying log is cleared..."
./rodney network list || echo "Network log is empty (expected)"
echo

# Stop the browser
echo "11. Stopping Chrome..."
./rodney stop

echo
echo "=== Demo complete! ==="
echo
echo "Network commands available:"
echo "  rodney network list [--json] [--filter <pattern>]"
echo "  rodney network filter <pattern>"
echo "  rodney network clear"
echo "  rodney network save <file.json>"
