// Création d'instances frps / frpc supplémentaires (frpc-maison…) : config et
// journal propres, ports d'écoute libres, unité systemd créée arrêtée (simple
// fichier de config en mode autonome). Aussi : créer le service d'une config
// déposée à la main sans unité (frpc-ancelade.toml à côté de frpc.toml).
// Creation of extra frps / frpc instances (frpc-maison…): their own config and
// log, free listening ports, systemd unit created stopped (a plain config file
// in standalone mode). Also: creating the service of a config dropped by hand
// without a unit (frpc-ancelade.toml next to frpc.toml).

package main

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	toml "github.com/pelletier/go-toml/v2"
)

var (
	// Suffixe choisi dans l'interface (« maison » → frpc-maison) : liste blanche
	// stricte, le nom finit dans un nom de fichier et d'unité systemd.
	// Suffix chosen in the interface ("maison" → frpc-maison): strict
	// whitelist, the name ends up in a file name and a systemd unit name.
	instanceSuffixRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	multiBlankRe     = regexp.MustCompile(`\n{3,}`)

	// Une création à la fois : vérifier puis écrire sans qu'une autre s'intercale.
	// One creation at a time: check then write without another one in between.
	createMu sync.Mutex

	// portFree : le port est-il libre sur cette machine ? Remplaçable (tests).
	// portFree: is the port free on this machine? Replaceable (tests).
	portFree = func(port int, udp bool) bool {
		if udp {
			c, err := net.ListenPacket("udp", netJoin("", port))
			if err != nil {
				return false
			}
			c.Close()
			return true
		}
		ln, err := net.Listen("tcp", netJoin("", port))
		if err != nil {
			return false
		}
		ln.Close()
		return true
	}
)

// frpListenKeys : clés d'une config frp qui ouvrent un port d'écoute sur la
// machine (hors visiteurs, lus à part), bindPort en tête.
// frpListenKeys: keys of an frp config that open a listening port on the
// machine (visitors aside, read separately), bindPort first.
var frpListenKeys = []struct {
	key string
	udp bool
}{
	{"bindPort", false}, {"kcpBindPort", true}, {"quicBindPort", true}, {"vhostHTTPPort", false},
	{"vhostHTTPSPort", false}, {"tcpmuxHTTPConnectPort", false}, {"webServer.port", false},
	{"sshTunnelGateway.bindPort", false},
}

// newInstanceID : « frpc » + « maison » → « frpc-maison », "" si refusé.
// newInstanceID: "frpc" + "maison" → "frpc-maison", "" if rejected.
func newInstanceID(bin, suffix string) string {
	if bin != "frps" && bin != "frpc" {
		return ""
	}
	suffix = strings.ToLower(strings.TrimSpace(suffix))
	// « frps-vps » ou « frps » pour un frps : le préfixe n'est pas répété
	// (frps-vps, frps) ; « frpc » ou « frpc-x » pour un frps : refusé, le nom
	// désignerait l'autre type.
	// "frps-vps" or "frps" for an frps: the prefix is not repeated (frps-vps,
	// frps); "frpc" or "frpc-x" for an frps: rejected, the name would point at
	// the other type.
	if suffix == bin {
		return bin
	}
	suffix = strings.TrimPrefix(suffix, bin+"-")
	if !instanceSuffixRe.MatchString(suffix) || otherTypeName(bin, suffix) {
		return ""
	}
	id := bin + "-" + suffix
	if !validServiceName(id) {
		return ""
	}
	return id
}

// otherTypeName : suffix (déjà en minuscules) désigne-t-il l'autre type
// (« frpc », « frpc-x » pour un frps) ?
// otherTypeName: does suffix (already lowercase) point at the other type
// ("frpc", "frpc-x" for an frps)?
func otherTypeName(bin, suffix string) bool {
	other := map[string]string{"frps": "frpc", "frpc": "frps"}[bin]
	return other != "" && (suffix == other || strings.HasPrefix(suffix, other+"-"))
}

// servicesCreatable : le panel peut-il créer une instance ? Pas dans un
// conteneur qui ne voit que le socket Docker (ni systemd de l'hôte, ni mode
// autonome).
// servicesCreatable: can the panel create an instance? Not in a container that
// only sees the Docker socket (neither the host's systemd nor standalone mode).
func servicesCreatable() bool { return standalone || !inDocker || dockerEnv }

func instanceLogPath(id string) string { return filepath.Join(frpLogDir, id+".log") }

// tomlString : chaîne TOML entre guillemets.
// tomlString: quoted TOML string.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func parseFrpConfig(content string) (map[string]any, error) {
	var conf map[string]any
	err := toml.Unmarshal([]byte(frpTOML(content)), &conf)
	return conf, err
}

// tomlLookup : valeur d'une clé à points (« webServer.port »), nil si absente.
// tomlLookup: value of a dotted key ("webServer.port"), nil if missing.
func tomlLookup(m map[string]any, key string) any {
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		m = mapv(m, p)
	}
	return m[parts[len(parts)-1]]
}

