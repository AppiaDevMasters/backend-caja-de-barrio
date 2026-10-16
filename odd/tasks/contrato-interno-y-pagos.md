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
