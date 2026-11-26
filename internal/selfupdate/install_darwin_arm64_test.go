//go:build cgo

package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMacUpdateStagesAndSwapsBundle(t *testing.T) {
	ctx := t.Context()
	run := func(command string, args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, command, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", command, err, output)
		}
	}
	source := t.TempDir()
	app := filepath.Join(source, macBundle)
	contents := filepath.Join(app, "Contents")
	macOS := filepath.Join(contents, "MacOS")
	if err := os.MkdirAll(macOS, 0755); err != nil {
		t.Fatal(err)
	}
	const plist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>ai.yungor.keydock-connect</string>
<key>CFBundleExecutable</key><string>KeyDockConnect</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>0.9.10</string>
<key>CFBundleShortVersionString</key><string>0.9.10</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0644); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macOS, "KeyDockConnect"), binary, 0755); err != nil {
		t.Fatal(err)
	}
	run("/usr/bin/codesign", "--force", "--sign", "-", app)
	if err := verifyDeveloperID(ctx, app, "ABCDE12345"); err == nil {
		t.Fatal("accepted an ad-hoc signature with the expected bundle identifier")
	}
	checkFixtureSignature := func(ctx context.Context, bundle string) error {
		return exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", `-R=identifier "ai.yungor.keydock-connect"`, bundle).Run()
	}
	image := filepath.Join(t.TempDir(), "update.dmg")
	run("/usr/bin/hdiutil", "create", "-quiet", "-volname", "KeyDock Connect Test", "-srcfolder", source, "-format", "UDZO", "-fs", "HFS+", image)
	destination := filepath.Join(t.TempDir(), macBundle)
	if err := os.Mkdir(destination, 0755); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if prepared, err := prepareMacOS(ctx, image, "0.9.11", destination, checkFixtureSignature); err == nil {
		prepared.Discard()
		t.Fatal("accepted a release with the wrong bundle version")
	}
	prepared, err := prepareMacOS(ctx, image, "0.9.10", destination, checkFixtureSignature)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Discard()
	newBundle := filepath.Join(prepared.staging, macBundle)
	stagedPlist := filepath.Join(newBundle, "Contents", "Info.plist")
	if err := os.WriteFile(stagedPlist, []byte(plist+"\n<!-- modified after staging -->"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Install(); err == nil {
		t.Fatal("installed a bundle that changed after preparation")
	}
	current, err := os.Stat(destination)
	if err != nil || !os.SameFile(current, original) {
		t.Fatalf("a failed signature check replaced the installed app: %v", err)
	}
	if err := os.WriteFile(stagedPlist, []byte(plist), 0644); err != nil {
		t.Fatal(err)
	}
	if err := swapMacBundles(newBundle, destination); err != nil {
		t.Fatal(err)
	}
	staged, err := os.Stat(newBundle)
	if err != nil || !os.SameFile(staged, original) {
		t.Fatalf("original bundle not preserved at staged path: %v", err)
	}
	updated, err := os.ReadFile(filepath.Join(destination, "Contents", "Info.plist"))
	if err != nil || string(updated) != plist {
		t.Fatalf("staged bundle not installed at original path: %v", err)
	}
}
