//go:build !windows

package tmux

import "testing"

func TestFindCtrlQ(t *testing.T) {
	tests := []struct {
		name       string
		in         []byte
		wantIdx    int
		wantLength int
	}{
		{"plain byte 17", []byte{17}, 0, 1},
		{"byte 17 after payload", []byte{'h', 'i', 17}, 2, 1},
		{"csi-u kitty format", []byte("\x1b[113;5u"), 0, 8},
		{"csi-u after payload", append([]byte("hi"), []byte("\x1b[113;5u")...), 2, 8},
		{"xterm modifyOtherKeys", []byte("\x1b[27;5;113~"), 0, 11},
		{"modifyOtherKeys after payload", append([]byte("ab"), []byte("\x1b[27;5;113~")...), 2, 11},
		{"not present", []byte("hello"), -1, 0},
		{"empty", []byte{}, -1, 0},
		{"unrelated csi", []byte("\x1b[A"), -1, 0},
		{"earliest wins", append([]byte{17}, []byte("\x1b[113;5u")...), 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIdx, gotLength := findCtrlQ(tt.in)
			if gotIdx != tt.wantIdx || gotLength != tt.wantLength {
				t.Errorf("findCtrlQ(%q) = (%d, %d), want (%d, %d)",
					tt.in, gotIdx, gotLength, tt.wantIdx, tt.wantLength)
			}
		})
	}
}
