package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// -----------------------------------------------------------------------------
// Test helpers
// -----------------------------------------------------------------------------

// testServer builds a server wired for tests: no warm-up delay unless the test
// asks for one, and a logger that writes to io.Discard so `go test` output
// stays readable.
func testServer(t *testing.T, mutate func(*config)) *server {
	t.Helper()

	cfg := config{
		Addr:            ":0",
		Env:             "test",
		LogLevel:        slog.LevelDebug,
		ReadyDelay:      0,
		PreStopDelay:    0,
		ShutdownTimeout: time.Second,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		IdleTimeout:     time.Second,
		MaxBodyBytes:    1024,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: cfg.LogLevel}))
	return newServer(cfg, logger)
}

// readyServer returns a server that has already completed warm-up, which is
// the steady state most handler tests care about.
func readyServer(t *testing.T) *server {
	t.Helper()
	s := testServer(t, nil)
	s.ready.Store(true)
	return s
}

// counterValue reads a single sample out of a CounterVec for the given labels.
// Prometheus client_golang has no public getter, so we go through the
// dto.Metric round-trip the library itself uses.
func counterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()

	c, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}

	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("writing metric: %v", err)
	}
	return m.GetCounter().GetValue()
}

func histogramCount(t *testing.T, vec *prometheus.HistogramVec, labels ...string) uint64 {
	t.Helper()

	o, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}

	var m dto.Metric
	if err := o.(prometheus.Metric).Write(&m); err != nil {
		t.Fatalf("writing metric: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func gaugeValue(t *testing.T, vec *prometheus.GaugeVec, labels ...string) float64 {
	t.Helper()

	g, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}

	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("writing metric: %v", err)
	}
	return m.GetGauge().GetValue()
}

// -----------------------------------------------------------------------------
// Routing and handler behaviour
// -----------------------------------------------------------------------------

func TestRoutes(t *testing.T) {
	tests := []struct {
		name            string
		method          string
		path            string
		body            string
		wantStatus      int
		wantContentType string
		wantBodyContain []string
	}{
		{
			name:            "root returns build identity",
			method:          http.MethodGet,
			path:            "/",
			wantStatus:      http.StatusOK,
			wantContentType: "application/json; charset=utf-8",
			wantBodyContain: []string{`"service"`, `"version"`, `"commit"`, `"environment":"test"`, `"hostname"`},
		},
		{
			name:            "unknown path under the catch-all is a 404",
			method:          http.MethodGet,
			path:            "/definitely-not-a-route",
			wantStatus:      http.StatusNotFound,
			wantBodyContain: []string{`"error":"not found"`},
		},
		{
			name:            "liveness is green while running",
			method:          http.MethodGet,
			path:            "/healthz",
			wantStatus:      http.StatusOK,
			wantBodyContain: []string{`"status":"ok"`},
		},
		{
			name:            "readiness is green once warmed up",
			method:          http.MethodGet,
			path:            "/readyz",
			wantStatus:      http.StatusOK,
			wantBodyContain: []string{`"status":"ready"`},
		},
		{
			name:            "metrics endpoint exposes the app_info gauge",
			method:          http.MethodGet,
			path:            "/metrics",
			wantStatus:      http.StatusOK,
			wantBodyContain: []string{"app_info", "http_requests_in_flight", "go_goroutines"},
		},
		{
			name:            "echo returns the message it was given",
			method:          http.MethodPost,
			path:            "/api/echo",
			body:            `{"message":"hello platform"}`,
			wantStatus:      http.StatusOK,
			wantBodyContain: []string{`"echo":"hello platform"`, `"length":14`},
		},
		{
			name:            "echo with an empty body is still valid",
			method:          http.MethodPost,
			path:            "/api/echo",
			body:            "",
			wantStatus:      http.StatusOK,
			wantBodyContain: []string{`"echo":""`, `"length":0`},
		},
		{
			name:            "echo rejects malformed JSON",
			method:          http.MethodPost,
			path:            "/api/echo",
			body:            `{"message": `,
			wantStatus:      http.StatusBadRequest,
			wantBodyContain: []string{"invalid JSON body"},
		},
		{
			name:            "echo rejects GET",
			method:          http.MethodGet,
			path:            "/api/echo",
			wantStatus:      http.StatusMethodNotAllowed,
			wantBodyContain: []string{"method not allowed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := readyServer(t)
			handler := s.routes()

			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}

			req := httptest.NewRequest(tc.method, tc.path, body)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantContentType != "" {
				if got := rec.Header().Get("Content-Type"); got != tc.wantContentType {
					t.Errorf("Content-Type = %q, want %q", got, tc.wantContentType)
				}
			}

			got := rec.Body.String()
			for _, want := range tc.wantBodyContain {
				if !strings.Contains(got, want) {
					t.Errorf("body does not contain %q\nfull body: %s", want, got)
				}
			}
		})
	}
}

