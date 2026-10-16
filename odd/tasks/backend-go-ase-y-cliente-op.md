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

| ID | Specs | Tarea | Ruta |
| --- | --- | --- | --- |
| T1 | S2,S5 | Baseline: `.gitignore` de raiz y commit del trabajo previo (`rafiki/`, `docs/`) | repo |
| T2 | S1,S3,S4 | Modulo Go, configuracion por entorno, servidor HTTP con `/health` y sus pruebas | `go.mod`, `cmd/backend`, `internal/config`, `internal/httpserver` |
| T3 | S7 | Cliente de la Admin API GraphQL de Rafiki: crear cuenta, wallet address y registrar clave publica del ASE | `internal/rafikiadmin` |
| T4 | S7 | `POST /webhooks` con verificacion de firma (`SIGNATURE_SECRET`) | `internal/ase` |
| T5 | S7 | `GET /rates` con el formato que espera Rafiki | `internal/ase` |
| T6 | S7 | `/idp/` para aprobar grants interactivos (cabecera `x-idp-secret`) | `internal/ase` |
| T7 | S1,S8 | `opclient`: cliente autenticado, resolver wallet address e incoming payment + quote | `internal/opclient` |
| T8 | S1,S8 | `opclient`: grant interactivo de outgoing-payment, `/op/finish` y rotacion del token | `internal/opclient` |
| T9 | S1,S8,S9 | `opclient`: outgoing payment, verificacion de completado y las pruebas de las invariantes 1 y 4 | `internal/opclient` |
| T10 | S3,S5 | `Dockerfile`, servicio en `docker-compose.yml`, `.env.example` con los nombres de las variables | raiz |
| T11 | S6 | `AI_USAGE.md` y `prompts/` con los prompts reales de esta sesion | raiz |
| T12 | S8,S9 | Verificacion de punta a punta contra el Rafiki local: retiro completo con wallet addresses propias | raiz |

## Log

L1 (pedido original del equipo, textual): "lee el contexto que esta en la carpeta de contexto, necesito empezar a crear el backend, se necesitara Go para el uso del sdk, ve contruyendo paso a paso y haciendo commits pequennos".

L2 (2026-10-16, decisiones del equipo en esta sesion): alcance = "Un solo servicio Go con ambos" (`cmd/backend` + `internal/ase` + `internal/opclient`); toolchain = "Te doy la contrasena de sudo" con `sudo apt install golang-go` (Go 1.22).

L3 (2026-10-16, hallazgo de exploracion): el repo tiene un solo commit y no existe `.gitignore` de raiz; `rafiki/.env` y `rafiki/config/keys/*` si estan protegidos por `rafiki/.gitignore`. Se agrego el `.gitignore` de raiz antes de cualquier commit para que ningun secreto entre al historial. La red alcanza `proxy.golang.org` y `github.com`; versiones publicadas del SDK: v0.1.0, v0.2.0, v0.4.0 y v0.5.0.
