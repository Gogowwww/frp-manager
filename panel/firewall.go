// Pare-feu frps (nftables).
//
// Filtre, dans le noyau de la machine frps, qui peut se connecter aux ports
// qu'ouvre frps. Tout vit dans une table dédiée, remplacée d'un bloc à chaque
// application (nft -f est atomique : une erreur ne change rien) :
//   - hook prerouting, priorité -150 : avant le DNAT de Docker, donc un frps en
//     conteneur est filtré aussi, et on voit la vraie IP du client ;
//   - la table ne fait que JETER des connexions, elle n'en autorise aucune :
//     ufw/Docker restent seuls juges de ce qui est ouvert ;
//   - seules les nouvelles connexions vers une adresse de la machine sont
//     examinées ; la boucle locale ne l'est jamais, le port du panel non plus.
// Une connexion doit passer chaque règle active qui concerne son port.
// Si le panel s'arrête, la table reste en place ; au démarrage il la réapplique.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	toml "github.com/pelletier/go-toml/v2"
	"golang.org/x/text/unicode/norm"
)

const (
	fwTable         = "frp_manager"
	fwSyncInterval  = 60 * time.Second
	fwSeenTimeout   = 86400 // une adresse bloquée reste listée 24 h après sa dernière tentative
	fwSeenSize      = 4096  // adresses retenues au plus, par règle et par famille
	fwLiveInterval  = time.Second
	fwSeenFullEvery = 60 * time.Second
	fwKnownTTL      = 15 * time.Second

	// Systèmes autonomes (AS) : préfixes annoncés, via RIPEstat. Gardés sur
	// disque et rafraîchis chaque jour ; si RIPEstat est injoignable, les
	// derniers préfixes connus restent utilisés.
	fwASNMaxAge = 86400
	ripestat    = "https://stat.ripe.net/data"

	// Listes de blocage communautaires : catalogue publié sur la branche
	// « blocklists » du dépôt GitHub public, gardé sur disque.
	fwListsBranch    = "blocklists"
	fwListsMaxAge    = 6 * 3600
	fwListsRetry     = 60 // après un échec, pas de nouvel essai avant 1 min
	fwListMaxSources = 20000
	fwListMaxASNs    = 50
)

var (
	fwASNFile   = "/var/lib/frp-manager/asn-cache.json"
	fwListsFile = "/var/lib/frp-manager/blocklists-cache.json"
	fwListsURL  = func() string {
		if u := os.Getenv("FRP_MANAGER_BLOCKLISTS_URL"); u != "" {
			return u
		}
		return "https://raw.githubusercontent.com/" + panelGitHubRepo + "/" + fwListsBranch + "/blocklists.json"
	}()
	fwListIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	asRe       = regexp.MustCompile(`(?i)^AS\s*(\d{1,10})$`)

	fwMu       sync.Mutex
	fwApplied  = ""   // dernier jeu de règles chargé avec succès
	fwSeenOK   = true // false si ce nftables refuse les ensembles dynamiques
	fwSeenOKMu sync.Mutex
)

// ── Sources d'une règle ─────────────────────────────────────────────────────

// fwSource : « 203.0.113.4 », « 198.51.100.0/24 » ou « AS16276 » → source de règle.
func (l Lang) fwSource(text, note string) (J, error) {
	text = strings.TrimSpace(text)
	if m := asRe.FindStringSubmatch(text); m != nil {
		asn, _ := strconv.ParseUint(m[1], 10, 64)
		if asn < 1 || asn > 4294967295 {
			return nil, errors.New(l.M("« "+text+" » n'est pas un numéro d'AS valide", "“"+text+"” is not a valid AS number"))
		}
		return J{"asn": int(asn), "note": note}, nil
	}
	p, ok := parseNetwork(text)
	if !ok {
		return nil, errors.New(l.M("« "+text+" » n'est ni une adresse IP, ni un réseau (203.0.113.0/24), ni un AS (AS16276)",
			"“"+text+"” is neither an IP address, a network (203.0.113.0/24) nor an AS (AS16276)"))
	}
	return J{"cidr": netString(p), "note": note}, nil
}

func srcASN(s map[string]any) int {
	n, _ := pyInt(s["asn"])
	if !truthy(s["asn"]) {
		return 0
	}
	return n
}

