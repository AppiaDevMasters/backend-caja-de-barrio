# Contrato interno del backend Go para los MCP de Caja de Barrio

Este documento cubre la integracion del contrato `contexto-caja-de-barrio/contrato-backend.md`:
la API `/internal` que consumen los cuatro MCP, la base PostgreSQL + PostGIS con las once tablas,
los proveedores Didit y Verifik, el cobro del 1% en su propia cuenta y el envoltorio HTTP del
flujo de pagos de `internal/opclient`.

Prerrequisito externo a este documento: **T7-T9** de
[`backend-go-ase-y-cliente-op.md`](backend-go-ase-y-cliente-op.md). El cliente de Open Payments
es el motor del flujo de pagos; este documento lo expone, no lo reescribe.

Decisiones cerradas el 2026-10-16 con el equipo (textual, en L2): el backend Go es dueno total de
la base y de la superficie `/internal` y los MCP son capas delgadas; el 1% de la plataforma se
cobra en una cuenta COP nueva (`app-cajadebarrio-cop`) con dos incoming payments y dos quotes; el
esquema y las migraciones viven en este repositorio; el seed crea la wallet de plataforma y las de
Andres y Rosa.

## Specs

S1. **Dueno de la base y de las llaves.** Textual del contrato: "El **backend en Go es dueno de la base** e integra **Open Payments, Didit y Verifik**. Es el **unico** que guarda esas llaves."

S2. **MCP delgados.** Textual del contrato: "Los cuatro MCP ... son **capas delgadas en Python**: cada herramienta llama un endpoint de este contrato y le devuelve el JSON al agente tal cual."

S3. **Base y autenticacion entre servicios.** Textual del contrato: "Base: `http://backend:<puerto>/internal` dentro de la red de Docker. **No se expone al publico.**" y "Autenticacion entre servicios: header `X-Servicio-Token: <SERVICIO_TOKEN>`. Sin el, `401`."

S4. **Cuerpo de error, con `codigo` y `mensaje` en espanol claro.** Textual del contrato: `{ "error": { "codigo": "SIN_VERIFICACION", "mensaje": "Primero hay que verificar la identidad en Didit." } }`. El contrato fija la tabla completa de `codigo` -> HTTP (`NO_AUTORIZADO` 401, `NO_ENCONTRADO` 404, `DATOS_INVALIDOS` 400, `SIN_POLITICAS` y siguientes 409, `HASH_INVALIDO`/`FIRMA_INVALIDA` 400/401, `PROVEEDOR` 502). Textual: "Decisiones de negocio que **no** son error (por ejemplo, riesgo que bloquea) responden `200` con el resultado."

S5. **Didit primero.** Textual del contrato: "sin verificacion de identidad aprobada en los ultimos **30 minutos** no se puede reservar."

S6. **Doble confirmacion de entrega.** Textual del contrato: "la reserva solo queda `entregada` cuando el tendero confirma (con el codigo) **y** el cliente confirma que recibio. Si el cliente dice que no recibio, queda `en_disputa`."

S7. **Regla de comisiones.** Textual del contrato: `comision_tendero = max(1000, round(monto x (0.01 + 0.02 x min(monto / efectivo_libre, 1))))`, `comision_plataforma = round(monto x 0.01)` y `total = monto + comision_tendero + comision_plataforma`. Se calcula "al reservar con el efectivo libre de la tienda en ese momento" y "**antes** de sumar al reservado".

S8. **Politicas y aprobacion escrita.** Textual del contrato: "sin aceptar las politicas vigentes (`v1-2026-10`) la persona no puede verificar identidad ni operar. El pago solo se ejecuta si existe la aprobacion escrita en el chat (APROBAR) y hay cupo."

S9. **Invariantes de seguridad.** Textual del contrato: "Ningun billete sale antes de que el pago este completado."; "No se paga sin identidad verificada, sin riesgo aprobado, sin aprobacion en el chat o por encima del cupo."; "Nunca se paga dos veces la misma reserva."; "No se guardan selfies ni fotos de documentos: solo la referencia de la sesion de Didit."; "Las cedulas se muestran enmascaradas."

S10. **Dos cobros y dos quotes.** Textual del contrato: "**Tienda:** grant no interactivo de `incoming-payment` y cobro por `monto + comision_tendero`. **App:** el mismo proceso en `OP_WALLET_PLATAFORMA`, por `comision_plataforma`." Textual de la propuesta aceptada: "para cobrar el 1% exacto en pesos, propongo crear una cuenta **COP** en el mismo usuario (`app-cajadebarrio-cop`)."

