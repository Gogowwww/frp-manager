// Langue des messages : ceux renvoyés à l'interface suivent sa langue (en-tête
// X-Lang posé par api.js, paramètre ?lang= pour les journaux en direct), sinon
// celle du navigateur. Hors requête (ligne de commande, tâches de fond) : la
// langue du système (LANG), comme install.sh.

package main

import (
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type Lang string

const (
	FR Lang = "fr"
	EN Lang = "en"
)

// M : le message dans cette langue (français par défaut).
func (l Lang) M(fr, en string) string {
	if l == EN {
		return en
	}
	return fr
}

func normLang(v string) Lang {
	if strings.HasPrefix(strings.ToLower(v), "en") {
		return EN
	}
	return FR
}

// systemLang : langue de la ligne de commande et des tâches de fond.
func systemLang() Lang {
	env := os.Getenv("LC_ALL")
	if env == "" {
		env = os.Getenv("LC_MESSAGES")
	}
	if env == "" {
		env = os.Getenv("LANG")
	}
	if strings.HasPrefix(strings.ToLower(env), "fr") {
		return FR
	}
	return EN
}

func requestLang(r *http.Request) Lang {
	v := r.Header.Get("X-Lang")
	if v == "" {
		v = r.URL.Query().Get("lang")
	}
	if v == "" {
		if al := r.Header.Get("Accept-Language"); al != "" {
			// Navigateur dans une autre langue : anglais, comme l'interface
			v = bestLang(al)
		}
	}
	return normLang(v)
}

// bestLang : fr ou en selon Accept-Language (meilleure qualité ; à égalité, fr).
func bestLang(header string) string {
	best, bestQ := "en", -1.0
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, "q=") {
				if x, err := strconv.ParseFloat(f[2:], 64); err == nil {
					q = x
				}
			}
		}
		if q <= 0 {
			continue
		}
		primary := strings.SplitN(tag, "-", 2)[0]
		cand := ""
		switch primary {
		case "fr", "*":
			cand = "fr"
		case "en":
			cand = "en"
		}
		if cand == "" {
			continue
		}
		if q > bestQ || (q == bestQ && cand == "fr") {
			best, bestQ = cand, q
		}
	}
	return best
}

// Libellés des ports frp (détectés en français, parfois mis en cache pour tous
// les onglets) : traduits au moment de la réponse
var portLabelsEN = map[string]string{
	"Connexion frpc": "frpc connections", "Connexion serveur": "Server connection",
	"Dashboard web": "Web dashboard", "Connexion des clients frpc": "frpc client connections",
	"Sites HTTP (vhost)": "HTTP sites (vhost)", "Sites HTTPS (vhost)": "HTTPS sites (vhost)",
	"Tableau de bord frps": "frps dashboard", "Port réservé aux clients": "Port reserved for clients",
	"Plage réservée aux clients": "Range reserved for clients", "Port ouvert par frps": "Port opened by frps",
}

var portQuotedRe = regexp.MustCompile(`^Port « (.*) »$`)

func (l Lang) portLabel(label string) string {
	if l != EN || label == "" {
		return label
	}
	if en, ok := portLabelsEN[label]; ok {
		return en
	}
	if m := portQuotedRe.FindStringSubmatch(label); m != nil {
		return "Port “" + m[1] + "”"
	}
	if strings.HasPrefix(label, "Visiteur ") {
		return "Visitor " + label[len("Visiteur "):]
	}
	return label
}

func (l Lang) withPortLabels(items []map[string]any) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, it := range items {
		c := make(map[string]any, len(it))
		for k, v := range it {
			c[k] = v
		}
		if s, ok := it["label"].(string); ok {
			c["label"] = l.portLabel(s)
		}
		out[i] = c
	}
	return out
}