// fwListSources : entrées d'une liste communautaire (« AS16276  # note », ou
// {cidr|asn, note}) → sources. Écartés (notés dans skipped) : l'illisible, les
// réseaux privés ou réservés, ceux plus larges qu'un /8 (/16 en IPv6), et les AS
// au-delà de 50.
func (l Lang) fwListSources(items []any, skipped *[]J) []J {
	var sources []J
	seen := map[string]bool{}
	asns := 0
	skip := func(text, why string) {
		if skipped != nil {
			*skipped = append(*skipped, J{"entry": text, "reason": why})
		}
	}
	for _, item := range items {
		if len(sources) >= fwListMaxSources {
			skip("…", l.M(fmt.Sprintf("liste limitée à %d entrées", fwListMaxSources), fmt.Sprintf("list limited to %d entries", fwListMaxSources)))
			break
		}
		var text, note string
		if m, ok := item.(map[string]any); ok {
			if truthy(m["asn"]) {
				text = "AS" + pyStr(m["asn"])
			} else if truthy(m["cidr"]) {
				text = pyStr(m["cidr"])
			}
			if truthy(m["note"]) {
				note = pyStr(m["note"])
			}
		} else {
			text, note, _ = strings.Cut(pyStr(item), "#")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		src, err := l.fwSource(text, truncRunes(strings.TrimSpace(note), 60))
		if err != nil {
			skip(text, err.Error())
			continue
		}
		key := ""
		if asn, ok := src["asn"].(int); ok {
			if asns >= fwListMaxASNs {
				skip(text, l.M(fmt.Sprintf("%d AS au plus par liste", fwListMaxASNs), fmt.Sprintf("%d AS at most per list", fwListMaxASNs)))
				continue
			}
			asns++
			key = "AS" + strconv.Itoa(asn)
		} else {
			p, _ := parseNetwork(src["cidr"].(string))
			if !prefixIsGlobal(p) {
				skip(text, l.M("adresse privée ou réservée", "private or reserved address"))
				continue
			}
			minBits := 8
			if p.Addr().Is6() {
				minBits = 16
			}
			if p.Bits() < minBits {
				skip(text, l.M("réseau trop large", "network too wide"))
				continue
			}
			key = src["cidr"].(string)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		sources = append(sources, src)
	}
	return sources
}

// ── Catalogue des listes communautaires ─────────────────────────────────────

type listsCache struct {
	Fetched int64        `json:"fetched"`
	Lists   map[string]J `json:"lists"`
}

var (
	listsMu      sync.Mutex
	listsVal     *listsCache
	listsFailAt  float64
	listsFailMsg string
)

// fwListNormalize : une liste du catalogue → liste propre, ou nil si inutilisable.
func fwListNormalize(raw any) J {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	id := ""
	if truthy(m["id"]) {
		id = pyStr(m["id"])
	}
	if !fwListIDRe.MatchString(id) {
		return nil
	}
	items, _ := m["sources"].([]any)
	sources := FR.fwListSources(items, nil)
	if len(sources) == 0 {
		return nil
	}
	field := func(k string, n int, def string) string {
		v := def
		if truthy(m[k]) {
			v = pyStr(m[k])
		}
		return truncRunes(strings.TrimSpace(v), n)
	}
	mode := "block"
	if m["mode"] == "allow" {
		mode = "allow"
	}
	return J{"id": id, "name": field("name", 60, id), "description": field("description", 300, ""),
		"author": field("author", 40, ""), "updated": field("updated", 10, ""), "mode": mode, "sources": sources}
}

func listsAll() *listsCache {
	listsMu.Lock()
	defer listsMu.Unlock()
	if listsVal == nil {
		listsVal = &listsCache{Lists: map[string]J{}}
		if raw, err := os.ReadFile(fwListsFile); err == nil {
			var c listsCache
			if json.Unmarshal(raw, &c) == nil && c.Lists != nil {
				listsVal = &c
			}
		}
	}
	return listsVal
}

// listsFetch télécharge le catalogue.
func listsFetch() (*listsCache, error) {
	resp, err := httpGet(fwListsURL, 15*time.Second, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%d Client Error for url: %s", resp.StatusCode, fwListsURL)
	}
	var data map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&data); err != nil {
		return nil, err
	}
	lists := map[string]J{}
	raw, _ := data["lists"].([]any)
	for _, r := range raw {
		if lst := fwListNormalize(r); lst != nil {
			if _, dup := lists[lst["id"].(string)]; !dup {
				lists[lst["id"].(string)] = lst
			}
		}
	}
	c := &listsCache{Fetched: time.Now().Unix(), Lists: lists}
	listsMu.Lock()
	listsVal = c
	listsMu.Unlock()
	if b, err := json.Marshal(c); err == nil {
		os.MkdirAll(filepath.Dir(fwListsFile), 0o755)
		if err := os.WriteFile(fwListsFile, b, 0o644); err != nil {
			log.Printf("[WARN] Catalogue des listes non enregistré : %v", err)
		}
	}
	return c, nil
}

// listsGet : catalogue de moins de maxAge secondes, retéléchargé au besoin.
// → (catalogue, erreur) : en cas d'échec, le dernier connu et le message.
func listsGet(maxAge float64) (*listsCache, string) {
	c := listsAll()
	now := nowFunc()
	if now-float64(c.Fetched) < maxAge {
		return c, ""
	}
	listsMu.Lock()
	failAt, failMsg := listsFailAt, listsFailMsg
	listsMu.Unlock()
	if now-failAt < fwListsRetry {
		return c, failMsg
	}
	fresh, err := listsFetch()
	if err != nil {
		listsMu.Lock()
		listsFailAt, listsFailMsg = nowFunc(), err.Error()
		listsMu.Unlock()
		return c, err.Error()
	}
	return fresh, ""
}

func fwRuleList(rule J) J {
	id, _ := rule["list"].(string)
	if id == "" {
		return nil
	}
	return listsAll().Lists[id]
}

// fwRuleSources : les sources d'une règle, plus celles de sa liste communautaire.
func fwRuleSources(rule J) []map[string]any {
	var out []map[string]any
	for _, s := range listv(rule, "sources") {
		if m, ok := s.(map[string]any); ok {
			out = append(out, m)
		}
	}
	if lst := fwRuleList(rule); lst != nil {
		switch srcs := lst["sources"].(type) {
		case []J:
			for _, s := range srcs {
				out = append(out, s)
			}
		case []any:
			for _, s := range srcs {
				if m, ok := s.(map[string]any); ok {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// fwEnsureLists : catalogue à jour pour les listes des règles → message d'erreur.
func (l Lang) fwEnsureLists(rules []J) string {
	wanted := map[string]bool{}
	for _, r := range rules {
		if id, _ := r["list"].(string); id != "" {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return ""
	}
	c, errMsg := listsGet(fwListsMaxAge)
	missingIn := func(c *listsCache) []string {
		var miss []string
		for id := range wanted {
			if _, ok := c.Lists[id]; !ok {
				miss = append(miss, id)
			}
		}
		sort.Strings(miss)
		return miss
	}
	if len(missingIn(c)) > 0 && errMsg == "" {
		c, errMsg = listsGet(0) // ajoutée depuis le dernier téléchargement ?
	}
	missing := missingIn(c)
	if len(missing) == 0 {
		return ""
	}
	names := strings.Join(missing, ", ")
	msg := l.M("Liste communautaire introuvable dans le catalogue : "+names, "Community list not found in the catalog: "+names)
	if errMsg != "" {
		return msg + l.M(" (catalogue injoignable : "+errMsg+")", " (catalog unreachable: "+errMsg+")")
	}
	return msg + l.M(" — retirée ? supprimez la règle qui s'y abonne", ": removed? Delete the rule that subscribes to it")
}

// listsInfo : {id: résumé} des listes auxquelles des règles s'abonnent (nil si inconnue).
func listsInfo(rules []J) J {
	info := J{}
	for _, rule := range rules {
		id, _ := rule["list"].(string)
		if id == "" {
			continue
		}
		if lst := fwRuleList(rule); lst != nil {
			info[id] = J{"name": lst["name"], "author": lst["author"], "updated": lst["updated"], "count": lenList(lst["sources"])}
		} else {
			info[id] = nil
		}
	}
	return info
}

func lenList(v any) int {
	switch x := v.(type) {
	case []J:
		return len(x)
	case []any:
		return len(x)
	}
	return 0
}

// ── Systèmes autonomes ──────────────────────────────────────────────────────

type asnEntry struct {
	Holder  string   `json:"holder"`
	V4      []string `json:"v4"`
	V6      []string `json:"v6"`
	Fetched int64    `json:"fetched"`
}

var (
	asnMu      sync.Mutex
	asnVal     map[string]*asnEntry
	ipASNMu    sync.Mutex
	ipASNCache = map[string]J{} // adresse → {"asn", "holder"}, ou nil sans AS
	ipASNBusy  sync.Mutex
)

func asnAll() map[string]*asnEntry {
	asnMu.Lock()
	defer asnMu.Unlock()
	if asnVal == nil {
		asnVal = map[string]*asnEntry{}
		if raw, err := os.ReadFile(fwASNFile); err == nil {
			json.Unmarshal(raw, &asnVal)
		}
	}
	return asnVal
}

func asnLookup(asn int) *asnEntry {
	all := asnAll()
	asnMu.Lock()
	defer asnMu.Unlock()
	return all[strconv.Itoa(asn)]
}

func asnStore(asn int, e *asnEntry) {
	asnAll()
	asnMu.Lock()
	asnVal[strconv.Itoa(asn)] = e
	b, _ := json.Marshal(asnVal)
	asnMu.Unlock()
	os.MkdirAll(filepath.Dir(fwASNFile), 0o755)
	if err := os.WriteFile(fwASNFile, b, 0o644); err != nil {
		log.Printf("[WARN] Cache des AS non enregistré : %v", err)
	}
}

func ripestatGet(endpoint, resource string, timeout time.Duration) (map[string]any, error) {
	q := url.Values{"resource": {resource}, "sourceapp": {"frp-manager"}}
	resp, err := httpGet(ripestat+"/"+endpoint+"/data.json?"+q.Encode(), timeout, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return nil, err
	}
	return mapv(body, "data"), nil
}

var asPrefixRe = regexp.MustCompile(`^AS\d+\s+`)

func asnHolder(asn int) string {
	d, err := ripestatGet("as-overview", fmt.Sprintf("AS%d", asn), 8*time.Second)
	if err != nil {
		return ""
	}
	// « AS3215 Orange S.A. » → « Orange S.A. »
	return asPrefixRe.ReplaceAllString(strings.TrimSpace(str(d, "holder", "")), "")
}

// asnFetch : préfixes annoncés par un AS (regroupés), et son nom.
func asnFetch(asn int) (*asnEntry, error) {
	d, err := ripestatGet("announced-prefixes", fmt.Sprintf("AS%d", asn), 20*time.Second)
	if err != nil {
		return nil, err
	}
	var v4, v6 []netip.Prefix
	for _, item := range listv(d, "prefixes") {
		m, _ := item.(map[string]any)
		p, ok := parseNetwork(str(m, "prefix", ""))
		if !ok {
			continue
		}
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	e := &asnEntry{Holder: asnHolder(asn), V4: prefixStrings(collapsePrefixes(v4)), V6: prefixStrings(collapsePrefixes(v6)), Fetched: time.Now().Unix()}
	asnStore(asn, e)
	return e, nil
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// asnGet : préfixes d'un AS, depuis le cache, sinon chez RIPEstat.
func asnGet(asn int) (*asnEntry, error) {
	if e := asnLookup(asn); e != nil {
		return e, nil
	}
	return asnFetch(asn)
}

func fwRuleASNs(rules []J) []int {
	set := map[int]bool{}
	for _, r := range rules {
		for _, s := range fwRuleSources(r) {
			if asn := srcASN(s); asn != 0 {
				set[asn] = true
			}
		}
	}
	out := make([]int, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Ints(out)
	return out
}

func asnInfo(asns []int) J {
	info := J{}
	for _, asn := range asns {
		if e := asnLookup(asn); e != nil {
			info[strconv.Itoa(asn)] = J{"holder": e.Holder, "prefixes": len(e.V4) + len(e.V6), "fetched": e.Fetched}
		} else {
			info[strconv.Itoa(asn)] = nil
		}
	}
	return info
}

// ipASNLookup : AS des adresses déjà connues. Les inconnues sont cherchées en
// arrière-plan (réponse au prochain rafraîchissement) : la page n'attend jamais RIPEstat.
func ipASNLookup(ips []string) map[string]any {
	ipASNMu.Lock()
	var todo []string
	for _, ip := range ips {
		if _, ok := ipASNCache[ip]; !ok {
			todo = append(todo, ip)
		}
	}
	ipASNMu.Unlock()
	if len(todo) > 0 && ipASNBusy.TryLock() {
		if len(todo) > 25 {
			todo = todo[:25]
		}
		go func(batch []string) {
			defer ipASNBusy.Unlock()
			for _, ip := range batch {
				d, err := ripestatGet("network-info", ip, 6*time.Second)
				if err != nil {
					continue // réessayé au prochain rafraîchissement
				}
				var res J
				if list := listv(d, "asns"); len(list) > 0 {
					if asn, ok := pyInt(list[0]); ok && asn != 0 {
						holder := ""
						if known := asnLookup(asn); known != nil {
							holder = known.Holder
						} else {
							holder = asnHolder(asn)
						}
						res = J{"asn": asn, "holder": holder}
					}
				}
				ipASNMu.Lock()
				ipASNCache[ip] = res
				ipASNMu.Unlock()
			}
		}(todo)
	}
	ipASNMu.Lock()
	defer ipASNMu.Unlock()
	out := map[string]any{}
	for _, ip := range ips {
		if v, ok := ipASNCache[ip]; ok {
			if v == nil {
				out[ip] = nil
			} else {
				out[ip] = v
			}
		}
	}
	return out
}

// ── Configuration et ports connus ───────────────────────────────────────────

type fwConfig struct {
	Enabled bool
	Rules   []J
}

func fwCfg() fwConfig {
	fw := mapv(cfg(), "firewall")
	var rules []J
	for _, r := range listv(fw, "rules") {
		if m, ok := r.(map[string]any); ok {
			rules = append(rules, m)
		}
	}
	return fwConfig{truthy(fw["enabled"]), rules}
}

func frpsInstances() []*Instance {
	var out []*Instance
	insts := instancesSnapshot()
	for _, k := range sortedKeys(insts) {
		if insts[k].Type == "frps" {
			out = append(out, insts[k])
		}
	}
	return out
}

func fwPanelPort() int {
	p, ok := pyInt(cfg()["bind_port"])
	if !ok || p == 0 {
		return 8765
	}
	return p
}

func frpsConfigText(inst *Instance) (string, bool) {
	if inst.Source == "docker" {
		st, data := dockerAPI("GET", "/containers/"+inst.ContainerName+"/json", nil)
		ins, ok := data.(map[string]any)
		if st != 200 || !ok {
			return "", false
		}
		p := dockerMountedConfig(ins, "frps")
		if p == "" {
			return "", false
		}
		return hostReadFile(p)
	}
	if inst.Config == "" || !fileExists(inst.Config) {
		return "", false
	}
	b, err := os.ReadFile(inst.Config)
	return string(b), err == nil
}

// tomlInt : entier d'une valeur TOML (0 si absente ou illisible).
func tomlInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

type activeProxy struct {
	port   int
	proto  string
	name   string
	online bool
}

// frpsActiveProxies : proxys connectés, via l'API du tableau de bord frps.
func frpsActiveProxies(conf map[string]any) []activeProxy {
	ws := mapv(conf, "webServer")
	port := tomlInt(ws["port"])
	if port == 0 {
		return nil
	}
	addr := str(ws, "addr", "")
	if addr == "" || addr == "0.0.0.0" || addr == "::" {
		addr = "127.0.0.1"
	}
	var found []activeProxy
	for _, ptype := range []string{"tcp", "udp"} {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/proxy/%s", netJoin(addr, port), ptype), nil)
		if user := str(ws, "user", ""); user != "" {
			req.SetBasicAuth(user, str(ws, "password", ""))
		}
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		if err != nil {
			continue
		}
		var body map[string]any
		ok := resp.StatusCode < 400 && json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body) == nil
		resp.Body.Close()
		if !ok {
			continue
		}
		for _, item := range listv(body, "proxies") {
			p, _ := item.(map[string]any)
			rp := tomlInt(mapv(p, "conf")["remotePort"])
			if rp == 0 {
				rp, _ = pyInt(p["remotePort"])
			}
			if rp != 0 {
				found = append(found, activeProxy{rp, ptype, str(p, "name", ""), p["status"] == "online"})
			}
		}
	}
	return found
}

func netJoin(host string, port int) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}

var (
	fwKnownMu   sync.Mutex
	fwKnownAt   time.Time
	fwKnownList []J
)

// fwKnownPorts : ports ouverts par le(s) frps de la machine, gardés 15 s.
func fwKnownPorts() []J {
	fwKnownMu.Lock()
	defer fwKnownMu.Unlock()
	if time.Since(fwKnownAt) >= fwKnownTTL {
		fwKnownList = fwKnownPortsRead()
		fwKnownAt = time.Now()
	}
	return fwKnownList
}

// fwKnownPortsRead → [{start, end, proto, label, kind, online, frps}] ; frps :
// l'instance qui ouvre le port ("" si on ne sait pas laquelle).
// fwKnownPortsRead → [{start, end, proto, label, kind, online, frps}]; frps: the
// instance that opens the port ("" when unknown).
func fwKnownPortsRead() []J {
	known := []J{}
	seen := map[string]bool{}
	owner := ""
	add := func(start, end int, proto, label, kind string, online any) {
		key := fmt.Sprintf("%d-%d-%s-%s", start, end, proto, owner)
		if seen[key] || !(1 <= start && start <= end && end <= 65535) {
			return
		}
		seen[key] = true
		known = append(known, J{"start": start, "end": end, "proto": proto, "label": label, "kind": kind, "online": online, "frps": owner})
	}
	instances := frpsInstances()
	for _, inst := range instances {
		owner = inst.ID
		text, ok := frpsConfigText(inst)
		if !ok {
			continue
		}
		var conf map[string]any
		if toml.Unmarshal([]byte(text), &conf) != nil || conf == nil {
			// Config illisible : lecture simple
			for _, p := range extractPortsFromConfig(text, "frps") {
				add(p["port"].(int), p["port"].(int), p["proto"].(string), p["label"].(string), "config", nil)
			}
			continue
		}
		bind := tomlInt(conf["bindPort"])
		if bind == 0 {
			bind = 7000
		}
		add(bind, bind, "tcp", "Connexion des clients frpc", "config", nil)
		for _, e := range []struct{ key, proto, label string }{
			{"kcpBindPort", "udp", "KCP"}, {"quicBindPort", "udp", "QUIC"},
			{"vhostHTTPPort", "tcp", "Sites HTTP (vhost)"}, {"vhostHTTPSPort", "tcp", "Sites HTTPS (vhost)"},
			{"tcpmuxHTTPConnectPort", "tcp", "TCP mux"},
		} {
			if p := tomlInt(conf[e.key]); p != 0 {
				add(p, p, e.proto, e.label, "config", nil)
			}
		}
		ws := mapv(conf, "webServer")
		wsAddr := "127.0.0.1"
		if v, ok := ws["addr"]; ok {
			wsAddr = pyStr(v)
		}
		if p := tomlInt(ws["port"]); p != 0 && wsAddr != "127.0.0.1" && wsAddr != "localhost" && wsAddr != "::1" {
			add(p, p, "tcp", "Tableau de bord frps", "config", nil)
		}
		if ranges, ok := conf["allowPorts"].([]any); ok {
			for _, r := range ranges {
				rng, _ := r.(map[string]any)
				if s := tomlInt(rng["single"]); s != 0 {
					add(s, s, "any", "Port réservé aux clients", "range", nil)
				} else if a, b := tomlInt(rng["start"]), tomlInt(rng["end"]); a != 0 && b != 0 {
					add(a, b, "any", "Plage réservée aux clients", "range", nil)
				}
			}
		}
		for _, p := range frpsActiveProxies(conf) {
			add(p.port, p.port, p.proto, "Port « "+p.name+" »", "proxy", p.online)
		}
	}
	// Ports réellement ouverts : sans tableau de bord frps ni allowPorts, ceux
	// des clients ne sont connus que par là.
	for _, lp := range frpsListeningPorts() {
		// Un seul frps : le port est forcément le sien
		// A single frps: the port is necessarily its own
		owner = lp.owner
		if owner == "" && len(instances) == 1 {
			owner = instances[0].ID
		}
		covered := false
		for _, k := range known {
			if k["start"].(int) <= lp.port && lp.port <= k["end"].(int) && (k["proto"] == lp.proto || k["proto"] == "any") {
				covered = true
				break
			}
		}
		if !covered {
			add(lp.port, lp.port, lp.proto, "Port ouvert par frps", "listen", nil)
		}
	}
	sort.SliceStable(known, func(i, j int) bool {
		if known[i]["start"].(int) != known[j]["start"].(int) {
			return known[i]["start"].(int) < known[j]["start"].(int)
		}
		return known[i]["end"].(int) < known[j]["end"].(int)
	})
	return known
}

type portProto struct {
	port  int
	proto string
	owner string // instance frps, "" si inconnue / frps instance, "" if unknown
}

var ssPIDRe = regexp.MustCompile(`\(\("frps",pid=(\d+)`)

// frpsPIDs : PID du processus principal → instance frps (services systemd et
// programmes lancés par le panel).
// frpsPIDs: PID of the main process → frps instance (systemd services and
// programs run by the panel).
func frpsPIDs(instances []*Instance) map[string]string {
	out := map[string]string{}
	var units []string
	for _, inst := range instances {
		switch inst.Source {
		case "standalone":
			p := procFor(inst.ID)
			p.mu.Lock()
			if p.cmd != nil && p.cmd.Process != nil {
				out[strconv.Itoa(p.cmd.Process.Pid)] = inst.ID
			}
			p.mu.Unlock()
		case "systemd":
			if validServiceName(inst.Service) {
				units = append(units, inst.Service+".service")
			}
		}
	}
	for unit, props := range systemctlShow(units, []string{"MainPID"}) {
		if pid := props["MainPID"]; pid != "" && pid != "0" {
			out[pid] = strings.TrimSuffix(unit, ".service")
		}
	}
	return out
}

// frpsListeningPorts : ports sur lesquels frps écoute (processus de l'hôte, ou
// conteneur en réseau « host »), et ports publiés par les conteneurs frps.
func frpsListeningPorts() []portProto {
	set := map[portProto]bool{}
	instances := frpsInstances()
	var pids map[string]string
	if ok, out, _ := runHost([]string{"ss", "-H", "-lntup"}, 15*time.Second, nil); ok {
		for _, line := range strings.Split(out, "\n") {
			cols := strings.Fields(line)
			if len(cols) < 6 || !strings.Contains(line, `(("frps"`) {
				continue
			}
			i := strings.LastIndex(cols[4], ":")
			if i < 0 {
				continue
			}
			addr, port := cols[4][:i], cols[4][i+1:]
			a := strings.Trim(addr, "[]")
			if a == "127.0.0.1" || a == "::1" || a == "localhost" || strings.HasPrefix(addr, "127.") {
				continue // joignable seulement depuis la machine
			}
			if isDigits(port) {
				proto := "tcp"
				if cols[0] == "udp" {
					proto = "udp"
				}
				owner := ""
				if m := ssPIDRe.FindStringSubmatch(line); m != nil {
					if pids == nil {
						pids = frpsPIDs(instances)
					}
					owner = pids[m[1]]
				}
				set[portProto{atoi(port), proto, owner}] = true
			}
		}
	}
	for _, inst := range instances {
		if inst.Source != "docker" {
			continue
		}
		st, data := dockerAPI("GET", "/containers/"+inst.ContainerName+"/json", nil)
		ins, ok := data.(map[string]any)
		if st != 200 || !ok {
			continue
		}
		for spec, binds := range mapv(mapv(ins, "NetworkSettings"), "Ports") {
			proto := "tcp"
			if strings.HasSuffix(spec, "/udp") {
				proto = "udp"
			}
			list, _ := binds.([]any)
			for _, b := range list {
				bm, _ := b.(map[string]any)
				hp, hip := pyStr(bm["HostPort"]), str(bm, "HostIp", "")
				if bm["HostPort"] != nil && isDigits(hp) && !strings.HasPrefix(hip, "127.") && !strings.HasPrefix(hip, "::1") {
					set[portProto{atoi(hp), proto, inst.ID}] = true
				}
			}
		}
	}
	out := make([]portProto, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].port != out[j].port {
			return out[i].port < out[j].port
		}
		return out[i].proto < out[j].proto
	})
	return out
}

// ── Règles ──────────────────────────────────────────────────────────────────

type portRange struct{ a, b int }

var portPartRe = regexp.MustCompile(`^(\d{1,5})(?:-(\d{1,5}))?$`)

// fwParsePorts : '7000, 25565, 30000-30010' → plages.
func (l Lang) fwParsePorts(spec string) ([]portRange, error) {
	var ranges []portRange
	for _, part := range regexp.MustCompile(`[,\s]+`).Split(strings.TrimSpace(spec), -1) {
		if part == "" {
			continue
		}
		m := portPartRe.FindStringSubmatch(part)
		if m == nil {
			return nil, errors.New(l.M("« "+part+" » n'est pas un port ni une plage (ex. 30000-30010)",
				"“"+part+"” is neither a port nor a range (e.g. 30000-30010)"))
		}
		a, b := atoi(m[1]), atoi(m[1])
		if m[2] != "" {
			b = atoi(m[2])
		}
		if !(1 <= a && a <= b && b <= 65535) {
			return nil, errors.New(l.M("« "+part+" » : les ports vont de 1 à 65535, dans l'ordre", "“"+part+"”: ports go from 1 to 65535, in order"))
		}
		ranges = append(ranges, portRange{a, b})
	}
	if len(ranges) == 0 {
		return nil, errors.New(l.M("Indiquez au moins un port", "Enter at least one port"))
	}
	return ranges, nil
}

// fwRuleRanges : plages de ports d'une règle, sans le port du panel (jamais filtré).
// « Tous les ports » : ceux de son frps, ou de tous les frps pour une règle sans
// frps (règles créées avant le choix du frps).
// fwRuleRanges: port ranges of a rule, without the panel port (never filtered).
// "All ports": those of its frps, or of every frps for a rule without frps
// (rules created before the frps choice).
func (l Lang) fwRuleRanges(rule J, known []J) ([]portRange, error) {
	var ranges []portRange
	if rule["ports"] == "*" {
		frps := str(rule, "frps", "")
		for _, k := range known {
			if frps == "" || k["frps"] == frps {
				ranges = append(ranges, portRange{k["start"].(int), k["end"].(int)})
			}
		}
	} else {
		var err error
		if ranges, err = l.fwParsePorts(pyStr(rule["ports"])); err != nil {
			return nil, err
		}
	}
	panel := fwPanelPort()
	var out []portRange
	for _, r := range ranges {
		if r.a <= panel && panel <= r.b {
			if r.a < panel {
				out = append(out, portRange{r.a, panel - 1})
			}
			if panel < r.b {
				out = append(out, portRange{panel + 1, r.b})
			}
		} else {
			out = append(out, r)
		}
	}
	return out, nil
}

func rangesText(ranges []portRange) string {
	parts := make([]string, len(ranges))
	for i, r := range ranges {
		if r.a == r.b {
			parts[i] = strconv.Itoa(r.a)
		} else {
			parts[i] = fmt.Sprintf("%d-%d", r.a, r.b)
		}
	}
	return strings.Join(parts, ", ")
}

// RuleError : règle refusée, message à montrer tel quel.
type RuleError struct{ msg string }

func (e *RuleError) Error() string { return e.msg }

var nonHexRe = regexp.MustCompile(`[^0-9a-f]`)

// fwNormalizeRule : valide une règle venue de l'interface.
func (l Lang) fwNormalizeRule(raw map[string]any, index int) (J, error) {
	where := l.M(fmt.Sprintf("Règle %d", index+1), fmt.Sprintf("Rule %d", index+1))
	name := ""
	if truthy(raw["name"]) {
		name = truncRunes(strings.TrimSpace(pyStr(raw["name"])), 60)
	}
	if name != "" {
		where = l.M("Règle « "+name+" »", "Rule “"+name+"”")
	}
	fail := func(fr, en string) (J, error) { return nil, &RuleError{l.M(where+" : "+fr, where+": "+en)} }
	mode, _ := raw["mode"].(string)
	if mode != "allow" && mode != "block" {
		return fail("action inconnue", "unknown action")
	}
	ports := ""
	if truthy(raw["ports"]) {
		ports = strings.TrimSpace(pyStr(raw["ports"]))
	}
	if ports != "*" {
		ranges, err := l.fwParsePorts(ports)
		if err != nil {
			return fail(err.Error(), err.Error())
		}
		panel := fwPanelPort()
		if len(ranges) == 1 && ranges[0].a == panel && ranges[0].b == panel {
			return fail(fmt.Sprintf("le port du panel (%d) n'est jamais filtré ici", panel), fmt.Sprintf("the panel port (%d) is never filtered here", panel))
		}
		ports = rangesText(ranges)
	}
	sources := []any{}
	for _, item := range listv(raw, "sources") {
		src, _ := item.(map[string]any)
		note := ""
		if truthy(src["note"]) {
			note = truncRunes(strings.TrimSpace(pyStr(src["note"])), 60)
		}
		text := ""
		if truthy(src["asn"]) {
			text = "AS" + pyStr(src["asn"])
		} else if truthy(src["cidr"]) {
			text = strings.TrimSpace(pyStr(src["cidr"]))
		}
		if text == "" {
			continue
		}
		s, err := l.fwSource(text, note)
		if err != nil {
			return fail(err.Error(), err.Error())
		}
		sources = append(sources, s)
	}
	lst := ""
	if truthy(raw["list"]) {
		lst = strings.TrimSpace(pyStr(raw["list"]))
	}
	if lst != "" && !fwListIDRe.MatchString(lst) {
		return fail("liste communautaire « "+lst+" » invalide", "invalid community list “"+lst+"”")
	}
	if mode == "block" && len(sources) == 0 && lst == "" {
		return fail("indiquez au moins une adresse à bloquer", "enter at least one address to block")
	}
	id := ""
	if truthy(raw["id"]) {
		id = nonHexRe.ReplaceAllString(pyStr(raw["id"]), "")
		if len(id) > 12 {
			id = id[:12]
		}
	}
	if id == "" {
		id = tokenHex(4)
	}
	rule := J{"id": id, "name": name, "mode": mode, "ports": ports, "sources": sources, "enabled": raw["enabled"] != false}
	if lst != "" {
		rule["list"] = lst
	}
	// frps concerné (page Pare-feu) ; absent : tous, comme avant ce choix
	// frps concerned (Firewall page); missing: all of them, as before this choice
	if frps := strings.TrimSpace(str(raw, "frps", "")); frps != "" {
		if !validInstanceID(frps) {
			return fail("frps « "+frps+" » invalide", "invalid frps “"+frps+"”")
		}
		rule["frps"] = frps
	}
	return rule, nil
}

var (
	fwNetsMu    sync.Mutex
	fwNetsCache = map[string][]netip.Prefix{}
	fwNetsOrder []string
)

// fwRuleNets : réseaux d'une règle (adresses, liste communautaire, préfixes des
// AS), en cache tant que ni les sources, ni la liste, ni les AS ne changent.
func fwRuleNets(rule J) []netip.Prefix {
	var keyParts []any
	for _, s := range listv(rule, "sources") {
		m, _ := s.(map[string]any)
		keyParts = append(keyParts, []any{m["asn"], m["cidr"]})
	}
	var listKey any
	if id, _ := rule["list"].(string); id != "" {
		listKey = []any{id, listsAll().Fetched}
	}
	var asnKey []any
	for _, a := range fwRuleASNs([]J{rule}) {
		if e := asnLookup(a); e != nil {
			asnKey = append(asnKey, e.Fetched)
		} else {
			asnKey = append(asnKey, nil)
		}
	}
	kb, _ := json.Marshal([]any{keyParts, listKey, asnKey})
	key := string(kb)
	fwNetsMu.Lock()
	cached, ok := fwNetsCache[key]
	fwNetsMu.Unlock()
	if ok {
		return cached
	}
	var nets []netip.Prefix
	for _, s := range fwRuleSources(rule) {
		if asn := srcASN(s); asn != 0 {
			if e := asnLookup(asn); e != nil {
				for _, x := range append(append([]string{}, e.V4...), e.V6...) {
					if p, ok := parseNetwork(x); ok {
						nets = append(nets, p)
					}
				}
			}
		} else if p, ok := parseNetwork(str(s, "cidr", "")); ok {
			nets = append(nets, p)
		}
	}
	fwNetsMu.Lock()
	fwNetsCache[key] = nets
	fwNetsOrder = append(fwNetsOrder, key)
	if len(fwNetsOrder) > 64 {
		delete(fwNetsCache, fwNetsOrder[0])
		fwNetsOrder = fwNetsOrder[1:]
	}
	fwNetsMu.Unlock()
	return nets
}

func ruleEnabled(r J) bool { v, ok := r["enabled"]; return !ok || truthy(v) }

// fwRuleset : texte nft qui remplace la table (ou la supprime si rien n'est à filtrer).
func fwRuleset(c fwConfig, known []J, seen bool) string {
	head := "table inet " + fwTable + "\ndelete table inet " + fwTable + "\n"
	var rules []J
	if c.Enabled {
		for _, r := range c.Rules {
			if ruleEnabled(r) {
				rules = append(rules, r)
			}
		}
	}
	var sets, chain []string
	for n, rule := range rules {
		ranges, err := FR.fwRuleRanges(rule, known)
		if err != nil || len(ranges) == 0 {
			continue
		}
		rid := fmt.Sprintf("r%d", n)
		nets := fwRuleNets(rule)
		sets = append(sets, fmt.Sprintf("  set %s_ports { type inet_service; flags interval; auto-merge; elements = { %s } }", rid, rangesText(ranges)))
		type fam struct {
			version    int
			sel, match string
		}
		var families []fam
		for _, f := range []struct {
			name, sel string
			version   int
			stype     string
		}{{"ipv4", "ip", 4, "ipv4_addr"}, {"ipv6", "ip6", 6, "ipv6_addr"}} {
			var sub []netip.Prefix
			for _, p := range nets {
				if (f.version == 4) == p.Addr().Is4() {
					sub = append(sub, p)
				}
			}
			elems := strings.Join(prefixNftStrings(collapsePrefixes(sub)), ", ")
			body := "type " + f.stype + "; flags interval; auto-merge;"
			if elems != "" {
				body += " elements = { " + elems + " }"
			}
			sets = append(sets, fmt.Sprintf("  set %s_v%d { %s }", rid, f.version, body))
			if rule["mode"] == "allow" {
				families = append(families, fam{f.version, f.sel, fmt.Sprintf("meta nfproto %s th dport @%s_ports %s saddr != @%s_v%d", f.name, rid, f.sel, rid, f.version)})
			} else if elems != "" {
				families = append(families, fam{f.version, f.sel, fmt.Sprintf("meta nfproto %s th dport @%s_ports %s saddr @%s_v%d", f.name, rid, f.sel, rid, f.version)})
			}
		}
		id := pyStr(rule["id"])
		for _, f := range families {
			if seen {
				// Retient l'adresse bloquée (protocole, port, dernière tentative) : la
				// liste « Connexions bloquées ». Règle à part : un ensemble plein ne
				// doit jamais empêcher le blocage.
				seenSet := fmt.Sprintf("seen%d_%s", f.version, id)
				addr := "ipv4_addr"
				if f.version == 6 {
					addr = "ipv6_addr"
				}
				sets = append(sets, fmt.Sprintf("  set %s { type %s . inet_proto . inet_service; flags dynamic, timeout; timeout %ds; size %d; }",
					seenSet, addr, fwSeenTimeout, fwSeenSize))
				chain = append(chain, fmt.Sprintf("    %s update @%s { %s saddr . meta l4proto . th dport }", f.match, seenSet, f.sel))
			}
			chain = append(chain, fmt.Sprintf(`    %s counter drop comment "%s"`, f.match, id))
		}
	}
	if len(chain) == 0 {
		return head
	}
	lines := []string{"table inet " + fwTable + " {"}
	lines = append(lines, sets...)
	lines = append(lines, "  chain filter {",
		"    type filter hook prerouting priority -150; policy accept;",
		`    iifname "lo" return`,
		"    ct state != new return",
		"    fib daddr type != local return",
		"    meta l4proto != { tcp, udp } return")
	lines = append(lines, chain...)
	lines = append(lines, "  }", "}")
	return head + strings.Join(lines, "\n") + "\n"
}

// prefixNftStrings : réseaux pour nft (une adresse seule garde son /32 ou /128,
// comme str() d'un réseau Python).
func prefixNftStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

func seenEnabled() bool { fwSeenOKMu.Lock(); defer fwSeenOKMu.Unlock(); return fwSeenOK }

// fwApply charge la table nft correspondant à la config → (ok, message d'erreur).
func fwApply(c *fwConfig) (bool, string) {
	fwMu.Lock()
	defer fwMu.Unlock()
	cur := fwCfg()
	if c != nil {
		cur = *c
	}
	known := fwKnownPorts()
	text := fwRuleset(cur, known, seenEnabled())
	ok, out, e := runHost([]string{"nft", "-f", "-"}, 15*time.Second, &text)
	if !ok && seenEnabled() && strings.Contains(text, "flags dynamic") {
		// nftables trop ancien pour les ensembles dynamiques : on filtre quand
		// même, sans la liste des connexions bloquées.
		text = fwRuleset(cur, known, false)
		ok, out, e = runHost([]string{"nft", "-f", "-"}, 15*time.Second, &text)
		if ok {
			fwSeenOKMu.Lock()
			fwSeenOK = false
			fwSeenOKMu.Unlock()
			log.Printf("[WARN] Pare-feu : ensembles dynamiques refusés par nftables, liste des blocages désactivée")
		}
	}
	if ok {
		fwApplied = text
		fwLiveReset()
	}
	msg := e
	if msg == "" {
		msg = out
	}
	return ok, strings.TrimSpace(msg)
}

func fwTablePresent() bool {
	ok, out, _ := runHost([]string{"nft", "list", "tables", "inet"}, 15*time.Second, nil)
	return ok && strings.Contains(out, "table inet "+fwTable)
}

var seenSetRe = regexp.MustCompile(`^seen[46]_[0-9a-f]+$`)

func jsonNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// fwParseState : JSON de « nft -j list … » → (compteurs {id: paquets jetés}, adresses bloquées).
func fwParseState(items []any, now float64) (map[string]int, []J) {
	counts := map[string]int{}
	seen := []J{}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if rule, ok := item["rule"].(map[string]any); ok {
			if comment, _ := rule["comment"].(string); comment != "" {
				for _, e := range listv(rule, "expr") {
					expr, _ := e.(map[string]any)
					if counter, ok := expr["counter"].(map[string]any); ok {
						n, _ := jsonNum(counter["packets"])
						counts[comment] += int(n)
					}
				}
			}
		}
		st, ok := item["set"].(map[string]any)
		if !ok || !seenSetRe.MatchString(str(st, "name", "")) {
			continue
		}
		rid := strings.SplitN(str(st, "name", ""), "_", 2)[1]
		for _, el := range listv(st, "elem") {
			// Avec délai : {"elem": {"val": {"concat": [...]}, "timeout", "expires"}}
			meta := map[string]any{}
			var val any = el
			if m, ok := el.(map[string]any); ok {
				if inner, ok := m["elem"].(map[string]any); ok {
					meta = inner
				} else {
					meta = m
				}
				if v, ok := meta["val"]; ok {
					val = v
				} else {
					val = el
				}
			}
			vm, _ := val.(map[string]any)
			parts, _ := vm["concat"].([]any)
			if len(parts) != 3 {
				continue
			}
			proto := pyStr(parts[1])
			if n, ok := jsonNum(parts[1]); ok {
				switch int(n) {
				case 6:
					proto = "tcp"
				case 17:
					proto = "udp"
				default:
					proto = strconv.Itoa(int(n))
				}
			}
			timeout, ok := jsonNum(meta["timeout"])
			if !ok {
				timeout = fwSeenTimeout
			}
			expires, ok := jsonNum(meta["expires"])
			if !ok {
				expires = timeout
			}
			port, _ := jsonNum(parts[2])
			seen = append(seen, J{"src": pyStr(parts[0]), "proto": proto, "port": int(port), "rule": rid,
				"last": int64(now - max(0, timeout-expires))})
		}
	}
	sort.SliceStable(seen, func(i, j int) bool { return seen[i]["last"].(int64) > seen[j]["last"].(int64) })
	return counts, seen
}

func nftJSON(args ...string) []any {
	ok, out, _ := runHost(append([]string{"nft", "-j", "list"}, args...), 15*time.Second, nil)
	if !ok {
		return nil
	}
	var data map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if dec.Decode(&data) != nil {
		return nil
	}
	return listv(data, "nftables")
}

// fwCounters : {id de règle: paquets jetés}, lecture de la seule chaîne.
func fwCounters() map[string]int {
	items := nftJSON("chain", "inet", fwTable, "filter")
	if items == nil {
		return map[string]int{}
	}
	counts, _ := fwParseState(items, nowFunc())
	return counts
}

// Lecture « en direct », partagée par tous les onglets ouverts. Les ensembles
// d'une règle ne sont relus que quand son compteur a bougé, et tous une fois par
// minute (expirations au bout de 24 h).
type seenKey struct {
	rid     string
	version int
}

var (
	fwLiveMu           sync.Mutex
	fwLiveAt           time.Time
	fwLiveCounts       map[string]int
	fwLiveSeen         = map[seenKey][]J{}
	fwLiveFullAt       time.Time
	fwLiveCountsResult = map[string]int{}
	fwLiveSeenResult   = []J{}
)

// fwTableState → (compteurs, adresses bloquées), au plus une lecture par seconde.
func fwTableState() (map[string]int, []J) {
	fwLiveMu.Lock()
	defer fwLiveMu.Unlock()
	now := time.Now()
	if now.Sub(fwLiveAt) < fwLiveInterval {
		return fwLiveCountsResult, fwLiveSeenResult
	}
	fwLiveAt = now
	counts := fwCounters()
	full := fwLiveCounts == nil || now.Sub(fwLiveFullAt) >= fwSeenFullEvery
	changed := map[string]bool{}
	for rid, n := range counts {
		if old, ok := fwLiveCounts[rid]; full || !ok || old != n {
			changed[rid] = true
		}
	}
	for k := range fwLiveSeen {
		if _, ok := counts[k.rid]; !ok {
			delete(fwLiveSeen, k) // règle supprimée ou table recréée
		}
	}
	if seenEnabled() {
		ts := float64(now.UnixNano()) / 1e9
		for rid := range changed {
			for _, v := range []int{4, 6} {
				items := nftJSON("set", "inet", fwTable, fmt.Sprintf("seen%d_%s", v, rid))
				if items != nil {
					_, s := fwParseState(items, ts)
					fwLiveSeen[seenKey{rid, v}] = s
				} else {
					fwLiveSeen[seenKey{rid, v}] = []J{}
				}
			}
		}
	}
	if full {
		fwLiveFullAt = now
	}
	fwLiveCounts = counts
	all := []J{}
	for _, l := range fwLiveSeen {
		all = append(all, l...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i]["last"].(int64) > all[j]["last"].(int64) })
	fwLiveCountsResult, fwLiveSeenResult = counts, all
	return counts, all
}

// fwLiveReset : table remplacée (nouvelles règles), tout relire au prochain appel.
func fwLiveReset() {
	fwLiveMu.Lock()
	fwLiveAt, fwLiveCounts, fwLiveSeen = time.Time{}, nil, map[seenKey][]J{}
	fwLiveMu.Unlock()
}

// fwVerdicts : pour une adresse, chaque port frp connu et les règles qui la bloqueraient.
func fwVerdicts(ipText string, c fwConfig, known []J) []J {
	ip, err := netip.ParseAddr(ipText)
	if err != nil {
		return []J{}
	}
	ip = ip.WithZone("")
	type blocker struct {
		ranges []portRange
		name   string
	}
	var blockers []blocker
	for _, rule := range c.Rules {
		if !ruleEnabled(rule) {
			continue
		}
		ranges, err := FR.fwRuleRanges(rule, known)
		if err != nil {
			continue
		}
		listed := false
		for _, n := range fwRuleNets(rule) {
			if n.Addr().Is4() == ip.Is4() && n.Contains(ip) {
				listed = true
				break
			}
		}
		if (rule["mode"] == "allow") != listed { // cette règle bloquerait l'adresse
			name := pyStr(rule["id"])
			if truthy(rule["name"]) {
				name = pyStr(rule["name"])
			}
			blockers = append(blockers, blocker{ranges, name})
		}
	}
	out := make([]J, len(known))
	for i, k := range known {
		e := J{}
		for kk, v := range k {
			e[kk] = v
		}
		by := []string{}
		for _, b := range blockers {
			for _, r := range b.ranges {
				if r.a <= k["end"].(int) && k["start"].(int) <= r.b {
					by = append(by, b.name)
					break
				}
			}
		}
		e["blocked_by"] = by
		out[i] = e
	}
	return out
}

// fwSyncLoop garde la table à jour : ports des proxys qui changent, table
// effacée (rechargement de nftables, redémarrage), démarrage du panel.
func fwSyncLoop() {
	time.Sleep(5 * time.Second)
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[WARN] Pare-feu : %v", r)
				}
			}()
			c := fwCfg()
			if !c.Enabled {
				return
			}
			for _, r := range c.Rules {
				if truthy(r["list"]) && ruleEnabled(r) {
					if _, err := listsGet(fwListsMaxAge); err != "" {
						log.Printf("[WARN] Pare-feu : catalogue des listes communautaires non rafraîchi (%s)", err)
					}
					break
				}
			}
			for _, asn := range fwRuleASNs(c.Rules) {
				if e := asnLookup(asn); e == nil || time.Now().Unix()-e.Fetched > fwASNMaxAge {
					if _, err := asnFetch(asn); err != nil {
						log.Printf("[WARN] Pare-feu : préfixes de AS%d non rafraîchis (%v)", asn, err)
					}
				}
			}
			detectFrp(false)
			text := fwRuleset(c, fwKnownPorts(), seenEnabled())
			expect := strings.Contains(text, "chain filter")
			fwMu.Lock()
			applied := fwApplied
			fwMu.Unlock()
			if text != applied || (expect && !fwTablePresent()) {
				if ok, msg := fwApply(&c); !ok {
					log.Printf("[WARN] Pare-feu : application impossible (%s)", msg)
				}
			}
		}()
		time.Sleep(fwSyncInterval)
	}
}

