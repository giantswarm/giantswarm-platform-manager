package plan

import "testing"

// The render of a Secret and the file on record as SOPS wrote it: the values
// under stringData encrypted, a comment encrypted, SOPS's block, its own
// indentation.
const (
	renderedFile = `# Rendered by giantswarm-platform-manager. Do not edit by hand.
apiVersion: v1
kind: Secret
metadata:
  name: muster-oauth-credentials
  namespace: agent-platform
  labels:
    application.giantswarm.io/team: bumblebee
type: Opaque
stringData:
  # the client's secret
  dex-client-secret: GENERATED(x-client)
  registration-token: GENERATED(x-token)
  slack-token: SUPPLIED(slack.token)
  client-id: muster
  ttl: 3
`
	fileOnRecord = `# Rendered by giantswarm-platform-manager. Do not edit by hand.
apiVersion: v1
kind: Secret
metadata:
    name: muster-oauth-credentials
    namespace: agent-platform
    labels:
        application.giantswarm.io/team: bumblebee
type: Opaque
stringData:
    #ENC[AES256_GCM,data:I2DzpZeN3yu0NcFBTBTKUife,iv:Irf80wj5V2NWlE3swytxHXUDa1KfIKVKTeOsF1AdlsA=,tag:U956/pjMocDHfYyaKssnAw==,type:comment]
    dex-client-secret: ENC[AES256_GCM,data:ZnYZwa8SADfM3pu3MhBifNDRvi49xAAjRs910Isnr9GQkQmMGKZ/SzYleeQ=,iv:LLmDjCiyTN9b70kZYIQBMu0J0EmtvqhxXSpydiJjYAQ=,tag:ozSXt5pRg04kf8VOndIZ2g==,type:str]
    registration-token: ENC[AES256_GCM,data:OlRRwRRzaKHH2VnBdsdBRofnCg5OYWATya3uTGDlz7+JTgEd9EDBokYx4mA=,iv:RoDe/urNguTTqYF3kym1MPLCTxgQ4xcQ0zxCXlu4sHo=,tag:gJ9j8yGFdlhYLlQtvYXbyA==,type:str]
    slack-token: ENC[AES256_GCM,data:ZnYZwa8SADfM3pu3MhBifNDRvi49xAAjRs910Isnr9GQkQmMGKZ/SzYleeQ=,iv:LLmDjCiyTN9b70kZYIQBMu0J0EmtvqhxXSpydiJjYAQ=,tag:ozSXt5pRg04kf8VOndIZ2g==,type:str]
    client-id: ENC[AES256_GCM,data:dg==,iv:Fcuva9vaandcVM0b53mjmYs7CpKzQxpB2dZxSH5f5l0=,tag:YmkXSIF6LaTU31PO7bFSQw==,type:str]
    ttl: ENC[AES256_GCM,data:dg==,iv:Fcuva9vaandcVM0b53mjmYs7CpKzQxpB2dZxSH5f5l0=,tag:YmkXSIF6LaTU31PO7bFSQw==,type:int]
sops:
    age:
        - enc: |
            -----BEGIN AGE ENCRYPTED FILE-----
            YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0+IFgyNTUxOSB2cmc5VnNMNy9kK0FtUVNY
            -----END AGE ENCRYPTED FILE-----
          recipient: age1hxsn6aume4v68nu7sj44emjz9mgkgcefanwhelylz4tdpgkswdpqld4tax
    encrypted_regex: ^(data|stringData)$
    lastmodified: "2026-09-19T22:12:29Z"
    mac: ENC[AES256_GCM,data:LbJlyoP8NLwC+cbfbTLEtHdeam5HKkVDuxbJNjzQy+CqCcpYuYVWof7IPkKwMLlYKFapxoaSyyqFqhwQCHj7lxATNC41I/wrPhrKF3O+BctEVv+kujsshqSnUrLxC1pcRA4eYu4zxzf8G2vRK1hhvVKjvAxZAXAOxw4BRIjhlI4=,iv:0qRS8ROt0nsw2f1ORg34wdGuMzjS7L2v9sGUSgdQyWg=,tag:OR9VIS4Mq7uqpDacZ1+VqA==,type:str]
    version: 3.13.3
`
)

// The file on record is the render's outside the encrypted values: SOPS's
// block, the encrypted comment, the indentation and the key order take no
// part; a value the commit fills in or the record holds encrypted is not
// compared, and a literal under an encrypted field is among those.
func TestSameSkeletonKeepsAnEncryptedFile(t *testing.T) {
	if !sameSkeleton(renderedFile, fileOnRecord) {
		t.Fatal("the encrypted file on record differs from the render outside the values")
	}
	reordered := "apiVersion: v1\nkind: Secret\ntype: Opaque\nstringData:\n  ttl: 3\n  client-id: other\n  slack-token: SUPPLIED(slack.token)\n  registration-token: GENERATED(x-token)\n  dex-client-secret: GENERATED(x-client)\nmetadata:\n  labels:\n    application.giantswarm.io/team: bumblebee\n  namespace: agent-platform\n  name: muster-oauth-credentials\n"
	if !sameSkeleton(reordered, fileOnRecord) {
		t.Fatal("the key order and a literal under an encrypted field are compared")
	}
}

