// Package site is the controller reached through an HTTPS reverse proxy: its public URL
// (-url) and the proxy's source addresses (-proxy). Without -url the controller is on
// localhost only and nothing here changes a request.
//
// With -url, Guard sits in front of everything else:
//   - The source: a connection from this machine (loopback, or from the address it came
//     in on) or from a -proxy address is served; any other gets 403, before the login and
//     the static files.
//   - The Host: the URL's host, or a localhost name from this machine and not relayed (no
//     X-Forwarded-For, Forwarded or X-Real-IP: a proxy on this machine passing on a client's
//     Host: localhost); anything else 403.
//   - A request by the URL's host is Public: the API's Origin check takes the URL's origin
//     (Origin), and the session cookie is Secure (TLS is the proxy's, implied by -url).
//   - Client is the address a login's backoff and the logs count: the last
//     X-Forwarded-For entry (the one the proxy added) on a connection from a -proxy
//     address, else the connection's own address. X-Forwarded-For is never used for
//     anything else, and no X-Forwarded-Proto or -Host is read.
package site

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// Config is the controller's public URL and its proxies. The zero Config: localhost only.
type Config struct {
	URL     *url.URL       // https://host[:port], no path; nil: none
	Proxies []netip.Prefix // the reverse proxy's source addresses
}

// ParseURL checks a -url: https, a host, an optional port, nothing else (a path "/" is
// allowed). "" is none.
func ParseURL(s string) (*url.URL, error) {
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("-url %q: %v", s, err)
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("-url %q: must be https://name or https://name:port, nothing after it", s)
	}
	host := strings.ToLower(u.Host)
	if u.Port() == "443" {
		host = strings.ToLower(u.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return &url.URL{Scheme: "https", Host: host}, nil
}

// ParseProxies reads a -proxy: addresses or networks (CIDR), separated by commas. "" is none.
func ParseProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 { // ::ffff:a.b.c.d/n: sources are matched unmapped
				p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("-proxy %q: not an IP address or network (CIDR)", f)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// New is the Config from -url and -proxy as given, checked against -listen: a listen
// address off loopback needs both, and -proxy needs -url.
func New(listen, rawURL, proxies string, loopbackListen bool) (Config, error) {
	u, err := ParseURL(rawURL)
	if err != nil {
		return Config{}, err
	}
	ps, err := ParseProxies(proxies)
	if err != nil {
		return Config{}, err
	}
	switch {
	case len(ps) > 0 && u == nil:
		return Config{}, errors.New("-proxy needs -url, the address the proxy serves the controller at")
	case !loopbackListen && (u == nil || len(ps) == 0):
		return Config{}, fmt.Errorf("-listen %s: off localhost only behind an HTTPS reverse proxy, with -url "+
			"(its https:// address) and -proxy (its source addresses); else 127.0.0.1, [::1] or localhost", listen)
	}
	return Config{URL: u, Proxies: ps}, nil
}

type ctxKey struct{}

type reqInfo struct {
	origin string // the URL's origin, for a request by its host; else ""
	client string
}

func info(r *http.Request) reqInfo {
	i, _ := r.Context().Value(ctxKey{}).(reqInfo)
	return i
}

// Public says whether a request came by the -url host (through the proxy): its Origin is
// the URL's, its cookie Secure.
func Public(r *http.Request) bool { return info(r).origin != "" }

// Origin is the -url's origin for a Public request, else "".
func Origin(r *http.Request) string { return info(r).origin }

// Client is the address a request counts as from: X-Forwarded-For's last entry when the
// proxy sent it, else the connection's (r.RemoteAddr's host).
func Client(r *http.Request) string {
	if c := info(r).client; c != "" {
		return c
	}
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func addrOf(hostport string) (netip.Addr, bool) {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	a, err := netip.ParseAddr(h)
	return a.Unmap(), err == nil
}

// thisMachine: a connection from loopback, or from the address it came in on (this
// machine to its own LAN address: the health check on a -listen off loopback).
func thisMachine(r *http.Request) bool {
	a, ok := addrOf(r.RemoteAddr)
	if !ok {
		return false
	}
	if a.IsLoopback() {
		return true
	}
	if l, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr); l != nil {
		if la, ok := addrOf(l.String()); ok && la == a {
			return true
		}
	}
	return false
}

func (c Config) fromProxy(r *http.Request) bool {
	a, ok := addrOf(r.RemoteAddr)
	if !ok {
		return false
	}
	for _, p := range c.Proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// localName: localhost or a loopback address, with any port.
func localName(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// relayed: the request carries a proxy's forwarding header, so it is someone else's.
func relayed(r *http.Request) bool {
	for _, k := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"} {
		if len(r.Header.Values(k)) > 0 {
			return true
		}
	}
	return false
}

func refuse(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintln(w, msg)
}

// Guard puts the source and Host checks in front of next (see the package comment). With
// no -url it is next itself.
func (c Config) Guard(next http.Handler) http.Handler {
	if c.URL == nil {
		return next
	}
	origin := c.URL.Scheme + "://" + c.URL.Host
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		local, proxy := thisMachine(r), c.fromProxy(r)
		if !local && !proxy {
			refuse(w, "forbidden: the controller is reached through its reverse proxy only")
			return
		}
		var i reqInfo
		switch {
		case strings.EqualFold(r.Host, c.URL.Host):
			i.origin = origin
		case local && localName(r.Host) && !relayed(r):
		default:
			refuse(w, fmt.Sprintf("forbidden: the controller is reached at %s, not %q", origin, r.Host))
			return
		}
		if proxy {
			if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
				parts := strings.Split(xff[len(xff)-1], ",")
				if a, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
					i.client = a.Unmap().String()
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, i)))
	})
}
