package cli

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

type tuiKey struct{ name, text string }

var tuiSequences = map[string]string{"\x1b[A": "up", "\x1b[B": "down", "\x1b[C": "right", "\x1b[D": "left", "\x1b[H": "home", "\x1b[F": "end", "\x1bOH": "home", "\x1bOF": "end", "\x1b[5~": "pageup", "\x1b[6~": "pagedown", "\x1b[3~": "delete", "\x1b[Z": "backtab"}

type tuiKeyboard struct {
	pending, paste []byte
	pasting        bool
	pasteTooLong   bool
}

func (k *tuiKeyboard) feed(data []byte, idle bool) []tuiKey {
	k.pending = append(k.pending, data...)
	var keys []tuiKey
	for len(k.pending) > 0 {
		if k.pasting {
			end := bytes.Index(k.pending, []byte("\x1b[201~"))
			if end < 0 {
				// Keep a possible split terminator, and cap clipboard memory.
				n := max(0, len(k.pending)-5)
				if len(k.paste)+n > 4096 {
					k.pasteTooLong = true
				}
				k.paste = append(k.paste, k.pending[:min(n, max(0, 4096-len(k.paste)))]...)
				k.pending = k.pending[n:]
				break
			}
			if len(k.paste)+end > 4096 {
				k.pasteTooLong = true
			}
			k.paste = append(k.paste, k.pending[:min(end, max(0, 4096-len(k.paste)))]...)
			if k.pasteTooLong {
				keys = append(keys, tuiKey{name: "paste-too-long"})
			} else {
				keys = append(keys, tuiKey{"paste", string(k.paste)})
			}
			k.pending = k.pending[end+6:]
			k.paste = nil
			k.pasting = false
			k.pasteTooLong = false
			continue
		}
		if k.pending[0] == 27 {
			if bytes.HasPrefix(k.pending, []byte("\x1b[200~")) {
				k.pasting = true
				k.pending = k.pending[6:]
				continue
			}
			if len(k.pending) == 1 {
				if idle {
					keys = append(keys, tuiKey{name: "escape"})
					k.pending = nil
				}
				break
			}
			if k.pending[1] == '[' || k.pending[1] == 'O' {
				end := 2
				for end < len(k.pending) && (k.pending[end] < 0x40 || k.pending[end] > 0x7e) {
					end++
				}
				if end >= len(k.pending) {
					if len(k.pending) > 64 {
						k.pending = nil
					}
					break
				}
				name := tuiSequences[string(k.pending[:end+1])]
				k.pending = k.pending[end+1:]
				if name != "" {
					keys = append(keys, tuiKey{name: name})
				}
				continue
			}
			k.pending = k.pending[2:]
			continue
		}
		switch k.pending[0] {
		case 1:
			keys = append(keys, tuiKey{name: "home"})
		case 5:
			keys = append(keys, tuiKey{name: "end"})
		case 11:
			keys = append(keys, tuiKey{name: "clearright"})
		case 21:
			keys = append(keys, tuiKey{name: "clearleft"})
		case 3:
			keys = append(keys, tuiKey{name: "interrupt"})
		case 9:
			keys = append(keys, tuiKey{name: "tab"})
		case 10, 13:
			keys = append(keys, tuiKey{name: "enter"})
		case 8, 127:
			keys = append(keys, tuiKey{name: "backspace"})
		case 18:
			keys = append(keys, tuiKey{name: "run"})
		default:
			if !utf8.FullRune(k.pending) {
				return keys
			}
			r, size := utf8.DecodeRune(k.pending)
			if r >= 32 && r != utf8.RuneError {
				keys = append(keys, tuiKey{"text", string(r)})
			}
			k.pending = k.pending[size:]
			continue
		}
		k.pending = k.pending[1:]
	}
	return keys
}

func tuiSingleLine(text string) string { return strings.TrimRight(text, "\r\n") }
