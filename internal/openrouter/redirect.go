package openrouter

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// SameOriginRedirects is an http.Client CheckRedirect that follows redirects
// only within the origin (scheme, host and port) of the first request. Go
// drops Authorization across origins but forwards other key headers, such as
// x-api-key, and a 307/308 resends the question body, so neither may leave.
// A change of scheme, including https to http, counts as another origin.
func SameOriginRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	first := via[0].URL
	if !strings.EqualFold(req.URL.Scheme, first.Scheme) || !strings.EqualFold(req.URL.Host, first.Host) {
		return fmt.Errorf("refused a redirect from %s://%s to %s://%s", first.Scheme, first.Host, req.URL.Scheme, req.URL.Host)
	}
	return nil
}
