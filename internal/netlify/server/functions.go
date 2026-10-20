package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// functions supervises the Node.js runtime running Netlify Functions and
// proxies requests to it.
type functions struct {
	script string
	node   string
	port   int
	env    []string // extra environment for the runtime
	names  map[string]bool
	routes []fnRoute
	proxy  *httputil.ReverseProxy
}

type fnRoute struct {
	Name         string   `json:"name"`
	Path         []string `json:"path"`
	ExcludedPath []string `json:"excludedPath"`
	Method       []string `json:"method"`
	PreferStatic bool     `json:"preferStatic"`

	paths, excluded []compiled
}

type compiled struct {
	host string
	re   *regexp.Regexp
}

func newFunctions(node, script string, port int) *functions {
	f := &functions{script: script, node: node, port: port, names: map[string]bool{}}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	f.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = target.Scheme, target.Host
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
			if ip, _, err := net.SplitHostPort(pr.In.RemoteAddr); err == nil {
				pr.Out.Header.Set("X-Nf-Client-Connection-Ip", ip)
			}
		},
		FlushInterval: -1, // stream responses (SSE, streamed bodies)
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("functions runtime unreachable: %v", err)
			http.Error(w, "Function runtime unavailable", http.StatusBadGateway)
		},
	}
	return f
}

// run starts the runtime and restarts it if it exits, until ctx is done.
func (f *functions) run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, f.node, f.script)
		cmd.Env = append(append(os.Environ(), f.env...), "SHIPYARD_FUNCTIONS_PORT="+strconv.Itoa(f.port))
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		start := time.Now()
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		log.Printf("functions runtime exited (%v), restarting in %s", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// waitReady polls the runtime until it reports its functions and routes.
func (f *functions) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	u := fmt.Sprintf("http://127.0.0.1:%d/__shipyard/routes", f.port)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			var body struct {
				Functions []string  `json:"functions"`
				Routes    []fnRoute `json:"routes"`
			}
			err = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if err == nil {
				for _, n := range body.Functions {
					f.names[n] = true
				}
				f.setRoutes(body.Routes)
				return nil
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("functions runtime not ready after %s: %v", timeout, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (f *functions) setRoutes(routes []fnRoute) {
	for _, r := range routes {
		for _, p := range r.Path {
			if host, re, err := compilePattern(p); err == nil {
				r.paths = append(r.paths, compiled{host, re})
			} else {
				log.Printf("function %s: unsupported path %q: %v", r.Name, p, err)
			}
		}
		for _, p := range r.ExcludedPath {
			if host, re, err := compilePattern(p); err == nil {
				r.excluded = append(r.excluded, compiled{host, re})
			}
		}
		f.routes = append(f.routes, r)
	}
}

// match finds a function whose config.path matches the request.
func (f *functions) match(r *http.Request, upath string, preferStatic bool) (string, map[string]string) {
	for _, rt := range f.routes {
		if rt.PreferStatic != preferStatic {
			continue
		}
		if len(rt.Method) > 0 && !contains(rt.Method, r.Method) {
			continue
		}
		excluded := false
		for _, e := range rt.excluded {
			if hostMatches(e.host, r.Host) && e.re.MatchString(upath) {
				excluded = true
			}
		}
		if excluded {
			continue
		}
		for _, p := range rt.paths {
			if !hostMatches(p.host, r.Host) {
				continue
			}
			if m := p.re.FindStringSubmatch(upath); m != nil {
				params := map[string]string{}
				for i, n := range p.re.SubexpNames() {
					if n != "" && n != "splat" {
						params[n] = m[i]
					}
				}
				return rt.Name, params
			}
		}
	}
	return "", nil
}

// serve proxies to the runtime. route is "default" for /.netlify/functions/
// requests and rewrites, "path" for config.path matches; fnPath is the path
// (with query) the function sees as event.path in the Lambda-style API.
func (f *functions) serve(w http.ResponseWriter, r *http.Request, name, route, fnPath string, params map[string]string) {
	r = r.Clone(r.Context())
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-shipyard-") {
			r.Header.Del(k)
		}
	}
	r.Header.Set("X-Shipyard-Function", name)
	r.Header.Set("X-Shipyard-Route", route)
	if fnPath != "" {
		r.Header.Set("X-Shipyard-Path", fnPath)
	}
	if len(params) > 0 {
		b, _ := json.Marshal(params)
		r.Header.Set("X-Shipyard-Params", base64.StdEncoding.EncodeToString(b))
	}
	f.proxy.ServeHTTP(w, r)
}

// functionName extracts <name> from /.netlify/functions/<name>[/…].
func functionName(upath string) (string, bool) {
	rest, ok := strings.CutPrefix(upath, "/.netlify/functions/")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(rest, "/")
	return name, name != ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
