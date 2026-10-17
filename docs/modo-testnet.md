# Modo testnet (`OP_MODO=testnet`)

El retiro de Caja de Barrio corre por defecto contra **nuestro propio Rafiki** (`OP_MODO=propio`): las wallets son de
`op.cajadebarrio.lat`, el saldo vive en la billetera virtual y el ASE pone y retira la liquidez. Ese flujo no cambia.

El **modo testnet** es para la demo de interoperabilidad. El mismo retiro corre contra wallets del **testnet de
Interledger** (`wallet.interledger-test.dev`), y el backend actúa solo como **cliente de Open Payments**, con un pago
**entre monedas**: el cliente paga en USD y la tienda recibe COP.

## Qué cambia en modo testnet

| | Modo propio (por defecto) | Modo testnet |
|---|---|---|
| Wallets | Las guardadas de la persona y la tienda (`op.cajadebarrio.lat`) | `OP_TESTNET_WALLET_CLIENTE` y `OP_TESTNET_WALLET_TIENDA` las **reemplazan**; la base no se toca |
| Cupo (grant interactivo) | 1.000.000 COP, aprobado en nuestro `/idp` | `OP_TESTNET_CUPO` en la moneda **del cliente** (por ejemplo 100 USD), aprobado en la **pantalla de la test wallet** |
| Cobros | Dos: tienda (monto + comisión) y Caja de Barrio (1 %) | **Uno**: la tienda, con `incomingAmount` en COP (monto + comisión). El 1 % se anota en el libro interno al cerrar |
| Cotización | Dos, con `debitAmount` en la moneda del cliente | **Una, por receptor y sin monto**: toma el `incomingAmount` del cobro y Rafiki calcula el débito en USD. Se devuelven `debit_amount` y `receive_amount` con su `asset_code` y `asset_scale` |
| Saldo | La billetera virtual debe cubrir el total | No se valida: el saldo es real en la wallet del cliente. Si no alcanza, el pago falla con *"La billetera del cliente no tiene fondos suficientes"* |
| Liquidez del ASE | `fondear()` antes y `retirarLiquidezCobros()` al cerrar | **No se llaman** (son del Admin API de nuestro Rafiki) |
| Pagos salientes | Dos | **Uno**, con un solo APROBAR |

**Las reglas de oro no cambian:**
- verificación facial vigente (`face_match_ok`) y visto bueno de riesgo antes de pagar;
- APROBAR atado a la cotización vigente (una cotización nueva exige otro APROBAR);
- la entrega solo se confirma si el incoming payment de la tienda figura completo.

## Cómo activarlo

En `deploy/llaves.env` (o en el entorno local). **Las llaves nunca van a git**:

```
OP_MODO=testnet
OP_TLS=true
OP_CLIENT_WALLET_ADDRESS=https://ilp.interledger-test.dev/9f9bfa3d
OP_KEY_ID=<el Key ID que muestra la test wallet para esa llave>
OP_PRIVATE_KEY_PATH=/home/backend/keys/testnet-private.key
OP_TESTNET_WALLET_CLIENTE=https://ilp.interledger-test.dev/andres-cajadebarrio
OP_TESTNET_WALLET_TIENDA=https://ilp.interledger-test.dev/laura-cajadebarrio
OP_TESTNET_CUPO=100
OP_FINISH_URL=https://agente.cajadebarrio.lat/op/finish
```

- `OP_CLIENT_WALLET_ADDRESS` es **la wallet a la que está atada la llave** en la test wallet (Settings → Developer
  Keys). Hoy es `9f9bfa3d`, de la cuenta EUR. Si se usa otra wallet sin llave, el testnet responde
  `400 could not determine client`. Se comprueba con `curl https://ilp.interledger-test.dev/<wallet>/jwks.json`,
  que debe listar el `kid`. Solo identifica a la app, así que su moneda no importa.
- Al arrancar, el backend **resuelve las dos wallets** por Open Payments. Si alguna no resuelve, no arranca y dice
  cuál es.
- Para volver al modo de siempre: `OP_MODO=propio`, o quitar la variable, y restaurar `OP_CLIENT_WALLET_ADDRESS`,
  `OP_KEY_ID` y `OP_PRIVATE_KEY_PATH` del Rafiki propio.

## Probarlo sin el agente: `cmd/retiro -testnet`

Hace el retiro completo y muestra cada ID:
1. resuelve las wallets;
2. pide el cupo y espera la aprobación de una **persona** en la test wallet;
3. crea el cobro de la tienda en COP;
4. pide la cotización desde la wallet en USD;
5. hace el pago saliente;
6. espera hasta que el cobro quede completo.

Se corre **en el computador de quien aprueba**, porque al aprobar el navegador vuelve a `http://localhost:8099`:

```bash
docker run --rm -it -p 8099:8099 -v "$PWD:/src" -w /src \
  -v "$HOME/Downloads/private.key:/keys/testnet.key:ro" \
  -e OP_MODO=testnet -e OP_TLS=true \
  -e OP_CLIENT_WALLET_ADDRESS=https://ilp.interledger-test.dev/9f9bfa3d \
  -e OP_KEY_ID=<key id> -e OP_PRIVATE_KEY_PATH=/keys/testnet.key \
  -e OP_TESTNET_WALLET_CLIENTE=https://ilp.interledger-test.dev/andres-cajadebarrio \
  -e OP_TESTNET_WALLET_TIENDA=https://ilp.interledger-test.dev/laura-cajadebarrio \
  -e OP_TESTNET_CUPO=100 \
  golang:1.22-alpine go run ./cmd/retiro -testnet -monto 10000
```

El comando imprime un enlace: ábrelo, entra a la test wallet y **aprueba**. Al volver el navegador, el comando sigue
solo y termina con *"el dinero se movió de USD a COP en el testnet"*.

## Evidencia: el pago real (2026-10-17, 08:33 en Colombia)

`cmd/retiro -testnet -monto 10000`, con la aprobación del cupo hecha por una persona en la test wallet:

| Paso | Resultado |
|---|---|
| Wallets | cliente `andres-cajadebarrio` en **USD** (escala 2) · tienda `laura-cajadebarrio` en **COP** (escala 2) |
| Cupo | 100 USD; aprobado en la test wallet; **hash del callback verificado** contra `https://auth.interledger-test.dev/f537937b-…` |
| Cobro de la tienda | `incoming-payments/174553ca-bbdb-49cd-a5b7-797ecb79b8a5` por 11.000 COP (10.000 más 1.000 de comisión) |
| Cotización por receptor | `quotes/1dc48616-b9c6-409c-90c4-82e9c58cd93a`: **debita 3,47 USD y recibe 11.000,00 COP** |
| Pago saliente | `outgoing-payments/1dc48616-b9c6-409c-90c4-82e9c58cd93a` |
| Cobro | **COMPLETADO**: la tienda recibió 11.000,00 COP |

## Qué no hace (a propósito)

- No hay peering entre el testnet y nuestro Rafiki: un pago desde el testnet hacia `op.cajadebarrio.lat` no tiene
  ruta. En modo testnet, **las dos puntas** están en el testnet.
- La comisión del 1 % de Caja de Barrio no se cobra por Open Payments en el testnet: se anota en el libro interno.
  En el modo propio sí se cobra con su propio incoming payment.
