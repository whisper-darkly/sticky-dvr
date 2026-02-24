//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func baseURL() string {
	if addr := os.Getenv("TEST_ADDR"); addr != "" {
		return addr
	}
	return "http://localhost"
}

func TestHealth(t *testing.T) {
	resp, err := http.Get(baseURL() + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestLogin(t *testing.T) {
	body := `{"username":"admin","password":"admin"}`
	resp, err := http.Post(baseURL()+"/api/auth/login", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /api/auth/login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
		return
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if tok, ok := result["access_token"].(string); !ok || tok == "" {
		t.Error("expected non-empty access_token in response")
	}
}

func TestMeAuthenticated(t *testing.T) {
	tok := adminToken(t)

	req, err := http.NewRequest(http.MethodGet, baseURL()+"/api/me", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
		return
	}
	var user map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if user["username"] != "admin" {
		t.Errorf("expected username=admin, got %v", user["username"])
	}
}

// adminToken logs in as the default admin and returns the access token.
func adminToken(t *testing.T) string {
	t.Helper()
	body := `{"username":"admin","password":"admin"}`
	resp, err := http.Post(baseURL()+"/api/auth/login", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	tok, ok := result["access_token"].(string)
	if !ok || tok == "" {
		t.Fatal("no access_token in login response")
	}
	return tok
}
