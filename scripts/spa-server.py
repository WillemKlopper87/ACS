#!/usr/bin/env python3
"""Static file server for the built console, with the SPA fallback.

`python3 -m http.server` (what scripts/start.sh used before) serves files
and nothing else: the console routes by URL (react-router — /dashboard,
/jobs, /fleet?device=<id>), so reloading or deep-linking any path other
than / asked for a file that doesn't exist and got

    Error code: 404
    Message: File not found.

This is the host-side equivalent of frontend/nginx.conf.template, which
the containerized profile has always had: `try_files $uri $uri/
/index.html`, plus the same browser-hardening headers. Kept to the
standard library on purpose — python3 is on every stock Ubuntu image,
which is why start.sh used http.server in the first place.

Usage: spa-server.py <port> <directory> [api-origin]

api-origin (e.g. http://13.246.35.62:8080) is the API base URL the
bundle was built with; it goes into connect-src/frame-src of the CSP,
exactly as frontend/Dockerfile substitutes it into the nginx template.
Omit it and no CSP is sent, rather than one that would silently block
every API call.
"""

import os
import sys
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit


def csp_for(api_origin):
    """Mirror of the Content-Security-Policy in nginx.conf.template."""
    if not api_origin:
        return None
    api = api_origin.rstrip("/")
    if api.startswith("https://"):
        ws = "wss://" + api[len("https://"):]
    elif api.startswith("http://"):
        ws = "ws://" + api[len("http://"):]
    else:
        # Same refusal as the Dockerfile: a scheme-less origin produces a
        # CSP that matches nothing, and the app then fails with no error
        # the user can see.
        print(f"spa-server: ignoring api-origin {api!r} — not an absolute http(s) URL",
              file=sys.stderr)
        return None
    return (
        "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "
        "img-src 'self' data:; font-src 'self' data:; "
        f"connect-src 'self' {api} {ws}; frame-src 'self' {api}; "
        "frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
    )


class SPAHandler(SimpleHTTPRequestHandler):
    # Keep-alive: the console pulls a dozen-plus hashed assets per load,
    # and http.server's HTTP/1.0 default tears down the connection after
    # each one. Safe here because every response this handler produces
    # (files, directory listings, errors) carries a Content-Length.
    protocol_version = "HTTP/1.1"
    csp = None

    def _rewrite_to_index(self):
        """try_files $uri $uri/ /index.html, for navigations only.

        A missing *subresource* keeps its honest 404. That is not a
        nicety: Vite names build output by content hash, so after a
        redeploy an already-open tab still asks for the previous hash,
        and answering that with index.html (what a bare try_files does)
        hands the browser HTML where it expects an ES module — the
        "Failed to fetch dynamically imported module" failure the app
        cannot recover from. A 404 is what lets it recognise a stale
        bundle and reload itself. nginx.conf.template carves /assets/ out
        of its fallback for exactly this reason; this mirrors it, and
        additionally requires a navigation (which is what asks for
        text/html) before falling back at all.
        """
        route = urlsplit(self.path).path
        if route.startswith("/assets/"):
            return
        path = self.translate_path(self.path)
        if os.path.isdir(path):
            if os.path.exists(os.path.join(path, "index.html")):
                return
        elif os.path.exists(path):
            return
        accept = self.headers.get("Accept", "")
        navigation = ("text/html" in accept
                      or self.headers.get("Sec-Fetch-Dest", "") == "document")
        if not navigation:
            return
        self.path = "/index.html"

    def do_GET(self):
        self._rewrite_to_index()
        super().do_GET()

    def do_HEAD(self):
        self._rewrite_to_index()
        super().do_HEAD()

    def end_headers(self):
        # Browser-side hardening, identical to nginx.conf.template. HSTS
        # is deliberately absent here too — this listener is plain HTTP.
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
        if self.csp:
            self.send_header("Content-Security-Policy", self.csp)
        # index.html must never be cached: it names the content-hashed
        # bundle, so a cached copy sends the browser after a JS file that
        # start.sh's rebuild has already deleted. The hashed assets
        # themselves can be cached hard, precisely because their names
        # change when their contents do.
        route = urlsplit(self.path).path
        if route.startswith("/assets/"):
            self.send_header("Cache-Control", "public, max-age=31536000, immutable")
        else:
            self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def log_message(self, fmt, *args):
        # Timestamped, line-buffered into the nohup log start.sh redirects.
        super().log_message(fmt, *args)
        sys.stderr.flush()


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 5173
    directory = sys.argv[2] if len(sys.argv) > 2 else "."
    api_origin = sys.argv[3] if len(sys.argv) > 3 else os.environ.get("ACS_API_ORIGIN", "")

    SPAHandler.csp = csp_for(api_origin)
    handler = partial(SPAHandler, directory=directory)
    # Threading, unlike http.server's default: one slow or half-open
    # client connection would otherwise stall the whole console.
    httpd = ThreadingHTTPServer(("0.0.0.0", port), handler)
    httpd.daemon_threads = True
    print(f"spa-server: serving {directory} on 0.0.0.0:{port} "
          f"(SPA fallback to index.html{'; CSP set' if SPAHandler.csp else '; no CSP'})",
          flush=True)
    httpd.serve_forever()


if __name__ == "__main__":
    main()
