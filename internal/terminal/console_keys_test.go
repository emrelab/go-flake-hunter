package terminal

import (
	"encoding/binary"
	"strings"
	"testing"
)

func keyRecord(key, char, repeat uint16, control uint32) consoleRecord {
	r := consoleRecord{Kind: 1}
	binary.LittleEndian.PutUint32(r.Data[:4], 1)
	binary.LittleEndian.PutUint16(r.Data[4:6], repeat)
	binary.LittleEndian.PutUint16(r.Data[6:8], key)
	binary.LittleEndian.PutUint16(r.Data[10:12], char)
	binary.LittleEndian.PutUint32(r.Data[12:16], control)
	return r
}

func TestWindowsKeyRecords(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records []consoleRecord
		want    string
	}{
		{"repeat", []consoleRecord{keyRecord(0, 'ş', 3, 0)}, "şşş"},
		{"surrogate repeat", []consoleRecord{keyRecord(0, 0xD83D, 2, 0), keyRecord(0, 0xDE00, 2, 0)}, "😀😀"},
		{"up repeat", []consoleRecord{keyRecord(0x26, 0, 3, 0)}, strings.Repeat("\x1b[A", 3)},
		{"down", []consoleRecord{keyRecord(0x28, 0, 1, 0)}, "\x1b[B"},
		{"right", []consoleRecord{keyRecord(0x27, 0, 1, 0)}, "\x1b[C"},
		{"left", []consoleRecord{keyRecord(0x25, 0, 1, 0)}, "\x1b[D"},
		{"shift tab", []consoleRecord{keyRecord(9, 9, 2, 0x10)}, "\x1b[Z\x1b[Z"},
		{"ctrl c", []consoleRecord{keyRecord('C', 3, 1, 0x8)}, "\x03"},
		{"zero repeat", []consoleRecord{keyRecord(0, 'x', 0, 0)}, ""},
		{"non key", []consoleRecord{{Kind: 4}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s consoleKeys
			var got strings.Builder
			for _, record := range tc.records {
				got.Write(s.key(record))
			}
			if got.String() != tc.want {
				t.Fatalf("got %q, want %q", got.String(), tc.want)
			}
		})
	}
	record := keyRecord(0, 'x', 3, 0)
	binary.LittleEndian.PutUint32(record.Data[:4], 0)
	if got := new(consoleKeys).key(record); len(got) != 0 {
		t.Fatal("key release generated input")
	}
}
