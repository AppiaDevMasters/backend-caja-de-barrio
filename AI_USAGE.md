# Uso de IA en este repositorio

Las reglas del evento piden que cada repo declare que herramientas de IA se usaron,
para que, y en que archivos, y que guarde en `prompts/` los prompts reales con los
que se trabajo, no resumenes.

## Herramientas

| Herramienta | Para que | Donde |
| --- | --- | --- |
| Pi (harness de agentes "el Gentleman", flujo ODD) con un modelo de lenguaje | Escribir el backend Go, la documentacion y las pruebas; releer el fuente publico de Rafiki para entender el contrato antes de implementarlo. | todo el repositorio |
| Go 1.22 (paquete de Ubuntu) | Compilar, probar y formatear: `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .`. | `go.mod`, `cmd/`, `internal/` |
| Docker con Rafiki `v2.4.5-de` (imagenes publicadas del ASE de Open Payments) | Entorno real contra el que se verifico cada paso en vivo, y fuente de verdad cuando la documentacion no alcanzaba. | `rafiki/`, `Dockerfile` |
| Fuente publica de Rafiki y de Open Payments | Referencia del protocolo: el esquema de firma de la Admin API, el esquema GraphQL, la canonicalizacion JCS y la validacion de wallet addresses. | comentarios de `internal/rafikiadmin` y de `internal/ase` |

## Como se uso

- **Nada se copio de otro proyecto.** Todo el codigo se escribio durante el evento. Las
  unicas dependencias son publicas (el SDK oficial de Go de Open Payments, la imagen
  de Rafiki y la biblioteca estandar de Go).
- **Desarrollo dirigido por pruebas.** Cada comportamiento se escribio primero como una
  prueba que falla (`go test`), y despues se implemento hasta que pasa. Los commits
  registran ese orden en sus mensajes.
- **Verificacion contra el ASE real, no solo contra servidores de prueba.** Eso encontro
  tres comportamientos que ninguna prueba unitaria habria detectado por si sola: los
  escalares `UInt64` de Rafiki viajan como string JSON, la clave de idempotencia queda
  atada a los argumentos de la primera peticion, y Go escapa `U+2028`/`U+2029` cuando
  JCS los deja literales (un 401 silencioso garantizado).
- **Dos sesiones de agente trabajaron en el mismo arbol** con superficies de archivos
  disjuntas y commits separados, para no pisarse.

## Que genero cada herramienta

| Area | Contenido |
| --- | --- |
| `internal/config` | Lectura y validacion del entorno, con error al arrancar si un valor no sirve |
| `internal/rafikiadmin` | Cliente de la Admin API de Rafiki: firma HMAC, activos, wallet addresses, ajustes de tenant, liquidez y claves |
| `internal/opkeys` | Clave Ed25519 del cliente de Open Payments: generacion, PEM en modo 0600 y `kid` por huella RFC 7638 |
| `internal/opclient` | Cliente de Open Payments (SDK de Go) para el flujo de retiro |
| `internal/httpserver` | Router del backend: `/health` y el montaje de las rutas que aportan los componentes |
| `internal/ase` | Endpoints que Rafiki y el IdP llaman: `/webhooks`, `/rates` y `/idp/` |
| `cmd/backend`, `cmd/seed` | El servicio y el aprovisionamiento idempotente del ASE |
| `docs/`, `odd/` | Configuracion documentada, evaluacion del SDK de Python y el plan con su bitacora |
| `rafiki/`, `Dockerfile` | El ASE local y la imagen del backend |

Los prompts reales, sin resumir, estan en `prompts/`.

## La segunda sesion de agente

Parte de este repositorio la escribio una segunda sesion de Pi en el mismo arbol de
trabajo, con una superficie de archivos disjunta y commits propios, para que las dos
sesiones no se pisaran. Su aporte, tal como ella lo declaro y con cada hash verificado
en este repositorio:

- Superficie: `Dockerfile`, `rafiki/**`, `env.example` e `internal/ase/**`.
- Commits propios: `5699d01`, `e46968f`, `a7da80a`, `934d3a3`, `39efeed`, `e0f0e6f`,
  `f6e7ad6`, `cf1c8be`, `1369dde`, `d5a07d4`, `4889147` y `f08ffb4`.
- Contenido: el stack local del ASE, la imagen del backend, la plantilla de variables
  y los endpoints que Rafiki y el proveedor de identidad llaman (`/webhooks`,
  `/rates` e `/idp/`) con su esquema de firma.

Los mensajes entre las dos sesiones y sus instrucciones internas no se publican: son
coordinacion entre herramientas, no prompts del equipo. Lo que importo de esa
coordinacion quedo destilado en `odd/tasks/`.
