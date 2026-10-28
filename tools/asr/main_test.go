package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-linux/v2"
)

func TestLongRecordingsSplitAtQuietAudioWithoutDroppingSamples(t *testing.T) {
	short := make([]float32, 16000)
	if chunkEnd(short) != len(short) {
		t.Fatal("short audio was split")
	}
	long := make([]float32, 32*16000)
	for i := range long {
		long[i] = 1
	}
	for i := 27 * 16000; i < 27*16000+1600; i++ {
		long[i] = 0
	}
	end := chunkEnd(long)
	if end != 27*16000+800 {
		t.Fatalf("did not prefer quiet boundary: %d", end)
	}
	if end+chunkEnd(long[end:]) != len(long) {
		t.Fatal("split lost audio")
	}
}

func TestNativeSpeechDetectionPreservesQuietSpeechAndResetsBetweenRecordings(t *testing.T) {
	root := os.Getenv("NTIKUMA_NATIVE_ASR_ROOT")
	if root == "" {
		t.Skip("set NTIKUMA_NATIVE_ASR_ROOT for real speech detector checks")
	}
	vad := newSpeechDetector(filepath.Join(root, "build/asr-eval/silero_vad.onnx"))
	if vad == nil {
		t.Fatal("could not load real speech detector")
	}
	defer sherpa.DeleteVoiceActivityDetector(vad)
	for _, clip := range []string{"clean", "silence-2", "quiet", "noise", "silence-5", "clean"} {
		wave := sherpa.ReadWave(filepath.Join(root, "build/asr-eval/smoke", clip+".wav"))
		if wave == nil {
			t.Fatal("missing public audio fixture", clip)
		}
		spans := speechSpans(vad, wave.Samples)
		wantSpeech := clip != "silence-2" && clip != "silence-5"
		if (len(spans) > 0) != wantSpeech {
			t.Fatalf("%s: speech spans %v", clip, spans)
		}
		for _, span := range spans {
			if span.start < 0 || span.end > len(wave.Samples) || span.end <= span.start {
				t.Fatalf("%s invalid span %v", clip, span)
			}
		}
	}
	// Cross the detector's circular buffer boundary, then flush speech at EOF.
	wave := sherpa.ReadWave(filepath.Join(root, "build/asr-eval/smoke/clean.wav"))
	long := make([]float32, 61*16000)
	copy(long[35*16000:], wave.Samples)
	copy(long[len(long)-len(wave.Samples):], wave.Samples)
	spans := speechSpans(vad, long)
	if len(spans) != 2 || spans[0].start < 34*16000 || spans[1].end != len(long) {
		t.Fatalf("lost long recording or final speech: %v", spans)
	}
	noise := make([]float32, 5*16000)
	random := rand.New(rand.NewSource(42))
	for i := range noise {
		noise[i] = float32(random.NormFloat64() * 0.05)
	}
	if got := speechSpans(vad, noise); len(got) != 0 {
		t.Fatalf("invented speech on stationary noise: %v", got)
	}
	if got := speechSpans(vad, nil); len(got) != 0 {
		t.Fatalf("empty recording reused speech: %v", got)
	}
}

func TestSpeechPaddingKeepsEdgesAndMergesOverlaps(t *testing.T) {
	spans := appendSpeechSpan(nil, 100, 1000, 40000)
	spans = appendSpeechSpan(spans, 5000, 10000, 40000)
	spans = appendSpeechSpan(spans, 30000, 39800, 40000)
	want := []speechSpan{{0, 14800}, {26800, 40000}}
	if !reflect.DeepEqual(spans, want) {
		t.Fatalf("got %v want %v", spans, want)
	}
	if got := appendSpeechSpan(nil, 10000, 11000, 5000); len(got) != 0 {
		t.Fatal("padded tail beyond recording created speech", got)
	}
}
