# backend-caja-de-barrio

Cada tienda del barrio es un cajero. Caja de Barrio: orquestación con Docker Compose,
documentación y uso de IA. Open Payments Hackathon Medellín 2026 · AppiaDevMaster.

Hoy este repositorio es el **backend en Go** que cubre el rol de **ASE**
(account servicing entity) frente a [Rafiki](https://rafiki.dev): los tres endpoints
que Rafiki y su servidor de autorización llaman de vuelta (webhooks, tipos de cambio e
IdP), más el aprovisionamiento del ASE local. Al lado vive el stack local de Rafiki
(servidor de recursos, servidor de autorización GNAP, Admin API, UI y sus bases de
datos). El **cliente** de Open Payments que hará el retiro todavía no está escrito, y la
capa de datos del producto (PostGIS) todavía no vive en este repositorio.

## Estado de hoy

| Pieza | Estado |
| --- | --- |
| `cmd/backend` — servidor HTTP con `/health` | Listo y corriendo como contenedor |
| `POST /webhooks` — firma `Rafiki-Signature` verificada, 200 exacto | Listo; la entrega real de un evento espera al flujo de pago |
| `GET /rates` — tabla demo USD↔COP | Listo; verificado a través del servicio de tasas de Rafiki |
| `/idp/` y `/idp/approve` — pantalla de consentimiento de un grant interactivo | Listo; solo se ve con un grant en vuelo (necesita al cliente) |
| `cmd/seed` — activo COP, liquidez, tres wallet addresses y el JWK del cliente | Listo e idempotente |
| `internal/rafikiadmin` — cliente de la Admin API (firma HMAC, GraphQL) | Listo |
| `internal/opkeys` — clave Ed25519 del cliente y su `kid` (huella RFC 7638) | Listo |
| `internal/opclient` — flujo de retiro con el SDK oficial de Go | **Pendiente** (tareas T7-T9) |
| Pago de punta a punta contra el Rafiki local | **Pendiente**: depende de `internal/opclient` |

## Arquitectura

```
  navegador del usuario                 host (curl, docker, go)
        |                                      |
        | IdP: http://<host>:8000/idp/         | Admin API: :3001/graphql
        v                                      v
  +---------------- red docker `rafiki` -----------------+
  |                                                       |
  |   backend:8000   <--- webhooks (Rafiki-Signature) ----+-- rafiki-backend:3000
  |   (Caja de        <--- GET /rates?base=USD -----------+   (servidor de recursos)
  |    Barrio)        ----> x-idp-secret ---------------->+-- rafiki-auth:3006
  |                                                       |   (GNAP, interacciones)
  |   rafiki-backend:3001 (Admin API)                     |
  |   rafiki-admin:3010 (UI)  ·  postgres  ·  redis       |
  +-------------------------------------------------------+
```

Dos decisiones de topología que explican el dibujo, y que están verificadas en este
host (el detalle, en [`rafiki/README.md`](rafiki/README.md)):

- **El backend corre como contenedor dentro de la red `rafiki`, no en el host.** Los
  contenedores no alcanzan los puertos TCP del host, así que un backend en el host
  nunca recibiría un webhook ni una consulta de tasas. Por eso `WEBHOOK_URL` y
  `EXCHANGE_RATES_URL` apuntan a `http://backend:8000/...`.
- **La wallet address se publica en `https` y se llama en `http`.** Rafiki rechaza toda
  wallet address URL que no sea `https`, pero en el puerto 3000 no hay TLS terminando.
  Dentro de la red el nombre de servicio resuelve; desde afuera, no (ver
  [Qué falta](#qué-falta-para-el-pago-de-punta-a-punta)).

## Requisitos

- Docker Engine con el plugin Compose v2
- Go 1.22 (`go version`) — el módulo no declara dependencias externas
- `openssl` y `bash` (los usa `rafiki/scripts/init.sh`)
- Puertos libres: 3000-3003, 3006, 3007, 3009-3011, 5432 y 8000

## Cómo levantar la POC

Los cinco pasos, en este orden. Se corre desde la raíz del repositorio salvo donde se
indique.

### 1. Preparar el directorio de claves

```bash
mkdir -p keys
```

`rafiki/docker-compose.yml` monta este directorio dentro del backend
(`../keys:/home/backend/keys:ro`). El directorio **no se versiona** (su contenido está
cubierto por `*.pem` del `.gitignore`), así que en un clon nuevo no existe. Si no lo
creas antes de levantar los contenedores, Docker crea el origen del montaje como
`root:root` y después el seed ya no puede escribir la clave privada del cliente.

### 2. Generar la configuración y las claves de Rafiki

```bash
cd rafiki
./scripts/init.sh
```

Crea `rafiki/.env` con secretos aleatorios y `rafiki/config/keys/` con la clave Ed25519
del ASE. Es idempotente. Si el stack va a ser alcanzado desde otro dispositivo, el host
público se fija aquí:

```bash
RAFIKI_PUBLIC_HOST=100.76.141.47 ./scripts/init.sh --force
```

### 3. Levantar el stack

```bash
cd rafiki
docker compose up -d --build
docker compose ps
```

`--build` compila la imagen `caja-de-barrio-backend:local` con el
[`Dockerfile`](Dockerfile) de la raíz y la corre dentro de la red `rafiki`. Deben quedar
seis servicios: `rafiki-backend`, `rafiki-auth`, `rafiki-postgres`, `rafiki-redis`,
`rafiki-admin` y `backend`.

### 4. Aprovisionar el ASE (seed)

En una terminal nueva, desde la raíz del repositorio:

```bash
# Lee un nombre de rafiki/.env sin ejecutarlo como shell: INSTANCE_NAME lleva
# espacios y `source` falla al leerlo.
env_val() { sed -n "s/^$1=//p" rafiki/.env | head -n 1; }

export RAFIKI_ADMIN_URL=http://localhost:3001/graphql
export RAFIKI_OPEN_PAYMENTS_URL=https://rafiki-backend:3000
export RAFIKI_TENANT_ID="$(env_val OPERATOR_TENANT_ID)"
export RAFIKI_ADMIN_API_SECRET="$(env_val ADMIN_API_SECRET)"

go run ./cmd/seed
```

Qué hace, en orden: crea el activo `COP` con escala 2, le carga la liquidez del demo,
fija el ajuste por tenant `WALLET_ADDRESS_URL`, crea una wallet address por actor
(`tienda-cajadebarrio`, `laura-cajadebarrio` y `app-cajadebarrio`) y registra la clave
pública del cliente en la de la app.

Dos detalles que importan:

- **Es idempotente**: correrlo de nuevo devuelve los mismos IDs y no crea nada. Sirve
  después de cada reinicio del stack.
- **`RAFIKI_OPEN_PAYMENTS_URL` es `https://rafiki-backend:3000` a propósito.** Es la base
  con la que Rafiki *compone y guarda* la URL de cada wallet address; el seed solo la
  concatena, no la resuelve, así que funciona igual corriendo en el host. Si cambias esa
  base se crean wallet addresses nuevas (las viejas quedan huérfanas: no hay mutación de
  borrado) porque Rafiki ata la clave de idempotencia a los argumentos de la primera
  petición.

El seed crea también `keys/op-private-key.pem` la primera vez (modo 0600, ignorado por
git) y lo reutiliza tal cual en las corridas siguientes, para no rotar la clave. Tiene
que ser la **misma** clave que el contenedor monta: si el contenedor generara otra, el
JWK registrado en la wallet address dejaría de verificar las peticiones firmadas.

### 5. Verificar que la POC responde

```bash
# El backend, desde el host (el puerto 8000 se publica porque el navegador abre el IdP)
curl -fsS http://localhost:8000/health
curl -fsS "http://localhost:8000/rates?base=USD"

# Un webhook sin firma tiene que ser rechazado
curl -sS -o /dev/null -w '%{http_code}\n' -X POST \
  -H 'Content-Type: application/json' --data-binary '{"id":"evt","type":"demo","data":{}}' \
  http://localhost:8000/webhooks

# El camino que de verdad usa Rafiki: de contenedor a contenedor
docker exec rafiki-backend wget -qO- http://backend:8000/health
docker exec rafiki-backend wget -qO- "http://backend:8000/rates?base=USD"

# El documento de wallet address que Rafiki sirve para nuestro ASE
docker exec rafiki-backend wget -qO- --header='Accept: application/json' \
  http://rafiki-backend:3000/app-cajadebarrio
```

Salidas esperadas, verificadas en vivo:

```
{"status":"ok"}
{"base":"USD","rates":{"COP":4000}}
401
{"id":"https://rafiki-backend:3000/app-cajadebarrio","publicName":"Caja de Barrio",
 "assetCode":"COP","assetScale":2,
 "authServer":"http://rafiki-auth:3006/<tenantId>",
 "resourceServer":"http://rafiki-backend:3000/<tenantId>"}
```

Los dos nombres del documento resuelven **dentro de la red**, que es lo que habilita el
flujo de grant sin reescribir hosts. Una URL `https://<host>:3000/...` desde fuera de
Docker no responde todavía: no hay TLS en el puerto 3000.

### Apagar

```bash
cd rafiki
docker compose down              # conserva los datos
docker compose down --volumes     # borra Postgres; después: ./scripts/init.sh --force
```

## Qué falta para el pago de punta a punta

Todo es trabajo de nuestro backend, no de Rafiki. El plan y la bitácora de lo ya
verificado están en [`odd/tasks/backend-go-ase-y-cliente-op.md`](odd/tasks/backend-go-ase-y-cliente-op.md).

1. **`internal/opclient` (T7-T9)**: cliente autenticado con el SDK oficial de Go,
   resolución de la wallet address, incoming payment y su quote, grant interactivo de
   `outgoing-payment` con `limits.debitAmount` igual al débito de la quote, `/op/finish`
   con `interact_ref`, rotación del token y verificación de que el incoming payment quedó
   completado. Sin esto no hay retiro, y sin retiro no hay entrega real de webhook ni
   pantalla de IdP que ver.
2. **TLS delante del puerto 3000** si el demo necesita que un tercero (una billetera
   externa) alcance este ASE. Rafiki exige `https` en toda wallet address URL y hoy solo
   la red de compose resuelve los nombres anunciados. La forma prevista:
   `tailscale serve --bg --https=443 http://localhost:3000` y reapuntar el ajuste por
   tenant.
3. **Un peer** para pagos hacia otro ASE. Los pagos entre wallet addresses del mismo ASE
   no lo necesitan.

## Estructura del repositorio

| Ruta | Qué es |
| --- | --- |
| `cmd/backend` | El proceso del backend: carga la configuración, monta las rutas y sirve |
| `cmd/seed` | Aprovisiona el ASE (activo, liquidez, wallet addresses, JWK del cliente) |
| `internal/config` | Lectura y validación del entorno, con error al arrancar si un valor no sirve |
| `internal/httpserver` | Router del backend: `/health` y el montaje de las rutas de los componentes |
| `internal/ase` | Los endpoints que Rafiki y el servidor de autorización llaman de vuelta |
| `internal/rafikiadmin` | Cliente de la Admin API de Rafiki: firma HMAC y GraphQL |
| `internal/opkeys` | Clave Ed25519 del cliente: generación, PEM en 0600 y `kid` por huella RFC 7638 |
| `rafiki/` | El stack local de Rafiki y su `init.sh` (ver [`rafiki/README.md`](rafiki/README.md)) |
| `docs/` | Referencia de configuración y evaluación del SDK de Python |
| `odd/` | El plan por tareas y su bitácora, con la evidencia verificada |
| `prompts/` | Los prompts reales con los que se trabajó, sin resumir |

## Pruebas

```bash
go test ./...        # 125 casos en 6 paquetes
go vet ./...
gofmt -l .           # no debe imprimir nada
```

La suite no necesita el stack levantado: los clientes HTTP se ejercitan contra
servidores de prueba. Lo que sí requiere el stack real es la verificación en vivo, y
está registrada con su evidencia en la bitácora de `odd/tasks/`.

Para correr el backend fuera de Docker (útil para depurar, no para la POC: un backend
en el host no recibe webhooks) hay que exportar las mismas variables mapeando los cuatro
nombres que difieren. La tabla está en
[`docs/configuracion.md`](docs/configuracion.md).

## Documentación

- [`docs/configuracion.md`](docs/configuracion.md) — referencia por variable, verificada
  contra los `os.Getenv` del código. Es la fuente de los nombres.
- [`env.example`](env.example) — los nombres con valores de ejemplo, sin secretos.
- [`rafiki/README.md`](rafiki/README.md) — el stack de Rafiki: servicios, puertos, la
  clave del ASE, por qué el backend vive dentro de la red y la UI de administración.
- [`odd/tasks/backend-go-ase-y-cliente-op.md`](odd/tasks/backend-go-ase-y-cliente-op.md) —
  qué se decidió, qué se verificó y con qué evidencia.
- [`docs/sdk-python-open-payments.md`](docs/sdk-python-open-payments.md) — evaluación del
  SDK oficial de Python (no es una base confiable todavía).

## Uso de IA

[`AI_USAGE.md`](AI_USAGE.md) declara las herramientas usadas, para qué y en qué
archivos. Los prompts reales, sin resumir, están en [`prompts/`](prompts/).
