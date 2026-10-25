-- Base D1 du récepteur de statistiques / D1 database of the statistics receiver
-- Compteurs par jour (aucune adresse) et, pour le jour en cours seulement, le
-- nombre d'envois par empreinte d'adresse (limite d'abus).
-- Daily counters (no address) and, for the current day only, the number of pings
-- per address hash (abuse limit).
CREATE TABLE IF NOT EXISTS pings (
  day TEXT NOT NULL,
  key TEXT NOT NULL,
  n INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, key)
);
CREATE TABLE IF NOT EXISTS ips (
  day TEXT NOT NULL,
  hash TEXT NOT NULL,
  n INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, hash)
);
-- Mots de passe refusés sur /prive/ (instant en millisecondes), tous visiteurs
-- confondus : au-delà de 10 en 10 minutes, l'espace privé répond 429. Le Worker
-- efface les lignes sorties de la fenêtre à chaque refus ; l'effacement ci-dessous
-- fait le même ménage à chaque application du schéma.
-- Passwords refused on /prive/ (instant in milliseconds), all visitors together:
-- beyond 10 in 10 minutes, the private area answers 429. The Worker deletes the rows
-- out of the window on each refusal; the deletion below does the same housekeeping
-- each time the schema is applied.
CREATE TABLE IF NOT EXISTS fails (
  at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS fails_at ON fails (at);
DELETE FROM fails WHERE at <= (CAST(strftime('%s', 'now') AS INTEGER) - 600) * 1000;
