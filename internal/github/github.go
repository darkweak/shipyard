// Package github lists the repositories a personal access token can reach.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrUnauthorized means the token is invalid or expired.
var ErrUnauthorized = errors.New("GitHub rejected the token (invalid or expired)")

type Client struct {
	// BaseURL is the REST API root: https://api.github.com, or
	// https://<host>/api/v3 for GitHub Enterprise Server.
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{BaseURL: strings.TrimSuffix(baseURL, "/"), HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type Repo struct {
	FullName      string    `json:"fullName"`
	CloneURL      string    `json:"cloneUrl"`
	SSHURL        string    `json:"sshUrl"`
	Private       bool      `json:"private"`
	Archived      bool      `json:"archived"`
	DefaultBranch string    `json:"defaultBranch"`
	Description   string    `json:"description"`
	PushedAt      time.Time `json:"pushedAt"`
}

// MaxPages bounds the listing (100 repositories per page).
const MaxPages = 10

func (c *Client) get(ctx context.Context, token, path string, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub API: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return nil, fmt.Errorf("GitHub API rate limit exceeded, retry later")
	case resp.StatusCode >= 300:
		var e struct{ Message string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return nil, fmt.Errorf("GitHub API %s: %d %s", path, resp.StatusCode, e.Message)
	}
	return resp.Header, json.NewDecoder(resp.Body).Decode(out)
}

// Repos returns the token owner's login and the repositories the token can
// access (owned, collaborator and organization ones), most recently pushed first.
func (c *Client) Repos(ctx context.Context, token string) (string, []Repo, bool, error) {
	var user struct{ Login string }
	if _, err := c.get(ctx, token, "/user", &user); err != nil {
		return "", nil, false, err
	}
	repos := []Repo{}
	q := url.Values{"per_page": {"100"}, "sort": {"pushed"}, "affiliation": {"owner,collaborator,organization_member"}}
	for page := 1; page <= MaxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		var raw []struct {
			FullName      string    `json:"full_name"`
			CloneURL      string    `json:"clone_url"`
			SSHURL        string    `json:"ssh_url"`
			Private       bool      `json:"private"`
			Archived      bool      `json:"archived"`
			DefaultBranch string    `json:"default_branch"`
			Description   string    `json:"description"`
			PushedAt      time.Time `json:"pushed_at"`
		}
		h, err := c.get(ctx, token, "/user/repos?"+q.Encode(), &raw)
		if err != nil {
			return "", nil, false, err
		}
		for _, r := range raw {
			repos = append(repos, Repo{r.FullName, r.CloneURL, r.SSHURL, r.Private, r.Archived, r.DefaultBranch, r.Description, r.PushedAt})
		}
		if !strings.Contains(h.Get("Link"), `rel="next"`) {
			return user.Login, repos, false, nil
		}
	}
	return user.Login, repos, true, nil
}

// IsGitHubURL reports whether a clone URL points at the GitHub host of this client.
func (c *Client) IsGitHubURL(repoURL string) bool {
	host := "github.com"
	if u, err := url.Parse(c.BaseURL); err == nil && u.Host != "api.github.com" {
		host = u.Host
	}
	return strings.Contains(repoURL, "://"+host+"/") || strings.Contains(repoURL, "@"+host+":")
}
