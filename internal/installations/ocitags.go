package installations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TagLister lists the tags of the OCI repository at an oci:// URL.
type TagLister func(ctx context.Context, ociURL string) ([]string, error)

// registryClient is the HTTP client the record's registry reads go through.
var registryClient = &http.Client{Timeout: 30 * time.Second}

// maxTagPages bounds the pages a listing follows: a registry that never ends
// its Link chain is refused rather than read forever.
const maxTagPages = 200

// RegistryTags lists tags through the OCI distribution API
// (GET /v2/<name>/tags/list), anonymously: a 401 with a Bearer challenge is
// answered with the token the challenge's realm hands out for its service
// and scope. Pages follow the Link header (rel="next") to the end.
func RegistryTags(client *http.Client) TagLister {
	return registryTags(client, "https")
}

func registryTags(client *http.Client, scheme string) TagLister {
	return func(ctx context.Context, ociURL string) ([]string, error) {
		host, name, ok := strings.Cut(strings.TrimPrefix(ociURL, "oci://"), "/")
		if !ok || host == "" || name == "" || !strings.HasPrefix(ociURL, "oci://") {
			return nil, fmt.Errorf("%q is no oci://<registry>/<repository> URL", ociURL)
		}
		next := scheme + "://" + host + "/v2/" + name + "/tags/list?n=100"
		var tags []string
		token := ""
		for page := 0; next != ""; page++ {
			if page == maxTagPages {
				return nil, fmt.Errorf("%s: more than %d pages of tags", ociURL, maxTagPages)
			}
			resp, err := getTags(ctx, client, next, token)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode == http.StatusUnauthorized && token == "" {
				challenge := resp.Header.Get("WWW-Authenticate")
				_ = resp.Body.Close()
				if token, err = anonymousToken(ctx, client, challenge); err != nil {
					return nil, fmt.Errorf("%s: %w", ociURL, err)
				}
				page--
				continue
			}
			var body struct {
				Tags []string `json:"tags"`
			}
			err = decodeJSON(resp, &body)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", ociURL, err)
			}
			tags = append(tags, body.Tags...)
			next, err = nextPage(next, resp.Header.Get("Link"))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", ociURL, err)
			}
		}
		return tags, nil
	}
}

func getTags(ctx context.Context, client *http.Client, target, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

// anonymousToken answers a Bearer challenge
// (realm="…",service="…",scope="…") with the realm's anonymous token.
func anonymousToken(ctx context.Context, client *http.Client, challenge string) (string, error) {
	scheme, params, ok := strings.Cut(challenge, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Errorf("the registry asks for %q, no Bearer challenge", challenge)
	}
	fields := map[string]string{}
	for _, p := range strings.Split(params, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok {
			fields[k] = strings.Trim(v, `"`)
		}
	}
	realm, err := url.Parse(fields["realm"])
	if err != nil || realm.Host == "" {
		return "", fmt.Errorf("the Bearer challenge names no realm: %q", challenge)
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if fields[k] != "" {
			q.Set(k, fields[k])
		}
	}
	realm.RawQuery = q.Encode()
	resp, err := getTags(ctx, client, realm.String(), "")
	if err != nil {
		return "", err
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := decodeJSON(resp, &body); err != nil {
		return "", fmt.Errorf("the anonymous token: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", errors.New("the anonymous token answer holds no token")
}

func decodeJSON(resp *http.Response, v any) error {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("GET %s answered %s: %s", resp.Request.URL.Redacted(), resp.Status, strings.TrimSpace(string(snippet)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// nextPage resolves a Link header's rel="next" target against the page it
// came from; no next link is the end.
func nextPage(current, link string) (string, error) {
	for _, part := range strings.Split(link, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		ref, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
		if err != nil {
			return "", fmt.Errorf("the next page %q: %w", target, err)
		}
		base, err := url.Parse(current)
		if err != nil {
			return "", err
		}
		return base.ResolveReference(ref).String(), nil
	}
	return "", nil
}
