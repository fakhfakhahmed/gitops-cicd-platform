// Command app is a deliberately small HTTP service.
//
// The point of this repository is the *delivery machinery* around the service
// — GitHub Actions, GHCR, Argo CD, Helm, supply-chain security — not the
// service itself. So the service stays tiny, but it is written the way a
// production service should be written, because the pipeline's job is to prove
// that *this exact binary* reached *that exact cluster*.
//
// Three design choices below exist purely to make the platform observable:
//
//  1. The root endpoint echoes back the version and git commit that were
//     baked in at build time. That is the end-to-end proof: if you push a
//     commit and later curl the pod and see that commit SHA, every link in
//     the chain (build -> push -> tag bump -> git commit -> Argo CD sync ->
//     rollout) worked.
//  2. Readiness is separate from liveness and flips to "not ready" *before*
//     the server stops accepting connections. That is what makes rolling
//     updates genuinely zero-downtime (see shutdown logic at the bottom).
//  3. Prometheus metrics are exposed on /metrics with bounded label
//     cardinality, so the chart's ServiceMonitor can scrape it safely.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Build metadata. These are *not* constants: they are injected at link time by
//
//	go build -ldflags "-X main.version=... -X main.commit=..."
//
// which both the Makefile and the Dockerfile do. A binary therefore knows its
// own provenance without reading any config. Defaults below are what you see
// if someone builds with a bare `go build`.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// serviceName is the logical name reported by the API and attached to every
// log line. It is a compile-time constant because it identifies the component,
// not the deployment — the per-deployment identity is APP_ENV plus the
// injected version/commit.
const serviceName = "gitops-cicd-platform-app"

// -----------------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------------

// config is the full runtime configuration of the service, sourced entirely
// from environment variables.
//
// Why environment variables and not a config file? Because in Kubernetes the
// config file would have to be a ConfigMap mounted as a volume, which means a
// pod restart to pick up changes anyway — and env vars compose far more
// cleanly with Helm values, which is how every knob in this repo is driven.
// The chart writes these from `.Values.env` and `.Values.extraEnv`.
type config struct {
	// Addr is the TCP address the HTTP server binds to, e.g. ":8080".
	Addr string

	// Env is the logical deployment environment: dev | staging | prod.
	// It is surfaced on the root endpoint so you can tell at a glance which
	// overlay produced the running pod.
	Env string

	// LogLevel controls the slog level. dev runs at debug, prod at info.
	LogLevel slog.Level

	// ReadyDelay is an artificial warm-up. A real service would use this
	// window to prime caches, open database pools or load a model. We keep it
	// because it makes the readiness-gate behaviour visible during a rollout:
	// the new pod exists but does not receive traffic until it is genuinely
	// able to serve.
	ReadyDelay time.Duration

	// PreStopDelay is the single most important value in this file for
	// zero-downtime deploys. See the shutdown section for the full story.
	PreStopDelay time.Duration

	// ShutdownTimeout bounds how long we wait for in-flight requests to
	// finish before forcing the process down. It must be comfortably less
	// than the pod's terminationGracePeriodSeconds, or the kubelet will
	// SIGKILL us mid-drain.
	ShutdownTimeout time.Duration

	// HTTP server timeouts. A server without these will happily accumulate
	// stuck connections until it runs out of file descriptors; this is the
	// classic "it was fine for three weeks then fell over" failure.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	// MaxBodyBytes caps request bodies so a single client cannot exhaust
	// memory on the echo endpoint.
	MaxBodyBytes int64
}

