package main

import (
	"testing"
	"time"
)

func TestParseMinAge(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{input: "0"},
		{input: "1", want: time.Hour},
		{input: "0.5", want: 30 * time.Minute},
		{input: "1e1", want: 10 * time.Hour},
		// An unset expression reaches this input as an empty string, and a
		// workflow that does so is green today.
		{input: ""},
		{input: "-1", wantErr: true},
		{input: "abc", wantErr: true},
		// ParseFloat accepts these, and they are not ages.
		{input: "NaN", wantErr: true},
		{input: "Inf", wantErr: true},
		{input: "-Inf", wantErr: true},
		// The largest hour count that fits in a time.Duration; anything above
		// it would overflow into a negative min-age and silently skip every
		// version.
		{input: "2562047", want: 2562047 * time.Hour},
		{input: "2562048", wantErr: true},
		{input: "1e15", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := parseMinAge(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseMinAge(%q) = %v, nil, want an error", test.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMinAge(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Errorf("parseMinAge(%q) = %v, want %v", test.input, got, test.want)
			}
		})
	}
}
