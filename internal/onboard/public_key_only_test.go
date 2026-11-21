package onboard

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The private key can live with the vehicle-command proxy only (docker
// compose mounts it into ./proxy; a cluster can mount it into the proxy's
// container alone), leaving the wizard just the public key. That public key is
// the one Tesla has registered and the cars trust, so it counts as an existing
// keypair: generating must not replace it without the confirmation.
const importedPublicKey = "-----BEGIN PUBLIC KEY-----\nimported\n-----END PUBLIC KEY-----\n"

func newPublicKeyOnlyServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, dir := newTestServer(t)
	if err := os.WriteFile(filepath.Join(dir, "public-key.pem"), []byte(importedPublicKey), 0o644); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestGenerateRefusesToReplaceAPublicKeyOnly(t *testing.T) {
	s, dir := newPublicKeyOnlyServer(t)
	w := httptest.NewRecorder()
	s.generate(w, loopbackReq("POST", "/generate", nil))
	got, err := os.ReadFile(filepath.Join(dir, "public-key.pem"))
	if err != nil || !bytes.Equal(got, []byte(importedPublicKey)) {
		t.Fatalf("public key replaced without confirmation (err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "private-key.pem")); err == nil {
		t.Error("a private key was generated without confirmation")
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Error("no refusal shown")
	}
}

func TestPublicKeyOnlyShowsKeysAsPresent(t *testing.T) {
	s, _ := newPublicKeyOnlyServer(t)
	w := httptest.NewRecorder()
	s.page(w, loopbackReq("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "Regenerate") {
		t.Error("step 2 offers a first keypair although a public key exists")
	}
}
