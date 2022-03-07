package client // import "github.com/docker/docker/client"

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/docker/docker/api"
	"github.com/docker/docker/api/types"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/env"
	"gotest.tools/v3/skip"
)

func TestNewClientWithOpsFromEnv(t *testing.T) {
	skip.If(t, runtime.GOOS == "windows")

	testcases := []struct {
		doc             string
		envs            map[string]string
		expectedError   string
		expectedVersion string
	}{
		{
			doc:             "default api version",
			envs:            map[string]string{},
			expectedVersion: api.DefaultVersion,
		},
		{
			doc: "invalid cert path",
			envs: map[string]string{
				"DOCKER_CERT_PATH": "invalid/path",
			},
			expectedError: "Could not load X509 key pair: open invalid/path/cert.pem: no such file or directory",
		},
		{
			doc: "default api version with cert path",
			envs: map[string]string{
				"DOCKER_CERT_PATH": "testdata/",
			},
			expectedVersion: api.DefaultVersion,
		},
		{
			doc: "default api version with cert path and tls verify",
			envs: map[string]string{
				"DOCKER_CERT_PATH":  "testdata/",
				"DOCKER_TLS_VERIFY": "1",
			},
			expectedVersion: api.DefaultVersion,
		},
		{
			doc: "default api version with cert path and host",
			envs: map[string]string{
				"DOCKER_CERT_PATH": "testdata/",
				"DOCKER_HOST":      "https://notaunixsocket",
			},
			expectedVersion: api.DefaultVersion,
		},
		{
			doc: "invalid docker host",
			envs: map[string]string{
				"DOCKER_HOST": "host",
			},
			expectedError: "unable to parse docker host `host`",
		},
		{
			doc: "invalid docker host, with good format",
			envs: map[string]string{
				"DOCKER_HOST": "invalid://url",
			},
			expectedVersion: api.DefaultVersion,
		},
		{
			doc: "override api version",
			envs: map[string]string{
				"DOCKER_API_VERSION": "1.22",
			},
			expectedVersion: "1.22",
		},
	}

	defer env.PatchAll(t, nil)()
	for _, tc := range testcases {
		env.PatchAll(t, tc.envs)
		client, err := NewClientWithOpts(FromEnv)
		if tc.expectedError != "" {
			assert.Check(t, is.Error(err, tc.expectedError), tc.doc)
		} else {
			assert.Check(t, err, tc.doc)
			assert.Check(t, is.Equal(client.ClientVersion(), tc.expectedVersion), tc.doc)
		}

		if tc.envs["DOCKER_TLS_VERIFY"] != "" {
			// pedantic checking that this is handled correctly
			tr := client.client.Transport.(*http.Transport)
			assert.Assert(t, tr.TLSClientConfig != nil, tc.doc)
			assert.Check(t, is.Equal(tr.TLSClientConfig.InsecureSkipVerify, false), tc.doc)
		}
	}
}

func TestGetAPIPath(t *testing.T) {
	testcases := []struct {
		version  string
		path     string
		query    url.Values
		expected string
	}{
		{"", "/containers/json", nil, "/containers/json"},
		{"", "/containers/json", url.Values{}, "/containers/json"},
		{"", "/containers/json", url.Values{"s": []string{"c"}}, "/containers/json?s=c"},
		{"1.22", "/containers/json", nil, "/v1.22/containers/json"},
		{"1.22", "/containers/json", url.Values{}, "/v1.22/containers/json"},
		{"1.22", "/containers/json", url.Values{"s": []string{"c"}}, "/v1.22/containers/json?s=c"},
		{"v1.22", "/containers/json", nil, "/v1.22/containers/json"},
		{"v1.22", "/containers/json", url.Values{}, "/v1.22/containers/json"},
		{"v1.22", "/containers/json", url.Values{"s": []string{"c"}}, "/v1.22/containers/json?s=c"},
		{"v1.22", "/networks/kiwl$%^", nil, "/v1.22/networks/kiwl$%25%5E"},
	}

	ctx := context.TODO()
	for _, tc := range testcases {
		client := Client{
			version:  tc.version,
			basePath: "/",
		}
		actual := client.getAPIPath(ctx, tc.path, tc.query)
		assert.Check(t, is.Equal(actual, tc.expected))
	}
}

