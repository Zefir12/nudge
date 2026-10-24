package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhooks(t *testing.T) {
	t.Parallel()
	got := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r
		if r.URL.Query().Get("fail") != "" {
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(target.Close)

	h, svc := newTestHandler(t)
	if _, err := svc.CreateFirstUser(t.Context(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds}))
	call := func(method, path, body string) (int, string) {
		t.Helper()
		rec := do(t, h, request{method: method, path: path, body: body, cookie: cookie})
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}

	steps := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
		// wantBody is the exact body, or with a leading "~" a part of it.
		wantBody string
	}{
		{name: "empty list", method: "GET", path: "/api/v1/webhooks", wantCode: 200, wantBody: "[]"},
		{name: "create", method: "POST", path: "/api/v1/webhooks", wantCode: 201,
			body: `{"name":"chat","url":"` + target.URL + `/hooks/secret-path","header_name":"x-token","header_value":"hunter2","enabled":true}`,
			wantBody: `{"id":1,"name":"chat","endpoint":"` + target.URL + `","header_name":"X-Token","enabled":true,"last_sent_at":null,"last_error":"",` +
				`"created_at":"2023-11-14T22:13:20Z","updated_at":"2023-11-14T22:13:20Z"}`},
		{name: "create taken", method: "POST", path: "/api/v1/webhooks", wantCode: 409,
			body: `{"name":"chat","url":"https://x.example"}`, wantBody: `{"error":"a webhook with that name already exists"}`},
		{name: "create without url", method: "POST", path: "/api/v1/webhooks", wantCode: 400,
			body: `{"name":"x"}`, wantBody: `{"error":"url is required"}`},
		{name: "create bad url", method: "POST", path: "/api/v1/webhooks", wantCode: 400,
			body: `{"name":"x","url":"file:///etc/passwd"}`, wantBody: "~url must be"},
		{name: "create malformed", method: "POST", path: "/api/v1/webhooks", body: `{`, wantCode: 400},
		{name: "get", method: "GET", path: "/api/v1/webhooks/1", wantCode: 200, wantBody: `~"header_name":"X-Token"`},
		{name: "get missing", method: "GET", path: "/api/v1/webhooks/2", wantCode: 404, wantBody: `{"error":"webhook not found"}`},
		{name: "get bad id", method: "GET", path: "/api/v1/webhooks/abc", wantCode: 404},
		{name: "test", method: "POST", path: "/api/v1/webhooks/1/test", wantCode: 200, wantBody: `{"ok":true}`},
		{name: "delivery recorded", method: "GET", path: "/api/v1/webhooks/1", wantCode: 200, wantBody: `~"last_sent_at":"2023-11-14T22:13:20Z","last_error":""`},
		{name: "update keeps secrets", method: "PUT", path: "/api/v1/webhooks/1", wantCode: 200,
			body: `{"name":"chat2","header_name":"X-Token","enabled":false}`, wantBody: `~"name":"chat2","endpoint":"` + target.URL + `","header_name":"X-Token","enabled":false`},
		{name: "test kept secrets", method: "POST", path: "/api/v1/webhooks/1/test", wantCode: 200, wantBody: `{"ok":true}`},
		{name: "secret stays with its host", method: "PUT", path: "/api/v1/webhooks/1", wantCode: 400,
			body: `{"name":"chat2","url":"https://evil.example/","header_name":"X-Token"}`, wantBody: "~header value is required"},
		{name: "update url", method: "PUT", path: "/api/v1/webhooks/1", wantCode: 200,
			body: `{"name":"chat2","url":"` + target.URL + `/?fail=1"}`, wantBody: `~"header_name":""`},
		{name: "test fails", method: "POST", path: "/api/v1/webhooks/1/test", wantCode: 200,
			wantBody: `{"ok":false,"error":"the webhook answered with status 403"}`},
		{name: "failure recorded", method: "GET", path: "/api/v1/webhooks/1", wantCode: 200, wantBody: `~"last_error":"the webhook answered with status 403"`},
		{name: "test missing", method: "POST", path: "/api/v1/webhooks/9/test", wantCode: 404},
		{name: "update missing", method: "PUT", path: "/api/v1/webhooks/9", wantCode: 404, body: `{"name":"x"}`},
		{name: "update invalid", method: "PUT", path: "/api/v1/webhooks/1", wantCode: 400, body: `{"name":""}`},
		{name: "list", method: "GET", path: "/api/v1/webhooks", wantCode: 200, wantBody: `~"name":"chat2"`},
		{name: "delete", method: "DELETE", path: "/api/v1/webhooks/1", wantCode: 204},
		{name: "delete again", method: "DELETE", path: "/api/v1/webhooks/1", wantCode: 404},
	}
	for _, st := range steps {
		code, body := call(st.method, st.path, st.body)
		if code != st.wantCode {
			t.Fatalf("%s: code = %d, want %d (%s)", st.name, code, st.wantCode, body)
		}
		if strings.Contains(body, "hunter2") || strings.Contains(body, "secret-path") || strings.Contains(body, "header_value") {
			t.Errorf("%s: body leaks a secret: %s", st.name, body)
		}
		if part, ok := strings.CutPrefix(st.wantBody, "~"); ok {
			if !strings.Contains(body, part) {
				t.Errorf("%s: body = %s, want it to contain %s", st.name, body, part)
			}
		} else if st.wantBody != "" && body != st.wantBody {
			t.Errorf("%s: body = %s, want %s", st.name, body, st.wantBody)
		}
		if code >= 400 {
			var e struct{ Error string }
			if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error == "" {
				t.Errorf("%s: body = %s, want a JSON error", st.name, body)
			}
		}
		switch st.name {
		case "test", "test kept secrets":
			if r := <-got; r.URL.Path != "/hooks/secret-path" || r.Header.Get("X-Token") != "hunter2" {
				t.Errorf("%s: delivered %s with %v", st.name, r.URL, r.Header)
			}
		case "test fails":
			if r := <-got; r.Header.Get("X-Token") != "" {
				t.Errorf("%s: the dropped header was sent", st.name)
			}
		}
	}
}

func TestWebhooksUnavailable(t *testing.T) {
	t.Parallel()
	h, svc, _ := newTestHandlerOpts(t, nil, func(o *Options) { o.Webhooks = nil })
	if _, err := svc.CreateFirstUser(t.Context(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds}))
	for _, path := range []string{"/api/v1/webhooks", "/api/v1/webhooks/1"} {
		if rec := do(t, h, request{method: "GET", path: path, cookie: cookie}); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s = %d, want 503", path, rec.Code)
		}
	}
}
