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

Los cuatro valores que se copian de `rafiki/.env` no son obligatorios para
arrancar: `/health` responde sin ellos, y el componente que necesita un secreto
es el que lo reporta faltante.

## Lado cliente de Open Payments

| Variable | Por defecto | Que es |
| --- | --- | --- |
| `OP_KEY_ID` | vacio | Nombre con el que se registra la clave. Vacio significa que la clave se nombra con la huella RFC 7638 de su clave publica. |
| `OP_PRIVATE_KEY_PATH` | `keys/op-private-key.pem` | Clave privada Ed25519 del cliente. Debe ser la **misma** cuyo JWK registro el seed: si el proceso generara otra, el JWK de la wallet address dejaria de verificar las peticiones firmadas. El archivo esta cubierto por `*.pem` del `.gitignore`. |

## Por que no hay `.env.example`

El contexto del proyecto pide un `.env.example` con los nombres de las variables y
nada mas. El harness de Pi bloquea escribir rutas `.env.*`, por politica de
seguridad, asi que esta tabla es la fuente de los nombres. Para crear el archivo
literal, con los nombres y sin valores:

```bash
grep -oE '`[A-Z_]+`' docs/configuracion.md | tr -d '`' | sort -u | sed 's/$/=/' > .env.example
```