// A literal under an encrypted field is what the comparison does not see:
// the kept file names it with the render's value, by path — never a value
// the commit fills in, never a plaintext field, and nothing for a file that
// is not the record's shape.
func TestUnseenNamesTheLiteralsUnderEncryptedFields(t *testing.T) {
	got := unseen(renderedFile, fileOnRecord)
	want := []Unseen{{Path: "stringData.client-id", Value: musterKey}, {Path: "stringData.ttl", Value: "3"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unseen: %+v, want %+v", got, want)
	}
	if got := unseen(renderedFile, renderedFile); len(got) != 0 {
		t.Errorf("a plaintext file holds nothing unseen: %+v", got)
	}
	if got := unseen("a: [1\n", fileOnRecord); got != nil {
		t.Errorf("no YAML: %+v", got)
	}
}

// A key the render adds or drops under the encrypted field, a plaintext
// field the render changes, a type that changes: the file has to be written.
func TestSameSkeletonSeesTheSkeletonChange(t *testing.T) {
	for name, rendered := range map[string]string{
		"a key added":             renderedFile + "  oauth-encryption-key: GENERATED(x-key)\n",
		"a key dropped":           "apiVersion: v1\nkind: Secret\nmetadata:\n  name: muster-oauth-credentials\n  namespace: agent-platform\n  labels:\n    application.giantswarm.io/team: bumblebee\ntype: Opaque\nstringData:\n  dex-client-secret: GENERATED(x-client)\n  registration-token: GENERATED(x-token)\n  slack-token: SUPPLIED(slack.token)\n  client-id: muster\n",
		"a plaintext field moved": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: muster-oauth-credentials\n  namespace: mcp-prometheus\n  labels:\n    application.giantswarm.io/team: bumblebee\ntype: Opaque\nstringData:\n  dex-client-secret: GENERATED(x-client)\n  registration-token: GENERATED(x-token)\n  slack-token: SUPPLIED(slack.token)\n  client-id: muster\n  ttl: 3\n",
		"a label added":           "apiVersion: v1\nkind: Secret\nmetadata:\n  name: muster-oauth-credentials\n  namespace: agent-platform\n  labels:\n    application.giantswarm.io/team: bumblebee\n    tier: two\ntype: Opaque\nstringData:\n  dex-client-secret: GENERATED(x-client)\n  registration-token: GENERATED(x-token)\n  slack-token: SUPPLIED(slack.token)\n  client-id: muster\n  ttl: 3\n",
		"a type changed":          "apiVersion: v1\nkind: Secret\nmetadata:\n  name: muster-oauth-credentials\n  namespace: agent-platform\n  labels:\n    application.giantswarm.io/team: bumblebee\ntype: \"Opaque\"\nstringData:\n  dex-client-secret: GENERATED(x-client)\n  registration-token: GENERATED(x-token)\n  slack-token: SUPPLIED(slack.token)\n  client-id: muster\n  ttl: 3\nimmutable: true\n",
		"a sops block of its own": renderedFile + "sops:\n  version: 1\n",
	} {
		if sameSkeleton(rendered, fileOnRecord) {
			t.Errorf("%s: the render reads as the file on record", name)
		}
	}
}

// A plain file the render puts a key pair's public half into is kept with
// the public key on record; a plain file without a marker, and a file that
// is neither encrypted nor marked, are compared by their bytes elsewhere:
// never the same here. A file that is no YAML is not compared either.
func TestSameSkeletonPlainFiles(t *testing.T) {
	rendered := "apiVersion: v1\nkind: ConfigMap\ndata:\n  public-key: |\n    GENERATED(pair)\n"
	onRecord := "apiVersion: v1\nkind: ConfigMap\ndata:\n  public-key: |\n    -----BEGIN PUBLIC KEY-----\n    MFkw\n    -----END PUBLIC KEY-----\n"
	if !sameSkeleton(rendered, onRecord) {
		t.Fatal("the public half on record is compared with the marker")
	}
	if sameSkeleton(rendered, "apiVersion: v1\nkind: ConfigMap\ndata:\n  public-key: x\n  other: y\n") {
		t.Fatal("a key the record adds is unseen")
	}
	if sameSkeleton("a: 1\n# a comment\n", "a: 1\n") || sameSkeleton("a: 1\n", "a: 1\n") {
		t.Fatal("a plain file without a marker is compared here")
	}
	if sameSkeleton("a: 1\n", "sops:\n  version: 1\n") || sameSkeleton(renderedFile, "sops: [\n") || sameSkeleton("a: [\n", fileOnRecord) {
		t.Fatal("an empty or unparsable file reads as the render")
	}
	if sameSkeleton("a: 1\n---\nb: 2\n", "a: 1\nsops:\n  version: 1\n") {
		t.Fatal("a second document is unseen")
	}
}
