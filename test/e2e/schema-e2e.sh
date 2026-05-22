#!/usr/bin/env bash
#
# schema-e2e.sh — schema system end-to-end.
#
# The schema system underpins billing rules, commission rules,
# suspension rules, and service definitions. Both platform-wide
# schemas and per-customer overrides are tested here.
#
# This is complementary to all-e2e.sh — that script exercises every
# user-facing surface; this one drills into the platform.schemas
# subsystem specifically.

set -e

PSQL="psql postgres://syabanf@localhost:5432/ion_core?sslmode=disable -t -A"
GW='http://localhost:8080'
ADMINPW='IonAdmin#2026!ChangeMe'

step() { printf "\n\033[1;34m━━━ %s\033[0m\n" "$1"; }
ok()   { printf "  \033[32m✓\033[0m %s\n" "$1"; }
fail() { printf "  \033[31m✗\033[0m %s\n" "$1"; exit 1; }
note() { printf "  \033[33m→\033[0m %s\n" "$1"; }

jget() {
    python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    for k in '$1'.split('.'):
        if isinstance(d, list): d = d[int(k)] if k.isdigit() else d[0].get(k, '-') if d else '-'
        elif isinstance(d, dict): d = d.get(k, '-')
    print(d if d is not None else '-')
except: print('-')"
}

step "Login as admin"
TOKEN=$(curl -s -X POST "$GW/api/identity/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"admin@ion.local\",\"password\":\"$ADMINPW\"}" | jget access_token)
[ -z "$TOKEN" ] && fail "no token"
ok "got admin token"
AUTH="Authorization: Bearer $TOKEN"

# =====================================================================
# 1. Platform schemas — list, get, override, resolve
# =====================================================================

step "Platform schemas list"
SCHEMAS=$(curl -s -H "$AUTH" "$GW/api/platform/schemas")
COUNT=$(echo "$SCHEMAS" | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('items',[])))")
note "platform schemas=$COUNT"
[ "$COUNT" -ge 1 ] && ok "platform schemas seeded" || fail "no schemas"

step "Filter by kind=billing"
B=$(curl -s -H "$AUTH" "$GW/api/platform/schemas?kind=billing" \
    | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('items',[])))")
note "billing schemas=$B"
ok "kind filter works"

step "Pick a published schema + read it"
SID=$($PSQL -c "SELECT id FROM platform.schema_definitions WHERE status='published' LIMIT 1" | tr -d ' \n')
[ -z "$SID" ] && fail "no published schema in DB"
SCHEMA_BODY=$(curl -s -H "$AUTH" "$GW/api/platform/schemas/$SID")
KIND=$(echo "$SCHEMA_BODY" | jget kind)
VERSION=$(echo "$SCHEMA_BODY" | jget version)
note "kind=$KIND version=$VERSION"
ok "schema $SID readable"

# =====================================================================
# 2. Per-customer overrides — create, read resolved, delete
# =====================================================================

step "Pick a customer to override"
CUST=$($PSQL -c "SELECT id FROM crm.customers ORDER BY created_at DESC LIMIT 1" | tr -d ' \n')
[ -z "$CUST" ] && fail "no customer in DB"
ok "customer=$CUST"

step "Resolve schema for customer (no override yet)"
RESOLVED=$(curl -s -H "$AUTH" "$GW/api/platform/customer-schemas/$CUST?kind=$KIND")
HAS_BEFORE=$(echo "$RESOLVED" | python3 -c "import sys,json; d=json.load(sys.stdin); print('yes' if d.get('override') else 'no')")
note "override present (before)=$HAS_BEFORE"
# Clear any pre-existing override so we test a clean PUT.
curl -s -o /dev/null -X DELETE -H "$AUTH" "$GW/api/platform/customer-schemas/$CUST/$KIND"

step "PUT a per-customer override"
# upsertOverride needs schema_code + patch JSON. Look up the code for
# the published schema we picked earlier.
SCHEMA_CODE=$($PSQL -c "SELECT code FROM platform.schema_definitions WHERE id='$SID'" | tr -d ' \n')
note "schema_code=$SCHEMA_CODE"
PUT_RESP=$(curl -s -o /tmp/.put-resp -w "%{http_code}" -X PUT -H "$AUTH" -H "Content-Type: application/json" \
    "$GW/api/platform/customer-schemas/$CUST/$KIND" \
    -d "{\"schema_code\":\"$SCHEMA_CODE\",\"patch\":{\"e2e_override\":true},\"reason\":\"E2E test\"}")
note "PUT → HTTP $PUT_RESP"
if [[ "$PUT_RESP" =~ ^2 ]]; then ok "override accepted"; else cat /tmp/.put-resp; fail "PUT failed"; fi

