package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCapturedWebResponses(t *testing.T) {
	for _, nonStreaming := range []bool{false, true} {
		t.Run(fmt.Sprint(nonStreaming), func(t *testing.T) {
			file, contentType := "web-stream.sse", "text/event-stream"
			if nonStreaming {
				file, contentType = "web-completion.json", "application/json"
			}
			fixture, err := os.ReadFile("testdata/" + file)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", contentType)
				w.Write(fixture)
			}))
			defer srv.Close()
			client := New("test-key")
			client.BaseURL = srv.URL
			var deltas []string
			res, err := client.Stream(context.Background(), Request{Models: []string{"~anthropic/claude-sonnet-latest"}, NonStreaming: nonStreaming}, func(s string) { deltas = append(deltas, s) })
			if err != nil {
				t.Fatal(err)
			}
			if res.Text == "" || strings.Join(deltas, "") != res.Text || len(res.Citations) == 0 || res.Searches == nil || *res.Searches != 1 || res.Fallback {
				t.Fatalf("captured response lost text or metadata: %+v", res)
			}
			if nonStreaming && len(deltas) != 1 {
				t.Fatalf("JSON answer emitted %d times", len(deltas))
			}
			if !nonStreaming && len(deltas) < 2 {
				t.Fatal("SSE was not processed incrementally")
			}
		})
	}
}

func TestWebRequestFieldsAreOptional(t *testing.T) {
	for _, web := range []bool{false, true} {
		t.Run(fmt.Sprint(web), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				_, tools := body["tools"]
				_, limit := body["max_tool_calls"]
				if tools != web || limit != web {
					t.Errorf("unexpected optional fields: %s", body)
				}
				if web {
					var got []Tool
					json.Unmarshal(body["tools"], &got)
					if len(got) != 2 || got[0].Type != "openrouter:web_search" || got[0].Parameters["max_results"] != float64(5) || got[1].Parameters["max_uses"] != float64(3) || string(body["max_tool_calls"]) != "6" || string(body["stream"]) != "false" {
						t.Errorf("incorrect web request: %s", body)
					}
					if strings.Contains(string(body["tools"]), "function") {
						t.Error("server tools include a function definition")
					}
				} else if string(body["stream"]) != "true" {
					t.Error("ordinary requests stopped streaming")
				}
				sse(w, "data: [DONE]")
			}))
			defer srv.Close()
			client := New("test-key")
			client.BaseURL = srv.URL
			req := Request{Models: []string{"test/model"}}
			if web {
				req.Tools = WebTools(5, 3)
				req.MaxToolCalls = 6
				req.NonStreaming = true
			}
			if _, err := client.Stream(context.Background(), req, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCitationsAndUsageAcrossTerminalFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`data: {"model":"other/model","choices":[{"delta":{"content":"Answer", "annotations":[{"type":"url_citation","url_citation":{"url":"https://go.dev/","title":""}},{"type":"url_citation","url_citation":{"url":"javascript:bad()","title":"unsafe"}}]},"finish_reason":"stop"}]}`,
			`data: {"choices":[{"message":{"content":"Answer","annotations":[{"type":"url_citation","url_citation":{"url":"https://go.dev/","title":"Go"}},{"type":"url_citation","url_citation":{"url":"https://example.com/","title":"Example"}},{"type":"url_citation","url_citation":{"url":"https://go.dev/","title":"Repeated"}}]}}]}`,
			`data: {"choices":[],"usage":{"server_tool_use":{"web_search_requests":2}}}`,
			"data: [DONE]",
		)
	}))
	defer srv.Close()
	client := New("k")
	client.BaseURL = srv.URL
	res, err := client.Stream(context.Background(), Request{Models: []string{"test/model"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Answer" || len(res.Citations) != 2 || res.Citations[0].Title != "Go" || res.Citations[1].URL != "https://example.com/" || res.Searches == nil || *res.Searches != 2 || !res.Fallback {
		t.Fatalf("terminal metadata lost or duplicated: %+v", res)
	}
}

func TestWebMetadataSurvivesErrorAndCancellation(t *testing.T) {
	for _, cancelStream := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelStream), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sse(w, `data: {"model":"other/model","choices":[{"delta":{"content":"Partial","annotations":[{"type":"url_citation","url_citation":{"url":"https://go.dev/","title":"Go"}}]}}]}`)
				if cancelStream {
					<-r.Context().Done()
					return
				}
				sse(w, `data: {"error":{"message":"provider failed"}}`)
			}))
			defer srv.Close()
			client := New("k")
			client.BaseURL = srv.URL
			res, err := client.Stream(ctx, Request{Models: []string{"test/model"}}, func(string) {
				if cancelStream {
					cancel()
				}
			})
			if err == nil || (cancelStream && !errors.Is(err, context.Canceled)) {
				t.Fatalf("expected terminal error: %v", err)
			}
			if res.Text != "Partial" || len(res.Citations) != 1 || res.Searches != nil || !res.Fallback {
				t.Fatalf("partial metadata lost: %+v", res)
			}
		})
	}
}

func TestJSONCompletionPreservesReportedZeroAndErrorMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"test/model","choices":[{"message":{"content":"Partial"}}],"usage":{"server_tool_use_details":{"web_search_requests":0}},"error":{"message":"failed"}}`)
	}))
	defer srv.Close()
	client := New("k")
	client.BaseURL = srv.URL
	res, err := client.Stream(context.Background(), Request{Models: []string{"test/model"}, NonStreaming: true}, nil)
	if err == nil || res.Text != "Partial" || res.Searches == nil || *res.Searches != 0 {
		t.Fatalf("result %+v, error %v", res, err)
	}
}

func TestHTTPFailureKeepsReportedSourcesAndUsageWithoutRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Partial","annotations":[{"type":"url_citation","url_citation":{"url":"https://go.dev/","title":"Go"}}]}}],"usage":{"server_tool_use_details":{"web_search_requests":1}},"error":{"message":"failed after searching"}}`)
	}))
	defer srv.Close()
	client := New("k")
	client.BaseURL = srv.URL
	res, err := client.Stream(context.Background(), Request{Models: []string{"test/model"}, NonStreaming: true}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway || res.Text != "Partial" || len(res.Citations) != 1 || res.Searches == nil || *res.Searches != 1 || calls != 1 {
		t.Fatalf("failure metadata or request count: %+v, %v, %d calls", res, err, calls)
	}
}