func TestRootResponseIsWellFormedJSON(t *testing.T) {
	s := readyServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)

	var got infoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("root response is not valid JSON: %v", err)
	}

	if got.Service != serviceName {
		t.Errorf("service = %q, want %q", got.Service, serviceName)
	}
	if got.Environment != "test" {
		t.Errorf("environment = %q, want %q", got.Environment, "test")
	}
	if got.Version == "" {
		t.Error("version is empty; the ldflags contract is broken")
	}
	if got.Commit == "" {
		t.Error("commit is empty; the ldflags contract is broken")
	}
	if !got.Ready {
		t.Error("ready = false, want true on a warmed-up server")
	}
	if got.Hostname == "" {
		t.Error("hostname is empty")
	}
}

func TestEchoRejectsOversizedBody(t *testing.T) {
	s := testServer(t, func(c *config) { c.MaxBodyBytes = 32 })
	s.ready.Store(true)

	// Comfortably larger than the 32-byte limit.
	payload := `{"message":"` + strings.Repeat("x", 512) + `"}`

	req := httptest.NewRequest(http.MethodPost, "/api/echo", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (body: %s)",
			rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exceeds 32 bytes") {
		t.Errorf("error message should name the limit, got: %s", rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Readiness and liveness transitions
//
// These are the tests that protect zero-downtime deploys. If someone
// "simplifies" the shutdown sequence later, these fail.
// -----------------------------------------------------------------------------

func TestReadinessTransitions(t *testing.T) {
	tests := []struct {
		name         string
		ready        bool
		shuttingDown bool
		wantReadyz   int
		wantHealthz  int
		wantReason   string
	}{
		{
			name:         "warming up: not ready, but alive",
			ready:        false,
			shuttingDown: false,
			wantReadyz:   http.StatusServiceUnavailable,
			wantHealthz:  http.StatusOK,
			wantReason:   "warming up",
		},
		{
			name:         "serving: ready and alive",
			ready:        true,
			shuttingDown: false,
			wantReadyz:   http.StatusOK,
			wantHealthz:  http.StatusOK,
		},
		{
			name:         "draining: readiness drops first, liveness follows",
			ready:        false,
			shuttingDown: true,
			wantReadyz:   http.StatusServiceUnavailable,
			wantHealthz:  http.StatusServiceUnavailable,
			wantReason:   "draining",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, nil)
			s.ready.Store(tc.ready)
			s.shuttingDown.Store(tc.shuttingDown)
			handler := s.routes()

			readyRec := httptest.NewRecorder()
			handler.ServeHTTP(readyRec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if readyRec.Code != tc.wantReadyz {
				t.Errorf("/readyz = %d, want %d", readyRec.Code, tc.wantReadyz)
			}
			if tc.wantReason != "" && !strings.Contains(readyRec.Body.String(), tc.wantReason) {
				t.Errorf("/readyz body = %s, want reason %q", readyRec.Body.String(), tc.wantReason)
			}

			healthRec := httptest.NewRecorder()
			handler.ServeHTTP(healthRec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if healthRec.Code != tc.wantHealthz {
				t.Errorf("/healthz = %d, want %d", healthRec.Code, tc.wantHealthz)
			}
		})
	}
}

func TestWarmUpFlipsReadiness(t *testing.T) {
	s := testServer(t, func(c *config) { c.ReadyDelay = 25 * time.Millisecond })

	if s.ready.Load() {
		t.Fatal("server reported ready before warm-up started")
	}

	done := make(chan struct{})
	go func() {
		s.warmUp(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("warmUp did not return within 2s")
	}

	if !s.ready.Load() {
		t.Error("server is not ready after warm-up completed")
	}
}

func TestWarmUpAbortsOnContextCancel(t *testing.T) {
	// A pod that receives SIGTERM during warm-up must never become ready —
	// otherwise it joins the Service endpoints on its way out of existence.
	s := testServer(t, func(c *config) { c.ReadyDelay = 10 * time.Second })

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		s.warmUp(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("warmUp ignored context cancellation")
	}

	if s.ready.Load() {
		t.Error("server became ready despite being cancelled during warm-up")
	}
}

func TestZeroReadyDelayIsImmediatelyReady(t *testing.T) {
	s := testServer(t, func(c *config) { c.ReadyDelay = 0 })
	s.warmUp(context.Background())

	if !s.ready.Load() {
		t.Error("ReadyDelay=0 should mark the server ready synchronously")
	}
}

// -----------------------------------------------------------------------------
// Middleware
// -----------------------------------------------------------------------------

func TestMetricsMiddlewareRecordsRequests(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantRoute  string
		wantStatus string
	}{
		{"root", http.MethodGet, "/", "/", "200"},
		{"liveness", http.MethodGet, "/healthz", "/healthz", "200"},
		{"echo post", http.MethodPost, "/api/echo", "/api/echo", "200"},
		{"unknown path collapses to other", http.MethodGet, "/random/junk", "other", "404"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := readyServer(t)
			handler := s.routes()

			before := counterValue(t, s.metrics.requests, tc.method, tc.wantRoute, tc.wantStatus)

			var body io.Reader
			if tc.method == http.MethodPost {
				body = strings.NewReader(`{"message":"m"}`)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			after := counterValue(t, s.metrics.requests, tc.method, tc.wantRoute, tc.wantStatus)
			if after != before+1 {
				t.Errorf("http_requests_total{method=%q,path=%q,status=%q} = %v, want %v",
					tc.method, tc.wantRoute, tc.wantStatus, after, before+1)
			}

			if got := histogramCount(t, s.metrics.duration, tc.method, tc.wantRoute); got == 0 {
				t.Errorf("http_request_duration_seconds{method=%q,path=%q} recorded no observations",
					tc.method, tc.wantRoute)
			}
		})
	}
}

func TestMetricsEndpointIsNotSelfCounted(t *testing.T) {
	// Counting scrapes makes every request graph a function of the scrape
	// interval. Assert explicitly that we skip it.
	s := readyServer(t)
	handler := s.routes()

	for i := 0; i < 3; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}

	if got := counterValue(t, s.metrics.requests, http.MethodGet, "/metrics", "200"); got != 0 {
		t.Errorf("scrapes of /metrics were counted: got %v, want 0", got)
	}
}

func TestAppInfoGaugeCarriesBuildLabels(t *testing.T) {
	m := newMetrics("1.2.3", "abc1234")

	if got := gaugeValue(t, m.info, "1.2.3", "abc1234"); got != 1 {
		t.Errorf("app_info{version=\"1.2.3\",commit=\"abc1234\"} = %v, want 1", got)
	}
}

func TestEachServerGetsItsOwnRegistry(t *testing.T) {
	// Regression guard: using the global prometheus registry would make the
	// second newServer call panic with a duplicate registration.
	a := testServer(t, nil)
	b := testServer(t, nil)

	if a.metrics.registry == b.metrics.registry {
		t.Fatal("two servers share a registry; tests will interfere with each other")
	}
}

func TestRecoveryMiddlewareTurnsPanicIntoFiveHundred(t *testing.T) {
	s := testServer(t, nil)

	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("synthetic failure")
	})

	// Same ordering as routes(): metrics -> logging -> recovery -> handler.
	handler := s.withMetrics(s.withLogging(s.withRecovery(boom)))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Errorf("body = %s, want a JSON error payload", rec.Body.String())
	}

	// The whole point of putting recovery innermost: the 500 must be visible
	// in metrics, not swallowed as a 200.
	if got := counterValue(t, s.metrics.requests, http.MethodGet, "/", "500"); got != 1 {
		t.Errorf("panic was not recorded as a 500 in metrics: got %v, want 1", got)
	}
}

