package main

import "testing"

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
