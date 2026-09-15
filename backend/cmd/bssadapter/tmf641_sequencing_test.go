package main

import "testing"

func TestTMF641SwapPair(t *testing.T) {
	tests := []struct {
		name  string
		items []tmf641ItemRequest
		want  bool
	}{
		{"delete then add same role", []tmf641ItemRequest{{Action: "delete", Role: "gateway"}, {Action: "add", Role: "gateway", OUISerial: "001122-S2"}}, true},
		{"default role", []tmf641ItemRequest{{Action: "delete"}, {Action: "add", OUISerial: "001122-S2"}}, true},
		{"add then delete", []tmf641ItemRequest{{Action: "add", Role: "gateway", OUISerial: "001122-S2"}, {Action: "delete", Role: "gateway"}}, false},
		{"different roles", []tmf641ItemRequest{{Action: "delete", Role: "gateway"}, {Action: "add", Role: "ont", OUISerial: "001122-S2"}}, false},
		{"three items", []tmf641ItemRequest{{Action: "delete", Role: "gateway"}, {Action: "add", Role: "gateway", OUISerial: "001122-S2"}, {Action: "noChange"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := tmf641SwapPair(tt.items)
			if got != tt.want {
				t.Fatalf("tmf641SwapPair() = %v, want %v", got, tt.want)
			}
		})
	}
}
