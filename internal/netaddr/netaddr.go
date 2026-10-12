// Package netaddr : adresses IP « globales » (publiques), comme le module
// ipaddress de Python (is_global). Partagé par le panel et le robot des listes
// communautaires (tools/blocklists), qui appliquent ainsi la même règle.
package netaddr

import (
	"net/netip"
	"strings"
)

func mustPrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Réseaux non globaux (registres IANA des adresses à usage spécial).
var (
	privateV4 = mustPrefixes("0.0.0.0/8", "10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32", "100.64.0.0/10")
	privateV4Exceptions = mustPrefixes("192.0.0.9/32", "192.0.0.10/32")
	privateV6           = mustPrefixes("::1/128", "::/128", "::ffff:0:0/96", "64:ff9b:1::/48", "100::/64",
		"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "fc00::/7", "fe80::/10")
	privateV6Exceptions = mustPrefixes("2001:1::1/128", "2001:1::2/128", "2001:3::/32", "2001:4:112::/48",
		"2001:20::/28", "2001:30::/28")
)

// IsGlobal : adresse publique (hors registres IANA des adresses à usage spécial).
func IsGlobal(a netip.Addr) bool {
	a = a.WithZone("")
	priv, exc := privateV6, privateV6Exceptions
	if a.Is4() {
		priv, exc = privateV4, privateV4Exceptions
	}
	for _, p := range exc {
		if p.Contains(a) {
			return true
		}
	}
	for _, p := range priv {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Last : dernière adresse d'un réseau.
func Last(p netip.Prefix) netip.Addr {
	p = p.Masked()
	b := p.Addr().As16()
	bits := p.Bits()
	if p.Addr().Is4() {
		bits += 96
	}
	for i := bits; i < 128; i++ {
		b[i/8] |= 1 << (7 - uint(i%8))
	}
	a := netip.AddrFrom16(b)
	if p.Addr().Is4() {
		a = a.Unmap()
	}
	return a
}

// PrefixIsGlobal : comme ipaddress, un réseau n'est pas global seulement si sa
// première et sa dernière adresse ne le sont pas.
func PrefixIsGlobal(p netip.Prefix) bool {
	p = p.Masked()
	return IsGlobal(p.Addr()) || IsGlobal(Last(p))
}

// ParseNetwork : « 203.0.113.4 » ou « 198.51.100.7/24 » → réseau (bits d'hôte
// ignorés, comme ip_network(strict=False)).
func ParseNetwork(text string) (netip.Prefix, bool) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "/") {
		p, err := netip.ParsePrefix(text)
		if err != nil || p.Addr().Zone() != "" {
			return netip.Prefix{}, false
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(text)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}