// listenPorts : ports d'écoute d'une config (frps sans bindPort écoute sur 7000).
// listenPorts: listening ports of a config (frps without bindPort listens on 7000).
func listenPorts(content, bin string) []int {
	conf, err := parseFrpConfig(content)
	if err != nil {
		return nil
	}
	var out []int
	for _, k := range frpListenKeys {
		if p := tomlInt(tomlLookup(conf, k.key)); p > 0 {
			out = append(out, p)
		}
	}
	if bin == "frps" && tomlInt(conf["bindPort"]) == 0 {
		out = append(out, 7000)
	}
	for _, v := range listv(conf, "visitors") {
		if m, ok := v.(map[string]any); ok {
			if p := tomlInt(m["bindPort"]); p > 0 {
				out = append(out, p)
			}
		}
	}
	return out
}

// logTo : log.to d'une config, "" s'il est absent ou s'il n'est pas un fichier.
// logTo: log.to of a config, "" if missing or if it is not a file.
func logTo(content string) string {
	conf, err := parseFrpConfig(content)
	if err != nil {
		return ""
	}
	if to := str(mapv(conf, "log"), "to", ""); to != "" && to != "console" {
		return to
	}
	return ""
}

// localConfigs : configs frp de la machine (instances hors conteneurs et
// fichiers des dossiers de configuration) → frps ou frpc.
// localConfigs: the machine's frp configs (non-container instances and files
// of the configuration folders) → frps or frpc.
func localConfigs() map[string]string {
	out := map[string]string{}
	for _, bin := range frpBinaries {
		for _, c := range findAllConfigs(bin) {
			out[c] = bin
		}
	}
	for _, inst := range instancesSnapshot() {
		if inst.Source != "docker" && inst.Config != "" {
			out[inst.Config] = inst.Type
		}
	}
	return out
}

// portOwners : port d'écoute → qui l'utilise (nom de la config, ou le panel),
// sans la config except.
// portOwners: listening port → who uses it (config name, or the panel),
// without the except config.
func portOwners(except string) map[int]string {
	owners := map[int]string{fwPanelPort(): "frp-manager"}
	confs := localConfigs()
	for _, conf := range sortedKeys(confs) {
		if conf == except {
			continue
		}
		if b, err := os.ReadFile(conf); err == nil {
			for _, p := range listenPorts(string(b), confs[conf]) {
				if _, ok := owners[p]; !ok {
					owners[p] = filepath.Base(conf)
				}
			}
		}
	}
	return owners
}

func usedFrpPorts(except string) map[int]bool {
	used := map[int]bool{}
	for p := range portOwners(except) {
		used[p] = true
	}
	return used
}

// pickPort : port libre absent de used, qui le reçoit : à partir de base
// (tableaux de bord : 7400 pour frpc, 7500 pour frps), sinon tiré entre 10000
// et 32767 comme le bindPort de frps à l'installation. 0 si aucun.
// pickPort: free port missing from used, which receives it: from base
// (dashboards: 7400 for frpc, 7500 for frps), otherwise drawn between 10000 and
// 32767 like frps's bindPort at install time. 0 if none.
func pickPort(used map[int]bool, base int, udp bool) int {
	try := func(p int) bool {
		if p < 1 || p > 65535 || used[p] || !portFree(p, udp) {
			return false
		}
		used[p] = true
		return true
	}
	if base > 0 {
		for p := base; p < base+100; p++ {
			if try(p) {
				return p
			}
		}
	}
	for i := 0; i < 200; i++ {
		if p := frpsBindPort(""); try(p) {
			return p
		}
	}
	return 0
}

// ── Réécriture de texte TOML ────────────────────────────────────────────────
// Ligne par ligne, pour garder les commentaires, l'ordre et les modèles frp
// ({{ … }}) d'une config ; le résultat est relu et vérifié ensuite.
// Line by line, to keep a config's comments, order and frp templates
// ({{ … }}); the result is read again and checked afterwards.

// tomlHeader : table ouverte par la ligne s (sans espaces autour) → (nom,
// tableau [[…]], ok).
// tomlHeader: table opened by line s (trimmed) → (name, [[…]] array, ok).
func tomlHeader(s string) (string, bool, bool) {
	if strings.HasPrefix(s, "[[") {
		if i := strings.Index(s, "]]"); i > 2 {
			return plainTOMLName(strings.Join(strings.Fields(s[2:i]), "")), true, true
		}
		return "", false, false
	}
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i > 1 {
			return plainTOMLName(strings.Join(strings.Fields(s[1:i]), "")), false, true
		}
	}
	return "", false, false
}

