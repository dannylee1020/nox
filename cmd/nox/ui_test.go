package main

import "testing"

func TestValidateUILoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8081", "localhost:8081", "[::1]:8081"} {
		if err := validateUILoopback(address); err != nil {
			t.Errorf("validateUILoopback(%q) = %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8081", ":8081", "192.0.2.1:8081", "bad"} {
		if err := validateUILoopback(address); err == nil {
			t.Errorf("validateUILoopback(%q) succeeded", address)
		}
	}
}
