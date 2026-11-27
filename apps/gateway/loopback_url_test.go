package main

import "testing"

func TestRequireLoopbackHTTP(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8083", "http://127.0.0.1:3000/"} {
		if err := requireLoopbackHTTP(ok); err != nil {
			t.Fatalf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"https://127.0.0.1:8083",
		"http://localhost:8083",
		"http://10.200.200.31:8083",
		"http://127.0.0.1",
		"http://router.example.com:8083",
		"://bad",
	} {
		if err := requireLoopbackHTTP(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}
