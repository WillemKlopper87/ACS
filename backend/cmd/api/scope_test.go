package main

import "testing"

func TestDeviceInScope(t *testing.T) {
	cust := func(s string) *string { return &s }
	cases := []struct {
		name       string
		customerID *string
		accessible []string
		want       bool
	}{
		{"unassigned device is invisible to a scoped operator", nil, []string{"c1"}, false},
		{"device in scope", cust("c1"), []string{"c1", "c2"}, true},
		{"device out of scope", cust("c3"), []string{"c1", "c2"}, false},
		{"empty resolved scope sees nothing", cust("c1"), []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceInScope(tc.customerID, tc.accessible); got != tc.want {
				t.Errorf("deviceInScope(%v, %v) = %v, want %v", tc.customerID, tc.accessible, got, tc.want)
			}
		})
	}
}
