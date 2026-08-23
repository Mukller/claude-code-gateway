package provider

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const gcpScope = "https://www.googleapis.com/auth/cloud-platform"

type saCreds struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type SATokenSource struct {
	mu     sync.Mutex
	creds  saCreds
	key    *rsa.PrivateKey
	token  string
	expiry time.Time
	http   *http.Client
}

func NewSATokenSource(data []byte) (*SATokenSource, error) {
	var creds saCreds
	if err := json.Unmarshal(bytes.TrimSpace(data), &creds); err != nil {
		return nil, fmt.Errorf("service account json: %w", err)
	}
	if creds.ClientEmail == "" || creds.PrivateKey == "" {
		return nil, fmt.Errorf("service account json: client_email/private_key required")
	}
	if creds.TokenURI == "" {
		creds.TokenURI = "https://oauth2.googleapis.com/token"
	}
	pemData := strings.ReplaceAll(creds.PrivateKey, "\\n", "\n")
	var block *pem.Block
	for {
		block, _ = pem.Decode([]byte(pemData))
		if block == nil {
			break
		}
		if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if rk, ok := key.(*rsa.PrivateKey); ok {
				return &SATokenSource{creds: creds, key: rk, http: &http.Client{Timeout: 20 * time.Second}}, nil
			}
			return nil, fmt.Errorf("service account key is %T, want RSA", key)
		}
		if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return &SATokenSource{creds: creds, key: key, http: &http.Client{Timeout: 20 * time.Second}}, nil
		}
		pemData = strings.Join(strings.SplitN(pemData, "-----END", 2)[1:], "")
		if !strings.Contains(pemData, "-----BEGIN") {
			break
		}
		pemData = "-----BEGIN" + pemData
	}
	return nil, fmt.Errorf("service account json: unparsable private key")
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *SATokenSource) signJWT(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss":   s.creds.ClientEmail,
		"scope": gcpScope,
		"aud":   s.creds.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := b64url(header) + "." + b64url(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64url(sig), nil
}

func (s *SATokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.expiry.Add(-60*time.Second)) {
		return s.token, nil
	}
	assertion, err := s.signJWT(time.Now())
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.creds.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gcp token endpoint: %d %s", resp.StatusCode, truncateBytes(body, 300))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("gcp token endpoint: bad response")
	}
	s.token = tr.AccessToken
	exp := tr.ExpiresIn
	if exp <= 0 {
		exp = 3600
	}
	s.expiry = time.Now().Add(time.Duration(exp) * time.Second)
	return s.token, nil
}
