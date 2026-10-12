// Adresses IP : adresses « globales » (publiques) et regroupement de réseaux,
// comme le module ipaddress de Python (is_global, collapse_addresses).

package main

import (
	"net/netip"
	"sort"
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

func addrIsGlobal(a netip.Addr) bool {
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

// lastAddr : dernière adresse d'un réseau.
func lastAddr(p netip.Prefix) netip.Addr {
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

// prefixIsGlobal : comme ipaddress, un réseau n'est pas global seulement si sa
// première et sa dernière adresse ne le sont pas.
func prefixIsGlobal(p netip.Prefix) bool {
	p = p.Masked()
	return addrIsGlobal(p.Addr()) || addrIsGlobal(lastAddr(p))
}

// parseNetwork : « 203.0.113.4 » ou « 198.51.100.7/24 » → réseau (bits d'hôte
// ignorés, comme ip_network(strict=False)).
func parseNetwork(text string) (netip.Prefix, bool) {
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

// netString : texte d'un réseau, sans « /32 » ni « /128 » pour une adresse seule.
func netString(p netip.Prefix) string {
	if p.Bits() == p.Addr().BitLen() {
		return p.Addr().String()
	}
	return p.String()
}

// collapsePrefixes : réseaux regroupés (même famille), triés, comme
// ipaddress.collapse_addresses : l'union, en un minimum de réseaux.
func collapsePrefixes(nets []netip.Prefix) []netip.Prefix {
	if len(nets) == 0 {
		return nil
	}
	type rng struct{ lo, hi netip.Addr }
	ranges := make([]rng, 0, len(nets))
	for _, p := range nets {
		p = p.Masked()
		ranges = append(ranges, rng{p.Addr(), lastAddr(p)})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].lo.Less(ranges[j].lo) })
	merged := []rng{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		next := last.hi.Next()
		if !r.lo.Less(next) && r.lo != next && next.IsValid() {
			merged = append(merged, r)
			continue
		}
		if last.hi.Less(r.hi) {
			last.hi = r.hi
		}
	}
	var out []netip.Prefix
	for _, r := range merged {
		lo := r.lo
		for {
			bits := lo.BitLen()
			// Le plus grand bloc aligné sur lo qui ne dépasse pas hi
			for b := 0; b <= lo.BitLen(); b++ {
				p := netip.PrefixFrom(lo, b)
				if p.Masked().Addr() == lo && !r.hi.Less(lastAddr(p)) {
					bits = b
					break
				}
			}
			p := netip.PrefixFrom(lo, bits)
			out = append(out, p)
			end := lastAddr(p)
			if end == r.hi {
				break
			}
			lo = end.Next()
			if !lo.IsValid() {
				break
			}
		}
	}
	return out
}