// tomlKeyOf : clé affectée par la ligne s (« webServer . port = 1 » →
// « webServer.port »), "" si la ligne n'en affecte pas.
// tomlKeyOf: key assigned by line s ("webServer . port = 1" →
// "webServer.port"), "" if the line assigns none.
func tomlKeyOf(s string) string {
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{{") {
		return ""
	}
	k, _, ok := cutTOMLKey(s)
	if !ok {
		return ""
	}
	// Guillemets retirés : "proxies" = [ … ] est la clé proxies
	// Quotes removed: "proxies" = [ … ] is the key proxies
	return strings.NewReplacer(`"`, "", `'`, "").Replace(strings.Join(strings.Fields(k), ""))
}

// cutTOMLKey : comme strings.Cut(line, "="), mais sur le premier « = » hors
// guillemets (une clé entre guillemets peut en contenir : "a=b" = 1) ; un « # »
// hors guillemets avant lui (commentaire) : pas de clé.
// cutTOMLKey: like strings.Cut(line, "="), but on the first "=" outside quotes
// (a quoted key may contain one: "a=b" = 1); a "#" outside quotes before it
// (comment): no key.
func cutTOMLKey(line string) (key, value string, ok bool) {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return line, "", false
		case c == '=':
			return line[:i], line[i+1:], true
		}
	}
	return line, "", false
}

