Fetches content from a URL via HTTP GET.

- The URL must start with http:// or https://
- Returns the HTTP status, content type, and response body
- Response body is limited to 64KB, 30s timeout
- Requests to private, loopback, and link-local addresses are blocked (SSRF protection)