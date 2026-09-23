# AGENTS.md — Convenciones y Competencias de Gentle Mesh

> **Proyecto:** `gentle-mesh`  
> **Ubicación:** `/home/rafael/proyectos/gentle-mesh`  
> **Propósito:** Transporte distribuido federado y ejecución de subagentes remotos para el ecosistema Pi & Gentle AI.  
> **Lenguaje:** Go (Golang 1.22+)  
> **Destino Comunitario:** Propuesta RFC oficial para la comunidad de Gentleman Programming.  

---

## 1. Misión del Proyecto

`gentle-mesh` es una extensión de infraestructura para el ecosistema Pi y Gentle AI que permite ejecutar y delegar subagentes en nodos remotos (servidores dedicados, clusters de GPU, máquinas de desarrollo secundarias o dispositivos móviles/edge) a través de una API REST ultra-ligera con streaming de eventos (Server-Sent Events - SSE).

### Objetivos Principales:
1. **Desacoplar el cómputo del cliente:** Permitir que una sesión local de Pi en una laptop delegue tareas pesadas a servidores remotos sin sobrecargar el procesador ni depender de la sesión interactiva local.
2. **Compatibilidad total (No-Breaking Change):** Integrarse como un transporte enchufable (`transport: "remote"` / `transport: "mesh"`) en la configuración de subagentes de Gentle AI, manteniendo el 100% de la experiencia local intacta.
3. **Binario Único sin Dependencias:** Escrito en Go para permitir despliegues instantáneos (`zero-dependency deploy`) tanto en servidores x86_64 como en nodos ARM64 (móviles/Termux, Raspberry Pi).
4. **Seguridad y Malla Privada:** Diseñado para operar sobre redes privadas seguras (Tailscale / Wireguard) con autenticación Bearer/mTLS.

---

## 2. Pila Tecnológica y Estructura

* **Lenguaje:** Go (Golang) con biblioteca estándar (`net/http`, `os/exec`, `context`).
* **Protocolo:** HTTP REST + Server-Sent Events (SSE) para streaming unidireccional de eventos y tokens.
* **Serialización:** JSON determinista.
* **Testing:** Go testing nativo (`go test ./...`).

```text
gentle-mesh/
├── cmd/
│   └── gentle-mesh/          # Punto de entrada principal (CLI: server / client / run)
├── pkg/
│   ├── protocol/             # Tipos, eventos SSE y contratos de la API
│   ├── server/               # Demonio HTTP, gestión de subprocesos Pi y streaming
│   └── client/               # Cliente HTTP/SSE y adaptador para Gentle AI
├── docs/
│   └── rfcs/                 # Documentos de especificación técnica y propuestas
└── scripts/                  # Scripts de compilación y despliegue cross-platform
```

---

## 3. Reglas de Desarrollo

1. **Pragmatismo y Cero Dependencias Pesadas:** Priorizar la biblioteca estándar de Go (`net/http`) antes de introducir frameworks externos innecesarios.
2. **TDD y Cobertura:** Cada endpoint, parser de eventos y gestor de subprocesos debe contar con pruebas unitarias en Go.
3. **Cross-Compiling Limpio:** Garantizar que el código compile sin flags de CGO (`CGO_ENABLED=0`) para máxima portabilidad estática.
