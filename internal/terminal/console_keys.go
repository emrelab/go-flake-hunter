package terminal

import (
	"bytes"
	"encoding/binary"
	"unicode/utf16"
)

type consoleRecord struct {
	Kind, Padding uint16
	Data          [16]byte
}

type consoleKeys struct{ surrogate uint16 }

func (k *consoleKeys) key(record consoleRecord) []byte {
	if record.Kind != 1 || binary.LittleEndian.Uint32(record.Data[:4]) == 0 {
		return nil
	}
	repeat := int(binary.LittleEndian.Uint16(record.Data[4:6]))
	if repeat == 0 {
		return nil
	}
	key := binary.LittleEndian.Uint16(record.Data[6:8])
	char := binary.LittleEndian.Uint16(record.Data[10:12])
	switch key {
	case 0x09:
		if binary.LittleEndian.Uint32(record.Data[12:16])&0x10 != 0 {
			return bytes.Repeat([]byte("\x1b[Z"), repeat)
		}
	case 0x2E:
		return bytes.Repeat([]byte("\x1b[3~"), repeat)
	case 0x26:
		return bytes.Repeat([]byte("\x1b[A"), repeat)
	case 0x28:
		return bytes.Repeat([]byte("\x1b[B"), repeat)
	case 0x27:
		return bytes.Repeat([]byte("\x1b[C"), repeat)
	case 0x25:
		return bytes.Repeat([]byte("\x1b[D"), repeat)
	case 0x21:
		return bytes.Repeat([]byte("\x1b[5~"), repeat)
	case 0x22:
		return bytes.Repeat([]byte("\x1b[6~"), repeat)
	case 0x24:
		return bytes.Repeat([]byte("\x1b[H"), repeat)
	case 0x23:
		return bytes.Repeat([]byte("\x1b[F"), repeat)
	}
	if char == 0 {
		return nil
	}
	if char >= 0xD800 && char <= 0xDBFF {
		k.surrogate = char
		return nil
	}
	r := rune(char)
	if char >= 0xDC00 && char <= 0xDFFF {
		r = utf16.DecodeRune(rune(k.surrogate), r)
	}
	k.surrogate = 0
	return bytes.Repeat([]byte(string(r)), repeat)
}
