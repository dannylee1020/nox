package main

import "testing"

func TestParseMaxConcurrentRuns(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{name: "unset uses server default", value: "", want: 0},
		{name: "positive", value: "2", want: 2},
		{name: "zero", value: "0", wantErr: true},
		{name: "negative", value: "-1", wantErr: true},
		{name: "not an integer", value: "many", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseMaxConcurrentRuns(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseMaxConcurrentRuns(%q) error = %v, wantErr %t", test.value, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("parseMaxConcurrentRuns(%q) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}