// loadConfig builds a config from the environment, applying sane defaults for
// every field. It never fails: an unparseable value falls back to the default
// and is reported by the caller's logger rather than crashing the pod, because
// a crash loop over a typo'd env var is a worse outcome than a default.
func loadConfig() (config, []string) {
	var warnings []string

	note := func(key, raw string, fallback any) {
		warnings = append(warnings,
			fmt.Sprintf("invalid value %q for %s, using default %v", raw, key, fallback))
	}

	cfg := config{
		Addr:            envString("ADDR", ":8080"),
		Env:             envString("APP_ENV", "local"),
		LogLevel:        slog.LevelInfo,
		ReadyDelay:      envDuration("READY_DELAY", 2*time.Second, note),
		PreStopDelay:    envDuration("PRESTOP_DELAY", 5*time.Second, note),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 15*time.Second, note),
		ReadTimeout:     envDuration("READ_TIMEOUT", 10*time.Second, note),
		WriteTimeout:    envDuration("WRITE_TIMEOUT", 30*time.Second, note),
		IdleTimeout:     envDuration("IDLE_TIMEOUT", 120*time.Second, note),
		MaxBodyBytes:    envInt64("MAX_BODY_BYTES", 1<<20, note), // 1 MiB
	}

	// LOG_LEVEL is parsed separately because slog.Level has its own
	// text unmarshaller (accepts "debug", "INFO", "warn", "error").
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		var lvl slog.Level
		if err := lvl.UnmarshalText([]byte(raw)); err != nil {
			note("LOG_LEVEL", raw, cfg.LogLevel)
		} else {
			cfg.LogLevel = lvl
		}
	}

	return cfg, warnings
}

// resolvedVersion prefers the link-time value but allows APP_VERSION to
// override it. That override is what lets the Helm chart surface the tag it
// actually deployed even when running a locally-built binary.
func resolvedVersion() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	return version
}

// resolvedCommit works the same way for the git SHA, via GIT_COMMIT.
func resolvedCommit() string {
	if c := os.Getenv("GIT_COMMIT"); c != "" {
		return c
	}
	return commit
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration, note func(string, string, any)) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		note(key, raw, fallback)
		return fallback
	}
	return d
}

func envInt64(key string, fallback int64, note func(string, string, any)) int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		note(key, raw, fallback)
		return fallback
	}
	return n
}

// -----------------------------------------------------------------------------
// Metrics
// -----------------------------------------------------------------------------

// metrics holds every Prometheus collector the service exports.
//
// They are bound to an explicit *prometheus.Registry rather than the package
// level default registry. This matters for two reasons:
//
//   - Tests can build a fresh registry per test case; with the default
//     registry a second call to newMetrics would panic with
//     "duplicate metrics collector registration attempted".
//   - Nothing a dependency happens to register leaks into our /metrics
//     output, so the scrape surface is exactly what we intend.
type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	info     *prometheus.GaugeVec
	inFlight prometheus.Gauge
	registry *prometheus.Registry
}

// newMetrics builds the collector set. buildVersion/buildCommit are passed in
// rather than read from the package vars so tests can assert on the labels.
func newMetrics(buildVersion, buildCommit string) *metrics {
	reg := prometheus.NewRegistry()

	m := &metrics{
		registry: reg,

		// http_requests_total is the classic RED-method counter: Rate,
		// Errors, Duration. `status` is the code as a string so a PromQL
		// query can do status=~"5.." for the error rate.
		requests: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total",
				Help: "Total number of HTTP requests, partitioned by method, route and status code.",
			},
			[]string{"method", "path", "status"},
		),

		// Histogram buckets are chosen for a service whose p99 should be in
		// the low milliseconds. The default Prometheus buckets start at 5ms,
		// which is too coarse to see anything here.
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_request_duration_seconds",
				Help:    "HTTP request latency in seconds, partitioned by method and route.",
				Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			},
			[]string{"method", "path"},
		),

		// app_info is the "info metric" pattern: a gauge that is always 1,
		// carrying the interesting data in its labels. Joining on it in
		// PromQL lets a dashboard annotate a latency graph with the exact
		// release that was running, which is how you spot a bad deploy.
		info: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "app_info",
				Help: "Build information for the running binary. Always 1; the data is in the labels.",
			},
			[]string{"version", "commit"},
		),

		inFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "http_requests_in_flight",
				Help: "Number of HTTP requests currently being served.",
			},
		),
	}

	reg.MustRegister(
		m.requests,
		m.duration,
		m.info,
		m.inFlight,
		// Go runtime and process metrics: goroutine count, GC pause, open
		// file descriptors, RSS. These are what you look at first when a pod
		// is OOMKilled or leaking goroutines.
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m.info.WithLabelValues(buildVersion, buildCommit).Set(1)

	return m
}

