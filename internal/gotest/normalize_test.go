package gotest

import (
	"strings"
	"testing"
)

func TestNormalizeCollapsesNoise(t *testing.T) {
	a := "2026/11/02 01:04:33 startup failed: /tmp/abc123/x.db (0xc000123456) 12.5ms 127.0.0.1:54321"
	b := "2026/11/02 01:09:59 startup failed: /tmp/zzz999/x.db (0xc000987654) 8ms 127.0.0.1:61001"

	na, nb := Normalize(a), Normalize(b)
	if na != nb {
		t.Fatalf("same error normalized differently:\n a=%q\n b=%q", na, nb)
	}
}

func TestNormalizeKeepsMeaning(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{"shared state polluted: seeded=42", "shared state polluted: seeded=42"},
		{"2026-11-02T01:04:33.123Z", "<ts>"},
		{"/var/folders/abc/run/file", "<tmp>"},
		{"/private/var/folders/abc/run/file", "/private<tmp>"},
		{"0xc000123456", "<ptr>"},
		{"12.5ms 1µs 2ns 3s", "<dur> <dur> <dur> <dur>"},
		{"127.0.0.1:54321 [::1]:54322", "<addr> <addr>"},
		{strings.Repeat("1ms ", 1000), strings.Repeat("<dur> ", 1000)},
	} {
		if got := Normalize(tc.input); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeEmpty(t *testing.T) {
	if Normalize("") != "" {
		t.Error("empty input must return empty")
	}
}

func BenchmarkNormalize(b *testing.B) {
	for _, tc := range []struct {
		name, input string
	}{
		{"clean", "shared state polluted: seeded=42"},
		{"noise", "2026/11/02 01:04:33 startup failed: /tmp/abc123/x.db (0xc000123456) 12.5ms 127.0.0.1:54321"},
		{"dense", strings.Repeat("1ms 127.0.0.1:54321 0xc000123456 ", 1000)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				Normalize(tc.input)
			}
		})
	}
}
