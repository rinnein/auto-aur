package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const releaseList = `[
  {"tag_name":"v6.4.0-beta02","name":"6.4.0-beta02","assets":[{"name":"ani-linux-x86_64.appimage","browser_download_url":"https://x/appimage"}]},
  {"tag_name":"v6.4.0","name":"6.4.0","assets":[]},
  {"tag_name":"v6.3.0-alpha01","name":"6.3.0-alpha01","assets":[]}
]`

func server(t *testing.T, body string, status int, seen *http.Header) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Clone()
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client(), Pages: 3}
}

func TestFetchSearchPrefersName(t *testing.T) {
	c := server(t, releaseList, 200, nil)
	rel, err := c.Fetch(Wanted{Repo: "o/r", Search: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v6.3.0-alpha01" {
		t.Fatalf("got %q", rel.TagName)
	}
}

// Upstream release names omit the channel ("6.4.0-beta02"), so tag matching has
// to work as a fallback, otherwise beta releases are never found.
func TestFetchSearchFallsBackToTag(t *testing.T) {
	c := server(t, releaseList, 200, nil)
	rel, err := c.Fetch(Wanted{Repo: "o/r", Search: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v6.4.0-beta02" {
		t.Fatalf("got %q", rel.TagName)
	}
}

func TestFetchLatest(t *testing.T) {
	c := server(t, `{"tag_name":"v9.9.9","name":"9.9.9","assets":[]}`, 200, nil)
	rel, err := c.Fetch(Wanted{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v9.9.9" {
		t.Fatalf("got %q", rel.TagName)
	}
}

func TestFetchByTag(t *testing.T) {
	c := server(t, `{"tag_name":"v1.2.3","assets":[]}`, 200, nil)
	if _, err := c.Fetch(Wanted{Repo: "o/r", Tag: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
}

func TestTokenIsSent(t *testing.T) {
	var hdr http.Header
	c := server(t, `{"tag_name":"v1","assets":[]}`, 200, &hdr)
	c.Token = "secret-token"
	if _, err := c.Fetch(Wanted{Repo: "o/r"}); err != nil {
		t.Fatal(err)
	}
	if got := hdr.Get("Authorization"); got != "Bearer secret-token" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestRateLimitIsDetected(t *testing.T) {
	c := server(t, `{"message":"API rate limit exceeded for 1.2.3.4."}`, 403, nil)
	_, err := c.Fetch(Wanted{Repo: "o/r"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}

func TestAPIErrorSurfacesMessage(t *testing.T) {
	c := server(t, `{"message":"Not Found"}`, 404, nil)
	_, err := c.Fetch(Wanted{Repo: "o/r"})
	if err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("want message in error, got %v", err)
	}
}

func TestNoMatchIsAnError(t *testing.T) {
	c := server(t, releaseList, 200, nil)
	if _, err := c.Fetch(Wanted{Repo: "o/r", Search: "gamma"}); err == nil {
		t.Fatal("expected an error when nothing matches")
	}
}
