# TLS Mesh Security — Opción A: CA Compartida

## Objetivo
Cifrar todo el tráfico de la malla gentle-mesh con TLS. El coordinator actúa como CA raíz de la malla.

## Arquitectura

```
Coordinador (inicia la malla)
    │
    ├─ Genera: CA raíz (gentle-mesh-ca.pem)
    ├─ Genera: certificado de servidor (coordinator.pem + .key)
    │
    ▼
Nodos (se unen a la malla)
    │
    ├─ Descargan CA del coordinador en el primer join (/v1/mesh/ca)
    └─ Confían en el CA para TLS
```

## Plan de implementación

### Fase 1: Infraestructura PKI
- [ ] `pkg/pki/pki.go` — Paquete para generación de CA, certificados y keys RSA
- [ ] Tests de PKI

### Fase 2: Servidor TLS
- [ ] Flags CLI: `-tls-enable`, `-tls-init`, `-tls-dir`
- [ ] Generación automática de CA + certificados en `-tls-init`
- [ ] Servidor HTTPS con `ListenAndServeTLS`
- [ ] Endpoint público `/v1/mesh/ca` para descargar CA.pem

### Fase 3: Cliente y Worker TLS
- [ ] Flag `-ca` en `runRPC` y `run` para especificar CA
- [ ] Worker: descarga automática de CA al hacer join
- [ ] Validación de certificado en cliente HTTP

### Fase 4: Verificación
- [ ] Test de integración: coordinator TLS + client con CA
- [ ] Test de integración: worker join + heartbeat sobre TLS

## Decisiones
- RSA 2048 bits mínimo para CA
- RSA 2048 bits para certificados de servidor
- CA expira en 10 años, certificados en 1 año
- CA descargable sin autenticación (es证书 pública)
