package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Redirect is one rule from netlify.toml ([[redirects]]) or _redirects.
type Redirect struct {
	From       string              `json:"from"`
	To         string              `json:"to"`
	Status     int                 `json:"status"`
	Force      bool                `json:"force"`
	Query      map[string]string   `json:"query,omitempty"`
	Conditions map[string][]string `json:"conditions,omitempty"`
	Headers    map[string]string   `json:"headers,omitempty"`

	host string
	re   *regexp.Regexp
}

// HeaderRule is one [[headers]] block or _headers section.
type HeaderRule struct {
	For    string            `json:"for"`
	Values map[string]string `json:"values"`

	host string
	re   *regexp.Regexp
}

type Config struct {
	Redirects []Redirect   `json:"redirects"`
	Headers   []HeaderRule `json:"headers"`
}

// loadConfig reads the rules Shipyard extracted from netlify.toml, then the
// _redirects and _headers files of the publish directory. As on Netlify,
// _redirects rules are evaluated before netlify.toml ones.
func loadConfig(root, cfgPath string) (*Config, error) {
	cfg := &Config{}
	if b, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(b, cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", cfgPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(root, "_redirects")); err == nil {
		cfg.Redirects = append(parseRedirects(string(b)), cfg.Redirects...)
	}
	if b, err := os.ReadFile(filepath.Join(root, "_headers")); err == nil {
		cfg.Headers = append(cfg.Headers, parseHeaders(string(b))...)
	}

	redirects := cfg.Redirects[:0]
	for _, r := range cfg.Redirects {
		if r.Status == 0 {
			r.Status = http.StatusMovedPermanently
		}
		var err error
		if r.host, r.re, err = compilePattern(r.From); err != nil {
			log.Printf("skipping redirect %q: %v", r.From, err)
			continue
		}
		redirects = append(redirects, r)
	}
	cfg.Redirects = redirects
	for i := range cfg.Headers {
		h := &cfg.Headers[i]
		var err error
		if h.host, h.re, err = compilePattern(h.For); err != nil {
			log.Printf("skipping headers for %q: %v", h.For, err)
		}
	}
	return cfg, nil
}

var statusToken = regexp.MustCompile(`^(\d{3})(!?)$`)

// parseRedirects reads the _redirects format:
//
//	/from  [key=:value …]  /to  [status[!]]  [Condition=a,b …]
func parseRedirects(s string) []Redirect {
	var out []Redirect
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		r := Redirect{From: f[0], Status: http.StatusMovedPermanently}
		i := 1
		for ; i < len(f) && isParamToken(f[i]); i++ {
			k, v, _ := strings.Cut(f[i], "=")
			if r.Query == nil {
				r.Query = map[string]string{}
			}
			r.Query[k] = v
		}
		if i >= len(f) {
			continue
		}
		r.To = f[i]
		i++
		if i < len(f) {
			if m := statusToken.FindStringSubmatch(f[i]); m != nil {
				r.Status, _ = strconv.Atoi(m[1])
				r.Force = m[2] == "!"
				i++
			}
		}
		for ; i < len(f); i++ {
			if k, v, ok := strings.Cut(f[i], "="); ok {
				if r.Conditions == nil {
					r.Conditions = map[string][]string{}
				}
				r.Conditions[k] = strings.Split(v, ",")
			}
		}
		out = append(out, r)
	}
	return out
}

func isParamToken(t string) bool {
	return strings.Contains(t, "=") && !strings.HasPrefix(t, "/") && !strings.Contains(t, "://")
}