S11. **Grant del cupo.** Textual del contrato: grant de `outgoing-payment` con `limits.debitAmount` de "COP 1.000.000 o USD 300, en la moneda de la wallet", `limits.interval` `R/<hoy>T00:00:00Z/P7D` e `interact.finish` con `{method: "redirect", uri: OP_FINISH_URL + "?grant_id=" + grant_id, nonce}`.

S12. **Hash del callback interactivo.** Textual del contrato: "Verifica el hash: `base64(sha256(clientNonce\nserverNonce\ninteractRef\nauthServerUrl))`. Si falla -> `HASH_INVALIDO`."

S13. **Idempotencia del pago.** Textual del contrato: "si llega otra vez la misma `reserva_id`, se devuelve el mismo resultado; **nunca** se crean pagos nuevos." El header enviado es `Idempotency-Key: <reserva_id>`.

S14. **Pitufeo.** Textual del contrato: "si la persona tiene 3 o mas reservas `pagada`/`entregada` en los ultimos 60 minutos, se bloquea la persona, se crea una alerta `pitufeo` y se responde `bloquear`." Se evalua en este orden: coincidencia de datos, persona no bloqueada, tope del nivel y pitufeo.

S15. **Tiendas cercanas.** Textual del contrato: "Maximo 3, ordenadas por distancia." con los filtros: tienda `activa`; efectivo libre ">= monto"; "tope del nivel >= monto"; "`ST_DWithin` dentro del radio, ordenado con el operador KNN `<->`."

S16. **Reserva y codigo.** Textual del contrato: "Vence en 15 minutos; al vencer, libera el efectivo." y "El codigo de 4 digitos solo se le muestra al cliente; **nunca** se le manda al tendero."

S17. **Convenciones del contrato.** Textual: "JSON en `snake_case`. Fechas ISO 8601 en UTC."; "Montos en COP: **enteros en pesos**. Hacia Open Payments, `value` es string con el `assetScale` de cada wallet."; "Ids con prefijo legible: `per_`, `tie_`, `res_`, `did_`, `gra_`, `apr_`, `ale_`"; "Logs con `reserva_id` en cada linea de un retiro."

S18. **Esquema y migraciones en este repositorio.** Decision textual del equipo (L2): el esquema "vive en este repo (contrato)": `backend-caja-de-barrio` crea y migra las once tablas.

S19. **Rol ASE intacto.** Los tres endpoints que Rafiki llama de vuelta (`POST /webhooks`, `GET /rates`, `GET /idp/`) siguen montados en la raiz del router; `/internal` es una superficie adicional del mismo binario, no un reemplazo. Su verdad esta en `backend-go-ase-y-cliente-op.md` (T4-T6) y no se reescribe aca.

S20. **Referencia de campos.** Textual del contrato: las respuestas "son exactamente las que el agente ya usa en modo simulado (`agente-caja-de-barrio/agente/herramientas/simuladas.py`). Ese archivo sirve de **implementacion de referencia** de todas las reglas". Ese archivo no esta en esta maquina: los campos exactos de cada respuesta se fijan contra el, y hay que pedirlo antes de C6.

## Tasks

Prerrequisito: T7-T9 (cliente de Open Payments). C1 no depende de ellas y desbloquea su forma final.

