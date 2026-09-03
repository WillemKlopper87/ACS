package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func encodeBody(t *testing.T, encoding string, payload []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	var w io.WriteCloser
	var err error
	switch encoding {
	case "gzip":
		w = gzip.NewWriter(&b)
	case "deflate":
		w = zlib.NewWriter(&b)
	case "deflate-raw":
		w, err = flate.NewWriter(&b, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown encoding %q", encoding)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadCWMPBodyCompatibilityEncodings(t *testing.T) {
	payload := []byte(`<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/"><soap-env:Body/></soap-env:Envelope>`)
	cases := []struct {
		name, header, encoder string
	}{
		{"identity", "", ""},
		{"gzip", "gzip", "gzip"},
		{"x-gzip", "x-gzip", "gzip"},
		{"deflate-zlib", "deflate", "deflate"},
		{"deflate-raw", "deflate", "deflate-raw"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := payload
			if tc.encoder != "" {
				body = encodeBody(t, tc.encoder, payload)
			}
			r := httptest.NewRequest(http.MethodPost, "/cwmp", bytes.NewReader(body))
			if tc.header != "" {
				r.Header.Set("Content-Encoding", tc.header)
			}
			w := httptest.NewRecorder()
			got, err := readCWMPBody(w, r)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("decoded body mismatch: %q", got)
			}
		})
	}
}

func TestReadCWMPBodyUnsupportedEncoding(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/cwmp", bytes.NewReader([]byte("soap")))
	r.Header.Set("Content-Encoding", "br")
	w := httptest.NewRecorder()
	if _, err := readCWMPBody(w, r); err == nil {
		t.Fatal("expected unsupported encoding error")
	}
}

func TestRespondEmptyDefaults204WithLegacy200Override(t *testing.T) {
	h := &handler{}

	t.Setenv("ACS_CWMP_EMPTY_RESPONSE_STATUS", "")
	w := httptest.NewRecorder()
	h.respondEmpty(w)
	if w.Code != http.StatusNoContent {
		t.Fatalf("default empty response = %d, want 204", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 must have an empty body, got %q", w.Body.String())
	}

	t.Setenv("ACS_CWMP_EMPTY_RESPONSE_STATUS", "200")
	w = httptest.NewRecorder()
	h.respondEmpty(w)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy empty response = %d, want 200", w.Code)
	}
}
