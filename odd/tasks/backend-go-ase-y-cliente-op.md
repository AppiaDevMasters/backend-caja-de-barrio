# Backend Go de Caja de Barrio: ASE para Rafiki + cliente Open Payments

Un solo servicio Go en este repo que cubre los dos roles frente a Open Payments: el ASE
que Rafiki exige (cuentas y wallet addresses, webhooks, tipos de cambio, IdP) y el
cliente que usa el SDK oficial de Go para el flujo de pago del retiro.

Decisiones cerradas en la sesion del 2026-10-16. Trabajo incremental, un commit de
unidad de trabajo por tarea.

## Specs

S1. **Backend en Go.** Pedido textual del equipo: "necesito empezar a crear el backend, se necesitara Go para el uso del sdk". El uso del SDK es el oficial de Go: `github.com/interledger/open-payments-go` (estado "Done" segun `openpayments.dev/sdk/before-you-begin`).

S2. **Construccion incremental con commits pequenos.** Pedido textual del equipo: "ve construyendo paso a paso y haciendo commits pequennos". Cada tarea cierra con al menos un commit de unidad de trabajo en `main`, con pruebas y documentacion junto al comportamiento.

S3. **Un solo servicio, paquetes separados.** Decidido en esta sesion con el equipo: `cmd/backend` + `internal/ase` + `internal/opclient`, por "una sola imagen Docker, un solo .env, una sola API para el agente".

S4. **Toolchain.** Decidido en esta sesion: `sudo apt install golang-go` (Go 1.22 de Ubuntu).

S5. **Secretos.** Textual del contexto: "Nunca subas secretos. Llaves, tokens y `private.key` van en `.env` (incluido en `.gitignore`). En el repo solo existe `.env.example` con los nombres de las variables."

S6. **Politica de IA.** Textual del contexto: cada repo lleva "un `AI_USAGE.md` en la raiz (herramientas usadas, para que y en que archivos) y una carpeta `prompts/` con los prompts reales, no resumenes".

S7. **Rol ASE, alcance pendiente.** Textual de `rafiki/README.md`: "El backend de Caja de Barrio (rol de ASE: webhooks, tipos de cambio e IdP) todavia no existe". Los pendientes listados son: cuentas y wallet addresses creadas con la Admin API GraphQL y registro de la clave publica del ASE; `POST /webhooks` que valide la firma con `SIGNATURE_SECRET`; `GET /rates` con el formato que espera Rafiki; `/idp/` para aprobar grants interactivos.

S8. **Flujo de pago, orden obligatorio.** Contexto seccion 11: incoming payment en la tienda -> quote hacia ese incoming -> grant interactivo de `outgoing-payment` con `limits.debitAmount` igual al debito de la quote -> `/op/finish` continua el grant con `interact_ref` y guarda el access token -> solo tras `face_match` y riesgo OK se crea el outgoing payment -> verificar que el incoming payment quedo completado. Puntos delicados: verificacion del hash del callback interactivo y rotacion del token tras cada pago.

S9. **Pruebas e invariantes.** Contexto seccion 17: "las invariantes de la seccion 6 deben tener una prueba cada una". Ademas: montos en COP como enteros y respeto del `assetScale` de cada wallet; toda escritura humana registra `motivo`; logs estructurados con `reserva_id` en cada linea de un retiro. Invariante 1 (ningun billete antes del pago completado) e invariante 4 (los topes los impone el grant con `limits.debitAmount`) se validan en codigo, no en instrucciones del modelo.

S10. **Idioma.** Codigo e identificadores en ingles; documentacion del repo y mensajes al usuario en espanol. Es la convencion existente del repo (`rafiki/README.md` y `docs/` ya estan en espanol) y del contexto: "mensajes al usuario siempre en espanol".

## Tasks

