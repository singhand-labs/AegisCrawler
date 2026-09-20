package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func newAuthenticatedTestServerWithKeys(t *testing.T, workerApiKey, adminApiKey, metricsApiKey, swaggerApiKey string) *httptest.Server {
	t.Helper()
	f, err := os.CreateTemp("", "auth-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
		WorkerAPIKey:                   workerApiKey,
		AdminAPIKey:                    adminApiKey,
		MetricsAPIKey:                  metricsApiKey,
		SwaggerAPIKey:                  swaggerApiKey,
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	h := NewHandler(s, sch, nil, nil, cfg, logger)
	metrics := NewMetrics()
	h.SetMetrics(metrics)
	router := NewRouter(h, cfg, logger, metrics)
	return httptest.NewServer(router)
}

func newAuthenticatedTestServer(t *testing.T, workerApiKey, adminApiKey string) *httptest.Server {
	return newAuthenticatedTestServerWithKeys(t, workerApiKey, adminApiKey, "", "")
}

func TestAuthMiddlewareAttachesServerDerivedPrincipals(t *testing.T) {
	tests := []struct {
		name        string
		middleware  func(http.Handler) http.Handler
		wantKind    authz.PrincipalKind
		wantRole    authz.Role
		wantSubject string
	}{
		{
			name:        "admin",
			middleware:  AdminAuthMiddleware(&config.Config{AuditActor: "configured-admin"}, zap.NewNop()),
			wantKind:    authz.PrincipalAdmin,
			wantRole:    authz.RoleAdmin,
			wantSubject: "configured-admin",
		},
		{
			name:        "worker",
			middleware:  AuthMiddleware(&config.Config{}),
			wantKind:    authz.PrincipalWorker,
			wantRole:    authz.RoleWorker,
			wantSubject: "worker-api-key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := tc.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				principal, ok := authz.PrincipalFromContext(r.Context())
				if !ok {
					t.Fatal("expected authenticated principal")
				}
				if principal.Subject != tc.wantSubject || principal.Kind != tc.wantKind || principal.WorkspaceID != authz.DefaultWorkspaceID || !principal.HasRole(tc.wantRole) {
					t.Fatalf("unexpected principal: %+v", principal)
				}
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			handler.ServeHTTP(httptest.NewRecorder(), req)
			if !called {
				t.Fatal("expected wrapped handler to run")
			}
		})
	}
}

func TestWorkerEndpointRejectsMissingAuth(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "secret-key", "")
	defer srv.Close()

	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHumanInterventionEndpointsRequireCorrectCredential(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "worker-secret", "admin-secret")
	defer srv.Close()

	tests := []struct {
		name   string
		method string
		path   string
		token  string
	}{
		{
			name:   "worker request rejects missing worker key",
			method: http.MethodPost,
			path:   "/tasks/task-1/human-interventions",
		},
		{
			name:   "admin list rejects missing admin key",
			method: http.MethodGet,
			path:   "/admin/tasks/task-1/human-interventions",
		},
		{
			name:   "admin decision rejects worker key",
			method: http.MethodPost,
			path:   "/admin/tasks/task-1/human-interventions/intervention-1/decision",
			token:  "worker-secret",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, bytes.NewReader([]byte(`{}`)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", resp.StatusCode)
			}
		})
	}
}

func TestWorkerEndpointRejectsInvalidAuth(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "secret-key", "")
	defer srv.Close()

	req, err := http.NewRequest("POST", srv.URL+"/tasks/claim", bytes.NewReader([]byte(`{"workerId":"worker-1"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestWorkerEndpointAcceptsValidAuth(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "secret-key", "")
	defer srv.Close()

	rule := models.Rule{
		ID:        "test-rule",
		Version:   "1.0.0",
		Name:      "Test",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	task := models.Task{
		ID:          store.NewID(),
		RuleID:      "test-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	req, err := http.NewRequest("POST", srv.URL+"/tasks/claim", bytes.NewReader([]byte(`{"workerId":"worker-1"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-key")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHealthEndpointSkipsAuth(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "secret-key", "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAdminEndpointsSkipsAuth(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "secret-key", "")
	defer srv.Close()

	rule := models.Rule{
		ID:        "admin-rule",
		Version:   "1.0.0",
		Name:      "Admin Test",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAdminEndpointRejectsMissingAuth(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "", "admin-secret")
	defer srv.Close()

	rule := models.Rule{
		ID:        "admin-rule",
		Version:   "1.0.0",
		Name:      "Admin Test",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAdminEndpointRejectsInvalidAuth(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "", "admin-secret")
	defer srv.Close()

	rule := models.Rule{
		ID:        "admin-rule",
		Version:   "1.0.0",
		Name:      "Admin Test",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	req, err := http.NewRequest("POST", srv.URL+"/admin/rules", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAdminEndpointAcceptsValidAuth(t *testing.T) {
	srv := newAuthenticatedTestServer(t, "", "admin-secret")
	defer srv.Close()

	rule := models.Rule{
		ID:        "admin-rule",
		Version:   "1.0.0",
		Name:      "Admin Test",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	req, err := http.NewRequest("POST", srv.URL+"/admin/rules", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer admin-secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMetricsEndpointSkipsAuthWhenKeyEmpty(t *testing.T) {
	srv := newAuthenticatedTestServerWithKeys(t, "", "", "", "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMetricsEndpointRejectsMissingAuth(t *testing.T) {
	srv := newAuthenticatedTestServerWithKeys(t, "", "", "metrics-secret", "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestMetricsEndpointRejectsInvalidAuth(t *testing.T) {
	srv := newAuthenticatedTestServerWithKeys(t, "", "", "metrics-secret", "")
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer wrong-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestMetricsEndpointAcceptsValidAuth(t *testing.T) {
	srv := newAuthenticatedTestServerWithKeys(t, "", "", "metrics-secret", "")
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer metrics-secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSwaggerAuthMiddlewareSkipsAuthWhenKeyEmpty(t *testing.T) {
	cfg := &config.Config{SwaggerAPIKey: ""}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	wrapped := SwaggerAuthMiddleware(cfg)(next)

	req := httptest.NewRequest("GET", "/swagger/", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestSwaggerAuthMiddlewareRejectsMissingAuth(t *testing.T) {
	cfg := &config.Config{SwaggerAPIKey: "swagger-secret"}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	wrapped := SwaggerAuthMiddleware(cfg)(next)

	req := httptest.NewRequest("GET", "/swagger/", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestSwaggerAuthMiddlewareRejectsInvalidAuth(t *testing.T) {
	cfg := &config.Config{SwaggerAPIKey: "swagger-secret"}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	wrapped := SwaggerAuthMiddleware(cfg)(next)

	req := httptest.NewRequest("GET", "/swagger/", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestSwaggerAuthMiddlewareAcceptsValidAuth(t *testing.T) {
	cfg := &config.Config{SwaggerAPIKey: "swagger-secret"}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	wrapped := SwaggerAuthMiddleware(cfg)(next)

	req := httptest.NewRequest("GET", "/swagger/", nil)
	req.Header.Set("Authorization", "Bearer swagger-secret")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}
