package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeStore resolves a reference to placeholder-<ref> and records the
// references it was asked for; the reference "missing" fails.
type fakeStore struct{ asked []string }

func (f *fakeStore) Resolve(_ context.Context, _, ref string) (string, error) {
	f.asked = append(f.asked, ref)
	if ref == "missing" {
		return "", errors.New("no such reference")
	}
	return placeholder + "-" + ref + "\n", nil
}

// TestSecretsComeFromTheStore: several fields resolved by the store in one
// run, beside stdin for one more; every reference asked for once, every value
// in the secrets object with one trailing line break dropped.
func TestSecretsComeFromTheStore(t *testing.T) {
	srcs, err := sources([]string{
		"oak/" + modelKey + "=beekeeper:op://vault/oak/key",
		"hazel/" + modelKey + "=beekeeper:op://vault/hazel/key",
		slackBot + "=beekeeper:k8s://lab/ns/slack/bot",
		slackSign + "=-",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{}
	got, err := readSecrets(context.Background(), srcs, strings.NewReader(placeholder+"-stdin\n"), nil, store)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"oak/" + modelKey:   placeholder + "-op://vault/oak/key",
		"hazel/" + modelKey: placeholder + "-op://vault/hazel/key",
		slackBot:            placeholder + "-k8s://lab/ns/slack/bot",
		slackSign:           placeholder + "-stdin",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d fields, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	if strings.Join(store.asked, " ") != "op://vault/oak/key op://vault/hazel/key k8s://lab/ns/slack/bot" {
		t.Errorf("the store was asked for %v", store.asked)
	}
}

func TestStoreRefusalsNameTheFieldAndTheReference(t *testing.T) {
	if _, err := sources([]string{modelKey + "=beekeeper:"}); err == nil || !strings.Contains(err.Error(), "--secret "+modelKey+": ") {
		t.Errorf("an empty reference: %v", err)
	}
	srcs, err := sources([]string{modelKey + "=beekeeper:missing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readSecrets(context.Background(), srcs, strings.NewReader(""), nil, &fakeStore{}); err == nil || err.Error() != "--secret "+modelKey+": no such reference" {
		t.Errorf("a reference the store cannot resolve: %v", err)
	}
}

// fakeBeekeeper puts a beekeeper on PATH that keeps beekeeper's consumer
// contract: `secret copy <ref> -- <consumer…>` runs the consumer with
// placeholder-<ref> on stdin and answers its output and exit code; the
// reference "missing" fails as beekeeper does, naming it. The consumer is
// this test binary as platformctl (consumerEnv).
func fakeBeekeeper(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
[ "$1 $2 $4" = "secret copy --" ] || { echo "fake beekeeper: unexpected $*" >&2; exit 2; }
ref=$3
shift 4
case "$*" in *" --secret "*"=-"*) ;; *) echo "fake beekeeper: $1 takes no value from beekeeper" >&2; exit 1;; esac
[ "$ref" = missing ] && { echo "op://missing: no such item" >&2; exit 1; }
printf '%s\n' "` + placeholder + `-$ref" | ` + consumerEnv + `=1 "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "beekeeper"), []byte(script), 0o700); err != nil { //nolint:gosec // the test's own script
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestBeekeeperStoreResolvesThroughItsConsumer: the real store against a
// beekeeper keeping the consumer contract hands back each value through the
// receive-secret consumer; a failing reference names beekeeper's reason.
func TestBeekeeperStoreResolvesThroughItsConsumer(t *testing.T) {
	fakeBeekeeper(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := beekeeperStore{bin: "beekeeper", self: self}
	for _, ref := range []string{"op://vault/oak/key", "k8s://lab/ns/slack/bot"} {
		v, err := store.Resolve(context.Background(), modelKey, ref)
		if err != nil {
			t.Fatal(err)
		}
		if v != placeholder+"-"+ref+"\n" {
			t.Errorf("%s: got %q", ref, v)
		}
	}
	_, err = store.Resolve(context.Background(), modelKey, "missing")
	if err == nil || !strings.Contains(err.Error(), "beekeeper secret copy missing") || !strings.Contains(err.Error(), "no such item") {
		t.Errorf("a reference beekeeper cannot resolve: %v", err)
	}
}

// TestWaveCommitTakesSeveralStoreSecrets runs the CLI end to end: a reconcile
// commit over a set with two fields from the store, each through beekeeper's
// consumer, reaches the manager in one call — the wave's answer names both
// fields with their values' lengths — and no value reaches the output.
func TestWaveCommitTakesSeveralStoreSecrets(t *testing.T) {
	fakeBeekeeper(t)
	oak, hazel := "op://vault/oak/key", "op://vault/hazel/key"
	code, out, errs := bridge(t, "connected", installationCmd, "reconcile", "oak", "hazel", agentPlatform, "--commit", "--reason", "r",
		"--secret", "oak/"+modelKey+"=beekeeper:"+oak, "--secret", "hazel/"+modelKey+"=beekeeper:"+hazel)
	if code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	want := "secrets: hazel/" + modelKey + " (" + strconv.Itoa(len(placeholder+"-"+hazel)) + " bytes), oak/" + modelKey + " (" + strconv.Itoa(len(placeholder+"-"+oak)) + " bytes)"
	if !strings.Contains(out, want) {
		t.Errorf("the wave was not sent both values; want %q in\n%s", want, out)
	}
	if strings.Contains(out+errs, placeholder) {
		t.Errorf("the output carries a value:\n%s%s", out, errs)
	}
}

// TestReceiveSecretIsHidden: beekeeper's consumer is plumbing, not a command
// in the usage or the help.
func TestReceiveSecretIsHidden(t *testing.T) {
	var stdout, stderr strings.Builder
	run([]string{"--help"}, &stdout, &stderr)
	if strings.Contains(stdout.String()+stderr.String(), receiveSecretCmd) {
		t.Errorf("the help lists %s", receiveSecretCmd)
	}
	if code := run([]string{receiveSecretCmd, "--socket", filepath.Join(t.TempDir(), "s"), "--secret", modelKey}, &stdout, &stderr); code != exitUsage {
		t.Errorf("--secret without =-: exit %d", code)
	}
}
