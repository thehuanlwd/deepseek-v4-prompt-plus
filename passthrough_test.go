package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 上游编码协商：只保留本进程能解码的编码，客户端拒绝压缩时退回 identity。
func TestSanitizeAcceptEncoding(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"br, gzip, deflate, zstd", "gzip, deflate"},
		{"gzip", "gzip"},
		{"br", "identity"},
		{"GZIP;q=0.9, br", "gzip"},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.in != "" {
			h.Set("Accept-Encoding", c.in)
		}
		sanitizeAcceptEncoding(h)
		if got := h.Get("Accept-Encoding"); got != c.want {
			t.Errorf("sanitizeAcceptEncoding(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 解压读取器必须还原原始字节，并标记为「已解码」。
func TestDecodeReaderGzip(t *testing.T) {
	payload := strings.Repeat("data: {\"choices\":[]}\n\n", 100)

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(payload)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	rd, changed, err := decodeReader("gzip", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decodeReader gzip: %v", err)
	}
	if !changed {
		t.Fatal("decodeReader 应标记为已解码")
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("read decoded: %v", err)
	}
	if string(got) != payload {
		t.Errorf("解压结果不一致：got %d bytes, want %d", len(got), len(payload))
	}
}

// 明文响应不应被包装，也不应改动响应头。
func TestOpenDecodedBodyPlainPassthrough(t *testing.T) {
	resp := &http.Response{
		StatusCode:    200,
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		ContentLength: -1,
		Body:          io.NopCloser(strings.NewReader("data: [DONE]\n")),
	}
	body, err := openDecodedBody(resp)
	if err != nil {
		t.Fatalf("openDecodedBody: %v", err)
	}
	if body != resp.Body {
		t.Error("明文响应不应被重新包装")
	}
}

// 改写模式下解压后必须去掉编码与长度头，否则客户端会把明文当压缩包解。
func TestOpenDecodedBodyStripsEncodingHeaders(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	resp := &http.Response{
		StatusCode:    200,
		ContentLength: -1,
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
			"Content-Length":   []string{"123"},
		},
		Body: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}

	body, err := openDecodedBody(resp)
	if err != nil {
		t.Fatalf("openDecodedBody: %v", err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read decoded: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("解压结果 = %q", data)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Error("Content-Encoding 未被移除")
	}
	if resp.Header.Get("Content-Length") != "" {
		t.Error("Content-Length 未被移除")
	}
}
