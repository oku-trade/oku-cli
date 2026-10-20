package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBridgeDestinationFillIsInformationalOnAPIOutage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("originChainId") != "42161" || r.URL.Query().Get("depositTxHash") != "0xabc" {
			t.Errorf("incorrect Across query: %s", r.URL)
		}
		fmt.Fprint(w, `{"status":"filled","fillTx":"0xdef"}`)
	}))
	defer server.Close()
	app := &App{Getenv: func(key string) string {
		if key == "OKU_ACROSS_API_URL" {
			return server.URL
		}
		return ""
	}}
	fill := app.acrossFillStatus(context.Background(), 42161, "0xabc")
	if fill.Status != "filled" || fill.FillTx != "0xdef" {
		t.Fatalf("fill: %+v", fill)
	}
	server.Close()
	fill = app.acrossFillStatus(context.Background(), 42161, "0xabc")
	if fill.Status != "unknown" {
		t.Fatalf("unreachable Across API should be informational unknown, got %+v", fill)
	}
}
