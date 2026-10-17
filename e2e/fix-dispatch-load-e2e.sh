#!/usr/bin/env bash
# e2e fix-dispatch-load: el selector reparte las tareas entre los nodos vivos (no las apila
# en el NodeID menor). Adaptado de ~/qs-keep/e2e/fix-dispatch-load-e2e.sh (que requeria SSH
# a 192.168.122.50 y fake-node2.py del anfitrion) a 100% local con 4 workers reales de
# gentle-mesh con --runner simulated --task-delay alto.
#
# Caso 1: 4 workers con task-delay=4 y concurrencia anunciada 2, 8 tareas SIN territorio
#         compartido lanzadas a la vez. Esperado: 2/2/2/2 (con el fix de balanceo).
#         Sin el fix: 8/0/0/0 (apilamiento en el NodeID menor).
# Caso 2: los mismos 4 workers con task-delay=1 y 8 tareas SECUENCIALES. Esperado: 2/2/2/2
#         por el desempate LRU. Sin el fix: 8/0/0/0.
#
# AVISO: este test es un RED hasta que el balanceo de tareas este arreglado en main.
#        En el main actual (12c1984, 2026-10-17) el router sigue apilando en el NodeID
#        menor; este script va a salir BUG PRESENT y dejara el motivo literal en stdout.
#
# Variables de entorno:
#   REPO   - ruta al clon de gentle-mesh (default: directorio padre de este script)
#   WORK   - directorio temporal (default: mktemp /tmp/e2e-dispatch-XXXXXX)
#   PORT   - puerto del coordinator (default: 8620)
#   P[ABCD] - puertos de los 4 workers (default: 8621-8624)
set -u
REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
WORK="${WORK:-$(mktemp -d /tmp/e2e-dispatch-XXXXXX)}"
PORT="${PORT:-8620}"; PA="${PA:-8621}"; PB="${PB:-8622}"; PC="${PC:-8623}"; PD="${PD:-8624}"
BIN="$WORK/gentle-mesh"
SP_COORD=""; SP_WA=""; SP_WB=""; SP_WC=""; SP_WD=""
FAIL=0
ok(){ echo "  ok: $*"; }
no(){ echo "  -> FAIL: $*"; FAIL=1; }
log(){ printf '%s\n' "$*"; }
cleanup() {
  for p in "$SP_WA" "$SP_WB" "$SP_WC" "$SP_WD" "$SP_COORD"; do
    [ -n "$p" ] && kill -0 "$p" 2>/dev/null && { kill "$p" 2>/dev/null; wait "$p" 2>/dev/null; }
  done
  SP_WA=""; SP_WB=""; SP_WC=""; SP_WD=""; SP_COORD=""
}
trap cleanup EXIT

log "work=$WORK repo=$REPO port=$PORT"
( cd "$REPO" && go build -o "$BIN" ./cmd/gentle-mesh ) || { log "BUILD FAILED"; exit 2; }
log "== build ok =="

log "== arranco coordinator en $PORT =="
( cd "$WORK" && exec "$BIN" server -addr "127.0.0.1:$PORT" -tasks-dir "$WORK/tasks" \
    -db-path "$WORK/mesh.db" -token A -heartbeat-timeout 8s -territory-mode queue > "$WORK/coord.log" 2>&1 ) &
SP_COORD=$!
for _ in $(seq 1 40); do grep -q "coordinator starting" "$WORK/coord.log" 2>/dev/null && break; sleep 0.5; done
grep -q "coordinator starting" "$WORK/coord.log" || { cat "$WORK/coord.log"; log "SETUP FAILED"; exit 2; }
ok "coordinator PID=$SP_COORD"

start_worker() { # start_worker <node-id> <port> <logfile>
  ( cd "$WORK" && exec "$BIN" worker -coordinator "http://127.0.0.1:$PORT" -token A -node-id "$1" \
      -endpoint "http://127.0.0.1:$2" -addr "127.0.0.1:$2" -heartbeat-interval 1s \
      -runner simulated -task-delay "$3" -concurrency "$4" > "$5" 2>&1 ) &
  echo $!
}

