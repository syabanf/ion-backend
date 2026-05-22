-- 0004 — DOWN: tear down the network schema.
BEGIN;

DROP TRIGGER IF EXISTS trg_radius_accounts_touch ON network.radius_accounts;
DROP TRIGGER IF EXISTS trg_network_nodes_touch ON network.nodes;
DROP FUNCTION IF EXISTS network.touch_updated_at();

DROP TABLE IF EXISTS network.ip_assignments;
DROP TABLE IF EXISTS network.ip_pools;
DROP TABLE IF EXISTS network.vlan_pools;
DROP TABLE IF EXISTS network.radius_accounts;

-- Drop circular FK first so the tables can come down in any order.
ALTER TABLE network.nodes DROP CONSTRAINT IF EXISTS fk_nodes_upstream_port;
DROP TABLE IF EXISTS network.ports;
DROP TABLE IF EXISTS network.nodes;
DROP TABLE IF EXISTS network.node_types;

DROP SCHEMA IF EXISTS network;

COMMIT;
