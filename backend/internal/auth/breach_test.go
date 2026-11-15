package auth

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBreachChecker(t *testing.T) {
	digestOf := func(p string) string {
		s := sha1.Sum([]byte(p))
		return strings.ToUpper(hex.EncodeToString(s[:]))
	}
	const breached, padded, clean = "correct horse battery", "padded-row-password", "nobody-has-used-this-one"
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+" padding="+r.Header.Get("Add-Padding"))
		if strings.HasSuffix(r.URL.Path, "/"+digestOf("service-is-down")[:5]) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, "0000000000000000000000000000000000A:3\r\n")
		fmt.Fprintf(w, "%s:1337\r\n", digestOf(breached)[5:])
		fmt.Fprintf(w, "%s:0\r\n", digestOf(padded)[5:]) // a padding row
	}))
	defer srv.Close()

	c, err := NewBreachChecker(srv.URL + "/range")
	if err != nil || c == nil {
		t.Fatalf("checker: %v", err)
	}
	ctx := context.Background()
	if err := c.Check(ctx, breached); !errors.Is(err, ErrPasswordBreached) {
		t.Fatalf("a breached password = %v", err)
	}
	if err := c.Check(ctx, clean); err != nil {
		t.Fatalf("a password the service does not list = %v", err)
	}
	if err := c.Check(ctx, padded); err != nil {
		t.Fatalf("a padding row counted as a breach: %v", err)
	}
	// Only the five-character prefix leaves the process.
	for _, a := range asked {
		path := strings.TrimPrefix(strings.Fields(a)[0], "/range/")
		if len(path) != 5 || !strings.HasSuffix(a, "padding=true") {
			t.Fatalf("request %q: want a 5-character prefix and a padded answer", a)
		}
	}
	for _, p := range []string{breached, clean} {
		for _, a := range asked {
			if strings.Contains(a, digestOf(p)[5:]) || strings.Contains(a, p) {
				t.Fatalf("the request %q carries more than the prefix", a)
			}
		}
	}
	// A lookup that fails is an error, not a verdict.
	if err := c.Check(ctx, "service-is-down"); err == nil || errors.Is(err, ErrPasswordBreached) {
		t.Fatalf("service down = %v, want a plain error", err)
	}
	// Off unless configured.
	if off, err := NewBreachChecker(""); off != nil || err != nil {
		t.Fatalf("unset: %v %v", off, err)
	}
	var none *BreachChecker
	if err := none.Check(ctx, breached); err != nil {
		t.Fatalf("a nil checker = %v", err)
	}
	if _, err := NewBreachChecker("not a url"); err == nil {
		t.Fatal("a malformed URL was accepted")
	}
}
