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

## Acceso desde otros dispositivos

El stack corre en el host donde está Docker, no en tu equipo: `localhost` solo
sirve para clientes que corren en ese mismo host (los contenedores y `curl` local).
Las URLs públicas se generan con `RAFIKI_PUBLIC_HOST`:

```bash
RAFIKI_PUBLIC_HOST=100.76.141.47 ./scripts/init.sh --force   # LAN, Tailscale o DNS
```

Rutas útiles con ese host: UI de administración `http://<host>:3010` (sin
autenticación en local), Admin API `http://<host>:3001/graphql` y servidor Open
Payments `http://<host>:3000`.

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
| `RAFIKI_PUBLIC_HOST` (solo `init.sh`) | Host con el que se anuncian `OPEN_PAYMENTS_URL`, `AUTH_SERVER_URL` e `IDENTITY_SERVER_URL`; por defecto `localhost` (no queda en `.env`) |
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
| `OPEN_PAYMENTS_URL`               | Base pública de las wallet addresses del servidor de recursos        |
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

1. **Wallet addresses y cuentas**: crearlas con la Admin API GraphQL y registrar la
   clave pública del ASE con la mutación `createWalletAddressKey` sobre esa wallet
   address (el auth server no guarda claves del ASE: `rafiki_auth` no tiene tabla de
   claves).
2. **Webhooks**: `POST /webhooks` que valide la firma con `SIGNATURE_SECRET`.
3. **Tipos de cambio**: `GET /rates` con el formato que espera Rafiki.
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