func TestRecoveryDoesNotOverwriteAnAlreadyWrittenResponse(t *testing.T) {
	s := testServer(t, nil)

	partial := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"partial":true}`))
		panic("failure after the header was flushed")
	})

	rec := httptest.NewRecorder()
	s.withRecovery(partial).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202: recovery must not rewrite a sent header", rec.Code)
	}
}

func TestLoggingMiddlewareEmitsStructuredFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	s := testServer(t, nil)
	s.log = logger
	s.ready.Store(true)

	s.routes().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	var entry map[string]any
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("logging middleware produced no output")
	}
	// Take the first JSON object in case more than one line was written.
	if idx := strings.IndexByte(line, '\n'); idx > 0 {
		line = line[:idx]
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("log line is not valid JSON (%v): %s", err, line)
	}

	for _, key := range []string{"method", "path", "route", "status", "bytes", "duration"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("log entry is missing field %q: %v", key, entry)
		}
	}
	if entry["msg"] != "http request" {
		t.Errorf("msg = %v, want \"http request\"", entry["msg"])
	}
}

func TestStatusRecorderIsNotDoubleWrapped(t *testing.T) {
	// If each middleware wrapped independently, the inner recorder's status
	// would never propagate to the outer one and every metric would read 200.
	inner := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	if got := wrapWriter(inner); got != inner {
		t.Error("wrapWriter re-wrapped an existing recorder")
	}

	plain := httptest.NewRecorder()
	if got := wrapWriter(plain); got == nil || got.ResponseWriter != plain {
		t.Error("wrapWriter did not wrap a plain ResponseWriter")
	}
}

func TestStatusRecorderDefaultsToTwoHundredOnImplicitWrite(t *testing.T) {
	rec := wrapWriter(httptest.NewRecorder())
	n, err := rec.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 || rec.bytes != 5 {
		t.Errorf("bytes = %d/%d, want 5/5", n, rec.bytes)
	}
	if rec.status != http.StatusOK {
		t.Errorf("status = %d, want 200 on an implicit header write", rec.status)
	}
}

// -----------------------------------------------------------------------------
// Label cardinality
// -----------------------------------------------------------------------------

func TestRouteLabelIsBounded(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/", "/"},
		{"/healthz", "/healthz"},
		{"/readyz", "/readyz"},
		{"/metrics", "/metrics"},
		{"/api/echo", "/api/echo"},
		{"/api/echo/anything", "/api/echo"},
		{"/attacker-controlled-0001", "other"},
		{"/attacker-controlled-0002", "other"},
		{"", "other"},
	}

	seen := map[string]struct{}{}
	for _, tc := range tests {
		got := routeLabel(tc.path)
		if got != tc.want {
			t.Errorf("routeLabel(%q) = %q, want %q", tc.path, got, tc.want)
		}
		seen[got] = struct{}{}
	}

	// Five known routes plus the catch-all. If this number grows without a
	// deliberate change to routeLabel, cardinality is leaking.
	if len(seen) != 6 {
		t.Errorf("route label cardinality = %d distinct values, want 6: %v", len(seen), seen)
	}
}

// -----------------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------------

func TestLoadConfigDefaults(t *testing.T) {
	// t.Setenv guarantees the environment is restored after the test, and
	// marks the test as unsafe for t.Parallel - which is correct here.
	for _, key := range []string{
		"ADDR", "APP_ENV", "LOG_LEVEL", "READY_DELAY", "PRESTOP_DELAY",
		"SHUTDOWN_TIMEOUT", "READ_TIMEOUT", "WRITE_TIMEOUT", "IDLE_TIMEOUT",
		"MAX_BODY_BYTES",
	} {
		t.Setenv(key, "")
	}

	cfg, warnings := loadConfig()

	if len(warnings) != 0 {
		t.Errorf("clean environment produced warnings: %v", warnings)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want \":8080\"", cfg.Addr)
	}
	if cfg.Env != "local" {
		t.Errorf("Env = %q, want \"local\"", cfg.Env)
	}
	if cfg.PreStopDelay != 5*time.Second {
		t.Errorf("PreStopDelay = %v, want 5s", cfg.PreStopDelay)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.MaxBodyBytes != 1<<20 {
		t.Errorf("MaxBodyBytes = %d, want %d", cfg.MaxBodyBytes, 1<<20)
	}
}

func TestLoadConfigReadsEnvironment(t *testing.T) {
	t.Setenv("ADDR", ":9090")
	t.Setenv("APP_ENV", "prod")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("PRESTOP_DELAY", "12s")
	t.Setenv("MAX_BODY_BYTES", "4096")

	cfg, warnings := loadConfig()

	if len(warnings) != 0 {
		t.Errorf("valid environment produced warnings: %v", warnings)
	}
	if cfg.Addr != ":9090" {
		t.Errorf("Addr = %q, want \":9090\"", cfg.Addr)
	}
	if cfg.Env != "prod" {
		t.Errorf("Env = %q, want \"prod\"", cfg.Env)
	}
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("LogLevel = %v, want warn", cfg.LogLevel)
	}
	if cfg.PreStopDelay != 12*time.Second {
		t.Errorf("PreStopDelay = %v, want 12s", cfg.PreStopDelay)
	}
	if cfg.MaxBodyBytes != 4096 {
		t.Errorf("MaxBodyBytes = %d, want 4096", cfg.MaxBodyBytes)
	}
}

func TestLoadConfigFallsBackOnBadInput(t *testing.T) {
	// A typo in a Helm value must degrade to a default, not crash-loop the
	// pod. Crash-looping on config is how a one-character mistake becomes an
	// outage across every replica simultaneously.
	t.Setenv("PRESTOP_DELAY", "five seconds")
	t.Setenv("MAX_BODY_BYTES", "lots")
	t.Setenv("LOG_LEVEL", "chatty")

	cfg, warnings := loadConfig()

	if len(warnings) != 3 {
		t.Errorf("warnings = %d, want 3: %v", len(warnings), warnings)
	}
	if cfg.PreStopDelay != 5*time.Second {
		t.Errorf("PreStopDelay = %v, want the 5s default", cfg.PreStopDelay)
	}
	if cfg.MaxBodyBytes != 1<<20 {
		t.Errorf("MaxBodyBytes = %d, want the default", cfg.MaxBodyBytes)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want the info default", cfg.LogLevel)
	}
}

func TestLoadConfigRejectsNegativeDurations(t *testing.T) {
	t.Setenv("READY_DELAY", "-5s")

	cfg, warnings := loadConfig()

	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want exactly one", warnings)
	}
	if cfg.ReadyDelay != 2*time.Second {
		t.Errorf("ReadyDelay = %v, want the 2s default", cfg.ReadyDelay)
	}
}

func TestResolvedVersionAndCommitPreferEnvironment(t *testing.T) {
	t.Setenv("APP_VERSION", "1.4.2")
	t.Setenv("GIT_COMMIT", "deadbeef")

	if got := resolvedVersion(); got != "1.4.2" {
		t.Errorf("resolvedVersion() = %q, want \"1.4.2\"", got)
	}
	if got := resolvedCommit(); got != "deadbeef" {
		t.Errorf("resolvedCommit() = %q, want \"deadbeef\"", got)
	}

	t.Setenv("APP_VERSION", "")
	t.Setenv("GIT_COMMIT", "")

	if got := resolvedVersion(); got != version {
		t.Errorf("resolvedVersion() = %q, want the ldflags value %q", got, version)
	}
	if got := resolvedCommit(); got != commit {
		t.Errorf("resolvedCommit() = %q, want the ldflags value %q", got, commit)
	}
}

// -----------------------------------------------------------------------------
// Response helpers
// -----------------------------------------------------------------------------

func TestWriteJSONSetsHardeningHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusTeapot, map[string]string{"hello": "world"})

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want \"nosniff\"", got)
	}

	var decoded map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if decoded["hello"] != "world" {
		t.Errorf("payload = %v", decoded)
	}
}
