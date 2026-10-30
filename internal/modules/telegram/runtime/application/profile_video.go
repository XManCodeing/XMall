package application

import (
	"encoding/binary"
	"errors"
)

// Telegram accepts square MPEG4 animated profile photos up to 1080x1080.
const maxProfileVideoSide = 1080

var errProfileVideo = errors.New("profile video must be a square MP4 up to 1080x1080")

func isMP4(data []byte) bool {
	return len(data) >= 12 && string(data[4:8]) == "ftyp"
}

func validateProfileVideo(data []byte) error {
	moov, ok := findBox(data, "moov")
	if !ok {
		return errProfileVideo
	}
	for rest := moov; len(rest) > 0; {
		kind, payload, next, ok := nextBox(rest)
		if !ok {
			break
		}
		rest = next
		if kind != "trak" {
			continue
		}
		tkhd, ok := findBox(payload, "tkhd")
		if !ok {
			continue
		}
		w, h, ok := trackSize(tkhd)
		if !ok || w == 0 || h == 0 {
			continue
		}
		if w != h || w > maxProfileVideoSide {
			return errProfileVideo
		}
		return nil
	}
	return errProfileVideo
}

func findBox(data []byte, want string) ([]byte, bool) {
	for len(data) > 0 {
		kind, payload, next, ok := nextBox(data)
		if !ok {
			return nil, false
		}
		if kind == want {
			return payload, true
		}
		data = next
	}
	return nil, false
}

func nextBox(data []byte) (kind string, payload, rest []byte, ok bool) {
	if len(data) < 8 {
		return "", nil, nil, false
	}
	size := uint64(binary.BigEndian.Uint32(data))
	kind = string(data[4:8])
	header := uint64(8)
	switch size {
	case 0:
		size = uint64(len(data))
	case 1:
		if len(data) < 16 {
			return "", nil, nil, false
		}
		size = binary.BigEndian.Uint64(data[8:16])
		header = 16
	}
	if size < header || size > uint64(len(data)) {
		return "", nil, nil, false
	}
	return kind, data[header:size], data[size:], true
}

// trackSize reads the 16.16 fixed-point width/height at the end of a tkhd box.
func trackSize(tkhd []byte) (uint32, uint32, bool) {
	if len(tkhd) < 1 {
		return 0, 0, false
	}
	offset := 76
	if tkhd[0] == 1 {
		offset = 88
	}
	if len(tkhd) < offset+8 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(tkhd[offset:]) >> 16, binary.BigEndian.Uint32(tkhd[offset+4:]) >> 16, true
}
