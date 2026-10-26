package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSleepAuthenticationAndDispatch(t *testing.T) {
	calls := make(chan struct{}, 1)
	h := newHandler("test-secret", func() error { calls <- struct{}{}; return nil })
	cases := []struct {
		method, auth string
		want         int
	}{
		{"GET", "Bearer test-secret", 405},
		{"POST", "", 401},
		{"POST", "test-secret", 401},
		{"POST", "Bearer wrong", 401},
		{"POST", "Bearer test-secret", 200},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, "/sleep", nil)
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %q: got %d want %d", tc.method, tc.auth, w.Code, tc.want)
		}
		if tc.want != 200 {
			select {
			case <-calls:
				t.Fatal("unauthorized request suspended system")
			default:
			}
		}
	}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("authorized suspend not dispatched")
	}
}

func TestEmptyTokenCannotAuthorize(t *testing.T) {
	h := newHandler("", func() error { t.Error("must not suspend"); return nil })
	r := httptest.NewRequest(http.MethodPost, "/sleep", nil)
	r.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestVerifyAuthenticatesWithoutSuspending(t *testing.T) {
	h := newHandler("secret", func() error { t.Error("verify suspended machine"); return nil })
	for _, tc := range []struct {
		method, auth string
		want         int
	}{{"GET", "", 401}, {"GET", "Bearer wrong", 401}, {"GET", "Bearer secret", 200}, {"POST", "Bearer secret", 405}} {
		r := httptest.NewRequest(tc.method, "/verify", nil)
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("verify got %d want %d", w.Code, tc.want)
		}
	}
}