| ID | Specs | Tarea | Ruta | Commit |
| --- | --- | --- | --- | --- |
| T1 | S2,S5 | Baseline: `.gitignore` de raiz y commit del trabajo previo (`rafiki/`, `docs/`) | repo | 568b5b4, f2e6996, 28bf05a, 865b37e |
| T2 | S1,S3,S4 | Modulo Go, configuracion por entorno, servidor HTTP con `/health` y sus pruebas | `go.mod`, `cmd/backend`, `internal/config`, `internal/httpserver` | d18bcaa |
| T3a | S7 | Cliente de la Admin API: firma HMAC del tenant, `Do` GraphQL y `Whoami` | `internal/rafikiadmin` | 42485a9 |
| T3b | S7 | Activos y wallet addresses con `Ensure*` idempotentes, escalar `UInt64` y `cmd/seed` | `internal/rafikiadmin`, `cmd/seed` | se registra en el commit siguiente |
| T3c | S7 | Clave Ed25519 del cliente y registro del JWK con `createWalletAddressKey` | `internal/rafikiadmin`, `internal/opkeys` | — |
| T4 | S7 | `POST /webhooks` con verificacion de firma (`SIGNATURE_SECRET`; T4 confirma el esquema real contra Rafiki) | `internal/ase` | — |
| T5 | S7 | `GET /rates` con el formato que espera Rafiki | `internal/ase` | — |
| T6 | S7 | `/idp/` para aprobar grants interactivos (cabecera `x-idp-secret`) | `internal/ase` | — |
| T7 | S1,S8 | `opclient`: cliente autenticado, resolver wallet address e incoming payment + quote (al llamar, bajar la URL https del ASE a http; ver L6) | `internal/opclient` | — |
| T8 | S1,S8 | `opclient`: grant interactivo de outgoing-payment, `/op/finish` y rotacion del token | `internal/opclient` | — |
| T9 | S1,S8,S9 | `opclient`: outgoing payment, verificacion de completado y las pruebas de las invariantes 1 y 4 | `internal/opclient` | — |
| T10 | S3,S5 | `Dockerfile`, servicio en `docker-compose.yml`, `.env.example` con los nombres de las variables | raiz | — |
| T11 | S6 | `AI_USAGE.md` y `prompts/` con los prompts reales de esta sesion | raiz | — |
| T12 | S8,S9 | Verificacion de punta a punta contra el Rafiki local: retiro completo con wallet addresses propias | raiz | — |

## Log

L1 (pedido original del equipo, textual): "lee el contexto que esta en la carpeta de contexto, necesito empezar a crear el backend, se necesitara Go para el uso del sdk, ve contruyendo paso a paso y haciendo commits pequennos".

L2 (2026-10-16, decisiones del equipo en esta sesion): alcance = "Un solo servicio Go con ambos" (`cmd/backend` + `internal/ase` + `internal/opclient`); toolchain = "Te doy la contrasena de sudo" con `sudo apt install golang-go` (Go 1.22).

L3 (2026-10-16, hallazgo de exploracion): el repo tiene un solo commit y no existe `.gitignore` de raiz; `rafiki/.env` y `rafiki/config/keys/*` si estan protegidos por `rafiki/.gitignore`. Se agrego el `.gitignore` de raiz antes de cualquier commit para que ningun secreto entre al historial. La red alcanza `proxy.golang.org` y `github.com`; versiones publicadas del SDK: v0.1.0, v0.2.0, v0.4.0 y v0.5.0.

L4 (2026-10-16, evidencia verificada por el agente; el stack ya estaba levantado, el agente no lo levanto): `rafiki-backend`, `rafiki-auth`, `rafiki-postgres`, `rafiki-redis` y `rafiki-admin` estan corriendo con `ghcr.io/interledger/rafiki-*:v2.4.5-de`, `postgres:16-alpine` y `redis:7-alpine`. `GET localhost:3001/healthz` y `GET localhost:3003/healthz` devuelven 200, y los puertos 3000-3003, 3006, 3007 y 3009-3011 estan escuchando. Todavia no hay ninguna cuenta ni wallet address creada en el ASE. Dato para T3: la Admin API responde `401 Unauthorized` tanto con `Authorization: GNAP <ADMIN_API_SECRET>` como con `Authorization: Bearer <ADMIN_API_SECRET>`, asi que la autenticacion no es un secreto en claro en esa cabecera; el `rafiki/README.md` indica que `SIGNATURE_SECRET` firma "las llamadas a la Admin API", de modo que T3 empieza por implementar esa firma (version 1 de Rafiki) y comprobarla contra `POST /graphql`. El agente no agrego nada al stack ni modifico su estado.

