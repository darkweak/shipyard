package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/user":
			fmt.Fprint(w, `{"login":"octo"}`)
		case r.URL.Query().Get("page") == "1":
			w.Header().Set("Link", `<x?page=2>; rel="next"`)
			fmt.Fprint(w, `[{"full_name":"octo/site","clone_url":"https://github.com/octo/site.git","private":true,"default_branch":"main"}]`)
		default:
			fmt.Fprint(w, `[{"full_name":"org/docs","clone_url":"https://github.com/org/docs.git","default_branch":"master"}]`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)

	login, repos, truncated, err := c.Repos(context.Background(), "good")
	if err != nil || login != "octo" || truncated || len(repos) != 2 || !repos[0].Private || repos[1].DefaultBranch != "master" {
		t.Fatalf("got %q %+v %v %v", login, repos, truncated, err)
	}
	if _, _, _, err := c.Repos(context.Background(), "bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	gh := New("")
	if !gh.IsGitHubURL("https://github.com/a/b.git") || !gh.IsGitHubURL("git@github.com:a/b.git") || gh.IsGitHubURL("https://gitlab.com/a/b.git") {
		t.Error("IsGitHubURL")
	}
}
