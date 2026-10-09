package dexsplit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// call is one command a fake Runner saw.
type call struct {
	dir, stdin string
	argv       []string
}

func recorder(answer func(argv []string) ([]byte, error)) (Runner, *[]call) {
	var calls []call
	return func(_ context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, error) {
		argv := append([]string{name}, args...)
		calls = append(calls, call{dir: dir, stdin: string(stdin), argv: argv})
		return answer(argv)
	}, &calls
}

// exitError is a real *exec.ExitError with code 1 and stderr.
func exitError(t *testing.T, stderr string) error {
	t.Helper()
	err := exec.Command("false").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Skip("no false(1)")
	}
	ee.Stderr = []byte(stderr)
	return ee
}

func TestSOPSVaultCopySecret(t *testing.T) {
	repo := t.TempDir()
	_ = os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte("creation_rules: []\n"), 0o600)
	run, calls := recorder(func(argv []string) ([]byte, error) {
		if argv[1] == "decrypt" {
			return []byte("oidc:\n  extraStaticClients:\n    - id: kagent\n      secret: the-value\n"), nil
		}
		return nil, nil
	})
	v, _ := NewVault(VaultSOPS, run)
	src := Ref{File: filepath.Join(repo, "installations/x/apps/dex-app/secret-values.yaml.patch"), Path: "oidc.extraStaticClients.0.secret"}
	dst := filepath.Join(repo, "management-clusters/x/extras/dex/dex-client-kagent-secret.yaml")
	if err := v.CopySecret(context.Background(), src, dst, "dex-client-kagent", "giantswarm", "secret"); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[1]
	want := "sops encrypt --input-type yaml --output-type yaml --filename-override management-clusters/x/extras/dex/dex-client-kagent-secret.yaml --output management-clusters/x/extras/dex/dex-client-kagent-secret.yaml"
	if strings.Join(c.argv, " ") != want || c.dir != repo {
		t.Errorf("encrypt %q in %s, want %q in %s", strings.Join(c.argv, " "), c.dir, want, repo)
	}
	for _, s := range []string{"name: dex-client-kagent", "namespace: giantswarm", "secret: the-value", "kind: Secret"} {
		if !strings.Contains(c.stdin, s) {
			t.Errorf("the manifest lacks %q:\n%s", s, c.stdin)
		}
	}
	if strings.Contains(strings.Join(c.argv, " "), "the-value") {
		t.Error("the value is an argument")
	}
}

func TestSOPSVaultUnsetAndReveal(t *testing.T) {
	run, calls := recorder(func(argv []string) ([]byte, error) {
		return []byte("oidc:\n  extraStaticClients:\n    - id: kagent\n      secret: the-value\n"), nil
	})
	v, _ := NewVault(VaultSOPS, run)
	if err := v.Unset(context.Background(), "/r/p.yaml.patch", []string{"oidc.extraStaticClients", "oidc.staticClients.muster"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join((*calls)[1].argv, " "); got != `sops unset --input-type yaml --output-type yaml p.yaml.patch ["oidc"]["staticClients"]["muster"]` {
		t.Errorf("unset %s", got)
	}
	got, err := v.Reveal(context.Background(), "/r/p.yaml.patch", []string{"oidc.extraStaticClients.0.id"})
	if err != nil || got["oidc.extraStaticClients.0.id"] != kagent || len(got) != 1 {
		t.Errorf("reveal %v, %v", got, err)
	}
}

func TestSOPSVaultErrorNamesNoValue(t *testing.T) {
	run, _ := recorder(func([]string) ([]byte, error) {
		return nil, exitError(t, "some noise\nError: no identity matched any of the recipients")
	})
	v, _ := NewVault(VaultSOPS, run)
	_, err := v.Reveal(context.Background(), "/r/p.yaml", []string{"a"})
	if err == nil || !strings.HasSuffix(err.Error(), "Error: no identity matched any of the recipients") {
		t.Fatalf("err %v", err)
	}
}

func TestBeekeeperVault(t *testing.T) {
	compare := 0
	run, calls := recorder(func(argv []string) ([]byte, error) {
		if argv[2] == "compare" {
			compare++
			if compare == 2 {
				return nil, exitError(t, "")
			}
		}
		return nil, nil
	})
	v, _ := NewVault(VaultBeekeeper, run)
	src := Ref{File: "/c/p.yaml.patch", Path: "oidc.extraStaticClients.2.secret"}
	if err := v.CopySecret(context.Background(), src, "/m/dex-client-kagent-secret.yaml", "dex-client-kagent", "giantswarm", "secret"); err != nil {
		t.Fatal(err)
	}
	want := "beekeeper secret copy /c/p.yaml.patch#oidc.extraStaticClients.2.secret=secret /m/dex-client-kagent-secret.yaml --name dex-client-kagent --namespace giantswarm"
	if got := strings.Join((*calls)[0].argv, " "); got != want {
		t.Errorf("copy %q, want %q", got, want)
	}
	if eq, err := v.Equal(context.Background(), src, src); !eq || err != nil {
		t.Errorf("equal %v %v", eq, err)
	}
	if eq, err := v.Equal(context.Background(), src, src); eq || err != nil {
		t.Errorf("different %v %v", eq, err)
	}
	if _, err := v.Reveal(context.Background(), "f", nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("reveal %v", err)
	}
	if err := v.Unset(context.Background(), "f", nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("unset %v", err)
	}
}