func TestParseHostURL(t *testing.T) {
	testcases := []struct {
		host        string
		expected    *url.URL
		expectedErr string
	}{
		{
			host:        "",
			expectedErr: "unable to parse docker host",
		},
		{
			host:        "foobar",
			expectedErr: "unable to parse docker host",
		},
		{
			host:     "foo://bar",
			expected: &url.URL{Scheme: "foo", Host: "bar"},
		},
		{
			host:     "tcp://localhost:2476",
			expected: &url.URL{Scheme: "tcp", Host: "localhost:2476"},
		},
		{
			host:     "tcp://localhost:2476/path",
			expected: &url.URL{Scheme: "tcp", Host: "localhost:2476", Path: "/path"},
		},
	}

	for _, testcase := range testcases {
		actual, err := ParseHostURL(testcase.host)
		if testcase.expectedErr != "" {
			assert.Check(t, is.ErrorContains(err, testcase.expectedErr))
		}
		assert.Check(t, is.DeepEqual(actual, testcase.expected))
	}
}

func TestNewClientWithOpsFromEnvSetsDefaultVersion(t *testing.T) {
	defer env.PatchAll(t, map[string]string{
		"DOCKER_HOST":        "",
		"DOCKER_API_VERSION": "",
		"DOCKER_TLS_VERIFY":  "",
		"DOCKER_CERT_PATH":   "",
	})()

	client, err := NewClientWithOpts(FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	assert.Check(t, is.Equal(client.ClientVersion(), api.DefaultVersion))

	const expected = "1.22"
	_ = os.Setenv("DOCKER_API_VERSION", expected)
	client, err = NewClientWithOpts(FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	assert.Check(t, is.Equal(client.ClientVersion(), expected))
}

// TestNegotiateAPIVersionEmpty asserts that client.Client version negotiation
// downgrades to the correct API version if the API's ping response does not
// return an API version.
func TestNegotiateAPIVersionEmpty(t *testing.T) {
	defer env.PatchAll(t, map[string]string{"DOCKER_API_VERSION": ""})()

	client, err := NewClientWithOpts(FromEnv)
	assert.NilError(t, err)

	// set our version to something new
	client.version = "1.25"

	// if no version from server, expect the earliest
	// version before APIVersion was implemented
	const expected = "1.24"

	// test downgrade
	client.NegotiateAPIVersionPing(types.Ping{})
	assert.Equal(t, client.ClientVersion(), expected)
}

// TestNegotiateAPIVersion asserts that client.Client can
// negotiate a compatible APIVersion with the server
func TestNegotiateAPIVersion(t *testing.T) {
	client, err := NewClientWithOpts(FromEnv)
	assert.NilError(t, err)

	expected := "1.21"
	ping := types.Ping{
		APIVersion:   expected,
		OSType:       "linux",
		Experimental: false,
	}

	// set our version to something new
	client.version = "1.22"

	// test downgrade
	client.NegotiateAPIVersionPing(ping)
	assert.Check(t, is.Equal(expected, client.version))

	// set the client version to something older, and verify that we keep the
	// original setting.
	expected = "1.20"
	client.version = expected
	client.NegotiateAPIVersionPing(ping)
	assert.Check(t, is.Equal(client.version, expected))

}

// TestNegotiateAPIVersionOverride asserts that we honor the DOCKER_API_VERSION
// environment variable when negotiating versions.
func TestNegotiateAPVersionOverride(t *testing.T) {
	const expected = "9.99"
	defer env.PatchAll(t, map[string]string{"DOCKER_API_VERSION": expected})()

	client, err := NewClientWithOpts(FromEnv)
	assert.NilError(t, err)

	// test that we honored the env var
	client.NegotiateAPIVersionPing(types.Ping{APIVersion: "1.24"})
	assert.Equal(t, client.ClientVersion(), expected)
}

func TestNegotiateAPIVersionAutomatic(t *testing.T) {
	var pingVersion string
	httpClient := newMockClient(func(req *http.Request) (*http.Response, error) {
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
		resp.Header.Set("API-Version", pingVersion)
		resp.Body = io.NopCloser(strings.NewReader("OK"))
		return resp, nil
	})

	ctx := context.Background()
	client, err := NewClientWithOpts(
		WithHTTPClient(httpClient),
		WithAPIVersionNegotiation(),
	)
	assert.NilError(t, err)

	// Client defaults to use api.DefaultVersion before version-negotiation.
	expected := api.DefaultVersion
	assert.Equal(t, client.ClientVersion(), expected)

	// First request should trigger negotiation
	pingVersion = "1.35"
	expected = "1.35"
	_, _ = client.Info(ctx)
	assert.Equal(t, client.ClientVersion(), expected)

	// Once successfully negotiated, subsequent requests should not re-negotiate
	pingVersion = "1.25"
	expected = "1.35"
	_, _ = client.Info(ctx)
	assert.Equal(t, client.ClientVersion(), expected)
}

// TestNegotiateAPIVersionWithEmptyVersion asserts that initializing a client
// with an empty version string does still allow API-version negotiation
func TestNegotiateAPIVersionWithEmptyVersion(t *testing.T) {
	client, err := NewClientWithOpts(WithVersion(""))
	assert.NilError(t, err)

	const expected = "1.35"
	client.NegotiateAPIVersionPing(types.Ping{APIVersion: expected})
	assert.Equal(t, client.ClientVersion(), expected)
}

// TestNegotiateAPIVersionWithFixedVersion asserts that initializing a client
// with a fixed version disables API-version negotiation
func TestNegotiateAPIVersionWithFixedVersion(t *testing.T) {
	const customVersion = "1.35"
	client, err := NewClientWithOpts(WithVersion(customVersion))
	assert.NilError(t, err)

	client.NegotiateAPIVersionPing(types.Ping{APIVersion: "1.31"})
	assert.Equal(t, client.ClientVersion(), customVersion)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (rtf roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return rtf(req)
}

type bytesBufferClose struct {
	*bytes.Buffer
}

func (bbc bytesBufferClose) Close() error {
	return nil
}

func TestClientRedirect(t *testing.T) {
	client := &http.Client{
		CheckRedirect: CheckRedirect,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() == "/bla" {
				return &http.Response{StatusCode: 404}, nil
			}
			return &http.Response{
				StatusCode: 301,
				Header:     map[string][]string{"Location": {"/bla"}},
				Body:       bytesBufferClose{bytes.NewBuffer(nil)},
			}, nil
		}),
	}

	cases := []struct {
		httpMethod  string
		expectedErr *url.Error
		statusCode  int
	}{
		{http.MethodGet, nil, 301},
		{http.MethodPost, &url.Error{Op: "Post", URL: "/bla", Err: ErrRedirect}, 301},
		{http.MethodPut, &url.Error{Op: "Put", URL: "/bla", Err: ErrRedirect}, 301},
		{http.MethodDelete, &url.Error{Op: "Delete", URL: "/bla", Err: ErrRedirect}, 301},
	}

	for _, tc := range cases {
		req, err := http.NewRequest(tc.httpMethod, "/redirectme", nil)
		assert.Check(t, err)
		resp, err := client.Do(req)
		assert.Check(t, is.Equal(resp.StatusCode, tc.statusCode))
		if tc.expectedErr == nil {
			assert.Check(t, is.Nil(err))
		} else {
			urlError, ok := err.(*url.Error)
			assert.Assert(t, ok, "%T is not *url.Error", err)
			assert.Check(t, is.Equal(*urlError, *tc.expectedErr))
		}
	}
}
