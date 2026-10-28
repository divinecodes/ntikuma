package voice

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleBytes(samples ...int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}
	return pcm
}

func wavFixture(pcm []byte, extraChunk bool) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(0)) // Unfinalized recording header.
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	for _, v := range []uint16{1, 1} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	for _, v := range []uint32{16000, 32000} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	for _, v := range []uint16{2, 16} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	if extraChunk {
		b.WriteString("JUNK")
		_ = binary.Write(&b, binary.LittleEndian, uint32(3))
		b.Write([]byte{1, 2, 3, 0})
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))
	b.Write(pcm)
	return b.Bytes()
}

func TestPCMAmplitude(t *testing.T) {
	for _, tc := range []struct {
		name string
		pcm  []byte
		want float64
	}{
		{"empty", nil, 0}, {"silence", sampleBytes(0, 0, 0), 0},
		{"negative full scale", sampleBytes(-32768, -32768), 1},
		{"half scale", sampleBytes(-16384, 16384), 0.5},
		{"mixed energy", sampleBytes(0, -32768), math.Sqrt(0.5)},
		{"partial sample", append(sampleBytes(16384), 1), 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pcmRMS(tc.pcm); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("RMS %g, want %g", got, tc.want)
			}
		})
	}
}

func TestGrowingWAVLevelDoesNotChangeRecording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.wav")
	pcm := sampleBytes(-16384, 16384)
	data := wavFixture(pcm, true)
	if err := os.WriteFile(path, data[:20], 0600); err != nil {
		t.Fatal(err)
	}
	m := &wavMeter{}
	defer m.close()
	now := time.Now()
	if got := m.read(path, now); got.Available {
		t.Fatal("partial header produced a level")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := m.read(path, now); !got.Available || got.Amplitude != 0.5 {
		t.Fatalf("level %+v", got)
	}
	if got := m.read(path, now.Add(100*time.Millisecond)); !got.Available || got.Amplitude != 0.5 {
		t.Fatal("normal write buffering lost latest sample")
	}
	if got := m.read(path, now.Add(time.Second)); got.Available || got.Amplitude != 0 {
		t.Fatal("stale audio kept showing activity")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(sampleBytes(0, 0))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.read(path, now.Add(time.Second)); !got.Available || got.Amplitude != 0 {
		t.Fatalf("silence %+v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, append(data, sampleBytes(0, 0)...)) {
		t.Fatal("meter modified the recording")
	}
}

func TestWAVMeterReadsRecentSamplesAndCompletePairs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.wav")
	pcm := bytes.Repeat(sampleBytes(-32768), 3200)
	pcm = append(pcm, make([]byte, 3200)...)
	if err := os.WriteFile(path, wavFixture(pcm, false), 0600); err != nil {
		t.Fatal(err)
	}
	m := &wavMeter{}
	defer m.close()
	now := time.Now()
	if got := m.read(path, now); !got.Available || got.Amplitude != 0 {
		t.Fatalf("replayed old audio: %+v", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	_ = m.read(path, now)
	if _, err = f.Write([]byte{0x80}); err != nil {
		t.Fatal(err)
	}
	if got := m.read(path, now); !got.Available || got.Amplitude != 1 {
		t.Fatalf("lost a sample split across writes: %+v", got)
	}
}

func TestWAVMeterUnavailableAndUnsupportedFormats(t *testing.T) {
	m := &wavMeter{}
	if got := m.read(filepath.Join(t.TempDir(), "missing.wav"), time.Now()); got.Available {
		t.Fatal("missing audio available")
	}
	for _, data := range [][]byte{[]byte("not a WAV"), wavFixture(nil, false)} {
		path := filepath.Join(t.TempDir(), "audio.wav")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		m := &wavMeter{}
		if got := m.read(path, time.Now()); got.Available {
			t.Fatal("missing samples produced activity")
		}
		m.close()
	}
	data := wavFixture(sampleBytes(1, 2), false)
	binary.LittleEndian.PutUint16(data[34:36], 32)
	if _, err := pcmDataOffset(bytes.NewReader(data)); err == nil {
		t.Fatal("accepted incompatible sample format")
	}
}