start_nodes() { # start_nodes <delay>
  local delay=$1 conc=$2
  SP_WA=$(start_worker w-a "$PA" "$delay" "$conc" "$WORK/wa.log"); sleep 0.5
  SP_WB=$(start_worker w-b "$PB" "$delay" "$conc" "$WORK/wb.log"); sleep 0.5
  SP_WC=$(start_worker w-c "$PC" "$delay" "$conc" "$WORK/wc.log"); sleep 0.5
  SP_WD=$(start_worker w-d "$PD" "$delay" "$conc" "$WORK/wd.log")
  sleep 3
  local n=$(curl -sS -H 'Authorization: Bearer A' "http://127.0.0.1:$PORT/v1/mesh/nodes" 2>/dev/null | python3 -c 'import json,sys
d=json.load(sys.stdin); print(len(d.get("nodes",[])))')
  [ "$n" = "4" ] || { log "SETUP FAILED: hay $n nodos, esperaba 4"; cat "$WORK/coord.log"; exit 2; }
  ok "4 workers unidos al coordinator"
}

stop_nodes(){
  for p in "$SP_WA" "$SP_WB" "$SP_WC" "$SP_WD"; do
    [ -n "$p" ] && kill -0 "$p" 2>/dev/null && { kill "$p" 2>/dev/null; wait "$p" 2>/dev/null; }
  done
  SP_WA=""; SP_WB=""; SP_WC=""; SP_WD=""
  sleep 1
}

run_concurrent() { # run_concurrent <caso>
  python3 - "$PORT" "$1" <<'PY'
import json, sys, threading, time, urllib.request
from collections import Counter
PORT, CASE = int(sys.argv[1]), sys.argv[2]
B = "http://127.0.0.1:%d" % PORT
H = {"Authorization": "Bearer A", "Content-Type": "application/json"}
def req(p, body=None):
    r = urllib.request.Request(B + p, data=json.dumps(body).encode() if body is not None else None, headers=H)
    with urllib.request.urlopen(r, timeout=15) as x: return x.status, json.load(x)
def body(i):
    return {"agent": "worker", "task": "dl-%s-%02d" % (CASE, i), "git_repo": "toy/repo",
            "git_branch": "dl-%s-%02d" % (CASE, i), "domain": "d%s%02d" % (CASE, i),
            "blast_radius": "isolated-branch", "edit_surfaces": ["surf/d%s%02d" % (CASE, i)],
            "timeout_seconds": 90, "max_retries": 0}
res = {}
t0 = time.time()
def post(i):
    st, d = req("/v1/tasks", body(i))
    res[i] = d.get("task_id", "")
th = [threading.Thread(target=post, args=(i,)) for i in range(8)]
for x in th: x.start()
for x in th: x.join()
print("POST_8_ms=%.0f ACEPTADAS=%d" % ((time.time()-t0)*1000, len([t for t in res.values() if t])))
fin = {}
while time.time() - t0 < 90:
    for i, tid in res.items():
        if tid: fin[i] = req("/v1/tasks/" + tid)[1]
    if len(fin) == 8 and all(fin[i].get("status") in ("completed", "failed") for i in fin): break
    time.sleep(0.3)
c = Counter(fin[i].get("worker_id") or "-" for i in fin)
print("CASO_%s_REPARTO=%s" % (CASE, dict(sorted(c.items()))))
print("CASO_%s_ESTADOS=%s DURACION_S=%.1f" % (CASE, dict(Counter(fin[i].get("status") for i in fin)), time.time()-t0))
top = max(c.values()) if c else 0
print("CASO_%s_MAX_POR_NODO=%d (esperado<=2) NODOS_USADOS=%d (esperado=4)" % (CASE, top, len(c)))
print("VEREDICTO_%s=%s" % (CASE, "OK" if top <= 2 and len(c) == 4 else "APILAMIENTO"))
PY
}

run_sequential() {
  python3 - "$PORT" "$1" <<'PY'
import json, sys, time, urllib.request
from collections import Counter
PORT, CASE = int(sys.argv[1]), sys.argv[2]
B = "http://127.0.0.1:%d" % PORT
H = {"Authorization": "Bearer A", "Content-Type": "application/json"}
def req(p, body=None):
    r = urllib.request.Request(B + p, data=json.dumps(body).encode() if body is not None else None, headers=H)
    with urllib.request.urlopen(r, timeout=15) as x: return x.status, json.load(x)
def body(i):
    return {"agent": "worker", "task": "dl-%s-%02d" % (CASE, i), "git_repo": "toy/repo",
            "git_branch": "dl-%s-%02d" % (CASE, i), "domain": "d%s%02d" % (CASE, i),
            "blast_radius": "isolated-branch", "edit_surfaces": ["surf/d%s%02d" % (CASE, i)],
            "timeout_seconds": 90, "max_retries": 0}
t0 = time.time()
fin = {}
for i in range(8):
    st, d = req("/v1/tasks", body(i)); tid = d.get("task_id","")
    if not tid: continue
    while time.time() - t0 < 90:
        _, dd = req("/v1/tasks/" + tid)
        if dd.get("status") in ("completed","failed"):
            fin[i] = dd; break
        time.sleep(0.2)
print("8 POST SECUENCIALES")
c = Counter(fin[i].get("worker_id") or "-" for i in fin)
print("CASO_%s_REPARTO=%s" % (CASE, dict(sorted(c.items()))))
top = max(c.values()) if c else 0
print("CASO_%s_MAX_POR_NODO=%d (esperado<=2) NODOS_USADOS=%d (esperado=4)" % (CASE, top, len(c)))
print("VEREDICTO_%s=%s" % (CASE, "OK" if top <= 2 and len(c) == 4 else "APILAMIENTO"))
PY
}

log ""
log "== CASO 1: 8 tareas concurrentes, nodos con task-delay=4s concurrencia=2 =="
start_nodes 4s 2
OUT1=$(run_concurrent 1)
echo "$OUT1" | sed 's/^/  /'
case "$OUT1" in *APILAMIENTO*) no "caso 1: apilamiento (esperaba 2/2/2/2, dio reparto desigual)";; *"VEREDICTO_1=OK"*) ok "caso 1 repartido 2/2/2/2";; *) no "caso 1 indeterminado";; esac

log ""
log "== CASO 2: 8 tareas SECUENCIALES, nodos con task-delay=1s concurrencia=2 =="
stop_nodes
start_nodes 1s 2
OUT2=$(run_sequential 2)
echo "$OUT2" | sed 's/^/  /'
case "$OUT2" in *APILAMIENTO*) no "caso 2: apilamiento (esperaba 2/2/2/2)";; *"VEREDICTO_2=OK"*) ok "caso 2 repartido 2/2/2/2";; *) no "caso 2 indeterminado";; esac

log ""
log "== RESULTADO =="
if [ "$FAIL" = 0 ]; then log "FIXED"; exit 0; fi
log "BUG PRESENT"; exit 1
