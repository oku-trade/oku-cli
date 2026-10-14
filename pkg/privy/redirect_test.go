package privy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshRefusesRedirect(t *testing.T) {
	for _, status := range []int{307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			targetCalls, sourceCalls := 0, 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++; w.WriteHeader(200) }))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceCalls++
				w.Header().Set("Location", target.URL+"/token")
				w.WriteHeader(status)
			}))
			defer source.Close()
			supplied := &http.Client{}
			c := &Client{AuthURL: source.URL, HTTP: supplied}
			_, err := c.Refresh(context.Background(), "secret-refresh")
			if err == nil || sourceCalls != 1 || targetCalls != 0 || supplied.CheckRedirect != nil {
				t.Fatalf("redirect error=%v source=%d target=%d client=%+v", err, sourceCalls, targetCalls, supplied)
			}
		})
	}
}
