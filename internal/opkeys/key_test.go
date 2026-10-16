package opkeys_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opkeys"
)

func TestGenerateThenLoadReturnsTheSameKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "op-private-key.pem")

	generated, err := opkeys.Generate()
	if err != nil {
		t.Fatalf("Generate() returned an error: %v", err)
	}
	if err := generated.WriteFile(path); err != nil {
		t.Fatalf("WriteFile() returned an error: %v", err)
	}

	loaded, err := opkeys.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() returned an error: %v", err)
	}
	if loaded.PublicKeyBase64() != generated.PublicKeyBase64() {
		t.Errorf("the loaded key has a different public key: %q, want %q", loaded.PublicKeyBase64(), generated.PublicKeyBase64())
	}
	if loaded.Kid() != generated.Kid() {
		t.Errorf("the loaded key has a different kid: %q, want %q", loaded.Kid(), generated.Kid())
	}
}

// The private key of the Open Payments client is a secret: nobody else on the
// machine should be able to read it.
func TestWriteFileKeepsTheKeyPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "op-private-key.pem")

	key, err := opkeys.Generate()
	if err != nil {
		t.Fatalf("Generate() returned an error: %v", err)
	}
	if err := key.WriteFile(path); err != nil {
		t.Fatalf("WriteFile() returned an error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
}

func TestLoadOrGenerateCreatesTheKeyOnceAndReusesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "op-private-key.pem")

	first, err := opkeys.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("the first LoadOrGenerate() returned an error: %v", err)
	}
	second, err := opkeys.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("the second LoadOrGenerate() returned an error: %v", err)
	}

	if first.PublicKeyBase64() != second.PublicKeyBase64() {
		t.Error("the second call generated a new key instead of reusing the stored one")
	}
}

func TestLoadFileRejectsSomethingThatIsNotAKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-key.pem")
	if err := os.WriteFile(path, []byte("no soy una clave\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	if _, err := opkeys.LoadFile(path); err == nil {
		t.Fatal("LoadFile() accepted a file that is not a key, want an error")
	}
}

// Open Payments carries the public key as unpadded base64url; a 32 byte Ed25519
// key is 43 characters.
func TestPublicKeyBase64IsUnpaddedBase64URL(t *testing.T) {
	key, err := opkeys.Generate()
	if err != nil {
		t.Fatalf("Generate() returned an error: %v", err)
	}

	x := key.PublicKeyBase64()
	if strings.ContainsAny(x, "+/=") {
		t.Errorf("PublicKeyBase64() = %q, want URL-safe base64 without padding", x)
	}
	if len(x) != 43 {
		t.Errorf("PublicKeyBase64() is %d characters, want 43", len(x))
	}
}

// The kid names the key on the wallet address, so it has to be stable for one
// key and different between keys.
func TestKidIsStableAndDistinct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "op-private-key.pem")

	first, err := opkeys.Generate()
	if err != nil {
		t.Fatalf("Generate() returned an error: %v", err)
	}
	if err := first.WriteFile(path); err != nil {
		t.Fatalf("WriteFile() returned an error: %v", err)
	}
	same, err := opkeys.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() returned an error: %v", err)
	}
	other, err := opkeys.Generate()
	if err != nil {
		t.Fatalf("Generate() returned an error: %v", err)
	}

	if first.Kid() != same.Kid() {
		t.Errorf("the stored key produced two kids: %q and %q", first.Kid(), same.Kid())
	}
	if first.Kid() == other.Kid() {
		t.Errorf("two different keys share the kid %q", first.Kid())
	}
	if len(first.Kid()) != 43 {
		t.Errorf("Kid() is %d characters, want 43 (a base64url SHA-256 thumbprint)", len(first.Kid()))
	}
}
