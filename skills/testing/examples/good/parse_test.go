package main

import "testing"

var parseCases = []struct {
	name    string
	in      string
	want    int64
	wantErr bool
}{
	{"whole number", "12", 1200, false},
	{"two decimals", "12.34", 1234, false},
	{"one decimal", "12.3", 1230, false},
	{"leading zeros", "007.50", 750, false},
	{"empty", "", 0, true},
	{"negative", "-1.00", 0, true},
	{"plus sign", "+1", 0, true},
	{"three decimals", "1.234", 0, true},
	{"trailing dot", "1.", 0, true},
	{"overflow", "99999999999999999999", 0, true},
}

func TestParseAmount(t *testing.T) {
	for _, tt := range parseCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAmount(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAmount(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseAmount(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// FuzzParseAmount checks properties that must hold for any input. Plain
// `go test` runs the seeds; `go test -fuzz=FuzzParseAmount` explores further.
func FuzzParseAmount(f *testing.F) {
	for _, tt := range parseCases {
		f.Add(tt.in)
	}
	f.Fuzz(func(t *testing.T, s string) {
		cents, err := ParseAmount(s)
		if err != nil {
			return
		}
		if cents < 0 {
			t.Fatalf("ParseAmount(%q) = %d, want a non-negative amount", s, cents)
		}
		back, err := ParseAmount(FormatCents(cents))
		if err != nil || back != cents {
			t.Fatalf("round trip of %q: got %d, %v; want %d", s, back, err, cents)
		}
	})
}

// Run with: go test -run '^$' -bench ParseAmount -benchmem -count 10 > new.txt
// and compare runs with benchstat old.txt new.txt.
func BenchmarkParseAmount(b *testing.B) {
	for b.Loop() {
		if _, err := ParseAmount("1234567.89"); err != nil {
			b.Fatal(err)
		}
	}
}
