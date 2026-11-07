package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckLoopback(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:6061", true},
		{"localhost:6061", true},
		{"[::1]:6061", true},
		{"127.0.0.2:0", true},
		{":6061", false}, // all interfaces
		{"0.0.0.0:6061", false},
		{"192.168.1.5:6061", false},
		{"example.com:6061", false},
		{"6061", false},
	}
	for _, tt := range tests {
		err := checkLoopback(tt.addr)
		if (err == nil) != tt.ok {
			t.Errorf("checkLoopback(%q) err = %v, want ok=%v", tt.addr, err, tt.ok)
		}
	}
}

func TestStartPprofEmptyIsNoop(t *testing.T) {
	if err := startPprof(""); err != nil {
		t.Errorf("startPprof(\"\") = %v, want nil", err)
	}
}

func TestLoopbackHostOnly(t *testing.T) {
	ok := loopbackHostOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for host, want := range map[string]int{
		"127.0.0.1:6061":    http.StatusOK,
		"localhost:6061":    http.StatusOK,
		"[::1]:6061":        http.StatusOK,
		"evil.example:6061": http.StatusForbidden, // DNS rebinding
		"evil.example":      http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		ok.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: status %d, want %d", host, rec.Code, want)
		}
	}
}

func TestStartPprofServesOnLoopback(t *testing.T) {
	if err := startPprof("127.0.0.1:0"); err != nil {
		t.Fatalf("startPprof: %v", err)
	}
}