L5 (2026-10-16, esquema real de la Admin API, leido en el codigo de Rafiki v2.4.5-de y verificado en vivo contra el stack local): la hipotesis de L4 sobre `SIGNATURE_SECRET` es **incorrecta**. `getTenantFromApiSignature` (`packages/backend/src/shared/utils.ts`) exige dos cabeceras: `tenant-id: <OPERATOR_TENANT_ID>` y `Signature: t=<unix_ms>, v1=<hex>`. El digest es el HMAC-SHA256 en hexadecimal sobre `<timestamp>.<cuerpo canonico JCS>` con el `apiSecret` del tenant, y el operador es el tenant cuyo `apiSecret` coincide con `ADMIN_API_SECRET` (`SIGNATURE_SECRET` es para firmar los webhooks que Rafiki envia hacia nosotros, o sea T4). `v1` debe ser igual a `ADMIN_API_SIGNATURE_VERSION` (1 por defecto) y la firma vence a los `ADMIN_API_SIGNATURE_TTL_SECONDS` (30 por defecto). Rafiki guarda la cabecera `Signature` completa en Redis (`signature:<cabecera>`), asi que una repeticion se rechaza como replay y el timestamp tiene que ser unico por peticion aunque se firmen dos llamadas en el mismo milisegundo. Verificado en vivo: `whoami` contra `http://localhost:3001/graphql` devuelve el id del `OPERATOR_TENANT_ID` con `isOperator: true`, y con un secreto equivocado responde `401`. Ademas, en v2.4.5-de **no existe la mutacion `createAccount`**: la cuenta nace con `createWalletAddress`, asi que T3 pierde ese paso y se parte en T3a (cliente y firma) y T3b (wallet address y clave).

L6 (2026-10-16, T3b contra el Rafiki real; 33 pruebas en verde y estos tres hechos verificados en vivo):

1. **Los escalares `UInt64` viajan como string JSON**, no como numero: `"liquidity":"500000"`. El primer intento con `int64` fallo con `cannot unmarshal string ... of type int64`. El tipo `rafikiadmin.UInt64` lee tanto el string como el numero y escribe siempre string.
2. **Con el stack como esta hoy no se puede crear ninguna wallet address.** `createWalletAddressUrl` (`packages/backend/src/open_payments/wallet_address/service.ts`) toma la base del tenant de `OPEN_PAYMENTS_URL` (`http://100.76.141.47:3000` en este stack), y despues `isValidWalletAddressUrl` exige `https:` y un path distinto de `/`. Con un nombre suelto la URL compuesta queda en http y Rafiki responde `invalid url`. Si el cliente manda la URL completa, tiene que empezar por la base del tenant, que sale del mismo `OPEN_PAYMENTS_URL`; con la base en http, una URL https no empieza por ella y tambien da `invalid url`. Verificado en vivo con las dos formas, asi que la conclusion no es una suposicion: hace falta `OPEN_PAYMENTS_URL` en https en el stack (y que los clientes locales bajen a http para llamar, como ya anota `rafiki/README.md`). Es una decision del equipo porque toca el stack que la otra sesion esta modificando.
3. **Rafiki guarda `address: finalWalletAddressUrl.toLowerCase()`**, o sea la URL completa en minusculas, asi que la busqueda de idempotencia del seed compara sin distinguir mayusculas; con una comparacion exacta, la segunda corrida intentaba crear un duplicado.

Estado del ASE tras T3b: el activo `COP` escala 2 quedo creado (id `a65721e8-640d-41fb-9963-2162018234f5`), no hay ninguna wallet address, y el seed ahora falla rapido nombrando `RAFIKI_OPEN_PAYMENTS_URL` en vez de dejar que la Admin API conteste "invalid url".
