# AGENTS.md — Convenciones y Competencias de Gentle Mesh

> **Proyecto:** `gentle-mesh`  
> **Ubicación:** `/home/rafael/proyectos/gentle-mesh`  
> **Propósito:** Transporte distribuido federado y ejecución de subagentes remotos para el ecosistema Pi & Gentle AI.  
> **Lenguaje:** Go (Golang 1.22+)  
> **Destino Comunitario:** Propuesta RFC oficial para la comunidad de Gentleman Programming.  

---

## 1. Misión del Proyecto

`gentle-mesh` es una extensión de infraestructura para el ecosistema Pi y Gentle AI que permite ejecutar y delegar subagentes en nodos remotos (servidores dedicados, clusters de GPU, máquinas de desarrollo secundarias o dispositivos móviles/edge) a través de una API HTTPS REST ultra-segura con streaming de eventos (Server-Sent Events - SSE).

### Objetivos Principales:
1. **Desacoplar el cómputo del cliente:** Permitir que una sesión local de Pi en una laptop delegue tareas pesadas a servidores remotos sin sobrecargar el procesador ni depender de la sesión interactiva local.
2. **Compatibilidad total (No-Breaking Change):** Integrarse como un transporte enchufable (`transport: "remote"` / `transport: "mesh"`) en la configuración de subagentes de Gentle AI, manteniendo el 100% de la experiencia local intacta.
3. **Binario Único sin Dependencias:** Escrito en Go para permitir despliegues instantáneos (`zero-dependency deploy`) tanto en servidores x86_64 como en nodos ARM64 (móviles/Termux, Raspberry Pi).
4. **Seguridad Zero-Trust y Malla Cifrada:** Diseñado con postura de seguridad en profundidad (Zero Trust). Toda comunicación viaja cifrada de extremo a extremo mediante HTTPS/mTLS (con autenticación mutua de certificados PKI y tokens CSR) aun cuando opere dentro de redes privadas seguras (Tailscale / WireGuard).

---

## 2. Pila Tecnológica y Estructura

* **Lenguaje:** Go (Golang) con biblioteca estándar (`net/http`, `os/exec`, `context`, `database/sql`).
* **Persistencia Dual:** JSONL append-only para streaming de eventos SSE y repetición con `Last-Event-ID` + SQLite embebido en Go puro (`modernc.org/sqlite`, `CGO_ENABLED=0`) para persistencia ACID de estado y crash recovery / rehidratación.
* **Protocolo:** HTTPS REST + Server-Sent Events (HTTPS/SSE) para streaming unidireccional seguro de eventos y tokens con cifrado en tránsito de nivel aplicación.
* **Serialización:** JSON determinista.
* **Testing:** Go testing nativo (`go test ./...`).

```text
gentle-mesh/
├── cmd/
│   └── gentle-mesh/          # Punto de entrada principal (CLI: server / client / run)
├── pkg/
│   ├── client/               # Cliente HTTPS/SSE y adaptador para Gentle AI
│   ├── protocol/             # Tipos, eventos SSE y contratos de la API
│   └── server/               # Demonio HTTPS, gestión de subprocesos Pi y streaming
│       ├── federation/       # Peering M2M y conciencia situacional territorial compartida
│       ├── http/             # Servidor HTTPS REST y endpoints SSE
│       ├── registry/         # Registro de nodos de la malla y keepalive (heartbeats)
│       ├── runner/           # Ejecución de subprocesos Pi headless
│       ├── store/            # Almacén de persistencia (SQLite pure-Go y memoria)
│       ├── task/             # Ciclo de vida de tareas, pub/sub SSE y loggers JSONL
│       └── worker/           # Pool de workers y gestión de cola de ejecución
├── docs/
│   └── rfcs/                 # Documentos de especificación técnica y propuestas
└── scripts/                  # Scripts de compilación y despliegue cross-platform
```

---

## 3. Reglas de Desarrollo

1. **Pragmatismo y Cero Dependencias Pesadas:** Priorizar la biblioteca estándar de Go (`net/http`) antes de introducir frameworks externos innecesarios.
2. **TDD y Cobertura:** Cada endpoint, parser de eventos y gestor de subprocesos debe contar con pruebas unitarias en Go.
3. **Cross-Compiling Limpio:** Garantizar que el código compile sin flags de CGO (`CGO_ENABLED=0`) para máxima portabilidad estática.