// -----------------------------------------------------------------------------
// Server
// -----------------------------------------------------------------------------

// server carries everything the handlers need. Passing it as a receiver rather
// than using package-level globals is what makes the handlers testable.
type server struct {
	cfg     config
	log     *slog.Logger
	metrics *metrics
	started time.Time

	// ready gates the readiness probe. atomic.Bool because it is written by
	// the warm-up goroutine and the signal handler, and read concurrently by
	// every kubelet probe.
	ready atomic.Bool

	// shuttingDown makes liveness fail only once we have decided to exit.
	// During normal operation liveness must stay green even if the service is
	// momentarily unable to serve — otherwise the kubelet restarts a pod that
	// readiness had already (correctly) taken out of rotation, turning a
	// recoverable blip into a restart storm.
	shuttingDown atomic.Bool

	// hostname is resolved once at startup; os.Hostname() on every request
	// would be a syscall in the hot path for no benefit. In Kubernetes this
	// is the pod name, which makes it genuinely useful: curl the service a
	// few times and you can see load spreading across replicas.
	hostname string
}

func newServer(cfg config, log *slog.Logger) *server {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}

	return &server{
		cfg:      cfg,
		log:      log,
		metrics:  newMetrics(resolvedVersion(), resolvedCommit()),
		started:  time.Now(),
		hostname: host,
	}
}

// routes builds the HTTP handler tree.
//
// Middleware order is deliberate and reads outside-in:
//
//	withMetrics( withLogging( withRecovery( mux ) ) )
//
// recovery sits *closest to the handler* so that when a handler panics, the
// 500 it produces is still observed by the logging and metrics layers above
// it. If recovery were outermost, a panic would be recorded as whatever
// status the recorder last saw (usually 200), and your error-rate alert would
// never fire for the one failure mode you most want alerting on.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/api/echo", s.handleEcho)

	// promhttp serves the registry we built, not the global one.
	mux.Handle("/metrics", promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{
		// Report scrape errors as HTTP 500 so a broken collector is visible
		// to Prometheus as a failed scrape rather than silently truncated.
		ErrorHandling: promhttp.HTTPErrorOnError,
		Registry:      s.metrics.registry,
	}))

	// ServeMux treats "/" as a catch-all, so handleRoot must reject anything
	// that is not exactly "/" — otherwise /nonsense would return 200 and a
	// typo in an ingress path would look like it worked.
	mux.HandleFunc("/", s.handleRoot)

	return s.withMetrics(s.withLogging(s.withRecovery(mux)))
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

// infoResponse is the payload of the root endpoint. Every field here exists to
// answer a question you ask during an incident: what is running, which commit
// is it, which environment, which pod, and how long has it been up.
type infoResponse struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuildDate   string `json:"buildDate"`
	Environment string `json:"environment"`
	Hostname    string `json:"hostname"`
	UptimeSec   int64  `json:"uptimeSeconds"`
	Ready       bool   `json:"ready"`
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "not found",
			"path":  r.URL.Path,
		})
		return
	}

	writeJSON(w, http.StatusOK, infoResponse{
		Service:     serviceName,
		Version:     resolvedVersion(),
		Commit:      resolvedCommit(),
		BuildDate:   buildDate,
		Environment: s.cfg.Env,
		Hostname:    s.hostname,
		UptimeSec:   int64(time.Since(s.started).Seconds()),
		Ready:       s.ready.Load(),
	})
}

