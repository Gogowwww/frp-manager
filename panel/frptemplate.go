// Modèles des configs frp : frpc et frps passent leur fichier par text/template
// avant de lire le TOML ; frp 0.56 et plus y ajoutent parseNumberRange et
// parseNumberRangePair, dont la page Ports se sert pour les plages de ports. Le
// panel n'exécute jamais un modèle : il vérifie sa syntaxe et lit le TOML avec
// les actions neutralisées.
// Templates in frp configs: frpc and frps run their file through text/template
// before reading the TOML; frp 0.56 and later add parseNumberRange and
// parseNumberRangePair, which the Ports page uses for port ranges. The panel
// never executes a template: it checks its syntax and reads the TOML with the
// actions neutralized.

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

var (
	// Action d'un modèle sur une ligne, et ligne faite seulement d'actions
	// A template action on one line, and a line made only of actions
	frpActionRe     = regexp.MustCompile(`\{\{.*?\}\}`)
	frpActionLineRe = regexp.MustCompile(`^\s*(?:\{\{.*?\}\}\s*)+$`)
	// Plage de ports écrite par la page Ports : ligne « range » devant le bloc,
	// nom « nom-{{ $v.First }} » dans le bloc
	// Port range written by the Ports page: "range" line in front of the block,
	// "name-{{ $v.First }}" name inside the block
	frpRangeLineRe = regexp.MustCompile(`^\{\{-?\s*range\s+\$_,\s*\$v\s*:=\s*parseNumberRangePair\s+"([^"]*)"\s+"([^"]*)"\s*-?\}\}$`)
	frpRangePairRe = regexp.MustCompile(`parseNumberRangePair\s+"([^"]*)"\s+"([^"]*)"`)
	frpFirstRe     = regexp.MustCompile(`\{\{\s*\$v\.First\s*\}\}`)
	frpSecondRe    = regexp.MustCompile(`^remotePort\s*=\s*\{\{\s*\$v\.Second\s*\}\}`)
)

// frpTOML : la config lisible en TOML. Les lignes faites seulement d'actions
// ({{- range … }}, {{- end }}) deviennent vides, les autres actions valent 0
// (« "web-{{ $v.First }}" » → « "web-0" ») ; les numéros de ligne ne changent pas,
// pour les messages d'erreur.
// frpTOML: the config readable as TOML. Lines made only of actions
// ({{- range … }}, {{- end }}) become empty, other actions become 0
// ("web-{{ $v.First }}" → "web-0"); line numbers do not change, for error
// messages.
func frpTOML(content string) string {
	if !strings.Contains(content, "{{") {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if frpActionLineRe.MatchString(line) {
			lines[i] = ""
			continue
		}
		lines[i] = frpActionRe.ReplaceAllString(line, "0")
	}
	return strings.Join(lines, "\n")
}

// frpPortRange : ports d'une plage frp (« 6000-6006,6007 »), comme
// ParseRangeNumbers de frp, ports de 1 à 65535 et 65535 au plus.
// frpPortRange: ports of an frp range ("6000-6006,6007"), like frp's
// ParseRangeNumbers, ports from 1 to 65535 and 65535 of them at most.
func frpPortRange(spec string) ([]int, error) {
	var ports []int
	for _, part := range strings.Split(strings.TrimSpace(spec), ",") {
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return nil, fmt.Errorf("%q: invalid port range", spec)
		}
		lo, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
		hi, err2 := lo, error(nil)
		if len(bounds) == 2 {
			hi, err2 = strconv.Atoi(strings.TrimSpace(bounds[1]))
		}
		if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi || len(ports)+hi-lo+1 > 65535 {
			return nil, fmt.Errorf("%q: invalid port range", spec)
		}
		for p := lo; p <= hi; p++ {
			ports = append(ports, p)
		}
	}
	return ports, nil
}

// checkFrpTemplate : erreur si frp refuserait le modèle de la config : syntaxe,
// fonction inconnue, plages de ports invalides ou de tailles différentes.
// checkFrpTemplate: error if frp would reject the config's template: syntax,
// unknown function, invalid port ranges or ranges of different sizes.
func checkFrpTemplate(content string) error {
	if !strings.Contains(content, "{{") {
		return nil
	}
	// Les fonctions de frp, déclarées sans être appelées : Parse n'exécute rien
	// frp's functions, declared without being called: Parse executes nothing
	funcs := template.FuncMap{
		"parseNumberRange":     func(string) ([]int, error) { return nil, nil },
		"parseNumberRangePair": func(string, string) ([]int, error) { return nil, nil },
	}
	if _, err := template.New("frp").Funcs(funcs).Parse(content); err != nil {
		return err
	}
	for _, m := range frpRangePairRe.FindAllStringSubmatch(content, -1) {
		first, err := frpPortRange(m[1])
		if err != nil {
			return err
		}
		second, err := frpPortRange(m[2])
		if err != nil {
			return err
		}
		if len(first) != len(second) {
			return fmt.Errorf("parseNumberRangePair %q %q: %d and %d ports, not in pairs", m[1], m[2], len(first), len(second))
		}
	}
	return nil
}
