#!/usr/bin/env bash
# e2e fix-4: un worker que deja de latir debe salir de "online" (registro) y el router no debe
# seguir mandandole tareas: tienen que completarse en otro worker vivo.
#
#   fase A: tres workers vivos -> 4 tareas; los despachos se reparten entre los vivos
#   kill -9 <PID> con -heartbeat-timeout 5s
#   fase B: se miden los estados del nodo muerto y el destino/resultado de las tareas siguientes
#   fase C: se mata al segundo worker y se despacha INMEDIATAMENTE (sin esperar 1.5x el timeout):
#           las tareas deben completarse en el tercer worker gracias al reintento
#
# Variables de entorno:
#   REPO   - ruta al clon de gentle-mesh (default: directorio padre de este script)
#   WORK   - directorio temporal de trabajo (default: mktemp /tmp/e2e-fix4-XXXXXX)
#   PORT   - puerto del coordinator (default: 8603)
#   P[ABC] - puertos de los workers (default: 8604/8605/8606)
set -u
export PATH="${PATH:-}"
REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
PORT="${PORT:-8603}"; PA="${PA:-8604}"; PB="${PB:-8605}"; PC="${PC:-8606}"
WORK="${WORK:-$(mktemp -d /tmp/e2e-fix4-XXXXXX)}"
BIN="$WORK/gentle-mesh"
SP_COORD=""; SP_WA=""; SP_WB=""; SP_WC=""
FAIL=0

log() { printf '%s\n' "$*"; }
fail() { log "  -> FAIL: $*"; FAIL=1; }
ok()   { log "  ok: $*"; }
cleanup() {
  for p in "$SP_WA" "$SP_WB" "$SP_WC" "$SP_COORD"; do
    [ -n "$p" ] && kill -0 "$p" 2>/dev/null && { kill "$p" 2>/dev/null; wait "$p" 2>/dev/null; }
  done
  SP_COORD=""; SP_WA=""; SP_WB=""; SP_WC=""
}
trap cleanup EXIT

log "work=$WORK  repo=$REPO  heartbeat-timeout=5s  ports coord=$PORT w-a=$PA w-b=$PB w-c=$PC"
( cd "$REPO" && go build -o "$BIN" ./cmd/gentle-mesh ) || { log "BUILD FAILED"; exit 2; }
log "== build ok (rama $(cd "$REPO" && git rev-parse --abbrev-ref HEAD) $(cd "$REPO" && git rev-parse --short HEAD)) =="

log "== arranco el coordinador en $PORT con -heartbeat-timeout 5s =="
( cd "$WORK" && exec "$BIN" server -addr "127.0.0.1:$PORT" -tasks-dir "$WORK/tasks" \
    -db-path "$WORK/mesh.db" -token A -heartbeat-timeout 5s > "$WORK/coord.log" 2>&1 ) &
SP_COORD=$!
for _ in $(seq 1 40); do grep -q "coordinator starting" "$WORK/coord.log" 2>/dev/null && break; sleep 0.5; done
grep -q "coordinator starting" "$WORK/coord.log" || { cat "$WORK/coord.log"; log "SETUP FAILED: el coordinador no arranco"; exit 2; }
log "  SP_COORD=$SP_COORD"

start_worker() {
  ( cd "$WORK" && exec "$BIN" worker -coordinator "http://127.0.0.1:$PORT" -token A -node-id "$1" \
      -endpoint "http://127.0.0.1:$2" -addr "127.0.0.1:$2" -heartbeat-interval 1s > "$3" 2>&1 ) &
  echo $!
}
SP_WA=$(start_worker w-a "$PA" "$WORK/worker-a.log"); sleep 1
SP_WB=$(start_worker w-b "$PB" "$WORK/worker-b.log"); sleep 1
SP_WC=$(start_worker w-c "$PC" "$WORK/worker-c.log")
log "  SP_WA(w-a)=$SP_WA  SP_WB(w-b)=$SP_WB  SP_WC(w-c)=$SP_WC"
for _ in $(seq 1 40); do
  n=$(curl -sS -H 'Authorization: Bearer A' "http://127.0.0.1:$PORT/v1/mesh/nodes" 2>/dev/null | grep -o '"node_id"' | wc -l)
  [ "$n" -ge 3 ] && break
  sleep 0.5