| ID | Specs | Tarea | Superficie | Commit |
| --- | --- | --- | --- | --- |
| C0 | S20 | Pedir `simuladas.py` (o el repo del agente) y fijar el JSON exacto de cada endpoint contra el, antes de la primera ruta | — | — |
| C1 | S10 | Activo USD, wallet `app-cajadebarrio-cop` (COP) y wallet `andres-cajadebarrio` (USD, la misma que la seccion 4 le da a Rosa), idempotentes, con la clave del cliente en las dos cuentas de la app | `cmd/seed` | `a840597` |
| C2 | S10,S11,S12,S13 | `opclient`: dos incoming payments y dos quotes (tienda + plataforma), grant interactivo, hash, rotacion del token y pago unico por `reserva_id` | `internal/opclient` | — |
| C3 | S3,S4,S17 | `internal/api`: router de `/internal`, middleware `X-Servicio-Token` (401 `NO_AUTORIZADO`), envoltura de errores `codigo`/`mensaje` y la tabla `codigo`->HTTP | `internal/api` | — |
| C4 | S1,S18 | `internal/store`: conexion Postgres/PostGIS, migraciones versionadas y las once tablas del contrato con indice GiST en `tiendas.ubicacion` | `internal/store`, `internal/config` | — |
| C5 | S7,S15,S16 | Seed de datos demo: personas, cinco tiendas de Aranjuez con coordenadas reales y efectivo publicado | `cmd/seed`, `internal/store` | — |
| C6 | S5,S8 | Identidad: `GET /personas`, `POST /politicas/aceptaciones`, `POST /personas` (con las dos comprobaciones previas) | `internal/api/identidad` | — |
| C7 | S4 | Verifik: `POST /cedulas/verificar`. Ruta de plan B (respuesta simulada declarada) si el proveedor no responde en el evento | `internal/verifik` | — |
| C8 | S5,S9 | Didit: `POST /verificaciones`, `GET /verificaciones/{session_id}`, `POST /webhooks/didit` con firma `X-Signature-V2` y los 30 minutos de vigencia | `internal/didit`, `internal/api/identidad` | — |
| C9 | S15 | Mapa: `GET /tiendas/cercanas` (KNN, maximo 3), `GET /tiendas/{tienda_id}`, `PUT /tiendas/{tienda_id}/efectivo` | `internal/api/mapa` | — |
| C10 | S7,S16 | Caja: `POST /reservas` con la regla de comisiones, vencimiento a 15 minutos y liberacion del efectivo | `internal/api/caja` | — |
| C11 | S14 | Caja: `POST /reservas/{id}/riesgo` (orden de evaluacion, pitufeo y alerta) y `GET /tiendas/{id}/reservas` | `internal/api/caja` | — |
| C12 | S6,S9 | Caja: `POST /reservas/{id}/entrega-tendero`, `POST /reservas/{id}/recibido-cliente`, `POST /reservas/{id}/cancelar` y el cierre que descuenta el efectivo | `internal/api/caja` | — |
| C13 | S11,S12 | Pagos: `GET /wallets`, `POST /cupos`, `POST /cupos/{grant_id}/continuar` | `internal/api/pagos` | — |
| C14 | S10,S13 | Pagos: `POST /cobros`, `POST /cotizaciones`, `POST /aprobaciones`, `POST /pagos` y `GET /cobros/estado` | `internal/api/pagos` | — |
| C15 | S9 | Una prueba por invariante de la seccion 9, sobre la interfaz publica y con estado aislado | `internal/api`, `internal/store` | — |
| C16 | S3,S18 | Compose: servicio `db` PostGIS, migraciones al arrancar y variables por servicio (los MCP solo reciben `BACKEND_URL` y `SERVICIO_TOKEN`) | `rafiki/docker-compose.yml`, `Dockerfile` | — |
| C17 | S17 | Documentacion: `README.md`, `docs/configuracion.md` y `env.example` con las variables nuevas (`DATABASE_URL`, `SERVICIO_TOKEN`, `VERIFIK_TOKEN`, `DIDIT_*`, `OP_WALLET_PLATAFORMA`) | raiz, `docs/` | — |
| C18 | — | Respuesta a la seccion 11 del contrato: URL base, tokens por canal privado, paths y campos, wallets finales, prueba minima de pago y configuracion del webhook de Didit | — | — |
| C19 | S9,S10 | Verificacion de punta a punta: retiro completo con la wallet del cliente, y la lista de comprobacion de la seccion 11.4 (cliente, tienda y app; y el segundo `POST /pagos` sin segundo cobro) | raiz | — |

## Log

L1 (pedido original del equipo, textual): "Revisa el contexto nuevamnete, revisa el archivo contrato-backen para iniciar las integraciones, tambien revisa que integraciones estan pendinetes entre rafiki y el backend".

L2 (2026-10-16, decisiones del equipo, textuales de la respuesta al cuestionario): 1) "Backend Go dueno total (contrato)" — "Este repo implementa todo /internal; los MCP son capas delgadas. Cambia el reparto del contexto." 2) "Cuenta COP nueva app-cajadebarrio-cop" — "Dos incoming payments y dos quotes como pide el contrato; el 1% entra exacto en COP. Hay que crear la wallet." 3) "Esquema DB: En este repo (contrato)" — "backend-caja-de-barrio crea y migra las tablas; la POC no depende de otro repo." 4) "Wallets: La de plataforma COP y las de Andres/Rosa" — "Cierra la seccion 11 del contrato y habilita la escena de remesa en USD."

