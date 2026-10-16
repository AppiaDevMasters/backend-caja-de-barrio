# Rafiki (Open Payments) — entorno local

Stack local de [Rafiki](https://rafiki.dev) para Caja de Barrio. Levanta **un ASE**
(account servicing entity) propio: servidor de recursos Open Payments, servidor de
autorización GNAP, Admin API, UI de administración y sus bases de datos.

Se usan **imágenes publicadas y fijadas por versión** (`ghcr.io/interledger/rafiki-*`,
`v2.4.5-de`). No se compila el monorepo de Rafiki.

> Alcance actual: infraestructura de Rafiki. El backend de Caja de Barrio (rol de ASE:
> webhooks, tipos de cambio e IdP) todavía no existe; ver [Pendientes](#pendientes).

## Requisitos

- Docker Engine con el plugin Compose v2
- `openssl` y `bash` (para `scripts/init.sh`)
- Puertos libres: 3000-3003, 3006, 3007, 3009-3011 y 5432

## Puesta en marcha

```bash
cd rafiki
./scripts/init.sh          # genera .env y config/keys/ (idempotente)
docker compose up -d
```

Si vas a entrar desde otro dispositivo (o el stack corre en otro host), `localhost`
no sirve: las URLs públicas tienen que apuntar a una dirección alcanzable por el
navegador y por los clientes Open Payments.

```bash
cd rafiki
RAFIKI_PUBLIC_HOST=100.76.141.47 ./scripts/init.sh --force   # el host/IP que uses
docker compose up -d
```

Comprobar que quedó sano:

```bash
docker compose ps
curl -fsS http://localhost:3001/healthz    # Admin API del backend
curl -fsS http://localhost:3003/healthz    # Admin API de auth
```

Apagar (conserva los datos) y borrar el estado por completo:

```bash
docker compose down
docker compose down --volumes              # borra Postgres
./scripts/init.sh --force                  # regenera .env y claves
```

## Servicios y puertos

| Servicio       | URL local                        | Qué es                                        |
| -------------- | -------------------------------- | --------------------------------------------- |
| `rafiki-backend` | `http://localhost:3000`        | API Open Payments (servidor de recursos)      |
| `rafiki-backend` | `http://localhost:3001/graphql` | Admin API GraphQL (cuentas, wallet addresses, liquidez) |
| `rafiki-backend` | `http://localhost:3002`        | Conector ILP (ILP over HTTP)                  |
| `rafiki-auth`  | `http://localhost:3003/graphql`  | Admin API de auth                             |
| `rafiki-auth`  | `http://localhost:3006`          | Servidor de autorización Open Payments (GNAP) |
| `rafiki-auth`  | `http://localhost:3007`          | Introspección de access tokens                |
| `rafiki-auth`  | `http://localhost:3009`          | Interacción del usuario en grants interactivos |
| `rafiki-auth`  | `http://localhost:3011`          | API servicio a servicio                       |
| `rafiki-admin` | `http://localhost:3010`          | UI de administración de Rafiki                |
| `postgres`     | `localhost:5432`                 | Bases `rafiki_backend` y `rafiki_auth`        |
| `redis`        | interno                          | Cache y datos de ILP                          |
| `backend`      | `http://localhost:8000`          | Backend de Caja de Barrio: webhooks, tipos de cambio e IdP |

Las bases de datos se separan en `config/dbinit.sql`, que Postgres ejecuta una sola
vez al inicializar su volumen.

## Contabilidad: Postgres o TigerBeetle

Por defecto la contabilidad usa Postgres (`USE_TIGERBEETLE: 'false'`), para tener
menos piezas móviles. Para usar TigerBeetle:

```bash
docker compose -f docker-compose.yml -f compose.tigerbeetle.yml up -d
```

TigerBeetle corre con `privileged: true` y necesita memoria holgada; si su
contenedor termina con código 137, sube el límite de memoria de Docker.

## La clave del ASE se persiste a propósito

`rafiki-backend` firma con una clave Ed25519. Si `PRIVATE_KEY_FILE` no está
definido, Rafiki **genera una clave efímera en memoria en cada arranque**: la clave
pública registrada en el auth server deja de ser válida al reiniciar y los flujos de
grant fallan sin un error evidente. Por eso `scripts/init.sh` crea
`config/keys/private-key.pem` (clave privada, montada en solo lectura) y
`config/keys/jwks.json` (clave pública con el `kid` de `KEY_ID`, que es la que debe
registrarse en el auth server).

Si regeneras las claves con `./scripts/init.sh --force`, hay que volver a registrar
la clave pública.

## Por qué el backend corre dentro de la red `rafiki`

`WEBHOOK_URL` y `EXCHANGE_RATES_URL` apuntan a `http://backend:8000/...`, el nombre del
servicio, y **no** a `host.docker.internal`. El motivo está verificado en este host:
desde dentro de los contenedores los puertos TCP del host no responden — timeouts a
`172.17.0.1:3001`, `10.10.0.1:3001`, `10.10.0.1:5432` y `100.76.141.47:8000`, mientras
que `10.10.0.1:22` sí conecta (devuelve el banner SSH). Un backend corriendo en el host
nunca recibiría un webhook ni una consulta de tasas: los reintentos de Rafiki morían en
`timeout of 2000ms exceeded` después de 10 intentos.

El puerto 8000 se publica igual, porque el IdP (`/idp/`) lo abre el navegador del
usuario, y el navegador sí alcanza el host. La clave del cliente Open Payments se monta
desde `../keys` en solo lectura: si el contenedor generara otra clave, el JWK registrado
en la wallet address dejaría de verificar.

## Variables del backend

La referencia por variable está en [`../docs/configuracion.md`](../docs/configuracion.md),
verificada contra los `os.Getenv` del código, y los nombres con valores de ejemplo en
[`../env.example`](../env.example). Esta tabla es la vista del stack: qué inyecta
`docker-compose.yml` en el servicio `backend` y de dónde sale cada valor.

| Variable | Sale de | Para qué |
| --- | --- | --- |
| `HTTP_ADDR` | compose | Dirección de escucha (`:8000`). |
| `LOG_LEVEL` | `.env` | Nivel de log del backend. |
| `RAFIKI_ADMIN_URL` | compose | Admin API del servidor de recursos, por nombre de servicio. |
| `RAFIKI_OPEN_PAYMENTS_URL` | compose | Base efectiva de las wallet addresses del ASE; la lee el seed. |
| `RAFIKI_TENANT_ID` | `.env` (`OPERATOR_TENANT_ID`) | Tenant operador que actúa. |
| `RAFIKI_ADMIN_API_SECRET` | `.env` (`ADMIN_API_SECRET`) | Firma las llamadas a la Admin API. |
| `RAFIKI_SIGNATURE_SECRET` | `.env` (`SIGNATURE_SECRET`) | Verifica la firma de los webhooks. |
| `RAFIKI_IDENTITY_SERVER_SECRET` | `.env` (`IDENTITY_SERVER_SECRET`) | Secreto compartido con el IdP. |
| `OP_KEY_ID`, `OP_PRIVATE_KEY_PATH` | `.env` (`KEY_ID`) | Identidad Open Payments del backend; la clave privada se monta desde `../keys`. |

Fuera de Docker, `cmd/backend` y `cmd/seed` leen las mismas variables del entorno del
proceso: exportalas desde `.env` mapeando los cuatro nombres que difieren.

## Acceso desde otros dispositivos

El stack corre en el host donde está Docker, no en tu equipo: `localhost` solo
sirve para clientes que corren en ese mismo host (los contenedores y `curl` local).
Las URLs públicas se generan con `RAFIKI_PUBLIC_HOST`:

```bash
RAFIKI_PUBLIC_HOST=100.76.141.47 ./scripts/init.sh --force   # LAN, Tailscale o DNS
```

Rutas útiles con ese host: Admin API `http://<host>:3001/graphql` y servidor Open
Payments `http://<host>:3000`. Hasta que exista una wallet address, el servidor Open
Payments responde 404; además Rafiki anuncia sus wallet addresses con `https://`
forzado aunque `OPEN_PAYMENTS_URL` sea `http://`, así que un cliente local tiene que
bajar a `http` al llamarlas.

### La UI de administración necesita HTTPS (o un túnel SSH)

La UI (`:3010`) pide credenciales: **Tenant ID** (`OPERATOR_TENANT_ID`) y **API
Secret** (`ADMIN_API_SECRET`), que guarda en la cookie de sesión `__session`. Pero esa
cookie está fijada como `secure: true` en la imagen del frontend
(`createCookieSessionStorage` en `packages/frontend/build/index.js`), así que un
navegador **la descarta sobre `http://`** y toda página con datos responde 401 → 500,
incluso después de guardar las credenciales. Los navegadores solo aceptan cookies
`Secure` en orígenes seguros: `https://…` o `http://localhost` (`localhost` cuenta
como contexto seguro; una IP de LAN o de Tailscale, no).

Dos formas de que funcione, sin tocar el stack:

```bash
# 1) Túnel SSH desde tu equipo; después abrí http://localhost:3010
ssh -N -L 3010:localhost:3010 carchila@homelab

# 2) HTTPS en la tailnet (una vez, en el host); después https://<host>.<tailnet>.ts.net
tailscale serve --bg --https=443 http://localhost:3010
```

Si la abrís por IP sobre `http://`, la UI va a mostrar errores de autorización
siempre, por más que las credenciales sean correctas.

`IDENTITY_SERVER_URL` tiene una trampa: `rafiki-auth` lo copia a la columna
`idpConsentUrl` de `rafiki_auth.tenants` **en el primer arranque**, porque lo siembra
la migración `20241205153036_seed_operator_tenant`. Si cambias el host después, hay
que corregir esa columna — la Admin API de auth no tiene mutación de tenant — o los
grants interactivos seguirán mandando el navegador al valor viejo:

```bash
docker exec rafiki-postgres psql -U rafiki -d rafiki_auth \
  -c "update tenants set \"idpConsentUrl\"='http://100.76.141.47:8000/idp/';"
```

## Variables de `.env`

`scripts/init.sh` genera `rafiki/.env` con secretos aleatorios. No se versiona
(está en `.gitignore`). La plantilla `.env.example` no se incluye porque el harness
de Pi bloquea escribir archivos `.env.*`; esta tabla es la referencia.

| Variable                          | Descripción                                                          |
| --------------------------------- | -------------------------------------------------------------------- |
| `RAFIKI_VERSION`                  | Tag de las imágenes `ghcr.io/interledger/rafiki-*`                    |
| `RAFIKI_PUBLIC_HOST` (solo `init.sh`) | Host con el que se anuncian `PUBLIC_OPEN_PAYMENTS_URL`, `AUTH_SERVER_URL` e `IDENTITY_SERVER_URL`; por defecto `localhost` (no queda en `.env`) |
| `POSTGRES_USER`, `POSTGRES_PASSWORD` | Credenciales de Postgres; la contraseña va en hex por ser segura en URL |
| `BACKEND_DB`, `AUTH_DB`           | Nombres de las dos bases                                             |
| `INSTANCE_NAME`                   | Nombre visible del ASE                                               |
| `OPERATOR_TENANT_ID`              | UUID v4 del tenant operador (Rafiki lo exige)                        |
| `KEY_ID`                          | `kid` de la clave pública del ASE                                    |
| `ADMIN_API_SECRET`                | HMAC SHA-256 de la Admin API                                         |
| `SIGNATURE_SECRET`                | Firma los webhooks hacia nuestro backend y las llamadas a la Admin API |
| `STREAM_SECRET`                   | Semilla (base64) de los secretos STREAM                              |
| `DB_ENCRYPTION_SECRET`            | Cifra payloads de pagos parciales (base64 de 32 bytes)               |
| `IDENTITY_SERVER_SECRET`          | Secreto compartido con el IdP (cabecera `x-idp-secret`)              |
| `COOKIE_KEY`                      | Firma las cookies de sesión de interacción                           |
| `OPEN_PAYMENTS_URL`               | Base que el servidor de recursos anuncia a los clientes (el `resourceServer` del documento de wallet address): por nombre de servicio, `http://rafiki-backend:3000` |
| `PUBLIC_OPEN_PAYMENTS_URL`        | La misma base vista por un navegador; la usa la UI de administración para enlazar wallet addresses |
| `AUTH_SERVER_URL`                 | URL pública del servidor de autorización                             |
| `IDENTITY_SERVER_URL`             | IdP que abre el navegador en un grant interactivo (lo sirve nuestro backend) |
| `WEBHOOK_URL`                     | Endpoint de webhooks de nuestro backend, visto desde los contenedores |
| `EXCHANGE_RATES_URL`              | Endpoint de tipos de cambio de nuestro backend                       |
| `*_PORT`                          | Puertos publicados en el host                                         |
| `LOG_LEVEL`                       | Nivel de log (Pino)                                                   |

Los contenedores alcanzan `localhost` del host como `host.docker.internal`, mapeado
en `rafiki-backend` con `extra_hosts`. Por eso `WEBHOOK_URL` y `EXCHANGE_RATES_URL`
apuntan al backend de Caja de Barrio en el puerto 8000.

## Pendientes

Lo que falta para que un pago funcione de punta a punta. Todos son trabajo de nuestro
backend (el ASE), no de Rafiki:

1. **Wallet addresses y cuentas**: ya están creadas (asset COP, tres wallet addresses y
   la clave pública registrada con `createWalletAddressKey`). Falta que sean alcanzables:
   están en `https://100.76.141.47:3000/...` y en el puerto 3000 no hay TLS todavía, así
   que necesitan un terminador (por ejemplo `tailscale serve`).
2. **Webhooks**: `POST /webhooks` implementado en `internal/ase`, con la firma
   `Rafiki-Signature` verificada y 200 exacto (lo único que Rafiki acepta).
3. **Tipos de cambio**: `GET /rates` implementado en `internal/ase`, con tabla demo
   explícita USD↔COP.
4. **IdP**: `/idp/` para aprobar grants interactivos.
5. **Peers**: este stack tiene un solo ASE sin pares. Los pagos entre wallet addresses
   del mismo ASE funcionan sin peer; para pagos hacia otro ASE hace falta peering o
   auto-peering (`ENABLE_AUTO_PEERING`, requiere URL pública).

Para desarrollo contra un entorno ya probado por el equipo de Rafiki existe el
[Local Playground](https://rafiki.dev/integration/playground/overview/) oficial, que
incluye dos ASE de prueba. Se levanta desde el monorepo de Rafiki (`pnpm localenv:compose up`)
y compila las imágenes desde el código fuente, por eso no se usa aquí.

## Backend en Python

Antes de escribir el backend conviene leer la evaluación del SDK oficial de Python:
[`../docs/sdk-python-open-payments.md`](../docs/sdk-python-open-payments.md).
Resumen: el SDK está marcado *work in progress* por el propio proyecto, no está
publicado en PyPI y no es una base segura para producción sin trabajo adicional.
