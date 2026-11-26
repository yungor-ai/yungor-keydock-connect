package codex

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func testStore(t *testing.T) storage {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "KeyDock Connect.exe")
	return storage{
		func(data []byte) ([]byte, error) {
			nonce := make([]byte, aead.NonceSize())
			if _, err := rand.Read(nonce); err != nil {
				return nil, err
			}
			return aead.Seal(nonce, nonce, data, nil), nil
		},
		func(data []byte) ([]byte, error) {
			if len(data) < aead.NonceSize() {
				return nil, errors.New("invalid test ciphertext")
			}
			nonce := data[:aead.NonceSize()]
			return aead.Open(nil, nonce, data[len(nonce):], nil)
		},
		func() (string, error) { return exe, nil },
	}
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const gateway = "https://gateway.example.test/v1"

func TestConfigLifecycle(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	path := filepath.Join(home, "config.toml")
	const previous = "# 原有配置\r\nmodel = 'old-model' # keep\r\n\"model_provider\" = 'elsewhere'\r\n[model_providers.elsewhere]\r\nbase_url = 'https://elsewhere.example.test'\r\n[plugins.special]\r\nenabled = true\r\n"
	mustWrite(t, path, previous)
	authPath := filepath.Join(home, "auth.json")
	mustWrite(t, authPath, "unchanged")
	for i, token := range []string{"sample-key-a", "sample-key-b"} {
		model := []string{"model-a", "model-b"}[i]
		if err := store.apply(home, dir, gateway, model, token); err != nil {
			t.Fatal(err)
		}
		cfg := read(t, path)
		for _, expected := range []string{"# 原有配置\r\n", "# keep\r\n", "[plugins.special]\r\nenabled = true", "[model_providers.elsewhere]", "--credential"} {
			if !strings.Contains(cfg, expected) {
				t.Errorf("missing original or managed setting %q", expected)
			}
		}
		if strings.Contains(cfg, token) || strings.Contains(cfg, "env_key") {
			t.Fatal("config leaked a credential")
		}
		var values struct {
			Model     string
			Provider  string `toml:"model_provider"`
			Providers map[string]struct {
				Base string `toml:"base_url"`
				Wire string `toml:"wire_api"`
				Auth struct {
					Command string
					Args    []string
				}
			} `toml:"model_providers"`
		}
		if err := toml.Unmarshal([]byte(cfg), &values); err != nil {
			t.Fatal(err)
		}
		got := values.Providers[providerID]
		exe, _ := store.executable()
		if values.Provider != providerID || values.Model != model || got.Base != gateway || got.Wire != "responses" || got.Auth.Command != exe || len(got.Auth.Args) != 1 || got.Auth.Args[0] != "--credential" {
			t.Fatal("managed provider settings incorrect")
		}
		key, err := store.credential(dir)
		if err != nil || key != token || !store.configured(home, dir) {
			t.Fatal("selected provider or helper credential incorrect")
		}
		encrypted := read(t, filepath.Join(dir, stateName))
		if strings.Contains(encrypted, token) || strings.Contains(encrypted, "old-model") {
			t.Fatal("unencrypted sensitive state")
		}
	}
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != previous || read(t, authPath) != "unchanged" {
		t.Fatal("round-trip lost original bytes")
	}
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.credential(dir); err == nil || store.configured(home, dir) {
		t.Fatal("credential remained after restore")
	}
}

func TestNewInstallAndExternalEdit(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := store.apply(home, dir, gateway, "model-a", "sample-key"); err != nil {
		t.Fatal(err)
	}
	managed := read(t, path)
	overlap := strings.Replace(managed, gateway, "https://different.example.test/v1", 1) + "\n# user change\n"
	mustWrite(t, path, overlap)
	for _, err := range []error{store.apply(home, dir, gateway, "other", "other-key"), store.restore(home, dir)} {
		if !errors.Is(err, conflict) {
			t.Fatal("expected edit protection", err)
		}
	}
	if got := read(t, path); !strings.HasSuffix(got, "# user change\n") {
		t.Fatal("external change overwritten")
	}
	if key, err := store.credential(dir); err != nil || key != "sample-key" {
		t.Fatal("unrelated edit broke credential helper")
	}
	mustWrite(t, path, managed)
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected absent original")
	}
}

func TestInlineProviderAndNameCollision(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	path := filepath.Join(home, "config.toml")
	original := "model_providers = { other = { name = 'legacy' } } # original\n"
	mustWrite(t, path, original)
	if err := store.apply(home, dir, gateway, "test", "sample-key"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), "other = { name = 'legacy' }") {
		t.Fatal("lost existing inline provider")
	}
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
	collision := "[model_providers.keydock-connect]\nname = 'user'\n"
	mustWrite(t, path, collision)
	if err := store.apply(home, dir, gateway, "test", "sample-key"); err == nil || read(t, path) != collision {
		t.Fatal("foreign provider overwritten")
	}
}

func TestRejectBadInputsAndSecureWriteFailure(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	for _, url := range []string{"http://gateway.example.test", "https://user@host", "https://example.test?token=secret", "https://example.test#", "https://"} {
		if err := store.apply(home, dir, url, "test", "sample"); err == nil {
			t.Fatal("accepted invalid URL")
		}
	}
	if err := store.apply(home, dir, gateway, "model", "first\nsecond"); err == nil {
		t.Fatal("accepted multiline credential")
	}
	store.seal = func([]byte) ([]byte, error) { return nil, errors.New("unavailable") }
	if err := store.apply(home, dir, gateway, "test", "sample"); err == nil {
		t.Fatal("expected encryption failure")
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("partially applied on secure write failure")
	}
}

