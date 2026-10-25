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
