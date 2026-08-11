package httpx

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ReadBody reads an external response with independent wire and decoded limits.
// Reading the wire representation first bounds gzip input before decompression.
func ReadBody(resp *http.Response, maxWireBytes, maxDecodedBytes int64) ([]byte, error) {
	if resp.ContentLength > maxWireBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxWireBytes)
	}
	wire, err := io.ReadAll(io.LimitReader(resp.Body, maxWireBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(wire)) > maxWireBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxWireBytes)
	}
	if !strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		if int64(len(wire)) > maxDecodedBytes {
			return nil, fmt.Errorf("decoded response exceeds %d bytes", maxDecodedBytes)
		}
		return wire, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer zr.Close()
	decoded, err := io.ReadAll(io.LimitReader(zr, maxDecodedBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(decoded)) > maxDecodedBytes {
		return nil, fmt.Errorf("decoded response exceeds %d bytes", maxDecodedBytes)
	}
	return decoded, nil
}
