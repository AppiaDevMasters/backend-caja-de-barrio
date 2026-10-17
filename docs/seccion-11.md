# Respuesta operativa a la sección 11

Este documento responde los cinco puntos de la sección 11 del contrato. Separa lo que ya está
verificado en el stack local de lo que depende de las rutas internas todavía pendientes.

## 1. URL base y canales privados

| Uso | Valor local | Entrega |
| --- | --- | --- |
| Backend dentro de Compose | `http://backend:8000` | Configuración del stack; no es secreto |
| Backend desde el host | `http://localhost:8000` | Sólo desarrollo local |
| Open Payments dentro de Compose | `http://rafiki-backend:3000` | Configuración interna del stack |
| Superficie privada | `X-Servicio-Token: <SERVICIO_TOKEN>` | Entregar por canal privado; nunca escribir el valor en Git o en esta documentación |

El compose entrega la `DATABASE_URL` de la base del contrato y `SERVICIO_TOKEN` sólo al backend Go.
Rafiki conserva su propia `DATABASE_URL` para `rafiki_backend` y `rafiki_auth`. Los MCP sólo
necesitan `BACKEND_URL` y `SERVICIO_TOKEN`.

## 2. Paths y campos

La superficie pública del contrato vive bajo `/internal` y exige `X-Servicio-Token`:

| Área | Paths previstos | Estado |
| --- | --- | --- |
| Identidad | `/internal/identidad/personas`, `/internal/identidad/politicas/aceptaciones`, `/internal/identidad/cedulas/verificar` | Implementados y probados |
| Mapa | `/internal/mapa/tiendas/cercanas`, `/internal/mapa/tiendas/{tienda_id}`, `/internal/mapa/tiendas/{tienda_id}/efectivo` | Implementados y probados |
| Caja | `/internal/caja/reservas` y sus acciones | Pendiente C10–C12 |
| Pagos | `/internal/pagos/wallets`, `/internal/pagos/cupos`, `/internal/pagos/cobros`, `/internal/pagos/cotizaciones`, `/internal/pagos/aprobaciones`, `/internal/pagos/pagos`, `/internal/pagos/cobros/estado` | Pendiente C13–C14 |

Los nombres de campos y ejemplos canónicos siguen en
[`contrato-backend.md`](../../contexto-caja-de-barrio/contrato-backend.md), secciones 5 a 8.
Las respuestas de error usan `{ "error": { "codigo": "...", "mensaje": "..." } }`.

## 3. Wallet addresses finales

El seed registra estas identidades en el tenant local:

- `tienda-cajadebarrio`: wallet de la tienda.
- `app-cajadebarrio-cop`: wallet COP donde entra el 1% de Caja de Barrio.
- `laura-cajadebarrio`: wallet de Laura, cliente del flujo de prueba.
- `andres-cajadebarrio`: wallet USD de Andrés, prevista para la escena de remesa.
- `app-cajadebarrio`: wallet de la aplicación usada por el conductor Open Payments.

El nombre de servicio que compone las direcciones dentro del stack es `rafiki-backend:3000`.
No se publican claves privadas ni tokens en este documento; el seed usa `keys/op-private-key.pem`,
que está ignorada por Git.

## 4. Prueba mínima de pago

### Verificado

T12 ejecutó directamente el cliente Open Payments dentro del contenedor `backend`:

- dos incoming payments y dos quotes en COP;
- grant interactivo, callback con hash verificado y espera GNAP;
- token aprobado y dos outgoing payments con rotación;
- liquidez adelantada sólo para el ensayo intra-ASE con `retiro -fondear`;
- liquidación observada: la tienda recibió `1.100.000` unidades mínimas (`11.000 COP`).

### Pendiente del contrato interno

La secuencia exacta de la sección 11.4 todavía no puede declararse completa porque las rutas que
la ejecutan desde los MCP no existen todas:

1. `POST /internal/pagos/cupos` y su continuación: pendiente C13.
2. Reserva con `face_match_ok` y `riesgo_ok`: pendiente C10–C12.
3. `POST /internal/pagos/cobros`, `/cotizaciones`, `/aprobaciones` y `/pagos`: pendiente C14.
4. Repetición idempotente de `POST /pagos`: pendiente C14/C15.

T12 no se presenta como sustituto de C19: verifica el cliente contra Rafiki, no el recorrido
completo del contrato interno.

## 5. Webhook de Didit

El webhook requiere `DIDIT_WEBHOOK_SECRET`, firma `X-Signature-V2` y la ruta contractual
`POST /internal/identidad/webhooks/didit` (el gateway debe reenviar el cuerpo crudo y la firma).
La URL pública final será `https://<túnel>/webhook/didit` cuando exista el túnel del entorno.

La implementación de Didit, la deduplicación por `Event.ID` antes de cualquier efecto y la
configuración de `DIDIT_CALLBACK_URL` siguen pendientes en C8. No hay tokens Didit en el repositorio.

## Estado de cierre

Este documento cierra la respuesta documental de la sección 11 con evidencia explícita. La prueba
end-to-end del contrato queda abierta como C19 hasta que C10–C14 y C8 estén implementadas y
verificadas con la interfaz pública.
