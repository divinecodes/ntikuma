// ntikuma-asr is the Linux native inference worker. Its only transport is stdin/stdout.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-linux/v2"
)

type frame struct {
	Protocol int    `json:"protocol,omitempty"`
	Ready    bool   `json:"ready,omitempty"`
	Text     string `json:"text,omitempty"`
	Error    string `json:"error,omitempty"`
}

func main() {
	engine := flag.String("engine", "", "qwen3-asr, parakeet, or cohere-transcribe")
	language := flag.String("language", "en", "Cohere spoken language")
	dir := flag.String("model-dir", "", "local model bundle")
	threads := flag.Int("threads", 4, "CPU inference threads")
	version := flag.Bool("version", false, "print runtime protocol version")
	flag.Parse()
	if *version {
		fmt.Println("ntikuma-asr protocol=1 sherpa=2.0.0 engines=qwen3-asr,parakeet,cohere-transcribe")
		return
	}
	output := json.NewEncoder(os.Stdout)
	if err := serve(os.Stdin, output, *engine, *dir, *threads, *language); err != nil {
		_ = output.Encode(frame{Error: err.Error()})
		os.Exit(1)
	}
}

func serve(input io.Reader, output *json.Encoder, engine, dir string, threads int, language string) error {
	if threads < 1 || threads > 64 {
		return fmt.Errorf("CPU threads must be between 1 and 64")
	}
	config := sherpa.OfflineRecognizerConfig{DecodingMethod: "greedy_search"}
	config.FeatConfig.SampleRate = 16000
	config.FeatConfig.FeatureDim = 80
	config.ModelConfig.NumThreads = threads
	config.ModelConfig.Provider = "cpu"
	path := func(name string) string { return filepath.Join(dir, name) }
	switch engine {
	case "qwen3-asr":
		config.FeatConfig.FeatureDim = 128
		config.ModelConfig.Qwen3ASR = sherpa.OfflineQwen3ASRModelConfig{
			ConvFrontend: path("conv_frontend.onnx"), Encoder: path("encoder.int8.onnx"),
			Decoder: path("decoder.int8.onnx"), Tokenizer: path("tokenizer"),
			MaxTotalLen: 1024, MaxNewTokens: 512, Temperature: 1e-6, TopP: 0.8, Seed: 42,
		}
	case "parakeet":
		config.ModelConfig.ModelType = "nemo_transducer"
		config.ModelConfig.Transducer = sherpa.OfflineTransducerModelConfig{
			Encoder: path("encoder.int8.onnx"), Decoder: path("decoder.int8.onnx"), Joiner: path("joiner.int8.onnx"),
		}
		config.ModelConfig.Tokens = path("tokens.txt")
	case "cohere-transcribe":
		if !slices.Contains(strings.Fields("en fr de it es pt el nl pl zh ja ko vi ar"), language) {
			return fmt.Errorf("unsupported Cohere language %q", language)
		}
		config.FeatConfig.FeatureDim = 128
		config.ModelConfig.CohereTranscribe = sherpa.OfflineCohereTranscribeModelConfig{
			Encoder: path("encoder.int8.onnx"), Decoder: path("decoder.int8.onnx"),
			Language: language, UsePunct: 1, UseInverseTextNormalization: 1,
		}
		config.ModelConfig.Tokens = path("tokens.txt")
	default:
		return fmt.Errorf("unsupported local transcription model %q", engine)
	}
	var vad *sherpa.VoiceActivityDetector
	if engine == "cohere-transcribe" {
		vad = newSpeechDetector(path("silero_vad.onnx"))
		if vad == nil {
			return fmt.Errorf("could not load the bundled speech detector")
		}
		defer sherpa.DeleteVoiceActivityDetector(vad)
	}
	recognizer := sherpa.NewOfflineRecognizer(&config)
	if recognizer == nil {
		return fmt.Errorf("could not load the selected model; check its bundle and runtime compatibility")
	}
	defer sherpa.DeleteOfflineRecognizer(recognizer)
	if err := output.Encode(frame{Protocol: 1, Ready: true}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var request struct {
			WAVPath string `json:"wavPath"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return fmt.Errorf("invalid transcription request")
		}
		text, err := transcribe(recognizer, request.WAVPath, engine, vad)
		response := frame{Text: text}
		if err != nil {
			response.Error = err.Error()
		}
		if err := output.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func transcribe(recognizer *sherpa.OfflineRecognizer, path, engine string, vad *sherpa.VoiceActivityDetector) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 20*1024*1024 {
		return "", fmt.Errorf("recording is missing or too large")
	}
	wave := sherpa.ReadWave(path)
	if wave == nil || wave.SampleRate != 16000 || len(wave.Samples) == 0 {
		return "", fmt.Errorf("recording must be a nonempty 16 kHz mono WAV")
	}
	var parts []string
	spans := []speechSpan{{0, len(wave.Samples)}}
	if vad != nil {
		spans = speechSpans(vad, wave.Samples)
	}
	for _, span := range spans {
		samples := wave.Samples[span.start:span.end]
		for len(samples) > 0 {
			end := chunkEnd(samples)
			stream := sherpa.NewOfflineStream(recognizer)
			if stream == nil {
				return "", fmt.Errorf("could not create a transcription stream")
			}
			stream.AcceptWaveform(16000, samples[:end])
			recognizer.Decode(stream)
			result := stream.GetResult()
			sherpa.DeleteOfflineStream(stream)
			if result == nil {
				return "", fmt.Errorf("speech runtime returned no result")
			}
			if engine == "qwen3-asr" && len(result.Tokens) >= 512 {
				return "", fmt.Errorf("transcription reached its output limit; try a shorter recording")
			}
			parts = append(parts, strings.TrimSpace(result.Text))
			samples = samples[end:]
		}
	}
	return strings.TrimSpace(strings.Join(parts, " ")), nil
}

type speechSpan struct{ start, end int }

func newSpeechDetector(path string) *sherpa.VoiceActivityDetector {
	config := sherpa.VadModelConfig{SampleRate: 16000, NumThreads: 1, Provider: "cpu",
		SileroVad: sherpa.SileroVadModelConfig{Model: path, Threshold: 0.5,
			MinSilenceDuration: 0.3, MinSpeechDuration: 0.1, WindowSize: 512, MaxSpeechDuration: 25}}
	return sherpa.NewVoiceActivityDetector(&config, 30)
}

// Pad detected speech with original audio, preserving quiet word boundaries.
// Merge overlapping padding so a phrase cannot be transcribed twice.
func appendSpeechSpan(spans []speechSpan, start, end, length int) []speechSpan {
	start, end = max(0, start-3200), min(length, end+4800)
	if end <= start {
		return spans
	}
	if len(spans) > 0 && start <= spans[len(spans)-1].end {
		spans[len(spans)-1].end = max(spans[len(spans)-1].end, end)
		return spans
	}
	return append(spans, speechSpan{start, end})
}

func speechSpans(vad *sherpa.VoiceActivityDetector, samples []float32) []speechSpan {
	vad.Reset()
	vad.Clear()
	var spans []speechSpan
	drain := func() {
		for !vad.IsEmpty() {
			segment := vad.Front()
			spans = appendSpeechSpan(spans, int(segment.Start), int(segment.Start)+len(segment.Samples), len(samples))
			vad.Pop()
		}
	}
	for offset := 0; offset < len(samples); offset += 512 {
		window := samples[offset:min(offset+512, len(samples))]
		if len(window) < 512 {
			padded := make([]float32, 512)
			copy(padded, window)
			window = padded
		}
		vad.AcceptWaveform(window)
		drain()
	}
	vad.Flush()
	drain()
	return spans
}

// Keep model inputs within 30 seconds; prefer a quiet boundary for longer takes.
func chunkEnd(samples []float32) int {
	if len(samples) <= 30*16000 {
		return len(samples)
	}
	best, energy := 28*16000, math.Inf(1)
	for start := 25 * 16000; start < 29*16000; start += 1600 {
		var sum float64
		for _, value := range samples[start : start+1600] {
			sum += float64(value * value)
		}
		if sum < energy {
			best, energy = start+800, sum
		}
	}
	return best
}