Estas cuatro respuestas **sobrescriben** el reparto del `CONTEXTO-AGENTE-caja-de-barrio.md` en su seccion 7 (los MCP con `DATABASE_URL` y PostGIS propios), su seccion 13 (tablas compartidas creadas por el repo `caja-de-barrio`) y su seccion 14, en lo que respecta a quien es dueno de la base. El contrato manda por instruccion directa del equipo.

L3 (2026-10-16, reconciliacion verificada; el estado del repo se midio, el contrato se leyo completo): lo que existe hoy es el rol ASE, no el contrato. `internal/ase` monta `POST /webhooks`, `GET /rates`, `GET /idp/` y `POST /idp/approve` en la raiz del router (`internal/ase/ase.go:109-112`); no hay `/internal`, ni base, ni PostGIS, ni Didit, ni Verifik, ni `SERVICIO_TOKEN`, y `go.mod` no declara ninguna dependencia externa. `internal/opclient` no existe (T7-T9 sin empezar). Del lado de Rafiki, el stack esta sano (seis servicios), el activo COP quedo en escala 2, hay seis filas de wallet address (tres efectivas con host `rafiki-backend:3000` y tres huerfanas) y la tabla `webhooks` tiene una fila: nunca hubo una entrega real de evento. Restricciones que condicionan el diseno y no son negociables: el unico evento de webhook nace del camino de cliente (`getOrPollByUrl`), asi que la primera entrega real depende de T7; y el `resourceServer`/`authServer` anunciados solo resuelven dentro de la red de compose, asi que el backend tiene que seguir corriendo como contenedor (S19).

L4 (2026-10-16, contraparte ausente, y por que C0 es la primera tarea): el contrato define las respuestas como "exactamente las que el agente ya usa en modo simulado" y nombra a `agente-caja-de-barrio/agente/herramientas/simuladas.py` como implementacion de referencia, pero ni el agente ni los cuatro MCP estan en esta maquina (`~/caja-de-barrio` solo tiene `backend-caja-de-barrio` y `contexto-caja-de-barrio`). Escribir C6-C14 sin ese archivo es inventar campos; por eso C0 lo pide antes de la primera ruta.

L5 (2026-10-16, correccion de titularidad de T7-T9 y cambio de su alcance): el `L23` del documento del ASE asignaba T7-T9 a la sesion peer. Despues de ese asiento, esa sesion las cedio por escrito al agotar su contexto y el humano autorizo arrancar T7 con el SDK oficial; el plan `backend-go-ase-y-cliente-op.md` queda con el `L24` que lo registra. Ademas, el contrato cambia el alcance de T7 (un incoming payment y una quote) y T9 (un outgoing payment) a **dos** de cada uno, con el 1% en `app-cajadebarrio-cop` (S10). La fila de T7 y la de T9 se reescribieron en consecuencia. Tambien queda registrada la brecha de dependencias: PostGIS exige el primer cliente de base de datos del modulo, o sea un cambio en `go.mod` que hoy no existe.

L6 (2026-10-16, reparto de superficie con la sesion 01a14664, acordado por mensaje antes de escribir codigo):

1. **La sesion 01a14664 declaro que NO reclama T7-T9** y que no toca `internal/opclient/**`, `go.mod` ni `go.sum`. Su unica superficie medida hasta ahora es `README.md` (commit `07a9c17`, sin trabajo pendiente) y no toco `odd/tasks/**`.
2. **Superficie de esta sesion, tramo siguiente (C1)**: `internal/rafikiadmin/**` y `cmd/seed/**`. C1 (wallets de plataforma COP, Andres y Rosa) no necesita ninguna dependencia nueva, asi que no toca `go.mod`.
3. **Superficie en espera de decision del humano**: `internal/opclient/**`, `go.mod` y `go.sum`. Esta sesion conserva T7-T9 por el handoff escrito (L24 del documento del ASE), pero el humano le indico a la sesion 01a14664 arrancar T7 en el mismo turno: como paquete unico solo admite un escritor, esta sesion **no los empieza** hasta que el humano falle. Si el humano se los asigna a 01a14664, esta sesion los entrega y toma `internal/verifik` y `internal/didit` en su lugar, con espejo exacto y sin trabajo en paralelo.
4. **Superficie de la sesion 01a14664, tramo siguiente**: `internal/verifik/**` (C7) y `internal/didit/**` (el cliente de proveedor de C8: crear sesion y verificar la firma `X-Signature-V2`). Las dos son de biblioteca estandar, asi que no toca `go.mod`, `internal/config`, `env.example`, `Dockerfile` ni el compose. La ruta `POST /webhooks/didit` y `POST /verificaciones` de `internal/api/identidad` quedan de esta sesion, que envuelve al cliente. Las dos tareas se pueden empezar sin C0, porque su forma sale del contrato (secciones 5.3, 5.5-5.7) y de las APIs de Verifik y Didit, no de `simuladas.py`.
5. **Lista de exclusion para 01a14664**: `internal/opclient/**`, `go.mod`, `go.sum`, `internal/rafikiadmin/**`, `cmd/seed/**`, `cmd/backend/**`, `internal/ase/**`, `internal/config/**`, `internal/httpserver/**`, `internal/api/**`, `internal/store/**`, `internal/opkeys/**`, `Dockerfile`, `rafiki/**`, `README.md`, `docs/**`, `env.example`, `odd/tasks/**`. Si necesita un nombre de variable en `env.example` o en `docs/`, lo pide y esta sesion lo agrega en C17 (C16 y C17 son de esta sesion).

