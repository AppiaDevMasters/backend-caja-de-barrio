# 06 — Modo testnet (interoperabilidad USD → COP)

Fecha: madrugada del 2026-10-17. Herramienta: Claude Code (Claude Opus 5.5). Textual.

## Prompts (textuales)

> Contexto: el retiro de punta a punta ya funciona contra nuestro Rafiki (op.cajadebarrio.lat).
> NO romper ese flujo. Quiero un MODO TESTNET opcional para la demo, activado por
> la variable OP_MODO=testnet (por defecto OP_MODO=propio, que es el comportamiento actual).
>
> Objetivo: que el mismo retiro corra contra wallets de wallet.interledger-test.dev,
> con nuestro backend como cliente de Open Payments, para demostrar interoperabilidad
> y un pago entre monedas (cliente en USD, tienda en COP).
>
> Requisitos:
> 1. Config nueva (internal/config): OP_MODO, y en modo testnet usar
>    OP_CLIENT_WALLET_ADDRESS=https://ilp.interledger-test.dev/app-cajadebarrio,
>    OP_KEY_ID y OP_PRIVATE_KEY_PATH de la llave de la test wallet, OP_TLS=true.
>    Nada de secretos en git: solo nombres en env.example y deploy/llaves.env.example.
> 2. En modo testnet, en internal/pagos:
>    - NO llamar a fondear() ni a retirarLiquidezCobros() (eso es del Admin API de
>      nuestro Rafiki; en la testnet el saldo es real en la wallet del cliente).
>    - NO usar la billetera virtual para validar saldo; confiar en el error del
>      outgoing payment si no hay fondos y devolverlo con un mensaje claro.
>    - El cupo (grant interactivo) se aprueba en la pantalla de la test wallet,
>      no en nuestro /idp. Revisar que el redirect y OP_FINISH_URL sigan funcionando.
>    - La quote es cross-currency (USD -> COP): guardar y mostrar debitAmount y
>      receiveAmount con su assetCode y assetScale, sin asumir COP ni escala 2.
>    - Comisión de plataforma: si no hay wallet de plataforma en la testnet
>      (OP_WALLET_PLATAFORMA vacía), crear solo el incoming payment de la tienda
>      y registrar la comisión en el libro interno.
> 3. Mantener intactas las reglas de oro: face_match_ok vigente, APROBAR atado a la
>    quote vigente, entrega solo si el incoming payment de la tienda está completed.
> 4. Un comando cmd/retiro (ya existe) o un test e2e que en modo testnet haga:
>    resolver wallets -> incoming payment (tienda COP) -> quote desde wallet USD ->
>    grant interactivo -> outgoing payment -> verificar completed. Imprimir los IDs.
> 5. Tests: los existentes deben seguir pasando en modo propio. Agrega tests
>    unitarios del modo testnet con el openPaymentsFalso que ya existe.
> 6. Documenta en docs/modo-testnet.md cómo activarlo, y agrega el prompt a
>    prompts/ y una entrada en AI_USAGE.md (regla del hackathon).
> Haz commits pequeños. No despliegues al VPS sin que yo lo confirme.
>
> es posible eso?

> 1. Wallets: NADA de rutas /admin ni cambios en la base. Solo para la demo, en
>    modo testnet se leen de variables:
>    OP_TESTNET_WALLET_CLIENTE=https://ilp.interledger-test.dev/andres-cajadebarrio   (USD)
>    OP_TESTNET_WALLET_TIENDA=https://ilp.interledger-test.dev/laura-cajadebarrio     (COP)
>    Si OP_MODO=testnet, esas URLs reemplazan la wallet guardada de la persona y de la
>    tienda. Valídalas al arrancar resolviéndolas por Open Payments y falla con un
>    error claro si no resuelven.
>
> 2. Cotización: sí, cotizar por lo que recibe la tienda, PERO solo en modo testnet.
>    - El incoming payment de la tienda se crea con incomingAmount en COP
>      (monto + comisión del tendero).
>    - La quote se pide con receiver = ese incoming payment y SIN debitAmount,
>      para que la cotización tome el incomingAmount y Rafiki calcule el débito
>      en USD. Guarda debitAmount y receiveAmount con su assetCode y assetScale.
>    - En modo propio, CreateQuotes queda exactamente igual. Agrega un test que lo
>      garantice.
>    - OJO con el grant: el límite debitAmount del cupo va en la moneda del CLIENTE.
>      En modo testnet usa OP_TESTNET_CUPO (por ejemplo 100 USD, en su escala), no
>      el 1.000.000 COP.
>    - Comisión de plataforma: en modo testnet NO crear el segundo incoming payment
>      ni el segundo pago. Un solo cobro a la tienda; la comisión del 1 % se
>      registra en el libro interno. Así hay un solo APROBAR y un solo pago saliente.
>    - Fuera de modo testnet: no llamar fondear() ni retirarLiquidezCobros().
>
> 3. COP en el testnet: confirmado. laura-cajadebarrio ya existe en COP
>    (con saldo) y andres-cajadebarrio en USD; las dos son del mismo usuario.
>    La llave del cliente es la de app-cajadebarrio (EUR), que solo identifica
>    a nuestra app; no necesita ser COP.
>
> Límite de tiempo: si a las 9:30 no hay un pago COMPLETED de USD a COP con
> cmd/retiro, paramos, dejamos todo en una rama y no se mezcla a main.
> No despliegues al VPS sin mi confirmación.

## Decisiones tomadas en el camino

- La línea *"Fuera de modo testnet: no llamar fondear() ni retirarLiquidezCobros()"* contradice el primer pedido y
  rompería el modo propio. Se leyó como **"en modo testnet no se llaman"** y se le dijo al equipo antes de empezar.
- *"Guarda debitAmount y receiveAmount"*, sin cambios en la base:
  - el débito se guarda como hasta ahora (`retiros.debito` y `moneda`), en la moneda del cliente;
  - los dos montos completos, con `asset_code` y `asset_scale`, viajan en las respuestas de cotizar y pagar, y quedan
    en `retiros.resultado` al pagar.

## Mensajes durante la prueba real (textuales)

> e481950a-0464-41fe-92fe-2fe2f16be887

(El Key ID. La primera corrida falló con `400 could not determine client`: la llave no estaba en `app-cajadebarrio`.)

> (captura de la test wallet: la llave `keyOfHackaton` atada a `$ilp.interledger-test.dev/9f9bfa3d`)

> listo

(La persona aprobó el cupo en la test wallet. El pago terminó **COMPLETADO**: 3,47 USD de `andres-cajadebarrio`
fueron 11.000 COP en `laura-cajadebarrio`. Ver `docs/modo-testnet.md`, "Evidencia".)

## Resultado

- Rama `feat/modo-testnet`, sin mezclar a main ni desplegar.
- `OP_MODO` en `internal/config`; en `internal/opclient`, las operaciones de un solo cobro (`unico.go`).
- En `internal/pagos`:
  - las wallets se reemplazan al leer la reserva;
  - un cobro y una cotización por receptor;
  - un pago sin billetera virtual ni liquidez;
  - el 1 % en el libro interno.
- `cmd/retiro -testnet` y `docs/modo-testnet.md`.
- Pruebas de los dos modos: las del modo propio no cambiaron y siguen pasando.
