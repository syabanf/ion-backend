-- Wave 94 down — drop the whole reseller schema. CASCADE so the four
-- tables + indexes go with it. Safe because no other schema references
-- reseller.* (cross-context UUIDs are plain, not FKs).
DROP SCHEMA IF EXISTS reseller CASCADE;