L7 (2026-10-16, C1 cerrado con verificacion viva; commit `a840597`):

1. **El plan del seed quedo en dos tablas**: `assets` (COP y USD, escala 2, con su liquidez) y `actors` (cinco cuentas, cada una con su `assetCode`), mas `clientKeyAddresses` (las dos cuentas que este backend opera como dueño del recurso). Cuatro pruebas rojas primero y verdes despues: el plan de cuentas contra el contrato, que todo `assetCode` este provisionado, que todo activo este financiado y que la clave del cliente este en las dos cuentas de la app.
2. **Evidencia viva contra el Rafiki local, no inferida.** Primera corrida: creo el activo `USD` (`78f9bd78-3231-4218-9ae1-794ea739b73f`, escala 2, `deposited now 300000`), la wallet `app-cajadebarrio-cop` (`aa0fcf24-6589-4cc2-ba48-691a44e9dd84`) y la wallet `andres-cajadebarrio` (`9b3bbf4b-41e9-4994-9aa4-be85d1f1d710`), y registro la clave `X7Exj1lBYgAFzMxsnv04i-CNBDSE1vaTYcMnWseOuE8` en `app-cajadebarrio` **y** en `app-cajadebarrio-cop`. Segunda corrida: mismos IDs, `deposited now 0` en los dos activos y ninguna escritura nueva, o sea idempotente con el stack ya sembrado.
3. **Estado de la base despues**: dos activos (`COP` y `USD`, escala 2) y **ocho** filas de wallet address: las seis anteriores mas las dos nuevas. Consulta de control sobre las cinco del host de la red: `app-cajadebarrio` y `app-cajadebarrio-cop` con una clave activa cada una, y `andres-cajadebarrio` (USD) sin clave, que es lo correcto porque nadie firma todavia en nombre de Andres.
4. **Por que el activo USD es de C1 y no de la escena de remesa**: la seccion 4 del contrato da a Andres una wallet en USD, y Rafiki rechaza toda wallet address cuyo activo no exista. La liquidez USD (3.000,00 USD) es del orden de los 10.000.000 COP del objetivo de pesos a la tasa de la demo (1 USD = 4.000 COP), con margen.
5. **Auditoria de tests en el modulo**: la suite completa paso de 125 a **129** casos, `go vet ./...` limpio y `gofmt -l .` sin salida.

L8 (2026-10-16, decision del humano sobre el entorno de T7, y un adelanto de C16):

1. **El SDK oficial exige un Go mas nuevo que el del host, y eso lo decidio el humano.** Medido: **todas** las versiones publicadas de `github.com/interledger/open-payments-go` (v0.1.0, v0.2.0, v0.4.0 y v0.5.0) declaran `go 1.22.5` mas una directiva `toolchain`; el host tiene **go 1.22.2** (`golang-go` de Ubuntu, decision `S4`) y `GOTOOLCHAIN=auto`. Verificado que el camino existe: el proxy sirve toolchains (200) y `go1.22.5` esta publicado (200). Respuesta textual del equipo: "SDK, y Go baja el toolchain" — "go.mod gana require y toolchain, aparece go.sum, y arreglo el Dockerfile con COPY go.sum y go mod download. El primer build del host baja go1.22.5." Descartada la via sin SDK, que habria exigido escribir RFC 9421 (HTTP Message Signatures) a mano y rompia `S1`.
2. **El `Dockerfile` se adelanta de C16, porque la dependencia lo rompe.** Su cabecera decia "El modulo no declara dependencias, asi que la compilacion no necesita red ni cache de modulos" y copiaba solo `go.mod`: con una dependencia, `go build` falla por entradas faltantes de `go.sum`. El arreglo es copiar `go.mod` y `go.sum` solos, `go mod download` en su propia capa y despues el codigo; la imagen `golang:1.22-alpine` trae go 1.22.12, asi que dentro del contenedor no hay que bajar toolchain. C16 conserva el resto (servicio `db` con PostGIS y las variables por servicio).

