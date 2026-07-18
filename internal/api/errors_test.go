package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testRequestBodyLimit = 64 << 10

type countingReader struct {
	reader io.Reader
	read   int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func TestDecodeJSONRejectsOversizedBodyWithoutReadingItAll(t *testing.T) {
	body := `{"padding":"` + strings.Repeat("x", testRequestBodyLimit*2) + `"}`
	reader := &countingReader{reader: strings.NewReader(body)}
	request := httptest.NewRequest("POST", "/", reader)
	var decoded map[string]string

	if err := decodeJSON(request, &decoded); err == nil {
		t.Fatal("decodeJSON accepted oversized JSON")
	}
	if reader.read > testRequestBodyLimit+1 {
		t.Fatalf("decodeJSON read %d bytes, want at most %d", reader.read, testRequestBodyLimit+1)
	}
}

func TestDecodeJSONAcceptsBodyAtExactLimit(t *testing.T) {
	prefix := []byte(`{"padding":"`)
	suffix := []byte(`"}`)
	body := append(prefix, bytes.Repeat([]byte{'x'}, testRequestBodyLimit-len(prefix)-len(suffix))...)
	body = append(body, suffix...)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	var decoded map[string]string

	if err := decodeJSON(request, &decoded); err != nil {
		t.Fatalf("decodeJSON rejected exact-limit body: %v", err)
	}
}

func TestOversizedProtectedRequestStillRequiresAuthentication(t *testing.T) {
	handler, _, _ := testServer(t)
	response := do(t, handler, http.MethodPost, "/api/v1/rules", "",
		strings.Repeat("x", testRequestBodyLimit+1))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated oversized status %d: %s", response.Code, response.Body.String())
	}
	exactBody(t, response, `{"error":{"code":"unauthorized","message":"Bearer token is missing or invalid","details":{}}}`)
}

func TestRequireNoBodyStopsAtRequestBodyLimit(t *testing.T) {
	reader := &countingReader{reader: strings.NewReader(strings.Repeat("x", testRequestBodyLimit*2))}
	request := httptest.NewRequest("GET", "/", reader)

	if err := requireNoBody(request); err == nil {
		t.Fatal("requireNoBody accepted a body")
	}
	if reader.read > testRequestBodyLimit+1 {
		t.Fatalf("requireNoBody read %d bytes, want at most %d", reader.read, testRequestBodyLimit+1)
	}
}
