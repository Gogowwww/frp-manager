// Pare-feu : mêmes résultats que la version Python sur les mêmes entrées
// (testdata/firewall_python.json, produit par panel/app.py).

package main

import (
	"encoding/json"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type pythonFirewall struct {
	Rules           []map[string]any            `json:"rules"`
	Known           []map[string]any            `json:"known"`
	RulesetSeen     string                      `json:"ruleset_seen"`
	RulesetNoSeen   string                      `json:"ruleset_noseen"`
	RulesetDisabled string                      `json:"ruleset_disabled"`
	Verdicts        map[string][]map[string]any `json:"verdicts"`
	CollapseV4      []string                    `json:"collapse_v4"`
	CollapseV4All   []string                    `json:"collapse_v4_all"`
	CollapseV6      []string                    `json:"collapse_v6"`
	IsGlobal        map[string]bool             `json:"is_global"`
	NftInput        map[string]any              `json:"nft_input"`
	NftCounts       map[string]int              `json:"nft_counts"`
	NftSeen         []map[string]any            `json:"nft_seen"`
	ListSourcesIn   []any                       `json:"list_sources_in"`
	ListSources     []map[string]any            `json:"list_sources"`
	ListSkipped     []map[string]any            `json:"list_skipped"`
}

func loadPythonFirewall(t *testing.T) pythonFirewall {
	raw, err := os.ReadFile("testdata/firewall_python.json")
	if err != nil {
		t.Fatal(err)
	}
	var p pythonFirewall
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// firewallState : mêmes caches que le script Python (AS et liste communautaire).
func firewallState(t *testing.T) pythonFirewall {
	setup(t)
	p := loadPythonFirewall(t)
	cfgUpdate(Cfg{"bind_port": float64(8765)})
	asnMu.Lock()
	asnVal = map[string]*asnEntry{"64500": {Holder: "Test", V4: []string{"198.51.100.0/24", "203.0.113.0/25"}, V6: []string{"2001:db8::/32"}, Fetched: 1}}
	asnMu.Unlock()
	listsMu.Lock()
	listsVal = &listsCache{Fetched: 1, Lists: map[string]J{"scan": {"id": "scan", "name": "Scan", "mode": "block",
		"sources": []any{map[string]any{"cidr": "45.0.0.0/16", "note": ""}, map[string]any{"cidr": "45.1.0.0/16", "note": ""}}}}}
	listsMu.Unlock()
	return p
}

func knownFrom(p pythonFirewall) []J {
	var known []J
	for _, k := range p.Known {
		known = append(known, J{"start": int(k["start"].(float64)), "end": int(k["end"].(float64)), "proto": k["proto"],
			"label": k["label"], "kind": k["kind"], "online": k["online"]})
	}
	return known
}

func normalizedRules(t *testing.T, p pythonFirewall) []J {
	raws := []map[string]any{
		{"id": "a1", "name": "Maison", "mode": "allow", "ports": "*", "sources": []any{map[string]any{"cidr": "192.0.2.10"}, map[string]any{"cidr": "2001:db8:1::/48"}}},
		{"id": "b2", "name": "Scanners", "mode": "block", "ports": "7000, 8000-8100", "sources": []any{map[string]any{"asn": float64(64500)}, map[string]any{"cidr": "203.0.113.128/25"}, map[string]any{"cidr": "10.0.0.1"}}, "list": "scan"},
		{"id": "c3", "name": "off", "mode": "block", "ports": "9000", "sources": []any{map[string]any{"cidr": "1.2.3.4"}}, "enabled": false},
		{"id": "d4", "name": "v4", "mode": "block", "ports": "7000", "sources": []any{map[string]any{"cidr": "8.8.8.8"}}},
	}
	var rules []J
	for i, r := range raws {
		rule, err := FR.fwNormalizeRule(r, i)
		if err != nil {
			t.Fatal(err)
		}
		rules = append(rules, rule)
	}
	return rules
}

func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestFirewallRulesNormalizedLikePython(t *testing.T) {
	p := firewallState(t)
	rules := normalizedRules(t, p)
	var a, b any
	json.Unmarshal([]byte(asJSON(rules)), &a)
	json.Unmarshal([]byte(asJSON(p.Rules)), &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Go : %s\nPython : %s", asJSON(rules), asJSON(p.Rules))
	}
}

func TestFirewallRulesetLikePython(t *testing.T) {
	p := firewallState(t)
	rules, known := normalizedRules(t, p), knownFrom(p)
	for name, c := range map[string]struct {
		got, want string
	}{
		"seen":     {fwRuleset(fwConfig{true, rules}, known, true), p.RulesetSeen},
		"noseen":   {fwRuleset(fwConfig{true, rules}, known, false), p.RulesetNoSeen},
		"disabled": {fwRuleset(fwConfig{false, rules}, known, true), p.RulesetDisabled},
	} {
		if c.got != c.want {
			t.Errorf("%s :\n--- Go ---\n%s\n--- Python ---\n%s", name, c.got, c.want)
		}
	}
}

func TestFirewallVerdictsLikePython(t *testing.T) {
	p := firewallState(t)
	rules, known := normalizedRules(t, p), knownFrom(p)
	for ip, want := range p.Verdicts {
		var a, b any
		json.Unmarshal([]byte(asJSON(fwVerdicts(ip, fwConfig{true, rules}, known))), &a)
		json.Unmarshal([]byte(asJSON(want)), &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s :\nGo : %s\nPython : %s", ip, asJSON(a), asJSON(b))
		}
	}
}

func prefixes(list []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range list {
		p, _ := parseNetwork(s)
		out = append(out, p)
	}
	return out
}

func TestCollapseLikePython(t *testing.T) {
	p := loadPythonFirewall(t)
	for _, c := range []struct {
		in   []string
		want []string
	}{
		{[]string{"10.0.0.0/24", "10.0.1.0/24", "10.0.2.0/23", "10.0.0.128/25", "192.168.1.1/32", "192.168.1.0/32", "192.168.1.2/31"}, p.CollapseV4},
		{[]string{"0.0.0.0/1", "128.0.0.0/1"}, p.CollapseV4All},
		{[]string{"2001:db8::/33", "2001:db8:8000::/33", "2001:db8:1::/48", "fe80::1/128"}, p.CollapseV6},
	} {
		if got := prefixNftStrings(collapsePrefixes(prefixes(c.in))); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v → %v, Python %v", c.in, got, c.want)
		}
	}
}

func TestIsGlobalLikePython(t *testing.T) {
	p := loadPythonFirewall(t)
	for cidr, want := range p.IsGlobal {
		n, _ := parseNetwork(cidr)
		if prefixIsGlobal(n) != want {
			t.Errorf("%s : %v, Python %v", cidr, !want, want)
		}
	}
}

func TestNftStateParsingLikePython(t *testing.T) {
	p := loadPythonFirewall(t)
	raw, _ := json.Marshal(p.NftInput)
	var data map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	dec.Decode(&data)
	counts, seen := fwParseState(listv(data, "nftables"), 1_000_000)
	if !reflect.DeepEqual(counts, p.NftCounts) {
		t.Fatalf("%v / %v", counts, p.NftCounts)
	}
	var a, b any
	json.Unmarshal([]byte(asJSON(seen)), &a)
	json.Unmarshal([]byte(asJSON(p.NftSeen)), &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Go : %s\nPython : %s", asJSON(a), asJSON(b))
	}
}

func TestListSourcesLikePython(t *testing.T) {
	setup(t)
	p := loadPythonFirewall(t)
	var skipped []J
	got := EN.fwListSources(p.ListSourcesIn, &skipped) // script Python lancé en anglais
	var a, b, c, d any
	json.Unmarshal([]byte(asJSON(got)), &a)
	json.Unmarshal([]byte(asJSON(p.ListSources)), &b)
	json.Unmarshal([]byte(asJSON(skipped)), &c)
	json.Unmarshal([]byte(asJSON(p.ListSkipped)), &d)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("sources Go : %s\nPython : %s", asJSON(a), asJSON(b))
	}
	if !reflect.DeepEqual(c, d) {
		t.Errorf("écartées Go : %s\nPython : %s", asJSON(c), asJSON(d))
	}
}

