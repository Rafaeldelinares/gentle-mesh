#!/usr/bin/env bash
# e2e fix-inflight (NODE_LOST en tareas ya atribuidas a un nodo que muere).
# Adaptado de ~/qs-keep/e2e/fix-inflight-e2e.sh (que requeria SSH a la VM 192.168.122.50
# y scripts lote3/launch.sh + fake-node2.py del anfitrion) a 100% local con el binario
# gentle-mesh de este repo y un worker real con --runner simulated --task-delay alto.
#
# Caso: 1 worker se une al coordinator, recibe 5 tareas que quedan running (atribuidas por
# worker_id). kill -9 al worker. Con el arreglo NODE_LOST (PR #97), las 5 tareas pasan a
# failed con codigo NODE_LOST, causa en estado/SSE/BD, territorio liberado.
#
# Variables de entorno:
#   REPO   - ruta al clon de gentle-mesh (default: directorio padre de este script)
#   WORK   - directorio temporal (default: mktemp /tmp/e2e-inflight-XXXXXX)
#   PORT   - puerto del coordinator (default: 8630)
#   WP     - puerto del worker (default: 8631)
#   NTASKS - numero de tareas a enviar (default: 5)
#   DELAY  - delay del simulated runner del worker (default: 60s, para que las tareas
#            queden running cuando lo matamos)
#   HBT    - heartbeat-timeout del coord (default: 5s)
set -u
REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
WORK="${WORK:-$(mktemp -d /tmp/e2e-inflight-XXXXXX)}"
PORT="${PORT:-8630}"; WP="${WP:-8631}"
NTASKS="${NTASKS:-5}"
DELAY="${DELAY:-60s}"
HBT="${HBT:-5s}"
BIN="$WORK/gentle-mesh"
SP_COORD=""; SP_W=""
FAIL=0
ok(){ echo "  ok: $*"; }
no(){ echo "  -> FAIL: $*"; FAIL=1; }
log(){ printf '%s\n' "$*"; }
cleanup() {
  for p in "$SP_W" "$SP_COORD"; do
    [ -n "$p" ] && kill -0 "$p" 2>/dev/null && { kill "$p" 2>/dev/null; wait "$p" 2>/dev/null; }
  done
  SP_W=""; SP_COORD=""
}
trap cleanup EXIT

log "work=$WORK repo=$REPO coord=$PORT worker=$WP ntasks=$NTASKS delay=$DELAY hbt=$HBT"
( cd "$REPO" && go build -o "$BIN" ./cmd/gentle-mesh ) || { log "BUILD FAILED"; exit 2; }
log "== build ok =="

log "== arranco coordinator en $PORT =="
( cd "$WORK" && exec "$BIN" server -addr "127.0.0.1:$PORT" -tasks-dir "$WORK/tasks" \
    -db-path "$WORK/mesh.db" -token A -heartbeat-timeout "$HBT" > "$WORK/coord.log" 2>&1 ) &
SP_COORD=$!
for _ in $(seq 1 40); do grep -q "coordinator starting" "$WORK/coord.log" 2>/dev/null && break; sleep 0.5; done
grep -q "coordinator starting" "$WORK/coord.log" || { cat "$WORK/coord.log"; log "SETUP FAILED: el coordinador no arranco"; exit 2; }
log "  SP_COORD=$SP_COORD"

log "== arranco 1 worker w-inflight en puerto $WP (--runner simulated --task-delay $DELAY) =="
( cd "$WORK" && exec "$BIN" worker -coordinator "http://127.0.0.1:$PORT" -token A -node-id "w-inflight" \
    -endpoint "http://127.0.0.1:$WP" -addr "127.0.0.1:$WP" -heartbeat-interval 1s \
    -runner simulated -task-delay "$DELAY" > "$WORK/worker.log" 2>&1 ) &
SP_W=$!
log "  SP_W=$SP_W"
for _ in $(seq 1 40); do
  n=$(curl -sS -H 'Authorization: Bearer A' "http://127.0.0.1:$PORT/v1/mesh/nodes" 2>/dev/null | grep -o '"node_id"' | wc -l)
  [ "$n" -ge 1 ] && break
  sleep 0.5
