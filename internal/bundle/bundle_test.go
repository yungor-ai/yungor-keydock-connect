package bundle

import (
	stdzip "archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	zip "go.foxforensics.eu/go-zip/zip"
)

const testPassword = "synthetic-password-for-tests"
const entryURL = "https://model.example.test/v1"
const subURL = "https://access.example.test/sub/synthetic"
const defaultSecret = "synthetic-default-key"

type member struct{ name, contents string }

func encoded(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func v1Members(t *testing.T) []member {
	return []member{{legacyName, encoded(t, map[string]any{"name": "Test User", "device_name": "Laptop", "api_base_url": entryURL, "api_key": defaultSecret, "clash_subscription_url": subURL})}}
}

func v2Members(t *testing.T) []member {
	refs := []map[string]any{
		{"id": 3, "device_id": 7, "path": "TokenHub/keys/3.json", "name": "secondary", "purpose": "test"},
		{"id": 9, "device_id": 7, "path": "TokenHub/keys/9.json", "name": "primary", "purpose": "test"},
	}
	return []member{
		{manifestName, encoded(t, map[string]any{"schema_version": 2, "delivery_version": 3, "device_id": 7, "codex_default_api_key_id": 9, "api_keys": refs})},
		{accountName, encoded(t, map[string]any{"schema_version": 2, "device_id": 7, "device_name": "Laptop", "api_base_url": entryURL, "clash_subscription_url": subURL})},
		{"TokenHub/keys/3.json", encoded(t, map[string]any{"schema_version": 2, "device_id": 7, "id": 3, "api_key": "synthetic-secondary"})},
		{"TokenHub/keys/9.json", encoded(t, map[string]any{"schema_version": 2, "device_id": 7, "id": 9, "api_key": defaultSecret})},
		{modelName, "model = \"synthetic-model\"\nmodel_provider = \"tokenhub\"\n\n[model_providers.tokenhub]\nwire_api = \"responses\"\n"},
	}
}

// KeyDock uses precisely this writer for the private ZIP. Use the standard
// library for public wrappers, as its delivery package does.
func personal(t *testing.T, members []member, method zip.EncryptionMethod) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for _, m := range members {
		var dest io.Writer
		var err error
		if method == 0 {
			dest, err = w.Create(m.name)
		} else {
			dest, err = w.Encrypt(m.name, testPassword, method)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(dest, m.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
func wrap(t *testing.T, inner []byte) []byte {
	t.Helper()
	var data bytes.Buffer
	w := stdzip.NewWriter(&data)
	for _, m := range []member{{"installers/ignored.exe", "installer contents are never executed"}, {innerName, string(inner)}} {
		entry, err := w.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(entry, m.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestSupportedDeliveries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []member
		outer   bool
		model   string
	}{
		{"legacy", v1Members(t), false, ""}, {"multi-key", v2Members(t), false, "synthetic-model"},
		{"software-outer", v2Members(t), true, "synthetic-model"}, {"no-codex", v2Members(t)[:4], true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := personal(t, tc.members, zip.AES256Encryption)
			if tc.outer {
				data = wrap(t, data)
			}
			got, err := Read(data, testPassword)
			want := Profile{DeviceName: "Laptop", BaseURL: entryURL, Key: defaultSecret, Model: tc.model, SubscriptionURL: subURL}
			if err != nil || got != want {
				t.Fatalf("failed to recover expected synthetic profile, err=%v", err)
			}
		})
	}
}
func TestRejectedDeliveries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]member) []member
	}{
		{"duplicate path", func(m []member) []member { return append(m, m[1]) }},
		{"misplaced key", func(m []member) []member {
			m[3].contents = strings.Replace(m[3].contents, `"device_id":7`, `"device_id":8`, 1)
			return m
		}},
		{"wrong id", func(m []member) []member {
			m[3].contents = strings.Replace(m[3].contents, `"id":9`, `"id":10`, 1)
			return m
		}},
		{"missing key", func(m []member) []member { return append(m[:3], m[4:]...) }},
		{"duplicate key id", func(m []member) []member {
			m[0].contents = strings.Replace(m[0].contents, `"id":3`, `"id":9`, 1)
			return m
		}},
		{"mixed schemas", func(m []member) []member { return append(m, v1Members(t)...) }},
		{"noncanonical key path", func(m []member) []member {
			m[0].contents = strings.Replace(m[0].contents, "TokenHub/keys/9.json", "TokenHub/keys/../keys/9.json", 1)
			return m
		}},
		{"oversize field", func(m []member) []member { m[1].contents = strings.Repeat("a", valueLimit+1); return m }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkReject(t, personal(t, tc.change(v2Members(t)), zip.AES256Encryption), testPassword)
		})
	}
}
func checkReject(t *testing.T, data []byte, password string) {
	t.Helper()
	got, err := Read(data, password)
	if err == nil || got != (Profile{}) {
		t.Fatalf("invalid personal package accepted")
	}
	for _, secret := range []string{testPassword, defaultSecret, subURL} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed private field")
		}
	}
}
func TestRequireAuthenticatedEncryption(t *testing.T) {
	for _, method := range []zip.EncryptionMethod{0, zip.StandardEncryption, zip.AES128Encryption} {
		checkReject(t, personal(t, v1Members(t), method), testPassword)
	}
	data := personal(t, v1Members(t), zip.AES256Encryption)
	checkReject(t, data, "wrong-password")
	checkReject(t, wrap(t, wrap(t, data)), testPassword)
	// Change one byte of the entry's authentication tag, keeping all lengths and headers intact.
	r, err := stdzip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	f := r.File[0]
	offset, err := f.DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	data[offset+int64(f.CompressedSize64)-1] ^= 1
	checkReject(t, data, testPassword)
}