func TestFirewallParsePortsAndPanelPort(t *testing.T) {
	setup(t)
	cfgUpdate(Cfg{"bind_port": float64(8765)})
	for _, bad := range []string{"", "abc", "0", "70000", "9-3", "1-2-3"} {
		if _, err := FR.fwParsePorts(bad); err == nil {
			t.Errorf("%q accepté", bad)
		}
	}
	if _, err := FR.fwNormalizeRule(map[string]any{"mode": "allow", "ports": "8765"}, 0); err == nil {
		t.Fatal("port du panel seul accepté")
	}
	ranges, _ := FR.fwRuleRanges(J{"ports": "8700-8800"}, nil)
	if rangesText(ranges) != "8700-8764, 8766-8800" {
		t.Fatal(rangesText(ranges))
	}
}

// Application : le texte part dans « nft -f - », une erreur ne change rien.
func TestFirewallSaveAppliesWithNft(t *testing.T) {
	e := setup(t)
	insts := e.fakeInstances()
	_ = insts
	var applied []string
	execCmd = func(args []string, _ time.Duration, input *string) (bool, string, string) {
		switch strings.Join(args, " ") {
		case "nft -f -":
			applied = append(applied, *input)
			return true, "", ""
		case "nft --version":
			return true, "nftables v1.0.6", ""
		case "nft list tables inet":
			return true, "table inet frp_manager", ""
		}
		return false, "", "absent"
	}
	c := e.owner()
	r := c.post("/api/firewall", J{"enabled": true, "rules": []any{J{"name": "SSH", "mode": "block", "ports": "7000", "sources": []any{J{"cidr": "203.0.113.4"}}}}})
	expectCode(t, r, 200)
	if len(applied) != 1 || !strings.Contains(applied[0], "elements = { 203.0.113.4/32 }") {
		t.Fatalf("%v", applied)
	}
	if fw := e.savedConfig()["firewall"].(map[string]any); fw["enabled"] != true || len(fw["rules"].([]any)) != 1 {
		t.Fatalf("%v", fw)
	}
	g := c.get("/api/firewall").json()
	if g["available"] != true || g["active"] != true || len(g["rules"].([]any)) != 1 {
		t.Fatalf("%v", g)
	}
	// nftables refuse : rien n'est enregistré
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		return false, "", "Error: syntax error"
	}
	expectCode(t, c.post("/api/firewall", J{"enabled": false, "rules": []any{}}), 500)
	if e.savedConfig()["firewall"].(map[string]any)["enabled"] != true {
		t.Fatal("config modifiée malgré le refus")
	}
	// Règle invalide
	expectCode(t, c.post("/api/firewall", J{"enabled": true, "rules": []any{J{"mode": "block", "ports": "7000"}}}), 400)
}

