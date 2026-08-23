package provider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func generateSA(t *testing.T) (saJSON []byte, pub *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	sa := map[string]string{
		"client_email": "svc@test.iam.gserviceaccount.com",
		"private_key":  string(pemKey),
	}
	data, _ := json.Marshal(sa)
	return data, &key.PublicKey
}

func TestSATokenSourceFlow(t *testing.T) {
	saJSON, pub := generateSA(t)

	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Fatalf("grant_type = %q", got)
		}
		assertion := r.Form.Get("assertion")
		parts := strings.Split(assertion, ".")
		if len(parts) != 3 {
			t.Fatalf("assertion parts = %d", len(parts))
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Fatalf("sig decode: %v", err)
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
			t.Fatalf("signature verification failed: %v", err)
		}
		var claims map[string]any
		cl, _ := base64.RawURLEncoding.DecodeString(parts[1])
		json.Unmarshal(cl, &claims)
		if claims["iss"] != "svc@test.iam.gserviceaccount.com" {
			t.Fatalf("iss = %v", claims["iss"])
		}
		if claims["scope"] != gcpScope {
			t.Fatalf("scope = %v", claims["scope"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"ya29.test-token","expires_in":3600}`))
	}))
	defer ts.Close()

	var saMap map[string]string
	if err := json.Unmarshal(saJSON, &saMap); err != nil {
		t.Fatal(err)
	}
	saMap["token_uri"] = ts.URL
	final, _ := json.Marshal(saMap)

	src, err := NewSATokenSource(final)
	if err != nil {
		t.Fatal(err)
	}

	tok1, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok1 != "ya29.test-token" {
		t.Fatalf("token = %q", tok1)
	}
	tok2, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok2 != tok1 || hits != 1 {
		t.Fatalf("expected cached token (hits=%d)", hits)
	}
}

func TestNewSATokenSourceRejectsGarbage(t *testing.T) {
	if _, err := NewSATokenSource([]byte(`{"client_email":"x"}`)); err == nil {
		t.Fatal("expected error for missing private key")
	}
	bad := map[string]string{"client_email": "x", "private_key": "not-a-pem"}
	b, _ := json.Marshal(bad)
	if _, err := NewSATokenSource(b); err == nil {
		t.Fatal("expected error for bad pem")
	}
}
