// Package pvetest provides an httptest-backed fake of the PVE API
// (/api2/json/...) for driver and client unit tests.
package pvetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Server routes by exact "METHOD /api2/json<path>" match. It deliberately
// does NOT use http.ServeMux: tests override routes (re-register the same
// path with different behavior), which ServeMux punishes with a panic.
type Server struct {
	t      *testing.T
	mu     sync.RWMutex
	routes map[string]http.HandlerFunc
	srv    *httptest.Server
}

func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t, routes: map[string]http.HandlerFunc{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path // Path excludes the query string
		s.mu.RLock()
		h, ok := s.routes[key]
		s.mu.RUnlock()
		if !ok {
			t.Logf("pvetest: unhandled %s", key)
			http.Error(w, `{"errors":{"unhandled":"route"}}`, http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// URL returns the base PVE URL (no /api2/json suffix) — what a user
// would put in --pvenode-url.
func (s *Server) URL() string { return s.srv.URL }

// HandleFunc registers (or overrides) a route. path is relative to
// /api2/json, e.g. "/version".
func (s *Server) HandleFunc(method, path string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[method+" /api2/json"+path] = h
}

// Handle registers a JSON response wrapped in PVE's {"data": ...} envelope.
func (s *Server) Handle(method, path string, status int, data interface{}) {
	s.HandleFunc(method, path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
	})
}

// OKTask registers a task-status route reporting completed/OK and returns
// the UPID that clone/config/etc. fixtures should hand back.
func (s *Server) OKTask(node string) string {
	return s.taskRoute(node, "0000AB12", "OK")
}

// FailedTask is like OKTask but the task ends in an error exit status.
func (s *Server) FailedTask(node, exitStatus string) string {
	return s.taskRoute(node, "0000AB13", exitStatus)
}

func (s *Server) taskRoute(node, pid, exitStatus string) string {
	upid := fmt.Sprintf("UPID:%s:%s:00FF12AA:65F00001:qmtask:100:root@pam!rancher:", node, pid)
	s.Handle("GET", fmt.Sprintf("/nodes/%s/tasks/%s/status", node, upid), http.StatusOK, map[string]interface{}{
		"status": "stopped", "exitstatus": exitStatus, "node": node, "upid": upid,
		"id": "100", "type": "qmtask", "user": "root@pam!rancher",
		"pid": 1, "pstart": 1, "starttime": 1751900000,
	})
	return upid
}

// PVEError reproduces how PVE reports errors: the detail lives in the HTTP
// STATUS LINE REASON PHRASE (e.g. "HTTP/1.1 500 got lock request timeout"),
// which go-proxmox turns into the Go error string. net/http cannot set a
// custom reason phrase, so we hijack the connection and write it raw.
func PVEError(w http.ResponseWriter, code int, reason string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, reason, code)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, reason, code)
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, reason)
	_ = buf.Flush()
}