func TestFirewallNeedsFrps(t *testing.T) {
	e := setup(t)
	c := e.owner()
	expectCode(t, c.post("/api/firewall", J{"enabled": true, "rules": []any{}}), 400)
	if c.get("/api/firewall").json()["has_frps"] != false {
		t.Fatal("frps détecté")
	}
}

// Plusieurs frps : « tous les ports » d'une règle visent ceux de son frps ; une
// règle sans frps (créée avant ce choix) garde tous les ports, comme avant.
// Several frps: a rule's "all ports" target its frps's ones; a rule without frps
// (created before this choice) keeps every port, as before.
func TestFirewallRulePerFrps(t *testing.T) {
	setup(t)
	known := []J{
		{"start": 7000, "end": 7000, "proto": "tcp", "frps": "frps"},
		{"start": 6000, "end": 6010, "proto": "any", "frps": "frps"},
		{"start": 17307, "end": 17307, "proto": "tcp", "frps": "frps-secours"},
		{"start": 9000, "end": 9000, "proto": "tcp", "frps": ""},
	}
	cases := map[string]string{"": "7000, 6000-6010, 17307, 9000", "frps": "7000, 6000-6010", "frps-secours": "17307"}
	for frps, want := range cases {
		rule := J{"ports": "*", "frps": frps}
		ranges, err := FR.fwRuleRanges(rule, known)
		if err != nil || rangesText(ranges) != want {
			t.Errorf("frps %q : %s %v, attendu %s", frps, rangesText(ranges), err, want)
		}
	}
	// Ports donnés : le frps ne change rien / Given ports: the frps changes nothing
	if r, _ := FR.fwRuleRanges(J{"ports": "25565", "frps": "frps-secours"}, known); rangesText(r) != "25565" {
		t.Errorf("ports donnés : %s", rangesText(r))
	}
	rule, err := FR.fwNormalizeRule(map[string]any{"mode": "allow", "ports": "*", "frps": "frps-secours"}, 0)
	if err != nil || rule["frps"] != "frps-secours" {
		t.Fatalf("%v %v", rule, err)
	}
	if rule, _ := FR.fwNormalizeRule(map[string]any{"mode": "allow", "ports": "*"}, 0); rule["frps"] != nil {
		t.Fatalf("frps ajouté à une règle sans frps : %v", rule)
	}
	if _, err := FR.fwNormalizeRule(map[string]any{"mode": "allow", "ports": "*", "frps": "frps; nft flush"}, 0); err == nil {
		t.Fatal("frps invalide accepté")
	}
	// Ruleset : la règle de frps-secours ne touche que 17307
	// Ruleset: the frps-secours rule only touches 17307
	nft := fwRuleset(fwConfig{true, []J{{"id": "ab", "mode": "allow", "ports": "*", "frps": "frps-secours", "sources": []any{}}}}, known, false)
	if !strings.Contains(nft, "elements = { 17307 }") || strings.Contains(nft, "7000") {
		t.Fatalf("%s", nft)
	}
}
