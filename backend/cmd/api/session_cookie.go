package main

import (
	"net/http"
	"strings"
	"time"

	"acs/internal/config"
)

// The operator console's session travels in a cookie the page's scripts
// cannot read (docs/SECURITY-ASSESSMENT-2026-11-14.md, O6). Before this
// the console kept the session token in browser storage, where any script
// running on the console's origin could copy it and use it from anywhere
// until it expired.
//
// The cookie is:
//
//   - HttpOnly: no script can read it, the console's own included;
//   - SameSite=Strict: the browser sends it only with requests made from
//     the console's own site;
//   - Secure whenever the request arrived over HTTPS, and always in the
//     production profile;
//   - scoped to /api/, so it is not sent for anything else this host
//     might serve.
//
// A cookie is sent by the browser on its own, so a request that relies
// on one must also show it was made by the console and not by another
// page (cross-site request forgery):
//
//   - a state-changing request needs the X-ACS-CSRF header. A page on
//     another origin cannot add a header without the browser first asking
//     this API's permission (the CORS preflight), which only the console's
//     origin is given;
//   - if the browser says which origin the request comes from, it must be
//     the console's (withSessionOriginCheck);
//   - the two routes a browser opens without control over headers (the
//     console WebSocket, the device web-GUI frame) never accept the
//     cookie: they keep requiring a 60-second ticket.
//
// The Authorization header still works exactly as before, for scripts and
// for the BSS adapter. A request with a bearer token is not a cookie
// request and none of the above applies to it.
const (
	sessionCookieName = "acs_session"
	sessionCookiePath = "/api/"
	csrfHeader        = "X-ACS-CSRF"
)

// sessionCookiesAllowed decides whether this API issues session cookies
// at all. It does not when:
//
//   - the CORS origin is "*": a credentialed browser request needs one
//     exact origin, so the cookie could never be used;
//   - ACS_WEBGUI_ALLOW_SAME_ORIGIN=true (lab only): device web pages then
//     run on this API's own origin, where the browser would attach the
//     cookie to anything their scripts request.
//
// The console then keeps its session in memory for the life of the page.
func sessionCookiesAllowed(corsOrigin string, webGUISameOrigin bool) bool {
	return corsOrigin != "*" && !webGUISameOrigin
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// setSessionCookie stores the session in the browser. A no-op when cookies
// are off.
func (h *handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	if !h.sessionCookies {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: sessionCookiePath, Expires: expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		// X-Forwarded-Proto can only make the cookie stricter, so it is
		// read without checking who sent it.
		Secure: h.deploymentProfile == config.ProfileProduction || requestIsHTTPS(r),
	})
}

// clearSessionCookie removes the cookie from the browser. Deleting needs
// only the same name and path.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: sessionCookiePath, MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func sessionCookieToken(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// reliesOnSessionCookie reports whether a request has no bearer token and
// so would be authenticated by its cookie.
func reliesOnSessionCookie(r *http.Request) bool {
	if _, _, ok := bearerToken(r); ok {
		return false
	}
	_, ok := sessionCookieToken(r)
	return ok
}

// withSessionOriginCheck refuses a state-changing request that relies on
// the session cookie and that the browser says comes from another origin
// than the console's. It is the second of the two forgery checks; the
// required header (withJWTAuth) is the first. A request with no Origin
// header is left to that check: browsers send Origin on every
// cross-origin and every state-changing request, so its absence means a
// non-browser client, which has no cookie to be tricked into sending.
func withSessionOriginCheck(consoleOrigin string, next http.Handler) http.Handler {
	consoleOrigin = strings.TrimRight(consoleOrigin, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(r.Header.Get("Origin"), "/")
		if origin != "" && !isSafeMethod(r.Method) && reliesOnSessionCookie(r) && !strings.EqualFold(origin, consoleOrigin) {
			http.Error(w, "this request did not come from the console", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// getSession is GET /api/v1/auth/session: who the caller is signed in as.
// The console asks this when it loads, because it cannot read its own
// session cookie. "via" says how the caller authenticated; a console
// that gets "cookie" needs to keep no token of its own.
func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := operatorClaims(r.Context())
	if !ok {
		// Operator authentication is switched off (no signing secret).
		writeJSON(w, http.StatusOK, map[string]any{"auth_required": false})
		return
	}
	via := "bearer"
	if reliesOnSessionCookie(r) {
		via = "cookie"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_required": true, "username": claims.Subject, "role": claims.Role,
		"expires_at": claims.ExpiresAt.UTC().Format(time.RFC3339), "via": via,
	})
}