L9 (2026-10-16, primer corte de T7 y dos hallazgos sobre el SDK):

1. **Lo que quedo escrito**: `internal/opclient` con `New` (valida la configuracion, carga la clave y nombra el `kid` por la huella cuando nadie lo fija), `ResolveWalletAddress` (el documento reducido a los dos servidores, el activo y su escala) y `downgradeTransport`, que baja a http **solo** el host de la wallet address de este cliente. Cinco pruebas (diez casos con subtests), rojas por compilacion primero y verdes despues. El downgrade se prueba de verdad: el stub habla solo http y la URL configurada es https, asi que alcanzarlo es la prueba; y hay una prueba de que **otro** host no se degrada.
2. **Estado de la dependencia, medido**: `github.com/interledger/open-payments-go v0.5.0` como dependencia directa, `go.mod` con `go 1.22.5`, `go.sum` de 23 lineas versionado, y el host ya corre **go1.22.5** (lo bajo solo, como se decidio en L8). `internal/opclient` aporta 10 casos; el total de la suite es 151 y **no es todo mio**: el peer ya tiene `internal/verifik` con sus pruebas en el mismo arbol.
3. **Hallazgo que corrige a L15: el SDK no manda `Accept` y Rafiki contesta JSON igual.** Medido en vivo dentro de la red: `GET http://rafiki-backend:3000/app-cajadebarrio` sin cabecera `Accept` responde `200` con `Content-Type: application/json; charset=utf-8` y el mismo documento (275 bytes) que con `Accept: application/json`. O sea que la cabecera no era un requisito de Rafiki, como sugereia la nota de L15, y no hace falta ninguna capa que la inyecte.
4. **Hallazgo del SDK que hay que recordar en T9**: en `client.go`, `NewAuthenticatedClient` cablea `IncomingPayment.DoUnsigned` con su **propio** `http.Client` interno y no con el que recibe por `WithHTTPClientAuthed`. Consecuencia: un `GetPublic` de incoming payment salta el downgrade y la capa de cliente inyectada. No afecta al flujo previsto, que usa el `Get` firmado con el access token, pero hay que no caer en `GetPublic` para el `estado_cobro`.
5. **Verificacion viva, con su limite dicho**: el documento de los stubs tiene la forma del documento real (L15), pero `rafiki-backend:3000` solo resuelve dentro de la red de compose, asi que **este corte no se probo contra el stack corriendo**. La prueba viva del cliente llega cuando el flujo completo corra dentro de la red (T9 y T12); no se declara aca.
6. **Lo que falta de T7**: los grants no interactivos de `incoming-payment` (tienda y plataforma) y de `quote`, los **dos** incoming payments y las **dos** quotes. Es el corte siguiente, sobre lo ya probado.

L10 (2026-10-16, verificacion independiente de C1 y del primer corte de T7, con dos correcciones):

