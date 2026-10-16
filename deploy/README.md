# Despliegue en un VPS

Corre el mismo stack de `rafiki/docker-compose.yml` (Rafiki + Postgres + Redis + backend Go) en un servidor
propio, con HTTPS real y siempre disponible. No hace falta túnel ni comprar dominio.

## Stack

| Capa | Qué |
| --- | --- |
| Servidor | VPS con Ubuntu 24.04, **4 GB de RAM o más** (Rafiki y Postgres usan entre 2 y 3 GB) |
| Contenedores | Docker + Compose: `rafiki/docker-compose.yml` + el overlay `deploy/compose.vps.yml` |
| HTTPS | Caddy 2.8 con certificados de Let's Encrypt automáticos |
| Dominio | `sslip.io`: `op.`, `auth.`, `backend.` y `admin.` + `<ip-con-guiones>.sslip.io` |
| Firewall | `ufw`, solo 22, 80 y 443 |

Opciones de servidor:

| Proveedor | Plan | Costo aproximado | Nota |
| --- | --- | --- | --- |
| **Hetzner Cloud** (recomendado) | CX22: 2 vCPU, 4 GB, x86 | €4–5 al mes, cobrado por hora | Se crea en un minuto. Se puede borrar después del evento |
| Oracle Cloud Always Free | Ampere A1 (ARM) | Gratis | Las imágenes de Rafiki son multiarquitectura. El registro puede tardar y a veces no hay capacidad |

## Qué queda público y qué no

| Nombre | Va a | Para qué |
| --- | --- | --- |
| `https://op.<dominio>` | `rafiki-backend:3000` | Open Payments: wallet addresses, incoming y outgoing payments, quotes |
| `https://auth.<dominio>` | `rafiki-auth:3006` | Servidor de autorización GNAP y la interacción `/interact/...` que abre el navegador |
| `https://backend.<dominio>` | `backend:8000` | IdP del grant interactivo (`/idp/`) y, después, la API `/internal` de los MCP |
| `https://admin.<dominio>` | `rafiki-admin:3010` | UI de Rafiki, **con usuario y clave** (`deploy/.credenciales-admin`) |

**Sin acceso desde internet:**
- Postgres (5432);
- las Admin API de Rafiki (3001 y 3003);
- la introspección (3007);
- la interacción interna (3009);
- el connector (3002).

Las wallet addresses se publican como `https://op.<dominio>/<nombre>`, y su documento anuncia `authServer`
y `resourceServer` en `https://`. Cualquier cliente de Open Payments puede resolverlas y pagarlas. Dentro de la
red de Docker esos mismos nombres apuntan a Caddy (por alias de red).

## Paso a paso (Hetzner)

1. Crea una cuenta en <https://console.hetzner.cloud>, luego un proyecto y después **Add Server**:
   - Ubicación: la más cercana disponible (por ejemplo, Ashburn, EE. UU.).
   - Imagen: **Ubuntu 24.04**.
   - Tipo: **CX22** (o cualquiera con 4 GB o más).
   - **SSH key:** pega tu clave pública (`cat ~/.ssh/id_ed25519.pub`; si no tienes una, créala con
     `ssh-keygen -t ed25519`).
2. Copia la IP pública del servidor y entra:
   ```bash
   ssh root@<ip>
   ```
3. Clona el repositorio. Si es privado, usa un token de GitHub de solo lectura o una deploy key:
   ```bash
   git clone https://github.com/AppiaDevMasters/backend-caja-de-barrio.git
   cd backend-caja-de-barrio
   ```
4. Instala:
   ```bash
   sudo ./deploy/instalar.sh
   ```
   El script hace todo esto:
   - instala Docker;
   - abre solo 22, 80 y 443;
   - genera `rafiki/.env` y las claves con `rafiki/scripts/init.sh`;
   - crea el usuario de la UI;
   - levanta el stack;
   - corre el seed (activo COP, liquidez, las tres wallet addresses y la clave del cliente);
   - reinicia el backend;
   - verifica las URLs públicas.

   La primera vez tarda entre 3 y 5 minutos.
5. Al final imprime las URLs. La prueba rápida desde tu computador:
   ```bash
   curl https://backend.<ip-con-guiones>.sslip.io/health
   curl -H 'Accept: application/json' https://op.<ip-con-guiones>.sslip.io/laura-cajadebarrio
   ```

## Operación

| Tarea | Comando (en el VPS, desde la raíz del repo) |
| --- | --- |
| Actualizar después de un `git pull` | `sudo ./deploy/instalar.sh` (idempotente: conserva secretos y datos) |
| Ver el estado | `docker compose -f rafiki/docker-compose.yml -f deploy/compose.vps.yml ps` |
| Ver logs | `docker logs -f caja-de-barrio-backend` · `docker logs -f rafiki-backend` · `docker logs -f caja-de-barrio-caddy` |
| Reiniciar todo | `docker compose -f rafiki/docker-compose.yml -f deploy/compose.vps.yml restart` |
| Clave de la UI de Rafiki | `cat deploy/.credenciales-admin` |

## Cuidados

- **No cambies `DOMINIO` después del primer arranque.** Rafiki guarda el `authServer` en el tenant y las
  wallet addresses con su URL completa. Si cambia la IP o el dominio, hay que empezar de cero:
  `docker compose ... down -v`, borrar `rafiki/.env` y `keys/`, y volver a correr el script.
- **Secretos:** `rafiki/.env`, `rafiki/config/keys/`, `keys/` y `deploy/.credenciales-admin` viven solo en el
  VPS (están en `.gitignore`). Haz copia de `rafiki/.env` y de las claves si el servidor va a durar.
- **Let's Encrypt:** el dominio de `sslip.io` debe resolver a la IP del VPS y los puertos 80 y 443 tienen que
  estar abiertos en el firewall del proveedor. Si Caddy no obtiene el certificado, revisa `docker logs caja-de-barrio-caddy`.
- **Dominio propio:** si más adelante hay uno, se crean registros A para `op`, `auth`, `backend` y `admin`, y se
  corre el script desde cero con `DOMINIO=midominio.com`.
