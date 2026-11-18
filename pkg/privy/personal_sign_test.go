package privy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPersonalSign(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const message = "Read the rules.\nI consent 📝"
	rs := "0x" + strings.Repeat("ab", 64)
	for _, tc := range []struct {
		name, signature, wantErr string
		wantV                    byte
	}{
		{name: "normalize zero", signature: rs + "00", wantV: 27},
		{name: "normalize one", signature: rs + "01", wantV: 28},
		{name: "short signature", signature: rs, wantErr: "invalid personal_sign signature"},
		{name: "bad hex", signature: rs + "zz", wantErr: "invalid personal_sign signature"},
		{name: "bad recovery ID", signature: rs + "02", wantErr: "invalid personal_sign recovery ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Method string `json:"method"`
					Params struct {
						Message  string `json:"message"`
						Encoding string `json:"encoding"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if body.Method != "personal_sign" || body.Params.Message != message || body.Params.Encoding != "utf-8" {
					t.Errorf("unexpected RPC payload: %+v", body)
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"signature": tc.signature}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			s := &Session{authKey: key, expiresAt: time.Now().Add(time.Hour), client: &Client{AuthURL: server.URL, AppID: "app"}}
			sig, err := s.PersonalSign(context.Background(), "wallet", message)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr || sig != [65]byte{} {
					t.Fatalf("expected %q and no signature, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || sig[64] != tc.wantV {
				t.Fatalf("v=%d, err=%v, want v=%d", sig[64], err, tc.wantV)
			}
		})
	}
}
