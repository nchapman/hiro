Fetches content from a URL via HTTP GET.

- URL must start with http:// or https://
- Returns HTTP status, content type, and response body
- Response body capped at 64KB, 30s timeout
- Requests to private, loopback, and link-local addresses are blocked (SSRF protection)
