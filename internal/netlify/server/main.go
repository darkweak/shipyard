// Command netlify-server serves a Netlify publish directory the way Netlify's
// CDN does: pretty URLs, 404.html, redirect / rewrite / proxy rules and custom
// headers from netlify.toml, _redirects and _headers.
//
// Shipyard embeds this source and compiles it inside Netlify-mode images. It
// only uses the standard library so that it builds without network access.
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	root := flag.String("root", "/site", "publish directory")
	cfgPath := flag.String("config", "/netlify.json", "rules extracted from netlify.toml")
	fnScript := flag.String("functions", "", "Netlify Functions runtime script (empty: no functions)")
	node := flag.String("node", "node", "Node.js binary running the functions")
	fnPort := flag.Int("functions-port", 9999, "loopback port of the functions runtime")
	flag.Parse()

	cfg, err := loadConfig(*root, *cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The functions runtime outlives the HTTP server so in-flight requests finish.
	fnCtx, stopFunctions := context.WithCancel(context.Background())
	defer stopFunctions()

	s := &site{root: *root, cfg: cfg}
	if *fnScript != "" {
		s.fn = newFunctions(*node, *fnScript, *fnPort)
		go s.fn.run(fnCtx)
		if err := s.fn.waitReady(ctx, 60*time.Second); err != nil {
			log.Printf("warning: %v", err)
		}
	}
	log.Printf("serving %s on %s (%d redirect rules, %d header rules)", *root, *addr, len(cfg.Redirects), len(cfg.Headers))
	srv := &http.Server{Addr: *addr, Handler: logRequests(s), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		// Load balancers (Swarm's routing mesh, proxies) may route a few more
		// requests here after SIGTERM: keep serving briefly before draining.
		log.Printf("shutting down: draining for %s", drainDelay)
		srv.SetKeepAlivesEnabled(false)
		time.Sleep(drainDelay)
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-done
}

const drainDelay = 2 * time.Second

type site struct {
	root string
	cfg  *Config
	fn   *functions // nil without Netlify Functions
}

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}
	s.applyHeaders(w, r.Host, upath)

	// Functions: /.netlify/functions/<name>, then functions with a custom
	// config.path, which take precedence over redirects and static files.
	if s.fn != nil {
		if name, ok := functionName(upath); ok {
			s.fn.serve(w, r, name, "default", "", nil)
			return
		}
		if name, params := s.fn.match(r, upath, false); name != "" {
			s.fn.serve(w, r, name, "path", "", params)
			return
		}
	}

	file, dirRedirect := s.resolve(upath)
	exists := file != "" || dirRedirect != ""

	for i := range s.cfg.Redirects {
		rule := &s.cfg.Redirects[i]
		params, ok := rule.match(r, upath)
		if !ok {
			continue
		}
		// Shadowing: an existing file wins over rules that aren't forced.
		if exists && !rule.Force {
			continue
		}
		s.apply(w, r, rule, params)
		return
	}

	switch {
	case dirRedirect != "":
		to := dirRedirect
		if r.URL.RawQuery != "" {
			to += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, to, http.StatusMovedPermanently)
	case file != "":
		s.serveFile(w, r, file, http.StatusOK)
	default:
		if s.fn != nil {
			if name, params := s.fn.match(r, upath, true); name != "" { // preferStatic
				s.fn.serve(w, r, name, "path", "", params)
				return
			}
		}
		s.notFound(w, r)
	}
}

func (s *site) apply(w http.ResponseWriter, r *http.Request, rule *Redirect, params map[string]string) {
	to := substitute(rule.To, params)
	if len(rule.Query) == 0 && r.URL.RawQuery != "" && !strings.Contains(to, "?") {
		to += "?" + r.URL.RawQuery // Netlify passes the query string through
	}
	switch {
	case rule.Status >= 300 && rule.Status < 400:
		w.Header().Set("Location", to)
		w.WriteHeader(rule.Status)
	case strings.HasPrefix(to, "http://") || strings.HasPrefix(to, "https://"):
		s.proxy(w, r, to, rule)
	default:
		// Rewrite: serve the target path, with the rule's status (200, 404…).
		target, _, _ := strings.Cut(to, "?")
		if name, ok := functionName(target); ok && s.fn != nil {
			s.fn.serve(w, r, name, "default", to, nil)
			return
		}
		file, dir := s.resolve(target)
		if file == "" && dir != "" {
			file, _ = s.resolve(dir)
		}
		if file == "" {
			s.notFound(w, r)
			return
		}
		s.serveFile(w, r, file, rule.Status)
	}
}

func (s *site) proxy(w http.ResponseWriter, r *http.Request, to string, rule *Redirect) {
	target, err := url.Parse(to)
	if err != nil {
		http.Error(w, "bad proxy target", http.StatusBadGateway)
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = target.Host
			pr.SetXForwarded()
			for k, v := range rule.Headers {
				pr.Out.Header.Set(k, v)
			}
		},
	}
	rp.ServeHTTP(w, r)
}

// resolve maps a URL path to a file, with Netlify's pretty URLs:
// /about serves about.html, or redirects to /about/ when about/index.html exists.
func (s *site) resolve(upath string) (file, redirect string) {
	clean := path.Clean("/" + upath)
	if clean == "/_redirects" || clean == "/_headers" {
		return "", "" // configuration, not content (hidden on Netlify too)
	}
	full := filepath.Join(s.root, filepath.FromSlash(clean))
	if strings.HasSuffix(upath, "/") {
		if isFile(filepath.Join(full, "index.html")) {
			return filepath.Join(full, "index.html"), ""
		}
		if clean != "/" && isFile(full+".html") {
			return full + ".html", ""
		}
		return "", ""
	}
	if isFile(full) {
		return full, ""
	}
	if isFile(full + ".html") {
		return full + ".html", ""
	}
	if isFile(filepath.Join(full, "index.html")) {
		return "", clean + "/"
	}
	return "", ""
}

func (s *site) notFound(w http.ResponseWriter, r *http.Request) {
	if p := filepath.Join(s.root, "404.html"); isFile(p) {
		s.serveFile(w, r, p, http.StatusNotFound)
		return
	}
	http.Error(w, "Not Found", http.StatusNotFound)
}

func (s *site) serveFile(w http.ResponseWriter, r *http.Request, file string, status int) {
	f, err := os.Open(file)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate") // Netlify's default
	}
	if status == http.StatusOK || status == 0 {
		http.ServeContent(w, r, file, st.ModTime(), f)
		return
	}
	ctype := mime.TypeByExtension(filepath.Ext(file))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
}

func (s *site) applyHeaders(w http.ResponseWriter, host, upath string) {
	for _, h := range s.cfg.Headers {
		if h.re != nil && h.matches(host, upath) {
			for k, v := range h.Values {
				w.Header().Set(k, v)
			}
		}
	}
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Microsecond))
	})
}
