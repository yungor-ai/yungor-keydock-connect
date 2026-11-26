// Package networkcheck measures reachability and the public address of a network path.
package networkcheck

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	googleURL      = "https://www.google.com/generate_204"
	ipv4URL        = "https://api.ipify.org"
	dualStackURL   = "https://api64.ipify.org"
	checkTimeout   = 8 * time.Second
	dnsTimeout     = 2 * time.Second
	ipv4Timeout    = 3 * time.Second
	maxAddressBody = 256
)

type Result struct {
	GoogleReachable bool
	ExitIP          string
	ServerIPs       []string
	IPMatch         bool
}

// Check sends the public probes through the supplied client's transport. A nil
// client uses the default HTTP transport. The IP comparison uses only IPv4.
func Check(ctx context.Context, client *http.Client, server string) Result {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	probeClient := http.Client{}
	if client != nil {
		probeClient = *client
	}
	probeClient.Jar = nil
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	google := make(chan bool, 1)
	go func() {
		response := get(ctx, &probeClient, googleURL)
		if response == nil {
			google <- false
			return
		}
		response.Body.Close()
		google <- response.StatusCode == http.StatusNoContent
	}()

	dnsContext, cancelDNS := context.WithTimeout(ctx, dnsTimeout)
	ips := serverIPv4s(dnsContext, server)
	cancelDNS()
	exit := exitIPv4(ctx, &probeClient)
	result := Result{GoogleReachable: <-google}
	for _, ip := range ips {
		result.ServerIPs = append(result.ServerIPs, ip.String())
	}
	if exit.IsValid() {
		result.ExitIP = exit.String()
		result.IPMatch = slices.Contains(ips, exit)
	}
	return result
}

func serverIPv4s(ctx context.Context, server string) []netip.Addr {
	host := strings.TrimSpace(server)
	if _, err := netip.ParseAddr(host); err != nil {
		if !strings.Contains(host, "://") {
			host = "//" + host
		}
		parsed, err := url.Parse(host)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil {
			return nil
		}
		host = parsed.Hostname()
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return nil
	}
	// The resolver can return an IPv6 literal even for an IPv4 lookup.
	ipv4s := ips[:0]
	for _, ip := range ips {
		if ip = ip.Unmap(); ip.Is4() {
			ipv4s = append(ipv4s, ip)
		}
	}
	slices.SortFunc(ipv4s, netip.Addr.Compare)
	return slices.Compact(ipv4s)
}

func exitIPv4(ctx context.Context, client *http.Client) netip.Addr {
	probeCtx, cancel := context.WithTimeout(ctx, ipv4Timeout)
	ip := readIPv4(probeCtx, client, ipv4URL)
	cancel()
	if ip.IsValid() {
		return ip
	}
	return readIPv4(ctx, client, dualStackURL)
}

func readIPv4(ctx context.Context, client *http.Client, endpoint string) netip.Addr {
	response := get(ctx, client, endpoint)
	if response == nil {
		return netip.Addr{}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return netip.Addr{}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAddressBody+1))
	if err != nil || len(body) > maxAddressBody {
		return netip.Addr{}
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}
	}
	ip = ip.Unmap()
	if !ip.Is4() {
		return netip.Addr{}
	}
	return ip
}

func get(ctx context.Context, client *http.Client, endpoint string) *http.Response {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	request.Header.Set("Cache-Control", "no-cache")
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	return response
}
