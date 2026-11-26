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
	ipv6URL        = "https://api6.ipify.org"
	dualStackURL   = "https://api64.ipify.org"
	checkTimeout   = 8 * time.Second
	dnsTimeout     = 2 * time.Second
	familyTimeout  = 3 * time.Second
	maxAddressBody = 256
)

type Result struct {
	GoogleReachable bool
	ExitIP          string
	ServerIPs       []string
	IPMatch         bool
}

// Check sends the public probes through the supplied client's transport. A nil
// client uses the default HTTP transport. A missing exit or server IP leaves
// the comparison unknown.
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
	ips := serverIPs(dnsContext, server)
	cancelDNS()
	exit := exitIP(ctx, &probeClient, ips)
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

func serverIPs(ctx context.Context, server string) []netip.Addr {
	server = strings.TrimSpace(server)
	if ip, err := netip.ParseAddr(server); err == nil {
		return []netip.Addr{ip.Unmap().WithZone("")}
	}
	if !strings.Contains(server, "://") {
		server = "//" + server
	}
	parsed, err := url.Parse(server)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", parsed.Hostname())
	if err != nil {
		return nil
	}
	for i, ip := range ips {
		ips[i] = ip.Unmap().WithZone("")
	}
	slices.SortFunc(ips, netip.Addr.Compare)
	return slices.Compact(ips)
}

func exitIP(ctx context.Context, client *http.Client, ips []netip.Addr) netip.Addr {
	var ipv4, ipv6 bool
	for _, addr := range ips {
		if addr.Is4() {
			ipv4 = true
		} else {
			ipv6 = true
		}
	}
	// Prefer the same address family as a single-family KeyDock server.
	if ipv4 != ipv6 {
		preferredURL := ipv4URL
		if ipv6 {
			preferredURL = ipv6URL
		}
		familyContext, cancel := context.WithTimeout(ctx, familyTimeout)
		preferredIP := readIP(familyContext, client, preferredURL)
		cancel()
		if preferredIP.IsValid() {
			return preferredIP
		}
	}
	return readIP(ctx, client, dualStackURL)
}

func readIP(ctx context.Context, client *http.Client, endpoint string) netip.Addr {
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
	return ip.Unmap()
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
