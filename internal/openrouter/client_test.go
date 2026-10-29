package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sse(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, l := range lines {
		fmt.Fprintf(w, "%s\n\n", l)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func TestModelInfoJSONMatchesFrontend(t *testing.T) {
	b, err := json.Marshal(ModelInfo{ID: "test/model", Name: "Test model", ContextLength: 8192})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["contextLength"]) != "8192" {
		t.Fatalf("frontend cannot read contextLength: %s", b)
	}
	if _, exists := fields["ContextLength"]; exists {
		t.Fatalf("incorrect context-length key emitted: %s", b)
	}
}

func TestStreamCollectsDeltasAndSkipsComments(t *testing.T) {
	var got chatBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing auth header")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w,
			": OPENROUTER PROCESSING",
			`data: {"model":"anthropic/claude-sonnet-5.5","choices":[{"delta":{"content":"Hel"}}]}`,
			`data: {"model":"anthropic/claude-sonnet-5.5","choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			"data: [DONE]",
		)
	}))
	defer srv.Close()

	c := New("k")
	c.BaseURL = srv.URL
	var deltas []string
	res, err := c.Stream(context.Background(), Request{
		Models:   []string{"~anthropic/claude-sonnet-latest", "openai/gpt-5.1"},
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello" || strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("text %q deltas %v", res.Text, deltas)
	}
	if res.Fallback {
		t.Fatalf("alias resolved to the same family should not count as fallback")
	}
	if res.FinishReason != "stop" {
		t.Fatalf("finish reason %q", res.FinishReason)
	}
	if !got.Stream || len(got.Models) != 2 || got.Model != "" {
		t.Fatalf("request body: %+v", got)
	}
}

func TestStreamDetectsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"model":"openai/gpt-5.1","choices":[{"delta":{"content":"x"}}]}`, "data: [DONE]")
	}))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL
	res, err := c.Stream(context.Background(), Request{Models: []string{"~anthropic/claude-sonnet-latest", "openai/gpt-5.1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fallback || res.Model != "openai/gpt-5.1" {
		t.Fatalf("want fallback to gpt-5.1, got %+v", res)
	}
}

func TestSingleModelUsesModelField(t *testing.T) {
	var got chatBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		sse(w, "data: [DONE]")
	}))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL
	if _, err := c.Stream(context.Background(), Request{Models: []string{"openai/gpt-5.1"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got.Model != "openai/gpt-5.1" || len(got.Models) != 0 {
		t.Fatalf("body %+v", got)
	}
}

func TestHTTPErrorIsDecoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"code":402,"message":"Insufficient credits"}}`))
	}))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL
	_, err := c.Stream(context.Background(), Request{Models: []string{"a/b"}}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 402 || apiErr.Message != "Insufficient credits" {
		t.Fatalf("got %v", err)
	}
}

func TestMidStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`data: {"model":"a/b","choices":[{"delta":{"content":"part"}}]}`,
			`data: {"error":{"code":"server_error","message":"provider went away"}}`,
		)
	}))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL
	res, err := c.Stream(context.Background(), Request{Models: []string{"a/b"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "provider went away") || res.Text != "part" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestCancelReturnsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `data: {"model":"a/b","choices":[{"delta":{"content":"partial"}}]}`)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	res, err := c.Stream(ctx, Request{Models: []string{"a/b"}}, func(string) {
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	})
	if !errors.Is(err, context.Canceled) || res.Text != "partial" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestSameFamily(t *testing.T) {
	cases := []struct {
		req, res string
		want     bool
	}{
		{"~anthropic/claude-sonnet-latest", "anthropic/claude-sonnet-5.5", true},
		{"~anthropic/claude-sonnet-latest", "anthropic/claude-opus-5.5", false},
		{"openai/gpt-5.1", "openai/gpt-5.1-2025-11-13", true},
		{"openai/gpt-5.1", "google/gemini-3-pro", false},
		{"meta/llama:free", "meta/llama", true},
	}
	for _, c := range cases {
		if got := SameFamily(c.req, c.res); got != c.want {
			t.Errorf("SameFamily(%q,%q)=%v want %v", c.req, c.res, got, c.want)
		}
	}
}

func TestImageRequestFormatAndCompatibleFallbacks(t *testing.T) {
	const dataURL = "data:image/png;base64,aW1hZ2U="
	var got struct {
		Model    string   `json:"model"`
		Models   []string `json:"models"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			fmt.Fprint(w, `{"data":[{"id":"vendor/text","architecture":{"input_modalities":["text"]}},{"id":"vendor/vision","architecture":{"input_modalities":["text","image"]}},{"id":"vendor/backup","architecture":{"input_modalities":["text","image"]}}]}`)
		case "/chat/completions":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			sse(w, `data: {"model":"vendor/vision","choices":[{"delta":{"content":"A picture"}}]}`, "data: [DONE]")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := New("key")
	c.BaseURL = srv.URL
	res, err := c.Stream(context.Background(), Request{
		Models:   []string{"vendor/text", "vendor/vision", "vendor/backup"},
		Messages: []Message{{Role: "system", Content: "Help"}, {Role: "user", Content: "What is this?", Images: []string{dataURL}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fallback || res.Text != "A picture" {
		t.Fatalf("result: %+v", res)
	}
	if strings.Join(got.Models, ",") != "vendor/vision,vendor/backup" || got.Model != "" {
		t.Fatalf("unfiltered models: %+v", got)
	}
	if len(got.Messages) != 2 || string(got.Messages[0].Content) != `"Help"` {
		t.Fatalf("text format changed: %+v", got)
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(got.Messages[1].Content, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "What is this?" || parts[1].Type != "image_url" || parts[1].ImageURL.URL != dataURL {
		t.Fatalf("bad multimodal content: %s", got.Messages[1].Content)
	}
}

func TestImageRequestRejectsTextOnlyModelsAndCatalogErrors(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					t.Error("sent an image to an incompatible model")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"data":[{"id":"vendor/text","architecture":{"input_modalities":["text"]}}]}`)
			}))
			defer srv.Close()
			c := New("key")
			c.BaseURL = srv.URL
			_, err := c.Stream(context.Background(), Request{Models: []string{"vendor/text"}, Messages: []Message{{Role: "user", Content: "Question", Images: []string{"data:image/png;base64,aW1hZ2U="}}}}, nil)
			if err == nil {
				t.Fatal("image silently discarded")
			}
			if status == http.StatusOK && !strings.Contains(err.Error(), "image-capable model") {
				t.Fatal(err)
			}
		})
	}
}

func TestImageModelAliasesAndMetadata(t *testing.T) {
	catalog := []ModelInfo{
		{ID: "vendor/vision-2026-01-01", InputModalities: []string{"text", "image"}},
		{ID: "vendor/text", InputModalities: []string{"text"}},
	}
	got := imageModels([]string{"vendor/text", "~vendor/vision-latest", "unknown/model"}, catalog)
	if len(got) != 1 || got[0] != "~vendor/vision-latest" {
		t.Fatalf("got %v", got)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"vendor/vision","name":"Vision","context_length":8192,"architecture":{"input_modalities":["text","image"]}}]}`)
	}))
	defer srv.Close()
	c := New("")
	c.BaseURL = srv.URL
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 1 || len(models[0].InputModalities) != 2 || models[0].ContextLength != 8192 {
		t.Fatalf("models: %+v, %v", models, err)
	}
}

