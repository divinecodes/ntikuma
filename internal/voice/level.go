package voice

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"time"
)

// InputLevel is normalized RMS amplitude, independent of the UI's display scale.
type InputLevel struct {
	Amplitude float64
	Available bool
}

type wavMeter struct {
	file       *os.File
	dataOffset int64
	readOffset int64
	last       InputLevel
	lastSample time.Time
}

func (m *wavMeter) close() {
	if m.file != nil {
		_ = m.file.Close()
	}
}

func (m *wavMeter) read(path string, now time.Time) InputLevel {
	if m.file == nil {
		var err error
		m.file, err = os.Open(path) // Read-only; the recorder owns all WAV writes.
		if err != nil {
			return InputLevel{}
		}
	}
	if m.dataOffset == 0 {
		offset, err := pcmDataOffset(m.file)
		if err != nil {
			return InputLevel{}
		} // A growing header may not be ready yet.
		m.dataOffset, m.readOffset = offset, offset
	}
	info, err := m.file.Stat()
	if err != nil {
		return InputLevel{}
	}
	end := info.Size()
	end -= (end - m.dataOffset) & 1 // Only complete signed 16-bit samples.
	if end <= m.readOffset {
		// Bridge normal recorder buffering, then stop showing stale activity.
		if now.Sub(m.lastSample) <= 250*time.Millisecond {
			return m.last
		}
		return InputLevel{}
	}
	start := m.readOffset
	const windowBytes = 16000 * 2 / 10 // The latest 100 ms, even after a delayed poll.
	if end-start > windowBytes {
		start = end - windowBytes
	}
	var buf [windowBytes]byte
	n, err := m.file.ReadAt(buf[:end-start], start)
	if err != nil && err != io.EOF {
		return InputLevel{}
	}
	n -= n & 1
	if n == 0 {
		return InputLevel{}
	}
	m.readOffset = start + int64(n)
	m.last = InputLevel{Amplitude: pcmRMS(buf[:n]), Available: true}
	m.lastSample = now
	return m.last
}

// pcmDataOffset scans RIFF chunks rather than assuming a 44-byte WAV header.
// The live data chunk's declared size can be zero or a placeholder until Stop.
func pcmDataOffset(r io.ReaderAt) (int64, error) {
	var header [12]byte
	if _, err := r.ReadAt(header[:], 0); err != nil {
		return 0, err
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return 0, errors.New("not a RIFF WAV")
	}
	validFormat := false
	for offset, chunks := int64(12), 0; offset < 1024*1024 && chunks < 64; chunks++ {
		var chunk [8]byte
		if _, err := r.ReadAt(chunk[:], offset); err != nil {
			return 0, err
		}
		size := int64(binary.LittleEndian.Uint32(chunk[4:]))
		offset += 8
		switch string(chunk[:4]) {
		case "fmt ":
			if size < 16 {
				return 0, errors.New("incomplete WAV format")
			}
			var format [16]byte
			if _, err := r.ReadAt(format[:], offset); err != nil {
				return 0, err
			}
			validFormat = binary.LittleEndian.Uint16(format[:2]) == 1 &&
				binary.LittleEndian.Uint16(format[2:4]) == 1 &&
				binary.LittleEndian.Uint32(format[4:8]) == 16000 &&
				binary.LittleEndian.Uint16(format[12:14]) == 2 &&
				binary.LittleEndian.Uint16(format[14:16]) == 16
		case "data":
			if !validFormat {
				return 0, errors.New("meter requires 16 kHz mono PCM16")
			}
			return offset, nil
		}
		offset += size + (size & 1)
	}
	return 0, errors.New("WAV header exceeds meter scan limit")
}

func pcmRMS(pcm []byte) float64 {
	var sum float64
	count := len(pcm) / 2
	if count == 0 {
		return 0
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(pcm[i:i+2]))) / 32768
		sum += sample * sample
	}
	return math.Sqrt(sum / float64(count))
}