done
[ "$(curl -sS -H 'Authorization: Bearer A' "http://127.0.0.1:$PORT/v1/mesh/nodes" 2>/dev/null | python3 -c 'import json,sys
d=json.load(sys.stdin); print(len(d.get("nodes",[])))')" -ge 1 ] || { cat "$WORK/coord.log"; log "SETUP FAILED: el worker no se unio"; exit 2; }
ok "worker w-inflight unido al coordinator"

log "== mando $NTASKS tareas (todas deben quedar running por $DELAY) =="
: > "$WORK/ids"
for i in $(seq 1 "$NTASKS"); do
  curl -sS -H 'Authorization: Bearer A' -H 'Content-Type: application/json' \
    --data-binary "{\"agent\":\"worker\",\"task\":\"inflight-e2e-$i\",\"timeout_seconds\":600}" \
    "http://127.0.0.1:$PORT/v1/tasks" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("task_id",""))' >> "$WORK/ids"
done
sleep 2
log "  ids: $(tr '\n' ' ' < "$WORK/ids")"

log "== verifico que las $NTASKS quedaron running y atribuidas al worker =="
sleep 5
ATTR=0
for tid in $(cat "$WORK/ids"); do
  d=$(curl -sS -H 'Authorization: Bearer A' "http://127.0.0.1:$PORT/v1/tasks/$tid")
  st=$(printf '%s' "$d" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status",""))')
  wid=$(printf '%s' "$d" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("worker_id","") or "")')
  log "  $tid status=$st worker_id=${wid:--}"
  [ -n "$wid" ] && ATTR=$((ATTR+1))
done
ok "$ATTR de $NTASKS atribuidas al worker antes del kill"

log "== kill -9 al worker PID=$SP_W =="
kill -9 "$SP_W" 2>/dev/null; wait "$SP_W" 2>/dev/null; SP_W=""
ok "worker muerto"

log "== espero 4x el heartbeat-timeout ($HBT) para que el coordinator pase a offline =="
SLEEP_SECS=$(echo "$HBT" | sed 's/s$//' | python3 -c 'import sys; print(int(float(sys.stdin.read())*4))')
log "  durmiendo ${SLEEP_SECS}s"
sleep "$SLEEP_SECS"

log "== estado final del worker: $(curl -sS -H 'Authorization: Bearer A' "http://127.0.0.1:$PORT/v1/mesh/nodes" | python3 -c 'import json,sys
d=json.load(sys.stdin); print(next((n.get("status") for n in d.get("nodes",[]) if n.get("node_id")=="w-inflight"), "ausente"))') =="

log "== resumen por tarea (solo las atribuidas al worker cuentan para NODE_LOST) =="
ATTR=0; ATTR_NODELOST=0; bad=0; nocause=0
for tid in $(cat "$WORK/ids"); do
  d=$(curl -sS -H "Authorization: Bearer A" "http://127.0.0.1:$PORT/v1/tasks/$tid")
  st=$(printf '%s' "$d" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status",""))')
  wid=$(printf '%s' "$d" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("worker_id","") or "")')
  ecode=$(printf '%s' "$d" | python3 -c 'import json,sys
d=json.load(sys.stdin); e=d.get("error") or {}; print(e.get("code",""))')
  log "  $tid status=$st worker_id=${wid:--} code=$ecode"
  if [ -n "$wid" ]; then
    ATTR=$((ATTR+1))
    [ "$st" = "failed" ] || bad=$((bad+1))
    [ -n "$ecode" ] || nocause=$((nocause+1))
    [ "$ecode" = "NODE_LOST" ] && ATTR_NODELOST=$((ATTR_NODELOST+1))
  fi
done
log "  resumen: atribuidas=$ATTR  sin_estado_terminal=$bad  sin_causa=$nocause  NODE_LOST=$ATTR_NODELOST"

[ "$ATTR" -ge 1 ] || no "ninguna tarea llego a atribuirse al worker antes del kill (no se puede validar el fix)"
[ "$bad" = "0" ] && ok "las $ATTR atribuidas terminaron en estado terminal (failed)" || no "$bad de $ATTR atribuidas sin estado terminal"
[ "$nocause" = "0" ] && ok "las $ATTR atribuidas con causa en el estado" || no "$nocause de $ATTR atribuidas sin error.code"
[ "$ATTR_NODELOST" = "$ATTR" ] && ok "las $ATTR atribuidas tienen codigo NODE_LOST (FIXED)" || no "solo $ATTR_NODELOST de $ATTR atribuidas con NODE_LOST"

log ""
log "== RESULTADO =="
if [ "$FAIL" = "0" ]; then log "FIXED"; exit 0; fi
log "BUG PRESENT"; exit 1