func TestRecoverAfterFinalStateWriteFails(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	originalSeal := store.seal
	count := 0
	store.seal = func(data []byte) ([]byte, error) {
		count++
		if count == 2 {
			return nil, errors.New("synthetic failure")
		}
		return originalSeal(data)
	}
	if err := store.apply(home, dir, gateway, "test", "sample"); err == nil {
		t.Fatal("expected final state write to fail")
	}
	key, err := store.credential(dir)
	if key != "sample" || err != nil {
		t.Fatal("interrupted write was not recovered", err)
	}
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentSetup(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	var w sync.WaitGroup
	errs := make(chan error, 5)
	for _, candidate := range []string{"aa", "bb", "cc", "dd", "ee"} {
		w.Add(1)
		go func(k string) { defer w.Done(); errs <- store.apply(home, dir, gateway, k, k) }(candidate)
	}
	w.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	model, err := store.credential(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := read(t, filepath.Join(home, "config.toml"))
	if !bytes.Contains([]byte(raw), []byte("model = "+quoted(model))) {
		t.Fatal("model/key mismatch after concurrent writes")
	}
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
}

func TestThreeWayRestoreAndReconfigure(t *testing.T) {
	for _, reapply := range []bool{false, true} {
		store, home, dir := testStore(t), t.TempDir(), t.TempDir()
		path := filepath.Join(home, "config.toml")
		original := "model = 'previous' # Original selection\n[features]\nfoo = false\n"
		mustWrite(t, path, original)
		if err := store.apply(home, dir, gateway, "first", "token-a"); err != nil {
			t.Fatal(err)
		}
		changed := strings.Replace(read(t, path), "foo = false", "foo = true # modified by user", 1)
		extra := "\n# trusted by Codex\n[projects.'/new work']\ntrust_level = 'trusted'\n"
		mustWrite(t, path, changed+extra)
		if !store.matches(home, dir, gateway, "first", "token-a") || store.matches(home, dir, gateway, "first", "token-b") {
			t.Fatal("profile status after trust edit")
		}
		if reapply {
			if err := store.apply(home, dir, gateway, "second", "token-b"); err != nil {
				t.Fatal(err)
			}
			if !store.matches(home, dir, gateway, "second", "token-b") || store.matches(home, dir, gateway, "first", "token-a") {
				t.Fatal("stale profile selected")
			}
		}
		if err := store.restore(home, dir); err != nil {
			t.Fatal(err)
		}
		restored := read(t, path)
		for _, want := range []string{"model = 'previous' # Original selection", "foo = true # modified by user", extra} {
			if !strings.Contains(restored, want) {
				t.Fatalf("lost unrelated edit or previous selection, got %q", restored)
			}
		}
		if strings.Contains(restored, providerID) || strings.Contains(restored, "model_provider") {
			t.Fatal("managed settings remain")
		}
	}
}

func TestRelocatedCredentialHelperNeedsResync(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	if err := store.apply(home, dir, gateway, "model", "test-key"); err != nil {
		t.Fatal(err)
	}
	store.executable = func() (string, error) { return filepath.Join(home, "Moved KeyDock Connect"), nil }
	if store.matches(home, dir, gateway, "model", "test-key") || !store.configured(home, dir) {
		t.Fatal("moving the helper should require resync while keeping restore available")
	}
	if err := store.apply(home, dir, gateway, "model", "test-key"); err != nil {
		t.Fatal(err)
	}
	if !store.matches(home, dir, gateway, "model", "test-key") {
		t.Fatal("resync did not update the credential helper path")
	}
}

func TestKeepNewTrustAfterFirstInstall(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := store.apply(home, dir, gateway, "foo", "sample"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, read(t, path)+"\n[projects.'new']\ntrust_level = 'trusted'\n")
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
	doc, err := parseSettings([]byte(read(t, path)))
	if err != nil || len(doc) != 1 || doc["projects"] == nil {
		t.Fatal("new user settings did not survive restoration")
	}
}

func TestKeepEditedInlineOtherProvider(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	path := filepath.Join(home, "config.toml")
	original := "model_providers = { other = { name = 'legacy' } }\n"
	mustWrite(t, path, original)
	if err := store.apply(home, dir, gateway, "foo", "sample"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, strings.Replace(read(t, path), "legacy", "changed", 1))
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), "other = { name = 'changed' }") || strings.Contains(read(t, path), providerID) {
		t.Fatal("foreign inline provider lost")
	}
}

func TestEditDuringPrepareCanBeRetried(t *testing.T) {
	store, home, dir := testStore(t), t.TempDir(), t.TempDir()
	path := filepath.Join(home, "config.toml")
	const latest = "# a different editor saved this before our commit\n"
	protect := store.seal
	edited := false
	store.seal = func(data []byte) ([]byte, error) {
		if !edited {
			edited = true
			mustWrite(t, path, latest)
		}
		return protect(data)
	}
	if err := store.apply(home, dir, gateway, "first", "key"); !errors.Is(err, conflict) {
		t.Fatalf("expected concurrent change detection: %v", err)
	}
	if got := read(t, path); got != latest {
		t.Fatal("concurrent edit overwritten")
	}
	if err := store.apply(home, dir, gateway, "first", "key"); err != nil {
		t.Fatal(err)
	}
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != latest {
		t.Fatal("retry lost previous edit")
	}
}
