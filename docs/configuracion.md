# Configuracion del backend

Variables que lee el backend, en el orden en que conviene cargarlas. La mitad
`RAFIKI_*` son valores que ya existen en `rafiki/.env`, que genera
`./rafiki/scripts/init.sh`; los `OP_*` son propios de esta aplicacion. Las dos
mitades estan separadas a proposito: `RAFIKI_*` es el lado ASE (lo que Rafiki nos
exige y nos llama) y `OP_*` es el lado cliente de Open Payments (con que clave
firmamos nuestras peticiones), y esa separacion es la que permitiria hablar con
un segundo ASE sin enredar la configuracion.

Ningun valor de esta tabla es un secreto por si mismo: los secretos viven en
`rafiki/.env`, que no se versiona.

## Servidor HTTP

| Variable | Por defecto | Que es |
| --- | --- | --- |
| `HTTP_ADDR` | `:8000` | Direccion de escucha. El healthcheck de compose usa `/health`. |
| `LOG_LEVEL` | `info` | Uno de `debug`, `info`, `warn`, `error`. Un valor distinto es un error al arrancar. |

## Lado ASE

| Variable | Por defecto | Que es |
| --- | --- | --- |
| `RAFIKI_ADMIN_URL` | `http://localhost:3001/graphql` | Admin API de Rafiki. Dentro de la red de compose es `http://rafiki-backend:3001/graphql`. |
| `RAFIKI_OPEN_PAYMENTS_URL` | `https://localhost:3000` | Base publica de las wallet addresses. **Tiene que ser https**: Rafiki rechaza toda wallet address URL que no lo sea. Su host tiene que ser el que el cliente efectivamente llama, porque el documento se busca por la URL completa. |
| `RAFIKI_TENANT_ID` | — | El `OPERATOR_TENANT_ID` de `rafiki/.env`. |
| `RAFIKI_ADMIN_API_SECRET` | — | El `ADMIN_API_SECRET` de `rafiki/.env`; es el `apiSecret` del tenant operador. |
| `RAFIKI_SIGNATURE_SECRET` | — | El `SIGNATURE_SECRET` de `rafiki/.env`: verifica la cabecera `Rafiki-Signature` de cada webhook. |
| `RAFIKI_IDENTITY_SERVER_SECRET` | — | El `IDENTITY_SERVER_SECRET` de `rafiki/.env`: lo comparte el IdP del grant interactivo. |
| `RAFIKI_AUTH_SERVER_URL` | — | Direccion **servidor a servidor** del auth server, la que resuelve dentro de la red de compose: `http://rafiki-auth:3006`. **Ojo: no es el valor `AUTH_SERVER_URL` de `rafiki/.env`**, que es el publico; adentro de un contenedor el camino al host esta bloqueado y esa direccion no sirve. |
| `RAFIKI_INTERACTION_SERVER_URL` | — | La **servidor de interaccion** del auth server (`http://rafiki-auth:3009`), que es donde el IdP lee y resuelve la interaccion: `GET /grant/{id}/{nonce}` y `POST /grant/{id}/{nonce}/{choice}`. **No es la direccion que abre el navegador** (`/interact/{id}/{nonce}` en el 3006): el IdP la usaba y el auth server contestaba 404, que la pagina convertia en 502. |
| `RAFIKI_AUTH_SERVER_PUBLIC_URL` | — | La que abre el navegador del usuario: el `AUTH_SERVER_URL` de `rafiki/.env` (hoy `http://100.76.141.47:3006`). Solo se usa para el redirect final del grant interactivo. |

Los cuatro valores que se copian de `rafiki/.env` no son obligatorios para
arrancar: `/health` responde sin ellos, y el componente que necesita un secreto
es el que lo reporta faltante.

## Lado cliente de Open Payments

| Variable | Por defecto | Que es |
| --- | --- | --- |
| `OP_CLIENT_WALLET_ADDRESS` | la base publica mas `/app-cajadebarrio` | La wallet address de este backend: la identidad con la que firma y con la que resuelve la de los demas (la validacion de `5.4`). Vacia se **deriva** de `RAFIKI_OPEN_PAYMENTS_URL`, que es el nombre con el que el seed registra la clave, para que los dos no se separen al cambiar la base. |
| `OP_KEY_ID` | vacio | Nombre con el que se registra la clave. Vacio significa que la clave se nombra con la huella RFC 7638 de su clave publica. |
| `OP_PRIVATE_KEY_PATH` | `keys/op-private-key.pem` | Clave privada Ed25519 del cliente. Debe ser la **misma** cuyo JWK registro el seed: si el proceso generara otra, el JWK de la wallet address dejaria de verificar las peticiones firmadas. El archivo esta cubierto por `*.pem` del `.gitignore`. |

## Capa de datos y superficie interna

| Variable | Por defecto | Que es |
| --- | --- | --- |
| `DATABASE_URL` | vacio | DSN de PostgreSQL **con PostGIS** para el esquema del contrato (las once tablas). **Lleva credencial y no se imprime nunca**: los mensajes del store nombran host, puerto y base, y el eco crudo del driver se sanea. Sin ella el backend sirve igual los endpoints que Rafiki llama, y la superficie `/internal` **no se monta**. |
| `SERVICIO_TOKEN` | vacio | Lo que los MCP mandan en la cabecera `X-Servicio-Token`. Sin el, la superficie `/internal` **no se monta**: una superficie sin token quedaria **abierta**, asi que no montarla es la respuesta segura. |

## Proveedores

| Variable | Por defecto | Que es |
| --- | --- | --- |
| `VERIFIK_TOKEN` | vacio | Token de Verifik para validar la cedula colombiana (ruta 5.3). Sin el, la ruta **esta montada igual** y responde que no esta configurada: un llamador que recibe una ruta desconocida no puede distinguir una configuracion que falta de una URL mal escrita. |
| `DIDIT_API_KEY` | vacio | Credencial de Didit para crear sesiones biométricas (C8). No está disponible todavía; sin ella no se ejecuta una llamada real. |
| `DIDIT_WORKFLOW_ID` | vacio | Workflow de Didit usado por las sesiones biométricas (C8). Debe entregarse con la credencial del proveedor. |
| `DIDIT_WEBHOOK_SECRET` | vacio | Secreto para verificar `X-Signature-V2` en el webhook de Didit (C8). No se registra en Git. |
| `DIDIT_CALLBACK_URL` | vacio | URL HTTPS pública que Didit usará para el webhook; requiere el túnel del entorno real (C8). |

## Relacion con `env.example`

`env.example` existe y está versionado en la raíz. Contiene los nombres y valores de ejemplo
de las variables del backend Go, sin secretos reales. Esta referencia explica el consumidor y
la semántica de cada variable; las variables privadas que genera `rafiki/scripts/init.sh` para
el stack (incluidas `CAJA_DB_PASSWORD` y `SERVICIO_TOKEN`) están documentadas en
[`rafiki/README.md`](../rafiki/README.md) y viven en `rafiki/.env`, que no se versiona.
