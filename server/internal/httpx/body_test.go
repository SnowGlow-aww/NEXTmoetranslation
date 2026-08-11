package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadBodyBoundsWireAndGzipExpansion(t *testing.T) {
	response := func(body []byte, encoding string) *http.Response {
		return &http.Response{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Header: http.Header{"Content-Encoding": []string{encoding}}}
	}
	if _, err := ReadBody(response([]byte("123456"), ""), 5, 10); err == nil {
		t.Fatal("wire limit was not enforced")
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(strings.Repeat("x", 1024)))
	_ = writer.Close()
	if _, err := ReadBody(response(compressed.Bytes(), "gzip"), 1024, 100); err == nil {
		t.Fatal("decoded gzip limit was not enforced")
	}
	got, err := ReadBody(response(compressed.Bytes(), "gzip"), 1024, 2048)
	if err != nil || len(got) != 1024 {
		t.Fatalf("bounded gzip read len=%d err=%v", len(got), err)
	}
}
