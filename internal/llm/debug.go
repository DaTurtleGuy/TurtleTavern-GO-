package llm

import (
	"context"
	"crypto/tls"
	"log"
	"net/http/httptrace"
	"strings"
	"time"
)

// Debug enables verbose logging of outbound AI-provider requests.
var Debug bool

func dbg(format string, args ...any) {
	if !Debug {
		return
	}
	log.Printf("[TT-DEBUG] "+format, args...)
}

var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"x-api-key":           true,
	"api-key":             true,
	"x-goog-api-key":      true,
	"cookie":              true,
}

func maskValue(key, value string) string {
	if sensitiveHeaders[strings.ToLower(key)] {
		if len(value) <= 8 {
			return strings.Repeat("*", len(value))
		}
		return value[:4] + "...(" + itoa(len(value)) + " chars)"
	}
	return value
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func preview(data []byte, max int) string {
	s := string(data)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "...(+" + itoa(len(s)-max) + " chars)"
	}
	return s
}

func withTrace(ctx context.Context, label string) context.Context {
	start := time.Now()
	trace := &httptrace.ClientTrace{
		GetConn:      func(hostPort string) { dbg("    getconn %s", hostPort) },
		DNSStart:     func(i httptrace.DNSStartInfo) { dbg("    dns start %s", i.Host) },
		DNSDone:      func(i httptrace.DNSDoneInfo) { dbg("    dns done err=%v addrs=%v", i.Err, i.Addrs) },
		ConnectStart: func(network, addr string) { dbg("    connect start %s %s", network, addr) },
		ConnectDone: func(network, addr string, err error) {
			dbg("    connect done %s %s err=%v (+%s)", network, addr, err, time.Since(start))
		},
		TLSHandshakeStart: func() { dbg("    tls start") },
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			dbg("    tls done version=%d err=%v", cs.Version, err)
		},
		GotConn:      func(info httptrace.GotConnInfo) { dbg("    gotconn reused=%v", info.Reused) },
		WroteRequest: func(info httptrace.WroteRequestInfo) { dbg("    wrote request err=%v", info.Err) },
	}
	return httptrace.WithClientTrace(ctx, trace)
}