// parseHeaders reads the _headers format: a path line followed by indented
// "Name: value" lines.
func parseHeaders(s string) []HeaderRule {
	var out []HeaderRule
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimRight(raw, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			out = append(out, HeaderRule{For: t, Values: map[string]string{}})
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok || len(out) == 0 {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		vals := out[len(out)-1].Values
		if prev, dup := vals[k]; dup {
			v = prev + ", " + v
		}
		vals[k] = v
	}
	return out
}

// compilePattern turns a Netlify path pattern into a regexp. "*" is a splat
// (captured as "splat"), ":name" matches one path segment. A pattern may start
// with a scheme and host ("https://old.example.com/*"). Trailing slashes are
// optional, as on Netlify.
func compilePattern(p string) (string, *regexp.Regexp, error) {
	host := ""
	if i := strings.Index(p, "://"); i >= 0 {
		rest := p[i+3:]
		h, pth, _ := strings.Cut(rest, "/")
		host, p = strings.ToLower(h), "/"+pth
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	var b strings.Builder
	b.WriteString("^")
	splat := false
	for i := 0; i < len(p); {
		switch {
		case strings.HasPrefix(p[i:], "/*") && i+2 == len(p) && !splat:
			// "/blog/*" also matches "/blog".
			b.WriteString("(?:/(?P<splat>.*))?")
			splat = true
			i += 2
		case p[i] == '*':
			if splat {
				b.WriteString(".*")
			} else {
				b.WriteString("(?P<splat>.*)")
				splat = true
			}
			i++
		case p[i] == ':' && i > 0 && p[i-1] == '/' && i+1 < len(p) && isIdent(p[i+1]):
			j := i + 1
			for j < len(p) && isIdent(p[j]) {
				j++
			}
			b.WriteString("(?P<" + p[i+1:j] + ">[^/]+)")
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
			i++
		}
	}
	b.WriteString("/?$")
	re, err := regexp.Compile(b.String())
	return host, re, err
}

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func hostMatches(want, host string) bool {
	if want == "" {
		return true
	}
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	return strings.EqualFold(want, host)
}

func (h *HeaderRule) matches(host, upath string) bool {
	return hostMatches(h.host, host) && h.re.MatchString(upath)
}

func (r *Redirect) match(req *http.Request, upath string) (map[string]string, bool) {
	if !hostMatches(r.host, req.Host) {
		return nil, false
	}
	m := r.re.FindStringSubmatch(upath)
	if m == nil {
		return nil, false
	}
	params := map[string]string{}
	for i, name := range r.re.SubexpNames() {
		if name != "" {
			params[name] = m[i]
		}
	}
	if !matchQuery(r.Query, req.URL.Query(), params) || !matchConditions(r.Conditions, req) {
		return nil, false
	}
	return params, true
}

// matchQuery requires every listed query parameter; ":name" values bind a placeholder.
func matchQuery(want map[string]string, got url.Values, params map[string]string) bool {
	for k, v := range want {
		if !got.Has(k) {
			return false
		}
		if strings.HasPrefix(v, ":") {
			params[v[1:]] = got.Get(k)
		} else if got.Get(k) != v {
			return false
		}
	}
	return true
}

// matchConditions supports Language and Cookie. Country and Role need
// Netlify's edge (GeoIP, Identity), so rules using them never match.
func matchConditions(conds map[string][]string, r *http.Request) bool {
	for k, vals := range conds {
		switch strings.ToLower(k) {
		case "language":
			if !acceptsLanguage(r.Header.Get("Accept-Language"), vals) {
				return false
			}
		case "cookie":
			found := false
			for _, name := range vals {
				if _, err := r.Cookie(strings.TrimSpace(name)); err == nil {
					found = true
				}
			}
			if !found {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func acceptsLanguage(header string, langs []string) bool {
	for _, part := range strings.Split(header, ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		primary, _, _ := strings.Cut(strings.ToLower(tag), "-")
		for _, l := range langs {
			l = strings.ToLower(strings.TrimSpace(l))
			if l == strings.ToLower(tag) || l == primary {
				return true
			}
		}
	}
	return false
}

// substitute replaces :splat and :placeholders in a rule target.
func substitute(to string, params map[string]string) string {
	names := make([]string, 0, len(params))
	for k := range params {
		names = append(names, k)
	}
	// Longest first so ":id" doesn't clobber ":identifier".
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, k := range names {
		to = strings.ReplaceAll(to, ":"+k, params[k])
	}
	return to
}