// tomlStringLines : pour chaque ligne de content (coupé sur "\n"), vrai si elle
// est à l'intérieur d'une chaîne sur plusieurs lignes (trois guillemets doubles
// ou trois apostrophes) ; la ligne
// de fermeture en fait partie, pas celle d'ouverture. Un commentaire n'ouvre
// pas de chaîne, une chaîne fermée sur sa ligne d'ouverture non plus. Les
// lecteurs ligne à ligne n'y cherchent ni en-tête ni clé.
// tomlStringLines: for each line of content (split on "\n"), true if it is
// inside a multi-line string (three double quotes or three single quotes); the
// closing line is part of it, the
// opening one is not. A comment opens no string, nor does a string closed on
// its opening line. Line-by-line readers look for no header or key there.
func tomlStringLines(content string) []bool {
	lines := strings.Split(content, "\n")
	inside := make([]bool, len(lines))
	delim := ""
	for i, line := range lines {
		if delim != "" {
			inside[i] = true
			if strings.Contains(line, delim) {
				delim = ""
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		_, v, ok := cutTOMLKey(line)
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		for _, d := range []string{`"""`, `'''`} {
			if strings.HasPrefix(v, d) && !strings.Contains(v[3:], d) {
				delim = d
				break
			}
		}
	}
	return inside
}

// withoutTOMLStrings : content dont les lignes internes aux chaînes sur
// plusieurs lignes sont vidées (même nombre de lignes), pour les lecteurs qui
// ne font que chercher des clés.
// withoutTOMLStrings: content whose lines inside multi-line strings are emptied
// (same number of lines), for the readers that only look for keys.
func withoutTOMLStrings(content string) string {
	if !strings.Contains(content, `"""`) && !strings.Contains(content, `'''`) {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, in := range tomlStringLines(content) {
		if in {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

func insertLine(lines []string, at int, line string) []string {
	return append(lines[:at], append([]string{line}, lines[at:]...)...)
}

// setTOMLKey donne à key la valeur value (déjà écrite en TOML) : la ligne
// existante est réécrite (clé à points à la racine, ou clé dans sa table) ;
// sinon la clé est ajoutée sous [table] si cette table existe, sinon en clé à
// points avant la première table (ou la première ligne de modèle frp).
// setTOMLKey gives key the value value (already written as TOML): the existing
// line is rewritten (dotted key at the root, or key inside its table);
// otherwise the key is added under [table] if that table exists, otherwise as
// a dotted key before the first table (or the first frp template line).
func setTOMLKey(content, key, value string) string {
	lines := strings.Split(content, "\n")
	keyTable, keyName := "", key
	if i := strings.LastIndex(key, "."); i >= 0 {
		keyTable, keyName = key[:i], key[i+1:]
	}
	table, array, found := "", false, false
	headerAt, firstTable := -1, -1
	inString := tomlStringLines(content)
	for i, line := range lines {
		// Ligne d'une chaîne sur plusieurs lignes : ni en-tête ni clé
		// Line of a multi-line string: neither header nor key
		if inString[i] {
			continue
		}
		s := strings.TrimSpace(line)
		if name, arr, ok := tomlHeader(s); ok {
			table, array = name, arr
			if firstTable < 0 {
				firstTable = i
			}
			if !arr && keyTable != "" && name == keyTable && headerAt < 0 {
				headerAt = i
			}
			continue
		}
		if frpActionLineRe.MatchString(line) {
			if firstTable < 0 {
				firstTable = i
			}
			continue
		}
		k := tomlKeyOf(s)
		if k == "" || array {
			continue
		}
		if table != "" {
			k = table + "." + k
		}
		if k == key {
			left, _, _ := cutTOMLKey(line)
			lines[i] = strings.TrimRight(left, " \t") + " = " + value
			found = true
		}
	}
	switch {
	case found:
	case headerAt >= 0:
		lines = insertLine(lines, headerAt+1, keyName+" = "+value)
	case firstTable >= 0:
		// Avant les commentaires et lignes vides qui précèdent la table
		// Before the comments and blank lines in front of the table
		at := firstTable
		for at > 0 && !inString[at-1] && strings.HasPrefix(strings.TrimSpace(lines[at-1]), "#") {
			at--
		}
		for at > 0 && !inString[at-1] && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		lines = insertLine(lines, at, key+" = "+value)
	default:
		at := len(lines)
		if at > 0 && lines[at-1] == "" {
			at--
		}
		lines = insertLine(lines, at, key+" = "+value)
	}
	return strings.Join(lines, "\n")
}

// clientConnectionOnly : config frpc sans ses [[proxies]] / [[visitors]] (ni
// leurs sous-tables, ni les lignes de modèle, ni includes et start qui les
// désignent) : une copie qui les garderait se battrait avec l'originale auprès
// du serveur (mêmes noms de tunnels, mêmes ports).
// clientConnectionOnly: frpc config without its [[proxies]] / [[visitors]]
// (nor their sub-tables, template lines, or the includes and start keys that
// point at them): a copy keeping them would fight with the original on the
// server (same tunnel names, same ports).
func clientConnectionOnly(content string) string {
	var out []string
	drop, skipValue, table := false, false, ""
	inlineDepth := 0
	inString := tomlStringLines(content)
	// La dernière ligne hors chaîne a été gardée
	// The last line outside a string was kept
	kept := false
	for i, line := range strings.Split(content, "\n") {
		// Ligne d'une chaîne sur plusieurs lignes : elle suit le sort de sa clé
		// (gardée dans la connexion, retirée avec un tunnel)
		// Line of a multi-line string: it follows its key (kept in the connection
		// part, removed with a tunnel)
		if inString[i] {
			if kept {
				out = append(out, line)
			}
			continue
		}
		kept = false
		s := strings.TrimSpace(line)
		// Forme en ligne « proxies = [ { … } ] » : retirée, sur une ou plusieurs lignes
		// Inline form "proxies = [ { … } ]": removed, on one or several lines
		if inlineDepth > 0 {
			inlineDepth += bracketBalance(s)
			continue
		}
		if k := tomlKeyOf(s); table == "" && (k == "proxies" || k == "visitors") {
			_, v, _ := strings.Cut(s, "=")
			if d := bracketBalance(strings.TrimSpace(v)); d > 0 {
				inlineDepth = d
			}
			continue
		}
		if skipValue {
			// Suite d'un tableau sur plusieurs lignes (includes = [ … ])
			// Rest of a multi-line array (includes = [ … ])
			skipValue = !strings.Contains(s, "]")
			continue
		}
		if name, _, ok := tomlHeader(s); ok {
			table = name
			root, _, _ := strings.Cut(name, ".")
			drop = root == "proxies" || root == "visitors"
		}
		if drop || frpActionLineRe.MatchString(line) {
			continue
		}
		if k := tomlKeyOf(s); table == "" && (k == "includes" || k == "start") {
			_, v, _ := strings.Cut(s, "=")
			v = strings.TrimSpace(v)
			skipValue = strings.HasPrefix(v, "[") && !strings.Contains(v, "]")
			continue
		}
		out = append(out, line)
		kept = true
	}
	text := multiBlankRe.ReplaceAllString(strings.Join(out, "\n"), "\n\n")
	return strings.TrimRight(text, "\n") + "\n"
}

// rewriteListenPorts donne un port libre à chaque port d'écoute de la config
// (tous si all, sinon ceux déjà pris), puis vérifie qu'aucun ne reste en
// conflit avec used. kcpBindPort / quicBindPort égaux à bindPort le restent.
// rewriteListenPorts gives a free port to each listening port of the config
// (all of them if all, otherwise those already taken), then checks that none is
// still in conflict with used. kcpBindPort / quicBindPort equal to bindPort stay
// so.
func (l Lang) rewriteListenPorts(content, bin string, used map[int]bool, all bool) (string, error) {
	conf, err := parseFrpConfig(content)
	if err != nil {
		return "", errors.New(l.M("TOML invalide : "+tomlErrorText(err), "Invalid TOML: "+tomlErrorText(err)))
	}
	taken := maps.Clone(used)
	bind, newBind := tomlInt(conf["bindPort"]), 0
	if bin == "frps" && bind == 0 {
		bind = 7000
	}
	for _, k := range frpListenKeys {
		p := tomlInt(tomlLookup(conf, k.key))
		if k.key == "bindPort" && bin == "frps" {
			p = bind
		}
		if p == 0 || (!all && !taken[p]) {
			continue
		}
		np := 0
		switch {
		case k.udp && p == bind && newBind != 0 && portFree(newBind, true):
			np = newBind
		case k.key == "webServer.port":
			np = pickPort(used, map[string]int{"frps": 7500, "frpc": 7400}[bin], false)
		default:
			np = pickPort(used, 0, k.udp)
		}
		if np == 0 {
			return "", errors.New(l.M("Aucun port libre trouvé pour "+k.key, "No free port found for "+k.key))
		}
		if k.key == "bindPort" {
			newBind = np
		}
		content = setTOMLKey(content, k.key, strconv.Itoa(np))
	}
	// Réécriture illisible (table en ligne…) : refus plutôt qu'une config cassée
	// Unreadable rewrite (inline table…): refusal rather than a broken config
	if _, err := parseFrpConfig(content); err != nil {
		return "", errors.New(l.M("ports impossibles à réécrire : modifiez-les à la main ("+tomlErrorText(err)+")",
			"ports that cannot be rewritten: change them by hand ("+tomlErrorText(err)+")"))
	}
	for _, p := range listenPorts(content, bin) {
		if taken[p] {
			return "", errors.New(l.M(fmt.Sprintf("le port %d resterait celui d'une autre instance : modifiez-le à la main", p),
				fmt.Sprintf("port %d would stay another instance's one: change it by hand", p)))
		}
	}
	return content, nil
}

// newInstanceConfig : config par défaut d'une nouvelle instance, avec son propre
// journal ; frps reçoit un port libre et un jeton tirés au hasard.
// newInstanceConfig: default config of a new instance, with its own log; frps
// gets a free port and a token drawn at random.
func (l Lang) newInstanceConfig(bin, id string, used map[int]bool) (string, error) {
	content := setTOMLKey(defaultConfigs[bin], "log.to", tomlString(instanceLogPath(id)))
	if bin == "frps" {
		port := pickPort(used, 0, false)
		if port == 0 {
			return "", errors.New(l.M("Aucun port libre trouvé pour bindPort", "No free port found for bindPort"))
		}
		content = setTOMLKey(content, "bindPort", strconv.Itoa(port))
		content = setTOMLKey(content, "auth.token", tomlString(tokenHex(16)))
	}
	return content, nil
}

// copyInstanceConfig : copie de la config de srcID pour l'instance id, avec son
// propre journal et des ports d'écoute libres ; frpc perd ses tunnels.
// copyInstanceConfig: copy of srcID's config for the id instance, with its own
// log and free listening ports; frpc loses its tunnels.
func (l Lang) copyInstanceConfig(bin, srcID, src, id string, used map[int]bool) (string, error) {
	content := src
	if bin == "frpc" {
		content = clientConnectionOnly(content)
	}
	// En-tête « # frpc.toml — généré par FRP Manager » de la page Configuration
	// "# frpc.toml — generated by FRP Manager" header of the Configuration page
	if first, rest, ok := strings.Cut(content, "\n"); ok && strings.HasPrefix(first, "# "+srcID+".toml") {
		content = "# " + id + ".toml" + strings.TrimPrefix(first, "# "+srcID+".toml") + "\n" + rest
	}
	content = setTOMLKey(content, "log.to", tomlString(instanceLogPath(id)))
	// frps : nouveau jeton, les clients de l'original n'entrent pas dans la copie.
	// Pas de changement si la config a des modèles frp (le jeton peut venir de
	// {{ .Envs.X }}), ni pour une autre méthode d'authentification (oidc). frpc
	// garde celui de son serveur.
	// frps: new token, the original's clients do not get into the copy. No change
	// when the config has frp templates (the token may come from
	// {{ .Envs.X }}), nor for another authentication method (oidc). frpc keeps
	// its server's one.
	if bin == "frps" && !strings.Contains(content, "{{") {
		if conf, err := parseFrpConfig(content); err == nil {
			auth := mapv(conf, "auth")
			if tok, ok := auth["token"].(string); ok && tok != "" && str(auth, "method", "token") == "token" {
				// Réécriture illisible (table en ligne…) : jeton gardé plutôt qu'une copie refusée
				// Unreadable rewrite (inline table…): token kept rather than a refused copy
				fresh := tokenHex(16)
				next := setTOMLKey(content, "auth.token", tomlString(fresh))
				if nc, err := parseFrpConfig(next); err == nil && str(mapv(nc, "auth"), "token", "") == fresh {
					content = next
				}
			}
		}
	}
	out, err := l.rewriteListenPorts(content, bin, used, true)
	if err != nil {
		return "", errors.New(l.M("Copie de "+srcID+" impossible : "+err.Error(), "Cannot copy "+srcID+": "+err.Error()))
	}
	return out, nil
}

// ── Création ────────────────────────────────────────────────────────────────

// configWithStem : config existante nommée id (n'importe quelle extension),
// dans l'un des dossiers de configuration, ou "".
// configWithStem: existing config named id (any extension), in one of the
// configuration folders, or "".
func configWithStem(id string) string {
	for _, d := range configSearchPaths {
		for _, ext := range configSuffixes {
			if p := filepath.Join(d, id+ext); fileExists(p) {
				return p
			}
		}
	}
	return ""
}

// createProblem : ce qui empêche de créer l'instance id → (message, code HTTP),
// "" si rien.
// createProblem: what prevents creating the id instance → (message, HTTP
// code), "" if nothing.
func (l Lang) createProblem(bin, id string) (string, int) {
	if !servicesCreatable() {
		return l.M("Sous Docker sans accès à l'hôte, une instance frp est un conteneur : créez-le avec l'image fatedier/"+bin+".",
			"Under Docker without host access, an frp instance is a container: create it with the fatedier/"+bin+" image."), http.StatusBadRequest
	}
	if (bin != "frps" && bin != "frpc") || !strings.HasPrefix(id, bin) || !validServiceName(id) || strings.Contains(id, "@") {
		return l.M("Nom d'instance refusé", "Instance name rejected"), http.StatusBadRequest
	}
	if findBinary(bin) == "" {
		return l.M("Programme "+bin+" introuvable : installez frp depuis Réglages → Mises à jour.",
			bin+" program not found: install frp from Settings → Updates."), http.StatusBadRequest
	}
	if getInstance(id) != nil || (!standalone && serviceExists(id)) {
		return l.M("L'instance "+id+" existe déjà.", "The "+id+" instance already exists."), http.StatusConflict
	}
	return "", 0
}

// createInstance écrit la config conf (si content n'est pas vide : sinon elle
// existe déjà et n'est pas touchée) puis, hors mode autonome, l'unité systemd
// <id>.service, arrêtée et non activée. → (ok, message, code HTTP).
// createInstance writes the conf config (if content is not empty: otherwise it
// already exists and is left alone) then, outside standalone mode, the
// <id>.service systemd unit, stopped and not enabled. → (ok, message, HTTP code).
func (l Lang) createInstance(bin, id, conf, content string) (bool, string, int) {
	if msg, code := l.createProblem(bin, id); msg != "" {
		return false, msg, code
	}
	written := ""
	if content != "" {
		if p := configWithStem(id); p != "" {
			return false, l.M(p+" existe déjà : choisissez un autre nom.", p+" already exists: choose another name."), http.StatusConflict
		}
		real, err := l.safeConfigPath(conf)
		if err == nil {
			err = l.validateConfigContent(real, content)
		}
		if err != nil {
			return false, err.Error(), http.StatusBadRequest
		}
		if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
			return false, l.M("Erreur écriture : "+err.Error(), "Write error: "+err.Error()), http.StatusInternalServerError
		}
		// O_EXCL : jamais une config existante écrasée / never an existing config overwritten
		f, err := os.OpenFile(real, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return false, l.M(conf+" existe déjà : choisissez un autre nom.", conf+" already exists: choose another name."), http.StatusConflict
			}
			return false, l.M("Erreur écriture : "+err.Error(), "Write error: "+err.Error()), http.StatusInternalServerError
		}
		_, err = f.WriteString(content)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(real)
			return false, l.M("Erreur écriture : "+err.Error(), "Write error: "+err.Error()), http.StatusInternalServerError
		}
		written = real
	}
	os.MkdirAll(frpLogDir, 0o755)
	if standalone {
		// Pas d'unité : le superviseur prend la config en charge
		// No unit: the supervisor takes the config over
		undismissConfig(conf)
		invalidateCache(true)
		return true, l.M(id+" créé, arrêté : vérifiez sa configuration puis démarrez-le depuis sa carte.",
			id+" created, stopped: check its configuration then start it from its card."), http.StatusOK
	}
	program := findBinary(bin)
	// Mode hôte sous Docker : /host/usr/local/bin est le /usr/local/bin de l'hôte
	// Host mode under Docker: /host/usr/local/bin is the host's /usr/local/bin
	if dockerEnv && strings.HasPrefix(program, "/host/") {
		program = strings.TrimPrefix(program, "/host")
	}
	unit := id + ".service"
	role := map[string]string{"frps": "server", "frpc": "client"}[bin]
	if !hostWriteFile(unitDir+unit, fmt.Sprintf(frpUnitTemplate, role, id, program, conf)) {
		if written != "" {
			os.Remove(written)
		}
		return false, l.M("Impossible d'écrire "+unitDir+unit, "Could not write "+unitDir+unit), http.StatusInternalServerError
	}
	runCmd([]string{"systemctl", "daemon-reload"})
	undismissConfig(conf)
	invalidateCache(true)
	return true, l.M("Service "+unit+" créé, arrêté : vérifiez sa configuration puis démarrez-le depuis sa carte.",
		"Service "+unit+" created, stopped: check its configuration then start it from its card."), http.StatusOK
}

// ── Configs sans service (mode systemd) ─────────────────────────────────────

// orphanConfigs : en mode systemd, configs frps*/frpc* qu'aucun service
// n'utilise (déposées à la main, comme frpc-ancelade.toml à côté de
// frpc.toml), avec ce qui entrerait en conflit avec les autres instances.
// orphanConfigs: in systemd mode, frps*/frpc* configs that no service uses
// (dropped by hand, like frpc-ancelade.toml next to frpc.toml), with what would
// conflict with the other instances.
func (l Lang) orphanConfigs() []J {
	out := []J{}
	if standalone || !servicesCreatable() {
		return out
	}
	ids, used := map[string]bool{}, map[string]bool{}
	for iid, inst := range instancesSnapshot() {
		ids[iid] = true
		if inst.Source != "docker" && inst.Config != "" {
			used[realPath(inst.Config)] = true
		}
	}
	dismissed := map[string]bool{}
	for _, d := range stringList(cfg()["dismissed_configs"]) {
		dismissed[d] = true
	}
	for _, bin := range frpBinaries {
		for _, conf := range findAllConfigs(bin) {
			id := standaloneID(conf, bin)
			if id == "" || strings.Contains(id, "@") || ids[id] || used[realPath(conf)] || dismissed[conf] {
				continue
			}
			ids[id] = true
			conflicts, fixable := l.configConflicts(conf, bin)
			out = append(out, J{"id": id, "type": bin, "config_path": conf, "conflicts": conflicts, "fixable": fixable})
		}
	}
	return out
}

// configConflicts : ce que la config conf partage avec les autres instances →
// (messages, corrigeable par fixInstanceConfig : journal, ports d'écoute).
// configConflicts: what the conf config shares with the other instances →
// (messages, fixable by fixInstanceConfig: log, listening ports).
func (l Lang) configConflicts(conf, bin string) ([]string, bool) {
	out := []string{}
	b, err := os.ReadFile(conf)
	if err != nil {
		return append(out, l.M("fichier illisible : "+err.Error(), "unreadable file: "+err.Error())), false
	}
	content := string(b)
	mine, err := parseFrpConfig(content)
	if err != nil {
		return append(out, l.M("TOML invalide : "+tomlErrorText(err), "invalid TOML: "+tomlErrorText(err))), false
	}
	fixable := false
	confs := localConfigs()
	others := sortedKeys(confs)
	if to := logTo(content); to != "" {
		for _, other := range others {
			if other == conf {
				continue
			}
			if ob, err := os.ReadFile(other); err == nil && logTo(string(ob)) == to {
				out = append(out, l.M("journal "+to+" partagé avec "+filepath.Base(other), "log "+to+" shared with "+filepath.Base(other)))
				fixable = true
				break
			}
		}
	}
	owners := portOwners(conf)
	ports := listenPorts(content, bin)
	sort.Ints(ports)
	for _, p := range ports {
		if owner, ok := owners[p]; ok {
			out = append(out, l.M(fmt.Sprintf("port %d déjà utilisé par %s", p, owner), fmt.Sprintf("port %d already used by %s", p, owner)))
			fixable = true
		}
	}
	if bin == "frpc" {
		// Mêmes noms de tunnels vers le même serveur : frps refuse le second
		// Same tunnel names to the same server: frps rejects the second one
		names := proxyNames(mine)
		for _, other := range others {
			if other == conf || confs[other] != "frpc" {
				continue
			}
			ob, err := os.ReadFile(other)
			if err != nil {
				continue
			}
			oc, err := parseFrpConfig(string(ob))
			if err != nil || str(oc, "serverAddr", "") != str(mine, "serverAddr", "") || tomlInt(oc["serverPort"]) != tomlInt(mine["serverPort"]) {
				continue
			}
			var shared []string
			for _, n := range sortedKeys(proxyNames(oc)) {
				if names[n] {
					shared = append(shared, n)
				}
			}
			if len(shared) > 0 {
				list := strings.Join(shared, ", ")
				out = append(out, l.M("tunnels déjà déclarés par "+filepath.Base(other)+" vers le même serveur : "+list,
					"tunnels already declared by "+filepath.Base(other)+" to the same server: "+list))
			}
		}
	}
	return out, fixable
}

func proxyNames(conf map[string]any) map[string]bool {
	names := map[string]bool{}
	for _, v := range listv(conf, "proxies") {
		if m, ok := v.(map[string]any); ok {
			if n := str(m, "name", ""); n != "" {
				names[n] = true
			}
		}
	}
	return names
}

// fixInstanceConfig : journal propre si le sien est partagé, port libre pour
// chaque port d'écoute déjà pris ; le reste de la config ne change pas.
// fixInstanceConfig: own log if its log is shared, free port for each
// listening port already taken; the rest of the config does not change.
func (l Lang) fixInstanceConfig(bin, id, conf, content string) (string, error) {
	if to := logTo(content); to != "" {
		for other := range localConfigs() {
			if other == conf {
				continue
			}
			if ob, err := os.ReadFile(other); err == nil && logTo(string(ob)) == to {
				content = setTOMLKey(content, "log.to", tomlString(instanceLogPath(id)))
				break
			}
		}
	}
	return l.rewriteListenPorts(content, bin, usedFrpPorts(conf), false)
}

func init() {
	// Nouvelle instance : nom choisi (suffixe), config par défaut ou copiée
	// New instance: chosen name (suffix), default or copied config
	route("POST /api/instance/create", "api_instance_create", perm(P("instances.create"), func(c *Ctx) {
		data := c.Body()
		bin := str(data, "type", "")
		if bin != "frps" && bin != "frpc" {
			c.Fail(http.StatusBadRequest, c.M("Programme inconnu (frps ou frpc)", "Unknown program (frps or frpc)"))
			return
		}
		// Le nom est une chaîne : un nombre JSON (5) n'est pas converti en « 5 »
		// The name is a string: a JSON number (5) is not turned into "5"
		name, _ := data["name"].(string)
		if otherTypeName(bin, strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), bin+"-")) {
			c.Fail(http.StatusBadRequest, c.M("Nom refusé : il désigne l'autre type d'instance, choisissez-en un autre.",
				"Name rejected: it points at the other instance type, choose another one."))
			return
		}
		id := newInstanceID(bin, name)
		if id == "" {
			c.Fail(http.StatusBadRequest, c.M("Nom invalide : 1 à 32 caractères parmi a-z, 0-9, - et _, en commençant par une lettre ou un chiffre.",
				"Invalid name: 1 to 32 characters among a-z, 0-9, - and _, starting with a letter or a digit."))
			return
		}
		createMu.Lock()
		defer createMu.Unlock()
		detectFrp(true)
		if msg, code := c.Lang.createProblem(bin, id); msg != "" {
			c.Fail(code, msg)
			return
		}
		used := usedFrpPorts("")
		var content string
		var err error
		if src := str(data, "copy_from", ""); src != "" {
			inst := getInstance(src)
			if inst == nil || inst.Type != bin || inst.Source == "docker" || !instanceAllowed(c.currentUser(), src) {
				c.Fail(http.StatusBadRequest, c.M("Instance à copier inconnue : "+src, "Unknown instance to copy: "+src))
				return
			}
			text, ok := readInstanceConfig(inst)
			if !ok {
				c.Fail(http.StatusBadRequest, c.M("Configuration de "+src+" illisible", "Configuration of "+src+" unreadable"))
				return
			}
			content, err = c.Lang.copyInstanceConfig(bin, src, text, id, used)
		} else {
			content, err = c.Lang.newInstanceConfig(bin, id, used)
		}
		if err != nil {
			c.Fail(http.StatusBadRequest, err.Error())
			return
		}
		ok, msg, code := c.Lang.createInstance(bin, id, filepath.Join(frpConfDir, id+".toml"), content)
		c.JSON(J{"ok": ok, "msg": msg, "id": id}, code)
	}))

	// Configs sans service (mode systemd) : proposées sur le tableau de bord
	// Configs without a service (systemd mode): offered on the dashboard
	route("GET /api/instance/orphans", "api_instance_orphans", perm(P("instances.create"), func(c *Ctx) {
		detectFrp(false)
		c.JSON(J{"ok": true, "orphans": c.Lang.orphanConfigs()})
	}))

	// Service d'une config sans service ; fix : journal et ports en conflit
	// corrigés dans la config, rien d'autre n'y change
	// Service of a config without a service; fix: conflicting log and ports
	// corrected in the config, nothing else changes in it
	route("POST /api/instance/adopt", "api_instance_adopt", perm(P("instances.create"), func(c *Ctx) {
		data := c.Body()
		conf := str(data, "config_path", "")
		createMu.Lock()
		defer createMu.Unlock()
		detectFrp(true)
		var orphan J
		for _, o := range c.Lang.orphanConfigs() {
			if o["config_path"] == conf {
				orphan = o
			}
		}
		if orphan == nil {
			c.Fail(http.StatusNotFound, c.M("Aucune configuration sans service à cet emplacement : "+conf, "No configuration without a service at this location: "+conf))
			return
		}
		bin, id := orphan["type"].(string), orphan["id"].(string)
		if msg, code := c.Lang.createProblem(bin, id); msg != "" {
			c.Fail(code, msg)
			return
		}
		if truthy(data["fix"]) {
			b, err := os.ReadFile(conf)
			if err != nil {
				c.Fail(http.StatusInternalServerError, err.Error())
				return
			}
			fixed, err := c.Lang.fixInstanceConfig(bin, id, conf, string(b))
			if err != nil {
				c.Fail(http.StatusBadRequest, err.Error())
				return
			}
			if fixed != string(b) {
				if ok, msg, code := c.Lang.writeConfigFile(conf, fixed); !ok {
					c.Fail(code, msg)
					return
				}
			}
		}
		ok, msg, code := c.Lang.createInstance(bin, id, conf, "")
		c.JSON(J{"ok": ok, "msg": msg, "id": id}, code)
	}))
}

// bracketBalance : crochets et accolades ouverts moins fermés, hors chaînes
// et commentaires
// bracketBalance: open minus closed brackets and braces, outside strings and
// comments
func bracketBalance(s string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return depth
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		}
	}
	return depth
}