// handleHealthz is the LIVENESS probe.
//
// Liveness answers exactly one question: "is this process wedged beyond
// recovery, such that the only fix is a restart?" It must NOT check
// dependencies. If your liveness probe pings the database, then a database
// outage restarts every pod in the fleet simultaneously — converting a
// degraded service into a total outage, and hammering the recovering database
// with a thundering herd of cold starts.
//
// So: always 200, right up until we have decided to terminate.
func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	if s.shuttingDown.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "shutting down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz is the READINESS probe.
//
// Readiness answers a different question: "should this pod receive traffic
// right now?" A failing readiness probe removes the pod's IP from the Service
// endpoints; it does not restart anything. That makes it the correct place to
// express "warming up" and "draining", which is exactly what we do here.
func (s *server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		reason := "warming up"
		if s.shuttingDown.Load() {
			reason = "draining"
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not ready",
			"reason": reason,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// echoRequest / echoResponse model the POST /api/echo contract.
type echoRequest struct {
	Message string `json:"message"`
}

type echoResponse struct {
	Echo       string `json:"echo"`
	Length     int    `json:"length"`
	ReceivedAt string `json:"receivedAt"`
	ServedBy   string `json:"servedBy"`
}

// handleEcho is a trivial POST endpoint. It exists so the pipeline has a
// non-GET route to exercise, and so there is somewhere to demonstrate request
// body limits and content-type validation.
func (s *server) handleEcho(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method not allowed, use POST",
		})
		return
	}

	// MaxBytesReader is the defensive bit: without it, a client can stream an
	// unbounded body and we will happily buffer all of it. It also makes the
	// server return a 413-shaped error rather than dying on an allocation.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": fmt.Sprintf("body exceeds %d bytes", s.cfg.MaxBodyBytes),
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot read request body"})
		return
	}

	var req echoRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid JSON body, expected {\"message\": \"...\"}",
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, echoResponse{
		Echo:       req.Message,
		Length:     len(req.Message),
		ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ServedBy:   s.hostname,
	})
}

// writeJSON centralises response encoding so every handler emits the same
// content type and so an encoding failure is handled in exactly one place.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Defence in depth: this API is never meant to be rendered in a browser.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The header is already written, so we cannot change the status.
		// All we can usefully do is make the failure visible in the logs.
		slog.Error("failed to encode response body", slog.String("error", err.Error()))
	}
}

// -----------------------------------------------------------------------------
// Middleware
// -----------------------------------------------------------------------------

// statusRecorder captures the status code and response size, which the stock
// http.ResponseWriter does not expose after the fact.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.wroteHeader {
		return
	}
	sr.status = code
	sr.wroteHeader = true
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	// net/http implicitly sends 200 on the first Write if WriteHeader was
	// never called. Mirror that so our recorded status is never zero.
	if !sr.wroteHeader {
		sr.WriteHeader(http.StatusOK)
	}
	n, err := sr.ResponseWriter.Write(b)
	sr.bytes += n
	return n, err
}

// Flush keeps the recorder transparent to handlers that stream (promhttp does
// not, but losing http.Flusher silently is the kind of bug that only shows up
// under load).
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// wrapWriter returns the existing recorder when the writer has already been
// wrapped further up the middleware chain, so the status is recorded once and
// every layer observes the same value.
func wrapWriter(w http.ResponseWriter) *statusRecorder {
	if sr, ok := w.(*statusRecorder); ok {
		return sr
	}
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

// routeLabel maps a request path onto a *bounded* set of label values.
//
// This is the single most important line of defence for a Prometheus setup.
// Labelling by raw r.URL.Path means an attacker — or an overenthusiastic
// scanner — can create an unbounded number of time series by requesting
// /aaaa, /aaab, /aaac... Each unique label combination is a new series held
// in memory forever. That is how you take down your own monitoring stack.
// Mapping to known routes, with everything else collapsed to "other", makes
// cardinality constant.
func routeLabel(path string) string {
	switch {
	case path == "/":
		return "/"
	case path == "/healthz":
		return "/healthz"
	case path == "/readyz":
		return "/readyz"
	case path == "/metrics":
		return "/metrics"
	case strings.HasPrefix(path, "/api/echo"):
		return "/api/echo"
	default:
		return "other"
	}
}

// withMetrics records the RED metrics for every request.
func (s *server) withMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The metrics endpoint itself is excluded from the counters. Including
		// it means every scrape increments a counter, which makes the graph a
		// function of your scrape interval rather than of real traffic.
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		route := routeLabel(r.URL.Path)
		rec := wrapWriter(w)

		s.metrics.inFlight.Inc()
		start := time.Now()

		next.ServeHTTP(rec, r)

		elapsed := time.Since(start).Seconds()
		s.metrics.inFlight.Dec()

		s.metrics.duration.WithLabelValues(r.Method, route).Observe(elapsed)
		s.metrics.requests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
	})
}

