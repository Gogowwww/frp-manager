// Configuration du panel (/etc/frp-manager/frp-manager.json).
//
// La configuration reste un objet JSON libre (map) : le fichier est partagé avec
// les versions Python du panel, et une clé inconnue de cette version doit
// survivre à un enregistrement. Chaque modification copie l'objet, l'enregistre
// puis le remplace (copie à l'écriture) : un lecteur garde un instantané cohérent.

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Cfg = map[string]any

var (
	mgrConfFile = func() string {
		if p := os.Getenv("FRP_MANAGER_CONFIG"); p != "" {
			return p
		}
		return "/etc/frp-manager/frp-manager.json"
	}()
	mgrConfDir  = filepath.Dir(mgrConfFile)
	sslCertDir  = filepath.Join(mgrConfDir, "ssl")
	sslCertFile = filepath.Join(sslCertDir, "cert.pem")
	sslKeyFile  = filepath.Join(sslCertDir, "key.pem")

	cfgMu  sync.Mutex // sérialise les écritures (lecture : cfg())
	cfgVal Cfg
	cfgRW  sync.RWMutex
)

func tokenHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func defaultManagerConfig() Cfg {
	return Cfg{
		// Nouvelle installation : joignable depuis cette machine seulement.
		// Pour ouvrir au réseau : Réglages, bind_host, ou FRP_MANAGER_HOST=0.0.0.0.
		"bind_host":       "127.0.0.1",
		"bind_port":       float64(8765),
		"username":        "admin",
		"password_hash":   "",
		"secret_key":      tokenHex(32),
		"session_timeout": float64(3600),
		"ssl_enabled":     true,
		// Miroirs tiers (ghproxy, ghfast, gh-proxy) en repli de github.com pour
		// télécharger frp : pratique quand GitHub est filtré, mais c'est un
		// intermédiaire de plus dans la chaîne d'approvisionnement.
		"download_mirrors": true,
		"nicknames":        Cfg{},
		"webhooks":         []any{},
		// Comptes supplémentaires (le propriétaire reste username/password_hash)
		// et connexion unique OpenID Connect
		"users": Cfg{},
		"oidc":  Cfg{},
	}
}

func readConfigFile() (Cfg, error) {
	raw, err := os.ReadFile(mgrConfFile)
	if err != nil {
		return nil, err
	}
	var data Cfg
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("configuration vide")
	}
	return data, nil
}

func loadManagerConfig() Cfg {
	cfg := defaultManagerConfig()
	if data, err := readConfigFile(); err == nil {
		for k, v := range data {
			cfg[k] = v
		}
	}
	return cfg
}

// saveManagerConfig : écriture atomique, lisible par root seulement (hash du mot
// de passe, clé de session).
func saveManagerConfig(c Cfg) error {
	if err := os.MkdirAll(mgrConfDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := mgrConfFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o600)
	return os.Rename(tmp, mgrConfFile)
}

// cfg : instantané de la configuration (à ne pas modifier).
func cfg() Cfg {
	cfgRW.RLock()
	defer cfgRW.RUnlock()
	return cfgVal
}

func setCfg(c Cfg) {
	cfgRW.Lock()
	cfgVal = c
	cfgRW.Unlock()
}

// cfgUpdate applique patch sur une copie, l'enregistre puis la publie.
func cfgUpdate(patch Cfg) (Cfg, error) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfgUpdateLocked(patch)
}

func cfgUpdateLocked(patch Cfg) (Cfg, error) {
	next := make(Cfg, len(cfgVal)+len(patch))
	for k, v := range cfg() {
		next[k] = v
	}
	for k, v := range patch {
		next[k] = v
	}
	if err := saveManagerConfig(next); err != nil {
		return nil, err
	}
	setCfg(next)
	return next, nil
}

// ensureManagerConfig : au démarrage, le fichier existe, avec sa clé de session,
// et n'est lisible que par root. Une installation antérieure sans fichier (panel
// jamais configuré) garde son ancienne adresse d'écoute, 0.0.0.0 : la passer à
// 127.0.0.1 couperait l'accès à distance à la mise à jour.
func ensureManagerConfig() {
	data, err := readConfigFile()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[WARN] %s illisible : laissé tel quel", mgrConfFile)
		return
	}
	exists := err == nil
	if exists && str(data, "secret_key", "") != "" {
		if _, ok := data["bind_host"]; ok {
			_ = os.Chmod(mgrConfFile, 0o600)
			return
		}
	}
	c := copyCfg(cfg())
	if !exists {
		// Nouvelle installation : FRP_MANAGER_PORT (docker-compose.yml) donne le port
		if p, err := strconv.Atoi(os.Getenv("FRP_MANAGER_PORT")); err == nil {
			c["bind_port"] = float64(p)
		}
		// Certificat déjà généré par un panel antérieur
		if fileExists(sslCertFile) {
			c["bind_host"] = "0.0.0.0"
		}
	} else if _, ok := data["bind_host"]; !ok {
		c["bind_host"] = "0.0.0.0"
	}
	if err := saveManagerConfig(c); err != nil {
		log.Printf("[WARN] Configuration non enregistrée : %v", err)
	}
	setCfg(c)
}

func copyCfg(c Cfg) Cfg {
	out := make(Cfg, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}

// deepCopy : copie complète d'une valeur JSON (map, liste, scalaires).
func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = deepCopy(val)
		}
		return out
	default:
		return v
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ── Accès typés aux valeurs JSON ────────────────────────────────────────────

func str(m map[string]any, key, def string) string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return pyStr(v)
	}
	return def
}

// num : valeur numérique (nombre JSON ou texte numérique), sinon def.
func num(m map[string]any, key string, def float64) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	case bool:
		if v {
			return 1
		}
		return 0
	}
	return def
}

func intv(m map[string]any, key string, def int) int {
	f := num(m, key, math.NaN())
	if math.IsNaN(f) {
		return def
	}
	return int(f)
}

// boolv : valeur « vraie » au sens de Python (clé absente : def).
func boolv(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok {
		return def
	}
	return truthy(v)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case []string:
		return len(x) > 0
	}
	return true
}

func mapv(m map[string]any, key string) map[string]any {
	if x, ok := m[key].(map[string]any); ok {
		return x
	}
	return map[string]any{}
}

func listv(m map[string]any, key string) []any {
	if x, ok := m[key].([]any); ok {
		return x
	}
	return nil
}

// pyStr : texte d'une valeur JSON comme le donnerait str() en Python.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
