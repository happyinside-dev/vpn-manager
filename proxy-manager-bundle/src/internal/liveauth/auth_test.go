package liveauth

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialChangesDoNotAffectOtherUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	h := Handler(path)
	replace := func(data string) {
		t.Helper()
		if err := os.WriteFile(path+".tmp", []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			t.Fatal(err)
		}
	}
	allowed := func(auth string) bool {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"auth": auth})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/auth", strings.NewReader(string(b))))
		var result struct {
			OK bool
			ID string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
			t.Fatalf("response: %d %s", w.Code, w.Body.String())
		}
		if result.OK && result.ID != strings.SplitN(auth, ":", 2)[0] {
			t.Fatal("wrong accounting identity")
		}
		return result.OK
	}
	replace(`{"alice":"old","bob":"unchanged"}`)
	if !allowed("alice:old") || !allowed("bob:unchanged") {
		t.Fatal("initial auth failed")
	}
	replace(`{"alice":"new","bob":"unchanged","charlie":"added"}`)
	if allowed("alice:old") || !allowed("alice:new") || !allowed("bob:unchanged") || !allowed("charlie:added") {
		t.Fatal("rotation/add affected wrong users")
	}
	replace(`{"bob":"unchanged"}`)
	if allowed("alice:new") || !allowed("bob:unchanged") {
		t.Fatal("disable/delete affected wrong users")
	}
	replace(`invalid`)
	if allowed("bob:unchanged") {
		t.Fatal("invalid snapshot must fail closed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if allowed("bob:unchanged") {
		t.Fatal("missing snapshot must fail closed")
	}
}

func TestMalformedAndOversizedRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte(`{"alice":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	h := Handler(path)
	for _, body := range []string{`{`, `{"auth":"alice"}`, `{"auth":"alice:"}`, `{"auth":"unknown:secret"}`, `{"auth":"alice:wrong"}`, `{"auth":"` + strings.Repeat("a", 5000) + `"}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/auth", strings.NewReader(body)))
		if strings.Contains(w.Body.String(), `"ok":true`) {
			t.Fatal("invalid request accepted")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/auth", nil))
	if w.Code != 405 {
		t.Fatalf("method: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 {
		t.Fatalf("health: %d", w.Code)
	}
}
