# 05 — El pago con Open Payments y la entrega

Fecha: madrugada del 2026-10-17. Herramienta: Claude Code (Claude Opus 5.5). Textual.

## Prompts (textuales)

> revisa los logs que cristian intentó y ocurrio un problema

> pide la ubicación, pero como estamos desde adk, posiblemente no le de, entonces como hacemos¡

> dice que mi tienda se quedó sin efectivo porque? si ya publiqué que tengo 500.000 en efectivo

> ya está ajustadop eso? haz el arreglo e implementa openpayment que lo que se necesita es hacer ya el pago

> pregunta: con esto puedo enviarme dinero desde el test de interledger, a la cuenta de por ejemplo cristian, porque la idea es esa. confirmame que eso pasará

> y como podemos solucionar eso? ya que se necesita hacer el pago sea con interledger o con rafiki, cual sería?

> cual es la diferencia entre interledger y rakifi? y debíamos utilzar cual?

> Sí, implementa la entrega (C12): entrega-tendero con el código, recibido-cliente y cancelar, y despliégalo.

> cuando estoy en la ui de rakifi porque no muestra el amoutn pero si el movimiento

(Con capturas de la UI de Rafiki: la wallet de la tienda con liquidez 0 y el pago saliente COMPLETED.)

> hazlo

> ahora bien, que cumplimos y que nos falta para lograr el objetivo de un prueba e2e completa? hazme un documento completo en .md que cumplimos y que nos falta

> Sí, sube los 4 commits del agente y actualiza prompts/ y AI_USAGE.md del agente y del backend con los prompts de esta noche.

## Resultado

- **Arreglos que salieron de los logs:**
  - el retiro reusa el `vendor_data` del alta, para que la comparación facial de Didit encuentre la cara guardada;
  - la reserva toma el efectivo del día de Colombia y no del día UTC;
  - `OP_TLS=true` para que el cliente de Open Payments use las URLs https públicas del VPS.
- **Contrato 7.2, 7.3 y 8.1 a 8.8 (`internal/pagos`):**
  - riesgo (tope por nivel y pitufeo);
  - cupo con grant interactivo y hash verificado, con el token cifrado;
  - dos cobros, dos cotizaciones, aprobación ligada a la cotización vigente;
  - pagos idempotentes y estado del cobro.
- **Liquidez:** Caja de Barrio es el proveedor de las wallets. Descuenta la billetera virtual del cliente, pone la
  liquidez de los pagos y, al completarse el cobro, la retira de Rafiki (`createIncomingPaymentWithdrawal`) y la abona
  a la tienda y a Caja de Barrio.
- **C12:**
  - entrega con el código de 4 dígitos, solo si el cobro está completo;
  - recibido del cliente (un "no" abre una disputa);
  - cancelar con devolución del efectivo;
  - reservas de la tienda.
- **Verificado en producción:** 205.600 COP debitados al cliente y 203.600 COP recibidos por la tienda en Rafiki. La
  reserva quedó `entregada`.