done
nodes() { curl -sS -H 'Authorization: Bearer A' "http://127.0.0.1:$PORT/v1/mesh/nodes" 2>/dev/null; }
estado() { nodes | python3 -c "import json,sys
d=json.load(sys.stdin)
print(next((n.get('status') for n in d.get('nodes',[]) if n.get('node_id')=='$1'), 'ausente'))"; }
log "  nodos al empezar:"; nodes | python3 -c 'import json,sys
d=json.load(sys.stdin)
for n in d.get("nodes",[]): print("    %-5s status=%-9s" % (n.get("node_id"), n.get("status")))' | sed 's/^/  /'

new_task() {
  local label="$1" tid st resp
  resp=$(curl -sS --max-time 20 -H "Authorization: Bearer A" -H 'Content-Type: application/json' \
    --data-binary "{\"agent\":\"worker\",\"task\":\"$label\",\"git_repo\":\"r\"}" "http://127.0.0.1:$PORT/v1/tasks")
  tid=$(printf '%s' "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("task_id",""))')
  st=""
  for _ in $(seq 1 25); do
    st=$(curl -sS -H "Authorization: Bearer A" "http://127.0.0.1:$PORT/v1/tasks/$tid" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')
    [ "$st" = "completed" ] || [ "$st" = "failed" ] && break
    sleep 1
  done
  echo "$tid $st"
}
PHASE_RESULT=""
run_phase() {
  local label="$1" n="$2" okc=0 st r i
  for i in $(seq 1 "$n"); do
    r=$(new_task "$label$i"); st="${r##* }"
    log "  tarea $label$i -> id=${r%% *} status=$st"
    [ "$st" = "completed" ] && okc=$((okc+1))
  done
  PHASE_RESULT="$okc/$n"
}

log ""
log "################ FASE A: tres workers vivos, 4 tareas ################"
run_phase A 4; A="$PHASE_RESULT"
log "  completadas: $A"
log "  despachos de la fase A:"; grep -o 'dispatching task.*node "[^"]*"' "$WORK/coord.log" | sed 's/^/    /' | sort | uniq -c | sed 's/^/   /'
[ "$A" = "4/4" ] || fail "fase A: solo $A completadas (los tres workers estan vivos)"
A_NODES=$(grep -o 'dispatching task.*node "[^\"]*"' "$WORK/coord.log" | grep -o 'node "[^\"]*"' | sort -u | wc -l)
[ "$A_NODES" -ge 2 ] || fail "fase A: las 4 tareas fueron a $A_NODES nodo(s), se espera reparto entre los vivos"

log ""
log "################ kill -9 al nodo ELEGIDO (w-a, PID $SP_WA) ################"
kill -9 "$SP_WA" 2>/dev/null; wait "$SP_WA" 2>/dev/null; SP_WA=""
log "  w-a muerto (kill -9), w-b=$SP_WB y w-c=$SP_WC siguen vivos"

log ""
log "################ FASE B: tras 20s (3x el timeout de 5s) ################"
for i in $(seq 1 4); do sleep 5; log "  t=$((i*5))s  w-a=$(estado w-a)  w-b=$(estado w-b)  w-c=$(estado w-c)"; done
log "  estado final del nodo muerto w-a: '$(estado w-a)' (esperado offline)"
run_phase B 4; B="$PHASE_RESULT"
log "  completadas: $B"
log "  ejecuciones en w-b: $(grep -c 'executing task' "$WORK/worker-b.log")  en w-c: $(grep -c 'executing task' "$WORK/worker-c.log")"

log ""
log "################ FASE C: mato a w-b y despacho YA, sin esperar el timeout ################"
kill -9 "$SP_WB" 2>/dev/null; wait "$SP_WB" 2>/dev/null; SP_WB=""
log "  w-b muerto (kill -9); estado que reporta el registro AHORA: w-b=$(estado w-b) (deberia seguir online: su heartbeat es fresco)"
log "  despacho inmediato de 4 tareas:"
run_phase C 4; C="$PHASE_RESULT"
log "  completadas: $C"
RETRIES=$(grep -c 'did not accept the task' "$WORK/coord.log")
log "  reintentos registrados en el log del coordinador: $RETRIES"
log "  ejecuciones en w-c (vivo): $(grep -c 'executing task' "$WORK/worker-c.log")"

log ""
log "################ veredicto ################"
[ "$(estado w-a)" = "offline" ] && ok "el registro marco offline al primer nodo muerto" || fail "el nodo muerto w-a sigue en '$(estado w-a)'"
[ "$B" = "4/4" ] && ok "las 4 tareas de la fase B se completaron" || fail "fase B: solo $B completadas (se despacharon al nodo muerto)"
grep -q 'dispatching task.*node "w-b"' "$WORK/coord.log" && ok "el router uso a w-b cuando w-a salio de rotacion" || fail "el router no uso a w-b tras la muerte de w-a"
[ "$C" = "4/4" ] && ok "las 4 tareas de la fase C se completaron sin esperar el timeout (reintento)" || fail "fase C: solo $C completadas (el nodo recien muerto se trago las tareas)"
[ "$RETRIES" -ge 1 ] && ok "hubo $RETRIES reintento(s) por nodo que no acepto la tarea" || fail "no hubo ningun reintento en la fase C"
[ "$(grep -c 'executing task' "$WORK/worker-c.log")" -ge 1 ] && ok "w-c ejecuto trabajo de la fase C" || fail "w-c no ejecuto nada en la fase C"

log ""
cleanup; sleep 1
LEFT=$(pgrep -x gentle-mesh | wc -l)
log "  procesos gentle-mesh: $LEFT"
for p in $PORT $PA $PB $PC; do log "  listeners en $p: $(ss -ltn 2>/dev/null | grep -c ":$p ")"; done
[ "$LEFT" = "0" ] || fail "quedan $LEFT procesos"

log ""
log "== RESULTADO =="
if [ "$FAIL" -eq 0 ]; then log "FIXED"; exit 0; fi
log "BUG PRESENT"; exit 1
