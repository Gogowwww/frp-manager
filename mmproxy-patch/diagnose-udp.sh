#!/usr/bin/env bash
# Diagnostic « IP réelle UDP » (go-mmproxy) — à lancer en root sur l'hôte frpc.
#   sudo bash diagnose-udp.sh
# Ne modifie rien : lecture seule. Copie-colle toute la sortie.
set +e
c(){ echo; echo "════ $* ════"; }

c "1. Binaire go-mmproxy (le fix anti-crash est-il en place ?)"
BIN=/usr/local/bin/go-mmproxy
if [ -x "$BIN" ]; then
  ls -l "$BIN"; sha256sum "$BIN" 2>/dev/null
  # La v0.0.23 corrige le panic UDP. Un binaire d'avant crashe au 1er paquet.
else
  echo "ABSENT — go-mmproxy non installé"
fi

c "2. Unités relais + règles de routage (actives ? qui crashe ?)"
systemctl --no-pager list-units 'frp-mmproxy-*' 2>/dev/null
echo "--- routes ---"
systemctl --no-pager status frp-mmproxy-routes.service 2>/dev/null | sed -n '1,6p'
echo "--- chaque relais UDP : état + éventuel panic ---"
for u in $(systemctl list-units --all --plain --no-legend 'frp-mmproxy-*' 2>/dev/null | awk '{print $1}'); do
  [ "$u" = "frp-mmproxy-routes.service" ] && continue
  echo ">> $u"
  systemctl show "$u" -p ExecStart --value 2>/dev/null
  systemctl is-active "$u" 2>/dev/null
  journalctl -u "$u" -n 8 --no-pager 2>/dev/null | grep -iE 'panic|error|listening|new connection|runtime' | tail -8
done

c "3. Règles ip rule / route table 123 (retour des réponses usurpées)"
ip rule show 2>/dev/null | grep -E '127|lookup 123' || echo "AUCUNE règle 123 — retour UDP cassé"
ip route show table 123 2>/dev/null || echo "table 123 vide"

c "4. rp_filter (le reverse-path filtering jette les réponses usurpées)"
for k in all lo; do
  echo -n "net.ipv4.conf.$k.rp_filter = "; sysctl -n net.ipv4.conf.$k.rp_filter 2>/dev/null
done
echo "(doit être 0 ou 2 ; 1 = strict = paquets de retour jetés)"

c "5. Config frpc générée (le proxy pointe-t-il vers le relais ?)"
for f in /etc/frp/frpc*.toml /etc/frp/*frpc*.toml; do
  [ -f "$f" ] || continue
  echo "### $f"
  awk '/^\[\[proxies\]\]/{p=1} /^\[\[visitors\]\]/{p=0} p' "$f"
done

c "6. Le service local écoute-t-il sur 127.0.0.1 (PAS 0.0.0.0) en UDP ?"
echo "(go-mmproxy UDP exige un service bindé sur 127.0.0.1/::1, jamais 0.0.0.0)"
ss -ulnp 2>/dev/null | grep -E '127.0.0.1|::1|0.0.0.0' | head -20

c "7. Ports relais 18xxx en écoute UDP + qui les tient"
ss -ulnp 2>/dev/null | grep -E ':18[0-9]{3}' || echo "aucun relais 18xxx en écoute (relais non démarré ?)"

c "8. Versions"
frpc --version 2>/dev/null || echo "frpc introuvable dans le PATH"
uname -r

c "9. Capture tcpdump guidée"
echo "Lancez la capture puis générez du trafic depuis un VRAI client externe :"
echo
echo "  # RELAY = port relais 18xxx (section 7)  |  SVC = port réel du service (section 6)"
echo "  tcpdump -ni lo -X 'udp and (port RELAY or port SVC)'"
echo
echo "Ce que vous DEVEZ voir, dans l'ordre :"
echo "  A) 127.0.0.1.xxxxx > 127.0.0.1.RELAY  + payload débutant par 0d0a 0d0a 000d 0a51"
echo "     → frpc→relais OK, en-tête PROXY v2 présent."
echo "        (rien ici = problème EN AMONT : frps / tunnel / firewall UDP du VPS)"
echo "  B) <IP_CLIENT_REELLE>.xxxxx > 127.0.0.1.SVC"
echo "     → go-mmproxy a transmis ET usurpé l'IP. 🎯"
echo "        (src=127.0.0.1 au lieu de l'IP client = usurpation KO : IP_TRANSPARENT/CAP_NET_ADMIN)"
echo "        (rien vers SVC = go-mmproxy reçoit mais ne relaie pas : crash ? -v 2 ?)"
echo "  C) 127.0.0.1.SVC > <IP_CLIENT_REELLE>.xxxxx"
echo "     → le service a répondu. (pas de réponse = service bindé sur 0.0.0.0, ou pb applicatif)"
echo "        (réponse présente mais client ne reçoit rien = retour jeté : rp_filter / table 123)"
echo
echo "Pour les logs internes de go-mmproxy, passez un relais en -v 2 :"
echo "  systemctl edit <frp-mmproxy-...>.service   # ajouter ExecStart vide puis -v 2"
echo "  journalctl -u <frp-mmproxy-...> -f"

echo; echo "════ FIN — copiez toute la sortie ════"