func (c *Ctx) clientIP() string {
	ip, err := netip.ParseAddr(c.RemoteIP())
	if err != nil || ip.IsLoopback() {
		return ""
	}
	return ip.WithZone("").String()
}

// Gestionnaires de paquets reconnus → commandes d'installation de nftables
var nftInstallers = []struct {
	manager string
	steps   [][]string
}{
	{"apt-get", [][]string{{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update", "-q"},
		{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "-q", "nftables"}}},
	{"dnf", [][]string{{"dnf", "install", "-y", "nftables"}}},
	{"yum", [][]string{{"yum", "install", "-y", "nftables"}}},
	{"apk", [][]string{{"apk", "add", "--no-cache", "nftables"}}},
	{"pacman", [][]string{{"pacman", "-Sy", "--noconfirm", "--needed", "nftables"}}},
	{"zypper", [][]string{{"zypper", "--non-interactive", "install", "nftables"}}},
}

// fwBlockedPayload : adresses bloquées ces dernières 24 h, avec leur AS, et les
// compteurs de chaque règle.
func fwBlockedPayload() J {
	counts, seen := fwTableState()
	var public []string
	dedup := map[string]bool{}
	for _, e := range seen {
		src, _ := e["src"].(string)
		if a, err := netip.ParseAddr(src); err == nil && addrIsGlobal(a) && !dedup[src] {
			dedup[src] = true
			public = append(public, src)
		}
	}
	if len(public) > 200 {
		public = public[:200]
	}
	asn := ipASNLookup(public)
	entries := make([]J, 0, min(len(seen), 200))
	for i, e := range seen {
		if i >= 200 {
			break
		}
		c := J{}
		for k, v := range e {
			c[k] = v
		}
		c["as"] = asn[e["src"].(string)]
		entries = append(entries, c)
	}
	return J{"ok": true, "entries": entries, "total": len(seen), "counters": counts, "available": seenEnabled()}
}

func (c *Ctx) parseRules(data map[string]any) ([]J, error) {
	var rules []J
	for i, r := range listv(data, "rules") {
		m, _ := r.(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		rule, err := c.Lang.fwNormalizeRule(m, i)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func init() {
	route("GET /api/firewall", "api_firewall_get", perm(P("firewall.view"), func(c *Ctx) {
		detectFrp(false)
		frpsIDs := []string{}
		for _, inst := range frpsInstances() {
			frpsIDs = append(frpsIDs, inst.ID)
		}
		hasFrps := len(frpsIDs) > 0
		fc := fwCfg()
		ok, out, e := false, "", ""
		if hasFrps {
			ok, out, e = runHost([]string{"nft", "--version"}, 15*time.Second, nil)
		}
		if hasFrps && ok && standalone {
			// nft est dans l'image, mais sans NET_ADMIN le conteneur ne peut rien filtrer
			// nft is in the image, but without NET_ADMIN the container cannot filter anything
			if listed, _, le := runHost([]string{"nft", "list", "tables"}, 15*time.Second, nil); !listed {
				ok, out, e = false, "", le
			}
		}
		known := []J{}
		if hasFrps && ok {
			known = fwKnownPorts()
		}
		nft := strings.TrimSpace(out)
		if !ok {
			nft = strings.TrimSpace(e)
			if nft == "" {
				nft = c.M("nft introuvable", "nft not found")
			}
		}
		counters := map[string]int{}
		if ok {
			counters = fwCounters()
		}
		client := c.clientIP()
		verdicts := []J{}
		if client != "" && len(known) > 0 && fc.Enabled {
			verdicts = c.Lang.withPortLabels(fwVerdicts(client, fc, known))
		}
		rules := fc.Rules
		if rules == nil {
			rules = []J{}
		}
		c.JSON(J{
			"ok": true, "has_frps": hasFrps, "frps": frpsIDs, "available": ok, "nft": nft,
			"enabled": fc.Enabled, "rules": rules, "ports": c.Lang.withPortLabels(known),
			"active": ok && fwTablePresent(), "counters": counters,
			"asns": asnInfo(fwRuleASNs(fc.Rules)), "lists": listsInfo(fc.Rules),
			"client_ip": nullable(client), "client_verdicts": verdicts,
			"panel_port": fwPanelPort(), "in_docker": inDocker, "standalone": standalone,
		})
	}))

	// Installe nftables sur la machine (l'hôte en Docker) avec son gestionnaire de paquets.
	route("POST /api/firewall/install", "api_firewall_install", perm(P("firewall.manage"), func(c *Ctx) {
		if standalone {
			// L'image contient déjà nftables ; un paquet ajouté ici disparaîtrait avec le conteneur
			// The image already contains nftables; a package added here would vanish with the container
			c.Fail(http.StatusBadRequest, c.M("En mode autonome, nftables fait partie de l'image : donnez au conteneur la capacité NET_ADMIN.",
				"In standalone mode, nftables is part of the image: give the container the NET_ADMIN capability."))
			return
		}
		if ok, _, _ := runHost([]string{"nft", "--version"}, 15*time.Second, nil); ok {
			c.JSON(J{"ok": true, "msg": c.M("nftables est déjà installé.", "nftables is already installed.")})
			return
		}
		for _, inst := range nftInstallers {
			if ok, _, _ := runHost([]string{"sh", "-c", `command -v "$1"`, "sh", inst.manager}, 15*time.Second, nil); !ok {
				continue
			}
			for _, cmd := range inst.steps {
				ok, out, e := runHost(cmd, 600*time.Second, nil)
				if !ok {
					detail := strings.TrimSpace(e)
					if detail == "" {
						detail = strings.TrimSpace(out)
					}
					lines := strings.Split(detail, "\n")
					detail = strings.TrimSpace(lines[len(lines)-1])
					if detail == "" {
						detail = c.M("erreur inconnue", "unknown error")
					}
					c.Fail(http.StatusInternalServerError, c.M("Installation de nftables échouée ("+inst.manager+") : "+detail,
						"nftables installation failed ("+inst.manager+"): "+detail))
					return
				}
			}
			ok, out, e := runHost([]string{"nft", "--version"}, 15*time.Second, nil)
			if !ok {
				d := e
				if d == "" {
					d = out
				}
				c.Fail(http.StatusInternalServerError, c.M("nftables installé, mais nft ne répond pas : "+d, "nftables installed, but nft does not respond: "+d))
				return
			}
			c.JSON(J{"ok": true, "msg": c.M("nftables installé ("+strings.TrimSpace(out)+").", "nftables installed ("+strings.TrimSpace(out)+").")})
			return
		}
		c.Fail(http.StatusBadRequest, c.M("Aucun gestionnaire de paquets reconnu (apt, dnf, yum, apk, pacman, zypper) : installez nftables à la main.",
			"No known package manager (apt, dnf, yum, apk, pacman, zypper): install nftables by hand."))
	}))

	route("POST /api/firewall", "api_firewall_save", perm(P("firewall.manage"), func(c *Ctx) {
		detectFrp(false)
		if len(frpsInstances()) == 0 {
			c.Fail(http.StatusBadRequest, c.M("Le pare-feu ne filtre que les ports d'un frps : aucun frps sur cette machine.",
				"The firewall only filters the ports of an frps: no frps on this machine."))
			return
		}
		data := c.Body()
		rules, err := c.parseRules(data)
		if err != nil {
			c.Fail(http.StatusBadRequest, err.Error())
			return
		}
		if msg := c.Lang.fwEnsureLists(rules); msg != "" {
			c.Fail(http.StatusBadGateway, msg+c.M(". Rien n'a changé.", ". Nothing was changed."))
			return
		}
		for _, asn := range fwRuleASNs(rules) {
			if _, err := asnGet(asn); err != nil {
				c.Fail(http.StatusBadGateway, c.M(fmt.Sprintf("Impossible de récupérer les préfixes de AS%d auprès de RIPEstat, rien n'a changé : %v", asn, err),
					fmt.Sprintf("Could not fetch the prefixes of AS%d from RIPEstat, nothing was changed: %v", asn, err)))
				return
			}
		}
		next := fwConfig{truthy(data["enabled"]), rules}
		if ok, msg := fwApply(&next); !ok {
			if msg == "" {
				msg = c.M("erreur inconnue", "unknown error")
			}
			c.Fail(http.StatusInternalServerError, c.M("nftables a refusé les règles, rien n'a changé : "+msg, "nftables rejected the rules, nothing was changed: "+msg))
			return
		}
		ruleList := make([]any, len(rules))
		for i, r := range rules {
			ruleList[i] = r
		}
		if _, err := cfgUpdate(Cfg{"firewall": map[string]any{"enabled": next.Enabled, "rules": ruleList}}); err != nil {
			c.Fail(http.StatusInternalServerError, err.Error())
			return
		}
		state := [2]string{"désactivé", "disabled"}
		if next.Enabled {
			state = [2]string{"activé", "enabled"}
		}
		webhookEmit("firewall.saved", [2]string{"Pare-feu modifié", "Firewall changed"},
			[2]string{fmt.Sprintf("%d règle(s), pare-feu %s.", len(rules), state[0]), fmt.Sprintf("%d rule(s), firewall %s.", len(rules), state[1])},
			"", J{"rules": len(rules), "enabled": next.Enabled})
		msg := c.M("Pare-feu désactivé : plus aucun filtrage", "Firewall disabled: no more filtering")
		if next.Enabled {
			msg = c.M("Pare-feu appliqué", "Firewall applied")
		}
		if rules == nil {
			rules = []J{}
		}
		c.JSON(J{"ok": true, "rules": rules, "msg": msg})
	}))

	// Ce que les règles (brouillon compris) feraient d'une adresse.
	route("POST /api/firewall/test", "api_firewall_test", perm(P("firewall.view"), func(c *Ctx) {
		detectFrp(false)
		data := c.Body()
		ipText := strings.TrimSpace(pyStrDefault(data, "ip"))
		if _, err := netip.ParseAddr(ipText); err != nil {
			c.Fail(http.StatusBadRequest, c.M("Adresse IP invalide", "Invalid IP address"))
			return
		}
		rules, err := c.parseRules(data)
		if err != nil {
			c.Fail(http.StatusBadRequest, err.Error())
			return
		}
		if msg := c.Lang.fwEnsureLists(rules); msg != "" {
			c.Fail(http.StatusBadGateway, msg)
			return
		}
		for _, asn := range fwRuleASNs(rules) {
			if _, err := asnGet(asn); err != nil {
				c.Fail(http.StatusBadGateway, c.M(fmt.Sprintf("Impossible de récupérer les préfixes de AS%d : %v", asn, err),
					fmt.Sprintf("Could not fetch the prefixes of AS%d: %v", asn, err)))
				return
			}
		}
		verdicts := fwVerdicts(ipText, fwConfig{true, rules}, fwKnownPorts())
		c.JSON(J{"ok": true, "verdicts": c.Lang.withPortLabels(verdicts)})
	}))

	// Catalogue des listes communautaires (retéléchargé s'il a plus de 10 min).
	route("GET /api/firewall/lists", "api_firewall_lists", perm(P("firewall.view"), func(c *Ctx) {
		maxAge := 600.0
		if c.Query("fresh") == "1" {
			maxAge = 0
		}
		lc, errMsg := listsGet(maxAge)
		lists := make([]J, 0, len(lc.Lists))
		for _, l := range lc.Lists {
			lists = append(lists, l)
		}
		sort.SliceStable(lists, func(i, j int) bool {
			return strings.ToLower(pyStr(lists[i]["name"])) < strings.ToLower(pyStr(lists[j]["name"]))
		})
		var fetched any
		if lc.Fetched != 0 {
			fetched = lc.Fetched
		}
		c.JSON(J{"ok": true, "lists": lists, "fetched": fetched, "error": nullable(errMsg),
			"browse_url": "https://github.com/" + panelGitHubRepo + "/tree/" + fwListsBranch})
	}))

	route("POST /api/firewall/lists/publish", "api_firewall_lists_publish", perm(P("firewall.manage"), publishList))

	// Repli de /ws/firewall quand le WebSocket ne passe pas.
	route("GET /api/firewall/blocked", "api_firewall_blocked", perm(P("firewall.view"), func(c *Ctx) {
		p := fwBlockedPayload()
		p["now"] = time.Now().Unix()
		c.JSON(p)
	}))

	route("GET /ws/firewall", "ws_firewall", wsFirewall)
}

var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

// publishList prépare la proposition d'une liste au catalogue : l'entrée telle
// que les panels l'accepteront, et le lien d'une issue GitHub pré-remplie. Rien
// n'est envoyé d'ici : l'utilisateur relit et soumet l'issue lui-même.
func publishList(c *Ctx) {
	data := c.Body()
	name := ""
	if truthy(data["name"]) {
		name = truncRunes(strings.TrimSpace(pyStr(data["name"])), 60)
	}
	if name == "" {
		c.Fail(http.StatusBadRequest, c.M("Donnez un nom à la liste", "Give the list a name"))
		return
	}
	var ascii strings.Builder
	for _, r := range norm.NFKD.String(name) {
		if r < 128 {
			ascii.WriteRune(r)
		}
	}
	lid := strings.Trim(nonAlnumRe.ReplaceAllString(strings.ToLower(ascii.String()), "-"), "-")
	if len(lid) > 40 {
		lid = lid[:40]
	}
	lid = strings.Trim(lid, "-")
	if lid == "" {
		lid = "liste-" + tokenHex(3)
	}
	var skipped []J
	keepNotes := true
	if v, ok := data["notes"]; ok {
		keepNotes = truthy(v)
	}
	var items []any
	for _, s := range listv(data, "sources") {
		if m, ok := s.(map[string]any); ok && !keepNotes {
			cp := map[string]any{}
			for k, v := range m {
				cp[k] = v
			}
			cp["note"] = ""
			items = append(items, cp)
		} else {
			items = append(items, s)
		}
	}
	sources := c.Lang.fwListSources(items, &skipped)
	if skipped == nil {
		skipped = []J{}
	}
	if len(sources) == 0 {
		c.JSON(J{"ok": false, "msg": c.M("Aucune adresse publiable : il faut des adresses publiques, des réseaux d'au plus /8 (/16 en IPv6) ou des AS",
			"Nothing to publish: public addresses, networks of /8 at most (/16 for IPv6) or AS numbers are required"), "skipped": skipped}, http.StatusBadRequest)
		return
	}
	mode := "block"
	if data["mode"] == "allow" {
		mode = "allow"
	}
	texts := make([]string, len(sources))
	for i, s := range sources {
		t := pyStr(s["cidr"])
		if asn, ok := s["asn"].(int); ok {
			t = fmt.Sprintf("AS%d", asn)
		}
		if note := pyStr(s["note"]); note != "" {
			t += "  # " + note
		}
		texts[i] = t
	}
	field := func(k string, n int) string {
		if !truthy(data[k]) {
			return ""
		}
		return truncRunes(strings.TrimSpace(pyStr(data[k])), n)
	}
	entry := struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Mode        string   `json:"mode"`
		Description string   `json:"description"`
		Author      string   `json:"author"`
		Updated     string   `json:"updated"`
		Sources     []string `json:"sources"`
	}{lid, name, mode, field("description", 300), field("author", 40), time.Now().Format("2006-01-02"), texts}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(entry)
	text := strings.TrimRight(buf.String(), "\n")
	_, existing := listsAll().Lists[lid]
	kind := "de blocage"
	if mode == "allow" {
		kind = "d'autorisation"
	}
	title := "Nouvelle liste " + kind + " : " + name
	if existing {
		title = "Mise à jour de la liste " + kind + " : " + name
	}
	intro := "Liste pour le catalogue communautaire du pare-feu (branche `" + fwListsBranch + "`), préparée par FRP Manager " +
		panelVersion + ". Elle est vérifiée puis ajoutée automatiquement dans les minutes qui suivent.\n\n"
	base := "https://github.com/" + panelGitHubRepo + "/issues/new?"
	link := base + url.Values{"title": {title}, "body": {intro + "```json\n" + text + "\n```\n"}}.Encode()
	tooLong := len(link) > 8000 // limite des liens GitHub : l'entrée est collée à la main
	if tooLong {
		link = base + url.Values{"title": {title}, "body": {intro + "```json\n(collez ici l'entrée copiée depuis le panel)\n```\n"}}.Encode()
	}
	c.JSON(J{"ok": true, "entry": entry, "json": text, "issue_url": link, "too_long": tooLong, "skipped": skipped, "update": existing})
}

// Connexions bloquées en temps réel : la table est relue chaque seconde et
// l'état n'est envoyé que s'il a changé.
func wsFirewall(c *Ctx) {
	conn, allowed := c.wsAccept("firewall.view", "")
	if conn == nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(c.R.Context())
	go wsKeepPing(ctx, conn)
	last, lastSent := "", time.Now()
	for {
		if allowed() == nil {
			conn.Close(websocket.StatusPolicyViolation, c.M("Accès retiré", "Access revoked"))
			return
		}
		data := fwBlockedPayload()
		b, err := json.Marshal(data)
		if err == nil && string(b) != last {
			// « now » hors comparaison : sinon chaque seconde serait un changement
			data["now"] = time.Now().Unix()
			out, _ := json.Marshal(data)
			if conn.Write(ctx, websocket.MessageText, out) != nil {
				return
			}
			last, lastSent = string(b), time.Now()
		} else if time.Since(lastSent) >= keepaliveInterval {
			if conn.Write(ctx, websocket.MessageText, []byte(wsKeepaliveState)) != nil {
				return
			}
			lastSent = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(fwLiveInterval):
		}
	}
}