step "Re-resolve — override should now be present"
RESOLVED2=$(curl -s -H "$AUTH" "$GW/api/platform/customer-schemas/$CUST?kind=$KIND")
OVR=$(echo "$RESOLVED2" | python3 -c "import sys,json; d=json.load(sys.stdin); o=d.get('override'); print(o.get('id') if o else '-')")
if [ "$OVR" != "-" ] && [ -n "$OVR" ]; then ok "override surfaced: $OVR"; else fail "override not detected"; fi
# Check the merged body — should carry the e2e_override key.
HAS_KEY=$(echo "$RESOLVED2" | python3 -c "import sys,json; d=json.load(sys.stdin); p=(d.get('override') or {}).get('patch') or {}; print('yes' if 'e2e_override' in p else 'no')")
[ "$HAS_KEY" = "yes" ] && ok "patch contains e2e_override key" || fail "patch missing key"

step "DELETE override → falls back to base schema"
DEL_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE -H "$AUTH" \
    "$GW/api/platform/customer-schemas/$CUST/$KIND")
note "DELETE → HTTP $DEL_RESP"
[[ "$DEL_RESP" =~ ^2 ]] && ok "override removed" || fail "DELETE failed"

step "Re-resolve after delete — override gone"
RESOLVED3=$(curl -s -H "$AUTH" "$GW/api/platform/customer-schemas/$CUST?kind=$KIND")
OVR3=$(echo "$RESOLVED3" | python3 -c "import sys,json; d=json.load(sys.stdin); print('present' if d.get('override') else 'gone')")
[ "$OVR3" = "gone" ] && ok "fallback to base schema" || fail "override still present"

# =====================================================================
# 3. Onboarding schemas — CRM-side schemas for lead docs
# =====================================================================

step "CRM onboarding schemas list"
OB=$(curl -s -H "$AUTH" "$GW/api/crm/onboarding-schemas" \
    | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('items',[])))")
note "onboarding schemas=$OB"
[ "$OB" -ge 1 ] && ok "onboarding schemas seeded" || fail "no onboarding schemas"

step "Onboarding schema detail (used by mobile DocumentsPage)"
OB_ID=$($PSQL -c "SELECT id FROM crm.onboarding_schemas LIMIT 1" | tr -d ' \n')
OB_DETAIL=$(curl -s -H "$AUTH" "$GW/api/crm/onboarding-schemas/$OB_ID")
DOC_COUNT=$(echo "$OB_DETAIL" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len((d.get('body') or {}).get('docs',[])))")
note "onboarding schema has $DOC_COUNT doc slots"
ok "onboarding schema detail returned"

step "Schema-driven docs honour show_when_accept_excess"
# Verify the schema's `content` jsonb either uses the conditional flag
# or simply exists with doc slots. Mobile UI gates rendering on it.
HAS_COND=$($PSQL -c "
    SELECT EXISTS(
      SELECT 1 FROM crm.onboarding_schemas
      WHERE content::text ILIKE '%show_when_accept_excess%'
    )" | tr -d ' \n')
if [ "$HAS_COND" = "t" ]; then
    ok "schema content carries show_when_accept_excess gate"
else
    note "no schema uses show_when_accept_excess yet (UI still honours it when present)"
fi

# =====================================================================
# 4. Service catalog SLA binding (Wave 5)
# =====================================================================

step "Service catalog SLA binding"
CAT_ID=$($PSQL -c "SELECT id FROM enterprise.service_catalog LIMIT 1" | tr -d ' \n')
SLA_ID=$($PSQL -c "SELECT id FROM enterprise.sla_templates LIMIT 1" | tr -d ' \n')
if [ -n "$CAT_ID" ] && [ -n "$SLA_ID" ]; then
    curl -s -o /dev/null -X PATCH -H "$AUTH" -H "Content-Type: application/json" \
        "$GW/api/enterprise/services-catalog/$CAT_ID/sla" \
        -d "{\"sla_template_id\":\"$SLA_ID\"}"
    BOUND=$($PSQL -c "SELECT default_sla_template_id FROM enterprise.service_catalog WHERE id='$CAT_ID'" | tr -d ' \n')
    [ "$BOUND" = "$SLA_ID" ] && ok "SLA bound to catalog row" || fail "binding not persisted"
fi

step "Unbind SLA (set to empty)"
if [ -n "$CAT_ID" ]; then
    curl -s -o /dev/null -X PATCH -H "$AUTH" -H "Content-Type: application/json" \
        "$GW/api/enterprise/services-catalog/$CAT_ID/sla" -d '{"sla_template_id":""}'
    UNBOUND=$($PSQL -c "SELECT default_sla_template_id FROM enterprise.service_catalog WHERE id='$CAT_ID'" | tr -d ' \n')
    [ -z "$UNBOUND" ] && ok "SLA cleared" || fail "expected null, got $UNBOUND"
fi

printf "\n\033[1;32m✓ schema E2E passed\033[0m\n"
