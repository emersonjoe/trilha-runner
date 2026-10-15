package queue

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cloud is a control plane answering one route per test. It refuses any
// call without the worker's bearer token, so every test also proves the
// header is sent.
func cloud(t *testing.T, routes map[string]http.HandlerFunc) Remote {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		handler, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return Remote{BaseURL: server.URL, Token: "tok", Worker: "w1", Project: "app"}
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func TestRemoteBundle(t *testing.T) {
	item := Item{ID: "run-1", Project: "app", TaskID: "TASK-001"}
	valid := `{"version":1,"run_id":"run-1","project":"app","default_branch":"main","specification":{"id":"spec-1","title":"Login","version":2},"round":{"id":"r1","number":1,"stages":[]},"task":{"id":"TASK-001","title":"Form","status":"ready"}}`
	cases := []struct {
		name   string
		status int
		body   string
		want   string // "" is success; otherwise a substring of the error
	}{
		{"valid", http.StatusOK, valid, ""},
		{"run predates bundles", http.StatusNotFound, "", "no work bundle"},
		{"server error", http.StatusInternalServerError, "boom", "500"},
		{"bundle for another task", http.StatusOK, strings.Replace(valid, `"TASK-001"`, `"TASK-002"`, 1), "invalid work bundle"},
		{"bundle for another project", http.StatusOK, strings.Replace(valid, `"project":"app"`, `"project":"other"`, 1), "invalid work bundle"},
		{"unknown version", http.StatusOK, strings.Replace(valid, `"version":1`, `"version":2`, 1), "invalid work bundle"},
		{"not JSON", http.StatusOK, "<html>", "invalid character"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			remote := cloud(t, map[string]http.HandlerFunc{"GET /api/runs/run-1/bundle": reply(c.status, c.body)})
			bundle, err := remote.Bundle(context.Background(), item)
			if c.want == "" {
				if err != nil || bundle.Specification.Title != "Login" || bundle.Task.ID != "TASK-001" {
					t.Fatalf("bundle=%+v err=%v", bundle, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	remote := cloud(t, map[string]http.HandlerFunc{"GET /api/runs/run-1/bundle": reply(http.StatusNotFound, "")})
	if _, err := remote.Bundle(context.Background(), item); !errors.Is(err, ErrNoBundle) {
		t.Fatalf("404 is not ErrNoBundle: %v", err)
	}
}

func TestRemoteProgress(t *testing.T) {
	var got Progress
	remote := cloud(t, map[string]http.HandlerFunc{"POST /api/runs/run-1/progress": func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}})
	sent := Progress{Phase: "consulting_ai", Attempt: 2, MaxAttempts: 3, Branch: "trilha/task-001", Model: "gpt-5"}
	if err := remote.Progress(context.Background(), Item{ID: "run-1"}, sent); err != nil {
		t.Fatal(err)
	}
	if got != sent {
		t.Fatalf("got %+v", got)
	}

	failing := cloud(t, map[string]http.HandlerFunc{"POST /api/runs/run-1/progress": reply(http.StatusBadGateway, "down")})
	if err := failing.Progress(context.Background(), Item{ID: "run-1"}, sent); err == nil || !strings.Contains(err.Error(), "progress") || !strings.Contains(err.Error(), "down") {
		t.Fatalf("err = %v", err)
	}
}

func TestRemoteNextDeployment(t *testing.T) {
	valid := `{"deployment":{"id":"dep-1","project":"app","environment":"prod","profile":"web","action":"deploy","revision":"abc"},"secrets":{"DB_PASSWORD":"s3cret"}}`
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"claimed", http.StatusOK, valid, ""},
		{"empty queue", http.StatusNoContent, "", ErrEmpty.Error()},
		{"server error", http.StatusInternalServerError, "boom", "deployment: 500"},
		{"another project", http.StatusOK, strings.Replace(valid, `"project":"app"`, `"project":"other"`, 1), "invalid deployment work"},
		{"no profile", http.StatusOK, strings.Replace(valid, `"profile":"web"`, `"profile":""`, 1), "invalid deployment work"},
		{"no id", http.StatusOK, strings.Replace(valid, `"id":"dep-1"`, `"id":""`, 1), "invalid deployment work"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var claim map[string]string
			remote := cloud(t, map[string]http.HandlerFunc{"POST /api/deployments/next": func(w http.ResponseWriter, r *http.Request) {
				json.NewDecoder(r.Body).Decode(&claim)
				reply(c.status, c.body)(w, r)
			}})
			work, err := remote.NextDeployment(context.Background())
			if claim["worker"] != "w1" || claim["project"] != "app" {
				t.Fatalf("claim body = %v", claim)
			}
			if c.want == "" {
				if err != nil || work.Deployment.ID != "dep-1" || work.Secrets["DB_PASSWORD"] != "s3cret" {
					t.Fatalf("work=%+v err=%v", work, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestRemoteDoneDeploymentSendsNoSecret(t *testing.T) {
	var raw []byte
	remote := cloud(t, map[string]http.HandlerFunc{"POST /api/deployments/dep-1/result": func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}})
	work := DeploymentWork{Deployment: Deployment{ID: "dep-1", Project: "app", Profile: "web"}, Secrets: map[string]string{"DB_PASSWORD": "s3cret"}}
	if err := remote.DoneDeployment(context.Background(), work, DeploymentResult{Passed: true, Health: "ok", Worker: "spoofed"}); err != nil {
		t.Fatal(err)
	}
	var got DeploymentResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Worker != "w1" || got.Project != "app" || !got.Passed || got.Health != "ok" {
		t.Fatalf("result = %+v", got)
	}
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "secrets") {
		t.Fatalf("secret sent back: %s", raw)
	}

	failing := cloud(t, map[string]http.HandlerFunc{"POST /api/deployments/dep-1/result": reply(http.StatusConflict, "already reported")})
	if err := failing.DoneDeployment(context.Background(), work, DeploymentResult{}); err == nil || !strings.Contains(err.Error(), "already reported") {
		t.Fatalf("err = %v", err)
	}
}