1. **La verificacion independiente (`gentle-ai-verify`, read-only) paso las ocho reglas** del encargo: las dos cuentas nuevas con su activo correcto y sin wallet `rosa-*`, la clave registrada en las dos cuentas de la app, la idempotencia de los `Ensure*`, la validacion y el `kid` derivado de `opclient.New`, la resolucion del documento, que el downgrade solo toca el host de la wallet address propia, el `Dockerfile` con `go mod download` y `docker build` en verde, y `go.mod`/`go.sum` sin diferencia tras `go mod tidy`. Ademas **volvio a medir en vivo y sin mutar nada** las afirmaciones de L7: dos activos, ocho wallet addresses, y exactamente una clave activa en `app-cajadebarrio` y en `app-cajadebarrio-cop`.
2. **El hallazgo que valio la verificacion: una prueba mia era vacua.** `TestTheClientKeyIsRegisteredOnEveryAppAccount` repetia la declaracion de `clientKeyAddresses` y **habria pasado igual** si el bucle hubiera registrado la clave solo en `appAddress`, o sea que el verde no decia nada sobre el efecto. Y `TestProvisioningPlanMatchesTheContract` es un detector de cambios que yo habia presentado como si probara comportamiento. Las dos quedaron rotuladas por lo que son, y la regla se cubre ahora de dos maneras que si miran el efecto: una **post-condicion en el seed** (`hasActiveKey`) que lee lo que el ASE guarda despues de registrar y falla si el `kid` no esta activo, y una **prueba real** de `hasActiveKey` con cinco casos (activo, revocado, otro `kid`, mezcla y lista vacia). Evidencia viva: la corrida del seed despues del cambio termina con codigo 0 y registra la clave en las dos cuentas.
3. **El arbol entero esta rojo por trabajo en vuelo de la otra sesion, no por este diff.** `go test ./...` y `go vet ./...` fallan en `internal/didit` (`didit_test.go:118:19: undefined: Error`), que esta sin seguimiento y es de la sesion 01a14664; los ocho paquetes con seguimiento pasan y `gofmt -l .` solo nombra los archivos de ese paquete. Se le aviso por mensaje. Leccion de coordinacion: mientras las dos sesiones escriban en el mismo arbol, `go test ./...` deja de ser una senal limpia para cualquiera.
4. **Huecos que quedan aceptados y dichos**: la rama de `KeyID` vacio no tiene prueba unitaria porque el `kid` solo es observable en una peticion firmada, y la primera peticion firmada llega con los grants (T8); y `internal/opclient` sigue sin verificacion viva, como ya decia L9. Ninguno de los dos invalida las afirmaciones de este diff.
5. **Artefactos que dejo el verificador**, para limpiar: la imagen `backend-caja-de-barrio:verify-tmp` y un directorio de copia de trabajo en `/home/carchila/tidy-scratch-nBnvgc` (fuera del repositorio; la sesion de verificacion no pudo borrarlo).

L11 (2026-10-16, enmienda acotada al reparto de L6, y una correccion de estado del arbol):

1. **La sesion 01a14664 puede crear UN archivo propio en `odd/tasks/`.** Pidio opciones porque `odd/tasks/**` esta en su lista de exclusion; se le concede la opcion (a): puede crear `odd/tasks/proveedores-identidad.md`, que es un archivo **nuevo** y no puede chocar con mis dos. Condiciones, aceptadas por ella: el archivo es suyo y no edita ninguno de los mios; **ninguna de las dos sesiones usa `git add -A` ni `git add .`**, siempre rutas nombradas (yo ya lo hacia); su documento cita el contrato (secciones 5.3 y 5.5-5.7) de forma textual, como manda ODD; y su espejo de Engram usa la clave `odd/proveedores-identidad/tasks`. Motivo de elegir (a) sobre (b): un log escrito por interposicion mia parafrasearia sus requisitos en vez de citarlos, y ODD quiere el documento en manos de quien implementa.
2. **Nombres de variables confirmados desde el contrato, no inventados.** El contrato ya los fija en su seccion 10 y en la seccion de integraciones externas del contexto: `VERIFIK_TOKEN`, `DIDIT_API_KEY`, `DIDIT_WORKFLOW_ID` y `DIDIT_WEBHOOK_SECRET` (el secreto de la firma `X-Signature-V2`). La sesion 01a14664 los usara como parametros de constructor; yo los agrego en `env.example`, `docs/configuracion.md` y `README.md` en C17, con su tipo y valor de ejemplo. No se adelanta ninguno.
3. **Correccion de estado, medida antes de contestar.** La sesion 01a14664 escribio que "no habia escrito nada en el repositorio": el dato de **commits** es correcto (su ultimo commit sigue siendo `07a9c17`), pero el **arbol de trabajo** tiene diez archivos sin seguimiento escritos por ella entre las 15:57 y las 16:02 (`internal/verifik/` con 3 archivos y `internal/didit/` con 7, con marcas de tiempo de `ls -la`). No es una contradiccion de mala fe, pero importa: **los archivos sin seguimiento de trabajo concurrente son exactamente lo que hizo fallar mi evaluacion nativa como `unassessable`**, y por eso las dos sesiones commitean con rutas nombradas en vez de arrastrar el arbol completo.
4. **El arbol volvio a estar verde**, medido ahora: los diez paquetes (`cmd/seed`, `ase`, `config`, `didit`, `httpserver`, `opclient`, `opkeys`, `rafikiadmin`, `verifik` y `cmd/backend` sin pruebas) pasan, `go vet ./...` y `gofmt -l .` no dicen nada. El error de compilacion de `internal/didit/didit_test.go` que vio el verificador independiente ya no esta: la sesion peer lo corrigio mientras yo redactaba el aviso.

L12 (2026-10-16, T7 cerrado salvo la prueba viva; commit `237f670`):