func streamFrom(t *testing.T, write func(http.ResponseWriter)) (Result, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { write(w) }))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL
	return c.Stream(context.Background(), Request{Models: []string{"a/b"}}, nil)
}

func TestStreamClosedBeforeItFinishedIsIncomplete(t *testing.T) {
	res, err := streamFrom(t, func(w http.ResponseWriter) {
		sse(w, `data: {"model":"a/b","choices":[{"index":0,"delta":{"content":"The first half of"}}]}`)
	})
	if !errors.Is(err, ErrIncomplete) || res.Text != "The first half of" {
		t.Fatalf("res %+v err %v", res, err)
	}
	// Either end signal on its own is enough: a finish reason or the end marker.
	for name, lines := range map[string][]string{
		"finish reason, no end marker": {`data: {"choices":[{"index":0,"delta":{"content":"Done."},"finish_reason":"stop"}]}`},
		"end marker, no finish reason": {`data: {"choices":[{"index":0,"delta":{"content":"Done."}}]}`, "data: [DONE]"},
	} {
		if res, err := streamFrom(t, func(w http.ResponseWriter) { sse(w, lines...) }); err != nil || res.Text != "Done." {
			t.Fatalf("%s: res %+v err %v", name, res, err)
		}
	}
}

func TestEarlyFinishReasonsAreErrorsThatKeepTheText(t *testing.T) {
	for _, reason := range []string{"length", "content_filter", "error"} {
		for _, transport := range []string{"sse", "json"} {
			res, err := streamFrom(t, func(w http.ResponseWriter) {
				if transport == "json" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"model":"a/b","choices":[{"index":0,"message":{"content":"cut by"},"finish_reason":%q}]}`, reason)
					return
				}
				sse(w, fmt.Sprintf(`data: {"model":"a/b","choices":[{"index":0,"delta":{"content":"cut by"},"finish_reason":%q}]}`, reason), "data: [DONE]")
			})
			var finish *FinishError
			if !errors.As(err, &finish) || finish.Reason != reason || res.Text != "cut by" || res.FinishReason != reason {
				t.Fatalf("%s/%s: res %+v err %v", reason, transport, res, err)
			}
		}
	}
	for _, reason := range []string{"stop", "end_turn"} {
		res, err := streamFrom(t, func(w http.ResponseWriter) {
			sse(w, fmt.Sprintf(`data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":%q}]}`, reason), "data: [DONE]")
		})
		if err != nil || res.Text != "ok" {
			t.Fatalf("%s: res %+v err %v", reason, res, err)
		}
	}
}
