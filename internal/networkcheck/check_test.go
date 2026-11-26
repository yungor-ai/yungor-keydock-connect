package networkcheck

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type testResponse struct {
	status int
	body   string
}

type testTransport map[string]testResponse

func (transport testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
		return nil, errors.New("unexpected probe method or credentials")
	}
	response, found := transport[request.URL.String()]
	if !found {
		return nil, errors.New("endpoint unavailable")
	}
	headers := make(http.Header)
	if response.status == http.StatusFound {
		headers.Set("Location", "https://other.example/")
	}
	return &http.Response{StatusCode: response.status, Header: headers, Body: io.NopCloser(strings.NewReader(response.body))}, nil
}

func TestCheckSeparatesGoogleReachabilityFromExitIPComparison(t *testing.T) {
	const google = "https://www.google.com/generate_204"
	const universal = "https://api64.ipify.org"
	tests := []struct {
		name      string
		server    string
		transport testTransport
		want      Result
	}{
		{
			name:   "IPv4 KeyDock uses IPv4 even when the universal endpoint shows IPv6",
			server: "https://192.0.2.20:8443/keydock",
			transport: testTransport{
				google:                  {status: 204},
				"https://api.ipify.org": {status: 200, body: "::ffff:192.0.2.20\n"},
				universal:               {status: 200, body: "2001:db8::20"},
			},
			want: Result{GoogleReachable: true, ExitIP: "192.0.2.20", ServerIPs: []string{"192.0.2.20"}, IPMatch: true},
		},
		{
			name:   "IPv6 without brackets and a different exit IP",
			server: "2001:db8:0:0::20",
			transport: testTransport{
				google:                   {status: 204},
				"https://api6.ipify.org": {status: 200, body: "2001:db8::21"},
			},
			want: Result{GoogleReachable: true, ExitIP: "2001:db8::21", ServerIPs: []string{"2001:db8::20"}},
		},
		{
			name:   "failed IPv6 endpoint keeps same path for dual-stack service",
			server: "https://[2001:db8::20]",
			transport: testTransport{
				google:                   {status: 204},
				"https://api6.ipify.org": {status: 503},
				universal:                {status: 200, body: "192.0.2.21"},
			},
			want: Result{GoogleReachable: true, ExitIP: "192.0.2.21", ServerIPs: []string{"2001:db8::20"}},
		},
		{
			name: "Google redirect does not resolve through another host",
			transport: testTransport{
				google:                   {status: 302},
				"https://other.example/": {status: 204},
				universal:                {status: 200, body: "192.0.2.20"},
			},
			want: Result{ExitIP: "192.0.2.20"},
		},
		{
			name: "oversized and malformed responses leave exit IP unknown",
			transport: testTransport{
				google:    {status: 204},
				universal: {status: 200, body: "192.0.2.20" + strings.Repeat("\n", 300)},
			},
			want: Result{GoogleReachable: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			for endpoint := range tt.transport {
				u, err := url.Parse(endpoint)
				if err != nil {
					t.Fatal(err)
				}
				jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "test-private-cookie"}})
			}
			got := Check(t.Context(), &http.Client{Transport: tt.transport, Jar: jar}, tt.server)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Check() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestServerIPLookupSupportsHostnames(t *testing.T) {
	ips := serverIPs(t.Context(), "https://localhost:8443/keydock")
	for _, ip := range ips {
		if !ip.IsLoopback() {
			t.Errorf("localhost returned non-loopback address %s", ip)
		}
	}
	if len(ips) == 0 {
		t.Fatal("localhost did not resolve")
	}
}