// withLogging emits one structured line per request.
//
// Structured (JSON) rather than free text because in Kubernetes these lines go
// to stdout, get collected by the node agent, and end up in Loki or
// Elasticsearch. A JSON object is queryable there; a formatted string needs a
// brittle regex to become queryable.
func (s *server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Probe traffic is high-volume and uninteresting: the kubelet hits
		// /healthz and /readyz every few seconds per pod. Logging it at info
		// drowns the real traffic, so it drops to debug.
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
			level = slog.LevelDebug
		}

		rec := wrapWriter(w)
		start := time.Now()

		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		if rec.status >= http.StatusInternalServerError {
			level = slog.LevelError
		}

		s.log.LogAttrs(r.Context(), level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("route", routeLabel(r.URL.Path)),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("duration", duration),
			slog.String("remote_addr", r.RemoteAddr),
			slog.String("user_agent", r.UserAgent()),
		)
	})
}

// withRecovery turns a panic into a 500 instead of killing the process.
//
// Go's http package already recovers panics per-connection, but it does so
// silently from the client's perspective (the connection is just closed) and
// it does not give us a chance to log structured context or record the error
// in metrics. Doing it ourselves means one bad request degrades one request.
func (s *server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := wrapWriter(w)

		defer func() {
			if rv := recover(); rv != nil {
				// http.ErrAbortHandler is the documented way for a handler to
				// abort a connection on purpose. Swallowing it would hide an
				// intentional abort; re-panic so net/http handles it.
				if rv == http.ErrAbortHandler {
					panic(rv)
				}

				s.log.Error("recovered from panic in handler",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("panic", rv),
				)

				if !rec.wroteHeader {
					writeJSON(rec, http.StatusInternalServerError, map[string]string{
						"error": "internal server error",
					})
				}
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// -----------------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------------

// warmUp flips readiness to true after the configured delay. Running it in a
// goroutine means the HTTP server is already listening and answering liveness
// probes while warm-up happens — if we blocked here instead, a long warm-up
// would look like a failed liveness probe and the kubelet would restart us.
func (s *server) warmUp(ctx context.Context) {
	if s.cfg.ReadyDelay <= 0 {
		s.ready.Store(true)
		s.log.Info("service ready", slog.String("warmup", "skipped"))
		return
	}

	timer := time.NewTimer(s.cfg.ReadyDelay)
	defer timer.Stop()

	select {
	case <-timer.C:
		s.ready.Store(true)
		s.log.Info("service ready", slog.Duration("warmup", s.cfg.ReadyDelay))
	case <-ctx.Done():
		// Shutdown was requested before we ever became ready. Stay not-ready.
		s.log.Warn("shutdown requested during warm-up; never became ready")
	}
}

func main() {
	cfg, warnings := loadConfig()

	// slog with a JSON handler writing to stdout. Container logs are a stream;
	// the platform (fluent-bit / promtail / vector) owns rotation, shipping
	// and retention. A service that writes to a file inside a container with
	// a read-only root filesystem simply crashes — and ours is read-only.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})).With(
		slog.String("service", serviceName),
		slog.String("version", resolvedVersion()),
		slog.String("commit", resolvedCommit()),
		slog.String("env", cfg.Env),
	)
	slog.SetDefault(logger)

	for _, warn := range warnings {
		logger.Warn("configuration warning", slog.String("detail", warn))
	}

	srv := newServer(cfg, logger)

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.routes(),

		// Explicit timeouts. ReadHeaderTimeout in particular is the fix for
		// Slowloris: a client that opens a connection and dribbles out header
		// bytes forever will otherwise hold a goroutine indefinitely.
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,

		// Route the http package's own errors through slog so they are
		// structured like everything else rather than going to the standard
		// logger in plain text.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	// signal.NotifyContext cancels ctx on SIGINT or SIGTERM. Kubernetes sends
	// SIGTERM when a pod is being deleted — during a rolling update, a node
	// drain, an eviction or a scale-down — so this path runs on every single
	// deploy, not just on a crash.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go srv.warmUp(ctx)

	// Run ListenAndServe on its own goroutine so main can block on ctx.
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening",
			slog.String("addr", cfg.Addr),
			slog.String("buildDate", buildDate),
			slog.Duration("readyDelay", cfg.ReadyDelay),
			slog.Duration("preStopDelay", cfg.PreStopDelay),
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		logger.Error("http server failed", slog.String("error", err.Error()))
		os.Exit(1)
	case <-ctx.Done():
		// fall through to the graceful shutdown sequence
	}

	// =========================================================================
	// GRACEFUL SHUTDOWN — the sequence that makes rolling updates lossless
	// =========================================================================
	//
	// When a pod is deleted, two things happen CONCURRENTLY and with no
	// ordering guarantee between them:
	//
	//   (a) the kubelet sends SIGTERM to the container, and
	//   (b) the endpoints/EndpointSlice controller removes the pod's IP from
	//       the Service, which then has to propagate to every kube-proxy (or
	//       every ingress controller, or every service mesh sidecar) in the
	//       cluster.
	//
	// (b) is eventually consistent and takes a few hundred milliseconds to a
	// few seconds. If we react to SIGTERM by closing the listener immediately,
	// we will refuse connections that were routed to us by a kube-proxy whose
	// rules have not caught up yet. The user sees a 502. This is *the* reason
	// "we deployed and saw a blip of errors" happens, and almost every team
	// hits it before they understand it.
	//
	// The fix is a three-phase drain:
	//
	//   1. Flip readiness to FALSE. This is what actively triggers the
	//      endpoints controller to remove us — we are asking to be taken out
	//      of rotation rather than waiting to be noticed.
	//   2. KEEP SERVING for PRESTOP_DELAY. New connections arriving from
	//      stale routing tables are still answered correctly. This is the
	//      "lame duck" period.
	//   3. Only then call Shutdown(), which stops accepting new connections
	//      and waits for in-flight requests to complete.
	//
	// The Helm chart also sets a preStop lifecycle hook with an equivalent
	// sleep, as belt-and-braces for the case where the container cannot
	// handle signals itself (and because the kubelet runs preStop *before*
	// sending SIGTERM, widening the window further).
	//
	// Budget check: terminationGracePeriodSeconds must exceed
	// PRESTOP_DELAY + SHUTDOWN_TIMEOUT, or the kubelet SIGKILLs us mid-drain
	// and we are back where we started. Defaults here are 5s + 15s = 20s,
	// against a grace period of 45s in the chart.
	// =========================================================================

	stop() // restore default signal handling: a second Ctrl-C/SIGTERM kills us
	srv.shuttingDown.Store(true)
	srv.ready.Store(false)

	logger.Info("SIGTERM received: readiness set to false, entering lame-duck period",
		slog.Duration("preStopDelay", cfg.PreStopDelay),
	)

	// Phase 2: keep serving while the data plane converges.
	time.Sleep(cfg.PreStopDelay)

	// Phase 3: stop accepting, drain in-flight.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	logger.Info("draining in-flight requests", slog.Duration("timeout", cfg.ShutdownTimeout))

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		// A deadline here means requests were still running when the budget
		// expired. Worth logging loudly: it usually means a handler is
		// missing a context deadline of its own.
		logger.Error("graceful shutdown did not complete in time",
			slog.String("error", err.Error()),
		)
		if closeErr := httpServer.Close(); closeErr != nil {
			logger.Error("forced close failed", slog.String("error", closeErr.Error()))
		}
		os.Exit(1)
	}

	logger.Info("shutdown complete", slog.Duration("uptime", time.Since(srv.started)))
}
