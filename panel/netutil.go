// Adresses IP : adresses « globales » (publiques) et regroupement de réseaux,
// comme le module ipaddress de Python (is_global, collapse_addresses).

package main

import (
	"net/netip"
	"sort"

	"github.com/Gogowwww/frp-manager/panel/netaddr"
)

// Règle des adresses publiques : panel/netaddr (partagée avec tools/blocklists).
// Public address rule: panel/netaddr (shared with tools/blocklists).
var (
	addrIsGlobal   = netaddr.IsGlobal
	prefixIsGlobal = netaddr.PrefixIsGlobal
	lastAddr       = netaddr.Last
	parseNetwork   = netaddr.ParseNetwork
)

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
