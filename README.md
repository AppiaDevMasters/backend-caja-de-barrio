# backend-caja-de-barrio

Cada tienda del barrio es un cajero. Caja de Barrio: orquestación con Docker Compose,
documentación y uso de IA. Open Payments Hackathon Medellín 2026 · AppiaDevMaster.

Hoy este repositorio es el **backend en Go** de Caja de Barrio, y tiene **tres piezas**:

1. el **rol de ASE** frente a [Rafiki](https://rafiki.dev): los tres endpoints que Rafiki
   y su servidor de autorización llaman de vuelta (webhooks, tipos de cambio e IdP);
2. el **cliente de Open Payments** que ejecuta el retiro (cobros, cotizaciones, grant
   interactivo y pagos), escrito con el SDK oficial de Go;
3. la **superficie interna `/internal`** y su **capa de datos PostGIS**: la API que los
   cuatro MCP del agente van a llamar, con las once tablas del contrato.

Al lado vive el stack local de Rafiki (servidor de recursos, servidor de autorización
GNAP, Admin API, UI y sus bases de datos), en [`rafiki/`](rafiki/README.md).

**Nada de esto mueve dinero todavía de punta a punta**: el cliente está escrito y probado
pero **nunca se ejecutó contra Rafiki**, y las rutas que lo disparan (`/internal/pagos`)
todavía no existen. El detalle está en
[Cómo hacer un intercambio](#cómo-hacer-un-intercambio).

## Estado de hoy

| Pieza | Estado |
| --- | --- |
| `cmd/backend` — servidor HTTP con `/health` | Listo y **corriendo** como contenedor |
| `POST /webhooks` — firma `Rafiki-Signature` verificada, 200 exacto | Listo y **en el stack**; la entrega real de un evento espera al flujo de pago |
| `GET /rates` — tabla demo USD↔COP | Listo; **verificado a través del servicio de tasas de Rafiki** |
| `/idp/` y `/idp/approve` — consentimiento de un grant interactivo | Listo; solo se ve con un grant en vuelo (necesita al cliente) |
| `cmd/seed` — activo COP y USD, liquidez, cinco wallet addresses y el JWK del cliente | Listo e idempotente, **verificado en vivo** |
| `internal/rafikiadmin` — cliente de la Admin API (firma HMAC, GraphQL) | Listo |
| `internal/opkeys` — clave Ed25519 del cliente y su `kid` (huella RFC 7638) | Listo |
| `internal/opclient` — el flujo del retiro con el SDK oficial de Go | Listo y con **92 pruebas**; **nunca ejecutado contra Rafiki** |
| `internal/store` — conexión, migraciones y las 11 tablas del contrato (PostGIS) | Listo y **verificado contra un PostGIS real** |
| `internal/caja` — las reglas de dinero (comisión, topes, niveles) | Listo, anclado en el ejemplo del contrato |
| `internal/mapa` — cercanas por distancia, detalle y efectivo del día | Rutas y consultas listas, probadas; **no montadas en el stack** |
| `internal/identidad` — quién escribe y aceptación de políticas | Rutas y consultas listas, probadas; **no montadas en el stack** |
| `internal/api` — la superficie `/internal`, el token y el sobre de errores | Listo (lo construyó la otra sesión del mismo árbol) |
| Rutas de pagos `/internal/pagos/*` y `POST /caja/reservas` | **No existen todavía** |
| Servicio `db` con PostGIS en el compose | **No existe todavía** |
| Retiro de punta a punta contra Rafiki | **Pendiente**: es el primer contacto real del camino del dinero |

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
  |                   <--- /internal/* (X-Servicio-Token) |   (GNAP, interacciones)
  |                                                        |
  |   rafiki-backend:3001 (Admin API)                      |
  |   rafiki-admin:3010 (UI)  ·  postgres  ·  redis        |
  +--------------------------------------------------------+

  (los cuatro MCP del agente llaman a /internal desde la misma red;
   la base con PostGIS del contrato es un servicio que todavía no está en el compose)
```

Tres decisiones de topología que explican el dibujo, y que están verificadas en este host
(el detalle, en [`rafiki/README.md`](rafiki/README.md)):

- **El backend corre como contenedor dentro de la red `rafiki`, no en el host.** Los
  contenedores no alcanzan los puertos TCP del host, así que un backend en el host nunca
  recibiría un webhook ni una consulta de tasas. Por eso `WEBHOOK_URL` y
  `EXCHANGE_RATES_URL` apuntan a `http://backend:8000/...`.
- **La wallet address se publica en `https` y se llama en `http`.** Rafiki rechaza toda
  wallet address URL que no sea `https`, pero en el puerto 3000 no hay TLS terminando.
  Dentro de la red el nombre de servicio resuelve; desde afuera, no.
- **La superficie interna no se expone al público.** Vive bajo `/internal`, exige la
  cabecera `X-Servicio-Token` en cada llamada y sólo se monta cuando `SERVICIO_TOKEN` y
  `DATABASE_URL` están configurados: sin token quedaría abierta y sin base sus rutas no
  podrían contestar.

## Requisitos

- Docker Engine con el plugin Compose v2
- Go **1.22.5** (`go version`) — el módulo declara `go 1.22.5` y dos dependencias: el SDK
  oficial de Open Payments y `pgx` v5.7.4
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

Qué hace, en orden: crea los activos `COP` y `USD` con escala 2, les carga la liquidez
del demo, fija el ajuste por tenant `WALLET_ADDRESS_URL`, crea una wallet address por
actor (`tienda-cajadebarrio`, `laura-cajadebarrio`, `app-cajadebarrio`,
`app-cajadebarrio-cop` —la cuenta COP donde entra el 1% de la plataforma— y
`andres-cajadebarrio` en USD) y registra la clave pública del cliente en las dos cuentas
que el backend opera.

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

La **superficie interna** no responde hoy en el stack porque el contenedor no recibe
`SERVICIO_TOKEN` ni `DATABASE_URL`. Cuando el compose los inyecte (tarea C16):

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://localhost:8000/internal/mapa/tiendas/cercanas   # 401: sin token
```

### Apagar

```bash
cd rafiki
docker compose down              # conserva los datos
docker compose down --volumes     # borra Postgres; después: ./scripts/init.sh --force
```

## Cómo hacer un intercambio

**Hoy no se puede hacer una transferencia de punta a punta, y conviene que el README lo
diga en vez de dejar que alguien lo intente y falle.** Faltan tres cosas, y ninguna es de
Rafiki:

| Lo que falta | Por qué |
| --- | --- |
| **Las rutas de pagos** `/internal/pagos/*` (tareas C13 y C14) | Nada puede disparar el flujo del cliente: `internal/opclient` está escrito y probado, pero **no tiene quién lo llame** |
| **El servicio `db` con PostGIS en el compose** (C16) | La capa de datos no tiene base en el stack, y la superficie interna no se monta sin `DATABASE_URL` |
| **La corrida viva** (C19) | El flujo del dinero **nunca se ejecutó contra Rafiki**: sus 92 pruebas corren contra servidores de prueba, no contra el stack |

También falta `POST /caja/reservas` (C10), que es la reserva que el pago paga: su capa de
datos ya está y la elegibilidad de la persona también, pero la ruta todavía no existe.

### Lo que sí se puede hacer hoy

**Ver que el ASE funciona**: los tres endpoints que Rafiki llama de vuelta (los comandos
del paso 5). Eso está corriendo y verificado.

**Ejercitar la capa de datos contra un PostGIS de verdad**, sin stack: se levanta un
contenedor desechable, se aplican las migraciones y corren las pruebas de integración
(ver [Pruebas](#pruebas)).

### Cómo será un intercambio cuando esté

El flujo es el del contrato del backend (secciones 6 a 8), y son **ocho pasos** desde el
punto de vista del agente:

1. **Buscar la tienda** — `GET /internal/mapa/tiendas/cercanas?lat=…&lon=…&monto_cop=…`
   devuelve hasta tres tiendas con el efectivo libre y lo que costaría el retiro en cada
   una (la comisión del tendero sube con la parte del efectivo que el retiro se lleva, y
   el 1% de Caja de Barrio va aparte).
2. **Reservar el efectivo** — `POST /internal/caja/reservas` aparta el monto por quince
   minutos y devuelve el código de cuatro dígitos, que **solo ve el cliente**.
3. **Cobrar y cotizar** — `POST /internal/pagos/cobros` crea los dos cobros (la tienda y
   la cuenta COP de la plataforma) y `POST /internal/pagos/cotizaciones` las dos
   cotizaciones desde la billetera del cliente.
4. **Pedir permiso** — `POST /internal/pagos/cupos` devuelve el enlace donde la persona
   aprueba el cupo en su billetera; al volver, `/op/finish` continúa el grant.
5. **Aprobar** — la persona escribe `APROBAR` en el chat y el backend lo registra.
6. **Pagar** — `POST /internal/pagos/pagos` ejecuta los dos pagos y **no da el visto bueno
   hasta comprobar que el cobro de la tienda quedó completado**.
7. **Entregar** — el tendero confirma con el código y el cliente confirma que recibió; la
   reserva queda `entregada` **solo con las dos confirmaciones**, y si el cliente dice que
   no recibió, queda `en_disputa`.
8. **Consultar** — `GET /internal/pagos/cobros/estado?url=…` dice si el dinero llegó.

Las reglas que el backend hace cumplir en código, y que valen la pena conocer antes de
tocar nada: sin identidad verificada en los últimos 30 minutos no hay reserva; el tope lo
impone el grant (`limits.debitAmount`), no la aritmética del cliente; un pago nunca se
ejecuta dos veces para la misma reserva; y ningún billete sale antes de que el pago esté
completado.

**El camino más corto para ver dinero moverse de verdad** no es ese: es ejecutar el flujo
de `internal/opclient` contra el Rafiki local **desde dentro de la red de compose** (es la
única forma de alcanzar `rafiki-backend:3000`). No necesita rutas ni identidad, porque las
cinco wallet addresses y la clave del cliente ya existen en el ASE. Es la tarea T12 del
plan.

## Qué falta, tarea por tarea

El plan completo, con su bitácora de evidencia, está en dos documentos:

- [`odd/tasks/backend-go-ase-y-cliente-op.md`](odd/tasks/backend-go-ase-y-cliente-op.md) —
  el rol de ASE y el cliente de Open Payments (T1 a T12).
- [`odd/tasks/contrato-interno-y-pagos.md`](odd/tasks/contrato-interno-y-pagos.md) — la
  superficie `/internal`, la capa de datos y el contrato de los MCP (C0 a C19).

Lo que falta hoy, en orden de dependencia:

1. **C6 y C8** — terminar identidad: `POST /personas` (necesita Verifik), las sesiones de
   Didit y su webhook, con la **deduplicación por `Event.ID` antes de cualquier efecto**.
2. **C10** — `POST /caja/reservas`: la reserva, con el código de cuatro dígitos desde
   `crypto/rand` (un código adivinable le entrega el dinero a quien lo adivine) y el tic
   que libera las vencidas.
3. **C13 y C14** — las rutas de pagos, que envuelven a `internal/opclient`.
4. **C16** — el servicio `db` con PostGIS y las variables por servicio en el compose.
5. **C19** — el retiro de punta a punta contra Rafiki, con la lista de comprobación del
   contrato.
6. **C5** — los datos de la demo (cinco tiendas de Aranjuez). Está **detenido**: el
   contrato delega las coordenadas reales de las tiendas en
   `agente-caja-de-barrio/agente/herramientas/simuladas.py`, que no está en esta máquina.

Fuera del backend, y no depende de nosotros: **TLS delante del puerto 3000** si el demo
necesita que una billetera externa alcance este ASE (`tailscale serve --bg --https=443
http://localhost:3000`), y **un peer** si un pago va hacia otro ASE. Los pagos entre
wallet addresses del mismo ASE no necesitan peer.

## Estructura del repositorio

| Ruta | Qué es |
| --- | --- |
| `cmd/backend` | El proceso: carga la configuración, migra, monta las rutas y sirve |
| `cmd/migrate` | Aplica las migraciones del esquema y sale (el backend también migra al arrancar) |
| `cmd/seed` | Aprovisiona el ASE: activos, liquidez, wallet addresses y el JWK del cliente |
| `internal/api` | La superficie `/internal`: el router, el chequeo de `X-Servicio-Token` y el sobre de errores |
| `internal/ase` | Los endpoints que Rafiki y el servidor de autorización llaman de vuelta |
| `internal/caja` | Las reglas de dinero: comisión del tendero, comisión de la plataforma, topes y niveles |
| `internal/config` | Lectura y validación del entorno, con error al arrancar si un valor no sirve |
| `internal/didit` | Cliente de Didit: sesiones de verificación y firma del webhook |
| `internal/httpserver` | Router del backend: `/health` y el montaje de las rutas de los componentes |
| `internal/identidad` | Las rutas de identidad: quién escribe y la aceptación de las políticas |
| `internal/mapa` | Las rutas del mapa: cercanas por distancia, detalle y efectivo del día |
| `internal/opclient` | El cliente de Open Payments: cobros, cotizaciones, grant interactivo y pagos |
| `internal/opkeys` | Clave Ed25519 del cliente: generación, PEM en 0600 y `kid` por huella RFC 7638 |
| `internal/rafikiadmin` | Cliente de la Admin API de Rafiki: firma HMAC y GraphQL |
| `internal/store` | Conexión, migraciones y el esquema del contrato en PostgreSQL + PostGIS |
| `internal/verifik` | Cliente de Verifik: validación de cédula colombiana |
| `rafiki/` | El stack local de Rafiki y su `init.sh` (ver [`rafiki/README.md`](rafiki/README.md)) |
| `deploy/` | Cómo correr el stack en un VPS: overlay de compose, Caddy con HTTPS, instalador y su guía (ver [`deploy/README.md`](deploy/README.md)) |
| `docs/` | Referencia de configuración y evaluación del SDK de Python |
| `odd/` | El plan por tareas y su bitácora, con la evidencia verificada |
| `prompts/` | Los prompts reales con los que se trabajó, sin resumir |

## Pruebas

```bash
go test ./...        # 413 casos en 16 paquetes
go vet ./...
gofmt -l .           # no debe imprimir nada
```

Ninguna prueba necesita el stack levantado: los clientes HTTP se ejercitan contra
servidores de prueba.

**Cinco pruebas de la capa de datos son de integración**: se ejecutan contra un PostgreSQL
con PostGIS de verdad y **se saltan solas** si no hay `DATABASE_URL`. Para correrlas:

```bash
# Un PostGIS desechable, en otro puerto para no tocar el del stack
docker run -d --name caja-postgis \
  -e POSTGRES_USER=caja -e POSTGRES_PASSWORD=desarrollo-local -e POSTGRES_DB=caja_de_barrio \
  -p 5433:5432 postgis/postgis:16-3.4

export DATABASE_URL="postgres://caja:desarrollo-local@localhost:5433/caja_de_barrio"
go run ./cmd/migrate          # aplica el esquema (y no hace nada la segunda vez)
go test ./internal/store/ -count=1 -v

docker rm -f caja-postgis
```

Esas pruebas traen sus propios datos y **no dejan filas**, con una excepción declarada: la
aceptación de políticas, porque el esquema **se niega** a borrar en esa tabla (es de solo
inserción, y la regla vale incluso contra una prueba).

Para correr el backend fuera de Docker (útil para depurar, no para la POC: un backend en
el host no recibe webhooks) hay que exportar las mismas variables mapeando los cuatro
nombres que difieren. La tabla está en
[`docs/configuracion.md`](docs/configuracion.md).

## Documentación

- [`docs/configuracion.md`](docs/configuracion.md) — referencia por variable, verificada
  contra los `os.Getenv` del código. Es la fuente de los nombres.
- [`env.example`](env.example) — los nombres con valores de ejemplo, sin secretos.
- [`rafiki/README.md`](rafiki/README.md) — el stack de Rafiki: servicios, puertos, la
  clave del ASE, por qué el backend vive dentro de la red y la UI de administración.
- [`odd/tasks/`](odd/tasks) — el plan por tareas con su bitácora: qué se decidió, qué se
  verificó y con qué evidencia.
- `contexto-caja-de-barrio/contrato-backend.md` (fuera de este repositorio) — el contrato
  del backend hacia los cuatro MCP: endpoints, errores, tablas y el flujo de pagos.
- [`docs/sdk-python-open-payments.md`](docs/sdk-python-open-payments.md) — evaluación del
  SDK oficial de Python (no es una base confiable todavía).

## Uso de IA

[`AI_USAGE.md`](AI_USAGE.md) declara las herramientas usadas, para qué y en qué archivos.
Los prompts reales, sin resumir, están en [`prompts/`](prompts/).