1. **La mitad no interactiva del retiro quedo implementada** (contrato 8.4 y 8.5): por cada cobro, un grant no interactivo de `incoming-payment` (acciones create, read, complete) pedido **al servidor de autorizacion de esa cuenta** e identificado por su wallet address, y despues su incoming payment por el monto en pesos ya convertido; para cotizar, un grant no interactivo de `quote` sobre la cuenta del cliente (sin identifier: la wallet del cliente ya lo delimita) y **dos** quotes desde su wallet, una por cada cobro, con metodo `ilp`. `Quotes.Debit` es la suma de los dos debitos en el activo del cliente y `Quotes.ExpiresAt` el vencimiento mas cercano, que son el `debito` y el `vence_en` del contrato.
2. **La conversion de pesos a unidad minima sale del documento de la cuenta**, no de un 2 fijo: `value = monto x 10^assetScale` con el `assetScale` que anuncia esa wallet address. 203000 pesos de tienda en una cuenta COP escala 2 viajan como `"20300000"`.
3. **Delegacion y verificacion propia.** La unidad se delego al escritor `gentle-ai-worker` con la superficie publica ya decidida por mi (mi contexto estaba muy cargado), con superficie de edicion acotada a `internal/opclient/**` y sin tocar `go.mod`. El escritor devolvio su evidencia RED/GREEN; **yo revise el diff y volvi a correr todo**: 35 casos en el paquete (12 pruebas) y el arbol completo de diez paquetes en verde. La evidencia de un subagente no es autoridad: se revisa.
4. **Una correccion mia sobre el trabajo del escritor.** `SmallestUnit` era el **unico `panic` de codigo no-test del repositorio** y se cambiaron sus dos pruebas a errores. Motivo, no estilo: este backend corre como servicio de larga vida, y un `panic` en el camino del dinero **mata el proceso**. Ahora devuelve `(string, error)` y el camino interno que ya devolvia error se fusiono con el, asi que hay una sola funcion. Verificado: `grep -rn "panic(" cmd internal` sin resultados fuera de pruebas.
5. **Detalles que el escritor decidio y acepto**: los montos tienen que ser positivos; se valida **todo** antes de tocar la red, para que un segundo cobro mal formado no deje el primero ya creado; un grant que vuelva interactivo o sin token detiene el cobro; la `ExpiresAt` de cada quote es obligatoria; y los stubs de prueba contestan `201` porque el SDK exige `StatusCreated` en los dos `Create` (leido en el fuente del SDK, no supuesto).
6. **Lo que sigue faltando, dicho**: la verificacion viva contra el stack. `rafiki-backend:3000` solo resuelve dentro de la red de compose, asi que el flujo se probo contra servidores de prueba que fijan las peticiones exactas. La prueba de punta a punta es T9 y T12.
7. **La otra sesion commiteo su rebanada** (`306f4fb`, `ffa93fa`, `64d3471`), asi que el arbol ya no tiene archivos sin seguimiento y la suite completa vuelve a ser una senal limpia para las dos.

L13 (2026-10-16, cierre de T7 por el plan de RDD, y por que la evaluacion cambio de resultado):

1. **La evaluacion nativa de este candidato ya pudo correr**: `risk: medium`, `nativeRisk: medium`, `reviewDue: false` por estar bajo presupuesto, y plan con `writerSelfVerification: true` e `independentVerifier: false` ("the writer's self-verification stands and no separate verifier is required"). Candidato: `64d3471..HEAD` (`237f670` y `0e5e43c`), 3 rutas y 1010 lineas contadas por el controlador.
2. **La asimetria con la ronda anterior tiene causa medida, no interpretacion**: el 2026-10-16 la misma llamada contesto `unassessable` porque habia archivos sin seguimiento de la otra sesion ("untracked files require an explicit declaration"), y eso forzo el camino de riesgo alto con verificador independiente. Esta vez la otra sesion ya habia commiteado su rebanada (`306f4fb`, `ffa93fa`, `64d3471`), el arbol no tenia archivos sin seguimiento, y la evaluacion corrio sola. Leccion para las dos sesiones: **mientras una escriba sin commitear, la evaluacion nativa del otro queda ciega**, y el controlador no puede distinguir "cambio mio pendiente" de "trabajo ajeno en vuelo".
3. **T7 queda cerrado** con: las pruebas del paquete (35 casos), la revision propia del diff delegado, el arbol completo en verde y el plan de RDD satisfecho. Lo que **no** esta verificado sigue siendo la corrida viva contra el stack, que por el plan pertenece a T9 y T12 y no a T7.
