package orbit

import (
	"mime"
	"net"
	"net/http"
	"strings"
)

// parseTrustedProxies reads ORBIT_TRUSTED_PROXIES: a comma separated list of
// IP addresses or CIDR ranges of reverse proxies whose X-Forwarded-For header
// may be used to determine the real client address. Invalid entries are
// returned so that startup can reject a misconfiguration instead of silently
// trusting nothing (or everything).
func parseTrustedProxies(value string) ([]*net.IPNet, []string) {
	var nets []*net.IPNet
	var invalid []string
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				bits := 128
				if ip.To4() != nil {
					ip, bits = ip.To4(), 32
				}
				nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
				continue
			}
			invalid = append(invalid, entry)
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			nets = append(nets, network)
		} else {
			invalid = append(invalid, entry)
		}
	}
	return nets, invalid
}

func trusted(nets []*net.IPNet, ip net.IP) bool {
	for _, network := range nets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP returns the address used for rate limiting and audit entries.
// X-Forwarded-For is only honoured when the direct peer is a configured
// trusted proxy; the header is walked from the right so that a client cannot
// spoof its address by prepending values.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || len(s.TrustedProxies) == 0 || !trusted(s.TrustedProxies, peer) {
		return host
	}
	hops := []string{}
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(header, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			return host
		}
		if !trusted(s.TrustedProxies, ip) {
			return ip.String()
		}
	}
	return host
}

// attachment builds a Content-Disposition header that stays well-formed for
// any file name (quotes, non-ASCII) by letting mime encode it per RFC 2231.
func attachment(name string) string {
	if name == "" {
		name = "download"
	}
	if header := mime.FormatMediaType("attachment", map[string]string{"filename": name}); header != "" {
		return header
	}
	return "attachment"
}
