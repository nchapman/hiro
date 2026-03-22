Fetch content from a URL via HTTP GET.

Use this to read web pages, API responses, documentation, or any HTTP resource. Returns the HTTP status, content type, and response body.

## Guidelines

- Response bodies are limited to 64KB.
- Timeout is 30 seconds.
- Only HTTP and HTTPS URLs are supported.
- For APIs that return JSON, the raw JSON will be returned.
- Non-2xx responses are not errors — the full response (status, headers, body) is always returned. Check the HTTP status line to detect failures.
